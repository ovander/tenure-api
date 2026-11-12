package golden

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"

	"github.com/ovander/tenure-api/internal/ledger"
	"github.com/ovander/tenure-api/internal/period"
)

// The journal cases (RENT-SPEC-014, Q-127) live under
// testdata/golden/<year>/ledger/ and post ledger.Journal with the chart of
// accounts of that year's fixtures. A step's expected value is its entries
// and balances, or its refusal.
type ledgerStep struct {
	Name   string        `json:"name"`
	From   period.Date   `json:"from"`
	To     period.Date   `json:"to"`
	Inputs ledger.Inputs `json:"inputs"`
}

type ledgerCase struct {
	Case        string          `json:"case"`
	Description string          `json:"description"`
	Steps       []ledgerStep    `json:"steps"`
	Expected    json.RawMessage `json:"expected"`
}

func TestLedgerGolden(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(root, "golden", "*", "ledger", "*.json"))
	sort.Strings(files)
	if len(files) == 0 {
		t.Fatal("no ledger golden case found")
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
			var c ledgerCase
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&c); err != nil {
				t.Fatalf("decode: %v", err)
			}
			ps := loadParams(t, year, nil)
			out := map[string]any{}
			for _, s := range c.Steps {
				chart, err := ps.LedgerChart(s.To)
				if err != nil {
					t.Fatal(err)
				}
				entries, balances, err := ledger.Journal(s.Inputs, chart, s.From, s.To)
				if err != nil {
					out[s.Name] = map[string]string{"error": err.Error()}
					continue
				}
				out[s.Name] = map[string]any{"entries": entries, "balances": balances}
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
