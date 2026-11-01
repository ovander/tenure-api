package charge

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/jobs"
)

// ParseKey reads the period key of charges.reconcile: "<asset id>/<year>".
func ParseKey(key string) (uuid.UUID, int, error) {
	a, y, ok := strings.Cut(key, "/")
	asset, err := uuid.Parse(a)
	if !ok || err != nil {
		return uuid.Nil, 0, fmt.Errorf("charges.reconcile: period is <asset id>/<year>, got %q", key)
	}
	year, err := strconv.Atoi(y)
	if err != nil || year < 1970 || year > 9999 {
		return uuid.Nil, 0, fmt.Errorf("charges.reconcile: year %q", y)
	}
	return asset, year, nil
}

// ReconcileJob is charges.reconcile (RENT-BE-001 §5): on demand, for one
// asset and exercise year (period "<asset id>/<year>"), it allocates the
// reconciliation in draft or allocated, or with dry_run returns the
// allocation without writing. Issuing stays an owner action.
func ReconcileJob(s Service) jobs.Job {
	return jobs.Job{
		Name:   "charges.reconcile",
		Period: func(time.Time) string { return "" },
		PerOwner: func(ctx context.Context, o jobs.Owner) (any, error) {
			asset, year, err := ParseKey(o.Period)
			if err != nil {
				return nil, err
			}
			r, ok, err := s.Repo.LiveRecon(ctx, asset, year)
			if err != nil {
				return nil, err
			}
			if !ok {
				return map[string]any{"skipped": "no reconciliation for this asset and year in this scope"}, nil
			}
			if st := State(r.State); st != StateDraft && st != StateAllocated {
				return map[string]any{"skipped": "reconciliation " + r.State}, nil
			}
			lines, err := s.Repo.Lines(ctx, r.ID)
			if err != nil {
				return nil, err
			}
			if o.DryRun {
				a, err := s.compute(ctx, r, lines)
				if err != nil {
					return nil, err
				}
				return map[string]any{"reconciliation_id": r.ID, "allocation": a.alloc, "totals": a.totals}, nil
			}
			before := r.Version
			lines, err = s.allocate(ctx, &r, lines)
			if err != nil {
				return nil, err
			}
			r.State, r.UpdatedAt, r.UpdatedBy = string(StateAllocated), s.now(), actorOf(ctx)
			if err := s.Repo.UpdateRecon(ctx, r, before); err != nil {
				return nil, err
			}
			r.Version = before + 1
			return reconView(r, lines), nil
		},
		ValidPeriod: func(p string) error { _, _, err := ParseKey(p); return err },
	}
}
