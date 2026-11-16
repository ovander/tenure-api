package loan

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/audit"
	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/document"
	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/period"
	"github.com/ovander/tenure-api/internal/portfolio"
)

// Sources of a schedule (loans.source).
const (
	SourceComputed = "computed"
	SourceImported = "imported"
)

// Service implements the loans of RENT-SPEC-014.
type Service struct {
	DB        *db.DB
	Repo      Repository
	Portfolio portfolio.Service
	Docs      document.Service
	Now       func() time.Time
	NewID     func() uuid.UUID
}

func (s Service) now() time.Time { return s.Now().UTC().Truncate(time.Microsecond) }

func scoped(p *auth.Principal, requestID string) (db.Scope, error) {
	if p.Active == nil {
		return db.Scope{}, invariant.ErrForbidden
	}
	return p.DBScope(requestID), nil
}

// InstalmentInput is one line of a bank's amortisation table.
type InstalmentInput struct {
	DueOn          period.Date `json:"due_on" swaggertype:"string" format:"date"`
	PrincipalCents money.Cents `json:"principal_cents"`
	InterestCents  money.Cents `json:"interest_cents"`
	InsuranceCents money.Cents `json:"insurance_cents"`
}

// Input is the body of POST /loans and PUT /loans/{id}. Without
// instalments the table is computed from the rate, the term, the first due
// day and the insurance (a fixed rate, constant monthly instalments);
// with instalments it is the bank's table as typed in, and the term and
// first due day are read from it.
type Input struct {
	// Kind is asset (the default: a loan financing an asset of the scope)
	// or parts (a loan that financed the owner's parts of an SCI, Q-133:
	// no asset, the SCI's name instead).
	Kind           string      `json:"kind,omitempty" enums:"asset,parts"`
	AssetID        uuid.UUID   `json:"asset_id"`
	SCIName        string      `json:"sci_name,omitempty"`
	Label          string      `json:"label" example:"Prêt bancaire"`
	Lender         string      `json:"lender,omitempty" example:"Banque Exemple"`
	PrincipalCents money.Cents `json:"principal_cents" example:"15000000"`
	// AnnualRatePPM is the nominal annual rate in parts per million (3.45 %
	// is 34500); required for a computed table.
	AnnualRatePPM  *int64            `json:"annual_rate_ppm,omitempty" example:"34500"`
	TermMonths     int               `json:"term_months,omitempty" example:"240"`
	FirstDueOn     period.Date       `json:"first_due_on,omitempty" swaggertype:"string" format:"date"`
	InsuranceCents money.Cents       `json:"insurance_cents,omitempty" example:"2500"`
	Instalments    []InstalmentInput `json:"instalments,omitempty"`
	// DocumentID is the bank's offer or table, uploaded through POST
	// /documents.
	DocumentID *uuid.UUID `json:"document_id,omitempty"`
}

// View is a loan as the API returns it. Instalments is left out of a list.
type View struct {
	ID             uuid.UUID    `json:"id"`
	Kind           string       `json:"kind" enums:"asset,parts"`
	AssetID        *uuid.UUID   `json:"asset_id"`
	SCIName        string       `json:"sci_name,omitempty"`
	Label          string       `json:"label"`
	Lender         string       `json:"lender,omitempty"`
	PrincipalCents money.Cents  `json:"principal_cents"`
	AnnualRatePPM  *int64       `json:"annual_rate_ppm"`
	TermMonths     int          `json:"term_months"`
	FirstDueOn     period.Date  `json:"first_due_on" swaggertype:"string" format:"date"`
	LastDueOn      period.Date  `json:"last_due_on" swaggertype:"string" format:"date"`
	InsuranceCents money.Cents  `json:"insurance_cents"`
	Source         string       `json:"source" enums:"computed,imported"`
	DocumentID     *uuid.UUID   `json:"document_id"`
	InterestCents  money.Cents  `json:"interest_cents"`
	InsuranceTotal money.Cents  `json:"insurance_total_cents"`
	Years          []Year       `json:"years"`
	Instalments    []Instalment `json:"instalments,omitempty"`
	Version        int          `json:"version"`
}

