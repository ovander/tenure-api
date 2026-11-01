package billing

import (
	"context"
	"fmt"

	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/jobs"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/period"
)

func deliverAfter(s Service, d *db.DB) func(ctx context.Context, o jobs.Owner, res any) any {
	return func(ctx context.Context, o jobs.Owner, res any) any {
		r, ok := res.(OwnerResult)
		if !ok || len(r.Issued) == 0 {
			return nil
		}
		return s.Deliver(ctx, d, db.Scope{OwnerID: o.ID, ActorSub: o.ActorSub, Role: "job"}, r.Issued)
	}
}

// MonthlyJob is billing.monthly (RENT-BE-001 §5): on the 1st at 06:00
// Europe/Paris, every owner's rent calls of the month, then their e-mails. d
// is the database the job runs on (for the deliveries after commit).
func MonthlyJob(s Service, d *db.DB) jobs.Job {
	return jobs.Job{
		Name:     "billing.monthly",
		Schedule: "0 6 1 * *",
		Period:   jobs.MonthKey,
		PerOwner: func(ctx context.Context, o jobs.Owner) (any, error) {
			p, err := period.Parse(o.Period)
			if err != nil {
				return nil, err
			}
			return s.MonthlyOwner(ctx, p, o.Today, o.DryRun)
		},
		AfterCommit: deliverAfter(s, d),
		ValidPeriod: func(p string) error { _, err := period.Parse(p); return err },
	}
}

// IRLFetchJob is irl.fetch (RENT-BE-001 §5): on the 15th of January, April,
// July and October at 07:00, cache the newly published IRL values, then apply
// every revision now due and send its notices.
func IRLFetchJob(s Service, d *db.DB, src params.INSEE) jobs.Job {
	return jobs.Job{
		Name:     "irl.fetch",
		Schedule: "0 7 15 1,4,7,10 *",
		Period:   jobs.QuarterKey,
		Platform: func(ctx context.Context, o jobs.Owner) (any, error) {
			series, err := src.Fetch(ctx)
			if err != nil {
				return nil, err
			}
			added := []string{}
			for q, v := range series {
				if o.DryRun {
					continue
				}
				ok, err := s.Params.InsertIRL(ctx, q, v, params.EstimatedPublication(q, o.Today).Time(), "insee")
				if err != nil {
					return nil, fmt.Errorf("irl %s: %w", q, err)
				}
				if ok {
					added = append(added, q)
				}
			}
			return map[string]any{"published": len(series), "added": added}, nil
		},
		PerOwner: func(ctx context.Context, o jobs.Owner) (any, error) {
			return s.RevisionsOwner(ctx, o.Today, o.DryRun)
		},
		AfterCommit: deliverAfter(s, d),
	}
}
