package charge

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/invariant"
)

type chargeRow struct {
	ID                     uuid.UUID  `gorm:"column:id;primaryKey"`
	OwnerID                uuid.UUID  `gorm:"column:owner_id"`
	AssetID                uuid.UUID  `gorm:"column:asset_id"`
	ChargedOn              time.Time  `gorm:"column:charged_on"`
	PaidOn                 *time.Time `gorm:"column:paid_on"`
	Category               string     `gorm:"column:category"`
	Label                  string     `gorm:"column:label"`
	AmountCents            int64      `gorm:"column:amount_cents"`
	RecoverableCents       int64      `gorm:"column:recoverable_cents"`
	SyndicRecoverableCents *int64     `gorm:"column:syndic_recoverable_cents"`
	ExerciseYear           int        `gorm:"column:exercise_year"`
	DocumentID             *uuid.UUID `gorm:"column:document_id"`
	Source                 string     `gorm:"column:source"`
	Status                 string     `gorm:"column:status"`
	ReconciliationID       *uuid.UUID `gorm:"column:reconciliation_id"`
	Version                int        `gorm:"column:version"`
	CreatedAt              time.Time  `gorm:"column:created_at"`
	CreatedBy              string     `gorm:"column:created_by"`
	UpdatedAt              time.Time  `gorm:"column:updated_at"`
	UpdatedBy              string     `gorm:"column:updated_by"`
}

func (chargeRow) TableName() string { return "charges" }

type reconRow struct {
	ID                     uuid.UUID       `gorm:"column:id;primaryKey"`
	OwnerID                uuid.UUID       `gorm:"column:owner_id"`
	AssetID                uuid.UUID       `gorm:"column:asset_id"`
	ExerciseYear           int             `gorm:"column:exercise_year"`
	ExerciseStart          time.Time       `gorm:"column:exercise_start"`
	ExerciseEnd            time.Time       `gorm:"column:exercise_end"`
	State                  string          `gorm:"column:state"`
	Totals                 json.RawMessage `gorm:"column:totals;type:jsonb"`
	ChargeIDs              json.RawMessage `gorm:"column:charge_ids;type:jsonb"`
	IssuedAt               *time.Time      `gorm:"column:issued_at"`
	CorrectsID             *uuid.UUID      `gorm:"column:corrects_id"`
	CancellationDocumentID *uuid.UUID      `gorm:"column:cancellation_document_id"`
	Version                int             `gorm:"column:version"`
	CreatedAt              time.Time       `gorm:"column:created_at"`
	CreatedBy              string          `gorm:"column:created_by"`
	UpdatedAt              time.Time       `gorm:"column:updated_at"`
	UpdatedBy              string          `gorm:"column:updated_by"`
}

func (reconRow) TableName() string { return "reconciliations" }

type lineRow struct {
	ID                     uuid.UUID       `gorm:"column:id;primaryKey"`
	OwnerID                uuid.UUID       `gorm:"column:owner_id"`
	ReconciliationID       uuid.UUID       `gorm:"column:reconciliation_id"`
	LeaseID                uuid.UUID       `gorm:"column:lease_id"`
	Days                   int             `gorm:"column:days"`
	AllocatedCents         int64           `gorm:"column:allocated_cents"`
	ProvisionsCents        int64           `gorm:"column:provisions_cents"`
	BalanceCents           int64           `gorm:"column:balance_cents"`
	Spread                 bool            `gorm:"column:spread"`
	Instalments            json.RawMessage `gorm:"column:instalments;type:jsonb"`
	ProposedProvisionCents int64           `gorm:"column:proposed_provision_cents"`
	NewProvisionCents      int64           `gorm:"column:new_provision_cents"`
	ApplyProvision         bool            `gorm:"column:apply_provision"`
	Settlement             string          `gorm:"column:settlement"`
	DocumentID             *uuid.UUID      `gorm:"column:document_id"`
	Number                 string          `gorm:"column:number"`
	Version                int             `gorm:"column:version"`
	CreatedAt              time.Time       `gorm:"column:created_at"`
	CreatedBy              string          `gorm:"column:created_by"`
	UpdatedAt              time.Time       `gorm:"column:updated_at"`
	UpdatedBy              string          `gorm:"column:updated_by"`
}

