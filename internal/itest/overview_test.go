//go:build integration

package itest

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
)

const (
	subOverview    = "9990" // owner of the month cockpit scenario (RENT-FE-001 §4b, Q-139)
	subOverviewX   = "9991" // another owner, who reads nothing of it
	subOverviewAcc = "9992" // the owner's accountant
	subOverviewCo  = "9993" // the owner's co-owner
)

type overviewView struct {
	Period   string `json:"period"`
	Today    string `json:"today"`
	UpToDate bool   `json:"up_to_date"`
	Steps    []struct {
		Key    string           `json:"key"`
		State  string           `json:"state"`
		Counts map[string]int64 `json:"counts"`
		On     *string          `json:"on"`
	} `json:"steps"`
	Anomalies []struct {
		Kind        string `json:"kind"`
		Severity    string `json:"severity"`
		LeaseID     string `json:"lease_id"`
		AssetID     string `json:"asset_id"`
		DocumentID  string `json:"document_id"`
		Ref         string `json:"ref"`
		AmountCents int64  `json:"amount_cents"`
		Since       string `json:"since"`
		Days        int    `json:"days"`
	} `json:"anomalies"`
}

func (v overviewView) state(key string) string {
	for _, s := range v.Steps {
		if s.Key == key {
			return s.State
		}
	}
	return ""
}

func (v overviewView) count(key, name string) int64 {
	for _, s := range v.Steps {
		if s.Key == key {
			return s.Counts[name]
		}
	}
	return -1
}

// has says an anomaly of kind concerns lease (or asset, or ref) when given.
func (v overviewView) has(kind, lease, ref string) bool {
	for _, a := range v.Anomalies {
		if a.Kind == kind && (lease == "" || a.LeaseID == lease || a.AssetID == lease) && (ref == "" || a.Ref == ref) {
			return true
		}
	}
	return false
}

