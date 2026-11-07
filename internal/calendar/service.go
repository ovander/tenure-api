package calendar

import (
	"context"
	"time"

	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/period"
)

// Service serves the computed calendar.
type Service struct {
	DB     *db.DB
	Repo   Repository
	Params params.Repository
	Now    func() time.Time
}

// View is the calendar over a range.
type View struct {
	From  period.Date `json:"from" swaggertype:"string" format:"date"`
	To    period.Date `json:"to" swaggertype:"string" format:"date"`
	Items []Item      `json:"items"`
}

// defaultDays is the default range, the « next 90 days » list of
// RENT-SPEC-012 (a product choice, not a legal delay).
const defaultDays = 90

// maxDays bounds a range (a product limit).
const maxDays = 1100

func (s Service) today() period.Date {
	loc, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		loc = time.UTC
	}
	return period.FromTime(s.Now().In(loc))
}

// Calendar lists the scope's obligations from from to to (today and the next
// 90 days by default), with the overdue ones, for one asset or all. The owner
// and the co-owner read it; the accountant does not (it carries inventory and
// deposit dates).
func (s Service) Calendar(ctx context.Context, p *auth.Principal, requestID string, from, to *period.Date, asset string) (View, error) {
	if p.Active == nil || p.Active.Role == auth.RoleAccountant {
		return View{}, invariant.ErrForbidden
	}
	today := s.today()
	v := View{From: today, To: today.AddDays(defaultDays)}
	if from != nil {
		v.From = *from
	}
	if to != nil {
		v.To = *to
	} else if from != nil {
		v.To = v.From.AddDays(defaultDays)
	}
	if v.To.Before(v.From) || v.From.AddDays(maxDays).Before(v.To) {
		return View{}, invariant.Fields{{Field: "to", Message: "La période va du début à la fin, sur trois ans au plus."}}
	}
	err := s.DB.InScope(ctx, p.DBScope(requestID), func(ctx context.Context) error {
		f, err := s.Repo.Facts(ctx)
		if err != nil {
			return err
		}
		ps, err := s.Params.Load(ctx)
		if err != nil {
			return err
		}
		items, err := Build(f, v.From, v.To, today, ps)
		if err != nil {
			return err
		}
		v.Items = []Item{}
		for _, it := range items {
			if asset == "" || it.AssetID == asset {
				v.Items = append(v.Items, it)
			}
		}
		return nil
	})
	return v, err
}
