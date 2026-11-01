//go:build integration

package itest

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/billing"
	"github.com/ovander/tenure-api/internal/db"
	"github.com/ovander/tenure-api/internal/payment"
)

const (
	subE = "3001" // owner of the payments scenario
	subF = "3002" // another owner
)

func TestPayments(t *testing.T) {
	tokE, tokF := token(t, subE), token(t, subF)
	var owner, ownerF, a1, a2, t1, t2, l1, l2 idv
	call(t, "PUT", "/me", req{tok: tokE, body: map[string]any{"legal_name": "Bailleur E", "address": "8 rue Mercière, Lyon"}}).json(t, &owner)
	call(t, "PUT", "/me", req{tok: tokF, body: map[string]any{"legal_name": "Bailleur F"}}).json(t, &ownerF)
	call(t, "POST", "/assets", req{tok: tokE, body: map[string]any{"kind": "apartment", "address": "8 rue Mercière, Lyon"}}).json(t, &a1)
	call(t, "POST", "/assets", req{tok: tokE, body: map[string]any{"kind": "apartment", "address": "10 rue Mercière, Lyon"}}).json(t, &a2)
	call(t, "POST", "/tenants", req{tok: tokE, body: map[string]any{"names": []string{"Paul Petit"}, "email": "paul.petit@example.test"}}).json(t, &t1)
	call(t, "POST", "/tenants", req{tok: tokE, body: map[string]any{"names": []string{"Lise Grand"}, "email": "lise.grand@example.test"}}).json(t, &t2)
	mk := func(asset, tenant string, dst *idv) {
		call(t, "POST", "/leases", req{tok: tokE, body: map[string]any{"kind": "unfurnished", "asset_ids": []string{asset},
			"tenants": []map[string]any{{"tenant_id": tenant}}, "start_on": "2025-01-01", "term_months": 36, "rent_cents": 80000,
			"provision_cents": 10000, "due_day": 5, "irl_ref_quarter": "2024-Q3", "irl_ref_value": 14451, "revision_month": 1}}).json(t, dst)
		if r := call(t, "POST", "/leases/"+dst.ID+"/transitions", req{tok: tokE, body: map[string]any{"action": "activate"}}); r.Status != 200 {
			t.Fatalf("activate: %d %s", r.Status, r.Body)
		}
	}
	mk(a1.ID, t1.ID, &l1)
	mk(a2.ID, t2.ID, &l2)
	for _, per := range []string{"2025-01", "2025-02"} {
		if r := call(t, "POST", "/billing/runs", req{tok: tokE, body: map[string]any{"period": per}}); r.Status != 201 {
			t.Fatalf("billing %s: %d %s", per, r.Status, r.Body)
		}
	}
	calls := func(lease string) map[string]billing.DocView {
		var list struct{ Items []billing.DocView }
		call(t, "GET", "/billing/documents?kind=rent_call&lease_id="+lease, req{tok: tokE}).json(t, &list)
		out := map[string]billing.DocView{}
		for _, d := range list.Items {
			out[d.Period] = d
		}
		return out
	}
	c1, c2 := calls(l1.ID), calls(l2.ID)
	if len(c1) != 2 || len(c2) != 2 || c1["2025-01"].TotalCents != 90000 {
		t.Fatalf("rent calls %+v %+v", c1, c2)
	}

	var p1, p3 payment.View
	t.Run("record and match", func(t *testing.T) {
		audited(t, seen, "POST /payments", 201, func() resp {
			return call(t, "POST", "/payments", req{tok: tokE, body: map[string]any{"lease_id": l1.ID, "paid_on": "2025-01-04",
				"amount_cents": 90000, "method": "transfer", "reference": "VIR JANV", "auto_match": true}})
		}).json(t, &p1)
		if len(p1.Matches) != 1 || p1.Matches[0].BillingDocumentID != c1["2025-01"].ID || p1.UnmatchedCents != 0 {
			t.Fatalf("auto match: %+v", p1)
		}
		// One payment split across two rent calls of lease 2.
		audited(t, seen, "POST /payments", 201, func() resp {
			return call(t, "POST", "/payments", req{tok: tokE, body: map[string]any{"lease_id": l2.ID, "paid_on": "2025-02-03",
				"amount_cents": 100000, "method": "cheque"}})
		}).json(t, &p3)
		audited(t, seen, "POST /payments/{id}/matches", 200, func() resp {
			return call(t, "POST", "/payments/"+p3.ID.String()+"/matches", req{tok: tokE, body: map[string]any{"matches": []map[string]any{
				{"billing_document_id": c2["2025-01"].ID, "amount_cents": 90000}, {"billing_document_id": c2["2025-02"].ID, "amount_cents": 10000}}}})
		}).json(t, &p3)
		if p3.MatchedCents != 100000 || len(p3.Matches) != 2 {
			t.Errorf("split: %+v", p3)
		}
	})

	t.Run("over-matching is refused", func(t *testing.T) {
		var p4 payment.View
		call(t, "POST", "/payments", req{tok: tokE, body: map[string]any{"lease_id": l1.ID, "paid_on": "2025-02-10", "amount_cents": 50000, "method": "cash"}}).json(t, &p4)
		cases := map[string][]map[string]any{
			"payment/document-over-paid":   {{"billing_document_id": c1["2025-01"].ID, "amount_cents": 1}},
			"payment/over-matched":         {{"billing_document_id": c1["2025-02"].ID, "amount_cents": 60000}},
			"payment/not-a-live-rent-call": {{"billing_document_id": c2["2025-02"].ID, "amount_cents": 100}},
		}
		for want, ms := range cases {
			audited(t, seen, "POST /payments/{id}/matches", 422, func() resp {
				r := call(t, "POST", "/payments/"+p4.ID.String()+"/matches", req{tok: tokE, body: map[string]any{"matches": ms}})
				if r.problemType(t) != want {
					t.Errorf("want %s, got %s", want, r.Body)
				}
				return r
			})
		}
		if r := call(t, "POST", "/payments", req{tok: tokE, body: map[string]any{"lease_id": l1.ID, "paid_on": "2999-01-01", "amount_cents": 1, "method": "cash"}}); r.Status != 422 {
			t.Errorf("payment in the future: %d", r.Status)
		}
		if r := call(t, "POST", "/payments", req{tok: tokF, body: map[string]any{"lease_id": l1.ID, "paid_on": "2025-02-10", "amount_cents": 1, "method": "cash"}}); r.Status != 422 {
			t.Errorf("payment on another owner's lease: %d %s", r.Status, r.Body)
		}
	})

	t.Run("receipts", func(t *testing.T) {
		mails := len(env.smtp.Messages())
		if r := call(t, "POST", "/jobs/billing.receipts/trigger", req{tok: tokE, body: map[string]any{}}); r.Status != 201 {
			t.Fatalf("receipts: %d %s", r.Status, r.Body)
		}
		var list struct{ Items []billing.DocView }
		call(t, "GET", "/billing/documents?kind=receipt", req{tok: tokE}).json(t, &list)
		if len(list.Items) != 2 {
			t.Fatalf("quittances: %+v", list.Items)
		}
		for _, q := range list.Items {
			if !strings.HasPrefix(q.Number, "QL-") || q.Period != "2025-01" || q.TotalCents != 90000 {
				t.Errorf("quittance %+v", q)
			}
		}
		var partial struct{ Items []billing.DocView }
		call(t, "GET", "/billing/documents?kind=partial_receipt", req{tok: tokE}).json(t, &partial)
		if len(partial.Items) != 1 || partial.Items[0].Period != "2025-02" || partial.Items[0].TotalCents != 10000 || partial.Items[0].LeaseID.String() != l2.ID {
			t.Fatalf("partial receipts: %+v", partial.Items)
		}
		if n := superCount(t, "SELECT count(*) FROM billing_documents WHERE kind = 'receipt' AND lease_id = $1 AND period = '2025-02'", l2.ID); n != 0 {
			t.Error("a partly paid month got a quittance")
		}
		got := env.smtp.Messages()
		if len(got) != mails+3 {
			t.Errorf("receipt e-mails: %d", len(got)-mails)
		}
		// Idempotent: a second pass for the same payments issues nothing.
		err := env.jobsDB.InScope(context.Background(), db.Scope{OwnerID: uuid.MustParse(owner.ID), Role: "job"}, func(ctx context.Context) error {
			res, err := env.app.Billing.ReceiptsOwner(ctx, false)
			if err == nil && len(res.Issued) != 0 {
				t.Errorf("second pass issued %d", len(res.Issued))
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("a receipted payment is frozen", func(t *testing.T) {
		body := map[string]any{"lease_id": l1.ID, "paid_on": "2025-01-04", "amount_cents": 95000, "method": "transfer"}
		audited(t, seen, "PUT /payments/{id}", 409, func() resp {
			return call(t, "PUT", "/payments/"+p1.ID.String(), req{tok: tokE, ifMatch: `W/"1"`, body: body})
		})
		body["amount_cents"], body["reference"] = 90000, "VIR JANVIER"
		audited(t, seen, "PUT /payments/{id}", 200, func() resp {
			return call(t, "PUT", "/payments/"+p1.ID.String(), req{tok: tokE, ifMatch: `W/"1"`, body: body})
		})
		if r := call(t, "POST", "/billing/documents/"+c1["2025-01"].ID.String()+"/cancel", req{tok: tokE, body: map[string]any{"reason": "x"}}); r.problemType(t) != "billing/receipt-issued" {
			t.Errorf("cancel a receipted rent call: %d %s", r.Status, r.Body)
		}
	})

	t.Run("arrears and reminders", func(t *testing.T) {
		var a1v, a2v billing.ArrearsView
		call(t, "GET", "/leases/"+l1.ID+"/arrears", req{tok: tokE}).json(t, &a1v)
		call(t, "GET", "/leases/"+l2.ID+"/arrears", req{tok: tokE}).json(t, &a2v)
		if len(a1v.Items) != 1 || a1v.OutstandingCents != 90000 || len(a2v.Items) != 1 || a2v.OutstandingCents != 80000 {
			t.Fatalf("arrears %+v %+v", a1v, a2v)
		}
		if it := a1v.Items[0]; it.DueOn.String() != "2025-02-05" || it.ReminderOn.String() != "2025-02-10" || !it.ReminderDue || !it.Overdue {
			t.Errorf("reminder date follows the parameter (due day + 5): %+v", it)
		}
		mails := len(env.smtp.Messages())
		if r := call(t, "POST", "/jobs/billing.reminders/trigger", req{tok: tokE, body: map[string]any{}}); r.Status != 201 {
			t.Fatalf("reminders: %d %s", r.Status, r.Body)
		}
		if n := superCount(t, "SELECT count(*) FROM billing_reminders WHERE owner_id = $1", owner.ID); n != 2 {
			t.Errorf("reminders recorded: %d", n)
		}
		if got := env.smtp.Messages(); len(got) != mails+2 || !strings.Contains(got[len(got)-1].Data, "Rappel") {
			t.Errorf("reminder e-mails: %d", len(got)-mails)
		}
		err := env.jobsDB.InScope(context.Background(), db.Scope{OwnerID: uuid.MustParse(owner.ID), Role: "job"}, func(ctx context.Context) error {
			due, err := env.app.Billing.RemindersOwner(ctx, a1v.Items[0].ReminderOn.AddDays(400))
			if err == nil && len(due) != 0 {
				t.Errorf("reminded twice: %d", len(due))
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		// Pay lease 1's February: only the unpaid lease stays in arrears.
		call(t, "POST", "/payments", req{tok: tokE, body: map[string]any{"lease_id": l1.ID, "paid_on": "2025-02-28", "amount_cents": 90000,
			"method": "transfer", "auto_match": true}})
		call(t, "GET", "/leases/"+l1.ID+"/arrears", req{tok: tokE}).json(t, &a1v)
		call(t, "GET", "/leases/"+l2.ID+"/arrears", req{tok: tokE}).json(t, &a2v)
		if len(a1v.Items) != 0 || a2v.OutstandingCents != 80000 {
			t.Errorf("arrears after payment %+v %+v", a1v, a2v)
		}
	})

	t.Run("isolation of the new tables", func(t *testing.T) {
		rlsZeroRows(t, owner.ID, ownerF.ID, subE, subF, a1.ID, map[string]string{
			"payments": "owner_id = $1", "payment_matches": "owner_id = $1", "billing_reminders": "owner_id = $1",
		})
		var l struct{ Total int }
		call(t, "GET", "/payments", req{tok: tokF}).json(t, &l)
		if l.Total != 0 {
			t.Errorf("owner F sees %d payments", l.Total)
		}
	})
}
