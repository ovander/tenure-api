package golden

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/ovander/tenure-api/internal/lease"
	"github.com/ovander/tenure-api/internal/period"
)

// The lease terms and vacancy cases (RENT-SPEC-010, Q-119) live under
// testdata/golden/<year>/terms/ and replay lease.Terms and lease.Vacancies.
type termsCase struct {
	Case        string      `json:"case"`
	Description string      `json:"description"`
	Today       period.Date `json:"today"`
	Leases      []struct {
		Name       string       `json:"name"`
		Kind       lease.Kind   `json:"kind"`
		StartOn    period.Date  `json:"start_on"`
		TermMonths int          `json:"term_months"`
		EndOn      *period.Date `json:"end_on,omitempty"`
	} `json:"leases"`
	// StudentTermMonths is the furnished student term of the fixtures.
	StudentTermMonths int             `json:"student_term_months"`
	AcquiredOn        period.Date     `json:"acquired_on"`
	Expected          json.RawMessage `json:"expected"`
}

func TestTermsGolden(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(root, "golden", "*", "terms", "*.json"))
	sort.Strings(files)
	if len(files) == 0 {
		t.Fatal("no terms golden case found")
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			var c termsCase
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&c); err != nil {
				t.Fatalf("decode: %v", err)
			}
			out := map[string]any{}
			var occ []lease.Occupancy
			for _, l := range c.Leases {
				out[l.Name] = lease.Terms(lease.TermFacts{StartOn: l.StartOn, TermMonths: l.TermMonths, EndOn: l.EndOn,
					Renews: lease.RenewsTacitly(l.Kind, l.TermMonths, c.StudentTermMonths)}, c.Today)
				occ = append(occ, lease.Occupancy{StartOn: l.StartOn, EndOn: l.EndOn})
			}
			periods, byYear := lease.Vacancies(occ, c.AcquiredOn, c.Today)
			out["vacancy"] = map[string]any{"periods": periods, "days_by_year": byYear}
			got, err := json.MarshalIndent(out, "  ", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if *update {
				if !bytes.Equal(normalise(t, c.Expected), normalise(t, got)) {
					c.Expected = got
					b, err := json.MarshalIndent(c, "", "  ")
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(f, append(b, '\n'), 0o644); err != nil {
						t.Fatal(err)
					}
					t.Logf("updated %s", f)
				}
				return
			}
			if len(c.Expected) == 0 {
				t.Fatal("no expected result; run with -update and review")
			}
			if want, have := normalise(t, c.Expected), normalise(t, got); !bytes.Equal(want, have) {
				t.Errorf("differs from the golden file\nwant %s\n got %s", want, have)
			}
		})
	}
}