func (lineRow) TableName() string { return "reconciliation_lines" }

// PostingRow is a regularisation_postings row: what a next fee carries.
type PostingRow struct {
	ID                uuid.UUID  `gorm:"column:id;primaryKey"`
	OwnerID           uuid.UUID  `gorm:"column:owner_id"`
	LeaseID           uuid.UUID  `gorm:"column:lease_id"`
	ReconciliationID  uuid.UUID  `gorm:"column:reconciliation_id"`
	LineID            uuid.UUID  `gorm:"column:line_id"`
	ParentID          *uuid.UUID `gorm:"column:parent_id"`
	Reference         string     `gorm:"column:reference"`
	Instalment        int        `gorm:"column:instalment"`
	Of                int        `gorm:"column:of"`
	AmountCents       int64      `gorm:"column:amount_cents"`
	DueFrom           string     `gorm:"column:due_from"`
	BillingDocumentID *uuid.UUID `gorm:"column:billing_document_id"`
	VoidedAt          *time.Time `gorm:"column:voided_at"`
	CreatedAt         time.Time  `gorm:"column:created_at"`
	CreatedBy         string     `gorm:"column:created_by"`
}

// TableName maps PostingRow to regularisation_postings.
func (PostingRow) TableName() string { return "regularisation_postings" }

// Repository stores charges, reconciliations and postings in the caller's
// scoped transaction, and reads the leases, rent calls and matches of an
// exercise.
type Repository struct{}

func tx(ctx context.Context) (*gorm.DB, error) { return db.Tx(ctx) }

// Filter narrows a charge list.
type Filter struct {
	AssetID  *uuid.UUID
	Year     int
	Status   string
	Category string
}

// ListCharges returns a page of charges and the total.
func (Repository) ListCharges(ctx context.Context, f Filter, offset, limit int) ([]chargeRow, int, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, 0, err
	}
	q := t.Model(&chargeRow{})
	if f.AssetID != nil {
		q = q.Where("asset_id = ?", *f.AssetID)
	}
	if f.Year != 0 {
		q = q.Where("exercise_year = ?", f.Year)
	}
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.Category != "" {
		q = q.Where("category = ?", f.Category)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []chargeRow
	err = q.Order("charged_on, id").Offset(offset).Limit(limit).Find(&rows).Error
	return rows, int(total), err
}

// Charge returns one charge, locked for the transaction.
func (Repository) Charge(ctx context.Context, id uuid.UUID) (chargeRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return chargeRow{}, err
	}
	var rows []chargeRow
	if err := t.Where("id = ?", id).Clauses(clause.Locking{Strength: "UPDATE"}).Limit(1).Find(&rows).Error; err != nil {
		return chargeRow{}, err
	}
	if len(rows) == 0 {
		return chargeRow{}, invariant.ErrNotFound
	}
	return rows[0], nil
}

// InsertCharges creates charges.
func (Repository) InsertCharges(ctx context.Context, rows []chargeRow) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	return t.Create(&rows).Error
}

// UpdateCharge updates a charge when version matches.
func (Repository) UpdateCharge(ctx context.Context, c chargeRow, version int) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	res := t.Model(&chargeRow{}).Where("id = ? AND version = ?", c.ID, version).Updates(map[string]any{
		"charged_on": c.ChargedOn, "paid_on": c.PaidOn, "category": c.Category, "label": c.Label, "amount_cents": c.AmountCents,
		"recoverable_cents": c.RecoverableCents, "exercise_year": c.ExerciseYear, "document_id": c.DocumentID, "status": c.Status,
		"version": version + 1, "updated_at": c.UpdatedAt, "updated_by": c.UpdatedBy,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return invariant.ErrStale
	}
	return nil
}

