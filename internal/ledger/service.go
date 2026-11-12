package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/document"
	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/payment"
	"github.com/ovander/tenure-api/internal/period"
	"github.com/ovander/tenure-api/internal/problem"
	"github.com/ovander/tenure-api/internal/tax"
)

// TypeChartIncomplete is a journal refused for a chart of accounts that
// misses a role or a charge category.
const TypeChartIncomplete = "ledger/chart-incomplete"

func init() {
	problem.Register(map[string]problem.Message{
		TypeChartIncomplete: problem.M(422, "Le plan de comptes ne couvre pas tous les mouvements de cette année.",
			"The chart of accounts does not cover every movement of this year."),
	})
}

// Service serves the owner's journal.
type Service struct {
	DB     *db.DB
	Repo   Repository
	Params params.Repository
	Now    func() time.Time
}

// View is the journal of a calendar year.
type View struct {
	Year int `json:"year"`
	// Closed says the year's tax summary is issued: its figures no longer
	// change (Q-127).
	Closed   bool      `json:"closed"`
	Entries  []Entry   `json:"entries"`
	Balances []Balance `json:"balances"`
}

func scoped(p *auth.Principal, requestID string) (db.Scope, error) {
	if p.Active == nil {
		return db.Scope{}, invariant.ErrForbidden
	}
	return p.DBScope(requestID), nil
}

// statementFigures are the figures of a deposit statement the journal
// posts (inventory.Statement as issued).
type statementFigures struct {
	DepositCents        money.Cents `json:"deposit_cents"`
	DeductionsCents     money.Cents `json:"deductions_cents"`
	ArrearsCents        money.Cents `json:"arrears_cents"`
	RegularisationCents money.Cents `json:"regularisation_cents"`
	HoldbackCents       money.Cents `json:"holdback_cents"`
	PenaltyCents        money.Cents `json:"penalty_cents"`
	TotalCents          money.Cents `json:"total_cents"`
}

// inputs gathers the movements of [from, to].
func (s Service) inputs(ctx context.Context, from, to time.Time) (Inputs, error) {
	var in Inputs
	ps, ms, err := s.Repo.Payments(ctx, from, to)
	if err != nil {
		return in, err
	}
	byPayment := map[uuid.UUID][]matchRow{}
	for _, m := range ms {
		byPayment[m.PaymentID] = append(byPayment[m.PaymentID], m)
	}
	for _, p := range ps {
		lp := Payment{ID: p.ID.String(), LeaseID: p.LeaseID.String(), PaidOn: period.FromTime(p.PaidOn), AmountCents: money.Cents(p.AmountCents)}
		for _, m := range byPayment[p.ID] {
			var lines []document.Line
			if err := json.Unmarshal(m.Lines, &lines); err != nil {
				return in, err
			}
			var rent, prov money.Cents
			for _, l := range lines {
				switch l.Kind {
				case string(payment.LineRent):
					rent += l.AmountCents
				case string(payment.LineProvision):
					prov += l.AmountCents
				}
			}
			// The same split as the tax summary (Q-65), so the rent account
			// is line 211's figure.
			parts, err := tax.SplitMatch(money.Cents(m.AmountCents), rent, prov, money.Cents(m.TotalCents))
			if err != nil {
				return in, err
			}
			for _, x := range parts {
				switch x.Line {
				case payment.LineRent:
					lp.RentCents += x.AmountCents
				case payment.LineProvision:
					lp.ProvisionCents += x.AmountCents
				default:
					lp.RegularisationCents += x.AmountCents
				}
			}
		}
		in.Payments = append(in.Payments, lp)
	}
	cs, err := s.Repo.Charges(ctx, from, to)
	if err != nil {
		return in, err
	}
	for _, c := range cs {
		in.Charges = append(in.Charges, Charge{ID: c.ID.String(), AssetID: c.AssetID.String(), Category: c.Category,
			PaidOn: period.FromTime(c.PaidOn), AmountCents: money.Cents(c.AmountCents), RecoverableCents: money.Cents(c.RecoverableCents)})
	}
	is, err := s.Repo.Instalments(ctx, from, to)
	if err != nil {
		return in, err
	}
	for _, i := range is {
		in.Instalments = append(in.Instalments, Instalment{LoanID: i.LoanID.String(), Seq: i.Seq, DueOn: period.FromTime(i.DueOn),
			PrincipalCents: money.Cents(i.PrincipalCents), InterestCents: money.Cents(i.InterestCents), InsuranceCents: money.Cents(i.InsuranceCents)})
	}
	ds, err := s.Repo.Deposits(ctx, from, to)
	if err != nil {
		return in, err
	}
	for _, d := range ds {
		in.Deposits = append(in.Deposits, Deposit{ID: d.ID.String(), LeaseID: d.LeaseID.String(), ReceivedOn: period.FromTime(d.ReceivedOn),
			AmountCents: money.Cents(d.AmountCents)})
	}
	ss, err := s.Repo.Settlements(ctx, from, to)
	if err != nil {
		return in, err
	}
	for _, x := range ss {
		var f statementFigures
		if err := json.Unmarshal(x.Figures, &f); err != nil {
			return in, fmt.Errorf("ledger: settlement %s: %w", x.ID, err)
		}
		in.Settlements = append(in.Settlements, Settlement{ID: x.ID.String(), LeaseID: x.LeaseID.String(), SettledOn: period.FromTime(x.SettledOn),
			ReleasedCents: f.DepositCents - f.HoldbackCents, DeductionsCents: f.DeductionsCents, ArrearsCents: f.ArrearsCents,
			RegularisationCents: f.RegularisationCents, PenaltyCents: f.PenaltyCents, TotalCents: f.TotalCents})
	}
	return in, nil
}

// Year returns the journal of a calendar year (RENT-SPEC-014, Q-127), the
// chart of accounts being the one effective on its 31 December.
func (s Service) Year(ctx context.Context, p *auth.Principal, requestID string, year int) (View, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return View{}, err
	}
	if year < 1970 || year > 9999 {
		var f invariant.Fields
		f.Add("year", "Une année sur quatre chiffres est attendue.")
		return View{}, f
	}
	from, to := period.NewDate(year, time.January, 1), period.NewDate(year, time.December, 31)
	out := View{Year: year}
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		ps, err := s.Params.Load(ctx)
		if err != nil {
			return err
		}
		chart, err := ps.LedgerChart(to)
		if err != nil {
			return err
		}
		in, err := s.inputs(ctx, from.Time(), to.Time())
		if err != nil {
			return err
		}
		out.Entries, out.Balances, err = Journal(in, chart, from, to)
		if errors.Is(err, ErrChart) {
			return invariant.New(TypeChartIncomplete, err.Error())
		}
		if err != nil {
			return err
		}
		out.Closed, err = s.Repo.Closed(ctx, year)
		return err
	})
	return out, err
}
