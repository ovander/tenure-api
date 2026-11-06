//go:build integration

package itest

import (
	"strings"
	"testing"
	"time"
)

const (
	subDeposit  = "9953" // owner of the deposit scenario (RENT-INV-001 slice 9)
	subDepositX = "9954"
)

type statementView struct {
	Kind            string  `json:"kind"`
	Deadline        string  `json:"deadline"`
	DepositCents    int64   `json:"deposit_cents"`
	DeductionsCents int64   `json:"deductions_cents"`
	ArrearsCents    int64   `json:"arrears_cents"`
	HoldbackCents   int64   `json:"holdback_cents"`
	Regularisation  int64   `json:"regularisation_cents"`
	BalanceCents    int64   `json:"balance_cents"`
	PenaltyCents    int64   `json:"penalty_cents"`
	TotalCents      int64   `json:"total_cents"`
	DocumentID      *string `json:"document_id"`
	Lines           []struct {
		AmountCents int64 `json:"amount_cents"`
	} `json:"lines"`
}

type depositView struct {
	Next    string `json:"next"`
	Flagged []struct {
		ExitItemID string `json:"exit_item_id"`
		Category   string `json:"category"`
		Result     string `json:"result"`
	} `json:"flagged"`
	MissingKeys []struct {
		Kind        string `json:"kind"`
		Destination string `json:"destination"`
	} `json:"missing_keys"`
	CollectiveBuilding bool `json:"collective_building"`
	Grid               *struct {
		ID string `json:"id"`
	} `json:"grid"`
	Deductions []struct {
		ID string `json:"id"`
	} `json:"deductions"`
	Preview    *statementView  `json:"preview"`
	Statements []statementView `json:"statements"`
}

// issuedExit captures and issues an exit on a lease under notice: the
// lease's first item worse, the others as at entry, one key missing, the
// keys returned today and the new address given.
func issuedExit(t *testing.T, tok, owner, lease, entry string) (exit, first, same string) {
	t.Helper()
	var scan docView
	upload(t, tok, "", map[string]string{"class": "inventory", "lease_id": lease}, "edl-entree-signe.pdf", signedPDF).json(t, &scan)
	if r := call(t, "POST", "/inventories/"+entry+"/signatures", req{tok: tok, body: map[string]any{"method": "wet_scan", "document_id": scan.ID,
		"signers": []map[string]any{{"party": "owner", "signer_name": owner}, {"party": "tenant", "signer_name": "Léa Martin"}}}}); r.Status != 200 {
		t.Fatalf("sign the entry: %d %s", r.Status, r.Body)
	}
	superExec(t, `UPDATE leases SET state = 'notice', end_on = current_date + 30 WHERE id = $1`, lease)
	var created idv
	call(t, "POST", "/leases/"+lease+"/inventories", req{tok: tok, body: map[string]any{"kind": "exit", "mode": "amicable"}}).json(t, &created)
	path := "/inventories/" + created.ID
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
	var inv exitView
	call(t, "GET", path, req{tok: tok}).json(t, &inv)
	for i, room := range inv.Rooms {
		for j, it := range room.Items {
			body := map[string]any{"condition": "good"}
			if i == 0 && j == 0 {
				body, first = map[string]any{"condition": "poor", "comment": "Rayures profondes"}, it.ID
			} else if same == "" {
				same = it.ID
			}
			if r := call(t, "PUT", path+"/items/"+it.ID, req{tok: tok, ifMatch: etag(it.Version), body: body}); r.Status != 200 {
				t.Fatalf("assess: %d %s", r.Status, r.Body)
			}
		}
	}
	call(t, "GET", path, req{tok: tok}).json(t, &inv)
	call(t, "PUT", path+"/keys", req{tok: tok, ifMatch: etag(inv.Version), body: map[string]any{"keys": []map[string]any{
		{"kind": "key", "destination": "porte d’entrée", "quantity": 1}}}})
	call(t, "GET", path, req{tok: tok}).json(t, &inv)
	if r := call(t, "PUT", path, req{tok: tok, ifMatch: etag(inv.Version), body: map[string]any{"place": "Sur place", "done_on": today().String(),
		"keys_returned_on": today().String(), "tenant_new_address": "8 avenue Neuve, Grenoble"}}); r.Status != 200 {
		t.Fatalf("header: %d %s", r.Status, r.Body)
	}
	move("complete", nil)
	var exitScan docView
	upload(t, tok, "", map[string]string{"class": "inventory", "lease_id": lease}, "edl-sortie-signe.pdf", signedPDF).json(t, &exitScan)
	var sv sigView
	call(t, "POST", path+"/signatures", req{tok: tok, body: map[string]any{"method": "wet_scan", "document_id": exitScan.ID,
		"signers": []map[string]any{{"party": "owner", "signer_name": owner}, {"party": "tenant", "signer_name": "Léa Martin"}}}}).json(t, &sv)
	if sv.State != "issued" {
		t.Fatalf("exit not issued: %+v", sv)
	}
	return created.ID, first, same
}

