// Package period holds the calendar types of the calculation engine: Date, a
// civil date with no time zone, and Period, a billing month written YYYY-MM
// (RENT-BE-001 §2 conventions). Calendar rules are evaluated on civil dates;
// conversion to UTC timestamps happens at the storage boundary.
package period

import (
	"fmt"
	"time"
)

// Date is a civil date (year, month, day) with no time of day or zone.
// The zero Date means "no date", for example an open-ended lease.
type Date struct {
	Year  int
	Month time.Month
	Day   int
}

// NewDate returns the date y-m-d, normalised as time.Date does.
func NewDate(y int, m time.Month, d int) Date {
	return FromTime(time.Date(y, m, d, 0, 0, 0, 0, time.UTC))
}

// FromTime returns the civil date of t in t's location.
func FromTime(t time.Time) Date {
	y, m, d := t.Date()
	return Date{y, m, d}
}

// ParseDate parses a YYYY-MM-DD date.
func ParseDate(s string) (Date, error) {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return Date{}, fmt.Errorf("period: invalid date %q", s)
	}
	return FromTime(t), nil
}

// IsZero reports whether d is the zero Date.
func (d Date) IsZero() bool { return d == Date{} }

// Time returns d at midnight UTC.
func (d Date) Time() time.Time { return time.Date(d.Year, d.Month, d.Day, 0, 0, 0, 0, time.UTC) }

// String formats d as YYYY-MM-DD, or "" for the zero Date.
func (d Date) String() string {
	if d.IsZero() {
		return ""
	}
	return d.Time().Format(time.DateOnly)
}

// Compare returns -1, 0 or +1 as d is before, equal to or after e.
func (d Date) Compare(e Date) int { return d.Time().Compare(e.Time()) }

// Before reports whether d is before e.
func (d Date) Before(e Date) bool { return d.Compare(e) < 0 }

// After reports whether d is after e.
func (d Date) After(e Date) bool { return d.Compare(e) > 0 }

// AddDays returns d plus n days.
func (d Date) AddDays(n int) Date { return FromTime(d.Time().AddDate(0, 0, n)) }

// AddMonths returns d plus n calendar months, normalised as time.AddDate does.
func (d Date) AddMonths(n int) Date { return FromTime(d.Time().AddDate(0, n, 0)) }

// Period returns the billing month that contains d.
func (d Date) Period() Period { return Period{d.Year, d.Month} }

// MarshalText implements encoding.TextMarshaler.
func (d Date) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler; an empty text is the zero Date.
func (d *Date) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*d = Date{}
		return nil
	}
	v, err := ParseDate(string(b))
	if err != nil {
		return err
	}
	*d = v
	return nil
}

// DaysInclusive returns the number of days from start to end, both included,
// or 0 when end is before start.
func DaysInclusive(start, end Date) int {
	if end.Before(start) {
		return 0
	}
	return int(end.Time().Sub(start.Time()).Hours()/24) + 1
}

// OverlapDays returns the number of days, both ends included, shared by the
// ranges [aStart, aEnd] and [bStart, bEnd]. A zero end date means open-ended;
// at least one of the two ranges must be bounded.
func OverlapDays(aStart, aEnd, bStart, bEnd Date) int {
	start := aStart
	if bStart.After(start) {
		start = bStart
	}
	end := aEnd
	if end.IsZero() || (!bEnd.IsZero() && bEnd.Before(end)) {
		end = bEnd
	}
	if end.IsZero() {
		panic("period: OverlapDays needs at least one bounded range")
	}
	return DaysInclusive(start, end)
}

// Period is a billing month.
type Period struct {
	Year  int
	Month time.Month
}

// Parse parses a YYYY-MM period.
func Parse(s string) (Period, error) {
	t, err := time.Parse("2006-01", s)
	if err != nil || len(s) != 7 {
		return Period{}, fmt.Errorf("period: invalid period %q", s)
	}
	return Period{t.Year(), t.Month()}, nil
}

// String formats p as YYYY-MM.
func (p Period) String() string { return fmt.Sprintf("%04d-%02d", p.Year, int(p.Month)) }

// First returns the first day of p.
func (p Period) First() Date { return Date{p.Year, p.Month, 1} }

// Last returns the last day of p.
func (p Period) Last() Date { return p.First().AddMonths(1).AddDays(-1) }

// Days returns the number of days in p.
func (p Period) Days() int { return p.Last().Day }

// Next returns the following period.
func (p Period) Next() Period { return p.First().AddMonths(1).Period() }

// Compare returns -1, 0 or +1 as p is before, equal to or after q.
func (p Period) Compare(q Period) int { return p.First().Compare(q.First()) }

// MarshalText implements encoding.TextMarshaler.
func (p Period) MarshalText() ([]byte, error) { return []byte(p.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (p *Period) UnmarshalText(b []byte) error {
	v, err := Parse(string(b))
	if err != nil {
		return err
	}
	*p = v
	return nil
}
