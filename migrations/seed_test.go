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
	seedFile  = "000001_init.sql"
	seedBegin = "-- seed:legal_params:begin\n"
	seedEnd   = "-- seed:legal_params:end\n"
)

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// renderSeed renders the INSERT statements of every fixture entry, in a stable
// order, so the migration seeds exactly what the calculation tests use.
func renderSeed(t *testing.T) string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "testdata", "params", "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures: %v", err)
	}
	sort.Strings(files)
	var b strings.Builder
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		set, err := params.Load(bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		fmt.Fprintf(&b, "-- %s\n", filepath.Base(f))
		for _, e := range set.Entries() {
			var v any
			if err := json.Unmarshal(e.Value, &v); err != nil {
				t.Fatal(err)
			}
			compact, _ := json.Marshal(v)
			fmt.Fprintf(&b, "INSERT INTO legal_params (key, effective_from, value, source, source_url) VALUES (%s, %s, %s, %s, %s);\n",
				quote(e.Key), quote(e.EffectiveFrom.String()), quote(string(compact))+"::jsonb", quote(e.Source), quote(e.SourceURL))
		}
	}
	return b.String()
}

func TestSeedMatchesFixtures(t *testing.T) {
	raw, err := os.ReadFile(seedFile)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	i, j := strings.Index(s, seedBegin), strings.Index(s, seedEnd)
	if i < 0 || j < i {
		t.Fatal("seed markers not found in " + seedFile)
	}
	want := renderSeed(t)
	got := s[i+len(seedBegin) : j]
	if got == want {
		return
	}
	if *update {
		out := s[:i+len(seedBegin)] + want + s[j:]
		if err := os.WriteFile(seedFile, []byte(out), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Log("seed regenerated")
		return
	}
	t.Errorf("the legal_params seed of %s differs from testdata/params; run go test ./migrations -update and review", seedFile)
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
