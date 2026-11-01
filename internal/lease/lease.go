// Package lease owns the Lease, its guarantees and inventories, and its
// lifecycle (RENT-SPEC-002, RENT-SPEC-008). Slice 1 holds the pure domain type
// the calculation engine reads; the state machine, repository and handlers
// arrive in slice 2.
package lease

import (
	"time"

	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/period"
	"github.com/ovander/tenure-api/internal/portfolio"
)

// Kind is the legal kind of a lease, which selects the contract template and
// the deposit rule.
type Kind string

// Lease kinds (RENT-SPEC-008).
const (
	KindUnfurnished Kind = "unfurnished"
	KindFurnished   Kind = "furnished"
	KindMobility    Kind = "mobility"
	KindBox         Kind = "box"
)

// State is the lifecycle state of a lease.
type State string

// Lease states (RENT-SPEC-002): draft → active → notice → ended → closed.
const (
	StateDraft  State = "draft"
	StateActive State = "active"
	StateNotice State = "notice"
	StateEnded  State = "ended"
	StateClosed State = "closed"
)

// Billable reports whether a lease in state s is billed (RENT-SPEC-002:
// billing runs only in active and notice-given states).
func (s State) Billable() bool { return s == StateActive || s == StateNotice }

// Lease is the contract between the owner and the tenants over one or more
// assets. Amounts are the current ones: RentCents changes on revision.
type Lease struct {
	ID     string            `json:"id"`
	Kind   Kind              `json:"kind"`
	State  State             `json:"state"`
	Assets []portfolio.Asset `json:"assets"`
	// StartOn is the first occupied day; EndOn the last one, zero while open.
	StartOn        period.Date `json:"start_on"`
	EndOn          period.Date `json:"end_on"`
	TermMonths     int         `json:"term_months"`
	RentCents      money.Cents `json:"rent_cents"`
	ProvisionCents money.Cents `json:"provision_cents"`
	DepositCents   money.Cents `json:"deposit_cents"`
	DueDay         int         `json:"due_day"`
	// IRLRefQuarter is the reference quarter, e.g. "2024-Q2"; IRLRefValue its
	// index in hundredths (145,17 → 14517, OPEN-QUESTIONS Q-14).
	IRLRefQuarter string     `json:"irl_ref_quarter"`
	IRLRefValue   int64      `json:"irl_ref_value"`
	RevisionMonth time.Month `json:"revision_month"`
	// RentExemption lifts the relocation rule when recorded (Q-28).
	RentExemption RentExemption `json:"rent_exemption,omitempty"`
	// RentBreakdown optionally splits RentCents by asset, for reporting from
	// phase 3; tax ignores it, since all assets of a lease share one ownership.
	RentBreakdown []AssetRent `json:"rent_breakdown,omitempty"`
}

// RentExemption is a recorded reason for a new lease's rent to exceed the
// relocation rule.
type RentExemption string

// Rent exemptions (rent_control.relocation_rule).
const (
	ExemptionNone        RentExemption = ""
	ExemptionWorks       RentExemption = "works"
	ExemptionUnderValued RentExemption = "under_valued"
)

// AssetRent is the part of the lease's rent attributed to one asset.
type AssetRent struct {
	AssetID   string      `json:"asset_id"`
	RentCents money.Cents `json:"rent_cents"`
}

// ControlledZone reports whether any asset of the lease is in a
// rent-controlled zone, which makes rent_control.zone_rules apply.
func (l Lease) ControlledZone() bool {
	for _, a := range l.Assets {
		if a.ControlledZone {
			return true
		}
	}
	return false
}

// Covers reports whether the lease includes the asset.
func (l Lease) Covers(assetID string) bool {
	for _, a := range l.Assets {
		if a.ID == assetID {
			return true
		}
	}
	return false
}

// PrincipalAsset returns the asset a lease's rent is attributed to: its first
// dwelling, or its first asset when it lets only a box (Q-32: valid because
// every asset of a lease has the same ownership shares).
func (l Lease) PrincipalAsset() (portfolio.Asset, bool) {
	for _, a := range l.Assets {
		if a.Dwelling() {
			return a, true
		}
	}
	if len(l.Assets) > 0 {
		return l.Assets[0], true
	}
	return portfolio.Asset{}, false
}
