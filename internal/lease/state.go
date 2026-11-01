package lease

import (
	"fmt"

	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/problem"
)

// Action is a lifecycle transition requested on a lease.
type Action string

// Lifecycle actions (RENT-BE-001 §3: activate, notice, end, close).
const (
	ActionActivate Action = "activate"
	ActionNotice   Action = "notice"
	ActionEnd      Action = "end"
	ActionClose    Action = "close"
)

// Problem types of the lease module.
const (
	TypeInvalidTransition = "lease/invalid-transition"
	TypeOneActivePerAsset = "lease/one-active-per-asset"
	TypeNotEditable       = "lease/not-editable"
	TypeDepositNotSettled = "lease/deposit-not-settled"
	TypeNoticeEndRequired = "lease/notice-end-date-required"
	TypeAssetsInvalid     = "lease/assets-invalid"
	TypeRelocationIndex   = "lease/relocation-index-unknown"
	TypeGuaranteeClosed   = "guarantee/lease-closed"
)

func init() {
	problem.Register(map[string]problem.Message{
		TypeInvalidTransition:    problem.M(409, "Cette transition n’est pas possible dans l’état actuel du bail.", "This transition is not possible in the lease's current state."),
		TypeOneActivePerAsset:    problem.M(409, "Un bien ne peut avoir qu’un seul bail en cours.", "An asset can have only one current lease."),
		TypeNotEditable:          problem.M(409, "Seul un bail en brouillon peut être modifié ; utilisez un avenant.", "Only a draft lease can be edited; use an amendment."),
		TypeDepositNotSettled:    problem.M(409, "Le dépôt de garantie doit être soldé avant la clôture.", "The deposit must be settled before closing."),
		TypeNoticeEndRequired:    problem.M(422, "La date de fin est obligatoire pour un congé.", "An end date is required for a notice."),
		TypeAssetsInvalid:        problem.M(422, "Les biens ne correspondent pas au type de bail.", "The assets do not match the lease kind."),
		TypeRelocationIndex:      problem.M(422, "L’indice IRL nécessaire au contrôle de relocation est inconnu.", "The IRL index needed for the relocation check is unknown."),
		TypeGuaranteeClosed:      problem.M(409, "Le bail est clos : ses garanties ne sont plus modifiables.", "The lease is closed: its guarantees can no longer change."),
		TypeConsistentShares:     problem.M(422, "Tous les biens d’un bail doivent avoir les mêmes quotes-parts de propriété.", "All assets of a lease must have the same ownership shares."),
		TypeRentBreakdown:        problem.M(422, "La ventilation du loyer par bien doit totaliser le loyer.", "The per-asset rent breakdown must add up to the rent."),
		TypeZoneCeilingExceeded:  problem.M(422, "Le loyer dépasse le loyer de référence majoré de la zone.", "The rent exceeds the zone's maximum reference rent."),
		TypeZoneCeilingUnknown:   problem.M(422, "Le plafond de la zone d’encadrement est inconnu pour ce bien.", "The rent-control ceiling is unknown for this asset."),
		TypeRelocationExceeded:   problem.M(422, "Le loyer dépasse le loyer précédent révisé selon l’IRL.", "The rent exceeds the previous rent revised by IRL."),
		TypeRentExemptionUnknown: problem.M(422, "Motif de dérogation inconnu.", "Unknown rent exemption."),
	})
}

var transitions = map[State]map[Action]State{
	StateDraft:  {ActionActivate: StateActive},
	StateActive: {ActionNotice: StateNotice},
	StateNotice: {ActionEnd: StateEnded},
	StateEnded:  {ActionClose: StateClosed},
}

// States lists every lease state.
var States = []State{StateDraft, StateActive, StateNotice, StateEnded, StateClosed}

// Actions lists every lifecycle action.
var Actions = []Action{ActionActivate, ActionNotice, ActionEnd, ActionClose}

// Next returns the state a lease in from reaches with a, or
// lease/invalid-transition (RENT-SPEC-002: draft → active → notice → ended →
// closed). Blockers (documents, deposit, one active lease per asset) are
// checked by the service on top of this table.
func Next(from State, a Action) (State, error) {
	if to, ok := transitions[from][a]; ok {
		return to, nil
	}
	return "", invariant.New(TypeInvalidTransition, fmt.Sprintf("%s from %s", a, from))
}
