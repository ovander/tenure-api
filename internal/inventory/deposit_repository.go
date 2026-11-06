package inventory

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm/clause"

	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/invariant"
)

type gridRow struct {
	ID        uuid.UUID       `gorm:"column:id;primaryKey"`
	OwnerID   uuid.UUID       `gorm:"column:owner_id"`
	Name      string          `gorm:"column:name"`
	Source    string          `gorm:"column:source"`
	Lines     json.RawMessage `gorm:"column:lines;type:jsonb"`
	Version   int             `gorm:"column:version"`
	CreatedAt time.Time       `gorm:"column:created_at"`
	CreatedBy string          `gorm:"column:created_by"`
	UpdatedAt time.Time       `gorm:"column:updated_at"`
	UpdatedBy string          `gorm:"column:updated_by"`
}

func (gridRow) TableName() string { return "vetuste_grids" }

// Grids returns the scope's wear grids, by name.
func (Repository) Grids(ctx context.Context) ([]gridRow, error) {
	t, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []gridRow
	err = t.Order("name, id").Find(&rows).Error
	return rows, err
}

// Grid returns one wear grid, locked for update when lock.
func (Repository) Grid(ctx context.Context, id uuid.UUID, lock bool) (gridRow, bool, error) {
	t, err := db.Tx(ctx)
	if err != nil {
		return gridRow{}, false, err
	}
	q := t.Where("id = ?", id).Limit(1)
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var rows []gridRow
	if err := q.Find(&rows).Error; err != nil || len(rows) == 0 {
		return gridRow{}, false, err
	}
	return rows[0], true, nil
}

// InsertGrid records a wear grid.
func (Repository) InsertGrid(ctx context.Context, r gridRow) error {
	t, err := db.Tx(ctx)
	if err != nil {
		return err
	}
	return t.Create(&r).Error
}

// SaveGrid writes a grid at version+1; ErrStale when someone saved first.
func (Repository) SaveGrid(ctx context.Context, r gridRow, version int) error {
	t, err := db.Tx(ctx)
	if err != nil {
		return err
	}
	res := t.Model(&gridRow{}).Where("id = ? AND version = ?", r.ID, version).Updates(map[string]any{
		"name": r.Name, "source": r.Source, "lines": r.Lines, "version": version + 1, "updated_at": r.UpdatedAt, "updated_by": r.UpdatedBy,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return invariant.ErrStale
	}
	return nil
}

// depositLease is what the settlement reads of a lease: the columns the
// inventory module owns (the grid, the collective building, migration
// 000011) and the deposit and rent of the contract.
type depositLease struct {
	ID           uuid.UUID  `gorm:"column:id"`
	State        string     `gorm:"column:state"`
	StartOn      time.Time  `gorm:"column:start_on"`
	DepositCents int64      `gorm:"column:deposit_cents"`
	RentCents    int64      `gorm:"column:rent_cents"`
	GridID       *uuid.UUID `gorm:"column:vetuste_grid_id"`
	Collective   bool       `gorm:"column:collective"`
}

// DepositLease returns a lease's deposit facts; false when it is not in the
// scope.
func (Repository) DepositLease(ctx context.Context, id uuid.UUID) (depositLease, bool, error) {
	t, err := db.Tx(ctx)
	if err != nil {
		return depositLease{}, false, err
	}
	var rows []depositLease
	err = t.Table("leases l").Select(`l.id, l.state, l.start_on, l.deposit_cents, l.rent_cents, l.vetuste_grid_id,
		EXISTS (SELECT 1 FROM lease_assets la JOIN assets a ON a.id = la.asset_id WHERE la.lease_id = l.id AND a.collective_building) AS collective`).
		Where("l.id = ?", id).Limit(1).Find(&rows).Error
	if err != nil || len(rows) == 0 {
		return depositLease{}, false, err
	}
	return rows[0], true, nil
}

// SetLeaseGrid attaches a wear grid to a lease, or detaches it (nil).
func (Repository) SetLeaseGrid(ctx context.Context, lease uuid.UUID, grid *uuid.UUID, at time.Time, by string) error {
	t, err := db.Tx(ctx)
	if err != nil {
		return err
	}
	return t.Table("leases").Where("id = ?", lease).Updates(map[string]any{"vetuste_grid_id": grid, "updated_at": at, "updated_by": by}).Error
}

// ExitOf returns the id of the lease's exit inventory, if any.
func (Repository) ExitOf(ctx context.Context, lease uuid.UUID) (uuid.UUID, bool, error) {
	t, err := db.Tx(ctx)
	if err != nil {
		return uuid.Nil, false, err
	}
	var ids []uuid.UUID
	if err := t.Table("inventories").Where("lease_id = ? AND kind = 'exit'", lease).Order("created_at DESC").Limit(1).Pluck("id", &ids).Error; err != nil || len(ids) == 0 {
		return uuid.Nil, false, err
	}
	return ids[0], true, nil
}

// InstalledOn returns the installation day of a layout element, when known.
func (Repository) InstalledOn(ctx context.Context, element uuid.UUID) (*time.Time, error) {
	t, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		InstalledOn *time.Time `gorm:"column:installed_on"`
	}
	if err := t.Table("asset_elements").Select("installed_on").Where("id = ?", element).Limit(1).Find(&rows).Error; err != nil || len(rows) == 0 {
		return nil, err
	}
	return rows[0].InstalledOn, nil
}

