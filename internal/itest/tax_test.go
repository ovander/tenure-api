//go:build integration

package itest

import (
	"strings"
	"testing"
	"time"

	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/tax"
)

const (
	subT = "8001" // owner of the tax scenario
	subU = "8002" // another owner
)

func TestTax(t *testing.T) {
	tok, tokU := token(t, subT), token(t, subU)
	fresh := func() string { return token(t, subT, stepUp(time.Minute)) }
	ps := fixtureParams(t)
	lm, err := ps.Form2044LineMap(2025)
	if err != nil {
		t.Fatal(err)
	}
	forfait, _ := ps.Int64(params.KeyForfait222, params.TaxYearEnd(2025))
	var owner, ownerU, a1, ten, l1 idv
	call(t, "PUT", "/me", req{tok: tok, body: map[string]any{"legal_name": "Bailleur T", "address": "8 cours Vitton, Lyon"}}).json(t, &owner)
	call(t, "PUT", "/me", req{tok: tokU, body: map[string]any{"legal_name": "Bailleur U", "address": "1 rue U"}}).json(t, &ownerU)
	call(t, "POST", "/assets", req{tok: tok, body: map[string]any{"kind": "apartment", "address": "8 cours Vitton, Lyon"}}).json(t, &a1)
	call(t, "POST", "/tenants", req{tok: tok, body: map[string]any{"names": []string{"Nina Faure"}, "email": "nina@example.test"}}).json(t, &ten)
	call(t, "POST", "/leases", req{tok: tok, body: map[string]any{"kind": "unfurnished", "asset_ids": []string{a1.ID},
		"tenants": []map[string]any{{"tenant_id": ten.ID}}, "start_on": "2025-01-01", "term_months": 36, "rent_cents": 80000,
		"provision_cents": 10000, "due_day": 5, "irl_ref_quarter": "2024-Q3", "irl_ref_value": 14451, "revision_month": 9}}).json(t, &l1)
	readyToActivate(t, tok, l1.ID)
	call(t, "POST", "/leases/"+l1.ID+"/transitions", req{tok: tok, body: map[string]any{"action": "activate"}})
	for _, per := range []string{"2025-01", "2025-02", "2025-03"} {
		call(t, "POST", "/billing/runs", req{tok: tok, body: map[string]any{"period": per}})
	}
	// Matching runs oldest first: January in full, February's first half, then
	// March's payment completes February, pays March and leaves 150,00 € unapplied (flagged).
	for _, p := range []struct {
		on     string
		amount int64
	}{{"2025-01-04", 90000}, {"2025-02-04", 45000}, {"2025-03-04", 150000}} {
		if r := call(t, "POST", "/payments", req{tok: tok, body: map[string]any{"lease_id": l1.ID, "paid_on": p.on, "amount_cents": p.amount,
			"method": "transfer", "auto_match": true}}); r.Status != 201 {
			t.Fatalf("payment: %d %s", r.Status, r.Body)
		}
	}
	var invoice docView
	upload(t, tok, "", map[string]string{"class": "supporting", "asset_id": a1.ID}, "assurance-pno.pdf", signedPDF).json(t, &invoice)
	for _, c := range []map[string]any{
		{"charged_on": "2025-03-10", "paid_on": "2025-03-10", "category": "insurance", "amount_cents": 30000, "recoverable_cents": 0, "document_id": invoice.ID},
		{"charged_on": "2025-10-15", "paid_on": "2025-10-15", "category": "property_tax", "amount_cents": 120000, "recoverable_cents": 0},
		// Dated 2025, paid 2026: counts in 2026.
		{"charged_on": "2025-12-20", "paid_on": "2026-01-05", "category": "repairs", "amount_cents": 50000, "recoverable_cents": 0},
		// A draft is flagged, never deducted.
		{"charged_on": "2025-06-01", "paid_on": "2025-06-01", "category": "management", "amount_cents": 9000, "recoverable_cents": 0, "status": "draft"},
	} {
		c["asset_id"] = a1.ID
		if r := call(t, "POST", "/charges", req{tok: tok, body: c}); r.Status != 201 {
			t.Fatalf("charge: %d %s", r.Status, r.Body)
		}
	}

	t.Run("tax.annual drafts after its line-map check", func(t *testing.T) {
		var run struct{ Status string }
		call(t, "POST", "/jobs/tax.annual/trigger", req{tok: tok, body: map[string]any{"period": "2024"}}).json(t, &run)
		if run.Status != "failed" {
			t.Errorf("no 2044 map for 2024: run %s", run.Status)
		}
		if n := superCount(t, "SELECT count(*) FROM tax_summaries WHERE owner_id = $1", owner.ID); n != 0 {
			t.Errorf("a failed check drafted %d summaries", n)
		}
		call(t, "POST", "/jobs/tax.annual/trigger", req{tok: tok, body: map[string]any{"period": "2025"}}).json(t, &run)
		if run.Status != "succeeded" || superCount(t, "SELECT count(*) FROM tax_summaries WHERE owner_id = $1 AND state = 'draft'", owner.ID) != 1 {
			t.Errorf("tax.annual 2025: %s", run.Status)
		}
	})

	var draft tax.View
	t.Run("draft shows both regimes", func(t *testing.T) {
		audited(t, seen, "POST /tax/summaries", 401, func() resp {
			r := call(t, "POST", "/tax/summaries", req{tok: tok, body: map[string]any{"year": 2025}})
			if r.problemType(t) != "auth/step-up-required" {
				t.Errorf("without step-up: %s", r.Body)
			}
			return r
		})
		audited(t, seen, "POST /tax/summaries", 200, func() resp {
			return call(t, "POST", "/tax/summaries", req{tok: fresh(), body: map[string]any{"year": 2025}})
		}).json(t, &draft)
		if draft.State != "draft" || len(draft.Summary.Owners) != 1 {
			t.Fatalf("draft %+v", draft)
		}
		o := draft.Summary.Owners[0]
		line := func(name string) (int64, []string) {
			for _, l := range o.Totals {
				if l.Line == lm.Lines[name] {
					return int64(l.AmountCents), l.Sources
				}
			}
			return 0, nil
		}
		// Rent only, on a cash basis, each matched amount split in proportion
		// to the rent call's lines (Q-65): 800 + (400 + 400) + 800 €.
		if v, src := line("rent"); v != 240000 || len(src) != 3 {
			t.Errorf("rent line %d from %v", v, src)
		}
		if v, _ := line("insurance"); v != 30000 {
			t.Errorf("insurance %d", v)
		}
		if v, _ := line("property_tax"); v != 120000 {
			t.Errorf("property tax %d", v)
		}
		if v, _ := line("forfait_222"); v != forfait {
			t.Errorf("forfait %d, want %d", v, forfait)
		}
		if v, _ := line("repairs"); v != 0 {
			t.Errorf("a charge paid in 2026 counted in 2025: %d", v)
		}
		if v, _ := line("management"); v != 0 {
			t.Errorf("a draft charge was deducted: %d", v)
		}
		kinds := map[string]int{}
		for _, f := range draft.Summary.Flags {
			kinds[f.Kind]++
		}
		if kinds[tax.FlagMissingDocument] != 1 || kinds[tax.FlagUnmatchedPayment] != 1 || kinds[tax.FlagDraftCharge] != 1 {
			t.Errorf("flags %v", draft.Summary.Flags)
		}
		if !o.Micro.Eligible || o.Micro.GrossCents != 240000 || o.Reel.IncomeCents != 240000 {
			t.Errorf("regimes side by side: micro %+v réel %+v", o.Micro, o.Reel)
		}
	})

	t.Run("issue", func(t *testing.T) {
		var issued tax.View
		audited(t, seen, "POST /tax/summaries", 200, func() resp {
			return call(t, "POST", "/tax/summaries", req{tok: fresh(), body: map[string]any{"year": 2025, "regime": "reel"}})
		}).json(t, &issued)
		if issued.State != "issued" || issued.Regime == nil || *issued.Regime != "reel" || issued.DocumentID == nil || issued.CSVDocumentID == nil {
			t.Fatalf("issued %+v", issued)
		}
		var pdf, csv docView
		call(t, "GET", "/documents/"+issued.DocumentID.String(), req{tok: tok}).json(t, &pdf)
		call(t, "GET", "/documents/"+issued.CSVDocumentID.String(), req{tok: tok}).json(t, &csv)
		if pdf.Class != "tax_summary" || pdf.ContentType != "application/pdf" || csv.Class != "tax_summary" || csv.ContentType != "text/csv" ||
			pdf.RetainUntil == nil {
			t.Errorf("archived %+v %+v", pdf, csv)
		}
		if n := superCount(t, "SELECT count(*) FROM documents WHERE id = $1 AND object_key LIKE '%/tax_summary/%/RF-%.pdf'", pdf.ID); n != 1 {
			t.Error("the summary is not an RF-… document")
		}
		if r := call(t, "POST", "/tax/summaries", req{tok: fresh(), body: map[string]any{"year": 2025, "regime": "micro"}}); r.problemType(t) != tax.TypeAlreadyIssued {
			t.Errorf("issued twice: %d %s", r.Status, r.Body)
		}
		if _, err := env.super.Exec("UPDATE tax_summaries SET regime = 'micro' WHERE id = $1", issued.ID); err == nil {
			t.Error("an issued summary changed")
		}
		if r := call(t, "GET", "/tax/summaries/"+issued.ID.String()+"/lines.csv", req{tok: tok}); r.Status != 401 {
			t.Errorf("export without step-up: %d", r.Status)
		}
		r := call(t, "GET", "/tax/summaries/"+issued.ID.String()+"/lines.csv", req{tok: fresh()})
		if r.Status != 200 || !strings.HasPrefix(string(r.Body), "tax_year,owner_id") || !strings.Contains(string(r.Body), "flag:missing_document") {
			t.Errorf("csv %d %s", r.Status, r.Body)
		}
		if n := superCount(t, "SELECT count(*) FROM audit_events WHERE action = 'tax.export' AND owner_id = $1", owner.ID); n != 1 {
			t.Errorf("export not audited: %d", n)
		}
	})

	t.Run("isolation", func(t *testing.T) {
		var l struct{ Total int }
		call(t, "GET", "/tax/summaries", req{tok: tokU}).json(t, &l)
		if l.Total != 0 {
			t.Errorf("U lists T's summaries: %d", l.Total)
		}
		rlsZeroRows(t, owner.ID, ownerU.ID, subT, subU, a1.ID, map[string]string{"tax_summaries": "owner_id = $1"})
	})
}
