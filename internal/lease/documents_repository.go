package lease

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/invariant"
)

type leaseDocumentRow struct {
	ID            uuid.UUID       `gorm:"column:id;primaryKey"`
	OwnerID       uuid.UUID       `gorm:"column:owner_id"`
	LeaseID       uuid.UUID       `gorm:"column:lease_id"`
	Kind          string          `gorm:"column:kind"`
	Number        string          `gorm:"column:number"`
	LeaseVersion  int             `gorm:"column:lease_version"`
	GuaranteeID   *uuid.UUID      `gorm:"column:guarantee_id"`
	DocumentID    uuid.UUID       `gorm:"column:document_id"`
	InputSnapshot json.RawMessage `gorm:"column:input_snapshot;type:jsonb"`
	IssuedAt      time.Time       `gorm:"column:issued_at"`
	CreatedBy     string          `gorm:"column:created_by"`
}

func (leaseDocumentRow) TableName() string { return "lease_documents" }

type inventoryRow struct {
	ID         uuid.UUID       `gorm:"column:id;primaryKey"`
	OwnerID    uuid.UUID       `gorm:"column:owner_id"`
	LeaseID    uuid.UUID       `gorm:"column:lease_id"`
	Kind       string          `gorm:"column:kind"`
	DoneOn     time.Time       `gorm:"column:done_on"`
	Items      json.RawMessage `gorm:"column:items;type:jsonb"`
	DocumentID uuid.UUID       `gorm:"column:document_id"`
	CreatedAt  time.Time       `gorm:"column:created_at"`
	CreatedBy  string          `gorm:"column:created_by"`
}

func (inventoryRow) TableName() string { return "inventories" }

// docInfo is what the lease reads of an archived document.
type docInfo struct {
	ID              uuid.UUID  `gorm:"column:id"`
	Class           string     `gorm:"column:class"`
	LeaseID         *uuid.UUID `gorm:"column:lease_id"`
	Source          string     `gorm:"column:source"`
	SignsDocumentID *uuid.UUID `gorm:"column:signs_document_id"`
}

// InsertLeaseDocument records a generated lease document.
func (Repository) InsertLeaseDocument(ctx context.Context, d leaseDocumentRow) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	return t.Create(&d).Error
}

// LeaseDocuments returns the generated documents of a lease, oldest first.
func (Repository) LeaseDocuments(ctx context.Context, leaseID uuid.UUID) ([]leaseDocumentRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []leaseDocumentRow
	err = t.Where("lease_id = ?", leaseID).Order("issued_at, number").Find(&rows).Error
	return rows, err
}

// LatestLeaseDocument returns the latest generated document of kind for a
// lease.
func (Repository) LatestLeaseDocument(ctx context.Context, leaseID uuid.UUID, kind string) (leaseDocumentRow, bool, error) {
	t, err := tx(ctx)
	if err != nil {
		return leaseDocumentRow{}, false, err
	}
	var rows []leaseDocumentRow
	err = t.Where("lease_id = ? AND kind = ?", leaseID, kind).Order("issued_at DESC, number DESC").Limit(1).Find(&rows).Error
	if err != nil || len(rows) == 0 {
		return leaseDocumentRow{}, false, err
	}
	return rows[0], true, nil
}

// SignedCopyExists reports whether an uploaded document is the signed copy of
// the generated document.
func (Repository) SignedCopyExists(ctx context.Context, documentID uuid.UUID) (bool, error) {
	t, err := tx(ctx)
	if err != nil {
		return false, err
	}
	var n int64
	err = t.Table("documents").Where("signs_document_id = ? AND source = 'upload'", documentID).Count(&n).Error
	return n > 0, err
}

// Document returns what the lease needs of one archived document.
func (Repository) Document(ctx context.Context, id uuid.UUID) (docInfo, bool, error) {
	t, err := tx(ctx)
	if err != nil {
		return docInfo{}, false, err
	}
	var rows []docInfo
	err = t.Table("documents").Select("id, class, lease_id, source, signs_document_id").Where("id = ?", id).Limit(1).Find(&rows).Error
	if err != nil || len(rows) == 0 {
		return docInfo{}, false, err
	}
	return rows[0], true, nil
}

// Inventories returns the inventories of a lease.
func (Repository) Inventories(ctx context.Context, leaseID uuid.UUID) ([]inventoryRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []inventoryRow
	err = t.Where("lease_id = ?", leaseID).Order("kind").Find(&rows).Error
	return rows, err
}

// InsertInventory records an inventory.
func (Repository) InsertInventory(ctx context.Context, i inventoryRow) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	return t.Create(&i).Error
}

// AddTenant links a tenant to a lease (amendment).
func (Repository) AddTenant(ctx context.Context, r leaseTenantRow) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	return t.Create(&r).Error
}

// AddAsset links an asset to a lease (amendment).
func (Repository) AddAsset(ctx context.Context, r leaseAssetRow) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	return t.Create(&r).Error
}

// Amend records an amendment on the lease: its rent when version matches,
// bumping the version. The deposit is never changed (RENT-SPEC-008).
func (Repository) Amend(ctx context.Context, id uuid.UUID, version int, rentCents int64, at time.Time, by string) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	res := t.Model(&leaseRow{}).Where("id = ? AND version = ?", id, version).Updates(map[string]any{
		"rent_cents": rentCents, "version": version + 1, "updated_at": at, "updated_by": by,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return invariant.ErrStale
	}
	return nil
}

// SetGuaranteeDocument records the generated document of a guarantee,
// bumping its version.
func (Repository) SetGuaranteeDocument(ctx context.Context, id uuid.UUID, version int, doc uuid.UUID, at time.Time, by string) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	res := t.Model(&guaranteeRow{}).Where("id = ? AND version = ?", id, version).Updates(map[string]any{
		"document_id": doc, "version": version + 1, "updated_at": at, "updated_by": by,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return invariant.ErrStale
	}
	return nil
}