// TestOverview reads the month cockpit (RENT-FE-001 §4b, Q-139) on a real
// scope, step by step: a past month's rent call missing, then issued and
// overdue with its recovery step due; paid, its receipt awaited, then late,
// then issued; the bank without an account, without a statement, with a
// stale line, then reconciled; and the anomalies of the scope — a failed job
// run, an IRL revision due, an exercise to regularise, an entry inventory
// missing, a diagnostic expiring, a deposit deadline. Every role of the
// scope reads it, another owner reads nothing, a bad period is refused, and
// a read writes no audit event.
func TestOverview(t *testing.T) {
	tok := token(t, subOverview)
	ps := fixtureParams(t)
	th, err := ps.Overview(today())
	if err != nil {
		t.Fatal(err)
	}
	bankRule, err := ps.Bank(today())
	if err != nil {
		t.Fatal(err)
	}
	var owner, ten, a1, l1 idv
	call(t, "PUT", "/me", req{tok: tok, body: map[string]any{"legal_name": "Bailleur Cockpit", "address": "4 rue C, Lyon"}}).json(t, &owner)
	call(t, "POST", "/tenants", req{tok: tok, body: map[string]any{"names": []string{"Léa Martin"}, "email": "lea.cockpit@example.test"}}).json(t, &ten)
	call(t, "POST", "/assets", req{tok: tok, body: map[string]any{"kind": "apartment", "address": "4 rue C, Lyon"}}).json(t, &a1)
	// A lease since 2024 whose reference quarter is 2023-Q3, which no other
	// scenario uses: its revision needs the 2024-Q3 index, cached below once
	// its month is billed.
	r := call(t, "POST", "/leases", req{tok: tok, body: map[string]any{"kind": "unfurnished", "asset_ids": []string{a1.ID},
		"tenants": []map[string]any{{"tenant_id": ten.ID}}, "start_on": "2024-01-01", "term_months": 36, "rent_cents": 70000,
		"provision_cents": 5000, "due_day": 5, "irl_ref_quarter": "2023-Q3", "irl_ref_value": 14159, "revision_month": 1}})
	if r.Status != 201 {
		t.Fatalf("lease: %d %s", r.Status, r.Body)
	}
	r.json(t, &l1)
	superExec(t, `UPDATE leases SET state = 'active' WHERE id = $1`, l1.ID)
	// A second lease, started this month, its entry completed: under notice
	// with its exit issued below, for the deposit deadline.
	lE, entry := completedEntry(t, tok, ten.ID, "6 rue C, Lyon")
	superExec(t, `UPDATE leases SET state = 'active', start_on = current_date - 20 WHERE id = $1`, lE)

	per := today().AddMonthsClamped(-2).Period().String()
	get := func(q string) overviewView {
		t.Helper()
		r := call(t, "GET", "/overview/month"+q, req{tok: tok})
		if r.Status != 200 {
			t.Fatalf("overview%s: %d %s", q, r.Status, r.Body)
		}
		var v overviewView
		r.json(t, &v)
		return v
	}

	// 1. A past month, its rent call not issued.
	v := get("?period=" + per)
	if v.Period != per || v.Today != today().String() || v.state("rent_calls") != "to_do" || v.count("rent_calls", "missing") != 1 ||
		!v.has("rent_call_missing", l1.ID, "") || v.UpToDate {
		t.Fatalf("before the run %+v", v)
	}
	if v.state("bank") != "nothing" || v.state("recovery") != "done" || !v.has("inventory_missing", l1.ID, "") {
		t.Errorf("bank, recovery, inventory %+v", v)
	}
	if r := call(t, "POST", "/billing/runs", req{tok: tok, body: map[string]any{"period": per}}); r.Status != 201 {
		t.Fatalf("billing run: %d %s", r.Status, r.Body)
	}
	v = get("?period=" + per)
	if v.state("rent_calls") != "done" || v.has("rent_call_missing", "", "") {
		t.Errorf("after the run %+v", v)
	}
	// 2. Overdue, its recovery step due.
	if v.state("payments") != "to_do" || v.count("payments", "unpaid") != 1 || v.count("payments", "late") != 1 ||
		!v.has("payment_overdue", l1.ID, "") || v.state("recovery") != "to_do" || v.count("recovery", "in_arrears") != 1 {
		t.Errorf("overdue %+v", v)
	}
	// The accountant and the co-owner read the same, recovery included,
	// their reads locking nothing (Q-139).
	now := time.Now().UTC()
	for _, g := range []struct{ sub, role string }{{subOverviewAcc, "accountant"}, {subOverviewCo, "co_owner"}} {
		superExec(t, `INSERT INTO owner_grants (id, owner_id, grantee_sub, role, granted_at, created_by, updated_at, updated_by)
			VALUES ($1, $2, $3, $4, $5, 'itest', $5, 'itest')`, uuid.Must(uuid.NewV7()), owner.ID, g.sub, g.role, now)
		r := call(t, "GET", "/overview/month?period="+per, req{tok: token(t, g.sub), scope: owner.ID})
		var w overviewView
		if r.Status != 200 {
			t.Fatalf("%s reads: %d %s", g.role, r.Status, r.Body)
		}
		r.json(t, &w)
		if w.state("recovery") != "to_do" || !w.has("payment_overdue", l1.ID, "") {
			t.Errorf("%s sees %+v", g.role, w)
		}
	}
	for _, a := range v.Anomalies {
		if a.Kind == "payment_overdue" && (a.AmountCents != 75000 || a.Severity != "to_act" || a.Days <= th.OverdueGraceDays) {
			t.Errorf("payment_overdue %+v", a)
		}
	}
	// 3. Paid: the receipt awaited within the delay, then late, then issued.
	var pay struct {
		Matches []struct {
			ID string `json:"id"`
		} `json:"matches"`
	}
	call(t, "POST", "/payments", req{tok: tok, body: map[string]any{"lease_id": l1.ID, "paid_on": today().String(),
		"amount_cents": 75000, "method": "transfer", "auto_match": true}}).json(t, &pay)
	if len(pay.Matches) != 1 {
		t.Fatalf("payment %+v", pay)
	}
	v = get("?period=" + per)
	if v.state("payments") != "done" || v.state("receipts") != "waiting" || v.has("receipt_missing", "", "") ||
		v.state("recovery") != "done" || v.has("payment_overdue", "", "") {
		t.Errorf("paid %+v", v)
	}
	superExec(t, `UPDATE payment_matches SET created_at = now() - make_interval(hours => $2) WHERE id = $1`, pay.Matches[0].ID, th.ReceiptDelayHours+1)
	v = get("?period=" + per)
	if v.state("receipts") != "to_do" || !v.has("receipt_missing", l1.ID, "") {
		t.Errorf("receipt late %+v", v)
	}
	if r := call(t, "POST", "/jobs/billing.receipts/trigger", req{tok: tok, body: map[string]any{}}); r.Status != 201 {
		t.Fatalf("receipts: %d %s", r.Status, r.Body)
	}
	v = get("?period=" + per)
	if v.state("receipts") != "done" || v.count("receipts", "issued") != 1 || v.has("receipt_missing", "", "") {
		t.Errorf("receipted %+v", v)
	}

	// 4. The bank: an account without a statement, then a stale line.
	var acc idv
	call(t, "POST", "/bank-accounts", req{tok: tok, body: map[string]any{"label": "Compte cockpit", "iban_last4": "0042"}}).json(t, &acc)
	if v = get(""); v.state("bank") != "to_do" {
		t.Errorf("an account without a statement %+v", v)
	}
	fresh, stale := today().AddDays(-1), today().AddDays(-bankRule.UnmatchedAlertDays-3)
	csv := fmt.Sprintf("Date;Libellé;Débit;Crédit\n%02d/%02d/%d;VIR INCONNU;;12,00\n%02d/%02d/%d;PRLV ANCIEN;30,00;\n",
		fresh.Day, int(fresh.Month), fresh.Year, stale.Day, int(stale.Month), stale.Year)
	var doc idv
	upload(t, tok, "", map[string]string{"class": "supporting"}, "releve-cockpit.csv", []byte(csv)).json(t, &doc)
	if r := call(t, "POST", "/payments/import", req{tok: tok, body: map[string]any{"account_id": acc.ID, "document_id": doc.ID, "format": "csv",
		"mapping": map[string]any{"date": "Date", "label": "Libellé", "debit": "Débit", "credit": "Crédit"}}}); r.Status != 200 && r.Status != 201 {
		t.Fatalf("import: %d %s", r.Status, r.Body)
	}
	v = get("")
	if v.state("bank") != "to_do" || v.count("bank", "unmatched") != 2 || v.count("bank", "stale") != 1 || !v.has("bank_line_stale", "", "") {
		t.Errorf("a stale line %+v", v)
	}
	var lines struct {
		Items []struct {
			ID       string `json:"id"`
			BookedOn string `json:"booked_on"`
			Version  int    `json:"version"`
		} `json:"items"`
	}
	call(t, "GET", "/bank-lines?status=unmatched", req{tok: tok}).json(t, &lines)
	for _, l := range lines.Items {
		if l.BookedOn == stale.String() {
			if r := call(t, "POST", "/bank-lines/"+l.ID+"/status", req{tok: tok, body: map[string]any{"status": "ignored", "note": "frais", "version": l.Version}}); r.Status != 200 {
				t.Fatalf("ignore: %d %s", r.Status, r.Body)
			}
		}
	}
	if v = get(""); v.state("bank") != "done" || v.has("bank_line_stale", "", "") {
		t.Errorf("reconciled %+v", v)
	}

	// The anomalies of the scope.
	superExec(t, `INSERT INTO job_runs (id, job, period, owner_id, started_at, finished_at, status, summary)
		VALUES ($1, 'billing.reminders', $2, $3, $4, $4, 'failed', '{}')`, uuid.Must(uuid.NewV7()), "cockpit-"+owner.ID[:8], owner.ID, now)
	superExec(t, `INSERT INTO job_runs (id, job, period, owner_id, dry_run, started_at, finished_at, status, summary)
		VALUES ($1, 'billing.receipts', $2, $3, true, $4, $4, 'failed', '{}')`, uuid.Must(uuid.NewV7()), "cockpit-dry-"+owner.ID[:8], owner.ID, now)
	// The revision as not applied yet, its index cached (removed afterwards
	// when this scenario added it).
	superExec(t, `UPDATE leases SET irl_ref_quarter = '2023-Q3', irl_ref_value = 14159, rent_cents = 70000 WHERE id = $1`, l1.ID)
	res, err := env.super.Exec(`INSERT INTO irl_index (quarter, value, published_on) VALUES ('2024-Q3', 14488, '2024-10-15') ON CONFLICT (quarter) DO NOTHING`)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := res.RowsAffected(); n == 1 {
		t.Cleanup(func() { superExec(t, `DELETE FROM irl_index WHERE quarter = '2024-Q3'`) })
	}
	charged := fmt.Sprintf("%d-06-01", today().Year-2)
	if r := call(t, "POST", "/charges", req{tok: tok, body: map[string]any{"asset_id": a1.ID, "charged_on": charged,
		"category": "teom", "label": "TEOM", "amount_cents": 15000, "recoverable_cents": 15000}}); r.Status != 201 {
		t.Fatalf("charge: %d %s", r.Status, r.Body)
	}
	rule, _ := ps.Diagnostics(today())
	months := rule.ValidityMonths["erp"]
	if r := call(t, "POST", "/assets/"+a1.ID+"/diagnostics", req{tok: tok, body: map[string]any{"kind": "erp",
		"done_on": today().AddMonths(-months).AddDays(20).String()}}); r.Status != 201 {
		t.Fatalf("diagnostic: %d %s", r.Status, r.Body)
	}
	issuedExit(t, tok, "Bailleur Cockpit", lE, entry)
	v = get("")
	for _, c := range []struct{ kind, about, ref, severity string }{
		{"job_failed", "", "billing.reminders", "blocking"},
		{"irl_revision_due", l1.ID, "", "to_act"},
		{"regularisation_due", a1.ID, fmt.Sprint(today().Year - 2), "to_act"},
		{"inventory_missing", l1.ID, "", "to_act"},
		{"diagnostic_expiring", a1.ID, "erp", "to_watch"},
		{"deposit_deadline", lE, "", "to_watch"},
	} {
		if !v.has(c.kind, c.about, c.ref) {
			t.Errorf("%s %s %s missing: %+v", c.kind, c.about, c.ref, v.Anomalies)
		}
	}
	if v.has("job_failed", "", "billing.receipts") {
		t.Errorf("a dry run is not an anomaly %+v", v.Anomalies)
	}
	if v.has("inventory_missing", lE, "") {
		t.Errorf("the exited lease has its entry issued %+v", v.Anomalies)
	}
	last := -1
	rank := map[string]int{"blocking": 0, "to_act": 1, "to_watch": 2}
	for _, a := range v.Anomalies {
		if rank[a.Severity] < last {
			t.Errorf("not sorted by severity %+v", v.Anomalies)
		}
		last = rank[a.Severity]
	}

	// Every role of the scope reads the same cockpit; another owner, nothing.
	for _, g := range []struct{ sub, role string }{{subOverviewAcc, "accountant"}, {subOverviewCo, "co_owner"}} {
		r := call(t, "GET", "/overview/month", req{tok: token(t, g.sub), scope: owner.ID})
		var w overviewView
		if r.Status != 200 {
			t.Fatalf("%s reads: %d %s", g.role, r.Status, r.Body)
		}
		r.json(t, &w)
		if len(w.Anomalies) != len(v.Anomalies) || len(w.Steps) != 5 || !w.has("deposit_deadline", lE, "") {
			t.Errorf("%s sees %+v", g.role, w)
		}
	}
	var x idv
	call(t, "PUT", "/me", req{tok: token(t, subOverviewX), body: map[string]any{"legal_name": "Bailleur X", "address": "1 rue X"}}).json(t, &x)
	var xv overviewView
	call(t, "GET", "/overview/month?period="+per, req{tok: token(t, subOverviewX)}).json(t, &xv)
	if xv.state("rent_calls") != "nothing" || xv.count("payments", "called_cents") != 0 || xv.state("bank") != "nothing" {
		t.Errorf("another owner %+v", xv)
	}
	for _, a := range xv.Anomalies {
		if a.LeaseID != "" || a.AssetID != "" {
			t.Errorf("another owner sees %+v", a)
		}
	}
	if r := call(t, "GET", "/overview/month?period=2026-13", req{tok: tok}); r.Status != 400 {
		t.Errorf("bad period: %d", r.Status)
	}
	if r := call(t, "GET", "/overview/month", req{tok: tok, scope: x.ID}); r.Status != 403 {
		t.Errorf("an ungranted scope: %d", r.Status)
	}
	// A future month only shows its rent calls to come.
	if v := get("?period=" + today().AddMonthsClamped(1).Period().String()); v.state("rent_calls") != "waiting" || v.has("rent_call_missing", "", "") {
		t.Errorf("next month %+v", v)
	}
	// A read writes nothing.
	before := superCount(t, `SELECT count(*) FROM audit_events WHERE owner_id = $1`, owner.ID)
	get("")
	get("?period=" + per)
	if after := superCount(t, `SELECT count(*) FROM audit_events WHERE owner_id = $1`, owner.ID); after != before {
		t.Errorf("audit events of a read: %d, then %d", before, after)
	}
}
