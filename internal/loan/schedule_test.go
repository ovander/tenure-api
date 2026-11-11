package loan

import (
	"errors"
	"testing"

	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/period"
)

func sum(rows []Instalment, f func(Instalment) money.Cents) money.Cents {
	var s money.Cents
	for _, r := range rows {
		s += f(r)
	}
	return s
}

func TestScheduleConstantInstalment(t *testing.T) {
	// 200 000 € at 4 % over 25 years: the textbook 1 055,67 € a month.
	rows, err := Schedule(Terms{PrincipalCents: 20_000_000, AnnualRatePPM: 40_000, TermMonths: 300,
		FirstDueOn: period.NewDate(2025, 1, 31), InsuranceCents: 2_500})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 300 {
		t.Fatalf("instalments = %d", len(rows))
	}
	first := rows[0]
	if first.InterestCents != 66_667 || first.PrincipalCents+first.InterestCents != 105_567 || first.InsuranceCents != 2_500 {
		t.Fatalf("first = %+v", first)
	}
	if got := sum(rows, func(i Instalment) money.Cents { return i.PrincipalCents }); got != 20_000_000 {
		t.Fatalf("principal adds up to %d", got)
	}
	last := rows[len(rows)-1]
	if last.RemainingCents != 0 || last.Seq != 300 {
		t.Fatalf("last = %+v", last)
	}
	for _, r := range rows[:len(rows)-1] {
		if r.PrincipalCents+r.InterestCents != 105_567 {
			t.Fatalf("instalment %d is %d", r.Seq, r.PrincipalCents+r.InterestCents)
		}
	}
	if d := last.PrincipalCents + last.InterestCents - 105_567; d > 300 || d < -300 {
		t.Fatalf("last instalment off by %d cents", d)
	}
	// The day is kept, clamped to a short month.
	if rows[1].DueOn != period.NewDate(2025, 2, 28) || rows[2].DueOn != period.NewDate(2025, 3, 31) {
		t.Fatalf("due days %s %s", rows[1].DueOn, rows[2].DueOn)
	}
}

func TestScheduleZeroRate(t *testing.T) {
	rows, err := Schedule(Terms{PrincipalCents: 1_000_000, TermMonths: 3, FirstDueOn: period.NewDate(2025, 5, 5)})
	if err != nil {
		t.Fatal(err)
	}
	want := []money.Cents{333_333, 333_333, 333_334}
	for i, r := range rows {
		if r.PrincipalCents != want[i] || r.InterestCents != 0 {
			t.Fatalf("row %d = %+v", i, r)
		}
	}
}

func TestScheduleRefusals(t *testing.T) {
	d := period.NewDate(2025, 1, 1)
	for name, tc := range map[string]Terms{
		"no principal": {TermMonths: 12, FirstDueOn: d},
		"no term":      {PrincipalCents: 100, FirstDueOn: d},
		"long term":    {PrincipalCents: 100, TermMonths: MaxInstalments + 1, FirstDueOn: d},
		"rate":         {PrincipalCents: 100, TermMonths: 12, AnnualRatePPM: RateScale, FirstDueOn: d},
		"insurance":    {PrincipalCents: 100, TermMonths: 12, InsuranceCents: -1, FirstDueOn: d},
		"no first day": {PrincipalCents: 100, TermMonths: 12},
	} {
		if _, err := Schedule(tc); !isInvalid(err) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func isInvalid(err error) bool {
	var e *invariant.Error
	return errors.As(err, &e) && e.Type == TypeScheduleInvalid
}

func TestCheck(t *testing.T) {
	rows := []Instalment{
		{DueOn: period.NewDate(2025, 1, 10), InterestCents: 500},
		{DueOn: period.NewDate(2025, 2, 10), PrincipalCents: 600, InterestCents: 500, InsuranceCents: 20},
		{DueOn: period.NewDate(2026, 3, 10), PrincipalCents: 400, InterestCents: 2},
	}
	out, err := Check(1_000, rows)
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Seq != 1 || out[0].RemainingCents != 1_000 || out[1].RemainingCents != 400 || out[2].RemainingCents != 0 {
		t.Fatalf("out = %+v", out)
	}
	ys := Years(out)
	if len(ys) != 2 || ys[0].InterestCents != 1_000 || ys[0].InsuranceCents != 20 || ys[0].RemainingCents != 400 || ys[1].Instalments != 1 {
		t.Fatalf("years = %+v", ys)
	}
	for name, bad := range map[string][]Instalment{
		"empty":     {},
		"short":     rows[:2],
		"order":     {rows[1], rows[0], rows[2]},
		"negative":  {{DueOn: rows[0].DueOn, PrincipalCents: 1_000, InterestCents: -1}},
		"over":      {{DueOn: rows[0].DueOn, PrincipalCents: 1_001}},
		"no due on": {{PrincipalCents: 1_000}},
	} {
		if _, err := Check(1_000, bad); !isInvalid(err) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
