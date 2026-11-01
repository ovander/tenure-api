// Command tenure is the Tenure backend (RENT-BE-001 §9): one binary that
// serves the API and runs the scheduler (serve), applies the migrations
// (migrate) and runs a job by hand (job). Configuration comes only from
// TENURE_* environment variables, validated at start. Build with
//
//	go build -ldflags "-X github.com/ovander/backendkit/buildinfo.Version=$VERSION \
//	  -X github.com/ovander/backendkit/buildinfo.GitCommit=$COMMIT \
//	  -X github.com/ovander/backendkit/buildinfo.BuildTime=$DATE" ./cmd/tenure
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	// Embedded zone data, so Europe/Paris resolves on any host.
	_ "time/tzdata"

	"github.com/google/uuid"
	"github.com/ovander/backendkit/buildinfo"
	"github.com/sirupsen/logrus"

	"github.com/ovander/tenure-api/internal/audit"
	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/config"
	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/httpapi"
	"github.com/ovander/tenure-api/internal/jobs"
	"github.com/ovander/tenure-api/internal/lease"
	"github.com/ovander/tenure-api/internal/migrate"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/portfolio"
)

const usage = `usage:
  tenure serve                         serve the API and run the scheduler
  tenure migrate up|down|status        apply, revert one, or list migrations
  tenure job <name> [-owner id] [-dry-run]   run a job now
  tenure version                       print the build version`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Getenv, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "tenure:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, getenv func(string) string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	switch args[0] {
	case "version":
		_, err := fmt.Fprintf(out, "%s %s %s\n", buildinfo.Version, buildinfo.GitCommit, buildinfo.BuildTime)
		return err
	case "migrate":
		return migrateCmd(ctx, args[1:], getenv, out)
	case "job":
		return jobCmd(ctx, args[1:], getenv, out)
	case "serve":
		return serve(ctx, getenv)
	}
	return errors.New(usage)
}

func logger(level slog.Level) *logrus.Entry {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})))
	l := logrus.New()
	l.SetFormatter(&logrus.JSONFormatter{FieldMap: logrus.FieldMap{logrus.FieldKeyMsg: "msg", logrus.FieldKeyTime: "time"}})
	l.SetOutput(os.Stdout)
	return logrus.NewEntry(l).WithField("component", "backendkit")
}

func migrateCmd(ctx context.Context, args []string, getenv func(string) string, out io.Writer) error {
	cfg, err := config.Load(getenv, config.NeedMigrate)
	if err != nil {
		return err
	}
	if len(args) != 1 {
		return errors.New(usage)
	}
	m, err := migrate.NewMigrator(cfg.MigrateDBURL)
	if err != nil {
		return err
	}
	defer func() { _ = m.Close() }()
	switch args[0] {
	case "up":
		return m.Up(ctx, out)
	case "down":
		return m.Down(ctx, out)
	case "status":
		return m.Status(ctx, out)
	}
	return errors.New(usage)
}

func jobCmd(ctx context.Context, args []string, getenv func(string) string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	name := args[0]
	fs := flag.NewFlagSet("job", flag.ContinueOnError)
	ownerFlag := fs.String("owner", "", "owner id")
	dry := fs.Bool("dry-run", false, "compute without recording")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	cfg, err := config.Load(getenv, config.NeedJobs)
	if err != nil {
		return err
	}
	logger(cfg.LogLevel)
	var owner *uuid.UUID
	if *ownerFlag != "" {
		id, err := uuid.Parse(*ownerFlag)
		if err != nil {
			return fmt.Errorf("-owner: %w", err)
		}
		owner = &id
	}
	jdb, err := db.Open(ctx, cfg.JobsDBURL)
	if err != nil {
		return err
	}
	defer func() { _ = jdb.Close() }()
	r := &jobs.Runner{DB: jdb, Jobs: jobs.Registry(), Now: time.Now}
	runRes, ran, err := r.Trigger(ctx, name, owner, *dry)
	if err != nil {
		return err
	}
	if !ran {
		_, _ = fmt.Fprintln(out, "already ran for this period; nothing done")
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(runRes); err != nil {
		return err
	}
	if runRes.Status != jobs.StatusSucceeded && runRes.Status != jobs.StatusRunning {
		return fmt.Errorf("job %s ended %s", name, runRes.Status)
	}
	return nil
}

func serve(ctx context.Context, getenv func(string) string) error {
	cfg, err := config.Load(getenv, config.NeedAPI)
	if err != nil {
		return err
	}
	log := logger(cfg.LogLevel)
	api, err := db.Open(ctx, cfg.DBURL)
	if err != nil {
		return err
	}
	defer func() { _ = api.Close() }()
	jdb, err := db.Open(ctx, cfg.JobsDBURL)
	if err != nil {
		return err
	}
	defer func() { _ = jdb.Close() }()

	authMW, err := auth.New(auth.Options{JWKSURL: cfg.Socrate.JWKSURL, Issuer: cfg.Socrate.Issuer, Audience: cfg.Socrate.Audience,
		AdminRole: cfg.Socrate.AdminRole, StepUpMaxAge: cfg.StepUpMaxAge, Logger: log}, auth.Repository{DB: api})
	if err != nil {
		return err
	}
	pf := portfolio.NewService(api)
	h, err := httpapi.NewRouter(httpapi.Deps{
		Auth:           authMW,
		Portfolio:      portfolio.Handler{Service: pf},
		Lease:          lease.Handler{Service: lease.NewService(api, pf)},
		Params:         params.Handler{Service: params.Service{DB: api, Now: time.Now}},
		Audit:          audit.Handler{Service: audit.Service{DB: api}},
		Jobs:           jobs.Handler{DB: api},
		Ready:          readiness(api, cfg.Socrate.JWKSURL),
		TrustedProxies: cfg.TrustedProxies,
	})
	if err != nil {
		return err
	}
	srv := &http.Server{Addr: cfg.HTTPAddr, Handler: h, ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 120 * time.Second}

	runner := &jobs.Runner{DB: jdb, Jobs: jobs.Registry(), Now: time.Now}
	errc := make(chan error, 2)
	go func() { errc <- runner.Start(ctx) }()
	go func() {
		slog.Info("listening", "addr", cfg.HTTPAddr, "version", buildinfo.Version)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()
	select {
	case <-ctx.Done():
	case err := <-errc:
		if err != nil {
			return err
		}
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}

// readiness checks the database and that the Socrate JWKS answers
// (RENT-BE-001 §8; the bucket check arrives with the archive in slice 3).
func readiness(d *db.DB, jwksURL string) func(context.Context) error {
	client := &http.Client{Timeout: 2 * time.Second}
	return func(ctx context.Context) error {
		if err := d.Ping(ctx); err != nil {
			return fmt.Errorf("database: %w", err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, jwksURL, nil)
		if err != nil {
			return err
		}
		res, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("jwks: %w", err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return fmt.Errorf("jwks: status %d", res.StatusCode)
		}
		return nil
	}
}
