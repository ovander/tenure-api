package dashboard

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/audit"
	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/bic"
	"github.com/ovander/tenure-api/internal/billing"
	"github.com/ovander/tenure-api/internal/csvx"
	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/document"
	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/lease"
	"github.com/ovander/tenure-api/internal/ledger"
	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/period"
	"github.com/ovander/tenure-api/internal/portfolio"
	"github.com/ovander/tenure-api/internal/tax"
)

// Service serves the dashboard.
type Service struct {
	DB        *db.DB
	Ledger    ledger.Service
	Lease     lease.Service
	Billing   billing.Service
	Portfolio portfolio.Service
	Tax       tax.Service
	BIC       bic.Service
	Params    params.Repository
	Now       func() time.Time
}

// TaxEstimate is the year's tax summary computed as a draft for the scope's
// owner, both regimes side by side; Available is false, with the reason,
// when the year has no 2044 map yet.
type TaxEstimate struct {
	Available bool       `json:"available"`
	Reason    string     `json:"reason,omitempty"`
	Micro     *tax.Micro `json:"micro,omitempty"`
	Reel      *tax.Reel  `json:"reel,omitempty"`
	Flags     int        `json:"flags"`
	Lower     string     `json:"lower,omitempty" enums:"micro,reel"`
	Millesime int        `json:"millesime,omitempty"`
	Owner     string     `json:"owner_id,omitempty"`
	// The consolidation per person (Q-133), each with where it comes from:
	// the shares of SCIs' issued 2072s and the loans for SCI parts, already
	// in Micro and Reel; the SCIs whose 2072 does not name the owner yet;
	// and the owner's furnished letting, a BIC result beside the revenus
	// fonciers, never added to them.
	SCIShares  []tax.SCIShare  `json:"sci_shares,omitempty"`
	PartsLoans []tax.PartsLoan `json:"parts_loans,omitempty"`
	SCIPending int             `json:"sci_pending,omitempty"`
	BIC        *BICEstimate    `json:"bic,omitempty"`
}

// BICEstimate is the owner's furnished letting of the year (RENT-SPEC-019,
// Q-132): as declared when it is, else computed under the réel, the micro
// beside.
type BICEstimate struct {
	Declared          bool        `json:"declared"`
	Regime            string      `json:"regime" enums:"reel,micro"`
	ReceiptsCents     money.Cents `json:"receipts_cents"`
	ResultCents       money.Cents `json:"result_cents"`
	TaxableCents      money.Cents `json:"taxable_cents"`
	MicroEligible     bool        `json:"micro_eligible"`
	MicroTaxableCents money.Cents `json:"micro_taxable_cents"`
}

// ReasonSCI is the estimate's reason in an SCI's scope: an SCI files the
// 2072, not the 2044 (GET /sci/result).
const ReasonSCI = "entity/sci"

// View is the dashboard as the API returns it.
type View struct {
	Dashboard
	Tax TaxEstimate `json:"tax"`
}

func paris() *time.Location {
	loc, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		return time.UTC
	}
	return loc
}

func scoped(p *auth.Principal, requestID string) (db.Scope, error) {
	if p.Active == nil {
		return db.Scope{}, invariant.ErrForbidden
	}
	return p.DBScope(requestID), nil
}

func checkYear(year int) error {
	if year < 1971 || year > 9999 {
		var f invariant.Fields
		f.Add("year", "Une année sur quatre chiffres est attendue.")
		return f
	}
	return nil
}

func label(a portfolio.AssetView) string {
	if a.Lot != "" {
		return a.Address + ", " + a.Lot
	}
	return a.Address
}

// build gathers the inputs in the scope's transaction and runs Build.
func (s Service) build(ctx context.Context, owner uuid.UUID, sci bool, year int) (View, error) {
	today := period.FromTime(s.Now().In(paris()))
	ps, err := s.Params.Load(ctx)
	if err != nil {
		return View{}, err
	}
	asOf := period.NewDate(year, time.December, 31)
	if today.Before(asOf) {
		asOf = today
	}
	chart, err := ps.LedgerChart(asOf)
	if err != nil {
		return View{}, err
	}
	aging, err := ps.DashboardAging(asOf)
	if err != nil {
		return View{}, err
	}
	cur, err := s.Ledger.JournalInTx(ctx, year)
	if err != nil {
		return View{}, err
	}
	prev, err := s.Ledger.JournalInTx(ctx, year-1)
	if err != nil {
		return View{}, err
	}
	assets, err := s.Portfolio.AllAssetsInTx(ctx)
	if err != nil {
		return View{}, err
	}
	principal, err := tax.Repository{}.PrincipalAssets(ctx)
	if err != nil {
		return View{}, err
	}
	overdue := map[uuid.UUID][]Overdue{}
	for leaseID, assetID := range principal {
		a, err := s.Billing.ArrearsInTx(ctx, leaseID, today)
		if err != nil {
			return View{}, err
		}
		for _, it := range a.Items {
			if it.Overdue {
				overdue[assetID] = append(overdue[assetID], Overdue{DaysLate: it.DaysLate, OutstandingCents: it.OutstandingCents})
			}
		}
	}
	in := make([]AssetInput, 0, len(assets))
	for _, a := range assets {
		v, err := s.Lease.VacanciesInTx(ctx, a.ID, today)
		if err != nil {
			return View{}, err
		}
		x := AssetInput{AssetID: a.ID.String(), Label: label(a), AcquiredOn: a.AcquiredOn, VacantDays: v.DaysByYear[year], Overdue: overdue[a.ID]}
		if a.AcquisitionCents != nil {
			c := money.Cents(*a.AcquisitionCents)
			x.AcquisitionCents = &c
		}
		in = append(in, x)
	}
	d, err := Build(cur.Entries, prev.Entries, chart, in, year, today, aging)
	if errors.Is(err, ledger.ErrChart) {
		return View{}, invariant.New(ledger.TypeChartIncomplete, err.Error())
	}
	if err != nil {
		return View{}, err
	}
	out := View{Dashboard: d}
	if sci {
		out.Tax = TaxEstimate{Reason: ReasonSCI}
		return out, nil
	}
	out.Tax, err = s.estimate(ctx, owner, year)
	return out, err
}

