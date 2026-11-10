package charge

import (
	"testing"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/period"
)

func TestSuggestCharges(t *testing.T) {
	r := params.BankCharges{WindowDays: 45}
	d := func(s string) period.Date { x, _ := period.ParseDate(s); return x }
	a1, a2 := uuid.New(), uuid.New()
	open := []OpenCharge{
		{ID: uuid.New(), AssetID: a1, ChargedOn: d("2026-10-01"), Label: "Charges courantes", AmountCents: 30000},
		{ID: uuid.New(), AssetID: a1, ChargedOn: d("2026-10-01"), Label: "Fonds travaux", AmountCents: 5000},
		{ID: uuid.New(), AssetID: a2, ChargedOn: d("2026-10-15"), Label: "Assurance PNO", AmountCents: 35000},
		{ID: uuid.New(), AssetID: a2, ChargedOn: d("2026-01-15"), Label: "Taxe foncière 2025", AmountCents: 35000},
	}
	s := SuggestCharges(d("2026-10-20"), -35000, open, r)
	if len(s) != 2 || s[0].Rule != RuleStatement || len(s[0].Charges) != 2 || s[1].Rule != RuleAmountDate || s[1].Charges[0].Label != "Assurance PNO" {
		t.Errorf("debit of a statement total and of one charge %+v", s)
	}
	if s := SuggestCharges(d("2026-10-20"), -30000, open, r); len(s) != 1 || s[0].Rule != RuleAmountDate {
		t.Errorf("one charge of a statement %+v", s)
	}
	if s := SuggestCharges(d("2026-10-20"), 35000, open, r); len(s) != 0 {
		t.Errorf("a credit %+v", s)
	}
	if s := SuggestCharges(d("2026-10-20"), -12300, open, r); len(s) != 0 {
		t.Errorf("no charge of that amount %+v", s)
	}
}
