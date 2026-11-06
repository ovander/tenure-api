package inventory

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
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

func pd(s string) *period.Date { v := d(s); return &v }

// rules reads the restitution parameters of the legal fixtures
// (testdata/params), so no figure is written here.
func rules(t *testing.T) params.DepositReturn {
	t.Helper()
	r, err := fixtures(t).DepositReturn(d("2026-01-01"))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// fixtures is the parameter set of the legal fixtures (testdata/params).
func fixtures(t *testing.T) params.Set {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "..", "testdata", "params", "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no parameter fixtures: %v", err)
	}
	var all []params.Entry
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		set, err := params.Load(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, set.Entries()...)
	}
	s, err := params.New(all...)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestConditionScale(t *testing.T) {
	order := []Condition{New, Good, Fair, Poor, Broken}
	for i := 1; i < len(order); i++ {
		if order[i-1].rank() <= order[i].rank() {
			t.Errorf("%s should rank above %s", order[i-1], order[i])
		}
	}
	if Condition("").Valid() || Condition("worn").Valid() {
		t.Error("an empty or unknown condition is not on the scale")
	}
}

func TestCompareEveryResult(t *testing.T) {
	entry := Inventory{Kind: Entry, Items: []Item{
		{ID: "e1", Condition: Good}, {ID: "e2", Condition: Good, Works: WorksOK}, {ID: "e3", Condition: Fair},
		{ID: "e4", Condition: Fair, Works: WorksKO}, {ID: "e5", Category: Furniture, Condition: Good, Quantity: 4}, {ID: "e6", Condition: Good},
	}, Keys: []Key{{Kind: "key", Destination: "porte", Quantity: 2}, {Kind: "badge", Destination: "hall", Quantity: 1}}}
	exit := Inventory{Kind: Exit, Items: []Item{
		{ID: "x1", EntryItemID: "e1", Condition: Good},
		{ID: "x2", EntryItemID: "e2", Condition: Good, Works: WorksKO},
		{ID: "x3", EntryItemID: "e3", Condition: Good},
		{ID: "x4", EntryItemID: "e4", Condition: Fair, Works: WorksOK},
		{ID: "x5", EntryItemID: "e5", Category: Furniture, Condition: Good, Quantity: 3},
		{ID: "x6", EntryItemID: "e6", Condition: Broken},
		{ID: "x7", Condition: Good},
	}, Keys: []Key{{Kind: "key", Destination: "porte", Quantity: 3}, {Kind: "remote", Destination: "portail", Quantity: 1}}}
	got := Compare(entry, nil, exit)
	want := []Result{Same, Worse, Better, Better, Missing, Worse, NewItem}
	for i, r := range got.Items {
		if r.Result != want[i] {
			t.Errorf("%s: %s, want %s", r.ExitItemID, r.Result, want[i])
		}
	}
	if got.Items[5].Steps != 3 || got.Items[2].Steps != -1 {
		t.Errorf("steps: %d and %d", got.Items[5].Steps, got.Items[2].Steps)
	}
	keys := []Result{Better, Missing, NewItem}
	for i, k := range got.Keys {
		if k.Result != keys[i] {
			t.Errorf("key %s: %s, want %s", k.Kind, k.Result, keys[i])
		}
	}
	if got.Flagged != 4 {
		t.Errorf("flagged %d, want 4 (two worse, missing furniture, missing badge)", got.Flagged)
	}
	// An accepted completion line moves the entry state; a refused one does not.
	c := Compare(entry, []Completion{{EntryItemID: "e6", Condition: Broken, Accepted: true}, {EntryItemID: "e2", Works: WorksKO}}, exit)
	if c.Items[5].Result != Same || c.Items[1].Result != Worse {
		t.Errorf("with completions: %s and %s", c.Items[5].Result, c.Items[1].Result)
	}
}

func TestPrefillBothWays(t *testing.T) {
	entry := Inventory{ID: "ee", Kind: Entry, DoneOn: pd("2026-01-02"), Items: []Item{{ID: "ee/a", ElementID: "a", Condition: Good, Photos: 2}},
		Keys: []Key{{Kind: "key", Destination: "porte", Quantity: 2}}, Meters: []Meter{{Kind: "water_cold", MeterNumber: "W1", Reading: "123,4"}}}
	exit := Prefill("es", entry, nil)
	it := exit.Items[0]
	if exit.Kind != Exit || it.ID != "es/a" || it.EntryItemID != "ee/a" || !it.ToCheck || it.Photos != 0 || exit.EntryDoneOn == nil {
		t.Errorf("exit pre-fill %+v", exit)
	}
	if exit.Meters[0].Reading != "" || exit.Meters[0].MeterNumber != "W1" {
		t.Errorf("a meter keeps its number, not its reading: %+v", exit.Meters[0])
	}
	next := Prefill("ee2", exit, nil)
	if next.Kind != Entry || next.Items[0].EntryItemID != "" || next.Items[0].ToCheck || next.EntryDoneOn != nil {
		t.Errorf("next entry %+v", next)
	}
}

