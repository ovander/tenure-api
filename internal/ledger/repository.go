package ledger

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/ovander/tenure-api/internal/db"
)

// Repository reads the movements of a period in the caller's scoped
// transaction.
type Repository struct{}

func tx(ctx context.Context) (*gorm.DB, error) { return db.Tx(ctx) }

type paymentRow struct {
	ID          uuid.UUID `gorm:"column:id"`
	LeaseID     uuid.UUID `gorm:"column:lease_id"`
	PaidOn      time.Time `gorm:"column:paid_on"`
	AmountCents int64     `gorm:"column:amount_cents"`
}

type matchRow struct {
	PaymentID   uuid.UUID       `gorm:"column:payment_id"`
	AmountCents int64           `gorm:"column:amount_cents"`
	TotalCents  int64           `gorm:"column:total_cents"`
	Lines       json.RawMessage `gorm:"column:lines"`
}

// Payments returns the payments received in [from, to] and their matches
// with the lines of their rent calls.
func (Repository) Payments(ctx context.Context, from, to time.Time) ([]paymentRow, []matchRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, nil, err
	}
	var ps []paymentRow
	if err := t.Table("payments").Select("id, lease_id, paid_on, amount_cents").
		Where("paid_on BETWEEN ? AND ?", from, to).Order("paid_on, id").Scan(&ps).Error; err != nil {
		return nil, nil, err
	}
	var ms []matchRow
	err = t.Table("payment_matches pm").Select("pm.payment_id, pm.amount_cents, bd.total_cents, bd.lines").
		Joins("JOIN billing_documents bd ON bd.id = pm.billing_document_id").
		Joins("JOIN payments p ON p.id = pm.payment_id").
		Where("p.paid_on BETWEEN ? AND ?", from, to).Order("pm.created_at, pm.id").Scan(&ms).Error
	return ps, ms, err
}

type chargeRow struct {
	ID               uuid.UUID  `gorm:"column:id"`
	AssetID          uuid.UUID  `gorm:"column:asset_id"`
	Category         string     `gorm:"column:category"`
	PaidOn           time.Time  `gorm:"column:paid_on"`
	AmountCents      int64      `gorm:"column:amount_cents"`
	RecoverableCents int64      `gorm:"column:recoverable_cents"`
	DocumentID       *uuid.UUID `gorm:"column:document_id"`
}

// Charges returns the confirmed charges paid in [from, to].
func (Repository) Charges(ctx context.Context, from, to time.Time) ([]chargeRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []chargeRow
	err = t.Table("charges").Select("id, asset_id, category, paid_on, amount_cents, recoverable_cents, document_id").
		Where("status = 'confirmed' AND paid_on BETWEEN ? AND ?", from, to).Order("paid_on, id").Scan(&rows).Error
	return rows, err
}

type instalmentRow struct {
	LoanID         uuid.UUID  `gorm:"column:loan_id"`
	AssetID        uuid.UUID  `gorm:"column:asset_id"`
	Seq            int        `gorm:"column:seq"`
	DueOn          time.Time  `gorm:"column:due_on"`
	PrincipalCents int64      `gorm:"column:principal_cents"`
	InterestCents  int64      `gorm:"column:interest_cents"`
	InsuranceCents int64      `gorm:"column:insurance_cents"`
	DocumentID     *uuid.UUID `gorm:"column:document_id"`
}

// Instalments returns the loan instalments due in [from, to].
func (Repository) Instalments(ctx context.Context, from, to time.Time) ([]instalmentRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []instalmentRow
	err = t.Table("loan_instalments i").Joins("JOIN loans l ON l.id = i.loan_id").
		Select("i.loan_id, l.asset_id, i.seq, i.due_on, i.principal_cents, i.interest_cents, i.insurance_cents, l.document_id").
		Where("i.due_on BETWEEN ? AND ?", from, to).Order("i.due_on, i.loan_id, i.seq").Scan(&rows).Error
	return rows, err
}

type depositRow struct {
	ID          uuid.UUID `gorm:"column:id"`
	LeaseID     uuid.UUID `gorm:"column:lease_id"`
	ReceivedOn  time.Time `gorm:"column:received_on"`
	AmountCents int64     `gorm:"column:amount_cents"`
}

