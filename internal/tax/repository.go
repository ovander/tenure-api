package tax

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/money"
)

type summaryRow struct {
	ID            uuid.UUID       `gorm:"column:id;primaryKey"`
	OwnerID       uuid.UUID       `gorm:"column:owner_id"`
	TaxYear       int             `gorm:"column:tax_year"`
	State         string          `gorm:"column:state"`
	Regime        *string         `gorm:"column:regime"`
	Summary       json.RawMessage `gorm:"column:summary;type:jsonb"`
	Inputs        json.RawMessage `gorm:"column:inputs;type:jsonb"`
	DeficitsAfter json.RawMessage `gorm:"column:deficits_after;type:jsonb"`
	ComputedAt    time.Time       `gorm:"column:computed_at"`
	IssuedAt      *time.Time      `gorm:"column:issued_at"`
	DocumentID    *uuid.UUID      `gorm:"column:document_id"`
	CSVDocumentID *uuid.UUID      `gorm:"column:csv_document_id"`
	Version       int             `gorm:"column:version"`
	CreatedAt     time.Time       `gorm:"column:created_at"`
	CreatedBy     string          `gorm:"column:created_by"`
	UpdatedAt     time.Time       `gorm:"column:updated_at"`
	UpdatedBy     string          `gorm:"column:updated_by"`
}

func (summaryRow) TableName() string { return "tax_summaries" }

// Repository reads the inputs of a tax year and stores the summaries, in the
// caller's scoped transaction.
type Repository struct{}

func tx(ctx context.Context) (*gorm.DB, error) { return db.Tx(ctx) }

// paymentRow is a payment of the year.
type paymentRow struct {
	ID          uuid.UUID `gorm:"column:id"`
	LeaseID     uuid.UUID `gorm:"column:lease_id"`
	PaidOn      time.Time `gorm:"column:paid_on"`
	AmountCents int64     `gorm:"column:amount_cents"`
}

// matchRow is one match of a payment with the lines of its rent call.
type matchRow struct {
	PaymentID         uuid.UUID       `gorm:"column:payment_id"`
	BillingDocumentID uuid.UUID       `gorm:"column:billing_document_id"`
	AmountCents       int64           `gorm:"column:amount_cents"`
	TotalCents        int64           `gorm:"column:total_cents"`
	Lines             json.RawMessage `gorm:"column:lines"`
}

// Payments returns the payments received in [from, to] and their matches.
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
	err = t.Table("payment_matches pm").Select("pm.payment_id, pm.billing_document_id, pm.amount_cents, bd.total_cents, bd.lines").
		Joins("JOIN billing_documents bd ON bd.id = pm.billing_document_id").
		Joins("JOIN payments p ON p.id = pm.payment_id").
		Where("p.paid_on BETWEEN ? AND ?", from, to).Order("pm.created_at, pm.id").Scan(&ms).Error
	return ps, ms, err
}

// PrincipalAssets returns each lease's principal asset: its first dwelling,
// or its first asset when it lets only a box (lease.Lease.PrincipalAsset).
func (Repository) PrincipalAssets(ctx context.Context) (map[uuid.UUID]uuid.UUID, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		LeaseID uuid.UUID `gorm:"column:lease_id"`
		AssetID uuid.UUID `gorm:"column:asset_id"`
	}
	err = t.Raw(`SELECT DISTINCT ON (la.lease_id) la.lease_id, la.asset_id FROM lease_assets la
		JOIN assets a ON a.id = la.asset_id ORDER BY la.lease_id, (a.kind = 'box'), la.asset_id`).Scan(&rows).Error
	out := map[uuid.UUID]uuid.UUID{}
	for _, r := range rows {
		out[r.LeaseID] = r.AssetID
	}
	return out, err
}

