package bic

import (
	"testing"

	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/params"
)

// Made-up micro parameters: the values come from legal_params.
func micro(int) params.BICMicro {
	return params.BICMicro{AbatementBP: 4000, MinimumAbatementCents: 100, ThresholdCents: 1_000_000}
}

func TestResultsCapAndCarry(t *testing.T) {
	rs := Results([]YearInput{
		{Year: 2027, ReceiptsCents: 500_000, ChargesCents: 100_000, DepreciationCents: 300_000},
		{Year: 2026, ReceiptsCents: 400_000, ChargesCents: 150_000, DepreciationCents: 400_000},
		{Year: 2028, ReceiptsCents: 100_000, ChargesCents: 200_000, DepreciationCents: 50_000},
	}, micro, 3)
	if len(rs) != 3 || rs[0].Year != 2026 {
		t.Fatalf("order %+v", rs)
	}
	// 2026: room 250 000, 400 000 available: 250 000 deducted, 150 000 carried.
	if r := rs[0]; r.RoomCents != 250_000 || r.DeductedCents != 250_000 || r.CarriedOutCents != 150_000 || r.ResultCents != 0 {
		t.Errorf("2026 %+v", r)
	}
	// 2027: room 400 000, 150 000 + 300 000 available: 400 000 deducted, 50 000 carried.
	if r := rs[1]; r.CarriedInCents != 150_000 || r.DeductedCents != 400_000 || r.CarriedOutCents != 50_000 || r.ResultCents != 0 {
		t.Errorf("2027 %+v", r)
	}
	// 2028: no room, a deficit from the charges; all depreciation carried.
	if r := rs[2]; r.RoomCents != 0 || r.DeductedCents != 0 || r.CarriedOutCents != 100_000 || r.ResultCents != -100_000 {
		t.Errorf("2028 %+v", r)
	}
}

func TestResultsMicro(t *testing.T) {
	rs := Results([]YearInput{
		{Year: 2026, ReceiptsCents: 200, ChargesCents: 0},
		{Year: 2027, ReceiptsCents: 500_000, ChargesCents: 400_000},
		{Year: 2028, ReceiptsCents: 2_000_000, ChargesCents: 0},
	}, micro, 3)
	if m := rs[0].Micro; m.AbatementCents != 100 || m.TaxableCents != 100 {
		t.Errorf("the minimum abatement: %+v", m)
	}
	if rs[1].Micro.TaxableCents != 300_000 || rs[1].Lower != "reel" {
		t.Errorf("réel lower: %+v", rs[1])
	}
	if rs[2].Micro.Eligible || rs[2].Lower != "reel" {
		t.Errorf("above the threshold: %+v", rs[2])
	}
	if Share(1001, 5000) != 501 {
		t.Errorf("share half-up")
	}
}

func TestResultsDeficits(t *testing.T) {
	// A made-up lifetime of 2 years: a 2026 deficit is used in 2027 and
	// 2028, then lapses.
	rs := Results([]YearInput{
		{Year: 2026, ReceiptsCents: 100_000, ChargesCents: 400_000},
		{Year: 2027, ReceiptsCents: 200_000, ChargesCents: 100_000},
		{Year: 2028, ReceiptsCents: 150_000, ChargesCents: 50_000},
		{Year: 2029, ReceiptsCents: 500_000, ChargesCents: 0},
	}, micro, 2)
	if r := rs[0]; r.ResultCents != -300_000 || len(r.Deficits) != 1 || r.TaxableCents != 0 {
		t.Errorf("2026 %+v", r)
	}
	if r := rs[1]; r.DeficitUsedCents != 100_000 || r.TaxableCents != 0 || r.Deficits[0].RemainingCents != 200_000 {
		t.Errorf("2027 %+v", r)
	}
	if r := rs[2]; r.DeficitUsedCents != 100_000 || r.Deficits[0].RemainingCents != 100_000 {
		t.Errorf("2028 %+v", r)
	}
	if r := rs[3]; r.DeficitUsedCents != 0 || r.TaxableCents != 500_000 || len(r.Deficits) != 0 {
		t.Errorf("2029: the deficit lapsed %+v", r)
	}
}

func TestResultsMicroYearAndStored(t *testing.T) {
	stored := YearResult{YearInput: YearInput{Year: 2026, Regime: RegimeReel}, CarriedOutCents: 70_000,
		Deficits: []Deficit{{Year: 2026, RemainingCents: 5_000}}}
	rs := Results([]YearInput{
		{Year: 2026, ReceiptsCents: 1, Stored: &stored},
		{Year: 2027, ReceiptsCents: 300_000, ChargesCents: 0, DepreciationCents: 40_000, Regime: RegimeMicro},
		{Year: 2028, ReceiptsCents: 100_000, ChargesCents: 0, DepreciationCents: 10_000},
	}, micro, 5)
	if !rs[0].Declared || rs[0].CarriedOutCents != 70_000 {
		t.Errorf("stored %+v", rs[0])
	}
	// The micro year: its own depreciation not carried, the carry passed on,
	// the deficit not used.
	if r := rs[1]; r.CarriedInCents != 70_000 || r.CarriedOutCents != 70_000 || r.DeductedCents != 0 || r.TaxableCents != r.Micro.TaxableCents || r.Deficits[0].RemainingCents != 5_000 {
		t.Errorf("micro year %+v", r)
	}
	// Back under the réel: 70 000 + 10 000 available, all deducted, then the deficit.
	if r := rs[2]; r.DeductedCents != 80_000 || r.ResultCents != 20_000 || r.DeficitUsedCents != 5_000 || r.TaxableCents != 15_000 {
		t.Errorf("réel again %+v", r)
	}
}

// A share rounds a negative amount like the matching positive one, halves
// away from zero: a deficit share is never a cent smaller than the profit
// share of the same size (Q-18).
func TestShareRoundsSymmetrically(t *testing.T) {
	for _, c := range []struct {
		amount money.Cents
		bp     int64
		want   money.Cents
	}{
		{15, FullShare / 2, 8},
		{-15, FullShare / 2, -8},
		{-14, FullShare / 2, -7},
		{-1, FullShare / 3, 0},
		{-2, FullShare / 3, -1},
	} {
		if got := Share(c.amount, c.bp); got != c.want {
			t.Errorf("Share(%d, %d) = %d, want %d", c.amount, c.bp, got, c.want)
		}
	}
}
