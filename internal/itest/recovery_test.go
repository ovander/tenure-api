//go:build integration

package itest

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

const (
	subRecovery    = "9966" // owner of the unpaid-rent recovery scenario (RENT-SPEC-011, part 13a)
	subRecoveryX   = "9967"
	subRecoveryAcc = "9968"
)

type recoveryView struct {
	Since *string `json:"since"`
	Steps []struct {
		Kind             string `json:"kind"`
		OutstandingCents int64  `json:"outstanding_cents"`
	} `json:"steps"`
	Next []struct {
		Kind        string  `json:"kind"`
		DueOn       string  `json:"due_on"`
		GuaranteeID *string `json:"guarantee_id"`
	} `json:"next"`
	ResolutionOn *string `json:"resolution_on"`
	Sureties     []struct {
		GuaranteeID string `json:"guarantee_id"`
		Name        string `json:"name"`
	} `json:"sureties"`
	Arrears struct {
		OverdueCents int64 `json:"overdue_cents"`
	} `json:"arrears"`
}

func nextKinds(v recoveryView) map[string]bool {
	out := map[string]bool{}
	for _, n := range v.Next {
		out[n.Kind] = true
	}
	return out
}

// TestRecovery escalates a real unpaid rent call (RENT-SPEC-011, Q-121): the
// recovery lists the formal notice and the surety's information as due; the
// MD letter is prepared, then the step recorded on it; the IC letter for the
// surety; the commandement de payer sets the clause résolutoire's day and
// calls for its notice to the surety; a step without its letter, before the
// episode or with an unknown surety is refused; the accountant reads but
// does not write; another owner reads nothing.
func TestRecovery(t *testing.T) {
	tok := token(t, subRecovery)
	var owner, ten, a, l, g idv
	call(t, "PUT", "/me", req{tok: tok, body: map[string]any{"legal_name": "Bailleur Impayé", "address": "3 rue I, Lyon"}}).json(t, &owner)
	call(t, "POST", "/tenants", req{tok: tok, body: map[string]any{"names": []string{"Inès Exemple"}, "email": "ines@example.test"}}).json(t, &ten)
	call(t, "POST", "/assets", req{tok: tok, body: map[string]any{"kind": "apartment", "address": "3 rue I, Lyon"}}).json(t, &a)
	r := call(t, "POST", "/leases", req{tok: tok, body: map[string]any{"kind": "unfurnished", "asset_ids": []string{a.ID},
		"tenants": []map[string]any{{"tenant_id": ten.ID}}, "start_on": "2025-01-01", "term_months": 36, "rent_cents": 70000,
		"provision_cents": 5000, "due_day": 5, "irl_ref_quarter": "2024-Q3", "irl_ref_value": 14451, "revision_month": 9}})
	if r.Status != 201 {
		t.Fatalf("lease: %d %s", r.Status, r.Body)
	}
	r.json(t, &l)
	call(t, "POST", "/leases/"+l.ID+"/guarantees", req{tok: tok, body: map[string]any{"kind": "surety", "amount_cents": 1000000, "status": "valid",
		"guarantor": map[string]any{"name": "Gilles Exemple", "address": "5 rue Fictive, Lyon"}}}).json(t, &g)
	superExec(t, `UPDATE leases SET state = 'active' WHERE id = $1`, l.ID)
	per := today().AddMonthsClamped(-2)
	if r := call(t, "POST", "/billing/runs", req{tok: tok, body: map[string]any{"period": per.String()[:7]}}); r.Status != 201 {
		t.Fatalf("billing run: %d %s", r.Status, r.Body)
	}
	path := "/leases/" + l.ID + "/recovery"
	var v recoveryView
	call(t, "GET", path, req{tok: tok}).json(t, &v)
	if v.Since == nil || v.Arrears.OverdueCents != 75000 || !nextKinds(v)["formal_notice"] || !nextKinds(v)["surety_notice"] || len(v.Sureties) != 1 || v.Sureties[0].GuaranteeID != g.ID {
		t.Fatalf("recovery %+v", v)
	}

	if r := call(t, "POST", path+"/steps", req{tok: tok, body: map[string]any{"kind": "formal_notice", "done_on": today().String()}}); r.Status != 400 && r.Status != 422 {
		t.Errorf("formal notice without its letter: %d %s", r.Status, r.Body)
	}
	var md struct {
		Number           string `json:"number"`
		DocumentID       string `json:"document_id"`
		OutstandingCents int64  `json:"outstanding_cents"`
	}
	audited(t, seen, "POST /leases/{id}/recovery/letter", 200, func() resp {
		return call(t, "POST", path+"/letter", req{tok: tok, body: map[string]any{"kind": "formal_notice"}})
	}).json(t, &md)
	if len(md.Number) < 3 || md.Number[:3] != "MD-" || md.OutstandingCents != 75000 {
		t.Fatalf("formal notice letter %+v", md)
	}
	var class string
	if err := env.super.QueryRow(`SELECT class FROM documents WHERE id = $1`, md.DocumentID).Scan(&class); err != nil || class != "recovery" {
		t.Errorf("letter class %q %v", class, err)
	}
	since, err := time.Parse(time.DateOnly, *v.Since)
	if err != nil {
		t.Fatal(err)
	}
	if r := call(t, "POST", path+"/steps", req{tok: tok, body: map[string]any{"kind": "formal_notice", "done_on": since.AddDate(0, 0, -1).Format(time.DateOnly),
		"document_id": md.DocumentID}}); r.Status != 422 {
		t.Errorf("a step before the episode: %d %s", r.Status, r.Body)
	}
	audited(t, seen, "POST /leases/{id}/recovery/steps", 200, func() resp {
		return call(t, "POST", path+"/steps", req{tok: tok, body: map[string]any{"kind": "formal_notice", "done_on": today().AddDays(-20).String(),
			"document_id": md.DocumentID, "delivery": "registered_letter", "tracking_ref": "1A 000 000 0000 2"}})
	}).json(t, &v)
	if nextKinds(v)["formal_notice"] || !nextKinds(v)["commandement"] {
		t.Errorf("after the formal notice %+v", v.Next)
	}

	if r := call(t, "POST", path+"/letter", req{tok: tok, body: map[string]any{"kind": "surety_notice", "guarantee_id": uuid.NewString()}}); r.Status != 422 {
		t.Errorf("unknown surety: %d %s", r.Status, r.Body)
	}
	var ic struct {
		Number     string `json:"number"`
		DocumentID string `json:"document_id"`
	}
	audited(t, seen, "POST /leases/{id}/recovery/letter", 200, func() resp {
		return call(t, "POST", path+"/letter", req{tok: tok, body: map[string]any{"kind": "surety_notice", "guarantee_id": g.ID}})
	}).json(t, &ic)
	if len(ic.Number) < 3 || ic.Number[:3] != "IC-" {
		t.Fatalf("surety letter %+v", ic)
	}
	if r := call(t, "POST", path+"/steps", req{tok: tok, body: map[string]any{"kind": "surety_notice", "done_on": today().String(),
		"guarantee_id": g.ID, "document_id": md.DocumentID}}); r.Status != 422 {
		t.Errorf("surety step on the formal notice: %d %s", r.Status, r.Body)
	}
	audited(t, seen, "POST /leases/{id}/recovery/steps", 200, func() resp {
		return call(t, "POST", path+"/steps", req{tok: tok, body: map[string]any{"kind": "surety_notice", "done_on": today().String(),
			"guarantee_id": g.ID, "document_id": ic.DocumentID, "delivery": "registered_letter"}})
	})
	audited(t, seen, "POST /leases/{id}/recovery/steps", 200, func() resp {
		return call(t, "POST", path+"/steps", req{tok: tok, body: map[string]any{"kind": "commandement", "done_on": today().String(), "delivery": "bailiff"}})
	}).json(t, &v)
	if v.ResolutionOn == nil || *v.ResolutionOn != today().AddDays(42).String() || !nextKinds(v)["surety_commandement"] || !nextKinds(v)["court"] || len(v.Steps) != 3 {
		t.Errorf("after the commandement %+v", v)
	}
	if r := call(t, "POST", path+"/steps", req{tok: tok, body: map[string]any{"kind": "court", "done_on": today().AddDays(1).String()}}); r.Status != 422 {
		t.Errorf("a step in the future: %d %s", r.Status, r.Body)
	}

	now := time.Now().UTC()
	superExec(t, `INSERT INTO owner_grants (id, owner_id, grantee_sub, role, granted_at, created_by, updated_at, updated_by)
		VALUES ($1, $2, $3, 'accountant', $4, 'itest', $4, 'itest')`, uuid.Must(uuid.NewV7()), owner.ID, subRecoveryAcc, now)
	if r := call(t, "GET", path, req{tok: token(t, subRecoveryAcc), scope: owner.ID}); r.Status != 200 {
		t.Errorf("accountant reads: %d", r.Status)
	}
	if r := call(t, "POST", path+"/letter", req{tok: token(t, subRecoveryAcc), scope: owner.ID, body: map[string]any{"kind": "formal_notice"}}); r.Status != 403 {
		t.Errorf("accountant writes: %d", r.Status)
	}
	var other idv
	call(t, "PUT", "/me", req{tok: token(t, subRecoveryX), body: map[string]any{"legal_name": "Bailleur X", "address": "1 rue X"}}).json(t, &other)
	if r := call(t, "GET", path, req{tok: token(t, subRecoveryX)}); r.Status != 404 {
		t.Errorf("another owner reads: %d", r.Status)
	}
	rlsZeroRows(t, owner.ID, other.ID, subRecovery, subRecoveryX, a.ID, map[string]string{"recovery_steps": "owner_id = $1"})
}
