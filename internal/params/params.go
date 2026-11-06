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
	KeyMicroFoncierThreshold  = "micro_foncier_threshold_cents"
	KeyMicroFoncierAllowance  = "micro_foncier_allowance_pct"
	KeyForfait222             = "forfait_222_cents"
	KeyDeficitGlobalCap       = "deficit_global_cap_cents"
	KeyDeficitCarryForward    = "deficit_carryforward_years"
	KeyCompletionWindowDays   = "inventory.completion_window_days"
	KeyHeatingWindowMonths    = "inventory.heating_window_months"
	KeyHeatingSeasonStart     = "heating_season.start_month"
	KeyHeatingSeasonEnd       = "heating_season.end_month"
	KeyDepositMaxUnfurnished  = "deposit_max_months.unfurnished"
	KeyDepositMaxFurnished    = "deposit_max_months.furnished"
	KeyDepositMaxMobility     = "deposit_max_months.mobility"
	KeyRegularisationSpread   = "regularisation_spread_after_months"
	KeyReceiptReminderDays    = "receipt_due_days_before_reminder"
	KeyIRLCap                 = "irl_cap"
	KeyRentControlZoneCeiling = "rent_control.zone_ceiling"
	KeyRentControlRelocation  = "rent_control.relocation_rule"
	KeyForm2044LineMap        = "form_2044_line_map"
	KeyDepositWarningBox      = "deposit_warning_months.box"
	KeyDiagnostics            = "diagnostics"
	KeyRentalMinDPEClass      = "rental_min_dpe_class"
	KeyMobilityTerm           = "mobility_term_months"
	KeyLeaseTerm              = "lease_term_months"
	KeySupportingDocsMonths   = "regularisation_supporting_docs_months"
	KeyReelLockYears          = "reel_option_lock_years"
	// Deposit restitution (RENT-INV-001 §6, §7), seeded by inventory slice 2.
	KeyDepositReturnConforming     = "deposit.return_months.conforming"
	KeyDepositReturnWithDeductions = "deposit.return_months.with_deductions"
	KeyDepositCollectiveHoldback   = "deposit.collective_holdback_bp"
	KeyDepositHoldbackBalance      = "deposit.holdback_balance_months_after_approval"
	KeyDepositLatePenalty          = "deposit.late_penalty_bp_of_rent"
)

// ErrNotFound is returned when no entry of a key is effective at the date
// asked, or when no 2044 map exists for a millésime.
var ErrNotFound = errors.New("params: no effective value")

// ErrDuplicate is returned by New for two entries of one key, date (and, for
// the 2044 map, income year).
var ErrDuplicate = errors.New("params: duplicate entry")

// Entry is one effective-dated value of a parameter, as stored in legal_params.
type Entry struct {
	Key           string          `json:"key"`
	EffectiveFrom period.Date     `json:"effective_from" swaggertype:"string" format:"date"`
	Value         json.RawMessage `json:"value" swaggertype:"object"`
	Source        string          `json:"source"`
	SourceURL     string          `json:"source_url"`
}

// Set is an immutable collection of entries.
type Set struct {
	entries map[string][]Entry // sorted by EffectiveFrom ascending
}

