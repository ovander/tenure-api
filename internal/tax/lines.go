// Package tax produces the owner's annual tax summary (RENT-SPEC-004): form
// 2044 lines per asset and per owner share, the micro-foncier check and the
// deficit carry-forward. Lines is a pure function: payments, charges, shares
// and prior deficits are passed by the caller, every legal constant and every
// form line number comes from the parameter set.
package tax

import (
	"errors"
	"fmt"
	"sort"

	"github.com/ovander/tenure-api/internal/charge"
	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/payment"
)

// Map keys that are not charge categories.
const (
	mapKeyRent    = "rent"
	mapKeyForfait = "forfait_222"
)

// OwnerShare is one owner's share of an asset, in basis points (10000 = 100 %).
type OwnerShare struct {
	OwnerID string `json:"owner_id"`
	BP      int64  `json:"bp"`
}

// AssetShares describes an asset for the tax year: whether it is let
// furnished (BIC, gross receipts only in phase 1, OD-01) and who owns it.
type AssetShares struct {
	AssetID   string       `json:"asset_id"`
	Furnished bool         `json:"furnished"`
	Shares    []OwnerShare `json:"shares"`
}

// Deficit is a revenus-fonciers deficit of a year still to be used.
type Deficit struct {
	Year           int         `json:"year"`
	RemainingCents money.Cents `json:"remaining_cents"`
}

// LineAmount is the amount of one 2044 line with the ids it comes from.
type LineAmount struct {
	Line        string      `json:"line"`
	AmountCents money.Cents `json:"amount_cents"`
	Sources     []string    `json:"sources"`
}

// AssetLines is one owner's share of one asset.
type AssetLines struct {
	AssetID   string `json:"asset_id"`
	Furnished bool   `json:"furnished"`
	// Lines holds the 2044 lines of an unfurnished asset, ordered by line.
	Lines []LineAmount `json:"lines,omitempty"`
	// NetCents is income − expenses for an unfurnished asset.
	NetCents money.Cents `json:"net_cents"`
	// GrossReceiptsCents is the rent received for a furnished asset.
	GrossReceiptsCents money.Cents `json:"gross_receipts_cents,omitempty"`
}

// Micro is the micro-foncier check.
type Micro struct {
	Eligible       bool        `json:"eligible"`
	GrossCents     money.Cents `json:"gross_cents"`
	AllowanceCents money.Cents `json:"allowance_cents"`
	TaxableCents   money.Cents `json:"taxable_cents"`
}

// Reel is the régime réel result with the deficit rules (OPEN-QUESTIONS Q-10).
type Reel struct {
	IncomeCents        money.Cents `json:"income_cents"`
	InterestCents      money.Cents `json:"interest_cents"`
	OtherExpensesCents money.Cents `json:"other_expenses_cents"`
	NetCents           money.Cents `json:"net_cents"`
	PriorUsed          []Deficit   `json:"prior_used"`
	TaxableCents       money.Cents `json:"taxable_cents"`
	// GlobalImputableCents is the deficit set against global income.
	GlobalImputableCents money.Cents `json:"global_imputable_cents"`
	// CarriedForward are the deficits left for later years, oldest first.
	CarriedForward []Deficit `json:"carried_forward"`
	Expired        []Deficit `json:"expired"`
}

// OwnerSummary is one owner's summary.
type OwnerSummary struct {
	OwnerID                     string       `json:"owner_id"`
	Assets                      []AssetLines `json:"assets"`
	Totals                      []LineAmount `json:"totals"`
	Micro                       Micro        `json:"micro"`
	Reel                        Reel         `json:"reel"`
	FurnishedGrossReceiptsCents money.Cents  `json:"furnished_gross_receipts_cents"`
	// SCIShares are the owner's shares of SCIs' issued 2072s, counted in
	// Reel and Micro (WithShares, Q-133).
	SCIShares []SCIShare `json:"sci_shares,omitempty"`
}

// Flag kinds: figures counted but needing the owner's attention.
const (
	FlagMissingDocument  = "missing_document"
	FlagUnmatchedPayment = "unmatched_payment"
)

