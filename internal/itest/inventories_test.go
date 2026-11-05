//go:build integration

package itest

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/migrate"
	"github.com/ovander/tenure-api/internal/params"
)

const (
	subN = "9501" // owner of the inventory scenario (RENT-INV-001 slice 2)
	subO = "9502" // another owner
)

// TestInventorySchema covers migration 000011 (RENT-INV-001 §3, slice 2): a
// phase 1 inventory survives as an issued upload with its items kept; the
// items field is refused from now on; an issued inventory and its children
// are immutable; system templates are read by every scope and written by
// none; the restitution parameters are seeded; and no owner reads another's
// rows in any new table.
func TestInventorySchema(t *testing.T) {
	tok, tokO := token(t, subN), token(t, subO)
	var owner, ownerO, apt, ten, l1 idv
	call(t, "PUT", "/me", req{tok: tok, body: map[string]any{"legal_name": "Bailleur N", "address": "5 rue N, Lyon"}}).json(t, &owner)
	call(t, "PUT", "/me", req{tok: tokO, body: map[string]any{"legal_name": "Bailleur O", "address": "1 rue O"}}).json(t, &ownerO)
	call(t, "POST", "/assets", req{tok: tok, body: map[string]any{"kind": "apartment", "address": "5 rue N, Lyon"}}).json(t, &apt)
	call(t, "POST", "/tenants", req{tok: tok, body: map[string]any{"names": []string{"Léa Martin"}}}).json(t, &ten)
	call(t, "POST", "/leases", req{tok: tok, body: map[string]any{"kind": "unfurnished", "asset_ids": []string{apt.ID},
		"tenants": []map[string]any{{"tenant_id": ten.ID}}, "start_on": "2025-01-01", "term_months": 36, "rent_cents": 70000,
		"provision_cents": 5000, "due_day": 5, "irl_ref_quarter": "2024-Q3", "irl_ref_value": 14451, "revision_month": 9}}).json(t, &l1)
	if l1.ID == "" {
		t.Fatal("lease not created")
	}
	readyToActivate(t, tok, l1.ID)
	var entry struct {
		ID         string          `json:"id"`
		DocumentID string          `json:"document_id"`
		Mode       string          `json:"mode"`
		State      string          `json:"state"`
		Legacy     json.RawMessage `json:"legacy_items"`
	}
	var invs []json.RawMessage
	call(t, "GET", "/leases/"+l1.ID+"/inventories", req{tok: tok}).json(t, &invs)
	if len(invs) != 1 {
		t.Fatalf("inventories: %d", len(invs))
	}
	_ = json.Unmarshal(invs[0], &entry)
	if entry.Mode != "upload" || entry.State != "issued" || len(entry.Legacy) != 0 {
		t.Errorf("an uploaded entry is an issued upload without legacy items: %s", invs[0])
	}

	t.Run("items field retired", func(t *testing.T) {
		if r := call(t, "POST", "/leases/"+l1.ID+"/inventories", req{tok: tok, body: map[string]any{"kind": "exit",
			"done_on": today().String(), "document_id": entry.DocumentID, "items": []map[string]any{{"piece": "séjour"}}}}); r.problemType(t) != "inventory/items-field-retired" {
			t.Errorf("items: %d %s", r.Status, r.Body)
		}
	})

	t.Run("a phase 1 row survives the migration", func(t *testing.T) {
		// A database of its own, stood at 000010 with a phase 1 row, then
		// migrated: the suite's database and its connections are left alone.
		ctx := context.Background()
		superExec(t, `CREATE DATABASE tenure_mig`)
		defer superExec(t, `DROP DATABASE tenure_mig WITH (FORCE)`)
		superExec(t, `GRANT CONNECT, CREATE ON DATABASE tenure_mig TO tenure_migrate`)
		other := strings.Replace(env.baseDSN, "/tenure?", "/tenure_mig?", 1)
		sup, err := sql.Open("pgx", other)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = sup.Close() }()
		if _, err := sup.Exec(`ALTER SCHEMA public OWNER TO tenure_migrate; REVOKE CREATE ON SCHEMA public FROM PUBLIC`); err != nil {
			t.Fatal(err)
		}
		mig, err := migrate.NewMigrator(strings.Replace(dsn(env.baseDSN, "tenure_migrate", rolePW), "/tenure?", "/tenure_mig?", 1))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = mig.Close() }()
		if err := mig.UpTo(ctx, 10, io.Discard); err != nil {
			t.Fatalf("up to 000010: %v", err)
		}
		legacy := `[{"piece": "séjour", "etat": "bon", "note": "parquet ciré"}]`
		id := uuid.Must(uuid.NewV7())
		// The row alone, its owner, lease and document left out (foreign keys
		// and triggers off for this one statement, as superuser).
		if _, err := sup.Exec(`SET session_replication_role = replica;
			INSERT INTO inventories (id, owner_id, lease_id, kind, done_on, items, document_id, created_at, created_by)
			VALUES ('` + id.String() + `', gen_random_uuid(), gen_random_uuid(), 'entry', '2025-01-02', '` + legacy + `'::jsonb, gen_random_uuid(), now(), 'itest');
			SET session_replication_role = DEFAULT`); err != nil {
			t.Fatal(err)
		}
		if err := mig.UpTo(ctx, 11, io.Discard); err != nil {
			t.Fatalf("up 000011: %v", err)
		}
		var mode, state string
		var kept json.RawMessage
		if err := sup.QueryRow(`SELECT mode, state, legacy_items FROM inventories WHERE id = $1`, id).Scan(&mode, &state, &kept); err != nil {
			t.Fatal(err)
		}
		if mode != "upload" || state != "issued" || !jsonEqual(decode(json.RawMessage(legacy)), decode(kept)) {
			t.Errorf("after the migration: mode %s, state %s, legacy_items %s", mode, state, kept)
		}
		if _, err := sup.Exec(`UPDATE inventories SET observations = 'x' WHERE id = $1`, id); err == nil {
			t.Error("the migrated phase 1 inventory is issued, so immutable")
		}
		// Down and up again: reversible while no phase 2 row exists.
		if err := mig.Down(ctx, io.Discard); err != nil {
			t.Fatalf("down 000011 with a phase 1 row: %v", err)
		}
		if err := sup.QueryRow(`SELECT items FROM inventories WHERE id = $1`, id).Scan(&kept); err != nil || !jsonEqual(decode(json.RawMessage(legacy)), decode(kept)) {
			t.Errorf("after down, items %s: %v", kept, err)
		}
	})

	t.Run("the API shows the inventory as an issued upload", func(t *testing.T) {
		// A phase 1 row recorded through the API: no legacy items to show.
		call(t, "GET", "/leases/"+l1.ID+"/inventories", req{tok: tok}).json(t, &invs)
		_ = json.Unmarshal(invs[0], &entry)
		if entry.Mode != "upload" || entry.State != "issued" || len(entry.Legacy) != 0 {
			t.Errorf("listed %s", invs[0])
		}
		var b struct{ Activation []struct{ Type string } }
		call(t, "GET", "/leases/"+l1.ID+"/blockers", req{tok: tok}).json(t, &b)
		for _, x := range b.Activation {
			if x.Type == "lease/entry-inventory-missing" {
				t.Error("an issued uploaded entry no longer counts")
			}
		}
	})

	// Phase 2 rows, as tenure_api in owner N's scope.
	room, element, exitID, itemID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	grid, meter := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	err := asAPICommit(t, owner.ID, subN, func(tx *sql.Tx) error {
		for _, q := range []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO asset_rooms (id, owner_id, asset_id, position, kind, label, created_at, created_by, updated_at, updated_by)
				VALUES ($1, $2, $3, 1, 'living', 'Séjour', now(), $4, now(), $4)`, []any{room, owner.ID, apt.ID, subN}},
			{`INSERT INTO asset_elements (id, owner_id, room_id, position, category, label, created_at, created_by, updated_at, updated_by)
				VALUES ($1, $2, $3, 1, 'floor', 'Sol', now(), $4, now(), $4)`, []any{element, owner.ID, room, subN}},
			{`INSERT INTO vetuste_grids (id, owner_id, name, source, created_at, created_by, updated_at, updated_by)
				VALUES ($1, $2, 'Grille', 'accord des parties', now(), $3, now(), $3)`, []any{grid, owner.ID, subN}},
			{`INSERT INTO inventories (id, owner_id, lease_id, kind, mode, state, entry_inventory_id, legacy_items, created_at, created_by)
				VALUES ($1, $2, $3, 'exit', 'amicable', 'draft', $4, '[]', now(), $5)`, []any{exitID, owner.ID, l1.ID, entry.ID, subN}},
			{`INSERT INTO inventory_items (id, owner_id, inventory_id, room_id, element_id, room_label, position, category, label, condition,
				created_at, created_by, updated_at, updated_by) VALUES ($1, $2, $3, $4, $5, 'Séjour', 1, 'floor', 'Sol', 'good', now(), $6, now(), $6)`,
				[]any{itemID, owner.ID, exitID, room, element, subN}},
			{`INSERT INTO inventory_meters (id, owner_id, inventory_id, kind, reading, created_at, created_by)
				VALUES ($1, $2, $3, 'water_cold', '123,45', now(), $4)`, []any{meter, owner.ID, exitID, subN}},
			{`INSERT INTO inventory_keys (id, owner_id, inventory_id, kind, destination, quantity, created_at, created_by)
				VALUES ($1, $2, $3, 'key', 'porte d’entrée', 2, now(), $4)`, []any{uuid.Must(uuid.NewV7()), owner.ID, exitID, subN}},
			{`INSERT INTO inventory_photos (id, owner_id, inventory_id, item_id, object_key, sha256, original_sha256, width, height, created_at, created_by)
				VALUES ($1, $2, $3, $4, 'k', repeat('a', 64), repeat('b', 64), 2048, 1536, now(), $5)`, []any{uuid.Must(uuid.NewV7()), owner.ID, exitID, itemID, subN}},
			{`INSERT INTO inventory_signatures (id, owner_id, inventory_id, party, signer_name, method, signed_at, created_at, created_by)
				VALUES ($1, $2, $3, 'tenant', 'Léa Martin', 'wet_scan', now(), now(), $4)`, []any{uuid.Must(uuid.NewV7()), owner.ID, exitID, subN}},
			{`INSERT INTO inventory_completions (id, owner_id, inventory_id, reason, requested_on, created_at, created_by, updated_at, updated_by)
				VALUES ($1, $2, $3, 'ten_day', current_date, now(), $4, now(), $4)`, []any{uuid.Must(uuid.NewV7()), owner.ID, entry.ID, subN}},
			{`INSERT INTO deposit_deductions (id, owner_id, lease_id, exit_item_id, description, cost_cents, tenant_share_bp, amount_cents, basis,
				document_id, created_at, created_by, updated_at, updated_by)
				VALUES ($1, $2, $3, $4, 'Ponçage du parquet', 48000, 6400, 30720, 'quote', $5, now(), $6, now(), $6)`,
				[]any{uuid.Must(uuid.NewV7()), owner.ID, l1.ID, itemID, entry.DocumentID, subN}},
		} {
			if _, err := tx.Exec(q.sql, q.args...); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("phase 2 rows as tenure_api: %v", err)
	}

	t.Run("issued is immutable", func(t *testing.T) {
		refused := func(what, q string, args ...any) {
			t.Helper()
			err := asAPI(t, owner.ID, subN, func(tx *sql.Tx) error { _, err := tx.Exec(q, args...); return err })
			if err == nil {
				t.Errorf("%s was allowed", what)
			}
		}
		// The migrated phase 1 entry is issued.
		refused("updating an issued entry", `UPDATE inventories SET observations = 'x' WHERE id = $1`, entry.ID)
		// A draft can be worked on, then issued with its document.
		if err := asAPICommit(t, owner.ID, subN, func(tx *sql.Tx) error {
			if _, err := tx.Exec(`UPDATE inventory_items SET condition = 'fair' WHERE id = $1`, itemID); err != nil {
				return err
			}
			_, err := tx.Exec(`UPDATE inventories SET state = 'issued', done_on = current_date, document_id = $1 WHERE id = $2`, entry.DocumentID, exitID)
			return err
		}); err != nil {
			t.Fatalf("working a draft and issuing it: %v", err)
		}
		refused("updating an issued exit", `UPDATE inventories SET place = 'x' WHERE id = $1`, exitID)
		refused("changing an item of an issued exit", `UPDATE inventory_items SET condition = 'poor' WHERE id = $1`, itemID)
		refused("deleting a meter of an issued exit", `DELETE FROM inventory_meters WHERE id = $1`, meter)
		refused("adding a key to an issued exit", `INSERT INTO inventory_keys (id, owner_id, inventory_id, kind, destination, quantity, created_at, created_by)
			VALUES ($1, $2, $3, 'badge', 'hall', 1, now(), $4)`, uuid.Must(uuid.NewV7()), owner.ID, exitID, subN)
		refused("issuing without a document", `INSERT INTO inventories (id, owner_id, lease_id, kind, mode, state, legacy_items, created_at, created_by)
			VALUES ($1, $2, $3, 'entry', 'amicable', 'issued', '[]', now(), $4)`, uuid.Must(uuid.NewV7()), owner.ID, l1.ID, subN)
		// Even a superuser cannot rewrite an issued inventory.
		if _, err := env.super.Exec(`DELETE FROM inventory_items WHERE id = $1`, itemID); err == nil {
			t.Error("a superuser deleted an item of an issued inventory")
		}
	})

	t.Run("system templates", func(t *testing.T) {
		var n int
		for _, who := range []struct{ owner, sub string }{{owner.ID, subN}, {ownerO.ID, subO}, {"", subO}} {
			if err := asAPI(t, who.owner, who.sub, func(tx *sql.Tx) error {
				return tx.QueryRow(`SELECT count(*) FROM inventory_templates WHERE owner_id IS NULL`).Scan(&n)
			}); err != nil || n != 3 {
				t.Errorf("scope %q reads %d system templates: %v", who.owner, n, err)
			}
		}
		err := asAPI(t, owner.ID, subN, func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO inventory_templates (id, owner_id, asset_kind, version, structure, created_at, created_by)
				VALUES ($1, NULL, 'box', 99, '{}', now(), $2)`, uuid.Must(uuid.NewV7()), subN)
			return err
		})
		if err == nil {
			t.Error("an owner wrote a system template")
		}
	})

	t.Run("restitution parameters seeded", func(t *testing.T) {
		ps := fixtureParams(t)
		want, err := ps.DepositReturn(today())
		if err != nil {
			t.Fatal(err)
		}
		for _, k := range []string{params.KeyDepositReturnConforming, params.KeyDepositReturnWithDeductions, params.KeyDepositCollectiveHoldback,
			params.KeyDepositHoldbackBalance, params.KeyDepositLatePenalty, "inventory.completion_window_days", "inventory.heating_window_months",
			"heating_season.start_month", "heating_season.end_month"} {
			if superCount(t, `SELECT count(*) FROM legal_params WHERE key = $1`, k) == 0 {
				t.Errorf("%s is not seeded", k)
			}
		}
		if want.ConformingMonths <= 0 || want.WithDeductionsMonths <= want.ConformingMonths {
			t.Errorf("restitution rules %+v", want)
		}
	})

	t.Run("isolation", func(t *testing.T) {
		rlsZeroRows(t, owner.ID, ownerO.ID, subN, subO, apt.ID, map[string]string{
			"asset_rooms": "owner_id = $1", "asset_elements": "owner_id = $1", "vetuste_grids": "owner_id = $1",
			"inventory_items": "owner_id = $1", "inventory_meters": "owner_id = $1", "inventory_keys": "owner_id = $1",
			"inventory_photos": "owner_id = $1", "inventory_signatures": "owner_id = $1", "inventory_completions": "owner_id = $1",
			"deposit_deductions": "owner_id = $1", "inventories": "owner_id = $1",
		})
	})
}

// asAPICommit runs f as tenure_api with the given row-level-security context
// and commits when f succeeds.
func asAPICommit(t *testing.T, owner, actor string, f func(*sql.Tx) error) error {
	t.Helper()
	conn, err := sql.Open("pgx", env.apiURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	tx, err := conn.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec("SELECT set_config('app.owner_id', $1, true), set_config('app.actor_sub', $2, true)", owner, actor); err != nil {
		t.Fatal(err)
	}
	if err := f(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func decode(b json.RawMessage) any {
	var v any
	_ = json.Unmarshal(b, &v)
	return v
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
