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
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/period"
)

// The BIC result cases (RENT-SPEC-019, Q-132) live under
// testdata/golden/<year>/bicresult/ and chain bic.Results over the years
// given, the micro parameter of each year and the deficit lifetime from the
// fixtures.
type bicResultCase struct {
	Case        string          `json:"case"`
	Description string          `json:"description"`
	Years       []bic.YearInput `json:"years"`
	Expected    json.RawMessage `json:"expected"`
}

func TestBICResultGolden(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(root, "golden", "*", "bicresult", "*.json"))
	sort.Strings(files)
	if len(files) == 0 {
		t.Fatal("no BIC result golden case found")
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
			var c bicResultCase
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&c); err != nil {
				t.Fatalf("decode: %v", err)
			}
			ps := loadParams(t, year, nil)
			micro := func(y int) params.BICMicro {
				m, err := ps.BICMicro(period.NewDate(y, 12, 31))
				if err != nil {
					t.Fatal(err)
				}
				return m
			}
			deficit, err := ps.BICDeficitYears(period.NewDate(year, 12, 31))
			if err != nil {
				t.Fatal(err)
			}
			got, err := json.MarshalIndent(bic.Results(c.Years, micro, deficit.Years), "  ", "  ")
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
