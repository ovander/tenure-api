//go:build integration

package itest

import (
	"testing"
	"time"
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

	// The year's BIC result (part 18c): no rent yet in this scenario, so no
	// room: nothing deducted, the whole depreciation carried; the property
	// tax and the lamp expensed make a deficit. 2026 chains 2025's carry.
	if r := call(t, "POST", "/charges", req{tok: tok, body: map[string]any{"asset_id": a.ID, "charged_on": "2025-10-15", "paid_on": "2025-10-15",
		"category": "property_tax", "amount_cents": 120000, "recoverable_cents": 0}}); r.Status != 201 {
		t.Fatalf("charge: %d %s", r.Status, r.Body)
	}
	type yearResult struct {
		Year              int   `json:"year"`
		ReceiptsCents     int64 `json:"receipts_cents"`
		ChargesCents      int64 `json:"charges_cents"`
		DepreciationCents int64 `json:"depreciation_cents"`
		CarriedInCents    int64 `json:"carried_in_cents"`
		DeductedCents     int64 `json:"deducted_cents"`
		CarriedOutCents   int64 `json:"carried_out_cents"`
		ResultCents       int64 `json:"result_cents"`
	}
	var res struct {
		Result yearResult   `json:"result"`
		Chain  []yearResult `json:"chain"`
		Assets []struct {
			AssetID string `json:"asset_id"`
			ShareBP int64  `json:"share_bp"`
		} `json:"assets"`
	}
	call(t, "GET", "/bic/result?year=2025", req{tok: tok}).json(t, &res)
	if r := res.Result; r.ChargesCents != 120000+3000 || r.DepreciationCents != dep.TotalCents || r.DeductedCents != 0 ||
		r.CarriedOutCents != dep.TotalCents || r.ResultCents != -123000 || len(res.Assets) != 1 || res.Assets[0].ShareBP <= 0 {
		t.Errorf("result 2025 %+v", res)
	}
	call(t, "GET", "/bic/result?year=2026", req{tok: tok}).json(t, &res)
	if len(res.Chain) != 2 || res.Result.CarriedInCents != dep.TotalCents {
		t.Errorf("result 2026 %+v", res)
	}
	var none struct {
		Assets []any `json:"assets"`
	}
	call(t, "GET", "/bic/result?year=2025", req{tok: tokX}).json(t, &none)
	if len(none.Assets) != 0 {
		t.Errorf("another owner's result: %+v", none)
	}

	// Part 18d: 2025 declared under the micro, behind a recent strong
	// sign-in; its grid frozen; the year declared once, in order; 2026
	// chains it as declared.
	decl := map[string]any{"year": 2025, "regime": "micro"}
	if r := call(t, "POST", "/bic/declarations", req{tok: tok, body: decl}); r.Status != 401 {
		t.Errorf("a declaration without step-up: %d", r.Status)
	}
	var d struct {
		Year   int    `json:"year"`
		Regime string `json:"regime"`
		Result struct {
			Declared        bool  `json:"declared"`
			CarriedOutCents int64 `json:"carried_out_cents"`
			TaxableCents    int64 `json:"taxable_cents"`
		} `json:"result"`
		Grids []struct {
			Version int `json:"version"`
		} `json:"grids"`
	}
	audited(t, seen, "POST /bic/declarations", 201, func() resp {
		return call(t, "POST", "/bic/declarations", req{tok: token(t, subBIC, stepUp(time.Minute)), body: decl})
	}).json(t, &d)
	if d.Year != 2025 || d.Regime != "micro" || !d.Result.Declared || d.Result.CarriedOutCents != 0 || len(d.Grids) != 1 || d.Grids[0].Version != 2 {
		t.Errorf("declaration %+v", d)
	}
	if r := call(t, "POST", "/bic/declarations", req{tok: token(t, subBIC, stepUp(time.Minute)), body: decl}); r.problemType(t) != "bic/declared" {
		t.Errorf("declared twice: %d %s", r.Status, r.Body)
	}
	if r := call(t, "POST", "/bic/declarations", req{tok: token(t, subBIC, stepUp(time.Minute)), body: map[string]any{"year": 2024, "regime": "reel"}}); r.problemType(t) != "bic/declaration-order" {
		t.Errorf("an earlier year after a later one: %d %s", r.Status, r.Body)
	}
	if r := call(t, "PUT", "/assets/"+a.ID+"/bic-grid", req{tok: tok, ifMatch: etag(2), body: body}); r.problemType(t) != "bic/grid-frozen" {
		t.Errorf("a frozen grid changed: %d %s", r.Status, r.Body)
	}
	var chained struct {
		Chain []struct {
			Year     int  `json:"year"`
			Declared bool `json:"declared"`
		} `json:"chain"`
	}
	call(t, "GET", "/bic/result?year=2026", req{tok: tok}).json(t, &chained)
	if len(chained.Chain) != 2 || !chained.Chain[0].Declared || chained.Chain[1].Declared {
		t.Errorf("2026 after 2025 declared %+v", chained)
	}
	var list []map[string]any
	call(t, "GET", "/bic/declarations", req{tok: tok}).json(t, &list)
	if len(list) != 1 {
		t.Errorf("declarations %v", list)
	}

	rlsZeroRows(t, owner.ID, other.ID, subBIC, subBICX, a.ID, map[string]string{"bic_grids": "owner_id = $1", "bic_fixed_assets": "owner_id = $1",
		"bic_declarations": "owner_id = $1"})
}
