package billing

import (
	"sort"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/period"
	"github.com/ovander/tenure-api/internal/problem"
)

// Steps of the recovery of unpaid rent (RENT-SPEC-011, Q-121), after the
// reminder e-mail billing.reminders sends.
const (
	StepFormalNotice       = "formal_notice"       // mise en demeure (MD letter)
	StepSuretyNotice       = "surety_notice"       // information of a natural-person surety (IC letter)
	StepInsuranceClaim     = "insurance_claim"     // claim to the rent-guarantee insurer or Visale
	StepCommandement       = "commandement"        // commandement de payer by a commissaire de justice
	StepSuretyCommandement = "surety_commandement" // the commandement signified to the surety
	StepCourt              = "court"               // referral to the court
)

// Steps lists the recovery steps, in their usual order.
var Steps = []string{StepFormalNotice, StepSuretyNotice, StepInsuranceClaim, StepCommandement, StepSuretyCommandement, StepCourt}

// Problem types of the recovery.
const (
	TypeRecoveryInvalid    = "billing/recovery-invalid"
	TypeRecoveryNotDue     = "billing/recovery-not-due"
	TypeRecoveryLetterKind = "billing/recovery-letter-missing"
)

func init() {
	problem.Register(map[string]problem.Message{
		TypeRecoveryInvalid:    problem.M(422, "Cette étape du recouvrement est incomplète.", "This recovery step is incomplete."),
		TypeRecoveryNotDue:     problem.M(422, "Aucun loyer impayé n’appelle cette étape.", "No unpaid rent calls for this step."),
		TypeRecoveryLetterKind: problem.M(422, "Cette étape doit reprendre une lettre préparée ou déposée pour ce bail.", "This step needs a letter prepared or uploaded for this lease."),
	})
}

// RecoveryStep is a recovery step recorded on a lease.
type RecoveryStep struct {
	ID               uuid.UUID   `json:"id"`
	Kind             string      `json:"kind" enums:"formal_notice,surety_notice,insurance_claim,commandement,surety_commandement,court"`
	DoneOn           period.Date `json:"done_on" swaggertype:"string" format:"date"`
	OutstandingCents money.Cents `json:"outstanding_cents"`
	GuaranteeID      *uuid.UUID  `json:"guarantee_id,omitempty"`
	DocumentID       *uuid.UUID  `json:"document_id,omitempty"`
	Delivery         string      `json:"delivery,omitempty" enums:"registered_letter,bailiff,hand_delivery,email"`
	TrackingRef      string      `json:"tracking_ref,omitempty"`
	// DeadlineOn is a date the step sets: the insurer's deadline, as the
	// owner records it.
	DeadlineOn *period.Date `json:"deadline_on,omitempty" swaggertype:"string" format:"date"`
	Note       string       `json:"note,omitempty"`
}

// RecoveryFacts is what the recovery reads of a lease besides its arrears.
type RecoveryFacts struct {
	// Sureties are the surety guarantees to inform (natural persons).
	Sureties []uuid.UUID
	// Insured says a rent-guarantee insurance or Visale covers the lease.
	Insured bool
	// TermStartOn is the first day of the lease's current term: the
	// clause résolutoire's delay depends on it.
	TermStartOn period.Date
}

// NextStep is a step the recovery calls for, from a day.
type NextStep struct {
	Kind        string      `json:"kind" enums:"formal_notice,surety_notice,insurance_claim,commandement,surety_commandement,court"`
	DueOn       period.Date `json:"due_on" swaggertype:"string" format:"date"`
	Overdue     bool        `json:"overdue"`
	GuaranteeID *uuid.UUID  `json:"guarantee_id,omitempty"`
}

// RecoveryView is where the recovery of a lease's unpaid rent stands.
type RecoveryView struct {
	Arrears ArrearsView `json:"arrears"`
	// Since is the due day of the oldest overdue call: the episode starts
	// there, and the steps done since belong to it.
	Since *period.Date   `json:"since,omitempty" swaggertype:"string" format:"date"`
	Steps []RecoveryStep `json:"steps"`
	Next  []NextStep     `json:"next"`
	// ResolutionOn is the day the clause résolutoire may take effect, the
	// delay after the commandement de payer having run.
	ResolutionOn *period.Date `json:"resolution_on,omitempty" swaggertype:"string" format:"date"`
}

