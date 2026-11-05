package inventory

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/invariant"
)

type captureRow struct {
	ID               uuid.UUID  `gorm:"column:id;primaryKey"`
	OwnerID          uuid.UUID  `gorm:"column:owner_id"`
	LeaseID          uuid.UUID  `gorm:"column:lease_id"`
	Kind             string     `gorm:"column:kind"`
	Mode             string     `gorm:"column:mode"`
	State            string     `gorm:"column:state"`
	DoneOn           *time.Time `gorm:"column:done_on"`
	ScheduledAt      *time.Time `gorm:"column:scheduled_at"`
	Place            string     `gorm:"column:place"`
	Observations     string     `gorm:"column:observations"`
	TenantNewAddress string     `gorm:"column:tenant_new_address"`
	KeysReturnedOn   *time.Time `gorm:"column:keys_returned_on"`
	DocumentID       *uuid.UUID `gorm:"column:document_id"`
	TemplateVersion  string     `gorm:"column:template_version"`
	Version          int        `gorm:"column:version"`
	CreatedAt        time.Time  `gorm:"column:created_at"`
	CreatedBy        string     `gorm:"column:created_by"`
	UpdatedAt        *time.Time `gorm:"column:updated_at"`
	UpdatedBy        *string    `gorm:"column:updated_by"`
}

func (captureRow) TableName() string { return "inventories" }

type itemRow struct {
	ID          uuid.UUID  `gorm:"column:id;primaryKey"`
	OwnerID     uuid.UUID  `gorm:"column:owner_id"`
	InventoryID uuid.UUID  `gorm:"column:inventory_id"`
	RoomID      *uuid.UUID `gorm:"column:room_id"`
	ElementID   *uuid.UUID `gorm:"column:element_id"`
	RoomLabel   string     `gorm:"column:room_label"`
	RoomKind    *string    `gorm:"column:room_kind"`
	Position    int        `gorm:"column:position"`
	Category    string     `gorm:"column:category"`
	Label       string     `gorm:"column:label"`
	Condition   *string    `gorm:"column:condition"`
	Works       *string    `gorm:"column:works"`
	Quantity    int        `gorm:"column:quantity"`
	Comment     string     `gorm:"column:comment"`
	ToCheck     bool       `gorm:"column:to_check"`
	EntryItemID *uuid.UUID `gorm:"column:entry_item_id"`
	Version     int        `gorm:"column:version"`
	CreatedAt   time.Time  `gorm:"column:created_at"`
	CreatedBy   string     `gorm:"column:created_by"`
	UpdatedAt   time.Time  `gorm:"column:updated_at"`
	UpdatedBy   string     `gorm:"column:updated_by"`
}

func (itemRow) TableName() string { return "inventory_items" }

type meterRow struct {
	ID          uuid.UUID `gorm:"column:id;primaryKey"`
	OwnerID     uuid.UUID `gorm:"column:owner_id"`
	InventoryID uuid.UUID `gorm:"column:inventory_id"`
	Kind        string    `gorm:"column:kind"`
	MeterNumber string    `gorm:"column:meter_number"`
	Reading     string    `gorm:"column:reading"`
	Unit        string    `gorm:"column:unit"`
	CreatedAt   time.Time `gorm:"column:created_at"`
	CreatedBy   string    `gorm:"column:created_by"`
}

func (meterRow) TableName() string { return "inventory_meters" }

type keyRow struct {
	ID          uuid.UUID `gorm:"column:id;primaryKey"`
	OwnerID     uuid.UUID `gorm:"column:owner_id"`
	InventoryID uuid.UUID `gorm:"column:inventory_id"`
	Kind        string    `gorm:"column:kind"`
	Destination string    `gorm:"column:destination"`
	Quantity    int       `gorm:"column:quantity"`
	Comment     string    `gorm:"column:comment"`
	CreatedAt   time.Time `gorm:"column:created_at"`
	CreatedBy   string    `gorm:"column:created_by"`
}

func (keyRow) TableName() string { return "inventory_keys" }

type signatureRow struct {
	ID           uuid.UUID `gorm:"column:id;primaryKey"`
	InventoryID  uuid.UUID `gorm:"column:inventory_id"`
	Party        string    `gorm:"column:party"`
	SignerName   string    `gorm:"column:signer_name"`
	Method       string    `gorm:"column:method"`
	SignedAt     time.Time `gorm:"column:signed_at"`
	Reservations string    `gorm:"column:reservations"`
}

func (signatureRow) TableName() string { return "inventory_signatures" }

// Capture returns an inventory header, locked for the transaction when lock
// is set (false when the scope has none of that id).
func (Repository) Capture(ctx context.Context, id uuid.UUID, lock bool) (captureRow, bool, error) {
	t, err := db.Tx(ctx)
	if err != nil {
		return captureRow{}, false, err
	}
	q := t.Where("id = ?", id).Limit(1)
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var rows []captureRow
	if err := q.Find(&rows).Error; err != nil || len(rows) == 0 {
		return captureRow{}, false, err
	}
	return rows[0], true, nil
}

