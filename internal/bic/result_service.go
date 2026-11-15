package bic

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/ledger"
	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/period"
	"github.com/ovander/tenure-api/internal/tax"
)

// AssetYear is one furnished asset's figures for a year, the owner's share
// applied.
type AssetYear struct {
	AssetID uuid.UUID `json:"asset_id"`
	// ShareBP is the owner's share of the asset.
	ShareBP           int64       `json:"share_bp"`
	ReceiptsCents     money.Cents `json:"receipts_cents"`
	ChargesCents      money.Cents `json:"charges_cents"`
	InterestCents     money.Cents `json:"interest_cents"`
	ExpensedCents     money.Cents `json:"expensed_cents"`
	DepreciationCents money.Cents `json:"depreciation_cents"`
	// GridVersion is the asset's grid, 0 while it has none (its building is
	// then not depreciated).
	GridVersion int `json:"grid_version"`
}

// ResultView is the BIC result of the scope's owner for a year: the year,
// the chain of the years before it that carry depreciation into it, and the
// year's detail by asset.
type ResultView struct {
	Year   int          `json:"year"`
	Result YearResult   `json:"result"`
	Chain  []YearResult `json:"chain"`
	Assets []AssetYear  `json:"assets"`
}

// yearFigures gathers the owner's furnished letting of a year (Q-132):
// receipts (rent, provisions, regularisation, what a statement kept, the
// income categories of charges), charges (the other charges paid, the
// recoverable part included, the penalty of a statement), the loan interest
// and insurance, the furniture expensed, and the depreciation; each asset's
// figures times the owner's share.
func (s Service) yearFigures(ctx context.Context, owner uuid.UUID, year int, ps params.Set) (YearInput, []AssetYear, error) {
	from, to := period.NewDate(year, time.January, 1), period.NewDate(year, time.December, 31)
	in := YearInput{Year: year}
	assets, shares, err := tax.Repository{}.Assets(ctx, from.Time(), to.Time())
	if err != nil {
		return in, nil, err
	}
	mine := map[uuid.UUID]int64{}
	for _, sh := range shares {
		if sh.OwnerID == owner {
			mine[sh.AssetID] = sh.BP
		}
	}
	chart, err := ps.LedgerChart(to)
	if err != nil {
		return in, nil, err
	}
	income := map[string]bool{}
	for _, c := range chart.IncomeCategories {
		income[c] = true
	}
	moves, err := ledger.Service{}.InputsInTx(ctx, from.Time(), to.Time())
	if err != nil {
		return in, nil, err
	}
	var out []AssetYear
	for _, a := range assets {
		bp := mine[a.ID]
		if !a.Furnished || bp == 0 {
			continue
		}
		id := a.ID.String()
		y := AssetYear{AssetID: a.ID, ShareBP: bp}
		var receipts, charges, interest money.Cents
		for _, p := range moves.Payments {
			if p.AssetID == id {
				receipts += p.RentCents + p.ProvisionCents + p.RegularisationCents
			}
		}
		for _, c := range moves.Charges {
			if c.AssetID != id {
				continue
			}
			if income[c.Category] {
				receipts += c.AmountCents
			} else {
				charges += c.AmountCents
			}
		}
		for _, st := range moves.Settlements {
			if st.AssetID == id {
				receipts += st.ArrearsCents + st.RegularisationCents + st.DeductionsCents
				charges += st.PenaltyCents
			}
		}
		for _, i := range moves.Instalments {
			if i.AssetID == id {
				interest += i.InterestCents + i.InsuranceCents
			}
		}
		items, err := s.Repo.Items(ctx, a.ID)
		if err != nil {
			return in, nil, err
		}
		var expensed money.Cents
		for _, it := range items {
			// Works expensed are recorded as charges; only furniture adds here.
			if it.Kind == KindFurniture && it.Treatment == TreatmentExpense && it.InServiceOn.Year() == year {
				expensed += money.Cents(it.CostCents)
			}
		}
		dep, err := s.depreciationInTx(ctx, a.ID, year)
		if err != nil {
			return in, nil, err
		}
		y.ReceiptsCents, y.ChargesCents, y.InterestCents = Share(receipts, bp), Share(charges, bp), Share(interest, bp)
		y.ExpensedCents, y.DepreciationCents, y.GridVersion = Share(expensed, bp), Share(dep.TotalCents, bp), dep.GridVersion
		in.ReceiptsCents += y.ReceiptsCents
		in.ChargesCents += y.ChargesCents + y.InterestCents + y.ExpensedCents
		in.DepreciationCents += y.DepreciationCents
		out = append(out, y)
	}
	if out == nil {
		out = []AssetYear{}
	}
	return in, out, nil
}

// firstYear is the first year to chain from: the earliest day an asset
// of the scope was put in service under a grid or an item, else year.
func (s Service) firstYear(ctx context.Context, year int) (int, error) {
	first, err := s.Repo.FirstInService(ctx)
	if err != nil || first.IsZero() || first.Year() > year {
		return year, err
	}
	return first.Year(), nil
}

// resultInTx chains the years from the first one in service to year: a
// declared year as stored, the others computed, year under regime (reel
// unless given), the years before it not declared under the réel.
func (s Service) resultInTx(ctx context.Context, owner uuid.UUID, year int, regime string, done map[int]Declaration) (ResultView, error) {
	out := ResultView{Year: year}
	ps, err := s.Params.Load(ctx)
	if err != nil {
		return out, err
	}
	first, err := s.firstYear(ctx, year)
	if err != nil {
		return out, err
	}
	for y := range done {
		first = min(first, y)
	}
	var years []YearInput
	for y := first; y <= year; y++ {
		if d, ok := done[y]; ok {
			r := d.Result
			years = append(years, YearInput{Year: y, Stored: &r})
			if y == year {
				out.Assets = d.Assets
			}
			continue
		}
		in, assets, err := s.yearFigures(ctx, owner, y, ps)
		if err != nil {
			return out, err
		}
		if y == year {
			in.Regime = regime
			out.Assets = assets
		}
		years = append(years, in)
	}
	var microErr error
	micro := func(y int) params.BICMicro {
		m, err := ps.BICMicro(period.NewDate(y, time.December, 31))
		if err != nil && microErr == nil {
			microErr = err
		}
		return m
	}
	deficit, err := ps.BICDeficitYears(period.NewDate(year, time.December, 31))
	if err != nil {
		return out, err
	}
	out.Chain = Results(years, micro, deficit.Years)
	if microErr != nil {
		return out, microErr
	}
	out.Result = out.Chain[len(out.Chain)-1]
	if out.Assets == nil {
		out.Assets = []AssetYear{}
	}
	return out, nil
}

// Result computes the BIC result of the scope's owner for a year
// (RENT-SPEC-019, Q-132): each year from the first one in service is
// chained, the declared ones as they were issued, so the depreciation the
// cap set aside and the deficits reach the year asked. A year not declared
// is computed under the réel; the micro figure is given beside.
func (s Service) Result(ctx context.Context, p *auth.Principal, requestID string, year int) (ResultView, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return ResultView{}, err
	}
	if year < 1970 || year > 9999 {
		return ResultView{}, fields("year", "Une année sur quatre chiffres est attendue.")
	}
	var out ResultView
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		done, err := s.declarationsInTx(ctx)
		if err != nil {
			return err
		}
		out, err = s.resultInTx(ctx, sc.OwnerID, year, RegimeReel, done)
		return err
	})
	return out, err
}
