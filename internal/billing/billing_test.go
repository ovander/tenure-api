package billing

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ovander/tenure-api/internal/lease"
	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/period"
	"github.com/ovander/tenure-api/internal/portfolio"
)

func fixtures(t *testing.T) params.Set {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "..", "testdata", "params", "2025.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	s, err := params.Load(f)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func d(y int, m time.Month, day int) period.Date { return period.NewDate(y, m, day) }
func p(y int, m time.Month) period.Period        { return period.Period{Year: y, Month: m} }

func baseLease() lease.Lease {
	return lease.Lease{
		ID: "L1", Kind: lease.KindUnfurnished, State: lease.StateActive,
		Assets:  []portfolio.Asset{{ID: "A1", Kind: portfolio.KindApartment}},
		StartOn: d(2025, 3, 15), RentCents: 85000, ProvisionCents: 9000, DueDay: 5,
		IRLRefQuarter: "2024-Q2", IRLRefValue: 14517, RevisionMonth: time.March,
	}
}

func TestComputeFee(t *testing.T) {
	ps := fixtures(t)
	ended := baseLease()
	ended.State, ended.EndOn = lease.StateNotice, d(2025, 6, 10)
	cases := []struct {
		name  string
		l     lease.Lease
		per   period.Period
		reg   *Regularisation
		rent  money.Cents
		prov  money.Cents
		total money.Cents
		carry money.Cents
		ratio [2]int
	}{
		{"first month from the 15th of 31", baseLease(), p(2025, 3), nil, 46613, 4935, 51548, 0, [2]int{17, 31}},
		{"full month", baseLease(), p(2025, 4), nil, 85000, 9000, 94000, 0, [2]int{30, 30}},
		{"last month to the 10th of 30", ended, p(2025, 6), nil, 28333, 3000, 31333, 0, [2]int{10, 30}},
		{"regularisation debit", baseLease(), p(2025, 4), &Regularisation{Reference: "RG-A1-2025-1", AmountCents: 2527, Instalment: 1, Of: 12}, 85000, 9000, 96527, 0, [2]int{30, 30}},
		{"credit within the fee", baseLease(), p(2025, 4), &Regularisation{Reference: "RG", AmountCents: -10000}, 85000, 9000, 84000, 0, [2]int{30, 30}},
		{"credit above the fee carries over", baseLease(), p(2025, 4), &Regularisation{Reference: "RG", AmountCents: -100000}, 85000, 9000, 0, -6000, [2]int{30, 30}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, err := ComputeFee(c.l, c.per, ps, FeeOptions{Regularisation: c.reg})
			if err != nil {
				t.Fatal(err)
			}
			if f.Lines[0].AmountCents != c.rent || f.Lines[1].AmountCents != c.prov {
				t.Errorf("rent %d prov %d; want %d %d", f.Lines[0].AmountCents, f.Lines[1].AmountCents, c.rent, c.prov)
			}
			if f.TotalCents != c.total || f.CarryOverCents != c.carry {
				t.Errorf("total %d carry %d; want %d %d", f.TotalCents, f.CarryOverCents, c.total, c.carry)
			}
			if f.OccupiedDays != c.ratio[0] || f.DaysInPeriod != c.ratio[1] || f.Prorated != (c.ratio[0] < c.ratio[1]) {
				t.Errorf("days %d/%d prorated %v", f.OccupiedDays, f.DaysInPeriod, f.Prorated)
			}
			var sum money.Cents
			for _, l := range f.Lines {
				sum += l.AmountCents
			}
			if sum != f.TotalCents || f.TotalCents < 0 {
				t.Errorf("lines add to %d, total %d", sum, f.TotalCents)
			}
		})
	}
}

