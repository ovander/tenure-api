package lease

import (
	"testing"

	"github.com/ovander/tenure-api/internal/period"
)

func dp(s string) *period.Date { d := day(s); return &d }

func TestRenewsTacitly(t *testing.T) {
	for _, c := range []struct {
		kind   Kind
		months int
		want   bool
	}{
		{KindUnfurnished, 36, true}, {KindUnfurnished, 72, true}, {KindFurnished, 12, true},
		{KindFurnished, 9, false}, {KindMobility, 6, false}, {KindBox, 12, false},
	} {
		if got := RenewsTacitly(c.kind, c.months, 9); got != c.want {
			t.Errorf("RenewsTacitly(%s, %d) = %v", c.kind, c.months, got)
		}
	}
}

func TestTerms(t *testing.T) {
	today := day("2026-11-08")
	v := Terms(TermFacts{StartOn: day("2020-01-01"), TermMonths: 36, Renews: true}, today)
	if len(v.Terms) != 3 || v.Terms[1].Renewal != RenewalTacit || v.Terms[2].Status != TermCurrent || v.Terms[2].EndOn != day("2028-12-31") ||
		v.NextRenewalOn == nil || *v.NextRenewalOn != day("2029-01-01") {
		t.Errorf("renewing lease %+v", v)
	}
	v = Terms(TermFacts{StartOn: day("2020-01-01"), TermMonths: 36, Renews: true, EndOn: dp("2027-03-14")}, today)
	if len(v.Terms) != 3 || !v.Terms[2].EndedEarly || v.Terms[2].EndOn != day("2027-03-14") || v.NextRenewalOn != nil {
		t.Errorf("under notice %+v", v)
	}
	v = Terms(TermFacts{StartOn: day("2026-01-01"), TermMonths: 6, Renews: false}, today)
	if len(v.Terms) != 1 || !v.BeyondTerm || v.Terms[0].Status != TermPast {
		t.Errorf("mobility past its term %+v", v)
	}
	v = Terms(TermFacts{StartOn: day("2027-01-01"), TermMonths: 12, Renews: true}, today)
	if len(v.Terms) != 1 || v.Terms[0].Status != TermUpcoming || v.NextRenewalOn == nil {
		t.Errorf("draft %+v", v)
	}
	v = Terms(TermFacts{StartOn: day("2023-10-01"), TermMonths: 12, Renews: true, EndOn: dp("2024-09-30")}, today)
	if len(v.Terms) != 1 || v.Terms[0].EndedEarly || v.Terms[0].Status != TermPast {
		t.Errorf("ended at its term %+v", v)
	}
}

func TestVacancies(t *testing.T) {
	today := day("2026-11-08")
	occ := []Occupancy{
		{StartOn: day("2025-03-01"), EndOn: dp("2025-12-31")},
		{StartOn: day("2024-01-15"), EndOn: dp("2025-01-31")},
		{StartOn: day("2026-02-01"), EndOn: dp("2026-10-31")},
	}
	v, by := Vacancies(occ, day("2024-01-01"), today)
	want := []Vacancy{
		{FromOn: day("2024-01-01"), ToOn: day("2024-01-14"), Days: 14},
		{FromOn: day("2025-02-01"), ToOn: day("2025-02-28"), Days: 28},
		{FromOn: day("2026-01-01"), ToOn: day("2026-01-31"), Days: 31},
		{FromOn: day("2026-11-01"), ToOn: today, Days: 8, Open: true},
	}
	if len(v) != len(want) {
		t.Fatalf("vacancies %+v", v)
	}
	for i := range want {
		if v[i] != want[i] {
			t.Errorf("vacancy %d = %+v, want %+v", i, v[i], want[i])
		}
	}
	if by[2024] != 14 || by[2025] != 28 || by[2026] != 39 {
		t.Errorf("days by year %v", by)
	}
	// Across a year end, an open lease, overlapping leases, no lease at all.
	v, by = Vacancies([]Occupancy{{StartOn: day("2025-12-20"), EndOn: nil}}, day("2025-12-01"), today)
	if len(v) != 1 || v[0].Days != 19 || by[2025] != 19 {
		t.Errorf("before an open lease %+v %v", v, by)
	}
	v, _ = Vacancies([]Occupancy{{StartOn: day("2025-01-01"), EndOn: dp("2025-06-30")}, {StartOn: day("2025-03-01"), EndOn: dp("2025-05-31")}}, period.Date{}, day("2025-07-02"))
	if len(v) != 1 || v[0].FromOn != day("2025-07-01") || v[0].Days != 2 || !v[0].Open {
		t.Errorf("overlapping %+v", v)
	}
	v, by = Vacancies(nil, day("2026-12-30"), day("2027-01-02"))
	if len(v) != 1 || v[0].Days != 4 || by[2026] != 2 || by[2027] != 2 {
		t.Errorf("never let %+v %v", v, by)
	}
	if v, _ := Vacancies(nil, period.Date{}, today); len(v) != 0 {
		t.Errorf("nothing known %+v", v)
	}
}
