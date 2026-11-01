package payment

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/invariant"
)

type paymentRow struct {
	ID          uuid.UUID `gorm:"column:id;primaryKey"`
	OwnerID     uuid.UUID `gorm:"column:owner_id"`
	LeaseID     uuid.UUID `gorm:"column:lease_id"`
	PaidOn      time.Time `gorm:"column:paid_on"`
	AmountCents int64     `gorm:"column:amount_cents"`
	Method      string    `gorm:"column:method"`
	Reference   string    `gorm:"column:reference"`
	BankLineID  *string   `gorm:"column:bank_line_id"`
	Version     int       `gorm:"column:version"`
	CreatedAt   time.Time `gorm:"column:created_at"`
	CreatedBy   string    `gorm:"column:created_by"`
	UpdatedAt   time.Time `gorm:"column:updated_at"`
	UpdatedBy   string    `gorm:"column:updated_by"`
}

func (paymentRow) TableName() string { return "payments" }

// MatchRow is a payment_matches row.
type MatchRow struct {
	ID                uuid.UUID `gorm:"column:id;primaryKey"`
	OwnerID           uuid.UUID `gorm:"column:owner_id"`
	PaymentID         uuid.UUID `gorm:"column:payment_id"`
	BillingDocumentID uuid.UUID `gorm:"column:billing_document_id"`
	AmountCents       int64     `gorm:"column:amount_cents"`
	CreatedAt         time.Time `gorm:"column:created_at"`
	CreatedBy         string    `gorm:"column:created_by"`
}

// TableName maps MatchRow to payment_matches.
func (MatchRow) TableName() string { return "payment_matches" }

// Due is a live rent call of a lease and what remains to pay on it.
type Due struct {
	BillingDocumentID uuid.UUID `json:"billing_document_id" gorm:"column:id"`
	Number            string    `json:"number" gorm:"column:number"`
	Period            string    `json:"period" gorm:"column:period"`
	TotalCents        int64     `json:"total_cents" gorm:"column:total_cents"`
	PaidCents         int64     `json:"paid_cents" gorm:"column:paid_cents"`
}

// OutstandingCents is what remains to pay.
func (d Due) OutstandingCents() int64 { return d.TotalCents - d.PaidCents }

// Repository stores payments and matches in the caller's scoped transaction.
// It reads rent calls from billing_documents directly: payment depends on
// lease only (RENT-BE-001 §1), never on the billing module.
type Repository struct{}

func tx(ctx context.Context) (*gorm.DB, error) { return db.Tx(ctx) }

// LockLease serialises matching on one lease until the transaction ends.
func (Repository) LockLease(ctx context.Context, leaseID uuid.UUID) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	return t.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", "match:"+leaseID.String()).Error
}

// LeaseState returns the state of a lease of the scope.
func (Repository) LeaseState(ctx context.Context, leaseID uuid.UUID) (string, error) {
	t, err := tx(ctx)
	if err != nil {
		return "", err
	}
	var s []string
	if err := t.Table("leases").Where("id = ?", leaseID).Limit(1).Pluck("state", &s).Error; err != nil {
		return "", err
	}
	if len(s) == 0 {
		return "", invariant.ErrNotFound
	}
	return s[0], nil
}

// Insert creates a payment.
func (Repository) Insert(ctx context.Context, p paymentRow) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	return t.Create(&p).Error
}

// Update changes a payment when version matches.
func (Repository) Update(ctx context.Context, p paymentRow, version int) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	res := t.Model(&paymentRow{}).Where("id = ? AND version = ?", p.ID, version).Updates(map[string]any{
		"paid_on": p.PaidOn, "amount_cents": p.AmountCents, "method": p.Method, "reference": p.Reference,
		"version": version + 1, "updated_at": p.UpdatedAt, "updated_by": p.UpdatedBy,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return invariant.ErrStale
	}
	return nil
}

// Get returns one payment of the scope.
func (Repository) Get(ctx context.Context, id uuid.UUID) (paymentRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return paymentRow{}, err
	}
	var rows []paymentRow
	if err := t.Where("id = ?", id).Limit(1).Find(&rows).Error; err != nil {
		return paymentRow{}, err
	}
	if len(rows) == 0 {
		return paymentRow{}, invariant.ErrNotFound
	}
	return rows[0], nil
}

// Filter narrows a payment list.
type Filter struct {
	LeaseID   *uuid.UUID
	Unmatched bool
}

// List returns a page of payments and the total.
func (Repository) List(ctx context.Context, f Filter, offset, limit int) ([]paymentRow, int, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, 0, err
	}
	q := t.Model(&paymentRow{})
	if f.LeaseID != nil {
		q = q.Where("lease_id = ?", *f.LeaseID)
	}
	if f.Unmatched {
		q = q.Where("amount_cents > (SELECT coalesce(sum(m.amount_cents), 0) FROM payment_matches m WHERE m.payment_id = payments.id)")
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []paymentRow
	err = q.Order("paid_on DESC, id").Offset(offset).Limit(limit).Find(&rows).Error
	return rows, int(total), err
}

