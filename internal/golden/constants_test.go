package golden

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ovander/tenure-api/internal/period"
)

// TestNoLegalConstantInCode enforces CLAUDE.md: legal constants live in
// legal_params, never in Go. It fails when a non-test Go file contains a
// parameter value of four digits or more, or a form 2044 line number as a
// string literal, taken from the fixtures.
func TestNoLegalConstantInCode(t *testing.T) {
	set := loadParams(t, 9999, nil)
	var patterns []*regexp.Regexp
	seen := map[string]bool{}
	addPattern := func(expr string) {
		if !seen[expr] {
			seen[expr] = true
			patterns = append(patterns, regexp.MustCompile(expr))
		}
	}
	for _, e := range set.Entries() {
		var n int64
		if json.Unmarshal(e.Value, &n) == nil && (n >= 1000 || n <= -1000) {
			addPattern(fmt.Sprintf(`\b%d\b`, n))
		}
	}
	m, err := set.Form2044LineMap(period.NewDate(2025, 1, 1).Year + 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range m.Lines {
		if line != "" {
			addPattern(regexp.QuoteMeta(`"` + line + `"`))
		}
	}
	if len(patterns) == 0 {
		t.Fatal("no pattern built from the fixtures")
	}
	err = filepath.WalkDir(filepath.Join("..", ".."), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "testdata") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, p := range patterns {
			if loc := p.FindIndex(b); loc != nil {
				t.Errorf("%s contains %s: move it to legal_params", path, b[loc[0]:loc[1]])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
