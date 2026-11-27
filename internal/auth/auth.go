// Package auth is the only package that reads a token (RENT-BE-001 §4,
// RENT-SPEC-006). It wraps backendkit's jwtauth middleware, which validates the
// Socrate RS256 token (issuer, audience, expiry, key) and puts its claims in
// the request context; this package then reads those claims through Claims and
// resolves the caller's owner scope:
//
//   - the caller's own scope comes from owners.socrate_sub (role owner);
//   - other scopes come from live owner_grants rows (co-owner, accountant);
//   - X-Owner-Scope selects one of them; the default is the caller's own;
//   - admin comes from the Socrate role claim, never from the database.
//
// There is no local user, password, API key or login route. Every route is
// denied by default: only /healthz and /readyz are outside Authenticate.
package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/ovander/backendkit/apierror"
	"github.com/ovander/backendkit/ctxutil"
	"github.com/ovander/backendkit/jwtauth"
	"github.com/sirupsen/logrus"

	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/problem"
)

// Role is the caller's role in an owner scope.
type Role string

// Roles (RENT-BE-001 §4). Admin is not a scope role: see Principal.Admin.
const (
	RoleOwner      Role = "owner"
	RoleCoOwner    Role = "co_owner"
	RoleAccountant Role = "accountant"
	// RoleManager is the grant role of an SCI's manager (gérant, Q-133). It
	// resolves to RoleOwner in the SCI's scope: Scope.Manager says it.
	RoleManager Role = "manager"
)

// ScopeHeader selects the active owner scope.
const ScopeHeader = "X-Owner-Scope"

// Scope is one owner scope the caller may act in.
type Scope struct {
	OwnerID   uuid.UUID `json:"owner_id"`
	Role      Role      `json:"role"`
	LegalName string    `json:"legal_name"`
	// Kind is the owner's kind: KindPerson, or KindSCI for an SCI (Q-133).
	Kind string `json:"kind" enums:"person,sci"`
	// Manager is true when the caller holds the scope as an SCI's manager:
	// their role is then RoleOwner.
	Manager bool `json:"manager,omitempty"`
}

// Owner kinds (Q-133).
const (
	KindPerson = "person"
	KindSCI    = "sci"
)

// IsSCI reports whether the scope is an SCI's.
func (s *Scope) IsSCI() bool { return s != nil && s.Kind == KindSCI }

// Principal is the authenticated caller.
type Principal struct {
	Sub      string
	Admin    bool
	Language string // of the caller's own owner profile; "fr" by default
	Scopes   []Scope
	Active   *Scope // nil when the caller has no scope or chose none
	Claims   Claims
}

// DBScope returns the database scope of the caller's active scope (none when
// Active is nil), tagged with the request id.
func (p *Principal) DBScope(requestID string) db.Scope {
	s := db.Scope{ActorSub: p.Sub, RequestID: requestID, Role: "none"}
	if p.Active != nil {
		s.OwnerID, s.Role = p.Active.OwnerID, string(p.Active.Role)
	} else if p.Admin {
		s.Role = "admin"
	}
	return s
}

// PlatformScope returns the database scope of an admin acting outside any
// owner scope (the platform audit chain).
func (p *Principal) PlatformScope(requestID string) db.Scope {
	return db.Scope{ActorSub: p.Sub, RequestID: requestID, Role: "admin"}
}

// Claims are the token claims Tenure uses, read from backendkit's context
// values. It is the accessor every other package uses (Q-02).
type Claims struct {
	Sub  string
	Role string
	// AuthTime is when the user last authenticated interactively; the zero
	// time when the token does not say, which is never treated as fresh.
	AuthTime time.Time
	AMR      []string
}

// ClaimsFrom reads the validated claims from ctx.
func ClaimsFrom(ctx context.Context) Claims {
	c := Claims{Sub: ctxutil.GetUserSub(ctx), Role: ctxutil.GetUserRole(ctx), AMR: ctxutil.GetAMR(ctx)}
	if t := ctxutil.GetAuthTime(ctx); t > 0 {
		c.AuthTime = time.Unix(t, 0).UTC()
	}
	return c
}

type principalKey struct{}

// FromContext returns the principal set by Authenticate.
func FromContext(ctx context.Context) (*Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(*Principal)
	return p, ok
}

// WithPrincipal returns ctx carrying p (tests and jobs).
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// Resolver loads the scopes of a Socrate subject.
type Resolver interface {
	Resolve(ctx context.Context, sub string) (scopes []Scope, language string, err error)
}

// Options configures the middleware.
type Options struct {
	JWKSURL      string
	Issuer       string
	Audience     string
	AdminRole    string
	StepUpMaxAge time.Duration
	Logger       *logrus.Entry
	Now          func() time.Time
}

// Middleware authenticates requests and resolves the caller's scope.
type Middleware struct {
	jwt      *jwtauth.Middleware
	resolver Resolver
	opts     Options
}