// chargeRow is a charge paid in the year.
type chargeRow struct {
	ID               uuid.UUID  `gorm:"column:id"`
	AssetID          uuid.UUID  `gorm:"column:asset_id"`
	Category         string     `gorm:"column:category"`
	AmountCents      int64      `gorm:"column:amount_cents"`
	RecoverableCents int64      `gorm:"column:recoverable_cents"`
	ChargedOn        time.Time  `gorm:"column:charged_on"`
	PaidOn           *time.Time `gorm:"column:paid_on"`
	DocumentID       *uuid.UUID `gorm:"column:document_id"`
	Status           string     `gorm:"column:status"`
}

// Charges returns the charges paid in [from, to] (cash basis, RENT-SPEC-004).
func (Repository) Charges(ctx context.Context, from, to time.Time) ([]chargeRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []chargeRow
	err = t.Table("charges").Select("id, asset_id, category, amount_cents, recoverable_cents, charged_on, paid_on, document_id, status").
		Where("paid_on BETWEEN ? AND ?", from, to).Order("paid_on, id").Scan(&rows).Error
	return rows, err
}

// instalmentRow is an instalment of a recorded loan due in the year.
type instalmentRow struct {
	LoanID         uuid.UUID  `gorm:"column:loan_id"`
	AssetID        uuid.UUID  `gorm:"column:asset_id"`
	Seq            int        `gorm:"column:seq"`
	DueOn          time.Time  `gorm:"column:due_on"`
	InterestCents  int64      `gorm:"column:interest_cents"`
	InsuranceCents int64      `gorm:"column:insurance_cents"`
	DocumentID     *uuid.UUID `gorm:"column:document_id"`
}

// LoanInstalments returns the instalments of the scope's loans due in
// [from, to] (RENT-SPEC-014, Q-126).
func (Repository) LoanInstalments(ctx context.Context, from, to time.Time) ([]instalmentRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []instalmentRow
	err = t.Table("loan_instalments i").Joins("JOIN loans l ON l.id = i.loan_id").
		Select("i.loan_id, l.asset_id, i.seq, i.due_on, i.interest_cents, i.insurance_cents, l.document_id").
		Where("i.due_on BETWEEN ? AND ? AND l.kind = 'asset'", from, to).Order("i.due_on, i.loan_id, i.seq").Scan(&rows).Error
	return rows, err
}

// PartsLoans returns, per loan for SCI parts (Q-133), the interest and
// insurance of its instalments due in [from, to].
func (Repository) PartsLoans(ctx context.Context, from, to time.Time) ([]PartsLoan, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		LoanID         uuid.UUID
		Label          string
		SCIName        string
		InterestCents  int64
		InsuranceCents int64
		DocumentID     *uuid.UUID
	}
	err = t.Table("loan_instalments i").Joins("JOIN loans l ON l.id = i.loan_id").
		Select("i.loan_id, l.label, l.sci_name, sum(i.interest_cents) AS interest_cents, sum(i.insurance_cents) AS insurance_cents, l.document_id").
		Where("i.due_on BETWEEN ? AND ? AND l.kind = 'parts'", from, to).
		Group("i.loan_id, l.label, l.sci_name, l.document_id").Order("l.sci_name, i.loan_id").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]PartsLoan, 0, len(rows))
	for _, r := range rows {
		pl := PartsLoan{LoanID: r.LoanID.String(), Label: r.Label, SCIName: r.SCIName,
			InterestCents: money.Cents(r.InterestCents), InsuranceCents: money.Cents(r.InsuranceCents)}
		if r.DocumentID != nil {
			pl.DocumentID = r.DocumentID.String()
		}
		out = append(out, pl)
	}
	return out, nil
}

// assetRow is an asset with its furnished flag for the year.
type assetRow struct {
	ID        uuid.UUID `gorm:"column:id"`
	Furnished bool      `gorm:"column:furnished"`
}

// shareRow is one ownership share in basis points.
type shareRow struct {
	AssetID uuid.UUID `gorm:"column:asset_id"`
	OwnerID uuid.UUID `gorm:"column:owner_id"`
	BP      int64     `gorm:"column:bp"`
}

