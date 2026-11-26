//go:build integration

package itest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"

	"github.com/ovander/tenure-api/internal/billing"
	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/document"
	"github.com/ovander/tenure-api/internal/jobs"
	"github.com/ovander/tenure-api/internal/period"
)

const (
	subC = "2001" // owner of the billing scenario
	subD = "2002" // another owner
)

type failingPDF struct{}

func (failingPDF) Convert(context.Context, []byte, []byte) ([]byte, error) {
	return nil, errors.New("pdf engine down (test)")
}

type goldenFee struct {
	OccupiedDays int `json:"occupied_days"`
	DaysInPeriod int `json:"days_in_period"`
	TotalCents   int `json:"total_cents"`
	Lines        []struct {
		Kind        string `json:"kind"`
		AmountCents int    `json:"amount_cents"`
	} `json:"lines"`
}

func goldenMarch(t *testing.T) goldenFee {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/golden/2025/apartment-box-unfurnished-3y.json")
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Steps []struct {
			Name     string          `json:"name"`
			Expected json.RawMessage `json:"expected"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	for _, s := range c.Steps {
		if s.Name == "fee 2025-03 prorated" {
			var f goldenFee
			if err := json.Unmarshal(s.Expected, &f); err != nil {
				t.Fatal(err)
			}
			return f
		}
	}
	t.Fatal("golden step not found")
	return goldenFee{}
}

func TestBilling(t *testing.T) {
	tokC, tokD := token(t, subC), token(t, subD)
	var owner, ownerD, apt, box, tenant, lse idv
	call(t, "PUT", "/me", req{tok: tokC, body: map[string]any{"legal_name": "Bailleur C", "address": "3 quai de Saône, Lyon"}}).json(t, &owner)
	call(t, "PUT", "/me", req{tok: tokD, body: map[string]any{"legal_name": "Bailleur D"}}).json(t, &ownerD)
	call(t, "POST", "/assets", req{tok: tokC, body: map[string]any{"kind": "apartment", "address": "3 quai de Saône, Lyon", "lot": "12"}}).json(t, &apt)
	call(t, "POST", "/assets", req{tok: tokC, body: map[string]any{"kind": "box", "address": "3 quai de Saône, Lyon", "parent_asset_id": apt.ID}}).json(t, &box)
	call(t, "POST", "/tenants", req{tok: tokC, body: map[string]any{"names": []string{"Jeanne Martin"}, "email": "jeanne.martin@example.test", "language": "en"}}).json(t, &tenant)
	r := call(t, "POST", "/leases", req{tok: tokC, body: map[string]any{"kind": "unfurnished", "asset_ids": []string{apt.ID, box.ID},
		"tenants": []map[string]any{{"tenant_id": tenant.ID}}, "start_on": "2025-03-15", "term_months": 36, "rent_cents": 85000,
		"provision_cents": 9000, "deposit_cents": 85000, "due_day": 5, "irl_ref_quarter": "2024-Q4", "irl_ref_value": 14464, "revision_month": 3}})
	r.json(t, &lse)
	readyToActivate(t, tokC, lse.ID)
	if r := call(t, "POST", "/leases/"+lse.ID+"/transitions", req{tok: tokC, body: map[string]any{"action": "activate"}}); r.Status != 200 {
		t.Fatalf("activate: %d %s", r.Status, r.Body)
	}

	t.Run("dry run equals the golden fee", func(t *testing.T) {
		before := superCount(t, "SELECT count(*) FROM audit_events")
		var v billing.RunView
		r := call(t, "POST", "/billing/runs", req{tok: tokC, body: map[string]any{"period": "2025-03", "dry_run": true}})
		if r.Status != 200 {
			t.Fatalf("dry run: %d %s", r.Status, r.Body)
		}
		r.json(t, &v)
		want := goldenMarch(t)
		if len(v.Fees) != 1 || int(v.Fees[0].TotalCents) != want.TotalCents || v.Fees[0].OccupiedDays != want.OccupiedDays ||
			len(v.Fees[0].Lines) != len(want.Lines) || int(v.Fees[0].Lines[0].AmountCents) != want.Lines[0].AmountCents {
			t.Errorf("dry run %+v; golden %+v", v.Fees, want)
		}
		if n := superCount(t, "SELECT count(*) FROM audit_events") - before; n != 0 {
			t.Errorf("dry run wrote %d audit events", n)
		}
		if n := superCount(t, "SELECT count(*) FROM billing_documents WHERE owner_id = $1", owner.ID); n != 0 {
			t.Errorf("dry run issued %d documents", n)
		}
	})

	var rentCall billing.DocView
	t.Run("issue, archive and e-mail", func(t *testing.T) {
		mails := len(env.smtp.Messages())
		audited(t, seen, "POST /billing/runs", 201, func() resp {
			return call(t, "POST", "/billing/runs", req{tok: tokC, body: map[string]any{"period": "2025-03"}})
		})
		var list struct{ Items []billing.DocView }
		call(t, "GET", "/billing/documents?period=2025-03", req{tok: tokC}).json(t, &list)
		if len(list.Items) != 1 || list.Items[0].Kind != billing.KindRentCall || list.Items[0].TotalCents != 51548 || list.Items[0].Generation != 1 {
			t.Fatalf("documents %+v", list.Items)
		}
		rentCall = list.Items[0]
		year := time.Now().In(jobs.Paris()).Year()
		code := document.AssetCode(uuid.MustParse(apt.ID))
		if want := "AL-" + code + "-" + strconv.Itoa(year) + "-0001"; rentCall.Number != want {
			t.Errorf("number %s; want %s", rentCall.Number, want)
		}
		var key, sum, version string
		var retain time.Time
		if err := env.super.QueryRow(`SELECT object_key, sha256, object_version, retain_until FROM documents WHERE id = $1`, rentCall.DocumentID).
			Scan(&key, &sum, &version, &retain); err != nil {
			t.Fatal(err)
		}
		if want := owner.ID + "/rent_call/" + strconv.Itoa(year) + "/" + rentCall.Number + ".pdf"; key != want {
			t.Errorf("object key %s; want %s", key, want)
		}
		obj, err := env.s3.GetObject(context.Background(), bucket, key, minio.GetObjectOptions{VersionID: version})
		if err != nil {
			t.Fatal(err)
		}
		pdf, _ := io.ReadAll(obj)
		if document.SHA256(pdf) != sum || !strings.HasPrefix(string(pdf), "%PDF-") || !strings.Contains(string(pdf), "pdfaid") {
			t.Errorf("stored PDF: sha ok %v, pdf/a %v", document.SHA256(pdf) == sum, strings.Contains(string(pdf), "pdfaid"))
		}
		mode, until, err := env.s3.GetObjectRetention(context.Background(), bucket, key, version)
		if err != nil || mode == nil || *mode != minio.Compliance || until.Year() != time.Now().Year()+10 {
			t.Errorf("retention %v %v %v", mode, until, err)
		}
		if retain.Year() != time.Now().Year()+10 {
			t.Errorf("retain_until %s", retain)
		}
		if err := env.s3.RemoveObject(context.Background(), bucket, key, minio.RemoveObjectOptions{VersionID: version}); err == nil {
			t.Error("a locked document version was deleted")
		}
		got := env.smtp.Messages()
		if len(got) != mails+1 || got[len(got)-1].To[0] != "jeanne.martin@example.test" ||
			!strings.Contains(got[len(got)-1].Data, rentCall.Number+".pdf") || !strings.Contains(got[len(got)-1].Data, "Please find attached") {
			t.Errorf("e-mail not sent in the tenant's language with the PDF: %d new", len(got)-mails)
		}
		if n := superCount(t, "SELECT count(*) FROM document_deliveries WHERE document_id = $1 AND status = 'sent'", rentCall.DocumentID); n != 1 {
			t.Errorf("deliveries sent: %d", n)
		}
	})

	t.Run("re-running the period issues nothing", func(t *testing.T) {
		r := call(t, "POST", "/billing/runs", req{tok: tokC, body: map[string]any{"period": "2025-03"}})
		if r.Status != 200 {
			t.Errorf("second run of the same key: %d", r.Status)
		}
		admin := token(t, subAdmin, withRole("admin"))
		if r := call(t, "POST", "/jobs/billing.monthly/trigger", req{tok: admin, body: map[string]any{"period": "2025-03"}}); r.Status != 201 {
			t.Fatalf("admin run: %d %s", r.Status, r.Body)
		}
		if n := superCount(t, "SELECT count(*) FROM billing_documents WHERE owner_id = $1 AND period = '2025-03'", owner.ID); n != 1 {
			t.Errorf("documents after re-runs: %d", n)
		}
	})

	t.Run("a platform run shows each caller only their own results", func(t *testing.T) {
		admin := token(t, subAdmin, withRole("admin"))
		type run struct {
			Job     string  `json:"job"`
			Period  string  `json:"period"`
			OwnerID *string `json:"owner_id"`
			Summary struct {
				Owners []struct {
					OwnerID string `json:"owner_id"`
					Result  any    `json:"result"`
				} `json:"owners"`
			} `json:"summary"`
		}
		runs := func(tok string) []run {
			t.Helper()
			r := call(t, "GET", "/jobs/runs?size=100", req{tok: tok})
			if r.Status != 200 {
				t.Fatalf("list runs: %d %s", r.Status, r.Body)
			}
			var page struct {
				Items []run `json:"items"`
			}
			r.json(t, &page)
			var out []run
			for _, x := range page.Items {
				if x.Job == "billing.monthly" && x.Period == "2025-03" && x.OwnerID == nil {
					out = append(out, x)
				}
			}
			if len(out) != 1 {
				t.Fatalf("platform runs of billing.monthly 2025-03: %d", len(out))
			}
			return out
		}
		seen := false
		for _, o := range runs(admin)[0].Summary.Owners {
			seen = seen || o.OwnerID == owner.ID
		}
		if !seen {
			t.Fatal("the admin does not see the owner's result")
		}
		mine := runs(tokC)[0].Summary.Owners
		if len(mine) != 1 || mine[0].OwnerID != owner.ID || mine[0].Result == nil {
			t.Errorf("the owner should see exactly their own result: %+v", mine)
		}
		stranger := token(t, "itest-stranger-no-profile")
		if got := runs(stranger)[0].Summary.Owners; len(got) != 0 {
			t.Errorf("a caller with no scope sees %d owner results", len(got))
		}
	})

	t.Run("issued documents are immutable", func(t *testing.T) {
		if _, err := env.super.Exec("UPDATE billing_documents SET total_cents = 1 WHERE id = $1", rentCall.ID); err == nil {
			t.Error("billing document updated")
		}
		if _, err := env.super.Exec("UPDATE documents SET sha256 = repeat('0', 64) WHERE id = $1", rentCall.DocumentID); err == nil {
			t.Error("issued document updated")
		}
		if _, err := env.super.Exec("DELETE FROM documents WHERE id = $1", rentCall.DocumentID); err == nil {
			t.Error("document deleted")
		}
	})

	t.Run("a failed issue gives its number back", func(t *testing.T) {
		broken := env.app.Billing
		broken.Issuer.PDF = failingPDF{}
		ctx := context.Background()
		err := env.jobsDB.InScope(ctx, db.Scope{OwnerID: uuid.MustParse(owner.ID), Role: "job"}, func(ctx context.Context) error {
			_, err := broken.MonthlyOwner(ctx, period.Period{Year: 2025, Month: 4}, period.FromTime(time.Now()), false)
			return err
		})
		if err == nil {
			t.Fatal("issue with a failing PDF engine succeeded")
		}
		if n := superCount(t, "SELECT last_seq FROM document_counters WHERE owner_id = $1 AND class = 'rent_call'", owner.ID); n != 1 {
			t.Errorf("counter after a failure: %d", n)
		}
		call(t, "POST", "/billing/runs", req{tok: tokC, body: map[string]any{"period": "2025-04"}})
		var list struct{ Items []billing.DocView }
		call(t, "GET", "/billing/documents?period=2025-04", req{tok: tokC}).json(t, &list)
		if len(list.Items) != 1 || !strings.HasSuffix(list.Items[0].Number, "-0002") || list.Items[0].TotalCents != 94000 {
			t.Errorf("April after the failure: %+v", list.Items)
		}
	})

	t.Run("cancel and reissue", func(t *testing.T) {
		var res billing.CancelResult
		audited(t, seen, "POST /billing/documents/{id}/cancel", 201, func() resp {
			return call(t, "POST", "/billing/documents/"+rentCall.ID.String()+"/cancel", req{tok: tokC, body: map[string]any{"reason": "erreur de provision"}})
		}).json(t, &res)
		if res.Reissued == nil || res.Cancellation.TotalCents != -51548 || res.Reissued.TotalCents != 51548 {
			t.Fatalf("cancel: %+v", res)
		}
		var c billing.DocView
		call(t, "GET", "/billing/documents/"+res.Cancellation.BillingDocumentID.String(), req{tok: tokC}).json(t, &c)
		var re billing.DocView
		call(t, "GET", "/billing/documents/"+res.Reissued.BillingDocumentID.String(), req{tok: tokC}).json(t, &re)
		if c.CancelsID == nil || *c.CancelsID != rentCall.ID || re.Generation != 2 {
			t.Errorf("cancellation %+v reissue %+v", c, re)
		}
		if r := call(t, "POST", "/billing/documents/"+rentCall.ID.String()+"/cancel", req{tok: tokC, body: map[string]any{"reason": "x"}}); r.problemType(t) != "billing/already-cancelled" {
			t.Errorf("second cancel: %d %s", r.Status, r.Body)
		}
		if r := call(t, "POST", "/billing/documents/"+c.ID.String()+"/cancel", req{tok: tokC, body: map[string]any{"reason": "x"}}); r.problemType(t) != "billing/not-cancellable" {
			t.Errorf("cancel a cancellation: %d %s", r.Status, r.Body)
		}
		if r := call(t, "POST", "/billing/documents/"+re.ID.String()+"/cancel", req{tok: tokC, body: map[string]any{"reason": " "}}); r.problemType(t) != "billing/cancel-reason-required" {
			t.Errorf("cancel without reason: %d %s", r.Status, r.Body)
		}
	})

	t.Run("signed download link", func(t *testing.T) {
		before := superCount(t, "SELECT count(*) FROM audit_events WHERE action = 'document.download'")
		var l document.Link
		r := call(t, "GET", "/documents/"+rentCall.DocumentID.String()+"/content", req{tok: tokC})
		if r.Status != 200 {
			t.Fatalf("content: %d %s", r.Status, r.Body)
		}
		r.json(t, &l)
		u, _ := url.Parse(l.URL)
		if u.Query().Get("X-Amz-Expires") != "600" || u.Query().Get("x-tenure-sub") != subC || time.Until(l.ExpiresAt) > 10*time.Minute {
			t.Errorf("link %s expires %s", l.URL, l.ExpiresAt)
		}
		res, err := http.Get(l.URL)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()
		var sum string
		_ = env.super.QueryRow("SELECT sha256 FROM documents WHERE id = $1", rentCall.DocumentID).Scan(&sum)
		if res.StatusCode != 200 || document.SHA256(body) != sum {
			t.Errorf("download: %d", res.StatusCode)
		}
		q := u.Query()
		q.Set("x-tenure-sub", subD)
		u.RawQuery = q.Encode()
		if res, err := http.Get(u.String()); err != nil || res.StatusCode != 403 {
			t.Errorf("link rebound to another subject: %v %v", res.StatusCode, err)
		}
		if r := call(t, "GET", "/documents/"+rentCall.DocumentID.String()+"/content", req{tok: tokD}); r.Status != 404 {
			t.Errorf("another owner gets a link: %d", r.Status)
		}
		if n := superCount(t, "SELECT count(*) FROM audit_events WHERE action = 'document.download'") - before; n != 1 {
			t.Errorf("download events: %d", n)
		}
	})

	t.Run("revision from INSEE", func(t *testing.T) {
		admin := token(t, subAdmin, withRole("admin"))
		mails := len(env.smtp.Messages())
		audited(t, seen, "POST /jobs/{name}/trigger", 201, func() resp {
			return call(t, "POST", "/jobs/irl.fetch/trigger", req{tok: admin, body: map[string]any{}})
		})
		if n := superCount(t, "SELECT count(*) FROM irl_index WHERE quarter IN ('2025-Q4', '2025-Q2', '2024-Q4')"); n != 3 {
			t.Errorf("cached quarters: %d", n)
		}
		var l struct {
			RentCents     int64  `json:"rent_cents"`
			IRLRefQuarter string `json:"irl_ref_quarter"`
		}
		call(t, "GET", "/leases/"+lse.ID, req{tok: tokC}).json(t, &l)
		// 85000 × 14610 ÷ 14464 = 85857.99 → 85858
		if l.RentCents != 85858 || l.IRLRefQuarter != "2025-Q4" {
			t.Errorf("revised lease: %+v", l)
		}
		var list struct{ Items []billing.DocView }
		call(t, "GET", "/billing/documents?kind=revision_notice", req{tok: tokC}).json(t, &list)
		if len(list.Items) != 1 || !strings.HasPrefix(list.Items[0].Number, "RV-") {
			t.Errorf("revision notice: %+v", list.Items)
		}
		if len(env.smtp.Messages()) != mails+1 {
			t.Errorf("revision e-mails: %d", len(env.smtp.Messages())-mails)
		}
		if r := call(t, "POST", "/leases/"+lse.ID+"/revision", req{tok: tokC, body: map[string]any{"dry_run": true}}); r.problemType(t) != "billing/no-revision-due" {
			t.Errorf("revision due twice: %d %s", r.Status, r.Body)
		}
	})

	t.Run("manual IRL override", func(t *testing.T) {
		admin := token(t, subAdmin, withRole("admin"), stepUp(time.Minute))
		audited(t, seen, "PUT /params/irl/{quarter}", 201, func() resp {
			return call(t, "PUT", "/params/irl/2026-Q1", req{tok: admin, body: map[string]any{"value": 14700, "published_on": "2026-04-15", "source": "itest"}})
		})
		if r := call(t, "PUT", "/params/irl/2025-Q4", req{tok: admin, body: map[string]any{"value": 1, "published_on": "2026-01-15", "source": "itest"}}); r.problemType(t) != "params/irl-quarter-exists" {
			t.Errorf("override of a cached quarter: %d %s", r.Status, r.Body)
		}
	})

	t.Run("revision on demand", func(t *testing.T) {
		// A second lease whose revision is due: the owner applies it by hand.
		var a2, l2 idv
		call(t, "POST", "/assets", req{tok: tokC, body: map[string]any{"kind": "apartment", "address": "5 quai de Saône, Lyon"}}).json(t, &a2)
		call(t, "POST", "/leases", req{tok: tokC, body: map[string]any{"kind": "unfurnished", "asset_ids": []string{a2.ID},
			"tenants": []map[string]any{{"tenant_id": tenant.ID}}, "start_on": "2024-07-01", "term_months": 36, "rent_cents": 70000,
			"due_day": 5, "irl_ref_quarter": "2024-Q2", "irl_ref_value": 14517, "revision_month": 7}}).json(t, &l2)
		readyToActivate(t, tokC, l2.ID)
		if r := call(t, "POST", "/leases/"+l2.ID+"/transitions", req{tok: tokC, body: map[string]any{"action": "activate"}}); r.Status != 200 {
			t.Fatalf("activate: %d %s", r.Status, r.Body)
		}
		audited(t, seen, "POST /leases/{id}/revision", 200, func() resp {
			return call(t, "POST", "/leases/"+l2.ID+"/revision", req{tok: tokC, body: map[string]any{}})
		})
		// The July 2025 anniversary is more than 12 months before the first
		// period it could apply to (the next billing period): forfeited, rent
		// unchanged, reference moved on, no notice (Q-13).
		var l struct {
			RentCents     int64  `json:"rent_cents"`
			IRLRefQuarter string `json:"irl_ref_quarter"`
		}
		call(t, "GET", "/leases/"+l2.ID, req{tok: tokC}).json(t, &l)
		if l.RentCents != 70000 || l.IRLRefQuarter != "2025-Q2" {
			t.Errorf("forfeited revision: %+v", l)
		}
		if n := superCount(t, "SELECT count(*) FROM billing_documents WHERE lease_id = $1 AND kind = 'revision_notice'", l2.ID); n != 0 {
			t.Errorf("notice for a forfeited revision: %d", n)
		}
	})

	t.Run("audit heads", func(t *testing.T) {
		admin := token(t, subAdmin, withRole("admin"))
		if r := call(t, "POST", "/jobs/audit.head/trigger", req{tok: admin, body: map[string]any{}}); r.Status != 201 {
			t.Fatalf("audit.head: %d %s", r.Status, r.Body)
		}
		var heads struct{ Items []map[string]any }
		call(t, "GET", "/audit/heads", req{tok: tokC}).json(t, &heads)
		if len(heads.Items) != 1 {
			t.Fatalf("heads: %+v", heads.Items)
		}
		key := heads.Items[0]["object_key"].(string)
		if !strings.HasPrefix(key, owner.ID+"/audit/") {
			t.Errorf("head key %s", key)
		}
		st, err := env.s3.StatObject(context.Background(), bucket, key, minio.StatObjectOptions{})
		if err != nil || st.Size == 0 {
			t.Errorf("head object: %v", err)
		}
		uid := uuid.MustParse(owner.ID)
		run, _, err := env.app.Runner.Trigger(context.Background(), "audit.verify", jobs.Options{Owner: &uid})
		if err != nil || run.Status != jobs.StatusSucceeded || !strings.Contains(string(run.Summary), `"heads_checked":1`) ||
			!strings.Contains(string(run.Summary), `"stored_heads_checked":1`) {
			t.Errorf("verify with heads: %s %s %v", run.Status, run.Summary, err)
		}
		// A head in the bucket that no longer matches the chain (as after a
		// consistent rewrite of the chain in the database) fails the check,
		// even though the head rows still match.
		forged := fmt.Sprintf(`{"owner_id":%q,"as_of":"2000-01-01","last_event_id":%d,"head_hash":%q}`,
			owner.ID, int64(heads.Items[0]["last_event_id"].(float64)), strings.Repeat("0", 64))
		info, err := env.s3.PutObject(context.Background(), bucket, key, strings.NewReader(forged), int64(len(forged)), minio.PutObjectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = env.s3.RemoveObject(context.Background(), bucket, key, minio.RemoveObjectOptions{VersionID: info.VersionID})
		})
		run, _, err = env.app.Runner.Trigger(context.Background(), "audit.verify", jobs.Options{Owner: &uid, DryRun: true})
		if err != nil || run.Status != jobs.StatusFailed || !strings.Contains(string(run.Summary), "stored head") {
			t.Errorf("verify with a forged stored head: %s %s %v", run.Status, run.Summary, err)
		}
		_ = env.s3.RemoveObject(context.Background(), bucket, key, minio.RemoveObjectOptions{VersionID: info.VersionID})
		// A head row whose object is missing from the bucket fails the check
		// too, though the row itself matches the chain.
		superExec(t, `INSERT INTO audit_chain_heads (owner_id, as_of, last_event_id, head_hash, object_key)
			SELECT owner_id, as_of - 1, last_event_id, head_hash, owner_id::text || '/audit/missing.json' FROM audit_chain_heads
			WHERE owner_id = $1 ORDER BY as_of DESC LIMIT 1`, owner.ID)
		t.Cleanup(func() {
			_, _ = env.super.Exec(`DELETE FROM audit_chain_heads WHERE owner_id = $1 AND object_key LIKE '%/audit/missing.json'`, owner.ID)
		})
		run, _, err = env.app.Runner.Trigger(context.Background(), "audit.verify", jobs.Options{Owner: &uid, DryRun: true})
		if err != nil || run.Status != jobs.StatusFailed || !strings.Contains(string(run.Summary), "missing from the bucket") {
			t.Errorf("verify with a head object missing: %s %s %v", run.Status, run.Summary, err)
		}
	})

	t.Run("isolation of the new tables", func(t *testing.T) {
		rlsZeroRows(t, owner.ID, ownerD.ID, subC, subD, apt.ID, map[string]string{
			"billing_documents": "owner_id = $1", "document_counters": "owner_id = $1", "document_deliveries": "owner_id = $1",
			"documents": "owner_id = $1", "audit_chain_heads": "owner_id = $1",
		})
		var l struct{ Total int }
		call(t, "GET", "/billing/documents", req{tok: tokD}).json(t, &l)
		if l.Total != 0 {
			t.Errorf("owner D sees %d billing documents", l.Total)
		}
	})
}
