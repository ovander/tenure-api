package payment

import (
	"context"
	"sort"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/audit"
	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/problem"
)

// TypePaymentsInvalid is a link of a bank credit to payments that are not
// the scope's, already name a bank line, or do not add up to the credit.
const TypePaymentsInvalid = "payment/bank-payments-invalid"

func init() {
	problem.Register(map[string]problem.Message{
		TypePaymentsInvalid: problem.M(422, "Choisissez des paiements déjà saisis, sans ligne bancaire, dont le total fait exactement le montant du crédit.",
			"Choose recorded payments with no bank line, adding up to exactly the credit."),
	})
}

// LinkPaymentsInput is the body of POST /bank-lines/{id}/payments: the
// payments already recorded that the credit is (#256).
type LinkPaymentsInput struct {
	PaymentIDs []uuid.UUID `json:"payment_ids"`
	Version    int         `json:"version"`
}

// PaymentSuggestions lists the payments already recorded that a credit bank
// line may be (#256): no bank line yet, at most the credit's amount, paid
// within bank.matching's window of the line's day; the exact amount first,
// then the nearest day. A debit, or a line already matched or ignored, has
// none.
func (s Service) PaymentSuggestions(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID) ([]View, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return nil, err
	}
	out := []View{}
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		r, err := s.Repo.Line(ctx, id)
		if err != nil {
			return err
		}
		if r.Status != LineUnmatched || r.AmountCents <= 0 {
			return nil
		}
		m, _, _, err := s.matchingRules(ctx)
		if err != nil {
			return err
		}
		booked := lineOf(r).BookedOn
		rows, err := s.Repo.UnlinkedPayments(ctx, r.AmountCents, booked.AddDays(-m.WindowDays).Time(), booked.AddDays(m.WindowDays).Time())
		if err != nil {
			return err
		}
		gap := func(x paymentRow) int64 {
			d := x.PaidOn.Sub(r.BookedOn).Hours()
			if d < 0 {
				d = -d
			}
			return int64(d)
		}
		sort.SliceStable(rows, func(i, j int) bool {
			ei, ej := rows[i].AmountCents == r.AmountCents, rows[j].AmountCents == r.AmountCents
			if ei != ej {
				return ei
			}
			return gap(rows[i]) < gap(rows[j])
		})
		for _, x := range rows {
			_, v, err := s.get(ctx, x.ID)
			if err != nil {
				return err
			}
			out = append(out, v)
		}
		return nil
	})
	return out, err
}

// LinkPayments records that a credit bank line is payments the owner already
// entered by hand (#256): payments of the scope that name no bank line,
// adding up to exactly the credit, now name it; nothing is paid twice. The
// line becomes matched.
func (s Service) LinkPayments(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID, in LinkPaymentsInput) (MatchLineResult, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return MatchLineResult{}, err
	}
	out := MatchLineResult{Payments: []View{}}
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		r, err := s.Repo.Line(ctx, id)
		if err != nil {
			return err
		}
		if r.Version != in.Version {
			return invariant.ErrStale
		}
		if r.Status != LineUnmatched {
			return invariant.New(TypeLineState, id.String())
		}
		if r.AmountCents <= 0 {
			return invariant.New(TypeLineNotCredit, id.String())
		}
		ids := map[uuid.UUID]bool{}
		for _, pid := range in.PaymentIDs {
			ids[pid] = true
		}
		if len(ids) == 0 || len(ids) != len(in.PaymentIDs) {
			return invariant.New(TypePaymentsInvalid, "payment_ids")
		}
		rows, err := s.Repo.PaymentsForUpdate(ctx, in.PaymentIDs)
		if err != nil {
			return err
		}
		var sum int64
		for _, x := range rows {
			if x.BankLineID != nil {
				return invariant.New(TypePaymentsInvalid, x.ID.String())
			}
			sum += x.AmountCents
		}
		if len(rows) != len(ids) || sum != r.AmountCents {
			return invariant.New(TypePaymentsInvalid, "total")
		}
		now := s.now()
		for _, x := range rows {
			_, before, err := s.get(ctx, x.ID)
			if err != nil {
				return err
			}
			if err := s.Repo.LinkBankLine(ctx, x.ID, x.Version, id, now, p.Sub); err != nil {
				return err
			}
			_, after, err := s.get(ctx, x.ID)
			if err != nil {
				return err
			}
			if err := audit.Record(ctx, "payment.bank_linked", audit.Entity{Type: "payment", ID: x.ID.String()}, before, after); err != nil {
				return err
			}
			out.Payments = append(out.Payments, after)
		}
		_, b, today, err := s.matchingRules(ctx)
		if err != nil {
			return err
		}
		before := s.lineView(r, today, b.UnmatchedAlertDays)
		if err := s.Repo.SetLineStatus(ctx, r.ID, r.Version, LineMatched, r.Note, now, p.Sub); err != nil {
			return err
		}
		r.Status, r.Version = LineMatched, r.Version+1
		out.Line = s.lineView(r, today, b.UnmatchedAlertDays)
		return audit.Record(ctx, "payment.bank_match", audit.Entity{Type: "bank_line", ID: r.ID.String()}, before, out)
	})
	return out, err
}
