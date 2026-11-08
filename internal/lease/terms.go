package lease

import (
	"sort"

	"github.com/ovander/tenure-api/internal/period"
)

// How a term of a lease began (RENT-SPEC-010).
const (
	RenewalInitial = "initial"
	RenewalTacit   = "tacit"
)

// Where a term stands on the day it is read.
const (
	TermPast     = "past"
	TermCurrent  = "current"
	TermUpcoming = "upcoming"
)

// Term is one period of a lease: the first, or one renewed tacitly for the
// same duration (loi 89-462 art. 10, 25-7). A term the lease's end cuts
// short ends on that day, EndedEarly set.
type Term struct {
	N          int         `json:"n"`
	StartOn    period.Date `json:"start_on" swaggertype:"string" format:"date"`
	EndOn      period.Date `json:"end_on" swaggertype:"string" format:"date"`
	Renewal    string      `json:"renewal" enums:"initial,tacit"`
	Status     string      `json:"status" enums:"past,current,upcoming"`
	EndedEarly bool        `json:"ended_early,omitempty"`
}

// TermFacts is what Terms reads of a lease.
type TermFacts struct {
	StartOn    period.Date
	TermMonths int
	// EndOn is the lease's last day once a notice or its end set one.
	EndOn *period.Date
	// Renews says a term ends by tacit renewal for the same duration: an
	// unfurnished or furnished lease, not a mobility lease, a furnished
	// student lease of its shorter term, or a box (RenewsTacitly).
	Renews bool
}

// TermsView is the terms of a lease as read on a day.
type TermsView struct {
	Terms  []Term `json:"terms"`
	Renews bool   `json:"renews"`
	// NextRenewalOn is the first day of the next tacit renewal, when the
	// lease renews and no end is set.
	NextRenewalOn *period.Date `json:"next_renewal_on,omitempty" swaggertype:"string" format:"date"`
	// BeyondTerm says a lease that does not renew runs past its term with
	// no end set: the owner should record how it goes on.
	BeyondTerm bool `json:"beyond_term,omitempty"`
}

// RenewsTacitly says whether a lease kind renews tacitly for the same term
// (loi 89-462 art. 10 for an unfurnished lease, 25-7 for a furnished one):
// not a mobility lease (art. 25-12), not a furnished lease to a student of
// the shorter student term, and not a box, which is not under loi 89-462.
func RenewsTacitly(kind Kind, termMonths, studentTermMonths int) bool {
	switch kind {
	case KindUnfurnished:
		return true
	case KindFurnished:
		return studentTermMonths <= 0 || termMonths != studentTermMonths
	default:
		return false
	}
}

// Terms lists a lease's terms up to the one running on today, or up to the
// one its end falls in, and the next renewal (RENT-SPEC-010). Pure: the
// caller passes the day and whether the lease renews.
func Terms(f TermFacts, today period.Date) TermsView {
	v := TermsView{Terms: []Term{}, Renews: f.Renews}
	if f.StartOn.IsZero() || f.TermMonths <= 0 {
		return v
	}
	for k := 0; k < 200; k++ {
		if k > 0 && !f.Renews {
			break
		}
		t := Term{N: k + 1, StartOn: f.StartOn.AddMonthsClamped(f.TermMonths * k), EndOn: f.StartOn.AddMonthsClamped(f.TermMonths * (k + 1)).AddDays(-1), Renewal: RenewalInitial}
		if k > 0 {
			t.Renewal = RenewalTacit
		}
		if f.EndOn != nil && f.EndOn.Before(t.StartOn) {
			break
		}
		last := false
		if f.EndOn != nil && !f.EndOn.After(t.EndOn) {
			t.EndedEarly = f.EndOn.Before(t.EndOn)
			t.EndOn = *f.EndOn
			last = true
		}
		switch {
		case t.EndOn.Before(today):
			t.Status = TermPast
		case t.StartOn.After(today):
			t.Status = TermUpcoming
		default:
			t.Status = TermCurrent
		}
		v.Terms = append(v.Terms, t)
		if last {
			return v
		}
		if !t.EndOn.Before(today) {
			if f.Renews {
				next := t.EndOn.AddDays(1)
				v.NextRenewalOn = &next
			}
			return v
		}
	}
	if !f.Renews && f.EndOn == nil && len(v.Terms) > 0 && v.Terms[len(v.Terms)-1].EndOn.Before(today) {
		v.BeyondTerm = true
	}
	return v
}

// Occupancy is a lease of an asset from its first day to its last, open
// while no end is set. A draft lease occupies nothing.
type Occupancy struct {
	StartOn period.Date
	EndOn   *period.Date
}

// Vacancy is a period an asset had no lease, from its first day to its last
// (today while it lasts, Open set).
type Vacancy struct {
	FromOn period.Date `json:"from_on" swaggertype:"string" format:"date"`
	ToOn   period.Date `json:"to_on" swaggertype:"string" format:"date"`
	Days   int         `json:"days"`
	Open   bool        `json:"open,omitempty"`
}

// Vacancies lists the periods an asset had no lease (RENT-SPEC-010), from
// since (its acquisition, or else its first lease's start) to today, and
// the vacant days of each calendar year. Occupancies may overlap. Pure.
func Vacancies(occ []Occupancy, since, today period.Date) ([]Vacancy, map[int]int) {
	out := []Vacancy{}
	byYear := map[int]int{}
	sort.Slice(occ, func(i, j int) bool { return occ[i].StartOn.Before(occ[j].StartOn) })
	if since.IsZero() {
		if len(occ) == 0 {
			return out, byYear
		}
		since = occ[0].StartOn
	}
	add := func(from, to period.Date, open bool) {
		if to.Before(from) {
			return
		}
		v := Vacancy{FromOn: from, ToOn: to, Open: open}
		for d := from; !d.After(to); {
			yearEnd := period.Date{Year: d.Year, Month: 12, Day: 31}
			end := to
			if yearEnd.Before(end) {
				end = yearEnd
			}
			n := int(end.Time().Sub(d.Time()).Hours()/24) + 1
			byYear[d.Year] += n
			v.Days += n
			d = end.AddDays(1)
		}
		out = append(out, v)
	}
	cursor := since
	for _, o := range occ {
		if o.StartOn.After(today) {
			break
		}
		if o.StartOn.After(cursor) {
			to := o.StartOn.AddDays(-1)
			if to.After(today) {
				to = today
			}
			add(cursor, to, false)
		}
		if o.EndOn == nil {
			return out, byYear
		}
		if next := o.EndOn.AddDays(1); next.After(cursor) {
			cursor = next
		}
	}
	if !cursor.After(today) {
		add(cursor, today, true)
	}
	return out, byYear
}
