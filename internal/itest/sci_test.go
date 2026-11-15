//go:build integration

package itest

import (
	"testing"
	"time"
)

const (
	subSCI      = "9985" // creates the SCI and manages it (RENT-SPEC-019, part 19b, Q-133)
	subSCIOther = "9986" // a second manager, then an associate
	subSCIX     = "9987" // an owner with no access to the SCI
)

type scopeView struct {
	OwnerID string `json:"owner_id"`
	Role    string `json:"role"`
	Kind    string `json:"kind"`
	Manager bool   `json:"manager"`
}

type registerView struct {
	CapitalParts int `json:"capital_parts"`
	HeldParts    int `json:"held_parts"`
	Associates   []struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Me       bool   `json:"me"`
		PartsOn  int    `json:"parts_on"`
		Holdings []struct {
			ID       string  `json:"id"`
			Parts    int     `json:"parts"`
			ClosedOn *string `json:"closed_on"`
		} `json:"holdings"`
	} `json:"associates"`
}

// TestSCI creates an SCI at the income tax, whose creator becomes its
// manager with the owner's rights there; appoints and revokes a second
// manager, never the last; keeps its register of associates and their
// dated holdings through subscriptions and a transfer; and refuses an SCI at
// the IS, a capital exceeded, a person's declaration in the SCI's scope
// (Q-133). Another owner reads none of the register's rows.
func TestSCI(t *testing.T) {
	tok, tokO, tokX := token(t, subSCI), token(t, subSCIOther), token(t, subSCIX)
	up, upO := token(t, subSCI, stepUp(time.Minute)), token(t, subSCIOther, stepUp(time.Minute))
	var person, other idv
	call(t, "PUT", "/me", req{tok: tok, body: map[string]any{"legal_name": "Gérante Fictive", "address": "1 rue S, Nantes"}}).json(t, &person)
	call(t, "PUT", "/me", req{tok: tokX, body: map[string]any{"legal_name": "Bailleur X", "address": "2 rue X"}}).json(t, &other)

	sci := map[string]any{"legal_name": "SCI des Tilleuls", "address": "1 rue S, Nantes", "siren": "123 456 789",
		"tax_regime": "ir", "regime_from": "2024-01-10", "capital_parts": 1000}
	if r := call(t, "POST", "/entities", req{tok: tok, body: sci}); r.Status != 401 || r.problemType(t) != "auth/step-up-required" {
		t.Errorf("without the step-up: %d %s", r.Status, r.Body)
	}
	is := map[string]any{"legal_name": "SCI IS", "tax_regime": "is", "capital_parts": 100}
	if r := call(t, "POST", "/entities", req{tok: up, body: is}); r.Status != 422 || r.problemType(t) != "entity/is-unsupported" {
		t.Errorf("an SCI at the IS: %d %s", r.Status, r.Body)
	}
	var entity struct {
		ID           string `json:"id"`
		Kind         string `json:"kind"`
		Siren        string `json:"siren"`
		CapitalParts int    `json:"capital_parts"`
		Version      int    `json:"version"`
	}
	audited(t, seen, "POST /entities", 201, func() resp {
		return call(t, "POST", "/entities", req{tok: up, body: sci})
	}).json(t, &entity)
	if entity.Kind != "sci" || entity.Siren != "123456789" || entity.CapitalParts != 1000 {
		t.Fatalf("entity %+v", entity)
	}
	if n := superCount(t, `SELECT count(*) FROM owners WHERE id = $1 AND socrate_sub IS NULL AND kind = 'sci'`, entity.ID); n != 1 {
		t.Errorf("the SCI has no Socrate subject: %d", n)
	}
	var me struct {
		Scopes []scopeView `json:"scopes"`
	}
	call(t, "GET", "/me", req{tok: tok}).json(t, &me)
	found := false
	for _, s := range me.Scopes {
		if s.OwnerID == entity.ID {
			found = s.Kind == "sci" && s.Manager && s.Role == "owner"
		}
	}
	if !found {
		t.Fatalf("the creator manages the SCI: %+v", me.Scopes)
	}
	in := req{tok: tok, scope: entity.ID}

	if r := call(t, "GET", "/entity", req{tok: tok}); r.Status != 409 || r.problemType(t) != "entity/not-sci" {
		t.Errorf("GET /entity in a person's scope: %d %s", r.Status, r.Body)
	}
	upd := map[string]any{"legal_name": "SCI des Tilleuls", "address": "3 rue S, Nantes", "siren": "123456789", "tax_regime": "ir", "capital_parts": 1000}
	if r := call(t, "PUT", "/entity", req{tok: tok, scope: entity.ID, ifMatch: etag(1), body: map[string]any{"legal_name": "SCI", "tax_regime": "ir", "capital_parts": 2000}}); r.Status != 422 {
		t.Errorf("the capital does not change: %d %s", r.Status, r.Body)
	}
	audited(t, seen, "PUT /entity", 200, func() resp {
		return call(t, "PUT", "/entity", req{tok: tok, scope: entity.ID, ifMatch: etag(1), body: upd})
	})

	// The manager writes in the SCI's scope, as its owner.
	var asset idv
	if r := call(t, "POST", "/assets", req{tok: tok, scope: entity.ID, body: map[string]any{"kind": "apartment", "address": "5 rue S, Nantes"}}); r.Status != 201 {
		t.Fatalf("an asset of the SCI: %d %s", r.Status, r.Body)
	} else {
		r.json(t, &asset)
	}
	if r := call(t, "POST", "/grants", req{tok: up, body: map[string]any{"grantee_sub": subSCIOther, "role": "manager"}}); r.Status != 422 || r.problemType(t) != "grant/manager-sci-only" {
		t.Errorf("a manager of a person: %d %s", r.Status, r.Body)
	}
	var g2 struct {
		ID string `json:"id"`
	}
	if r := call(t, "POST", "/grants", req{tok: up, scope: entity.ID, body: map[string]any{"grantee_sub": subSCIOther, "role": "manager"}}); r.Status != 201 {
		t.Fatalf("a second manager: %d %s", r.Status, r.Body)
	} else {
		r.json(t, &g2)
	}
	if r := call(t, "GET", "/entity", req{tok: tokO, scope: entity.ID}); r.Status != 200 {
		t.Errorf("the second manager reads the SCI: %d %s", r.Status, r.Body)
	}

	// The register.
	var a, b, c struct {
		ID string `json:"id"`
	}
	audited(t, seen, "POST /associates", 201, func() resp {
		return call(t, "POST", "/associates", req{tok: tok, scope: entity.ID, body: map[string]any{"name": "Gérante Fictive", "link": "self"}})
	}).json(t, &a)
	if r := call(t, "POST", "/associates", req{tok: tok, scope: entity.ID, body: map[string]any{"name": "Encore moi", "link": "self"}}); r.Status != 409 || r.problemType(t) != "sci/associate-linked" {
		t.Errorf("the same person twice: %d %s", r.Status, r.Body)
	}
	if r := call(t, "POST", "/associates", req{tok: tok, scope: entity.ID, body: map[string]any{"name": "X", "grant_id": person.ID}}); r.Status != 422 || r.problemType(t) != "sci/link-grant" {
		t.Errorf("a link to no live grant: %d %s", r.Status, r.Body)
	}
	call(t, "POST", "/associates", req{tok: tok, scope: entity.ID, body: map[string]any{"name": "Associé Fictif", "grant_id": g2.ID}}).json(t, &b)
	call(t, "POST", "/associates", req{tok: tok, scope: entity.ID, body: map[string]any{"name": "Nom Seul"}}).json(t, &c)
	if r := call(t, "POST", "/associates", req{tok: tok, body: map[string]any{"name": "Z"}}); r.Status != 409 || r.problemType(t) != "entity/not-sci" {
		t.Errorf("an associate of a person: %d %s", r.Status, r.Body)
	}
	audited(t, seen, "PUT /associates/{id}", 200, func() resp {
		return call(t, "PUT", "/associates/"+c.ID, req{tok: tok, scope: entity.ID, ifMatch: etag(1), body: map[string]any{"name": "Nom Seul Fictif"}})
	})

	mv := func(body map[string]any) resp {
		return call(t, "POST", "/sci/movements", req{tok: tok, scope: entity.ID, body: body})
	}
	var subA struct {
		Opened []struct {
			ID string `json:"id"`
		} `json:"opened"`
	}
	audited(t, seen, "POST /sci/movements", 201, func() resp {
		return mv(map[string]any{"on_date": "2024-01-10", "reason": "subscription", "parts": 600, "to_associate_id": a.ID})
	}).json(t, &subA)
	if r := call(t, "POST", "/sci/declarations", req{tok: up, scope: entity.ID, body: map[string]any{"year": 2025}}); r.Status != 409 || r.problemType(t) != "sci/register-incomplete" {
		t.Errorf("600 parts held out of 1000: %d %s", r.Status, r.Body)
	}
	if r := mv(map[string]any{"on_date": "2024-01-10", "reason": "subscription", "parts": 400, "to_associate_id": b.ID}); r.Status != 201 {
		t.Fatalf("subscription b: %d %s", r.Status, r.Body)
	}
	if r := mv(map[string]any{"on_date": "2025-01-01", "reason": "subscription", "parts": 1, "to_associate_id": c.ID}); r.Status != 422 || r.problemType(t) != "sci/capital-exceeded" {
		t.Errorf("beyond the capital: %d %s", r.Status, r.Body)
	}
	from := subA.Opened[0].ID
	if r := mv(map[string]any{"on_date": "2025-07-01", "reason": "transfer", "parts": 700, "from_holding_id": from, "to_associate_id": c.ID}); r.Status != 422 || r.problemType(t) != "sci/holding-parts" {
		t.Errorf("more parts than held: %d %s", r.Status, r.Body)
	}
	if r := mv(map[string]any{"on_date": "2025-07-01", "reason": "gift", "parts": 100, "from_holding_id": from, "to_associate_id": a.ID}); r.Status != 422 || r.problemType(t) != "sci/same-associate" {
		t.Errorf("to the same associate: %d %s", r.Status, r.Body)
	}
	if r := mv(map[string]any{"on_date": "2025-07-01", "reason": "transfer", "parts": 200, "from_holding_id": from, "to_associate_id": c.ID}); r.Status != 201 {
		t.Fatalf("transfer: %d %s", r.Status, r.Body)
	}
	if r := mv(map[string]any{"on_date": "2025-09-01", "reason": "transfer", "parts": 10, "from_holding_id": from, "to_associate_id": b.ID}); r.Status != 409 || r.problemType(t) != "sci/holding-closed" {
		t.Errorf("from a closed holding: %d %s", r.Status, r.Body)
	}
	if n := superCount(t, `SELECT count(*) FROM sci_holdings WHERE owner_id = $1`, entity.ID); n != 4 {
		t.Errorf("holdings: %d, want 4 (600 closed, 400, 400 kept, 200 received)", n)
	}

	parts := func(on string) map[string]int {
		var reg registerView
		call(t, "GET", "/associates?on="+on, req{tok: tok, scope: entity.ID}).json(t, &reg)
		out := map[string]int{"held": reg.HeldParts, "capital": reg.CapitalParts}
		for _, x := range reg.Associates {
			out[x.ID] = x.PartsOn
		}
		return out
	}
	if p := parts("2025-06-30"); p[a.ID] != 600 || p[b.ID] != 400 || p[c.ID] != 0 || p["held"] != 1000 || p["capital"] != 1000 {
		t.Errorf("before the transfer %v", p)
	}
	if p := parts("2025-12-31"); p[a.ID] != 400 || p[b.ID] != 400 || p[c.ID] != 200 || p["held"] != 1000 {
		t.Errorf("on 31 December %v", p)
	}
	var movements []map[string]any
	call(t, "GET", "/sci/movements", req{tok: tok, scope: entity.ID}).json(t, &movements)
	if len(movements) != 3 {
		t.Errorf("movements %d", len(movements))
	}

	// The 2072 of 2025: a property tax paid by the SCI, a deficit split by
	// the parts held on 31 December (400, 400, 200).
	if r := call(t, "POST", "/charges", req{tok: tok, scope: entity.ID, body: map[string]any{"asset_id": asset.ID, "charged_on": "2025-10-15", "paid_on": "2025-10-15",
		"category": "property_tax", "amount_cents": 120001, "recoverable_cents": 0}}); r.Status != 201 {
		t.Fatalf("charge: %d %s", r.Status, r.Body)
	}
	type yearView struct {
		Declared bool `json:"declared"`
		Result   struct {
			NetCents           int64 `json:"net_cents"`
			OtherExpensesCents int64 `json:"other_expenses_cents"`
		} `json:"result"`
		Allocations []struct {
			AssociateID string  `json:"associate_id"`
			PersonSub   *string `json:"person_sub"`
			Parts       int     `json:"parts"`
			NetCents    int64   `json:"net_cents"`
		} `json:"allocations"`
		Blockers []string `json:"blockers"`
	}
	var y yearView
	call(t, "GET", "/sci/result?year=2025", req{tok: tok, scope: entity.ID}).json(t, &y)
	if y.Declared || y.Result.NetCents != -120001 || len(y.Blockers) != 0 || len(y.Allocations) != 3 {
		t.Fatalf("2025 computed %+v", y)
	}
	nets := map[string]int64{}
	for _, al := range y.Allocations {
		nets[al.AssociateID] = al.NetCents
	}
	if nets[a.ID] != -48001 || nets[b.ID] != -48000 || nets[c.ID] != -24000 {
		t.Errorf("the split by 400/400/200 parts %v", nets)
	}
	if r := call(t, "POST", "/sci/declarations", req{tok: tok, scope: entity.ID, body: map[string]any{"year": 2025}}); r.Status != 401 {
		t.Errorf("without the step-up: %d", r.Status)
	}
	if r := call(t, "POST", "/sci/declarations", req{tok: up, scope: entity.ID, body: map[string]any{"year": 2026}}); r.Status != 409 || r.problemType(t) != "sci/year-open" {
		t.Errorf("an open year: %d %s", r.Status, r.Body)
	}
	audited(t, seen, "POST /sci/declarations", 201, func() resp {
		return call(t, "POST", "/sci/declarations", req{tok: up, scope: entity.ID, body: map[string]any{"year": 2025}})
	}).json(t, &y)
	if !y.Declared || len(y.Allocations) != 3 {
		t.Fatalf("issued %+v", y)
	}
	if r := call(t, "POST", "/sci/declarations", req{tok: up, scope: entity.ID, body: map[string]any{"year": 2025}}); r.Status != 409 || r.problemType(t) != "sci/declared" {
		t.Errorf("issued twice: %d %s", r.Status, r.Body)
	}
	if n := superCount(t, `SELECT count(*) FROM sci_allocations WHERE owner_id = $1 AND tax_year = 2025 AND person_sub = $2`, entity.ID, subSCI); n != 1 {
		t.Errorf("the linked associate's share names them: %d", n)
	}
	// Stored as issued: a later charge of 2025 does not change it.
	if r := call(t, "POST", "/charges", req{tok: tok, scope: entity.ID, body: map[string]any{"asset_id": asset.ID, "charged_on": "2025-11-15", "paid_on": "2025-11-15",
		"category": "property_tax", "amount_cents": 1000, "recoverable_cents": 0}}); r.Status != 201 {
		t.Fatalf("late charge: %d %s", r.Status, r.Body)
	}
	call(t, "GET", "/sci/result?year=2025", req{tok: tok, scope: entity.ID}).json(t, &y)
	if !y.Declared || y.Result.NetCents != -120001 {
		t.Errorf("as issued %+v", y.Result)
	}
	var list []map[string]any
	call(t, "GET", "/sci/declarations", req{tok: tok, scope: entity.ID}).json(t, &list)
	if len(list) != 1 {
		t.Errorf("declarations %d", len(list))
	}
	if r := mv(map[string]any{"on_date": "2025-11-01", "reason": "gift", "parts": 10, "from_holding_id": subA.Opened[0].ID, "to_associate_id": b.ID}); r.Status != 409 || r.problemType(t) != "sci/year-declared" {
		t.Errorf("a movement in a declared year: %d %s", r.Status, r.Body)
	}

	// A person's declarations are refused in the SCI's scope.
	if r := call(t, "POST", "/tax/summaries", req{tok: up, scope: entity.ID, body: map[string]any{"year": 2025, "regime": "reel"}}); r.Status != 409 || r.problemType(t) != "tax/person-only" {
		t.Errorf("a 2044 for the SCI: %d %s", r.Status, r.Body)
	}
	if r := call(t, "POST", "/bic/declarations", req{tok: up, scope: entity.ID, body: map[string]any{"year": 2025, "regime": "reel"}}); r.Status != 409 || r.problemType(t) != "tax/person-only" {
		t.Errorf("a BIC declaration for the SCI: %d %s", r.Status, r.Body)
	}

	// Managers: never the last one.
	var grants []struct {
		ID         string `json:"id"`
		GranteeSub string `json:"grantee_sub"`
		Role       string `json:"role"`
	}
	call(t, "GET", "/grants", in).json(t, &grants)
	mine := ""
	for _, g := range grants {
		if g.GranteeSub == subSCI && g.Role == "manager" {
			mine = g.ID
		}
	}
	if r := call(t, "DELETE", "/grants/"+g2.ID, req{tok: upO, scope: entity.ID}); r.Status != 422 || r.problemType(t) != "grant/self-revoke" {
		t.Errorf("a manager revokes their own grant: %d %s", r.Status, r.Body)
	}
	if r := call(t, "DELETE", "/grants/"+g2.ID, req{tok: up, scope: entity.ID}); r.Status != 200 {
		t.Fatalf("the second manager revoked: %d %s", r.Status, r.Body)
	}
	if r := call(t, "DELETE", "/grants/"+mine, req{tok: up, scope: entity.ID}); r.Status != 422 || r.problemType(t) != "grant/self-revoke" {
		t.Errorf("the last manager: %d %s", r.Status, r.Body)
	}
	if r := call(t, "GET", "/entity", req{tok: tokO, scope: entity.ID}); r.Status != 403 {
		t.Errorf("a revoked manager: %d %s", r.Status, r.Body)
	}
	if r := call(t, "GET", "/associates", req{tok: tokX, scope: entity.ID}); r.Status != 403 {
		t.Errorf("an outsider: %d %s", r.Status, r.Body)
	}

	rlsZeroRows(t, entity.ID, other.ID, subSCI, subSCIX, asset.ID, map[string]string{"sci_associates": "owner_id = $1",
		"sci_movements": "owner_id = $1", "sci_holdings": "owner_id = $1", "sci_declarations": "owner_id = $1", "sci_allocations": "owner_id = $1"})
}
