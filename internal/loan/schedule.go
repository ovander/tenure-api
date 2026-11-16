// Package loan holds the loans that finance an asset and their amortisation
// schedule (RENT-SPEC-014): a schedule computed from the loan's terms or
// typed in from the bank's table, each instalment split between principal,
// interest and insurance. The tax summary reads the interest and insurance
// of the instalments due in the income year (line 250, Q-126). Schedule and
// Check are pure: no database, clock or environment.
package loan

import (
	"fmt"
	"math/big"

	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/period"
	"github.com/ovander/tenure-api/internal/problem"
)

// Problem types of a loan.
const (
	TypeScheduleInvalid = "loan/schedule-invalid"
	// TypePartsPersonOnly refuses a loan for SCI parts in an SCI's scope.
	TypePartsPersonOnly = "loan/parts-person-only"
)

func init() {
	problem.Register(map[string]problem.Message{
		TypeScheduleInvalid: problem.M(422, "Le tableau d’amortissement de cet emprunt est incohérent.", "This loan's amortisation table is inconsistent."),
		TypePartsPersonOnly: problem.M(409, "Un emprunt pour des parts de SCI est celui d’un associé : enregistrez-le dans son propre portefeuille, pas dans celui de la SCI.",
			"A loan for parts of an SCI is an associate's: record it in their own portfolio, not the SCI's."),
	})
}

// RateScale is the scale of an annual rate: parts per million of the
// principal a year, so 3.45 % is 34 500.
const RateScale = 1_000_000

// monthsPerYear turns an annual rate into the monthly rate a French bank
// applies (taux proportionnel, Q-126).
const monthsPerYear = 12

// MaxInstalments bounds a schedule: fifty years of monthly instalments.
const MaxInstalments = 600

// Terms are what a fixed-rate loan with monthly instalments is computed
// from.
type Terms struct {
	PrincipalCents money.Cents `json:"principal_cents"`
	// AnnualRatePPM is the nominal annual rate in parts per million.
	AnnualRatePPM int64 `json:"annual_rate_ppm"`
	TermMonths    int   `json:"term_months"`
	// FirstDueOn is the day of the first instalment; the next ones fall on
	// the same day of the following months (the month's last day when it
	// has no such day).
	FirstDueOn period.Date `json:"first_due_on" swaggertype:"string" format:"date"`
	// InsuranceCents is the borrower insurance paid with each instalment.
	InsuranceCents money.Cents `json:"insurance_cents"`
}

// Instalment is one line of an amortisation table. RemainingCents is the
// principal still owed once it is paid.
type Instalment struct {
	Seq            int         `json:"seq"`
	DueOn          period.Date `json:"due_on" swaggertype:"string" format:"date"`
	PrincipalCents money.Cents `json:"principal_cents"`
	InterestCents  money.Cents `json:"interest_cents"`
	InsuranceCents money.Cents `json:"insurance_cents"`
	RemainingCents money.Cents `json:"remaining_cents"`
}

// TotalCents is what the instalment costs the borrower.
func (i Instalment) TotalCents() money.Cents {
	return i.PrincipalCents + i.InterestCents + i.InsuranceCents
}

func invalid(format string, a ...any) error {
	return invariant.New(TypeScheduleInvalid, fmt.Sprintf(format, a...))
}

// Schedule computes the amortisation table of a fixed-rate loan repaid in
// constant monthly instalments (Q-126): the monthly rate is the annual rate
// divided by twelve; the constant instalment P·r / (1 − (1 + r)^−n) is
// computed exactly and rounded to the cent half-up; each month's interest is
// the balance times the monthly rate, to the cent half-up, and the
// principal the rest of the instalment; the last instalment repays the
// balance, so the principal always adds up. A zero rate repays the
// principal in equal parts, the remainder on the last one.
func Schedule(t Terms) ([]Instalment, error) {
	switch {
	case t.PrincipalCents <= 0:
		return nil, invalid("principal_cents must be positive")
	case t.TermMonths <= 0 || t.TermMonths > MaxInstalments:
		return nil, invalid("term_months must be between 1 and %d", MaxInstalments)
	case t.AnnualRatePPM < 0 || t.AnnualRatePPM >= RateScale:
		return nil, invalid("annual_rate_ppm must be between 0 and %d", RateScale-1)
	case t.InsuranceCents < 0:
		return nil, invalid("insurance_cents must not be negative")
	case t.FirstDueOn.IsZero():
		return nil, invalid("first_due_on is required")
	}
	n := t.TermMonths
	var constant money.Cents
	if t.AnnualRatePPM == 0 {
		c, err := money.MulDivFloor(t.PrincipalCents, 1, int64(n))
		if err != nil {
			return nil, err
		}
		constant = c
	} else {
		constant = annuity(t.PrincipalCents, t.AnnualRatePPM, n)
	}
	out := make([]Instalment, 0, n)
	remaining := t.PrincipalCents
	for k := 1; k <= n; k++ {
		interest, err := money.MulDivHalfUp(remaining, t.AnnualRatePPM, int64(RateScale)*monthsPerYear)
		if err != nil {
			return nil, err
		}
		principal := constant - interest
		if k == n || principal > remaining {
			principal = remaining
		}
		if principal < 0 {
			return nil, invalid("the instalment does not cover the interest")
		}
		remaining -= principal
		out = append(out, Instalment{Seq: k, DueOn: t.FirstDueOn.AddMonthsClamped(k - 1), PrincipalCents: principal,
			InterestCents: interest, InsuranceCents: t.InsuranceCents, RemainingCents: remaining})
	}
	return out, nil
}

