package lease

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/period"
	"github.com/ovander/tenure-api/internal/portfolio"
)

func paramsWithCeiling(t *testing.T) params.Set {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "..", "testdata", "params", "2025.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	ps, err := params.Load(f)
	if err != nil {
		t.Fatal(err)
	}
	// Synthetic table: 20,00 €/m² in zone Z1 category T2.
	b, _ := json.Marshal(params.ZoneCeiling{Zones: map[string]map[string]int64{"Z1": {"T2": 2000}}})
	ps, err = ps.With(params.Entry{Key: params.KeyRentControlZoneCeiling, EffectiveFrom: period.NewDate(2025, 6, 1), Value: b, Source: "synthetic test value"})
	if err != nil {
		t.Fatal(err)
	}
	return ps
}

var (
	o1   = []portfolio.Share{{OwnerID: "O1", BP: 10000}}
	half = []portfolio.Share{{OwnerID: "O2", BP: 5000}, {OwnerID: "O1", BP: 5000}}
)

func flat(id string, controlled bool, surface int64, shares []portfolio.Share) portfolio.Asset {
	a := portfolio.Asset{ID: id, Kind: portfolio.KindApartment, SurfaceHundredthsM2: surface, Shares: shares}
	if controlled {
		a.ControlledZone, a.RentControlZone, a.RentControlCategory = true, "Z1", "T2"
	}
	return a
}

// tense is a dwelling in a zone tendue with no rent ceiling and no surface.
func tense(id string) portfolio.Asset {
	return portfolio.Asset{ID: id, Kind: portfolio.KindApartment, TenseZone: true, Shares: o1}
}

func types(errs []*invariant.Error) []string {
	var out []string
	for _, e := range errs {
		out = append(out, e.Type)
	}
	return out
}

