package tax

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/audit"
	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/charge"
	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/document"
	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/payment"
	"github.com/ovander/tenure-api/internal/period"
	"github.com/ovander/tenure-api/internal/problem"
)

// SummaryTemplate is the version of the tax summary template.
const SummaryTemplate = "v1"

// Regimes.
const (
	RegimeMicro = "micro"
	RegimeReel  = "reel"
)

// FlagDraftCharge marks a charge paid in the year but still a draft: it is
// not deducted until the owner confirms it (Q-88).
const FlagDraftCharge = "draft_charge"

// Problem types of the tax module.
const (
	TypeAlreadyIssued   = "tax/already-issued"
	TypeMicroIneligible = "tax/micro-not-eligible"
	TypeRegimeUnknown   = "tax/regime-unknown"
	// TypePersonOnly refuses to issue a person's declaration in an SCI's
	// scope: an SCI at the income tax files the 2072 (Q-133).
	TypePersonOnly       = "tax/person-only"
	TypeLineMapMissing   = "tax/line-map-missing"
	TypeCategoryUnmapped = "tax/category-unmapped"
	TypeYearInvalid      = "tax/year-invalid"
)

func init() {
	problem.Register(map[string]problem.Message{
		TypeAlreadyIssued:   problem.M(409, "Le récapitulatif fiscal de cette année est déjà émis.", "This year's tax summary is already issued."),
		TypeMicroIneligible: problem.M(422, "Le micro-foncier n’est pas ouvert pour cette année : recettes au-delà du seuil.", "Micro-foncier is not available this year: receipts above the threshold."),
		TypeRegimeUnknown:   problem.M(422, "Régime attendu : micro ou reel.", "Regime expected: micro or reel."),
		TypePersonOnly: problem.M(409, "Une SCI ne dépose ni la 2044 ni de déclaration BIC : ses revenus se déclarent sur la 2072, au nom de ses associés.",
			"An SCI files neither the 2044 nor a BIC declaration: its income goes on the 2072, for its associates."),
		TypeLineMapMissing:   problem.M(409, "La correspondance des lignes 2044 de cette année n’est pas encore publiée.", "This year's form 2044 line map is not published yet."),
		TypeCategoryUnmapped: problem.M(409, "Une catégorie de charge n’a pas de ligne 2044 cette année.", "A charge category has no form 2044 line this year."),
		TypeYearInvalid:      problem.M(422, "L’année doit être une année de revenus achevée.", "The year must be a completed income year."),
	})
}

// Service implements the annual tax summary.
type Service struct {
	DB     *db.DB
	Repo   Repository
	Params params.Repository
	Issuer document.Issuer
	Now    func() time.Time
	NewID  func() uuid.UUID
}

func (s Service) now() time.Time { return s.Now().UTC().Truncate(time.Microsecond) }

func (s Service) today() period.Date {
	loc, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		loc = time.UTC
	}
	return period.FromTime(s.Now().In(loc))
}

func actorOf(ctx context.Context) string {
	sc, _ := db.ScopeFrom(ctx)
	if sc.ActorSub != "" {
		return sc.ActorSub
	}
	return "system:" + sc.Role
}

// Inputs counts what a summary was computed from.
type Inputs struct {
	Payments     int `json:"payments"`
	Charges      int `json:"charges"`
	DraftCharges int `json:"draft_charges"`
	// LoanInstalments counts the recorded loans' instalments due in the
	// year (Q-126).
	LoanInstalments int `json:"loan_instalments,omitempty"`
	PriorYear       int `json:"prior_year,omitempty"`
}

// computed is a summary with its inputs.
type computed struct {
	summary Summary
	inputs  Inputs
	prior   map[string][]Deficit
	ps      params.Set
	docs    []uuid.UUID
}

// mapErr turns the calculation's refusals into business rules.
func mapErr(err error) error {
	switch {
	case errors.Is(err, params.ErrNotFound):
		return invariant.New(TypeLineMapMissing, err.Error())
	case errors.Is(err, ErrUnmapped):
		return invariant.New(TypeCategoryUnmapped, err.Error())
	}
	return err
}

