package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ovander/tenure-api/internal/auth"
)

func testRouter(t *testing.T) http.Handler {
	t.Helper()
	jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"keys":[]}`))
	}))
	t.Cleanup(jwks.Close)
	a, err := auth.New(auth.Options{JWKSURL: jwks.URL, Issuer: "https://socrate.test", Audience: "tenure-api",
		AdminRole: "admin", StepUpMaxAge: 5 * time.Minute}, nil)
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewRouter(Deps{Auth: a})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// Deny by default (RENT-BE-001 §4): every /api/v1 route answers 401 without a
// valid Socrate token; only /healthz and /readyz are open.
func TestEveryRouteRequiresAToken(t *testing.T) {
	h := testRouter(t)
	routes := Routes(Deps{})
	if len(routes) == 0 {
		t.Fatal("no routes")
	}
	n := 0
	for _, rt := range routes {
		for _, authz := range []string{"", "Bearer not-a-jwt", "Basic dXNlcjpwYXNz"} {
			n++
			path := "/api/v1" + strings.NewReplacer("{id}", "01900000-0000-7000-8000-000000000001", "{key}", "irl_cap").Replace(rt.Pattern)
			req := httptest.NewRequest(rt.Method, path, strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
			if authz != "" {
				req.Header.Set("Authorization", authz)
			}
			// One client address per request keeps the failed-auth lockout out of the way.
			req.RemoteAddr = fmt.Sprintf("198.18.%d.%d:1234", n/250, n%250+1)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("%s %s with %q: %d; want 401", rt.Method, rt.Pattern, authz, rec.Code)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
				t.Errorf("%s %s: content type %q", rt.Method, rt.Pattern, ct)
			}
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("/healthz: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	unknown := httptest.NewRequest("GET", "/api/v1/nothing-here", nil)
	unknown.RemoteAddr = "198.19.0.1:1234"
	h.ServeHTTP(rec, unknown)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("unknown /api/v1 path without a token: %d; want 401 before 404", rec.Code)
	}
}

func TestFailedAuthLockout(t *testing.T) {
	h := testRouter(t)
	for i := 0; i < 12; i++ {
		req := httptest.NewRequest("GET", "/api/v1/me", nil)
		req.RemoteAddr = "198.51.100.7:4000"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		want := http.StatusUnauthorized
		if i >= 10 {
			want = http.StatusTooManyRequests
		}
		if rec.Code != want {
			t.Fatalf("attempt %d: %d; want %d", i+1, rec.Code, want)
		}
	}
	req := httptest.NewRequest("GET", "/api/v1/me", nil)
	req.RemoteAddr = "198.51.100.8:4000"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("another address is locked out too: %d", rec.Code)
	}
}

func TestEveryRouteHasASwagPath(t *testing.T) {
	for _, rt := range Routes(Deps{}) {
		if rt.Pattern == "/openapi.json" {
			continue
		}
		if !strings.Contains(string(openAPI), `"`+rt.Pattern+`"`) {
			t.Errorf("%s %s is missing from openapi/swagger.json; run swag init (see CONTRIBUTING)", rt.Method, rt.Pattern)
		}
	}
}
