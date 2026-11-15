// Package sci keeps the register of an SCI at the income tax (RENT-SPEC-019,
// part 19, Q-133): its associates, the dated holdings of its parts, and the
// movements (subscription, transfer, gift, succession) that open and close
// them. The SCI itself is an owner scope (portfolio.CreateEntity). The pure
// functions below take the holdings and a day; they read nothing else.
package sci

import (
	"errors"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/period"
)

// Holding is a number of parts an associate holds from From, up to the day
// before Closed (nil while held).
type Holding struct {
	ID        uuid.UUID
	Associate uuid.UUID
	Parts     int
	From      period.Date
	Closed    *period.Date
}

// HeldOn reports whether the holding covers the day.
func (h Holding) HeldOn(day period.Date) bool {
	return !h.From.After(day) && (h.Closed == nil || h.Closed.After(day))
}

// PartsOn is the number of parts each associate holds on the day, and their
// total. The split of the year's result uses it on 31 December (Q-133).
func PartsOn(hs []Holding, day period.Date) (map[uuid.UUID]int, int) {
	by := map[uuid.UUID]int{}
	total := 0
	for _, h := range hs {
		if h.HeldOn(day) {
			by[h.Associate] += h.Parts
			total += h.Parts
		}
	}
	return by, total
}

// MaxHeldFrom is the largest number of parts held on any day from the given
// one on: the total changes only on a holding's first day, so those days and
// the given one are the ones to look at.
func MaxHeldFrom(hs []Holding, day period.Date) int {
	_, best := PartsOn(hs, day)
	for _, h := range hs {
		if h.From.After(day) {
			if _, n := PartsOn(hs, h.From); n > best {
				best = n
			}
		}
	}
	return best
}

// Figures is an SCI's result of a year by nature, computed with the rules of
// the 2044 réel: its income, its loan interest and its other expenses; Net is
// income less both.
type Figures struct {
	IncomeCents        money.Cents `json:"income_cents"`
	InterestCents      money.Cents `json:"interest_cents"`
	OtherExpensesCents money.Cents `json:"other_expenses_cents"`
	NetCents           money.Cents `json:"net_cents"`
}

// Holder is an associate holding parts on the day the result is split.
type Holder struct {
	Associate uuid.UUID `json:"associate_id"`
	Parts     int       `json:"parts"`
}

// Allocation is one associate's share of the result.
type Allocation struct {
	Holder
	Figures
}

// ErrNoParts is returned when nobody holds a part on the day of the split.
var ErrNoParts = errors.New("sci: no part held on the day of the split")

// Allocate splits the result between the holders by their parts (Q-133):
// each figure is split by largest remainder (money.Split, ties to the
// earlier holder in register order), so the shares of every figure add up
// to it exactly; a negative figure is split as its opposite. Each holder's
// net is their income less their interest and other expenses, so the nets
// add up to the SCI's net too. A holder with no part is left out.
func Allocate(f Figures, holders []Holder) ([]Allocation, error) {
	var hs []Holder
	weights := []int64{}
	for _, h := range holders {
		if h.Parts > 0 {
			hs = append(hs, h)
			weights = append(weights, int64(h.Parts))
		}
	}
	if len(hs) == 0 {
		return nil, ErrNoParts
	}
	split := func(c money.Cents) ([]money.Cents, error) {
		neg := c < 0
		if neg {
			c = -c
		}
		parts, err := money.Split(c, weights)
		if err != nil {
			return nil, err
		}
		if neg {
			for i := range parts {
				parts[i] = -parts[i]
			}
		}
		return parts, nil
	}
	income, err := split(f.IncomeCents)
	if err != nil {
		return nil, err
	}
	interest, err := split(f.InterestCents)
	if err != nil {
		return nil, err
	}
	other, err := split(f.OtherExpensesCents)
	if err != nil {
		return nil, err
	}
	out := make([]Allocation, len(hs))
	for i, h := range hs {
		out[i] = Allocation{Holder: h, Figures: Figures{IncomeCents: income[i], InterestCents: interest[i], OtherExpensesCents: other[i],
			NetCents: income[i] - interest[i] - other[i]}}
	}
	return out, nil
}
