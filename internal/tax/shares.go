package tax

import (
	"sort"

	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/params"
)

// SCIShare is an associate's share of an SCI's issued 2072 (RENT-SPEC-019,
// part 19d, Q-133): the SCI's figures of the year by nature, for the parts
// they held on 31 December.
type SCIShare struct {
	DeclarationID      string      `json:"declaration_id"`
	SCIName            string      `json:"sci_name"`
	Parts              int         `json:"parts"`
	TotalParts         int         `json:"total_parts"`
	IncomeCents        money.Cents `json:"income_cents"`
	InterestCents      money.Cents `json:"interest_cents"`
	OtherExpensesCents money.Cents `json:"other_expenses_cents"`
	NetCents           money.Cents `json:"net_cents"`
}

// PartsLoan is a loan the owner took to buy parts of an SCI (Q-133): the
// interest and insurance of its instalments due in the year, deducted with
// the owner's SCI shares.
type PartsLoan struct {
	LoanID         string      `json:"loan_id"`
	Label          string      `json:"label"`
	SCIName        string      `json:"sci_name"`
	InterestCents  money.Cents `json:"interest_cents"`
	InsuranceCents money.Cents `json:"insurance_cents"`
	DocumentID     string      `json:"document_id,omitempty"`
}

// Flag kinds of an associate's summary.
const (
	// FlagSCIPending points at an SCI the owner is a linked associate of,
	// whose 2072 of the year is not issued: their share is not in the
	// summary yet.
	FlagSCIPending = "sci_pending"
)

// WithShares adds an owner's SCI shares to their summary (Q-133), after
// Lines:
//   - the réel takes each share's income, interest and other expenses with
//     the owner's own, then the owner's deficit rules apply to the whole, as
//     in Lines (prior deficits oldest first, the global income cap, interest
//     carried forward);
//   - the micro-foncier counts the shares' income in the gross receipts, and
//     is open to an owner with shares only if they also let a property
//     unfurnished directly;
//   - the interest and insurance of the owner's loans for SCI parts join
//     the réel's interest, as a loan's do on line 250;
//   - an owner with shares or such loans and no asset of their own gets a
//     summary of their own.
//
// The shares and the loans for parts also fill the form's section for
// partnerships (Q-135), its lines read from the parameter set: gross income,
// expenses other than interest, interest (the SCI's and the owner's own loans
// for parts, their insurance with them as on line 250) and the result. A
// year without that section in the parameters is refused, as Lines refuses a
// year without its line map.
func WithShares(sum Summary, owner string, shares []SCIShare, loans []PartsLoan, year int, ps params.Set, prior map[string][]Deficit) (Summary, error) {
	if len(shares) == 0 && len(loans) == 0 {
		return sum, nil
	}
	asOf := params.TaxYearEnd(year)
	get := func(key string) (int64, error) { return ps.Int64(key, asOf) }
	threshold, err := get(params.KeyMicroFoncierThreshold)
	if err != nil {
		return sum, err
	}
	allowancePct, err := get(params.KeyMicroFoncierAllowance)
	if err != nil {
		return sum, err
	}
	globalCap, err := get(params.KeyDeficitGlobalCap)
	if err != nil {
		return sum, err
	}
	carryYears, err := get(params.KeyDeficitCarryForward)
	if err != nil {
		return sum, err
	}
	lm, err := ps.Form2044SCILineMap(year)
	if err != nil {
		return sum, err
	}
	i := -1
	for k := range sum.Owners {
		if sum.Owners[k].OwnerID == owner {
			i = k
		}
	}
	if i < 0 {
		sum.Owners = append(sum.Owners, OwnerSummary{OwnerID: owner, Assets: []AssetLines{}, Totals: []LineAmount{}})
		i = len(sum.Owners) - 1
	}
	o := &sum.Owners[i]
	direct := len(shares) == 0
	for _, a := range o.Assets {
		if !a.Furnished {
			direct = true
		}
	}
	r := Reel{IncomeCents: o.Reel.IncomeCents, InterestCents: o.Reel.InterestCents, OtherExpensesCents: o.Reel.OtherExpensesCents}
	for _, s := range shares {
		r.IncomeCents += s.IncomeCents
		r.InterestCents += s.InterestCents
		r.OtherExpensesCents += s.OtherExpensesCents
	}
	for _, l := range loans {
		r.InterestCents += l.InterestCents + l.InsuranceCents
	}
	r.NetCents = r.IncomeCents - r.InterestCents - r.OtherExpensesCents
	if err := applyDeficits(&r, prior[owner], year, int(carryYears), money.Cents(globalCap)); err != nil {
		return sum, err
	}
	o.Reel = r
	o.SCIShares, o.PartsLoans = shares, loans
	o.SCILines = sciLines(lm, shares, loans)
	taxable, err := money.MulDivHalfUp(r.IncomeCents, 100-allowancePct, 100)
	if err != nil {
		return sum, err
	}
	o.Micro = Micro{Eligible: direct && r.IncomeCents <= money.Cents(threshold), GrossCents: r.IncomeCents,
		AllowanceCents: r.IncomeCents - taxable, TaxableCents: taxable}
	return sum, nil
}

// sciLines is the form's section for partnerships: one line per figure, each
// with the declarations and loans it comes from.
func sciLines(lm params.SCILineMap, shares []SCIShare, loans []PartsLoan) []LineAmount {
	income := LineAmount{Line: lm.Income, Sources: []string{}}
	expenses := LineAmount{Line: lm.Expenses, Sources: []string{}}
	interest := LineAmount{Line: lm.Interest, Sources: []string{}}
	for _, s := range shares {
		income.AmountCents += s.IncomeCents
		expenses.AmountCents += s.OtherExpensesCents
		interest.AmountCents += s.InterestCents
		for _, l := range []*LineAmount{&income, &expenses, &interest} {
			l.Sources = append(l.Sources, s.DeclarationID)
		}
	}
	for _, l := range loans {
		interest.AmountCents += l.InterestCents + l.InsuranceCents
		interest.Sources = append(interest.Sources, l.LoanID)
	}
	net := LineAmount{Line: lm.Net, AmountCents: income.AmountCents - expenses.AmountCents - interest.AmountCents,
		Sources: append([]string{}, interest.Sources...)}
	for _, l := range []*LineAmount{&income, &expenses, &interest, &net} {
		sort.Strings(l.Sources)
		l.Sources = compactSorted(l.Sources)
	}
	return []LineAmount{income, expenses, interest, net}
}

func compactSorted(s []string) []string {
	out := s[:0]
	for i, v := range s {
		if i == 0 || v != s[i-1] {
			out = append(out, v)
		}
	}
	return out
}
