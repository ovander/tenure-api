package calendar

import (
	"testing"

	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/period"
)

func d(s string) period.Date {
	v, err := period.ParseDate(s)
	if err != nil {
		panic(err)
	}
	return v
}

func TestTermEnd(t *testing.T) {
	for _, c := range []struct {
		l     Lease
		today string
		want  string
	}{
		{Lease{StartOn: d("2026-01-15"), TermMonths: 36}, "2026-11-07", "2029-01-14"},
		{Lease{StartOn: d("2023-01-15"), TermMonths: 36}, "2026-11-07", "2029-01-14"}, // renewed tacitly
		{Lease{StartOn: d("2025-12-01"), TermMonths: 12}, "2026-11-30", "2026-11-30"}, // the last day itself
		{Lease{StartOn: d("2024-01-31"), TermMonths: 1}, "2024-02-10", "2024-02-28"},  // clamped to February
		{Lease{Kind: "mobility", StartOn: d("2026-03-10"), TermMonths: 6}, "2026-11-07", ""},
		{Lease{StartOn: d("2026-03-10")}, "2026-11-07", ""},
	} {
		got, ok := termEnd(c.l, d(c.today))
		if (c.want == "") == ok || (ok && got.String() != c.want) {
			t.Errorf("%+v on %s: %s %v, want %q", c.l, c.today, got, ok, c.want)
		}
	}
}

func TestNextRevision(t *testing.T) {
	for _, c := range []struct {
		l     Lease
		today string
		want  string
	}{
		{Lease{StartOn: d("2026-01-15"), RevisionMonth: 1, IRLRefQuarter: "2025-Q3"}, "2026-11-07", "2027-01-15"},
		{Lease{StartOn: d("2024-03-31"), RevisionMonth: 2, IRLRefQuarter: "2025-Q4"}, "2026-01-01", "2026-02-28"}, // start day capped
		{Lease{StartOn: d("2024-03-31"), RevisionMonth: 2, IRLRefQuarter: "2025-Q4"}, "2026-03-01", "2027-02-28"}, // passed this year
		{Lease{StartOn: d("2026-01-15"), RevisionMonth: 0, IRLRefQuarter: "2025-Q3"}, "2026-11-07", ""},
		{Lease{StartOn: d("2026-01-15"), RevisionMonth: 1}, "2026-11-07", ""},
	} {
		got, ok := nextRevision(c.l, d(c.today))
		if (c.want == "") == ok || (ok && got.String() != c.want) {
			t.Errorf("%+v on %s: %s %v, want %q", c.l, c.today, got, ok, c.want)
		}
	}
}

func TestNextAnniversary(t *testing.T) {
	for _, c := range [][3]string{
		{"2024-02-29", "2026-01-01", "2026-02-28"},
		{"2026-01-15", "2026-11-07", "2027-01-15"},
		{"2025-11-07", "2026-11-07", "2026-11-07"},
		{"2026-11-01", "2026-11-07", "2027-11-01"}, // never the start itself
	} {
		if got := nextAnniversary(d(c[0]), d(c[1])); got.String() != c[2] {
			t.Errorf("%s on %s: %s, want %s", c[0], c[1], got, c[2])
		}
	}
}

func TestBuildWithoutFacts(t *testing.T) {
	items, err := Build(Facts{}, d("2026-11-07"), d("2027-02-05"), d("2026-11-07"), params.Set{})
	if err != nil || items == nil || len(items) != 0 {
		t.Errorf("%v %v", items, err)
	}
}
