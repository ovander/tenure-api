package golden

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"

	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/lease"
	"github.com/ovander/tenure-api/internal/period"
)

// The re-evaluation cases (RENT-SPEC-010, Q-120) live under
// testdata/golden/<year>/reevaluation/ and replay lease.Reevaluate with the
// legal fixtures of that year. Each step is a proposal on one lease; its
// expected value is the computed schedule or the problem type refusing it.
type reevaluationStep struct {
	Name       string                  `json:"name"`
	Facts      lease.ReevaluationFacts `json:"facts"`
	Input      lease.ReevaluationInput `json:"input"`
	ReceivedOn period.Date             `json:"received_on"`
	Today      period.Date             `json:"today"`
}

type reevaluationCase struct {
	Case        string             `json:"case"`
	Description string             `json:"description"`
	Steps       []reevaluationStep `json:"steps"`
	Expected    json.RawMessage    `json:"expected"`
}

func TestReevaluationGolden(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(root, "golden", "*", "reevaluation", "*.json"))
	sort.Strings(files)
	if len(files) == 0 {
		t.Fatal("no re-evaluation golden case found")
	}
	for _, f := range files {
		year, err := strconv.Atoi(filepath.Base(filepath.Dir(filepath.Dir(f))))
		if err != nil {
			t.Fatal(err)
		}
		t.Run(filepath.Base(f), func(t *testing.T) {
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			var c reevaluationCase
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&c); err != nil {
				t.Fatalf("decode: %v", err)
			}
			ps := loadParams(t, year, nil)
			out := map[string]any{}
			for _, s := range c.Steps {
				asOf := s.ReceivedOn
				if asOf.IsZero() {
					asOf = s.Today
				}
				r, err := ps.Reevaluation(asOf)
				if err != nil {
					t.Fatal(err)
				}
				terms, err := lease.Reevaluate(s.Facts, s.Input, s.ReceivedOn, s.Today, r)
				var ie *invariant.Error
				switch {
				case errors.As(err, &ie):
					out[s.Name] = map[string]string{"refused": ie.Type}
				case err != nil:
					t.Fatal(err)
				default:
					out[s.Name] = terms
				}
			}
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
