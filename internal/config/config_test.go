package config

import (
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func valid() map[string]string {
	return map[string]string{
		"TENURE_DB_URL":                "postgres://tenure_api:x@db.internal:5432/tenure?sslmode=verify-full",
		"TENURE_JOBS_DB_URL":           "postgres://tenure_jobs:x@localhost:5432/tenure",
		"TENURE_SOCRATE_ISSUER":        "https://socrate.example.test",
		"TENURE_SOCRATE_JWKS_URL":      "https://socrate.example.test/.well-known/jwks.json",
		"TENURE_SOCRATE_AUDIENCE":      "tenure-api",
		"TENURE_SOCRATE_ADMIN_ROLE":    "admin",
		"TENURE_CLAMAV_ADDR":           "clamav.internal:3310",
		"TENURE_SOCRATE_CLIENT_ID":     "tenure-web",
		"TENURE_SOCRATE_CLIENT_SECRET": "placeholder-client-secret",
		"TENURE_PUBLIC_URL":            "https://tenure.example.test",
		// 32 zero bytes: an obviously fake key.
		"TENURE_SESSION_KEY": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
	}
}

func withDocs(m map[string]string) map[string]string {
	for k, v := range map[string]string{
		"TENURE_S3_ENDPOINT": "s3.fr-par.scw.cloud", "TENURE_S3_REGION": "fr-par", "TENURE_S3_BUCKET": "tenure-archive",
		"TENURE_S3_ACCESS_KEY": "placeholder-access", "TENURE_S3_SECRET_KEY": "placeholder-secret",
		"TENURE_SMTP_ADDR": "smtp.example.test:587", "TENURE_SMTP_FROM": "no-reply@example.test",
		"TENURE_PDF_ENGINE_URL": "http://127.0.0.1:3000",
	} {
		m[k] = v
	}
	return m
}

func TestLoadDocuments(t *testing.T) {
	c, err := Load(env(withDocs(valid())), NeedAPI|NeedDocuments)
	if err != nil {
		t.Fatal(err)
	}
	if !c.S3.Secure || c.IRLSourceURL != DefaultIRLSource || c.PDFEngineURL == "" {
		t.Errorf("%+v", c)
	}
	bad := map[string][2]string{
		"insecure remote store":  {"TENURE_S3_INSECURE", "true"},
		"scheme in endpoint":     {"TENURE_S3_ENDPOINT", "https://s3.example.test"},
		"remote http pdf":        {"TENURE_PDF_ENGINE_URL", "http://gotenberg.internal:3000"},
		"smtp without port":      {"TENURE_SMTP_ADDR", "smtp.example.test"},
		"user without password":  {"TENURE_SMTP_USERNAME", "u"},
		"missing secret":         {"TENURE_S3_SECRET_KEY", ""},
		"missing antivirus":      {"TENURE_CLAMAV_ADDR", ""},
		"antivirus without port": {"TENURE_CLAMAV_ADDR", "clamav.internal"},
		"antivirus with scheme":  {"TENURE_CLAMAV_ADDR", "tcp://clamav.internal:3310"},
		"missing client secret":  {"TENURE_SOCRATE_CLIENT_SECRET", ""},
		"public url over http":   {"TENURE_PUBLIC_URL", "http://tenure.example.test"},
		"public url with a path": {"TENURE_PUBLIC_URL", "https://tenure.example.test/app"},
		"short session key":      {"TENURE_SESSION_KEY", "c2hvcnQ="},
		"missing session key":    {"TENURE_SESSION_KEY", ""},
	}
	for name, kv := range bad {
		m := withDocs(valid())
		m[kv[0]] = kv[1]
		if _, err := Load(env(m), NeedAPI|NeedDocuments); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	m := withDocs(valid())
	m["TENURE_S3_ENDPOINT"], m["TENURE_S3_INSECURE"] = "localhost:9000", "true"
	if c, err := Load(env(m), NeedAPI|NeedDocuments); err != nil || c.S3.Secure {
		t.Errorf("loopback insecure store: %v %v", c.S3.Secure, err)
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
