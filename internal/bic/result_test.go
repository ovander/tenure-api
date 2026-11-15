package bic

import (
	"testing"

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
	}, micro)
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
	}, micro)
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
