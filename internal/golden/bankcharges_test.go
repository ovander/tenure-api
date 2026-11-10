package golden

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"

	"github.com/ovander/tenure-api/internal/charge"
	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/period"
)

// The bank debit cases (RENT-SPEC-013, Q-125) live under
// testdata/golden/<year>/bankcharges/ and replay charge.SuggestCharges with
// the legal fixtures of that year. Each step is a debit against the open
// charges; its expected value is the suggestions.
type debitStep struct {
	Name        string              `json:"name"`
	BookedOn    period.Date         `json:"booked_on"`
	AmountCents money.Cents         `json:"amount_cents"`
	Charges     []charge.OpenCharge `json:"charges"`
}

type debitCase struct {
	Case        string          `json:"case"`
	Description string          `json:"description"`
	Steps       []debitStep     `json:"steps"`
	Expected    json.RawMessage `json:"expected"`
}

func TestBankChargesGolden(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(root, "golden", "*", "bankcharges", "*.json"))
	sort.Strings(files)
	if len(files) == 0 {
		t.Fatal("no bank debit golden case found")
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
			var c debitCase
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&c); err != nil {
				t.Fatalf("decode: %v", err)
			}
			ps := loadParams(t, year, nil)
			out := map[string]any{}
			for _, s := range c.Steps {
				r, err := ps.BankCharges(s.BookedOn)
				if err != nil {
					t.Fatal(err)
				}
				out[s.Name] = charge.SuggestCharges(s.BookedOn, s.AmountCents, s.Charges, r)
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
