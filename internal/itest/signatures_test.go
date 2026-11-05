//go:build integration

package itest

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// reset zeroes v before a response is decoded into it: fields a response
// omits (a link no longer shown) must not keep the previous value.
func reset(v *sigView) *sigView {
	*v = sigView{}
	return v
}

const (
	subSig   = "9901" // owner of the signature scenario (RENT-INV-001 slice 6)
	subSigCo = "9902" // their co-owner
	subSigX  = "9903" // another owner
)

type sigView struct {
	State          string  `json:"state"`
	ESignAvailable bool    `json:"esign_available"`
	DocumentID     *string `json:"document_id"`
	Parties        []struct {
		Party  string `json:"party"`
		Name   string `json:"name"`
		Signed bool   `json:"signed"`
	} `json:"parties"`
	Signatures []struct {
		Party              string  `json:"party"`
		SignerName         string  `json:"signer_name"`
		Method             string  `json:"method"`
		Reservations       string  `json:"reservations"`
		EvidenceDocumentID *string `json:"evidence_document_id"`
	} `json:"signatures"`
	Request *struct {
		ID      string `json:"id"`
		State   string `json:"state"`
		Signers []struct {
			Party      string `json:"party"`
			SignerName string `json:"signer_name"`
			Link       string `json:"link"`
			Signed     bool   `json:"signed"`
		} `json:"signers"`
	} `json:"request"`
}

