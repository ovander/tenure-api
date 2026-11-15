package golden

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/ovander/tenure-api/internal/sci"
)

// The SCI split cases (RENT-SPEC-019, part 19c, Q-133) live under
// testdata/golden/<year>/sci/: an SCI's result of the year by nature, the
// holders of the parts on 31 December in register order, and each one's
// share. The split takes no parameter: the year only places the case.
type sciCase struct {
	Case     string          `json:"case"`
	Figures  sci.Figures     `json:"figures"`
	Holders  []sci.Holder    `json:"holders"`
	Expected json.RawMessage `json:"expected"`
}

func TestSCIGolden(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(root, "golden", "*", "sci", "*.json"))
	sort.Strings(files)
	if len(files) == 0 {
		t.Fatal("no SCI golden case found")
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			var c sciCase
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&c); err != nil {
				t.Fatalf("decode: %v", err)
			}
			var out any
			alloc, err := sci.Allocate(c.Figures, c.Holders)
			if err != nil {
				out = map[string]string{"error": err.Error()}
			} else {
				out = alloc
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
