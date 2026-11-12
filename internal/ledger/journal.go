// Package ledger is the owner's double-entry journal (RENT-SPEC-014): every
// movement of money the tool records, posted on a cash basis to the accounts
// of the chart in legal_params, and the period lock that freezes a year once
// its tax summary is issued (Q-127). Journal is pure: the caller passes the
// movements and the chart.
package ledger

import (
	"errors"
	"fmt"
	"sort"

	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/period"
	"github.com/ovander/tenure-api/internal/problem"
)

// TypePeriodLocked is the problem of a write into a year whose tax summary
// is issued; the database raises it (migration 000025) and internal/db maps
// it.
const TypePeriodLocked = db.TypePeriodLocked

func init() {
	problem.Register(map[string]problem.Message{
		TypePeriodLocked: problem.M(409, "Cette année est close : son récapitulatif fiscal est émis, ses chiffres ne changent plus.",
			"This year is closed: its tax summary is issued and its figures no longer change."),
	})
}

// Account roles of the chart (params.LedgerChart.Accounts).
const (
	RoleBank           = "bank"
	RoleTenants        = "tenants"
	RoleTenantAdvances = "tenant_advances"
	RoleProvisions     = "provisions"
	RoleRent           = "rent"
	RoleOtherIncome    = "other_income"
	RoleDeposits       = "deposits"
	RoleLoans          = "loans"
	RoleLoanInterest   = "loan_interest"
	RoleLoanInsurance  = "loan_insurance"
	RolePenalties      = "penalties"
)

var roles = []string{RoleBank, RoleTenants, RoleTenantAdvances, RoleProvisions, RoleRent, RoleOtherIncome,
	RoleDeposits, RoleLoans, RoleLoanInterest, RoleLoanInsurance, RolePenalties}

// Journals of an entry.
const (
	JournalBank    = "BQ"
	JournalGeneral = "OD"
)

// ErrChart is returned for a chart missing a role, or a charge whose
// category it does not map: the journal is refused rather than posting to
// no account.
var ErrChart = errors.New("ledger: chart of accounts incomplete")

// Payment is money received for a lease, with what it paid: the rent, the
// provisions and the regularisation of the rent calls it was matched to;
// the rest is unapplied.
type Payment struct {
	ID                  string      `json:"id"`
	LeaseID             string      `json:"lease_id"`
	PaidOn              period.Date `json:"paid_on"`
	AmountCents         money.Cents `json:"amount_cents"`
	RentCents           money.Cents `json:"rent_cents"`
	ProvisionCents      money.Cents `json:"provision_cents"`
	RegularisationCents money.Cents `json:"regularisation_cents"`
}

// Charge is a confirmed charge paid by the owner, with its recoverable part.
type Charge struct {
	ID               string      `json:"id"`
	AssetID          string      `json:"asset_id"`
	Category         string      `json:"category"`
	PaidOn           period.Date `json:"paid_on"`
	AmountCents      money.Cents `json:"amount_cents"`
	RecoverableCents money.Cents `json:"recoverable_cents"`
	// DocumentID is its invoice, if uploaded.
	DocumentID string `json:"document_id,omitempty"`
}

// Instalment is a loan instalment, debited on its due day.
type Instalment struct {
	LoanID         string      `json:"loan_id"`
	Seq            int         `json:"seq"`
	DueOn          period.Date `json:"due_on"`
	PrincipalCents money.Cents `json:"principal_cents"`
	InterestCents  money.Cents `json:"interest_cents"`
	InsuranceCents money.Cents `json:"insurance_cents"`
	// DocumentID is the loan's offer or table, if uploaded.
	DocumentID string `json:"document_id,omitempty"`
}

// Deposit is a security deposit received for a lease.
type Deposit struct {
	ID          string      `json:"id"`
	LeaseID     string      `json:"lease_id"`
	ReceivedOn  period.Date `json:"received_on"`
	AmountCents money.Cents `json:"amount_cents"`
}

// Settlement is a deposit restitution statement (RD): the deposit it
// releases, what it keeps (deductions, arrears, regularisation), the late
// penalty, and the total paid back (negative: what the tenant still owes).
type Settlement struct {
	ID                  string      `json:"id"`
	LeaseID             string      `json:"lease_id"`
	SettledOn           period.Date `json:"settled_on"`
	ReleasedCents       money.Cents `json:"released_cents"`
	DeductionsCents     money.Cents `json:"deductions_cents"`
	ArrearsCents        money.Cents `json:"arrears_cents"`
	RegularisationCents money.Cents `json:"regularisation_cents"`
	PenaltyCents        money.Cents `json:"penalty_cents"`
	TotalCents          money.Cents `json:"total_cents"`
	// DocumentID is the RD statement.
	DocumentID string `json:"document_id,omitempty"`
}

