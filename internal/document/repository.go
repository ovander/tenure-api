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
	Source          string     `gorm:"column:source"`
	ContentType     string     `gorm:"column:content_type"`
	OriginalName    string     `gorm:"column:original_name"`
	SignsDocumentID *uuid.UUID `gorm:"column:signs_document_id"`
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
func (Repository) ReserveNumber(ctx context.Context, owner uuid.UUID, class Class, series, assetCode string, year int) (int, error) {
	tx, err := db.Tx(ctx)
	if err != nil {
		return 0, err
	}
	var seq int
	err = tx.Raw(`INSERT INTO document_counters (owner_id, class, series, asset_code, year, last_seq) VALUES (?, ?, ?, ?, ?, 1)
		ON CONFLICT (owner_id, class, series, asset_code, year) DO UPDATE SET last_seq = document_counters.last_seq + 1
		RETURNING last_seq`, owner, string(class), series, assetCode, year).Scan(&seq).Error
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

// LinksExist reports whether the asset and the lease, when given, exist in the
// scope.
func (Repository) LinksExist(ctx context.Context, assetID, leaseID *uuid.UUID) (bool, error) {
	tx, err := db.Tx(ctx)
	if err != nil {
		return false, err
	}
	for table, id := range map[string]*uuid.UUID{"assets": assetID, "leases": leaseID} {
		if id == nil {
			continue
		}
		var n int64
		if err := tx.Table(table).Where("id = ?", *id).Count(&n).Error; err != nil {
			return false, err
		}
		if n == 0 {
			return false, nil
		}
	}
	return true, nil
}

// Filter narrows a document list.
type Filter struct {
	Class   string
	AssetID *uuid.UUID
	LeaseID *uuid.UUID
	Period  string
	Year    int
	// Number is a generated document's number (AL-…, QL-…, CT-…).
	Number string
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
	if f.Number != "" {
		// Numbers are [A-Z0-9-] only (checked by the handler): no LIKE wildcard.
		q = q.Where("object_key LIKE ?", "%/"+f.Number+".pdf")
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

// ExportFilter selects the documents of an export: one year of issue, or
// everything about one asset (its own documents and its leases').
type ExportFilter struct {
	Year    int
	AssetID *uuid.UUID
	// Classes, when set, limits the export to these classes (accountant).
	Classes []string
}

// ExportDocuments returns the scope's documents an export holds.
func (Repository) ExportDocuments(ctx context.Context, f ExportFilter) ([]documentRow, error) {
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	q := tx.Model(&documentRow{}).Where("issued_at IS NOT NULL")
	if f.Year != 0 {
		q = q.Where("extract(year FROM issued_at AT TIME ZONE 'Europe/Paris') = ?", f.Year)
	}
	if f.AssetID != nil {
		q = q.Where("asset_id = ? OR lease_id IN (SELECT lease_id FROM lease_assets WHERE asset_id = ?)", *f.AssetID, *f.AssetID)
	}
	if len(f.Classes) > 0 {
		q = q.Where("class IN ?", f.Classes)
	}
	var rows []documentRow
	err = q.Order("issued_at, id").Find(&rows).Error
	return rows, err
}

type exportRow struct {
	ID            uuid.UUID  `gorm:"column:id;primaryKey"`
	OwnerID       uuid.UUID  `gorm:"column:owner_id"`
	RequestedBy   string     `gorm:"column:requested_by"`
	Role          string     `gorm:"column:role"`
	Year          *int       `gorm:"column:year"`
	AssetID       *uuid.UUID `gorm:"column:asset_id"`
	State         string     `gorm:"column:state"`
	ObjectKey     string     `gorm:"column:object_key"`
	ObjectVersion string     `gorm:"column:object_version"`
	SHA256        string     `gorm:"column:sha256"`
	SizeBytes     int64      `gorm:"column:size_bytes"`
	Documents     int        `gorm:"column:documents"`
	Error         string     `gorm:"column:error"`
	CreatedAt     time.Time  `gorm:"column:created_at"`
	FinishedAt    *time.Time `gorm:"column:finished_at"`
}

func (exportRow) TableName() string { return "export_jobs" }

// InsertExport records a requested export.
func (Repository) InsertExport(ctx context.Context, e exportRow) error {
	tx, err := db.Tx(ctx)
	if err != nil {
		return err
	}
	return tx.Create(&e).Error
}

// Export returns one export visible in the scope.
func (Repository) Export(ctx context.Context, id uuid.UUID) (exportRow, error) {
	tx, err := db.Tx(ctx)
	if err != nil {
		return exportRow{}, err
	}
	var rows []exportRow
	if err := tx.Where("id = ?", id).Limit(1).Find(&rows).Error; err != nil {
		return exportRow{}, err
	}
	if len(rows) == 0 {
		return exportRow{}, invariant.ErrNotFound
	}
	return rows[0], nil
}

// FinishExport records the outcome of an export.
func (Repository) FinishExport(ctx context.Context, e exportRow) error {
	tx, err := db.Tx(ctx)
	if err != nil {
		return err
	}
	return tx.Model(&exportRow{}).Where("id = ? AND state = 'pending'", e.ID).Updates(map[string]any{
		"state": e.State, "object_key": e.ObjectKey, "object_version": e.ObjectVersion, "sha256": e.SHA256,
		"size_bytes": e.SizeBytes, "documents": e.Documents, "error": e.Error, "finished_at": e.FinishedAt,
	}).Error
}

// ClockStart is a document whose retention clock can now start.
type ClockStart struct {
	ID            uuid.UUID `gorm:"column:id"`
	ObjectKey     string    `gorm:"column:object_key"`
	ObjectVersion string    `gorm:"column:object_version"`
	// From is the day the clock starts: the lease's end, or 31 December of
	// the latest exercise year of the charges it supports.
	From time.Time `gorm:"column:clock_from"`
	// IssuedAt dates the lock the object already carries.
	IssuedAt time.Time `gorm:"column:issued_at"`
}

// ClockStarts returns the scope's documents with no retain_until whose clock
// has started (Q-68): lease documents of a lease that ended, and supporting
// documents of charges.
func (Repository) ClockStarts(ctx context.Context) ([]ClockStart, error) {
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	var out []ClockStart
	err = tx.Raw(`SELECT d.id, d.object_key, d.object_version, l.end_on AS clock_from, d.issued_at
			FROM documents d JOIN leases l ON l.id = d.lease_id
			WHERE d.retain_until IS NULL AND d.issued_at IS NOT NULL
				AND d.class IN ('contract', 'amendment', 'deposit', 'surety', 'inventory', 'restitution', 'notice')
				AND l.state IN ('ended', 'closed') AND l.end_on IS NOT NULL
		UNION ALL
		SELECT d.id, d.object_key, d.object_version, make_date(max(c.exercise_year), 12, 31) AS clock_from, d.issued_at
			FROM documents d JOIN charges c ON c.document_id = d.id
			WHERE d.retain_until IS NULL AND d.issued_at IS NOT NULL AND d.lease_id IS NULL
			GROUP BY d.id, d.object_key, d.object_version, d.issued_at
		ORDER BY 1`).Scan(&out).Error
	return out, err
}

// SetRetainUntil starts a document's retention clock (allowed once by the
// trigger, from unset).
func (Repository) SetRetainUntil(ctx context.Context, id uuid.UUID, until time.Time) error {
	tx, err := db.Tx(ctx)
	if err != nil {
		return err
	}
	return tx.Model(&documentRow{}).Where("id = ? AND retain_until IS NULL", id).Update("retain_until", until).Error
}

// PastRetention returns the ids of the scope's documents whose retention
// ended before day: eligible for a manual, logged deletion, never deleted
// automatically (RENT-SPEC-005).
func (Repository) PastRetention(ctx context.Context, day time.Time) ([]uuid.UUID, error) {
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	var ids []uuid.UUID
	err = tx.Model(&documentRow{}).Where("retain_until < ?", day).Order("retain_until, id").Pluck("id", &ids).Error
	return ids, err
}

// Sample returns up to n of the scope's archived documents, at random.
func (Repository) Sample(ctx context.Context, n int) ([]documentRow, error) {
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []documentRow
	err = tx.Where("issued_at IS NOT NULL").Order("random()").Limit(n).Find(&rows).Error
	return rows, err
}

// TemplateUsage counts the scope's generated documents by template version,
// with the first and last issue times.
func (Repository) TemplateUsage(ctx context.Context) ([]TemplateUsage, error) {
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	var out []TemplateUsage
	err = tx.Model(&documentRow{}).
		Select("template_version, count(*) AS documents, min(issued_at) AS first_at, max(issued_at) AS last_at").
		Where("source = ? AND template_version <> '' AND issued_at IS NOT NULL", SourceGenerated).
		Group("template_version").Order("template_version").Scan(&out).Error
	return out, err
}
