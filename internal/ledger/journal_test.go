package ledger

import (
	"errors"
	"testing"

	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/period"
)

func chart() params.LedgerChart {
	c := params.LedgerChart{Accounts: map[string]params.LedgerAccount{}, Categories: map[string]params.LedgerAccount{
		"repairs": {Code: "R", Label: "repairs"}, "indemnity": {Code: "I", Label: "indemnity"}}, IncomeCategories: []string{"indemnity"}}
	for _, r := range roles {
		c.Accounts[r] = params.LedgerAccount{Code: r, Label: r}
	}
	return c
}

func TestJournalSigns(t *testing.T) {
	d := period.NewDate(2026, 3, 1)
	in := Inputs{Charges: []Charge{
		{ID: "c", Category: "repairs", PaidOn: d, AmountCents: 1000},
		{ID: "refund", Category: "repairs", PaidOn: d, AmountCents: -300},
		{ID: "i", Category: "indemnity", PaidOn: d, AmountCents: 500},
		{ID: "zero", Category: "repairs", PaidOn: d},
	}}
	es, bs, err := Journal(in, chart(), d, d)
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 3 {
		t.Fatalf("entries %+v", es)
	}
	want := map[string][2]int64{"bank": {800, 1000}, "R": {1000, 300}, "I": {0, 500}}
	for _, b := range bs {
		w := want[b.Account]
		if int64(b.DebitCents) != w[0] || int64(b.CreditCents) != w[1] {
			t.Errorf("%s = %+v, want %v", b.Account, b, w)
		}
	}
}

func TestJournalChart(t *testing.T) {
	c := chart()
	delete(c.Accounts, RoleDeposits)
	if _, _, err := Journal(Inputs{}, c, period.NewDate(2026, 1, 1), period.NewDate(2026, 12, 31)); !errors.Is(err, ErrChart) {
		t.Fatalf("missing role: %v", err)
	}
	es, bs, err := Journal(Inputs{}, chart(), period.NewDate(2026, 1, 1), period.NewDate(2026, 12, 31))
	if err != nil || len(es) != 0 || len(bs) != 0 {
		t.Fatalf("empty journal %v %v %v", es, bs, err)
	}
}
