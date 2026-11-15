package bic

import (
	"sort"

	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/params"
)

// YearInput is an owner's furnished letting for a calendar year, their
// shares applied: the receipts, the charges other than depreciation and the
// year's depreciation (Q-132).
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
}

// Micro is the micro-BIC figure of a year: eligible when the receipts do
// not exceed the threshold; the taxable result is the receipts less the
// abatement, which is never below its minimum nor above the receipts.
type Micro struct {
	Eligible       bool        `json:"eligible"`
	AbatementCents money.Cents `json:"abatement_cents"`
	TaxableCents   money.Cents `json:"taxable_cents"`
}

// YearResult is the réel result of a year and its micro comparison.
type YearResult struct {
	YearInput
	// CarriedInCents is the depreciation carried from earlier years.
	CarriedInCents money.Cents `json:"carried_in_cents"`
	// RoomCents is what the depreciation may take: receipts less the other
	// charges, never below zero (CGI art. 39 C, II).
	RoomCents       money.Cents `json:"room_cents"`
	DeductedCents   money.Cents `json:"deducted_cents"`
	CarriedOutCents money.Cents `json:"carried_out_cents"`
	// ResultCents is receipts − charges − depreciation deducted; negative only
	// through the other charges (a deficit).
	ResultCents money.Cents `json:"result_cents"`
	Micro       Micro       `json:"micro"`
	// Lower is the regime with the lower taxable result: micro or reel; reel
	// when micro is not open.
	Lower string `json:"lower" enums:"micro,reel"`
}

// Results chains the years in order (RENT-SPEC-019, Q-132). Each year the
// depreciation available is the year's plus what earlier years carried;
// it is deducted up to the room (receipts less the other charges, CGI
// art. 39 C, II), carried earlier amounts first, and the rest is carried to
// the next year without time limit. The micro figure is computed beside
// with the parameter in force that year. Pure: the caller passes the years
// and the micro parameter of each.
func Results(years []YearInput, micro func(year int) params.BICMicro) []YearResult {
	sorted := append([]YearInput(nil), years...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Year < sorted[j].Year })
	out := make([]YearResult, 0, len(sorted))
	var carried money.Cents
	for _, y := range sorted {
		r := YearResult{YearInput: y, CarriedInCents: carried}
		if room := y.ReceiptsCents - y.ChargesCents; room > 0 {
			r.RoomCents = room
		}
		available := carried + y.DepreciationCents
		r.DeductedCents = min(available, r.RoomCents)
		r.CarriedOutCents = available - r.DeductedCents
		carried = r.CarriedOutCents
		r.ResultCents = y.ReceiptsCents - y.ChargesCents - r.DeductedCents
		m := micro(y.Year)
		r.Micro.Eligible = int64(y.ReceiptsCents) <= m.ThresholdCents
		ab := money.Cents(halfUp(int64(y.ReceiptsCents)*m.AbatementBP, FullShare))
		ab = max(ab, money.Cents(m.MinimumAbatementCents))
		r.Micro.AbatementCents = min(ab, max(y.ReceiptsCents, 0))
		r.Micro.TaxableCents = y.ReceiptsCents - r.Micro.AbatementCents
		r.Lower = "reel"
		if r.Micro.Eligible && r.Micro.TaxableCents < r.ResultCents {
			r.Lower = "micro"
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
