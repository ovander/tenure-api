package bic

import (
	"errors"
	"testing"

	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/period"
)

func d(t *testing.T, s string) period.Date {
	t.Helper()
	v, err := period.ParseDate(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// A made-up grid: the values come from the parameters, not the code.
func grid(t *testing.T) Grid {
	return Grid{InServiceOn: d(t, "2026-07-01"), BasisCents: 100_001, Lines: []Line{
		{Component: "land", ShareBP: 2000, Months: 0},
		{Component: "walls", ShareBP: 5000, Months: 120},
		{Component: "fit", ShareBP: 3000, Months: 12},
	}}
}

func TestBasesAddUp(t *testing.T) {
	b := grid(t).Bases()
	if b[0]+b[1]+b[2] != 100_001 || b[0] != 20_000 || b[1] != 50_001 {
		t.Errorf("bases %v", b)
	}
}

func TestValidate(t *testing.T) {
	g := grid(t)
	g.Lines[0].ShareBP = 1999
	if err := g.Validate(); !errors.Is(err, ErrGrid) {
		t.Errorf("shares short of the full: %v", err)
	}
	g = grid(t)
	g.Lines = append(g.Lines, Line{Component: "fit", ShareBP: 0, Months: 1})
	if err := g.Validate(); !errors.Is(err, ErrGrid) {
		t.Errorf("a component twice: %v", err)
	}
}

func TestDepreciationClosesEachLine(t *testing.T) {
	g := grid(t)
	var total [3]money.Cents
	for year := 2026; year <= 2037; year++ {
		rows, err := Depreciation(&g, nil, year)
		if err != nil {
			t.Fatal(err)
		}
		for i, r := range rows {
			if r.YearCents < 0 || r.PriorCents != total[i] {
				t.Errorf("%d %s: %+v (prior %d)", year, r.Ref, r, total[i])
			}
			total[i] += r.YearCents
		}
		if year == 2026 && (rows[0].Depreciable || rows[0].YearCents != 0 || rows[2].YearCents <= rows[2].BaseCents/3) {
			t.Errorf("first year %+v", rows)
		}
		if year == 2027 && (rows[2].NetCents != 0 || rows[2].EndOn.String() != "2027-06-30") {
			t.Errorf("a 12-month line ends after 12 months: %+v", rows[2])
		}
	}
	b := g.Bases()
	if total[0] != 0 || total[1] != b[1] || total[2] != b[2] {
		t.Errorf("totals %v, bases %v", total, b)
	}
}

func TestItemsDisposedOf(t *testing.T) {
	gone := d(t, "2027-03-31")
	items := []Item{
		{ID: "b", Label: "Canapé", CostCents: 36_500, InServiceOn: d(t, "2026-01-01"), Months: 60, DisposedOn: &gone},
		{ID: "a", Label: "Lit", CostCents: 73_000, InServiceOn: d(t, "2026-01-01"), Months: 120},
	}
	rows, err := Depreciation(nil, items, 2027)
	if err != nil || len(rows) != 2 || rows[0].Ref != "a" {
		t.Fatalf("rows %+v %v", rows, err)
	}
	if rows[1].NetCents != 0 || rows[1].YearCents <= 0 || rows[1].YearCents >= rows[1].PriorCents {
		t.Errorf("disposed of in March: %+v", rows[1])
	}
	if Total(rows) != rows[0].YearCents+rows[1].YearCents {
		t.Errorf("total")
	}
	after, _ := Depreciation(nil, items, 2028)
	if after[1].YearCents != 0 {
		t.Errorf("nothing after disposal: %+v", after[1])
	}
}

func TestCapitalise(t *testing.T) {
	const threshold = 777 // made up
	cases := []struct {
		kind, works string
		cost        money.Cents
		override    string
		want        string
	}{
		{KindFurniture, "", 776, "", TreatmentExpense},
		{KindFurniture, "", 777, "", TreatmentAsset},
		{KindWorks, WorksRepair, 1_000_000, "", TreatmentExpense},
		{KindWorks, WorksImprovement, 1_000_000, "", TreatmentAsset},
		{KindWorks, WorksConstruction, 10, "", TreatmentAsset},
		{KindFurniture, "", 10, TreatmentAsset, TreatmentAsset},
		{KindWorks, WorksImprovement, 1_000_000, TreatmentExpense, TreatmentExpense},
	}
	for _, c := range cases {
		if got := Capitalise(c.kind, c.works, c.cost, threshold, c.override); got != c.want {
			t.Errorf("%+v: %s", c, got)
		}
	}
}
