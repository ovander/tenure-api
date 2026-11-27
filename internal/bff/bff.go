// Package bff is Tenure's Backend-for-Frontend (RENT-BE-001 §4): the browser
// never holds an OAuth token. tenure is Socrate's confidential client; it runs
// the authorization-code flow with PKCE server-side, keeps the tokens in an
// encrypted server-side session (backendkit bff) and gives the browser only an
// HttpOnly, SameSite=Strict, __Host- session cookie.
//
// The web bundle and the API share one origin, so the session is turned into
// a bearer in process rather than through a proxy: Authenticate runs in front
// of internal/auth on /api/v1, checks the CSRF token of an unsafe request,
// refreshes the access token when needed and sets the Authorization header;
// internal/auth then validates that token like any other. This package stores
// and forwards tokens but never parses one.
package bff

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/go-chi/chi/v5"
	kit "github.com/ovander/backendkit/bff"
	"github.com/ovander/backendkit/socrate"

	"github.com/ovander/tenure-api/internal/problem"
)

// Session lifetimes (RENT-SPEC-022: refresh 8 h): a session ends after 30
// minutes without a request, and 8 hours after the login in any case.
const (
	IdleTimeout     = 30 * time.Minute
	AbsoluteTimeout = 8 * time.Hour
)

// Cookie names, sent as __Host-<name> over HTTPS.
const (
	SessionCookie = "tenure_session"
	LoginCookie   = "tenure_login"
)

// Problem types of the BFF.
const (
	TypeLoginInvalid       = "bff/login-invalid"
	TypeLoginFailed        = "bff/login-failed"
	TypeLoginBusy          = "bff/login-unavailable"
	TypeCSRF               = "bff/csrf-failed"
	TypeSessionExpired     = "auth/session-expired"
	TypeRefreshUnavailable = "bff/refresh-unavailable"
)

func init() {
	problem.Register(map[string]problem.Message{
		TypeLoginInvalid:       problem.M(400, "Cette connexion n’est plus valable ; recommencez depuis Tenure.", "This sign-in is no longer valid; start again from Tenure."),
		TypeLoginFailed:        problem.M(502, "Socrate n’a pas validé la connexion ; réessayez.", "Socrate did not complete the sign-in; try again."),
		TypeLoginBusy:          problem.M(503, "Trop de connexions en cours ; réessayez dans un instant.", "Too many sign-ins in progress; try again shortly."),
		TypeCSRF:               problem.M(403, "Jeton anti-falsification absent ou invalide.", "Missing or invalid anti-forgery token."),
		TypeSessionExpired:     problem.M(401, "Votre session a expiré ; reconnectez-vous.", "Your session has expired; sign in again."),
		TypeRefreshUnavailable: problem.M(502, "Socrate est momentanément injoignable ; réessayez.", "Socrate cannot be reached right now; try again."),
	})
}

// OAuth is what the BFF asks of Socrate; *socrate.Client satisfies it.
type OAuth interface {
	ExchangeCode(ctx context.Context, code, redirectURI, codeVerifier string) (*socrate.TokenSet, error)
	RefreshToken(ctx context.Context, refreshToken string) (*socrate.TokenSet, error)
	RevokeToken(ctx context.Context, token string) error
	GetCurrentUserProfile(ctx context.Context) (*socrate.ProfileInfo, error)
}

// Config is the BFF's view of the configuration.
type Config struct {
	// Issuer is Socrate's OAuth base URL; its /oauth/authorize starts a login.
	Issuer string
	// ClientID is tenure's confidential client at Socrate.
	ClientID string
	// PublicURL is the origin the browser uses (https://…); the callback is
	// <PublicURL>/bff/callback. Its scheme decides the Secure cookie.
	PublicURL *url.URL
}

