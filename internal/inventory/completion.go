package inventory

import (
	"time"

	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/period"
)

// Reasons of a completion request (completion_reason): within the days
// after the entry, or for the heating within the first month of the heating
// season (loi 89-462 art. 3-2).
const (
	ReasonTenDay  = "ten_day"
	ReasonHeating = "heating"
)

// WindowRules are the parameters of the completion windows, read from
// legal_params as of the entry's date: the days after the entry, the months
// of the heating window, and the heating season's first and last months
// (INV-OD-07, a product rule: the law fixes no season).
type WindowRules struct {
	Days          int `json:"days"`
	HeatingMonths int `json:"heating_months"`
	SeasonStart   int `json:"season_start_month"`
	SeasonEnd     int `json:"season_end_month"`
}

// WindowRulesAt reads the window rules in force on day on.
func WindowRulesAt(ps params.Set, on period.Date) (WindowRules, error) {
	var r WindowRules
	for _, k := range []struct {
		key string
		to  *int
	}{{params.KeyCompletionWindowDays, &r.Days}, {params.KeyHeatingWindowMonths, &r.HeatingMonths},
		{params.KeyHeatingSeasonStart, &r.SeasonStart}, {params.KeyHeatingSeasonEnd, &r.SeasonEnd}} {
		v, err := ps.Int64(k.key, on)
		if err != nil {
			return WindowRules{}, err
		}
		*k.to = int(v)
	}
	return r, nil
}

// Windows are the days a tenant may ask to complete an issued entry, first
// and last day included. The heating window exists only when the entry was
// done outside the heating season (RENT-INV-001 §5 step 4).
type Windows struct {
	TenDayEndsOn    period.Date  `json:"ten_day_ends_on" swaggertype:"string" format:"date"`
	HeatingStartsOn *period.Date `json:"heating_starts_on,omitempty" swaggertype:"string" format:"date"`
	HeatingEndsOn   *period.Date `json:"heating_ends_on,omitempty" swaggertype:"string" format:"date"`
}

// inSeason says whether month m falls within the heating season, which may
// run across the new year (October to May).
func (r WindowRules) inSeason(m time.Month) bool {
	if r.SeasonStart <= r.SeasonEnd {
		return int(m) >= r.SeasonStart && int(m) <= r.SeasonEnd
	}
	return int(m) >= r.SeasonStart || int(m) <= r.SeasonEnd
}

// CompletionWindows returns the windows of an entry done on doneOn: the
// ten-day window ends doneOn + Days; outside the heating season, the heating
// window is the first HeatingMonths of the next season, from its first day.
func CompletionWindows(doneOn period.Date, r WindowRules) Windows {
	w := Windows{TenDayEndsOn: doneOn.AddDays(r.Days)}
	if r.inSeason(doneOn.Month) || r.SeasonStart < 1 || r.SeasonStart > 12 {
		return w
	}
	start := period.NewDate(doneOn.Year, time.Month(r.SeasonStart), 1)
	if start.Before(doneOn) {
		start = period.NewDate(doneOn.Year+1, time.Month(r.SeasonStart), 1)
	}
	end := start.AddMonthsClamped(r.HeatingMonths).AddDays(-1)
	w.HeatingStartsOn, w.HeatingEndsOn = &start, &end
	return w
}

// Open says whether a request made on day on, for reason, falls within its
// window: from the entry's day to the ten-day end, or within the heating
// window.
func (w Windows) Open(reason string, doneOn, on period.Date) bool {
	switch reason {
	case ReasonTenDay:
		return !on.Before(doneOn) && !on.After(w.TenDayEndsOn)
	case ReasonHeating:
		return w.HeatingStartsOn != nil && !on.Before(*w.HeatingStartsOn) && !on.After(*w.HeatingEndsOn)
	}
	return false
}
