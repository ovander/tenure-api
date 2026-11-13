// Package dashboard is the owner's view of the portfolio's performance
// (RENT-SPEC-016): per asset, the year's rent, income, expenses and net,
// their yields on the acquisition price, the vacant days, the provisions
// against the recoverable charges and the aged arrears; for the portfolio,
// the cash by month and the year to date against the previous year. Every
// figure comes from the owner's journal, whose split is the tax summary's,
// never recomputed separately (Q-129). Build is pure.
package dashboard

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ovander/tenure-api/internal/ledger"
	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/period"
)

// bpScale is a whole, in basis points.
const bpScale = 10_000

// Overdue is an overdue rent call of one of the asset's leases.
type Overdue struct {
	DaysLate         int         `json:"days_late"`
	OutstandingCents money.Cents `json:"outstanding_cents"`
}

// AssetInput is what Build needs of an asset besides the journal.
type AssetInput struct {
	AssetID          string       `json:"asset_id"`
	Label            string       `json:"label"`
	AcquiredOn       *period.Date `json:"acquired_on,omitempty"`
	AcquisitionCents *money.Cents `json:"acquisition_cents,omitempty"`
	// VacantDays are the asset's vacant days in the year, up to the as-of
	// day (lease.Vacancies).
	VacantDays int       `json:"vacant_days"`
	Overdue    []Overdue `json:"overdue,omitempty"`
}

// Bucket is the arrears overdue between FromDays and ToDays days (ToDays
// nil: and more).
type Bucket struct {
	FromDays int         `json:"from_days"`
	ToDays   *int        `json:"to_days"`
	Cents    money.Cents `json:"cents"`
}

// Figures are the year's figures of an asset, or of the portfolio.
type Figures struct {
	RentCents        money.Cents `json:"rent_cents"`
	OtherIncomeCents money.Cents `json:"other_income_cents"`
	ExpensesCents    money.Cents `json:"expenses_cents"`
	// InterestCents is the part of the expenses that is loan interest.
	InterestCents money.Cents `json:"interest_cents"`
	// NetCents is rent + other income − expenses.
	NetCents money.Cents `json:"net_cents"`
	// ProvisionsReceivedCents is what the tenants paid towards the
	// recoverable charges; RecoverablePaidCents what the owner paid of
	// them. The difference is the regularisation to come.
	ProvisionsReceivedCents money.Cents `json:"provisions_received_cents"`
	RecoverablePaidCents    money.Cents `json:"recoverable_paid_cents"`
	ArrearsCents            money.Cents `json:"arrears_cents"`
	Arrears                 []Bucket    `json:"arrears"`
}

// Asset is one asset's line of the dashboard.
type Asset struct {
	AssetID          string       `json:"asset_id"`
	Label            string       `json:"label"`
	AcquisitionCents *money.Cents `json:"acquisition_cents"`
	Figures
	// GrossYieldBP is the rent over the acquisition price, and NetYieldBP
	// the net, in basis points a year (annualised over the days elapsed in
	// the current year); nil without an acquisition price.
	GrossYieldBP *int64 `json:"gross_yield_bp"`
	NetYieldBP   *int64 `json:"net_yield_bp"`
	// Days are the days of the year the asset was held, up to the as-of
	// day; VacantDays those it was let to no one.
	Days         int `json:"days"`
	VacantDays   int `json:"vacant_days"`
	OccupiedDays int `json:"occupied_days"`
}

// Month is the money of one month.
type Month struct {
	Month         string      `json:"month"`
	InCents       money.Cents `json:"in_cents"`
	OutCents      money.Cents `json:"out_cents"`
	NetCents      money.Cents `json:"net_cents"`
	IncomeCents   money.Cents `json:"income_cents"`
	ExpensesCents money.Cents `json:"expenses_cents"`
}

// Span is income and expenses over a span of days.
type Span struct {
	From          period.Date `json:"from" swaggertype:"string" format:"date"`
	To            period.Date `json:"to" swaggertype:"string" format:"date"`
	IncomeCents   money.Cents `json:"income_cents"`
	ExpensesCents money.Cents `json:"expenses_cents"`
	NetCents      money.Cents `json:"net_cents"`
}

