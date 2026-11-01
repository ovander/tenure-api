// Package config reads the TENURE_* environment variables and validates them
// at start (RENT-BE-001 §9). A missing or malformed value stops the process;
// there is no default for a secret or for the Socrate issuer, audience, JWKS
// URL or admin role (OPEN-QUESTIONS Q-01).
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strings"
	"time"
)

// Need says which groups of settings a subcommand requires.
type Need uint8

// Setting groups.
const (
	NeedAPI       Need = 1 << iota // HTTP listener, API database role, Socrate
	NeedJobs                       // jobs database role
	NeedMigrate                    // migration database role
	NeedDocuments                  // object store, PDF engine, SMTP (serve and job)
)

// DefaultIRLSource is the INSEE BDM SDMX series of the IRL (idBank 001515333).
const DefaultIRLSource = "https://bdm.insee.fr/series/sdmx/data/SERIES_BDM/001515333"

// MaxStepUpAge is the upper bound of TENURE_STEP_UP_MAX_AGE (RENT-BE-001 §8b).
const MaxStepUpAge = 5 * time.Minute

// Socrate holds the identity-provider settings.
type Socrate struct {
	Issuer    string
	JWKSURL   string
	Audience  string
	AdminRole string
}

// S3 holds the object-store settings (Scaleway in production, OD-04).
type S3 struct {
	Endpoint  string // host[:port]
	Region    string
	Bucket    string
	AccessKey string
	SecretKey string
	// Secure is false only for a loopback endpoint (local development).
	Secure bool
}

// SMTP holds the mail settings. TLS (STARTTLS) is required except on a
// loopback host.
type SMTP struct {
	Addr     string // host:port
	Username string
	Password string
	From     string
}

// Config is the validated configuration.
type Config struct {
	HTTPAddr     string
	DBURL        string // tenure_api
	JobsDBURL    string // tenure_jobs
	MigrateDBURL string // tenure_migrate
	Socrate      Socrate
	StepUpMaxAge time.Duration
	LogLevel     slog.Level
	S3           S3
	SMTP         SMTP
	PDFEngineURL string // gotenberg
	// IRLSourceURL is the INSEE BDM series of the IRL (irl.fetch).
	IRLSourceURL string
	// TrustedProxies are the reverse proxies (Caddy) whose X-Forwarded-For is
	// believed when finding a client's address; empty trusts none.
	TrustedProxies []*net.IPNet
}

