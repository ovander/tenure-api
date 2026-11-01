package tax

import (
	"context"
	"strconv"

	"github.com/ovander/tenure-api/internal/jobs"
)

// AnnualJob is tax.annual (RENT-BE-001 §5): on 10 January at 06:00, every
// owner's draft summary of the previous income year; the owner issues it. It
// first checks that form_2044_line_map has a map for that income year, and
// fails the run before drafting anything when it has none.
func AnnualJob(s Service) jobs.Job {
	return jobs.Job{
		Name:     "tax.annual",
		Schedule: "0 6 10 1 *",
		Period:   jobs.PreviousYearKey,
		Check: func(ctx context.Context, o jobs.Owner) error {
			year, err := strconv.Atoi(o.Period)
			if err != nil {
				return err
			}
			ps, err := s.Params.Load(ctx)
			if err != nil {
				return err
			}
			_, err = ps.Form2044LineMap(year)
			return err
		},
		PerOwner: func(ctx context.Context, o jobs.Owner) (any, error) {
			year, err := strconv.Atoi(o.Period)
			if err != nil {
				return nil, err
			}
			return s.DraftOwner(ctx, o.ID, year, o.DryRun)
		},
		ValidPeriod: func(p string) error {
			_, err := strconv.Atoi(p)
			return err
		},
	}
}
