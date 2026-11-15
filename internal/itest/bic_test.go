//go:build integration

package itest

import (
	"testing"
)

const (
	subBIC  = "9983" // owner of the furnished-letting scenario (RENT-SPEC-019, part 18b)
	subBICX = "9984"
)

type gridView struct {
	Version     int     `json:"version"`
	Proposed    bool    `json:"proposed"`
	InServiceOn *string `json:"in_service_on"`
	PriceCents  int64   `json:"price_cents"`
	BasisCents  int64   `json:"basis_cents"`
	Lines       []struct {
		Component string `json:"component"`
		ShareBP   int64  `json:"share_bp"`
		Months    int    `json:"months"`
	} `json:"lines"`
	Bases []int64 `json:"bases"`
}

type itemView struct {
	ID         string `json:"id"`
	Months     int    `json:"months"`
	Treatment  string `json:"treatment"`
	Suggested  string `json:"suggested"`
	Overridden bool   `json:"overridden"`
	Version    int    `json:"version"`
}

// TestBIC proposes a furnished apartment's default grid from the
// parameters, its price and its first furnished lease; records it, then a
// second version with its reason; records furniture and works, each
// expensed or capitalised by nature and threshold, one forced with its
// reason; and computes the year's depreciation (Q-132). Another owner reads
// none of the rows.
func TestBIC(t *testing.T) {
	tok, tokX := token(t, subBIC), token(t, subBICX)
	var owner, other, a, ten, lease idv
	call(t, "PUT", "/me", req{tok: tok, body: map[string]any{"legal_name": "Bailleur Meublé", "address": "10 rue M, Lyon"}}).json(t, &owner)
	call(t, "PUT", "/me", req{tok: tokX, body: map[string]any{"legal_name": "Bailleur W", "address": "11 rue W"}}).json(t, &other)
	call(t, "POST", "/assets", req{tok: tok, body: map[string]any{"kind": "apartment", "address": "10 rue M, Lyon", "furnished": true,
		"acquired_on": "2025-01-10", "acquisition_cents": 20000000}}).json(t, &a)
	call(t, "POST", "/tenants", req{tok: tok, body: map[string]any{"names": []string{"Léa Fictive"}, "email": "meuble@example.test"}}).json(t, &ten)
	if r := call(t, "POST", "/leases", req{tok: tok, body: map[string]any{"kind": "furnished", "asset_ids": []string{a.ID},
		"tenants": []map[string]any{{"tenant_id": ten.ID}}, "start_on": "2025-03-15", "term_months": 12, "rent_cents": 90000,
		"provision_cents": 5000, "due_day": 5, "irl_ref_quarter": "2024-Q3", "irl_ref_value": 14451, "revision_month": 3}}); r.Status != 201 {
		t.Fatalf("lease: %d %s", r.Status, r.Body)
	} else {
		r.json(t, &lease)
	}
	superExec(t, `UPDATE leases SET state = 'active' WHERE id = $1`, lease.ID)

	var g gridView
	call(t, "GET", "/assets/"+a.ID+"/bic-grid", req{tok: tok}).json(t, &g)
	if !g.Proposed || g.Version != 0 || g.InServiceOn == nil || *g.InServiceOn != "2025-03-15" || g.PriceCents != 20000000 || len(g.Lines) == 0 {
		t.Fatalf("proposal %+v", g)
	}
	body := map[string]any{"in_service_on": "2025-03-15", "price_cents": 20000000, "acquisition_costs_cents": 1500000, "lines": g.Lines}
	audited(t, seen, "PUT /assets/{id}/bic-grid", 200, func() resp {
		return call(t, "PUT", "/assets/"+a.ID+"/bic-grid", req{tok: tok, ifMatch: etag(0), body: body})
	}).json(t, &g)
	var sum int64
	for _, b := range g.Bases {
		sum += b
	}
	if g.Version != 1 || g.BasisCents != 21500000 || sum != 21500000 {
		t.Errorf("grid v1 %+v", g)
	}
	if r := call(t, "PUT", "/assets/"+a.ID+"/bic-grid", req{tok: tok, ifMatch: etag(1), body: body}); r.Status != 422 {
		t.Errorf("a second version without its reason: %d %s", r.Status, r.Body)
	}
	bad := map[string]any{"in_service_on": "2025-03-15", "price_cents": 20000000, "reason": "x", "lines": []map[string]any{{"component": "all", "share_bp": 1, "months": 12}}}
	if r := call(t, "PUT", "/assets/"+a.ID+"/bic-grid", req{tok: tok, ifMatch: etag(1), body: bad}); r.Status != 422 {
		t.Errorf("shares short of 100 %%: %d %s", r.Status, r.Body)
	}
	body["reason"] = "Frais de notaire corrigés"
	body["acquisition_costs_cents"] = 1600000
	call(t, "PUT", "/assets/"+a.ID+"/bic-grid", req{tok: tok, ifMatch: etag(1), body: body}).json(t, &g)
	if g.Version != 2 || g.BasisCents != 21600000 {
		t.Errorf("grid v2 %+v", g)
	}
	if r := call(t, "PUT", "/assets/"+a.ID+"/bic-grid", req{tok: tok, ifMatch: etag(1), body: body}); r.Status != 412 {
		t.Errorf("stale grid: %d", r.Status)
	}
	if r := call(t, "GET", "/assets/"+a.ID+"/bic-grid", req{tok: tokX}); r.Status != 404 {
		t.Errorf("another owner's grid: %d", r.Status)
	}

	item := func(want int, b map[string]any) itemView {
		t.Helper()
		var v itemView
		r := audited(t, seen, "POST /assets/{id}/fixed-assets", want, func() resp {
			return call(t, "POST", "/assets/"+a.ID+"/fixed-assets", req{tok: tok, body: b})
		})
		if r.Status < 300 {
			r.json(t, &v)
		}
		return v
	}
	furniture := item(201, map[string]any{"kind": "furniture", "label": "Mobilier", "cost_cents": 600000, "in_service_on": "2025-03-15"})
	lamp := item(201, map[string]any{"kind": "furniture", "label": "Lampe", "cost_cents": 3000, "in_service_on": "2025-03-15"})
	item(422, map[string]any{"kind": "furniture", "label": "Chaise", "cost_cents": 3000, "in_service_on": "2025-03-15", "treatment": "asset"})
	forced := item(201, map[string]any{"kind": "furniture", "label": "Chaise", "cost_cents": 3000, "in_service_on": "2025-03-15", "treatment": "asset",
		"override_reason": "Lot de chaises acheté ensemble", "months": 60})
	repair := item(201, map[string]any{"kind": "works", "works": "repair", "label": "Joints de la douche", "cost_cents": 900000, "in_service_on": "2025-06-01", "months": 12})
	item(422, map[string]any{"kind": "works", "works": "improvement", "label": "Cuisine", "cost_cents": 1200000, "in_service_on": "2025-09-01"})
	kitchen := item(201, map[string]any{"kind": "works", "works": "improvement", "label": "Cuisine", "cost_cents": 1200000, "in_service_on": "2025-09-01", "months": 180})
	if furniture.Treatment != "asset" || furniture.Months <= 0 || lamp.Treatment != "expense" || !forced.Overridden || forced.Treatment != "asset" ||
		repair.Treatment != "expense" || kitchen.Treatment != "asset" {
		t.Errorf("treatments: furniture %+v lamp %+v forced %+v repair %+v kitchen %+v", furniture, lamp, forced, repair, kitchen)
	}
	audited(t, seen, "PUT /fixed-assets/{id}", 200, func() resp {
		return call(t, "PUT", "/fixed-assets/"+forced.ID, req{tok: tok, ifMatch: etag(forced.Version), body: map[string]any{"kind": "furniture", "label": "Chaise",
			"cost_cents": 3000, "in_service_on": "2025-03-15", "treatment": "asset", "override_reason": "Lot de chaises acheté ensemble", "months": 60,
			"disposed_on": "2026-01-31"}})
	})

	var dep struct {
		GridVersion   int   `json:"grid_version"`
		TotalCents    int64 `json:"total_cents"`
		ExpensedCents int64 `json:"expensed_cents"`
		Rows          []struct {
			Ref       string `json:"ref"`
			YearCents int64  `json:"year_cents"`
		} `json:"rows"`
	}
	call(t, "GET", "/assets/"+a.ID+"/depreciation?year=2025", req{tok: tok}).json(t, &dep)
	if dep.GridVersion != 2 || dep.TotalCents <= 0 || dep.ExpensedCents != 3000+900000 || len(dep.Rows) != len(g.Lines)+3 {
		t.Errorf("depreciation 2025 %+v", dep)
	}
	if r := call(t, "GET", "/assets/"+a.ID+"/depreciation?year=abc", req{tok: tok}); r.Status != 422 {
		t.Errorf("bad year: %d", r.Status)
	}

	rlsZeroRows(t, owner.ID, other.ID, subBIC, subBICX, a.ID, map[string]string{"bic_grids": "owner_id = $1", "bic_fixed_assets": "owner_id = $1"})
}
