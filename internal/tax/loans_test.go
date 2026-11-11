package tax

import (
	"testing"
	gotime "time"

	"github.com/ovander/tenure-api/internal/charge"
	"github.com/ovander/tenure-api/internal/period"
)

func TestWithLoans(t *testing.T) {
	d := func(y, m, day int) period.Date { return period.NewDate(y, gotime.Month(m), day) }
	typed := []charge.Charge{
		{ID: "int-a", AssetID: "A", Category: charge.CategoryLoanInterest, AmountCents: 500, PaidOn: d(2025, 12, 31)},
		{ID: "ins-a", AssetID: "A", Category: charge.CategoryLoanInsurance, AmountCents: 50, PaidOn: d(2025, 12, 31)},
		{ID: "int-a-2024", AssetID: "A", Category: charge.CategoryLoanInterest, AmountCents: 400, PaidOn: d(2024, 12, 31)},
		{ID: "int-b", AssetID: "B", Category: charge.CategoryLoanInterest, AmountCents: 300, PaidOn: d(2025, 12, 31)},
		{ID: "ins-b", AssetID: "B", Category: charge.CategoryLoanInsurance, AmountCents: 30, PaidOn: d(2025, 12, 31)},
		{ID: "int-c", AssetID: "C", Category: charge.CategoryLoanInterest, AmountCents: 200, PaidOn: d(2025, 12, 31)},
		{ID: "rep-a", AssetID: "A", Category: charge.CategoryRepairs, AmountCents: 900, PaidOn: d(2025, 3, 1)},
	}
	loans := []LoanInstalment{
		{LoanID: "K1", AssetID: "A", Seq: 2, DueOn: d(2025, 2, 10), InterestCents: 90, InsuranceCents: 10},
		{LoanID: "K1", AssetID: "A", Seq: 1, DueOn: d(2025, 1, 10), InterestCents: 100, InsuranceCents: 10},
		{LoanID: "K2", AssetID: "B", Seq: 1, DueOn: d(2025, 6, 1), InterestCents: 70},
		{LoanID: "K3", AssetID: "C", Seq: 9, DueOn: d(2026, 1, 1), InterestCents: 60},
	}
	out, flags := WithLoans(typed, loans, 2025)
	var got []string
	for _, c := range out {
		got = append(got, c.ID+":"+string(c.Category))
	}
	want := []string{"int-a-2024:loan_interest", "ins-b:loan_insurance", "int-c:loan_interest", "rep-a:repairs",
		"loan:K1:1:loan_interest", "loan:K1:1:insurance:loan_insurance", "loan:K1:2:loan_interest", "loan:K1:2:insurance:loan_insurance",
		"loan:K2:1:loan_interest"}
	if len(got) != len(want) {
		t.Fatalf("charges %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("charges %v, want %v", got, want)
		}
	}
	if len(flags) != 3 || flags[0].ID != "int-a" || flags[1].ID != "ins-a" || flags[2].ID != "int-b" || flags[0].Kind != FlagSupersededByLoan {
		t.Fatalf("flags %+v", flags)
	}
	for _, c := range out[4:] {
		if c.PaidOn.Year != 2025 || c.RecoverableAmountCents == nil || *c.RecoverableAmountCents != 0 {
			t.Fatalf("derived charge %+v", c)
		}
	}
	if same, none := WithLoans(typed, nil, 2025); len(same) != len(typed) || none != nil {
		t.Fatalf("no loans changed the charges: %d %v", len(same), none)
	}
}
