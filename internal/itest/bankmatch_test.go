//go:build integration

package itest

import (
	"fmt"
	"testing"
)

const subBankMatch = "9973" // owner of the bank matching scenario (RENT-SPEC-013, part 14b)

// TestBankMatching matches real bank credits to real rent calls (RENT-SPEC-013,
// Q-124): two leases with a call each; a statement holds the first tenant's
// transfer naming them, an anonymous transfer of the second call's amount, a
// part paid by the housing allowance naming the second tenant, and a debit.
// The automatic matching takes the named transfer and the allowance, not the
// anonymous one, whose suggestion is the amount near the due day; an
// allocation short of the line is refused; the owner splits it across the
// second lease; the calls are paid.
func TestBankMatching(t *testing.T) {
	tok := token(t, subBankMatch)
	var owner, acc idv
	call(t, "PUT", "/me", req{tok: tok, body: map[string]any{"legal_name": "Bailleur Rapprochement", "address": "6 rue R, Lyon"}}).json(t, &owner)
	call(t, "POST", "/bank-accounts", req{tok: tok, body: map[string]any{"label": "Compte loyers"}}).json(t, &acc)
	per := today().AddMonthsClamped(-1)
	leases := make([]idv, 2)
	for i, name := range []string{"Zoé Fictivia", "Marc Imaginaire"} {
		var ten, a idv
		call(t, "POST", "/tenants", req{tok: tok, body: map[string]any{"names": []string{name}, "email": fmt.Sprintf("t%d@example.test", i)}}).json(t, &ten)
		call(t, "POST", "/assets", req{tok: tok, body: map[string]any{"kind": "apartment", "address": fmt.Sprintf("%d rue M, Lyon", i+1)}}).json(t, &a)
		r := call(t, "POST", "/leases", req{tok: tok, body: map[string]any{"kind": "unfurnished", "asset_ids": []string{a.ID},
			"tenants": []map[string]any{{"tenant_id": ten.ID}}, "start_on": "2025-01-01", "term_months": 36, "rent_cents": 70000 + int64(i)*10000,
			"provision_cents": 5000, "due_day": 5, "irl_ref_quarter": "2024-Q3", "irl_ref_value": 14451, "revision_month": 9}})
		if r.Status != 201 {
			t.Fatalf("lease %d: %d %s", i, r.Status, r.Body)
		}
		r.json(t, &leases[i])
		superExec(t, `UPDATE leases SET state = 'active' WHERE id = $1`, leases[i].ID)
	}
	if r := call(t, "POST", "/billing/runs", req{tok: tok, body: map[string]any{"period": per.String()[:7]}}); r.Status != 201 {
		t.Fatalf("billing run: %d %s", r.Status, r.Body)
	}
	due := per.String()[:7] + "-05"
	day := func(n int) string { return fmt.Sprintf("%s/%s/%s", due[8:10], due[5:7], due[:4]) + fmt.Sprint(n)[:0] }
	csv := "Date;Libellé;Montant\n" +
		day(0) + ";VIR SEPA MME ZOE FICTIVIA LOYER;750,00\n" +
		day(0) + ";VIR SEPA LOYER;637,66\n" +
		day(0) + ";CAF APL MARC IMAGINAIRE;212,34\n" +
		day(0) + ";PRLV ASSURANCE PNO;-15,00\n"
	var doc idv
	upload(t, tok, "", map[string]string{"class": "supporting"}, "releve.csv", []byte(csv)).json(t, &doc)
	call(t, "POST", "/payments/import", req{tok: tok, body: map[string]any{"account_id": acc.ID, "document_id": doc.ID, "format": "csv",
		"mapping": map[string]any{"date": "Date", "label": "Libellé", "amount": "Montant"}}})

	var auto struct {
		Matched []struct {
			Suggestion struct {
				LeaseID string `json:"lease_id"`
				Rule    string `json:"rule"`
				Method  string `json:"method"`
			} `json:"suggestion"`
		} `json:"matched"`
		Left int `json:"left"`
	}
	call(t, "POST", "/bank-lines/auto", req{tok: tok, body: map[string]any{"dry_run": true}}).json(t, &auto)
	if len(auto.Matched) != 2 || auto.Left != 1 {
		t.Fatalf("dry run %+v", auto)
	}
	var lines bankLines
	call(t, "GET", "/bank-lines?status=matched", req{tok: tok}).json(t, &lines)
	if lines.Total != 0 {
		t.Errorf("a dry run matched %d lines", lines.Total)
	}
	audited(t, seen, "POST /bank-lines/auto", 200, func() resp {
		return call(t, "POST", "/bank-lines/auto", req{tok: tok, body: map[string]any{}})
	}).json(t, &auto)
	rules := map[string]string{}
	for _, m := range auto.Matched {
		rules[m.Suggestion.Rule] = m.Suggestion.LeaseID
	}
	if rules["name_amount"] != leases[0].ID || rules["allowance"] != leases[1].ID {
		t.Errorf("auto %+v", auto)
	}

	call(t, "GET", "/bank-lines?status=unmatched", req{tok: tok}).json(t, &lines)
	var anon *struct {
		ID          string `json:"id"`
		AmountCents int64  `json:"amount_cents"`
		Status      string `json:"status"`
		Stale       bool   `json:"stale"`
		Version     int    `json:"version"`
	}
	for i := range lines.Items {
		if lines.Items[i].AmountCents == 63766 {
			anon = &lines.Items[i]
		}
	}
	if anon == nil || lines.Total != 2 {
		t.Fatalf("unmatched %+v", lines)
	}
	var sug []struct {
		LeaseID string `json:"lease_id"`
		Rule    string `json:"rule"`
	}
	call(t, "GET", "/bank-lines/"+anon.ID+"/suggestions", req{tok: tok}).json(t, &sug)
	if len(sug) != 1 || sug[0].Rule != "amount_period" || sug[0].LeaseID != leases[1].ID {
		t.Errorf("suggestions %+v", sug)
	}
	if r := call(t, "POST", "/bank-lines/"+anon.ID+"/match", req{tok: tok, body: map[string]any{"version": anon.Version,
		"allocations": []map[string]any{{"lease_id": leases[1].ID, "amount_cents": 60000, "method": "transfer"}}}}); r.Status != 422 {
		t.Errorf("short allocation: %d %s", r.Status, r.Body)
	}
	var matched struct {
		Line struct {
			Status string `json:"status"`
		} `json:"line"`
		Payments []struct {
			MatchedCents int64 `json:"matched_cents"`
		} `json:"payments"`
	}
	audited(t, seen, "POST /bank-lines/{id}/match", 200, func() resp {
		return call(t, "POST", "/bank-lines/"+anon.ID+"/match", req{tok: tok, body: map[string]any{"version": anon.Version,
			"allocations": []map[string]any{{"lease_id": leases[1].ID, "amount_cents": 63766, "method": "transfer"}}}})
	}).json(t, &matched)
	if matched.Line.Status != "matched" || len(matched.Payments) != 1 || matched.Payments[0].MatchedCents != 63766 {
		t.Errorf("match %+v", matched)
	}
	for i, l := range leases {
		var arr struct {
			OutstandingCents int64 `json:"outstanding_cents"`
		}
		call(t, "GET", "/leases/"+l.ID+"/arrears", req{tok: tok}).json(t, &arr)
		if arr.OutstandingCents != 0 {
			t.Errorf("lease %d still owes %d", i, arr.OutstandingCents)
		}
	}
	if r := call(t, "POST", "/bank-lines/"+anon.ID+"/match", req{tok: tok, body: map[string]any{"version": anon.Version + 1,
		"allocations": []map[string]any{{"lease_id": leases[1].ID, "amount_cents": 63766, "method": "transfer"}}}}); r.Status != 409 {
		t.Errorf("a matched line again: %d %s", r.Status, r.Body)
	}
}
