package billing

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/ovander/tenure-api/internal/lease"
	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/period"
)

// Index is a published INSEE IRL value, in hundredths (145,17 → 14517).
type Index struct {
	Quarter     string      `json:"quarter"` // "YYYY-Qn"
	Value       int64       `json:"value"`
	PublishedOn period.Date `json:"published_on"`
}

// Revision errors.
var (
	ErrRevisionIndex   = errors.New("billing: index does not match the lease reference quarter")
	ErrRevisionTooSoon = errors.New("billing: revision before its anniversary")
)

// Revision is the result of an IRL revision (RENT-SPEC-003).
type Revision struct {
	LeaseID     string      `json:"lease_id"`
	Anniversary period.Date `json:"anniversary"`
	// EffectiveFrom is the first period billed at the new rent; there is no
	// retroactive billing of earlier periods (OPEN-QUESTIONS Q-13).
	EffectiveFrom period.Period `json:"effective_from"`
	// Forfeited is true when the anniversary is more than 12 months before
	// EffectiveFrom: the revision is lost and the rent is unchanged.
	Forfeited    bool        `json:"forfeited"`
	OldRentCents money.Cents `json:"old_rent_cents"`
	// IndexedRentCents is old × current ÷ reference, before any cap.
	IndexedRentCents money.Cents `json:"indexed_rent_cents"`
	NewRentCents     money.Cents `json:"new_rent_cents"`
	// CappedBy names the rule that limited the rent: "irl_cap" or
	// "rent_control.zone_ceiling"; empty when uncapped.
	CappedBy string `json:"capped_by,omitempty"`
	// CeilingCents is the zone ceiling of a controlled-zone lease (0 when none
	// applies). AboveCeiling is set when the current rent already exceeds it:
	// the revision then leaves the rent unchanged and the owner is told (Q-34).
	CeilingCents  money.Cents `json:"ceiling_cents,omitempty"`
	AboveCeiling  bool        `json:"above_ceiling,omitempty"`
	NewRefQuarter string      `json:"new_ref_quarter"`
	NewRefValue   int64       `json:"new_ref_value"`
}

// Apply returns l with the revised rent and reference index. A forfeited
// revision leaves the rent unchanged and moves the reference to the new index,
// so the next revision starts from it.
func (r Revision) Apply(l lease.Lease) lease.Lease {
	if !r.Forfeited {
		l.RentCents = r.NewRentCents
	}
	l.IRLRefQuarter, l.IRLRefValue = r.NewRefQuarter, r.NewRefValue
	return l
}

// Revise computes the IRL revision of l due on anniversary and first billed in
// from (RENT-SPEC-003, Q-28):
//   - new rent = rent × current IRL ÷ reference IRL, rounded half-up to the
//     cent, where the index variation is capped by irl_cap when one is in force
//     on the anniversary, for every asset;
//   - for a lease with controlled-zone dwellings, the revised rent may not
//     exceed the zone ceiling (lease.ZoneCeiling); a rent already above it is
//     not raised (AboveCeiling);
//   - a revision whose anniversary is more than 12 months before from is
//     forfeited, and none is ever billed retroactively (Q-13).
//
// idx must be the index of the lease's reference quarter, one or more years
// later.
func Revise(l lease.Lease, idx Index, anniversary period.Date, from period.Period, ps params.Set) (Revision, error) {
	refYear, refQ, err := parseQuarter(l.IRLRefQuarter)
	if err != nil {
		return Revision{}, err
	}
	year, q, err := parseQuarter(idx.Quarter)
	if err != nil {
		return Revision{}, err
	}
	if q != refQ || year <= refYear {
		return Revision{}, fmt.Errorf("%w: %s against %s", ErrRevisionIndex, idx.Quarter, l.IRLRefQuarter)
	}
	if l.IRLRefValue <= 0 || idx.Value <= 0 {
		return Revision{}, errors.New("billing: index values must be positive")
	}
	if anniversary.Before(l.StartOn.AddMonths(12)) || from.Last().Before(anniversary) {
		return Revision{}, ErrRevisionTooSoon
	}
	r := Revision{LeaseID: l.ID, Anniversary: anniversary, EffectiveFrom: from, OldRentCents: l.RentCents,
		NewRefQuarter: idx.Quarter, NewRefValue: idx.Value}
	if from.First().After(anniversary.AddMonths(12)) {
		r.Forfeited = true
		r.IndexedRentCents, r.NewRentCents = l.RentCents, l.RentCents
		return r, nil
	}
	c, err := ps.IRLCap(anniversary)
	if err != nil {
		return Revision{}, err
	}
	num, den := idx.Value, l.IRLRefValue
	// Cap the variation itself: current/reference ≤ 1 + rate.
	if c.Applies && idx.Value*10000 > l.IRLRefValue*(10000+c.RateBP) {
		num, den = 10000+c.RateBP, 10000
		r.CappedBy = params.KeyIRLCap
	}
	indexed, err := money.MulDivHalfUp(l.RentCents, num, den)
	if err != nil {
		return Revision{}, err
	}
	r.IndexedRentCents, r.NewRentCents = indexed, indexed

	ceiling, applies, inv, err := lease.ZoneCeiling(l, anniversary, ps)
	if err != nil {
		return Revision{}, err
	}
	if inv != nil {
		return Revision{}, inv
	}
	if applies {
		r.CeilingCents = ceiling
		switch {
		case l.RentCents > ceiling:
			// Already above: no increase, but a falling index still applies.
			r.AboveCeiling = true
			if r.NewRentCents > l.RentCents {
				r.NewRentCents, r.CappedBy = l.RentCents, params.KeyRentControlZoneCeiling
			}
		case r.NewRentCents > ceiling:
			r.NewRentCents, r.CappedBy = ceiling, params.KeyRentControlZoneCeiling
		}
	}
	return r, nil
}

func parseQuarter(s string) (year, q int, err error) {
	y, qs, ok := strings.Cut(s, "-Q")
	year, err1 := strconv.Atoi(y)
	q, err2 := strconv.Atoi(qs)
	if !ok || err1 != nil || err2 != nil || len(y) != 4 || q < 1 || q > 4 {
		return 0, 0, fmt.Errorf("billing: invalid quarter %q", s)
	}
	return year, q, nil
}
