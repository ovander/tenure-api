package billing

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ovander/tenure-api/internal/invariant"
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

func withEntry(t *testing.T, ps params.Set, key string, from period.Date, v any) params.Set {
	t.Helper()
	b, _ := json.Marshal(v)
	out, err := ps.With(params.Entry{Key: key, EffectiveFrom: from, Value: b, Source: "synthetic test value"})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A synthetic ceiling table: 20,00 €/m² in zone Z1 category T2.
func withCeiling(t *testing.T, ps params.Set) params.Set {
	return withEntry(t, ps, params.KeyRentControlZoneCeiling, d(2025, 6, 1),
		params.ZoneCeiling{Zones: map[string]map[string]int64{"Z1": {"T2": 2000}}})
}

func controlledAsset(surface int64) portfolio.Asset {
	return portfolio.Asset{ID: "A1", Kind: portfolio.KindApartment, ControlledZone: true,
		RentControlZone: "Z1", RentControlCategory: "T2", SurfaceHundredthsM2: surface}
}

func TestRevise(t *testing.T) {
	ps := fixtures(t)
	l := baseLease()
	l.StartOn, l.RentCents, l.RevisionMonth = d(2023, 7, 1), 70000, time.July
	idx := Index{Quarter: "2025-Q2", Value: 14668, PublishedOn: d(2025, 7, 15)}
	steep := Index{Quarter: "2025-Q2", Value: 15200} // +4,70 %, synthetic
	ann := d(2025, 7, 1)
	bouclier := withEntry(t, ps, params.KeyIRLCap, d(2025, 6, 1), params.Cap{Applies: true, RateBP: 350})

	inZone := func(surface int64) lease.Lease {
		c := l
		c.Assets = []portfolio.Asset{controlledAsset(surface)}
		return c
	}
	cases := []struct {
		name      string
		l         lease.Lease
		idx       Index
		ps        params.Set
		from      period.Period
		newRent   money.Cents
		cappedBy  string
		forfeited bool
		above     bool
	}{
		// 70000 × 14668 ÷ 14517 = 70728.11
		{"uncapped", l, idx, ps, p(2025, 8), 70728, "", false, false},
		// 1,04 % is under the 3,5 % cap: no effect.
		{"cap in force, variation below it", l, idx, bouclier, p(2025, 8), 70728, "", false, false},
		// 70000 × 1,035 = 72450 instead of 70000 × 15200 ÷ 14517 = 73293.38
		{"cap in force, variation above it", l, steep, bouclier, p(2025, 8), 72450, params.KeyIRLCap, false, false},
		// 35,20 m² × 20,00 € = 704,00 € < 707,28 €: the ceiling bites.
		{"controlled zone, ceiling bites", inZone(3520), idx, withCeiling(t, ps), p(2025, 8), 70400, params.KeyRentControlZoneCeiling, false, false},
		// 40,00 m² × 20,00 € = 800,00 €: room left.
		{"controlled zone, ceiling does not bite", inZone(4000), idx, withCeiling(t, ps), p(2025, 8), 70728, "", false, false},
		// 30,00 m² × 20,00 € = 600,00 € < 700,00 €: no increase.
		{"rent already above the ceiling", inZone(3000), idx, withCeiling(t, ps), p(2025, 8), 70000, params.KeyRentControlZoneCeiling, false, true},
		{"applied 12 months later still allowed", l, idx, ps, p(2026, 7), 70728, "", false, false},
		{"more than 12 months late is forfeited", l, idx, ps, p(2026, 8), 70000, "", true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, err := Revise(c.l, c.idx, ann, c.from, c.ps)
			if err != nil {
				t.Fatal(err)
			}
			if r.NewRentCents != c.newRent || r.CappedBy != c.cappedBy || r.Forfeited != c.forfeited || r.AboveCeiling != c.above {
				t.Errorf("got new %d capped %q forfeited %v above %v; want %d %q %v %v",
					r.NewRentCents, r.CappedBy, r.Forfeited, r.AboveCeiling, c.newRent, c.cappedBy, c.forfeited, c.above)
			}
			applied := r.Apply(c.l)
			if applied.RentCents != c.newRent || applied.IRLRefQuarter != "2025-Q2" || applied.IRLRefValue != c.idx.Value {
				t.Errorf("Apply gave %+v", applied)
			}
		})
	}
}

func TestReviseLeaseSignedOn29February(t *testing.T) {
	l := baseLease()
	l.StartOn, l.RentCents, l.RevisionMonth = d(2024, 2, 29), 70000, time.February
	l.IRLRefQuarter, l.IRLRefValue = "2023-Q4", 14259
	idx := Index{Quarter: "2024-Q4", Value: 14470, PublishedOn: d(2025, 1, 15)}
	// The first anniversary in a common year is 28 February, twelve months
	// after the start once the month is capped.
	r, err := Revise(l, idx, d(2025, 2, 28), p(2025, 3), fixtures(t))
	if err != nil || r.Forfeited || r.NewRentCents == l.RentCents {
		t.Fatalf("revision on 28 February: %+v, %v", r, err)
	}
	// Applied twelve months after that anniversary, it is still allowed.
	if r, err := Revise(l, idx, d(2025, 2, 28), p(2026, 2), fixtures(t)); err != nil || r.Forfeited {
		t.Errorf("applied in February of the next year: %+v, %v", r, err)
	}
}

func TestReviseAboveCeilingStillFalls(t *testing.T) {
	l := baseLease()
	l.StartOn, l.RentCents = d(2023, 7, 1), 70000
	l.Assets = []portfolio.Asset{controlledAsset(3000)} // ceiling 600,00 €
	r, err := Revise(l, Index{Quarter: "2025-Q2", Value: 14400}, d(2025, 7, 1), p(2025, 8), withCeiling(t, fixtures(t)))
	if err != nil || r.NewRentCents != 69436 || !r.AboveCeiling || r.CappedBy != "" {
		t.Errorf("falling index above the ceiling: %+v, %v", r, err)
	}
}

func TestReviseUnknownZoneFailsClosed(t *testing.T) {
	l := baseLease()
	l.StartOn, l.RentCents = d(2023, 7, 1), 70000
	a := controlledAsset(4000)
	a.RentControlZone = "Z9"
	l.Assets = []portfolio.Asset{a}
	_, err := Revise(l, Index{Quarter: "2025-Q2", Value: 14668}, d(2025, 7, 1), p(2025, 8), withCeiling(t, fixtures(t)))
	if !invariant.Is(err, lease.TypeZoneCeilingUnknown) {
		t.Errorf("unknown zone: %v; want %s", err, lease.TypeZoneCeilingUnknown)
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
