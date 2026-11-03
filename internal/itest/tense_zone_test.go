//go:build integration

package itest

import (
	"strings"
	"testing"
)

const (
	subZ = "9101" // owner of the zone tendue scenario (Q-102)
)

// TestTenseZone: a dwelling in a zone tendue without a rent ceiling (a
// Baillargues flat) is accepted without a ceiling zone, and a new lease on it
// may not exceed the previous rent revised by IRL; a rent-ceiling asset is
// always returned as tense.
func TestTenseZone(t *testing.T) {
	tok := token(t, subZ)
	var flat, controlled, tenant, first idv
	call(t, "PUT", "/me", req{tok: tok, body: map[string]any{"legal_name": "Bailleur Z", "address": "1 place de la Comédie, Montpellier"}})

	var asset struct {
		TenseZone      bool `json:"tense_zone"`
		ControlledZone bool `json:"controlled_zone"`
	}
	r := call(t, "POST", "/assets", req{tok: tok, body: map[string]any{"kind": "apartment", "address": "3 rue du Fort, Baillargues",
		"surface_hundredths_m2": 4000, "tense_zone": true}})
	if r.Status != 201 {
		t.Fatalf("tense asset: %d %s", r.Status, r.Body)
	}
	r.json(t, &flat)
	r.json(t, &asset)
	if !asset.TenseZone || asset.ControlledZone {
		t.Errorf("tense asset read back as %+v", asset)
	}

	r = call(t, "POST", "/assets", req{tok: tok, body: map[string]any{"kind": "apartment", "address": "2 rue Foch, Montpellier",
		"surface_hundredths_m2": 4000, "controlled_zone": true, "rent_control_zone": "Z1", "rent_control_category": "T2"}})
	if r.Status != 201 {
		t.Fatalf("controlled asset: %d %s", r.Status, r.Body)
	}
	r.json(t, &controlled)
	call(t, "GET", "/assets/"+controlled.ID, req{tok: tok}).json(t, &asset)
	if !asset.TenseZone || !asset.ControlledZone {
		t.Errorf("a rent-ceiling asset must read as tense: %+v", asset)
	}

	call(t, "POST", "/tenants", req{tok: tok, body: map[string]any{"names": []string{"Lucie Fabre"}, "email": "lucie.fabre@example.test"}}).json(t, &tenant)
	lease := func(start string, rent int) resp {
		return call(t, "POST", "/leases", req{tok: tok, body: map[string]any{"kind": "unfurnished", "asset_ids": []string{flat.ID},
			"tenants": []map[string]any{{"tenant_id": tenant.ID}}, "start_on": start, "term_months": 36, "rent_cents": rent,
			"provision_cents": 5000, "deposit_cents": rent, "due_day": 5, "irl_ref_quarter": "2024-Q4", "irl_ref_value": 14464, "revision_month": 3}})
	}
	r = lease("2025-03-01", 60000)
	if r.Status != 201 {
		t.Fatalf("first lease: %d %s", r.Status, r.Body)
	}
	r.json(t, &first)
	readyToActivate(t, tok, first.ID)
	if r := call(t, "POST", "/leases/"+first.ID+"/transitions", req{tok: tok, body: map[string]any{"action": "activate"}}); r.Status != 200 {
		t.Fatalf("activate: %d %s", r.Status, r.Body)
	}

	// The relocation check needs the latest IRL of the previous lease's
	// quarter. irl.fetch fills that cache in the billing scenario; triggering
	// it here would issue that scenario's revision notices, so the test INSEE
	// value is cached directly (the job inserts with ON CONFLICT DO NOTHING).
	if _, err := env.super.Exec(`INSERT INTO irl_index (quarter, value, published_on) VALUES ('2024-Q4', 14464, '2025-01-15') ON CONFLICT (quarter) DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	if r := lease("2026-09-01", 90000); r.Status != 422 || !strings.Contains(string(r.Body), "lease/relocation-rule-exceeded") {
		t.Errorf("a new lease above the previous rent revised by IRL: %d %s", r.Status, r.Body)
	}
	if r := lease("2026-09-01", 60000); r.Status != 201 {
		t.Errorf("a new lease at the previous rent: %d %s", r.Status, r.Body)
	}
}
