package inventory

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/invariant"
)

// Repository runs the inventory module's SQL. It returns storage errors only;
// row-level security scopes every query to the transaction's owner.
type Repository struct{}

type layoutRow struct {
	AssetID   uuid.UUID `gorm:"column:asset_id;primaryKey"`
	OwnerID   uuid.UUID `gorm:"column:owner_id"`
	Version   int       `gorm:"column:version"`
	CreatedAt time.Time `gorm:"column:created_at"`
	CreatedBy string    `gorm:"column:created_by"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
	UpdatedBy string    `gorm:"column:updated_by"`
}

func (layoutRow) TableName() string { return "asset_layouts" }

type roomRow struct {
	ID        uuid.UUID `gorm:"column:id;primaryKey"`
	OwnerID   uuid.UUID `gorm:"column:owner_id"`
	AssetID   uuid.UUID `gorm:"column:asset_id"`
	Position  int       `gorm:"column:position"`
	Kind      string    `gorm:"column:kind"`
	Label     string    `gorm:"column:label"`
	Surface   *int64    `gorm:"column:surface_hundredths_m2"`
	Version   int       `gorm:"column:version"`
	CreatedAt time.Time `gorm:"column:created_at"`
	CreatedBy string    `gorm:"column:created_by"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
	UpdatedBy string    `gorm:"column:updated_by"`
}

func (roomRow) TableName() string { return "asset_rooms" }

type elementRow struct {
	ID          uuid.UUID  `gorm:"column:id;primaryKey"`
	OwnerID     uuid.UUID  `gorm:"column:owner_id"`
	RoomID      uuid.UUID  `gorm:"column:room_id"`
	Position    int        `gorm:"column:position"`
	Category    string     `gorm:"column:category"`
	Label       string     `gorm:"column:label"`
	Quantity    int        `gorm:"column:quantity"`
	InstalledOn *time.Time `gorm:"column:installed_on"`
	Version     int        `gorm:"column:version"`
	CreatedAt   time.Time  `gorm:"column:created_at"`
	CreatedBy   string     `gorm:"column:created_by"`
	UpdatedAt   time.Time  `gorm:"column:updated_at"`
	UpdatedBy   string     `gorm:"column:updated_by"`
}

func (elementRow) TableName() string { return "asset_elements" }

type templateRow struct {
	ID        uuid.UUID       `gorm:"column:id;primaryKey"`
	OwnerID   *uuid.UUID      `gorm:"column:owner_id"`
	AssetKind string          `gorm:"column:asset_kind"`
	Version   int             `gorm:"column:version"`
	Structure json.RawMessage `gorm:"column:structure;type:jsonb"`
	Source    string          `gorm:"column:source"`
}

func (templateRow) TableName() string { return "inventory_templates" }

// Layout returns an asset's layout version (false when it has none yet), its
// rooms and their elements, in position order.
func (Repository) Layout(ctx context.Context, assetID uuid.UUID) (layoutRow, bool, []roomRow, []elementRow, error) {
	t, err := db.Tx(ctx)
	if err != nil {
		return layoutRow{}, false, nil, nil, err
	}
	var ls []layoutRow
	if err := t.Where("asset_id = ?", assetID).Limit(1).Find(&ls).Error; err != nil {
		return layoutRow{}, false, nil, nil, err
	}
	var rooms []roomRow
	if err := t.Where("asset_id = ?", assetID).Order("position").Find(&rooms).Error; err != nil {
		return layoutRow{}, false, nil, nil, err
	}
	var els []elementRow
	if len(rooms) > 0 {
		ids := make([]uuid.UUID, len(rooms))
		for i, r := range rooms {
			ids[i] = r.ID
		}
		if err := t.Where("room_id IN ?", ids).Order("position").Find(&els).Error; err != nil {
			return layoutRow{}, false, nil, nil, err
		}
	}
	if len(ls) == 0 {
		return layoutRow{}, false, rooms, els, nil
	}
	return ls[0], true, rooms, els, nil
}

// BumpLayout moves an asset's layout from version to version+1, or creates it
// at 1 when version is 0; ErrStale when someone saved first.
func (Repository) BumpLayout(ctx context.Context, l layoutRow, version int) error {
	t, err := db.Tx(ctx)
	if err != nil {
		return err
	}
	if version == 0 {
		l.Version = 1
		if err := t.Create(&l).Error; err != nil {
			if db.IsUniqueViolation(err) {
				return invariant.ErrStale
			}
			return err
		}
		return nil
	}
	res := t.Model(&layoutRow{}).Where("asset_id = ? AND version = ?", l.AssetID, version).
		Updates(map[string]any{"version": version + 1, "updated_at": l.UpdatedAt, "updated_by": l.UpdatedBy})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return invariant.ErrStale
	}
	return nil
}

// SaveRooms applies a layout edit: rows to insert, to update and to delete,
// in that order, so an element moved out of a removed room is moved before
// the room (and its remaining elements) goes. Positions are unique per asset
// and per room only at commit (deferred), so rooms and elements may swap
// places in one edit.
func (Repository) SaveRooms(ctx context.Context, insR, updR []roomRow, delR []uuid.UUID, insE, updE []elementRow, delE []uuid.UUID) error {
	t, err := db.Tx(ctx)
	if err != nil {
		return err
	}
	for _, r := range updR {
		if err := t.Model(&roomRow{}).Where("id = ?", r.ID).Updates(map[string]any{"position": r.Position, "kind": r.Kind,
			"label": r.Label, "surface_hundredths_m2": r.Surface, "version": r.Version, "updated_at": r.UpdatedAt, "updated_by": r.UpdatedBy}).Error; err != nil {
			return err
		}
	}
	if len(insR) > 0 {
		if err := t.Create(&insR).Error; err != nil {
			return err
		}
	}
	for _, e := range updE {
		if err := t.Model(&elementRow{}).Where("id = ?", e.ID).Updates(map[string]any{"room_id": e.RoomID, "position": e.Position,
			"category": e.Category, "label": e.Label, "quantity": e.Quantity, "installed_on": e.InstalledOn, "version": e.Version,
			"updated_at": e.UpdatedAt, "updated_by": e.UpdatedBy}).Error; err != nil {
			return err
		}
	}
	if len(insE) > 0 {
		if err := t.Create(&insE).Error; err != nil {
			return err
		}
	}
	if len(delE) > 0 {
		if err := t.Where("id IN ?", delE).Delete(&elementRow{}).Error; err != nil {
			return err
		}
	}
	if len(delR) > 0 {
		if err := t.Where("id IN ?", delR).Delete(&roomRow{}).Error; err != nil {
			return err
		}
	}
	return nil
}

// Templates returns the templates the scope can read: the system ones and its
// own, newest version first within each asset kind.
func (Repository) Templates(ctx context.Context) ([]templateRow, error) {
	t, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []templateRow
	err = t.Order("asset_kind, owner_id NULLS LAST, version DESC").Find(&rows).Error
	return rows, err
}
