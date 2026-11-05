package inventory

import (
	"errors"
	"fmt"

	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/period"
)

// fullBP is 100 % in basis points.
const fullBP = 10_000

// Grid is a wear-and-tear grid (grille de vetuste) agreed between the parties and annexed to the lease
// (décret 2016-382 art. 4, RENT-INV-001 §3): per category, a theoretical
// lifetime, a franchise during which no wear is counted, a yearly flat
// reduction and a floor, rates in basis points. Tenure ships no default grid
// (INV-OD-08): its lines come from the owner.
type Grid struct {
	Name   string     `json:"name"`
	Source string     `json:"source"`
	Lines  []GridLine `json:"lines"`
}

// GridLine is one category of a grid.
type GridLine struct {
	Category       Category `json:"category"`
	LifetimeYears  int      `json:"lifetime_years"`
	FranchiseYears int      `json:"franchise_years"`
	YearlyRateBP   int64    `json:"yearly_rate_bp"`
	FloorBP        int64    `json:"floor_bp"`
}

// ErrNoGridLine is returned when the lease's grid has no line for the item's
// category: the deduction is refused rather than charged in full, so a gap in
// the grid never overstates what the tenant owes.
var ErrNoGridLine = errors.New("inventory: the wear grid has no line for this category")

// DeductionInput is an item qualified as tenant damage, with the cost of its
// repair or replacement from an archived quote or invoice.
type DeductionInput struct {
	Category    Category     `json:"category"`
	CostCents   money.Cents  `json:"cost_cents"`
	InstalledOn *period.Date `json:"installed_on,omitempty"`
	LeaseStart  period.Date  `json:"lease_start"`
	ExitOn      period.Date  `json:"exit_on"`
}

// Deduction is the tenant's share of a cost and its amount.
type Deduction struct {
	AgeYears    int         `json:"age_years"`
	ShareBP     int64       `json:"share_bp"`
	AmountCents money.Cents `json:"amount_cents"`
	Line        *GridLine   `json:"line,omitempty"`
}

// Deduct computes the deduction for an item (RENT-INV-001 §6):
//
//	share = max(floor, 10 000 − rate × max(0, age − franchise)), in bp
//	amount = cost × share ÷ 10 000, half-up to the cent
//
// age in full years from the element's installation (or the lease start when
// unknown) to the exit; once the line's lifetime is reached the share is the
// floor. Without a grid the share is the whole cost, which the owner then
// justifies item by item. A cost is never negative.
func Deduct(in DeductionInput, grid *Grid) (Deduction, error) {
	if in.CostCents < 0 {
		return Deduction{}, fmt.Errorf("inventory: negative cost")
	}
	from := in.LeaseStart
	if in.InstalledOn != nil {
		from = *in.InstalledOn
	}
	d := Deduction{AgeYears: FullYears(from, in.ExitOn), ShareBP: fullBP}
	if grid != nil {
		line, ok := lineFor(grid, in.Category)
		if !ok {
			return Deduction{}, ErrNoGridLine
		}
		if err := line.check(); err != nil {
			return Deduction{}, err
		}
		worn := int64(max(0, d.AgeYears-line.FranchiseYears))
		share := max(line.FloorBP, fullBP-line.YearlyRateBP*worn)
		if line.LifetimeYears > 0 && d.AgeYears >= line.LifetimeYears {
			share = line.FloorBP
		}
		d.ShareBP, d.Line = min(share, fullBP), &line
	}
	amount, err := money.MulDivHalfUp(in.CostCents, d.ShareBP, fullBP)
	if err != nil {
		return Deduction{}, err
	}
	d.AmountCents = amount
	return d, nil
}

func lineFor(g *Grid, c Category) (GridLine, bool) {
	for _, l := range g.Lines {
		if l.Category == c {
			return l, true
		}
	}
	return GridLine{}, false
}

func (l GridLine) check() error {
	if l.LifetimeYears < 0 || l.FranchiseYears < 0 || l.YearlyRateBP < 0 || l.FloorBP < 0 || l.FloorBP > fullBP {
		return fmt.Errorf("inventory: invalid wear grid line for %s", l.Category)
	}
	return nil
}

// FullYears is the number of whole years from from to to, 0 when to is not
// after from.
func FullYears(from, to period.Date) int {
	if !to.After(from) {
		return 0
	}
	y := to.Year - from.Year
	if to.Month < from.Month || (to.Month == from.Month && to.Day < from.Day) {
		y--
	}
	return max(0, y)
}

