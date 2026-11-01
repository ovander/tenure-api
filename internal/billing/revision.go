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
	// "rent_control.zone_rules"; empty when uncapped.
	CappedBy      string `json:"capped_by,omitempty"`
	NewRefQuarter string `json:"new_ref_quarter"`
	NewRefValue   int64  `json:"new_ref_value"`
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
// from (RENT-SPEC-003): new rent = rent × current IRL ÷ reference IRL, rounded
// half-up to the cent, then capped by irl_cap and, when an asset of the lease
// is in a controlled zone, by rent_control.zone_rules, both in force on the
// anniversary (Q-12, Q-28); the stricter cap wins. idx must be the index of
// the lease's reference quarter, one or more years later. A revision whose
// anniversary is more than 12 months before from is forfeited (Q-13).
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
	indexed, err := money.MulDivHalfUp(l.RentCents, idx.Value, l.IRLRefValue)
	if err != nil {
		return Revision{}, err
	}
	r.IndexedRentCents, r.NewRentCents = indexed, indexed

	limit := func(bp int64, rule string) error {
		ceiling, err := money.MulDivHalfUp(l.RentCents, 10000+bp, 10000)
		if err != nil {
			return err
		}
		if r.NewRentCents > ceiling {
			r.NewRentCents, r.CappedBy = ceiling, rule
		}
		return nil
	}
	c, err := ps.IRLCap(anniversary)
	if err != nil {
		return Revision{}, err
	}
	if c.Applies {
		if err := limit(c.RateBP, params.KeyIRLCap); err != nil {
			return Revision{}, err
		}
	}
	if l.ControlledZone() {
		z, err := ps.ZoneRule(anniversary)
		if err != nil {
			return Revision{}, err
		}
		if z.MaxVariationBP != nil {
			if err := limit(*z.MaxVariationBP, params.KeyRentControlZoneRules); err != nil {
				return Revision{}, err
			}
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
