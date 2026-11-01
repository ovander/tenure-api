// Package params holds the legal parameter set with effective dates
// (RENT-BE-001 §7). Every legal constant used by a calculation is read from a
// Set as of the date the calculation concerns, so a 2025 figure recomputed in
// 2027 still uses the 2025 values. The Set is plain data: slice 1 loads it
// from fixtures, slice 2 from the legal_params table.
package params

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/ovander/tenure-api/internal/period"
)

// Keys of the parameters the calculation engine reads.
const (
	KeyMicroFoncierThreshold = "micro_foncier_threshold_cents"
	KeyMicroFoncierAllowance = "micro_foncier_allowance_pct"
	KeyForfait222            = "forfait_222_cents"
	KeyDeficitGlobalCap      = "deficit_global_cap_cents"
	KeyDeficitCarryForward   = "deficit_carryforward_years"
	KeyDepositMaxUnfurnished = "deposit_max_months.unfurnished"
	KeyDepositMaxFurnished   = "deposit_max_months.furnished"
	KeyDepositMaxMobility    = "deposit_max_months.mobility"
	KeyRegularisationSpread  = "regularisation_spread_after_months"
	KeyReceiptReminderDays   = "receipt_due_days_before_reminder"
	KeyIRLCap                = "irl_cap"
	KeyRentControlZoneRules  = "rent_control.zone_rules"
	KeyForm2044LineMap       = "form_2044_line_map"
)

// ErrNotFound is returned when no entry of a key is effective at the date
// asked, or when no 2044 map exists for a millésime.
var ErrNotFound = errors.New("params: no effective value")

// Entry is one effective-dated value of a parameter, as stored in legal_params.
type Entry struct {
	Key           string          `json:"key"`
	EffectiveFrom period.Date     `json:"effective_from"`
	Value         json.RawMessage `json:"value"`
	Source        string          `json:"source"`
	SourceURL     string          `json:"source_url"`
}

// Set is an immutable collection of entries.
type Set struct {
	entries map[string][]Entry // sorted by EffectiveFrom ascending
}

// New builds a Set, rejecting an entry without key, date, value or source,
// and two entries of one key with the same effective date (two
// form_2044_line_map entries may share a date when their millésimes differ).
func New(entries ...Entry) (Set, error) {
	s := Set{entries: map[string][]Entry{}}
	seen := map[string]bool{}
	for _, e := range entries {
		if e.Key == "" || e.EffectiveFrom.IsZero() || len(e.Value) == 0 || e.Source == "" {
			return Set{}, fmt.Errorf("params: incomplete entry %q", e.Key)
		}
		id := e.Key + "@" + e.EffectiveFrom.String()
		if e.Key == KeyForm2044LineMap {
			var m LineMap
			if err := json.Unmarshal(e.Value, &m); err != nil || m.Millesime == 0 || len(m.Lines) == 0 ||
				len(m.IncomeLines) == 0 || len(m.InterestLines) == 0 {
				return Set{}, fmt.Errorf("params: invalid %s entry", KeyForm2044LineMap)
			}
			id += fmt.Sprintf("#%d", m.Millesime)
		}
		if seen[id] {
			return Set{}, fmt.Errorf("params: duplicate entry %s", id)
		}
		seen[id] = true
		s.entries[e.Key] = append(s.entries[e.Key], e)
	}
	for k := range s.entries {
		sort.SliceStable(s.entries[k], func(i, j int) bool {
			return s.entries[k][i].EffectiveFrom.Before(s.entries[k][j].EffectiveFrom)
		})
	}
	return s, nil
}

// File is the JSON layout of a fixture file.
type File struct {
	Entries []Entry `json:"entries"`
}

// Load reads one or more fixture files and merges them into one Set.
func Load(readers ...io.Reader) (Set, error) {
	var all []Entry
	for _, r := range readers {
		var f File
		dec := json.NewDecoder(r)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&f); err != nil {
			return Set{}, fmt.Errorf("params: decode: %w", err)
		}
		all = append(all, f.Entries...)
	}
	return New(all...)
}

// Entries returns every entry, ordered by key then effective date.
func (s Set) Entries() []Entry {
	keys := make([]string, 0, len(s.entries))
	for k := range s.entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []Entry
	for _, k := range keys {
		out = append(out, s.entries[k]...)
	}
	return out
}