// Assets returns the scope's assets, furnished when the asset is or a
// furnished or mobility lease covered it during [from, to] (Q-88), and their
// shares.
func (Repository) Assets(ctx context.Context, from, to time.Time) ([]assetRow, []shareRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, nil, err
	}
	var as []assetRow
	err = t.Raw(`SELECT a.id, a.furnished OR EXISTS (
			SELECT 1 FROM lease_assets la JOIN leases l ON l.id = la.lease_id
			WHERE la.asset_id = a.id AND l.kind IN ('furnished', 'mobility') AND l.state <> 'draft'
				AND l.start_on <= ? AND (l.end_on IS NULL OR l.end_on >= ?)) AS furnished
		FROM assets a ORDER BY a.id`, to, from).Scan(&as).Error
	if err != nil {
		return nil, nil, err
	}
	var sh []shareRow
	err = t.Raw(`SELECT asset_id, owner_id, (share_pct * 100)::bigint AS bp FROM asset_shares ORDER BY asset_id, owner_id`).Scan(&sh).Error
	return as, sh, err
}

// Summary returns the summary of the scope's owner for year, locked when
// forUpdate.
func (Repository) Summary(ctx context.Context, year int, forUpdate bool) (summaryRow, bool, error) {
	t, err := tx(ctx)
	if err != nil {
		return summaryRow{}, false, err
	}
	q := t.Where("tax_year = ?", year)
	if forUpdate {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var rows []summaryRow
	if err := q.Limit(1).Find(&rows).Error; err != nil || len(rows) == 0 {
		return summaryRow{}, false, err
	}
	return rows[0], true, nil
}

// SummaryByID returns one summary of the scope.
func (Repository) SummaryByID(ctx context.Context, id uuid.UUID) (summaryRow, error) {
	t, err := tx(ctx)
	if err != nil {
		return summaryRow{}, err
	}
	var rows []summaryRow
	if err := t.Where("id = ?", id).Limit(1).Find(&rows).Error; err != nil {
		return summaryRow{}, err
	}
	if len(rows) == 0 {
		return summaryRow{}, invariant.ErrNotFound
	}
	return rows[0], nil
}

// List returns a page of the scope's summaries, latest year first.
func (Repository) List(ctx context.Context, year, offset, limit int) ([]summaryRow, int, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, 0, err
	}
	q := t.Model(&summaryRow{})
	if year != 0 {
		q = q.Where("tax_year = ?", year)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []summaryRow
	err = q.Order("tax_year DESC").Offset(offset).Limit(limit).Find(&rows).Error
	return rows, int(total), err
}

// PriorIssued returns the latest issued summary of a year before year.
func (Repository) PriorIssued(ctx context.Context, year int) (summaryRow, bool, error) {
	t, err := tx(ctx)
	if err != nil {
		return summaryRow{}, false, err
	}
	var rows []summaryRow
	err = t.Where("tax_year < ? AND state = 'issued'", year).Order("tax_year DESC").Limit(1).Find(&rows).Error
	if err != nil || len(rows) == 0 {
		return summaryRow{}, false, err
	}
	return rows[0], true, nil
}

// Insert creates a summary.
func (Repository) Insert(ctx context.Context, r summaryRow) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	return t.Create(&r).Error
}

