package payment

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/period"
)

func matchingParams(t *testing.T) params.BankMatching {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join("..", "..", "testdata", "params", "*.json"))
	var rs []io.Reader
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = fh.Close() }()
		rs = append(rs, fh)
	}
	ps, err := params.Load(rs...)
	if err != nil {
		t.Fatal(err)
	}
	r, err := ps.BankMatching(period.NewDate(2026, 1, 1))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSuggest(t *testing.T) {
	r := matchingParams(t)
	l1, l2 := uuid.New(), uuid.New()
	d := func(s string) period.Date { x, _ := period.ParseDate(s); return x }
	calls := []OpenCall{
		{BillingDocumentID: uuid.New(), LeaseID: l1, Number: "AL-0190A1B2-2026-0010", Period: "2026-10", DueOn: d("2026-10-05"), OutstandingCents: 80000, Tenants: []string{"Léa Martin-Durand"}},
		{BillingDocumentID: uuid.New(), LeaseID: l2, Number: "AL-0190C3D4-2026-0011", Period: "2026-10", DueOn: d("2026-10-05"), OutstandingCents: 80000, Tenants: []string{"Paul Roux"}},
	}
	line := func(label string, cents int64, on string) BankLine {
		return BankLine{BookedOn: d(on), AmountCents: moneyCents(cents), Label: label}
	}
	for _, c := range []struct {
		name  string
		line  BankLine
		rules []string
		lease uuid.UUID
		sure  bool
	}{
		{"reference", line("VIR AL-0190A1B2-2026-0010", 80000, "2026-10-06"), []string{RuleReference, RuleAmountPeriod}, l1, true},
		{"name and amount", line("VIR SEPA MME LEA MARTIN DURAND LOYER", 80000, "2026-10-20"), []string{RuleNameAmount}, l1, true},
		{"allowance part", line("CAF DU RHONE APL ROUX PAUL", 21234, "2026-10-08"), []string{RuleAllowance}, l2, true},
		{"amount and period only", line("VIR SEPA LOYER", 80000, "2026-10-12"), []string{RuleAmountPeriod, RuleAmountPeriod}, l1, false},
		{"too far from the due day", line("VIR SEPA LOYER", 80000, "2026-11-20"), nil, uuid.Nil, false},
		{"wrong amount", line("VIR LEA MARTIN DURAND", 79000, "2026-10-06"), nil, uuid.Nil, false},
		{"a debit", line("PRLV SYNDIC", -80000, "2026-10-06"), nil, uuid.Nil, false},
	} {
		s := Suggest(c.line, calls, r)
		if len(s) != len(c.rules) {
			t.Errorf("%s: %+v", c.name, s)
			continue
		}
		for i, rule := range c.rules {
			if s[i].Rule != rule {
				t.Errorf("%s: rule %d %s, want %s", c.name, i, s[i].Rule, rule)
			}
		}
		got, sure := Sure(s)
		if sure != c.sure || (sure && got.LeaseID != c.lease) {
			t.Errorf("%s: sure %v %+v", c.name, sure, got)
		}
		if c.name == "allowance part" && (s[0].Method != "caf" || s[0].AmountCents != 21234) {
			t.Errorf("allowance %+v", s[0])
		}
	}
	// Two tenants named alike: never sure.
	twin := append(calls, OpenCall{BillingDocumentID: uuid.New(), LeaseID: uuid.New(), Number: "AL-X", Period: "2026-10", DueOn: d("2026-10-05"), OutstandingCents: 80000, Tenants: []string{"Léa Martin Durand"}})
	if _, sure := Sure(Suggest(line("VIR LEA MARTIN DURAND", 80000, "2026-10-06"), twin, r)); sure {
		t.Errorf("two leases on the same name is not sure")
	}
}

func moneyCents(c int64) money.Cents { return money.Cents(c) }
