package payment

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/audit"
	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/period"
	"github.com/ovander/tenure-api/internal/problem"
)

// Problem types of the payment module.
const (
	TypeOverMatched    = "payment/over-matched"
	TypeOverPaid       = "payment/document-over-paid"
	TypeNotARentCall   = "payment/not-a-live-rent-call"
	TypeLeaseDraft     = "payment/lease-not-active"
	TypeReceipted      = "payment/already-receipted"
	TypeBelowMatched   = "payment/amount-below-matches"
	TypeNothingToMatch = "payment/nothing-to-match"
)

func init() {
	problem.Register(map[string]problem.Message{
		TypeOverMatched:    problem.M(422, "Le total affecté dépasse le montant du paiement.", "The amount applied exceeds the payment."),
		TypeOverPaid:       problem.M(422, "Le montant affecté dépasse le reste dû de l’appel de loyer.", "The amount applied exceeds what remains due on the rent call."),
		TypeNotARentCall:   problem.M(422, "Un paiement ne s’affecte qu’à un appel de loyer en vigueur du même bail.", "A payment applies only to a current rent call of the same lease."),
		TypeLeaseDraft:     problem.M(409, "Un bail en brouillon ne reçoit pas de paiement.", "A draft lease takes no payment."),
		TypeReceipted:      problem.M(409, "Ce paiement figure sur une quittance ou un reçu ; il ne peut plus être modifié.", "This payment is on a receipt and can no longer change."),
		TypeBelowMatched:   problem.M(422, "Le montant ne peut pas descendre sous le total déjà affecté.", "The amount cannot go below what is already applied."),
		TypeNothingToMatch: problem.M(409, "Aucun appel de loyer n’attend de paiement sur ce bail.", "No rent call of this lease awaits payment."),
	})
}

// Method is a payment method.
type Method string

// Payment methods; caf is a housing allowance paid directly to the owner
// (RENT-SPEC-013).
var methods = []Method{"transfer", "direct_debit", "cheque", "cash", "card", "caf", "other"}

// Input is the body of POST and PUT /payments.
type Input struct {
	LeaseID     uuid.UUID   `json:"lease_id"`
	PaidOn      period.Date `json:"paid_on" swaggertype:"string" format:"date"`
	AmountCents int64       `json:"amount_cents"`
	Method      Method      `json:"method" enums:"transfer,direct_debit,cheque,cash,card,caf,other"`
	Reference   string      `json:"reference"`
	// AutoMatch applies the payment to the lease's oldest unpaid rent calls
	// (POST only; default false).
	AutoMatch bool `json:"auto_match"`
}

// MatchView is the part of a payment applied to a rent call.
type MatchView struct {
	ID                uuid.UUID `json:"id"`
	BillingDocumentID uuid.UUID `json:"billing_document_id"`
	AmountCents       int64     `json:"amount_cents"`
	CreatedAt         time.Time `json:"created_at"`
}

// View is a payment as the API returns it.
type View struct {
	ID             uuid.UUID   `json:"id"`
	LeaseID        uuid.UUID   `json:"lease_id"`
	PaidOn         period.Date `json:"paid_on" swaggertype:"string" format:"date"`
	AmountCents    int64       `json:"amount_cents"`
	Method         Method      `json:"method"`
	Reference      string      `json:"reference"`
	MatchedCents   int64       `json:"matched_cents"`
	UnmatchedCents int64       `json:"unmatched_cents"`
	Matches        []MatchView `json:"matches"`
	Version        int         `json:"version"`
	CreatedAt      time.Time   `json:"created_at"`
	UpdatedAt      time.Time   `json:"updated_at"`
}

// MatchInput is the body of POST /payments/{id}/matches: explicit matches,
// or none with auto set to apply the remainder to the oldest unpaid rent calls.
type MatchInput struct {
	Matches []MatchLine `json:"matches"`
	Auto    bool        `json:"auto"`
}

// MatchLine applies an amount of the payment to one rent call.
type MatchLine struct {
	BillingDocumentID uuid.UUID `json:"billing_document_id"`
	AmountCents       int64     `json:"amount_cents"`
}

// Service implements the payment use cases with the API role.
type Service struct {
	DB    *db.DB
	Repo  Repository
	Now   func() time.Time
	NewID func() uuid.UUID
}

func (s Service) now() time.Time { return s.Now().UTC().Truncate(time.Microsecond) }

func paris() *time.Location {
	loc, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		return time.UTC
	}
	return loc
}

func scoped(p *auth.Principal, requestID string) (db.Scope, error) {
	if p.Active == nil {
		return db.Scope{}, invariant.ErrForbidden
	}
	return p.DBScope(requestID), nil
}

func view(p paymentRow, ms []MatchRow) View {
	v := View{ID: p.ID, LeaseID: p.LeaseID, PaidOn: period.FromTime(p.PaidOn), AmountCents: p.AmountCents, Method: Method(p.Method),
		Reference: p.Reference, Version: p.Version, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt, Matches: []MatchView{}}
	for _, m := range ms {
		v.MatchedCents += m.AmountCents
		v.Matches = append(v.Matches, MatchView{ID: m.ID, BillingDocumentID: m.BillingDocumentID, AmountCents: m.AmountCents, CreatedAt: m.CreatedAt})
	}
	v.UnmatchedCents = v.AmountCents - v.MatchedCents
	return v
}

