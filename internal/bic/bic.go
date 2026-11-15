// Package bic holds furnished letting under the réel regime (RENT-SPEC-019,
// LMNP, Q-132): the component grid of an asset, its furniture and works,
// and their depreciation. Depreciation and Capitalise are pure: the caller
// passes the grid, the items, the year and the parameters.
package bic

import (
	"errors"
	"fmt"
	"sort"

	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/period"
)

// FullShare is the sum of a grid's shares, in basis points.
const FullShare = 10_000

// Line is one component of a grid: its share of the depreciable basis in
// basis points and its duration in months; a duration of 0 is not
// depreciated (the land).
type Line struct {
	Component string `json:"component"`
	ShareBP   int64  `json:"share_bp"`
	Months    int    `json:"months"`
}

// Grid is an asset's component grid: the depreciable basis (the price, with
// the acquisition costs unless expensed) spread over its lines from the day
// the asset was first let furnished.
type Grid struct {
	InServiceOn period.Date `json:"in_service_on" swaggertype:"string" format:"date"`
	BasisCents  money.Cents `json:"basis_cents"`
	Lines       []Line      `json:"lines"`
}

// Item is furniture or works capitalised on an asset, depreciated from the
// day put in service over its months, until disposed of if it is.
type Item struct {
	ID          string       `json:"id"`
	Label       string       `json:"label"`
	CostCents   money.Cents  `json:"cost_cents"`
	InServiceOn period.Date  `json:"in_service_on" swaggertype:"string" format:"date"`
	Months      int          `json:"months"`
	DisposedOn  *period.Date `json:"disposed_on,omitempty" swaggertype:"string" format:"date"`
}

// Row is one depreciable line's figures for a year.
type Row struct {
	// Ref is the component, or the item's id.
	Ref         string      `json:"ref"`
	Label       string      `json:"label,omitempty"`
	BaseCents   money.Cents `json:"base_cents"`
	Months      int         `json:"months"`
	StartOn     period.Date `json:"start_on" swaggertype:"string" format:"date"`
	EndOn       period.Date `json:"end_on" swaggertype:"string" format:"date"`
	PriorCents  money.Cents `json:"prior_cents"`
	YearCents   money.Cents `json:"year_cents"`
	NetCents    money.Cents `json:"net_cents"`
	Depreciable bool        `json:"depreciable"`
}

// ErrGrid is returned for a grid whose shares do not add up to FullShare,
// or with a negative share, basis or duration.
var ErrGrid = errors.New("bic: invalid grid")

// Validate checks a grid's shares and durations.
func (g Grid) Validate() error {
	if g.BasisCents < 0 {
		return fmt.Errorf("%w: negative basis", ErrGrid)
	}
	var sum int64
	seen := map[string]bool{}
	for _, l := range g.Lines {
		if l.ShareBP < 0 || l.Months < 0 || l.Component == "" {
			return fmt.Errorf("%w: line %q", ErrGrid, l.Component)
		}
		if seen[l.Component] {
			return fmt.Errorf("%w: %q twice", ErrGrid, l.Component)
		}
		seen[l.Component] = true
		sum += l.ShareBP
	}
	if sum != FullShare {
		return fmt.Errorf("%w: shares add up to %d", ErrGrid, sum)
	}
	return nil
}

// Bases spreads the basis over the lines by share, to the cent half-up, the
// last line taking what rounding leaves so the lines add up to the basis.
func (g Grid) Bases() []money.Cents {
	out := make([]money.Cents, len(g.Lines))
	var given money.Cents
	for i, l := range g.Lines {
		if i == len(g.Lines)-1 {
			out[i] = g.BasisCents - given
			break
		}
		out[i] = money.Cents(halfUp(int64(g.BasisCents)*l.ShareBP, FullShare))
		given += out[i]
	}
	return out
}

func halfUp(num, den int64) int64 {
	if den == 0 {
		return 0
	}
	return (2*num + den) / (2 * den)
}

func days(from, to period.Date) int64 {
	if to.Before(from) {
		return 0
	}
	return int64(to.Time().Sub(from.Time()).Hours()/24) + 1
}

