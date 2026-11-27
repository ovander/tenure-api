package bff

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	kit "github.com/ovander/backendkit/bff"
	"github.com/ovander/backendkit/socrate"
)

// fakeOAuth stands for Socrate; every token here is an opaque test string.
type fakeOAuth struct {
	code, verifier, redirect string
	refreshErr               error
	refreshed, revoked       []string
}

func (f *fakeOAuth) ExchangeCode(_ context.Context, code, redirectURI, verifier string) (*socrate.TokenSet, error) {
	f.code, f.verifier, f.redirect = code, verifier, redirectURI
	if code != "good-code" {
		return nil, &socrate.OAuthError{StatusCode: 400, Code: "invalid_grant"}
	}
	return &socrate.TokenSet{AccessToken: "access-1", RefreshToken: "refresh-1", ExpiresIn: 900, Roles: []string{"user"}}, nil
}

func (f *fakeOAuth) RefreshToken(_ context.Context, rt string) (*socrate.TokenSet, error) {
	f.refreshed = append(f.refreshed, rt)
	if f.refreshErr != nil {
		return nil, f.refreshErr
	}
	return &socrate.TokenSet{AccessToken: "access-2", RefreshToken: "refresh-2", ExpiresIn: 900}, nil
}

func (f *fakeOAuth) RevokeToken(_ context.Context, token string) error {
	f.revoked = append(f.revoked, token)
	return nil
}

func (f *fakeOAuth) GetCurrentUserProfile(context.Context) (*socrate.ProfileInfo, error) {
	return &socrate.ProfileInfo{Sub: "1001", Name: "Propriétaire A"}, nil
}

type harness struct {
	srv   *Server
	oauth *fakeOAuth
	h     http.Handler
	store *kit.MemoryStore
	now   time.Time
	// seen is the Authorization the API received.
	seen string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	pub, _ := url.Parse("https://tenure.example.test")
	x := &harness{oauth: &fakeOAuth{}, store: kit.NewMemoryStore(IdleTimeout, AbsoluteTimeout), now: time.Now()}
	x.srv = New(Config{Issuer: "https://socrate.example.test", ClientID: "tenure-web", PublicURL: pub}, x.oauth, x.store,
		kit.NewMemoryPendingLoginStore(kit.DefaultPendingLoginTTL, 10), func() time.Time { return x.now })
	r := chi.NewRouter()
	x.srv.Mount(r)
	r.With(x.srv.Authenticate).HandleFunc("/api/v1/*", func(w http.ResponseWriter, r *http.Request) {
		x.seen = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	})
	x.h = r
	return x
}

func (x *harness) do(method, path string, cookies []*http.Cookie, header map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	x.h.ServeHTTP(rec, req)
	return rec
}

// login runs /bff/login then /bff/callback and returns the session cookie.
func (x *harness) login(t *testing.T, query string) *http.Cookie {
	t.Helper()
	rec := x.do("GET", "/bff/login"+query, nil, nil)
	if rec.Code != http.StatusFound {
		t.Fatalf("login: %d", rec.Code)
	}
	loc, _ := url.Parse(rec.Header().Get("Location"))
	state := loc.Query().Get("state")
	cb := x.do("GET", "/bff/callback?code=good-code&state="+url.QueryEscape(state), rec.Result().Cookies(), nil)
	if cb.Code != http.StatusFound {
		t.Fatalf("callback: %d %s", cb.Code, cb.Body)
	}
	for _, c := range cb.Result().Cookies() {
		if c.Name == "__Host-"+SessionCookie && c.Value != "" {
			return c
		}
	}
	t.Fatal("no session cookie")
	return nil
}

func TestLoginRedirect(t *testing.T) {
	x := newHarness(t)
	rec := x.do("GET", "/bff/login?return_to=/leases/42", nil, nil)
	loc, err := url.Parse(rec.Header().Get("Location"))
	if rec.Code != http.StatusFound || err != nil || loc.Host != "socrate.example.test" || loc.Path != "/oauth/authorize" {
		t.Fatalf("redirect %d %s", rec.Code, loc)
	}
	q := loc.Query()
	if q.Get("client_id") != "tenure-web" || q.Get("redirect_uri") != "https://tenure.example.test/bff/callback" ||
		q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("state") == "" || q.Get("prompt") != "" {
		t.Errorf("authorize query %v", q)
	}
	if strings.Contains(loc.RawQuery, "client_secret") {
		t.Error("the client secret reached the browser")
	}
	var binding *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "__Host-"+LoginCookie {
			binding = c
		}
	}
	if binding == nil || !binding.HttpOnly || !binding.Secure {
		t.Errorf("login binding cookie %+v", binding)
	}
	step := x.do("GET", "/bff/login?step_up=1", nil, nil)
	sq, _ := url.Parse(step.Header().Get("Location"))
	// prompt=login alone: Socrate answers max_age=0 with login_required (Q-85).
	if sq.Query().Get("prompt") != "login" || sq.Query().Has("max_age") {
		t.Errorf("step-up login %v", sq.Query())
	}
}

