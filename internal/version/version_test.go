package version

import (
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

func TestGet(t *testing.T) {
	v := Get()
	if v.API.GoVersion != runtime.Version() || v.API.Platform != runtime.GOOS+"/"+runtime.GOARCH {
		t.Errorf("toolchain %+v", v.API)
	}
	if v.API.Stamped || v.API.Version != "dev" {
		t.Errorf("a test binary is not a release build: %+v", v.API)
	}
	if len(v.API.Schema) != 6 {
		t.Errorf("schema %q", v.API.Schema)
	}
	if v.Web.Release != "" && len(v.Web.SHA256) != 64 {
		t.Errorf("web %+v", v.Web)
	}
}

func TestLines(t *testing.T) {
	v := View{API: API{Version: "v1.2.3", Commit: "abc", BuildTime: "2026-11-07T00:00:00Z", Stamped: true, GoVersion: "go1.27.3", Platform: "linux/amd64", Schema: "000016"},
		Web: Web{Release: "v0.20.0", SHA256: "ff"}}
	s := strings.Join(v.Lines(), "\n")
	for _, want := range []string{"tenure v1.2.3", "abc", "release build", "go1.27.3 linux/amd64", "000016", "v0.20.0 ff", "backendkit  unknown"} {
		if !strings.Contains(s, want) {
			t.Errorf("lines lack %q:\n%s", want, s)
		}
	}
	v.API.Stamped, v.API.Modified, v.Web = false, true, Web{}
	s = strings.Join(v.Lines(), "\n")
	if !strings.Contains(s, "unstamped build (go run or go build), local changes") || !strings.Contains(s, "none pinned") {
		t.Errorf("unstamped:\n%s", s)
	}
}

func TestHandler(t *testing.T) {
	w := httptest.NewRecorder()
	Handler(w, httptest.NewRequest("GET", "/api/v1/version", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"go_version":"`+runtime.Version()+`"`) || w.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("%d %s", w.Code, w.Body)
	}
}
