package period

import (
	"encoding/json"
	"testing"
	"time"
)

func TestPeriodDays(t *testing.T) {
	cases := map[string]int{"2025-01": 31, "2025-02": 28, "2024-02": 29, "2025-04": 30, "2025-12": 31}
	for s, want := range cases {
		p, err := Parse(s)
		if err != nil {
			t.Fatal(err)
		}
		if p.Days() != want || p.String() != s {
			t.Errorf("%s: Days()=%d String()=%s; want %d", s, p.Days(), p.String(), want)
		}
	}
	for _, bad := range []string{"2025-1", "2025-13", "25-01", "2025-01-01", ""} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) accepted", bad)
		}
	}
	if got := (Period{2025, time.December}).Next(); got != (Period{2026, time.January}) {
		t.Errorf("Next = %v", got)
	}
}

func TestOverlapDays(t *testing.T) {
	d := func(s string) Date {
		v, err := ParseDate(s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	mar := Period{2025, time.March}
	cases := []struct {
		name       string
		start, end Date
		want       int
	}{
		{"mid-month start", d("2025-03-15"), Date{}, 17},
		{"first day", d("2025-03-01"), Date{}, 31},
		{"last day start", d("2025-03-31"), Date{}, 1},
		{"ends on the 10th", d("2024-01-01"), d("2025-03-10"), 10},
		{"starts and ends in month", d("2025-03-05"), d("2025-03-06"), 2},
		{"before month", d("2024-01-01"), d("2025-02-28"), 0},
		{"after month", d("2025-04-01"), Date{}, 0},
	}
	for _, c := range cases {
		if got := OverlapDays(c.start, c.end, mar.First(), mar.Last()); got != c.want {
			t.Errorf("%s: %d; want %d", c.name, got, c.want)
		}
	}
}

func TestDateJSON(t *testing.T) {
	var v struct {
		D Date   `json:"d"`
		E Date   `json:"e"`
		P Period `json:"p"`
	}
	if err := json.Unmarshal([]byte(`{"d":"2025-03-15","e":"","p":"2025-03"}`), &v); err != nil {
		t.Fatal(err)
	}
	if v.D != (Date{2025, time.March, 15}) || !v.E.IsZero() || v.P != (Period{2025, time.March}) {
		t.Fatalf("decoded %+v", v)
	}
	b, _ := json.Marshal(v)
	if string(b) != `{"d":"2025-03-15","e":"","p":"2025-03"}` {
		t.Errorf("encoded %s", b)
	}
	if err := json.Unmarshal([]byte(`{"d":"2025-02-30"}`), &v); err == nil {
		t.Error("invalid date accepted")
	}
}
