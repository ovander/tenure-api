package golden

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/ovander/tenure-api/internal/payment"
)

// The bank statement cases (RENT-SPEC-013, Q-123) live under
// testdata/golden/<year>/bank/ and replay payment.ParseCSV and
// payment.ParseOFX on statements as banks export them. Each step is one
// statement; its expected value is the lines read, or the refusal.
type bankStep struct {
	Name    string              `json:"name"`
	Format  string              `json:"format"`
	Mapping *payment.CSVMapping `json:"mapping,omitempty"`
	Data    string              `json:"data"`
}

type bankCase struct {
	Case        string          `json:"case"`
	Description string          `json:"description"`
	Steps       []bankStep      `json:"steps"`
	Expected    json.RawMessage `json:"expected"`
}

func TestBankGolden(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(root, "golden", "*", "bank", "*.json"))
	sort.Strings(files)
	if len(files) == 0 {
		t.Fatal("no bank golden case found")
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			var c bankCase
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&c); err != nil {
				t.Fatalf("decode: %v", err)
			}
			out := map[string]any{}
			for _, s := range c.Steps {
				var lines []payment.BankLine
				var err error
				if s.Format == "ofx" {
					lines, err = payment.ParseOFX([]byte(s.Data))
				} else {
					lines, err = payment.ParseCSV([]byte(s.Data), *s.Mapping)
				}
				switch {
				case errors.Is(err, payment.ErrStatement):
					out[s.Name] = map[string]string{"refused": err.Error()}
				case err != nil:
					t.Fatal(err)
				default:
					out[s.Name] = lines
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
