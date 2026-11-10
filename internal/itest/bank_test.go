//go:build integration

package itest

import (
	"testing"
)

const (
	subBank  = "9971" // owner of the bank import scenario (RENT-SPEC-013, part 14a)
	subBankX = "9972"
)

type bankImport struct {
	New        int `json:"new"`
	Duplicates int `json:"duplicates"`
	Lines      []struct {
		AmountCents int64  `json:"amount_cents"`
		Key         string `json:"key"`
	} `json:"lines"`
}

type bankLines struct {
	Items []struct {
		ID          string `json:"id"`
		AmountCents int64  `json:"amount_cents"`
		Status      string `json:"status"`
		Stale       bool   `json:"stale"`
		Version     int    `json:"version"`
	} `json:"items"`
	Total int `json:"total"`
}

const statementCSV = "Date;Libellé;Débit;Crédit\n05/10/2026;VIR SEPA LEA EXEMPLE LOYER OCT;;800,00\n07/10/2026;PRLV SYNDIC;1 250,40;\n"
const statementOFX = "OFXHEADER:100\nDATA:OFXSGML\n\n<OFX><BANKTRANLIST>\n<STMTTRN>\n<DTPOSTED>20261008\n<TRNAMT>212.34\n<FITID>F1\n<NAME>CAF APL\n</STMTTRN>\n</BANKTRANLIST></OFX>\n"