// annuity returns P·r·q / (q − 1) with q = (1 + r)^n and r the monthly rate,
// rounded to the cent half-up, computed exactly with rationals.
func annuity(principal money.Cents, ratePPM int64, n int) money.Cents {
	r := big.NewRat(ratePPM, int64(RateScale)*monthsPerYear)
	one := big.NewRat(1, 1)
	base := new(big.Rat).Add(one, r)
	q := new(big.Rat).Set(one)
	for range n {
		q.Mul(q, base)
	}
	num := new(big.Rat).Mul(new(big.Rat).SetInt64(int64(principal)), r)
	num.Mul(num, q)
	a := num.Quo(num, new(big.Rat).Sub(q, one))
	// Half-up: floor(a + 1/2), a being positive.
	a.Add(a, big.NewRat(1, 2))
	return money.Cents(new(big.Int).Quo(a.Num(), a.Denom()).Int64())
}

// Check validates an amortisation table typed in or imported from the bank
// and numbers it (Q-126): at least one instalment and at most
// MaxInstalments, due on strictly increasing days, with no negative amount,
// whose principal adds up to the loan's. A deferral month repays no
// principal. It returns the table with its sequence and the balance after
// each instalment.
func Check(principal money.Cents, rows []Instalment) ([]Instalment, error) {
	if principal <= 0 {
		return nil, invalid("principal_cents must be positive")
	}
	if len(rows) == 0 || len(rows) > MaxInstalments {
		return nil, invalid("the table must have between 1 and %d instalments", MaxInstalments)
	}
	out := make([]Instalment, 0, len(rows))
	remaining := principal
	for i, r := range rows {
		switch {
		case r.DueOn.IsZero():
			return nil, invalid("instalments[%d]: due_on is required", i)
		case i > 0 && !r.DueOn.After(rows[i-1].DueOn):
			return nil, invalid("instalments[%d]: due_on must follow the previous instalment", i)
		case r.PrincipalCents < 0 || r.InterestCents < 0 || r.InsuranceCents < 0:
			return nil, invalid("instalments[%d]: no amount may be negative", i)
		case r.PrincipalCents > remaining:
			return nil, invalid("instalments[%d]: repays more than the balance", i)
		}
		remaining -= r.PrincipalCents
		out = append(out, Instalment{Seq: i + 1, DueOn: r.DueOn, PrincipalCents: r.PrincipalCents,
			InterestCents: r.InterestCents, InsuranceCents: r.InsuranceCents, RemainingCents: remaining})
	}
	if remaining != 0 {
		return nil, invalid("the instalments repay %d cents of a %d cent principal", principal-remaining, principal)
	}
	return out, nil
}

// Year is what a loan's instalments due in a calendar year add up to.
type Year struct {
	Year           int         `json:"year"`
	Instalments    int         `json:"instalments"`
	PrincipalCents money.Cents `json:"principal_cents"`
	InterestCents  money.Cents `json:"interest_cents"`
	InsuranceCents money.Cents `json:"insurance_cents"`
	// RemainingCents is the principal still owed at the year's end.
	RemainingCents money.Cents `json:"remaining_cents"`
}

// Years sums a schedule by the year its instalments fall due, in order.
func Years(rows []Instalment) []Year {
	out := []Year{}
	for _, r := range rows {
		if len(out) == 0 || out[len(out)-1].Year != r.DueOn.Year {
			out = append(out, Year{Year: r.DueOn.Year})
		}
		y := &out[len(out)-1]
		y.Instalments++
		y.PrincipalCents += r.PrincipalCents
		y.InterestCents += r.InterestCents
		y.InsuranceCents += r.InsuranceCents
		y.RemainingCents = r.RemainingCents
	}
	return out
}
