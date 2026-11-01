// Package billing computes and issues the monthly fee, rent calls, receipts,
// revisions and arrears (RENT-SPEC-003). The calculations in this slice —
// ComputeFee and Revise — are pure functions with no I/O: the caller passes
// the lease, the period, the parameter set and any regularisation line.
package billing

import (
	"errors"
	"fmt"

	"github.com/ovander/tenure-api/internal/lease"
	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/period"
)

// ErrNotBillable is returned for a lease that is not billed for the period:
// not active or under notice, or not occupied on any day of the period.
var ErrNotBillable = errors.New("billing: lease not billable for the period")

// LineKind identifies a line of the monthly fee.
type LineKind string

// Fee line kinds. Labels are rendered by the French document templates.
const (
	LineRent           LineKind = "rent"
	LineProvision      LineKind = "provision"
	LineRegularisation LineKind = "regularisation"
)

// Line is one line of the monthly fee.
type Line struct {
	Kind        LineKind    `json:"kind"`
	AmountCents money.Cents `json:"amount_cents"`
	// BaseCents is the monthly amount before proration (rent and provision).
	BaseCents money.Cents `json:"base_cents,omitempty"`
	// Reference names the regularisation statement; Instalment/Of say which
	// monthly instalment this is when the balance is spread.
	Reference  string `json:"reference,omitempty"`
	Instalment int    `json:"instalment,omitempty"`
	Of         int    `json:"of,omitempty"`
}

// Regularisation is the charge-regularisation amount due on this fee
// (RENT-SPEC-007 step 7), computed by the charge module: positive when the
// tenant owes, negative for a credit. For a spread balance it is the amount of
// one instalment.
type Regularisation struct {
	Reference   string      `json:"reference"`
	AmountCents money.Cents `json:"amount_cents"`
	Instalment  int         `json:"instalment,omitempty"`
	Of          int         `json:"of,omitempty"`
}

// FeeOptions carries the optional inputs of ComputeFee.
type FeeOptions struct {
	Regularisation *Regularisation
}

// Fee is the computed monthly fee of one lease for one period.
type Fee struct {
	LeaseID      string        `json:"lease_id"`
	Period       period.Period `json:"period"`
	OccupiedDays int           `json:"occupied_days"`
	DaysInPeriod int           `json:"days_in_period"`
	Prorated     bool          `json:"prorated"`
	Lines        []Line        `json:"lines"`
	TotalCents   money.Cents   `json:"total_cents"`
	// CarryOverCents is the part of a credit that did not fit on this fee and
	// moves to the next one (always <= 0, OPEN-QUESTIONS Q-17).
	CarryOverCents money.Cents `json:"carry_over_cents"`
}

// ComputeFee returns the fee of lease l for period p (RENT-SPEC-003): monthly
// rent plus provisional charges, both prorated by days in the first and last
// month, plus the regularisation line when one is supplied. Proration uses the
// actual days of the month, the start and end days counting as occupied, each
// line rounded half-up to the cent (OPEN-QUESTIONS Q-11). A credit larger than
// the rent and provision is capped so the fee is never negative, the rest
// being returned as carry-over (Q-17). ps is accepted so that every fee is
// computed against an explicit parameter set; no parameter changes the fee
// itself in phase 1.
func ComputeFee(l lease.Lease, p period.Period, ps params.Set, opts FeeOptions) (Fee, error) {
	_ = ps
	if !l.State.Billable() {
		return Fee{}, fmt.Errorf("%w: state %s", ErrNotBillable, l.State)
	}
	if l.StartOn.IsZero() {
		return Fee{}, errors.New("billing: lease has no start date")
	}
	if l.RentCents < 0 || l.ProvisionCents < 0 {
		return Fee{}, errors.New("billing: negative rent or provision")
	}
	days := p.Days()
	occupied := period.OverlapDays(l.StartOn, l.EndOn, p.First(), p.Last())
	if occupied == 0 {
		return Fee{}, fmt.Errorf("%w: not occupied in %s", ErrNotBillable, p)
	}
	f := Fee{LeaseID: l.ID, Period: p, OccupiedDays: occupied, DaysInPeriod: days, Prorated: occupied < days}

	rent, err := money.MulDivHalfUp(l.RentCents, int64(occupied), int64(days))
	if err != nil {
		return Fee{}, err
	}
	prov, err := money.MulDivHalfUp(l.ProvisionCents, int64(occupied), int64(days))
	if err != nil {
		return Fee{}, err
	}
	f.Lines = append(f.Lines, Line{Kind: LineRent, AmountCents: rent, BaseCents: l.RentCents})
	if l.ProvisionCents > 0 {
		f.Lines = append(f.Lines, Line{Kind: LineProvision, AmountCents: prov, BaseCents: l.ProvisionCents})
	}
	f.TotalCents = rent + prov

	if r := opts.Regularisation; r != nil && r.AmountCents != 0 {
		amount := r.AmountCents
		if amount < 0 && -amount > f.TotalCents {
			f.CarryOverCents = amount + f.TotalCents
			amount = -f.TotalCents
		}
		f.Lines = append(f.Lines, Line{Kind: LineRegularisation, AmountCents: amount,
			Reference: r.Reference, Instalment: r.Instalment, Of: r.Of})
		f.TotalCents += amount
	}
	return f, nil
}