// Server serves /bff and turns sessions into bearers.
type Server struct {
	cfg      Config
	oauth    OAuth
	sessions kit.SessionStore
	pending  kit.PendingLoginStore
	gw       *kit.Gateway
	login    kit.LoginBinding
	now      func() time.Time
}

// New returns the BFF over its stores (bff.PostgresStore and
// bff.PostgresPendingLoginStore in production).
func New(cfg Config, oauth OAuth, sessions kit.SessionStore, pending kit.PendingLoginStore, now func() time.Time) *Server {
	if now == nil {
		now = time.Now
	}
	secure := cfg.PublicURL.Scheme == "https"
	return &Server{cfg: cfg, oauth: oauth, sessions: sessions, pending: pending, now: now,
		gw:    &kit.Gateway{Store: sessions, Cookie: kit.CookieConfig{Name: SessionCookie, Secure: secure}, Refresher: oauth, Now: now},
		login: kit.LoginBinding{Cookie: kit.CookieConfig{Name: LoginCookie, Secure: secure}, TTL: kit.DefaultPendingLoginTTL}}
}

func (s *Server) redirectURI() string { return s.cfg.PublicURL.JoinPath("/bff/callback").String() }

// Mount adds the /bff routes. None requires a token: they are how a browser
// gets a session (RENT-BE-001 §4).
func (s *Server) Mount(r chi.Router) {
	r.Get("/bff/login", s.Login)
	r.Get("/bff/callback", s.Callback)
	r.Get("/bff/session", s.Session)
	r.Post("/bff/logout", s.Logout)
}

// Login starts a sign-in: a PKCE pair, a state and a browser binding are kept
// server-side, and the browser goes to Socrate. ?return_to= is the same-origin
// path to come back to; ?step_up=1 asks Socrate for a fresh authentication
// with prompt=login, which the step_up routes need (Q-85). max_age=0 is not
// sent: Socrate answers it with login_required instead of a login page.
func (s *Server) Login(w http.ResponseWriter, r *http.Request) {
	p, state := kit.NewPKCE(), kit.RandomToken(32)
	err := s.pending.Put(r.Context(), state, kit.PendingLogin{Verifier: p.Verifier, Nonce: s.login.Begin(w),
		ReturnTo: kit.SanitizeReturnTo(r.URL.Query().Get("return_to")), Created: s.now()})
	if err != nil {
		slog.WarnContext(r.Context(), "bff login refused", "error", err)
		problem.Write(w, r, TypeLoginBusy, "", "", nil)
		return
	}
	q := url.Values{"response_type": {"code"}, "client_id": {s.cfg.ClientID}, "redirect_uri": {s.redirectURI()},
		"scope": {"openid profile email"}, "state": {state}, "code_challenge": {p.Challenge}, "code_challenge_method": {"S256"}}
	if r.URL.Query().Get("step_up") == "1" {
		q.Set("prompt", "login")
	}
	http.Redirect(w, r, s.cfg.Issuer+"/oauth/authorize?"+q.Encode(), http.StatusFound)
}

// Callback completes a sign-in: the state is taken once, the browser must be
// the one that started it, the code is exchanged server-side, and a new
// session replaces any previous one of this browser.
func (s *Server) Callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	st, ok := s.pending.Take(r.Context(), q.Get("state"))
	if !ok || !s.login.Verify(w, r, st.Nonce) || q.Get("error") != "" || q.Get("code") == "" {
		problem.Write(w, r, TypeLoginInvalid, "", q.Get("error"), nil)
		return
	}
	ts, err := s.oauth.ExchangeCode(r.Context(), q.Get("code"), s.redirectURI(), st.Verifier)
	if err != nil {
		slog.WarnContext(r.Context(), "bff code exchange failed", "error", err)
		problem.Write(w, r, TypeLoginFailed, "", "", nil)
		return
	}
	user := kit.UserInfo{Roles: ts.Roles}
	if p, err := s.oauth.GetCurrentUserProfile(socrate.WithJWT(r.Context(), ts.AccessToken)); err == nil && p != nil {
		user.Sub, user.Email, user.Name = p.Sub, p.Email, p.Name
	}
	if old, ok := s.gw.SessionFromRequest(r); ok {
		s.sessions.Delete(old.ID())
	}
	sess := kit.NewSession(kit.RandomToken(32), kit.RandomToken(32), ts, user, s.now())
	s.sessions.Put(sess)
	s.gw.Cookie.SetSession(w, sess.ID())
	slog.InfoContext(r.Context(), "bff login", "sub", user.Sub)
	http.Redirect(w, r, st.ReturnTo, http.StatusFound)
}