// Until returns the Set restricted to entries effective on or before d. The
// golden tests of a tax year use it, so a value dated in a later year can never
// change an earlier year's result.
func (s Set) Until(d period.Date) Set {
	var kept []Entry
	for _, e := range s.Entries() {
		if !e.EffectiveFrom.After(d) {
			kept = append(kept, e)
		}
	}
	out, _ := New(kept...) // a subset of a valid set is valid
	return out
}

// With returns a copy of s with extra entries added (used by tests and by
// what-if computations; never by an issued document).
func (s Set) With(extra ...Entry) (Set, error) {
	return New(append(s.Entries(), extra...)...)
}

// Lookup returns the entry of key in force on asOf: the latest one whose
// effective date is not after asOf.
func (s Set) Lookup(key string, asOf period.Date) (Entry, error) {
	list := s.entries[key]
	for i := len(list) - 1; i >= 0; i-- {
		if !list[i].EffectiveFrom.After(asOf) {
			return list[i], nil
		}
	}
	return Entry{}, fmt.Errorf("%w: %s on %s", ErrNotFound, key, asOf)
}

// Decode looks key up as of asOf and decodes its value into v.
func (s Set) Decode(key string, asOf period.Date, v any) error {
	e, err := s.Lookup(key, asOf)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(e.Value, v); err != nil {
		return fmt.Errorf("params: %s: %w", key, err)
	}
	return nil
}

// Int64 returns the integer value of key in force on asOf.
func (s Set) Int64(key string, asOf period.Date) (int64, error) {
	var v int64
	err := s.Decode(key, asOf, &v)
	return v, err
}

// Cap is the national IRL revision cap (irl_cap): when Applies, a revision may
// not raise the rent by more than RateBP basis points.
type Cap struct {
	Applies bool  `json:"applies"`
	RateBP  int64 `json:"rate_bp"`
}

// IRLCap returns the national cap in force on asOf.
func (s Set) IRLCap(asOf period.Date) (Cap, error) {
	var c Cap
	err := s.Decode(KeyIRLCap, asOf, &c)
	return c, err
}

// ZoneRule is the rent-control rule for assets in a controlled zone
// (rent_control.zone_rules, OPEN-QUESTIONS Q-28): a maximum rent variation at
// revision in basis points, or no limit when MaxVariationBP is nil.
type ZoneRule struct {
	MaxVariationBP *int64 `json:"max_variation_bp"`
}

// ZoneRule returns the rent-control rule in force on asOf.
func (s Set) ZoneRule(asOf period.Date) (ZoneRule, error) {
	var z ZoneRule
	err := s.Decode(KeyRentControlZoneRules, asOf, &z)
	return z, err
}

// LineMap is the form 2044 map of one millésime: category → line ("" when the
// category is deliberately not on the form, for example a recoverable charge),
// and the role of each line, so that no line number is written in Go code.
type LineMap struct {
	Millesime int               `json:"millesime"`
	Lines     map[string]string `json:"lines"`
	// IncomeLines are the receipt lines (211, 212, 213 on the form).
	IncomeLines []string `json:"income_lines"`
	// InterestLines are the loan lines (250), which cannot create a deficit
	// against global income.
	InterestLines []string `json:"interest_lines"`
	// SubtractedLines are expense lines that reduce the expenses (230).
	SubtractedLines []string `json:"subtracted_lines"`
}

// Has reports whether line is one of lines.
func Has(lines []string, line string) bool {
	for _, l := range lines {
		if l == line {
			return true
		}
	}
	return false
}

// Form2044LineMap returns the map for millésime (the form's year: income year
// + 1, OPEN-QUESTIONS Q-31). When several entries carry that millésime, the
// latest effective one wins. A missing millésime is ErrNotFound: tax.Lines and
// the tax.annual pre-check refuse to compute without it.
func (s Set) Form2044LineMap(millesime int) (LineMap, error) {
	list := s.entries[KeyForm2044LineMap]
	for i := len(list) - 1; i >= 0; i-- {
		var m LineMap
		if err := json.Unmarshal(list[i].Value, &m); err != nil {
			return LineMap{}, err
		}
		if m.Millesime == millesime {
			return m, nil
		}
	}
	return LineMap{}, fmt.Errorf("%w: %s for millésime %d", ErrNotFound, KeyForm2044LineMap, millesime)
}