// ExerciseCharges returns the charges of an asset dated in [start, end].
func (Repository) ExerciseCharges(ctx context.Context, asset uuid.UUID, start, end time.Time) ([]chargeRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []chargeRow
	err = t.Where("asset_id = ? AND charged_on BETWEEN ? AND ?", asset, start, end).Order("charged_on, id").Find(&rows).Error
	return rows, err
}

// FreezeCharges links charges to the reconciliation that used them, or
// unlinks them (recon nil) when it is cancelled.
func (Repository) FreezeCharges(ctx context.Context, ids []uuid.UUID, recon *uuid.UUID, at time.Time, by string) error {
	if len(ids) == 0 {
		return nil
	}
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	return t.Model(&chargeRow{}).Where("id IN ?", ids).Updates(map[string]any{
		"reconciliation_id": recon, "version": gorm.Expr("version + 1"), "updated_at": at, "updated_by": by}).Error
}

// ReconFilter narrows a reconciliation list.
type ReconFilter struct {
	AssetID *uuid.UUID
	Year    int
	LeaseID *uuid.UUID
}

// ListRecons returns a page of reconciliations and the total.
func (Repository) ListRecons(ctx context.Context, f ReconFilter, offset, limit int) ([]reconRow, int, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, 0, err
	}
	q := t.Model(&reconRow{})
	if f.AssetID != nil {
		q = q.Where("asset_id = ?", *f.AssetID)
	}
	if f.Year != 0 {
		q = q.Where("exercise_year = ?", f.Year)
	}
	if f.LeaseID != nil {
		q = q.Where("id IN (SELECT reconciliation_id FROM reconciliation_lines WHERE lease_id = ?)", *f.LeaseID)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []reconRow
	err = q.Order("exercise_start DESC, created_at DESC").Offset(offset).Limit(limit).Find(&rows).Error
	return rows, int(total), err
}

// Recon returns one reconciliation, locked when forUpdate.
func (Repository) Recon(ctx context.Context, id uuid.UUID, forUpdate bool) (reconRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return reconRow{}, err
	}
	q := t.Where("id = ?", id)
	if forUpdate {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var rows []reconRow
	if err := q.Limit(1).Find(&rows).Error; err != nil {
		return reconRow{}, err
	}
	if len(rows) == 0 {
		return reconRow{}, invariant.ErrNotFound
	}
	return rows[0], nil
}

// LiveRecon returns the reconciliation of an asset and exercise year that is
// not cancelled.
func (Repository) LiveRecon(ctx context.Context, asset uuid.UUID, year int) (reconRow, bool, error) {
	t, err := tx(ctx)
	if err != nil {
		return reconRow{}, false, err
	}
	var rows []reconRow
	err = t.Where("asset_id = ? AND exercise_year = ? AND state <> 'cancelled'", asset, year).
		Clauses(clause.Locking{Strength: "UPDATE"}).Limit(1).Find(&rows).Error
	if err != nil || len(rows) == 0 {
		return reconRow{}, false, err
	}
	return rows[0], true, nil
}

// InsertRecon creates a reconciliation.
func (Repository) InsertRecon(ctx context.Context, r reconRow) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	return t.Create(&r).Error
}

// UpdateRecon records a transition when version matches.
func (Repository) UpdateRecon(ctx context.Context, r reconRow, version int) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	res := t.Model(&reconRow{}).Where("id = ? AND version = ?", r.ID, version).Updates(map[string]any{
		"state": r.State, "totals": r.Totals, "charge_ids": r.ChargeIDs, "issued_at": r.IssuedAt,
		"cancellation_document_id": r.CancellationDocumentID, "version": version + 1, "updated_at": r.UpdatedAt, "updated_by": r.UpdatedBy,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return invariant.ErrStale
	}
	return nil
}

// Lines returns the lines of a reconciliation.
func (Repository) Lines(ctx context.Context, recon uuid.UUID) ([]lineRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []lineRow
	err = t.Where("reconciliation_id = ?", recon).Order("created_at, lease_id").Find(&rows).Error
	return rows, err
}

