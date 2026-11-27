package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestFresh(t *testing.T) {
	now := time.Date(2025, 9, 1, 12, 0, 0, 0, time.UTC)
	mfa := []string{"pwd", "otp", "mfa"}
	cases := []struct {
		name string
		c    Claims
		want bool
	}{
		{"fresh with mfa", Claims{AuthTime: now.Add(-4 * time.Minute), AMR: mfa}, true},
		{"exactly at the limit", Claims{AuthTime: now.Add(-5 * time.Minute), AMR: mfa}, true},
		{"stale", Claims{AuthTime: now.Add(-5*time.Minute - time.Second), AMR: mfa}, false},
		{"no mfa", Claims{AuthTime: now.Add(-time.Minute), AMR: []string{"pwd"}}, false},
		{"no auth_time", Claims{AMR: mfa}, false},
		{"far in the future", Claims{AuthTime: now.Add(2 * time.Minute), AMR: mfa}, false},
	}
	for _, c := range cases {
		if got := Fresh(c.c, now, 5*time.Minute); got != c.want {
			t.Errorf("%s: %v", c.name, got)
		}
	}
}

func TestGuards(t *testing.T) {
	own := Scope{OwnerID: uuid.New(), Role: RoleOwner}
	acc := Scope{OwnerID: uuid.New(), Role: RoleAccountant}
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	run := func(h http.Handler, method string, p *Principal) int {
		req := httptest.NewRequest(method, "/x", nil)
		if p != nil {
			req = req.WithContext(WithPrincipal(context.Background(), p))
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	cases := []struct {
		name   string
		h      http.Handler
		method string
		p      *Principal
		want   int
	}{
		{"no principal", RequireScope(ok), "GET", nil, 401},
		{"no scope", RequireScope(ok), "GET", &Principal{Sub: "1"}, 403},
		{"owner reads", RequireOwnerForWrites(ok), "GET", &Principal{Sub: "1", Active: &own}, 204},
		{"owner writes", RequireOwnerForWrites(ok), "POST", &Principal{Sub: "1", Active: &own}, 204},
		{"accountant reads", RequireOwnerForWrites(ok), "GET", &Principal{Sub: "1", Active: &acc}, 204},
		{"accountant writes", RequireOwnerForWrites(ok), "PUT", &Principal{Sub: "1", Active: &acc}, 403},
		{"roles", RequireRoles(RoleOwner, RoleAccountant)(ok), "GET", &Principal{Sub: "1", Active: &Scope{Role: RoleCoOwner}}, 403},
		{"admin", RequireAdmin(ok), "PUT", &Principal{Sub: "1", Admin: true}, 204},
		{"not admin", RequireAdmin(ok), "PUT", &Principal{Sub: "1"}, 403},
	}
	for _, c := range cases {
		if got := run(c.h, c.method, c.p); got != c.want {
			t.Errorf("%s: %d; want %d", c.name, got, c.want)
		}
	}
}

// TestRefusal: a step-up refused says why, and a recent sign-in without a
// second factor is told apart: signing in again would not help (#262).
func TestRefusal(t *testing.T) {
	now := time.Date(2025, 9, 1, 12, 0, 0, 0, time.UTC)
	cases := map[string]struct {
		c    Claims
		want string
	}{
		"no auth_time":  {Claims{AMR: []string{"pwd", "otp", "mfa"}}, "auth_time_missing"},
		"stale":         {Claims{AuthTime: now.Add(-10 * time.Minute), AMR: []string{"pwd", "otp", "mfa"}}, "auth_time_stale"},
		"future":        {Claims{AuthTime: now.Add(2 * time.Minute), AMR: []string{"pwd", "otp", "mfa"}}, "auth_time_stale"},
		"recent no mfa": {Claims{AuthTime: now.Add(-20 * time.Second), AMR: []string{"pwd"}}, "mfa_missing"},
	}
	for name, c := range cases {
		if got := refusal(c.c, now, 5*time.Minute); got != c.want {
			t.Errorf("%s: %s; want %s", name, got, c.want)
		}
	}
	if authAge(Claims{}, now) != -1 || authAge(Claims{AuthTime: now.Add(-20 * time.Second)}, now) != 20 {
		t.Error("authAge")
	}
}
