package sci

import (
	"testing"

	"github.com/google/uuid"

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
