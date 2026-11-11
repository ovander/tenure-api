package golden

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/loan"
	"github.com/ovander/tenure-api/internal/money"
)

// The amortisation cases (RENT-SPEC-014, Q-126) live under
// testdata/golden/<year>/loan/. A step computes a table from terms
// (loan.Schedule) or checks a typed-in one (loan.Check); its expected value
// is the totals by year, the first three and the last two instalments, or
// the refusal.
type loanStep struct {
	Name        string            `json:"name"`
	Terms       *loan.Terms       `json:"terms,omitempty"`
	Principal   money.Cents       `json:"principal_cents,omitempty"`
	Instalments []loan.Instalment `json:"instalments,omitempty"`
}

type loanCase struct {
	Case        string          `json:"case"`
	Description string          `json:"description"`
	Steps       []loanStep      `json:"steps"`
	Expected    json.RawMessage `json:"expected"`
}

func loanResult(rows []loan.Instalment, err error) any {
	var inv *invariant.Error
	if errors.As(err, &inv) {
		return map[string]string{"error": inv.Type, "detail": inv.Detail}
	}
	if err != nil {
		return map[string]string{"error": err.Error()}
	}
	first, last := rows, rows
	if len(rows) > 3 {
		first = rows[:3]
	}
	if len(rows) > 2 {
		last = rows[len(rows)-2:]
	}
	return map[string]any{"count": len(rows), "years": loan.Years(rows), "first": first, "last": last}
}

func TestLoanGolden(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(root, "golden", "*", "loan", "*.json"))
	sort.Strings(files)
	if len(files) == 0 {
		t.Fatal("no loan golden case found")
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			var c loanCase
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&c); err != nil {
				t.Fatalf("decode: %v", err)
			}
			out := map[string]any{}
			for _, s := range c.Steps {
				if s.Terms != nil {
					out[s.Name] = loanResult(loan.Schedule(*s.Terms))
				} else {
					out[s.Name] = loanResult(loan.Check(s.Principal, s.Instalments))
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
			if !bytes.Equal(normalise(t, c.Expected), normalise(t, got)) {
				t.Fatalf("%s: got\n%s\nwant\n%s", c.Case, got, c.Expected)
			}
		})
	}
}
