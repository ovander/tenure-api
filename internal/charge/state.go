package charge

import (
	"fmt"

	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/money"
)

// State is the state of a reconciliation (RENT-SPEC-007).
type State string

// Reconciliation states: draft → allocated → issued → settled; an issued or
// settled reconciliation is corrected by cancelling it (cancelled) and
// starting a new one.
const (
	StateDraft     State = "draft"
	StateAllocated State = "allocated"
	StateIssued    State = "issued"
	StateSettled   State = "settled"
	StateCancelled State = "cancelled"
)

// Action is a reconciliation transition.
type Action string

// Reconciliation actions (POST /reconciliations/{id}/transitions).
const (
	ActionAllocate Action = "allocate"
	ActionIssue    Action = "issue"
	ActionSettle   Action = "settle"
	ActionCancel   Action = "cancel"
)

// States lists every reconciliation state.
var States = []State{StateDraft, StateAllocated, StateIssued, StateSettled, StateCancelled}

// Actions lists every reconciliation action.
var Actions = []Action{ActionAllocate, ActionIssue, ActionSettle, ActionCancel}

// TypeInvalidTransition is the problem type of a transition the state does
// not allow.
const TypeInvalidTransition = "reconciliation/invalid-transition"

var transitions = map[State]map[Action]State{
	// allocate again recomputes an allocated reconciliation (new charges,
	// new payments) until it is issued.
	StateDraft:     {ActionAllocate: StateAllocated},
	StateAllocated: {ActionAllocate: StateAllocated, ActionIssue: StateIssued},
	StateIssued:    {ActionSettle: StateSettled, ActionCancel: StateCancelled},
	StateSettled:   {ActionCancel: StateCancelled},
}

// Next returns the state a reconciliation in from reaches with a, or
// reconciliation/invalid-transition. An issued statement is never edited: a
// correction cancels it (RENT-SPEC-007).
func Next(from State, a Action) (State, error) {
	if to, ok := transitions[from][a]; ok {
		return to, nil
	}
	return "", invariant.New(TypeInvalidTransition, fmt.Sprintf("%s from %s", a, from))
}

// PaidCall is what the provisions collected read of one rent call: its
// total, its provision line and the payments matched to it.
type PaidCall struct {
	TotalCents     money.Cents `json:"total_cents"`
	ProvisionCents money.Cents `json:"provision_cents"`
	MatchedCents   money.Cents `json:"matched_cents"`
}

// ProvisionsCollected returns the provisional charges actually collected
// over the rent calls of an exercise (RENT-SPEC-007 step 4): for each call,
// the matched payments are split between its lines in proportion to their
// amounts (Q-65), and the provision part is rounded up to the cent, so that
// the tenant's balance is never overstated (Q-78). A call never contributes
// more than its provision line.
func ProvisionsCollected(calls []PaidCall) (money.Cents, error) {
	var sum money.Cents
	for _, c := range calls {
		if c.TotalCents <= 0 || c.ProvisionCents <= 0 || c.MatchedCents <= 0 {
			continue
		}
		paid := min(c.MatchedCents, c.TotalCents)
		part, err := money.MulDivFloor(c.ProvisionCents, int64(paid), int64(c.TotalCents))
		if err != nil {
			return 0, err
		}
		if part*c.TotalCents < c.ProvisionCents*paid {
			part++
		}
		sum += min(part, c.ProvisionCents)
	}
	return sum, nil
}
