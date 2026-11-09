package billing

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/audit"
	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/document"
	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/lease"
	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/period"
)

// RecoveryTemplate is the version of the recovery letters (templates/recovery).
const RecoveryTemplate = "v1"

// RecoveryLetterInput is the body of POST /leases/{id}/recovery/letter: the
// formal notice, or the information of one surety, and dry_run to preview
// the debt it states.
type RecoveryLetterInput struct {
	Kind        string     `json:"kind" enums:"formal_notice,surety_notice"`
	GuaranteeID *uuid.UUID `json:"guarantee_id,omitempty"`
	DryRun      bool       `json:"dry_run"`
}

// RecoveryLetter is a recovery letter prepared, or previewed.
type RecoveryLetter struct {
	Kind             string        `json:"kind" enums:"formal_notice,surety_notice"`
	Items            []ArrearsItem `json:"items"`
	OutstandingCents money.Cents   `json:"outstanding_cents"`
	DocumentID       *uuid.UUID    `json:"document_id,omitempty"`
	Number           string        `json:"number,omitempty"`
}

// RecoveryStepInput is the body of POST /leases/{id}/recovery/steps: a step
// done on a day. The formal notice and the surety's information name their
// letter (prepared or uploaded); a commandement or a court referral may name
// the deed uploaded.
type RecoveryStepInput struct {
	Kind        string       `json:"kind" enums:"formal_notice,surety_notice,insurance_claim,commandement,surety_commandement,court"`
	DoneOn      period.Date  `json:"done_on" swaggertype:"string" format:"date"`
	GuaranteeID *uuid.UUID   `json:"guarantee_id,omitempty"`
	DocumentID  *uuid.UUID   `json:"document_id,omitempty"`
	Delivery    string       `json:"delivery,omitempty" enums:"registered_letter,bailiff,hand_delivery,email"`
	TrackingRef string       `json:"tracking_ref,omitempty"`
	DeadlineOn  *period.Date `json:"deadline_on,omitempty" swaggertype:"string" format:"date"`
	Note        string       `json:"note,omitempty"`
}

// RecoveryResult is a lease's recovery with its parties' sureties.
type RecoveryResult struct {
	RecoveryView
	Sureties []lease.Surety `json:"sureties"`
	Insured  bool           `json:"insured"`
	// Plan is the lease's latest payment plan and where it stands.
	Plan *PlanState `json:"plan,omitempty"`
}

func (st recoveryState) result() RecoveryResult {
	out := RecoveryResult{RecoveryView: st.view, Sureties: st.parties.Sureties, Insured: st.parties.Insured, Plan: st.plan}
	if out.Sureties == nil {
		out.Sureties = []lease.Surety{}
	}
	return out
}

func planOf(r planRow) Plan {
	p := Plan{ID: r.ID, AgreedOn: period.FromTime(r.AgreedOn), DebtCents: money.Cents(r.DebtCents), DocumentID: r.DocumentID, Calls: []uuid.UUID{}, Instalments: []Instalment{}, Version: r.Version}
	_ = json.Unmarshal(r.Calls, &p.Calls)
	_ = json.Unmarshal(r.Instalments, &p.Instalments)
	if r.CancelledOn != nil {
		d := period.FromTime(*r.CancelledOn)
		p.CancelledOn = &d
	}
	return p
}

func stepOf(r recoveryRow) RecoveryStep {
	s := RecoveryStep{ID: r.ID, Kind: r.Kind, DoneOn: period.FromTime(r.DoneOn), OutstandingCents: money.Cents(r.OutstandingCents),
		GuaranteeID: r.GuaranteeID, DocumentID: r.DocumentID, TrackingRef: r.TrackingRef, Note: r.Note}
	if r.Delivery != nil {
		s.Delivery = *r.Delivery
	}
	if r.DeadlineOn != nil {
		d := period.FromTime(*r.DeadlineOn)
		s.DeadlineOn = &d
	}
	return s
}