func (s Service) validate(in *Input) invariant.Fields {
	var f invariant.Fields
	in.Reference = strings.TrimSpace(in.Reference)
	if in.LeaseID == uuid.Nil {
		f.Add("lease_id", "Le bail est obligatoire.")
	}
	if in.PaidOn.IsZero() {
		f.Add("paid_on", "La date de paiement est obligatoire.")
	} else if in.PaidOn.After(period.FromTime(s.Now().In(paris()))) {
		f.Add("paid_on", "La date de paiement ne peut pas être dans le futur.")
	}
	if in.AmountCents <= 0 {
		f.Add("amount_cents", "Le montant doit être positif.")
	}
	if !slices.Contains(methods, in.Method) {
		f.Add("method", "Mode de paiement inconnu.")
	}
	return f
}

// get loads a payment with its matches inside the transaction.
func (s Service) get(ctx context.Context, id uuid.UUID) (paymentRow, View, error) {
	p, err := s.Repo.Get(ctx, id)
	if err != nil {
		return paymentRow{}, View{}, err
	}
	ms, err := s.Repo.Matches(ctx, []uuid.UUID{id})
	return p, view(p, ms[id]), err
}

// Create records a payment (OD-02: manual entry in phase 1), applied at once
// to the oldest unpaid rent calls when AutoMatch is set.
func (s Service) Create(ctx context.Context, pr *auth.Principal, requestID string, in Input) (View, error) {
	sc, err := scoped(pr, requestID)
	if err != nil {
		return View{}, err
	}
	if f := s.validate(&in); len(f) > 0 {
		return View{}, f
	}
	var out View
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		state, err := s.Repo.LeaseState(ctx, in.LeaseID)
		if err != nil {
			if errors.Is(err, invariant.ErrNotFound) {
				var f invariant.Fields
				f.Add("lease_id", "Ce bail n’existe pas dans ce portefeuille.")
				return f
			}
			return err
		}
		if state == "draft" {
			return invariant.New(TypeLeaseDraft, in.LeaseID.String())
		}
		now := s.now()
		p := paymentRow{ID: s.NewID(), OwnerID: sc.OwnerID, LeaseID: in.LeaseID, PaidOn: in.PaidOn.Time(), AmountCents: in.AmountCents,
			Method: string(in.Method), Reference: in.Reference, Version: 1, CreatedAt: now, CreatedBy: pr.Sub, UpdatedAt: now, UpdatedBy: pr.Sub}
		if err := s.Repo.Insert(ctx, p); err != nil {
			return err
		}
		if err := audit.Record(ctx, "payment.create", audit.Entity{Type: "payment", ID: p.ID.String()}, nil, view(p, nil)); err != nil {
			return err
		}
		if in.AutoMatch {
			if _, err := s.match(ctx, pr.Sub, p, nil, MatchInput{Auto: true}, true); err != nil {
				return err
			}
		}
		_, out, err = s.get(ctx, p.ID)
		return err
	})
	return out, err
}

// Update changes a payment when version matches and no receipt covers it; the
// amount may not drop below what is already applied.
func (s Service) Update(ctx context.Context, pr *auth.Principal, requestID string, id uuid.UUID, version int, in Input) (View, error) {
	sc, err := scoped(pr, requestID)
	if err != nil {
		return View{}, err
	}
	if f := s.validate(&in); len(f) > 0 {
		return View{}, f
	}
	var out View
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		cur, before, err := s.get(ctx, id)
		if err != nil {
			return err
		}
		if cur.Version != version {
			return invariant.ErrStale
		}
		if in.LeaseID != cur.LeaseID {
			var f invariant.Fields
			f.Add("lease_id", "Un paiement ne change pas de bail.")
			return f
		}
		if err := s.Repo.LockLease(ctx, cur.LeaseID); err != nil {
			return err
		}
		receipted, err := s.Repo.Receipted(ctx, id)
		if err != nil {
			return err
		}
		if receipted && (in.AmountCents != cur.AmountCents || !in.PaidOn.Time().Equal(cur.PaidOn)) {
			return invariant.New(TypeReceipted, id.String())
		}
		if in.AmountCents < before.MatchedCents {
			return invariant.New(TypeBelowMatched, fmt.Sprintf("%d < %d", in.AmountCents, before.MatchedCents))
		}
		cur.PaidOn, cur.AmountCents, cur.Method, cur.Reference = in.PaidOn.Time(), in.AmountCents, string(in.Method), in.Reference
		cur.UpdatedAt, cur.UpdatedBy = s.now(), pr.Sub
		if err := s.Repo.Update(ctx, cur, version); err != nil {
			return err
		}
		cur.Version = version + 1
		_, out, err = s.get(ctx, id)
		if err != nil {
			return err
		}
		return audit.Record(ctx, "payment.update", audit.Entity{Type: "payment", ID: id.String()}, before, out)
	})
	return out, err
}