// ReplaceLines replaces the lines of an allocated reconciliation.
func (Repository) ReplaceLines(ctx context.Context, recon uuid.UUID, rows []lineRow) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	if err := t.Where("reconciliation_id = ?", recon).Delete(&lineRow{}).Error; err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	return t.Create(&rows).Error
}

// UpdateLine updates a line when version matches.
func (Repository) UpdateLine(ctx context.Context, l lineRow, version int) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	res := t.Model(&lineRow{}).Where("id = ? AND version = ?", l.ID, version).Updates(map[string]any{
		"new_provision_cents": l.NewProvisionCents, "apply_provision": l.ApplyProvision, "settlement": l.Settlement,
		"document_id": l.DocumentID, "number": l.Number, "version": version + 1, "updated_at": l.UpdatedAt, "updated_by": l.UpdatedBy,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return invariant.ErrStale
	}
	return nil
}

// LeasesOnAsset returns the leases, other than drafts, that cover the asset on
// a day of [start, end].
func (Repository) LeasesOnAsset(ctx context.Context, asset uuid.UUID, start, end time.Time) ([]uuid.UUID, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var ids []uuid.UUID
	err = t.Table("leases l").Joins("JOIN lease_assets la ON la.lease_id = l.id").
		Where("la.asset_id = ? AND l.state <> 'draft' AND l.start_on <= ? AND (l.end_on IS NULL OR l.end_on >= ?)", asset, end, start).
		Order("l.start_on").Pluck("l.id", &ids).Error
	return ids, err
}

// PaidCalls returns, for each live rent call of the lease in [from, to]
// (periods YYYY-MM), its total, its provision lines and what payments
// matched to it.
func (Repository) PaidCalls(ctx context.Context, lease uuid.UUID, from, to string) ([]PaidCall, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var out []PaidCall
	err = t.Raw(`SELECT bd.total_cents,
			COALESCE((SELECT sum((l->>'amount_cents')::bigint) FROM jsonb_array_elements(bd.lines) l
				WHERE l->>'kind' = 'provision'), 0) AS provision_cents,
			COALESCE((SELECT sum(pm.amount_cents) FROM payment_matches pm WHERE pm.billing_document_id = bd.id), 0) AS matched_cents
		FROM billing_documents bd
		WHERE bd.lease_id = ? AND bd.kind = 'rent_call' AND bd.period BETWEEN ? AND ?
			AND NOT EXISTS (SELECT 1 FROM billing_documents c WHERE c.cancels_id = bd.id)
		ORDER BY bd.period`, lease, from, to).Scan(&out).Error
	return out, err
}

// InsertPostings records postings.
func (Repository) InsertPostings(ctx context.Context, rows []PostingRow) error {
	if len(rows) == 0 {
		return nil
	}
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	return t.Create(&rows).Error
}

// Postings returns the live postings of a reconciliation, with whether the
// rent call that carried each one is still live.
func (Repository) Postings(ctx context.Context, recon uuid.UUID) ([]PostingState, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var out []PostingState
	err = t.Raw(`SELECT p.*, (p.billing_document_id IS NOT NULL AND NOT EXISTS (
			SELECT 1 FROM billing_documents c WHERE c.cancels_id = p.billing_document_id)) AS carried
		FROM regularisation_postings p WHERE p.reconciliation_id = ? AND p.voided_at IS NULL
		ORDER BY p.due_from, p.created_at`, recon).Scan(&out).Error
	return out, err
}

// PostingState is a posting and whether a live rent call carried it.
type PostingState struct {
	PostingRow
	Carried bool `gorm:"column:carried"`
}

// VoidPostings voids the postings with ids.
func (Repository) VoidPostings(ctx context.Context, ids []uuid.UUID, at time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	return t.Model(&PostingRow{}).Where("id IN ?", ids).Update("voided_at", at).Error
}
