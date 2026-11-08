package lease

import (
	"fmt"
	"strings"

	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/period"
	"github.com/ovander/tenure-api/internal/problem"
)

// Problem types of a rent re-evaluation at renewal.
const (
	TypeReevaluationInvalid    = "lease/reevaluation-invalid"
	TypeReevaluationNotAllowed = "lease/reevaluation-not-allowed"
	TypeReevaluationTooLate    = "lease/reevaluation-too-late"
	TypeReevaluationAboveCap   = "lease/reevaluation-above-cap"
	TypeReevaluationControlled = "lease/reevaluation-controlled-zone"
	TypeReevaluationExists     = "lease/reevaluation-exists"
	TypeReevaluationState      = "lease/reevaluation-state"
	TypeReevaluationNoStepDue  = "lease/reevaluation-no-step-due"
)

func init() {
	problem.Register(map[string]problem.Message{
		TypeReevaluationInvalid:    problem.M(422, "La proposition de nouveau loyer est incomplète.", "The proposed new rent is incomplete."),
		TypeReevaluationNotAllowed: problem.M(422, "Ce bail ne permet pas de réévaluer le loyer à son renouvellement.", "This lease does not allow a rent re-evaluation at renewal."),
		TypeReevaluationTooLate:    problem.M(422, "La proposition arrive trop tard pour ce renouvellement.", "The proposal arrives too late for this renewal."),
		TypeReevaluationAboveCap:   problem.M(422, "Le loyer proposé dépasse la hausse permise en zone tendue.", "The proposed rent exceeds the increase allowed in a zone tendue."),
		TypeReevaluationControlled: problem.M(422, "La réévaluation d’un loyer encadré n’est pas encore prise en charge.", "Re-evaluating a controlled rent is not supported yet."),
		TypeReevaluationExists:     problem.M(409, "Une proposition de nouveau loyer a déjà été remise pour ce renouvellement.", "A proposed new rent was already delivered for this renewal."),
		TypeReevaluationState:      problem.M(409, "Cette proposition n’en est pas à cette étape.", "This proposal is not at this step."),
		TypeReevaluationNoStepDue:  problem.M(422, "Aucun palier de hausse n’est échu.", "No step of the increase is due."),
	})
}

// Reference is a rent of a comparable dwelling the owner relies on (loi
// 89-462 art. 17-2).
type Reference struct {
	Address     string      `json:"address"`
	Description string      `json:"description,omitempty"`
	RentCents   money.Cents `json:"rent_cents"`
}

// ReevaluationInput is the owner's proposal: the new rent and its references.
type ReevaluationInput struct {
	ProposedRentCents money.Cents `json:"proposed_rent_cents"`
	References        []Reference `json:"references"`
	// LargeAgglomeration says the home is in an agglomeration of more than a
	// million inhabitants, which needs more references.
	LargeAgglomeration bool `json:"large_agglomeration"`
}

// ReevaluationFacts is the lease a re-evaluation applies to.
type ReevaluationFacts struct {
	Kind           Kind
	StartOn        period.Date
	TermMonths     int
	EndOn          *period.Date
	RentCents      money.Cents
	TenseZone      bool
	ControlledZone bool
}

// Step is the rent from a day of the increase's schedule.
type Step struct {
	From      period.Date `json:"from" swaggertype:"string" format:"date"`
	RentCents money.Cents `json:"rent_cents"`
}

// ReevaluationTerms is what a proposal computes.
type ReevaluationTerms struct {
	// RenewalOn is the first day of the renewed term.
	RenewalOn period.Date `json:"renewal_on" swaggertype:"string" format:"date"`
	// LatestReceiptOn is the last day the tenant may receive the proposal.
	LatestReceiptOn  period.Date `json:"latest_receipt_on" swaggertype:"string" format:"date"`
	CurrentRentCents money.Cents `json:"current_rent_cents"`
	IncreaseCents    money.Cents `json:"increase_cents"`
	// IncreaseBP is the increase over the current rent, in basis points,
	// rounded down.
	IncreaseBP int64 `json:"increase_bp"`
	// ReferencesMeanCents is the mean of the references, rounded down.
	ReferencesMeanCents money.Cents `json:"references_mean_cents"`
	// CapCents is the highest rent a zone tendue allows, when it applies.
	CapCents *money.Cents `json:"cap_cents,omitempty"`
	// Steps is the yearly schedule of the increase, from the renewal.
	Steps []Step `json:"steps"`
}