// completedEntry creates a captured entry on a new lease of a seeded asset
// and takes it to completed through the API; it returns the lease and the
// inventory.
func completedEntry(t *testing.T, tok, tenant, address string) (string, string) {
	t.Helper()
	var asset, l idv
	call(t, "POST", "/assets", req{tok: tok, body: map[string]any{"kind": "apartment", "address": address}}).json(t, &asset)
	if r := call(t, "POST", "/assets/"+asset.ID+"/layout/seed", req{tok: tok, body: map[string]any{"bedrooms": 0}}); r.Status != 201 {
		t.Fatalf("seed: %d %s", r.Status, r.Body)
	}
	call(t, "POST", "/leases", req{tok: tok, body: map[string]any{"kind": "unfurnished", "asset_ids": []string{asset.ID},
		"tenants": []map[string]any{{"tenant_id": tenant}}, "start_on": "2025-01-01", "term_months": 36, "rent_cents": 70000,
		"provision_cents": 5000, "due_day": 5, "irl_ref_quarter": "2024-Q3", "irl_ref_value": 14451, "revision_month": 9}}).json(t, &l)
	var inv captureView
	call(t, "POST", "/leases/"+l.ID+"/inventories", req{tok: tok, body: map[string]any{"kind": "entry", "mode": "amicable"}}).json(t, &inv)
	path := "/inventories/" + inv.ID
	move := func(action string, extra map[string]any) {
		t.Helper()
		body := map[string]any{"action": action}
		for k, v := range extra {
			body[k] = v
		}
		if r := call(t, "POST", path+"/transitions", req{tok: tok, body: body}); r.Status != 200 {
			t.Fatalf("%s: %d %s", action, r.Status, r.Body)
		}
	}
	move("schedule", map[string]any{"scheduled_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339), "place": "Sur place"})
	move("start", nil)
	call(t, "GET", path, req{tok: tok}).json(t, &inv)
	for _, room := range inv.Rooms {
		for _, it := range room.Items {
			if r := call(t, "PUT", path+"/items/"+it.ID, req{tok: tok, ifMatch: etag(it.Version), body: map[string]any{"condition": "good"}}); r.Status != 200 {
				t.Fatalf("assess: %d %s", r.Status, r.Body)
			}
		}
	}
	call(t, "GET", path, req{tok: tok}).json(t, &inv)
	if r := call(t, "PUT", path+"/keys", req{tok: tok, ifMatch: etag(inv.Version), body: map[string]any{"keys": []map[string]any{
		{"kind": "key", "destination": "porte d’entrée", "quantity": 2}}}}); r.Status != 200 {
		t.Fatalf("keys: %d %s", r.Status, r.Body)
	}
	move("complete", nil)
	return l.ID, inv.ID
}

// TestInventorySignatures signs entry inventories (RENT-INV-001 slice 6,
// OD-08, INV-OD-03): a hand-signed copy recorded party by party, then the
// EE issued once the last has signed; and an electronic request through the
// provider, cancelled, refused, then signed on the tablet party by party,
// its signed file archived as evidence.
func TestInventorySignatures(t *testing.T) {
	tok := token(t, subSig)
	var owner, ten idv
	call(t, "PUT", "/me", req{tok: tok, body: map[string]any{"legal_name": "Bailleur Sig", "address": "9 rue S, Lyon"}}).json(t, &owner)
	call(t, "POST", "/tenants", req{tok: tok, body: map[string]any{"names": []string{"Léa Martin", "Paul Durand"}, "email": "colocation@example.test"}}).json(t, &ten)

	t.Run("a hand-signed copy", func(t *testing.T) {
		lease, inv := completedEntry(t, tok, ten.ID, "9 rue S, Lyon")
		path := "/inventories/" + inv
		r := call(t, "GET", path+"/preview", req{tok: tok})
		if r.Status != 200 || r.Header.Get("Content-Type") != "application/pdf" || !bytes.HasPrefix(r.Body, []byte("%PDF-")) {
			t.Fatalf("preview: %d %s", r.Status, r.Header.Get("Content-Type"))
		}
		var v sigView
		call(t, "GET", path+"/signatures", req{tok: tok}).json(t, reset(&v))
		if len(v.Parties) != 3 || v.Parties[1].Name != "Léa Martin" || v.Parties[2].Name != "Paul Durand" || !v.ESignAvailable {
			t.Fatalf("one party per co-tenant: %+v", v.Parties)
		}
		var scan, other docView
		upload(t, tok, "", map[string]string{"class": "inventory", "lease_id": lease}, "edl-signe.pdf", signedPDF).json(t, &scan)
		upload(t, tok, "", map[string]string{"class": "other"}, "x.pdf", signedPDF).json(t, &other)
		sign := func(doc string, signers ...map[string]any) resp {
			return call(t, "POST", path+"/signatures", req{tok: tok, body: map[string]any{"method": "wet_scan", "document_id": doc, "signers": signers}})
		}
		if r := sign(other.ID, map[string]any{"party": "owner", "signer_name": "Bailleur Sig"}); r.problemType(t) != "inventory/signature-document-invalid" {
			t.Errorf("a document of another class: %d %s", r.Status, r.Body)
		}
		if r := sign(scan.ID, map[string]any{"party": "tenant", "signer_name": "Jean Inconnu"}); r.Status != 422 {
			t.Errorf("someone who is not a party: %d %s", r.Status, r.Body)
		}
		audited(t, seen, "POST /inventories/{id}/signatures", 200, func() resp {
			return sign(scan.ID, map[string]any{"party": "owner", "signer_name": "Bailleur Sig"})
		}).json(t, reset(&v))
		if v.State != "completed" || len(v.Signatures) != 1 || v.Signatures[0].EvidenceDocumentID == nil || *v.Signatures[0].EvidenceDocumentID != scan.ID {
			t.Fatalf("one signature: %+v", v)
		}
		if r := sign(scan.ID, map[string]any{"party": "owner", "signer_name": "Bailleur Sig"}); r.Status != 422 {
			t.Errorf("the owner twice: %d", r.Status)
		}
		if r := call(t, "POST", path+"/transitions", req{tok: tok, body: map[string]any{"action": "reopen"}}); r.problemType(t) != "inventory/signature-started" {
			t.Errorf("reopen once signed: %d %s", r.Status, r.Body)
		}
		audited(t, seen, "POST /inventories/{id}/signatures", 200, func() resp {
			return sign(scan.ID, map[string]any{"party": "tenant", "signer_name": "Léa Martin", "reservations": "Rayure sur le parquet non relevée"},
				map[string]any{"party": "tenant", "signer_name": "Paul Durand"})
		}).json(t, reset(&v))
		if v.State != "issued" || v.DocumentID == nil || len(v.Signatures) != 3 {
			t.Fatalf("issued once all signed: %+v", v)
		}
		if n := superCount(t, `SELECT count(*) FROM audit_events WHERE entity_id = $1 AND action = 'inventory.transition' AND after->>'action' IN ('sign', 'issue')`, inv); n != 2 {
			t.Errorf("%d sign and issue events", n)
		}
		var key string
		if err := env.super.QueryRow(`SELECT object_key FROM documents WHERE id = $1`, *v.DocumentID).Scan(&key); err != nil || !strings.Contains(key, "/EE-") {
			t.Errorf("EE document %s %v", key, err)
		}
		if r := call(t, "GET", path+"/preview", req{tok: tok}); r.problemType(t) != "inventory/not-signable" {
			t.Errorf("preview once issued: %d", r.Status)
		}
	})

	t.Run("an electronic request, signed on the tablet", func(t *testing.T) {
		_, inv := completedEntry(t, tok, ten.ID, "11 rue S, Lyon")
		path := "/inventories/" + inv
		all := []map[string]any{{"party": "owner", "signer_name": "Bailleur Sig", "email": "bailleur@example.test"},
			{"party": "tenant", "signer_name": "Léa Martin", "email": "lea@example.test", "reservations": "Volet grippé"},
			{"party": "tenant", "signer_name": "Paul Durand", "email": "paul@example.test"}}
		request := func(signers []map[string]any) resp {
			return call(t, "POST", path+"/signatures", req{tok: tok, body: map[string]any{"method": "esign", "signers": signers}})
		}
		if r := request(all[:2]); r.Status != 422 {
			t.Errorf("a request for some parties only: %d %s", r.Status, r.Body)
		}
		if r := request([]map[string]any{all[0], all[1], {"party": "tenant", "signer_name": "Paul Durand"}}); r.Status != 422 {
			t.Errorf("a signer without e-mail: %d %s", r.Status, r.Body)
		}
		var v sigView
		// A first request, cancelled; a second, declined at the provider.
		audited(t, seen, "POST /inventories/{id}/signatures", 200, func() resp { return request(all) }).json(t, reset(&v))
		if v.Request == nil || v.Request.State != "pending" || len(v.Request.Signers) != 3 || v.Request.Signers[0].Link == "" {
			t.Fatalf("request %+v", v.Request)
		}
		if r := request(all); r.problemType(t) != "inventory/signature-pending" {
			t.Errorf("a second request while one is pending: %d", r.Status)
		}
		if r := call(t, "POST", path+"/transitions", req{tok: tok, body: map[string]any{"action": "reopen"}}); r.problemType(t) != "inventory/signature-pending" {
			t.Errorf("reopen while pending: %d %s", r.Status, r.Body)
		}
		audited(t, seen, "POST /inventories/{id}/signatures/cancel", 200, func() resp {
			return call(t, "POST", path+"/signatures/cancel", req{tok: tok})
		}).json(t, reset(&v))
		if v.Request.State != "cancelled" || v.Request.Signers[0].Link != "" {
			t.Errorf("cancelled %+v", v.Request)
		}
		request(all).json(t, reset(&v))
		providerID := func() string {
			var id string
			if err := env.super.QueryRow(`SELECT provider_request_id FROM inventory_signature_requests WHERE id = $1`, v.Request.ID).Scan(&id); err != nil {
				t.Fatal(err)
			}
			return id
		}
		env.esign.Decline(providerID())
		call(t, "POST", path+"/signatures/refresh", req{tok: tok}).json(t, reset(&v))
		if v.Request.State != "refused" || v.State != "completed" {
			t.Errorf("declined at the provider %+v", v.Request)
		}

		// The third request is signed party by party on the tablet.
		request(all).json(t, reset(&v))
		pid := providerID()
		var sent []byte
		for _, rq := range env.esign.Requests() {
			if rq.ID == pid {
				sent = rq.Document
				if rq.DeliveryMode != "none" || !rq.ParseAnchors || len(rq.Signers) != 3 || rq.Signers[1].Email != "lea@example.test" ||
					rq.Signers[1].FirstName != "Léa" || rq.Signers[1].LastName != "Martin" {
					t.Errorf("at the provider %+v", rq)
				}
			}
		}
		if !bytes.HasPrefix(sent, []byte("%PDF-")) {
			t.Fatal("the provider got no PDF")
		}
		before := superCount(t, "SELECT count(*) FROM audit_events")
		call(t, "POST", path+"/signatures/refresh", req{tok: tok}).json(t, reset(&v))
		if superCount(t, "SELECT count(*) FROM audit_events") != before {
			t.Error("a refresh with nothing new wrote an event")
		}
		signerIDs := func() []string {
			rows, err := env.super.Query(`SELECT s->>'signer_id' FROM inventory_signature_requests, jsonb_array_elements(signers) s WHERE id = $1`, v.Request.ID)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = rows.Close() }()
			var out []string
			for rows.Next() {
				var s string
				_ = rows.Scan(&s)
				out = append(out, s)
			}
			return out
		}
		ids := signerIDs()
		env.esign.Sign(ids[0])
		audited(t, seen, "POST /inventories/{id}/signatures/refresh", 200, func() resp {
			return call(t, "POST", path+"/signatures/refresh", req{tok: tok})
		}).json(t, reset(&v))
		if !v.Request.Signers[0].Signed || v.Request.Signers[0].Link != "" || v.Request.Signers[1].Link == "" || len(v.Signatures) != 0 {
			t.Errorf("after the owner signed %+v", v.Request)
		}
		// The co-owner reads the request without its links.
		superExec(t, `INSERT INTO owner_grants (id, owner_id, grantee_sub, role, granted_at, created_by, updated_at, updated_by)
			VALUES ($1, $2, $3, 'co_owner', now(), 'itest', now(), 'itest')`, uuid.Must(uuid.NewV7()), owner.ID, subSigCo)
		var co sigView
		call(t, "GET", path+"/signatures", req{tok: token(t, subSigCo), scope: owner.ID}).json(t, &co)
		if co.Request == nil || co.Request.Signers[1].Link != "" {
			t.Errorf("the co-owner sees a signing link: %+v", co.Request)
		}
		if r := call(t, "POST", path+"/signatures/refresh", req{tok: token(t, subSigCo), scope: owner.ID}); r.Status != 403 {
			t.Errorf("the co-owner refreshes: %d", r.Status)
		}
		env.esign.Sign(ids[1])
		env.esign.Sign(ids[2])
		audited(t, seen, "POST /inventories/{id}/signatures/refresh", 200, func() resp {
			return call(t, "POST", path+"/signatures/refresh", req{tok: tok})
		}).json(t, reset(&v))
		if v.State != "issued" || v.DocumentID == nil || v.Request.State != "done" || len(v.Signatures) != 3 {
			t.Fatalf("issued once all signed: %+v", v)
		}
		evidence := v.Signatures[0].EvidenceDocumentID
		if evidence == nil || v.Signatures[1].Method != "esign" || v.Signatures[1].Reservations != "Volet grippé" {
			t.Fatalf("signatures %+v", v.Signatures)
		}
		var class, lease string
		var retain *time.Time
		if err := env.super.QueryRow(`SELECT class, lease_id, retain_until FROM documents WHERE id = $1`, *evidence).Scan(&class, &lease, &retain); err != nil ||
			class != "inventory" || retain != nil {
			t.Errorf("evidence %s %s %v %v", class, lease, retain, err)
		}

		var other, asset idv
		call(t, "PUT", "/me", req{tok: token(t, subSigX), body: map[string]any{"legal_name": "Bailleur X", "address": "1 rue X"}}).json(t, &other)
		if err := env.super.QueryRow(`SELECT asset_id FROM lease_assets WHERE lease_id = $1 LIMIT 1`, lease).Scan(&asset.ID); err != nil {
			t.Fatal(err)
		}
		rlsZeroRows(t, owner.ID, other.ID, subSig, subSigX, asset.ID, map[string]string{"inventory_signature_requests": "owner_id = $1",
			"inventory_signatures": "owner_id = $1"})
	})
}
