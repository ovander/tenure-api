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

	"github.com/ovander/tenure-api/internal/db"
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
}

// Job is a scheduled job. Each runs once per period key; PerOwner is called
// for every owner (or only the requested one) inside that owner's scope, and
// Platform, when set, once in the platform scope (no owner).
type Job struct {
	Name     string
	Schedule string // cron spec, Europe/Paris
	Period   func(t time.Time) string
	PerOwner func(ctx context.Context, o Owner) (any, error)
	Platform func(ctx context.Context, dryRun bool) (any, error)
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

// DayKey returns the key YYYY-MM-DD of t in Europe/Paris.
func DayKey(t time.Time) string { return t.In(Paris()).Format(time.DateOnly) }

type ownerResult struct {
	OwnerID string `json:"owner_id"`
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	Result  any    `json:"result,omitempty"`
}

// Trigger runs name now for its current period. owner limits the run to one
// owner; dryRun computes without recording the run. When a non-dry run of the
// same key exists, it is returned unchanged and nothing runs again.
func (r *Runner) Trigger(ctx context.Context, name string, owner *uuid.UUID, dryRun bool) (Run, bool, error) {
	j, ok := r.Jobs[name]
	if !ok {
		return Run{}, false, fmt.Errorf("%w: %s", ErrUnknownJob, name)
	}
	now := r.Now().UTC().Truncate(time.Microsecond)
	run := Run{ID: uuid.Must(uuid.NewV7()), Job: name, Period: j.Period(now), OwnerID: owner, DryRun: dryRun,
		StartedAt: now, Status: StatusRunning, Summary: json.RawMessage("{}")}
	if !dryRun {
		existing, created, err := r.claim(ctx, run)
		if err != nil || !created {
			return existing, false, err
		}
	}
	summary, status := r.execute(ctx, j, owner, dryRun)
	run.Status = status
	run.Summary, _ = json.Marshal(summary)
	fin := r.Now().UTC().Truncate(time.Microsecond)
	run.FinishedAt = &fin
	if !dryRun {
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

// execute runs the job and returns its summary and status: failed when every
// unit failed, partial when some did, succeeded otherwise.
func (r *Runner) execute(ctx context.Context, j Job, only *uuid.UUID, dryRun bool) (map[string]any, Status) {
	summary := map[string]any{}
	failures, units := 0, 0
	if j.Platform != nil && only == nil {
		units++
		var res any
		err := r.DB.InScope(ctx, db.Scope{Role: "job"}, func(ctx context.Context) error {
			var err error
			res, err = j.Platform(ctx, dryRun)
			return err
		})
		pr := ownerResult{OwnerID: "platform", OK: err == nil, Result: res}
		if err != nil {
			failures++
			pr.Error = err.Error()
		}
		summary["platform"] = pr
	}
	if j.PerOwner != nil {
		ids := []uuid.UUID{}
		if only != nil {
			ids = append(ids, *only)
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
			var res any
			err := r.DB.InScope(ctx, db.Scope{OwnerID: id, Role: "job"}, func(ctx context.Context) error {
				var err error
				res, err = j.PerOwner(ctx, Owner{ID: id, DryRun: dryRun})
				return err
			})
			or := ownerResult{OwnerID: id.String(), OK: err == nil, Result: res}
			if err != nil {
				failures++
				or.Error = err.Error()
				slog.ErrorContext(ctx, "job owner failed", "job", j.Name, "owner_id", id, "error", err)
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
		if _, err := r.cron.AddFunc(j.Schedule, func() {
			if _, _, err := r.Trigger(ctx, name, nil, false); err != nil {
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