// TestBankImport imports real bank statements (RENT-SPEC-013, Q-123): an
// account takes no full IBAN; a CSV statement with its mapping is read, a
// dry run writes nothing, the import adds its lines and a second import of
// the same statement adds none; an OFX statement adds its line keyed by
// FITID; a PDF is refused as a statement; a line is ignored then reopened,
// a stale version refused; lines past the alert delay are flagged; another
// owner reads nothing.
func TestBankImport(t *testing.T) {
	tok := token(t, subBank)
	var owner, acc idv
	call(t, "PUT", "/me", req{tok: tok, body: map[string]any{"legal_name": "Bailleur Banque", "address": "2 rue B, Lyon"}}).json(t, &owner)
	if r := call(t, "POST", "/bank-accounts", req{tok: tok, body: map[string]any{"label": "Compte courant", "iban_last4": "FR7630001007941234567890185"}}); r.Status != 422 {
		t.Errorf("full IBAN: %d %s", r.Status, r.Body)
	}
	audited(t, seen, "POST /bank-accounts", 201, func() resp {
		return call(t, "POST", "/bank-accounts", req{tok: tok, body: map[string]any{"label": "Compte courant", "iban_last4": "0185"}})
	}).json(t, &acc)

	var csvDoc, ofxDoc, pdfDoc idv
	upload(t, tok, "", map[string]string{"class": "supporting"}, "releve-octobre.csv", []byte(statementCSV)).json(t, &csvDoc)
	upload(t, tok, "", map[string]string{"class": "supporting"}, "releve-octobre.ofx", []byte(statementOFX)).json(t, &ofxDoc)
	upload(t, tok, "", map[string]string{"class": "supporting"}, "releve.pdf", signedPDF).json(t, &pdfDoc)
	if csvDoc.ID == "" || ofxDoc.ID == "" {
		t.Fatalf("uploads %+v %+v", csvDoc, ofxDoc)
	}
	mapping := map[string]any{"date": "Date", "label": "Libellé", "debit": "Débit", "credit": "Crédit"}
	body := func(doc, format string, dry bool) map[string]any {
		b := map[string]any{"account_id": acc.ID, "document_id": doc, "format": format, "dry_run": dry}
		if format == "csv" {
			b["mapping"] = mapping
		}
		return b
	}
	var got bankImport
	call(t, "POST", "/payments/import", req{tok: tok, body: body(csvDoc.ID, "csv", true)}).json(t, &got)
	if got.New != 2 || len(got.Lines) != 2 || got.Lines[1].AmountCents != -125040 {
		t.Fatalf("dry run %+v", got)
	}
	var lines bankLines
	call(t, "GET", "/bank-lines", req{tok: tok}).json(t, &lines)
	if lines.Total != 0 {
		t.Errorf("a dry run wrote %d lines", lines.Total)
	}
	audited(t, seen, "POST /payments/import", 200, func() resp {
		return call(t, "POST", "/payments/import", req{tok: tok, body: body(csvDoc.ID, "csv", false)})
	}).json(t, &got)
	if got.New != 2 || got.Duplicates != 0 {
		t.Errorf("import %+v", got)
	}
	call(t, "POST", "/payments/import", req{tok: tok, body: body(csvDoc.ID, "csv", false)}).json(t, &got)
	if got.New != 0 || got.Duplicates != 2 {
		t.Errorf("reimport %+v", got)
	}
	call(t, "POST", "/payments/import", req{tok: tok, body: body(ofxDoc.ID, "ofx", false)}).json(t, &got)
	if got.New != 1 || got.Lines[0].Key != "fitid:F1" {
		t.Errorf("OFX %+v", got)
	}
	if r := call(t, "POST", "/payments/import", req{tok: tok, body: body(pdfDoc.ID, "ofx", false)}); r.Status != 422 {
		t.Errorf("a PDF as a statement: %d %s", r.Status, r.Body)
	}
	if r := call(t, "POST", "/payments/import", req{tok: tok, body: map[string]any{"account_id": acc.ID, "document_id": csvDoc.ID, "format": "csv",
		"mapping": map[string]any{"date": "Date", "label": "Libellé", "amount": "Montant"}}}); r.Status != 422 {
		t.Errorf("a mapping naming no column: %d %s", r.Status, r.Body)
	}

	call(t, "GET", "/bank-lines?status=unmatched", req{tok: tok}).json(t, &lines)
	if lines.Total != 3 {
		t.Fatalf("lines %+v", lines)
	}
	// Booked in October 2026, they are past the ten-day alert today.
	stale := 0
	for _, l := range lines.Items {
		if l.Stale {
			stale++
		}
	}
	if today().String() > "2026-10-18" && stale != 3 {
		t.Errorf("stale %d of %+v", stale, lines.Items)
	}
	fee := lines.Items[0]
	for _, l := range lines.Items {
		if l.AmountCents < 0 {
			fee = l
		}
	}
	var line struct {
		Status  string `json:"status"`
		Version int    `json:"version"`
		Stale   bool   `json:"stale"`
	}
	audited(t, seen, "POST /bank-lines/{id}/status", 200, func() resp {
		return call(t, "POST", "/bank-lines/"+fee.ID+"/status", req{tok: tok, body: map[string]any{"status": "ignored", "note": "charges payées par ailleurs", "version": fee.Version}})
	}).json(t, &line)
	if line.Status != "ignored" || line.Stale {
		t.Errorf("ignored %+v", line)
	}
	if r := call(t, "POST", "/bank-lines/"+fee.ID+"/status", req{tok: tok, body: map[string]any{"status": "unmatched", "version": fee.Version}}); r.Status != 412 {
		t.Errorf("stale version: %d %s", r.Status, r.Body)
	}
	call(t, "GET", "/bank-lines?status=ignored", req{tok: tok}).json(t, &lines)
	if lines.Total != 1 {
		t.Errorf("ignored lines %+v", lines)
	}

	var other idv
	call(t, "PUT", "/me", req{tok: token(t, subBankX), body: map[string]any{"legal_name": "Bailleur X", "address": "1 rue X"}}).json(t, &other)
	call(t, "GET", "/bank-lines", req{tok: token(t, subBankX)}).json(t, &lines)
	if lines.Total != 0 {
		t.Errorf("another owner reads %d lines", lines.Total)
	}
	if r := call(t, "POST", "/payments/import", req{tok: token(t, subBankX), body: body(csvDoc.ID, "csv", false)}); r.Status != 404 {
		t.Errorf("another owner imports into the account: %d", r.Status)
	}
	rlsZeroRows(t, owner.ID, other.ID, subBank, subBankX, "", map[string]string{"bank_accounts": "owner_id = $1", "bank_lines": "owner_id = $1"})
}
