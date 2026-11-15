package portfolio

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/invariant"
)

type ownerRow struct {
	ID         uuid.UUID `gorm:"column:id;primaryKey"`
	SocrateSub *string   `gorm:"column:socrate_sub"`
	Kind       string    `gorm:"column:kind"`
	LegalName  string    `gorm:"column:legal_name"`
	Address    string    `gorm:"column:address"`
	TaxID      string    `gorm:"column:tax_id"`
	Language   string    `gorm:"column:language"`
	// An SCI's (Q-133); nil or empty for a person.
	Siren        string     `gorm:"column:siren"`
	TaxRegime    *string    `gorm:"column:tax_regime"`
	RegimeFrom   *time.Time `gorm:"column:regime_from;type:date"`
	CapitalParts *int       `gorm:"column:capital_parts"`
	Version      int        `gorm:"column:version"`
	CreatedAt    time.Time  `gorm:"column:created_at"`
	CreatedBy    string     `gorm:"column:created_by"`
	UpdatedAt    time.Time  `gorm:"column:updated_at"`
	UpdatedBy    string     `gorm:"column:updated_by"`
}

func (ownerRow) TableName() string { return "owners" }

type assetRow struct {
	ID                  uuid.UUID      `gorm:"column:id;primaryKey"`
	OwnerID             uuid.UUID      `gorm:"column:owner_id"`
	Kind                string         `gorm:"column:kind"`
	Address             string         `gorm:"column:address"`
	Lot                 string         `gorm:"column:lot"`
	Surface             *db.Hundredths `gorm:"column:surface_m2"`
	Furnished           bool           `gorm:"column:furnished"`
	DPEClass            *string        `gorm:"column:dpe_class"`
	ControlledZone      bool           `gorm:"column:controlled_zone"`
	TenseZone           bool           `gorm:"column:tense_zone"`
	RentControlZone     string         `gorm:"column:rent_control_zone"`
	RentControlCategory string         `gorm:"column:rent_control_category"`
	CollectiveBuilding  bool           `gorm:"column:collective_building"`
	AcquiredOn          *time.Time     `gorm:"column:acquired_on"`
	AcquisitionCents    *int64         `gorm:"column:acquisition_cents"`
	ParentAssetID       *uuid.UUID     `gorm:"column:parent_asset_id"`
	Version             int            `gorm:"column:version"`
	CreatedAt           time.Time      `gorm:"column:created_at"`
	CreatedBy           string         `gorm:"column:created_by"`
	UpdatedAt           time.Time      `gorm:"column:updated_at"`
	UpdatedBy           string         `gorm:"column:updated_by"`
}

func (assetRow) TableName() string { return "assets" }

type shareRow struct {
	AssetID  uuid.UUID     `gorm:"column:asset_id;primaryKey"`
	OwnerID  uuid.UUID     `gorm:"column:owner_id;primaryKey"`
	SharePct db.Hundredths `gorm:"column:share_pct"`
}

func (shareRow) TableName() string { return "asset_shares" }

type tenantRow struct {
	ID         uuid.UUID    `gorm:"column:id;primaryKey"`
	OwnerID    uuid.UUID    `gorm:"column:owner_id"`
	Names      db.TextArray `gorm:"column:names"`
	Email      string       `gorm:"column:email"`
	Phone      string       `gorm:"column:phone"`
	Language   string       `gorm:"column:language"`
	Occupation *string      `gorm:"column:occupation"`
	Version    int          `gorm:"column:version"`
	CreatedAt  time.Time    `gorm:"column:created_at"`
	CreatedBy  string       `gorm:"column:created_by"`
	UpdatedAt  time.Time    `gorm:"column:updated_at"`
	UpdatedBy  string       `gorm:"column:updated_by"`
}

func (tenantRow) TableName() string { return "tenants" }

// Repository stores owners, assets, shares and tenants. Every method runs in
// the caller's scoped transaction: row-level security limits it to the active
// owner scope, so it never filters on owner_id itself.
type Repository struct{}

func tx(ctx context.Context) (*gorm.DB, error) { return db.Tx(ctx) }

// OwnerBySub returns the owner row of a Socrate subject.
func (Repository) OwnerBySub(ctx context.Context, sub string) (ownerRow, bool, error) {
	t, err := tx(ctx)
	if err != nil {
		return ownerRow{}, false, err
	}
	var rows []ownerRow
	if err := t.Where("socrate_sub = ?", sub).Limit(1).Find(&rows).Error; err != nil {
		return ownerRow{}, false, err
	}
	if len(rows) == 0 {
		return ownerRow{}, false, nil
	}
	return rows[0], true, nil
}

// InsertOwner creates an owner row.
func (Repository) InsertOwner(ctx context.Context, o ownerRow) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	return t.Create(&o).Error
}

// UpdateEntity updates an SCI's profile when the version matches, bumping it.
func (Repository) UpdateEntity(ctx context.Context, o ownerRow, version int) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	res := t.Model(&ownerRow{}).Where("id = ? AND kind = 'sci' AND version = ?", o.ID, version).Updates(map[string]any{
		"legal_name": o.LegalName, "address": o.Address, "tax_id": o.TaxID, "language": o.Language,
		"siren": o.Siren, "regime_from": o.RegimeFrom, "capital_parts": o.CapitalParts,
		"version": version + 1, "updated_at": o.UpdatedAt, "updated_by": o.UpdatedBy,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return invariant.ErrStale
	}
	return nil
}