// estimate is the tax summary of year computed as a draft (Q-129); a year
// without its 2044 map has none.
func (s Service) estimate(ctx context.Context, owner uuid.UUID, year int) (TaxEstimate, error) {
	sum, err := s.Tax.EstimateInTx(ctx, year)
	var inv *invariant.Error
	if errors.As(err, &inv) && (inv.Type == tax.TypeLineMapMissing || inv.Type == tax.TypeCategoryUnmapped) {
		return TaxEstimate{Reason: inv.Type}, nil
	}
	if err != nil {
		return TaxEstimate{}, err
	}
	t := TaxEstimate{Available: true, Millesime: sum.Millesime, Flags: len(sum.Flags)}
	for _, f := range sum.Flags {
		if f.Kind == tax.FlagSCIPending {
			t.SCIPending++
		}
	}
	b, ok, err := s.BIC.ResultInTx(ctx, owner, year)
	switch {
	case errors.Is(err, params.ErrNotFound):
	case err != nil:
		return TaxEstimate{}, err
	case ok:
		r := b.Result
		t.BIC = &BICEstimate{Declared: r.Declared, Regime: r.Regime, ReceiptsCents: r.ReceiptsCents, ResultCents: r.ResultCents,
			TaxableCents: r.TaxableCents, MicroEligible: r.Micro.Eligible, MicroTaxableCents: r.Micro.TaxableCents}
	}
	for _, o := range sum.Owners {
		if o.OwnerID != owner.String() {
			continue
		}
		m, r := o.Micro, o.Reel
		t.Micro, t.Reel, t.Owner = &m, &r, o.OwnerID
		t.SCIShares, t.PartsLoans = o.SCIShares, o.PartsLoans
		t.Lower = "reel"
		if m.Eligible && m.TaxableCents <= r.TaxableCents {
			t.Lower = "micro"
		}
	}
	return t, nil
}

// Year returns the dashboard of a year (RENT-SPEC-016, Q-129).
func (s Service) Year(ctx context.Context, p *auth.Principal, requestID string, year int) (View, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return View{}, err
	}
	if err := checkYear(year); err != nil {
		return View{}, err
	}
	var out View
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		out, err = s.build(ctx, sc.OwnerID, p.Active.IsSCI(), year)
		return err
	})
	return out, err
}

// CSV writes the dashboard's assets, one row each, then the portfolio.
func CSV(d Dashboard) []byte {
	var b bytes.Buffer
	w := csvx.NewWriter(&b)
	_ = w.Write([]string{"year", "as_of", "asset_id", "label", "acquisition_cents", "rent_cents", "other_income_cents", "expenses_cents",
		"interest_cents", "net_cents", "gross_yield_bp", "net_yield_bp", "days", "vacant_days", "occupied_days",
		"provisions_received_cents", "recoverable_paid_cents", "arrears_cents"})
	c := func(v money.Cents) string { return strconv.FormatInt(int64(v), 10) }
	opt := func(v *int64) string {
		if v == nil {
			return ""
		}
		return strconv.FormatInt(*v, 10)
	}
	optc := func(v *money.Cents) string {
		if v == nil {
			return ""
		}
		return c(*v)
	}
	y, asOf := strconv.Itoa(d.Year), d.AsOf.String()
	for _, a := range d.Assets {
		_ = w.Write([]string{y, asOf, a.AssetID, a.Label, optc(a.AcquisitionCents), c(a.RentCents), c(a.OtherIncomeCents), c(a.ExpensesCents),
			c(a.InterestCents), c(a.NetCents), opt(a.GrossYieldBP), opt(a.NetYieldBP), strconv.Itoa(a.Days), strconv.Itoa(a.VacantDays),
			strconv.Itoa(a.OccupiedDays), c(a.ProvisionsReceivedCents), c(a.RecoverablePaidCents), c(a.ArrearsCents)})
	}
	p := d.Portfolio
	_ = w.Write([]string{y, asOf, "portfolio", "", "", c(p.RentCents), c(p.OtherIncomeCents), c(p.ExpensesCents), c(p.InterestCents),
		c(p.NetCents), "", "", "", "", "", c(p.ProvisionsReceivedCents), c(p.RecoverablePaidCents), c(p.ArrearsCents)})
	w.Flush()
	return b.Bytes()
}

// Export returns the dashboard's CSV, recording the export with its digest.
func (s Service) Export(ctx context.Context, p *auth.Principal, requestID string, year int) ([]byte, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return nil, err
	}
	if err := checkYear(year); err != nil {
		return nil, err
	}
	var out []byte
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		v, err := s.build(ctx, sc.OwnerID, p.Active.IsSCI(), year)
		if err != nil {
			return err
		}
		out = CSV(v.Dashboard)
		return audit.Record(ctx, "dashboard.export", audit.Entity{Type: "dashboard", ID: strconv.Itoa(year)}, nil,
			map[string]any{"year": year, "as_of": v.AsOf.String(), "sha256": document.SHA256(out)})
	})
	return out, err
}