// TestDepositSettlement runs the deposit restitution (RENT-INV-001 §6, slice
// 9, Q-115): a wear grid annexed to the lease; deductions only on a flagged
// exit item or a missing key, backed by a quote, the share from the grid;
// the first RD statement with the holdback of a collective building, due two
// months after the keys; the deductions frozen; the holdback released by a
// second RD net of the regularisation.
func TestDepositSettlement(t *testing.T) {
	tok := token(t, subDeposit)
	var owner, ten idv
	call(t, "PUT", "/me", req{tok: tok, body: map[string]any{"legal_name": "Bailleur Dépôt", "address": "7 rue D, Lyon"}}).json(t, &owner)
	call(t, "POST", "/tenants", req{tok: tok, body: map[string]any{"names": []string{"Léa Martin"}, "email": "lea@example.test"}}).json(t, &ten)
	lease, entry := completedEntry(t, tok, ten.ID, "7 rue D, Lyon")
	superExec(t, `UPDATE leases SET deposit_cents = 140000 WHERE id = $1`, lease)
	superExec(t, `UPDATE assets SET collective_building = true WHERE id IN (SELECT asset_id FROM lease_assets WHERE lease_id = $1)`, lease)
	dpath := "/leases/" + lease + "/deposit"

	var dv depositView
	call(t, "GET", dpath, req{tok: tok}).json(t, &dv)
	if dv.Next != "wait_exit" || !dv.CollectiveBuilding {
		t.Fatalf("before the exit %+v", dv)
	}
	_, first, same := issuedExit(t, tok, "Bailleur Dépôt", lease, entry)
	call(t, "GET", dpath, req{tok: tok}).json(t, &dv)
	if dv.Next != "initial" || len(dv.Flagged) != 1 || dv.Flagged[0].ExitItemID != first || len(dv.MissingKeys) != 1 || dv.Preview == nil {
		t.Fatalf("after the exit %+v", dv)
	}

	// The grid, for the flagged item's category, annexed to the lease.
	category := dv.Flagged[0].Category
	grid := map[string]any{"name": "Accord collectif", "source": "Accord collectif de location",
		"lines": []map[string]any{{"category": category, "lifetime_years": 10, "franchise_years": 0, "yearly_rate_bp": 1000, "floor_bp": 2000}}}
	if r := call(t, "POST", "/vetuste-grids", req{tok: tok, body: map[string]any{"name": "x", "source": "y", "lines": []map[string]any{{"category": "floor", "floor_bp": 12000}}}}); r.Status != 422 {
		t.Errorf("a floor above 100 %%: %d", r.Status)
	}
	var g idv
	audited(t, seen, "POST /vetuste-grids", 201, func() resp { return call(t, "POST", "/vetuste-grids", req{tok: tok, body: grid}) }).json(t, &g)
	grid["name"] = "Accord collectif 2026"
	audited(t, seen, "PUT /vetuste-grids/{id}", 200, func() resp {
		return call(t, "PUT", "/vetuste-grids/"+g.ID, req{tok: tok, ifMatch: etag(g.Version), body: grid})
	})
	audited(t, seen, "PUT /leases/{id}/vetuste-grid", 200, func() resp {
		return call(t, "PUT", "/leases/"+lease+"/vetuste-grid", req{tok: tok, body: map[string]any{"vetuste_grid_id": g.ID}})
	}).json(t, &dv)
	if dv.Grid == nil || dv.Grid.ID != g.ID {
		t.Fatalf("grid %+v", dv.Grid)
	}

	var quote, letter docView
	upload(t, tok, "", map[string]string{"class": "supporting", "lease_id": lease}, "devis.pdf", signedPDF).json(t, &quote)
	upload(t, tok, "", map[string]string{"class": "inventory", "lease_id": lease}, "autre.pdf", signedPDF).json(t, &letter)
	ded := func(item string, extra map[string]any) map[string]any {
		b := map[string]any{"description": "Ponçage et vitrification", "cost_cents": 48000, "basis": "quote", "document_id": quote.ID}
		if item != "" {
			b["exit_item_id"] = item
		}
		for k, v := range extra {
			b[k] = v
		}
		return b
	}
	dded := "/leases/" + lease + "/deposit-deductions"
	for name, c := range map[string]struct {
		body map[string]any
		want string
	}{
		"an item as at entry": {ded(same, nil), "inventory/deduction-unjustified"},
		"a key present":       {ded("", map[string]any{"key": map[string]any{"kind": "badge", "destination": "hall"}}), "inventory/deduction-unjustified"},
		"not a quote":         {ded(first, map[string]any{"document_id": letter.ID}), "inventory/deduction-document-invalid"},
		"no cost":             {ded(first, map[string]any{"cost_cents": 0}), "validation/invalid"},
	} {
		if r := call(t, "POST", dded, req{tok: tok, body: c.body}); r.problemType(t) != c.want {
			t.Errorf("%s: %d %s", name, r.Status, r.Body)
		}
	}
	var d1, d2 struct {
		ID          string `json:"id"`
		ShareBP     int64  `json:"tenant_share_bp"`
		AmountCents int64  `json:"amount_cents"`
	}
	audited(t, seen, "POST /leases/{id}/deposit-deductions", 201, func() resp { return call(t, "POST", dded, req{tok: tok, body: ded(first, nil)}) }).json(t, &d1)
	// Lease from 2025-01-01, exit this year: one full year, 10 % worn.
	if d1.ShareBP != 9000 || d1.AmountCents != 43200 {
		t.Errorf("deduction %+v", d1)
	}
	if r := call(t, "POST", dded, req{tok: tok, body: ded(first, nil)}); r.Status != 422 {
		t.Errorf("a second deduction on the item: %d", r.Status)
	}
	call(t, "POST", dded, req{tok: tok, body: ded("", map[string]any{"description": "Clé manquante", "cost_cents": 2500,
		"key": map[string]any{"kind": "key", "destination": "porte d’entrée"}})}).json(t, &d2)
	if d2.ShareBP != 10000 || d2.AmountCents != 2500 {
		t.Errorf("key deduction %+v", d2)
	}
	audited(t, seen, "DELETE /deposit-deductions/{id}", 204, func() resp { return call(t, "DELETE", "/deposit-deductions/"+d2.ID, req{tok: tok}) })

	// The first statement: deposit − deduction − holdback (no rent call is
	// due here), within two months of the keys, as a deduction is retained.
	settle := "/leases/" + lease + "/deposit-settlement"
	if r := call(t, "POST", settle, req{tok: tok, body: map[string]any{"release": true, "dry_run": true}}); r.problemType(t) != "inventory/settlement-unavailable" {
		t.Errorf("release before the first: %d %s", r.Status, r.Body)
	}
	var pre statementView
	call(t, "POST", settle, req{tok: tok, body: map[string]any{"dry_run": true}}).json(t, &pre)
	wantDeadline := today().AddMonthsClamped(2).String()
	if pre.Kind != "initial" || pre.DeductionsCents != 43200 || pre.HoldbackCents != 28000 || pre.TotalCents != 140000-43200-28000-pre.ArrearsCents ||
		pre.Deadline != wantDeadline || pre.PenaltyCents != 0 || pre.DocumentID != nil {
		t.Errorf("dry run %+v (deadline %s)", pre, wantDeadline)
	}
	var st statementView
	audited(t, seen, "POST /leases/{id}/deposit-settlement", 201, func() resp { return call(t, "POST", settle, req{tok: tok, body: map[string]any{}}) }).json(t, &st)
	if st.DocumentID == nil || st.TotalCents != pre.TotalCents || len(st.Lines) != 1 {
		t.Fatalf("statement %+v", st)
	}
	var key, class, tmpl string
	if err := env.super.QueryRow(`SELECT object_key, class, template_version FROM documents WHERE id = $1`, *st.DocumentID).Scan(&key, &class, &tmpl); err != nil ||
		!strings.Contains(key, "/RD-") || class != "restitution" || tmpl != "restitution/v1" {
		t.Errorf("RD document %s %s %s %v", key, class, tmpl, err)
	}
	if r := call(t, "POST", settle, req{tok: tok, body: map[string]any{}}); r.problemType(t) != "inventory/deposit-settled" {
		t.Errorf("settled twice: %d %s", r.Status, r.Body)
	}
	if r := call(t, "DELETE", "/deposit-deductions/"+d1.ID, req{tok: tok}); r.problemType(t) != "inventory/deposit-settled" {
		t.Errorf("a deduction removed once settled: %d %s", r.Status, r.Body)
	}
	if r := call(t, "PUT", "/leases/"+lease+"/vetuste-grid", req{tok: tok, body: map[string]any{"vetuste_grid_id": nil}}); r.problemType(t) != "inventory/deposit-settled" {
		t.Errorf("grid changed once settled: %d", r.Status)
	}
	if _, err := env.super.Exec(`DELETE FROM deposit_deductions WHERE id = $1`, d1.ID); err == nil {
		t.Error("the database let a frozen deduction go")
	}
	if _, err := env.super.Exec(`UPDATE deposit_settlements SET balance_cents = 0 WHERE lease_id = $1`, lease); err == nil {
		t.Error("a settlement was changed")
	}

	// The release, once the accounts are approved, net of the regularisation.
	call(t, "GET", dpath, req{tok: tok}).json(t, &dv)
	if dv.Next != "release" || len(dv.Statements) != 1 {
		t.Fatalf("after the first %+v", dv)
	}
	if r := call(t, "POST", settle, req{tok: tok, body: map[string]any{"release": true}}); r.Status != 422 {
		t.Errorf("release without the approval: %d", r.Status)
	}
	if r := call(t, "POST", settle, req{tok: tok, body: map[string]any{"release": true, "accounts_approved_on": today().String(), "regularisation_cents": 12000}}); r.problemType(t) != "inventory/regularisation-document-invalid" {
		t.Errorf("regularisation without its document: %d %s", r.Status, r.Body)
	}
	var rel statementView
	call(t, "POST", settle, req{tok: tok, body: map[string]any{"release": true, "accounts_approved_on": today().String(),
		"regularisation_cents": 12000, "regularisation_document_id": quote.ID}}).json(t, &rel)
	if rel.Kind != "release" || rel.TotalCents != 28000-12000 || rel.Deadline != today().AddMonthsClamped(1).String() || rel.DocumentID == nil {
		t.Errorf("release %+v", rel)
	}
	dv = depositView{}
	call(t, "GET", dpath, req{tok: tok}).json(t, &dv)
	if dv.Next != "done" || len(dv.Statements) != 2 || dv.Preview != nil {
		t.Errorf("after the release %+v", dv)
	}

	t.Run("roles", func(t *testing.T) {
		var other idv
		call(t, "PUT", "/me", req{tok: token(t, subDepositX), body: map[string]any{"legal_name": "Bailleur X", "address": "1 rue X"}}).json(t, &other)
		if r := call(t, "GET", dpath, req{tok: token(t, subDepositX)}); r.Status != 404 {
			t.Errorf("another owner's deposit: %d", r.Status)
		}
		var asset string
		if err := env.super.QueryRow(`SELECT asset_id FROM lease_assets WHERE lease_id = $1 LIMIT 1`, lease).Scan(&asset); err != nil {
			t.Fatal(err)
		}
		rlsZeroRows(t, owner.ID, other.ID, subDeposit, subDepositX, asset, map[string]string{"deposit_settlements": "owner_id = $1",
			"deposit_deductions": "owner_id = $1", "vetuste_grids": "owner_id = $1"})
	})
}
