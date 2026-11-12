//go:build integration

package itest

import (
	"testing"
	"time"

	"github.com/ovander/tenure-api/internal/ledger"
)

const subLedger = "9977" // owner of the journal and period lock scenario (RENT-SPEC-014, part 15b)

// TestLedger reads the 2025 journal of an owner (a charge partly recoverable,
// a loan's instalments), balanced on every entry; issues the 2025 tax
// summary, which closes the year; then a payment, a charge or a loan table
// touching 2025 is refused with ledger/period-locked, while attaching an
// invoice to a closed charge and writing into 2026 still work (Q-127).
func TestLedger(t *testing.T) {
	tok := token(t, subLedger)
	fresh := func() string { return token(t, subLedger, stepUp(time.Minute)) }
	var owner, a idv
	call(t, "PUT", "/me", req{tok: tok, body: map[string]any{"legal_name": "Bailleur Journal", "address": "5 rue J, Lyon"}}).json(t, &owner)
	call(t, "POST", "/assets", req{tok: tok, body: map[string]any{"kind": "apartment", "address": "5 rue J, Lyon"}}).json(t, &a)
	var ch struct {
		ID      string `json:"id"`
		Version int    `json:"version"`
	}
	chargeBody := map[string]any{"asset_id": a.ID, "charged_on": "2025-04-01", "paid_on": "2025-04-01", "category": "copro_provision",
		"amount_cents": 60000, "recoverable_cents": 35000}
	if r := call(t, "POST", "/charges", req{tok: tok, body: chargeBody}); r.Status != 201 {
		t.Fatalf("charge: %d %s", r.Status, r.Body)
	} else {
		r.json(t, &ch)
	}
	var l loanView
	call(t, "POST", "/loans", req{tok: tok, body: map[string]any{"asset_id": a.ID, "label": "Prêt bancaire", "principal_cents": 1200000,
		"annual_rate_ppm": 24000, "term_months": 24, "first_due_on": "2025-11-05", "insurance_cents": 500}}).json(t, &l)

	var j ledger.View
	call(t, "GET", "/ledger?year=2025", req{tok: tok}).json(t, &j)
	if j.Closed || len(j.Entries) != 3 {
		t.Fatalf("journal 2025 %+v", j)
	}
	for _, e := range j.Entries {
		var d, c int64
		for _, x := range e.Lines {
			d, c = d+int64(x.DebitCents), c+int64(x.CreditCents)
		}
		if d != c || d == 0 {
			t.Errorf("unbalanced entry %+v", e)
		}
	}
	var total int64
	for _, b := range j.Balances {
		total += int64(b.BalanceCents)
		if b.Account == "419800" && b.DebitCents != 35000 {
			t.Errorf("recoverable part %+v", b)
		}
	}
	if total != 0 {
		t.Errorf("balances add up to %d", total)
	}
	if r := call(t, "GET", "/ledger?year=abc", req{tok: tok}); r.Status != 422 {
		t.Errorf("bad year: %d", r.Status)
	}

	call(t, "POST", "/tax/summaries", req{tok: fresh(), body: map[string]any{"year": 2025, "regime": "reel"}})
	call(t, "GET", "/ledger?year=2025", req{tok: tok}).json(t, &j)
	if !j.Closed {
		t.Fatal("2025 is not closed after its summary was issued")
	}
	locked := func(what string, r resp) {
		t.Helper()
		if r.problemType(t) != ledger.TypePeriodLocked || r.Status != 409 {
			t.Errorf("%s in a closed year: %d %s", what, r.Status, r.Body)
		}
	}
	locked("a charge paid", call(t, "POST", "/charges", req{tok: tok, body: chargeBody}))
	changed := map[string]any{}
	for k, v := range chargeBody {
		changed[k] = v
	}
	changed["amount_cents"] = 61000
	locked("a charge's amount", call(t, "PUT", "/charges/"+ch.ID, req{tok: tok, ifMatch: etag(ch.Version), body: changed}))
	moved := map[string]any{}
	for k, v := range chargeBody {
		moved[k] = v
	}
	moved["paid_on"] = "2026-01-02"
	locked("a charge moved out", call(t, "PUT", "/charges/"+ch.ID, req{tok: tok, ifMatch: etag(ch.Version), body: moved}))
	locked("a loan's 2025 instalments", call(t, "PUT", "/loans/"+l.ID, req{tok: tok, ifMatch: etag(l.Version), body: map[string]any{
		"asset_id": a.ID, "label": "Prêt bancaire", "principal_cents": 1200000, "annual_rate_ppm": 20000, "term_months": 24,
		"first_due_on": "2025-11-05", "insurance_cents": 500}}))

	var invoice docView
	upload(t, tok, "", map[string]string{"class": "supporting", "asset_id": a.ID}, "appel-syndic.pdf", signedPDF).json(t, &invoice)
	withDoc := map[string]any{"document_id": invoice.ID}
	for k, v := range chargeBody {
		withDoc[k] = v
	}
	if r := call(t, "PUT", "/charges/"+ch.ID, req{tok: tok, ifMatch: etag(ch.Version), body: withDoc}); r.Status != 200 {
		t.Errorf("an invoice attached to a closed charge: %d %s", r.Status, r.Body)
	}
	if r := call(t, "POST", "/charges", req{tok: tok, body: map[string]any{"asset_id": a.ID, "charged_on": "2026-02-01", "paid_on": "2026-02-01",
		"category": "repairs", "amount_cents": 10000, "recoverable_cents": 0}}); r.Status != 201 {
		t.Errorf("a 2026 charge: %d %s", r.Status, r.Body)
	}
	if n := superCount(t, "SELECT count(*) FROM charges WHERE owner_id = $1 AND paid_on BETWEEN '2025-01-01' AND '2025-12-31'", owner.ID); n != 1 {
		t.Errorf("2025 charges: %d", n)
	}
}
