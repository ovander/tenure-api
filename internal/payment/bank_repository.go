package payment

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm/clause"

	"github.com/ovander/tenure-api/internal/invariant"
)

type accountRow struct {
	ID        uuid.UUID `gorm:"column:id;primaryKey"`
	OwnerID   uuid.UUID `gorm:"column:owner_id"`
	Label     string    `gorm:"column:label"`
	IBANLast4 string    `gorm:"column:iban_last4"`
	CreatedAt time.Time `gorm:"column:created_at"`
	CreatedBy string    `gorm:"column:created_by"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
	UpdatedBy string    `gorm:"column:updated_by"`
	Version   int       `gorm:"column:version"`
}

func (accountRow) TableName() string { return "bank_accounts" }

type lineRow struct {
	ID               uuid.UUID `gorm:"column:id;primaryKey"`
	OwnerID          uuid.UUID `gorm:"column:owner_id"`
	AccountID        uuid.UUID `gorm:"column:account_id"`
	BookedOn         time.Time `gorm:"column:booked_on;type:date"`
	AmountCents      int64     `gorm:"column:amount_cents"`
	Label            string    `gorm:"column:label"`
	Reference        string    `gorm:"column:reference"`
	LineKey          string    `gorm:"column:line_key"`
	ImportDocumentID uuid.UUID `gorm:"column:import_document_id"`
	Status           string    `gorm:"column:status"`
	Note             string    `gorm:"column:note"`
	CreatedAt        time.Time `gorm:"column:created_at"`
	CreatedBy        string    `gorm:"column:created_by"`
	UpdatedAt        time.Time `gorm:"column:updated_at"`
	UpdatedBy        string    `gorm:"column:updated_by"`
	Version          int       `gorm:"column:version"`
}

func (lineRow) TableName() string { return "bank_lines" }

// Accounts returns the owner's bank accounts, by label.
func (Repository) Accounts(ctx context.Context) ([]accountRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []accountRow
	err = t.Order("label, created_at").Find(&rows).Error
	return rows, err
}

// Account returns one bank account.
func (Repository) Account(ctx context.Context, id uuid.UUID) (accountRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return accountRow{}, err
	}
	var rows []accountRow
	if err := t.Where("id = ?", id).Limit(1).Find(&rows).Error; err != nil {
		return accountRow{}, err
	}
	if len(rows) == 0 {
		return accountRow{}, invariant.ErrNotFound
	}
	return rows[0], nil
}

// InsertAccount records a bank account.
func (Repository) InsertAccount(ctx context.Context, r accountRow) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	return t.Create(&r).Error
}

// LineKeys returns which of the keys an account already holds.
func (Repository) LineKeys(ctx context.Context, account uuid.UUID, keys []string) (map[string]bool, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	if len(keys) == 0 {
		return out, nil
	}
	var got []string
	if err := t.Model(&lineRow{}).Where("account_id = ? AND line_key IN ?", account, keys).Pluck("line_key", &got).Error; err != nil {
		return nil, err
	}
	for _, k := range got {
		out[k] = true
	}
	return out, nil
}

// InsertLines records bank lines.
func (Repository) InsertLines(ctx context.Context, rows []lineRow) error {
	if len(rows) == 0 {
		return nil
	}
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	return t.CreateInBatches(rows, 200).Error
}

// LineFilter narrows the bank lines listed.
type LineFilter struct {
	AccountID *uuid.UUID
	Status    string
}

// Lines lists bank lines, the latest booked first.
func (Repository) Lines(ctx context.Context, f LineFilter, offset, limit int) ([]lineRow, int, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, 0, err
	}
	q := t.Model(&lineRow{})
	if f.AccountID != nil {
		q = q.Where("account_id = ?", *f.AccountID)
	}
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []lineRow
	err = q.Order("booked_on DESC, created_at DESC, id").Offset(offset).Limit(limit).Find(&rows).Error
	return rows, int(total), err
}

// Line returns one bank line, locked for update.
func (Repository) Line(ctx context.Context, id uuid.UUID) (lineRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return lineRow{}, err
	}
	var rows []lineRow
	if err := t.Where("id = ?", id).Clauses(clause.Locking{Strength: "UPDATE"}).Limit(1).Find(&rows).Error; err != nil {
		return lineRow{}, err
	}
	if len(rows) == 0 {
		return lineRow{}, invariant.ErrNotFound
	}
	return rows[0], nil
}

// SetLineStatus sets a bank line's status and note when its version is the
// one read, bumping it.
func (Repository) SetLineStatus(ctx context.Context, id uuid.UUID, version int, status, note string, at time.Time, by string) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	res := t.Model(&lineRow{}).Where("id = ? AND version = ?", id, version).
		Updates(map[string]any{"status": status, "note": note, "version": version + 1, "updated_at": at, "updated_by": by})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return invariant.ErrStale
	}
	return nil
}