// compute gathers the year's inputs in the scope and runs Lines
// (RENT-SPEC-004): payments received in the year with each matched amount
// split between the rent call's lines (Q-65); charges paid in the year,
// confirmed ones only (a draft is flagged, never deducted, Q-88); every
// asset with its shares and furnished flag; the deficits left by the last
// issued summary.
func (s Service) compute(ctx context.Context, year int) (computed, error) {
	var out computed
	ps, err := s.Params.Load(ctx)
	if err != nil {
		return out, err
	}
	out.ps = ps
	from, to := period.NewDate(year, 1, 1).Time(), params.TaxYearEnd(year).Time()
	pays, matches, err := s.Repo.Payments(ctx, from, to)
	if err != nil {
		return out, err
	}
	principal, err := s.Repo.PrincipalAssets(ctx)
	if err != nil {
		return out, err
	}
	byPayment := map[uuid.UUID][]matchRow{}
	for _, m := range matches {
		byPayment[m.PaymentID] = append(byPayment[m.PaymentID], m)
	}
	var payments []payment.Payment
	for _, p := range pays {
		dp := payment.Payment{ID: p.ID.String(), LeaseID: p.LeaseID.String(), AssetID: principal[p.LeaseID].String(),
			PaidOn: period.FromTime(p.PaidOn), AmountCents: money.Cents(p.AmountCents)}
		for _, m := range byPayment[p.ID] {
			var lines []document.Line
			if err := json.Unmarshal(m.Lines, &lines); err != nil {
				return out, err
			}
			var rent, prov money.Cents
			for _, l := range lines {
				switch l.Kind {
				case string(payment.LineRent):
					rent += l.AmountCents
				case string(payment.LineProvision):
					prov += l.AmountCents
				}
			}
			parts, err := SplitMatch(money.Cents(m.AmountCents), rent, prov, money.Cents(m.TotalCents))
			if err != nil {
				return out, err
			}
			for _, x := range parts {
				x.BillingDocumentID = m.BillingDocumentID.String()
				dp.Matches = append(dp.Matches, x)
			}
		}
		payments = append(payments, dp)
	}
	rows, err := s.Repo.Charges(ctx, from, to)
	if err != nil {
		return out, err
	}
	var charges []charge.Charge
	var drafts []Flag
	for _, c := range rows {
		if c.Status == "draft" {
			drafts = append(drafts, Flag{Kind: FlagDraftCharge, ID: c.ID.String()})
			continue
		}
		rec := money.Cents(c.RecoverableCents)
		dc := charge.Charge{ID: c.ID.String(), AssetID: c.AssetID.String(), Category: charge.Category(c.Category),
			AmountCents: money.Cents(c.AmountCents), ChargedOn: period.FromTime(c.ChargedOn), RecoverableAmountCents: &rec}
		if c.PaidOn != nil {
			dc.PaidOn = period.FromTime(*c.PaidOn)
		}
		if c.DocumentID != nil {
			dc.DocumentID = c.DocumentID.String()
			out.docs = append(out.docs, *c.DocumentID)
		}
		charges = append(charges, dc)
	}
	insts, err := s.Repo.LoanInstalments(ctx, from, to)
	if err != nil {
		return out, err
	}
	var loans []LoanInstalment
	seenDoc := map[uuid.UUID]bool{}
	for _, i := range insts {
		li := LoanInstalment{LoanID: i.LoanID.String(), AssetID: i.AssetID.String(), Seq: i.Seq, DueOn: period.FromTime(i.DueOn),
			InterestCents: money.Cents(i.InterestCents), InsuranceCents: money.Cents(i.InsuranceCents)}
		if i.DocumentID != nil {
			li.DocumentID = i.DocumentID.String()
			if !seenDoc[*i.DocumentID] {
				seenDoc[*i.DocumentID] = true
				out.docs = append(out.docs, *i.DocumentID)
			}
		}
		loans = append(loans, li)
	}
	typed := len(charges)
	charges, superseded := WithLoans(charges, loans, year)
	as, shares, err := s.Repo.Assets(ctx, from, to)
	if err != nil {
		return out, err
	}
	byAsset := map[uuid.UUID][]OwnerShare{}
	for _, sh := range shares {
		byAsset[sh.AssetID] = append(byAsset[sh.AssetID], OwnerShare{OwnerID: sh.OwnerID.String(), BP: sh.BP})
	}
	var assets []AssetShares
	for _, a := range as {
		assets = append(assets, AssetShares{AssetID: a.ID.String(), Furnished: a.Furnished, Shares: byAsset[a.ID]})
	}
	out.prior = map[string][]Deficit{}
	if prev, ok, err := s.Repo.PriorIssued(ctx, year); err != nil {
		return out, err
	} else if ok {
		_ = json.Unmarshal(prev.DeficitsAfter, &out.prior)
		out.inputs.PriorYear = prev.TaxYear
	}
	sum, err := Lines(payments, charges, assets, year, ps, out.prior)
	if err != nil {
		return out, mapErr(err)
	}
	sum.Flags = append(sum.Flags, drafts...)
	sum.Flags = append(sum.Flags, superseded...)
	out.summary = sum
	out.inputs.Payments, out.inputs.Charges, out.inputs.DraftCharges = len(payments), typed, len(drafts)
	out.inputs.LoanInstalments = len(loans)
	return out, nil
}

