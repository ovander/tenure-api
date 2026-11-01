// Package jobs runs the scheduled jobs in process (RENT-BE-001 §5, BE-OD-01):
// robfig/cron on Europe/Paris time, a job_runs row as the lock keyed on (job,
// period[, owner]), owners iterated one by one under their own row-level
// security scope as tenure_jobs, failures kept per owner so a run ends
// partial rather than stopping. A second start of the same key is a no-op that
// returns the existing run; a dry run computes and records nothing.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
	"gorm.io/gorm/clause"

	"github.com/ovander/tenure-api/internal/audit"
	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/period"
)

// Status is the outcome of a run.
type Status string

// Run statuses.
const (
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusPartial   Status = "partial"
)

// Run is a job_runs row.
type Run struct {
	ID         uuid.UUID       `json:"id" gorm:"column:id;primaryKey"`
	Job        string          `json:"job" gorm:"column:job"`
	Period     string          `json:"period" gorm:"column:period"`
	OwnerID    *uuid.UUID      `json:"owner_id" gorm:"column:owner_id"`
	DryRun     bool            `json:"dry_run" gorm:"column:dry_run"`
	StartedAt  time.Time       `json:"started_at" gorm:"column:started_at"`
	FinishedAt *time.Time      `json:"finished_at" gorm:"column:finished_at"`
	Status     Status          `json:"status" gorm:"column:status"`
	Summary    json.RawMessage `json:"summary" gorm:"column:summary;type:jsonb" swaggertype:"object"`
}

// TableName maps Run to job_runs.
func (Run) TableName() string { return "job_runs" }

// Owner is the context of one owner iteration.
type Owner struct {
	ID     uuid.UUID
	DryRun bool
	// Period is the run's period key; Today the run's date in Europe/Paris.
	Period string
	Today  period.Date
	RunID  uuid.UUID
	// ActorSub is the subject of the caller who triggered the run, if any.
	ActorSub string
}

// Job is a scheduled job. Each runs once per period key; PerOwner is called
// for every owner (or only the requested one) inside that owner's scope, and
// Platform, when set, once in the platform scope (no owner).
type Job struct {
	Name     string
	Schedule string // cron spec, Europe/Paris; empty for a job run on demand only
	Period   func(t time.Time) string
	PerOwner func(ctx context.Context, o Owner) (any, error)
	Platform func(ctx context.Context, o Owner) (any, error)
	// AfterCommit runs after an owner's transaction committed, with its
	// result (e-mail delivery: never sent for a rolled-back document).
	AfterCommit func(ctx context.Context, o Owner, result any) any
	// ValidPeriod checks a period given by a caller instead of Period(now).
	ValidPeriod func(p string) error
}

// Options are the options of a triggered run.
type Options struct {
	// Owner limits the run to one owner scope.
	Owner *uuid.UUID
	// DryRun computes without writing or recording the run.
	DryRun bool
	// ActorSub is the Socrate subject of a caller who triggered the run (its
	// audit events name them); empty for the scheduler.
	ActorSub string
	// Period replaces the current period key.
	Period string
}

// Runner runs jobs against the tenure_jobs database role.
type Runner struct {
	DB   *db.DB
	Jobs map[string]Job
	Now  func() time.Time
	cron *cron.Cron
}

// ErrUnknownJob is returned for a job name that is not registered.
var ErrUnknownJob = errors.New("jobs: unknown job")

// Paris is the zone of the schedules and period keys.
func Paris() *time.Location {
	loc, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		return time.UTC
	}
	return loc
}

// WeekKey returns the ISO week key YYYY-Www of t in Europe/Paris.
func WeekKey(t time.Time) string {
	y, w := t.In(Paris()).ISOWeek()
	return fmt.Sprintf("%04d-W%02d", y, w)
}

// MonthKey returns the key YYYY-MM of t in Europe/Paris.
func MonthKey(t time.Time) string { return t.In(Paris()).Format("2006-01") }

