package loan

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/invariant"
)

// Repository runs the loan module's SQL in the request's scoped transaction.
type Repository struct{}

func tx(ctx context.Context) (*gorm.DB, error) { return db.Tx(ctx) }

type loanRow struct {
	ID             uuid.UUID  `gorm:"column:id;primaryKey"`
	OwnerID        uuid.UUID  `gorm:"column:owner_id"`
	AssetID        uuid.UUID  `gorm:"column:asset_id"`
	Label          string     `gorm:"column:label"`
	Lender         string     `gorm:"column:lender"`
	PrincipalCents int64      `gorm:"column:principal_cents"`
	AnnualRatePPM  *int64     `gorm:"column:annual_rate_ppm"`
	TermMonths     int        `gorm:"column:term_months"`
	FirstDueOn     time.Time  `gorm:"column:first_due_on;type:date"`
	InsuranceCents int64      `gorm:"column:insurance_cents"`
	Source         string     `gorm:"column:source"`
	DocumentID     *uuid.UUID `gorm:"column:document_id"`
	CreatedAt      time.Time  `gorm:"column:created_at"`
	CreatedBy      string     `gorm:"column:created_by"`
	UpdatedAt      time.Time  `gorm:"column:updated_at"`
	UpdatedBy      string     `gorm:"column:updated_by"`
	Version        int        `gorm:"column:version"`
}

func (loanRow) TableName() string { return "loans" }

type instalmentRow struct {
	ID             uuid.UUID `gorm:"column:id;primaryKey"`
	OwnerID        uuid.UUID `gorm:"column:owner_id"`
	LoanID         uuid.UUID `gorm:"column:loan_id"`
	Seq            int       `gorm:"column:seq"`
	DueOn          time.Time `gorm:"column:due_on;type:date"`
	PrincipalCents int64     `gorm:"column:principal_cents"`
	InterestCents  int64     `gorm:"column:interest_cents"`
	InsuranceCents int64     `gorm:"column:insurance_cents"`
	RemainingCents int64     `gorm:"column:remaining_cents"`
}

func (instalmentRow) TableName() string { return "loan_instalments" }

// Loans lists the scope's loans, of one asset when asset is set, by first
// due day.
func (Repository) Loans(ctx context.Context, asset *uuid.UUID) ([]loanRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	q := t.Model(&loanRow{})
	if asset != nil {
		q = q.Where("asset_id = ?", *asset)
	}
	var rows []loanRow
	err = q.Order("first_due_on, created_at, id").Find(&rows).Error
	return rows, err
}

// Loan returns one loan, locked for update when forUpdate.
func (Repository) Loan(ctx context.Context, id uuid.UUID, forUpdate bool) (loanRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return loanRow{}, err
	}
	q := t.Where("id = ?", id)
	if forUpdate {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var rows []loanRow
	if err := q.Limit(1).Find(&rows).Error; err != nil {
		return loanRow{}, err
	}
	if len(rows) == 0 {
		return loanRow{}, invariant.ErrNotFound
	}
	return rows[0], nil
}

// Instalments returns the instalments of the loans, by loan and sequence.
func (Repository) Instalments(ctx context.Context, loans []uuid.UUID) ([]instalmentRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []instalmentRow
	if len(loans) == 0 {
		return rows, nil
	}
	err = t.Where("loan_id IN ?", loans).Order("loan_id, seq").Find(&rows).Error
	return rows, err
}

// InsertLoan records a loan.
func (Repository) InsertLoan(ctx context.Context, r loanRow) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	return t.Create(&r).Error
}

// UpdateLoan replaces a loan's terms when its version is the one read,
// bumping it.
func (Repository) UpdateLoan(ctx context.Context, r loanRow, version int) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	res := t.Model(&loanRow{}).Where("id = ? AND version = ?", r.ID, version).Updates(map[string]any{
		"asset_id": r.AssetID, "label": r.Label, "lender": r.Lender, "principal_cents": r.PrincipalCents,
		"annual_rate_ppm": r.AnnualRatePPM, "term_months": r.TermMonths, "first_due_on": r.FirstDueOn,
		"insurance_cents": r.InsuranceCents, "source": r.Source, "document_id": r.DocumentID,
		"updated_at": r.UpdatedAt, "updated_by": r.UpdatedBy, "version": version + 1,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return invariant.ErrStale
	}
	return nil
}

// ReplaceInstalments deletes a loan's instalments and records rows instead.
func (Repository) ReplaceInstalments(ctx context.Context, loan uuid.UUID, rows []instalmentRow) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	if err := t.Where("loan_id = ?", loan).Delete(&instalmentRow{}).Error; err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	return t.CreateInBatches(rows, 200).Error
}
