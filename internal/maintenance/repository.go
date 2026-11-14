package maintenance

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/invariant"
)

// Repository runs the maintenance module's SQL in the caller's scoped
// transaction.
type Repository struct{}

func tx(ctx context.Context) (*gorm.DB, error) { return db.Tx(ctx) }

type contractorRow struct {
	ID        uuid.UUID `gorm:"column:id;primaryKey"`
	OwnerID   uuid.UUID `gorm:"column:owner_id"`
	Name      string    `gorm:"column:name"`
	Trade     string    `gorm:"column:trade"`
	Email     string    `gorm:"column:email"`
	Phone     string    `gorm:"column:phone"`
	Notes     string    `gorm:"column:notes"`
	CreatedAt time.Time `gorm:"column:created_at"`
	CreatedBy string    `gorm:"column:created_by"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
	UpdatedBy string    `gorm:"column:updated_by"`
	Version   int       `gorm:"column:version"`
}

func (contractorRow) TableName() string { return "contractors" }

type incidentRow struct {
	ID                uuid.UUID    `gorm:"column:id;primaryKey"`
	OwnerID           uuid.UUID    `gorm:"column:owner_id"`
	AssetID           uuid.UUID    `gorm:"column:asset_id"`
	LeaseID           *uuid.UUID   `gorm:"column:lease_id"`
	ReportedOn        time.Time    `gorm:"column:reported_on;type:date"`
	ReportedBy        string       `gorm:"column:reported_by"`
	Channel           string       `gorm:"column:channel"`
	ReporterName      string       `gorm:"column:reporter_name"`
	Title             string       `gorm:"column:title"`
	Description       string       `gorm:"column:description"`
	Urgency           string       `gorm:"column:urgency"`
	Status            string       `gorm:"column:status"`
	PhotoDocumentIDs  db.TextArray `gorm:"column:photo_document_ids;type:text[]"`
	ContractorID      *uuid.UUID   `gorm:"column:contractor_id"`
	QuoteCents        *int64       `gorm:"column:quote_cents"`
	QuoteDocumentID   *uuid.UUID   `gorm:"column:quote_document_id"`
	ScheduledOn       *time.Time   `gorm:"column:scheduled_on;type:date"`
	DoneOn            *time.Time   `gorm:"column:done_on;type:date"`
	InvoiceCents      *int64       `gorm:"column:invoice_cents"`
	InvoiceDocumentID *uuid.UUID   `gorm:"column:invoice_document_id"`
	Treatment         *string      `gorm:"column:treatment"`
	PaidOn            *time.Time   `gorm:"column:paid_on;type:date"`
	ChargeID          *uuid.UUID   `gorm:"column:charge_id"`
	CancelReason      string       `gorm:"column:cancel_reason"`
	CreatedAt         time.Time    `gorm:"column:created_at"`
	CreatedBy         string       `gorm:"column:created_by"`
	UpdatedAt         time.Time    `gorm:"column:updated_at"`
	UpdatedBy         string       `gorm:"column:updated_by"`
	Version           int          `gorm:"column:version"`
}

func (incidentRow) TableName() string { return "incidents" }

// Contractors lists the scope's contractors, by name.
func (Repository) Contractors(ctx context.Context) ([]contractorRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []contractorRow
	err = t.Order("name, id").Find(&rows).Error
	return rows, err
}

// Contractor returns one contractor.
func (Repository) Contractor(ctx context.Context, id uuid.UUID) (contractorRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return contractorRow{}, err
	}
	var rows []contractorRow
	if err := t.Where("id = ?", id).Limit(1).Find(&rows).Error; err != nil {
		return contractorRow{}, err
	}
	if len(rows) == 0 {
		return contractorRow{}, invariant.ErrNotFound
	}
	return rows[0], nil
}

// InsertContractor records a contractor.
func (Repository) InsertContractor(ctx context.Context, r contractorRow) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	return t.Create(&r).Error
}

// UpdateContractor replaces a contractor when its version is the one read.
func (Repository) UpdateContractor(ctx context.Context, r contractorRow, version int) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	res := t.Model(&contractorRow{}).Where("id = ? AND version = ?", r.ID, version).Updates(map[string]any{
		"name": r.Name, "trade": r.Trade, "email": r.Email, "phone": r.Phone, "notes": r.Notes,
		"updated_at": r.UpdatedAt, "updated_by": r.UpdatedBy, "version": version + 1})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return invariant.ErrStale
	}
	return nil
}

// IncidentFilter narrows the incidents listed.
type IncidentFilter struct {
	AssetID *uuid.UUID
	Status  string
	Open    bool
}

// Incidents lists the scope's incidents, the latest reported first.
func (Repository) Incidents(ctx context.Context, f IncidentFilter) ([]incidentRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	q := t.Model(&incidentRow{})
	if f.AssetID != nil {
		q = q.Where("asset_id = ?", *f.AssetID)
	}
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.Open {
		q = q.Where("status NOT IN ('invoiced', 'cancelled')")
	}
	var rows []incidentRow
	err = q.Order("reported_on DESC, created_at DESC, id").Find(&rows).Error
	return rows, err
}

// Incident returns one incident, locked for update when forUpdate.
func (Repository) Incident(ctx context.Context, id uuid.UUID, forUpdate bool) (incidentRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return incidentRow{}, err
	}
	q := t.Where("id = ?", id)
	if forUpdate {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var rows []incidentRow
	if err := q.Limit(1).Find(&rows).Error; err != nil {
		return incidentRow{}, err
	}
	if len(rows) == 0 {
		return incidentRow{}, invariant.ErrNotFound
	}
	return rows[0], nil
}

// InsertIncident records an incident.
func (Repository) InsertIncident(ctx context.Context, r incidentRow) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	return t.Create(&r).Error
}

// SaveIncident replaces an incident when its version is the one read,
// bumping it.
func (Repository) SaveIncident(ctx context.Context, r incidentRow, version int) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	res := t.Model(&incidentRow{}).Where("id = ? AND version = ?", r.ID, version).Updates(map[string]any{
		"lease_id": r.LeaseID, "reported_on": r.ReportedOn, "reported_by": r.ReportedBy, "channel": r.Channel,
		"reporter_name": r.ReporterName, "title": r.Title, "description": r.Description, "urgency": r.Urgency, "status": r.Status,
		"photo_document_ids": r.PhotoDocumentIDs, "contractor_id": r.ContractorID, "quote_cents": r.QuoteCents,
		"quote_document_id": r.QuoteDocumentID, "scheduled_on": r.ScheduledOn, "done_on": r.DoneOn, "invoice_cents": r.InvoiceCents,
		"invoice_document_id": r.InvoiceDocumentID, "treatment": r.Treatment, "paid_on": r.PaidOn, "charge_id": r.ChargeID,
		"cancel_reason": r.CancelReason, "updated_at": r.UpdatedAt, "updated_by": r.UpdatedBy, "version": version + 1})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return invariant.ErrStale
	}
	return nil
}

// LeaseOfAsset reports whether a lease of the scope lets the asset.
func (Repository) LeaseOfAsset(ctx context.Context, lease, asset uuid.UUID) (bool, error) {
	t, err := tx(ctx)
	if err != nil {
		return false, err
	}
	var n int64
	err = t.Table("lease_assets").Where("lease_id = ? AND asset_id = ?", lease, asset).Count(&n).Error
	return n > 0, err
}
