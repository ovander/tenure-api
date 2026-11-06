//go:build integration

package itest

import (
	"strings"
	"testing"
	"time"
)

const subExit = "9951" // owner of the exit scenario (RENT-INV-001 slice 8)

type exitView struct {
	captureView
	EntryInventoryID *string `json:"entry_inventory_id"`
	KeysReturnedOn   *string `json:"keys_returned_on"`
	TenantNewAddress string  `json:"tenant_new_address"`
	Keys             []struct {
		Kind        string `json:"kind"`
		Destination string `json:"destination"`
		Quantity    int    `json:"quantity"`
	} `json:"keys"`
	Kind string `json:"kind"`
}

// TestExitInventory runs an exit captured from its issued entry (RENT-INV-001
// §5, §6, slice 8): refused without an issued entry; pre-filled with the
// entry's items, keys and meters, each item to check; completed once each
// item is looked at and the return of the keys and the new address are
// recorded; compared with the entry; signed, then issued into an ES
// document.
func TestExitInventory(t *testing.T) {
	tok := token(t, subExit)
	var owner, ten idv
	call(t, "PUT", "/me", req{tok: tok, body: map[string]any{"legal_name": "Bailleur Exit", "address": "4 rue E, Lyon"}}).json(t, &owner)
	call(t, "POST", "/tenants", req{tok: tok, body: map[string]any{"names": []string{"Léa Martin"}, "email": "lea@example.test"}}).json(t, &ten)
	lease, entry := completedEntry(t, tok, ten.ID, "4 rue E, Lyon")
	notice := func(l string) {
		t.Helper()
		superExec(t, `UPDATE leases SET state = 'notice', end_on = current_date + 30 WHERE id = $1`, l)
	}

	// Before the entry is issued, there is no exit to capture.
	notice(lease)
	if r := call(t, "POST", "/leases/"+lease+"/inventories", req{tok: tok, body: map[string]any{"kind": "exit", "mode": "amicable"}}); r.problemType(t) != "inventory/exit-without-entry" {
		t.Fatalf("an exit without an issued entry: %d %s", r.Status, r.Body)
	}
	var scan docView
	upload(t, tok, "", map[string]string{"class": "inventory", "lease_id": lease}, "edl-entree-signe.pdf", signedPDF).json(t, &scan)
	if r := call(t, "POST", "/inventories/"+entry+"/signatures", req{tok: tok, body: map[string]any{"method": "wet_scan", "document_id": scan.ID,
		"signers": []map[string]any{{"party": "owner", "signer_name": "Bailleur Exit"}, {"party": "tenant", "signer_name": "Léa Martin"}}}}); r.Status != 200 {
		t.Fatalf("sign the entry: %d %s", r.Status, r.Body)
	}
	if r := call(t, "GET", "/inventories/"+entry+"/comparison", req{tok: tok}); r.problemType(t) != "inventory/comparison-unavailable" {
		t.Errorf("comparison of an entry: %d %s", r.Status, r.Body)
	}

	var created struct {
		ID string `json:"id"`
	}
	audited(t, seen, "POST /leases/{id}/inventories", 201, func() resp {
		return call(t, "POST", "/leases/"+lease+"/inventories", req{tok: tok, body: map[string]any{"kind": "exit", "mode": "amicable"}})
	}).json(t, &created)
	path := "/inventories/" + created.ID
	var exit, entryView exitView
	call(t, "GET", path, req{tok: tok}).json(t, &exit)
	call(t, "GET", "/inventories/"+entry, req{tok: tok}).json(t, &entryView)
	if exit.Kind != "exit" || exit.EntryInventoryID == nil || *exit.EntryInventoryID != entry {
		t.Fatalf("exit %+v", exit.captureView)
	}
	n, ne := 0, 0
	for _, r := range exit.Rooms {
		n += len(r.Items)
	}
	for _, r := range entryView.Rooms {
		ne += len(r.Items)
	}
	if n != ne || n == 0 {
		t.Fatalf("%d exit items for %d entry items", n, ne)
	}
	if superCount(t, `SELECT count(*) FROM inventory_items WHERE inventory_id = $1 AND to_check AND entry_item_id IS NOT NULL AND condition = 'good'`, created.ID) != n {
		t.Error("every exit item pre-filled with its entry condition, linked and to check")
	}
	if len(exit.Keys) != 1 || exit.Keys[0].Quantity != 2 {
		t.Errorf("keys copied %+v", exit.Keys)
	}

	move := func(action string, extra map[string]any) resp {
		body := map[string]any{"action": action}
		for k, v := range extra {
			body[k] = v
		}
		return call(t, "POST", path+"/transitions", req{tok: tok, body: body})
	}
	move("schedule", map[string]any{"scheduled_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339), "place": "Sur place"})
	move("start", nil)
	call(t, "GET", path, req{tok: tok}).json(t, &exit)
	codes := map[string]int{}
	for _, c := range exit.Checks.Blocking {
		codes[c.Code]++
	}
	if codes["exit-item-to-check"] != n || codes["exit-new-address-missing"] != 1 || codes["exit-keys-returned-missing"] != 1 {
		t.Errorf("checks before the exit is captured %v", codes)
	}
	if r := move("complete", nil); r.problemType(t) != "inventory/mandatory-mention-missing" {
		t.Errorf("complete with items to check: %d %s", r.Status, r.Body)
	}

	// On site: the first item is worse, the others as at entry; one key is missing.
	first := exit.Rooms[0].Items[0]
	for i, room := range exit.Rooms {
		for j, it := range room.Items {
			body := map[string]any{"condition": "good"}
			if i == 0 && j == 0 {
				body = map[string]any{"condition": "poor", "comment": "Tache de 20 cm"}
			}
			if r := call(t, "PUT", path+"/items/"+it.ID, req{tok: tok, ifMatch: etag(it.Version), body: body}); r.Status != 200 {
				t.Fatalf("assess: %d %s", r.Status, r.Body)
			}
		}
	}
	call(t, "GET", path, req{tok: tok}).json(t, &exit)
	if r := call(t, "PUT", path+"/keys", req{tok: tok, ifMatch: etag(exit.Version), body: map[string]any{"keys": []map[string]any{
		{"kind": "key", "destination": "porte d’entrée", "quantity": 1}}}}); r.Status != 200 {
		t.Fatalf("keys: %d %s", r.Status, r.Body)
	}
	call(t, "GET", path, req{tok: tok}).json(t, &exit)
	future := time.Now().AddDate(0, 0, 3).Format(time.DateOnly)
	if r := call(t, "PUT", path, req{tok: tok, ifMatch: etag(exit.Version), body: map[string]any{"keys_returned_on": future}}); r.Status != 422 {
		t.Errorf("keys returned in the future: %d", r.Status)
	}
	audited(t, seen, "PUT /inventories/{id}", 200, func() resp {
		return call(t, "PUT", path, req{tok: tok, ifMatch: etag(exit.Version), body: map[string]any{"place": "Sur place",
			"done_on": today().String(), "keys_returned_on": today().String(), "tenant_new_address": "8 avenue Neuve, Grenoble"}})
	}).json(t, &exit)
	if exit.KeysReturnedOn == nil || exit.TenantNewAddress != "8 avenue Neuve, Grenoble" {
		t.Errorf("header %+v", exit)
	}

	var cmp struct {
		Flagged int `json:"flagged"`
		Items   []struct {
			ExitItemID string `json:"exit_item_id"`
			Result     string `json:"result"`
			Steps      int    `json:"steps"`
			Entry      *struct {
				Condition string `json:"condition"`
			} `json:"entry"`
		} `json:"items"`
		Keys []struct {
			Result string `json:"result"`
			Entry  int    `json:"entry"`
			Exit   int    `json:"exit"`
		} `json:"keys"`
		EntryDoneOn string `json:"entry_done_on"`
	}
	call(t, "GET", path+"/comparison", req{tok: tok}).json(t, &cmp)
	if len(cmp.Items) != n || cmp.Items[0].ExitItemID != first.ID || cmp.Items[0].Result != "worse" || cmp.Items[0].Steps != 2 ||
		cmp.Items[0].Entry == nil || cmp.Items[0].Entry.Condition != "good" || cmp.Items[1].Result != "same" {
		t.Errorf("items %+v", cmp.Items[:2])
	}
	if len(cmp.Keys) != 1 || cmp.Keys[0].Result != "missing" || cmp.Keys[0].Entry != 2 || cmp.Keys[0].Exit != 1 || cmp.Flagged != 2 || cmp.EntryDoneOn == "" {
		t.Errorf("keys %+v, flagged %d", cmp.Keys, cmp.Flagged)
	}

	if r := move("complete", nil); r.Status != 200 {
		t.Fatalf("complete: %d %s", r.Status, r.Body)
	}
	var exitScan docView
	upload(t, tok, "", map[string]string{"class": "inventory", "lease_id": lease}, "edl-sortie-signe.pdf", signedPDF).json(t, &exitScan)
	var sv sigView
	call(t, "POST", path+"/signatures", req{tok: tok, body: map[string]any{"method": "wet_scan", "document_id": exitScan.ID,
		"signers": []map[string]any{{"party": "owner", "signer_name": "Bailleur Exit"}, {"party": "tenant", "signer_name": "Léa Martin"}}}}).json(t, &sv)
	if sv.State != "issued" || sv.DocumentID == nil {
		t.Fatalf("exit issued: %+v", sv)
	}
	var key, tmpl string
	if err := env.super.QueryRow(`SELECT object_key, template_version FROM documents WHERE id = $1`, *sv.DocumentID).Scan(&key, &tmpl); err != nil ||
		!strings.Contains(key, "/ES-") || tmpl != "inventory/v3" {
		t.Errorf("ES document %s %s %v", key, tmpl, err)
	}
	// The comparison still reads once issued.
	if r := call(t, "GET", path+"/comparison", req{tok: tok}); r.Status != 200 {
		t.Errorf("comparison of the issued exit: %d", r.Status)
	}
}