// EstimateInTx computes the summary of an income year in the caller's
// scoped transaction without storing it (the dashboard's tax estimate,
// RENT-SPEC-016). A year with no 2044 map is the problem
// tax/line-map-missing.
func (s Service) EstimateInTx(ctx context.Context, year int) (Summary, error) {
	c, err := s.compute(ctx, year)
	return c.summary, err
}

// View is a tax summary as the API returns it.
type View struct {
	ID      uuid.UUID `json:"id"`
	TaxYear int       `json:"tax_year"`
	State   string    `json:"state" enums:"draft,issued"`
	Regime  *string   `json:"regime" enums:"micro,reel"`
	Summary Summary   `json:"summary"`
	Inputs  Inputs    `json:"inputs"`
	// Deficits are the deficits left for later years, by owner.
	Deficits      map[string][]Deficit `json:"deficits_after"`
	ComputedAt    time.Time            `json:"computed_at"`
	IssuedAt      *time.Time           `json:"issued_at"`
	DocumentID    *uuid.UUID           `json:"document_id"`
	CSVDocumentID *uuid.UUID           `json:"csv_document_id"`
	Version       int                  `json:"version"`
}

func view(r summaryRow) View {
	v := View{ID: r.ID, TaxYear: r.TaxYear, State: r.State, Regime: r.Regime, ComputedAt: r.ComputedAt, IssuedAt: r.IssuedAt,
		DocumentID: r.DocumentID, CSVDocumentID: r.CSVDocumentID, Version: r.Version, Deficits: map[string][]Deficit{}}
	_ = json.Unmarshal(r.Summary, &v.Summary)
	_ = json.Unmarshal(r.Inputs, &v.Inputs)
	_ = json.Unmarshal(r.DeficitsAfter, &v.Deficits)
	return v
}

func scoped(p *auth.Principal, requestID string) (db.Scope, error) {
	if p.Active == nil {
		return db.Scope{}, invariant.ErrForbidden
	}
	return p.DBScope(requestID), nil
}

func (s Service) checkYear(year int) error {
	if year < 1970 || year >= s.today().Year {
		return invariant.New(TypeYearInvalid, strconv.Itoa(year))
	}
	return nil
}

