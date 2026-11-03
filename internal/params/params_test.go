package params

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ovander/tenure-api/internal/period"
)

func fixtures(t *testing.T) Set {
	t.Helper()
	var files []*os.File
	for _, y := range []string{"2025", "2026"} {
		f, err := os.Open(filepath.Join("..", "..", "testdata", "params", y+".json"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.Close() })
		files = append(files, f)
	}
	s, err := Load(files[0], files[1])
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func d(y int, m time.Month, day int) period.Date { return period.NewDate(y, m, day) }

func TestFixtures(t *testing.T) {
	s := fixtures(t)
	want := map[string]int64{
		KeyMicroFoncierThreshold: 1500000,
		KeyMicroFoncierAllowance: 30,
		KeyForfait222:            2000,
		KeyDeficitGlobalCap:      1070000,
		KeyDeficitCarryForward:   10,
		KeyDepositMaxUnfurnished: 1,
		KeyDepositMaxFurnished:   2,
		KeyRegularisationSpread:  12,
	}
	for _, asOf := range []period.Date{d(2025, 1, 1), d(2025, 12, 31), d(2026, 6, 30)} {
		for k, v := range want {
			got, err := s.Int64(k, asOf)
			if err != nil || got != v {
				t.Errorf("%s on %s = %d, %v; want %d", k, asOf, got, err, v)
			}
		}
	}
	for _, e := range s.Entries() {
		if e.Source == "" {
			t.Errorf("%s has no source", e.Key)
		}
	}
}

func TestLookupEffectiveDates(t *testing.T) {
	v := func(n int) json.RawMessage { b, _ := json.Marshal(n); return b }
	s, err := New(
		Entry{Key: "k", EffectiveFrom: d(2025, 1, 1), Value: v(1), Source: "a"},
		Entry{Key: "k", EffectiveFrom: d(2026, 7, 1), Value: v(2), Source: "b"},
	)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		asOf period.Date
		want int64
		err  bool
	}{
		{d(2024, 12, 31), 0, true},
		{d(2025, 1, 1), 1, false},
		{d(2026, 6, 30), 1, false},
		{d(2026, 7, 1), 2, false},
		{d(2030, 1, 1), 2, false},
	}
	for _, c := range cases {
		got, err := s.Int64("k", c.asOf)
		if (err != nil) != c.err || got != c.want {
			t.Errorf("Int64(k, %s) = %d, %v; want %d (error %v)", c.asOf, got, err, c.want, c.err)
		}
		if c.err && !errors.Is(err, ErrNotFound) {
			t.Errorf("error %v is not ErrNotFound", err)
		}
	}
	if got := s.Until(d(2025, 12, 31)); len(got.Entries()) != 1 {
		t.Errorf("Until kept %d entries; want 1", len(got.Entries()))
	}
}

func TestNewRejects(t *testing.T) {
	ok := Entry{Key: "k", EffectiveFrom: d(2025, 1, 1), Value: json.RawMessage(`1`), Source: "s"}
	noSource := ok
	noSource.Source = ""
	noDate := ok
	noDate.EffectiveFrom = period.Date{}
	if _, err := New(ok, ok); err == nil {
		t.Error("duplicate accepted")
	}
	if _, err := New(noSource); err == nil {
		t.Error("entry without source accepted")
	}
	if _, err := New(noDate); err == nil {
		t.Error("entry without date accepted")
	}
	badMap := Entry{Key: KeyForm2044LineMap, EffectiveFrom: d(2025, 1, 1), Value: json.RawMessage(`{"millesime":2026,"lines":{"rent":"211"},"income_lines":["211"],"interest_lines":["250"]}`), Source: "s"}
	if _, err := New(badMap); err == nil {
		t.Error("line map without income year accepted")
	}
}

func TestForm2044LineMap(t *testing.T) {
	s := fixtures(t)
	m, err := s.Form2044LineMap(2025)
	if err != nil {
		t.Fatal(err)
	}
	if m.TaxYear != 2025 || m.Millesime != 2026 {
		t.Errorf("map keyed %d labelled %d; want income year 2025, millésime 2026", m.TaxYear, m.Millesime)
	}
	for cat, line := range map[string]string{"rent": "211", "repairs": "224", "copro_regularisation_prior": "230", "teom": ""} {
		if got, ok := m.Lines[cat]; !ok || got != line {
			t.Errorf("income year 2025 %s → %q (present %v); want %q", cat, got, ok, line)
		}
	}
	if _, err := s.Form2044LineMap(2026); !errors.Is(err, ErrNotFound) {
		t.Errorf("income year 2026: %v; want ErrNotFound (form not published yet)", err)
	}
}

func TestRentControlFixtures(t *testing.T) {
	s := fixtures(t)
	c, err := s.IRLCap(d(2025, 7, 1))
	if err != nil || c.Applies {
		t.Errorf("IRLCap 2025 = %+v, %v; want not in force", c, err)
	}
	z, err := s.ZoneCeiling(d(2025, 7, 1))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := z.PerSquareMetre("Z1", "T2"); ok {
		t.Error("the 2025 fixture has no zone yet")
	}
	r, err := s.RelocationRule(d(2025, 7, 1))
	if err != nil || !r.Applies {
		t.Errorf("RelocationRule 2025 = %+v, %v; want applying", r, err)
	}
	if _, err := s.ZoneCeiling(d(2024, 12, 31)); !errors.Is(err, ErrNotFound) {
		t.Errorf("ceiling before the fixtures: %v; want ErrNotFound", err)
	}
}

// The standard lease terms are read from the fixtures, never written here:
// every term is set, and the special cases sit on the right side of the
// standard ones (a legal-person lessor longer, a student shorter).
func TestLeaseTerm(t *testing.T) {
	s := fixtures(t)
	for _, on := range []period.Date{d(2025, time.June, 1), d(2026, time.January, 1)} {
		lt, err := s.LeaseTerm(on)
		if err != nil {
			t.Fatal(err)
		}
		if lt.Unfurnished <= 0 || lt.Furnished <= 0 || lt.UnfurnishedLegalOwner <= lt.Unfurnished || lt.FurnishedStudent <= 0 || lt.FurnishedStudent >= lt.Furnished {
			t.Errorf("%v: %+v", on, lt)
		}
	}
	if _, err := s.LeaseTerm(d(2020, time.January, 1)); err == nil {
		t.Error("a date before every entry has a lease term")
	}
}
