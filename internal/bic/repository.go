package bic

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/invariant"
)

// Repository runs the bic module's SQL in the caller's scoped transaction.
type Repository struct{}

func tx(ctx context.Context) (*gorm.DB, error) { return db.Tx(ctx) }

type gridRow struct {
	ID                    uuid.UUID       `gorm:"column:id;primaryKey"`
	OwnerID               uuid.UUID       `gorm:"column:owner_id"`
	AssetID               uuid.UUID       `gorm:"column:asset_id"`
	Version               int             `gorm:"column:version"`
	InServiceOn           time.Time       `gorm:"column:in_service_on;type:date"`
	PriceCents            int64           `gorm:"column:price_cents"`
	AcquisitionCostsCents int64           `gorm:"column:acquisition_costs_cents"`
	CostsExpensed         bool            `gorm:"column:costs_expensed"`
	BasisCents            int64           `gorm:"column:basis_cents"`
	Lines                 json.RawMessage `gorm:"column:lines;type:jsonb"`
	Reason                string          `gorm:"column:reason"`
	FrozenAt              *time.Time      `gorm:"column:frozen_at"`
	CreatedAt             time.Time       `gorm:"column:created_at"`
	CreatedBy             string          `gorm:"column:created_by"`
}

func (gridRow) TableName() string { return "bic_grids" }

type itemRow struct {
	ID             uuid.UUID  `gorm:"column:id;primaryKey"`
	OwnerID        uuid.UUID  `gorm:"column:owner_id"`
	AssetID        uuid.UUID  `gorm:"column:asset_id"`
	Kind           string     `gorm:"column:kind"`
	Works          *string    `gorm:"column:works"`
	Label          string     `gorm:"column:label"`
	CostCents      int64      `gorm:"column:cost_cents"`
	InServiceOn    time.Time  `gorm:"column:in_service_on;type:date"`
	Months         int        `gorm:"column:months"`
	Treatment      string     `gorm:"column:treatment"`
	Overridden     bool       `gorm:"column:overridden"`
	OverrideReason string     `gorm:"column:override_reason"`
	DocumentID     *uuid.UUID `gorm:"column:document_id"`
	DisposedOn     *time.Time `gorm:"column:disposed_on;type:date"`
	CreatedAt      time.Time  `gorm:"column:created_at"`
	CreatedBy      string     `gorm:"column:created_by"`
	UpdatedAt      time.Time  `gorm:"column:updated_at"`
	UpdatedBy      string     `gorm:"column:updated_by"`
	Version        int        `gorm:"column:version"`
}

func (itemRow) TableName() string { return "bic_fixed_assets" }

// Grid returns the latest version of an asset's grid; found is false when it
// has none.
func (Repository) Grid(ctx context.Context, asset uuid.UUID) (gridRow, bool, error) {
	t, err := tx(ctx)
	if err != nil {
		return gridRow{}, false, err
	}
	var rows []gridRow
	if err := t.Where("asset_id = ?", asset).Order("version DESC").Limit(1).Find(&rows).Error; err != nil {
		return gridRow{}, false, err
	}
	if len(rows) == 0 {
		return gridRow{}, false, nil
	}
	return rows[0], true, nil
}

// InsertGrid records a grid version; a concurrent version of the same
// number is a stale write.
func (Repository) InsertGrid(ctx context.Context, r gridRow) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	var n int64
	if err := t.Model(&gridRow{}).Where("asset_id = ? AND version >= ?", r.AssetID, r.Version).Count(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return invariant.ErrStale
	}
	return t.Create(&r).Error
}

// FirstFurnishedLet returns the start of the first furnished or mobility
// lease, past its draft, that lets the asset; ok is false when none does.
func (Repository) FirstFurnishedLet(ctx context.Context, asset uuid.UUID) (time.Time, bool, error) {
	t, err := tx(ctx)
	if err != nil {
		return time.Time{}, false, err
	}
	var days []time.Time
	err = t.Raw(`SELECT l.start_on FROM leases l JOIN lease_assets la ON la.lease_id = l.id
		WHERE la.asset_id = ? AND l.state <> 'draft' AND l.kind IN ('furnished', 'mobility')
		ORDER BY l.start_on LIMIT 1`, asset).Scan(&days).Error
	if err != nil || len(days) == 0 {
		return time.Time{}, false, err
	}
	return days[0], true, nil
}

