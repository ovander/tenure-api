package payment

import (
	"errors"
	"testing"

	"golang.org/x/text/encoding/charmap"
)

func TestParseCSV(t *testing.T) {
	csv := "Relevé du compte courant;;;\n" +
		"Date;Libellé;Débit;Crédit\n" +
		"05/10/2026;VIR SEPA LEA MARTIN LOYER OCT;;800,00\n" +
		"07/10/2026;PRLV SYNDIC RESIDENCE;1 250,40;\n" +
		";Solde au 31/10;;\n" +
		"05/10/2026;VIR SEPA LEA MARTIN LOYER OCT;;800,00\n"
	// The title row is the header the mapping does not know: a bank export
	// with a title is read from its real header.
	if _, err := ParseCSV([]byte(csv), CSVMapping{Date: "Date", Label: "Libellé", Debit: "Débit", Credit: "Crédit"}); !errors.Is(err, ErrStatement) {
		t.Errorf("title row as header: %v", err)
	}
	body := csv[len("Relevé du compte courant;;;\n"):]
	lines, err := ParseCSV([]byte(body), CSVMapping{Date: "Date", Label: "Libellé", Debit: "Débit", Credit: "Crédit"})
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 3 || lines[0].AmountCents != 80000 || lines[1].AmountCents != -125040 || lines[0].BookedOn.String() != "2026-10-05" {
		t.Fatalf("lines %+v", lines)
	}
	if lines[0].Key == lines[2].Key || lines[0].Key[:5] != "hash:" {
		t.Errorf("two equal lines keep two keys: %s %s", lines[0].Key, lines[2].Key)
	}
	again, _ := ParseCSV([]byte(body), CSVMapping{Date: "Date", Label: "Libellé", Debit: "Débit", Credit: "Crédit"})
	if again[0].Key != lines[0].Key || again[2].Key != lines[2].Key {
		t.Errorf("keys are stable across imports")
	}

	signed := "date,label,amount,ref\n2026-10-05,Rent,800.00,R1\n2026-10-06,Fee,-1.50,\n"
	lines, err = ParseCSV([]byte(signed), CSVMapping{Delimiter: ",", DecimalPoint: true, Date: "date", DateFormat: "2006-01-02", Amount: "amount", Label: "label", Reference: "ref"})
	if err != nil || len(lines) != 2 || lines[1].AmountCents != -150 || lines[0].Reference != "R1" {
		t.Fatalf("signed %+v %v", lines, err)
	}

	latin, _ := charmap.Windows1252.NewEncoder().String("Date;Libellé;Montant\n05/10/2026;Loyer réglé;800,00\n")
	lines, err = ParseCSV([]byte(latin), CSVMapping{Date: "Date", Label: "Libellé", Amount: "Montant"})
	if err != nil || len(lines) != 1 || lines[0].Label != "Loyer réglé" {
		t.Errorf("Windows-1252 %+v %v", lines, err)
	}

	for name, c := range map[string]struct {
		data string
		m    CSVMapping
	}{
		"bad date":      {"Date;L;M\n31/02/2026;x;1,00\n", CSVMapping{Date: "Date", Label: "L", Amount: "M"}},
		"no amount":     {"Date;L;M\n05/10/2026;x;\n", CSVMapping{Date: "Date", Label: "L", Amount: "M"}},
		"bad amount":    {"Date;L;M\n05/10/2026;x;12,345\n", CSVMapping{Date: "Date", Label: "L", Amount: "M"}},
		"both amounts":  {"Date;L;M;D\n", CSVMapping{Date: "Date", Label: "L", Amount: "M", Debit: "D"}},
		"unknown col":   {"Date;L\n", CSVMapping{Date: "Date", Label: "X", Amount: "L"}},
		"bad delimiter": {"a\n", CSVMapping{Delimiter: ";;", Date: "a", Label: "a", Amount: "a"}},
	} {
		if _, err := ParseCSV([]byte(c.data), c.m); !errors.Is(err, ErrStatement) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestParseOFX(t *testing.T) {
	v1 := "OFXHEADER:100\r\nDATA:OFXSGML\r\nCHARSET:1252\r\n\r\n<OFX><BANKMSGSRSV1><STMTTRNRS><STMTRS><BANKTRANLIST>\r\n" +
		"<STMTTRN>\r\n<TRNTYPE>CREDIT\r\n<DTPOSTED>20261005\r\n<TRNAMT>800.00\r\n<FITID>A1\r\n<NAME>VIR LEA MARTIN\r\n<MEMO>LOYER OCTOBRE\r\n</STMTTRN>\r\n" +
		"<STMTTRN>\r\n<TRNTYPE>DEBIT\r\n<DTPOSTED>20261007120000[+1:CET]\r\n<TRNAMT>-1250,40\r\n<FITID>A2\r\n<NAME>PRLV SYNDIC\r\n</STMTTRN>\r\n" +
		"</BANKTRANLIST></STMTRS></STMTTRNRS></BANKMSGSRSV1></OFX>\r\n"
	lines, err := ParseOFX([]byte(v1))
	if err != nil || len(lines) != 2 {
		t.Fatalf("v1 %+v %v", lines, err)
	}
	if lines[0].AmountCents != 80000 || lines[0].Label != "VIR LEA MARTIN LOYER OCTOBRE" || lines[0].Key != "fitid:A1" ||
		lines[1].AmountCents != -125040 || lines[1].BookedOn.String() != "2026-10-07" {
		t.Errorf("v1 %+v", lines)
	}
	v2 := `<?xml version="1.0"?><OFX><BANKTRANLIST><STMTTRN><DTPOSTED>20261005</DTPOSTED><TRNAMT>12.5</TRNAMT><NAME>CAF</NAME></STMTTRN></BANKTRANLIST></OFX>`
	lines, err = ParseOFX([]byte(v2))
	if err != nil || len(lines) != 1 || lines[0].AmountCents != 1250 || lines[0].Key[:5] != "hash:" {
		t.Errorf("v2 %+v %v", lines, err)
	}
	for name, data := range map[string]string{
		"no transaction": "<OFX></OFX>",
		"bad date":       "<STMTTRN><DTPOSTED>2026<TRNAMT>1.00</STMTTRN>",
		"zero amount":    "<STMTTRN><DTPOSTED>20261005<TRNAMT>0.00</STMTTRN>",
	} {
		if _, err := ParseOFX([]byte(data)); !errors.Is(err, ErrStatement) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