func view(r loanRow, rows []Instalment, withRows bool) View {
	v := View{ID: r.ID, Kind: r.Kind, AssetID: r.AssetID, SCIName: r.SCIName, Label: r.Label, Lender: r.Lender, PrincipalCents: money.Cents(r.PrincipalCents),
		AnnualRatePPM: r.AnnualRatePPM, TermMonths: r.TermMonths, FirstDueOn: period.FromTime(r.FirstDueOn),
		InsuranceCents: money.Cents(r.InsuranceCents), Source: r.Source, DocumentID: r.DocumentID, Years: Years(rows), Version: r.Version}
	for _, i := range rows {
		v.InterestCents += i.InterestCents
		v.InsuranceTotal += i.InsuranceCents
		v.LastDueOn = i.DueOn
	}
	if withRows {
		v.Instalments = rows
	}
	return v
}

func instalments(rs []instalmentRow) []Instalment {
	out := make([]Instalment, 0, len(rs))
	for _, r := range rs {
		out = append(out, Instalment{Seq: r.Seq, DueOn: period.FromTime(r.DueOn), PrincipalCents: money.Cents(r.PrincipalCents),
			InterestCents: money.Cents(r.InterestCents), InsuranceCents: money.Cents(r.InsuranceCents), RemainingCents: money.Cents(r.RemainingCents)})
	}
	return out
}

// build validates an input and computes or checks its table.
func build(in *Input) (string, []Instalment, error) {
	in.Label, in.Lender = strings.TrimSpace(in.Label), strings.TrimSpace(in.Lender)
	in.SCIName = strings.TrimSpace(in.SCIName)
	if in.Kind == "" {
		in.Kind = KindAsset
	}
	var f invariant.Fields
	switch in.Kind {
	case KindAsset:
		if in.AssetID == uuid.Nil {
			f.Add("asset_id", "Le bien financé est obligatoire.")
		}
		if in.SCIName != "" {
			f.Add("sci_name", "Seul un emprunt pour des parts de SCI nomme une SCI.")
		}
	case KindParts:
		if in.AssetID != uuid.Nil {
			f.Add("asset_id", "Un emprunt pour des parts de SCI ne finance aucun bien du portefeuille.")
		}
		if in.SCIName == "" || len([]rune(in.SCIName)) > 200 {
			f.Add("sci_name", "Le nom de la SCI dont les parts ont été achetées est obligatoire (200 caractères au plus).")
		}
	default:
		f.Add("kind", "Nature attendue : asset ou parts.")
	}
	if in.Label == "" || len(in.Label) > 100 {
		f.Add("label", "Un libellé de 1 à 100 caractères est obligatoire.")
	}
	if len(in.Lender) > 100 {
		f.Add("lender", "100 caractères au plus.")
	}
	if in.PrincipalCents <= 0 {
		f.Add("principal_cents", "Le capital emprunté doit être positif.")
	}
	if r := in.AnnualRatePPM; r != nil && (*r < 0 || *r >= RateScale) {
		f.Add("annual_rate_ppm", "Un taux annuel entre 0 et 100 %, en millionièmes.")
	}
	if len(in.Instalments) == 0 {
		if in.AnnualRatePPM == nil {
			f.Add("annual_rate_ppm", "Le taux est obligatoire pour calculer le tableau.")
		}
		if in.TermMonths <= 0 || in.TermMonths > MaxInstalments {
			f.Add("term_months", "Une durée de 1 à 600 mois.")
		}
		if in.FirstDueOn.IsZero() {
			f.Add("first_due_on", "La date de la première échéance est obligatoire.")
		}
		if in.InsuranceCents < 0 {
			f.Add("insurance_cents", "L’assurance ne peut pas être négative.")
		}
	}
	if err := f.Err(); err != nil {
		return "", nil, err
	}
	if len(in.Instalments) == 0 {
		rows, err := Schedule(Terms{PrincipalCents: in.PrincipalCents, AnnualRatePPM: *in.AnnualRatePPM, TermMonths: in.TermMonths,
			FirstDueOn: in.FirstDueOn, InsuranceCents: in.InsuranceCents})
		return SourceComputed, rows, err
	}
	typed := make([]Instalment, 0, len(in.Instalments))
	for _, i := range in.Instalments {
		typed = append(typed, Instalment{DueOn: i.DueOn, PrincipalCents: i.PrincipalCents, InterestCents: i.InterestCents, InsuranceCents: i.InsuranceCents})
	}
	rows, err := Check(in.PrincipalCents, typed)
	if err != nil {
		return "", nil, err
	}
	in.TermMonths, in.FirstDueOn, in.InsuranceCents = len(rows), rows[0].DueOn, rows[0].InsuranceCents
	return SourceImported, rows, nil
}