type recoveryState struct {
	parties lease.RecoveryParties
	ps      params.Set
	r       params.Recovery
	today   period.Date
	view    RecoveryView
	plan    *PlanState
	planRow *planRow
}

// recovery reads a lease's recovery inside the caller's scoped transaction.
func (s Service) recovery(ctx context.Context, leaseID uuid.UUID) (recoveryState, error) {
	sc, _ := db.ScopeFrom(ctx)
	st := recoveryState{today: period.FromTime(s.Now().In(parisTZ()))}
	var err error
	if st.parties, err = s.Lease.RecoveryPartiesInTx(ctx, sc, leaseID, st.today); err != nil {
		return st, err
	}
	if st.ps, err = s.Params.Load(ctx); err != nil {
		return st, err
	}
	if st.r, err = st.ps.Recovery(st.today); err != nil {
		return st, err
	}
	days, err := st.ps.Int64(params.KeyReceiptReminderDays, st.today)
	if err != nil {
		return st, err
	}
	calls, err := s.calls(ctx, leaseID)
	if err != nil {
		return st, err
	}
	rows, err := s.Repo.RecoverySteps(ctx, leaseID)
	if err != nil {
		return st, err
	}
	steps := make([]RecoveryStep, len(rows))
	for i, r := range rows {
		steps[i] = stepOf(r)
	}
	f := RecoveryFacts{Insured: st.parties.Insured, TermStartOn: st.parties.TermStartOn}
	for _, g := range st.parties.Sureties {
		f.Sureties = append(f.Sureties, g.GuaranteeID)
	}
	arr := Arrears(leaseID, calls, st.today, int(days))
	plans, err := s.Repo.Plans(ctx, leaseID)
	if err != nil {
		return st, err
	}
	if len(plans) > 0 {
		pr, err := st.ps.RecoveryPlan(st.today)
		if err != nil {
			return st, err
		}
		ps := PlanStatus(planOf(plans[0]), arr, st.today, pr)
		st.plan, st.planRow = &ps, &plans[0]
		f.PlanRespected = ps.Status == PlanRespected
	}
	st.view, err = Recovery(arr, steps, f, st.today, st.r)
	return st, err
}

// RecoveryFor returns where the recovery of a lease's unpaid rent stands
// (RENT-SPEC-011, Q-121): the arrears, the steps done, the next ones with the
// day each falls due, and the clause résolutoire's day once a commandement
// is recorded.
func (s Service) RecoveryFor(ctx context.Context, p *auth.Principal, requestID string, leaseID uuid.UUID) (RecoveryResult, error) {
	sc, err := ownerScope(p, requestID)
	if err != nil {
		return RecoveryResult{}, err
	}
	var out RecoveryResult
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		st, err := s.recovery(ctx, leaseID)
		if err != nil {
			return err
		}
		out = st.result()
		return nil
	})
	return out, err
}

// recoveryLetterData is what templates/recovery renders.
type recoveryLetterData struct {
	Kind             string
	Number           string
	IssuedOn         period.Date
	Owner            document.Party
	Tenants          []string
	Premises         []string
	ContractNumber   string
	StartOn          period.Date
	Surety           *lease.Surety
	Items            []ArrearsItem
	OutstandingCents money.Cents
}

