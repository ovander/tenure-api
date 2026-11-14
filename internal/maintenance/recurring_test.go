package maintenance

import (
	"testing"

	"github.com/ovander/tenure-api/internal/period"
)

func TestNextDue(t *testing.T) {
	d := func(s string) period.Date {
		v, err := period.ParseDate(s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	p := func(s string) *period.Date { v := d(s); return &v }
	cases := []struct {
		name     string
		first    string
		last     *period.Date
		interval int
		end      *period.Date
		want     string
		ok       bool
	}{
		{"never done", "2026-03-01", nil, 12, nil, "2026-03-01", true},
		{"done once", "2026-03-01", p("2026-02-20"), 12, nil, "2027-02-20", true},
		{"month end clamped", "2026-01-31", p("2026-08-31"), 6, nil, "2027-02-28", true},
		{"ended before", "2026-03-01", p("2026-02-20"), 12, p("2026-12-31"), "", false},
		{"ends that day", "2026-03-01", nil, 12, p("2026-03-01"), "2026-03-01", true},
	}
	for _, c := range cases {
		got, ok := NextDue(d(c.first), c.last, c.interval, c.end)
		if ok != c.ok || (ok && got.String() != c.want) {
			t.Errorf("%s: %v %v, want %s %v", c.name, got, ok, c.want, c.ok)
		}
	}
}
