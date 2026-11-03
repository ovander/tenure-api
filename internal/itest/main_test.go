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

	"github.com/minio/minio-go/v7"

	"github.com/ovander/tenure-api/internal/app"
	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/bff"
	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/document"
	"github.com/ovander/tenure-api/internal/migrate"
	"github.com/ovander/tenure-api/internal/notify"
	"github.com/ovander/tenure-api/internal/notify/smtptest"
	"github.com/ovander/tenure-api/internal/params"
)

// Illustrative IRL values served by the fake INSEE endpoint, not a reference.
const inseeSample = `<?xml version="1.0" encoding="UTF-8"?><message:StructureSpecificData xmlns:message="m"><message:DataSet>
<Series IDBANK="001515333"><Obs TIME_PERIOD="2025-Q4" OBS_VALUE="146.10"/><Obs TIME_PERIOD="2025-Q2" OBS_VALUE="146.68"/>
<Obs TIME_PERIOD="2024-Q4" OBS_VALUE="144.64"/></Series></message:DataSet></message:StructureSpecificData>`

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
	apiDB   *db.DB
	jobsDB  *db.DB
	server  *httptest.Server
	key     *rsa.PrivateKey
	baseDSN string
	app     *app.App
	deps    app.Deps
	s3      *minio.Client
	store   *document.S3
	smtp    *smtptest.Server
	insee   *httptest.Server
	bff     *bff.Server
}

const bucket = "tenure-itest"

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
	env.apiDB = api
	// /health is polled once a second, not testcontainers' 10 times a second: each poll
	// runs a Chromium check, and on a starved host that flood keeps every
	// check timing out after Chromium has started (Q-103).
	gotenberg, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{Started: true,
		ContainerRequest: testcontainers.ContainerRequest{Image: "gotenberg/gotenberg:8.21", ExposedPorts: []string{"3000/tcp"},
			Cmd:        document.GotenbergFlags,
			WaitingFor: wait.ForHTTP("/health").WithPort("3000/tcp").WithPollInterval(time.Second).WithStartupTimeout(120 * time.Second)}})
	if err != nil {
		return 0, fmt.Errorf("gotenberg: %w", err)
	}
	defer func() { _ = testcontainers.TerminateContainer(gotenberg) }()
	pdfURL, err := gotenberg.PortEndpoint(ctx, "3000/tcp", "http")
	if err != nil {
		return 0, err
	}
	// /health answers before Chromium and LibreOffice can render: wait for one
	// real PDF/A conversion before any test issues a document (Q-101).
	if err := warmPDF(ctx, pdfURL, 120*time.Second); err != nil {
		return 0, fmt.Errorf("gotenberg warm-up: %w", err)
	}
	rustfs, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{Started: true,
		ContainerRequest: testcontainers.ContainerRequest{Image: "rustfs/rustfs:1.0.3", ExposedPorts: []string{"9000/tcp"},
			Env:        map[string]string{"RUSTFS_ACCESS_KEY": "itest-access", "RUSTFS_SECRET_KEY": "itest-secret-key"},
			WaitingFor: wait.ForListeningPort("9000/tcp").WithStartupTimeout(90 * time.Second)}})
	if err != nil {
		return 0, fmt.Errorf("rustfs: %w", err)
	}
	defer func() { _ = testcontainers.TerminateContainer(rustfs) }()
	s3Host, err := rustfs.PortEndpoint(ctx, "9000/tcp", "")
	if err != nil {
		return 0, err
	}
	if env.store, err = document.NewS3(s3Host, "us-east-1", bucket, "itest-access", "itest-secret-key", false); err != nil {
		return 0, err
	}
	env.s3 = env.store.Client
	for i := 0; ; i++ {
		if err = env.s3.MakeBucket(ctx, bucket, minio.MakeBucketOptions{ObjectLocking: true}); err == nil || i == 20 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil {
		return 0, fmt.Errorf("bucket: %w", err)
	}
	// The antivirus sidecar starts on the signatures bundled in its image.
	clamav, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{Started: true,
		ContainerRequest: testcontainers.ContainerRequest{Image: "clamav/clamav:1.4.3", ExposedPorts: []string{"3310/tcp"},
			WaitingFor: wait.ForAll(wait.ForLog("socket found, clamd started"), wait.ForListeningPort("3310/tcp")).
				WithDeadline(5 * time.Minute)}})
	if err != nil {
		return 0, fmt.Errorf("clamav: %w", err)
	}
	defer func() { _ = testcontainers.TerminateContainer(clamav) }()
	clamAddr, err := clamav.PortEndpoint(ctx, "3310/tcp", "")
	if err != nil {
		return 0, err
	}
	if env.smtp, err = smtptest.Start(); err != nil {
		return 0, err
	}
	defer func() { _ = env.smtp.Close() }()
	env.insee = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(inseeSample))
	}))
	defer env.insee.Close()
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
	socrateFake := httptest.NewServer(fakeSocrate())
	defer socrateFake.Close()
	if env.bff, err = newBFF(ctx, socrateFake.URL, api); err != nil {
		return 0, fmt.Errorf("bff: %w", err)
	}
	env.deps = app.Deps{API: api, Jobs: env.jobsDB, Auth: authMW, Store: env.store, PDF: document.Gotenberg{URL: pdfURL}, BFF: env.bff,
		Scanner: document.Clamd{Addr: clamAddr}, WriteBurst: 10000, FailedAuthMax: 10000,
		Mailer: notify.SMTP{Addr: env.smtp.Addr, From: "no-reply@tenure.itest"}, INSEE: params.INSEE{URL: env.insee.URL},
		Ready: func(ctx context.Context) error { return api.Ping(ctx) }}
	if env.app, err = app.New(env.deps); err != nil {
		return 0, err
	}
	env.server = httptest.NewServer(env.app.Handler)
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

// warmPDF converts a page until the engine renders one, or until timeout.
func warmPDF(ctx context.Context, url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		_, err := document.Gotenberg{URL: url}.Convert(ctx, []byte("<!doctype html><p>warm-up</p>"), []byte("<p></p>"))
		if err == nil || time.Now().After(deadline) {
			return err
		}
		time.Sleep(time.Second)
	}
}
