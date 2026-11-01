//go:build integration

package itest

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/httpapi"
	"github.com/ovander/tenure-api/internal/jobs"
)

// Socrate subjects of the scenario (numeric strings, as Socrate issues them).
const (
	subA     = "1001" // owner A
	subB     = "1002" // owner B
	subAcc   = "1003" // accountant of A
	subCo    = "1004" // co-owner of A
	subAdmin = "1999" // Socrate admin
)

type exercised map[string]bool

// audited runs a mutating call and asserts it committed exactly one audit
// event when it succeeded, and none when it was refused.
func audited(t *testing.T, seen exercised, route string, wantStatus int, f func() resp) resp {
	t.Helper()
	before := superCount(t, "SELECT count(*) FROM audit_events")
	r := f()
	if r.Status != wantStatus {
		t.Fatalf("%s: status %d, want %d: %s", route, r.Status, wantStatus, r.Body)
	}
	delta := superCount(t, "SELECT count(*) FROM audit_events") - before
	want := 0
	if r.Status < 300 {
		want = 1
		seen[route] = true
	}
	if delta != want {
		t.Fatalf("%s: %d audit events, want %d", route, delta, want)
	}
	return r
}

type idv struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
}

func TestScenario(t *testing.T) {
	seen := exercised{}
	tokA, tokB := token(t, subA), token(t, subB)
	var ownerA, ownerB idv

	t.Run("profiles", func(t *testing.T) {
		r := call(t, "GET", "/me", req{tok: tokA})
		if r.Status != 200 || !strings.Contains(string(r.Body), `"owner":null`) {
			t.Fatalf("new caller /me: %d %s", r.Status, r.Body)
		}
		if r := call(t, "GET", "/assets", req{tok: tokA}); r.Status != 403 || r.problemType(t) != "auth/no-scope" {
			t.Fatalf("assets without a profile: %d %s", r.Status, r.Body)
		}
		audited(t, seen, "PUT /me", 201, func() resp {
			return call(t, "PUT", "/me", req{tok: tokA, body: map[string]any{"legal_name": "Propriétaire A", "language": "fr"}})
		}).json(t, &ownerA)
		audited(t, seen, "PUT /me", 201, func() resp {
			return call(t, "PUT", "/me", req{tok: tokB, body: map[string]any{"legal_name": "Owner B", "language": "en"}})
		}).json(t, &ownerB)
		audited(t, seen, "PUT /me", 409, func() resp {
			return call(t, "PUT", "/me", req{tok: tokA, body: map[string]any{"legal_name": "again"}})
		})
		audited(t, seen, "PUT /me", 200, func() resp {
			return call(t, "PUT", "/me", req{tok: tokA, ifMatch: `W/"1"`, body: map[string]any{"legal_name": "Propriétaire A", "address": "1 rue de Lyon"}})
		})
		// Messages follow the profile language.
		if r := call(t, "GET", "/assets/not-a-uuid", req{tok: tokB}); !strings.Contains(string(r.Body), "Resource not found.") {
			t.Errorf("English title expected: %s", r.Body)
		}
	})

	var apt, box, tenant, lse, guar idv
	t.Run("portfolio", func(t *testing.T) {
		audited(t, seen, "POST /assets", 201, func() resp {
			return call(t, "POST", "/assets", req{tok: tokA, body: map[string]any{"kind": "apartment", "address": "1 rue de Lyon",
				"surface_hundredths_m2": 4530, "dpe_class": "C"}})
		}).json(t, &apt)
		audited(t, seen, "POST /assets", 201, func() resp {
			return call(t, "POST", "/assets", req{tok: tokA, body: map[string]any{"kind": "box", "address": "1 rue de Lyon", "parent_asset_id": apt.ID}})
		}).json(t, &box)
		audited(t, seen, "POST /assets", 422, func() resp {
			return call(t, "POST", "/assets", req{tok: tokA, body: map[string]any{"kind": "castle", "address": ""}})
		})
		audited(t, seen, "PUT /assets/{id}", 200, func() resp {
			return call(t, "PUT", "/assets/"+apt.ID, req{tok: tokA, ifMatch: `W/"1"`, body: map[string]any{"kind": "apartment",
				"address": "1 rue de Lyon", "lot": "12", "surface_hundredths_m2": 4530}})
		})
		if r := call(t, "PUT", "/assets/"+apt.ID, req{tok: tokA, body: map[string]any{"kind": "apartment", "address": "x"}}); r.Status != 428 {
			t.Errorf("PUT without If-Match: %d", r.Status)
		}
		audited(t, seen, "PUT /assets/{id}", 412, func() resp {
			return call(t, "PUT", "/assets/"+apt.ID, req{tok: tokA, ifMatch: `W/"1"`, body: map[string]any{"kind": "apartment", "address": "x"}})
		})
		audited(t, seen, "PUT /assets/{id}", 409, func() resp {
			return call(t, "PUT", "/assets/"+apt.ID, req{tok: tokA, ifMatch: `W/"2"`, body: map[string]any{"kind": "house", "address": "x"}})
		})
		audited(t, seen, "POST /tenants", 201, func() resp {
			return call(t, "POST", "/tenants", req{tok: tokA, body: map[string]any{"names": []string{"Jeanne Dupont"}, "email": "jeanne@example.test"}})
		}).json(t, &tenant)
		audited(t, seen, "PUT /tenants/{id}", 200, func() resp {
			return call(t, "PUT", "/tenants/"+tenant.ID, req{tok: tokA, ifMatch: `W/"1"`, body: map[string]any{
				"names": []string{"Jeanne Dupont", "Paul Dupont"}, "email": "jeanne@example.test", "phone": "+33 6 00 00 00 00"}})
		})
	})

	leaseBody := func(assets ...string) map[string]any {
		return map[string]any{"kind": "unfurnished", "asset_ids": assets, "tenants": []map[string]any{{"tenant_id": tenant.ID}},
			"start_on": "2025-03-15", "term_months": 36, "rent_cents": 85000, "provision_cents": 9000, "deposit_cents": 85000,
			"due_day": 5, "irl_ref_quarter": "2024-Q4", "irl_ref_value": 14464, "revision_month": 3}
	}
	t.Run("lease", func(t *testing.T) {
		audited(t, seen, "POST /leases", 201, func() resp { return call(t, "POST", "/leases", req{tok: tokA, body: leaseBody(apt.ID, box.ID)}) }).json(t, &lse)
		audited(t, seen, "POST /leases", 422, func() resp {
			b := leaseBody(apt.ID)
			b["due_day"] = 31
			return call(t, "POST", "/leases", req{tok: tokA, body: b})
		})
		audited(t, seen, "PUT /leases/{id}", 200, func() resp {
			b := leaseBody(apt.ID, box.ID)
			b["rent_cents"] = 86000
			return call(t, "PUT", "/leases/"+lse.ID, req{tok: tokA, ifMatch: `W/"1"`, body: b})
		})
		audited(t, seen, "POST /leases/{id}/guarantees", 201, func() resp {
			return call(t, "POST", "/leases/"+lse.ID+"/guarantees", req{tok: tokA, body: map[string]any{"kind": "deposit", "amount_cents": 86000,
				"guarantor": map[string]any{"name": "Caution Martin"}}})
		}).json(t, &guar)
		audited(t, seen, "PUT /guarantees/{id}", 200, func() resp {
			return call(t, "PUT", "/guarantees/"+guar.ID, req{tok: tokA, ifMatch: `W/"1"`, body: map[string]any{"kind": "deposit", "amount_cents": 86000, "status": "held"}})
		})
		audited(t, seen, "POST /leases/{id}/transitions", 200, func() resp {
			return call(t, "POST", "/leases/"+lse.ID+"/transitions", req{tok: tokA, body: map[string]any{"action": "activate"}})
		})
		audited(t, seen, "PUT /leases/{id}", 409, func() resp {
			return call(t, "PUT", "/leases/"+lse.ID, req{tok: tokA, ifMatch: `W/"3"`, body: leaseBody(apt.ID, box.ID)})
		})
		// One current lease per asset.
		var second idv
		call(t, "POST", "/leases", req{tok: tokA, body: leaseBody(apt.ID)}).json(t, &second)
		if r := call(t, "POST", "/leases/"+second.ID+"/transitions", req{tok: tokA, body: map[string]any{"action": "activate"}}); r.Status != 409 || r.problemType(t) != "lease/one-active-per-asset" {
			t.Errorf("second activation: %d %s", r.Status, r.Body)
		}
		audited(t, seen, "POST /leases/{id}/transitions", 409, func() resp {
			return call(t, "POST", "/leases/"+lse.ID+"/transitions", req{tok: tokA, body: map[string]any{"action": "close"}})
		})
		for _, step := range []map[string]any{{"action": "notice", "end_on": "2026-03-14"}, {"action": "end"}} {
			audited(t, seen, "POST /leases/{id}/transitions", 200, func() resp {
				return call(t, "POST", "/leases/"+lse.ID+"/transitions", req{tok: tokA, body: step})
			})
		}
		if r := call(t, "POST", "/leases/"+lse.ID+"/transitions", req{tok: tokA, body: map[string]any{"action": "close"}}); r.problemType(t) != "lease/deposit-not-settled" {
			t.Errorf("close with a held deposit: %d %s", r.Status, r.Body)
		}
		call(t, "PUT", "/guarantees/"+guar.ID, req{tok: tokA, ifMatch: `W/"2"`, body: map[string]any{"kind": "deposit", "amount_cents": 86000, "status": "settled"}})
		audited(t, seen, "POST /leases/{id}/transitions", 200, func() resp {
			return call(t, "POST", "/leases/"+lse.ID+"/transitions", req{tok: tokA, body: map[string]any{"action": "close"}})
		})
	})

	t.Run("consistent ownership shares", func(t *testing.T) {
		var half, box2 idv
		call(t, "POST", "/assets", req{tok: tokA, body: map[string]any{"kind": "apartment", "address": "2 rue de Lyon",
			"shares": []map[string]any{{"owner_id": ownerA.ID, "bp": 5000}, {"owner_id": ownerB.ID, "bp": 5000}}}}).json(t, &half)
		call(t, "POST", "/assets", req{tok: tokA, body: map[string]any{"kind": "box", "address": "2 rue de Lyon"}}).json(t, &box2)
		if r := call(t, "POST", "/leases", req{tok: tokA, body: leaseBody(half.ID, box2.ID)}); r.Status != 422 || r.problemType(t) != "lease/consistent-ownership-shares" {
			t.Errorf("mixed shares: %d %s", r.Status, r.Body)
		}
	})

	t.Run("cross-owner isolation", func(t *testing.T) {
		for _, path := range []string{"/assets", "/tenants", "/leases"} {
			var l struct{ Total int }
			call(t, "GET", path, req{tok: tokB}).json(t, &l)
			if l.Total != 0 {
				t.Errorf("owner B lists %d items of %s", l.Total, path)
			}
		}
		if r := call(t, "GET", "/assets/"+apt.ID, req{tok: tokB}); r.Status != 404 {
			t.Errorf("B reads A's asset: %d", r.Status)
		}
		if r := call(t, "GET", "/assets", req{tok: tokB, scope: ownerA.ID}); r.Status != 403 || r.problemType(t) != "auth/scope-forbidden" {
			t.Errorf("B selects A's scope: %d %s", r.Status, r.Body)
		}
		if r := call(t, "GET", "/audit/events", req{tok: tokB}); !strings.Contains(string(r.Body), `"total":1`) {
			t.Errorf("B's audit trail holds only B's profile event: %s", r.Body)
		}
		rlsZeroRows(t, ownerA.ID, ownerB.ID, apt.ID)
	})

	t.Run("grants", func(t *testing.T) {
		now := time.Now().UTC()
		for sub, role := range map[string]string{subAcc: "accountant", subCo: "co_owner"} {
			superExec(t, `INSERT INTO owner_grants (id, owner_id, grantee_sub, role, granted_at, created_by, updated_at, updated_by)
				VALUES ($1, $2, $3, $4, $5, 'itest', $5, 'itest')`, uuid.Must(uuid.NewV7()), ownerA.ID, sub, role, now)
		}
		acc, co := token(t, subAcc), token(t, subCo)
		if r := call(t, "GET", "/assets", req{tok: acc}); r.Status != 403 {
			t.Errorf("a grantee has no default scope: %d", r.Status)
		}
		var ts struct{ Items []map[string]any }
		call(t, "GET", "/tenants", req{tok: acc, scope: ownerA.ID}).json(t, &ts)
		if len(ts.Items) != 1 || ts.Items[0]["email"] != "" || ts.Items[0]["phone"] != "" {
			t.Errorf("accountant sees tenant contact data: %+v", ts.Items)
		}
		var gs []map[string]any
		call(t, "GET", "/leases/"+lse.ID+"/guarantees", req{tok: acc, scope: ownerA.ID}).json(t, &gs)
		if len(gs) != 1 || fmt.Sprint(gs[0]["guarantor"]) != "map[]" {
			t.Errorf("accountant sees guarantor data: %+v", gs)
		}
		for _, tk := range []string{acc, co} {
			if r := call(t, "POST", "/assets", req{tok: tk, scope: ownerA.ID, body: map[string]any{"kind": "box", "address": "x"}}); r.Status != 403 {
				t.Errorf("read-only grantee writes: %d", r.Status)
			}
		}
		if r := call(t, "GET", "/audit/events", req{tok: acc, scope: ownerA.ID}); r.Status != 200 {
			t.Errorf("accountant reads the audit trail: %d", r.Status)
		}
		if r := call(t, "GET", "/audit/events", req{tok: co, scope: ownerA.ID}); r.Status != 403 {
			t.Errorf("co-owner reads the audit trail: %d (Q-37)", r.Status)
		}
		var me struct{ Scopes []map[string]any }
		call(t, "GET", "/me", req{tok: co}).json(t, &me)
		if len(me.Scopes) != 1 || me.Scopes[0]["role"] != "co_owner" {
			t.Errorf("co-owner scopes: %+v", me.Scopes)
		}
		// Row-level security refuses a grantee's write even past the API.
		err := asAPI(t, ownerA.ID, subCo, func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO tenants (id, owner_id, names, created_at, created_by, updated_at, updated_by)
				VALUES ($1, $2, '{X}', now(), 'x', now(), 'x')`, uuid.Must(uuid.NewV7()), ownerA.ID)
			return err
		})
		if err == nil || !strings.Contains(err.Error(), "row-level security") {
			t.Errorf("co-owner insert past the API: %v", err)
		}
	})

	t.Run("tokens", func(t *testing.T) {
		for name, tk := range map[string]string{"other audience": token(t, subA, withAud("another-app")),
			"other issuer": token(t, subA, withIss("https://evil.test")), "expired": token(t, subA, expired())} {
			if r := call(t, "GET", "/me", req{tok: tk}); r.Status != 401 {
				t.Errorf("%s: %d", name, r.Status)
			}
		}
	})

	t.Run("params step-up", func(t *testing.T) {
		tomorrow := time.Now().In(jobs.Paris()).AddDate(0, 0, 1).Format(time.DateOnly)
		body := map[string]any{"effective_from": tomorrow, "value": 5, "source": "itest"}
		if r := call(t, "PUT", "/params/receipt_due_days_before_reminder", req{tok: token(t, subA, stepUp(time.Minute)), body: body}); r.Status != 403 {
			t.Errorf("non-admin: %d", r.Status)
		}
		for name, tk := range map[string]string{"no auth_time": token(t, subAdmin, withRole("admin")),
			"stale": token(t, subAdmin, withRole("admin"), stepUp(6*time.Minute))} {
			audited(t, seen, "PUT /params/{key}", 401, func() resp {
				return call(t, "PUT", "/params/receipt_due_days_before_reminder", req{tok: tk, body: body})
			})
			_ = name
		}
		fresh := token(t, subAdmin, withRole("admin"), stepUp(time.Minute))
		audited(t, seen, "PUT /params/{key}", 201, func() resp {
			return call(t, "PUT", "/params/receipt_due_days_before_reminder", req{tok: fresh, body: body})
		})
		if n := superCount(t, "SELECT count(*) FROM audit_events WHERE owner_id IS NULL AND action = 'params.put'"); n != 1 {
			t.Errorf("platform chain events: %d", n)
		}
		for want, b := range map[string]map[string]any{
			"params/unknown-key":            {"effective_from": tomorrow, "value": 1, "source": "x"},
			"params/effective-date-in-past": {"effective_from": "2020-01-01", "value": 1, "source": "x"},
			"params/duplicate-entry":        body,
		} {
			key := "receipt_due_days_before_reminder"
			if want == "params/unknown-key" {
				key = "no_such_param"
			}
			if r := call(t, "PUT", "/params/"+key, req{tok: fresh, body: b}); r.problemType(t) != want {
				t.Errorf("want %s, got %d %s", want, r.Status, r.Body)
			}
		}
	})

	t.Run("audit export and chain", func(t *testing.T) {
		if r := call(t, "GET", "/audit/events.csv", req{tok: tokA}); r.Status != 401 {
			t.Errorf("export without step-up: %d", r.Status)
		}
		r := call(t, "GET", "/audit/events.csv?entity_type=lease", req{tok: token(t, subA, stepUp(time.Minute))})
		if r.Status != 200 || !strings.HasPrefix(string(r.Body), "id,occurred_at") || !strings.Contains(string(r.Body), "lease.activate") {
			t.Fatalf("export: %d %s", r.Status, r.Body)
		}
		if n := superCount(t, "SELECT count(*) FROM audit_events WHERE action = 'audit.export'"); n != 1 {
			t.Errorf("export events: %d", n)
		}
		runner := &jobs.Runner{DB: env.jobsDB, Jobs: jobs.Registry(), Now: time.Now}
		ctx := context.Background()
		run, ran, err := runner.Trigger(ctx, "audit.verify", nil, false)
		if err != nil || !ran || run.Status != jobs.StatusSucceeded {
			t.Fatalf("verify: %+v %v %v", run, ran, err)
		}
		again, ran, err := runner.Trigger(ctx, "audit.verify", nil, false)
		if err != nil || ran || again.ID != run.ID {
			t.Errorf("second start of the same period ran again: %v %v", ran, err)
		}
		if n := superCount(t, "SELECT count(*) FROM job_runs WHERE job = 'audit.verify'"); n != 1 {
			t.Errorf("job runs: %d", n)
		}
		// The trail refuses updates; tampering needs the trigger disabled.
		if _, err := env.super.Exec("UPDATE audit_events SET action = 'x' WHERE id = (SELECT min(id) FROM audit_events)"); err == nil {
			t.Error("audit_events accepted an UPDATE")
		}
		superExec(t, "ALTER TABLE audit_events DISABLE TRIGGER audit_events_no_update")
		superExec(t, `UPDATE audit_events SET after = jsonb_set(after, '{legal_name}', '"Forged"')
			WHERE id = (SELECT min(id) FROM audit_events WHERE owner_id = $1)`, ownerA.ID)
		superExec(t, "ALTER TABLE audit_events ENABLE TRIGGER audit_events_no_update")
		dry, _, err := runner.Trigger(ctx, "audit.verify", nil, true)
		if err != nil || dry.Status != jobs.StatusPartial || !strings.Contains(string(dry.Summary), "hash does not match") {
			t.Errorf("tampered chain: %s %s %v", dry.Status, dry.Summary, err)
		}
	})

	t.Run("every mutating route is audited", func(t *testing.T) {
		listed := readAuditedRoutes(t)
		var mutating []string
		for _, rt := range httpapi.Routes(httpapi.Deps{}) {
			if rt.Method != "GET" {
				mutating = append(mutating, rt.Method+" "+rt.Pattern)
			}
		}
		sort.Strings(mutating)
		if strings.Join(mutating, "\n") != strings.Join(listed, "\n") {
			t.Errorf("testdata/audited_routes.txt\n%s\ndiffers from the router's mutating routes\n%s", strings.Join(listed, "\n"), strings.Join(mutating, "\n"))
		}
		for _, r := range listed {
			if !seen[r] {
				t.Errorf("%s was never exercised with a successful, audited call", r)
			}
		}
	})
}

func readAuditedRoutes(t *testing.T) []string {
	f, err := os.Open("../../testdata/audited_routes.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	sort.Strings(out)
	return out
}

// asAPI runs f as tenure_api with the given row-level-security context.
func asAPI(t *testing.T, owner, actor string, f func(*sql.Tx) error) error {
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
	return f(tx)
}

// rlsZeroRows proves owner B — or nobody — reads none of owner A's rows in any
// owner table, even with app.owner_id spoofed to A (RENT-BE-001 §10).
func rlsZeroRows(t *testing.T, ownerA, ownerB, assetA string) {
	t.Helper()
	superExec(t, `INSERT INTO documents (id, owner_id, class, object_key, sha256, size_bytes, created_at, created_by)
		VALUES ($1, $2, 'other', 'k', repeat('a', 64), 1, now(), 'itest')`, uuid.Must(uuid.NewV7()), ownerA)
	superExec(t, `INSERT INTO audit_chain_heads (owner_id, as_of, last_event_id, head_hash) VALUES ($1, current_date, 1, repeat('b', 64))`, ownerA)
	superExec(t, `INSERT INTO job_runs (id, job, period, owner_id, started_at) VALUES ($1, 'itest', 'x', $2, now())`, uuid.Must(uuid.NewV7()), ownerA)
	superExec(t, `INSERT INTO owner_grants (id, owner_id, grantee_sub, role, granted_at, created_by, updated_at, updated_by)
		VALUES ($1, $2, 'someone-else', 'accountant', now(), 'itest', now(), 'itest')`, uuid.Must(uuid.NewV7()), ownerA)
	tables := map[string]string{
		"owners": "id = $1", "owner_grants": "owner_id = $1", "assets": "owner_id = $1", "tenants": "owner_id = $1",
		"leases": "owner_id = $1", "lease_assets": "owner_id = $1", "lease_tenants": "owner_id = $1", "guarantees": "owner_id = $1",
		"documents": "owner_id = $1", "audit_events": "owner_id = $1", "audit_chain_heads": "owner_id = $1",
		"job_runs": "owner_id = $1", "asset_shares": "asset_id = '" + assetA + "'",
	}
	count := func(owner, actor, table, where string) int {
		var n int
		err := asAPI(t, owner, actor, func(tx *sql.Tx) error {
			return tx.QueryRow("SELECT count(*) FROM " + table + " WHERE " + strings.ReplaceAll(where, "$1", "'"+ownerA+"'")).Scan(&n)
		})
		if err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		return n
	}
	for table, where := range tables {
		if superCount(t, "SELECT count(*) FROM "+table+" WHERE "+strings.ReplaceAll(where, "$1", "'"+ownerA+"'")) == 0 {
			t.Fatalf("fixture: owner A has no row in %s", table)
		}
		if n := count(ownerA, subA, table, where); n == 0 && table != "owner_grants" {
			t.Errorf("%s: owner A cannot read its own rows", table)
		}
		for _, ctx := range []struct{ owner, actor string }{{ownerA, subB}, {ownerB, subB}, {"", subB}, {"", ""}, {ownerA, ""}} {
			if n := count(ctx.owner, ctx.actor, table, where); n != 0 {
				t.Errorf("%s: %d of owner A's rows visible with app.owner_id=%q app.actor_sub=%q", table, n, ctx.owner, ctx.actor)
			}
		}
	}
}
