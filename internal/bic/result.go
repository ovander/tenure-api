package bic

import (
	"sort"

	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/params"
)

// Regimes of a year.
const (
	RegimeReel  = "reel"
	RegimeMicro = "micro"
)

// YearInput is an owner's furnished letting for a calendar year, their
// shares applied: the receipts, the charges other than depreciation and the
// year's depreciation (Q-132). A year already declared gives its stored
// result instead, which the chain takes as it is.
type YearInput struct {
	Year int `json:"year"`
	// ReceiptsCents is rent, provisions, regularisation and what a deposit
	// statement kept.
	ReceiptsCents money.Cents `json:"receipts_cents"`
	// ChargesCents is the charges paid (the recoverable part included, the
	// provisions being receipts), the loan interest and insurance, and the
	// furniture expensed.
	ChargesCents      money.Cents `json:"charges_cents"`
	DepreciationCents money.Cents `json:"depreciation_cents"`
	// Regime is the regime of the year: reel (the default) or micro.
	Regime string `json:"regime,omitempty" enums:"reel,micro"`
	// Stored is the result of a year already declared.
	Stored *YearResult `json:"stored,omitempty"`
}

// Micro is the micro-BIC figure of a year: eligible when the receipts do
// not exceed the threshold; the taxable result is the receipts less the
// abatement, which is never below its minimum nor above the receipts.
type Micro struct {
	Eligible       bool        `json:"eligible"`
	AbatementCents money.Cents `json:"abatement_cents"`
	TaxableCents   money.Cents `json:"taxable_cents"`
}

// Deficit is a deficit of the réel not yet used: its year and what is left.
type Deficit struct {
	Year           int         `json:"year"`
	RemainingCents money.Cents `json:"remaining_cents"`
}

// YearResult is the result of a year under its regime, with the réel and
// micro figures and what it carries to the next year.
type YearResult struct {
	YearInput
	// Declared says the year comes from an issued declaration.
	Declared bool `json:"declared"`
	// CarriedInCents is the depreciation carried from earlier years.
	CarriedInCents money.Cents `json:"carried_in_cents"`
	// RoomCents is what the depreciation may take: receipts less the other
	// charges, never below zero (CGI art. 39 C, II).
	RoomCents       money.Cents `json:"room_cents"`
	DeductedCents   money.Cents `json:"deducted_cents"`
	CarriedOutCents money.Cents `json:"carried_out_cents"`
	// ResultCents is the réel result: receipts − charges − depreciation
	// deducted; negative only through the other charges (a deficit).
	ResultCents money.Cents `json:"result_cents"`
	// DeficitUsedCents is the earlier deficits set against a positive réel
	// result, the oldest first.
	DeficitUsedCents money.Cents `json:"deficit_used_cents"`
	// TaxableCents is the year's taxable result under its regime: the réel
	// result less the deficits used (never below zero), or the micro figure.
	TaxableCents money.Cents `json:"taxable_cents"`
	// Deficits are the deficits still open after the year.
	Deficits []Deficit `json:"deficits"`
	Micro    Micro     `json:"micro"`
	// Lower is the regime with the lower taxable result: micro or reel; reel
	// when micro is not open.
	Lower string `json:"lower" enums:"micro,reel"`
}

// Results chains the years in order (RENT-SPEC-019, Q-132).
//   - A year declared is taken as stored: its carry and its open deficits
//     go on to the next year.
//   - Under the réel, the depreciation available is the year's plus what
//     earlier years carried; it is deducted up to the room (receipts less
//     the other charges, CGI art. 39 C, II) and the rest is carried without
//     time limit. A negative result (from the charges) opens a deficit; a
//     positive one uses the open deficits, the oldest first, those older
//     than deficitYears having lapsed.
//   - Under the micro, the year carries no depreciation of its own and
//     passes the earlier carry on; the open deficits are not used on it,
//     the cautious reading, and keep ageing.
//
// The micro figure is computed every year beside. Pure: the caller passes
// the years, the micro parameter of each and the deficit lifetime.
func Results(years []YearInput, micro func(year int) params.BICMicro, deficitYears int) []YearResult {
	sorted := append([]YearInput(nil), years...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Year < sorted[j].Year })
	out := make([]YearResult, 0, len(sorted))
	var carried money.Cents
	var deficits []Deficit
	for _, y := range sorted {
		if y.Stored != nil {
			r := *y.Stored
			r.Declared = true
			out = append(out, r)
			carried = r.CarriedOutCents
			deficits = append([]Deficit(nil), r.Deficits...)
			continue
		}
		if y.Regime == "" {
			y.Regime = RegimeReel
		}
		open := deficits[:0:0]
		for _, d := range deficits {
			if y.Year-d.Year <= deficitYears && d.RemainingCents > 0 {
				open = append(open, d)
			}
		}
		r := YearResult{YearInput: y, CarriedInCents: carried}
		if room := y.ReceiptsCents - y.ChargesCents; room > 0 {
			r.RoomCents = room
		}
		m := micro(y.Year)
		r.Micro.Eligible = int64(y.ReceiptsCents) <= m.ThresholdCents
		ab := money.Cents(halfUp(int64(y.ReceiptsCents)*m.AbatementBP, FullShare))
		ab = max(ab, money.Cents(m.MinimumAbatementCents))
		r.Micro.AbatementCents = min(ab, max(y.ReceiptsCents, 0))
		r.Micro.TaxableCents = y.ReceiptsCents - r.Micro.AbatementCents
		available := carried + y.DepreciationCents
		r.DeductedCents = min(available, r.RoomCents)
		r.ResultCents = y.ReceiptsCents - y.ChargesCents - r.DeductedCents
		if y.Regime == RegimeMicro {
			r.DeductedCents, r.CarriedOutCents, r.TaxableCents = 0, carried, r.Micro.TaxableCents
			r.ResultCents = y.ReceiptsCents - y.ChargesCents
		} else {
			r.CarriedOutCents = available - r.DeductedCents
			if r.ResultCents < 0 {
				open = append(open, Deficit{Year: y.Year, RemainingCents: -r.ResultCents})
			} else {
				left := r.ResultCents
				for i := range open {
					use := min(open[i].RemainingCents, left)
					open[i].RemainingCents -= use
					left -= use
					r.DeficitUsedCents += use
				}
				r.TaxableCents = left
			}
		}
		kept := []Deficit{}
		for _, d := range open {
			if d.RemainingCents > 0 {
				kept = append(kept, d)
			}
		}
		r.Deficits, deficits, carried = kept, kept, r.CarriedOutCents
		r.Lower = RegimeReel
		if r.Micro.Eligible && r.Micro.TaxableCents < max(r.ResultCents, 0) {
			r.Lower = RegimeMicro
		}
		out = append(out, r)
	}
	return out
}

// Share applies an ownership share in basis points to an amount, to the
// cent half-up.
func Share(c money.Cents, bp int64) money.Cents {
	return money.Cents(halfUp(int64(c)*bp, FullShare))
}
