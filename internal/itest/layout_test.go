//go:build integration

package itest

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

const (
	subP  = "9601" // owner of the layout scenario (RENT-INV-001 slice 3)
	subQ  = "9602" // another owner
	subPA = "9603" // P's accountant
	subPC = "9604" // P's co-owner
)

type layoutView struct {
	AssetID string `json:"asset_id"`
	Version int    `json:"version"`
	Rooms   []struct {
		ID       string `json:"id"`
		Position int    `json:"position"`
		Kind     string `json:"kind"`
		Label    string `json:"label"`
		Version  int    `json:"version"`
		Elements []struct {
			ID          string `json:"id"`
			Position    int    `json:"position"`
			Category    string `json:"category"`
			Label       string `json:"label"`
			Quantity    int    `json:"quantity"`
			InstalledOn string `json:"installed_on"`
			Version     int    `json:"version"`
		} `json:"elements"`
	} `json:"rooms"`
}

// TestLayouts covers the asset layout routes (RENT-INV-001 §4, §7): seed from
// the system template, edit with If-Match keeping ids, refusals, roles and
// isolation.
func TestLayouts(t *testing.T) {
	tok, tokQ := token(t, subP), token(t, subQ)
	var owner, ownerQ, apt, box idv
	call(t, "PUT", "/me", req{tok: tok, body: map[string]any{"legal_name": "Bailleur P", "address": "2 rue P, Lyon"}}).json(t, &owner)
	call(t, "PUT", "/me", req{tok: tokQ, body: map[string]any{"legal_name": "Bailleur Q", "address": "1 rue Q"}}).json(t, &ownerQ)
	call(t, "POST", "/assets", req{tok: tok, body: map[string]any{"kind": "apartment", "address": "2 rue P, Lyon", "furnished": true}}).json(t, &apt)
	call(t, "POST", "/assets", req{tok: tok, body: map[string]any{"kind": "box", "address": "2 rue P, Lyon", "lot": "Box 3"}}).json(t, &box)
	path := "/assets/" + apt.ID + "/layout"

	var empty layoutView
	if r := call(t, "GET", path, req{tok: tok}); r.Status != 200 || r.Header.Get("ETag") != `W/"0"` {
		t.Fatalf("empty layout: %d %s %s", r.Status, r.Header.Get("ETag"), r.Body)
	} else {
		r.json(t, &empty)
	}
	if len(empty.Rooms) != 0 || empty.Version != 0 {
		t.Errorf("empty layout %+v", empty)
	}

	var seeded layoutView
	t.Run("seed", func(t *testing.T) {
		audited(t, seen, "POST /assets/{id}/layout/seed", 201, func() resp {
			return call(t, "POST", path+"/seed", req{tok: tok, body: map[string]any{"bedrooms": 2, "features": map[string]bool{"cellar": true}}})
		}).json(t, &seeded)
		labels := []string{}
		for _, r := range seeded.Rooms {
			labels = append(labels, r.Label)
		}
		want := []string{"Entrée", "Séjour", "Cuisine", "Chambre 1", "Chambre 2", "Salle d’eau", "WC", "Cellier ou cave"}
		if seeded.Version != 1 || len(labels) != len(want) {
			t.Fatalf("seeded %d rooms, version %d: %v", len(labels), seeded.Version, labels)
		}
		for i := range want {
			if labels[i] != want[i] {
				t.Errorf("room %d: %s, want %s", i+1, labels[i], want[i])
			}
		}
		// The asset is furnished: the kitchen has the furnished hob, not two.
		hob := 0
		for _, e := range seeded.Rooms[2].Elements {
			if e.Label == "Plaques de cuisson" || e.Label == "Plaques" {
				hob++
			}
		}
		if hob != 1 {
			t.Errorf("%d hobs in the furnished kitchen", hob)
		}
		if r := call(t, "POST", path+"/seed", req{tok: tok, body: map[string]any{"bedrooms": 1}}); r.problemType(t) != "inventory/layout-exists" {
			t.Errorf("second seed: %d %s", r.Status, r.Body)
		}
		if r := call(t, "POST", path+"/seed", req{tok: tok, body: map[string]any{"bedrooms": 1, "features": map[string]bool{"pool": true}}}); r.Status != 422 {
			t.Errorf("unknown feature: %d", r.Status)
		}
	})

	t.Run("edit with If-Match, ids kept", func(t *testing.T) {
		kitchen, bed2, cellar := seeded.Rooms[2], seeded.Rooms[4], seeded.Rooms[7]
		// New order: the kitchen first, renamed; the second bedroom removed but
		// its radiator moved to the cellar; a new balcony with one element.
		rooms := []map[string]any{}
		elems := func(r int) []map[string]any {
			out := []map[string]any{}
			for _, e := range seeded.Rooms[r].Elements {
				out = append(out, map[string]any{"id": e.ID, "category": e.Category, "label": e.Label, "quantity": e.Quantity})
			}
			return out
		}
		k := map[string]any{"id": kitchen.ID, "kind": "kitchen", "label": "Cuisine ouverte", "elements": elems(2)}
		rooms = append(rooms, k)
		for i, r := range seeded.Rooms {
			if i == 2 || i == 4 || i == 7 {
				continue
			}
			rooms = append(rooms, map[string]any{"id": r.ID, "kind": r.Kind, "label": r.Label, "elements": elems(i)})
		}
		radiator := bed2.Elements[7]
		cel := elems(7)
		cel = append(cel, map[string]any{"id": radiator.ID, "category": "heating", "label": "Radiateur", "quantity": 0, "installed_on": "2019-10-01"})
		rooms = append(rooms, map[string]any{"id": cellar.ID, "kind": "storage", "label": cellar.Label, "elements": cel},
			map[string]any{"kind": "outside", "label": "Balcon", "elements": []map[string]any{{"category": "floor", "label": "Sol du balcon"}}})
		body := map[string]any{"rooms": rooms}
		if r := call(t, "PUT", path, req{tok: tok, body: body}); r.Status != 428 {
			t.Errorf("without If-Match: %d", r.Status)
		}
		if r := call(t, "PUT", path, req{tok: tok, ifMatch: `W/"7"`, body: body}); r.Status != 412 {
			t.Errorf("stale: %d", r.Status)
		}
		future := time.Now().AddDate(1, 0, 0).Format(time.DateOnly)
		bad := map[string]any{"rooms": []map[string]any{{"kind": "attic", "label": "", "elements": []map[string]any{{"category": "floor", "label": "Sol", "installed_on": future}}}}}
		if r := call(t, "PUT", path, req{tok: tok, ifMatch: `W/"1"`, body: bad}); r.Status != 422 {
			t.Errorf("invalid layout: %d %s", r.Status, r.Body)
		}
		foreign := map[string]any{"rooms": []map[string]any{{"id": uuid.Must(uuid.NewV7()).String(), "kind": "box", "label": "X", "elements": []any{}}}}
		if r := call(t, "PUT", path, req{tok: tok, ifMatch: `W/"1"`, body: foreign}); r.Status != 422 {
			t.Errorf("a room id of no room of this asset: %d %s", r.Status, r.Body)
		}
		var got layoutView
		audited(t, seen, "PUT /assets/{id}/layout", 200, func() resp {
			return call(t, "PUT", path, req{tok: tok, ifMatch: `W/"1"`, body: body})
		}).json(t, &got)
		if got.Version != 2 || len(got.Rooms) != 8 || got.Rooms[0].ID != kitchen.ID || got.Rooms[0].Label != "Cuisine ouverte" ||
			got.Rooms[0].Version != 2 {
			t.Fatalf("after the edit: version %d, %d rooms, first %+v", got.Version, len(got.Rooms), got.Rooms[0])
		}
		last := got.Rooms[7]
		if last.Label != "Balcon" || len(last.Elements) != 1 || last.Elements[0].Version != 1 {
			t.Errorf("new room %+v", last)
		}
		cellarNow := got.Rooms[6]
		moved := cellarNow.Elements[len(cellarNow.Elements)-1]
		if cellarNow.ID != cellar.ID || moved.ID != radiator.ID || moved.InstalledOn != "2019-10-01" || moved.Version != 2 {
			t.Errorf("the radiator kept its id and moved to the cellar: %+v", moved)
		}
		for _, r := range got.Rooms {
			if r.ID == bed2.ID {
				t.Error("the second bedroom is still there")
			}
		}
		if n := superCount(t, `SELECT count(*) FROM asset_elements WHERE room_id = $1`, bed2.ID); n != 0 {
			t.Errorf("%d elements of the removed bedroom left", n)
		}
		// Saving the same layout again changes nothing but the version.
		var again layoutView
		call(t, "PUT", path, req{tok: tok, ifMatch: `W/"2"`, body: map[string]any{"rooms": toInput(got)}}).json(t, &again)
		if again.Version != 3 || again.Rooms[0].Version != 2 {
			t.Errorf("an unchanged save bumped a room: %+v", again.Rooms[0])
		}
	})

	t.Run("roles and isolation", func(t *testing.T) {
		now := time.Now().UTC()
		for sub, role := range map[string]string{subPA: "accountant", subPC: "co_owner"} {
			superExec(t, `INSERT INTO owner_grants (id, owner_id, grantee_sub, role, granted_at, created_by, updated_at, updated_by)
				VALUES ($1, $2, $3, $4, $5, 'itest', $5, 'itest')`, uuid.Must(uuid.NewV7()), owner.ID, sub, role, now)
		}
		acc, co := token(t, subPA), token(t, subPC)
		if r := call(t, "GET", path, req{tok: acc, scope: owner.ID}); r.Status != 403 {
			t.Errorf("accountant reads the layout: %d", r.Status)
		}
		if r := call(t, "GET", path, req{tok: co, scope: owner.ID}); r.Status != 200 {
			t.Errorf("co-owner cannot read the layout: %d", r.Status)
		}
		if r := call(t, "POST", "/assets/"+box.ID+"/layout/seed", req{tok: co, scope: owner.ID, body: map[string]any{}}); r.Status != 403 {
			t.Errorf("co-owner seeds: %d", r.Status)
		}
		if r := call(t, "GET", path, req{tok: tokQ}); r.Status != 404 {
			t.Errorf("Q reads P's layout: %d", r.Status)
		}
		if r := call(t, "POST", "/assets/"+box.ID+"/layout/seed", req{tok: tokQ, body: map[string]any{}}); r.Status != 404 {
			t.Errorf("Q seeds P's box: %d", r.Status)
		}
		var tpls []struct {
			AssetKind string `json:"asset_kind"`
			System    bool   `json:"system"`
		}
		call(t, "GET", "/inventory-templates", req{tok: tokQ}).json(t, &tpls)
		if len(tpls) != 3 || !tpls[0].System {
			t.Errorf("templates %+v", tpls)
		}
		rlsZeroRows(t, owner.ID, ownerQ.ID, subP, subQ, apt.ID, map[string]string{"asset_layouts": "owner_id = $1",
			"asset_rooms": "owner_id = $1", "asset_elements": "owner_id = $1"})
	})
}

// toInput turns a layout view back into a PUT body, unchanged.
func toInput(v layoutView) []map[string]any {
	rooms := []map[string]any{}
	for _, r := range v.Rooms {
		els := []map[string]any{}
		for _, e := range r.Elements {
			m := map[string]any{"id": e.ID, "category": e.Category, "label": e.Label, "quantity": e.Quantity}
			if e.InstalledOn != "" {
				m["installed_on"] = e.InstalledOn
			}
			els = append(els, m)
		}
		rooms = append(rooms, map[string]any{"id": r.ID, "kind": r.Kind, "label": r.Label, "elements": els})
	}
	return rooms
}