// New builds a Set, rejecting an entry without key, date, value or source,
// and two entries of one key with the same effective date (two
// form_2044_line_map entries may share a date when their income years differ).
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
			if err := json.Unmarshal(e.Value, &m); err != nil || m.TaxYear == 0 || m.Millesime == 0 ||
				len(m.Lines) == 0 || len(m.IncomeLines) == 0 || len(m.InterestLines) == 0 {
				return Set{}, fmt.Errorf("params: invalid %s entry", KeyForm2044LineMap)
			}
			id += fmt.Sprintf("#%d", m.TaxYear)
		}
		if seen[id] {
			return Set{}, fmt.Errorf("%w %s", ErrDuplicate, id)
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

// Cap is the national cap on the IRL variation itself (irl_cap), as the
// 2022–2024 « bouclier » was: when Applies, the index variation used in a
// revision may not exceed RateBP basis points, whatever the asset.
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

// ZoneCeiling is the rent-control ceiling table (rent_control.zone_ceiling):
// the loyer de référence majoré in cents per m² per month, by zone and then by
// category. It applies only to assets with controlled_zone set; the
// relocation rule applies to tense assets (Q-102).
type ZoneCeiling struct {
	Zones map[string]map[string]int64 `json:"zones"`
}

// PerSquareMetre returns the ceiling of zone and category, and false when the
// table has no value for them.
func (z ZoneCeiling) PerSquareMetre(zone, category string) (int64, bool) {
	v, ok := z.Zones[zone][category]
	return v, ok
}

// ZoneCeiling returns the ceiling table in force on asOf.
func (s Set) ZoneCeiling(asOf period.Date) (ZoneCeiling, error) {
	var z ZoneCeiling
	err := s.Decode(KeyRentControlZoneCeiling, asOf, &z)
	return z, err
}

// RelocationRule is rent_control.relocation_rule: when Applies, the rent of a
// new lease on a controlled-zone asset may not exceed the previous lease's
// rent revised by IRL, unless the lease records an exemption.
type RelocationRule struct {
	Applies bool `json:"applies"`
}

// RelocationRule returns the relocation rule in force on asOf.
func (s Set) RelocationRule(asOf period.Date) (RelocationRule, error) {
	var r RelocationRule
	err := s.Decode(KeyRentControlRelocation, asOf, &r)
	return r, err
}

// Diagnostics is the diagnostics rule (Q-23): the kinds a lease needs by
// asset family ("dwelling", "box"), the validity of each kind in months (0:
// no expiry), and the date before which a DPE is void whatever its age.
type Diagnostics struct {
	Required       map[string][]string `json:"required"`
	ValidityMonths map[string]int      `json:"validity_months"`
	DPEMinDoneOn   period.Date         `json:"dpe_min_done_on"`
}

// Diagnostics returns the diagnostics rule in force on asOf.
func (s Set) Diagnostics(asOf period.Date) (Diagnostics, error) {
	var d Diagnostics
	err := s.Decode(KeyDiagnostics, asOf, &d)
	return d, err
}

// MinDPEClass returns the worst DPE class a dwelling may have to be let on
// asOf (« A » best, « G » worst).
func (s Set) MinDPEClass(asOf period.Date) (string, error) {
	var c string
	err := s.Decode(KeyRentalMinDPEClass, asOf, &c)
	return c, err
}

// DepositReturn holds the deposit restitution rules (loi 89-462 art. 22,
// RENT-INV-001 §6): the months after the return of the keys, without and with
// a retained deduction; the holdback allowed in a collective building, in
// basis points of the deposit, and the months after the approval of the
// accounts to pay its balance; the late penalty per started month, in basis
// points of the monthly rent excluding charges.
type DepositReturn struct {
	ConformingMonths      int
	WithDeductionsMonths  int
	CollectiveHoldbackBP  int64
	HoldbackBalanceMonths int
	LatePenaltyBP         int64
}

// DepositReturn returns the restitution rules in force on asOf; every key
// must be present.
func (s Set) DepositReturn(asOf period.Date) (DepositReturn, error) {
	var r DepositReturn
	ints := []struct {
		key string
		dst *int64
	}{{KeyDepositCollectiveHoldback, &r.CollectiveHoldbackBP}, {KeyDepositLatePenalty, &r.LatePenaltyBP}}
	for _, x := range ints {
		v, err := s.Int64(x.key, asOf)
		if err != nil {
			return DepositReturn{}, err
		}
		*x.dst = v
	}
	months := []struct {
		key string
		dst *int
	}{{KeyDepositReturnConforming, &r.ConformingMonths}, {KeyDepositReturnWithDeductions, &r.WithDeductionsMonths},
		{KeyDepositHoldbackBalance, &r.HoldbackBalanceMonths}}
	for _, x := range months {
		v, err := s.Int64(x.key, asOf)
		if err != nil {
			return DepositReturn{}, err
		}
		*x.dst = int(v)
	}
	return r, nil
}

// Range is an inclusive integer range.
type Range struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

// LeaseTerms are the standard terms of a lease, in months (loi 89-462
// art. 10 and 25-7): what the web form proposes; the parties may agree a
// longer one. A mobility lease has its range in MobilityTerm.
type LeaseTerms struct {
	Unfurnished           int `json:"unfurnished"`
	UnfurnishedLegalOwner int `json:"unfurnished_legal_owner"`
	Furnished             int `json:"furnished"`
	FurnishedStudent      int `json:"furnished_student"`
}

// LeaseTerm returns the standard lease terms in force on asOf.
func (s Set) LeaseTerm(asOf period.Date) (LeaseTerms, error) {
	var t LeaseTerms
	err := s.Decode(KeyLeaseTerm, asOf, &t)
	return t, err
}

// MobilityTerm returns the allowed term of a bail mobilité, in months.
func (s Set) MobilityTerm(asOf period.Date) (Range, error) {
	var r Range
	err := s.Decode(KeyMobilityTerm, asOf, &r)
	return r, err
}

// LineMap is the form 2044 map of one millésime: category → line ("" when the
// category is deliberately not on the form, for example a recoverable charge),
// and the role of each line, so that no line number is written in Go code.
type LineMap struct {
	// TaxYear is the income year the map is for; it is the lookup key.
	TaxYear int `json:"tax_year"`
	// Millesime is the form's year (TaxYear + 1), a label only.
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

// Form2044LineMap returns the map for income year taxYear (OPEN-QUESTIONS
// Q-31: every tax parameter is keyed by income year, the millésime is a
// label). When several entries carry that year, the latest effective one wins.
// A missing year is ErrNotFound: tax.Lines and the tax.annual pre-check refuse
// to compute without it.
func (s Set) Form2044LineMap(taxYear int) (LineMap, error) {
	list := s.entries[KeyForm2044LineMap]
	for i := len(list) - 1; i >= 0; i-- {
		var m LineMap
		if err := json.Unmarshal(list[i].Value, &m); err != nil {
			return LineMap{}, err
		}
		if m.TaxYear == taxYear {
			return m, nil
		}
	}
	return LineMap{}, fmt.Errorf("%w: %s for income year %d", ErrNotFound, KeyForm2044LineMap, taxYear)
}

// TaxYearEnd is the date the parameters of income year taxYear are read at:
// 31 December of that year.
func TaxYearEnd(taxYear int) period.Date { return period.NewDate(taxYear, 12, 31) }
