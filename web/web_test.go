package web

import (
	"errors"
	"io/fs"
	"testing"
)

func TestVersionParses(t *testing.T) {
	if _, _, err := Pinned(); err != nil && !errors.Is(err, ErrUnpinned) {
		t.Fatal(err)
	}
	if _, err := fs.Stat(FS(), "index.html"); err != nil {
		t.Fatalf("the embedded bundle has no index.html: %v", err)
	}
}

func TestParse(t *testing.T) {
	sum := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	for _, c := range []struct {
		in       string
		tag      string
		unpinned bool
		bad      bool
	}{
		{in: "# comment\nnone\n", unpinned: true},
		{in: "v1.2.3 " + sum + "\n", tag: "v1.2.3"},
		{in: "v1.2.3-rc.1 " + sum, tag: "v1.2.3-rc.1"},
		{in: "", bad: true},
		{in: "v1.2.3", bad: true},
		{in: "1.2.3 " + sum, bad: true},
		{in: "v1.2.3 " + sum[:63], bad: true},
		{in: "none\nv1.2.3 " + sum, bad: true},
	} {
		tag, got, err := parse(c.in)
		switch {
		case c.unpinned:
			if !errors.Is(err, ErrUnpinned) {
				t.Errorf("%q: %v", c.in, err)
			}
		case c.bad:
			if err == nil || errors.Is(err, ErrUnpinned) {
				t.Errorf("%q accepted", c.in)
			}
		default:
			if err != nil || tag != c.tag || got != sum {
				t.Errorf("%q: %s %s %v", c.in, tag, got, err)
			}
		}
	}
}