// QuarterKey returns the key YYYY-Qn of t in Europe/Paris.
func QuarterKey(t time.Time) string {
	p := t.In(Paris())
	return fmt.Sprintf("%04d-Q%d", p.Year(), (int(p.Month())+2)/3)
}

// HourKey returns the key YYYY-MM-DD-HH of t in Europe/Paris.
func HourKey(t time.Time) string { return t.In(Paris()).Format("2006-01-02-15") }

// DayKey returns the key YYYY-MM-DD of t in Europe/Paris.
func DayKey(t time.Time) string { return t.In(Paris()).Format(time.DateOnly) }

type ownerResult struct {
	OwnerID     string `json:"owner_id"`
	OK          bool   `json:"ok"`
	Error       string `json:"error,omitempty"`
	Result      any    `json:"result,omitempty"`
	AfterCommit any    `json:"after_commit,omitempty"`
}

// ErrInvalidPeriod is returned for a period a job does not accept.
var ErrInvalidPeriod = errors.New("jobs: invalid period")

// Trigger runs name now (RENT-BE-001 §5). With Options.Owner the run is
// limited to that owner; with DryRun it computes without writing and is not
// recorded. When a non-dry run of the same (job, period, owner) exists, it is
// returned unchanged and nothing runs again (ran is false).
func (r *Runner) Trigger(ctx context.Context, name string, opts Options) (run Run, ran bool, err error) {
	j, ok := r.Jobs[name]
	if !ok {
		return Run{}, false, fmt.Errorf("%w: %s", ErrUnknownJob, name)
	}
	now := r.Now().UTC().Truncate(time.Microsecond)
	key := j.Period(now)
	if opts.Period != "" {
		if j.ValidPeriod == nil {
			return Run{}, false, fmt.Errorf("%w: %s takes no period", ErrInvalidPeriod, name)
		}
		if err := j.ValidPeriod(opts.Period); err != nil {
			return Run{}, false, fmt.Errorf("%w: %v", ErrInvalidPeriod, err)
		}
		key = opts.Period
	}
	run = Run{ID: uuid.Must(uuid.NewV7()), Job: name, Period: key, OwnerID: opts.Owner, DryRun: opts.DryRun,
		StartedAt: now, Status: StatusRunning, Summary: json.RawMessage("{}")}
	if !opts.DryRun {
		existing, created, err := r.claim(ctx, run)
		if err != nil || !created {
			return existing, false, err
		}
	}
	summary, status := r.execute(ctx, j, run, opts, period.FromTime(now.In(Paris())))
	run.Status = status
	run.Summary, _ = json.Marshal(summary)
	fin := r.Now().UTC().Truncate(time.Microsecond)
	run.FinishedAt = &fin
	if !opts.DryRun {
		if err := r.finish(ctx, run); err != nil {
			return run, true, err
		}
	}
	if status != StatusSucceeded {
		slog.ErrorContext(ctx, "job did not succeed", "job", name, "period", run.Period, "status", status, "run_id", run.ID)
	}
	return run, true, nil
}

func (r *Runner) claim(ctx context.Context, run Run) (Run, bool, error) {
	var out Run
	created := false
	err := r.DB.InScope(ctx, db.Scope{Role: "job"}, func(ctx context.Context) error {
		tx, err := db.Tx(ctx)
		if err != nil {
			return err
		}
		res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&run)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 1 {
			out, created = run, true
			return nil
		}
		q := tx.Where("job = ? AND period = ? AND NOT dry_run", run.Job, run.Period)
		if run.OwnerID == nil {
			q = q.Where("owner_id IS NULL")
		} else {
			q = q.Where("owner_id = ?", *run.OwnerID)
		}
		return q.First(&out).Error
	})
	return out, created, err
}

func (r *Runner) finish(ctx context.Context, run Run) error {
	return r.DB.InScope(ctx, db.Scope{Role: "job"}, func(ctx context.Context) error {
		tx, err := db.Tx(ctx)
		if err != nil {
			return err
		}
		return tx.Model(&Run{}).Where("id = ?", run.ID).Updates(map[string]any{
			"status": run.Status, "summary": run.Summary, "finished_at": run.FinishedAt}).Error
	})
}