type deductionRow struct {
	ID            uuid.UUID  `gorm:"column:id;primaryKey"`
	OwnerID       uuid.UUID  `gorm:"column:owner_id"`
	LeaseID       uuid.UUID  `gorm:"column:lease_id"`
	ExitItemID    *uuid.UUID `gorm:"column:exit_item_id"`
	Description   string     `gorm:"column:description"`
	CostCents     int64      `gorm:"column:cost_cents"`
	TenantShareBP int64      `gorm:"column:tenant_share_bp"`
	AmountCents   int64      `gorm:"column:amount_cents"`
	Basis         string     `gorm:"column:basis"`
	DocumentID    uuid.UUID  `gorm:"column:document_id"`
	Version       int        `gorm:"column:version"`
	CreatedAt     time.Time  `gorm:"column:created_at"`
	CreatedBy     string     `gorm:"column:created_by"`
	UpdatedAt     time.Time  `gorm:"column:updated_at"`
	UpdatedBy     string     `gorm:"column:updated_by"`
}

func (deductionRow) TableName() string { return "deposit_deductions" }

// Deductions returns a lease's deductions, oldest first.
func (Repository) Deductions(ctx context.Context, lease uuid.UUID) ([]deductionRow, error) {
	t, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []deductionRow
	err = t.Where("lease_id = ?", lease).Order("created_at, id").Find(&rows).Error
	return rows, err
}

// Deduction returns one deduction.
func (Repository) Deduction(ctx context.Context, id uuid.UUID) (deductionRow, bool, error) {
	t, err := db.Tx(ctx)
	if err != nil {
		return deductionRow{}, false, err
	}
	var rows []deductionRow
	if err := t.Where("id = ?", id).Limit(1).Find(&rows).Error; err != nil || len(rows) == 0 {
		return deductionRow{}, false, err
	}
	return rows[0], true, nil
}

// InsertDeduction records a deduction.
func (Repository) InsertDeduction(ctx context.Context, r deductionRow) error {
	t, err := db.Tx(ctx)
	if err != nil {
		return err
	}
	return t.Create(&r).Error
}

// DeleteDeduction removes a deduction before the settlement.
func (Repository) DeleteDeduction(ctx context.Context, id uuid.UUID) error {
	t, err := db.Tx(ctx)
	if err != nil {
		return err
	}
	return t.Where("id = ?", id).Delete(&deductionRow{}).Error
}

type settlementRow struct {
	ID            uuid.UUID       `gorm:"column:id;primaryKey"`
	OwnerID       uuid.UUID       `gorm:"column:owner_id"`
	LeaseID       uuid.UUID       `gorm:"column:lease_id"`
	Kind          string          `gorm:"column:kind"`
	SettledOn     time.Time       `gorm:"column:settled_on;type:date"`
	Deadline      time.Time       `gorm:"column:deadline;type:date"`
	Figures       json.RawMessage `gorm:"column:figures;type:jsonb"`
	BalanceCents  int64           `gorm:"column:balance_cents"`
	HoldbackCents int64           `gorm:"column:holdback_cents"`
	DocumentID    uuid.UUID       `gorm:"column:document_id"`
	CreatedAt     time.Time       `gorm:"column:created_at"`
	CreatedBy     string          `gorm:"column:created_by"`
}

func (settlementRow) TableName() string { return "deposit_settlements" }

// Settlements returns a lease's settlements, the first one first.
func (Repository) Settlements(ctx context.Context, lease uuid.UUID) ([]settlementRow, error) {
	t, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []settlementRow
	err = t.Where("lease_id = ?", lease).Order("created_at, id").Find(&rows).Error
	return rows, err
}

// InsertSettlement records an issued statement.
func (Repository) InsertSettlement(ctx context.Context, r settlementRow) error {
	t, err := db.Tx(ctx)
	if err != nil {
		return err
	}
	return t.Create(&r).Error
}