// Items lists an asset's furniture and works, in service order.
func (Repository) Items(ctx context.Context, asset uuid.UUID) ([]itemRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []itemRow
	err = t.Where("asset_id = ?", asset).Order("in_service_on, id").Find(&rows).Error
	return rows, err
}

// Item returns one item.
func (Repository) Item(ctx context.Context, id uuid.UUID) (itemRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return itemRow{}, err
	}
	var rows []itemRow
	if err := t.Where("id = ?", id).Limit(1).Find(&rows).Error; err != nil {
		return itemRow{}, err
	}
	if len(rows) == 0 {
		return itemRow{}, invariant.ErrNotFound
	}
	return rows[0], nil
}

// InsertItem records an item.
func (Repository) InsertItem(ctx context.Context, r itemRow) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	return t.Create(&r).Error
}

// UpdateItem replaces an item when its version is the one read.
func (Repository) UpdateItem(ctx context.Context, r itemRow, version int) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	res := t.Model(&itemRow{}).Where("id = ? AND version = ?", r.ID, version).Updates(map[string]any{
		"kind": r.Kind, "works": r.Works, "label": r.Label, "cost_cents": r.CostCents, "in_service_on": r.InServiceOn,
		"months": r.Months, "treatment": r.Treatment, "overridden": r.Overridden, "override_reason": r.OverrideReason,
		"document_id": r.DocumentID, "disposed_on": r.DisposedOn, "updated_at": r.UpdatedAt, "updated_by": r.UpdatedBy,
		"version": version + 1})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return invariant.ErrStale
	}
	return nil
}

// FirstInService returns the earliest day a grid or an item of the scope
// was put in service; zero when there is none.
func (Repository) FirstInService(ctx context.Context) (time.Time, error) {
	t, err := tx(ctx)
	if err != nil {
		return time.Time{}, err
	}
	// A scope with neither a grid nor an item has no first day: NULL.
	var rows []struct {
		Day *time.Time `gorm:"column:day"`
	}
	err = t.Raw(`SELECT least((SELECT min(in_service_on) FROM bic_grids), (SELECT min(in_service_on) FROM bic_fixed_assets)) AS day`).Scan(&rows).Error
	if err != nil || len(rows) == 0 || rows[0].Day == nil {
		return time.Time{}, err
	}
	return *rows[0].Day, nil
}

type declarationRow struct {
	ID       uuid.UUID       `gorm:"column:id;primaryKey"`
	OwnerID  uuid.UUID       `gorm:"column:owner_id"`
	TaxYear  int             `gorm:"column:tax_year"`
	Regime   string          `gorm:"column:regime"`
	Result   json.RawMessage `gorm:"column:result;type:jsonb"`
	Assets   json.RawMessage `gorm:"column:assets;type:jsonb"`
	Grids    json.RawMessage `gorm:"column:grids;type:jsonb"`
	IssuedAt time.Time       `gorm:"column:issued_at"`
	IssuedBy string          `gorm:"column:issued_by"`
}

func (declarationRow) TableName() string { return "bic_declarations" }

// Declarations lists the scope's owner's declarations, by year.
func (Repository) Declarations(ctx context.Context) ([]declarationRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []declarationRow
	err = t.Order("tax_year").Find(&rows).Error
	return rows, err
}

// InsertDeclaration records a declaration.
func (Repository) InsertDeclaration(ctx context.Context, r declarationRow) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	return t.Create(&r).Error
}

// FreezeGrid sets frozen_at on a grid version not yet frozen.
func (Repository) FreezeGrid(ctx context.Context, id uuid.UUID, at time.Time) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	return t.Model(&gridRow{}).Where("id = ? AND frozen_at IS NULL", id).Update("frozen_at", at).Error
}

// GridID returns the id of an asset's latest grid version.
func (r Repository) GridID(ctx context.Context, asset uuid.UUID) (uuid.UUID, bool, error) {
	g, found, err := r.Grid(ctx, asset)
	return g.ID, found, err
}