// Dashboard is the dashboard of a year.
type Dashboard struct {
	Year int         `json:"year"`
	AsOf period.Date `json:"as_of" swaggertype:"string" format:"date"`
	// Partial says the year is not over: its figures run to AsOf.
	Partial bool    `json:"partial"`
	Assets  []Asset `json:"assets"`
	// Portfolio is the sum of every movement, those tied to no asset
	// included.
	Portfolio Figures `json:"portfolio"`
	Months    []Month `json:"months"`
	// YearToDate and PreviousYear compare the same span of days.
	YearToDate   Span `json:"year_to_date"`
	PreviousYear Span `json:"previous_year"`
}

type codes struct {
	rent, provisions, bank, interest string
}

func chartCodes(chart params.LedgerChart) (codes, error) {
	c := codes{rent: chart.Accounts[ledger.RoleRent].Code, provisions: chart.Accounts[ledger.RoleProvisions].Code,
		bank: chart.Accounts[ledger.RoleBank].Code, interest: chart.Accounts[ledger.RoleLoanInterest].Code}
	if c.rent == "" || c.provisions == "" || c.bank == "" || c.interest == "" {
		return c, fmt.Errorf("%w: rent, provisions, bank and loan_interest are needed", ledger.ErrChart)
	}
	return c, nil
}

// income and expense classes of the plan comptable général.
func isIncome(code string) bool  { return strings.HasPrefix(code, "7") }
func isExpense(code string) bool { return strings.HasPrefix(code, "6") }

// add posts an entry's lines to f.
func (c codes) add(f *Figures, e ledger.Entry) {
	for _, l := range e.Lines {
		net := l.DebitCents - l.CreditCents
		switch {
		case l.Account == c.rent:
			f.RentCents -= net
		case isIncome(l.Account):
			f.OtherIncomeCents -= net
		case isExpense(l.Account):
			f.ExpensesCents += net
			if l.Account == c.interest {
				f.InterestCents += net
			}
		case l.Account == c.provisions:
			if e.Kind == "charge" {
				f.RecoverablePaidCents += net
			} else {
				f.ProvisionsReceivedCents -= net
			}
		}
	}
	f.NetCents = f.RentCents + f.OtherIncomeCents - f.ExpensesCents
}

func (c codes) span(entries []ledger.Entry, from, to period.Date) Span {
	s := Span{From: from, To: to}
	var f Figures
	for _, e := range entries {
		if !e.Date.Before(from) && !e.Date.After(to) {
			c.add(&f, e)
		}
	}
	s.IncomeCents, s.ExpensesCents = f.RentCents+f.OtherIncomeCents, f.ExpensesCents
	s.NetCents = s.IncomeCents - s.ExpensesCents
	return s
}

func buckets(bounds []int, overdue []Overdue) ([]Bucket, money.Cents, error) {
	out := make([]Bucket, 0, len(bounds)+1)
	from := 1
	for i, b := range bounds {
		if b < from {
			return nil, 0, fmt.Errorf("dashboard: aging bounds must increase (%d)", b)
		}
		to := bounds[i]
		out = append(out, Bucket{FromDays: from, ToDays: &to})
		from = b + 1
	}
	out = append(out, Bucket{FromDays: from})
	var total money.Cents
	for _, o := range overdue {
		if o.DaysLate <= 0 || o.OutstandingCents <= 0 {
			continue
		}
		for i := range out {
			if out[i].ToDays == nil || o.DaysLate <= *out[i].ToDays {
				out[i].Cents += o.OutstandingCents
				break
			}
		}
		total += o.OutstandingCents
	}
	return out, total, nil
}

// yield is amount over base in basis points a year, annualised from days
// out of the year's length; nil without a base or a day.
func yield(amount money.Cents, base *money.Cents, days, yearDays int) (*int64, error) {
	if base == nil || *base <= 0 || days <= 0 {
		return nil, nil
	}
	a, err := money.MulDivFloor(amount, int64(yearDays)*bpScale, int64(days))
	if err != nil {
		return nil, err
	}
	y, err := money.MulDivFloor(a, 1, int64(*base))
	if err != nil {
		return nil, err
	}
	v := int64(y)
	return &v, nil
}

