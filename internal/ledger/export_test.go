package ledger

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/period"
)

func TestFECAmountsAndText(t *testing.T) {
	for c, want := range map[money.Cents]string{0: "0,00", 5: "0,05", 123456: "1234,56", -250: "-2,50"} {
		if got := fecAmount(c); got != want {
			t.Errorf("fecAmount(%d) = %q, want %q", c, got, want)
		}
	}
	d := period.NewDate(2026, 3, 9)
	es := []Entry{{Date: d, Journal: JournalBank, Source: "S", Label: "Prime d’assurance\tété",
		Lines: []Line{{Account: "616000", Label: "Assurance", DebitCents: 100}, {Account: "512000", Label: "Banque", CreditCents: 100}}}}
	if _, err := FEC(es, period.Date{}); err == nil {
		t.Fatal("a FEC without its closing day")
	}
	b, err := FEC(es, period.NewDate(2027, 5, 20))
	if err != nil {
		t.Fatal(err)
	}
	rows := strings.Split(strings.TrimSuffix(string(b), "\r\n"), "\r\n")
	if len(rows) != 3 || len(strings.Split(rows[1], "\t")) != len(fecHeader) {
		t.Fatalf("rows %q", rows)
	}
	if !bytes.Contains(b, []byte("Prime d'assurance \xe9t\xe9")) || !strings.HasPrefix(rows[1], "BQ\tBanque\t1\t20260309\t616000") ||
		!strings.Contains(rows[1], "\t1,00\t0,00\t\t\t20270520\t") {
		t.Fatalf("row %q", rows[1])
	}
}

func TestPiecesAndPack(t *testing.T) {
	es := []Entry{{Source: "c1", DocumentID: "D1"}, {Source: "c2", DocumentID: "D1"}, {Source: "c3"}, {Source: "c1", DocumentID: "D1"}}
	ps := Pieces(es, map[string]string{"D0": "tax_summary:2026"})
	if len(ps) != 2 || ps[0].DocumentID != "D0" || strings.Join(ps[1].Sources, ",") != "c1,c2" {
		t.Fatalf("pieces %+v", ps)
	}
	at := time.Date(2027, 5, 20, 8, 0, 0, 0, time.UTC)
	files := []PackFile{{Name: "a.csv", Data: []byte("x")}, {Name: "LISEZMOI.txt", Data: []byte("y")}}
	a, err := Pack(files, at)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Pack(files, at)
	if !bytes.Equal(a, b) {
		t.Fatal("the same pack zipped to different bytes")
	}
	z, err := zip.NewReader(bytes.NewReader(a), int64(len(a)))
	if err != nil || len(z.File) != 2 || z.File[0].Name != "a.csv" {
		t.Fatalf("zip %v %v", z, err)
	}
}
