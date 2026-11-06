//go:build integration

package itest

import (
	"strings"
	"testing"
)

const subCompletion = "9952" // owner of the completion scenario (RENT-INV-001 slice 7)

type completionsView struct {
	DoneOn  string `json:"done_on"`
	Windows *struct {
		TenDayEndsOn    string  `json:"ten_day_ends_on"`
		HeatingStartsOn *string `json:"heating_starts_on"`
	} `json:"windows"`
	Open        []string         `json:"open"`
	Completions []completionView `json:"completions"`
}

type completionView struct {
	ID         string  `json:"id"`
	Decision   string  `json:"decision"`
	DecidedOn  *string `json:"decided_on"`
	DocumentID *string `json:"document_id"`
	Version    int     `json:"version"`
	Lines      []struct {
		EntryItemID string `json:"entry_item_id"`
		Label       string `json:"label"`
		Entry       struct {
			Condition string `json:"condition"`
		} `json:"entry"`
		Requested struct {
			Condition string `json:"condition"`
			Comment   string `json:"comment"`
		} `json:"requested"`
		Accepted *bool `json:"accepted"`
	} `json:"lines"`
}

// TestEntryCompletions runs a tenant's request to complete an issued entry
// (loi 89-462 art. 3-2, RENT-INV-001 slice 7): the windows; refused before
// the entry is issued, outside its window, or on an unknown item; recorded
// and shown as a lease warning; decided line by line into an EC document;
// frozen once decided; the accepted line carried into the exit pre-fill.
func TestEntryCompletions(t *testing.T) {
	tok := token(t, subCompletion)
	var owner, ten idv
	call(t, "PUT", "/me", req{tok: tok, body: map[string]any{"legal_name": "Bailleur Complément", "address": "6 rue C, Lyon"}}).json(t, &owner)
	call(t, "POST", "/tenants", req{tok: tok, body: map[string]any{"names": []string{"Léa Martin"}, "email": "lea@example.test"}}).json(t, &ten)
	lease, entry := completedEntry(t, tok, ten.ID, "6 rue C, Lyon")
	path := "/inventories/" + entry + "/completions"
	var inv captureView
	call(t, "GET", "/inventories/"+entry, req{tok: tok}).json(t, &inv)
	first, second := inv.Rooms[0].Items[0], inv.Rooms[0].Items[1]
	ask := func(reason, on string, lines ...map[string]any) map[string]any {
		return map[string]any{"reason": reason, "requested_on": on, "lines": lines}
	}
	tenDay := ask("ten_day", today().String(), map[string]any{"entry_item_id": first.ID, "condition": "fair", "comment": "Rayure près de la fenêtre"},
		map[string]any{"entry_item_id": second.ID, "condition": "poor"})

	if r := call(t, "POST", path, req{tok: tok, body: tenDay}); r.problemType(t) != "inventory/completion-unavailable" {
		t.Errorf("completion of a completed, unissued entry: %d %s", r.Status, r.Body)
	}
	var scan docView
	upload(t, tok, "", map[string]string{"class": "inventory", "lease_id": lease}, "edl-entree-signe.pdf", signedPDF).json(t, &scan)
	if r := call(t, "POST", "/inventories/"+entry+"/signatures", req{tok: tok, body: map[string]any{"method": "wet_scan", "document_id": scan.ID,
		"signers": []map[string]any{{"party": "owner", "signer_name": "Bailleur Complément"}, {"party": "tenant", "signer_name": "Léa Martin"}}}}); r.Status != 200 {
		t.Fatalf("sign the entry: %d %s", r.Status, r.Body)
	}

	var cv completionsView
	call(t, "GET", path, req{tok: tok}).json(t, &cv)
	if cv.Windows == nil || cv.Windows.TenDayEndsOn <= cv.DoneOn || len(cv.Open) == 0 || cv.Open[0] != "ten_day" || len(cv.Completions) != 0 {
		t.Fatalf("windows %+v", cv)
	}
	for name, c := range map[string]struct {
		body map[string]any
		want string
	}{
		"before the entry":  {ask("ten_day", today().AddDays(-1).String(), map[string]any{"entry_item_id": first.ID, "condition": "fair"}), "inventory/completion-window-closed"},
		"in the future":     {ask("ten_day", today().AddDays(1).String(), map[string]any{"entry_item_id": first.ID, "condition": "fair"}), "validation/invalid"},
		"unknown item":      {ask("ten_day", today().String(), map[string]any{"entry_item_id": lease, "condition": "fair"}), "validation/invalid"},
		"nothing asked":     {ask("ten_day", today().String(), map[string]any{"entry_item_id": first.ID}), "validation/invalid"},
		"no line":           {ask("ten_day", today().String()), "validation/invalid"},
		"letter of nothing": {map[string]any{"reason": "ten_day", "requested_on": today().String(), "request_document_id": lease, "lines": []map[string]any{{"entry_item_id": first.ID, "condition": "fair"}}}, "inventory/completion-document-invalid"},
	} {
		if r := call(t, "POST", path, req{tok: tok, body: c.body}); !strings.HasSuffix(r.problemType(t), c.want) {
			t.Errorf("%s: %d %s", name, r.Status, r.Body)
		}
	}
	// The heating window is the first month of the season, only for an entry
	// done outside it: either closed today, or open for heating items only.
	if r := call(t, "POST", path, req{tok: tok, body: ask("heating", today().String(), map[string]any{"entry_item_id": first.ID, "condition": "fair"})}); r.Status != 409 && r.Status != 422 {
		t.Errorf("heating request on a floor: %d %s", r.Status, r.Body)
	}

	var c completionView
	audited(t, seen, "POST /inventories/{id}/completions", 201, func() resp {
		return call(t, "POST", path, req{tok: tok, body: tenDay})
	}).json(t, &c)
	if c.Decision != "pending" || len(c.Lines) != 2 || c.Lines[0].Entry.Condition != "good" || c.Lines[0].Requested.Comment != "Rayure près de la fenêtre" || c.Lines[0].Accepted != nil {
		t.Fatalf("request %+v", c)
	}
	var b struct {
		Warnings []struct {
			Type  string `json:"type"`
			Title string `json:"title"`
		} `json:"warnings"`
	}
	call(t, "GET", "/leases/"+lease+"/blockers", req{tok: tok}).json(t, &b)
	if len(b.Warnings) != 1 || b.Warnings[0].Type != "inventory/completion-pending" || b.Warnings[0].Title == "" {
		t.Errorf("warnings %+v", b.Warnings)
	}

	cpath := "/completions/" + c.ID
	decide := func(v int, lines ...map[string]any) resp {
		return call(t, "PUT", cpath, req{tok: tok, ifMatch: etag(v), body: map[string]any{"lines": lines}})
	}
	yes := map[string]any{"entry_item_id": first.ID, "accepted": true}
	no := map[string]any{"entry_item_id": second.ID, "accepted": false}
	if r := decide(c.Version, yes); r.Status != 422 {
		t.Errorf("a line left undecided: %d %s", r.Status, r.Body)
	}
	if r := decide(c.Version+1, yes, no); r.Status != 412 {
		t.Errorf("stale decision: %d", r.Status)
	}
	audited(t, seen, "PUT /completions/{id}", 200, func() resp { return decide(c.Version, yes, no) }).json(t, &c)
	if c.Decision != "partial" || c.DocumentID == nil || c.DecidedOn == nil || c.Lines[0].Accepted == nil || !*c.Lines[0].Accepted || *c.Lines[1].Accepted {
		t.Fatalf("decision %+v", c)
	}
	var key, tmpl string
	if err := env.super.QueryRow(`SELECT object_key, template_version FROM documents WHERE id = $1`, *c.DocumentID).Scan(&key, &tmpl); err != nil ||
		!strings.Contains(key, "/EC-") || tmpl != "inventory_completion/v1" {
		t.Errorf("EC document %s %s %v", key, tmpl, err)
	}
	if r := decide(c.Version, yes, no); r.problemType(t) != "inventory/completion-decided" {
		t.Errorf("decided twice: %d %s", r.Status, r.Body)
	}
	if _, err := env.super.Exec(`UPDATE inventory_completions SET lines = '[]' WHERE id = $1`, c.ID); err == nil {
		t.Error("a decided request was changed")
	}
	if _, err := env.super.Exec(`DELETE FROM inventory_completions WHERE id = $1`, c.ID); err == nil {
		t.Error("a request was deleted")
	}
	call(t, "GET", "/leases/"+lease+"/blockers", req{tok: tok}).json(t, &b)
	if len(b.Warnings) != 0 {
		t.Errorf("warnings once decided %+v", b.Warnings)
	}

	// The exit starts from the entry corrected by the accepted line only.
	superExec(t, `UPDATE leases SET state = 'notice', end_on = current_date + 30 WHERE id = $1`, lease)
	var exit idv
	call(t, "POST", "/leases/"+lease+"/inventories", req{tok: tok, body: map[string]any{"kind": "exit", "mode": "amicable"}}).json(t, &exit)
	cond := func(entryItem string) string {
		var s string
		if err := env.super.QueryRow(`SELECT condition FROM inventory_items WHERE inventory_id = $1 AND entry_item_id = $2`, exit.ID, entryItem).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	if cond(first.ID) != "fair" || cond(second.ID) != "good" {
		t.Errorf("exit pre-fill %s %s", cond(first.ID), cond(second.ID))
	}
	if r := call(t, "GET", "/inventories/"+exit.ID+"/completions", req{tok: tok}); r.problemType(t) != "inventory/completion-unavailable" {
		t.Errorf("completions of an exit: %d %s", r.Status, r.Body)
	}
}
