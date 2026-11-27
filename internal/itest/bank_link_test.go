//go:build integration

package itest

import (
	"fmt"
	"testing"
)

const subBankLink = "9974" // owner of the bank link scenario (#256)

// TestBankLinkPayment: a rent paid and recorded by hand, then imported with
// the bank statement, is linked to the credit instead of being recorded
// twice (#256). The credit has no rent call to pay, the payment it is comes
// first among the payment suggestions, a link short of the credit is
// refused, the link names the line and records no payment, and a payment
// linked once is not linked again.
func TestBankLinkPayment(t *testing.T) {
	tok := token(t, subBankLink)
	var owner, acc, ten, a, l idv
	call(t, "PUT", "/me", req{tok: tok, body: map[string]any{"legal_name": "Bailleur Lien", "address": "7 rue L, Lyon"}}).json(t, &owner)
	call(t, "POST", "/bank-accounts", req{tok: tok, body: map[string]any{"label": "Compte loyers"}}).json(t, &acc)
	call(t, "POST", "/tenants", req{tok: tok, body: map[string]any{"names": []string{"Ana Fictive"}, "email": "ana.fictive@example.test"}}).json(t, &ten)
	call(t, "POST", "/assets", req{tok: tok, body: map[string]any{"kind": "apartment", "address": "9 rue L, Lyon"}}).json(t, &a)
	r := call(t, "POST", "/leases", req{tok: tok, body: map[string]any{"kind": "unfurnished", "asset_ids": []string{a.ID},
		"tenants": []map[string]any{{"tenant_id": ten.ID}}, "start_on": "2025-01-01", "term_months": 36, "rent_cents": 80000,
		"provision_cents": 5000, "due_day": 5, "irl_ref_quarter": "2025-Q4", "irl_ref_value": 14610, "revision_month": 1}})
	if r.Status != 201 {
		t.Fatalf("lease: %d %s", r.Status, r.Body)
	}
	r.json(t, &l)
	superExec(t, `UPDATE leases SET state = 'active' WHERE id = $1`, l.ID)
	per := today().AddMonthsClamped(-1)
	if r := call(t, "POST", "/billing/runs", req{tok: tok, body: map[string]any{"period": per.String()[:7]}}); r.Status != 201 {
		t.Fatalf("billing run: %d %s", r.Status, r.Body)
	}
	due := per.String()[:7] + "-05"
	var paid idv
	call(t, "POST", "/payments", req{tok: tok, body: map[string]any{"lease_id": l.ID, "paid_on": due, "amount_cents": 85000,
		"method": "transfer", "auto_match": true}}).json(t, &paid)

	day := fmt.Sprintf("%s/%s/%s", due[8:10], due[5:7], due[:4])
	csv := "Date;Libellé;Montant\n" + day + ";VIR SEPA ANA FICTIVE;850,00\n" + day + ";VIR SEPA LOYER BIS;850,00\n"
	var doc idv
	upload(t, tok, "", map[string]string{"class": "supporting"}, "releve.csv", []byte(csv)).json(t, &doc)
	call(t, "POST", "/payments/import", req{tok: tok, body: map[string]any{"account_id": acc.ID, "document_id": doc.ID, "format": "csv",
		"mapping": map[string]any{"date": "Date", "label": "Libellé", "amount": "Montant"}}})
	var lines bankLines
	call(t, "GET", "/bank-lines?status=unmatched", req{tok: tok}).json(t, &lines)
	if lines.Total != 2 {
		t.Fatalf("lines %+v", lines)
	}
	line, other := lines.Items[0], lines.Items[1]

	var rent []map[string]any
	call(t, "GET", "/bank-lines/"+line.ID+"/suggestions", req{tok: tok}).json(t, &rent)
	if len(rent) != 0 {
		t.Errorf("a paid rent call is suggested: %+v", rent)
	}
	var sug []struct {
		ID string `json:"id"`
	}
	call(t, "GET", "/bank-lines/"+line.ID+"/payment-suggestions", req{tok: tok}).json(t, &sug)
	if len(sug) != 1 || sug[0].ID != paid.ID {
		t.Fatalf("payment suggestions %+v", sug)
	}
	if r := call(t, "POST", "/bank-lines/"+line.ID+"/payments", req{tok: tok, body: map[string]any{"version": line.Version, "payment_ids": []string{}}}); r.problemType(t) != "payment/bank-payments-invalid" {
		t.Errorf("empty link: %d %s", r.Status, r.Body)
	}
	payments := superCount(t, "SELECT count(*) FROM payments WHERE owner_id = $1", owner.ID)
	var res struct {
		Line struct {
			Status string `json:"status"`
		} `json:"line"`
		Payments []struct {
			ID           string `json:"id"`
			MatchedCents int64  `json:"matched_cents"`
		} `json:"payments"`
	}
	audited(t, seen, "POST /bank-lines/{id}/payments", 200, func() resp {
		return call(t, "POST", "/bank-lines/"+line.ID+"/payments", req{tok: tok, body: map[string]any{"version": line.Version, "payment_ids": []string{paid.ID}}})
	}).json(t, &res)
	if res.Line.Status != "matched" || len(res.Payments) != 1 || res.Payments[0].ID != paid.ID || res.Payments[0].MatchedCents != 85000 {
		t.Errorf("link %+v", res)
	}
	if n := superCount(t, "SELECT count(*) FROM payments WHERE owner_id = $1", owner.ID); n != payments {
		t.Errorf("payments after the link: %d, before %d", n, payments)
	}
	if n := superCount(t, "SELECT count(*) FROM payments WHERE id = $1 AND bank_line_id = $2", paid.ID, line.ID); n != 1 {
		t.Error("the payment does not name its bank line")
	}
	call(t, "GET", "/bank-lines/"+other.ID+"/payment-suggestions", req{tok: tok}).json(t, &sug)
	if len(sug) != 0 {
		t.Errorf("a linked payment is suggested again: %+v", sug)
	}
	if r := call(t, "POST", "/bank-lines/"+other.ID+"/payments", req{tok: tok, body: map[string]any{"version": other.Version, "payment_ids": []string{paid.ID}}}); r.problemType(t) != "payment/bank-payments-invalid" {
		t.Errorf("second link of a payment: %d %s", r.Status, r.Body)
	}
}