// Flag points at a source the owner should look at; it never excludes it.
type Flag struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// Summary is the tax summary of a year.
type Summary struct {
	Year      int            `json:"year"`
	Millesime int            `json:"millesime"`
	Owners    []OwnerSummary `json:"owners"`
	Flags     []Flag         `json:"flags"`
}

// ErrUnmapped is returned for a deductible charge whose category has no entry
// in the millésime's line map: the summary is refused rather than silently
// leaving the charge out.
var ErrUnmapped = errors.New("tax: category missing from the 2044 line map")

// Lines computes the summary of income year (RENT-SPEC-004):
//   - the line map and every parameter are those of income year year, read on
//     31 December of it; the millésime (year + 1) is the map's label (Q-31). No
//     map for the year is an error, the check tax.annual makes before drafting;
//   - cash basis: payments and charges count in the year they were paid;
//   - rent income only from amounts matched to rent lines; provisions and
//     regularisations received are not income, recoverable shares are not
//     deducted, except the category for line 225 at lease close (Q-08);
//   - one forfait_222_cents per local: per lease of unfurnished assets that
//     produced rent in the year, put on the lease's principal asset; furnished
//     assets never count (Q-27);
//   - line 229 takes the non-recoverable share of syndic provisions paid; the
//     category for line 230 is signed, the line being subtracted (Q-29);
//   - each line split between owners by largest remainder (Q-14);
//   - per owner: the micro-foncier check, and the réel result with prior
//     deficits applied oldest first, the deficit split between global income
//     (other expenses, capped) and carry-forward (interest and the excess), and
//     deficits older than deficit_carryforward_years expired (Q-10).
//
// prior holds each owner's unused deficits of earlier years.
func Lines(payments []payment.Payment, charges []charge.Charge, assets []AssetShares, year int, ps params.Set, prior map[string][]Deficit) (Summary, error) {
	lm, err := ps.Form2044LineMap(year)
	if err != nil {
		return Summary{}, err
	}
	asOf := params.TaxYearEnd(year)
	get := func(key string) (int64, error) { return ps.Int64(key, asOf) }
	threshold, err := get(params.KeyMicroFoncierThreshold)
	if err != nil {
		return Summary{}, err
	}
	allowancePct, err := get(params.KeyMicroFoncierAllowance)
	if err != nil {
		return Summary{}, err
	}
	forfait, err := get(params.KeyForfait222)
	if err != nil {
		return Summary{}, err
	}
	globalCap, err := get(params.KeyDeficitGlobalCap)
	if err != nil {
		return Summary{}, err
	}
	carryYears, err := get(params.KeyDeficitCarryForward)
	if err != nil {
		return Summary{}, err
	}
	rentLine, ok := lm.Lines[mapKeyRent]
	if !ok || rentLine == "" {
		return Summary{}, fmt.Errorf("%w: %s", ErrUnmapped, mapKeyRent)
	}

	type acc struct {
		lines map[string]*LineAmount
		gross money.Cents
		gsrc  []string
	}
	byAsset := map[string]*acc{}
	info := map[string]AssetShares{}
	for _, a := range assets {
		var sum int64
		for _, s := range a.Shares {
			if s.BP <= 0 {
				return Summary{}, fmt.Errorf("tax: asset %s has a non-positive share", a.AssetID)
			}
			sum += s.BP
		}
		if sum != 10000 {
			return Summary{}, fmt.Errorf("tax: shares of asset %s add up to %d bp, not 10000", a.AssetID, sum)
		}
		info[a.AssetID] = a
		byAsset[a.AssetID] = &acc{lines: map[string]*LineAmount{}}
	}
	add := func(assetID, line string, amount money.Cents, source string) {
		a := byAsset[assetID]
		la := a.lines[line]
		if la == nil {
			la = &LineAmount{Line: line}
			a.lines[line] = la
		}
		la.AmountCents += amount
		la.Sources = append(la.Sources, source)
	}
	var flags []Flag
	rentByLease := map[string]string{} // lease id → asset id, for line 222

	for _, p := range payments {
		if p.PaidOn.Year != year {
			continue
		}
		a, ok := byAsset[p.AssetID]
		if !ok {
			return Summary{}, fmt.Errorf("tax: payment %s for unknown asset %s", p.ID, p.AssetID)
		}
		if p.Unmatched() != 0 {
			flags = append(flags, Flag{FlagUnmatchedPayment, p.ID})
		}
		rent := p.Matched(payment.LineRent)
		if rent == 0 {
			continue
		}
		if info[p.AssetID].Furnished {
			a.gross += rent
			a.gsrc = append(a.gsrc, p.ID)
			continue
		}
		add(p.AssetID, rentLine, rent, p.ID)
		if rent > 0 && rentByLease[p.LeaseID] == "" {
			rentByLease[p.LeaseID] = p.AssetID
		}
	}

	for _, c := range charges {
		if c.PaidOn.IsZero() || c.PaidOn.Year != year {
			continue
		}
		if _, ok := byAsset[c.AssetID]; !ok {
			return Summary{}, fmt.Errorf("tax: charge %s for unknown asset %s", c.ID, c.AssetID)
		}
		if info[c.AssetID].Furnished {
			continue // BIC, out of scope in phase 1 (OD-01)
		}
		line, ok := lm.Lines[string(c.Category)]
		if !ok {
			return Summary{}, fmt.Errorf("%w: %s (charge %s)", ErrUnmapped, c.Category, c.ID)
		}
		if line == "" {
			continue
		}
		amount := c.AmountCents
		if c.Category != charge.CategoryUnrecoveredAtClose {
			rec, err := c.RecoverableCents()
			if err != nil {
				return Summary{}, err
			}
			amount -= rec
		}
		if amount == 0 {
			continue
		}
		if amount < 0 && !params.Has(lm.SubtractedLines, line) {
			return Summary{}, fmt.Errorf("tax: charge %s is negative on line %s", c.ID, line)
		}
		if c.DocumentID == "" {
			flags = append(flags, Flag{FlagMissingDocument, c.ID})
		}
		add(c.AssetID, line, amount, c.ID)
	}

	if forfaitLine := lm.Lines[mapKeyForfait]; forfaitLine != "" {
		for leaseID, assetID := range rentByLease {
			add(assetID, forfaitLine, money.Cents(forfait), mapKeyForfait+":"+leaseID)
		}
	}

	// Split every asset between its owners.
	owners := map[string]*OwnerSummary{}
	assetIDs := make([]string, 0, len(byAsset))
	for id := range byAsset {
		assetIDs = append(assetIDs, id)
	}
	sort.Strings(assetIDs)
	for _, id := range assetIDs {
		a, sh := byAsset[id], info[id]
		weights := make([]int64, len(sh.Shares))
		for i, s := range sh.Shares {
			weights[i] = s.BP
			if owners[s.OwnerID] == nil {
				owners[s.OwnerID] = &OwnerSummary{OwnerID: s.OwnerID}
			}
		}
		perOwner := make([]AssetLines, len(sh.Shares))
		for i := range perOwner {
			perOwner[i] = AssetLines{AssetID: id, Furnished: sh.Furnished}
		}
		if sh.Furnished {
			parts, err := money.Split(a.gross, weights)
			if err != nil {
				return Summary{}, err
			}
			for i := range perOwner {
				perOwner[i].GrossReceiptsCents = parts[i]
			}
		}
		lines := make([]string, 0, len(a.lines))
		for l := range a.lines {
			lines = append(lines, l)
		}
		sort.Strings(lines)
		for _, l := range lines {
			la := a.lines[l]
			if la.AmountCents < 0 && !params.Has(lm.SubtractedLines, l) {
				return Summary{}, fmt.Errorf("tax: line %s of asset %s is negative", l, id)
			}
			abs := la.AmountCents
			if abs < 0 {
				abs = -abs
			}
			parts, err := money.Split(abs, weights)
			if err != nil {
				return Summary{}, err
			}
			if la.AmountCents < 0 {
				for k := range parts {
					parts[k] = -parts[k]
				}
			}
			sort.Strings(la.Sources)
			for i := range perOwner {
				perOwner[i].Lines = append(perOwner[i].Lines, LineAmount{Line: l, AmountCents: parts[i], Sources: la.Sources})
			}
		}
		for i, s := range sh.Shares {
			perOwner[i].NetCents = net(perOwner[i].Lines, lm)
			owners[s.OwnerID].Assets = append(owners[s.OwnerID].Assets, perOwner[i])
		}
	}

	ids := make([]string, 0, len(owners))
	for id := range owners {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	sum := Summary{Year: year, Millesime: lm.Millesime}
	for _, id := range ids {
		o := owners[id]
		totals := map[string]*LineAmount{}
		for _, a := range o.Assets {
			o.FurnishedGrossReceiptsCents += a.GrossReceiptsCents
			for _, la := range a.Lines {
				t := totals[la.Line]
				if t == nil {
					t = &LineAmount{Line: la.Line}
					totals[la.Line] = t
				}
				t.AmountCents += la.AmountCents
				t.Sources = append(t.Sources, la.Sources...)
			}
		}
		keys := make([]string, 0, len(totals))
		for k := range totals {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			sort.Strings(totals[k].Sources)
			o.Totals = append(o.Totals, *totals[k])
		}
		var r Reel
		for _, t := range o.Totals {
			switch {
			case params.Has(lm.IncomeLines, t.Line):
				r.IncomeCents += t.AmountCents
			case params.Has(lm.InterestLines, t.Line):
				r.InterestCents += t.AmountCents
			case params.Has(lm.SubtractedLines, t.Line):
				r.OtherExpensesCents -= t.AmountCents
			default:
				r.OtherExpensesCents += t.AmountCents
			}
		}
		r.NetCents = r.IncomeCents - r.InterestCents - r.OtherExpensesCents
		if err := applyDeficits(&r, prior[id], year, int(carryYears), money.Cents(globalCap)); err != nil {
			return Summary{}, err
		}
		o.Reel = r

		taxable, err := money.MulDivHalfUp(r.IncomeCents, 100-allowancePct, 100)
		if err != nil {
			return Summary{}, err
		}
		o.Micro = Micro{Eligible: r.IncomeCents <= money.Cents(threshold), GrossCents: r.IncomeCents,
			AllowanceCents: r.IncomeCents - taxable, TaxableCents: taxable}
		sum.Owners = append(sum.Owners, *o)
	}
	sort.Slice(flags, func(i, j int) bool {
		if flags[i].Kind != flags[j].Kind {
			return flags[i].Kind < flags[j].Kind
		}
		return flags[i].ID < flags[j].ID
	})
	sum.Flags = flags
	return sum, nil
}

func net(lines []LineAmount, lm params.LineMap) money.Cents {
	var n money.Cents
	for _, l := range lines {
		switch {
		case params.Has(lm.IncomeLines, l.Line), params.Has(lm.SubtractedLines, l.Line):
			n += l.AmountCents
		default:
			n -= l.AmountCents
		}
	}
	return n
}

func applyDeficits(r *Reel, prior []Deficit, year, carryYears int, globalCap money.Cents) error {
	ps := append([]Deficit(nil), prior...)
	sort.Slice(ps, func(i, j int) bool { return ps[i].Year < ps[j].Year })
	var live []Deficit
	for _, d := range ps {
		switch {
		case d.Year >= year:
			return fmt.Errorf("tax: prior deficit of %d is not before %d", d.Year, year)
		case d.RemainingCents <= 0:
		case d.Year+carryYears < year:
			r.Expired = append(r.Expired, d)
		default:
			live = append(live, d)
		}
	}
	if r.NetCents >= 0 {
		left := r.NetCents
		for _, d := range live {
			use := min(d.RemainingCents, left)
			if use > 0 {
				r.PriorUsed = append(r.PriorUsed, Deficit{Year: d.Year, RemainingCents: use})
				left -= use
			}
			if rest := d.RemainingCents - use; rest > 0 {
				r.CarriedForward = append(r.CarriedForward, Deficit{Year: d.Year, RemainingCents: rest})
			}
		}
		r.TaxableCents = left
		return nil
	}
	r.CarriedForward = live
	deficit := -r.NetCents
	var carry, globalPart money.Cents
	if r.IncomeCents >= r.InterestCents {
		globalPart = deficit
	} else {
		carry = r.InterestCents - r.IncomeCents
		globalPart = max(0, deficit-carry)
	}
	r.GlobalImputableCents = min(globalPart, globalCap)
	carry += globalPart - r.GlobalImputableCents
	if carry > 0 {
		r.CarriedForward = append(r.CarriedForward, Deficit{Year: year, RemainingCents: carry})
	}
	return nil
}
