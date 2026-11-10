package payment

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/audit"
	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/period"
	"github.com/ovander/tenure-api/internal/problem"
)

// Problem types of bank matching.
const (
	TypeAllocationInvalid = "payment/bank-allocation-invalid"
	TypeLineNotCredit     = "payment/bank-line-not-credit"
)

func init() {
	problem.Register(map[string]problem.Message{
		TypeAllocationInvalid: problem.M(422, "La répartition doit couvrir exactement le montant de la ligne bancaire.", "The allocation must cover exactly the bank line's amount."),
		TypeLineNotCredit:     problem.M(422, "Seul un crédit se rapproche d’un loyer.", "Only a credit matches a rent."),
	})
}

// Allocation is the part of a credit line recorded as a payment of a lease:
// a tenant's rent, a housing allowance, a third party paying for a tenant.
type Allocation struct {
	LeaseID     uuid.UUID `json:"lease_id"`
	AmountCents int64     `json:"amount_cents"`
	Method      Method    `json:"method" enums:"transfer,direct_debit,cheque,cash,card,caf,other"`
}

// MatchLineInput is the body of POST /bank-lines/{id}/match: the line split
// across one or more leases, adding up to its amount.
type MatchLineInput struct {
	Allocations []Allocation `json:"allocations"`
	Version     int          `json:"version"`
}

// MatchLineResult is a bank line matched: the line and the payments it
// recorded, each applied to the lease's oldest unpaid rent calls.
type MatchLineResult struct {
	Line     LineView `json:"line"`
	Payments []View   `json:"payments"`
}

// AutoMatchInput is the body of POST /bank-lines/auto.
type AutoMatchInput struct {
	AccountID *uuid.UUID `json:"account_id,omitempty"`
	DryRun    bool       `json:"dry_run"`
}

// AutoMatched is one line matched, or to be matched, without asking.
type AutoMatched struct {
	LineID     uuid.UUID  `json:"line_id"`
	Suggestion Suggestion `json:"suggestion"`
}

// AutoMatchResult lists the lines matched and how many stay for the owner.
type AutoMatchResult struct {
	Matched []AutoMatched `json:"matched"`
	Left    int           `json:"left"`
}

func openCalls(rows []openCallRow) []OpenCall {
	out := make([]OpenCall, len(rows))
	for i, r := range rows {
		out[i] = OpenCall{BillingDocumentID: r.ID, LeaseID: r.LeaseID, Number: r.Number, Period: r.Period, DueOn: period.FromTime(r.DueOn),
			OutstandingCents: money.Cents(r.TotalCents - r.PaidCents), Tenants: []string{}}
		for _, n := range strings.Split(r.Tenants, "\n") {
			if n = strings.TrimSpace(n); n != "" {
				out[i].Tenants = append(out[i].Tenants, n)
			}
		}
	}
	return out
}

func lineOf(r lineRow) BankLine {
	return BankLine{BookedOn: period.FromTime(r.BookedOn), AmountCents: money.Cents(r.AmountCents), Label: r.Label, Reference: r.Reference, Key: r.LineKey}
}

func (s Service) matchingRules(ctx context.Context) (params.BankMatching, params.Bank, period.Date, error) {
	today := s.today()
	ps, err := s.Params.Load(ctx)
	if err != nil {
		return params.BankMatching{}, params.Bank{}, today, err
	}
	m, err := ps.BankMatching(today)
	if err != nil {
		return params.BankMatching{}, params.Bank{}, today, err
	}
	b, err := ps.Bank(today)
	return m, b, today, err
}

// Suggestions lists the rent calls a credit bank line may pay (RENT-SPEC-013,
// Q-124), strongest rule first. A debit, or a line already matched or
// ignored, has none.
func (s Service) Suggestions(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID) ([]Suggestion, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return nil, err
	}
	out := []Suggestion{}
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		r, err := s.Repo.Line(ctx, id)
		if err != nil {
			return err
		}
		if r.Status != LineUnmatched {
			return nil
		}
		m, _, _, err := s.matchingRules(ctx)
		if err != nil {
			return err
		}
		calls, err := s.Repo.OpenCalls(ctx)
		if err != nil {
			return err
		}
		out = Suggest(lineOf(r), openCalls(calls), m)
		return nil
	})
	return out, err
}