// New builds the middleware. The audience is always checked: without it a
// token issued to another application of the same Socrate would be accepted.
func New(opts Options, resolver Resolver) (*Middleware, error) {
	if opts.JWKSURL == "" || opts.Issuer == "" || opts.Audience == "" || opts.AdminRole == "" {
		return nil, errors.New("auth: JWKS URL, issuer, audience and admin role are required")
	}
	if opts.StepUpMaxAge <= 0 {
		return nil, errors.New("auth: step-up max age must be positive")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Logger == nil {
		opts.Logger = logrus.NewEntry(logrus.StandardLogger())
	}
	write := func(w http.ResponseWriter, r *http.Request, e *apierror.AppError) {
		t := problem.Unauthenticated
		if e.StatusCode == http.StatusForbidden {
			t = problem.Forbidden
		}
		problem.Write(w, r, t, "", "", nil)
	}
	j := jwtauth.New(opts.JWKSURL, opts.Issuer, opts.Logger,
		jwtauth.WithAudience(opts.Audience), jwtauth.WithErrorWriter(write))
	return &Middleware{jwt: j, resolver: resolver, opts: opts}, nil
}

// Authenticate validates the token, then resolves the principal and its
// active scope. An X-Owner-Scope naming a scope the caller does not hold is
// 403.
func (m *Middleware) Authenticate(next http.Handler) http.Handler {
	return m.jwt.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		c := ClaimsFrom(ctx)
		if c.Sub == "" {
			problem.Write(w, r, problem.Unauthenticated, "", "", nil)
			return
		}
		scopes, lang, err := m.resolver.Resolve(ctx, c.Sub)
		if err != nil {
			m.opts.Logger.WithError(err).Error("auth: resolve scopes")
			problem.Write(w, r, problem.Unavailable, "", "", nil)
			return
		}
		p := &Principal{Sub: c.Sub, Admin: c.Role == m.opts.AdminRole, Language: problem.Language(lang), Scopes: scopes, Claims: c}
		if h := r.Header.Get(ScopeHeader); h != "" {
			id, err := uuid.Parse(h)
			if err != nil {
				problem.Write(w, r, problem.ScopeForbidden, p.Language, "", nil)
				return
			}
			i := slices.IndexFunc(scopes, func(s Scope) bool { return s.OwnerID == id })
			if i < 0 {
				problem.Write(w, r, problem.ScopeForbidden, p.Language, "", nil)
				return
			}
			p.Active = &scopes[i]
		} else if i := slices.IndexFunc(scopes, func(s Scope) bool { return s.Role == RoleOwner }); i >= 0 {
			p.Active = &scopes[i]
		}
		next.ServeHTTP(w, r.WithContext(WithPrincipal(ctx, p)))
	}))
}

func principal(w http.ResponseWriter, r *http.Request) (*Principal, bool) {
	p, ok := FromContext(r.Context())
	if !ok {
		problem.Write(w, r, problem.Unauthenticated, "", "", nil)
	}
	return p, ok
}

// RequireScope refuses a caller without an active owner scope.
func RequireScope(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := principal(w, r)
		if !ok {
			return
		}
		if p.Active == nil {
			problem.Write(w, r, problem.NoScope, p.Language, "", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireRoles refuses a caller whose active role is not one of roles.
func RequireRoles(roles ...Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return RequireScope(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, _ := FromContext(r.Context())
			if !slices.Contains(roles, p.Active.Role) {
				problem.Write(w, r, problem.Forbidden, p.Language, "", nil)
				return
			}
			next.ServeHTTP(w, r)
		}))
	}
}

// RequireOwnerForWrites lets reads through for any scope role and requires
// the owner role for every other method (co-owners and accountants are
// read-only, RENT-BE-001 §4).
func RequireOwnerForWrites(next http.Handler) http.Handler {
	owner := RequireRoles(RoleOwner)(next)
	scoped := RequireScope(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			scoped.ServeHTTP(w, r)
			return
		}
		owner.ServeHTTP(w, r)
	})
}

// RequireAdmin refuses a caller without the Socrate admin role.
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := principal(w, r)
		if !ok {
			return
		}
		if !p.Admin {
			problem.Write(w, r, problem.Forbidden, p.Language, "", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// StepUp guards a route tagged step_up (tax issue, grants, params, exports):
// auth_time at most StepUpMaxAge old and mfa in amr, otherwise 401
// auth/step-up-required. A token without auth_time is refused (Q-02).
func (m *Middleware) StepUp(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := principal(w, r)
		if !ok {
			return
		}
		if !Fresh(p.Claims, m.opts.Now(), m.opts.StepUpMaxAge) {
			reason := refusal(p.Claims, m.opts.Now(), m.opts.StepUpMaxAge)
			slog.InfoContext(r.Context(), "step-up refused", "sub", p.Sub, "reason", reason, "mfa", slices.Contains(p.Claims.AMR, "mfa"),
				"auth_age_s", authAge(p.Claims, m.opts.Now()))
			w.Header().Set("WWW-Authenticate", `Bearer error="insufficient_user_authentication", acr_values="mfa"`)
			if reason == "mfa_missing" {
				// A recent sign-in without a second factor: Socrate gives mfa
				// only to an account with MFA on, so signing in again would
				// not help (#262).
				problem.Write(w, r, problem.MFARequired, p.Language, "", nil)
				return
			}
			problem.Write(w, r, problem.StepUpRequired, p.Language, "", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// refusal names why c fails the step-up, for the log and the problem type:
// no auth_time, a sign-in older than maxAge (or in the future), or no mfa in
// amr on a recent sign-in.
func refusal(c Claims, now time.Time, maxAge time.Duration) string {
	if c.AuthTime.IsZero() {
		return "auth_time_missing"
	}
	if age := now.Sub(c.AuthTime); age < -time.Minute || age > maxAge {
		return "auth_time_stale"
	}
	return "mfa_missing"
}

// authAge is the age of c's sign-in in seconds, -1 when it is unknown.
func authAge(c Claims, now time.Time) int64 {
	if c.AuthTime.IsZero() {
		return -1
	}
	return int64(now.Sub(c.AuthTime).Seconds())
}

// Fresh reports whether c proves a strong authentication within maxAge of now.
func Fresh(c Claims, now time.Time, maxAge time.Duration) bool {
	if c.AuthTime.IsZero() || !slices.Contains(c.AMR, "mfa") {
		return false
	}
	age := now.Sub(c.AuthTime)
	return age >= -time.Minute && age <= maxAge // one minute of clock skew into the future
}