// Inputs are the movements the journal posts.
type Inputs struct {
	Payments    []Payment    `json:"payments,omitempty"`
	Charges     []Charge     `json:"charges,omitempty"`
	Instalments []Instalment `json:"instalments,omitempty"`
	Deposits    []Deposit    `json:"deposits,omitempty"`
	Settlements []Settlement `json:"settlements,omitempty"`
}

// Line is one side of an entry on one account. Exactly one of debit and
// credit is positive.
type Line struct {
	Account     string      `json:"account"`
	Label       string      `json:"label"`
	DebitCents  money.Cents `json:"debit_cents"`
	CreditCents money.Cents `json:"credit_cents"`
}

// Entry is one balanced movement. Source is the record it comes from
// (payment, charge, loan instalment, guarantee, settlement), which the
// owner's archive keeps as its supporting document.
type Entry struct {
	Date    period.Date `json:"date" swaggertype:"string" format:"date"`
	Journal string      `json:"journal" enums:"BQ,OD"`
	Kind    string      `json:"kind" enums:"payment,charge,instalment,deposit,settlement"`
	Source  string      `json:"source"`
	Label   string      `json:"label"`
	// DocumentID is the supporting document archived for it, if any.
	DocumentID string `json:"document_id,omitempty"`
	Lines      []Line `json:"lines"`
}

// Balance is an account's movements over the journal.
type Balance struct {
	Account     string      `json:"account"`
	Label       string      `json:"label"`
	DebitCents  money.Cents `json:"debit_cents"`
	CreditCents money.Cents `json:"credit_cents"`
	// BalanceCents is debit − credit.
	BalanceCents money.Cents `json:"balance_cents"`
}

// entry builds an entry from signed amounts: positive debits, negative
// credits; zero amounts are left out.
type builder struct {
	e Entry
}

func (b *builder) add(a params.LedgerAccount, signed money.Cents) {
	switch {
	case signed > 0:
		b.e.Lines = append(b.e.Lines, Line{Account: a.Code, Label: a.Label, DebitCents: signed})
	case signed < 0:
		b.e.Lines = append(b.e.Lines, Line{Account: a.Code, Label: a.Label, CreditCents: -signed})
	}
}

func (b *builder) done() (Entry, bool, error) {
	if len(b.e.Lines) == 0 {
		return Entry{}, false, nil
	}
	var d, c money.Cents
	for _, l := range b.e.Lines {
		d += l.DebitCents
		c += l.CreditCents
	}
	if d != c {
		return Entry{}, false, fmt.Errorf("ledger: %s %s is unbalanced (%d / %d)", b.e.Kind, b.e.Source, d, c)
	}
	return b.e, true, nil
}

