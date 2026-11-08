package lease

import (
	"context"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/period"
)

// VacancyView is an asset's vacancy: the periods without a lease and the
// vacant days of each calendar year (RENT-SPEC-010, RENT-SPEC-016).
type VacancyView struct {
	AssetID    uuid.UUID   `json:"asset_id"`
	Since      period.Date `json:"since" swaggertype:"string" format:"date"`
	Periods    []Vacancy   `json:"periods"`
	DaysByYear map[int]int `json:"days_by_year"`
}

// Terms lists a lease's terms, renewed tacitly, as read today (RENT-SPEC-010,
// Q-119): the furnished student term of legal_params does not renew.
func (s Service) Terms(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID) (TermsView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return TermsView{}, err
	}
	var out TermsView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		st, err := s.Repo.Get(ctx, id, false)
		if err != nil {
			return err
		}
		ps, err := s.Params.Load(ctx)
		if err != nil {
			return err
		}
		v := view(st)
		lt, err := ps.LeaseTerm(v.StartOn)
		if err != nil {
			return err
		}
		f := TermFacts{StartOn: v.StartOn, TermMonths: v.TermMonths, Renews: RenewsTacitly(v.Kind, v.TermMonths, lt.FurnishedStudent)}
		if st.lease.EndOn != nil {
			e := period.FromTime(*st.lease.EndOn)
			f.EndOn = &e
		}
		out = Terms(f, s.today())
		return nil
	})
	return out, err
}

// Vacancies lists an asset's periods without a lease that is not a draft,
// from its acquisition (or else its first lease) to today, and the vacant
// days of each year (RENT-SPEC-010, Q-119).
func (s Service) Vacancies(ctx context.Context, p *auth.Principal, requestID string, asset uuid.UUID) (VacancyView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return VacancyView{}, err
	}
	var out VacancyView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		a, err := s.Portfolio.AssetInTx(ctx, asset)
		if err != nil {
			return err
		}
		rows, err := s.Repo.Occupancies(ctx, asset)
		if err != nil {
			return err
		}
		occ := make([]Occupancy, len(rows))
		for i, r := range rows {
			occ[i] = Occupancy{StartOn: period.FromTime(r.StartOn)}
			if r.EndOn != nil {
				e := period.FromTime(*r.EndOn)
				occ[i].EndOn = &e
			}
		}
		var since period.Date
		if a.AcquiredOn != nil {
			since = *a.AcquiredOn
		} else if len(occ) > 0 {
			since = occ[0].StartOn
		}
		out.AssetID, out.Since = asset, since
		out.Periods, out.DaysByYear = Vacancies(occ, since, s.today())
		return nil
	})
	return out, err
}
