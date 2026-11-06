package inventory

import (
	"strings"
	"testing"

	"github.com/ovander/tenure-api/internal/document"
	"github.com/ovander/tenure-api/internal/lease"
	"github.com/ovander/tenure-api/internal/period"
)

func TestSplitName(t *testing.T) {
	for in, want := range map[string][2]string{"Léa Martin": {"Léa", "Martin"}, "Jean de La Fontaine": {"Jean de La", "Fontaine"},
		"Cher": {"Cher", "Cher"}, "  Paul Durand ": {"Paul", "Durand"}} {
		if f, l := splitName(in); f != want[0] || l != want[1] {
			t.Errorf("%q: %q %q", in, f, l)
		}
	}
}

func TestSignableParties(t *testing.T) {
	c := sampleCapture()
	c.sigs = c.sigs[:1] // the owner has signed
	c.facts.Signers = []lease.InventorySigner{{Name: "Léa Martin"}, {Name: "Paul Durand"}}
	ok := []SignerInput{{Party: "tenant", SignerName: "Léa Martin", Email: "lea@example.test"}, {Party: "tenant", SignerName: "Paul Durand", Email: "paul@example.test"}}
	if open, err := signable(c, ok, true); err != nil || len(open) != 2 {
		t.Fatalf("two co-tenants still to sign: %v %v", open, err)
	}
	for name, in := range map[string][]SignerInput{
		"none":           nil,
		"already signed": {{Party: "owner", SignerName: "Bailleur A", Email: "a@example.test"}},
		"not a party":    {{Party: "tenant", SignerName: "Jean Inconnu", Email: "j@example.test"}},
		"twice":          {ok[0], ok[0]},
		"no e-mail":      {{Party: "tenant", SignerName: "Léa Martin"}},
		"bad e-mail":     {{Party: "tenant", SignerName: "Léa Martin", Email: "Léa <lea@example.test>"}},
		"long reserves":  {{Party: "tenant", SignerName: "Léa Martin", Email: "lea@example.test", Reservations: strings.Repeat("x", maxReservations+1)}},
	} {
		if _, err := signable(c, in, true); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// By hand, no e-mail is needed.
	if _, err := signable(c, []SignerInput{{Party: "tenant", SignerName: "Léa Martin"}}, false); err != nil {
		t.Errorf("a hand signature without e-mail: %v", err)
	}
}

func TestDraftToSign(t *testing.T) {
	c := sampleCapture()
	d := entryData(c, nil)
	d.Draft, d.CompletionDays, d.HeatingMonths = true, 7, 1
	d.Signers = []EntrySigner{{Party: "Bailleur", Name: "Bailleur A", Anchor: "{{s1|signature|180|60}}"}, {Party: "Locataire", Name: "Léa Martin"}}
	html, _, err := document.Renderer{}.Render(document.ClassInventory, DocumentTemplate, d, document.Footer{Number: "Projet", SnapshotHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	s := string(html)
	for _, want := range []string{"Projet soumis à la signature des parties", "{{s1|signature|180|60}}", "Léa Martin"} {
		if !strings.Contains(s, want) {
			t.Errorf("the draft lacks %q", want)
		}
	}
	if strings.Contains(s, "N° ") || strings.Contains(s, "émis le") {
		t.Error("a draft carries a number")
	}
	// Issued: the signatures with their evidence, no draft banner.
	d = entryData(c, nil)
	d.Number, d.IssuedOn, d.CompletionDays, d.HeatingMonths = "EE-X-2026-0001", period.NewDate(2026, 3, 2), 7, 1
	d.Signatures[0].Evidence = strings.Repeat("c", 64)
	html, _, _ = document.Renderer{}.Render(document.ClassInventory, DocumentTemplate, d, document.Footer{Number: d.Number, SnapshotHash: "x"})
	if s = string(html); strings.Contains(s, "Projet soumis") || !strings.Contains(s, strings.Repeat("c", 64)) {
		t.Error("the issued document")
	}
}
