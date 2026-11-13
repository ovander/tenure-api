package golden

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/ovander/tenure-api/internal/dashboard"
	"github.com/ovander/tenure-api/internal/ledger"
	"github.com/ovander/tenure-api/internal/period"
)

// The dashboard cases (RENT-SPEC-016, Q-129) live under
// testdata/golden/<year>/dashboard/ and run dashboard.Build with the chart
// of accounts and the aging of that year's fixtures. A step's expected
// value is the dashboard and its CSV.
type dashboardStep struct {
	Name     string                 `json:"name"`
	Year     int                    `json:"year"`
	AsOf     period.Date            `json:"as_of"`
	Current  []ledger.Entry         `json:"current"`
	Previous []ledger.Entry         `json:"previous,omitempty"`
	Assets   []dashboard.AssetInput `json:"assets"`
}

type dashboardCase struct {
	Case        string          `json:"case"`
	Description string          `json:"description"`
	Steps       []dashboardStep `json:"steps"`
	Expected    json.RawMessage `json:"expected"`
}

func TestDashboardGolden(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(root, "golden", "*", "dashboard", "*.json"))
	sort.Strings(files)
	if len(files) == 0 {
		t.Fatal("no dashboard golden case found")
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
			var c dashboardCase
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&c); err != nil {
				t.Fatalf("decode: %v", err)
			}
			ps := loadParams(t, year, nil)
			out := map[string]any{}
			for _, s := range c.Steps {
				chart, err := ps.LedgerChart(s.AsOf)
				if err != nil {
					t.Fatal(err)
				}
				aging, err := ps.DashboardAging(s.AsOf)
				if err != nil {
					t.Fatal(err)
				}
				d, err := dashboard.Build(s.Current, s.Previous, chart, s.Assets, s.Year, s.AsOf, aging)
				if err != nil {
					out[s.Name] = map[string]string{"error": err.Error()}
					continue
				}
				out[s.Name] = map[string]any{"dashboard": d, "csv": strings.Split(string(dashboard.CSV(d)), "\n")}
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
