//go:build integration

package itest

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

const (
	subV   = "9001" // owner of the archive scenario
	subW   = "9002" // another owner
	subAcV = "9003" // V's accountant
)

type exportView struct {
	ID        string
	State     string
	Documents int
	Link      *struct{ URL string }
}

// waitExport polls an export until it is no longer pending.
func waitExport(t *testing.T, tok, scope, id string) exportView {
	t.Helper()
	var e exportView
	for i := 0; i < 120; i++ {
		call(t, "GET", "/documents/exports/"+id, req{tok: tok, scope: scope}).json(t, &e)
		if e.State != "pending" {
			return e
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("export %s still pending", id)
	return e
}

// zipIndex downloads an export and returns its index rows (header skipped)
// and its entry names.
func zipIndex(t *testing.T, link string) ([][]string, []string) {
	t.Helper()
	res, err := http.Get(link)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 {
		t.Fatalf("download %d %s", res.StatusCode, b)
	}
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	var rows [][]string
	for _, f := range zr.File {
		names = append(names, f.Name)
		if f.Name == "index.csv" {
			rc, _ := f.Open()
			rows, _ = csv.NewReader(rc).ReadAll()
			_ = rc.Close()
		}
	}
	if len(rows) == 0 {
		t.Fatal("no index.csv")
	}
	return rows[1:], names
}

func runJob(t *testing.T, tok, name string, body map[string]any) (string, string) {
	t.Helper()
	var run struct {
		Status  string
		Summary any
	}
	r := call(t, "POST", "/jobs/"+name+"/trigger", req{tok: tok, body: body})
	r.json(t, &run)
	return run.Status, string(r.Body)
}

func TestArchive(t *testing.T) {
	tok, tokW, tokAcc := token(t, subV), token(t, subW), token(t, subAcV)
	freshV := func() string { return token(t, subV, stepUp(time.Minute)) }
	var owner, ownerW, a1, ten, l1 idv
	call(t, "PUT", "/me", req{tok: tok, body: map[string]any{"legal_name": "Bailleur V", "address": "9 quai Perrache, Lyon"}}).json(t, &owner)
	call(t, "PUT", "/me", req{tok: tokW, body: map[string]any{"legal_name": "Bailleur W", "address": "1 rue W"}}).json(t, &ownerW)
	call(t, "POST", "/assets", req{tok: tok, body: map[string]any{"kind": "apartment", "address": "9 quai Perrache, Lyon"}}).json(t, &a1)
	call(t, "POST", "/tenants", req{tok: tok, body: map[string]any{"names": []string{"Hugo Petit"}, "email": "hugo@example.test"}}).json(t, &ten)
	call(t, "POST", "/leases", req{tok: tok, body: map[string]any{"kind": "unfurnished", "asset_ids": []string{a1.ID},
		"tenants": []map[string]any{{"tenant_id": ten.ID}}, "start_on": "2025-01-01", "term_months": 36, "rent_cents": 70000,
		"provision_cents": 5000, "due_day": 5, "irl_ref_quarter": "2024-Q3", "irl_ref_value": 14451, "revision_month": 9}}).json(t, &l1)
	ct := readyToActivate(t, tok, l1.ID)
	call(t, "POST", "/leases/"+l1.ID+"/transitions", req{tok: tok, body: map[string]any{"action": "activate"}})
	call(t, "POST", "/billing/runs", req{tok: tok, body: map[string]any{"period": "2025-01"}})
	// Owner W has documents of their own, which V's exports must never hold.
	upload(t, tokW, "", map[string]string{"class": "other"}, "w.pdf", signedPDF)
	var calls struct{ Items []struct{ Number string } }
	call(t, "GET", "/billing/documents?kind=rent_call", req{tok: tok}).json(t, &calls)
	year := today().Year

	t.Run("browse by number", func(t *testing.T) {
		var list struct {
			Total int
			Items []struct {
				Number string `json:"number"`
			}
		}
		call(t, "GET", "/documents?number="+calls.Items[0].Number, req{tok: tok}).json(t, &list)
		if list.Total != 1 {
			t.Errorf("by number: %d", list.Total)
		}
		// The view carries the number the search found it by (#122).
		if len(list.Items) != 1 || list.Items[0].Number != calls.Items[0].Number {
			t.Errorf("number in the view: %+v, want %s", list.Items, calls.Items[0].Number)
		}
		call(t, "GET", "/documents?number="+ct.Number, req{tok: tok}).json(t, &list)
		if list.Total != 1 {
			t.Errorf("contract by number: %d", list.Total)
		}
		if r := call(t, "GET", "/documents?number=AL-%25", req{tok: tok}); r.Status != 422 {
			t.Errorf("malformed number: %d", r.Status)
		}
	})

	var grant struct{ ID string }
	t.Run("grants", func(t *testing.T) {
		audited(t, seen, "POST /grants", 401, func() resp {
			return call(t, "POST", "/grants", req{tok: tok, body: map[string]any{"grantee_sub": subAcV, "role": "accountant"}})
		})
		audited(t, seen, "POST /grants", 201, func() resp {
			return call(t, "POST", "/grants", req{tok: freshV(), body: map[string]any{"grantee_sub": subAcV, "role": "accountant"}})
		}).json(t, &grant)
		if r := call(t, "POST", "/grants", req{tok: freshV(), body: map[string]any{"grantee_sub": subAcV, "role": "co_owner"}}); r.Status != 409 {
			t.Errorf("second live grant: %d", r.Status)
		}
		if r := call(t, "POST", "/grants", req{tok: freshV(), body: map[string]any{"grantee_sub": subV, "role": "co_owner"}}); r.Status != 422 {
			t.Errorf("self grant: %d", r.Status)
		}
		if n := superCount(t, "SELECT count(*) FROM audit_events WHERE owner_id = $1 AND action = 'grant.create'", owner.ID); n != 1 {
			t.Errorf("grant not in the owner's audit chain: %d", n)
		}
	})

	t.Run("exports hold only what the caller may read", func(t *testing.T) {
		var e exportView
		audited(t, seen, "POST /documents/export", 202, func() resp {
			return call(t, "POST", "/documents/export", req{tok: freshV(), body: map[string]any{"year": year}})
		}).json(t, &e)
		e = waitExport(t, tok, "", e.ID)
		if e.State != "ready" || e.Link == nil || e.Documents == 0 {
			t.Fatalf("owner export %+v", e)
		}
		rows, names := zipIndex(t, e.Link.URL)
		classes := map[string]bool{}
		for _, r := range rows {
			classes[r[2]] = true
			if superCount(t, "SELECT count(*) FROM documents WHERE id = $1 AND owner_id = $2", r[1], owner.ID) != 1 {
				t.Errorf("document %s of another scope in V's export", r[1])
			}
		}
		if !classes["contract"] || !classes["rent_call"] || len(names) != len(rows)+1 {
			t.Errorf("owner export classes %v, %d entries for %d rows", classes, len(names), len(rows))
		}
		var acc exportView
		call(t, "POST", "/documents/export", req{tok: token(t, subAcV, stepUp(time.Minute)), scope: owner.ID, body: map[string]any{"year": year}}).json(t, &acc)
		acc = waitExport(t, tokAcc, owner.ID, acc.ID)
		rows, _ = zipIndex(t, acc.Link.URL)
		for _, r := range rows {
			if r[2] == "contract" || r[2] == "inventory" || r[2] == "other" {
				t.Errorf("the accountant's export holds a %s", r[2])
			}
		}
		var byAsset exportView
		call(t, "POST", "/documents/export", req{tok: freshV(), body: map[string]any{"asset_id": a1.ID}}).json(t, &byAsset)
		if byAsset = waitExport(t, tok, "", byAsset.ID); byAsset.State != "ready" || byAsset.Documents == 0 {
			t.Errorf("asset export %+v", byAsset)
		}
		if r := call(t, "POST", "/documents/export", req{tok: freshV(), body: map[string]any{"year": year, "asset_id": a1.ID}}); r.Status != 422 {
			t.Errorf("year and asset: %d", r.Status)
		}
		if r := call(t, "GET", "/documents/exports/"+e.ID, req{tok: tokW}); r.Status != 404 {
			t.Errorf("W reads V's export: %d", r.Status)
		}
	})

	t.Run("a revoked grant ends access at once", func(t *testing.T) {
		if r := call(t, "GET", "/documents", req{tok: tokAcc, scope: owner.ID}); r.Status != 200 {
			t.Fatalf("accountant before revoke: %d", r.Status)
		}
		if _, err := env.super.Exec(`UPDATE owner_grants SET role = 'co_owner' WHERE id = $1`, grant.ID); err == nil {
			t.Error("a live grant's role changed")
		}
		audited(t, seen, "DELETE /grants/{id}", 200, func() resp {
			return call(t, "DELETE", "/grants/"+grant.ID, req{tok: freshV()})
		})
		if r := call(t, "GET", "/documents", req{tok: tokAcc, scope: owner.ID}); r.Status != 403 {
			t.Errorf("accountant after revoke: %d %s", r.Status, r.Body)
		}
		if r := call(t, "DELETE", "/grants/"+grant.ID, req{tok: freshV()}); r.Status != 409 {
			t.Errorf("revoked twice: %d", r.Status)
		}
		if n := superCount(t, "SELECT count(*) FROM owner_grants WHERE id = $1 AND revoked_at IS NOT NULL", grant.ID); n != 1 {
			t.Error("a revoked grant is kept, never deleted")
		}
		// The database itself refuses to revive, edit or delete a grant, for
		// every role; only setting revoked_at once is allowed.
		for name, q := range map[string]string{
			"revive":        `UPDATE owner_grants SET revoked_at = NULL WHERE id = $1`,
			"move the date": `UPDATE owner_grants SET revoked_at = now() WHERE id = $1`,
			"delete":        `DELETE FROM owner_grants WHERE id = $1`,
		} {
			if _, err := env.super.Exec(q, grant.ID); err == nil {
				t.Errorf("%s a revoked grant: accepted", name)
			}
		}
	})

	t.Run("RGPD export", func(t *testing.T) {
		if r := call(t, "GET", "/me/export", req{tok: tok}); r.Status != 401 {
			t.Errorf("without step-up: %d", r.Status)
		}
		var ex struct {
			Sub         string
			Owner       *struct{ ID string }
			GrantsGiven []struct {
				GranteeSub string `json:"grantee_sub"`
			} `json:"grants_given"`
		}
		call(t, "GET", "/me/export", req{tok: freshV()}).json(t, &ex)
		if ex.Sub != subV || ex.Owner == nil || ex.Owner.ID != owner.ID || len(ex.GrantsGiven) != 1 {
			t.Errorf("me/export %+v", ex)
		}
	})

	t.Run("archive.verify catches a corrupted object", func(t *testing.T) {
		if status, body := runJob(t, tok, "archive.verify", map[string]any{}); status != "succeeded" {
			t.Fatalf("clean archive: %s %s", status, body)
		}
		var id, sum string
		if err := env.super.QueryRow("SELECT id, sha256 FROM documents WHERE owner_id = $1 ORDER BY issued_at LIMIT 1", owner.ID).Scan(&id, &sum); err != nil {
			t.Fatal(err)
		}
		superExec(t, "ALTER TABLE documents DISABLE TRIGGER documents_immutable")
		superExec(t, "UPDATE documents SET sha256 = repeat('0', 64) WHERE id = $1", id)
		superExec(t, "ALTER TABLE documents ENABLE TRIGGER documents_immutable")
		// A dry run recomputes (a second real run this week returns the first).
		status, body := runJob(t, tok, "archive.verify", map[string]any{"dry_run": true})
		if status == "succeeded" || !strings.Contains(body, id) {
			t.Errorf("corruption not caught: %s %s", status, body)
		}
		superExec(t, "ALTER TABLE documents DISABLE TRIGGER documents_immutable")
		superExec(t, "UPDATE documents SET sha256 = $2 WHERE id = $1", id, sum)
		superExec(t, "ALTER TABLE documents ENABLE TRIGGER documents_immutable")
	})

	t.Run("archive.retention starts clocks and deletes nothing", func(t *testing.T) {
		call(t, "POST", "/leases/"+l1.ID+"/transitions", req{tok: tok, body: map[string]any{"action": "notice", "end_on": "2027-06-30"}})
		call(t, "POST", "/leases/"+l1.ID+"/transitions", req{tok: tok, body: map[string]any{"action": "end"}})
		// A document whose retention is over, for the review list.
		old := uuid.Must(uuid.NewV7())
		superExec(t, `INSERT INTO documents (id, owner_id, class, object_key, sha256, size_bytes, issued_at, retain_until, created_at, created_by)
			VALUES ($1, $2, 'other', 'old', repeat('a', 64), 1, now() - interval '11 years', current_date - 1, now(), 'itest')`, old, owner.ID)
		docs := superCount(t, "SELECT count(*) FROM documents WHERE owner_id = $1", owner.ID)
		status, body := runJob(t, tok, "archive.retention", map[string]any{})
		if status != "succeeded" || !strings.Contains(body, old.String()) || !strings.Contains(body, `"deleted":0`) {
			t.Errorf("retention: %s %s", status, body)
		}
		if n := superCount(t, "SELECT count(*) FROM documents WHERE owner_id = $1", owner.ID); n != docs {
			t.Errorf("retention deleted %d documents", docs-n)
		}
		var key, version string
		var until time.Time
		if err := env.super.QueryRow("SELECT object_key, object_version, retain_until FROM documents WHERE id = $1", ct.DocumentID).Scan(&key, &version, &until); err != nil {
			t.Fatalf("contract retention not started: %v", err)
		}
		// The later of the lease end plus the retention years and the lock
		// the contract got when issued: never a shorter lock.
		// Here the lease ends in mid-2027, so the clock extends the lock.
		if !until.Equal(time.Date(2037, 6, 30, 0, 0, 0, 0, time.UTC)) {
			t.Errorf("contract retain_until %s", until)
		}
		mode, lock, err := env.s3.GetObjectRetention(context.Background(), bucket, key, version)
		if err != nil || mode == nil || lock == nil || lock.Before(until) {
			t.Errorf("object lock %v %v %v; want at least %s", mode, lock, err, until)
		}
		if _, err := env.super.Exec("UPDATE documents SET retain_until = current_date WHERE id = $1", ct.DocumentID); err == nil {
			t.Error("a started retention clock changed again")
		}
	})

	t.Run("isolation", func(t *testing.T) {
		rlsZeroRows(t, owner.ID, ownerW.ID, subV, subW, a1.ID, map[string]string{"export_jobs": "owner_id = $1"})
	})
}
