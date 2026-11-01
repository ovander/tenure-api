package billing

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/period"
)

// Kind is a billing document kind (billing_documents.kind).
type Kind string

// Billing document kinds.
const (
	KindRentCall       Kind = "rent_call"
	KindRevisionNotice Kind = "revision_notice"
	KindCancellation   Kind = "cancellation"
)

type docRow struct {
	ID            uuid.UUID       `gorm:"column:id;primaryKey"`
	OwnerID       uuid.UUID       `gorm:"column:owner_id"`
	LeaseID       uuid.UUID       `gorm:"column:lease_id"`
	Kind          string          `gorm:"column:kind"`
	Period        string          `gorm:"column:period"`
	Generation    int             `gorm:"column:generation"`
	Number        string          `gorm:"column:number"`
	Lines         json.RawMessage `gorm:"column:lines;type:jsonb"`
	TotalCents    int64           `gorm:"column:total_cents"`
	IssuedAt      time.Time       `gorm:"column:issued_at"`
	CancelsID     *uuid.UUID      `gorm:"column:cancels_id"`
	DocumentID    uuid.UUID       `gorm:"column:document_id"`
	InputSnapshot json.RawMessage `gorm:"column:input_snapshot;type:jsonb"`
	CreatedBy     string          `gorm:"column:created_by"`
	// CancelledBy is filled by queries, not stored.
	CancelledBy *uuid.UUID `gorm:"column:cancelled_by;->"`
}

func (docRow) TableName() string { return "billing_documents" }

// Tenant is a tenant as documents need it.
type Tenant struct {
	ID       uuid.UUID
	Names    []string
	Email    string
	Language string
}

// Record is a lease with what its documents need.
type Record struct {
	LeaseID      uuid.UUID
	Version      int
	OwnerName    string
	OwnerAddress string
	OwnerLang    string
	Assets       []AssetRef
	Tenants      []Tenant
}

// AssetRef is a let asset.
type AssetRef struct {
	ID      uuid.UUID
	Kind    string
	Address string
	Lot     string
}

// Repository reads billable leases and stores billing documents in the
// caller's scoped transaction.
type Repository struct{}

func tx(ctx context.Context) (*gorm.DB, error) { return db.Tx(ctx) }

// BillableLeaseIDs returns the active or notice leases occupied during p.
func (Repository) BillableLeaseIDs(ctx context.Context, p period.Period) ([]uuid.UUID, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var ids []uuid.UUID
	err = t.Table("leases").Where("state IN ('active', 'notice') AND start_on <= ? AND (end_on IS NULL OR end_on >= ?)",
		p.Last().Time(), p.First().Time()).Order("start_on, id").Pluck("id", &ids).Error
	return ids, err
}

// LeaseIDsInState returns the scope's leases in the given states.
func (Repository) LeaseIDsInState(ctx context.Context, states ...string) ([]uuid.UUID, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var ids []uuid.UUID
	err = t.Table("leases").Where("state IN ?", states).Order("start_on, id").Pluck("id", &ids).Error
	return ids, err
}

// Record returns the parties, assets and tenants of a lease.
func (Repository) Record(ctx context.Context, leaseID uuid.UUID) (Record, error) {
	t, err := tx(ctx)
	if err != nil {
		return Record{}, err
	}
	var head []struct {
		Version   int
		LegalName string
		Address   string
		Language  string
	}
	if err := t.Raw(`SELECT l.version, o.legal_name, o.address, o.language FROM leases l JOIN owners o ON o.id = l.owner_id
		WHERE l.id = ?`, leaseID).Scan(&head).Error; err != nil {
		return Record{}, err
	}
	if len(head) == 0 {
		return Record{}, invariant.ErrNotFound
	}
	r := Record{LeaseID: leaseID, Version: head[0].Version, OwnerName: head[0].LegalName, OwnerAddress: head[0].Address, OwnerLang: head[0].Language}
	var assets []AssetRef
	if err := t.Raw(`SELECT a.id, a.kind, a.address, a.lot FROM lease_assets la JOIN assets a ON a.id = la.asset_id
		WHERE la.lease_id = ? ORDER BY (a.kind = 'box'), a.id`, leaseID).Scan(&assets).Error; err != nil {
		return Record{}, err
	}
	r.Assets = assets
	var tenants []struct {
		ID       uuid.UUID
		Names    db.TextArray
		Email    string
		Language string
	}
	if err := t.Raw(`SELECT t.id, t.names, t.email, t.language FROM lease_tenants lt JOIN tenants t ON t.id = lt.tenant_id
		WHERE lt.lease_id = ? ORDER BY t.created_at, t.id`, leaseID).Scan(&tenants).Error; err != nil {
		return Record{}, err
	}
	for _, x := range tenants {
		r.Tenants = append(r.Tenants, Tenant{ID: x.ID, Names: x.Names, Email: x.Email, Language: x.Language})
	}
	return r, nil
}

const withCancelled = `billing_documents.*, (SELECT c.id FROM billing_documents c WHERE c.cancels_id = billing_documents.id) AS cancelled_by`