// SettlementDeadline is the last day to return the deposit (loi 89-462
// art. 22): the return of the keys plus the months without a retained
// deduction, or with one. Tenure takes the shorter delay whenever no deduction
// is retained, even when the exit differs from the entry (INV-OD-13): the
// test is stricter than the law's « conforming exit », so it can only bring
// the date closer for the tenant.
func SettlementDeadline(keysReturnedOn period.Date, deductionRetained bool, r params.DepositReturn) (period.Date, error) {
	if keysReturnedOn.IsZero() {
		return period.Date{}, fmt.Errorf("inventory: no return of the keys")
	}
	months := r.ConformingMonths
	if deductionRetained {
		months = r.WithDeductionsMonths
	}
	if months <= 0 {
		return period.Date{}, fmt.Errorf("inventory: invalid restitution delay")
	}
	return keysReturnedOn.AddMonthsClamped(months), nil
}

// HoldbackBalanceDeadline is the last day to pay the balance of a holdback:
// the approval of the building's annual accounts plus its months.
func HoldbackBalanceDeadline(accountsApprovedOn period.Date, r params.DepositReturn) (period.Date, error) {
	if accountsApprovedOn.IsZero() || r.HoldbackBalanceMonths <= 0 {
		return period.Date{}, fmt.Errorf("inventory: no approval date or delay")
	}
	return accountsApprovedOn.AddMonthsClamped(r.HoldbackBalanceMonths), nil
}

// Penalty is the late restitution penalty owed on a day.
type Penalty struct {
	StartedMonths int         `json:"started_months"`
	AmountCents   money.Cents `json:"amount_cents"`
}

// LatePenalty is what is owed on asOf when the deposit was not returned by
// deadline: per started month of delay, the penalty rate of the monthly rent
// excluding charges, half-up to the cent. Nothing is owed while the tenant
// has given no new address (art. 22).
func LatePenalty(deadline, asOf period.Date, monthlyRentCents money.Cents, newAddressGiven bool, r params.DepositReturn) (Penalty, error) {
	if !newAddressGiven || !asOf.After(deadline) {
		return Penalty{}, nil
	}
	m := 1
	for deadline.AddMonthsClamped(m).Before(asOf) {
		m++
	}
	amount, err := money.MulDivHalfUp(monthlyRentCents, r.LatePenaltyBP*int64(m), fullBP)
	if err != nil {
		return Penalty{}, err
	}
	return Penalty{StartedMonths: m, AmountCents: amount}, nil
}

// SettlementInput is what the restitution statement draws on.
type SettlementInput struct {
	DepositCents        money.Cents   `json:"deposit_cents"`
	Deductions          []money.Cents `json:"deductions"`
	ArrearsCents        money.Cents   `json:"arrears_cents"`
	RegularisationCents money.Cents   `json:"regularisation_cents"`
	CollectiveBuilding  bool          `json:"collective_building"`
	AccountsApproved    bool          `json:"accounts_approved"`
}

// Settlement is the restitution statement's arithmetic. Balance is what goes
// back to the tenant; a negative balance is what the tenant still owes.
type Settlement struct {
	DeductionsCents     money.Cents `json:"deductions_cents"`
	ArrearsCents        money.Cents `json:"arrears_cents"`
	RegularisationCents money.Cents `json:"regularisation_cents"`
	HoldbackCents       money.Cents `json:"holdback_cents"`
	BalanceCents        money.Cents `json:"balance_cents"`
}

// Settle computes the balance (RENT-INV-001 §6): the deposit less the
// deductions, the unpaid arrears and the pending regularisation; in a
// collective building whose annual accounts are not yet approved, a holdback
// of at most the allowed share of the deposit (rounded down, so never more)
// takes the regularisation's place until it is issued.
func Settle(in SettlementInput, r params.DepositReturn) (Settlement, error) {
	if in.DepositCents < 0 || in.ArrearsCents < 0 {
		return Settlement{}, fmt.Errorf("inventory: negative deposit or arrears")
	}
	s := Settlement{ArrearsCents: in.ArrearsCents}
	for _, d := range in.Deductions {
		if d < 0 {
			return Settlement{}, fmt.Errorf("inventory: negative deduction")
		}
		s.DeductionsCents += d
	}
	if in.CollectiveBuilding && !in.AccountsApproved {
		h, err := money.MulDivFloor(in.DepositCents, r.CollectiveHoldbackBP, fullBP)
		if err != nil {
			return Settlement{}, err
		}
		s.HoldbackCents = h
	} else {
		s.RegularisationCents = in.RegularisationCents
	}
	s.BalanceCents = in.DepositCents - s.DeductionsCents - s.ArrearsCents - s.RegularisationCents - s.HoldbackCents
	return s, nil
}
