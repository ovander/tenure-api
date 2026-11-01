package tax

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ovander/tenure-api/internal/document"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/period"

	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/payment"
)

func TestSplitMatch(t *testing.T) {
	type want struct{ rent, prov, rest money.Cents }
	cases := []struct {
		name                       string
		matched, rent, prov, total money.Cents
		want                       want
	}{
		{"paid in full", 90000, 80000, 10000, 90000, want{80000, 10000, 0}},
		{"half paid, in proportion", 45000, 80000, 10000, 90000, want{40000, 5000, 0}},
		// 10000 × 80000 / 90000 = 8888,89 → 8889; 10000 × 10000 / 90000 = 1111,11 → 1111.
		{"rounded half-up, parts add up", 10000, 80000, 10000, 90000, want{8889, 1111, 0}},
		// A credit line of −5000: the rent received stays the rent, never reduced by the credit.
		{"call with a credit line", 85000, 80000, 10000, 85000, want{80000, 10000, -5000}},
		{"debit regularisation line", 95000, 80000, 10000, 95000, want{80000, 10000, 5000}},
		{"no total", 1000, 0, 0, 0, want{0, 0, 1000}},
	}
	for _, c := range cases {
		ms, err := SplitMatch(c.matched, c.rent, c.prov, c.total)
		if err != nil {
			t.Fatal(err)
		}
		p := payment.Payment{AmountCents: c.matched, Matches: ms}
		got := want{p.Matched(payment.LineRent), p.Matched(payment.LineProvision), p.Matched(payment.LineRegularisation)}
		if got != c.want || got.rent+got.prov+got.rest != c.matched {
			t.Errorf("%s: %+v; want %+v", c.name, got, c.want)
		}
	}
}

func TestSummaryTemplateRenders(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "..", "testdata", "params", "2025.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	ps, err := params.Load(f)
	if err != nil {
		t.Fatal(err)
	}
	lm, err := ps.Form2044LineMap(2025)
	if err != nil {
		t.Fatal(err)
	}
	rent := lm.Lines["rent"]
	lock, err := ps.Int64(params.KeyReelLockYears, params.TaxYearEnd(2025))
	if err != nil {
		t.Fatal(err)
	}
	data := document.TaxSummaryData{Number: "RF-X-2026-0001", IssuedOn: period.NewDate(2026, 2, 1), Owner: document.Party{Name: "Bailleur"},
		TaxYear: 2025, Millesime: 2026, Regime: RegimeReel, LockYears: lock, MicroEligible: true, MicroGrossCents: 160000,
		Assets: []document.TaxAsset{{Label: "1 rue X", Lines: []document.TaxLine{{Line: rent, AmountCents: 160000}}, NetCents: 140000}},
		Totals: []document.TaxLine{{Line: rent, AmountCents: 160000}}, Supporting: []string{"facture.pdf"}}
	var html []byte
	html, _, err = document.Renderer{}.Render(document.ClassTaxSummary, SummaryTemplate, data, document.Footer{Number: "N"})
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"Loyers bruts", "Micro-foncier", "Régime réel", strconv.FormatInt(lock, 10) + " ans", "facture.pdf", "revenus 2025"} {
		if !strings.Contains(string(html), w) {
			t.Errorf("summary lacks %q", w)
		}
	}
	if strings.Contains(string(html), "<no value>") {
		t.Error("unfilled value")
	}
}

func TestCSV(t *testing.T) {
	sum := Summary{Year: 2025, Owners: []OwnerSummary{{OwnerID: "O1", Assets: []AssetLines{{AssetID: "A1",
		Lines: []LineAmount{{Line: "L1", AmountCents: 160000, Sources: []string{"P1", "P2"}}}}},
		Totals: []LineAmount{{Line: "L1", AmountCents: 160000, Sources: []string{"P1", "P2"}}}}},
		Flags: []Flag{{Kind: FlagMissingDocument, ID: "C1"}}}
	b, err := CSV(2025, sum, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "tax_year,owner_id,asset_id,line,amount_cents,sources\n2025,O1,A1,L1,160000,P1 P2\n2025,O1,total,L1,160000,P1 P2\n2025,,,flag:missing_document,,C1\n"
	if string(b) != want {
		t.Errorf("csv\n%s\nwant\n%s", b, want)
	}
}
