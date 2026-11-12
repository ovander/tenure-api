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
	ID               uuid.UUID `gorm:"column:id"`
	AssetID          uuid.UUID `gorm:"column:asset_id"`
	Category         string    `gorm:"column:category"`
	PaidOn           time.Time `gorm:"column:paid_on"`
	AmountCents      int64     `gorm:"column:amount_cents"`
	RecoverableCents int64     `gorm:"column:recoverable_cents"`
}

// Charges returns the confirmed charges paid in [from, to].
func (Repository) Charges(ctx context.Context, from, to time.Time) ([]chargeRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []chargeRow
	err = t.Table("charges").Select("id, asset_id, category, paid_on, amount_cents, recoverable_cents").
		Where("status = 'confirmed' AND paid_on BETWEEN ? AND ?", from, to).Order("paid_on, id").Scan(&rows).Error
	return rows, err
}

type instalmentRow struct {
	LoanID         uuid.UUID `gorm:"column:loan_id"`
	Seq            int       `gorm:"column:seq"`
	DueOn          time.Time `gorm:"column:due_on"`
	PrincipalCents int64     `gorm:"column:principal_cents"`
	InterestCents  int64     `gorm:"column:interest_cents"`
	InsuranceCents int64     `gorm:"column:insurance_cents"`
}

// Instalments returns the loan instalments due in [from, to].
func (Repository) Instalments(ctx context.Context, from, to time.Time) ([]instalmentRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []instalmentRow
	err = t.Table("loan_instalments").Select("loan_id, seq, due_on, principal_cents, interest_cents, insurance_cents").
		Where("due_on BETWEEN ? AND ?", from, to).Order("due_on, loan_id, seq").Scan(&rows).Error
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
	ID        uuid.UUID       `gorm:"column:id"`
	LeaseID   uuid.UUID       `gorm:"column:lease_id"`
	SettledOn time.Time       `gorm:"column:settled_on"`
	Figures   json.RawMessage `gorm:"column:figures"`
}

// Settlements returns the deposit statements settled in [from, to].
func (Repository) Settlements(ctx context.Context, from, to time.Time) ([]settlementRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []settlementRow
	err = t.Table("deposit_settlements").Select("id, lease_id, settled_on, figures").
		Where("settled_on BETWEEN ? AND ?", from, to).Order("settled_on, id").Scan(&rows).Error
	return rows, err
}

// Closed reports whether the scope's tax summary of year is issued.
func (Repository) Closed(ctx context.Context, year int) (bool, error) {
	t, err := tx(ctx)
	if err != nil {
		return false, err
	}
	var n int64
	err = t.Table("tax_summaries").Where("tax_year = ? AND state = 'issued'", year).Count(&n).Error
	return n > 0, err
}
