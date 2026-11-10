package golden

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"

	"github.com/ovander/tenure-api/internal/payment"
)

// The bank matching cases (RENT-SPEC-013, Q-124) live under
// testdata/golden/<year>/bankmatch/ and replay payment.Suggest and
// payment.Sure with the legal fixtures of that year. Each step is a bank
// line against the open rent calls; its expected value is the suggestions
// and the sure one, if any.
type matchStep struct {
	Name  string             `json:"name"`
	Line  payment.BankLine   `json:"line"`
	Calls []payment.OpenCall `json:"calls"`
}

type matchCase struct {
	Case        string          `json:"case"`
	Description string          `json:"description"`
	Steps       []matchStep     `json:"steps"`
	Expected    json.RawMessage `json:"expected"`
}

func TestBankMatchGolden(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(root, "golden", "*", "bankmatch", "*.json"))
	sort.Strings(files)
	if len(files) == 0 {
		t.Fatal("no bank matching golden case found")
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
			var c matchCase
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&c); err != nil {
				t.Fatalf("decode: %v", err)
			}
			out := map[string]any{}
			ps := loadParams(t, year, nil)
			for _, s := range c.Steps {
				r, err := ps.BankMatching(s.Line.BookedOn)
				if err != nil {
					t.Fatal(err)
				}
				sg := payment.Suggest(s.Line, s.Calls, r)
				sure, ok := payment.Sure(sg)
				res := map[string]any{"suggestions": sg}
				if ok {
					res["sure"] = sure
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
			if len(c.Expected) == 0 {
				t.Fatal("no expected result; run with -update and review")
			}
			if want, have := normalise(t, c.Expected), normalise(t, got); !bytes.Equal(want, have) {
				t.Errorf("differs from the golden file\nwant %s\n got %s", want, have)
			}
		})
	}
}