// Journal posts the movements dated in [from, to] (RENT-SPEC-014, Q-127),
// on a cash basis, one balanced entry per movement:
//   - a payment debits the bank; it credits rent with the rent it paid, the
//     provisions account with the provisions and the regularisation it paid,
//     and the tenants' advances with what is unapplied;
//   - a charge paid credits the bank; its recoverable part is debited to the
//     provisions account (to be recovered from the tenants), the rest to the
//     account of its category; an income category is money received, its
//     signs reversed;
//   - a loan instalment credits the bank and debits the loan (principal),
//     the interest and the borrower insurance;
//   - a deposit received debits the bank and credits the deposits account,
//     a liability;
//   - a settlement debits the deposit it releases and the late penalty; it
//     credits rent with the arrears kept, the provisions with the
//     regularisation kept, other income with the deductions, and the bank
//     with what is paid back; what the tenant still owes is debited to the
//     tenants' account.
//
// Entries are ordered by date, kind and source. Balances are by account
// number.
func Journal(in Inputs, chart params.LedgerChart, from, to period.Date) ([]Entry, []Balance, error) {
	acc := map[string]params.LedgerAccount{}
	for _, r := range roles {
		a, ok := chart.Accounts[r]
		if !ok || a.Code == "" {
			return nil, nil, fmt.Errorf("%w: role %s", ErrChart, r)
		}
		acc[r] = a
	}
	income := map[string]bool{}
	for _, c := range chart.IncomeCategories {
		income[c] = true
	}
	in2 := func(d period.Date) bool { return !d.IsZero() && !d.Before(from) && !d.After(to) }
	var out []Entry
	push := func(b *builder) error {
		e, ok, err := b.done()
		if ok {
			out = append(out, e)
		}
		return err
	}
	for _, p := range in.Payments {
		if !in2(p.PaidOn) {
			continue
		}
		b := &builder{Entry{Date: p.PaidOn, Journal: JournalBank, Kind: "payment", Source: p.ID, Label: "Encaissement du locataire"}}
		unapplied := p.AmountCents - p.RentCents - p.ProvisionCents - p.RegularisationCents
		b.add(acc[RoleBank], p.AmountCents)
		b.add(acc[RoleRent], -p.RentCents)
		b.add(acc[RoleProvisions], -(p.ProvisionCents + p.RegularisationCents))
		b.add(acc[RoleTenantAdvances], -unapplied)
		if err := push(b); err != nil {
			return nil, nil, err
		}
	}
	for _, c := range in.Charges {
		if !in2(c.PaidOn) {
			continue
		}
		a, ok := chart.Categories[c.Category]
		if !ok || a.Code == "" {
			return nil, nil, fmt.Errorf("%w: category %s (charge %s)", ErrChart, c.Category, c.ID)
		}
		b := &builder{Entry{Date: c.PaidOn, Journal: JournalBank, Kind: "charge", Source: c.ID, Label: a.Label, DocumentID: c.DocumentID}}
		if income[c.Category] {
			b.add(acc[RoleBank], c.AmountCents)
			b.add(a, -c.AmountCents)
		} else {
			b.add(acc[RoleProvisions], c.RecoverableCents)
			b.add(a, c.AmountCents-c.RecoverableCents)
			b.add(acc[RoleBank], -c.AmountCents)
		}
		if err := push(b); err != nil {
			return nil, nil, err
		}
	}
	for _, i := range in.Instalments {
		if !in2(i.DueOn) {
			continue
		}
		b := &builder{Entry{Date: i.DueOn, Journal: JournalBank, Kind: "instalment", Source: fmt.Sprintf("loan:%s:%d", i.LoanID, i.Seq), Label: "Échéance d’emprunt", DocumentID: i.DocumentID}}
		b.add(acc[RoleLoans], i.PrincipalCents)
		b.add(acc[RoleLoanInterest], i.InterestCents)
		b.add(acc[RoleLoanInsurance], i.InsuranceCents)
		b.add(acc[RoleBank], -(i.PrincipalCents + i.InterestCents + i.InsuranceCents))
		if err := push(b); err != nil {
			return nil, nil, err
		}
	}
	for _, d := range in.Deposits {
		if !in2(d.ReceivedOn) {
			continue
		}
		b := &builder{Entry{Date: d.ReceivedOn, Journal: JournalBank, Kind: "deposit", Source: d.ID, Label: "Dépôt de garantie reçu"}}
		b.add(acc[RoleBank], d.AmountCents)
		b.add(acc[RoleDeposits], -d.AmountCents)
		if err := push(b); err != nil {
			return nil, nil, err
		}
	}
	for _, s := range in.Settlements {
		if !in2(s.SettledOn) {
			continue
		}
		b := &builder{Entry{Date: s.SettledOn, Journal: JournalGeneral, Kind: "settlement", Source: s.ID, Label: "Restitution du dépôt de garantie", DocumentID: s.DocumentID}}
		b.add(acc[RoleDeposits], s.ReleasedCents)
		b.add(acc[RolePenalties], s.PenaltyCents)
		b.add(acc[RoleRent], -s.ArrearsCents)
		b.add(acc[RoleProvisions], -s.RegularisationCents)
		b.add(acc[RoleOtherIncome], -s.DeductionsCents)
		if s.TotalCents >= 0 {
			b.add(acc[RoleBank], -s.TotalCents)
		} else {
			b.add(acc[RoleTenants], -s.TotalCents)
		}
		if err := push(b); err != nil {
			return nil, nil, err
		}
	}
	order := map[string]int{"deposit": 0, "payment": 1, "charge": 2, "instalment": 3, "settlement": 4}
	sort.SliceStable(out, func(a, b int) bool {
		if c := out[a].Date.Compare(out[b].Date); c != 0 {
			return c < 0
		}
		if order[out[a].Kind] != order[out[b].Kind] {
			return order[out[a].Kind] < order[out[b].Kind]
		}
		return out[a].Source < out[b].Source
	})
	if out == nil {
		out = []Entry{}
	}
	return out, Balances(out), nil
}

// Balances sums the entries by account, ordered by account number.
func Balances(entries []Entry) []Balance {
	by := map[string]*Balance{}
	for _, e := range entries {
		for _, l := range e.Lines {
			b := by[l.Account]
			if b == nil {
				b = &Balance{Account: l.Account, Label: l.Label}
				by[l.Account] = b
			}
			b.DebitCents += l.DebitCents
			b.CreditCents += l.CreditCents
		}
	}
	out := make([]Balance, 0, len(by))
	for _, b := range by {
		b.BalanceCents = b.DebitCents - b.CreditCents
		out = append(out, *b)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Account < out[b].Account })
	return out
}
