//go:build integration

package itest

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
)

const (
	subRenewal    = "9963" // owner of the rent re-evaluation scenario (RENT-SPEC-010, part 12d)
	subRenewalX   = "9964"
	subRenewalAcc = "9965"
)

type renewalView struct {
	ID           string `json:"id"`
	RenewalOn    string `json:"renewal_on"`
	Status       string `json:"status"`
	DocumentID   string `json:"document_id"`
	StepsApplied int    `json:"steps_applied"`
	StepsDue     int    `json:"steps_due"`
	Version      int    `json:"version"`
	Terms        struct {
		RenewalOn       string `json:"renewal_on"`
		LatestReceiptOn string `json:"latest_receipt_on"`
		Steps           []struct {
			From      string `json:"from"`
			RentCents int64  `json:"rent_cents"`
		} `json:"steps"`
	} `json:"terms"`
}

// TestRenewal proposes a new rent at the renewal of a real unfurnished lease
// (RENT-SPEC-010, loi 89-462 art. 17-2, Q-120): too few references are
// refused; the PR letter is issued for the term about eight months away,
// replaced while not received; its reception is recorded, then the tenant's
// agreement; a step not yet due is refused, a due one adds its share to the
// rent; a received proposal cannot be replaced; the accountant reads but does
// not write; another owner reads nothing.
func TestRenewal(t *testing.T) {
	tok := token(t, subRenewal)
	var owner, ten, a, l idv
	call(t, "PUT", "/me", req{tok: tok, body: map[string]any{"legal_name": "Bailleur Loyer", "address": "8 rue R, Lyon"}}).json(t, &owner)
	call(t, "POST", "/tenants", req{tok: tok, body: map[string]any{"names": []string{"Rémi Exemple"}, "email": "remi@example.test"}}).json(t, &ten)
	call(t, "POST", "/assets", req{tok: tok, body: map[string]any{"kind": "apartment", "address": "8 rue R, Lyon"}}).json(t, &a)
	start := today().AddMonthsClamped(-28)
	r := call(t, "POST", "/leases", req{tok: tok, body: map[string]any{"kind": "unfurnished", "asset_ids": []string{a.ID},
		"tenants": []map[string]any{{"tenant_id": ten.ID}}, "start_on": start.String(), "term_months": 36, "rent_cents": 70000,
		"provision_cents": 5000, "due_day": 5, "irl_ref_quarter": "2024-Q3", "irl_ref_value": 14451, "revision_month": 9}})
	if r.Status != 201 {
		t.Fatalf("lease: %d %s", r.Status, r.Body)
	}
	r.json(t, &l)
	superExec(t, `UPDATE leases SET state = 'active' WHERE id = $1`, l.ID)
	refs := func(n int) []map[string]any {
		out := make([]map[string]any, n)
		for i := range out {
			out[i] = map[string]any{"address": fmt.Sprintf("%d rue Fictive, 69003 Lyon", i+1), "description": "T2, 45 m²", "rent_cents": 80000}
		}
		return out
	}
	letter := "/leases/" + l.ID + "/renewal/letter"
	renewalOn := start.AddMonthsClamped(36)

	if r := call(t, "POST", letter, req{tok: tok, body: map[string]any{"proposed_rent_cents": 74000, "references": refs(2)}}); r.Status != 422 {
		t.Errorf("two references: %d %s", r.Status, r.Body)
	}
	var first, got struct {
		Letter *struct {
			Number     string `json:"number"`
			Kind       string `json:"kind"`
			DocumentID string `json:"document_id"`
		} `json:"letter"`
		Renewal *renewalView `json:"renewal"`
	}
	audited(t, seen, "POST /leases/{id}/renewal/letter", 200, func() resp {
		return call(t, "POST", letter, req{tok: tok, body: map[string]any{"proposed_rent_cents": 73000, "references": refs(3)}})
	}).json(t, &first)
	audited(t, seen, "POST /leases/{id}/renewal/letter", 200, func() resp {
		return call(t, "POST", letter, req{tok: tok, body: map[string]any{"proposed_rent_cents": 73001, "references": refs(3)}})
	}).json(t, &got)
	if got.Letter == nil || got.Letter.Kind != "renewal_proposal" || got.Letter.Number[:3] != "PR-" || got.Renewal == nil ||
		got.Renewal.ID != first.Renewal.ID || got.Renewal.RenewalOn != renewalOn.String() || got.Renewal.Status != "prepared" ||
		len(got.Renewal.Terms.Steps) != 3 || got.Renewal.Terms.Steps[2].RentCents != 73001 || got.Renewal.DocumentID == first.Renewal.DocumentID {
		t.Fatalf("letter %+v / %+v", got.Letter, got.Renewal)
	}
	var class string
	if err := env.super.QueryRow(`SELECT class FROM documents WHERE id = $1`, got.Letter.DocumentID).Scan(&class); err != nil || class != "renewal" {
		t.Errorf("letter class %q %v", class, err)
	}
	base := "/leases/" + l.ID + "/renewals/" + got.Renewal.ID
	if r := call(t, "POST", base+"/answer", req{tok: tok, body: map[string]any{"answer": "accepted", "answered_on": today().String(), "version": got.Renewal.Version}}); r.Status != 409 {
		t.Errorf("answer before reception: %d %s", r.Status, r.Body)
	}
	var rv renewalView
	audited(t, seen, "POST /leases/{id}/renewals/{rid}/reception", 200, func() resp {
		return call(t, "POST", base+"/reception", req{tok: tok, body: map[string]any{"received_on": today().String(), "delivery": "registered_letter",
			"tracking_ref": "1A 000 000 0000 1", "version": got.Renewal.Version}})
	}).json(t, &rv)
	if rv.Status != "received" {
		t.Fatalf("reception %+v", rv)
	}
	if r := call(t, "POST", letter, req{tok: tok, body: map[string]any{"proposed_rent_cents": 73500, "references": refs(3)}}); r.Status != 409 {
		t.Errorf("replace a received proposal: %d %s", r.Status, r.Body)
	}
	audited(t, seen, "POST /leases/{id}/renewals/{rid}/answer", 200, func() resp {
		return call(t, "POST", base+"/answer", req{tok: tok, body: map[string]any{"answer": "accepted", "answered_on": today().String(), "version": rv.Version}})
	}).json(t, &rv)
	if rv.Status != "accepted" || rv.StepsDue != 0 {
		t.Fatalf("answer %+v", rv)
	}
	if r := call(t, "POST", base+"/steps", req{tok: tok, body: map[string]any{"version": rv.Version}}); r.Status != 422 {
		t.Errorf("step not due: %d %s", r.Status, r.Body)
	}
	// The renewal comes: the first step is due. The lease's rent was revised
	// since the letter; the step adds its share on top.
	superExec(t, `UPDATE lease_renewals SET terms = jsonb_set(terms, '{steps,0,from}', to_jsonb($2::text)) WHERE id = $1`, rv.ID, today().String())
	superExec(t, `UPDATE leases SET rent_cents = 71000 WHERE id = $1`, l.ID)
	var steps struct {
		RentCents int64       `json:"rent_cents"`
		Renewal   renewalView `json:"renewal"`
		Lease     *struct {
			RentCents int64 `json:"rent_cents"`
		} `json:"lease"`
	}
	audited(t, seen, "POST /leases/{id}/renewals/{rid}/steps", 200, func() resp {
		return call(t, "POST", base+"/steps", req{tok: tok, body: map[string]any{"version": rv.Version}})
	}).json(t, &steps)
	share := rv.Terms.Steps[0].RentCents - 70000
	if steps.Lease == nil || steps.Lease.RentCents != 71000+share || steps.Renewal.StepsApplied != 1 || steps.Renewal.StepsDue != 0 {
		t.Errorf("steps %+v", steps)
	}

	now := time.Now().UTC()
	superExec(t, `INSERT INTO owner_grants (id, owner_id, grantee_sub, role, granted_at, created_by, updated_at, updated_by)
		VALUES ($1, $2, $3, 'accountant', $4, 'itest', $4, 'itest')`, uuid.Must(uuid.NewV7()), owner.ID, subRenewalAcc, now)
	var list []renewalView
	if r := call(t, "GET", "/leases/"+l.ID+"/renewals", req{tok: token(t, subRenewalAcc), scope: owner.ID}); r.Status != 200 {
		t.Errorf("accountant reads: %d", r.Status)
	} else if r.json(t, &list); len(list) != 1 || list[0].StepsApplied != 1 {
		t.Errorf("list %+v", list)
	}
	if r := call(t, "POST", letter, req{tok: token(t, subRenewalAcc), scope: owner.ID,
		body: map[string]any{"proposed_rent_cents": 73000, "references": refs(3)}}); r.Status != 403 {
		t.Errorf("accountant writes: %d", r.Status)
	}
	var other idv
	call(t, "PUT", "/me", req{tok: token(t, subRenewalX), body: map[string]any{"legal_name": "Bailleur X", "address": "1 rue X"}}).json(t, &other)
	if r := call(t, "GET", "/leases/"+l.ID+"/renewals", req{tok: token(t, subRenewalX)}); r.Status != 404 {
		t.Errorf("another owner reads: %d", r.Status)
	}
	rlsZeroRows(t, owner.ID, other.ID, subRenewal, subRenewalX, a.ID, map[string]string{"lease_renewals": "owner_id = $1"})
}