// Live returns the live (not cancelled) document of (lease, kind, period) and
// the highest generation issued, cancelled or not.
func (Repository) Live(ctx context.Context, leaseID uuid.UUID, kind Kind, p string) (*docRow, int, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, 0, err
	}
	var rows []docRow
	if err := t.Select(withCancelled).Where("lease_id = ? AND kind = ? AND period = ?", leaseID, string(kind), p).
		Order("generation DESC").Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	if len(rows) == 0 {
		return nil, 0, nil
	}
	if rows[0].CancelledBy == nil {
		return &rows[0], rows[0].Generation, nil
	}
	return nil, rows[0].Generation, nil
}

// Insert records a billing document.
func (Repository) Insert(ctx context.Context, d docRow) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	return t.Omit("cancelled_by").Create(&d).Error
}

// Get returns one billing document of the scope.
func (Repository) Get(ctx context.Context, id uuid.UUID) (docRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return docRow{}, err
	}
	var rows []docRow
	if err := t.Select(withCancelled).Where("id = ?", id).Limit(1).Find(&rows).Error; err != nil {
		return docRow{}, err
	}
	if len(rows) == 0 {
		return docRow{}, invariant.ErrNotFound
	}
	return rows[0], nil
}

// DocFilter narrows a billing document list.
type DocFilter struct {
	Period  string
	LeaseID *uuid.UUID
	Kind    string
}

// List returns a page of the scope's billing documents and the total.
func (Repository) List(ctx context.Context, f DocFilter, offset, limit int) ([]docRow, int, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, 0, err
	}
	q := t.Model(&docRow{})
	if f.Period != "" {
		q = q.Where("period = ?", f.Period)
	}
	if f.LeaseID != nil {
		q = q.Where("lease_id = ?", *f.LeaseID)
	}
	if f.Kind != "" {
		q = q.Where("kind = ?", f.Kind)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []docRow
	err = q.Select(withCancelled).Order("issued_at DESC, id").Offset(offset).Limit(limit).Find(&rows).Error
	return rows, int(total), err
}

// IRLSeries returns the cached IRL index by quarter.
func (Repository) IRLSeries(ctx context.Context) (map[string]Index, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		Quarter     string
		Value       int64
		PublishedOn time.Time
	}
	if err := t.Table("irl_index").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := map[string]Index{}
	for _, r := range rows {
		out[r.Quarter] = Index{Quarter: r.Quarter, Value: r.Value, PublishedOn: period.FromTime(r.PublishedOn)}
	}
	return out, nil
}

// ReceiptsOf returns the receipts (quittance and partial) issued for a rent call.
func (Repository) ReceiptsOf(ctx context.Context, rentCallID uuid.UUID) ([]docRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []docRow
	err = t.Select(withCancelled).Where("kind IN ('receipt', 'partial_receipt') AND input_snapshot ->> 'rent_call_id' = ?", rentCallID.String()).
		Order("issued_at").Find(&rows).Error
	return rows, err
}

// CountKind returns how many documents of kind exist for (lease, period).
func (Repository) CountKind(ctx context.Context, leaseID uuid.UUID, kind Kind, p string) (int, error) {
	t, err := tx(ctx)
	if err != nil {
		return 0, err
	}
	var n int64
	err = t.Model(&docRow{}).Where("lease_id = ? AND kind = ? AND period = ?", leaseID, string(kind), p).Count(&n).Error
	return int(n), err
}

// DueDay returns the due day of a lease.
func (Repository) DueDay(ctx context.Context, leaseID uuid.UUID) (int, error) {
	t, err := tx(ctx)
	if err != nil {
		return 0, err
	}
	var d []int
	if err := t.Table("leases").Where("id = ?", leaseID).Limit(1).Pluck("due_day", &d).Error; err != nil {
		return 0, err
	}
	if len(d) == 0 {
		return 0, invariant.ErrNotFound
	}
	return d[0], nil
}

// ReminderRow is a billing_reminders row.
type ReminderRow struct {
	ID                uuid.UUID `gorm:"column:id;primaryKey"`
	OwnerID           uuid.UUID `gorm:"column:owner_id"`
	BillingDocumentID uuid.UUID `gorm:"column:billing_document_id"`
	SentOn            time.Time `gorm:"column:sent_on"`
	OutstandingCents  int64     `gorm:"column:outstanding_cents"`
	Recipients        int       `gorm:"column:recipients"`
	Failures          int       `gorm:"column:failures"`
	CreatedAt         time.Time `gorm:"column:created_at"`
}

// TableName maps ReminderRow to billing_reminders.
func (ReminderRow) TableName() string { return "billing_reminders" }

// RemindedOn returns the reminder date of rent calls, by id.
func (Repository) RemindedOn(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]period.Date, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	out := map[uuid.UUID]period.Date{}
	if len(ids) == 0 {
		return out, nil
	}
	var rows []ReminderRow
	if err := t.Where("billing_document_id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.BillingDocumentID] = period.FromTime(r.SentOn)
	}
	return out, nil
}

// InsertReminder records a sent reminder; a second one for the same rent call
// is refused by the unique key.
func (Repository) InsertReminder(ctx context.Context, r ReminderRow) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	return t.Create(&r).Error
}
