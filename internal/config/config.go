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
	NeedAPI     Need = 1 << iota // HTTP listener, API database role, Socrate
	NeedJobs                     // jobs database role
	NeedMigrate                  // migration database role
)

// MaxStepUpAge is the upper bound of TENURE_STEP_UP_MAX_AGE (RENT-BE-001 §8b).
const MaxStepUpAge = 5 * time.Minute

// Socrate holds the identity-provider settings.
type Socrate struct {
	Issuer    string
	JWKSURL   string
	Audience  string
	AdminRole string
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
