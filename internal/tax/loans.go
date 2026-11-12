package tax

import (
	"fmt"
	"sort"

	"github.com/ovander/tenure-api/internal/charge"
	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/period"
)

// FlagSupersededByLoan marks a charge typed in a loan category on an asset
// whose recorded loan has instalments in the year: the loan's table gives
// line 250, so the charge is set aside rather than counted twice (Q-126).
const FlagSupersededByLoan = "superseded_by_loan"

// LoanInstalment is an instalment of a recorded loan (RENT-SPEC-014).
type LoanInstalment struct {
	LoanID         string      `json:"loan_id"`
	AssetID        string      `json:"asset_id"`
	Seq            int         `json:"seq"`
	DueOn          period.Date `json:"due_on"`
	InterestCents  money.Cents `json:"interest_cents"`
	InsuranceCents money.Cents `json:"insurance_cents"`
	// DocumentID is the loan's offer or table, if uploaded.
	DocumentID string `json:"document_id,omitempty"`
}

// LoanSource is the source id a loan's instalment gives line 250: its
// interest, or its insurance when insurance is set.
func LoanSource(i LoanInstalment, insurance bool) string {
	if insurance {
		return fmt.Sprintf("loan:%s:%d:insurance", i.LoanID, i.Seq)
	}
	return fmt.Sprintf("loan:%s:%d", i.LoanID, i.Seq)
}

// WithLoans adds the recorded loans' instalments due in year to the charges
// (Q-126): each instalment counts on its due day, its interest as a
// loan_interest charge and its insurance as a loan_insurance one, so Lines
// puts both on line 250 and splits them between the asset's owners like any
// charge. A charge typed in category loan_interest and paid in the year, on
// an asset with an instalment due that year, is set aside and flagged; so is
// a loan_insurance one when an instalment of the year carries insurance. A
// loan_insurance charge stays when the tables carry none (an insurance taken
// apart from the bank). Pure.
func WithLoans(charges []charge.Charge, instalments []LoanInstalment, year int) ([]charge.Charge, []Flag) {
	running, insured := map[string]bool{}, map[string]bool{}
	var mine []LoanInstalment
	for _, i := range instalments {
		if i.DueOn.Year != year {
			continue
		}
		mine = append(mine, i)
		running[i.AssetID] = true
		if i.InsuranceCents > 0 {
			insured[i.AssetID] = true
		}
	}
	out := make([]charge.Charge, 0, len(charges)+2*len(mine))
	var flags []Flag
	for _, c := range charges {
		paidInYear := !c.PaidOn.IsZero() && c.PaidOn.Year == year
		if paidInYear && ((c.Category == charge.CategoryLoanInterest && running[c.AssetID]) ||
			(c.Category == charge.CategoryLoanInsurance && insured[c.AssetID])) {
			flags = append(flags, Flag{Kind: FlagSupersededByLoan, ID: c.ID})
			continue
		}
		out = append(out, c)
	}
	sort.SliceStable(mine, func(a, b int) bool {
		if c := mine[a].DueOn.Compare(mine[b].DueOn); c != 0 {
			return c < 0
		}
		if mine[a].LoanID != mine[b].LoanID {
			return mine[a].LoanID < mine[b].LoanID
		}
		return mine[a].Seq < mine[b].Seq
	})
	var zero money.Cents
	for _, i := range mine {
		for _, part := range []struct {
			cat       charge.Category
			amount    money.Cents
			insurance bool
		}{{charge.CategoryLoanInterest, i.InterestCents, false}, {charge.CategoryLoanInsurance, i.InsuranceCents, true}} {
			if part.amount <= 0 {
				continue
			}
			out = append(out, charge.Charge{ID: LoanSource(i, part.insurance), AssetID: i.AssetID, Category: part.cat, AmountCents: part.amount,
				ChargedOn: i.DueOn, PaidOn: i.DueOn, RecoverableAmountCents: &zero, DocumentID: i.DocumentID})
		}
	}
	return out, flags
}