// Build computes the dashboard of year as of asOf (RENT-SPEC-016, Q-129):
//   - cur is the year's journal, prev the previous year's;
//   - an asset's figures sum its entries: rent the rent account's credits,
//     other income the rest of class 7, expenses class 6 (loan interest
//     among them), provisions received the tenants' payments to the
//     provisions account, recoverable paid the charges' debits to it;
//   - the yields are the rent and the net over the acquisition price, a
//     year's worth: over the days elapsed when the year is not over, over
//     the days held when the asset was bought during it;
//   - the arrears are the overdue calls as of asOf, aged into the buckets;
//   - the portfolio is every entry, those tied to no asset included;
//   - the months are the bank's movements and the income and expenses of
//     each month to asOf; the year to date is compared with the same days
//     of the previous year.
func Build(cur, prev []ledger.Entry, chart params.LedgerChart, assets []AssetInput, year int, asOf period.Date, aging params.DashboardAging) (Dashboard, error) {
	c, err := chartCodes(chart)
	if err != nil {
		return Dashboard{}, err
	}
	first, last := period.NewDate(year, 1, 1), period.NewDate(year, 12, 31)
	d := Dashboard{Year: year, AsOf: asOf, Assets: []Asset{}, Months: []Month{}}
	end := last
	switch {
	case asOf.Before(first):
		end = first.AddDays(-1)
	case !asOf.After(last):
		end, d.Partial = asOf, true
	default:
		d.AsOf = last
	}
	yearDays := period.DaysInclusive(first, last)
	byAsset := map[string]*Figures{}
	for _, e := range cur {
		if e.Date.After(end) {
			continue
		}
		c.add(&d.Portfolio, e)
		if e.AssetID != "" {
			f := byAsset[e.AssetID]
			if f == nil {
				f = &Figures{}
				byAsset[e.AssetID] = f
			}
			c.add(f, e)
		}
	}
	var allOverdue []Overdue
	for _, a := range assets {
		x := Asset{AssetID: a.AssetID, Label: a.Label, AcquisitionCents: a.AcquisitionCents}
		if f := byAsset[a.AssetID]; f != nil {
			x.Figures = *f
		}
		if x.Arrears, x.ArrearsCents, err = buckets(aging.BucketsDays, a.Overdue); err != nil {
			return Dashboard{}, err
		}
		allOverdue = append(allOverdue, a.Overdue...)
		held := first
		if a.AcquiredOn != nil && a.AcquiredOn.After(held) {
			held = *a.AcquiredOn
		}
		if !held.After(end) {
			x.Days = period.DaysInclusive(held, end)
		}
		x.VacantDays = min(a.VacantDays, x.Days)
		x.OccupiedDays = x.Days - x.VacantDays
		if x.GrossYieldBP, err = yield(x.RentCents, a.AcquisitionCents, x.Days, yearDays); err != nil {
			return Dashboard{}, err
		}
		if x.NetYieldBP, err = yield(x.NetCents, a.AcquisitionCents, x.Days, yearDays); err != nil {
			return Dashboard{}, err
		}
		d.Assets = append(d.Assets, x)
	}
	sort.SliceStable(d.Assets, func(i, j int) bool { return d.Assets[i].Label < d.Assets[j].Label })
	if d.Portfolio.Arrears, d.Portfolio.ArrearsCents, err = buckets(aging.BucketsDays, allOverdue); err != nil {
		return Dashboard{}, err
	}
	for m := first; !m.After(end); m = m.AddMonthsClamped(1) {
		p := m.Period()
		mo := Month{Month: p.String()}
		var f Figures
		for _, e := range cur {
			if e.Date.Period() != p || e.Date.After(end) {
				continue
			}
			c.add(&f, e)
			for _, l := range e.Lines {
				if l.Account == c.bank {
					mo.InCents += l.DebitCents
					mo.OutCents += l.CreditCents
				}
			}
		}
		mo.NetCents = mo.InCents - mo.OutCents
		mo.IncomeCents, mo.ExpensesCents = f.RentCents+f.OtherIncomeCents, f.ExpensesCents
		d.Months = append(d.Months, mo)
	}
	if !end.Before(first) {
		d.YearToDate = c.span(cur, first, end)
		pf := period.NewDate(year-1, 1, 1)
		pe := period.NewDate(year-1, end.Month, 1)
		pe = period.NewDate(year-1, end.Month, min(end.Day, pe.Period().Days()))
		d.PreviousYear = c.span(prev, pf, pe)
	}
	return d, nil
}
