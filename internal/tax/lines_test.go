package tax

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ovander/tenure-api/internal/charge"
	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/payment"
	"github.com/ovander/tenure-api/internal/period"
)

func fixtures(t *testing.T) params.Set {
	t.Helper()
	var sets []*os.File
	for _, y := range []string{"2025", "2026"} {
		f, err := os.Open(filepath.Join("..", "..", "testdata", "params", y+".json"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.Close() })
		sets = append(sets, f)
	}
	s, err := params.Load(sets[0], sets[1])
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func d(y int, m time.Month, day int) period.Date { return period.NewDate(y, m, day) }

func rentPayment(id, asset string, paid period.Date, rent, prov money.Cents) payment.Payment {
	return payment.Payment{ID: id, LeaseID: "L", AssetID: asset, PaidOn: paid, AmountCents: rent + prov,
		Matches: []payment.Match{{BillingDocumentID: "AL-" + id, Line: payment.LineRent, AmountCents: rent},
			{BillingDocumentID: "AL-" + id, Line: payment.LineProvision, AmountCents: prov}}}
}

func line(lines []LineAmount, l string) money.Cents {
	for _, x := range lines {
		if x.Line == l {
			return x.AmountCents
		}
	}
	return 0
}

var single = []AssetShares{{AssetID: "A", Shares: []OwnerShare{{OwnerID: "O1", BP: 10000}}}}

func TestLinesIncomeAndExpenses(t *testing.T) {
	pays := []payment.Payment{
		rentPayment("p1", "A", d(2025, 1, 5), 100000, 9000),
		rentPayment("p2", "A", d(2026, 1, 5), 100000, 9000),                // next year: not counted
		{ID: "p3", AssetID: "A", PaidOn: d(2025, 2, 5), AmountCents: 5000}, // unmatched
	}
	charges := []charge.Charge{
		{ID: "repair", AssetID: "A", Category: charge.CategoryRepairs, AmountCents: 30000, PaidOn: d(2025, 3, 1), DocumentID: "doc"},
		{ID: "teom", AssetID: "A", Category: charge.CategoryTEOM, AmountCents: 12000, PaidOn: d(2025, 10, 1), RecoverableBP: 10000, DocumentID: "doc"},
		{ID: "prov", AssetID: "A", Category: charge.CategoryCoproProvision, AmountCents: 40000, PaidOn: d(2025, 4, 1), RecoverableBP: 4000, DocumentID: "doc"},
		{ID: "reg", AssetID: "A", Category: charge.CategoryCoproRegularisationPrior, AmountCents: 3000, PaidOn: d(2025, 5, 1), DocumentID: "doc"},
		{ID: "unrec", AssetID: "A", Category: charge.CategoryUnrecoveredAtClose, AmountCents: 7000, PaidOn: d(2025, 6, 1), RecoverableBP: 10000, DocumentID: "doc"},
		{ID: "nodoc", AssetID: "A", Category: charge.CategoryInsurance, AmountCents: 15000, PaidOn: d(2025, 1, 10)},
		{ID: "late", AssetID: "A", Category: charge.CategoryRepairs, AmountCents: 99999, ChargedOn: d(2025, 12, 1), PaidOn: d(2026, 1, 10)},
		{ID: "unpaid", AssetID: "A", Category: charge.CategoryRepairs, AmountCents: 88888, ChargedOn: d(2025, 12, 1)},
	}
	s, err := Lines(pays, charges, single, 2025, fixtures(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.Millesime != 2026 || len(s.Owners) != 1 {
		t.Fatalf("summary %+v", s)
	}
	o := s.Owners[0]
	want := map[string]money.Cents{
		"211": 100000, // rent only: provisions received are not income
		"222": 2000,   // one property earned rent
		"223": 15000,
		"224": 30000,
		"225": 7000,  // unrecovered at lease close: in full
		"229": 24000, // non-recoverable 60 % of the provisions
		"230": 3000,
		"227": 0, // TEOM is recoverable: not deducted
	}
	for l, w := range want {
		if got := line(o.Totals, l); got != w {
			t.Errorf("line %s = %d; want %d", l, got, w)
		}
	}
	// net = 100000 − (2000+15000+30000+7000+24000 − 3000)
	if o.Reel.NetCents != 25000 || o.Assets[0].NetCents != 25000 || o.Reel.TaxableCents != 25000 {
		t.Errorf("reel %+v asset net %d", o.Reel, o.Assets[0].NetCents)
	}
	if !o.Micro.Eligible || o.Micro.TaxableCents != 70000 || o.Micro.AllowanceCents != 30000 {
		t.Errorf("micro %+v", o.Micro)
	}
	wantFlags := []Flag{{FlagMissingDocument, "nodoc"}, {FlagUnmatchedPayment, "p3"}}
	if len(s.Flags) != 2 || s.Flags[0] != wantFlags[0] || s.Flags[1] != wantFlags[1] {
		t.Errorf("flags %+v", s.Flags)
	}
}

func TestLinesRefuses(t *testing.T) {
	ps := fixtures(t)
	if _, err := Lines(nil, nil, single, 2026, ps, nil); !errors.Is(err, params.ErrNotFound) {
		t.Errorf("no map for millésime 2027: %v; want ErrNotFound", err)
	}
	odd := []charge.Charge{{ID: "x", AssetID: "A", Category: "boat", AmountCents: 1, PaidOn: d(2025, 1, 1)}}
	if _, err := Lines(nil, odd, single, 2025, ps, nil); !errors.Is(err, ErrUnmapped) {
		t.Errorf("unknown category: %v; want ErrUnmapped", err)
	}
	half := []AssetShares{{AssetID: "A", Shares: []OwnerShare{{OwnerID: "O1", BP: 5000}}}}
	if _, err := Lines(nil, nil, half, 2025, ps, nil); err == nil {
		t.Error("shares adding to 50 % accepted")
	}
	stray := []payment.Payment{rentPayment("p", "Z", d(2025, 1, 1), 1, 0)}
	if _, err := Lines(stray, nil, single, 2025, ps, nil); err == nil {
		t.Error("payment for an unknown asset accepted")
	}
}

func TestLinesCoOwnershipSplit(t *testing.T) {
	assets := []AssetShares{{AssetID: "H", Shares: []OwnerShare{{OwnerID: "O1", BP: 5000}, {OwnerID: "O2", BP: 5000}}}}
	pays := []payment.Payment{rentPayment("p", "H", d(2025, 1, 5), 100001, 0)}
	charges := []charge.Charge{{ID: "r", AssetID: "H", Category: charge.CategoryRepairs, AmountCents: 333, PaidOn: d(2025, 2, 1), DocumentID: "x"}}
	s, err := Lines(pays, charges, assets, 2025, fixtures(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	o1, o2 := s.Owners[0], s.Owners[1]
	if line(o1.Totals, "211")+line(o2.Totals, "211") != 100001 || line(o1.Totals, "211") != 50001 {
		t.Errorf("211 split %d / %d", line(o1.Totals, "211"), line(o2.Totals, "211"))
	}
	if line(o1.Totals, "224") != 167 || line(o2.Totals, "224") != 166 || line(o1.Totals, "222") != 1000 {
		t.Errorf("224 split %d / %d, 222 %d", line(o1.Totals, "224"), line(o2.Totals, "224"), line(o1.Totals, "222"))
	}
}

func TestLinesFurnished(t *testing.T) {
	assets := []AssetShares{{AssetID: "S", Furnished: true, Shares: []OwnerShare{{OwnerID: "O1", BP: 10000}}}}
	pays := []payment.Payment{rentPayment("p", "S", d(2025, 1, 5), 62000, 5000)}
	charges := []charge.Charge{{ID: "r", AssetID: "S", Category: charge.CategoryRepairs, AmountCents: 1000, PaidOn: d(2025, 2, 1)}}
	s, err := Lines(pays, charges, assets, 2025, fixtures(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	o := s.Owners[0]
	if o.FurnishedGrossReceiptsCents != 62000 || len(o.Totals) != 0 || o.Micro.GrossCents != 0 || len(s.Flags) != 0 {
		t.Errorf("furnished %+v flags %v", o, s.Flags)
	}
}

func TestDeficits(t *testing.T) {
	cases := []struct {
		name                    string
		income, interest, other money.Cents
		prior                   []Deficit
		taxable, global         money.Cents
		carried, used, expired  []Deficit
	}{
		{name: "profit uses priors oldest first, expired dropped",
			income: 500000, interest: 100000, other: 100000,
			prior:   []Deficit{{2020, 250000}, {2014, 90000}, {2018, 100000}},
			taxable: 0, used: []Deficit{{2018, 100000}, {2020, 200000}}, carried: []Deficit{{2020, 50000}}, expired: []Deficit{{2014, 90000}}},
		{name: "profit larger than priors",
			income: 500000, interest: 0, other: 100000, prior: []Deficit{{2015, 100000}},
			taxable: 300000, used: []Deficit{{2015, 100000}}},
		{name: "deficit from other expenses goes to global income",
			income: 720006, interest: 150000, other: 1086501,
			global: 516495, prior: []Deficit{{2022, 50000}}, carried: []Deficit{{2022, 50000}}},
		{name: "interest above income is carried forward",
			income: 100000, interest: 150000, other: 80000,
			global: 80000, carried: []Deficit{{2025, 50000}}},
		{name: "global part above the cap is carried forward",
			income: 0, interest: 0, other: 1500000,
			global: 1070000, carried: []Deficit{{2025, 430000}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Reel{IncomeCents: c.income, InterestCents: c.interest, OtherExpensesCents: c.other,
				NetCents: c.income - c.interest - c.other}
			if err := applyDeficits(&r, c.prior, 2025, 10, 1070000); err != nil {
				t.Fatal(err)
			}
			eq := func(a, b []Deficit) bool {
				if len(a) != len(b) {
					return false
				}
				for i := range a {
					if a[i] != b[i] {
						return false
					}
				}
				return true
			}
			if r.TaxableCents != c.taxable || r.GlobalImputableCents != c.global ||
				!eq(r.CarriedForward, c.carried) || !eq(r.PriorUsed, c.used) || !eq(r.Expired, c.expired) {
				t.Errorf("got %+v", r)
			}
		})
	}
	r := Reel{}
	if err := applyDeficits(&r, []Deficit{{2025, 1}}, 2025, 10, 1); err == nil {
		t.Error("prior deficit of the same year accepted")
	}
}