// draft computes the year in the current transaction and stores it as the
// scope's draft, with its audit event tax.draft; an issued year is refused.
func (s Service) draft(ctx context.Context, owner uuid.UUID, year int) (View, error) {
	c, err := s.compute(ctx, year)
	if err != nil {
		return View{}, err
	}
	cur, exists, err := s.Repo.Summary(ctx, year, true)
	if err != nil {
		return View{}, err
	}
	if exists && cur.State == "issued" {
		return View{}, invariant.New(TypeAlreadyIssued, strconv.Itoa(year))
	}
	now, by := s.now(), actorOf(ctx)
	row := summaryRow{ID: s.NewID(), OwnerID: owner, TaxYear: year, State: "draft", ComputedAt: now, DeficitsAfter: json.RawMessage("{}"),
		Version: 1, CreatedAt: now, CreatedBy: by, UpdatedAt: now, UpdatedBy: by}
	row.Summary, _ = json.Marshal(c.summary)
	row.Inputs, _ = json.Marshal(c.inputs)
	var before any
	if exists {
		before = view(cur)
		row.ID, row.CreatedAt, row.CreatedBy, row.Version = cur.ID, cur.CreatedAt, cur.CreatedBy, cur.Version+1
		if err := s.Repo.Update(ctx, row, cur.Version); err != nil {
			return View{}, err
		}
	} else if err := s.Repo.Insert(ctx, row); err != nil {
		return View{}, err
	}
	out := view(row)
	return out, audit.Record(ctx, "tax.draft", audit.Entity{Type: "tax_summary", ID: row.ID.String()}, before, out)
}

// IssueInput is the body of POST /tax/summaries: the income year, and the
// regime to issue; without a regime the year is (re)computed as a draft with
// both regimes side by side.
type IssueInput struct {
	Year   int     `json:"year"`
	Regime *string `json:"regime" enums:"micro,reel"`
}

// Create drafts the year, or issues it in the regime given (a step_up route,
// RENT-BE-001 §8b). Issuing recomputes the year, refuses micro-foncier when
// the owner is not eligible, archives the RF-… PDF and the CSV of its lines
// (RENT-SPEC-004 outputs) and freezes the summary; a year is issued once.
func (s Service) Create(ctx context.Context, p *auth.Principal, requestID string, in IssueInput) (View, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return View{}, err
	}
	if err := s.checkYear(in.Year); err != nil {
		return View{}, err
	}
	if in.Regime != nil && *in.Regime != RegimeMicro && *in.Regime != RegimeReel {
		return View{}, invariant.New(TypeRegimeUnknown, *in.Regime)
	}
	if in.Regime != nil && p.Active.IsSCI() {
		return View{}, invariant.New(TypePersonOnly, "")
	}
	var out View
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		d, err := s.draft(ctx, sc.OwnerID, in.Year)
		if err != nil || in.Regime == nil {
			out = d
			return err
		}
		out, err = s.issue(ctx, sc.OwnerID, in.Year, *in.Regime)
		return err
	})
	return out, err
}

func ownerOf(sum Summary, owner string) (OwnerSummary, bool) {
	for _, o := range sum.Owners {
		if o.OwnerID == owner {
			return o, true
		}
	}
	return OwnerSummary{OwnerID: owner}, false
}

// deficitsAfter are the deficits each owner keeps for later years once the
// regime is applied: the réel carry-forward, or under micro-foncier the prior
// deficits, which micro does not use, less those that expired (Q-89).
func deficitsAfter(sum Summary, prior map[string][]Deficit, regime string) map[string][]Deficit {
	out := map[string][]Deficit{}
	for _, o := range sum.Owners {
		if regime == RegimeReel {
			out[o.OwnerID] = o.Reel.CarriedForward
			continue
		}
		expired := map[int]bool{}
		for _, e := range o.Reel.Expired {
			expired[e.Year] = true
		}
		for _, d := range prior[o.OwnerID] {
			if !expired[d.Year] {
				out[o.OwnerID] = append(out[o.OwnerID], d)
			}
		}
	}
	return out
}