// UpdateOwner updates the profile when the version matches, bumping it.
func (Repository) UpdateOwner(ctx context.Context, o ownerRow, version int) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	res := t.Model(&ownerRow{}).Where("id = ? AND version = ?", o.ID, version).Updates(map[string]any{
		"legal_name": o.LegalName, "address": o.Address, "tax_id": o.TaxID, "language": o.Language,
		"version": version + 1, "updated_at": o.UpdatedAt, "updated_by": o.UpdatedBy,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return invariant.ErrStale
	}
	return nil
}

// ListAssets returns a page of assets and the total.
func (Repository) ListAssets(ctx context.Context, offset, limit int) ([]assetRow, int, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, 0, err
	}
	var total int64
	if err := t.Model(&assetRow{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []assetRow
	err = t.Order("created_at, id").Offset(offset).Limit(limit).Find(&rows).Error
	return rows, int(total), err
}

// Assets returns the assets with the given ids that are in scope.
func (Repository) Assets(ctx context.Context, ids []uuid.UUID) ([]assetRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []assetRow
	err = t.Where("id IN ?", ids).Find(&rows).Error
	return rows, err
}

// Asset returns one asset, or ErrNotFound when it is not in scope.
func (r Repository) Asset(ctx context.Context, id uuid.UUID) (assetRow, error) {
	rows, err := r.Assets(ctx, []uuid.UUID{id})
	if err != nil {
		return assetRow{}, err
	}
	if len(rows) == 0 {
		return assetRow{}, invariant.ErrNotFound
	}
	return rows[0], nil
}

// Shares returns the shares of the assets, by asset id.
func (Repository) Shares(ctx context.Context, assetIDs []uuid.UUID) (map[uuid.UUID][]shareRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []shareRow
	if err := t.Where("asset_id IN ?", assetIDs).Order("owner_id").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := map[uuid.UUID][]shareRow{}
	for _, s := range rows {
		out[s.AssetID] = append(out[s.AssetID], s)
	}
	return out, nil
}

// InsertAsset creates an asset and its shares.
func (Repository) InsertAsset(ctx context.Context, a assetRow, shares []shareRow) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	if err := t.Create(&a).Error; err != nil {
		return err
	}
	return t.Create(&shares).Error
}

// UpdateAsset updates an asset when the version matches and replaces its
// shares.
func (Repository) UpdateAsset(ctx context.Context, a assetRow, version int, shares []shareRow) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	res := t.Model(&assetRow{}).Where("id = ? AND version = ?", a.ID, version).Updates(map[string]any{
		"address": a.Address, "lot": a.Lot, "surface_m2": a.Surface, "furnished": a.Furnished, "dpe_class": a.DPEClass,
		"controlled_zone": a.ControlledZone, "tense_zone": a.TenseZone, "rent_control_zone": a.RentControlZone, "rent_control_category": a.RentControlCategory,
		"collective_building": a.CollectiveBuilding, "acquired_on": a.AcquiredOn, "acquisition_cents": a.AcquisitionCents, "parent_asset_id": a.ParentAssetID,
		"version": version + 1, "updated_at": a.UpdatedAt, "updated_by": a.UpdatedBy,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return invariant.ErrStale
	}
	if err := t.Where("asset_id = ?", a.ID).Delete(&shareRow{}).Error; err != nil {
		return err
	}
	return t.Create(&shares).Error
}

// ListTenants returns a page of tenants and the total.
func (Repository) ListTenants(ctx context.Context, offset, limit int) ([]tenantRow, int, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, 0, err
	}
	var total int64
	if err := t.Model(&tenantRow{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []tenantRow
	err = t.Order("created_at, id").Offset(offset).Limit(limit).Find(&rows).Error
	return rows, int(total), err
}

// Owner returns one owner row visible in the scope.
func (Repository) Owner(ctx context.Context, id uuid.UUID) (ownerRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return ownerRow{}, err
	}
	var rows []ownerRow
	if err := t.Where("id = ?", id).Limit(1).Find(&rows).Error; err != nil {
		return ownerRow{}, err
	}
	if len(rows) == 0 {
		return ownerRow{}, invariant.ErrNotFound
	}
	return rows[0], nil
}

// Tenants returns the tenants with the given ids that are in scope.
func (Repository) Tenants(ctx context.Context, ids []uuid.UUID) ([]tenantRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []tenantRow
	err = t.Where("id IN ?", ids).Find(&rows).Error
	return rows, err
}

// InsertTenant creates a tenant.
func (Repository) InsertTenant(ctx context.Context, tr tenantRow) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	return t.Create(&tr).Error
}

// UpdateTenant updates a tenant when the version matches.
func (Repository) UpdateTenant(ctx context.Context, tr tenantRow, version int) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	res := t.Model(&tenantRow{}).Where("id = ? AND version = ?", tr.ID, version).Updates(map[string]any{
		"names": tr.Names, "email": tr.Email, "phone": tr.Phone, "language": tr.Language, "occupation": tr.Occupation,
		"version": version + 1, "updated_at": tr.UpdatedAt, "updated_by": tr.UpdatedBy,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return invariant.ErrStale
	}
	return nil
}