func TestCallback(t *testing.T) {
	x := newHarness(t)
	rec := x.do("GET", "/bff/login?return_to=//evil.example/x", nil, nil)
	loc, _ := url.Parse(rec.Header().Get("Location"))
	state, binding := loc.Query().Get("state"), rec.Result().Cookies()
	// Another browser (no binding cookie) cannot finish this login.
	if r := x.do("GET", "/bff/callback?code=good-code&state="+state, nil, nil); r.Code != http.StatusBadRequest {
		t.Errorf("callback without the binding: %d", r.Code)
	}
	// The state was spent by that attempt: single use.
	if r := x.do("GET", "/bff/callback?code=good-code&state="+state, binding, nil); r.Code != http.StatusBadRequest {
		t.Errorf("replayed state: %d", r.Code)
	}
	rec = x.do("GET", "/bff/login?return_to=//evil.example/x", nil, nil)
	loc, _ = url.Parse(rec.Header().Get("Location"))
	cb := x.do("GET", "/bff/callback?code=good-code&state="+loc.Query().Get("state"), rec.Result().Cookies(), nil)
	if cb.Code != http.StatusFound || strings.Contains(cb.Header().Get("Location"), "evil") {
		t.Errorf("callback %d to %q: an open redirect", cb.Code, cb.Header().Get("Location"))
	}
	if x.oauth.verifier == "" || x.oauth.redirect != "https://tenure.example.test/bff/callback" {
		t.Errorf("exchange %+v", x.oauth)
	}
	rec = x.do("GET", "/bff/login", nil, nil)
	loc, _ = url.Parse(rec.Header().Get("Location"))
	if r := x.do("GET", "/bff/callback?code=bad&state="+loc.Query().Get("state"), rec.Result().Cookies(), nil); r.Code != http.StatusBadGateway {
		t.Errorf("rejected code: %d", r.Code)
	}
}

func TestSessionAndBearer(t *testing.T) {
	x := newHarness(t)
	if r := x.do("GET", "/bff/session", nil, nil); !strings.Contains(r.Body.String(), `"authenticated":false`) {
		t.Errorf("anonymous session: %s", r.Body)
	}
	c := x.login(t, "")
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode {
		t.Errorf("session cookie %+v", c)
	}
	r := x.do("GET", "/bff/session", []*http.Cookie{c}, nil)
	if !strings.Contains(r.Body.String(), `"authenticated":true`) || strings.Contains(r.Body.String(), "access-1") ||
		strings.Contains(r.Body.String(), "refresh-1") {
		t.Fatalf("session view leaks a token or is not signed in: %s", r.Body)
	}
	sess, _ := x.store.Get(c.Value)
	csrf := sess.CSRF()

	x.do("GET", "/api/v1/me", []*http.Cookie{c}, nil)
	if x.seen != "Bearer access-1" {
		t.Errorf("bearer injected %q", x.seen)
	}
	if r := x.do("POST", "/api/v1/assets", []*http.Cookie{c}, nil); r.Code != http.StatusForbidden {
		t.Errorf("unsafe call without CSRF: %d", r.Code)
	}
	if r := x.do("POST", "/api/v1/assets", []*http.Cookie{c}, map[string]string{"X-CSRF-Token": csrf}); r.Code != http.StatusOK {
		t.Errorf("unsafe call with CSRF: %d", r.Code)
	}
	// A machine client keeps its own bearer; the session is not consulted.
	x.do("GET", "/api/v1/me", []*http.Cookie{c}, map[string]string{"Authorization": "Bearer machine"})
	if x.seen != "Bearer machine" {
		t.Errorf("bearer client %q", x.seen)
	}
	// No session: nothing injected, the API answers 401 itself.
	x.seen = ""
	x.do("GET", "/api/v1/me", nil, nil)
	if x.seen != "" {
		t.Errorf("anonymous request got %q", x.seen)
	}
	// Near expiry the token is refreshed server-side.
	x.now = x.now.Add(16 * time.Minute)
	x.do("GET", "/api/v1/me", []*http.Cookie{c}, nil)
	if x.seen != "Bearer access-2" || len(x.oauth.refreshed) != 1 || x.oauth.refreshed[0] != "refresh-1" {
		t.Errorf("refresh: %q %v", x.seen, x.oauth.refreshed)
	}
}

func TestRefreshRejectedEndsTheSession(t *testing.T) {
	x := newHarness(t)
	c := x.login(t, "")
	x.now = x.now.Add(16 * time.Minute)
	x.oauth.refreshErr = errors.New("connection refused")
	if r := x.do("GET", "/api/v1/me", []*http.Cookie{c}, nil); r.Code != http.StatusBadGateway {
		t.Errorf("transient refresh failure: %d", r.Code)
	}
	if _, ok := x.store.Get(c.Value); !ok {
		t.Error("a transient failure ended the session")
	}
	x.oauth.refreshErr = &socrate.OAuthError{StatusCode: 400, Code: "invalid_grant"}
	if r := x.do("GET", "/api/v1/me", []*http.Cookie{c}, nil); r.Code != http.StatusUnauthorized || !strings.Contains(r.Body.String(), TypeSessionExpired) {
		t.Errorf("rejected refresh: %d %s", r.Code, r.Body)
	}
	if _, ok := x.store.Get(c.Value); ok {
		t.Error("a rejected refresh kept the session")
	}
}

func TestLogout(t *testing.T) {
	x := newHarness(t)
	c := x.login(t, "")
	sess, _ := x.store.Get(c.Value)
	if r := x.do("POST", "/bff/logout", []*http.Cookie{c}, nil); r.Code != http.StatusForbidden {
		t.Errorf("logout without CSRF: %d", r.Code)
	}
	if r := x.do("POST", "/bff/logout", []*http.Cookie{c}, map[string]string{"X-CSRF-Token": sess.CSRF()}); r.Code != http.StatusNoContent {
		t.Errorf("logout: %d", r.Code)
	}
	if _, ok := x.store.Get(c.Value); ok || len(x.oauth.revoked) != 1 || x.oauth.revoked[0] != "refresh-1" {
		t.Errorf("after logout: revoked %v", x.oauth.revoked)
	}
	x.seen = ""
	x.do("GET", "/api/v1/me", []*http.Cookie{c}, nil)
	if x.seen != "" {
		t.Error("a logged-out cookie still authenticates")
	}
}
