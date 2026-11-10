//go:build integration

package itest

import (
	"testing"
)

const subBankCharges = "9974" // owner of the bank debits scenario (RENT-SPEC-013, part 14c)

// TestBankCharges matches real bank debits to real charges (RENT-SPEC-013,
// Q-125): a syndic call pays the two charges of one statement, suggested as
// a whole; a total short of the debit is refused; the match pays both on the
// line's day and the line becomes matched; a charge already paid by a line
// is refused for another; a credit has no charge suggestion.
func TestBankCharges(t *testing.T) {
	tok := token(t, subBankCharges)
	var owner, acc, a idv
	call(t, "PUT", "/me", req{tok: tok, body: map[string]any{"legal_name": "Bailleur Charges", "address": "7 rue C, Lyon"}}).json(t, &owner)
	call(t, "POST", "/assets", req{tok: tok, body: map[string]any{"kind": "apartment", "address": "7 rue C, Lyon"}}).json(t, &a)
	call(t, "POST", "/bank-accounts", req{tok: tok, body: map[string]any{"label": "Compte charges"}}).json(t, &acc)
	on := today().AddDays(-20).String()
	var c1, c2, c3 idv
	for _, x := range []struct {
		into   *idv
		label  string
		amount int64
		day    string
	}{{&c1, "Charges courantes", 30000, on}, {&c2, "Fonds travaux", 5000, on}, {&c3, "Assurance PNO", 12000, today().AddDays(-5).String()}} {
		r := call(t, "POST", "/charges", req{tok: tok, body: map[string]any{"asset_id": a.ID, "charged_on": x.day,
			"category": "copro_statement", "label": x.label, "amount_cents": x.amount, "recoverable_cents": 0}})
		if r.Status != 201 {
			t.Fatalf("charge %s: %d %s", x.label, r.Status, r.Body)
		}
		r.json(t, x.into)
	}
	fr := func(iso string) string { return iso[8:10] + "/" + iso[5:7] + "/" + iso[:4] }
	csv := "Date;Libellé;Montant\n" + fr(today().AddDays(-10).String()) + ";PRLV SYNDIC RESIDENCE;-350,00\n" +
		fr(today().AddDays(-3).String()) + ";PRLV ASSUREUR PNO;-120,00\n" + fr(today().AddDays(-3).String()) + ";VIR REMBOURSEMENT;42,00\n"
	var doc idv
	upload(t, tok, "", map[string]string{"class": "supporting"}, "releve-charges.csv", []byte(csv)).json(t, &doc)
	call(t, "POST", "/payments/import", req{tok: tok, body: map[string]any{"account_id": acc.ID, "document_id": doc.ID, "format": "csv",
		"mapping": map[string]any{"date": "Date", "label": "Libellé", "amount": "Montant"}}})
	var lines bankLines
	call(t, "GET", "/bank-lines", req{tok: tok}).json(t, &lines)
	ids := map[int64]struct {
		id      string
		version int
	}{}
	for _, l := range lines.Items {
		ids[l.AmountCents] = struct {
			id      string
			version int
		}{l.ID, l.Version}
	}
	syndic, insurer, credit := ids[-35000], ids[-12000], ids[4200]
	if syndic.id == "" || insurer.id == "" || credit.id == "" {
		t.Fatalf("lines %+v", lines)
	}
	var sug []struct {
		Rule    string `json:"rule"`
		Charges []struct {
			ID string `json:"id"`
		} `json:"charges"`
	}
	call(t, "GET", "/bank-lines/"+syndic.id+"/charge-suggestions", req{tok: tok}).json(t, &sug)
	if len(sug) != 1 || sug[0].Rule != "statement" || len(sug[0].Charges) != 2 {
		t.Fatalf("syndic suggestions %+v", sug)
	}
	call(t, "GET", "/bank-lines/"+credit.id+"/charge-suggestions", req{tok: tok}).json(t, &sug)
	if len(sug) != 0 {
		t.Errorf("a credit has charge suggestions %+v", sug)
	}
	if r := call(t, "POST", "/bank-lines/"+syndic.id+"/charges", req{tok: tok, body: map[string]any{"charge_ids": []string{c1.ID}, "version": syndic.version}}); r.Status != 422 {
		t.Errorf("short of the debit: %d %s", r.Status, r.Body)
	}
	var res struct {
		Line struct {
			Status string `json:"status"`
		} `json:"line"`
		Charges []struct {
			PaidOn string `json:"paid_on"`
		} `json:"charges"`
	}
	audited(t, seen, "POST /bank-lines/{id}/charges", 200, func() resp {
		return call(t, "POST", "/bank-lines/"+syndic.id+"/charges", req{tok: tok, body: map[string]any{"charge_ids": []string{c1.ID, c2.ID}, "version": syndic.version}})
	}).json(t, &res)
	if res.Line.Status != "matched" || len(res.Charges) != 2 || res.Charges[0].PaidOn != today().AddDays(-10).String() {
		t.Errorf("match %+v", res)
	}
	if n := superCount(t, "SELECT count(*) FROM charges WHERE bank_line_id = '"+syndic.id+"'"); n != 2 {
		t.Errorf("charges naming the line: %d", n)
	}
	if r := call(t, "POST", "/bank-lines/"+insurer.id+"/charges", req{tok: tok, body: map[string]any{"charge_ids": []string{c2.ID}, "version": insurer.version}}); r.Status != 422 {
		t.Errorf("a charge paid twice: %d %s", r.Status, r.Body)
	}
	if r := call(t, "POST", "/bank-lines/"+credit.id+"/charges", req{tok: tok, body: map[string]any{"charge_ids": []string{c3.ID}, "version": credit.version}}); r.Status != 422 {
		t.Errorf("a credit to a charge: %d %s", r.Status, r.Body)
	}
}
