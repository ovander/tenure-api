package charge

import (
	"errors"
	"fmt"
	"sort"

	"github.com/ovander/tenure-api/internal/lease"
	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/period"
)

// Exercise is one asset's charge exercise: a date range that may differ from
// the calendar year, and the date its regularisation statement is issued.
type Exercise struct {
	AssetID  string      `json:"asset_id"`
	Start    period.Date `json:"start"`
	End      period.Date `json:"end"`
	IssuedOn period.Date `json:"issued_on"`
}

// Days returns the number of days of the exercise.
func (e Exercise) Days() int { return period.DaysInclusive(e.Start, e.End) }

// Line is the reconciliation of one lease.
type Line struct {
	LeaseID         string      `json:"lease_id"`
	Days            int         `json:"days"`
	AllocatedCents  money.Cents `json:"allocated_cents"`
	ProvisionsCents money.Cents `json:"provisions_cents"`
	// BalanceCents = allocated − provisions: positive, the tenant owes;
	// negative, a credit.
	BalanceCents money.Cents `json:"balance_cents"`
	// Spread is set when the statement is issued late and the positive balance
	// is billed in 12 instalments (RENT-SPEC-007, Q-16).
	Spread      bool          `json:"spread"`
	Instalments []money.Cents `json:"instalments,omitempty"`
	// NewProvisionCents is the proposed monthly provision for a lease still
	// running after the exercise; zero for a lease that ended.
	NewProvisionCents money.Cents `json:"new_provision_cents"`
}

// Allocation is the result of reconciling one asset's exercise.
type Allocation struct {
	AssetID             string      `json:"asset_id"`
	ExerciseDays        int         `json:"exercise_days"`
	RecoverableCents    money.Cents `json:"recoverable_cents"`
	NonRecoverableCents money.Cents `json:"non_recoverable_cents"`
	// VacancyDays and VacancyCents are the days with no lease and the
	// recoverable charges they leave with the owner.
	VacancyDays  int         `json:"vacancy_days"`
	VacancyCents money.Cents `json:"vacancy_cents"`
	Lines        []Line      `json:"lines"`
	ChargeIDs    []string    `json:"charge_ids"`
}