// matchInTx records an unmatched credit line as payments of the leases it is
// split across, each paid on the line's day with its label as reference and
// applied to the lease's oldest unpaid rent calls, and marks it matched.
func (s Service) matchInTx(ctx context.Context, owner uuid.UUID, sub string, r lineRow, al []Allocation, today period.Date, alert int) (MatchLineResult, error) {
	out := MatchLineResult{Payments: []View{}}
	if r.Status != LineUnmatched {
		return out, invariant.New(TypeLineState, r.ID.String())
	}
	if r.AmountCents <= 0 {
		return out, invariant.New(TypeLineNotCredit, r.ID.String())
	}
	var sum int64
	for _, a := range al {
		if a.AmountCents <= 0 || !slicesContains(methods, a.Method) {
			return out, invariant.New(TypeAllocationInvalid, a.LeaseID.String())
		}
		sum += a.AmountCents
	}
	if len(al) == 0 || sum != r.AmountCents {
		return out, invariant.New(TypeAllocationInvalid, "total")
	}
	before := s.lineView(r, today, alert)
	ref := strings.TrimSpace(r.Label + " " + r.Reference)
	if len(ref) > 200 {
		ref = ref[:200]
	}
	for _, a := range al {
		pr, err := s.createInTx(ctx, owner, sub, Input{LeaseID: a.LeaseID, PaidOn: period.FromTime(r.BookedOn), AmountCents: a.AmountCents,
			Method: a.Method, Reference: ref, AutoMatch: true}, &r.ID)
		if err != nil {
			return out, err
		}
		_, v, err := s.get(ctx, pr.ID)
		if err != nil {
			return out, err
		}
		out.Payments = append(out.Payments, v)
	}
	if err := s.Repo.SetLineStatus(ctx, r.ID, r.Version, LineMatched, r.Note, s.now(), sub); err != nil {
		return out, err
	}
	r.Status, r.Version = LineMatched, r.Version+1
	out.Line = s.lineView(r, today, alert)
	return out, audit.Record(ctx, "payment.bank_match", audit.Entity{Type: "bank_line", ID: r.ID.String()}, before, out)
}

func slicesContains(ms []Method, m Method) bool {
	for _, x := range ms {
		if x == m {
			return true
		}
	}
	return false
}

// MatchLine records a credit bank line as the payments of the leases the
// owner splits it across (RENT-SPEC-013: a third party may pay, a line may
// pay several leases), adding up to its amount, and marks it matched.
func (s Service) MatchLine(ctx context.Context, p *auth.Principal, requestID string, id uuid.UUID, in MatchLineInput) (MatchLineResult, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return MatchLineResult{}, err
	}
	var out MatchLineResult
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		r, err := s.Repo.Line(ctx, id)
		if err != nil {
			return err
		}
		if r.Version != in.Version {
			return invariant.ErrStale
		}
		_, b, today, err := s.matchingRules(ctx)
		if err != nil {
			return err
		}
		out, err = s.matchInTx(ctx, sc.OwnerID, p.Sub, r, in.Allocations, today, b.UnmatchedAlertDays)
		return err
	})
	return out, err
}

// AutoMatch matches, without asking, every unmatched credit line that has a
// sure suggestion (payment.Sure: one rent call recognised by its number, the
// tenant's name and amount, or a housing allowance naming the tenant), each
// against the open calls as the earlier lines left them, oldest line first;
// every other line stays for the owner. A dry run only lists them.
func (s Service) AutoMatch(ctx context.Context, p *auth.Principal, requestID string, in AutoMatchInput) (AutoMatchResult, error) {
	sc, err := scoped(p, requestID)
	if err != nil {
		return AutoMatchResult{}, err
	}
	out := AutoMatchResult{Matched: []AutoMatched{}}
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		m, b, today, err := s.matchingRules(ctx)
		if err != nil {
			return err
		}
		lines, err := s.Repo.UnmatchedCredits(ctx, in.AccountID)
		if err != nil {
			return err
		}
		rows, err := s.Repo.OpenCalls(ctx)
		if err != nil {
			return err
		}
		calls := openCalls(rows)
		for _, r := range lines {
			sg, ok := Sure(Suggest(lineOf(r), calls, m))
			if !ok {
				out.Left++
				continue
			}
			out.Matched = append(out.Matched, AutoMatched{LineID: r.ID, Suggestion: sg})
			// What this line pays leaves the next lines less to match.
			left := sg.AmountCents
			for i := range calls {
				if calls[i].LeaseID == sg.LeaseID && left > 0 {
					take := min(left, calls[i].OutstandingCents)
					calls[i].OutstandingCents -= take
					left -= take
				}
			}
			if in.DryRun {
				continue
			}
			if _, err := s.matchInTx(ctx, sc.OwnerID, p.Sub, r, []Allocation{{LeaseID: sg.LeaseID, AmountCents: r.AmountCents, Method: sg.Method}}, today, b.UnmatchedAlertDays); err != nil {
				return err
			}
		}
		if in.DryRun || len(out.Matched) == 0 {
			return nil
		}
		return audit.Record(ctx, "payment.bank_auto_match", audit.Entity{Type: "owner", ID: sc.OwnerID.String()}, nil, out)
	})
	return out, err
}