// Deposits returns the security deposits received in [from, to]: a deposit
// guarantee of a lease past its draft, received on its valid_from day or
// else the lease's start.
func (Repository) Deposits(ctx context.Context, from, to time.Time) ([]depositRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []depositRow
	err = t.Raw(`SELECT g.id, g.lease_id, coalesce(g.valid_from, l.start_on) AS received_on, g.amount_cents
		FROM guarantees g JOIN leases l ON l.id = g.lease_id
		WHERE g.kind = 'deposit' AND g.amount_cents > 0 AND l.state <> 'draft'
			AND coalesce(g.valid_from, l.start_on) BETWEEN ? AND ?
		ORDER BY received_on, g.id`, from, to).Scan(&rows).Error
	return rows, err
}

type settlementRow struct {
	ID         uuid.UUID       `gorm:"column:id"`
	LeaseID    uuid.UUID       `gorm:"column:lease_id"`
	SettledOn  time.Time       `gorm:"column:settled_on"`
	Figures    json.RawMessage `gorm:"column:figures"`
	DocumentID uuid.UUID       `gorm:"column:document_id"`
}

// Settlements returns the deposit statements settled in [from, to].
func (Repository) Settlements(ctx context.Context, from, to time.Time) ([]settlementRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []settlementRow
	err = t.Table("deposit_settlements").Select("id, lease_id, settled_on, figures, document_id").
		Where("settled_on BETWEEN ? AND ?", from, to).Order("settled_on, id").Scan(&rows).Error
	return rows, err
}

type improvementRow struct {
	ID                uuid.UUID  `gorm:"column:id"`
	AssetID           uuid.UUID  `gorm:"column:asset_id"`
	Treatment         string     `gorm:"column:treatment"`
	Title             string     `gorm:"column:title"`
	PaidOn            time.Time  `gorm:"column:paid_on"`
	InvoiceCents      int64      `gorm:"column:invoice_cents"`
	InvoiceDocumentID *uuid.UUID `gorm:"column:invoice_document_id"`
}

// Improvements returns the works invoiced, paid in [from, to] and
// capitalised: an improvement or construction that created no charge
// (Q-130).
func (Repository) Improvements(ctx context.Context, from, to time.Time) ([]improvementRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []improvementRow
	err = t.Table("incidents").Select("id, asset_id, treatment, title, paid_on, invoice_cents, invoice_document_id").
		Where("status = 'invoiced' AND charge_id IS NULL AND treatment IN ('improvement', 'construction') AND paid_on BETWEEN ? AND ?", from, to).
		Order("paid_on, id").Scan(&rows).Error
	return rows, err
}

type issuedRow struct {
	IssuedAt      time.Time       `gorm:"column:issued_at"`
	Summary       json.RawMessage `gorm:"column:summary"`
	DocumentID    *uuid.UUID      `gorm:"column:document_id"`
	CSVDocumentID *uuid.UUID      `gorm:"column:csv_document_id"`
}

// Issued returns the scope's issued tax summary of year, if any.
func (Repository) Issued(ctx context.Context, year int) (issuedRow, bool, error) {
	t, err := tx(ctx)
	if err != nil {
		return issuedRow{}, false, err
	}
	var rows []issuedRow
	err = t.Table("tax_summaries").Select("issued_at, summary, document_id, csv_document_id").
		Where("tax_year = ? AND state = 'issued'", year).Limit(1).Scan(&rows).Error
	if err != nil || len(rows) == 0 {
		return issuedRow{}, false, err
	}
	return rows[0], true, nil
}

type documentRow struct {
	ID           uuid.UUID `gorm:"column:id"`
	Class        string    `gorm:"column:class"`
	OriginalName string    `gorm:"column:original_name"`
	ObjectKey    string    `gorm:"column:object_key"`
	SHA256       string    `gorm:"column:sha256"`
	SizeBytes    int64     `gorm:"column:size_bytes"`
	CreatedAt    time.Time `gorm:"column:created_at"`
}

// Documents returns the scope's documents with ids, by id.
func (Repository) Documents(ctx context.Context, ids []uuid.UUID) ([]documentRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []documentRow
	if len(ids) == 0 {
		return rows, nil
	}
	err = t.Table("documents").Select("id, class, original_name, object_key, sha256, size_bytes, created_at").
		Where("id IN ?", ids).Order("id").Scan(&rows).Error
	return rows, err
}
