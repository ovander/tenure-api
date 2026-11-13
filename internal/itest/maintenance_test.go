//go:build integration

package itest

import (
	"testing"
	"time"

	"github.com/ovander/tenure-api/internal/ledger"
	"github.com/ovander/tenure-api/internal/maintenance"
	"github.com/ovander/tenure-api/internal/tax"
)

const (
	subWorks  = "9979" // owner of the maintenance scenario (RENT-SPEC-015, part 17a)
	subWorksX = "9980"
)

type incidentView struct {
	ID        string  `json:"id"`
	Status    string  `json:"status"`
	ChargeID  *string `json:"charge_id"`
	Treatment *string `json:"treatment"`
	Version   int     `json:"version"`
}

// TestMaintenance records a contractor, reports two incidents with a photo,
// takes the first through quote, schedule, done and a repair invoice — a
// repairs charge, on line 224 of the tax summary — and the second to an
// improvement invoice, capitalised in the journal and deducted nowhere; a
// third is cancelled. Wrong transitions, stale versions and another owner's
// contractor are refused, and another owner reads none of the rows (Q-130).
func TestMaintenance(t *testing.T) {
	tok, tokX := token(t, subWorks), token(t, subWorksX)
	var owner, other, a idv
	call(t, "PUT", "/me", req{tok: tok, body: map[string]any{"legal_name": "Bailleur Travaux", "address": "6 rue T, Lyon"}}).json(t, &owner)
	call(t, "PUT", "/me", req{tok: tokX, body: map[string]any{"legal_name": "Bailleur Y", "address": "7 rue Y"}}).json(t, &other)
	call(t, "POST", "/assets", req{tok: tok, body: map[string]any{"kind": "apartment", "address": "6 rue T, Lyon"}}).json(t, &a)

	var c, cx struct {
		ID      string `json:"id"`
		Version int    `json:"version"`
	}
	audited(t, seen, "POST /contractors", 201, func() resp {
		return call(t, "POST", "/contractors", req{tok: tok, body: map[string]any{"name": "Plomberie Exemple", "trade": "plombier", "email": "contact@plomberie.example"}})
	}).json(t, &c)
	audited(t, seen, "PUT /contractors/{id}", 200, func() resp {
		return call(t, "PUT", "/contractors/"+c.ID, req{tok: tok, ifMatch: etag(c.Version), body: map[string]any{"name": "Plomberie Exemple", "trade": "plombier", "phone": "04 00 00 00 00"}})
	}).json(t, &c)
	if r := call(t, "POST", "/contractors", req{tok: tok, body: map[string]any{"name": " ", "email": "pas-une-adresse"}}); r.Status != 422 {
		t.Errorf("an empty contractor: %d %s", r.Status, r.Body)
	}
	call(t, "POST", "/contractors", req{tok: tokX, body: map[string]any{"name": "Artisan Y"}}).json(t, &cx)

	var photo docView
	upload(t, tok, "", map[string]string{"class": "supporting", "asset_id": a.ID}, "fuite.pdf", signedPDF).json(t, &photo)
	report := map[string]any{"asset_id": a.ID, "reported_on": "2025-03-01", "reported_by": "tenant", "channel": "email",
		"reporter_name": "Locataire Exemple", "title": "Fuite sous l’évier", "urgency": "urgent", "photo_document_ids": []string{photo.ID}}
	var i incidentView
	audited(t, seen, "POST /incidents", 201, func() resp {
		return call(t, "POST", "/incidents", req{tok: tok, body: report})
	}).json(t, &i)
	if i.Status != "new" {
		t.Fatalf("incident %+v", i)
	}
	if r := call(t, "POST", "/incidents", req{tok: tokX, body: report}); r.Status != 422 {
		t.Errorf("another owner's asset: %d %s", r.Status, r.Body)
	}
	report["description"] = "L’eau coule sous l’évier de la cuisine."
	audited(t, seen, "PUT /incidents/{id}", 200, func() resp {
		return call(t, "PUT", "/incidents/"+i.ID, req{tok: tok, ifMatch: etag(i.Version), body: report})
	}).json(t, &i)

	move := func(inc *incidentView, want int, body map[string]any) resp {
		t.Helper()
		r := audited(t, seen, "POST /incidents/{id}/transitions", want, func() resp {
			return call(t, "POST", "/incidents/"+inc.ID+"/transitions", req{tok: tok, ifMatch: etag(inc.Version), body: body})
		})
		if r.Status < 300 {
			r.json(t, inc)
		}
		return r
	}
	if r := move(&i, 409, map[string]any{"action": "invoice", "invoice_cents": 1000, "treatment": "repair"}); r.problemType(t) != maintenance.TypeTransition {
		t.Errorf("invoice from new: %s", r.Body)
	}
	if r := move(&i, 422, map[string]any{"action": "quote", "contractor_id": cx.ID, "quote_cents": 42000}); r.problemType(t) != maintenance.TypeContractor {
		t.Errorf("another owner's contractor: %s", r.Body)
	}
	move(&i, 200, map[string]any{"action": "quote", "contractor_id": c.ID, "quote_cents": 42000})
	if r := call(t, "POST", "/incidents/"+i.ID+"/transitions", req{tok: tok, ifMatch: etag(1), body: map[string]any{"action": "schedule", "scheduled_on": "2025-03-05"}}); r.Status != 412 {
		t.Errorf("stale version: %d %s", r.Status, r.Body)
	}
	move(&i, 200, map[string]any{"action": "schedule", "scheduled_on": "2025-03-05"})
	move(&i, 422, map[string]any{"action": "done"})
	move(&i, 200, map[string]any{"action": "done", "done_on": "2025-03-05"})
	if r := call(t, "PUT", "/incidents/"+i.ID, req{tok: tok, ifMatch: etag(i.Version), body: report}); r.Status != 409 {
		t.Errorf("a done incident's report changed: %d %s", r.Status, r.Body)
	}
	var invoice docView
	upload(t, tok, "", map[string]string{"class": "supporting", "asset_id": a.ID}, "facture-plombier.pdf", signedPDF).json(t, &invoice)
	move(&i, 200, map[string]any{"action": "invoice", "invoice_cents": 42000, "treatment": "repair", "paid_on": "2025-03-20", "invoice_document_id": invoice.ID})
	if i.Status != "invoiced" || i.ChargeID == nil {
		t.Fatalf("repair invoiced %+v", i)
	}
	if n := superCount(t, "SELECT count(*) FROM charges WHERE id = $1 AND category = 'repairs' AND source = 'invoice' AND amount_cents = 42000 AND document_id = $2 AND charged_on = '2025-03-05'", *i.ChargeID, invoice.ID); n != 1 {
		t.Errorf("repairs charge: %d", n)
	}

	var im incidentView
	call(t, "POST", "/incidents", req{tok: tok, body: map[string]any{"asset_id": a.ID, "reported_on": "2025-04-01", "reported_by": "owner",
		"channel": "visit", "title": "Remplacement de la chaudière"}}).json(t, &im)
	move(&im, 200, map[string]any{"action": "schedule", "contractor_id": c.ID, "scheduled_on": "2025-05-10"})
	move(&im, 200, map[string]any{"action": "done", "done_on": "2025-05-12"})
	move(&im, 200, map[string]any{"action": "invoice", "invoice_cents": 650000, "treatment": "improvement", "paid_on": "2025-05-20"})
	if im.ChargeID != nil || im.Treatment == nil || *im.Treatment != "improvement" {
		t.Errorf("improvement %+v", im)
	}

	var gone incidentView
	call(t, "POST", "/incidents", req{tok: tok, body: map[string]any{"asset_id": a.ID, "reported_on": "2025-06-01", "reported_by": "tenant",
		"channel": "phone", "title": "Volet bloqué"}}).json(t, &gone)
	move(&gone, 200, map[string]any{"action": "cancel", "reason": "Réglé par le locataire."})
	move(&gone, 409, map[string]any{"action": "quote", "contractor_id": c.ID, "quote_cents": 100})

	var open, all []incidentView
	call(t, "GET", "/incidents?open=true", req{tok: tok}).json(t, &open)
	call(t, "GET", "/incidents?asset_id="+a.ID, req{tok: tok}).json(t, &all)
	if len(open) != 0 || len(all) != 3 || all[0].ID != gone.ID {
		t.Errorf("open %+v all %+v", open, all)
	}
	if r := call(t, "GET", "/incidents?status=unknown", req{tok: tok}); r.Status != 422 {
		t.Errorf("unknown status: %d", r.Status)
	}
	if r := call(t, "GET", "/incidents/"+i.ID, req{tok: tokX}); r.Status != 404 {
		t.Errorf("another owner's incident: %d", r.Status)
	}

	// The repair is deducted on line 224; the improvement nowhere, but it is
	// in the journal, debited to the fixed-asset account.
	var draft tax.View
	call(t, "POST", "/tax/summaries", req{tok: token(t, subWorks, stepUp(time.Minute)), body: map[string]any{"year": 2025}}).json(t, &draft)
	var on224, deducted int64
	for _, x := range draft.Summary.Owners[0].Totals {
		deducted += int64(x.AmountCents)
		if x.Line == "224" {
			on224 = int64(x.AmountCents)
		}
	}
	if on224 != 42000 {
		t.Errorf("line 224: %d in %+v", on224, draft.Summary.Owners[0].Totals)
	}
	if deducted >= 650000 {
		t.Errorf("the improvement was deducted: %+v", draft.Summary.Owners[0].Totals)
	}
	var j ledger.View
	call(t, "GET", "/ledger?year=2025", req{tok: tok}).json(t, &j)
	capitalised := false
	for _, e := range j.Entries {
		if e.Kind == "improvement" && e.Source == im.ID && len(e.Lines) == 2 && e.Lines[0].Account == "213500" && e.Lines[0].DebitCents == 650000 {
			capitalised = true
		}
	}
	if !capitalised {
		t.Errorf("journal %+v", j.Entries)
	}

	rlsZeroRows(t, owner.ID, other.ID, subWorks, subWorksX, a.ID, map[string]string{"contractors": "owner_id = $1", "incidents": "owner_id = $1"})
}
