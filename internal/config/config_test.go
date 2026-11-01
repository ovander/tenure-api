package config

import (
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func valid() map[string]string {
	return map[string]string{
		"TENURE_DB_URL":             "postgres://tenure_api:x@db.internal:5432/tenure?sslmode=verify-full",
		"TENURE_JOBS_DB_URL":        "postgres://tenure_jobs:x@localhost:5432/tenure",
		"TENURE_SOCRATE_ISSUER":     "https://socrate.example.test",
		"TENURE_SOCRATE_JWKS_URL":   "https://socrate.example.test/.well-known/jwks.json",
		"TENURE_SOCRATE_AUDIENCE":   "tenure-api",
		"TENURE_SOCRATE_ADMIN_ROLE": "admin",
	}
}

func TestLoadValid(t *testing.T) {
	c, err := Load(env(valid()), NeedAPI)
	if err != nil {
		t.Fatal(err)
	}
	m := valid()
	m["TENURE_TRUSTED_PROXIES"] = "127.0.0.1, 10.0.0.0/8"
	c2, err := Load(env(m), NeedAPI)
	if err != nil || len(c2.TrustedProxies) != 2 {
		t.Errorf("trusted proxies: %v %v", c2.TrustedProxies, err)
	}
	if c.HTTPAddr != ":8080" || c.StepUpMaxAge != 5*time.Minute || c.Socrate.Audience != "tenure-api" {
		t.Errorf("%+v", c)
	}
}

func TestLoadRefuses(t *testing.T) {
	cases := map[string]struct {
		key, value, want string
	}{
		"missing issuer":      {"TENURE_SOCRATE_ISSUER", "", "TENURE_SOCRATE_ISSUER is required"},
		"missing audience":    {"TENURE_SOCRATE_AUDIENCE", "", "TENURE_SOCRATE_AUDIENCE is required"},
		"missing admin role":  {"TENURE_SOCRATE_ADMIN_ROLE", "", "TENURE_SOCRATE_ADMIN_ROLE is required"},
		"http issuer":         {"TENURE_SOCRATE_ISSUER", "http://socrate.example.test", "must use https"},
		"remote db no tls":    {"TENURE_DB_URL", "postgres://u:p@db.internal/tenure", "sslmode"},
		"not postgres":        {"TENURE_DB_URL", "mysql://u:p@db/tenure", "postgres://"},
		"step-up above 5 min": {"TENURE_STEP_UP_MAX_AGE", "6m", "TENURE_STEP_UP_MAX_AGE"},
		"bad log level":       {"TENURE_LOG_LEVEL", "loud", "TENURE_LOG_LEVEL"},
		"bad proxy":           {"TENURE_TRUSTED_PROXIES", "127.0.0.1, nope", "TENURE_TRUSTED_PROXIES"},
	}
	for name, c := range cases {
		m := valid()
		m[c.key] = c.value
		if _, err := Load(env(m), NeedAPI); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v; want %q", name, err, c.want)
		}
	}
	if _, err := Load(env(map[string]string{}), NeedMigrate); err == nil || !strings.Contains(err.Error(), "TENURE_MIGRATE_DB_URL") {
		t.Errorf("migrate without URL: %v", err)
	}
	if _, err := Load(env(map[string]string{"TENURE_MIGRATE_DB_URL": "postgres://m:p@localhost/tenure"}), NeedMigrate); err != nil {
		t.Errorf("migrate needs only its URL: %v", err)
	}
	m := valid()
	m["TENURE_SOCRATE_ISSUER"] = "http://127.0.0.1:9000"
	if _, err := Load(env(m), NeedAPI); err != nil {
		t.Errorf("loopback http issuer refused: %v", err)
	}
}
