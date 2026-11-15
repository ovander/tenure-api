package golden

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"

	"github.com/ovander/tenure-api/internal/bic"
	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/period"
)

// The depreciation cases (RENT-SPEC-019, Q-132) live under
// testdata/golden/<year>/bic/. The grid is the default of that year's
// fixtures (bic.components) unless the case gives its lines; a furniture
// item without months takes bic.furniture. Each year's expected value is
// its rows and their total.
type bicCase struct {
	Case        string          `json:"case"`
	Description string          `json:"description"`
	InServiceOn period.Date     `json:"in_service_on"`
	BasisCents  money.Cents     `json:"basis_cents"`
	Lines       []bic.Line      `json:"lines,omitempty"`
	Items       []bic.Item      `json:"items,omitempty"`
	Years       []int           `json:"years"`
	Expected    json.RawMessage `json:"expected"`
}

func TestBICGolden(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(root, "golden", "*", "bic", "*.json"))
	sort.Strings(files)
	if len(files) == 0 {
		t.Fatal("no depreciation golden case found")
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
			var c bicCase
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&c); err != nil {
				t.Fatalf("decode: %v", err)
			}
			ps := loadParams(t, year, nil)
			g := bic.Grid{InServiceOn: c.InServiceOn, BasisCents: c.BasisCents, Lines: c.Lines}
			if len(g.Lines) == 0 {
				def, err := ps.BICComponents(c.InServiceOn)
				if err != nil {
					t.Fatal(err)
				}
				for _, l := range def.Lines {
					g.Lines = append(g.Lines, bic.Line{Component: l.Component, ShareBP: l.ShareBP, Months: l.Months})
				}
			}
			furniture, err := ps.BICFurniture(c.InServiceOn)
			if err != nil {
				t.Fatal(err)
			}
			for i := range c.Items {
				if c.Items[i].Months == 0 {
					c.Items[i].Months = furniture.Months
				}
			}
			out := map[string]any{}
			for _, y := range c.Years {
				rows, err := bic.Depreciation(&g, c.Items, y)
				if err != nil {
					out[strconv.Itoa(y)] = map[string]string{"error": err.Error()}
					continue
				}
				out[strconv.Itoa(y)] = map[string]any{"rows": rows, "total_cents": bic.Total(rows)}
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