func (s Service) issue(ctx context.Context, owner uuid.UUID, year int, regime string) (View, error) {
	c, err := s.compute(ctx, year)
	if err != nil {
		return View{}, err
	}
	cur, _, err := s.Repo.Summary(ctx, year, true)
	if err != nil {
		return View{}, err
	}
	mine, _ := ownerOf(c.summary, owner.String())
	if regime == RegimeMicro && !mine.Micro.Eligible {
		return View{}, invariant.New(TypeMicroIneligible, strconv.FormatInt(int64(mine.Micro.GrossCents), 10))
	}
	lock, err := c.ps.Int64(params.KeyReelLockYears, params.TaxYearEnd(year))
	if err != nil {
		return View{}, err
	}
	name, address, err := s.Repo.Owner(ctx, owner)
	if err != nil {
		return View{}, err
	}
	labels, err := s.Repo.AssetLabels(ctx)
	if err != nil {
		return View{}, err
	}
	docs, err := s.Repo.Documents(ctx, c.docs)
	if err != nil {
		return View{}, err
	}
	data := document.TaxSummaryData{Owner: document.Party{Name: name, Address: address}, TaxYear: year, Millesime: c.summary.Millesime,
		Regime: regime, MicroEligible: mine.Micro.Eligible, MicroGrossCents: mine.Micro.GrossCents, MicroAllowanceCents: mine.Micro.AllowanceCents,
		MicroTaxableCents: mine.Micro.TaxableCents, ReelIncomeCents: mine.Reel.IncomeCents,
		ReelExpensesCents: mine.Reel.InterestCents + mine.Reel.OtherExpensesCents, ReelNetCents: mine.Reel.NetCents,
		ReelTaxableCents: mine.Reel.TaxableCents, GlobalImputable: mine.Reel.GlobalImputableCents,
		FurnishedGrossCents: mine.FurnishedGrossReceiptsCents, LockYears: lock, Flags: len(c.summary.Flags)}
	for _, a := range mine.Assets {
		id, _ := uuid.Parse(a.AssetID)
		ta := document.TaxAsset{Label: labels[id], Furnished: a.Furnished, NetCents: a.NetCents, GrossReceiptsCents: a.GrossReceiptsCents}
		for _, l := range a.Lines {
			ta.Lines = append(ta.Lines, document.TaxLine{Line: l.Line, AmountCents: l.AmountCents})
		}
		data.Assets = append(data.Assets, ta)
	}
	for _, l := range mine.Totals {
		data.Totals = append(data.Totals, document.TaxLine{Line: l.Line, AmountCents: l.AmountCents})
	}
	after := deficitsAfter(c.summary, c.prior, regime)
	for _, d := range after[owner.String()] {
		data.CarriedForward = append(data.CarriedForward, document.TaxDeficit{Year: d.Year, RemainingCents: d.RemainingCents})
	}
	for _, d := range docs {
		label := d.OriginalName
		if label == "" {
			label = d.ID.String()
		}
		data.Supporting = append(data.Supporting, fmt.Sprintf("%s (SHA-256 %s…)", label, d.SHA256[:16]))
	}
	snap := map[string]any{"template": "tax_summary/" + SummaryTemplate, "year": year, "regime": regime, "summary": c.summary,
		"inputs": c.inputs, "prior": c.prior, "supporting": docs}
	iss, err := s.Issuer.Issue(ctx, document.Request{Class: document.ClassTaxSummary, TemplateVersion: SummaryTemplate,
		Code: document.AssetCode(owner), Snapshot: snap,
		Data: func(number string, issued period.Date) any {
			data.Number, data.IssuedOn = number, issued
			return data
		}}, c.ps)
	if err != nil {
		return View{}, err
	}
	csvData, err := CSV(year, c.summary, docs)
	if err != nil {
		return View{}, err
	}
	att, err := s.Issuer.Attach(ctx, document.AttachRequest{Class: document.ClassTaxSummary, Name: iss.Number + ".csv",
		Data: csvData, ContentType: "text/csv"}, c.ps)
	if err != nil {
		return View{}, err
	}
	now, by := s.now(), actorOf(ctx)
	before := view(cur)
	row := cur
	row.State, row.Regime, row.IssuedAt, row.DocumentID, row.CSVDocumentID = "issued", &regime, &now, &iss.DocumentID, &att.DocumentID
	row.Summary, _ = json.Marshal(c.summary)
	row.Inputs, _ = json.Marshal(c.inputs)
	row.DeficitsAfter, _ = json.Marshal(after)
	row.ComputedAt, row.UpdatedAt, row.UpdatedBy = now, now, by
	if err := s.Repo.Update(ctx, row, cur.Version); err != nil {
		return View{}, err
	}
	row.Version = cur.Version + 1
	out := view(row)
	return out, audit.Record(ctx, "tax.issue", audit.Entity{Type: "tax_summary", ID: row.ID.String()}, before,
		map[string]any{"summary": out, "number": iss.Number, "sha256": iss.SHA256, "csv_sha256": att.SHA256})
}

