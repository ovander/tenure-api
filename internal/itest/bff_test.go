//go:build integration

package itest

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	kit "github.com/ovander/backendkit/bff"
	"github.com/ovander/backendkit/socrate"

	"github.com/ovander/tenure-api/internal/bff"
	"github.com/ovander/tenure-api/internal/db"
)

// fakeSocrate is Socrate's token, userinfo and revoke endpoints for the BFF:
// the code "sub:<sub>" signs in that subject, with a fresh MFA
// authentication, under the suite's signing key.
func fakeSocrate() http.Handler {
	sign := func(sub string) string {
		c := jwt.MapClaims{"iss": issuer, "aud": audience, "sub": sub, "iat": time.Now().Unix(),
			"exp": time.Now().Add(10 * time.Minute).Unix(), "role": "user",
			"auth_time": time.Now().Unix(), "amr": []string{"pwd", "otp", "mfa"}}
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, c)
		tok.Header["kid"] = "itest"
		s, _ := tok.SignedString(env.key)
		return s
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		sub := ""
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			if r.Form.Get("code_verifier") == "" || r.Form.Get("client_secret") != "itest-client-secret" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
				return
			}
			sub, _ = strings.CutPrefix(r.Form.Get("code"), "sub:")
		case "refresh_token":
			sub, _ = strings.CutPrefix(r.Form.Get("refresh_token"), "refresh:")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(socrate.TokenSet{AccessToken: sign(sub), RefreshToken: "refresh:" + sub, TokenType: "Bearer",
			ExpiresIn: 600, Roles: []string{"user"}})
	})
	mux.HandleFunc("GET /oauth/userinfo", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"sub": "bff-user"})
	})
	mux.HandleFunc("POST /oauth/revoke", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	return mux
}

// newBFF builds the BFF as tenure serve does: PostgreSQL stores on the
// tables of migration 000006, run by tenure_api with the managed schema.
func newBFF(ctx context.Context, socrateURL string, api *db.DB) (*bff.Server, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	client, err := socrate.NewClient(socrate.ClientConfig{BaseURL: socrateURL, ClientID: "tenure-itest", ClientSecret: "itest-client-secret"})
	if err != nil {
		return nil, err
	}
	sessions, err := kit.NewPostgresStore(ctx, api.SQL(), key, bff.IdleTimeout, bff.AbsoluteTimeout, kit.WithPostgresManagedSchema())
	if err != nil {
		return nil, err
	}
	pending, err := kit.NewPostgresPendingLoginStore(ctx, api.SQL(), key, kit.DefaultPendingLoginTTL, kit.WithPendingLoginManagedSchema())
	if err != nil {
		return nil, err
	}
	pub, _ := url.Parse("http://127.0.0.1:8080")
	return bff.New(bff.Config{Issuer: socrateURL, ClientID: "tenure-itest", PublicURL: pub}, client, sessions, pending, nil), nil
}

type browser struct {
	t       *testing.T
	cookies map[string]*http.Cookie
}

func (b *browser) do(method, path string, body string, header map[string]string) resp {
	b.t.Helper()
	var r *http.Request
	if body != "" {
		r, _ = http.NewRequest(method, env.server.URL+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r, _ = http.NewRequest(method, env.server.URL+path, nil)
	}
	for _, c := range b.cookies {
		r.AddCookie(c)
	}
	for k, v := range header {
		r.Header.Set(k, v)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(r)
	if err != nil {
		b.t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	for _, c := range res.Cookies() {
		if c.MaxAge < 0 || c.Value == "" {
			delete(b.cookies, c.Name)
		} else {
			b.cookies[c.Name] = c
		}
	}
	bodyBytes, _ := io.ReadAll(res.Body)
	return resp{Status: res.StatusCode, Header: res.Header, Body: bodyBytes}
}

// A browser signs in through the BFF and uses the API with its session
// cookie only: it never holds a token (RENT-BE-001 §4).
func TestBFF(t *testing.T) {
	const sub = "7001"
	b := &browser{t: t, cookies: map[string]*http.Cookie{}}
	r := b.do("GET", "/bff/login?return_to=/biens", "", nil)
	loc, err := url.Parse(r.Header.Get("Location"))
	if r.Status != http.StatusFound || err != nil || loc.Query().Get("state") == "" {
		t.Fatalf("login: %d %s", r.Status, r.Header.Get("Location"))
	}
	state := loc.Query().Get("state")
	r = b.do("GET", "/bff/callback?code=sub:"+sub+"&state="+url.QueryEscape(state), "", nil)
	if r.Status != http.StatusFound || r.Header.Get("Location") != "/biens" {
		t.Fatalf("callback: %d %s %s", r.Status, r.Header.Get("Location"), r.Body)
	}
	if b.cookies[bff.SessionCookie] == nil || !b.cookies[bff.SessionCookie].HttpOnly {
		t.Fatalf("no HttpOnly session cookie: %v", b.cookies)
	}
	if n := superCount(t, "SELECT count(*) FROM bff_store_sessions WHERE deleted_at IS NULL"); n == 0 {
		t.Error("the session is not in PostgreSQL")
	}
	if n := superCount(t, "SELECT count(*) FROM bff_store_sessions WHERE position('refresh:'::bytea in data) > 0"); n != 0 {
		t.Error("a refresh token is stored in clear")
	}
	var sess struct {
		Authenticated bool
		CSRF          string
	}
	if err := json.Unmarshal(b.do("GET", "/bff/session", "", nil).Body, &sess); err != nil || !sess.Authenticated || sess.CSRF == "" {
		t.Fatalf("session: %+v %v", sess, err)
	}
	var me struct{ Sub string }
	if r := b.do("GET", "/api/v1/me", "", nil); r.Status != 200 {
		t.Fatalf("me through the session: %d %s", r.Status, r.Body)
	} else {
		r.json(t, &me)
	}
	if me.Sub != sub {
		t.Errorf("me %+v", me)
	}
	if r := b.do("PUT", "/api/v1/me", `{"legal_name":"Bailleur BFF","address":"1 rue BFF"}`, nil); r.Status != 403 || r.problemType(t) != bff.TypeCSRF {
		t.Errorf("write without CSRF: %d %s", r.Status, r.Body)
	}
	if r := b.do("PUT", "/api/v1/me", `{"legal_name":"Bailleur BFF","address":"1 rue BFF"}`, map[string]string{"X-CSRF-Token": sess.CSRF}); r.Status != 201 {
		t.Errorf("write with CSRF: %d %s", r.Status, r.Body)
	}
	// A step-up route accepts the session's fresh MFA authentication.
	if r := b.do("GET", "/api/v1/audit/events.csv", "", nil); r.Status != 200 {
		t.Errorf("step-up route through the session: %d %s", r.Status, r.Body)
	}
	if r := b.do("POST", "/bff/logout", "", map[string]string{"X-CSRF-Token": sess.CSRF}); r.Status != http.StatusNoContent {
		t.Errorf("logout: %d %s", r.Status, r.Body)
	}
	if r := b.do("GET", "/api/v1/me", "", nil); r.Status != 401 {
		t.Errorf("after logout: %d", r.Status)
	}
	// A callback replayed with a spent state is refused.
	if r := b.do("GET", "/bff/callback?code=sub:"+sub+"&state="+url.QueryEscape(state), "", nil); r.Status != 400 {
		t.Errorf("replayed callback: %d", r.Status)
	}
}
