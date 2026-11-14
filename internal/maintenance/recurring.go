package maintenance

import "github.com/ovander/tenure-api/internal/period"

// Kinds of recurring maintenance (RENT-SPEC-015, Q-131).
const (
	KindBoiler  = "boiler"
	KindChimney = "chimney"
	KindOther   = "other"
)

// NextDue is the day the next visit of a recurring maintenance is due: the
// first due day while no visit is recorded, then the latest visit plus the
// interval, the month's end clamped. ok is false once the contract has
// ended before that day.
func NextDue(firstDue period.Date, lastDone *period.Date, intervalMonths int, endOn *period.Date) (period.Date, bool) {
	next := firstDue
	if lastDone != nil {
		next = lastDone.AddMonthsClamped(intervalMonths)
	}
	if endOn != nil && endOn.Before(next) {
		return period.Date{}, false
	}
	return next, true
}
