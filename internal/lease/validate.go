package lease

import (
	"fmt"
	"sort"

	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/period"
	"github.com/ovander/tenure-api/internal/portfolio"
)

// Problem types of the lease rules checked at save (RENT-BE-001 §3, §8).
const (
	TypeConsistentShares     = "lease/consistent-ownership-shares"
	TypeRentBreakdown        = "lease/rent-breakdown-mismatch"
	TypeZoneCeilingExceeded  = "lease/zone-ceiling-exceeded"
	TypeZoneCeilingUnknown   = "lease/zone-ceiling-unknown"
	TypeRelocationExceeded   = "lease/relocation-rule-exceeded"
	TypeRentExemptionUnknown = "lease/rent-exemption-unknown"
)

// Event is what a lease is being validated for.
type Event string

// Validation events.
const (
	EventCreation Event = "creation"
	EventRenewal  Event = "renewal"
	EventUpdate   Event = "update"
)

// Previous describes the asset's previous lease for the relocation rule: its
// last rent, the IRL value that rent was set on, and the latest published
// value of the same quarter at the new lease's start.
type Previous struct {
	RentCents      money.Cents `json:"rent_cents"`
	IRLRefValue    int64       `json:"irl_ref_value"`
	IRLLatestValue int64       `json:"irl_latest_value"`
}

// Check is the context of a validation.
type Check struct {
	Event Event
	// AsOf selects the parameters: the lease start for a creation, the
	// renewal date for a renewal.
	AsOf period.Date
	// Previous is set for a creation on an asset that was let before.
	Previous *Previous
}

// Validate returns every lease rule l breaks (nil when none):
//   - all assets carry the same ownership shares (Q-32);
//   - a rent breakdown, when present, adds up to the rent;
//   - at creation and renewal, the rent of controlled-zone dwellings does not
//     exceed rent_control.zone_ceiling (a missing zone, category, surface or
//     table value is refused, Q-33);
//   - at creation, on an asset in a zone tendue let before, the rent does not
//     exceed the previous rent revised by IRL when
//     rent_control.relocation_rule applies, unless an exemption is recorded;
//   - at creation, the deposit respects deposit_max_months for the kind
//     (none for a mobility lease, no cap for a box let alone, Q-26) and a
//     mobility lease's term is within mobility_term_months (RENT-SPEC-008).
func Validate(l Lease, c Check, ps params.Set) ([]*invariant.Error, error) {
	var out []*invariant.Error
	if e := consistentShares(l.Assets); e != nil {
		out = append(out, e)
	}
	switch l.RentExemption {
	case ExemptionNone, ExemptionWorks, ExemptionUnderValued:
	default:
		out = append(out, invariant.New(TypeRentExemptionUnknown, string(l.RentExemption)))
	}
	if len(l.RentBreakdown) > 0 {
		var sum money.Cents
		for _, r := range l.RentBreakdown {
			if !l.Covers(r.AssetID) {
				out = append(out, invariant.New(TypeRentBreakdown, "breakdown names an asset outside the lease"))
			}
			sum += r.RentCents
		}
		if sum != l.RentCents {
			out = append(out, invariant.New(TypeRentBreakdown, fmt.Sprintf("breakdown %d ≠ rent %d", sum, l.RentCents)))
		}
	}
	if c.Event == EventCreation || c.Event == EventRenewal {
		ceiling, applies, e, err := ZoneCeiling(l, c.AsOf, ps)
		if err != nil {
			return nil, err
		}
		switch {
		case e != nil:
			out = append(out, e)
		case applies && ControlledRent(l) > ceiling:
			out = append(out, invariant.New(TypeZoneCeilingExceeded,
				fmt.Sprintf("controlled rent %d above ceiling %d", ControlledRent(l), ceiling)))
		}
	}
	if c.Event == EventCreation {
		e, err := CheckDeposit(l.Kind, l.RentCents, l.DepositCents, c.AsOf, ps)
		if err != nil {
			return nil, err
		}
		if e != nil {
			out = append(out, e)
		}
		if l.Kind == KindMobility {
			r, err := ps.MobilityTerm(c.AsOf)
			if err != nil {
				return nil, err
			}
			if l.TermMonths < r.Min || l.TermMonths > r.Max {
				out = append(out, invariant.New(TypeMobilityTerm, fmt.Sprintf("%d months, allowed %d to %d", l.TermMonths, r.Min, r.Max)))
			}
		}
	}
	if c.Event == EventCreation && c.Previous != nil && l.TenseZone() && l.RentExemption == ExemptionNone {
		rule, err := ps.RelocationRule(c.AsOf)
		if err != nil {
			return nil, err
		}
		if rule.Applies {
			if c.Previous.IRLRefValue <= 0 || c.Previous.IRLLatestValue <= 0 {
				return nil, fmt.Errorf("lease: previous lease index values must be positive")
			}
			limit, err := money.MulDivHalfUp(c.Previous.RentCents, c.Previous.IRLLatestValue, c.Previous.IRLRefValue)
			if err != nil {
				return nil, err
			}
			if l.RentCents > limit {
				out = append(out, invariant.New(TypeRelocationExceeded,
					fmt.Sprintf("rent %d above previous rent revised by IRL %d", l.RentCents, limit)))
			}
		}
	}
	return out, nil
}

