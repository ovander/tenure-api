//go:build integration

package itest

import (
	"testing"
)

const (
	subPlan  = "9969" // owner of the payment plan scenario (RENT-SPEC-011, part 13b)
	subPlanX = "9970"
)

type planResult struct {
	Suspended bool `json:"suspended"`
	Next      []struct {
		Kind string `json:"kind"`
	} `json:"next"`
	Plan *struct {
		ID        string `json:"id"`
		Status    string `json:"status"`
		DebtCents int64  `json:"debt_cents"`
		Version   int    `json:"version"`
	} `json:"plan"`
}

// TestPaymentPlan agrees a payment plan on a real overdue rent call
// (RENT-SPEC-011, Q-122): instalments short of the debt are refused; the
// plan covering it is respected and suspends the recovery; a second plan is
// refused while it holds; cancelled, the escalation resumes; another owner
// reads nothing.
func TestPaymentPlan(t *testing.T) {
	tok := token(t, subPlan)
	var owner, ten, a, l idv
	call(t, "PUT", "/me", req{tok: tok, body: map[string]any{"legal_name": "Bailleur Plan", "address": "4 rue P, Lyon"}}).json(t, &owner)
	call(t, "POST", "/tenants", req{tok: tok, body: map[string]any{"names": []string{"Paul Exemple"}, "email": "paul@example.test"}}).json(t, &ten)
	call(t, "POST", "/assets", req{tok: tok, body: map[string]any{"kind": "apartment", "address": "4 rue P, Lyon"}}).json(t, &a)
	r := call(t, "POST", "/leases", req{tok: tok, body: map[string]any{"kind": "unfurnished", "asset_ids": []string{a.ID},
		"tenants": []map[string]any{{"tenant_id": ten.ID}}, "start_on": "2025-01-01", "term_months": 36, "rent_cents": 70000,
		"provision_cents": 5000, "due_day": 5, "irl_ref_quarter": "2024-Q3", "irl_ref_value": 14451, "revision_month": 9}})
	if r.Status != 201 {
		t.Fatalf("lease: %d %s", r.Status, r.Body)
	}
	r.json(t, &l)
	superExec(t, `UPDATE leases SET state = 'active' WHERE id = $1`, l.ID)
	per := today().AddMonthsClamped(-2)
	if r := call(t, "POST", "/billing/runs", req{tok: tok, body: map[string]any{"period": per.String()[:7]}}); r.Status != 201 {
		t.Fatalf("billing run: %d %s", r.Status, r.Body)
	}
	path := "/leases/" + l.ID + "/recovery"
	inst := func(amounts ...int64) []map[string]any {
		out := []map[string]any{}
		for i, x := range amounts {
			out = append(out, map[string]any{"due_on": today().AddMonthsClamped(i + 1).String(), "amount_cents": x})
		}
		return out
	}
	if r := call(t, "POST", path+"/plans", req{tok: tok, body: map[string]any{"agreed_on": today().String(), "instalments": inst(30000)}}); r.Status != 422 {
		t.Errorf("short of the debt: %d %s", r.Status, r.Body)
	}
	var v planResult
	audited(t, seen, "POST /leases/{id}/recovery/plans", 200, func() resp {
		return call(t, "POST", path+"/plans", req{tok: tok, body: map[string]any{"agreed_on": today().String(), "instalments": inst(40000, 35000)}})
	}).json(t, &v)
	if v.Plan == nil || v.Plan.Status != "respected" || v.Plan.DebtCents != 75000 || !v.Suspended || len(v.Next) != 0 {
		t.Fatalf("plan %+v", v)
	}
	if r := call(t, "POST", path+"/plans", req{tok: tok, body: map[string]any{"agreed_on": today().String(), "instalments": inst(75000)}}); r.Status != 409 {
		t.Errorf("a second plan: %d %s", r.Status, r.Body)
	}
	if r := call(t, "POST", path+"/plans/"+v.Plan.ID+"/cancel", req{tok: tok, body: map[string]any{"cancelled_on": today().String(), "version": v.Plan.Version + 1}}); r.Status != 412 {
		t.Errorf("stale cancel: %d %s", r.Status, r.Body)
	}
	var c planResult
	audited(t, seen, "POST /leases/{id}/recovery/plans/{pid}/cancel", 200, func() resp {
		return call(t, "POST", path+"/plans/"+v.Plan.ID+"/cancel", req{tok: tok, body: map[string]any{"cancelled_on": today().String(), "version": v.Plan.Version}})
	}).json(t, &c)
	if c.Plan == nil || c.Plan.Status != "cancelled" || c.Suspended || len(c.Next) == 0 {
		t.Errorf("cancelled %+v", c)
	}

	var other idv
	call(t, "PUT", "/me", req{tok: token(t, subPlanX), body: map[string]any{"legal_name": "Bailleur X", "address": "1 rue X"}}).json(t, &other)
	if r := call(t, "POST", path+"/plans", req{tok: token(t, subPlanX), body: map[string]any{"agreed_on": today().String(), "instalments": inst(75000)}}); r.Status != 404 {
		t.Errorf("another owner writes: %d", r.Status)
	}
	rlsZeroRows(t, owner.ID, other.ID, subPlan, subPlanX, a.ID, map[string]string{"payment_plans": "owner_id = $1"})
}