// CSV writes the lines of a summary, one row per owner, asset and line with
// the ids it comes from, then the owner totals, then the supporting
// documents (RENT-SPEC-004: every figure traceable to its payment or charge).
func CSV(year int, sum Summary, docs []SupportingDoc) ([]byte, error) {
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	_ = w.Write([]string{"tax_year", "owner_id", "asset_id", "line", "amount_cents", "sources"})
	for _, o := range sum.Owners {
		for _, a := range o.Assets {
			for _, l := range a.Lines {
				_ = w.Write([]string{strconv.Itoa(year), o.OwnerID, a.AssetID, l.Line, strconv.FormatInt(int64(l.AmountCents), 10), strings.Join(l.Sources, " ")})
			}
			if a.Furnished {
				_ = w.Write([]string{strconv.Itoa(year), o.OwnerID, a.AssetID, "furnished_gross_receipts", strconv.FormatInt(int64(a.GrossReceiptsCents), 10), ""})
			}
		}
		for _, l := range o.Totals {
			_ = w.Write([]string{strconv.Itoa(year), o.OwnerID, "total", l.Line, strconv.FormatInt(int64(l.AmountCents), 10), strings.Join(l.Sources, " ")})
		}
	}
	flags := append([]Flag(nil), sum.Flags...)
	sort.Slice(flags, func(i, j int) bool { return flags[i].Kind+flags[i].ID < flags[j].Kind+flags[j].ID })
	for _, f := range flags {
		_ = w.Write([]string{strconv.Itoa(year), "", "", "flag:" + f.Kind, "", f.ID})
	}
	for _, d := range docs {
		_ = w.Write([]string{strconv.Itoa(year), "", "", "supporting_document", "", d.ID.String() + " " + d.SHA256})
	}
	w.Flush()
	return b.Bytes(), w.Error()
}

// List returns a page of the scope's summaries.
func (s Service) List(ctx context.Context, p *auth.Principal, requestID string, year, offset, limit int) ([]View, int, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return nil, 0, err
	}
	var out []View
	var total int
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		rows, n, err := s.Repo.List(ctx, year, offset, limit)
		for _, r := range rows {
			out = append(out, view(r))
		}
		total = n
		return err
	})
	return out, total, err
}

// Get returns one summary.
func (s Service) Get(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID) (View, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return View{}, err
	}
	var out View
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		r, err := s.Repo.SummaryByID(ctx, id)
		out = view(r)
		return err
	})
	return out, err
}

// ExportCSV returns the CSV of a summary's lines, recording the export as an
// audit event tax.export (an export is a step_up route, RENT-BE-001 §8b).
func (s Service) ExportCSV(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID) ([]byte, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return nil, err
	}
	var out []byte
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		r, err := s.Repo.SummaryByID(ctx, id)
		if err != nil {
			return err
		}
		v := view(r)
		if out, err = CSV(v.TaxYear, v.Summary, nil); err != nil {
			return err
		}
		return audit.Record(ctx, "tax.export", audit.Entity{Type: "tax_summary", ID: id.String()}, nil,
			map[string]any{"tax_year": v.TaxYear, "sha256": document.SHA256(out)})
	})
	return out, err
}

// DraftOwner is tax.annual for one owner, in the job's scoped transaction:
// the draft of the income year, never issued; an issued year is left as it
// is. Dry run computes without writing.
func (s Service) DraftOwner(ctx context.Context, owner uuid.UUID, year int, dry bool) (any, error) {
	if dry {
		c, err := s.compute(ctx, year)
		if err != nil {
			return nil, err
		}
		return map[string]any{"summary": c.summary, "inputs": c.inputs}, nil
	}
	v, err := s.draft(ctx, owner, year)
	if invariant.Is(err, TypeAlreadyIssued) {
		return map[string]any{"skipped": "issued"}, nil
	}
	return v, err
}
