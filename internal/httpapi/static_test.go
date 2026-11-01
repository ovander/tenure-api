package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/ovander/tenure-api/internal/auth"
)

func TestStaticApp(t *testing.T) {
	jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"keys":[]}`))
	}))
	t.Cleanup(jwks.Close)
	a, err := auth.New(auth.Options{JWKSURL: jwks.URL, Issuer: "https://socrate.test", Audience: "tenure-api",
		AdminRole: "admin", StepUpMaxAge: 5 * time.Minute}, nil)
	if err != nil {
		t.Fatal(err)
	}
	files := fstest.MapFS{
		"index.html":           {Data: []byte("<!doctype html><title>app</title>")},
		"assets/app-1a2b3c.js": {Data: []byte("console.log(1)")},
		"manifest.webmanifest": {Data: []byte("{}")},
	}
	h, err := NewRouter(Deps{Auth: a, Web: files})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		method, path string
		status       int
		body, cache  string
		problem      bool
	}{
		{method: "GET", path: "/", status: 200, body: "<title>app</title>", cache: "no-cache"},
		{method: "GET", path: "/leases/123", status: 200, body: "<title>app</title>", cache: "no-cache"},
		{method: "HEAD", path: "/tax", status: 200, cache: "no-cache"},
		{method: "GET", path: "/assets/app-1a2b3c.js", status: 200, body: "console.log", cache: "public, max-age=31536000, immutable"},
		{method: "GET", path: "/../../etc/passwd", status: 200, body: "<title>app</title>", cache: "no-cache"},
		{method: "GET", path: "/assets/missing.js", status: 404, problem: true},
		{method: "GET", path: "/assets", status: 200, body: "<title>app</title>", cache: "no-cache"},
		{method: "POST", path: "/leases", status: 404, problem: true},
		{method: "GET", path: "/api/v2/x", status: 404, problem: true},
		{method: "GET", path: "/api", status: 404, problem: true},
		{method: "GET", path: "/bff/unknown", status: 404, problem: true},
		{method: "GET", path: "/api/v1/unknown", status: 401, problem: true},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		if rec.Code != c.status {
			t.Errorf("%s %s: %d, want %d", c.method, c.path, rec.Code, c.status)
			continue
		}
		ct := rec.Header().Get("Content-Type")
		if c.problem {
			if !strings.HasPrefix(ct, "application/problem+json") {
				t.Errorf("%s %s: content type %q", c.method, c.path, ct)
			}
			continue
		}
		if !strings.Contains(rec.Body.String(), c.body) || rec.Header().Get("Cache-Control") != c.cache {
			t.Errorf("%s %s: %q cache %q", c.method, c.path, rec.Body.String(), rec.Header().Get("Cache-Control"))
		}
		if csp := rec.Header().Get("Content-Security-Policy"); csp != webCSP {
			t.Errorf("%s %s: CSP %q", c.method, c.path, csp)
		}
	}

	// Without a bundle every such path is a 404 problem.
	h, err = NewRouter(Deps{Auth: a})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 404 {
		t.Errorf("no bundle: %d", rec.Code)
	}
}