// SessionView is GET /bff/session: who is signed in and the CSRF token to
// echo as X-CSRF-Token on every unsafe request; never a token.
type SessionView struct {
	Authenticated bool          `json:"authenticated"`
	User          *kit.UserInfo `json:"user,omitempty"`
	CSRF          string        `json:"csrf,omitempty"`
}

// Session reports the browser's session.
func (s *Server) Session(w http.ResponseWriter, r *http.Request) {
	v := SessionView{}
	if sess, ok := s.gw.SessionFromRequest(r); ok {
		u := sess.User()
		v = SessionView{Authenticated: true, User: &u, CSRF: sess.CSRF()}
	}
	writeJSON(w, http.StatusOK, v)
}

// Logout ends the browser's session: its refresh token is revoked at Socrate
// (best effort), the session deleted and the cookie cleared. It needs the
// session's CSRF token, so another site cannot sign the user out.
func (s *Server) Logout(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.gw.SessionFromRequest(r)
	if !ok {
		s.gw.Cookie.ClearSession(w)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !s.gw.CheckCSRF(r, sess) {
		problem.Write(w, r, TypeCSRF, "", "", nil)
		return
	}
	if rt := sess.RefreshToken(); rt != "" {
		if err := s.oauth.RevokeToken(r.Context(), rt); err != nil {
			slog.WarnContext(r.Context(), "bff revoke failed", "error", err)
		}
	}
	s.sessions.Delete(sess.ID())
	s.gw.Cookie.ClearSession(w)
	slog.InfoContext(r.Context(), "bff logout", "sub", sess.User().Sub)
	w.WriteHeader(http.StatusNoContent)
}

// Authenticate turns a browser session into a bearer for the API. A request
// that carries an Authorization header (a machine client) or no session
// passes unchanged, and internal/auth decides; with a session, an unsafe
// method needs its CSRF token (403), the access token is refreshed when it is
// about to expire, and a refresh Socrate rejects ends the session (401). The
// idle clock is persisted at most once a minute.
func (s *Server) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			next.ServeHTTP(w, r)
			return
		}
		sess, ok := s.gw.SessionFromRequest(r)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		if !s.gw.CheckCSRF(r, sess) {
			problem.Write(w, r, TypeCSRF, "", "", nil)
			return
		}
		tok, err := s.gw.EnsureFresh(r.Context(), sess)
		if err != nil {
			if kit.IsFatalRefreshError(err) {
				s.sessions.Delete(sess.ID())
				s.gw.Cookie.ClearSession(w)
				problem.Write(w, r, TypeSessionExpired, "", "", nil)
				return
			}
			slog.WarnContext(r.Context(), "bff refresh failed", "error", err)
			problem.Write(w, r, TypeRefreshUnavailable, "", "", nil)
			return
		}
		if now := s.now(); now.Sub(sess.LastSeen()) > time.Minute {
			sess.Touch(now)
			s.sessions.Put(sess)
		}
		r = r.Clone(r.Context())
		r.Header.Set("Authorization", "Bearer "+tok)
		next.ServeHTTP(w, r)
	})
}

// Sweep removes expired sessions and pending logins every minute until ctx
// ends.
func (s *Server) Sweep(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.sessions.Sweep()
			s.pending.Sweep(ctx)
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