// Update replaces a draft summary (the trigger refuses an issued one).
func (Repository) Update(ctx context.Context, r summaryRow, version int) error {
	t, err := tx(ctx)
	if err != nil {
		return err
	}
	res := t.Model(&summaryRow{}).Where("id = ? AND version = ?", r.ID, version).Updates(map[string]any{
		"state": r.State, "regime": r.Regime, "summary": r.Summary, "inputs": r.Inputs, "deficits_after": r.DeficitsAfter,
		"computed_at": r.ComputedAt, "issued_at": r.IssuedAt, "document_id": r.DocumentID, "csv_document_id": r.CSVDocumentID,
		"version": version + 1, "updated_at": r.UpdatedAt, "updated_by": r.UpdatedBy,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return invariant.ErrStale
	}
	return nil
}

// Owner returns the legal name and address of an owner of the scope.
func (Repository) Owner(ctx context.Context, id uuid.UUID) (name, address string, err error) {
	t, err := tx(ctx)
	if err != nil {
		return "", "", err
	}
	var row struct {
		LegalName string `gorm:"column:legal_name"`
		Address   string `gorm:"column:address"`
	}
	err = t.Table("owners").Select("legal_name, address").Where("id = ?", id).Limit(1).Scan(&row).Error
	return row.LegalName, row.Address, err
}

// SupportingDoc is a supporting document of a charge, listed for audit.
type SupportingDoc struct {
	ID           uuid.UUID `gorm:"column:id" json:"id"`
	OriginalName string    `gorm:"column:original_name" json:"original_name"`
	SHA256       string    `gorm:"column:sha256" json:"sha256"`
}

// Documents returns the documents with ids.
func (Repository) Documents(ctx context.Context, ids []uuid.UUID) ([]SupportingDoc, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var out []SupportingDoc
	err = t.Table("documents").Select("id, original_name, sha256").Where("id IN ?", ids).Order("id").Scan(&out).Error
	return out, err
}

// AssetLabels returns each asset's address and lot.
func (Repository) AssetLabels(ctx context.Context) (map[uuid.UUID]string, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		ID      uuid.UUID `gorm:"column:id"`
		Address string    `gorm:"column:address"`
		Lot     string    `gorm:"column:lot"`
	}
	err = t.Table("assets").Select("id, address, lot").Scan(&rows).Error
	out := map[uuid.UUID]string{}
	for _, r := range rows {
		out[r.ID] = r.Address
		if r.Lot != "" {
			out[r.ID] += ", lot " + r.Lot
		}
	}
	return out, err
}

// SCIShares returns the shares of SCIs' issued 2072s of a year that name the
// scope's person (Q-133): the person's own Socrate subject on the
// allocation, read through the rows' person policy. An SCI's scope has no
// person, so it reads none.
func (Repository) SCIShares(ctx context.Context, owner uuid.UUID, year int) ([]SCIShare, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		DeclarationID      uuid.UUID
		SCIName            string
		Parts              int
		TotalParts         int
		IncomeCents        int64
		InterestCents      int64
		OtherExpensesCents int64
		NetCents           int64
	}
	err = t.Table("sci_allocations").
		Select("declaration_id, sci_name, parts, total_parts, income_cents, interest_cents, other_expenses_cents, net_cents").
		Where("tax_year = ? AND person_sub = (SELECT socrate_sub FROM owners WHERE id = ? AND kind = 'person')", year, owner).
		Order("sci_name, declaration_id").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]SCIShare, 0, len(rows))
	for _, r := range rows {
		out = append(out, SCIShare{DeclarationID: r.DeclarationID.String(), SCIName: r.SCIName, Parts: r.Parts, TotalParts: r.TotalParts,
			IncomeCents: money.Cents(r.IncomeCents), InterestCents: money.Cents(r.InterestCents),
			OtherExpensesCents: money.Cents(r.OtherExpensesCents), NetCents: money.Cents(r.NetCents)})
	}
	return out, nil
}

// SCIsPending returns the SCIs the scope's person is a linked associate of
// that have no 2072 of the year naming them.
func (Repository) SCIsPending(ctx context.Context, owner uuid.UUID, year int) ([]uuid.UUID, error) {
	t, err := tx(ctx)
	if err != nil {
		return nil, err
	}
	var ids []uuid.UUID
	err = t.Table("sci_associates a").Distinct("a.owner_id").
		Where("a.person_sub = (SELECT socrate_sub FROM owners WHERE id = ? AND kind = 'person')", owner).
		Where("NOT EXISTS (SELECT 1 FROM sci_allocations al WHERE al.owner_id = a.owner_id AND al.associate_id = a.id AND al.tax_year = ?)", year).
		Order("a.owner_id").Pluck("a.owner_id", &ids).Error
	return ids, err
}
