//go:build integration

package itest

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

const (
	subNotice    = "9957" // owner of the notice scenario (RENT-SPEC-010, part 12a)
	subNoticeX   = "9958"
	subNoticeAcc = "9959"
	subNoticeCo  = "9960"
)

type noticeResult struct {
	Terms struct {
		Months          int      `json:"months"`
		EndOn           string   `json:"end_on"`
		LatestReceiptOn string   `json:"latest_receipt_on"`
		Reduced         bool     `json:"reduced"`
		Warnings        []string `json:"warnings"`
	} `json:"terms"`
	Notice *struct {
		ID         string `json:"id"`
		GivenBy    string `json:"given_by"`
		EndOn      string `json:"end_on"`
		DocumentID string `json:"document_id"`
	} `json:"notice"`
	Lease *struct {
		State string `json:"state"`
		EndOn string `json:"end_on"`
	} `json:"lease"`
	Letter *struct {
		Number     string `json:"number"`
		Kind       string `json:"kind"`
		DocumentID string `json:"document_id"`
	} `json:"letter"`
}

// TestNotice gives notice on two real leases (RENT-SPEC-010, Q-118): the
// tenant's reduced notice, its letter uploaded, computes the end and puts the
// lease under notice, once; the owner's letter on a repossession is refused
// without the protected-tenant answer or to a protected tenant, then issued
// as a CG document for the end of the term, and its reception recorded; a
// reception in the future and an owner notice without its letter are
// refused; the accountant does not read a notice, the co-owner does; another
// owner reads nothing.
func TestNotice(t *testing.T) {
	tok := token(t, subNotice)
	var owner, ten, a1, a2, l1, l2 idv
	call(t, "PUT", "/me", req{tok: tok, body: map[string]any{"legal_name": "Bailleur Congé", "address": "4 rue N, Lyon"}}).json(t, &owner)
	call(t, "POST", "/tenants", req{tok: tok, body: map[string]any{"names": []string{"Nora Petit"}, "email": "nora@example.test"}}).json(t, &ten)
	call(t, "POST", "/assets", req{tok: tok, body: map[string]any{"kind": "apartment", "address": "4 rue N, Lyon"}}).json(t, &a1)
	call(t, "POST", "/assets", req{tok: tok, body: map[string]any{"kind": "apartment", "address": "6 rue N, Lyon"}}).json(t, &a2)
	// Lease 1's term ends about eight months from now: an owner's notice can
	// still reach it; lease 2 is the tenant's.
	start1 := today().AddMonthsClamped(-28)
	for i, x := range []struct {
		asset string
		into  *idv
		start string
	}{{a1.ID, &l1, start1.String()}, {a2.ID, &l2, today().AddMonthsClamped(-5).String()}} {
		r := call(t, "POST", "/leases", req{tok: tok, body: map[string]any{"kind": "unfurnished", "asset_ids": []string{x.asset},
			"tenants": []map[string]any{{"tenant_id": ten.ID}}, "start_on": x.start, "term_months": 36, "rent_cents": 70000,
			"provision_cents": 5000, "due_day": 5, "irl_ref_quarter": "2024-Q3", "irl_ref_value": 14451, "revision_month": 9}})
		if r.Status != 201 {
			t.Fatalf("lease %d: %d %s", i, r.Status, r.Body)
		}
		r.json(t, x.into)
		superExec(t, `UPDATE leases SET state = 'active' WHERE id = $1`, x.into.ID)
	}

	t.Run("tenant notice", func(t *testing.T) {
		path := "/leases/" + l2.ID + "/notice"
		var dry noticeResult
		r := call(t, "POST", path, req{tok: tok, body: map[string]any{"given_by": "tenant", "reason": "job_loss",
			"received_on": today().String(), "delivery": "registered_letter", "dry_run": true}})
		if r.Status != 200 {
			t.Fatalf("dry run: %d %s", r.Status, r.Body)
		}
		r.json(t, &dry)
		want := today().AddMonthsClamped(1)
		if want.Day == today().Day {
			want = want.AddDays(-1)
		}
		if dry.Terms.Months != 1 || !dry.Terms.Reduced || dry.Terms.EndOn != want.String() || dry.Notice != nil {
			t.Fatalf("dry run %+v", dry)
		}
		if r := call(t, "POST", path, req{tok: tok, body: map[string]any{"given_by": "tenant", "received_on": today().AddDays(1).String(),
			"delivery": "hand_delivery"}}); r.Status != 422 {
			t.Errorf("reception in the future: %d %s", r.Status, r.Body)
		}
		var letter idv
		up := upload(t, tok, "", map[string]string{"class": "notice", "lease_id": l2.ID}, "conge-locataire.pdf", signedPDF)
		if up.Status != 201 {
			t.Fatalf("upload: %d %s", up.Status, up.Body)
		}
		up.json(t, &letter)
		var got noticeResult
		audited(t, seen, "POST /leases/{id}/notice", 200, func() resp {
			return call(t, "POST", path, req{tok: tok, body: map[string]any{"given_by": "tenant", "reason": "job_loss", "received_on": today().String(),
				"delivery": "registered_letter", "sent_on": today().AddDays(-2).String(), "tracking_ref": "1A 234 567 8901 2", "document_id": letter.ID}})
		}).json(t, &got)
		if got.Lease == nil || got.Lease.State != "notice" || got.Lease.EndOn != want.String() || got.Notice == nil || got.Notice.DocumentID != letter.ID {
			t.Errorf("notice %+v", got)
		}
		audited(t, seen, "POST /leases/{id}/notice", 409, func() resp {
			return call(t, "POST", path, req{tok: tok, body: map[string]any{"given_by": "tenant", "received_on": today().String(), "delivery": "hand_delivery"}})
		})
		if r := call(t, "GET", path, req{tok: tok}); r.Status != 200 {
			t.Errorf("read notice: %d", r.Status)
		}
	})

	t.Run("owner notice", func(t *testing.T) {
		path := "/leases/" + l1.ID + "/notice"
		ground := map[string]any{"reason": "repossession", "beneficiary": map[string]any{"name": "Claire Exemple",
			"relationship": "descendant", "address": "3 rue Fictive, Lyon"}}
		if r := call(t, "POST", path+"/letter", req{tok: tok, body: ground}); r.Status != 422 {
			t.Errorf("letter without the protected-tenant answer: %d %s", r.Status, r.Body)
		}
		ground["protected_tenant"] = "yes"
		if r := call(t, "POST", path+"/letter", req{tok: tok, body: ground}); r.Status != 422 {
			t.Errorf("letter to a protected tenant: %d %s", r.Status, r.Body)
		}
		ground["protected_tenant"] = "no"
		var letter noticeResult
		audited(t, seen, "POST /leases/{id}/notice/letter", 200, func() resp {
			return call(t, "POST", path+"/letter", req{tok: tok, body: ground})
		}).json(t, &letter)
		termEnd := start1.AddMonthsClamped(36).AddDays(-1)
		if letter.Letter == nil || letter.Letter.Kind != "owner_notice" || len(letter.Letter.Number) < 3 || letter.Letter.Number[:3] != "CG-" ||
			letter.Terms.EndOn != termEnd.String() || letter.Terms.Months != 6 {
			t.Fatalf("letter %+v", letter)
		}
		var class string
		if err := env.super.QueryRow(`SELECT class FROM documents WHERE id = $1`, letter.Letter.DocumentID).Scan(&class); err != nil || class != "notice" {
			t.Errorf("letter class %q %v", class, err)
		}
		if r := call(t, "POST", path, req{tok: tok, body: map[string]any{"given_by": "owner", "received_on": today().String(),
			"delivery": "bailiff"}}); r.Status != 422 {
			t.Errorf("owner notice without its letter: %d %s", r.Status, r.Body)
		}
		var got noticeResult
		audited(t, seen, "POST /leases/{id}/notice", 200, func() resp {
			return call(t, "POST", path, req{tok: tok, body: map[string]any{"given_by": "owner", "received_on": today().String(),
				"delivery": "bailiff", "document_id": letter.Letter.DocumentID}})
		}).json(t, &got)
		if got.Lease == nil || got.Lease.State != "notice" || got.Lease.EndOn != termEnd.String() || got.Notice.GivenBy != "owner" {
			t.Errorf("owner notice %+v", got)
		}
		var n struct {
			Ground struct {
				Reason      string `json:"reason"`
				Beneficiary struct {
					Name string `json:"name"`
				} `json:"beneficiary"`
			} `json:"ground"`
		}
		call(t, "GET", path, req{tok: tok}).json(t, &n)
		if n.Ground.Reason != "repossession" || n.Ground.Beneficiary.Name != "Claire Exemple" {
			t.Errorf("recorded ground %+v", n)
		}
	})

	t.Run("roles and isolation", func(t *testing.T) {
		now := time.Now().UTC()
		for sub, role := range map[string]string{subNoticeAcc: "accountant", subNoticeCo: "co_owner"} {
			superExec(t, `INSERT INTO owner_grants (id, owner_id, grantee_sub, role, granted_at, created_by, updated_at, updated_by)
				VALUES ($1, $2, $3, $4, $5, 'itest', $5, 'itest')`, uuid.Must(uuid.NewV7()), owner.ID, sub, role, now)
		}
		path := "/leases/" + l1.ID + "/notice"
		if r := call(t, "GET", path, req{tok: token(t, subNoticeAcc), scope: owner.ID}); r.Status != 403 {
			t.Errorf("accountant reads the notice: %d", r.Status)
		}
		if r := call(t, "GET", path, req{tok: token(t, subNoticeCo), scope: owner.ID}); r.Status != 200 {
			t.Errorf("co-owner cannot read the notice: %d", r.Status)
		}
		if r := call(t, "POST", "/leases/"+l2.ID+"/notice/letter", req{tok: token(t, subNoticeCo), scope: owner.ID,
			body: map[string]any{"reason": "legitimate", "grounds": "x", "protected_tenant": "no"}}); r.Status != 403 {
			t.Errorf("co-owner prepares a letter: %d", r.Status)
		}
		var other idv
		call(t, "PUT", "/me", req{tok: token(t, subNoticeX), body: map[string]any{"legal_name": "Bailleur X", "address": "1 rue X"}}).json(t, &other)
		if r := call(t, "GET", path, req{tok: token(t, subNoticeX)}); r.Status != 404 {
			t.Errorf("another owner reads the notice: %d", r.Status)
		}
		rlsZeroRows(t, owner.ID, other.ID, subNotice, subNoticeX, a1.ID, map[string]string{"lease_notices": "owner_id = $1"})
	})
}
