package document

import (
	"strings"
	"testing"
	"time"

	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/period"
)

func TestFrenchFormats(t *testing.T) {
	cases := map[money.Cents]string{0: "0,00 €", 5: "0,05 €", 123456: "1 234,56 €",
		100000000: "1 000 000,00 €", -2527: "−25,27 €"}
	for c, want := range cases {
		if got := Euros(c); got != want {
			t.Errorf("Euros(%d) = %q; want %q", c, got, want)
		}
	}
	if DateFR(period.NewDate(2025, 3, 5)) != "05/03/2025" || PeriodFR(period.Period{Year: 2025, Month: 8}) != "août 2025" || Index100(14668) != "146,68" {
		t.Error("date, period or index format")
	}
	if RetainUntil(time.Date(2025, 3, 1, 9, 0, 0, 0, time.UTC), 10) != period.NewDate(2035, 3, 1) {
		t.Error("retention date")
	}
}

func TestRenderEveryTemplate(t *testing.T) {
	owner := Party{Name: "Propriétaire A", Address: "1 rue de Lyon"}
	data := map[Class]any{
		ClassRentCall: RentCallData{Number: "AL-X-2025-0001", IssuedOn: period.NewDate(2025, 3, 1), Owner: owner,
			Tenants: []string{"Jeanne <b>Dupont</b>"}, Assets: []string{"1 rue de Lyon"}, Period: period.Period{Year: 2025, Month: 3},
			DueOn: period.NewDate(2025, 3, 5), Lines: []Line{{Label: "Loyer", AmountCents: 46613}}, TotalCents: 46613},
		ClassRevisionNotice: RevisionData{Number: "RV-X-2025-0001", IssuedOn: period.NewDate(2025, 7, 20), Owner: owner,
			Tenants: []string{"Jeanne"}, Assets: []string{"x"}, RefQuarter: "2024-Q2", RefValue: 14517, NewQuarter: "2025-Q2",
			NewValue: 14668, OldRentCents: 70000, NewRentCents: 70728, EffectiveFrom: period.Period{Year: 2025, Month: 8}},
		ClassReceipt: ReceiptData{Number: "QL-X-2025-0001", IssuedOn: period.NewDate(2025, 3, 20), Owner: owner, Tenants: []string{"Jeanne"},
			Assets: []string{"x"}, Period: period.Period{Year: 2025, Month: 3}, RentCallNumber: "AL-X-2025-0001",
			Lines: []Line{{Label: "Loyer", AmountCents: 46613}}, TotalCents: 46613, RentCents: 46613, PaidCents: 46613,
			Payments: []PaymentLine{{PaidOn: period.NewDate(2025, 3, 18), Method: "virement", AmountCents: 46613}}},
		ClassCancellation: CancellationData{Number: "AL-X-2025-0002", IssuedOn: period.NewDate(2025, 3, 2), Owner: owner,
			Tenants: []string{"Jeanne"}, CancelledNumber: "AL-X-2025-0001", CancelledIssuedOn: period.NewDate(2025, 3, 1),
			Period: period.Period{Year: 2025, Month: 3}, Reason: "erreur de montant", TotalCents: -46613},
	}
	for class, d := range data {
		html, footer, err := Renderer{}.Render(class, "v1", d, Footer{Number: "N-1", SnapshotHash: "abcd"})
		if err != nil {
			t.Fatalf("%s: %v", class, err)
		}
		if !strings.Contains(string(html), "Propriétaire A") || !strings.Contains(string(footer), "N° N-1") {
			t.Errorf("%s: missing content", class)
		}
		if strings.Contains(string(html), "<b>Dupont</b>") {
			t.Errorf("%s: tenant name not escaped", class)
		}
	}
	if _, _, err := (Renderer{}).Render(ClassRentCall, "v9", data[ClassRentCall], Footer{}); err == nil {
		t.Error("unknown template version accepted")
	}
}