// SaveHeader writes an inventory header at version+1; ErrStale when someone
// saved first.
func (Repository) SaveHeader(ctx context.Context, r captureRow, version int) error {
	t, err := db.Tx(ctx)
	if err != nil {
		return err
	}
	res := t.Model(&captureRow{}).Where("id = ? AND version = ?", r.ID, version).Updates(map[string]any{
		"state": r.State, "done_on": r.DoneOn, "scheduled_at": r.ScheduledAt, "place": r.Place, "observations": r.Observations,
		"document_id": r.DocumentID, "template_version": r.TemplateVersion, "version": version + 1,
		"updated_at": r.UpdatedAt, "updated_by": r.UpdatedBy,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return invariant.ErrStale
	}
	return nil
}

// Items returns the items of an inventory in position order.
func (Repository) Items(ctx context.Context, inventory uuid.UUID) ([]itemRow, error) {
	t, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []itemRow
	err = t.Where("inventory_id = ?", inventory).Order("position").Find(&rows).Error
	return rows, err
}

// InsertItems records new items.
func (Repository) InsertItems(ctx context.Context, rows []itemRow) error {
	if len(rows) == 0 {
		return nil
	}
	t, err := db.Tx(ctx)
	if err != nil {
		return err
	}
	return t.Create(&rows).Error
}

// SaveItem writes an item at version+1; ErrStale when someone saved first.
func (Repository) SaveItem(ctx context.Context, r itemRow, version int) error {
	t, err := db.Tx(ctx)
	if err != nil {
		return err
	}
	res := t.Model(&itemRow{}).Where("id = ? AND inventory_id = ? AND version = ?", r.ID, r.InventoryID, version).Updates(map[string]any{
		"condition": r.Condition, "works": r.Works, "quantity": r.Quantity, "comment": r.Comment, "to_check": r.ToCheck,
		"version": version + 1, "updated_at": r.UpdatedAt, "updated_by": r.UpdatedBy,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return invariant.ErrStale
	}
	return nil
}

// shiftOffset moves items out of the way while positions are renumbered:
// (inventory_id, position) is unique and checked row by row.
const shiftOffset = 1_000_000

// ShiftItems moves the items at position from and after one place down, to
// make room for an item at from.
func (Repository) ShiftItems(ctx context.Context, inventory uuid.UUID, from int) error {
	t, err := db.Tx(ctx)
	if err != nil {
		return err
	}
	if err := t.Model(&itemRow{}).Where("inventory_id = ? AND position >= ?", inventory, from).
		Update("position", gorm.Expr("position + ?", shiftOffset)).Error; err != nil {
		return err
	}
	return t.Model(&itemRow{}).Where("inventory_id = ? AND position >= ?", inventory, shiftOffset).
		Update("position", gorm.Expr("position - ?", shiftOffset-1)).Error
}

// Meters returns the meters of an inventory in the order they were listed.
func (Repository) Meters(ctx context.Context, inventory uuid.UUID) ([]meterRow, error) {
	t, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []meterRow
	err = t.Where("inventory_id = ?", inventory).Order("created_at, id").Find(&rows).Error
	return rows, err
}

// ReplaceMeters makes keep the meters of an inventory: rows with a known id
// are updated, the others inserted, and the meters left out deleted.
func (Repository) ReplaceMeters(ctx context.Context, inventory uuid.UUID, keep []meterRow, existing map[uuid.UUID]bool) error {
	t, err := db.Tx(ctx)
	if err != nil {
		return err
	}
	ids := []uuid.UUID{}
	for _, m := range keep {
		ids = append(ids, m.ID)
		if existing[m.ID] {
			if err := t.Model(&meterRow{}).Where("id = ? AND inventory_id = ?", m.ID, inventory).
				Updates(map[string]any{"kind": m.Kind, "meter_number": m.MeterNumber, "reading": m.Reading, "unit": m.Unit}).Error; err != nil {
				return err
			}
			continue
		}
		if err := t.Create(&m).Error; err != nil {
			return err
		}
	}
	q := t.Where("inventory_id = ?", inventory)
	if len(ids) > 0 {
		q = q.Where("id NOT IN ?", ids)
	}
	return q.Delete(&meterRow{}).Error
}

// Keys returns the key lines of an inventory in the order they were listed.
func (Repository) Keys(ctx context.Context, inventory uuid.UUID) ([]keyRow, error) {
	t, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []keyRow
	err = t.Where("inventory_id = ?", inventory).Order("created_at, id").Find(&rows).Error
	return rows, err
}

// ReplaceKeys makes rows the key lines of an inventory.
func (Repository) ReplaceKeys(ctx context.Context, inventory uuid.UUID, rows []keyRow) error {
	t, err := db.Tx(ctx)
	if err != nil {
		return err
	}
	if err := t.Where("inventory_id = ?", inventory).Delete(&keyRow{}).Error; err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	return t.Create(&rows).Error
}

// Photos returns the photos of an inventory, oldest first.
func (Repository) Photos(ctx context.Context, inventory uuid.UUID) ([]photoRow, error) {
	t, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []photoRow
	err = t.Where("inventory_id = ?", inventory).Order("created_at, id").Find(&rows).Error
	return rows, err
}

// Signatures returns the signatures of an inventory, oldest first.
func (Repository) Signatures(ctx context.Context, inventory uuid.UUID) ([]signatureRow, error) {
	t, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []signatureRow
	err = t.Where("inventory_id = ?", inventory).Order("signed_at, id").Find(&rows).Error
	return rows, err
}
