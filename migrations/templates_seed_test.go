package migrations

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

const (
	tplBegin = "-- seed:inventory_templates:begin\n"
	tplEnd   = "-- seed:inventory_templates:end\n"
)

// systemTemplateNS derives the stable id of a system template from its asset
// kind and version, so a re-seed never mints a second row.
var systemTemplateNS = uuid.MustParse("6f1c2d6a-6b0e-4f7e-9a51-3f0b1c2e7d40")

// templateInserts renders the system inventory templates of
// testdata/inventory/templates.json (RENT-INV-001 §4) as INSERTs.
func templateInserts(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "inventory", "templates.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Source    string            `json:"source"`
		Templates []json.RawMessage `json:"templates"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, tp := range f.Templates {
		var head struct {
			AssetKind string `json:"asset_kind"`
			Version   int    `json:"version"`
		}
		if err := json.Unmarshal(tp, &head); err != nil || head.AssetKind == "" || head.Version < 1 {
			t.Fatalf("template without asset_kind or version: %v", err)
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, tp); err != nil {
			t.Fatal(err)
		}
		id := uuid.NewSHA1(systemTemplateNS, []byte(fmt.Sprintf("%s/%d", head.AssetKind, head.Version)))
		out = append(out, fmt.Sprintf("INSERT INTO inventory_templates (id, owner_id, asset_kind, version, structure, source, created_at, created_by) VALUES (%s, NULL, %s, %d, %s::jsonb, %s, '2026-11-05T00:00:00Z', 'migration');",
			quote(id.String()), quote(head.AssetKind), head.Version, quote(compact.String()), quote(f.Source)))
	}
	return out
}

// TestTemplateSeedMatchesFixtures checks that the inventory_templates seed
// blocks insert exactly the system templates of the fixture, each once; with
// -update the missing ones are appended to the latest block. A changed
// template is a new version, never an edited row.
func TestTemplateSeedMatchesFixtures(t *testing.T) {
	want := templateInserts(t)
	// Read from disk, not the embedded FS, so an -update of the legal_params
	// seed in the same run is not overwritten.
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	seeded := map[string]int{}
	var lastFile, lastText string
	var lastEnd int
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		b, _ := os.ReadFile(e.Name())
		s := string(b)
		i, j := strings.Index(s, tplBegin), strings.Index(s, tplEnd)
		if i < 0 || j < i {
			continue
		}
		for _, l := range strings.Split(s[i+len(tplBegin):j], "\n") {
			if strings.HasPrefix(l, "INSERT INTO inventory_templates") {
				seeded[l]++
			}
		}
		lastFile, lastText, lastEnd = e.Name(), s, j
	}
	if lastFile == "" {
		t.Fatal("no inventory_templates seed block found")
	}
	wanted := map[string]bool{}
	var missing []string
	for _, w := range want {
		wanted[w] = true
		switch seeded[w] {
		case 0:
			missing = append(missing, w)
		case 1:
		default:
			t.Errorf("seeded twice: %s", w)
		}
	}
	for l := range seeded {
		if !wanted[l] {
			t.Errorf("seeded but not in testdata/inventory/templates.json (add a version instead): %.120s…", l)
		}
	}
	if len(missing) == 0 {
		return
	}
	if !*update {
		t.Errorf("%d system templates are not seeded; run go test ./migrations -update and review", len(missing))
		return
	}
	out := lastText[:lastEnd] + strings.Join(missing, "\n") + "\n" + lastText[lastEnd:]
	if err := os.WriteFile(lastFile, []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("appended %d templates to %s", len(missing), lastFile)
}
