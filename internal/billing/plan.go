package billing

import (
	"fmt"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/period"
	"github.com/ovander/tenure-api/internal/problem"
)

// Where a payment plan stands (RENT-SPEC-011, Q-122), computed on the day it
// is read.
const (
	PlanRespected = "respected"
	PlanBroken    = "broken"
	PlanCompleted = "completed"
	PlanCancelled = "cancelled"
)

// Why a plan is broken.
const (
	PlanLateInstalment = "instalment_late"
	PlanCurrentUnpaid  = "current_rent_unpaid"
)

// Problem types of payment plans.
const (
	TypePlanInvalid = "billing/plan-invalid"
	TypePlanExists  = "billing/plan-exists"
	TypePlanClosed  = "billing/plan-closed"
)

func init() {
	problem.Register(map[string]problem.Message{
		TypePlanInvalid: problem.M(422, "Ce plan d’apurement est incomplet ou ne couvre pas la dette.", "This payment plan is incomplete or does not cover the debt."),
		TypePlanExists:  problem.M(409, "Un plan d’apurement est déjà en cours pour ce bail.", "A payment plan is already running for this lease."),
		TypePlanClosed:  problem.M(409, "Ce plan d’apurement est déjà terminé ou annulé.", "This payment plan is already over or cancelled."),
	})
}

// Instalment is one agreed payment of a plan.
type Instalment struct {
	DueOn       period.Date `json:"due_on" swaggertype:"string" format:"date"`
	AmountCents money.Cents `json:"amount_cents"`
}

// Plan is a payment plan as recorded: the overdue rent calls it covers on
// the day it was agreed, their debt and the instalments.
type Plan struct {
	ID          uuid.UUID    `json:"id"`
	AgreedOn    period.Date  `json:"agreed_on" swaggertype:"string" format:"date"`
	DebtCents   money.Cents  `json:"debt_cents"`
	Calls       []uuid.UUID  `json:"calls"`
	Instalments []Instalment `json:"instalments"`
	DocumentID  *uuid.UUID   `json:"document_id,omitempty"`
	CancelledOn *period.Date `json:"cancelled_on,omitempty" swaggertype:"string" format:"date"`
}

// PlanState is a plan and where it stands on a day.
type PlanState struct {
	Plan
	Status string `json:"status" enums:"respected,broken,completed,cancelled"`
	// Reason says why a plan is broken.
	Reason string `json:"reason,omitempty" enums:"instalment_late,current_rent_unpaid"`
	// PaidCents is what the covered calls received since: the debt less
	// what they still owe.
	PaidCents money.Cents `json:"paid_cents"`
	// DueCents is the instalments past their day and grace.
	DueCents money.Cents `json:"due_cents"`
	// NextInstalment is the first instalment not yet covered by the
	// payments.
	NextInstalment *Instalment `json:"next_instalment,omitempty"`
}

// PlanStatus says where a payment plan stands on today (RENT-SPEC-011,
// Q-122): completed once the covered calls are paid; broken when the
// payments fall short of the instalments past their day plus GraceDays, or
// when a call the plan does not cover is overdue past the same grace (the
// current rent must be paid too); respected otherwise. Pure.
func PlanStatus(p Plan, arr ArrearsView, today period.Date, r params.RecoveryPlan) PlanState {
	st := PlanState{Plan: p}
	if p.CancelledOn != nil {
		st.Status = PlanCancelled
		return st
	}
	covered := map[uuid.UUID]bool{}
	for _, id := range p.Calls {
		covered[id] = true
	}
	var owed money.Cents
	newer := false
	for _, it := range arr.Items {
		if covered[it.BillingDocumentID] {
			owed += it.OutstandingCents
		} else if it.Overdue && today.After(it.DueOn.AddDays(r.GraceDays)) {
			newer = true
		}
	}
	st.PaidCents = max(p.DebtCents-owed, 0)
	if owed <= 0 {
		st.Status = PlanCompleted
		return st
	}
	var cum money.Cents
	for _, in := range p.Instalments {
		cum += in.AmountCents
		if today.After(in.DueOn.AddDays(r.GraceDays)) {
			st.DueCents = cum
		}
		if st.NextInstalment == nil && cum > st.PaidCents {
			x := in
			st.NextInstalment = &x
		}
	}
	switch {
	case st.PaidCents < st.DueCents:
		st.Status, st.Reason = PlanBroken, PlanLateInstalment
	case newer:
		st.Status, st.Reason = PlanBroken, PlanCurrentUnpaid
	default:
		st.Status = PlanRespected
	}
	return st
}

// NewPlan checks a plan agreed on agreedOn and returns it (Q-122): it covers
// every call overdue on that day and their whole outstanding debt, in at
// most MaxInstalments instalments of positive amounts, on strictly
// increasing days from the agreement on. Nothing overdue is refused. Pure.
func NewPlan(arr ArrearsView, agreedOn, today period.Date, in []Instalment, r params.RecoveryPlan) (Plan, error) {
	if agreedOn.IsZero() || agreedOn.After(today) {
		return Plan{}, invariant.New(TypePlanInvalid, "agreed_on")
	}
	p := Plan{AgreedOn: agreedOn, Calls: []uuid.UUID{}, Instalments: in}
	for _, it := range arr.Items {
		if it.Overdue {
			p.Calls = append(p.Calls, it.BillingDocumentID)
			p.DebtCents += it.OutstandingCents
		}
	}
	if p.DebtCents <= 0 {
		return Plan{}, invariant.New(TypeRecoveryNotDue, "nothing overdue")
	}
	if len(in) == 0 || len(in) > r.MaxInstalments {
		return Plan{}, invariant.New(TypePlanInvalid, fmt.Sprintf("instalments: %d", len(in)))
	}
	var sum money.Cents
	prev := agreedOn.AddDays(-1)
	for i, x := range in {
		if x.AmountCents <= 0 || !x.DueOn.After(prev) {
			return Plan{}, invariant.New(TypePlanInvalid, fmt.Sprintf("instalments[%d]", i))
		}
		sum += x.AmountCents
		prev = x.DueOn
	}
	if sum != p.DebtCents {
		return Plan{}, invariant.New(TypePlanInvalid, fmt.Sprintf("instalments total %d, debt %d", sum, p.DebtCents))
	}
	return p, nil
}