// RecoveryLetter prepares the formal notice (MD-<asset>-<YYYY>-<seq>) or the
// information of a natural-person surety (IC-…) of a lease's overdue rent
// (RENT-SPEC-011, Q-121): the calls still owed, their total, and what the
// letter asks or tells. Nothing overdue is refused. A dry run only lists the
// debt.
func (s Service) RecoveryLetter(ctx context.Context, p *auth.Principal, requestID string, leaseID uuid.UUID, in RecoveryLetterInput) (RecoveryLetter, error) {
	sc, err := ownerScope(p, requestID)
	if err != nil {
		return RecoveryLetter{}, err
	}
	series := map[string]string{StepFormalNotice: document.SeriesFormalNotice, StepSuretyNotice: document.SeriesSuretyNotice}[in.Kind]
	if series == "" {
		return RecoveryLetter{}, invariant.New(TypeRecoveryInvalid, "kind")
	}
	if (in.Kind == StepSuretyNotice) != (in.GuaranteeID != nil) {
		return RecoveryLetter{}, invariant.New(TypeRecoveryInvalid, "guarantee_id")
	}
	out := RecoveryLetter{Kind: in.Kind, Items: []ArrearsItem{}}
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		st, err := s.recovery(ctx, leaseID)
		if err != nil {
			return err
		}
		var surety *lease.Surety
		if in.GuaranteeID != nil {
			for _, g := range st.parties.Sureties {
				if g.GuaranteeID == *in.GuaranteeID {
					surety = &g
				}
			}
			if surety == nil {
				return invariant.New(TypeRecoveryInvalid, "guarantee_id")
			}
		}
		for _, it := range st.view.Arrears.Items {
			if it.Overdue {
				out.Items = append(out.Items, it)
				out.OutstandingCents += it.OutstandingCents
			}
		}
		if len(out.Items) == 0 {
			return invariant.New(TypeRecoveryNotDue, leaseID.String())
		}
		if in.DryRun {
			return nil
		}
		data := recoveryLetterData{Kind: in.Kind, Owner: st.parties.Owner, Tenants: st.parties.Tenants, Premises: st.parties.Premises,
			ContractNumber: st.parties.ContractNumber, StartOn: st.parties.StartOn, Surety: surety, Items: out.Items, OutstandingCents: out.OutstandingCents}
		snap := map[string]any{"template": "recovery/" + RecoveryTemplate, "kind": in.Kind, "items": out.Items, "outstanding_cents": out.OutstandingCents, "surety": surety}
		lid := leaseID
		iss, err := s.Issuer.Issue(ctx, document.Request{Class: document.ClassRecovery, TemplateVersion: RecoveryTemplate, Series: series,
			AssetID: st.parties.AssetID, LeaseID: &lid, Snapshot: snap,
			Data: func(number string, issued period.Date) any {
				data.Number, data.IssuedOn = number, issued
				return data
			}}, st.ps)
		if err != nil {
			return err
		}
		out.DocumentID, out.Number = &iss.DocumentID, iss.Number
		return audit.Record(ctx, "billing.recovery_letter", audit.Entity{Type: "lease", ID: leaseID.String()}, nil, out)
	})
	return out, err
}

var deliveries = map[string]bool{"registered_letter": true, "bailiff": true, "hand_delivery": true, "email": true}

