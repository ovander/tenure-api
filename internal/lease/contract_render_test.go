package lease

import (
	"strings"
	"testing"

	"github.com/ovander/tenure-api/internal/document"
	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/period"
)

func render(t *testing.T, class document.Class, version string, data any) string {
	t.Helper()
	html, _, err := document.Renderer{}.Render(class, version, data, document.Footer{Number: "N-1", SnapshotHash: "abcd"})
	if err != nil {
		t.Fatalf("%s: %v", class, err)
	}
	s := string(html)
	if strings.Contains(s, "<no value>") || strings.Contains(s, "%!") {
		t.Errorf("%s: unfilled value in %s", class, s)
	}
	return s
}

// Every contract variant renders, states its legal frame and every vetted
// clause has its French text (RENT-SPEC-008).
func TestContractVariants(t *testing.T) {
	owner := document.Party{Name: "Propriétaire A", Address: "1 rue de Lyon"}
	prev := money.Cents(80000)
	until := period.NewDate(2036, 1, 1)
	done := period.NewDate(2026, 1, 2)
	base := document.ContractData{Number: "CT-X-2026-0001", IssuedOn: period.NewDate(2026, 2, 1), Owner: owner,
		Tenants: []document.ContractTenant{{Name: "Jeanne Dupont", Solidary: true}, {Name: "Paul <i>Martin</i>", Solidary: true}},
		Assets:  []document.ContractAsset{{Kind: "apartment", Address: "2 rue de Lyon", Lot: "12", SurfaceHundredthsM2: 4520, DPEClass: "D"}},
		StartOn: period.NewDate(2026, 3, 1), TermMonths: 36, RentCents: 85000, ProvisionCents: 9000, DepositCents: 85000, DueDay: 5,
		IRLRefQuarter: "2025-Q2", IRLRefValue: 14668, RevisionMonth: 3, Controlled: true, CeilingCents: 90400, PreviousRentCents: &prev,
		Guarantees: []document.ContractGuarantee{{Kind: "deposit", AmountCents: 85000}, {Kind: "surety", Guarantor: "Marie Dupont", AmountCents: 2000000, ValidTo: &until}},
		Clauses:    VettedClauses,
		Diagnostics: []document.ContractDiagnostic{{Asset: "2 rue de Lyon", Kind: "dpe", DoneOn: &done, ValidUntil: &until, Result: "D"},
			{Asset: "2 rue de Lyon", Kind: "lead", NotRequiredReason: "immeuble de 1975"}},
		Copropriete: true}
	want := map[string][]string{
		"unfurnished": {"Contrat de location vide", "décret n° 2015-587", "habitation principale", "Clause résolutoire", "Clause de solidarité", "oyer de référence majoré"},
		"furnished":   {"Contrat de location meublée", "25-3", "inventaire détaillé du mobilier"},
		"mobility":    {"bail mobilité", "Aucun dépôt de garantie", "mutation professionnelle", "forfait mensuel"},
		"box":         {"emplacement de stationnement", "1709", "usage exclusif de stationnement"},
	}
	clauseText := []string{"chaudière", "ramoner", "jardin", "assurance contre les risques locatifs"}
	for kind, words := range want {
		d := base
		d.Kind = kind
		if kind == "mobility" {
			d.MobilityReason = string(ReasonJobTransfer)
		}
		html := render(t, document.ClassContract, ContractTemplate, d)
		for _, w := range append(words, clauseText...) {
			if !strings.Contains(html, w) {
				t.Errorf("%s contract lacks %q", kind, w)
			}
		}
		if strings.Contains(html, "<i>Martin</i>") {
			t.Errorf("%s: tenant name not escaped", kind)
		}
		if kind == "mobility" && strings.Contains(html, "Dépôt de garantie :") {
			t.Errorf("a mobility contract states a deposit")
		}
	}
	if len(clauseText) != len(VettedClauses) {
		t.Fatalf("every vetted clause needs its expected text here")
	}
}

func TestOtherLeaseDocumentsRender(t *testing.T) {
	owner := document.Party{Name: "Propriétaire A", Address: "1 rue de Lyon"}
	on := period.NewDate(2026, 2, 1)
	for _, kind := range []string{string(AmendRent), string(AmendTenantAdd), string(AmendAssetAdd)} {
		html := render(t, document.ClassAmendment, AmendmentTemplate, document.AmendmentData{Number: "AV-X-2026-0001", IssuedOn: on,
			Owner: owner, Tenants: []string{"Jeanne"}, Assets: []string{"x"}, ContractNumber: "CT-X-2026-0001", LeaseStartOn: on,
			Kind: kind, EffectiveOn: on, OldRentCents: 85000, NewRentCents: 90000, TenantName: "Paul", AssetLabel: "Box 3"})
		if !strings.Contains(html, "Avenant") || !strings.Contains(html, "CT-X-2026-0001") {
			t.Errorf("amendment %s: missing content", kind)
		}
	}
	rent := render(t, document.ClassAmendment, AmendmentTemplate, document.AmendmentData{Kind: string(AmendRent), OldRentCents: 85000, NewRentCents: 90000})
	if !strings.Contains(rent, "dépôt de garantie versé reste inchangé") {
		t.Error("a rent amendment must state that the deposit is unchanged")
	}
	dg := render(t, "deposit_receipt", DepositReceiptTemplate, document.DepositReceiptData{Number: "DG-X-2026-0001", IssuedOn: on, Owner: owner,
		Tenants: []string{"Jeanne"}, Assets: []string{"x"}, AmountCents: 85000, ReceivedOn: on, RentCents: 85000})
	if !strings.Contains(dg, document.Euros(85000)) || !strings.Contains(dg, "article 22") {
		t.Error("deposit receipt content")
	}
	ca := render(t, "surety", SuretyTemplate, document.SuretyData{Number: "CA-X-2026-0001", IssuedOn: on, Owner: owner,
		Tenants: []string{"Jeanne"}, Assets: []string{"x"}, Guarantor: document.Party{Name: "Marie Dupont", Address: "3 rue"}, Solidary: true,
		MaxAmountCents: 2000000, StartOn: on, TermMonths: 36, RentCents: 85000, IRLRefQuarter: "2025-Q2", RevisionMonth: 3})
	for _, w := range []string{"solidaire", "bénéfice de discussion", "22-1", document.Euros(2000000), "renouvellements"} {
		if !strings.Contains(ca, w) {
			t.Errorf("surety act lacks %q", w)
		}
	}
}
