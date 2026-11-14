// Package maintenance holds the owner's contractors and the incidents of
// their assets with the works that answer them (RENT-SPEC-015): an incident
// is reported, quoted, scheduled, done and invoiced, or cancelled; the
// invoice of a repair, or of an improvement of a dwelling, becomes a
// deductible repairs charge; that of construction, enlargement or an
// improvement of another asset is capitalised, never deducted (Q-130). Next
// and Deductible are pure.
package maintenance

import (
	"fmt"
	"slices"

	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/problem"
)

// Status of an incident (incidents.status).
type Status string

// Statuses.
const (
	StatusNew       Status = "new"
	StatusQuoted    Status = "quoted"
	StatusScheduled Status = "scheduled"
	StatusDone      Status = "done"
	StatusInvoiced  Status = "invoiced"
	StatusCancelled Status = "cancelled"
)

// Statuses lists every status, for the exhaustive tests.
var Statuses = []Status{StatusNew, StatusQuoted, StatusScheduled, StatusDone, StatusInvoiced, StatusCancelled}

// Action moves an incident.
type Action string

// Actions.
const (
	ActionQuote    Action = "quote"
	ActionSchedule Action = "schedule"
	ActionDone     Action = "done"
	ActionInvoice  Action = "invoice"
	ActionCancel   Action = "cancel"
)

// Actions lists every action, for the exhaustive tests.
var Actions = []Action{ActionQuote, ActionSchedule, ActionDone, ActionInvoice, ActionCancel}

// Treatments of an invoice (incidents.treatment, Q-130): a repair or
// upkeep; an improvement; construction, reconstruction or enlargement.
const (
	TreatmentRepair       = "repair"
	TreatmentImprovement  = "improvement"
	TreatmentConstruction = "construction"
)

// Deductible says an invoice becomes a deductible repairs charge (line 224
// of the 2044, CGI art. 31, I-1° b): a repair always; an improvement when
// the asset's kind is one the parameter tax.improvement_deductible lists
// (the dwellings), or when the asset is let in the same lease as such an
// asset, whose regime it follows (an annex, such as a box let with the
// apartment: leaseKinds are the kinds of the other assets of the lease that
// covered it on the day the works were done, Q-130); construction,
// reconstruction and enlargement never. Pure: the caller passes the
// parameter in force and the lease's other assets.
func Deductible(treatment, assetKind string, leaseKinds []string, rule params.ImprovementDeductible) bool {
	switch treatment {
	case TreatmentRepair:
		return true
	case TreatmentImprovement:
		if slices.Contains(rule.AssetKinds, assetKind) {
			return true
		}
		for _, k := range leaseKinds {
			if slices.Contains(rule.AssetKinds, k) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// Problem types of the maintenance module.
const (
	TypeTransition = "maintenance/transition"
	TypeContractor = "maintenance/contractor-unknown"
)

func init() {
	problem.Register(map[string]problem.Message{
		TypeTransition: problem.M(409, "Cet incident n’en est pas à cette étape.", "This incident is not at this step."),
		TypeContractor: problem.M(422, "Cet artisan n’existe pas dans ce portefeuille.", "This contractor does not exist in this portfolio."),
	})
}

var transitions = map[Status]map[Action]Status{
	StatusNew:       {ActionQuote: StatusQuoted, ActionSchedule: StatusScheduled, ActionCancel: StatusCancelled},
	StatusQuoted:    {ActionQuote: StatusQuoted, ActionSchedule: StatusScheduled, ActionCancel: StatusCancelled},
	StatusScheduled: {ActionSchedule: StatusScheduled, ActionDone: StatusDone, ActionCancel: StatusCancelled},
	StatusDone:      {ActionInvoice: StatusInvoiced},
}

// Next returns the status an action moves an incident to (Q-130):
//   - new: quoted (a contractor's quote), scheduled straight away (a small
//     job without a quote), or cancelled;
//   - quoted: quoted again (another quote replaces it), scheduled, or
//     cancelled;
//   - scheduled: rescheduled, done, or cancelled;
//   - done: invoiced, once; a job done is no longer cancelled;
//   - invoiced and cancelled are final.
func Next(s Status, a Action) (Status, error) {
	if to, ok := transitions[s][a]; ok {
		return to, nil
	}
	return "", invariant.New(TypeTransition, fmt.Sprintf("%s from %s", a, s))
}

// Editable says whether an incident's report (title, description, urgency,
// photos) may still change: until the works are done.
func Editable(s Status) bool {
	return s == StatusNew || s == StatusQuoted || s == StatusScheduled
}
