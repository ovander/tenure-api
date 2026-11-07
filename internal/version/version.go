// Package version describes the running tenure: the API build (version,
// commit, time, Go toolchain, platform, backendkit, last embedded migration)
// and the tenure-web bundle pinned in web/VERSION. It reads only what is
// compiled in, so it is safe to serve to any signed-in user.
package version

import (
	"io/fs"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"

	"github.com/ovander/backendkit/buildinfo"

	"github.com/ovander/tenure-api/migrations"
	"github.com/ovander/tenure-api/web"
)

// API is the build of the tenure binary.
type API struct {
	// Version is the release tag stamped by scripts/build.sh, or "dev".
	Version string `json:"version" example:"v0.1.20"`
	// Commit and BuildTime come from the stamp, or from Go's VCS stamp of an
	// unstamped build; empty when neither is known (go run).
	Commit    string `json:"commit,omitempty"`
	BuildTime string `json:"build_time,omitempty"`
	// Stamped says the binary was built by scripts/build.sh (a release).
	Stamped bool `json:"stamped"`
	// Modified says an unstamped build was made from a tree with local
	// changes.
	Modified   bool   `json:"modified,omitempty"`
	GoVersion  string `json:"go_version" example:"go1.27.3"`
	Platform   string `json:"platform" example:"linux/amd64"`
	Backendkit string `json:"backendkit,omitempty" example:"v1.9.0"`
	// Schema is the last migration embedded in the binary (applied by
	// tenure migrate up).
	Schema string `json:"schema,omitempty" example:"000016"`
}

// Web is the tenure-web bundle the binary serves.
type Web struct {
	// Release is the tag pinned in web/VERSION, empty when none is.
	Release string `json:"release,omitempty" example:"v0.20.0"`
	SHA256  string `json:"sha256,omitempty"`
}

// View is what GET /api/v1/version returns.
type View struct {
	API API `json:"api"`
	Web Web `json:"web"`
}

// Get returns the running tenure's build.
func Get() View {
	b := buildinfo.Get()
	v := View{API: API{Version: b.Version, Commit: b.GitCommit, BuildTime: b.BuildTime, Stamped: b.Version != "dev" && b.GitCommit != "",
		GoVersion: runtime.Version(), Platform: runtime.GOOS + "/" + runtime.GOARCH, Schema: lastMigration()}}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, d := range info.Deps {
			if d.Path == "github.com/ovander/backendkit" {
				v.API.Backendkit = d.Version
			}
		}
		if !v.API.Stamped {
			for _, s := range info.Settings {
				switch s.Key {
				case "vcs.revision":
					v.API.Commit = s.Value
				case "vcs.time":
					v.API.BuildTime = s.Value
				case "vcs.modified":
					v.API.Modified = s.Value == "true"
				}
			}
		}
	}
	if tag, sum, err := web.Pinned(); err == nil {
		v.Web = Web{Release: tag, SHA256: sum}
	}
	return v
}

// lastMigration is the number of the last embedded migration file.
func lastMigration() string {
	names, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil || len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	n, _, _ := strings.Cut(names[len(names)-1], "_")
	return n
}

// Lines prints the build for tenure version: the version, commit and build
// time on the first line, then one fact per line.
func (v View) Lines() []string {
	or := func(s, d string) string {
		if s == "" {
			return d
		}
		return s
	}
	stamp := "release build (scripts/build.sh)"
	if !v.API.Stamped {
		stamp = "unstamped build (go run or go build)"
		if v.API.Modified {
			stamp += ", local changes"
		}
	}
	// The first line is "<version> <commit> <build time>", as before #113:
	// the release workflow reads its first field as the version.
	return []string{
		strings.TrimSpace(v.API.Version + " " + v.API.Commit + " " + v.API.BuildTime),
		"commit      " + or(v.API.Commit, "unknown"),
		"built       " + or(v.API.BuildTime, "unknown"),
		"build       " + stamp,
		"go          " + v.API.GoVersion + " " + v.API.Platform,
		"backendkit  " + or(v.API.Backendkit, "unknown"),
		"schema      " + or(v.API.Schema, "unknown"),
		"web bundle  " + or(strings.TrimSpace(v.Web.Release+" "+v.Web.SHA256), "none pinned"),
	}
}
