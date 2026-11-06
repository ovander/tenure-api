package inventory

import (
	"strings"
	"testing"
	"time"

	"github.com/ovander/tenure-api/internal/document"
	"github.com/ovander/tenure-api/internal/period"
)

func TestSharePct(t *testing.T) {
	for bp, want := range map[int64]string{10000: "100 %", 6400: "64 %", 1250: "12,50 %", 5: "0,05 %"} {
		if got := sharePct(bp); got != want {
			t.Errorf("%d bp: %q, want %q", bp, got, want)
		}
	}
}

func TestNextStatement(t *testing.T) {
	keys := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	exit := &capture{head: captureRow{State: StateIssued, KeysReturnedOn: &keys}}
	for _, c := range []struct {
		name string
		df   depositFacts
		want string
	}{
		{"no exit", depositFacts{}, "wait_exit"},
		{"exit not issued", depositFacts{exit: &capture{head: captureRow{State: StateCompleted, KeysReturnedOn: &keys}}}, "wait_exit"},
		{"issued exit", depositFacts{exit: exit}, SettlementInitial},
		{"settled, no holdback", depositFacts{exit: exit, settlements: []settlementRow{{Kind: SettlementInitial}}}, "done"},
		{"holdback to release", depositFacts{exit: exit, settlements: []settlementRow{{Kind: SettlementInitial, HoldbackCents: 1}}}, SettlementRelease},
		{"released", depositFacts{exit: exit, settlements: []settlementRow{{Kind: SettlementInitial, HoldbackCents: 1}, {Kind: SettlementRelease}}}, "done"},
	} {
		if got := next(c.df); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

func TestRestitutionDocument(t *testing.T) {
	keys := period.NewDate(2026, 9, 30)
	st := Statement{Kind: SettlementInitial, KeysReturnedOn: &keys, Deadline: period.NewDate(2026, 11, 30), DepositCents: 160000,
		Settlement: Settlement{DeductionsCents: 30720, ArrearsCents: 5000, HoldbackCents: 32000, BalanceCents: 92280}, TotalCents: 92280}
	d := RestitutionData{Number: "RD-X-2026-0001", IssuedOn: period.NewDate(2026, 10, 15), ExitNumber: "ES-X-2026-0001",
		Owner: document.Party{Name: "Bailleur Test", Address: "1 rue A, Lyon"}, Tenants: []string{"Léa Martin"}, Premises: []string{"12 rue des Lilas"},
		Statement: st, KeysReturnedOn: keys, TotalAbs: 92280,
		Lines: []RestitutionLine{{Item: "Séjour · Parquet", Entry: "Bon état", Exit: "Mauvais état", Description: "Ponçage", Basis: "devis",
			CostCents: 48000, AmountCents: 30720, SharePct: "64 %", Grid: "Accord collectif", Document: strings.Repeat("a", 64)}}}
	html, _, err := document.Renderer{}.Render(document.ClassRestitution, RestitutionTemplate, d, document.Footer{Number: d.Number, SnapshotHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	s := string(html)
	for _, want := range []string{"Restitution du dépôt de garantie", "RD-X-2026-0001", "ES-X-2026-0001", "30/09/2026", "30/11/2026",
		"Séjour · Parquet", "Bon état → Mauvais état", "64 %", "Accord collectif", "Provision retenue", "À restituer au locataire", "second relevé"} {
		if !strings.Contains(s, want) {
			t.Errorf("the RD document lacks %q", want)
		}
	}
	d.Owed, d.Lines, d.Statement.HoldbackCents = true, nil, 0
	html, _, _ = document.Renderer{}.Render(document.ClassRestitution, RestitutionTemplate, d, document.Footer{Number: d.Number, SnapshotHash: "x"})
	if s := string(html); !strings.Contains(s, "Reste dû par le locataire") || !strings.Contains(s, "Aucune retenue") || strings.Contains(s, "second relevé") {
		t.Error("a statement without deductions or holdback, owed by the tenant")
	}
	d.Release, d.FirstNumber, d.Owed = true, "RD-X-2026-0001", false
	d.Statement = Statement{Kind: SettlementRelease, Deadline: period.NewDate(2027, 5, 15), DepositCents: 32000,
		Settlement: Settlement{RegularisationCents: 12000, BalanceCents: 20000}, TotalCents: 20000}
	html, _, _ = document.Renderer{}.Render(document.ClassRestitution, RestitutionTemplate, d, document.Footer{Number: d.Number, SnapshotHash: "x"})
	if s := string(html); !strings.Contains(s, "Solde de la provision") || !strings.Contains(s, "fait suite au relevé n° RD-X-2026-0001") || !strings.Contains(s, "15/05/2027") {
		t.Error("the release statement")
	}
}
