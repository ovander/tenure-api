package billing

import (
	"fmt"

	"github.com/ovander/tenure-api/internal/lease"
	"github.com/ovander/tenure-api/internal/period"
)

// Due is a revision that can be applied.
type Due struct {
	Anniversary period.Date   `json:"anniversary"`
	Index       Index         `json:"index"`
	From        period.Period `json:"from"`
}

// DueRevision reports the annual IRL revision of l that is due on today
// (RENT-SPEC-003, Q-13, Q-52):
//   - the index is the lease's reference quarter one year later; the revision
//     is not due until INSEE has published it (series reports it);
//   - the anniversary is the revision month, on the day of the month the lease
//     started (capped to the month's length), in the index's year or later,
//     and at least 12 months after the start;
//   - the revision applies from the first billing period that starts on or
//     after the anniversary, the index's publication and today, never earlier.
//
// Revise then applies the caps and forfeits a revision claimed more than 12
// months after its anniversary. A lease revised once moves its reference to
// the new index, so the same revision is never due twice.
func DueRevision(l lease.Lease, series func(quarter string) (Index, bool), today period.Date) (Due, bool, error) {
	refYear, q, err := parseQuarter(l.IRLRefQuarter)
	if err != nil {
		return Due{}, false, err
	}
	if l.RevisionMonth < 1 || l.RevisionMonth > 12 || l.StartOn.IsZero() {
		return Due{}, false, fmt.Errorf("billing: lease %s has no revision month or start", l.ID)
	}
	idx, ok := series(fmt.Sprintf("%04d-Q%d", refYear+1, q))
	if !ok {
		return Due{}, false, nil
	}
	first := l.StartOn.AddMonths(12)
	at := func(y int) period.Date {
		p := period.Period{Year: y, Month: l.RevisionMonth}
		return period.Date{Year: y, Month: l.RevisionMonth, Day: min(l.StartOn.Day, p.Days())}
	}
	y := max(first.Year, refYear+1)
	ann := at(y)
	for ann.Before(first) {
		y++
		ann = at(y)
	}
	if today.Before(ann) {
		return Due{}, false, nil
	}
	start := ann
	if idx.PublishedOn.After(start) {
		start = idx.PublishedOn
	}
	if today.After(start) {
		start = today
	}
	from := start.Period()
	if start.Day != 1 {
		from = from.Next()
	}
	return Due{Anniversary: ann, Index: idx, From: from}, true, nil
}
