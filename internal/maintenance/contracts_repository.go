package maintenance

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm/clause"

	"github.com/ovander/tenure-api/internal/invariant"
)

type contractRow struct {
	ID             uuid.UUID  `gorm:"column:id;primaryKey"`
	OwnerID        uuid.UUID  `gorm:"column:owner_id"`
	AssetID        uuid.UUID  `gorm:"column:asset_id"`
	Kind           string     `gorm:"column:kind"`
	Fuel           *string    `gorm:"column:fuel"`
	Title          string     `gorm:"column:title"`
	ContractorID   *uuid.UUID `gorm:"column:contractor_id"`
	IntervalMonths int        `gorm:"column:interval_months"`
	Responsible    string     `gorm:"column:responsible"`
	FirstDueOn     time.Time  `gorm:"column:first_due_on;type:date"`
	EndOn          *time.Time `gorm:"column:end_on;type:date"`
	DocumentID     *uuid.UUID `gorm:"column:document_id"`
	Notes          string     `gorm:"column:notes"`
	CreatedAt      time.Time  `gorm:"column:created_at"`
	CreatedBy      string     `gorm:"column:created_by"`
	UpdatedAt      time.Time  `gorm:"column:updated_at"`
	UpdatedBy      string     `gorm:"column:updated_by"`
	Version        int        `gorm:"column:version"`
	// LastDoneOn is read, not stored: the latest visit.
	LastDoneOn *time.Time `gorm:"column:last_done_on;->"`
}

func (contractRow) TableName() string { return "maintenance_contracts" }

type visitRow struct {
	ID         uuid.UUID  `gorm:"column:id;primaryKey"`
	OwnerID    uuid.UUID  `gorm:"column:owner_id"`
	ContractID uuid.UUID  `gorm:"column:contract_id"`
	DoneOn     time.Time  `gorm:"column:done_on;type:date"`
	DocumentID *uuid.UUID `gorm:"column:document_id"`
	Notes      string     `gorm:"column:notes"`
	CreatedAt  time.Time  `gorm:"column:created_at"`
	CreatedBy  string     `gorm:"column:created_by"`
}

func (visitRow) TableName() string { return "maintenance_visits" }

const contractColumns = "maintenance_contracts.*, (SELECT max(v.done_on) FROM maintenance_visits v WHERE v.contract_id = maintenance_contracts.id) AS last_done_on"

// Contracts lists the scope's maintenance contracts, of one asset when
// asset is set, with their latest visit.
func (Repository) Contracts(ctx context.Context, asset *uuid.UUID) ([]contractRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	q := t.Model(&contractRow{}).Select(contractColumns)
	if asset != nil {
		q = q.Where("asset_id = ?", *asset)
	}
	var rows []contractRow
	err = q.Order("asset_id, kind, title, id").Find(&rows).Error
	return rows, err
}

// Contract returns one contract with its latest visit, locked for update
// when forUpdate.
func (Repository) Contract(ctx context.Context, id uuid.UUID, forUpdate bool) (contractRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return contractRow{}, err
	}
	if forUpdate {
		var ids []uuid.UUID
		if err := t.Model(&contractRow{}).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Pluck("id", &ids).Error; err != nil {
			return contractRow{}, err
		}
	}
	var rows []contractRow
	if err := t.Model(&contractRow{}).Select(contractColumns).Where("id = ?", id).Limit(1).Find(&rows).Error; err != nil {
		return contractRow{}, err
	}
	if len(rows) == 0 {
		return contractRow{}, invariant.ErrNotFound
	}
	return rows[0], nil
}

// InsertContract records a contract.
func (Repository) InsertContract(ctx context.Context, r contractRow) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	return t.Omit("last_done_on").Create(&r).Error
}

// UpdateContract replaces a contract when its version is the one read.
func (Repository) UpdateContract(ctx context.Context, r contractRow, version int) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	res := t.Model(&contractRow{}).Where("id = ? AND version = ?", r.ID, version).Updates(map[string]any{
		"kind": r.Kind, "fuel": r.Fuel, "title": r.Title, "contractor_id": r.ContractorID, "interval_months": r.IntervalMonths,
		"responsible": r.Responsible, "first_due_on": r.FirstDueOn, "end_on": r.EndOn, "document_id": r.DocumentID,
		"notes": r.Notes, "updated_at": r.UpdatedAt, "updated_by": r.UpdatedBy, "version": version + 1})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return invariant.ErrStale
	}
	return nil
}

// TouchContract bumps a contract's version when version is the one read (a
// visit changes when the next one is due).
func (Repository) TouchContract(ctx context.Context, id uuid.UUID, version int, at time.Time, by string) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	res := t.Model(&contractRow{}).Where("id = ? AND version = ?", id, version).Updates(map[string]any{
		"updated_at": at, "updated_by": by, "version": version + 1})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return invariant.ErrStale
	}
	return nil
}

// Visits lists a contract's visits, the latest first.
func (Repository) Visits(ctx context.Context, contract uuid.UUID) ([]visitRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []visitRow
	err = t.Where("contract_id = ?", contract).Order("done_on DESC, id").Find(&rows).Error
	return rows, err
}

// InsertVisit records a visit.
func (Repository) InsertVisit(ctx context.Context, r visitRow) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	return t.Create(&r).Error
}
