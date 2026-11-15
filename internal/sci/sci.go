// Package sci keeps the register of an SCI at the income tax (RENT-SPEC-019,
// part 19, Q-133): its associates, the dated holdings of its parts, and the
// movements (subscription, transfer, gift, succession) that open and close
// them. The SCI itself is an owner scope (portfolio.CreateEntity). The pure
// functions below take the holdings and a day; they read nothing else.
package sci

import (
	"github.com/google/uuid"

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