// match applies the payment p (locked by the caller's lease lock): explicit
// matches, each to a live rent call of the same lease, never more than its
// outstanding amount nor, in total, more than the payment's unapplied part; or,
// with Auto, the unapplied part to the oldest unpaid rent calls first.
func (s Service) match(ctx context.Context, sub string, p paymentRow, existing []MatchRow, in MatchInput, quietEmpty bool) ([]MatchRow, error) {
	if err := s.Repo.LockLease(ctx, p.LeaseID); err != nil {
		return nil, err
	}
	var applied int64
	for _, m := range existing {
		applied += m.AmountCents
	}
	free := p.AmountCents - applied
	var rows []MatchRow
	now := s.now()
	add := func(doc uuid.UUID, amount int64) {
		rows = append(rows, MatchRow{ID: s.NewID(), OwnerID: p.OwnerID, PaymentID: p.ID, BillingDocumentID: doc, AmountCents: amount, CreatedAt: now, CreatedBy: sub})
	}
	if in.Auto {
		dues, err := s.Repo.Dues(ctx, p.LeaseID)
		if err != nil {
			return nil, err
		}
		for _, d := range dues {
			if free == 0 {
				break
			}
			if o := d.OutstandingCents(); o > 0 {
				a := min(o, free)
				add(d.BillingDocumentID, a)
				free -= a
			}
		}
		if len(rows) == 0 {
			if quietEmpty {
				return nil, nil
			}
			return nil, invariant.New(TypeNothingToMatch, p.LeaseID.String())
		}
	} else {
		var total int64
		seen := map[uuid.UUID]int64{}
		for _, m := range in.Matches {
			if m.AmountCents <= 0 {
				var f invariant.Fields
				f.Add("matches", "Chaque montant affecté doit être positif.")
				return nil, f
			}
			d, err := s.Repo.Due(ctx, p.LeaseID, m.BillingDocumentID)
			if errors.Is(err, invariant.ErrNotFound) {
				return nil, invariant.New(TypeNotARentCall, m.BillingDocumentID.String())
			}
			if err != nil {
				return nil, err
			}
			seen[d.BillingDocumentID] += m.AmountCents
			if seen[d.BillingDocumentID] > d.OutstandingCents() {
				return nil, invariant.New(TypeOverPaid, d.Number)
			}
			total += m.AmountCents
			add(d.BillingDocumentID, m.AmountCents)
		}
		if total > free {
			return nil, invariant.New(TypeOverMatched, fmt.Sprintf("%d > %d", total, free))
		}
		if len(rows) == 0 {
			var f invariant.Fields
			f.Add("matches", "Indiquez au moins un appel de loyer, ou auto.")
			return nil, f
		}
	}
	if err := s.Repo.InsertMatches(ctx, rows); err != nil {
		return nil, err
	}
	mv := make([]MatchView, len(rows))
	for i, m := range rows {
		mv[i] = MatchView{ID: m.ID, BillingDocumentID: m.BillingDocumentID, AmountCents: m.AmountCents, CreatedAt: m.CreatedAt}
	}
	return rows, audit.Record(ctx, "payment.match", audit.Entity{Type: "payment", ID: p.ID.String()}, nil, map[string]any{"matches": mv})
}

// Match applies a payment to rent calls (POST /payments/{id}/matches).
func (s Service) Match(ctx context.Context, pr *auth.Principal, requestID string, id uuid.UUID, in MatchInput) (View, error) {
	sc, err := scoped(pr, requestID)
	if err != nil {
		return View{}, err
	}
	var out View
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		p, err := s.Repo.Get(ctx, id)
		if err != nil {
			return err
		}
		if err := s.Repo.LockLease(ctx, p.LeaseID); err != nil {
			return err
		}
		ms, err := s.Repo.Matches(ctx, []uuid.UUID{id})
		if err != nil {
			return err
		}
		if _, err := s.match(ctx, pr.Sub, p, ms[id], in, false); err != nil {
			return err
		}
		_, out, err = s.get(ctx, id)
		return err
	})
	return out, err
}

// List returns a page of the scope's payments.
func (s Service) List(ctx context.Context, pr *auth.Principal, requestID string, f Filter, offset, limit int) ([]View, int, error) {
	sc, err := scoped(pr, requestID)
	if err != nil {
		return nil, 0, err
	}
	var out []View
	var total int
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		rows, n, err := s.Repo.List(ctx, f, offset, limit)
		if err != nil {
			return err
		}
		ids := make([]uuid.UUID, len(rows))
		for i, r := range rows {
			ids[i] = r.ID
		}
		ms, err := s.Repo.Matches(ctx, ids)
		for _, r := range rows {
			out = append(out, view(r, ms[r.ID]))
		}
		total = n
		return err
	})
	return out, total, err
}

// Get returns one payment of the scope.
func (s Service) Get(ctx context.Context, pr *auth.Principal, requestID string, id uuid.UUID) (View, error) {
	sc, err := scoped(pr, requestID)
	if err != nil {
		return View{}, err
	}
	var out View
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		_, v, err := s.get(ctx, id)
		out = v
		return err
	})
	return out, err
}