// Matches returns the matches of payments, by payment id.
func (Repository) Matches(ctx context.Context, paymentIDs []uuid.UUID) (map[uuid.UUID][]MatchRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	out := map[uuid.UUID][]MatchRow{}
	if len(paymentIDs) == 0 {
		return out, nil
	}
	var rows []MatchRow
	if err := t.Where("payment_id IN ?", paymentIDs).Order("created_at, id").Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, m := range rows {
		out[m.PaymentID] = append(out[m.PaymentID], m)
	}
	return out, nil
}

// MatchesOfDocument returns the matches applied to a rent call.
func (Repository) MatchesOfDocument(ctx context.Context, billingDocumentID uuid.UUID) ([]MatchRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []MatchRow
	err = t.Where("billing_document_id = ?", billingDocumentID).Order("created_at, id").Find(&rows).Error
	return rows, err
}

// InsertMatches records matches.
func (Repository) InsertMatches(ctx context.Context, ms []MatchRow) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	return t.Create(&ms).Error
}

// MoveMatch points a match to another rent call with a new amount (cancel and
// reissue); an amount of 0 removes the match, its money becoming unapplied.
func (Repository) MoveMatch(ctx context.Context, id, billingDocumentID uuid.UUID, amount int64) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	if amount == 0 {
		return t.Where("id = ?", id).Delete(&MatchRow{}).Error
	}
	return t.Model(&MatchRow{}).Where("id = ?", id).Updates(map[string]any{
		"billing_document_id": billingDocumentID, "amount_cents": amount}).Error
}

// PaymentsByID returns payments of the scope by id.
func (Repository) PaymentsByID(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]paymentRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	out := map[uuid.UUID]paymentRow{}
	if len(ids) == 0 {
		return out, nil
	}
	var rows []paymentRow
	if err := t.Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.ID] = r
	}
	return out, nil
}

// Info is what a receipt shows of a payment.
type Info struct {
	PaidOn time.Time
	Method string
}

// Info returns the date and method of payments.
func (r Repository) Info(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]Info, error) {
	rows, err := r.PaymentsByID(ctx, ids)
	out := map[uuid.UUID]Info{}
	for id, p := range rows {
		out[id] = Info{PaidOn: p.PaidOn, Method: p.Method}
	}
	return out, err
}

// LeasesWithDues returns the leases of the scope that have rent calls.
func (Repository) LeasesWithDues(ctx context.Context) ([]uuid.UUID, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var ids []uuid.UUID
	err = t.Table("billing_documents").Where("kind = 'rent_call'").Distinct().Order("lease_id").Pluck("lease_id", &ids).Error
	return ids, err
}

const liveRentCalls = `SELECT d.id, d.number, d.period, d.total_cents,
	coalesce((SELECT sum(m.amount_cents) FROM payment_matches m WHERE m.billing_document_id = d.id), 0) AS paid_cents
	FROM billing_documents d
	WHERE d.kind = 'rent_call' AND NOT EXISTS (SELECT 1 FROM billing_documents c WHERE c.cancels_id = d.id)`

// Dues returns the live rent calls of a lease, oldest period first.
func (Repository) Dues(ctx context.Context, leaseID uuid.UUID) ([]Due, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var out []Due
	err = t.Raw(liveRentCalls+` AND d.lease_id = ? ORDER BY d.period, d.issued_at`, leaseID).Scan(&out).Error
	return out, err
}

// Due returns one live rent call, or ErrNotFound when it is not a live rent
// call of the lease.
func (Repository) Due(ctx context.Context, leaseID, billingDocumentID uuid.UUID) (Due, error) {
	t, err := tx(ctx)
	if err != nil {
		return Due{}, err
	}
	var out []Due
	if err := t.Raw(liveRentCalls+` AND d.lease_id = ? AND d.id = ?`, leaseID, billingDocumentID).Scan(&out).Error; err != nil {
		return Due{}, err
	}
	if len(out) == 0 {
		return Due{}, invariant.ErrNotFound
	}
	return out[0], nil
}

// Receipted reports whether a receipt (quittance or partial receipt) already
// covers one of the payment's matches.
func (Repository) Receipted(ctx context.Context, paymentID uuid.UUID) (bool, error) {
	t, err := tx(ctx)
	if err != nil {
		return false, err
	}
	var n int64
	err = t.Raw(`SELECT count(*) FROM billing_documents r JOIN payment_matches m ON m.payment_id = ?
		WHERE r.kind IN ('receipt', 'partial_receipt') AND jsonb_exists(r.input_snapshot -> 'match_ids', m.id::text)`, paymentID).Scan(&n).Error
	return n > 0, err
}
