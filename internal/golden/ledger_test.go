package golden

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/text/encoding/charmap"

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
	// ValidOn, when set, also writes the journal CSV and the FEC validated
	// on that day (Q-128).
	ValidOn *period.Date `json:"valid_on,omitempty"`
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
				res := map[string]any{"entries": entries, "balances": balances}
				if s.ValidOn != nil {
					fec, err := ledger.FEC(entries, *s.ValidOn)
					if err != nil {
						t.Fatal(err)
					}
					text, err := charmap.ISO8859_15.NewDecoder().Bytes(fec)
					if err != nil {
						t.Fatal(err)
					}
					sum := sha256.Sum256(fec)
					res["csv"] = strings.Split(string(ledger.CSV(entries)), "\n")
					res["fec"] = strings.Split(string(text), "\r\n")
					res["fec_sha256"] = hex.EncodeToString(sum[:])
				}
				out[s.Name] = res
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