// RecordRecoveryStep records a recovery step done on a day (RENT-SPEC-011,
// Q-121), immutable once recorded: the formal notice and the surety's
// information on their letter (MD or IC prepared for the lease, or one
// uploaded), a surety step naming a surety of the lease, the insurer's
// claim with its deadline if known. A step needs something overdue on its
// day, which tenure computes and records.
func (s Service) RecordRecoveryStep(ctx context.Context, p *auth.Principal, requestID string, leaseID uuid.UUID, in RecoveryStepInput) (RecoveryResult, error) {
	sc, err := ownerScope(p, requestID)
	if err != nil {
		return RecoveryResult{}, err
	}
	var fs invariant.Fields
	known := false
	for _, k := range Steps {
		known = known || k == in.Kind
	}
	if !known {
		fs.Add("kind", "Étape inconnue.")
	}
	if in.DoneOn.IsZero() {
		fs.Add("done_on", "La date est obligatoire.")
	}
	if in.Delivery != "" && !deliveries[in.Delivery] {
		fs.Add("delivery", "Attendu : registered_letter, bailiff, hand_delivery ou email.")
	}
	in.TrackingRef, in.Note = strings.TrimSpace(in.TrackingRef), strings.TrimSpace(in.Note)
	if len(in.TrackingRef) > 100 {
		fs.Add("tracking_ref", "Au plus 100 caractères.")
	}
	if len(in.Note) > 500 {
		fs.Add("note", "Au plus 500 caractères.")
	}
	surety := in.Kind == StepSuretyNotice || in.Kind == StepSuretyCommandement
	if surety != (in.GuaranteeID != nil) {
		fs.Add("guarantee_id", "Une étape de la caution nomme la caution, et elle seule.")
	}
	if (in.Kind == StepFormalNotice || in.Kind == StepSuretyNotice) && in.DocumentID == nil {
		fs.Add("document_id", "La lettre est obligatoire.")
	}
	if in.DeadlineOn != nil && in.Kind != StepInsuranceClaim {
		fs.Add("deadline_on", "Seule la déclaration à l’assureur a une échéance.")
	}
	if err := fs.Err(); err != nil {
		return RecoveryResult{}, err
	}
	var out RecoveryResult
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		st, err := s.recovery(ctx, leaseID)
		if err != nil {
			return err
		}
		if in.DoneOn.After(st.today) {
			return invariant.New(TypeRecoveryInvalid, "done_on after today")
		}
		if st.view.Since == nil || in.DoneOn.Before(*st.view.Since) {
			return invariant.New(TypeRecoveryNotDue, leaseID.String())
		}
		if in.GuaranteeID != nil {
			found := false
			for _, g := range st.parties.Sureties {
				found = found || g.GuaranteeID == *in.GuaranteeID
			}
			if !found {
				return invariant.New(TypeRecoveryInvalid, "guarantee_id")
			}
		}
		if in.DocumentID != nil {
			series, ok, err := s.Repo.RecoveryDocument(ctx, leaseID, *in.DocumentID)
			if err != nil {
				return err
			}
			want := map[string]string{StepFormalNotice: document.SeriesFormalNotice, StepSuretyNotice: document.SeriesSuretyNotice}[in.Kind]
			if !ok || (series != "upload" && series != want) {
				return invariant.New(TypeRecoveryLetterKind, in.DocumentID.String())
			}
		}
		row := recoveryRow{ID: s.NewID(), OwnerID: sc.OwnerID, LeaseID: leaseID, Kind: in.Kind, DoneOn: in.DoneOn.Time(),
			OutstandingCents: int64(st.view.Arrears.OutstandingCents), GuaranteeID: in.GuaranteeID, DocumentID: in.DocumentID,
			TrackingRef: in.TrackingRef, Note: in.Note, CreatedAt: s.now(), CreatedBy: p.Sub}
		if in.Delivery != "" {
			d := in.Delivery
			row.Delivery = &d
		}
		if in.DeadlineOn != nil {
			t := in.DeadlineOn.Time()
			row.DeadlineOn = &t
		}
		if err := s.Repo.InsertRecoveryStep(ctx, row); err != nil {
			return err
		}
		after, err := s.recovery(ctx, leaseID)
		if err != nil {
			return err
		}
		out = after.result()
		return audit.Record(ctx, "billing.recovery_step", audit.Entity{Type: "lease", ID: leaseID.String()}, nil, stepOf(row))
	})
	return out, err
}

// PlanInput is the body of POST /leases/{id}/recovery/plans: the day the
// plan was agreed, its instalments and the signed agreement if uploaded.
type PlanInput struct {
	AgreedOn    period.Date  `json:"agreed_on" swaggertype:"string" format:"date"`
	Instalments []Instalment `json:"instalments"`
	DocumentID  *uuid.UUID   `json:"document_id,omitempty"`
	DryRun      bool         `json:"dry_run"`
}

// CancelPlanInput is the body of POST /leases/{id}/recovery/plans/{pid}/cancel.
type CancelPlanInput struct {
	CancelledOn period.Date `json:"cancelled_on" swaggertype:"string" format:"date"`
	Version     int         `json:"version"`
}

