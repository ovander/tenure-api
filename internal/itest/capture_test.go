//go:build integration

package itest

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

const (
	subCap      = "9801" // owner of the capture scenario (RENT-INV-001 slice 5)
	subCapOther = "9802" // another owner
	subCapAcc   = "9803" // T's accountant
	subCapCo    = "9804" // T's co-owner
)

type captureView struct {
	ID         string  `json:"id"`
	State      string  `json:"state"`
	Mode       string  `json:"mode"`
	DoneOn     string  `json:"done_on"`
	DocumentID *string `json:"document_id"`
	Version    int     `json:"version"`
	Rooms      []struct {
		Label string `json:"label"`
		Kind  string `json:"kind"`
		Items []struct {
			ID        string   `json:"id"`
			Category  string   `json:"category"`
			Label     string   `json:"label"`
			Condition string   `json:"condition"`
			Version   int      `json:"version"`
			Photos    []string `json:"photos"`
		} `json:"items"`
	} `json:"rooms"`
	Meters []struct {
		ID string `json:"id"`
	} `json:"meters"`
	Parties []struct {
		Party  string `json:"party"`
		Name   string `json:"name"`
		Signed bool   `json:"signed"`
	} `json:"parties"`
	Checks struct {
		Blocking []struct {
			Code string `json:"code"`
		} `json:"blocking"`
	} `json:"checks"`
}

func etag(v int) string { return fmt.Sprintf(`W/"%d"`, v) }