func TestValidate(t *testing.T) {
	ps := paramsWithCeiling(t)
	asOf := period.NewDate(2025, 9, 1)
	box := portfolio.Asset{ID: "B", Kind: portfolio.KindBox, ParentAssetID: "A", Shares: o1}
	prev := &Previous{RentCents: 60000, IRLRefValue: 14517, IRLLatestValue: 14668} // limit 606,24 €

	cases := []struct {
		name string
		l    Lease
		c    Check
		want []string
	}{
		{"uncontrolled, consistent", Lease{RentCents: 90000, Assets: []portfolio.Asset{flat("A", false, 4000, o1), box}},
			Check{Event: EventCreation, AsOf: asOf, Previous: prev}, nil},
		{"shares differ between apartment and box", Lease{RentCents: 50000, Assets: []portfolio.Asset{flat("A", false, 4000, half), box}},
			Check{Event: EventUpdate, AsOf: asOf}, []string{TypeConsistentShares}},
		{"same shares in another order", Lease{RentCents: 50000, Assets: []portfolio.Asset{
			flat("A", false, 4000, half), {ID: "B", Kind: portfolio.KindBox, Shares: []portfolio.Share{half[1], half[0]}}}},
			Check{Event: EventUpdate, AsOf: asOf}, nil},
		// 40,00 m² × 20,00 € = 800,00 €
		{"ceiling bites at creation", Lease{RentCents: 80001, Assets: []portfolio.Asset{flat("A", true, 4000, o1)}},
			Check{Event: EventCreation, AsOf: asOf}, []string{TypeZoneCeilingExceeded}},
		{"ceiling met at renewal", Lease{RentCents: 80000, Assets: []portfolio.Asset{flat("A", true, 4000, o1)}},
			Check{Event: EventRenewal, AsOf: asOf}, nil},
		{"ceiling not checked on an ordinary update", Lease{RentCents: 90000, Assets: []portfolio.Asset{flat("A", true, 4000, o1)}},
			Check{Event: EventUpdate, AsOf: asOf}, nil},
		{"breakdown keeps the box out of the ceiling", Lease{RentCents: 85000, Assets: []portfolio.Asset{flat("A", true, 4000, o1), box},
			RentBreakdown: []AssetRent{{"A", 79000}, {"B", 6000}}}, Check{Event: EventCreation, AsOf: asOf}, nil},
		{"breakdown that does not add up", Lease{RentCents: 85000, Assets: []portfolio.Asset{flat("A", false, 4000, o1), box},
			RentBreakdown: []AssetRent{{"A", 79000}}}, Check{Event: EventUpdate, AsOf: asOf}, []string{TypeRentBreakdown}},
		{"unknown zone fails closed", Lease{RentCents: 1, Assets: []portfolio.Asset{{ID: "A", Kind: portfolio.KindApartment, ControlledZone: true, RentControlZone: "Z9", RentControlCategory: "T2", SurfaceHundredthsM2: 4000}}},
			Check{Event: EventCreation, AsOf: asOf}, []string{TypeZoneCeilingUnknown}},
		{"no surface fails closed", Lease{RentCents: 1, Assets: []portfolio.Asset{flat("A", true, 0, o1)}},
			Check{Event: EventCreation, AsOf: asOf}, []string{TypeZoneCeilingUnknown}},
		// 600,00 € × 146,68 ÷ 145,17 = 606,24 €
		{"relocation rule bites", Lease{RentCents: 60625, Assets: []portfolio.Asset{flat("A", true, 4000, o1)}},
			Check{Event: EventCreation, AsOf: asOf, Previous: prev}, []string{TypeRelocationExceeded}},
		{"relocation rule met", Lease{RentCents: 60624, Assets: []portfolio.Asset{flat("A", true, 4000, o1)}},
			Check{Event: EventCreation, AsOf: asOf, Previous: prev}, nil},
		{"works exemption lifts the relocation rule", Lease{RentCents: 75000, RentExemption: ExemptionWorks, Assets: []portfolio.Asset{flat("A", true, 4000, o1)}},
			Check{Event: EventCreation, AsOf: asOf, Previous: prev}, nil},
		{"exemption does not lift the ceiling", Lease{RentCents: 85000, RentExemption: ExemptionUnderValued, Assets: []portfolio.Asset{flat("A", true, 4000, o1)}},
			Check{Event: EventCreation, AsOf: asOf, Previous: prev}, []string{TypeZoneCeilingExceeded}},
		// Q-102: a zone tendue without a ceiling (Baillargues) has the relocation
		// rule but no ceiling; outside any zone, neither applies.
		{"tense zone: relocation rule bites", Lease{RentCents: 60625, Assets: []portfolio.Asset{tense("A")}},
			Check{Event: EventCreation, AsOf: asOf, Previous: prev}, []string{TypeRelocationExceeded}},
		{"tense zone: relocation rule met, no ceiling", Lease{RentCents: 60624, Assets: []portfolio.Asset{tense("A")}},
			Check{Event: EventCreation, AsOf: asOf, Previous: prev}, nil},
		{"outside any zone: no relocation rule", Lease{RentCents: 99999, Assets: []portfolio.Asset{flat("A", false, 4000, o1)}},
			Check{Event: EventCreation, AsOf: asOf, Previous: prev}, nil},
		{"unknown exemption", Lease{RentCents: 1, RentExemption: "friendship", Assets: []portfolio.Asset{flat("A", false, 4000, o1)}},
			Check{Event: EventUpdate, AsOf: asOf}, []string{TypeRentExemptionUnknown}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			errs, err := Validate(c.l, c.c, ps)
			if err != nil {
				t.Fatal(err)
			}
			got := types(errs)
			if len(got) != len(c.want) {
				t.Fatalf("got %v; want %v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("got %v; want %v", got, c.want)
				}
			}
		})
	}
}

func TestZoneCeilingNoTableValueIsAnError(t *testing.T) {
	// The 2025 fixture's table is empty: a controlled asset cannot be validated.
	f, err := os.Open(filepath.Join("..", "..", "testdata", "params", "2025.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	ps, err := params.Load(f)
	if err != nil {
		t.Fatal(err)
	}
	_, applies, inv, err := ZoneCeiling(Lease{Assets: []portfolio.Asset{flat("A", true, 4000, o1)}}, period.NewDate(2025, 9, 1), ps)
	if err != nil || !applies || inv == nil || inv.Type != TypeZoneCeilingUnknown {
		t.Errorf("applies %v inv %v err %v", applies, inv, err)
	}
}
