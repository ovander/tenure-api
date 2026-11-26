package charge

import (
	"errors"
	"strings"
	"testing"

	"github.com/ovander/tenure-api/internal/money"
)

func TestParseCents(t *testing.T) {
	cases := map[string]money.Cents{"1 234,56": 123456, "12": 1200, "0,5": 50, "": 0, "-3,10": -310, "1 000,00 €": 100000}
	for in, want := range cases {
		got, err := ParseCents(in, false)
		if err != nil || got != want {
			t.Errorf("%q: %d %v; want %d", in, got, err, want)
		}
	}
	if got, err := ParseCents("1,234.56", true); err != nil || got != 123456 {
		t.Errorf("decimal point: %d %v", got, err)
	}
	for _, bad := range []string{"1,234", "abc", "1,2,3", "1234.56", "12.5"} {
		if _, err := ParseCents(bad, false); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestParseSyndic(t *testing.T) {
	m := Mapping{Label: "Libellé", Amount: "Montant", Recoverable: "Récupérable"}
	csv := "\xef\xbb\xbfLibellé;Montant;Récupérable;Clé\n" +
		"Eau froide;412,30;412,30;tantièmes\n" +
		"Honoraires syndic;1 050,00;;tantièmes\n" +
		";;;\n" +
		"Ascenseur;300,00;240,00;\n"
	lines, err := ParseSyndic([]byte(csv), m)
	if err != nil {
		t.Fatal(err)
	}
	want := []SyndicLine{{2, "Eau froide", 41230, 41230}, {3, "Honoraires syndic", 105000, 0}, {5, "Ascenseur", 30000, 24000}}
	if len(lines) != len(want) {
		t.Fatalf("%+v", lines)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d: %+v; want %+v", i, lines[i], want[i])
		}
	}
	bad := map[string]string{
		"recoverable above the amount": "Libellé;Montant;Récupérable\nX;10,00;12,00\n",
		"negative amount":              "Libellé;Montant;Récupérable\nX;-10,00;\n",
		"unknown column":               "Label;Montant;Récupérable\nX;10,00;\n",
		"unreadable amount":            "Libellé;Montant;Récupérable\nX;dix;\n",
		"no line":                      "Libellé;Montant;Récupérable\n",
	}
	for name, in := range bad {
		_, err := ParseSyndic([]byte(in), m)
		if !errors.Is(err, ErrSyndicFile) {
			t.Errorf("%s: %v", name, err)
		}
		if name == "negative amount" && !strings.Contains(err.Error(), "row 2") {
			t.Errorf("the error names its row: %v", err)
		}
	}
	tab := Mapping{Delimiter: "\t", DecimalPoint: true, Label: "label", Amount: "amount"}
	if l, err := ParseSyndic([]byte("label\tamount\nWater\t1,000.50\n"), tab); err != nil || l[0].AmountCents != 100050 {
		t.Errorf("tab and decimal point: %+v %v", l, err)
	}
}
