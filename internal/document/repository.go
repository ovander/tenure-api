package document

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/invariant"
)

type documentRow struct {
	ID              uuid.UUID  `gorm:"column:id;primaryKey"`
	OwnerID         uuid.UUID  `gorm:"column:owner_id"`
	Class           string     `gorm:"column:class"`
	AssetID         *uuid.UUID `gorm:"column:asset_id"`
	LeaseID         *uuid.UUID `gorm:"column:lease_id"`
	Period          *string    `gorm:"column:period"`
	ObjectKey       string     `gorm:"column:object_key"`
	ObjectVersion   string     `gorm:"column:object_version"`
	SHA256          string     `gorm:"column:sha256"`
	SizeBytes       int64      `gorm:"column:size_bytes"`
	TemplateVersion string     `gorm:"column:template_version"`
	IssuedAt        *time.Time `gorm:"column:issued_at"`
	RetainUntil     *time.Time `gorm:"column:retain_until"`
	CreatedAt       time.Time  `gorm:"column:created_at"`
	CreatedBy       string     `gorm:"column:created_by"`
}

func (documentRow) TableName() string { return "documents" }

// DeliveryRecord is a document_deliveries row.
type DeliveryRecord struct {
	ID         uuid.UUID `gorm:"column:id;primaryKey"`
	OwnerID    uuid.UUID `gorm:"column:owner_id"`
	DocumentID uuid.UUID `gorm:"column:document_id"`
	Recipient  string    `gorm:"column:recipient"`
	Channel    string    `gorm:"column:channel"`
	Status     string    `gorm:"column:status"`
	Attempts   int       `gorm:"column:attempts"`
	LastError  string    `gorm:"column:last_error"`
	CreatedAt  time.Time `gorm:"column:created_at"`
	UpdatedAt  time.Time `gorm:"column:updated_at"`
}

// TableName maps DeliveryRecord to document_deliveries.
func (DeliveryRecord) TableName() string { return "document_deliveries" }

// Repository stores document metadata, counters and deliveries in the
// caller's scoped transaction.
type Repository struct{}

// ReserveNumber returns the next sequence of (class, asset, year) for the
// scope's owner, locking the counter row until the transaction ends, so a
// rollback gives the number back.
func (Repository) ReserveNumber(ctx context.Context, owner uuid.UUID, class Class, assetCode string, year int) (int, error) {
	tx, err := db.Tx(ctx)
	if err != nil {
		return 0, err
	}
	var seq int
	err = tx.Raw(`INSERT INTO document_counters (owner_id, class, asset_code, year, last_seq) VALUES (?, ?, ?, ?, 1)
		ON CONFLICT (owner_id, class, asset_code, year) DO UPDATE SET last_seq = document_counters.last_seq + 1
		RETURNING last_seq`, owner, string(class), assetCode, year).Scan(&seq).Error
	return seq, err
}

// Insert records a document.
func (Repository) Insert(ctx context.Context, d documentRow) error {
	tx, err := db.Tx(ctx)
	if err != nil {
		return err
	}
	return tx.Create(&d).Error
}

// Get returns one document of the scope.
func (Repository) Get(ctx context.Context, id uuid.UUID) (documentRow, error) {
	tx, err := db.Tx(ctx)
	if err != nil {
		return documentRow{}, err
	}
	var rows []documentRow
	if err := tx.Where("id = ?", id).Limit(1).Find(&rows).Error; err != nil {
		return documentRow{}, err
	}
	if len(rows) == 0 {
		return documentRow{}, invariant.ErrNotFound
	}
	return rows[0], nil
}

// Filter narrows a document list.
type Filter struct {
	Class   string
	AssetID *uuid.UUID
	LeaseID *uuid.UUID
	Period  string
	Year    int
	// Classes, when set, limits the list to these classes.
	Classes []string
}

// List returns a page of the scope's documents and the total.
func (Repository) List(ctx context.Context, f Filter, offset, limit int) ([]documentRow, int, error) {
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, 0, err
	}
	q := tx.Model(&documentRow{})
	if f.Class != "" {
		q = q.Where("class = ?", f.Class)
	}
	if len(f.Classes) > 0 {
		q = q.Where("class IN ?", f.Classes)
	}
	if f.AssetID != nil {
		q = q.Where("asset_id = ?", *f.AssetID)
	}
	if f.LeaseID != nil {
		q = q.Where("lease_id = ?", *f.LeaseID)
	}
	if f.Period != "" {
		q = q.Where("period = ?", f.Period)
	}
	if f.Year != 0 {
		q = q.Where("extract(year FROM issued_at AT TIME ZONE 'Europe/Paris') = ?", f.Year)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []documentRow
	err = q.Order("issued_at DESC NULLS LAST, id").Offset(offset).Limit(limit).Find(&rows).Error
	return rows, int(total), err
}

// InsertDelivery records a pending delivery.
func (Repository) InsertDelivery(ctx context.Context, d DeliveryRecord) error {
	tx, err := db.Tx(ctx)
	if err != nil {
		return err
	}
	return tx.Create(&d).Error
}

// SetDelivery records the outcome of a delivery attempt.
func (Repository) SetDelivery(ctx context.Context, id uuid.UUID, status, lastError string, at time.Time) error {
	tx, err := db.Tx(ctx)
	if err != nil {
		return err
	}
	return tx.Model(&DeliveryRecord{}).Where("id = ?", id).Updates(map[string]any{
		"status": status, "last_error": lastError, "attempts": gorm.Expr("attempts + 1"), "updated_at": at}).Error
}

// Deliveries returns the deliveries of a document.
func (Repository) Deliveries(ctx context.Context, documentID uuid.UUID) ([]DeliveryRecord, error) {
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	var out []DeliveryRecord
	err = tx.Where("document_id = ?", documentID).Order("created_at").Find(&out).Error
	return out, err
}

// DeliveryRow builds a delivery row (used by the modules that issue documents).
func DeliveryRow(id, owner, documentID uuid.UUID, recipient, status string, at time.Time) DeliveryRecord {
	return DeliveryRecord{ID: id, OwnerID: owner, DocumentID: documentID, Recipient: recipient, Channel: "email",
		Status: status, CreatedAt: at, UpdatedAt: at}
}