// Recovery says where the recovery of a lease's unpaid rent stands and which
// steps are next (RENT-SPEC-011, Q-121). An episode starts on the due day of
// the oldest overdue call and ends once nothing is overdue; the steps done
// since that day belong to it, and each step is called for once:
//   - the formal notice from Since plus FormalNoticeAfterDays;
//   - each surety informed from Since plus SuretyNoticeAfterMonths;
//   - the insurer's claim, when insured, from Since plus
//     InsuranceClaimAfterDays;
//   - the commandement de payer from the formal notice plus
//     CommandementAfterDays, then its notice to each surety within
//     SuretyCommandementDays;
//   - the court once the clause résolutoire's delay after the commandement
//     has run: ResolutionWeeks for a term begun on or after
//     ResolutionWeeksFrom, else FormerResolutionMonths.
//
// Pure: the caller passes the day, the arrears and the steps.
func Recovery(arr ArrearsView, steps []RecoveryStep, f RecoveryFacts, today period.Date, r params.Recovery) (RecoveryView, error) {
	v := RecoveryView{Arrears: arr, Steps: []RecoveryStep{}, Next: []NextStep{}}
	var since *period.Date
	for _, it := range arr.Items {
		if it.Overdue && (since == nil || it.DueOn.Before(*since)) {
			d := it.DueOn
			since = &d
		}
	}
	if since == nil {
		v.Steps = append(v.Steps, steps...)
		sortSteps(v.Steps)
		return v, nil
	}
	v.Since = since
	done := map[string]RecoveryStep{}
	suretyDone := map[uuid.UUID]map[string]bool{}
	for _, s := range steps {
		v.Steps = append(v.Steps, s)
		if s.DoneOn.Before(*since) {
			continue
		}
		if s.GuaranteeID != nil {
			if suretyDone[*s.GuaranteeID] == nil {
				suretyDone[*s.GuaranteeID] = map[string]bool{}
			}
			suretyDone[*s.GuaranteeID][s.Kind] = true
			continue
		}
		if prev, ok := done[s.Kind]; !ok || s.DoneOn.Before(prev.DoneOn) {
			done[s.Kind] = s
		}
	}
	sortSteps(v.Steps)
	next := func(kind string, due period.Date, g *uuid.UUID) {
		v.Next = append(v.Next, NextStep{Kind: kind, DueOn: due, Overdue: today.After(due), GuaranteeID: g})
	}
	if _, ok := done[StepFormalNotice]; !ok {
		next(StepFormalNotice, since.AddDays(r.FormalNoticeAfterDays), nil)
	}
	for _, g := range f.Sureties {
		if !suretyDone[g][StepSuretyNotice] {
			next(StepSuretyNotice, since.AddMonthsClamped(r.SuretyNoticeAfterMonths), &g)
		}
	}
	if _, ok := done[StepInsuranceClaim]; f.Insured && !ok {
		next(StepInsuranceClaim, since.AddDays(r.InsuranceClaimAfterDays), nil)
	}
	fn, noticed := done[StepFormalNotice]
	cmd, commanded := done[StepCommandement]
	if noticed && !commanded {
		next(StepCommandement, fn.DoneOn.AddDays(r.CommandementAfterDays), nil)
	}
	if commanded {
		from, err := period.ParseDate(r.ResolutionWeeksFrom)
		if err != nil {
			return RecoveryView{}, err
		}
		res := cmd.DoneOn.AddMonthsClamped(r.FormerResolutionMonths)
		if !f.TermStartOn.IsZero() && !f.TermStartOn.Before(from) {
			res = cmd.DoneOn.AddDays(7 * r.ResolutionWeeks)
		}
		v.ResolutionOn = &res
		for _, g := range f.Sureties {
			if !suretyDone[g][StepSuretyCommandement] {
				next(StepSuretyCommandement, cmd.DoneOn.AddDays(r.SuretyCommandementDays), &g)
			}
		}
		if _, ok := done[StepCourt]; !ok {
			next(StepCourt, res.AddDays(1), nil)
		}
	}
	sort.SliceStable(v.Next, func(i, j int) bool { return v.Next[i].DueOn.Before(v.Next[j].DueOn) })
	return v, nil
}

func sortSteps(s []RecoveryStep) {
	sort.SliceStable(s, func(i, j int) bool { return s[i].DoneOn.Before(s[j].DoneOn) })
}
