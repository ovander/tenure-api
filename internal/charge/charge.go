// Package charge owns the charges ledger, the syndic import and the annual
// reconciliation of recoverable charges (RENT-SPEC-007). Allocate is the pure
// calculation of the reconciliation: no I/O, every input passed by the caller.
package charge

import (
	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/period"
)

// Category drives both the regularisation and the 2044 line (RENT-SPEC-002);
// params.LineMap maps each category to its line.
type Category string

// Charge categories.
const (
	// CategoryCoproProvision is a syndic's provisional call (appel de fonds).
	// It is replaced by the statement's actual figures in the reconciliation
	// and feeds 2044 line 229.
	CategoryCoproProvision Category = "copro_provision"
	// CategoryCoproStatement is a line of the syndic's annual statement: the
	// actual charges reconciled against provisions.
	CategoryCoproStatement Category = "copro_statement"
	// CategoryCoproRegularisationPrior is the non-deductible part of the
	// previous year's provisions established by its statement (line 230).
	CategoryCoproRegularisationPrior Category = "copro_regularisation_prior"
	CategoryPropertyTax              Category = "property_tax" // excluding TEOM
	CategoryTEOM                     Category = "teom"
	CategoryInsurance                Category = "insurance"
	CategoryRepairs                  Category = "repairs"
	CategoryManagement               Category = "management"
	CategoryLoanInterest             Category = "loan_interest"
	CategoryLoanInsurance            Category = "loan_insurance"
	CategoryRecoverableUtilities     Category = "recoverable_utilities"
	// CategoryIndemnity is income: insurance indemnities and subsidies.
	CategoryIndemnity Category = "indemnity"
	// CategoryOwnerExpenseChargedToTenant is income: an owner expense the
	// lease puts on the tenant (line 212).
	CategoryOwnerExpenseChargedToTenant Category = "owner_expense_charged_to_tenant"
	// CategoryUnrecoveredAtClose is a recoverable charge left unrecovered when
	// a lease closed (line 225).
	CategoryUnrecoveredAtClose Category = "unrecovered_at_close"
)

// Charge is an entry of the charges ledger.
type Charge struct {
	ID          string      `json:"id"`
	AssetID     string      `json:"asset_id"`
	Category    Category    `json:"category"`
	AmountCents money.Cents `json:"amount_cents"`
	ChargedOn   period.Date `json:"charged_on"`
	// PaidOn is when the owner paid it; zero while unpaid.
	PaidOn period.Date `json:"paid_on"`
	// RecoverableBP is the recoverable share in basis points (10000 = all of
	// it), from the syndic's split or the owner's override.
	RecoverableBP int64  `json:"recoverable_bp"`
	DocumentID    string `json:"document_id,omitempty"`
}

// RecoverableCents returns the recoverable part of the charge, rounded
// half-up to the cent.
func (c Charge) RecoverableCents() (money.Cents, error) {
	return money.MulDivHalfUp(c.AmountCents, c.RecoverableBP, 10000)
}

// Reconciled reports whether the category takes part in the reconciliation of
// actual charges: provisional calls and tax-only entries do not.
func (c Category) Reconciled() bool {
	switch c {
	case CategoryCoproProvision, CategoryCoproRegularisationPrior, CategoryIndemnity,
		CategoryOwnerExpenseChargedToTenant, CategoryUnrecoveredAtClose:
		return false
	}
	return true
}
