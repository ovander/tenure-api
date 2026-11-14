//go:build integration

package itest

import (
	"testing"

	"github.com/ovander/tenure-api/internal/calendar"
)

const (
	subContracts  = "9981" // owner of the maintenance contracts scenario (RENT-SPEC-015, part 17b)
	subContractsX = "9982"
)

type contractView struct {
	ID             string  `json:"id"`
	IntervalMonths int     `json:"interval_months"`
	Responsible    string  `json:"responsible"`
	LastDoneOn     *string `json:"last_done_on"`
	NextDueOn      *string `json:"next_due_on"`
	Visits         []struct {
		DoneOn     string  `json:"done_on"`
		DocumentID *string `json:"document_id"`
	} `json:"visits"`
	Version int `json:"version"`
}

// TestMaintenanceContracts records a boiler's servicing with the legal
// rhythm filled in, refuses a longer interval and an "other" without one,
// shows the first visit on the calendar, records a visit with its
// certificate, which moves the next one a year on, and proves another owner
// reads none of the rows (Q-131).
func TestMaintenanceContracts(t *testing.T) {
	tok, tokX := token(t, subContracts), token(t, subContractsX)
	var owner, other, a idv
	call(t, "PUT", "/me", req{tok: tok, body: map[string]any{"legal_name": "Bailleur Entretien", "address": "8 rue E, Lyon"}}).json(t, &owner)
	call(t, "PUT", "/me", req{tok: tokX, body: map[string]any{"legal_name": "Bailleur Z", "address": "9 rue Z"}}).json(t, &other)
	call(t, "POST", "/assets", req{tok: tok, body: map[string]any{"kind": "house", "address": "8 rue E, Lyon"}}).json(t, &a)

	first := today().AddDays(10).String()
	boiler := map[string]any{"asset_id": a.ID, "kind": "boiler", "title": "Entretien de la chaudière", "first_due_on": first}
	var c contractView
	audited(t, seen, "POST /maintenance-contracts", 201, func() resp {
		return call(t, "POST", "/maintenance-contracts", req{tok: tok, body: boiler})
	}).json(t, &c)
	if c.IntervalMonths != 12 || c.Responsible != "tenant" || c.NextDueOn == nil || *c.NextDueOn != first {
		t.Fatalf("boiler %+v", c)
	}
	longer := map[string]any{"asset_id": a.ID, "kind": "boiler", "title": "Chaudière", "first_due_on": first, "interval_months": 24}
	if r := call(t, "POST", "/maintenance-contracts", req{tok: tok, body: longer}); r.Status != 422 {
		t.Errorf("an interval beyond the legal one: %d %s", r.Status, r.Body)
	}
	if r := call(t, "POST", "/maintenance-contracts", req{tok: tok, body: map[string]any{"asset_id": a.ID, "kind": "other", "title": "VMC", "first_due_on": first}}); r.Status != 422 {
		t.Errorf("other without interval: %d %s", r.Status, r.Body)
	}
	if r := call(t, "POST", "/maintenance-contracts", req{tok: tokX, body: boiler}); r.Status != 422 {
		t.Errorf("another owner's asset: %d %s", r.Status, r.Body)
	}
	boiler["responsible"] = "owner"
	boiler["notes"] = "Chaudière gaz, contrat annuel."
	audited(t, seen, "PUT /maintenance-contracts/{id}", 200, func() resp {
		return call(t, "PUT", "/maintenance-contracts/"+c.ID, req{tok: tok, ifMatch: etag(c.Version), body: boiler})
	}).json(t, &c)

	due := func() []calendar.Item {
		var v calendar.View
		call(t, "GET", "/calendar?asset_id="+a.ID, req{tok: tok}).json(t, &v)
		var out []calendar.Item
		for _, it := range v.Items {
			if it.Kind == calendar.KindMaintenanceDue {
				out = append(out, it)
			}
		}
		return out
	}
	if items := due(); len(items) != 1 || items[0].On.String() != first || items[0].ContractID != c.ID || items[0].Responsible != "owner" {
		t.Errorf("calendar %+v", items)
	}

	var cert docView
	upload(t, tok, "", map[string]string{"class": "supporting", "asset_id": a.ID}, "attestation-entretien.pdf", signedPDF).json(t, &cert)
	if r := call(t, "POST", "/maintenance-contracts/"+c.ID+"/visits", req{tok: tok, ifMatch: etag(c.Version), body: map[string]any{"done_on": today().AddDays(1).String()}}); r.Status != 422 {
		t.Errorf("a visit in the future: %d %s", r.Status, r.Body)
	}
	audited(t, seen, "POST /maintenance-contracts/{id}/visits", 200, func() resp {
		return call(t, "POST", "/maintenance-contracts/"+c.ID+"/visits", req{tok: tok, ifMatch: etag(c.Version), body: map[string]any{"done_on": today().String(), "document_id": cert.ID}})
	}).json(t, &c)
	next := today().AddMonthsClamped(12).String()
	if c.LastDoneOn == nil || *c.LastDoneOn != today().String() || c.NextDueOn == nil || *c.NextDueOn != next || len(c.Visits) != 1 || c.Visits[0].DocumentID == nil {
		t.Errorf("after the visit %+v", c)
	}
	if r := call(t, "POST", "/maintenance-contracts/"+c.ID+"/visits", req{tok: tok, ifMatch: etag(1), body: map[string]any{"done_on": today().String()}}); r.Status != 412 {
		t.Errorf("stale version: %d %s", r.Status, r.Body)
	}
	if items := due(); len(items) != 0 {
		t.Errorf("the next visit is a year away, beyond the 90 days: %+v", items)
	}
	var list []contractView
	call(t, "GET", "/maintenance-contracts?asset_id="+a.ID, req{tok: tok}).json(t, &list)
	if len(list) != 1 || list[0].NextDueOn == nil || *list[0].NextDueOn != next {
		t.Errorf("list %+v", list)
	}
	if r := call(t, "GET", "/maintenance-contracts/"+c.ID, req{tok: tokX}); r.Status != 404 {
		t.Errorf("another owner's contract: %d", r.Status)
	}
	if n := superCount(t, "SELECT count(*) FROM audit_events WHERE owner_id = $1 AND action = 'maintenance.visit'", owner.ID); n != 1 {
		t.Errorf("visits audited: %d", n)
	}

	rlsZeroRows(t, owner.ID, other.ID, subContracts, subContractsX, a.ID, map[string]string{"maintenance_contracts": "owner_id = $1", "maintenance_visits": "owner_id = $1"})
}
