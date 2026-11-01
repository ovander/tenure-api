//go:build integration

// Package itest is the integration suite (RENT-BE-001 §10): PostgreSQL 17 in a
// container, the real migrations and roles, the real router, and Socrate
// replaced by a local JWKS signing test tokens. Run with
//
//	go test -tags integration ./internal/itest
package itest

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	tcpg "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/ovander/tenure-api/internal/audit"
	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/httpapi"
	"github.com/ovander/tenure-api/internal/jobs"
	"github.com/ovander/tenure-api/internal/lease"
	"github.com/ovander/tenure-api/internal/migrate"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/portfolio"
)

const (
	issuer   = "https://socrate.itest"
	audience = "tenure-api"
	// Test-only passwords of a throwaway container.
	superPW = "itest-super"
	rolePW  = "itest-role"
)

var env struct {
	super   *sql.DB // superuser: assertions and fixtures only
	apiURL  string  // tenure_api DSN
	jobsDB  *db.DB
	server  *httptest.Server
	key     *rsa.PrivateKey
	baseDSN string
}

func dsn(base, user, pw string) string {
	u, _ := url.Parse(base)
	u.User = url.UserPassword(user, pw)
	return u.String()
}

func TestMain(m *testing.M) {
	code, err := setup(m)
	if err != nil {
		fmt.Fprintln(os.Stderr, "itest setup:", err)
		os.Exit(1)
	}
	os.Exit(code)
}

func setup(m *testing.M) (int, error) {
	ctx := context.Background()
	c, err := tcpg.Run(ctx, "postgres:17", tcpg.WithDatabase("tenure"), tcpg.WithUsername("postgres"), tcpg.WithPassword(superPW),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(90*time.Second)))
	if err != nil {
		return 0, err
	}
	defer func() { _ = testcontainers.TerminateContainer(c) }()
	base, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return 0, err
	}
	env.baseDSN = base
	if env.super, err = sql.Open("pgx", base); err != nil {
		return 0, err
	}
	roles, err := os.ReadFile("../../deploy/sql/roles.sql")
	if err != nil {
		return 0, err
	}
	script := strings.NewReplacer(`\set ON_ERROR_STOP on`, "", `:'migrate_pw'`, "'"+rolePW+"'", `:'api_pw'`, "'"+rolePW+"'",
		`:'jobs_pw'`, "'"+rolePW+"'", `:"db"`, "tenure").Replace(string(roles))
	if _, err := env.super.ExecContext(ctx, script); err != nil {
		return 0, fmt.Errorf("roles: %w", err)
	}
	mig, err := migrate.NewMigrator(dsn(base, "tenure_migrate", rolePW))
	if err != nil {
		return 0, err
	}
	// Up, down, up: the migration is reversible (RENT-BE-001 §9).
	for _, step := range []func(context.Context, io.Writer) error{mig.Up, mig.Down, mig.Up} {
		if err := step(ctx, io.Discard); err != nil {
			return 0, fmt.Errorf("migrate: %w", err)
		}
	}
	_ = mig.Close()

	env.apiURL = dsn(base, "tenure_api", rolePW)
	api, err := db.Open(ctx, env.apiURL)
	if err != nil {
		return 0, err
	}
	if env.jobsDB, err = db.Open(ctx, dsn(base, "tenure_jobs", rolePW)); err != nil {
		return 0, err
	}
	if env.key, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
		return 0, err
	}
	jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		pub := env.key.PublicKey
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": "itest", "alg": "RS256", "use": "sig",
			"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
		}}})
	}))
	defer jwks.Close()
	authMW, err := auth.New(auth.Options{JWKSURL: jwks.URL, Issuer: issuer, Audience: audience, AdminRole: "admin",
		StepUpMaxAge: 5 * time.Minute}, auth.Repository{DB: api})
	if err != nil {
		return 0, err
	}
	pf := portfolio.NewService(api)
	h, err := httpapi.NewRouter(httpapi.Deps{
		Auth: authMW, Portfolio: portfolio.Handler{Service: pf}, Lease: lease.Handler{Service: lease.NewService(api, pf)},
		Params: params.Handler{Service: params.Service{DB: api, Now: time.Now}}, Audit: audit.Handler{Service: audit.Service{DB: api}},
		Jobs: jobs.Handler{DB: api}, Ready: func(ctx context.Context) error { return api.Ping(ctx) },
	})
	if err != nil {
		return 0, err
	}
	env.server = httptest.NewServer(h)
	defer env.server.Close()
	return m.Run(), nil
}

// --- tokens and calls -------------------------------------------------------------

type claim func(jwt.MapClaims)

func withRole(r string) claim { return func(c jwt.MapClaims) { c["role"] = r } }
func withAud(a string) claim  { return func(c jwt.MapClaims) { c["aud"] = a } }
func withIss(i string) claim  { return func(c jwt.MapClaims) { c["iss"] = i } }
func expired() claim          { return func(c jwt.MapClaims) { c["exp"] = time.Now().Add(-time.Hour).Unix() } }
func stepUp(age time.Duration) claim {
	return func(c jwt.MapClaims) {
		c["auth_time"] = time.Now().Add(-age).Unix()
		c["amr"] = []string{"pwd", "otp", "mfa"}
	}
}

func token(t *testing.T, sub string, opts ...claim) string {
	t.Helper()
	c := jwt.MapClaims{"iss": issuer, "aud": audience, "sub": sub, "iat": time.Now().Unix(),
		"exp": time.Now().Add(10 * time.Minute).Unix(), "role": "user"}
	for _, o := range opts {
		o(c)
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, c)
	tok.Header["kid"] = "itest"
	s, err := tok.SignedString(env.key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

type resp struct {
	Status int
	Header http.Header
	Body   []byte
}

func (r resp) json(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.Body, v); err != nil {
		t.Fatalf("decode %s: %v", r.Body, err)
	}
}

func (r resp) problemType(t *testing.T) string {
	var p struct{ Type string }
	_ = json.Unmarshal(r.Body, &p)
	return p.Type
}

type req struct {
	tok, scope, ifMatch string
	body                any
}

func call(t *testing.T, method, path string, q req) resp {
	t.Helper()
	var body io.Reader
	if q.body != nil {
		b, _ := json.Marshal(q.body)
		body = bytes.NewReader(b)
	}
	r, _ := http.NewRequest(method, env.server.URL+"/api/v1"+path, body)
	if q.body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if q.tok != "" {
		r.Header.Set("Authorization", "Bearer "+q.tok)
	}
	if q.scope != "" {
		r.Header.Set(auth.ScopeHeader, q.scope)
	}
	if q.ifMatch != "" {
		r.Header.Set("If-Match", q.ifMatch)
	}
	res, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	return resp{Status: res.StatusCode, Header: res.Header, Body: b}
}

func superCount(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := env.super.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func superExec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := env.super.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}
