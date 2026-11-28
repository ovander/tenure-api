package golden

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"

	"github.com/ovander/tenure-api/internal/overview"
)

// The month cockpit cases (RENT-FE-001 §4b, Q-139) live under
// testdata/golden/<year>/overview/ and replay overview.Steps and
// overview.Anomalies with the thresholds of that year's legal fixtures
// (overview.cockpit, bank.reconciliation), never written in the case.
type overviewCase struct {
	Case        string          `json:"case"`
	Description string          `json:"description"`
	Facts       overview.Facts  `json:"facts"`
	Expected    json.RawMessage `json:"expected"`
}

type overviewResult struct {
	UpToDate  bool                `json:"up_to_date"`
	Steps     []overview.StepView `json:"steps"`
	Anomalies []overview.Anomaly  `json:"anomalies"`
}

func TestOverviewGolden(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(root, "golden", "*", "overview", "*.json"))
	sort.Strings(files)
	if len(files) == 0 {
		t.Fatal("no overview golden case found")
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
			var c overviewCase
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&c); err != nil {
				t.Fatalf("decode: %v", err)
			}
			ps := loadParams(t, year, nil)
			if c.Facts.Thresholds, err = ps.Overview(c.Facts.Today); err != nil {
				t.Fatal(err)
			}
			b, err := ps.Bank(c.Facts.Today)
			if err != nil {
				t.Fatal(err)
			}
			c.Facts.Bank.AlertDays = b.UnmatchedAlertDays
			steps := overview.Steps(c.Facts)
			got, err := json.MarshalIndent(overviewResult{UpToDate: overview.UpToDate(steps), Steps: steps, Anomalies: overview.Anomalies(c.Facts)}, "  ", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if *update {
				if !bytes.Equal(normalise(t, c.Expected), normalise(t, got)) {
					c.Expected = got
					out, err := json.MarshalIndent(c, "", "  ")
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(f, append(out, '\n'), 0o644); err != nil {
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