func (s Service) checkLinks(ctx context.Context, in Input) error {
	if in.Kind == KindParts {
		return nil
	}
	if _, err := s.Portfolio.AssetInTx(ctx, in.AssetID); err != nil {
		if errors.Is(err, invariant.ErrNotFound) {
			var f invariant.Fields
			f.Add("asset_id", "Ce bien n’existe pas dans ce portefeuille.")
			return f
		}
		return err
	}
	if in.DocumentID != nil {
		if _, err := s.Docs.Repo.Get(ctx, *in.DocumentID); err != nil {
			if errors.Is(err, invariant.ErrNotFound) {
				var f invariant.Fields
				f.Add("document_id", "Ce document n’existe pas dans ce portefeuille.")
				return f
			}
			return err
		}
	}
	return nil
}

func (s Service) rows(loan uuid.UUID, owner uuid.UUID, table []Instalment) []instalmentRow {
	out := make([]instalmentRow, 0, len(table))
	for _, i := range table {
		out = append(out, instalmentRow{ID: s.NewID(), OwnerID: owner, LoanID: loan, Seq: i.Seq, DueOn: i.DueOn.Time(),
			PrincipalCents: int64(i.PrincipalCents), InterestCents: int64(i.InterestCents), InsuranceCents: int64(i.InsuranceCents),
			RemainingCents: int64(i.RemainingCents)})
	}
	return out
}

// Preview computes the table of a fixed-rate loan without recording it.
func (s Service) Preview(t Terms) (View, error) {
	rows, err := Schedule(t)
	if err != nil {
		return View{}, err
	}
	r := t.AnnualRatePPM
	return view(loanRow{PrincipalCents: int64(t.PrincipalCents), AnnualRatePPM: &r, TermMonths: t.TermMonths,
		FirstDueOn: t.FirstDueOn.Time(), InsuranceCents: int64(t.InsuranceCents), Source: SourceComputed}, rows, true), nil
}

// List returns the scope's loans, of one asset when asset is set, without
// their instalments.
func (s Service) List(ctx context.Context, p *auth.Principal, requestID string, asset *uuid.UUID) ([]View, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return nil, err
	}
	out := []View{}
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		loans, err := s.Repo.Loans(ctx, asset)
		if err != nil {
			return err
		}
		ids := make([]uuid.UUID, 0, len(loans))
		for _, l := range loans {
			ids = append(ids, l.ID)
		}
		rs, err := s.Repo.Instalments(ctx, ids)
		if err != nil {
			return err
		}
		by := map[uuid.UUID][]instalmentRow{}
		for _, r := range rs {
			by[r.LoanID] = append(by[r.LoanID], r)
		}
		for _, l := range loans {
			out = append(out, view(l, instalments(by[l.ID]), false))
		}
		return nil
	})
	return out, err
}

// Get returns one loan with its instalments.
func (s Service) Get(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID) (View, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return View{}, err
	}
	var out View
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		l, err := s.Repo.Loan(ctx, id, false)
		if err != nil {
			return err
		}
		rs, err := s.Repo.Instalments(ctx, []uuid.UUID{id})
		out = view(l, instalments(rs), true)
		return err
	})
	return out, err
}

