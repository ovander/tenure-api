package sci

import (
	"testing"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/period"
)

func d(s string) period.Date {
	v, err := period.ParseDate(s)
	if err != nil {
		panic(err)
	}
	return v
}

func dp(s string) *period.Date {
	v := d(s)
	return &v
}

func TestPartsOn(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	// a holds 60 and b 40 from the start; a gives 20 to b on 1 July, which
	// closes a's holding and opens a 40 for a and a 20 for b on that day.
	hs := []Holding{
		{Associate: a, Parts: 60, From: d("2024-01-10"), Closed: dp("2025-07-01")},
		{Associate: b, Parts: 40, From: d("2024-01-10")},
		{Associate: a, Parts: 40, From: d("2025-07-01")},
		{Associate: b, Parts: 20, From: d("2025-07-01")},
	}
	cases := []struct {
		day       string
		a, b, sum int
	}{
		{"2024-01-09", 0, 0, 0},
		{"2024-01-10", 60, 40, 100},
		{"2025-06-30", 60, 40, 100},
		{"2025-07-01", 40, 60, 100},
		{"2025-12-31", 40, 60, 100},
	}
	for _, c := range cases {
		by, sum := PartsOn(hs, d(c.day))
		if by[a] != c.a || by[b] != c.b || sum != c.sum {
			t.Errorf("%s: a %d b %d total %d, want %d %d %d", c.day, by[a], by[b], sum, c.a, c.b, c.sum)
		}
	}
}

func TestHeldOnBounds(t *testing.T) {
	h := Holding{Parts: 1, From: d("2025-03-01"), Closed: dp("2025-03-02")}
	if !h.HeldOn(d("2025-03-01")) || h.HeldOn(d("2025-03-02")) || h.HeldOn(d("2025-02-28")) {
		t.Fatal("a holding covers its first day and stops the day it is closed")
	}
}

func TestMaxHeldFrom(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	hs := []Holding{
		{Associate: a, Parts: 50, From: d("2024-01-01")},
		{Associate: b, Parts: 30, From: d("2026-01-01")},
		{Associate: b, Parts: 10, From: d("2025-01-01"), Closed: dp("2025-06-01")},
	}
	cases := []struct {
		day  string
		want int
	}{
		{"2023-01-01", 80}, // the later holdings count
		{"2024-06-01", 80},
		{"2025-02-01", 80},
		{"2026-06-01", 80},
	}
	for _, c := range cases {
		if got := MaxHeldFrom(hs, d(c.day)); got != c.want {
			t.Errorf("%s: %d, want %d", c.day, got, c.want)
		}
	}
	if got := MaxHeldFrom(hs[:1], d("2024-01-01")); got != 50 {
		t.Errorf("one holding: %d", got)
	}
	if got := MaxHeldFrom(nil, d("2024-01-01")); got != 0 {
		t.Errorf("none: %d", got)
	}
}

func TestAllocate(t *testing.T) {
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	cases := []struct {
		name    string
		f       Figures
		holders []Holder
		want    [][4]money.Cents // income, interest, other, net per holder
	}{
		{"thirds, a profit", Figures{IncomeCents: 1000000, InterestCents: 100000, OtherExpensesCents: 200001, NetCents: 699999},
			[]Holder{{a, 1}, {b, 1}, {c, 1}},
			[][4]money.Cents{{333334, 33334, 66667, 233333}, {333333, 33333, 66667, 233333}, {333333, 33333, 66667, 233333}}},
		{"60/40, a deficit", Figures{IncomeCents: 500000, InterestCents: 300000, OtherExpensesCents: 400001, NetCents: -200001},
			[]Holder{{a, 600}, {b, 400}},
			[][4]money.Cents{{300000, 180000, 240001, -120001}, {200000, 120000, 160000, -80000}}},
		{"a holder with no part is left out", Figures{IncomeCents: 100, NetCents: 100},
			[]Holder{{a, 0}, {b, 3}},
			[][4]money.Cents{{100, 0, 0, 100}}},
		{"a negative figure (a refund) is split as its opposite", Figures{IncomeCents: 0, OtherExpensesCents: -101, NetCents: 101},
			[]Holder{{a, 1}, {b, 1}},
			[][4]money.Cents{{0, 0, -51, 51}, {0, 0, -50, 50}}},
	}
	for _, tc := range cases {
		got, err := Allocate(tc.f, tc.holders)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(got) != len(tc.want) {
			t.Fatalf("%s: %d allocations", tc.name, len(got))
		}
		var net money.Cents
		for i, g := range got {
			w := tc.want[i]
			if g.IncomeCents != w[0] || g.InterestCents != w[1] || g.OtherExpensesCents != w[2] || g.NetCents != w[3] {
				t.Errorf("%s: holder %d %+v, want %v", tc.name, i, g.Figures, w)
			}
			net += g.NetCents
		}
		if net != tc.f.NetCents {
			t.Errorf("%s: nets add up to %d, not %d", tc.name, net, tc.f.NetCents)
		}
	}
	if _, err := Allocate(Figures{IncomeCents: 1}, []Holder{{a, 0}}); err != ErrNoParts {
		t.Errorf("nobody holds a part: %v", err)
	}
}