// ZoneCeiling returns the rent ceiling of l's controlled-zone dwellings in
// force on asOf: the sum over those dwellings of loyer de référence majoré ×
// surface, each rounded down to the cent. applies is false when no dwelling of
// the lease is in a controlled zone. A controlled dwelling whose zone,
// category, surface or table value is missing returns an invariant error
// rather than no ceiling (fail closed).
func ZoneCeiling(l Lease, asOf period.Date, ps params.Set) (money.Cents, bool, *invariant.Error, error) {
	var controlled []portfolio.Asset
	for _, a := range l.Assets {
		if a.ControlledZone && a.Dwelling() {
			controlled = append(controlled, a)
		}
	}
	if len(controlled) == 0 {
		return 0, false, nil, nil
	}
	table, err := ps.ZoneCeiling(asOf)
	if err != nil {
		return 0, false, nil, err
	}
	var ceiling money.Cents
	for _, a := range controlled {
		perM2, ok := table.PerSquareMetre(a.RentControlZone, a.RentControlCategory)
		if !ok || a.SurfaceHundredthsM2 <= 0 {
			return 0, true, invariant.New(TypeZoneCeilingUnknown,
				fmt.Sprintf("asset %s: no ceiling for zone %q category %q or no surface", a.ID, a.RentControlZone, a.RentControlCategory)), nil
		}
		part, err := money.MulDivFloor(money.Cents(perM2), a.SurfaceHundredthsM2, 100)
		if err != nil {
			return 0, false, nil, err
		}
		ceiling += part
	}
	return ceiling, true, nil, nil
}

// ControlledRent returns the part of l's rent compared with the zone ceiling:
// the breakdown of its controlled-zone dwellings when the lease has one,
// otherwise the whole rent, which is the stricter reading (Q-33).
func ControlledRent(l Lease) money.Cents {
	if len(l.RentBreakdown) == 0 {
		return l.RentCents
	}
	controlled := map[string]bool{}
	for _, a := range l.Assets {
		if a.ControlledZone && a.Dwelling() {
			controlled[a.ID] = true
		}
	}
	var s money.Cents
	for _, r := range l.RentBreakdown {
		if controlled[r.AssetID] {
			s += r.RentCents
		}
	}
	return s
}

func consistentShares(assets []portfolio.Asset) *invariant.Error {
	key := func(sh []portfolio.Share) string {
		c := append([]portfolio.Share(nil), sh...)
		sort.Slice(c, func(i, j int) bool { return c[i].OwnerID < c[j].OwnerID })
		return fmt.Sprint(c)
	}
	for i := 1; i < len(assets); i++ {
		if key(assets[i].Shares) != key(assets[0].Shares) {
			return invariant.New(TypeConsistentShares,
				fmt.Sprintf("asset %s and asset %s have different ownership shares", assets[0].ID, assets[i].ID))
		}
	}
	return nil
}