// schedule is a line's straight-line depreciation by days: base over the
// days from start to its end, the accumulated amount at the end of a day
// being base × days elapsed ÷ days of life, to the cent half-up, so the last
// day closes the line exactly.
type schedule struct {
	base       money.Cents
	start, end period.Date
	stop       *period.Date
}

func (s schedule) accumulatedAt(day period.Date) money.Cents {
	if day.Before(s.start) {
		return 0
	}
	last := s.end
	if s.stop != nil && s.stop.Before(last) {
		last = *s.stop
	}
	if day.After(last) {
		day = last
	}
	return money.Cents(halfUp(int64(s.base)*days(s.start, day), days(s.start, s.end)))
}

func row(ref, label string, base money.Cents, months int, start period.Date, stop *period.Date, year int) Row {
	r := Row{Ref: ref, Label: label, BaseCents: base, Months: months, StartOn: start, Depreciable: months > 0 && !start.IsZero()}
	if !r.Depreciable {
		r.NetCents = base
		return r
	}
	s := schedule{base: base, start: start, end: start.AddMonthsClamped(months).AddDays(-1), stop: stop}
	r.EndOn = s.end
	prior := s.accumulatedAt(period.NewDate(year-1, 12, 31))
	upTo := s.accumulatedAt(period.NewDate(year, 12, 31))
	r.PriorCents, r.YearCents, r.NetCents = prior, upTo-prior, base-upTo
	if stop != nil && !stop.After(period.NewDate(year, 12, 31)) {
		// Disposed of: what is left is no longer carried.
		r.NetCents = 0
	}
	return r
}

// Depreciation gives the depreciation of a calendar year (RENT-SPEC-019,
// Q-132): one row per line of the grid, then one per capitalised item in
// their order. Each line is depreciated straight-line by days, from the day
// the asset was first let furnished (an item: put in service) over its
// months; the accumulated amount at a year's end is the base times the days
// elapsed over the days of life, to the cent half-up, so the year's amount
// is the difference and the last day closes the line. A line of 0 months
// (the land) is not depreciated. An item disposed of stops on that day.
func Depreciation(g *Grid, items []Item, year int) ([]Row, error) {
	out := []Row{}
	if g != nil {
		if err := g.Validate(); err != nil {
			return nil, err
		}
		bases := g.Bases()
		for i, l := range g.Lines {
			out = append(out, row(l.Component, "", bases[i], l.Months, g.InServiceOn, nil, year))
		}
	}
	sorted := append([]Item(nil), items...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if c := sorted[i].InServiceOn.Compare(sorted[j].InServiceOn); c != 0 {
			return c < 0
		}
		return sorted[i].ID < sorted[j].ID
	})
	for _, it := range sorted {
		if it.CostCents < 0 || it.Months <= 0 {
			return nil, fmt.Errorf("%w: item %s", ErrGrid, it.ID)
		}
		out = append(out, row(it.ID, it.Label, it.CostCents, it.Months, it.InServiceOn, it.DisposedOn, year))
	}
	return out, nil
}

// Total is the sum of the rows' amounts for the year.
func Total(rows []Row) money.Cents {
	var t money.Cents
	for _, r := range rows {
		t += r.YearCents
	}
	return t
}

// Treatments of an item.
const (
	TreatmentAsset   = "asset"
	TreatmentExpense = "expense"
)

// Capitalise says how an item is treated (Q-132). Its nature decides
// first: works of upkeep or repair are an expense; improvement and
// construction works are fixed assets whatever their cost. The threshold
// then applies to furniture and small equipment only: under it, the item is
// expensed. An override set by the owner wins.
func Capitalise(kind, works string, cost money.Cents, thresholdCents int64, override string) string {
	if override == TreatmentAsset || override == TreatmentExpense {
		return override
	}
	if kind == KindWorks {
		if works == WorksRepair {
			return TreatmentExpense
		}
		return TreatmentAsset
	}
	if int64(cost) < thresholdCents {
		return TreatmentExpense
	}
	return TreatmentAsset
}

// Kinds of item, and the nature of works (as an incident's treatment,
// Q-130).
const (
	KindFurniture = "furniture"
	KindWorks     = "works"

	WorksRepair       = "repair"
	WorksImprovement  = "improvement"
	WorksConstruction = "construction"
)