// Allocate reconciles the recoverable charges of one asset and exercise with
// the provisions collected (RENT-SPEC-007 steps 3, 5, 7, 8):
//   - every reconciled charge of the asset dated in the exercise counts for its
//     recoverable part;
//   - each lease covering the asset gets a share pro rata temporis by days; with
//     vacancy, shares are rounded down and the vacancy keeps the remainder, so a
//     tenant is never charged a rounding cent; with none, the total is split by
//     largest remainder (Q-15);
//   - balance = allocated − provisions collected (from matched payments, passed
//     in provisions by lease id);
//   - a statement issued more than regularisation_spread_after_months after the
//     exercise end spreads each positive balance over 12 instalments, rounded
//     down, the remainder on the last (Q-16);
//   - the proposed provision is the recoverable total ÷ 12, rounded up to the
//     euro, for every lease still running after the exercise.
func Allocate(charges []Charge, leases []lease.Lease, ex Exercise, provisions map[string]money.Cents, ps params.Set) (Allocation, error) {
	days := ex.Days()
	if days == 0 || ex.IssuedOn.IsZero() {
		return Allocation{}, errors.New("charge: exercise needs a start, an end and an issue date")
	}
	a := Allocation{AssetID: ex.AssetID, ExerciseDays: days}
	for _, c := range charges {
		if c.AssetID != ex.AssetID {
			return Allocation{}, fmt.Errorf("charge: %s belongs to asset %s", c.ID, c.AssetID)
		}
		if !c.Category.Reconciled() {
			continue
		}
		if c.ChargedOn.Before(ex.Start) || c.ChargedOn.After(ex.End) {
			return Allocation{}, fmt.Errorf("charge: %s dated %s is outside the exercise", c.ID, c.ChargedOn)
		}
		if c.AmountCents < 0 || c.RecoverableBP < 0 || c.RecoverableBP > 10000 {
			return Allocation{}, fmt.Errorf("charge: %s has an invalid amount or recoverable share", c.ID)
		}
		rec, err := c.RecoverableCents()
		if err != nil {
			return Allocation{}, err
		}
		a.RecoverableCents += rec
		a.NonRecoverableCents += c.AmountCents - rec
		a.ChargeIDs = append(a.ChargeIDs, c.ID)
	}
	sort.Strings(a.ChargeIDs)

	var covering []lease.Lease
	for _, l := range leases {
		if l.Covers(ex.AssetID) && period.OverlapDays(l.StartOn, l.EndOn, ex.Start, ex.End) > 0 {
			covering = append(covering, l)
		}
	}
	sort.Slice(covering, func(i, j int) bool { return covering[i].StartOn.Before(covering[j].StartOn) })
	occupied := 0
	weights := make([]int64, len(covering))
	for i, l := range covering {
		n := period.OverlapDays(l.StartOn, l.EndOn, ex.Start, ex.End)
		weights[i] = int64(n)
		occupied += n
	}
	if occupied > days {
		return Allocation{}, errors.New("charge: leases of the asset overlap during the exercise")
	}
	a.VacancyDays = days - occupied

	shares := make([]money.Cents, len(covering))
	switch {
	case len(covering) == 0:
	case a.VacancyDays > 0:
		for i, w := range weights {
			s, err := money.MulDivFloor(a.RecoverableCents, w, int64(days))
			if err != nil {
				return Allocation{}, err
			}
			shares[i] = s
		}
	default:
		s, err := money.Split(a.RecoverableCents, weights)
		if err != nil {
			return Allocation{}, err
		}
		shares = s
	}
	a.VacancyCents = a.RecoverableCents - money.Sum(shares...)

	spreadAfter, err := ps.Int64(params.KeyRegularisationSpread, ex.IssuedOn)
	if err != nil {
		return Allocation{}, err
	}
	// Clamped to the month's last day: an exercise ending on the 31st, or on
	// 29 February, is late from the same day of the month, never a day later.
	late := ex.IssuedOn.After(ex.End.AddMonthsClamped(int(spreadAfter)))
	// The instalment count is read only for a late exercise: one issued on
	// time never spreads, and does not depend on it.
	var instalments int64
	if late {
		if instalments, err = ps.Int64(params.KeyRegularisationInstalments, ex.IssuedOn); err != nil {
			return Allocation{}, err
		}
		if instalments < 1 {
			return Allocation{}, fmt.Errorf("charge: %s must be at least 1", params.KeyRegularisationInstalments)
		}
	}
	monthly, err := money.MulDivHalfUp(a.RecoverableCents, 1, 12)
	if err != nil {
		return Allocation{}, err
	}
	if monthly*12 < a.RecoverableCents { // ÷ 12 rounded up, then up to the euro
		monthly++
	}
	proposal := money.CeilToEuro(monthly)

	for i, l := range covering {
		ln := Line{LeaseID: l.ID, Days: int(weights[i]), AllocatedCents: shares[i], ProvisionsCents: provisions[l.ID]}
		ln.BalanceCents = ln.AllocatedCents - ln.ProvisionsCents
		if late && ln.BalanceCents > 0 {
			ln.Spread = true
			n := int(instalments)
			each, err := money.MulDivFloor(ln.BalanceCents, 1, instalments)
			if err != nil {
				return Allocation{}, err
			}
			ln.Instalments = make([]money.Cents, n)
			for k := range ln.Instalments {
				ln.Instalments[k] = each
			}
			ln.Instalments[n-1] = ln.BalanceCents - money.Cents(instalments-1)*each
		}
		if l.EndOn.IsZero() || l.EndOn.After(ex.End) {
			ln.NewProvisionCents = proposal
		}
		a.Lines = append(a.Lines, ln)
	}
	return a, nil
}