// Reevaluate checks an owner's proposal of a new rent at the next renewal of
// an unfurnished lease and computes its schedule (loi 89-462 art. 17-2,
// Q-120): received at the latest its lead time before the term (with
// receivedOn zero, the next renewal still reachable from today), with
// enough references; in a zone tendue, the increase is at most a share of
// the gap between the references' mean and the current rent; the increase
// applies in yearly steps, thirds or sixths. A lease under rent control is
// refused until its rules are built. Pure.
func Reevaluate(f ReevaluationFacts, in ReevaluationInput, receivedOn, today period.Date, r params.Reevaluation) (ReevaluationTerms, error) {
	if f.Kind != KindUnfurnished {
		return ReevaluationTerms{}, invariant.New(TypeReevaluationNotAllowed, string(f.Kind))
	}
	if f.EndOn != nil {
		return ReevaluationTerms{}, invariant.New(TypeReevaluationNotAllowed, "the lease has an end")
	}
	if f.ControlledZone {
		return ReevaluationTerms{}, invariant.New(TypeReevaluationControlled, "controlled zone")
	}
	need := r.MinReferences
	if in.LargeAgglomeration {
		need = r.MinReferencesLargeAgglomeration
	}
	if len(in.References) < need {
		return ReevaluationTerms{}, invariant.New(TypeReevaluationInvalid, fmt.Sprintf("references: %d, at least %d", len(in.References), need))
	}
	var sum money.Cents
	for i, ref := range in.References {
		if strings.TrimSpace(ref.Address) == "" || ref.RentCents <= 0 {
			return ReevaluationTerms{}, invariant.New(TypeReevaluationInvalid, fmt.Sprintf("references[%d]", i))
		}
		sum += ref.RentCents
	}
	if f.RentCents <= 0 || in.ProposedRentCents <= f.RentCents {
		return ReevaluationTerms{}, invariant.New(TypeReevaluationInvalid, "proposed_rent_cents must exceed the current rent")
	}
	t := ReevaluationTerms{CurrentRentCents: f.RentCents, ReferencesMeanCents: sum / money.Cents(len(in.References)), Steps: []Step{}}
	on := receivedOn
	if on.IsZero() {
		on = today
	}
	end, ok := TermEnd(f.StartOn, f.TermMonths, f.Kind, on)
	if !ok || on.Before(f.StartOn) {
		return ReevaluationTerms{}, invariant.New(TypeReevaluationNotAllowed, "no term")
	}
	latest := latestReceipt(end, r.LeadMonths)
	if on.After(latest) {
		if !receivedOn.IsZero() {
			return ReevaluationTerms{}, invariant.New(TypeReevaluationTooLate, fmt.Sprintf("received %s after %s", receivedOn, latest))
		}
		end, _ = TermEnd(f.StartOn, f.TermMonths, f.Kind, end.AddDays(1))
		latest = latestReceipt(end, r.LeadMonths)
	}
	t.RenewalOn, t.LatestReceiptOn = end.AddDays(1), latest
	if f.TenseZone {
		if t.ReferencesMeanCents <= f.RentCents {
			return ReevaluationTerms{}, invariant.New(TypeReevaluationAboveCap, "the references do not exceed the current rent")
		}
		half, err := money.MulDivFloor(t.ReferencesMeanCents-f.RentCents, r.TenseZoneCapBP, 10000)
		if err != nil {
			return ReevaluationTerms{}, err
		}
		ceiling := f.RentCents + half
		t.CapCents = &ceiling
		if in.ProposedRentCents > ceiling {
			return ReevaluationTerms{}, invariant.New(TypeReevaluationAboveCap, fmt.Sprintf("proposed %d above %d", in.ProposedRentCents, ceiling))
		}
	}
	t.IncreaseCents = in.ProposedRentCents - f.RentCents
	bp, err := money.MulDivFloor(t.IncreaseCents, 10000, int64(f.RentCents))
	if err != nil {
		return ReevaluationTerms{}, err
	}
	t.IncreaseBP = int64(bp)
	n := r.StepsShort
	if f.TermMonths >= r.LongTermMonths || t.IncreaseBP > r.IncreaseThresholdBP {
		n = r.StepsLong
	}
	if n <= 0 {
		return ReevaluationTerms{}, invariant.New(TypeReevaluationInvalid, "no steps")
	}
	for k := 1; k <= n; k++ {
		part, err := money.MulDivFloor(t.IncreaseCents, int64(k), int64(n))
		if err != nil {
			return ReevaluationTerms{}, err
		}
		t.Steps = append(t.Steps, Step{From: t.RenewalOn.AddMonthsClamped(12 * (k - 1)), RentCents: f.RentCents + part})
	}
	return t, nil
}