// TestInventoryCapture runs an entry inventory captured in Tenure (RENT-INV-001
// §5, slice 5): drafted from the layout, scheduled, captured on site,
// completed once its mentions pass, issued once signed into an EE document,
// which lifts the lease's activation blocker; every write audited; edits
// refused out of their stage; roles and isolation.
func TestInventoryCapture(t *testing.T) {
	tok, tokU := token(t, subCap), token(t, subCapOther)
	var owner, ownerU, apt, bare, ten, l1, l2 idv
	call(t, "PUT", "/me", req{tok: tok, body: map[string]any{"legal_name": "Bailleur T", "address": "3 rue T, Lyon"}}).json(t, &owner)
	call(t, "PUT", "/me", req{tok: tokU, body: map[string]any{"legal_name": "Bailleur U", "address": "1 rue U"}}).json(t, &ownerU)
	call(t, "POST", "/assets", req{tok: tok, body: map[string]any{"kind": "apartment", "address": "3 rue T, Lyon"}}).json(t, &apt)
	call(t, "POST", "/assets", req{tok: tok, body: map[string]any{"kind": "apartment", "address": "5 rue T, Lyon"}}).json(t, &bare)
	call(t, "POST", "/tenants", req{tok: tok, body: map[string]any{"names": []string{"Léa Martin"}}}).json(t, &ten)
	if r := call(t, "POST", "/assets/"+apt.ID+"/layout/seed", req{tok: tok, body: map[string]any{"bedrooms": 1}}); r.Status != 201 {
		t.Fatalf("seed: %d %s", r.Status, r.Body)
	}
	newLease := func(assets ...string) idv {
		var l idv
		call(t, "POST", "/leases", req{tok: tok, body: map[string]any{"kind": "unfurnished", "asset_ids": assets,
			"tenants": []map[string]any{{"tenant_id": ten.ID}}, "start_on": "2025-01-01", "term_months": 36, "rent_cents": 70000,
			"provision_cents": 5000, "due_day": 5, "irl_ref_quarter": "2024-Q3", "irl_ref_value": 14451, "revision_month": 9}}).json(t, &l)
		if l.ID == "" {
			t.Fatal("lease not created")
		}
		return l
	}
	l1, l2 = newLease(apt.ID), newLease(bare.ID)

	var inv captureView
	t.Run("create from the layout", func(t *testing.T) {
		if r := call(t, "POST", "/leases/"+l2.ID+"/inventories", req{tok: tok, body: map[string]any{"kind": "entry", "mode": "amicable"}}); r.problemType(t) != "inventory/layout-missing" {
			t.Errorf("an asset without a layout: %d %s", r.Status, r.Body)
		}
		for name, body := range map[string]map[string]any{
			"exit":        {"kind": "exit", "mode": "amicable"},
			"with a date": {"kind": "entry", "mode": "amicable", "done_on": "2025-01-01"},
			"bad mode":    {"kind": "entry", "mode": "tablet"},
		} {
			if r := call(t, "POST", "/leases/"+l1.ID+"/inventories", req{tok: tok, body: body}); r.Status != 422 {
				t.Errorf("%s: %d %s", name, r.Status, r.Body)
			}
		}
		var created struct {
			ID    string `json:"id"`
			State string `json:"state"`
			Mode  string `json:"mode"`
		}
		audited(t, seen, "POST /leases/{id}/inventories", 201, func() resp {
			return call(t, "POST", "/leases/"+l1.ID+"/inventories", req{tok: tok, body: map[string]any{"kind": "entry", "mode": "amicable"}})
		}).json(t, &created)
		if created.State != "draft" || created.Mode != "amicable" {
			t.Fatalf("created %+v", created)
		}
		if r := call(t, "POST", "/leases/"+l1.ID+"/inventories", req{tok: tok, body: map[string]any{"kind": "entry", "mode": "amicable"}}); r.problemType(t) != "inventory/already-recorded" {
			t.Errorf("second entry: %d %s", r.Status, r.Body)
		}
		r := call(t, "GET", "/inventories/"+created.ID, req{tok: tok})
		if r.Status != 200 || r.Header.Get("ETag") != etag(1) {
			t.Fatalf("get: %d %s %s", r.Status, r.Header.Get("ETag"), r.Body)
		}
		r.json(t, &inv)
		var layout layoutView
		call(t, "GET", "/assets/"+apt.ID+"/layout", req{tok: tok}).json(t, &layout)
		if len(inv.Rooms) != len(layout.Rooms) || inv.Rooms[0].Label != layout.Rooms[0].Label || inv.Rooms[0].Kind != layout.Rooms[0].Kind {
			t.Fatalf("rooms %d, layout %d", len(inv.Rooms), len(layout.Rooms))
		}
		if len(inv.Rooms[0].Items) != len(layout.Rooms[0].Elements) || inv.Rooms[0].Items[0].Condition != "" {
			t.Errorf("items of the first room %+v", inv.Rooms[0].Items)
		}
		if len(inv.Parties) != 2 || inv.Parties[0].Name != "Bailleur T" || inv.Parties[1].Name != "Léa Martin" || len(inv.Checks.Blocking) == 0 {
			t.Errorf("parties %+v, %d checks", inv.Parties, len(inv.Checks.Blocking))
		}
		// An inventory changed through the API does not change the layout.
		if superCount(t, `SELECT count(*) FROM inventory_items WHERE inventory_id = $1 AND room_kind IS NULL`, inv.ID) != 0 {
			t.Error("an item without its room's kind")
		}
	})
	path := "/inventories/" + inv.ID
	transition := func(action string, extra map[string]any) resp {
		body := map[string]any{"action": action}
		for k, v := range extra {
			body[k] = v
		}
		return call(t, "POST", path+"/transitions", req{tok: tok, body: body})
	}
	reload := func() {
		t.Helper()
		call(t, "GET", path, req{tok: tok}).json(t, &inv)
	}
	assess := func(item string, version int, body map[string]any) resp {
		return call(t, "PUT", path+"/items/"+item, req{tok: tok, ifMatch: etag(version), body: body})
	}

	t.Run("schedule and start", func(t *testing.T) {
		if r := transition("complete", nil); r.problemType(t) != "inventory/transition-invalid" {
			t.Errorf("complete a draft: %d %s", r.Status, r.Body)
		}
		if r := transition("schedule", nil); r.Status != 422 {
			t.Errorf("schedule without date and place: %d %s", r.Status, r.Body)
		}
		at := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
		audited(t, seen, "POST /inventories/{id}/transitions", 200, func() resp {
			return transition("schedule", map[string]any{"scheduled_at": at, "place": "Sur place, 3 rue T"})
		}).json(t, &inv)
		if inv.State != "scheduled" {
			t.Fatalf("state %s", inv.State)
		}
		it := inv.Rooms[0].Items[0]
		if r := assess(it.ID, it.Version, map[string]any{"condition": "good"}); r.problemType(t) != "inventory/not-editable" {
			t.Errorf("assess a scheduled inventory: %d %s", r.Status, r.Body)
		}
		audited(t, seen, "PUT /inventories/{id}", 200, func() resp {
			return call(t, "PUT", path, req{tok: tok, ifMatch: etag(inv.Version), body: map[string]any{"scheduled_at": at,
				"place": "Sur place, 3 rue T", "observations": "Logement vide, repeint en 2024."}})
		}).json(t, &inv)
		if r := call(t, "PUT", path, req{tok: tok, ifMatch: etag(1), body: map[string]any{}}); r.Status != 412 {
			t.Errorf("stale header: %d", r.Status)
		}
		transition("start", nil).json(t, &inv)
		if inv.State != "in_progress" || inv.DoneOn != today().String() {
			t.Fatalf("started %s on %s", inv.State, inv.DoneOn)
		}
	})

	t.Run("capture on site", func(t *testing.T) {
		first := inv.Rooms[0].Items[0]
		var saved struct {
			Version  int  `json:"version"`
			ToCheck  bool `json:"to_check"`
			Quantity int  `json:"quantity"`
		}
		audited(t, seen, "PUT /inventories/{id}/items/{itemId}", 200, func() resp {
			return assess(first.ID, first.Version, map[string]any{"condition": "poor", "works": "n/a"})
		}).json(t, &saved)
		if saved.Version != first.Version+1 {
			t.Errorf("item version %d", saved.Version)
		}
		if r := assess(first.ID, first.Version, map[string]any{"condition": "good"}); r.Status != 412 {
			t.Errorf("stale item: %d", r.Status)
		}
		if r := assess(first.ID, saved.Version, map[string]any{"condition": "superb"}); r.Status != 422 {
			t.Errorf("off the scale: %d", r.Status)
		}
		var added struct {
			ID      string `json:"id"`
			Version int    `json:"version"`
		}
		audited(t, seen, "POST /inventories/{id}/items", 201, func() resp {
			return call(t, "POST", path+"/items", req{tok: tok, body: map[string]any{"room_label": inv.Rooms[0].Label,
				"category": "furniture", "label": "Étagère murale", "quantity": 1}})
		}).json(t, &added)
		audited(t, seen, "PUT /inventories/{id}/meters", 200, func() resp {
			return call(t, "PUT", path+"/meters", req{tok: tok, ifMatch: etag(inv.Version), body: map[string]any{"meters": []map[string]any{
				{"kind": "water_cold", "meter_number": "A12", "reading": "123,45", "unit": "m³"}, {"kind": "elec_base", "reading": "4567"}}}})
		}).json(t, &inv)
		if r := call(t, "PUT", path+"/meters", req{tok: tok, ifMatch: etag(inv.Version), body: map[string]any{"meters": []map[string]any{
			{"kind": "gas", "reading": "12.3.4"}}}}); r.Status != 422 {
			t.Errorf("a reading that is not a number: %d", r.Status)
		}
		audited(t, seen, "PUT /inventories/{id}/keys", 200, func() resp {
			return call(t, "PUT", path+"/keys", req{tok: tok, ifMatch: etag(inv.Version), body: map[string]any{"keys": []map[string]any{
				{"kind": "key", "destination": "porte d’entrée", "quantity": 2}, {"kind": "badge", "destination": "hall", "quantity": 1}}}})
		}).json(t, &inv)
		// A photo of the meter keeps it from being removed.
		if r := postPhoto(t, tok, "", inv.ID, map[string]string{"meter_id": inv.Meters[0].ID}, exifJPEG(t)); r.Status != 201 {
			t.Fatalf("meter photo: %d %s", r.Status, r.Body)
		}
		if r := call(t, "PUT", path+"/meters", req{tok: tok, ifMatch: etag(inv.Version), body: map[string]any{"meters": []any{}}}); r.problemType(t) != "inventory/meter-has-photos" {
			t.Errorf("removing a photographed meter: %d %s", r.Status, r.Body)
		}
		// Assess everything else as good.
		reload()
		for _, room := range inv.Rooms {
			for _, it := range room.Items {
				if it.ID == first.ID {
					continue
				}
				if r := assess(it.ID, it.Version, map[string]any{"condition": "good", "quantity": 1}); r.Status != 200 {
					t.Fatalf("assess %s: %d %s", it.Label, r.Status, r.Body)
				}
			}
		}
		reload()
		// The poor item has neither comment nor photo: complete is refused.
		var dry struct {
			Allowed bool `json:"allowed"`
			Checks  struct {
				Blocking []struct {
					Code  string `json:"code"`
					Stage string `json:"stage"`
				} `json:"blocking"`
			} `json:"checks"`
		}
		before := superCount(t, "SELECT count(*) FROM audit_events")
		transition("complete", map[string]any{"dry_run": true}).json(t, &dry)
		if dry.Allowed || len(dry.Checks.Blocking) == 0 || superCount(t, "SELECT count(*) FROM audit_events") != before {
			t.Errorf("dry run %+v", dry)
		}
		if r := transition("complete", nil); r.problemType(t) != "inventory/mandatory-mention-missing" {
			t.Errorf("complete with an unexplained damage: %d %s", r.Status, r.Body)
		}
		if r := postPhoto(t, tok, "", inv.ID, map[string]string{"item_id": first.ID}, exifJPEG(t)); r.Status != 201 {
			t.Fatalf("item photo: %d %s", r.Status, r.Body)
		}
		transition("complete", map[string]any{"dry_run": true}).json(t, &dry)
		if !dry.Allowed {
			t.Errorf("still blocked: %+v", dry.Checks)
		}
	})

	t.Run("complete, reopen, complete", func(t *testing.T) {
		transition("complete", nil).json(t, &inv)
		if inv.State != "completed" {
			t.Fatalf("state %s", inv.State)
		}
		it := inv.Rooms[1].Items[0]
		if r := assess(it.ID, it.Version, map[string]any{"condition": "fair"}); r.problemType(t) != "inventory/not-editable" {
			t.Errorf("assess a completed inventory: %d", r.Status)
		}
		if r := postPhoto(t, tok, "", inv.ID, map[string]string{"item_id": it.ID}, exifJPEG(t)); r.problemType(t) != "inventory/not-editable" {
			t.Errorf("photo on a completed inventory: %d %s", r.Status, r.Body)
		}
		if r := transition("issue", nil); r.problemType(t) != "inventory/transition-invalid" {
			t.Errorf("issue unsigned: %d %s", r.Status, r.Body)
		}
		transition("reopen", nil).json(t, &inv)
		if inv.State != "in_progress" {
			t.Errorf("reopened to %s", inv.State)
		}
		transition("complete", nil).json(t, &inv)
	})

	t.Run("issue once signed", func(t *testing.T) {
		// The signatures come with slice 6; they are written here directly.
		sign := func(party, name string) {
			superExec(t, `INSERT INTO inventory_signatures (id, owner_id, inventory_id, party, signer_name, method, signed_at, created_at, created_by)
				VALUES ($1, $2, $3, $4, $5, 'wet_scan', now(), now(), 'itest')`, uuid.Must(uuid.NewV7()), owner.ID, inv.ID, party, name)
		}
		sign("owner", "Bailleur T")
		superExec(t, `UPDATE inventories SET state = 'signed' WHERE id = $1`, inv.ID)
		if r := transition("issue", nil); r.problemType(t) != "inventory/signature-missing" {
			t.Errorf("issue without the tenant's signature: %d %s", r.Status, r.Body)
		}
		sign("tenant", "Léa Martin")
		audited(t, seen, "POST /inventories/{id}/transitions", 200, func() resp { return transition("issue", nil) }).json(t, &inv)
		if inv.State != "issued" || inv.DocumentID == nil {
			t.Fatalf("issued %+v", inv)
		}
		var doc struct {
			Class           string  `json:"class"`
			TemplateVersion string  `json:"template_version"`
			RetainUntil     *string `json:"retain_until"`
		}
		call(t, "GET", "/documents/"+*inv.DocumentID, req{tok: tok}).json(t, &doc)
		if doc.Class != "inventory" || doc.TemplateVersion != "inventory/v1" || doc.RetainUntil != nil {
			t.Errorf("document %+v: class inventory, its clock started at lease end", doc)
		}
		var key string
		if err := env.super.QueryRow(`SELECT object_key FROM documents WHERE id = $1`, *inv.DocumentID).Scan(&key); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(key, "/inventory/") || !strings.Contains(key, "/EE-") {
			t.Errorf("object key %s: an EE number", key)
		}
		if r := call(t, "PUT", path, req{tok: tok, ifMatch: etag(inv.Version), body: map[string]any{}}); r.problemType(t) != "inventory/issued-immutable" {
			t.Errorf("edit an issued inventory: %d %s", r.Status, r.Body)
		}
		var b struct {
			Activation []struct {
				Type string `json:"type"`
			} `json:"activation"`
		}
		call(t, "GET", "/leases/"+l1.ID+"/blockers", req{tok: tok}).json(t, &b)
		for _, x := range b.Activation {
			if x.Type == "lease/entry-inventory-missing" {
				t.Error("the issued entry does not lift the activation blocker")
			}
		}
		var listed []struct {
			State      string  `json:"state"`
			DocumentID *string `json:"document_id"`
			DoneOn     *string `json:"done_on"`
		}
		call(t, "GET", "/leases/"+l1.ID+"/inventories", req{tok: tok}).json(t, &listed)
		if len(listed) != 1 || listed[0].State != "issued" || listed[0].DocumentID == nil || listed[0].DoneOn == nil {
			t.Errorf("listed %+v", listed)
		}
	})

	t.Run("roles and isolation", func(t *testing.T) {
		now := time.Now().UTC()
		for sub, role := range map[string]string{subCapAcc: "accountant", subCapCo: "co_owner"} {
			superExec(t, `INSERT INTO owner_grants (id, owner_id, grantee_sub, role, granted_at, created_by, updated_at, updated_by)
				VALUES ($1, $2, $3, $4, $5, 'itest', $5, 'itest')`, uuid.Must(uuid.NewV7()), owner.ID, sub, role, now)
		}
		acc, co := token(t, subCapAcc), token(t, subCapCo)
		if r := call(t, "GET", path, req{tok: acc, scope: owner.ID}); r.Status != 403 {
			t.Errorf("accountant reads the inventory: %d", r.Status)
		}
		if r := call(t, "GET", path, req{tok: co, scope: owner.ID}); r.Status != 200 {
			t.Errorf("co-owner cannot read the inventory: %d", r.Status)
		}
		if r := call(t, "POST", "/inventories/"+inv.ID+"/transitions", req{tok: co, scope: owner.ID, body: map[string]any{"action": "reopen"}}); r.Status != 403 {
			t.Errorf("co-owner moves the inventory: %d", r.Status)
		}
		if r := call(t, "GET", path, req{tok: tokU}); r.Status != 404 {
			t.Errorf("U reads T's inventory: %d", r.Status)
		}
		if r := call(t, "PUT", path+"/keys", req{tok: tokU, ifMatch: etag(inv.Version), body: map[string]any{"keys": []any{}}}); r.Status != 404 {
			t.Errorf("U edits T's inventory: %d", r.Status)
		}
		rlsZeroRows(t, owner.ID, ownerU.ID, subCap, subCapOther, apt.ID, map[string]string{"inventories": "owner_id = $1",
			"inventory_items": "owner_id = $1", "inventory_meters": "owner_id = $1", "inventory_keys": "owner_id = $1"})
	})
}