func TestComputeFeeLeapFebruary(t *testing.T) {
	l := baseLease()
	l.StartOn = d(2024, 2, 15)
	f, err := ComputeFee(l, p(2024, 2), fixtures(t), FeeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if f.OccupiedDays != 15 || f.DaysInPeriod != 29 || f.Lines[0].AmountCents != 43966 { // 85000×15/29 = 43965.52
		t.Errorf("got %+v", f)
	}
}

func TestComputeFeeNotBillable(t *testing.T) {
	ps := fixtures(t)
	for _, s := range []lease.State{lease.StateDraft, lease.StateEnded, lease.StateClosed} {
		l := baseLease()
		l.State = s
		if _, err := ComputeFee(l, p(2025, 4), ps, FeeOptions{}); !errors.Is(err, ErrNotBillable) {
			t.Errorf("state %s: %v; want ErrNotBillable", s, err)
		}
	}
	if _, err := ComputeFee(baseLease(), p(2025, 2), ps, FeeOptions{}); !errors.Is(err, ErrNotBillable) {
		t.Errorf("month before start: %v; want ErrNotBillable", err)
	}
	neg := baseLease()
	neg.RentCents = -1
	if _, err := ComputeFee(neg, p(2025, 4), ps, FeeOptions{}); err == nil {
		t.Error("negative rent accepted")
	}
}

func withCap(t *testing.T, ps params.Set, key string, v any) params.Set {
	t.Helper()
	b, _ := json.Marshal(v)
	out, err := ps.With(params.Entry{Key: key, EffectiveFrom: d(2025, 6, 1), Value: b, Source: "synthetic test value"})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRevise(t *testing.T) {
	ps := fixtures(t)
	l := baseLease()
	l.StartOn, l.RentCents, l.RevisionMonth = d(2023, 7, 1), 70000, time.July
	idx := Index{Quarter: "2025-Q2", Value: 14668, PublishedOn: d(2025, 7, 15)}
	ann := d(2025, 7, 1)
	five := int64(50)

	controlled := l
	controlled.Assets = []portfolio.Asset{{ID: "A1", Kind: portfolio.KindApartment, ControlledZone: true}}

	cases := []struct {
		name      string
		l         lease.Lease
		ps        params.Set
		from      period.Period
		newRent   money.Cents
		cappedBy  string
		forfeited bool
	}{
		// 70000 × 14668 ÷ 14517 = 70728.11
		{"uncapped", l, ps, p(2025, 8), 70728, "", false},
		{"national cap 0.5 %", l, withCap(t, ps, params.KeyIRLCap, params.Cap{Applies: true, RateBP: 50}), p(2025, 8), 70350, params.KeyIRLCap, false},
		{"zone rule ignored outside a controlled zone", l, withCap(t, ps, params.KeyRentControlZoneRules, params.ZoneRule{MaxVariationBP: &five}), p(2025, 8), 70728, "", false},
		{"zone rule in a controlled zone", controlled, withCap(t, ps, params.KeyRentControlZoneRules, params.ZoneRule{MaxVariationBP: &five}), p(2025, 8), 70350, params.KeyRentControlZoneRules, false},
		{"applied 12 months later still allowed", l, ps, p(2026, 7), 70728, "", false},
		{"more than 12 months late is forfeited", l, ps, p(2026, 8), 70000, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, err := Revise(c.l, idx, ann, c.from, c.ps)
			if err != nil {
				t.Fatal(err)
			}
			if r.NewRentCents != c.newRent || r.CappedBy != c.cappedBy || r.Forfeited != c.forfeited {
				t.Errorf("got new %d capped %q forfeited %v; want %d %q %v", r.NewRentCents, r.CappedBy, r.Forfeited, c.newRent, c.cappedBy, c.forfeited)
			}
			applied := r.Apply(c.l)
			if applied.RentCents != c.newRent || applied.IRLRefQuarter != "2025-Q2" || applied.IRLRefValue != 14668 {
				t.Errorf("Apply gave %+v", applied)
			}
		})
	}
}

func TestReviseDecreaseAndErrors(t *testing.T) {
	ps := fixtures(t)
	l := baseLease()
	l.StartOn, l.RentCents = d(2023, 7, 1), 70000
	r, err := Revise(l, Index{Quarter: "2025-Q2", Value: 14400}, d(2025, 7, 1), p(2025, 8), ps)
	if err != nil || r.NewRentCents != 69436 { // 70000 × 14400 ÷ 14517 = 69435.83
		t.Errorf("decrease: %+v, %v", r, err)
	}
	bad := []struct {
		name string
		idx  Index
		ann  period.Date
		from period.Period
	}{
		{"other quarter", Index{Quarter: "2025-Q3", Value: 14600}, d(2025, 7, 1), p(2025, 8)},
		{"same year as reference", Index{Quarter: "2024-Q2", Value: 14600}, d(2025, 7, 1), p(2025, 8)},
		{"malformed quarter", Index{Quarter: "2025Q2", Value: 14600}, d(2025, 7, 1), p(2025, 8)},
		{"zero index", Index{Quarter: "2025-Q2", Value: 0}, d(2025, 7, 1), p(2025, 8)},
		{"first anniversary not reached", Index{Quarter: "2025-Q2", Value: 14600}, d(2024, 6, 1), p(2024, 7)},
		{"applied before the anniversary", Index{Quarter: "2025-Q2", Value: 14600}, d(2025, 7, 1), p(2025, 6)},
	}
	for _, c := range bad {
		if _, err := Revise(l, c.idx, c.ann, c.from, ps); err == nil {
			t.Errorf("%s: accepted", c.name)
		}
	}
}
