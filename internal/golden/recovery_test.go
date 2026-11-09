package golden

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/billing"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/period"
)

// The recovery cases (RENT-SPEC-011, Q-121) live under
// testdata/golden/<year>/recovery/ and replay billing.Arrears then
// billing.Recovery with the legal fixtures of that year. Each step is one
// lease's calls and recorded steps on a day; its expected value is the
// recovery view.
type recoveryStep struct {
	Name  string                 `json:"name"`
	Calls []billing.Call         `json:"calls"`
	Steps []billing.RecoveryStep `json:"steps"`
	Facts billing.RecoveryFacts  `json:"facts"`
	Today period.Date            `json:"today"`
}

type recoveryCase struct {
	Case        string          `json:"case"`
	Description string          `json:"description"`
	Steps       []recoveryStep  `json:"steps"`
	Expected    json.RawMessage `json:"expected"`
}

func TestRecoveryGolden(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(root, "golden", "*", "recovery", "*.json"))
	sort.Strings(files)
	if len(files) == 0 {
		t.Fatal("no recovery golden case found")
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
			var c recoveryCase
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&c); err != nil {
				t.Fatalf("decode: %v", err)
			}
			ps := loadParams(t, year, nil)
			out := map[string]any{}
			for _, s := range c.Steps {
				r, err := ps.Recovery(s.Today)
				if err != nil {
					t.Fatal(err)
				}
				days, err := ps.Int64(params.KeyReceiptReminderDays, s.Today)
				if err != nil {
					t.Fatal(err)
				}
				v, err := billing.Recovery(billing.Arrears(uuid.Nil, s.Calls, s.Today, int(days)), s.Steps, s.Facts, s.Today, r)
				if err != nil {
					t.Fatal(err)
				}
				out[s.Name] = v
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
