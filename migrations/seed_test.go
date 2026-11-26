package migrations

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ovander/tenure-api/internal/params"
)

var update = flag.Bool("update", false, "regenerate the legal_params seed of 000001_init.sql")

const (
	seedBegin = "-- seed:legal_params:begin\n"
	seedEnd   = "-- seed:legal_params:end\n"
)

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// fixtureInserts renders one INSERT per fixture entry, in a stable order, so
// the migrations seed exactly what the calculation tests use.
func fixtureInserts(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "testdata", "params", "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures: %v", err)
	}
	sort.Strings(files)
	var out []string
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		set, err := params.Load(bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for _, e := range set.Entries() {
			var v any
			if err := json.Unmarshal(e.Value, &v); err != nil {
				t.Fatal(err)
			}
			compact, _ := json.Marshal(v)
			out = append(out, fmt.Sprintf("INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES (%s, %s, %s, %s, %s);",
				quote(e.Key), quote(e.EffectiveFrom.String()), quote(string(compact))+"::jsonb", quote(e.Source), quote(e.SourceURL)))
		}
	}
	return out
}

type seedBlock struct {
	file       string
	text       string
	begin, end int
}

func seedBlocks(t *testing.T) []seedBlock {
	t.Helper()
	entries, err := FS.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var out []seedBlock
	for _, e := range entries {
		raw, _ := FS.ReadFile(e.Name())
		s := string(raw)
		i, j := strings.Index(s, seedBegin), strings.Index(s, seedEnd)
		if i < 0 || j < i {
			continue
		}
		out = append(out, seedBlock{file: e.Name(), text: s, begin: i + len(seedBegin), end: j})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].file < out[b].file })
	return out
}

// TestSeedMatchesFixtures checks that the seed blocks of every migration,
// together, insert exactly the fixture entries, each once. With -update, the
// missing entries are appended to the latest migration's block; an entry that
// is seeded but no longer in the fixtures is an error to fix by hand (a new
// migration that deletes it), never by editing an applied migration.
func TestSeedMatchesFixtures(t *testing.T) {
	want := fixtureInserts(t)
	blocks := seedBlocks(t)
	if len(blocks) == 0 {
		t.Fatal("no seed block found")
	}
	seeded := map[string]int{}
	for _, b := range blocks {
		for _, l := range strings.Split(b.text[b.begin:b.end], "\n") {
			if strings.HasPrefix(l, "INSERT INTO legal_params") {
				seeded[l]++
			}
		}
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
			t.Errorf("seeded but not in testdata/params: %s", l)
		}
	}
	if len(missing) == 0 {
		return
	}
	if !*update {
		t.Errorf("%d fixture entries are not seeded; run go test ./migrations -update and review", len(missing))
		return
	}
	last := blocks[len(blocks)-1]
	out := last.text[:last.end] + strings.Join(missing, "\n") + "\n" + last.text[last.end:]
	if err := os.WriteFile(last.file, []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("appended %d entries to %s", len(missing), last.file)
}

func TestEveryMigrationHasUpAndDown(t *testing.T) {
	entries, err := FS.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if len(name) < 7 || name[6] != '_' || strings.Trim(name[:6], "0123456789") != "" {
			t.Errorf("%s: name must start with a six-digit zero-padded number", name)
		}
		b, _ := FS.ReadFile(name)
		if !bytes.Contains(b, []byte("-- +goose Up")) || !bytes.Contains(b, []byte("-- +goose Down")) {
			t.Errorf("%s: needs both -- +goose Up and -- +goose Down sections", name)
		}
	}
}

// downDeletesBySeedDate is the first migration whose Down must name the
// effective dates it seeded: a rollback removes its own rows only, never an
// entry a later migration added under the same key. Earlier migrations are
// applied and left as they are.
const downDeletesBySeedDate = "000040"

// TestDownDeletesOnlyItsSeed checks that, from downDeletesBySeedDate on,
// every legal_params deletion in a Down section is scoped by effective_from.
func TestDownDeletesOnlyItsSeed(t *testing.T) {
	entries, err := FS.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".sql") || name[:6] < downDeletesBySeedDate {
			continue
		}
		b, _ := FS.ReadFile(name)
		_, down, _ := strings.Cut(string(b), "-- +goose Down")
		for _, stmt := range strings.Split(down, ";") {
			if strings.Contains(stmt, "DELETE FROM legal_params") && !strings.Contains(stmt, "effective_from") {
				t.Errorf("%s: %s: scope the deletion by effective_from", name, strings.TrimSpace(stmt))
			}
		}
	}
}