// Load reads the configuration through getenv and validates the groups in
// need, reporting every problem at once.
func Load(getenv func(string) string, need Need) (Config, error) {
	var errs []error
	get := func(key string, required bool) string {
		v := strings.TrimSpace(getenv(key))
		if v == "" && required {
			errs = append(errs, fmt.Errorf("%s is required", key))
		}
		return v
	}
	c := Config{
		HTTPAddr:     get("TENURE_HTTP_ADDR", false),
		DBURL:        get("TENURE_DB_URL", need&NeedAPI != 0),
		JobsDBURL:    get("TENURE_JOBS_DB_URL", need&(NeedAPI|NeedJobs) != 0),
		MigrateDBURL: get("TENURE_MIGRATE_DB_URL", need&NeedMigrate != 0),
		Socrate: Socrate{
			Issuer:    get("TENURE_SOCRATE_ISSUER", need&NeedAPI != 0),
			JWKSURL:   get("TENURE_SOCRATE_JWKS_URL", need&NeedAPI != 0),
			Audience:  get("TENURE_SOCRATE_AUDIENCE", need&NeedAPI != 0),
			AdminRole: get("TENURE_SOCRATE_ADMIN_ROLE", need&NeedAPI != 0),
		},
		StepUpMaxAge: MaxStepUpAge,
	}
	if c.HTTPAddr == "" {
		c.HTTPAddr = ":8080"
	}
	docs := need&NeedDocuments != 0
	c.S3 = S3{Endpoint: get("TENURE_S3_ENDPOINT", docs), Region: get("TENURE_S3_REGION", docs), Bucket: get("TENURE_S3_BUCKET", docs),
		AccessKey: get("TENURE_S3_ACCESS_KEY", docs), SecretKey: get("TENURE_S3_SECRET_KEY", docs), Secure: true}
	if c.S3.Endpoint != "" {
		host, _, err := net.SplitHostPort(c.S3.Endpoint)
		if err != nil {
			host = c.S3.Endpoint
		}
		if strings.Contains(c.S3.Endpoint, "://") {
			errs = append(errs, errors.New("TENURE_S3_ENDPOINT is host[:port], without scheme"))
		}
		switch v := get("TENURE_S3_INSECURE", false); v {
		case "", "false":
		case "true":
			if !isLocal(host) {
				errs = append(errs, errors.New("TENURE_S3_INSECURE is only allowed for a loopback endpoint"))
			}
			c.S3.Secure = false
		default:
			errs = append(errs, errors.New("TENURE_S3_INSECURE must be true or false"))
		}
	}
	c.SMTP = SMTP{Addr: get("TENURE_SMTP_ADDR", docs), Username: get("TENURE_SMTP_USERNAME", false),
		Password: get("TENURE_SMTP_PASSWORD", false), From: get("TENURE_SMTP_FROM", docs)}
	if c.SMTP.Addr != "" {
		if _, _, err := net.SplitHostPort(c.SMTP.Addr); err != nil {
			errs = append(errs, errors.New("TENURE_SMTP_ADDR must be host:port"))
		}
	}
	if (c.SMTP.Username == "") != (c.SMTP.Password == "") {
		errs = append(errs, errors.New("TENURE_SMTP_USERNAME and TENURE_SMTP_PASSWORD go together"))
	}
	if c.SMTP.From != "" && !strings.Contains(c.SMTP.From, "@") {
		errs = append(errs, errors.New("TENURE_SMTP_FROM must be an e-mail address"))
	}
	c.PDFEngineURL = get("TENURE_PDF_ENGINE_URL", docs)
	c.IRLSourceURL = get("TENURE_IRL_SOURCE_URL", false)
	if c.IRLSourceURL == "" {
		c.IRLSourceURL = DefaultIRLSource
	}
	for key, v := range map[string]string{"TENURE_PDF_ENGINE_URL": c.PDFEngineURL, "TENURE_IRL_SOURCE_URL": c.IRLSourceURL} {
		if v != "" {
			if err := checkHTTPS(v); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", key, err))
			}
		}
	}
	for key, v := range map[string]string{"TENURE_DB_URL": c.DBURL, "TENURE_JOBS_DB_URL": c.JobsDBURL, "TENURE_MIGRATE_DB_URL": c.MigrateDBURL} {
		if v != "" {
			if err := checkDBURL(v); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", key, err))
			}
		}
	}
	for key, v := range map[string]string{"TENURE_SOCRATE_ISSUER": c.Socrate.Issuer, "TENURE_SOCRATE_JWKS_URL": c.Socrate.JWKSURL} {
		if v != "" {
			if err := checkHTTPS(v); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", key, err))
			}
		}
	}
	if v := get("TENURE_STEP_UP_MAX_AGE", false); v != "" {
		d, err := time.ParseDuration(v)
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("TENURE_STEP_UP_MAX_AGE: %w", err))
		case d <= 0 || d > MaxStepUpAge:
			errs = append(errs, fmt.Errorf("TENURE_STEP_UP_MAX_AGE must be in (0, %s]", MaxStepUpAge))
		default:
			c.StepUpMaxAge = d
		}
	}
	if v := get("TENURE_TRUSTED_PROXIES", false); v != "" {
		for _, part := range strings.Split(v, ",") {
			part = strings.TrimSpace(part)
			if !strings.Contains(part, "/") {
				if ip := net.ParseIP(part); ip != nil && ip.To4() != nil {
					part += "/32"
				} else {
					part += "/128"
				}
			}
			_, n, err := net.ParseCIDR(part)
			if err != nil {
				errs = append(errs, fmt.Errorf("TENURE_TRUSTED_PROXIES: %q is not an address or CIDR", part))
				continue
			}
			c.TrustedProxies = append(c.TrustedProxies, n)
		}
	}
	if v := get("TENURE_LOG_LEVEL", false); v != "" {
		if err := c.LogLevel.UnmarshalText([]byte(v)); err != nil {
			errs = append(errs, fmt.Errorf("TENURE_LOG_LEVEL: %w", err))
		}
	}
	return c, errors.Join(errs...)
}

func isLocal(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// checkHTTPS requires https, except for a loopback host (local development).
func checkHTTPS(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return errors.New("not an absolute URL")
	}
	if u.Scheme != "https" && (u.Scheme != "http" || !isLocal(u.Hostname())) {
		return errors.New("must use https")
	}
	return nil
}

// checkDBURL requires a postgres URL with TLS (sslmode require, verify-ca or
// verify-full), except for a loopback host (RENT-BE-001 §8b).
func checkDBURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" {
		return errors.New("must be a postgres:// URL")
	}
	switch u.Query().Get("sslmode") {
	case "require", "verify-ca", "verify-full":
		return nil
	}
	if isLocal(u.Hostname()) {
		return nil
	}
	return errors.New("must set sslmode=require, verify-ca or verify-full")
}
