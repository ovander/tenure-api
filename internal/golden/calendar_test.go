package golden

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"

	"github.com/ovander/tenure-api/internal/calendar"
	"github.com/ovander/tenure-api/internal/period"
)

// The calendar cases (RENT-SPEC-012, Q-116) live under
// testdata/golden/<year>/calendar/ and replay calendar.Build with the legal
// fixtures of that year.
type calendarCase struct {
	Case        string          `json:"case"`
	Description string          `json:"description"`
	Today       period.Date     `json:"today"`
	From        period.Date     `json:"from"`
	To          period.Date     `json:"to"`
	Facts       calendar.Facts  `json:"facts"`
	Expected    json.RawMessage `json:"expected"`
}

func TestCalendarGolden(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(root, "golden", "*", "calendar", "*.json"))
	sort.Strings(files)
	if len(files) == 0 {
		t.Fatal("no calendar golden case found")
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
			var c calendarCase
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&c); err != nil {
				t.Fatalf("decode: %v", err)
			}
			items, err := calendar.Build(c.Facts, c.From, c.To, c.Today, loadParams(t, year, nil))
			if err != nil {
				t.Fatal(err)
			}
			got, err := json.MarshalIndent(items, "  ", "  ")
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