func (r *Runner) owners(ctx context.Context) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	err := r.DB.InScope(ctx, db.Scope{Role: "job"}, func(ctx context.Context) error {
		tx, err := db.Tx(ctx)
		if err != nil {
			return err
		}
		return tx.Table("owners").Order("id").Pluck("id", &ids).Error
	})
	return ids, err
}

// unit runs one owner (or the platform when id is nil) in its scope, records
// the job's audit event in the same transaction, then runs AfterCommit.
func (r *Runner) unit(ctx context.Context, j Job, run Run, opts Options, today period.Date, id *uuid.UUID) ownerResult {
	o := Owner{DryRun: opts.DryRun, Period: run.Period, Today: today, RunID: run.ID, ActorSub: opts.ActorSub}
	sc := db.Scope{Role: "job", ActorSub: opts.ActorSub}
	label := "platform"
	f := j.Platform
	if id != nil {
		o.ID, sc.OwnerID, label, f = *id, *id, id.String(), j.PerOwner
	}
	var res any
	err := r.DB.InScope(ctx, sc, func(ctx context.Context) error {
		var err error
		if res, err = f(ctx, o); err != nil {
			return err
		}
		if opts.DryRun {
			return nil
		}
		return audit.Record(ctx, "job."+j.Name, audit.Entity{Type: "job_run", ID: run.ID.String()}, nil,
			map[string]any{"period": run.Period, "owner_scope": opts.Owner != nil})
	})
	out := ownerResult{OwnerID: label, OK: err == nil, Result: res}
	if err != nil {
		out.Error = err.Error()
		slog.ErrorContext(ctx, "job unit failed", "job", j.Name, "owner", label, "error", err)
		return out
	}
	if j.AfterCommit != nil && !opts.DryRun {
		out.AfterCommit = j.AfterCommit(ctx, o, res)
	}
	return out
}

// execute runs the job and returns its summary and status: failed when every
// unit failed, partial when some did, succeeded otherwise.
func (r *Runner) execute(ctx context.Context, j Job, run Run, opts Options, today period.Date) (map[string]any, Status) {
	summary := map[string]any{}
	failures, units := 0, 0
	if j.Platform != nil && opts.Owner == nil {
		units++
		pr := r.unit(ctx, j, run, opts, today, nil)
		if !pr.OK {
			failures++
		}
		summary["platform"] = pr
	}
	if j.PerOwner != nil {
		ids := []uuid.UUID{}
		if opts.Owner != nil {
			ids = append(ids, *opts.Owner)
		} else {
			var err error
			if ids, err = r.owners(ctx); err != nil {
				summary["error"] = err.Error()
				return summary, StatusFailed
			}
		}
		results := []ownerResult{}
		for _, id := range ids {
			units++
			or := r.unit(ctx, j, run, opts, today, &id)
			if !or.OK {
				failures++
			}
			results = append(results, or)
		}
		sort.Slice(results, func(a, b int) bool { return results[a].OwnerID < results[b].OwnerID })
		summary["owners"] = results
	}
	summary["units"], summary["failures"] = units, failures
	switch failures {
	case 0:
		return summary, StatusSucceeded
	case units:
		return summary, StatusFailed
	default:
		return summary, StatusPartial
	}
}

// Start schedules every job and returns when ctx ends.
func (r *Runner) Start(ctx context.Context) error {
	r.cron = cron.New(cron.WithLocation(Paris()))
	for name, j := range r.Jobs {
		name := name
		if j.Schedule == "" {
			continue // on demand only (charges.reconcile)
		}
		if _, err := r.cron.AddFunc(j.Schedule, func() {
			if _, _, err := r.Trigger(ctx, name, Options{}); err != nil {
				slog.ErrorContext(ctx, "scheduled job failed", "job", name, "error", err)
			}
		}); err != nil {
			return fmt.Errorf("jobs: schedule %s: %w", name, err)
		}
	}
	r.cron.Start()
	<-ctx.Done()
	<-r.cron.Stop().Done()
	return nil
}
