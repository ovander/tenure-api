package billing

import (
	"testing"
	"time"

	"github.com/ovander/tenure-api/internal/lease"
	"github.com/ovander/tenure-api/internal/period"
)

func TestDueRevision(t *testing.T) {
	series := func(published period.Date, quarters ...string) func(string) (Index, bool) {
		return func(q string) (Index, bool) {
			for _, x := range quarters {
				if x == q {
					return Index{Quarter: q, Value: 14668, PublishedOn: published}, true
				}
			}
			return Index{}, false
		}
	}
	july := lease.Lease{ID: "L", StartOn: d(2023, 7, 1), IRLRefQuarter: "2024-Q2", IRLRefValue: 14517, RevisionMonth: time.July}
	mid := lease.Lease{ID: "M", StartOn: d(2025, 3, 15), IRLRefQuarter: "2024-Q4", IRLRefValue: 14464, RevisionMonth: time.March}
	cases := []struct {
		name  string
		l     lease.Lease
		s     func(string) (Index, bool)
		today period.Date
		ok    bool
		ann   period.Date
		from  period.Period
	}{
		{"index not published yet", july, series(d(2025, 7, 15)), d(2025, 8, 1), false, period.Date{}, period.Period{}},
		{"published, run on the 1st after", july, series(d(2025, 7, 15), "2025-Q2"), d(2025, 8, 1), true, d(2025, 7, 1), p(2025, 8)},
		{"fetched on the 15th: next month", july, series(d(2025, 7, 15), "2025-Q2"), d(2025, 7, 15), true, d(2025, 7, 1), p(2025, 8)},
		{"published before the anniversary", july, series(d(2025, 4, 15), "2025-Q2"), d(2025, 7, 1), true, d(2025, 7, 1), p(2025, 7)},
		{"before the anniversary", july, series(d(2025, 4, 15), "2025-Q2"), d(2025, 6, 30), false, period.Date{}, period.Period{}},
		{"first anniversary of a mid-month start", mid, series(d(2025, 4, 15), "2025-Q4"), d(2026, 3, 20), true, d(2026, 3, 15), p(2026, 4)},
		{"not before twelve months", mid, series(d(2025, 4, 15), "2025-Q4"), d(2026, 3, 14), false, period.Date{}, period.Period{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			due, ok, err := DueRevision(c.l, c.s, c.today)
			if err != nil {
				t.Fatal(err)
			}
			if ok != c.ok || (ok && (due.Anniversary != c.ann || due.From != c.from)) {
				t.Errorf("got %+v %v; want ann %s from %s ok %v", due, ok, c.ann, c.from, c.ok)
			}
		})
	}
	// Once revised, the reference moves on and the same revision is not due again.
	revised := july
	revised.IRLRefQuarter = "2025-Q2"
	if _, ok, _ := DueRevision(revised, series(d(2025, 7, 15), "2025-Q2"), d(2025, 9, 1)); ok {
		t.Error("the same revision is due twice")
	}
}
