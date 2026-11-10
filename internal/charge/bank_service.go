package charge

import (
	"context"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/audit"
	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/payment"
	"github.com/ovander/tenure-api/internal/period"
	"github.com/ovander/tenure-api/internal/problem"
)

// Problem types of debits matched to charges.
const (
	TypeChargesInvalid = "charge/bank-charges-invalid"
	TypeLineNotDebit   = "charge/bank-line-not-debit"
)

func init() {
	problem.Register(map[string]problem.Message{
		TypeChargesInvalid: problem.M(422, "Les charges choisies doivent être confirmées, non payées par une autre ligne et totaliser exactement le débit.", "The charges chosen must be confirmed, not paid by another line, and add up to exactly the debit."),
		TypeLineNotDebit:   problem.M(422, "Seul un débit se rapproche d’une charge.", "Only a debit matches a charge."),
	})
}

// MatchChargesInput is the body of POST /bank-lines/{id}/charges: the
// charges a bank debit paid, adding up to it.
type MatchChargesInput struct {
	ChargeIDs []uuid.UUID `json:"charge_ids"`
	Version   int         `json:"version"`
}

// MatchChargesResult is a debit matched: the line and the charges it paid.
type MatchChargesResult struct {
	Line    payment.LineView `json:"line"`
	Charges []View           `json:"charges"`
}

func openCharge(c chargeRow) OpenCharge {
	return OpenCharge{ID: c.ID, AssetID: c.AssetID, ChargedOn: period.FromTime(c.ChargedOn), Label: c.Label, AmountCents: money.Cents(c.AmountCents)}
}

// ChargeSuggestions lists the charges a bank debit may pay (RENT-SPEC-013,
// Q-125): a whole statement, then one charge of its amount. A credit, or a
// line already matched or ignored, has none.
func (s Service) ChargeSuggestions(ctx context.Context, p *auth.Principal, requestID string, lineID uuid.UUID) ([]Suggestion, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return nil, err
	}
	out := []Suggestion{}
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		l, err := s.Payments.LineInTx(ctx, lineID)
		if err != nil {
			return err
		}
		if l.Status != payment.LineUnmatched {
			return nil
		}
		ps, err := s.Params.Load(ctx)
		if err != nil {
			return err
		}
		r, err := ps.BankCharges(l.BookedOn)
		if err != nil {
			return err
		}
		rows, err := s.Repo.OpenCharges(ctx)
		if err != nil {
			return err
		}
		open := make([]OpenCharge, len(rows))
		for i, c := range rows {
			open[i] = openCharge(c)
		}
		out = SuggestCharges(l.BookedOn, l.AmountCents, open, r)
		return nil
	})
	return out, err
}

// MatchCharges records that a bank debit paid charges (RENT-SPEC-013: the
// charges ledger bank-backed): confirmed charges no other line paid, adding
// up to exactly the debit, each paid on the line's day and naming it; the
// line becomes matched.
func (s Service) MatchCharges(ctx context.Context, p *auth.Principal, requestID string, lineID uuid.UUID, in MatchChargesInput) (MatchChargesResult, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return MatchChargesResult{}, err
	}
	out := MatchChargesResult{Charges: []View{}}
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		l, err := s.Payments.LineInTx(ctx, lineID)
		if err != nil {
			return err
		}
		if l.Version != in.Version {
			return invariant.ErrStale
		}
		if l.Status != payment.LineUnmatched {
			return invariant.New(payment.TypeLineState, lineID.String())
		}
		if l.AmountCents >= 0 {
			return invariant.New(TypeLineNotDebit, lineID.String())
		}
		ids := map[uuid.UUID]bool{}
		for _, id := range in.ChargeIDs {
			ids[id] = true
		}
		if len(ids) == 0 || len(ids) != len(in.ChargeIDs) {
			return invariant.New(TypeChargesInvalid, "charge_ids")
		}
		rows, err := s.Repo.ChargesByID(ctx, in.ChargeIDs)
		if err != nil {
			return err
		}
		var sum money.Cents
		for _, c := range rows {
			if c.Status != "confirmed" || c.BankLineID != nil {
				return invariant.New(TypeChargesInvalid, c.ID.String())
			}
			sum += money.Cents(c.AmountCents)
		}
		if len(rows) != len(ids) || sum != -l.AmountCents {
			return invariant.New(TypeChargesInvalid, "total")
		}
		now := s.now()
		for _, c := range rows {
			before := chargeView(c)
			if err := s.Repo.PayByLine(ctx, c, lineID, l.BookedOn.Time(), now, p.Sub); err != nil {
				return err
			}
			paid := l.BookedOn.Time()
			c.PaidOn, c.BankLineID, c.Version, c.UpdatedAt, c.UpdatedBy = &paid, &lineID, c.Version+1, now, p.Sub
			v := chargeView(c)
			if err := audit.Record(ctx, "charge.bank_paid", audit.Entity{Type: "charge", ID: c.ID.String()}, before, v); err != nil {
				return err
			}
			out.Charges = append(out.Charges, v)
		}
		lv, err := s.Payments.MarkMatchedInTx(ctx, l, p.Sub)
		if err != nil {
			return err
		}
		out.Line = lv
		return audit.Record(ctx, "payment.bank_match", audit.Entity{Type: "bank_line", ID: lineID.String()}, nil, out)
	})
	return out, err
}
