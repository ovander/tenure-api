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
	bffkit "github.com/ovander/backendkit/bff"
	"github.com/ovander/backendkit/buildinfo"
	"github.com/ovander/backendkit/socrate"
	"github.com/sirupsen/logrus"

	"github.com/ovander/tenure-api/internal/app"
	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/bff"
	"github.com/ovander/tenure-api/internal/config"
	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/document"
	"github.com/ovander/tenure-api/internal/jobs"
	"github.com/ovander/tenure-api/internal/migrate"
	"github.com/ovander/tenure-api/internal/notify"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/web"
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
	cfg, err := config.Load(getenv, config.NeedJobs|config.NeedDocuments)
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
	d, _, _, err := deps(cfg, jdb, jdb)
	if err != nil {
		return err
	}
	a, err := app.New(d)
	if err != nil {
		return err
	}
	runRes, ran, err := a.Runner.Trigger(ctx, name, jobs.Options{Owner: owner, DryRun: *dry})
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
	cfg, err := config.Load(getenv, config.NeedAPI|config.NeedDocuments)
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
	d, store, pdf, err := deps(cfg, api, jdb)
	if err != nil {
		return err
	}
	d.Scanner = document.Clamd{Addr: cfg.ClamAVAddr}
	d.Auth, d.Ready = authMW, readiness(api, cfg.Socrate.JWKSURL, store, pdf, d.Scanner)
	if d.BFF, err = browserSessions(ctx, cfg, api); err != nil {
		return err
	}
	go d.BFF.Sweep(ctx)
	d.Web = web.FS()
	if tag, _, err := web.Pinned(); err != nil {
		slog.Warn("web bundle", "error", err)
	} else {
		slog.Info("web bundle", "release", tag)
	}
	a, err := app.New(d)
	if err != nil {
		return err
	}
	h, runner := a.Handler, a.Runner
	srv := &http.Server{Addr: cfg.HTTPAddr, Handler: h, ReadHeaderTimeout: 10 * time.Second,
		// A 25 MB upload is read, scanned and archived within one request.
		ReadTimeout: 2 * time.Minute, WriteTimeout: 3 * time.Minute, IdleTimeout: 120 * time.Second}

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

// browserSessions builds the BFF (RENT-BE-001 §4): tenure as Socrate's
// confidential client, sessions and pending logins encrypted in PostgreSQL
// (tables of migration 000006, so the stores run no DDL).
func browserSessions(ctx context.Context, cfg config.Config, api *db.DB) (*bff.Server, error) {
	client, err := socrate.NewClient(socrate.ClientConfig{BaseURL: cfg.Socrate.Issuer, ClientID: cfg.Socrate.ClientID,
		ClientSecret: cfg.Socrate.ClientSecret, Timeout: 10 * time.Second})
	if err != nil {
		return nil, err
	}
	onErr := func(op string, err error) { slog.ErrorContext(ctx, "bff store", "op", op, "error", err) }
	sessions, err := bffkit.NewPostgresStore(ctx, api.SQL(), cfg.SessionKey, bff.IdleTimeout, bff.AbsoluteTimeout,
		bffkit.WithPostgresManagedSchema(), bffkit.WithPostgresErrorHandler(onErr))
	if err != nil {
		return nil, fmt.Errorf("bff sessions: %w", err)
	}
	pending, err := bffkit.NewPostgresPendingLoginStore(ctx, api.SQL(), cfg.SessionKey, bffkit.DefaultPendingLoginTTL,
		bffkit.WithPendingLoginManagedSchema(), bffkit.WithPendingLoginErrorHandler(onErr))
	if err != nil {
		return nil, fmt.Errorf("bff pending logins: %w", err)
	}
	return bff.New(bff.Config{Issuer: cfg.Socrate.Issuer, ClientID: cfg.Socrate.ClientID, PublicURL: cfg.PublicURL},
		client, sessions, pending, nil), nil
}

func deps(cfg config.Config, api, jdb *db.DB) (app.Deps, *document.S3, document.Gotenberg, error) {
	store, err := document.NewS3(cfg.S3.Endpoint, cfg.S3.Region, cfg.S3.Bucket, cfg.S3.AccessKey, cfg.S3.SecretKey, cfg.S3.Secure)
	if err != nil {
		return app.Deps{}, nil, document.Gotenberg{}, err
	}
	pdf := document.Gotenberg{URL: cfg.PDFEngineURL}
	return app.Deps{API: api, Jobs: jdb, Store: store, PDF: pdf, INSEE: params.INSEE{URL: cfg.IRLSourceURL},
		Mailer:         notify.SMTP{Addr: cfg.SMTP.Addr, Username: cfg.SMTP.Username, Password: cfg.SMTP.Password, From: cfg.SMTP.From},
		TrustedProxies: cfg.TrustedProxies}, store, pdf, nil
}

// readiness checks the database, the Socrate JWKS, the bucket's object lock,
// the PDF engine and the antivirus (RENT-BE-001 §8).
func readiness(d *db.DB, jwksURL string, store document.Store, pdf document.Gotenberg, scanner document.Scanner) func(context.Context) error {
	client := &http.Client{Timeout: 2 * time.Second}
	return func(ctx context.Context) error {
		if err := d.Ping(ctx); err != nil {
			return fmt.Errorf("database: %w", err)
		}
		if err := store.CheckLock(ctx); err != nil {
			return err
		}
		if err := pdf.Ping(ctx); err != nil {
			return err
		}
		if err := scanner.Ping(ctx); err != nil {
			return err
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
