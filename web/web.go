// Package web embeds the built bundle of tenure-web at the release pinned in
// VERSION (RENT-BE-001 §9). scripts/fetch-web.sh replaces dist/ with that
// release before a release build; the committed dist/ is a placeholder page
// (Q-96).
package web

import (
	"bufio"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strings"
)

//go:embed all:dist
var dist embed.FS

//go:embed VERSION
var version string

// FS returns the embedded bundle, rooted at its index.html.
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // dist is embedded at build time
	}
	return sub
}

// ErrUnpinned reports a VERSION that pins no release ("none").
var ErrUnpinned = errors.New("web: no tenure-web release pinned")

var (
	tagPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`)
	sumPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Pinned returns the tag and SHA-256 that VERSION pins, ErrUnpinned for
// "none", or an error when the file is malformed.
func Pinned() (tag, sum string, err error) {
	return parse(version)
}

func parse(s string) (string, string, error) {
	var lines []string
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" && !strings.HasPrefix(l, "#") {
			lines = append(lines, l)
		}
	}
	if len(lines) != 1 {
		return "", "", fmt.Errorf("web/VERSION: want one line, got %d", len(lines))
	}
	if lines[0] == "none" {
		return "", "", ErrUnpinned
	}
	f := strings.Fields(lines[0])
	if len(f) != 2 || !tagPattern.MatchString(f[0]) || !sumPattern.MatchString(f[1]) {
		return "", "", fmt.Errorf("web/VERSION: want \"<vX.Y.Z> <sha256>\", got %q", lines[0])
	}
	return f[0], f[1], nil
}
