//go:build integration

package itest

import (
	"testing"

	"github.com/ovander/tenure-api/internal/billing"
)

// TestBillingRerun: a lease activated after its period was billed gets its
// rent call when the owner issues the period again, and the run reports the
// rent calls it issued (#254).
func TestBillingRerun(t *testing.T) {
	tok := token(t, "itest-billing-rerun")
	call(t, "PUT", "/me", req{tok: tok, body: map[string]any{"legal_name": "Bailleur Relance", "address": "8 rue Fictive, Lyon"}})
	var tenant idv
	call(t, "POST", "/tenants", req{tok: tok, body: map[string]any{"names": []string{"Lou Relance"}, "email": "lou.relance@example.test"}}).json(t, &tenant)
	// The latest quarter as the reference: irl.fetch (TestBilling) revises
	// no lease of this owner.
	lease := func(address string) string {
		var a, l idv
		call(t, "POST", "/assets", req{tok: tok, body: map[string]any{"kind": "apartment", "address": address}}).json(t, &a)
		call(t, "POST", "/leases", req{tok: tok, body: map[string]any{"kind": "unfurnished", "asset_ids": []string{a.ID},
			"tenants": []map[string]any{{"tenant_id": tenant.ID}}, "start_on": "2025-05-01", "term_months": 36, "rent_cents": 60000,
			"due_day": 5, "irl_ref_quarter": "2025-Q4", "irl_ref_value": 14610, "revision_month": 5}}).json(t, &l)
		readyToActivate(t, tok, l.ID)
		if r := call(t, "POST", "/leases/"+l.ID+"/transitions", req{tok: tok, body: map[string]any{"action": "activate"}}); r.Status != 200 {
			t.Fatalf("activate: %d %s", r.Status, r.Body)
		}
		return l.ID
	}
	run := func() (int, billing.RunView) {
		var v billing.RunView
		r := call(t, "POST", "/billing/runs", req{tok: tok, body: map[string]any{"period": "2025-05"}})
		r.json(t, &v)
		return r.Status, v
	}
	first := lease("1 rue de la Relance, Lyon")
	if status, v := run(); status != 201 || len(v.Fees) != 1 || v.Fees[0].LeaseID != first {
		t.Fatalf("first run: %d %+v", status, v.Fees)
	}
	second := lease("2 rue de la Relance, Lyon")
	status, v := run()
	if status != 201 || len(v.Fees) != 1 || v.Fees[0].LeaseID != second {
		t.Fatalf("second run: %d %+v", status, v.Fees)
	}
	for _, id := range []string{first, second} {
		if n := superCount(t, "SELECT count(*) FROM billing_documents WHERE lease_id = $1 AND period = '2025-05' AND kind = 'rent_call'", id); n != 1 {
			t.Errorf("lease %s: %d rent calls", id, n)
		}
	}
	if n := superCount(t, "SELECT count(*) FROM job_runs WHERE job = 'billing.monthly' AND period = '2025-05' AND NOT dry_run AND status = 'succeeded' AND owner_id = (SELECT id FROM owners WHERE legal_name = 'Bailleur Relance')"); n != 1 {
		t.Errorf("job_runs rows for the period: %d", n)
	}
}