// Create records a loan and its table (RENT-SPEC-014, Q-126).
func (s Service) Create(ctx context.Context, p *auth.Principal, requestID string, in Input) (View, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return View{}, err
	}
	source, table, err := build(&in)
	if err != nil {
		return View{}, err
	}
	if in.Kind == KindParts && p.Active.IsSCI() {
		return View{}, invariant.New(TypePartsPersonOnly, "")
	}
	var out View
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		if err := s.checkLinks(ctx, in); err != nil {
			return err
		}
		now := s.now()
		r := loanRow{ID: s.NewID(), OwnerID: sc.OwnerID, Kind: in.Kind, AssetID: assetOf(in), SCIName: in.SCIName, Label: in.Label, Lender: in.Lender,
			PrincipalCents: int64(in.PrincipalCents), AnnualRatePPM: in.AnnualRatePPM, TermMonths: in.TermMonths,
			FirstDueOn: in.FirstDueOn.Time(), InsuranceCents: int64(in.InsuranceCents), Source: source, DocumentID: in.DocumentID,
			CreatedAt: now, CreatedBy: p.Sub, UpdatedAt: now, UpdatedBy: p.Sub, Version: 1}
		if err := s.Repo.InsertLoan(ctx, r); err != nil {
			return err
		}
		if err := s.Repo.ReplaceInstalments(ctx, r.ID, s.rows(r.ID, sc.OwnerID, table)); err != nil {
			return err
		}
		out = view(r, table, true)
		return audit.Record(ctx, "loan.create", audit.Entity{Type: "loan", ID: r.ID.String()}, nil, view(r, table, false))
	})
	return out, err
}

// Update replaces a loan's terms and table when version is the version
// read (If-Match). A tax summary already issued keeps the figures it was issued with.
func (s Service) Update(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID, version int, in Input) (View, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return View{}, err
	}
	source, table, err := build(&in)
	if err != nil {
		return View{}, err
	}
	if in.Kind == KindParts && p.Active.IsSCI() {
		return View{}, invariant.New(TypePartsPersonOnly, "")
	}
	var out View
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		old, err := s.Repo.Loan(ctx, id, true)
		if err != nil {
			return err
		}
		if old.Version != version {
			return invariant.ErrStale
		}
		before, err := s.Repo.Instalments(ctx, []uuid.UUID{id})
		if err != nil {
			return err
		}
		if err := s.checkLinks(ctx, in); err != nil {
			return err
		}
		r := old
		r.Kind, r.AssetID, r.SCIName = in.Kind, assetOf(in), in.SCIName
		r.Label, r.Lender, r.PrincipalCents, r.AnnualRatePPM = in.Label, in.Lender, int64(in.PrincipalCents), in.AnnualRatePPM
		r.TermMonths, r.FirstDueOn, r.InsuranceCents, r.Source, r.DocumentID = in.TermMonths, in.FirstDueOn.Time(), int64(in.InsuranceCents), source, in.DocumentID
		r.UpdatedAt, r.UpdatedBy = s.now(), p.Sub
		if err := s.Repo.UpdateLoan(ctx, r, old.Version); err != nil {
			return err
		}
		r.Version = old.Version + 1
		if err := s.Repo.ReplaceInstalments(ctx, id, s.rows(id, sc.OwnerID, table)); err != nil {
			return err
		}
		out = view(r, table, true)
		return audit.Record(ctx, "loan.update", audit.Entity{Type: "loan", ID: id.String()},
			view(old, instalments(before), false), view(r, table, false))
	})
	return out, err
}

// Loan kinds (Q-133).
const (
	KindAsset = "asset"
	KindParts = "parts"
)

func assetOf(in Input) *uuid.UUID {
	if in.Kind == KindParts {
		return nil
	}
	id := in.AssetID
	return &id
}