func TestValidateStages(t *testing.T) {
	inv := Inventory{Kind: Exit, DoneOn: pd("2026-10-31"), Rooms: []Room{{ID: "r", Label: "Box"}, {ID: "o", Kind: OutsideKind, Label: "Jardin"}},
		Items: []Item{{RoomID: "r", Category: Floor, Condition: Good}, {RoomID: "r", Category: Walls, Condition: Good},
			{RoomID: "r", Category: Ceiling, Condition: Good}, {RoomID: "o", Category: Other, Condition: Fair}},
		Keys: []Key{{Kind: "key", Quantity: 1}}, Parties: []Party{{Party: "tenant", Name: "L", Signed: true}}}
	c := Validate(inv)
	if c.Blocks(StageComplete) || !c.Blocks(StageIssue) {
		t.Fatalf("an exit without address, key return and entry date completes but is not issued: %+v", c)
	}
	inv.TenantNewAddress, inv.KeysReturnedOn, inv.EntryDoneOn = "ailleurs", pd("2026-10-31"), pd("2026-01-02")
	if c := Validate(inv); c.Blocks(StageIssue) {
		t.Errorf("complete exit still blocked: %+v", c)
	}
	inv.Keys = []Key{{Kind: "key", Quantity: 0}}
	if c := Validate(inv); !c.Blocks(StageComplete) || c.Blocking[0].Code != CodeNoKey {
		t.Errorf("no key line must block: %+v", c)
	}
}

func TestDeductRefusals(t *testing.T) {
	g := &Grid{Lines: []GridLine{{Category: Walls, LifetimeYears: 10, YearlyRateBP: 1000}}}
	in := DeductionInput{Category: Floor, CostCents: 100, LeaseStart: d("2020-01-01"), ExitOn: d("2026-01-01")}
	if _, err := Deduct(in, g); !errors.Is(err, ErrNoGridLine) {
		t.Errorf("no line for floor: %v", err)
	}
	in.Category, in.CostCents = Walls, -1
	if _, err := Deduct(in, g); err == nil {
		t.Error("a negative cost is refused")
	}
	in.CostCents = 100
	if _, err := Deduct(in, &Grid{Lines: []GridLine{{Category: Walls, FloorBP: 10_001}}}); err == nil {
		t.Error("a floor above 100 % is refused")
	}
	// A rate that would go below zero stops at the floor (here zero).
	r, err := Deduct(in, &Grid{Lines: []GridLine{{Category: Walls, YearlyRateBP: 5000}}})
	if err != nil || r.ShareBP != 0 || r.AmountCents != 0 {
		t.Errorf("share below zero: %+v %v", r, err)
	}
}

func TestFullYears(t *testing.T) {
	for _, c := range []struct {
		from, to string
		want     int
	}{{"2020-02-29", "2021-02-28", 0}, {"2020-02-29", "2021-03-01", 1}, {"2026-05-01", "2026-04-30", 0}, {"2016-06-01", "2026-06-01", 10}} {
		if got := FullYears(d(c.from), d(c.to)); got != c.want {
			t.Errorf("FullYears(%s, %s) = %d, want %d", c.from, c.to, got, c.want)
		}
	}
}

func TestLatePenaltyBoundaries(t *testing.T) {
	r := rules(t)
	deadline := d("2026-03-31")
	if p, _ := LatePenalty(deadline, deadline, 80000, true, r); p.StartedMonths != 0 {
		t.Errorf("paid on the deadline: %+v", p)
	}
	if p, _ := LatePenalty(deadline, d("2026-04-01"), 80000, true, r); p.StartedMonths != 1 {
		t.Errorf("one day late is one started month: %+v", p)
	}
	if p, _ := LatePenalty(deadline, d("2026-04-30"), 80000, true, r); p.StartedMonths != 1 {
		t.Errorf("a full month late is still one: %+v", p)
	}
	if p, _ := LatePenalty(deadline, d("2026-05-01"), 80000, true, r); p.StartedMonths != 2 {
		t.Errorf("the next day starts a second month: %+v", p)
	}
	if _, err := SettlementDeadline(period.Date{}, false, r); err == nil {
		t.Error("no deadline without the return of the keys")
	}
}

func TestSeedRefusals(t *testing.T) {
	tp := Template{AssetKind: "box", Rooms: []TemplateRoom{{Kind: "box", Label: "Box", Repeat: "floor"}}}
	if _, err := Seed(tp, Asset{Kind: "house"}); err == nil {
		t.Error("a template of another kind is refused")
	}
	if _, err := Seed(tp, Asset{Kind: "box"}); err == nil {
		t.Error("an unknown repeat is refused")
	}
	if _, err := Seed(Template{AssetKind: "box"}, Asset{Kind: "box", Bedrooms: -1}); err == nil {
		t.Error("a negative bedroom count is refused")
	}
}
