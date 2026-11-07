package lease

import (
	"errors"
	"strings"
	"testing"

	"github.com/ovander/tenure-api/internal/document"
	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/period"
)

func day(s string) period.Date {
	d, err := period.ParseDate(s)
	if err != nil {
		panic(err)
	}
	return d
}

func TestNoticeEnd(t *testing.T) {
	for _, c := range []struct {
		received string
		months   int
		want     string
	}{
		{"2026-03-15", 3, "2026-06-14"},
		{"2026-03-01", 1, "2026-03-31"},
		{"2026-01-31", 1, "2026-02-28"},
		{"2027-01-30", 1, "2027-02-28"},
		{"2028-01-30", 1, "2028-02-29"},
		{"2026-12-31", 3, "2027-03-30"},
	} {
		if got := noticeEnd(day(c.received), c.months); got != day(c.want) {
			t.Errorf("noticeEnd(%s, %d) = %s, want %s", c.received, c.months, got, c.want)
		}
	}
}

func TestTermEnd(t *testing.T) {
	for _, c := range []struct {
		start  string
		months int
		kind   Kind
		on     string
		want   string
		ok     bool
	}{
		{"2024-01-01", 36, KindUnfurnished, "2026-11-07", "2026-12-31", true},
		{"2024-01-01", 36, KindUnfurnished, "2026-12-31", "2026-12-31", true},
		{"2024-01-01", 36, KindUnfurnished, "2027-01-01", "2029-12-31", true},
		{"2025-10-01", 12, KindFurnished, "2027-02-01", "2027-09-30", true},
		{"2024-01-31", 1, KindMobility, "2024-02-15", "2024-02-28", true},
		{"2026-06-01", 6, KindMobility, "2026-12-01", "", false},
		{"2026-06-01", 0, KindUnfurnished, "2026-07-01", "", false},
	} {
		got, ok := TermEnd(day(c.start), c.months, c.kind, day(c.on))
		if ok != c.ok || (ok && got != day(c.want)) {
			t.Errorf("TermEnd(%s, %d, %s, %s) = %s %v, want %s %v", c.start, c.months, c.kind, c.on, got, ok, c.want, c.ok)
		}
	}
}

func TestOwnerGroundIsRequired(t *testing.T) {
	r, err := fixtures(t).Notice(day("2026-05-01"))
	if err != nil {
		t.Fatal(err)
	}
	f := NoticeFacts{Kind: KindUnfurnished, StartOn: day("2024-01-01"), TermMonths: 36}
	for name, g := range map[string]NoticeGround{
		"no reason":          {Protected: ProtectedNo},
		"repossession alone": {Reason: OwnerRepossession, Protected: ProtectedNo},
		"unknown relation":   {Reason: OwnerRepossession, Protected: ProtectedNo, Beneficiary: &Beneficiary{Name: "A", Address: "B", Relationship: "cousin"}},
		"empty grounds":      {Reason: OwnerLegitimate, Protected: ProtectedNo, Grounds: "  "},
	} {
		_, err := OwnerNotice(f, g, day("2026-05-01"), day("2026-05-01"), r)
		var ie *invariant.Error
		if !errors.As(err, &ie) || ie.Type != TypeNoticeInvalid {
			t.Errorf("%s: %v, want %s", name, err, TypeNoticeInvalid)
		}
	}
}

// The owner's letter renders for each ground with what the law requires.
func TestNoticeLetterRenders(t *testing.T) {
	base := noticeLetterData{Number: "CG-0A1B2C3D-2026-0001", IssuedOn: day("2026-05-01"),
		Owner: document.Party{Name: "Propriétaire A", Address: "1 rue de Lyon"}, Tenants: []string{"Locataire B"},
		Premises: []string{"12 rue des Lilas, 69003 Lyon"}, ContractNumber: "CT-0A1B2C3D-2024-0001", StartOn: day("2024-01-01"),
		EndOn: day("2026-12-31"), LatestReceiptOn: day("2026-06-30"), Months: 6}
	cases := map[string]struct {
		g    NoticeGround
		rel  string
		want []string
	}{
		"sale": {g: NoticeGround{Reason: OwnerSale, PriceCents: 28500000, Conditions: "Paiement comptant", Protected: ProtectedNo},
			want: []string{"Congé pour vente", "vaut offre de vente", document.Euros(28500000), "cinq premiers alinéas", "arrêté du 13 décembre 2017", "31/12/2026", "30/06/2026"}},
		"repossession": {g: NoticeGround{Reason: OwnerRepossession, Protected: ProtectedRehousing,
			Beneficiary: &Beneficiary{Name: "Claire Exemple", Relationship: "descendant", Address: "3 rue Fictive"}}, rel: relationshipFR["descendant"],
			want: []string{"Congé pour reprise", "Claire Exemple", "un descendant du bailleur", "propose un logement", "arrêté du 13 décembre 2017"}},
		"legitimate": {g: NoticeGround{Reason: OwnerLegitimate, Grounds: "Troubles de voisinage", Protected: ProtectedNo},
			want: []string{"motif légitime et sérieux", "Troubles de voisinage"}},
	}
	for name, c := range cases {
		d := base
		d.Ground, d.Relationship = c.g, c.rel
		html := render(t, document.ClassNotice, NoticeTemplate, d)
		for _, w := range c.want {
			if !strings.Contains(html, w) {
				t.Errorf("%s: missing %q", name, w)
			}
		}
		if name == "legitimate" && strings.Contains(html, "Pièces jointes") {
			t.Errorf("legitimate: no annex expected")
		}
	}
}
