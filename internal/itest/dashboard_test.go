//go:build integration

package itest

import (
	"strings"
	"testing"
	"time"

	"github.com/ovander/tenure-api/internal/dashboard"
)

const subDashboard = "9978" // owner of the dashboard scenario (RENT-SPEC-016, part 16a)

// TestDashboard reads the 2025 dashboard of an owner with one apartment
// (RENT-SPEC-016, Q-129): the rent received, the charges and the loan's
// interest as expenses, the yields on the acquisition price, the two unpaid
// calls in the oldest bucket, the tax estimate; 2026 has no 2044 map, so no
// estimate; the CSV needs a fresh strong sign-in and is audited.
func TestDashboard(t *testing.T) {
	tok := token(t, subDashboard)
	fresh := func() string { return token(t, subDashboard, stepUp(time.Minute)) }
	var owner, a, ten, l idv
	call(t, "PUT", "/me", req{tok: tok, body: map[string]any{"legal_name": "Bailleur Tableau", "address": "9 rue D, Lyon"}}).json(t, &owner)
	call(t, "POST", "/assets", req{tok: tok, body: map[string]any{"kind": "apartment", "address": "9 rue D, Lyon",
		"acquired_on": "2024-06-01", "acquisition_cents": 20000000}}).json(t, &a)
	call(t, "POST", "/tenants", req{tok: tok, body: map[string]any{"names": []string{"Iris Dumas"}, "email": "iris@example.test"}}).json(t, &ten)
	call(t, "POST", "/leases", req{tok: tok, body: map[string]any{"kind": "unfurnished", "asset_ids": []string{a.ID},
		"tenants": []map[string]any{{"tenant_id": ten.ID}}, "start_on": "2025-01-01", "term_months": 36, "rent_cents": 80000,
		"provision_cents": 10000, "due_day": 5, "irl_ref_quarter": "2024-Q3", "irl_ref_value": 14451, "revision_month": 9}}).json(t, &l)
	readyToActivate(t, tok, l.ID)
	call(t, "POST", "/leases/"+l.ID+"/transitions", req{tok: tok, body: map[string]any{"action": "activate"}})
	for _, per := range []string{"2025-01", "2025-02", "2025-03"} {
		call(t, "POST", "/billing/runs", req{tok: tok, body: map[string]any{"period": per}})
	}
	if r := call(t, "POST", "/payments", req{tok: tok, body: map[string]any{"lease_id": l.ID, "paid_on": "2025-01-04", "amount_cents": 90000,
		"method": "transfer", "auto_match": true}}); r.Status != 201 {
		t.Fatalf("payment: %d %s", r.Status, r.Body)
	}
	if r := call(t, "POST", "/charges", req{tok: tok, body: map[string]any{"asset_id": a.ID, "charged_on": "2025-10-15", "paid_on": "2025-10-15",
		"category": "property_tax", "amount_cents": 100000, "recoverable_cents": 0}}); r.Status != 201 {
		t.Fatalf("charge: %d %s", r.Status, r.Body)
	}
	call(t, "POST", "/loans", req{tok: tok, body: map[string]any{"asset_id": a.ID, "label": "Prêt bancaire", "principal_cents": 1200000,
		"instalments": []map[string]any{{"due_on": "2025-06-10", "principal_cents": 1200000, "interest_cents": 20000, "insurance_cents": 0}}}})

	var v dashboard.View
	call(t, "GET", "/dashboard?year=2025", req{tok: tok}).json(t, &v)
	if v.Partial || len(v.Assets) != 1 {
		t.Fatalf("dashboard 2025 %+v", v)
	}
	x := v.Assets[0]
	if x.RentCents != 80000 || x.ExpensesCents != 120000 || x.InterestCents != 20000 || x.NetCents != -40000 || x.ProvisionsReceivedCents != 10000 {
		t.Errorf("figures %+v", x.Figures)
	}
	if x.GrossYieldBP == nil || *x.GrossYieldBP != 40 || x.Days != 365 || x.VacantDays != 0 {
		t.Errorf("yield and days %+v", x)
	}
	last := x.Arrears[len(x.Arrears)-1]
	if x.ArrearsCents != 180000 || last.Cents != 180000 || last.ToDays != nil {
		t.Errorf("arrears %+v", x.Arrears)
	}
	if !v.Tax.Available || v.Tax.Micro == nil || v.Tax.Reel == nil || v.Tax.Micro.GrossCents != 80000 {
		t.Errorf("tax estimate %+v", v.Tax)
	}
	if len(v.Months) != 12 || v.Months[0].InCents != 90000 {
		t.Errorf("months %+v", v.Months)
	}
	call(t, "GET", "/dashboard?year=2026", req{tok: tok}).json(t, &v)
	if v.Tax.Available || v.Tax.Reason != "tax/line-map-missing" || !v.Partial {
		t.Errorf("2026 %+v", v.Tax)
	}
	if r := call(t, "GET", "/dashboard?year=abc", req{tok: tok}); r.Status != 422 {
		t.Errorf("bad year: %d", r.Status)
	}
	if r := call(t, "GET", "/dashboard/assets.csv?year=2025", req{tok: tok}); r.Status != 401 {
		t.Errorf("csv without step-up: %d", r.Status)
	}
	r := call(t, "GET", "/dashboard/assets.csv?year=2025", req{tok: fresh()})
	if r.Status != 200 || !strings.HasPrefix(string(r.Body), "year,as_of,asset_id") || !strings.Contains(string(r.Body), a.ID) {
		t.Errorf("csv %d %s", r.Status, r.Body)
	}
	if n := superCount(t, "SELECT count(*) FROM audit_events WHERE action = 'dashboard.export' AND owner_id = $1", owner.ID); n != 1 {
		t.Errorf("export audited: %d", n)
	}
}