// RecordPlan records a payment plan of a lease's overdue rent (RENT-SPEC-011,
// Q-122): it covers every call overdue on the day and their whole debt; while
// respected it suspends the recovery. A lease has one plan in force at a
// time: a new one needs the latest completed, broken or cancelled. A dry run
// only checks it.
func (s Service) RecordPlan(ctx context.Context, p *auth.Principal, requestID string, leaseID uuid.UUID, in PlanInput) (RecoveryResult, error) {
	sc, err := ownerScope(p, requestID)
	if err != nil {
		return RecoveryResult{}, err
	}
	var out RecoveryResult
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		st, err := s.recovery(ctx, leaseID)
		if err != nil {
			return err
		}
		if st.plan != nil && st.plan.Status == PlanRespected {
			return invariant.New(TypePlanExists, st.plan.ID.String())
		}
		pr, err := st.ps.RecoveryPlan(st.today)
		if err != nil {
			return err
		}
		plan, err := NewPlan(st.view.Arrears, in.AgreedOn, st.today, in.Instalments, pr)
		if err != nil {
			return err
		}
		if in.DocumentID != nil {
			series, ok, err := s.Repo.RecoveryDocument(ctx, leaseID, *in.DocumentID)
			if err != nil {
				return err
			}
			if !ok || series != "upload" {
				return invariant.New(TypeRecoveryLetterKind, in.DocumentID.String())
			}
		}
		if in.DryRun {
			ps := PlanStatus(plan, st.view.Arrears, st.today, pr)
			st.plan = &ps
			out = st.result()
			return nil
		}
		calls, err := json.Marshal(plan.Calls)
		if err != nil {
			return err
		}
		inst, err := json.Marshal(plan.Instalments)
		if err != nil {
			return err
		}
		now := s.now()
		row := planRow{ID: s.NewID(), OwnerID: sc.OwnerID, LeaseID: leaseID, AgreedOn: plan.AgreedOn.Time(), DebtCents: int64(plan.DebtCents),
			Calls: calls, Instalments: inst, DocumentID: in.DocumentID, CreatedAt: now, CreatedBy: p.Sub, UpdatedAt: now, UpdatedBy: p.Sub, Version: 1}
		if err := s.Repo.InsertPlan(ctx, row); err != nil {
			return err
		}
		after, err := s.recovery(ctx, leaseID)
		if err != nil {
			return err
		}
		out = after.result()
		return audit.Record(ctx, "billing.payment_plan", audit.Entity{Type: "lease", ID: leaseID.String()}, nil, out.Plan)
	})
	return out, err
}

// CancelPlan cancels a lease's latest payment plan on a day, which ends its
// suspension of the recovery. A completed or cancelled plan is refused.
func (s Service) CancelPlan(ctx context.Context, p *auth.Principal, requestID string, leaseID, planID uuid.UUID, in CancelPlanInput) (RecoveryResult, error) {
	sc, err := ownerScope(p, requestID)
	if err != nil {
		return RecoveryResult{}, err
	}
	var out RecoveryResult
	err = s.DB.InScope(ctx, sc, func(ctx context.Context) error {
		st, err := s.recovery(ctx, leaseID)
		if err != nil {
			return err
		}
		if st.plan == nil || st.plan.ID != planID {
			return invariant.ErrNotFound
		}
		if st.plan.Status == PlanCompleted || st.plan.Status == PlanCancelled {
			return invariant.New(TypePlanClosed, planID.String())
		}
		if in.CancelledOn.IsZero() || in.CancelledOn.After(st.today) || in.CancelledOn.Before(st.plan.AgreedOn) {
			return invariant.New(TypePlanInvalid, "cancelled_on")
		}
		if st.planRow.Version != in.Version {
			return invariant.ErrStale
		}
		before := st.plan
		if err := s.Repo.CancelPlan(ctx, planID, in.Version, in.CancelledOn.Time(), s.now(), p.Sub); err != nil {
			return err
		}
		after, err := s.recovery(ctx, leaseID)
		if err != nil {
			return err
		}
		out = after.result()
		return audit.Record(ctx, "billing.payment_plan_cancel", audit.Entity{Type: "lease", ID: leaseID.String()}, before, out.Plan)
	})
	return out, err
}
