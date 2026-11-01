//go:build integration

package itest

import (
	"strings"
	"testing"

	"github.com/ovander/tenure-api/internal/billing"
	"github.com/ovander/tenure-api/internal/charge"
	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/period"
)

const (
	subK = "6001" // owner of the charges scenario
	subL = "6002" // another owner
)

type reconLine struct {
	ID                     string
	LeaseID                string `json:"lease_id"`
	AllocatedCents         int64  `json:"allocated_cents"`
	ProvisionsCents        int64  `json:"provisions_cents"`
	BalanceCents           int64  `json:"balance_cents"`
	Spread                 bool
	Instalments            []int64
	ProposedProvisionCents int64 `json:"proposed_provision_cents"`
	Settlement             string
	Number                 string
	Version                int
}

type recon struct {
	ID         string
	State      string
	Version    int
	CorrectsID *string `json:"corrects_id"`
	Totals     struct {
		RecoverableCents int64    `json:"recoverable_cents"`
		Cancellations    []string `json:"cancellations"`
	}
	Lines []reconLine
}

func TestCharges(t *testing.T) {
	tok, tokL := token(t, subK), token(t, subL)
	var owner, ownerL, a1, a2, a3, t1, t2, t3, l1, l2, l3 idv
	call(t, "PUT", "/me", req{tok: tok, body: map[string]any{"legal_name": "Bailleur K", "address": "6 rue Royale, Lyon"}}).json(t, &owner)
	call(t, "PUT", "/me", req{tok: tokL, body: map[string]any{"legal_name": "Bailleur L", "address": "1 rue L"}}).json(t, &ownerL)
	for i, dst := range []*idv{&a1, &a2, &a3} {
		call(t, "POST", "/assets", req{tok: tok, body: map[string]any{"kind": "apartment", "address": "6 rue Royale, Lyon", "lot": string(rune('A' + i))}}).json(t, dst)
	}
	for i, dst := range []*idv{&t1, &t2, &t3} {
		call(t, "POST", "/tenants", req{tok: tok, body: map[string]any{"names": []string{"Locataire " + string(rune('A'+i))},
			"email": "locataire" + string(rune('a'+i)) + "@example.test"}}).json(t, dst)
	}
	mk := func(asset, tenant, start string, provision int64, dst *idv) {
		body := map[string]any{"kind": "unfurnished", "asset_ids": []string{asset}, "tenants": []map[string]any{{"tenant_id": tenant}},
			"start_on": start, "term_months": 36, "rent_cents": 80000, "provision_cents": provision, "deposit_cents": 80000, "due_day": 5,
			"irl_ref_quarter": "2024-Q3", "irl_ref_value": 14451, "revision_month": 9}
		if r := call(t, "POST", "/leases", req{tok: tok, body: body}); r.Status != 201 {
			t.Fatalf("lease: %d %s", r.Status, r.Body)
		} else {
			r.json(t, dst)
		}
		readyToActivate(t, tok, dst.ID)
		if r := call(t, "POST", "/leases/"+dst.ID+"/transitions", req{tok: tok, body: map[string]any{"action": "activate"}}); r.Status != 200 {
			t.Fatalf("activate: %d %s", r.Status, r.Body)
		}
	}
	mk(a1.ID, t1.ID, "2025-01-01", 10000, &l1)
	mk(a2.ID, t2.ID, "2024-11-01", 5000, &l2)
	mk(a3.ID, t3.ID, "2025-01-01", 6000, &l3)
	// Tenant 3 leaves on 30 June 2025, a deposit still held.
	call(t, "POST", "/leases/"+l3.ID+"/guarantees", req{tok: tok, body: map[string]any{"kind": "deposit", "amount_cents": 80000}})
	call(t, "POST", "/leases/"+l3.ID+"/transitions", req{tok: tok, body: map[string]any{"action": "notice", "end_on": "2025-06-30"}})
	for _, per := range []string{"2025-01", "2025-02", "2025-03"} {
		if r := call(t, "POST", "/billing/runs", req{tok: tok, body: map[string]any{"period": per}}); r.Status != 201 {
			t.Fatalf("billing %s: %d %s", per, r.Status, r.Body)
		}
	}
	call(t, "POST", "/leases/"+l3.ID+"/transitions", req{tok: tok, body: map[string]any{"action": "end"}})
	// Two of lease 1's three rent calls are paid: provisions come from payments, not calls.
	for _, d := range []string{"2025-01-04", "2025-02-04"} {
		if r := call(t, "POST", "/payments", req{tok: tok, body: map[string]any{"lease_id": l1.ID, "paid_on": d, "amount_cents": 90000,
			"method": "transfer", "auto_match": true}}); r.Status != 201 {
			t.Fatalf("payment: %d %s", r.Status, r.Body)
		}
	}

	var imported []struct {
		ID                     string
		Label                  string
		RecoverableCents       int64  `json:"recoverable_cents"`
		SyndicRecoverableCents *int64 `json:"syndic_recoverable_cents"`
		Status                 string
		Version                int
	}
	t.Run("syndic import", func(t *testing.T) {
		var csv docView
		upload(t, tok, "", map[string]string{"class": "supporting", "asset_id": a1.ID}, "decompte-2025.csv",
			[]byte("Libellé;Montant;Récupérable;Clé\nEau froide;412,30;412,30;tantièmes\nHonoraires syndic;1 050,00;;tantièmes\nAscenseur;300,00;240,00;\n")).json(t, &csv)
		mapping := map[string]any{"label": "Libellé", "amount": "Montant", "recoverable": "Récupérable"}
		audited(t, seen, "POST /charges/import/syndic", 422, func() resp {
			return call(t, "POST", "/charges/import/syndic", req{tok: tok, body: map[string]any{"asset_id": a1.ID, "document_id": csv.ID,
				"charged_on": "2025-12-31", "mapping": map[string]any{"label": "Label", "amount": "Montant"}}})
		})
		audited(t, seen, "POST /charges/import/syndic", 201, func() resp {
			return call(t, "POST", "/charges/import/syndic", req{tok: tok, body: map[string]any{"asset_id": a1.ID, "document_id": csv.ID,
				"charged_on": "2025-12-31", "mapping": mapping}})
		}).json(t, &imported)
		if len(imported) != 3 || imported[0].Status != "draft" || imported[2].RecoverableCents != 24000 || *imported[2].SyndicRecoverableCents != 24000 {
			t.Fatalf("imported %+v", imported)
		}
		audited(t, seen, "POST /charges", 201, func() resp {
			return call(t, "POST", "/charges", req{tok: tok, body: map[string]any{"asset_id": a1.ID, "charged_on": "2025-10-15",
				"category": "teom", "label": "TEOM 2025", "amount_cents": 15000, "recoverable_cents": 15000}})
		})
		call(t, "POST", "/charges", req{tok: tok, body: map[string]any{"asset_id": a2.ID, "charged_on": "2025-06-01",
			"category": "copro_statement", "label": "Charges 2024-2025", "amount_cents": 120000, "recoverable_cents": 120000}})
		call(t, "POST", "/charges", req{tok: tok, body: map[string]any{"asset_id": a3.ID, "charged_on": "2025-12-31",
			"category": "copro_statement", "label": "Charges 2025", "amount_cents": 73000, "recoverable_cents": 73000}})
	})

	var r1 recon
	t.Run("reconciliation of asset 1", func(t *testing.T) {
		audited(t, seen, "POST /reconciliations", 422, func() resp {
			return call(t, "POST", "/reconciliations", req{tok: tok, body: map[string]any{"asset_id": a1.ID, "exercise_start": "2025-01-15", "exercise_end": "2025-12-31"}})
		})
		audited(t, seen, "POST /reconciliations", 201, func() resp {
			return call(t, "POST", "/reconciliations", req{tok: tok, body: map[string]any{"asset_id": a1.ID, "exercise_start": "2025-01-01", "exercise_end": "2025-12-31"}})
		}).json(t, &r1)
		audited(t, seen, "POST /reconciliations/{id}/transitions", 409, func() resp {
			r := call(t, "POST", "/reconciliations/"+r1.ID+"/transitions", req{tok: tok, body: map[string]any{"action": "allocate"}})
			if r.problemType(t) != charge.TypeDraftCharges {
				t.Errorf("draft charges: %s", r.Body)
			}
			return r
		})
		if r := call(t, "POST", "/reconciliations/"+r1.ID+"/transitions", req{tok: tok, body: map[string]any{"action": "issue"}}); r.problemType(t) != charge.TypeInvalidTransition {
			t.Errorf("issue a draft: %d %s", r.Status, r.Body)
		}
		// Confirm the import, overriding the syndic's split of the lift: the audit keeps the split before.
		for i, c := range imported {
			rec := c.RecoverableCents
			if i == 2 {
				rec = 30000
			}
			audited(t, seen, "PUT /charges/{id}", 200, func() resp {
				return call(t, "PUT", "/charges/"+c.ID, req{tok: tok, ifMatch: `W/"1"`, body: map[string]any{"asset_id": a1.ID, "charged_on": "2025-12-31",
					"category": "copro_statement", "label": c.Label, "amount_cents": map[int]int64{0: 41230, 1: 105000, 2: 30000}[i],
					"recoverable_cents": rec, "status": "confirmed"}})
			})
		}
		if n := superCount(t, `SELECT count(*) FROM audit_events WHERE action = 'charge.update' AND entity_id = $1
			AND (before->>'recoverable_cents')::bigint = 24000 AND (after->>'recoverable_cents')::bigint = 30000
			AND (after->>'syndic_recoverable_cents')::bigint = 24000`, imported[2].ID); n != 1 {
			t.Errorf("override not audited with the syndic's split: %d", n)
		}
		audited(t, seen, "POST /reconciliations/{id}/transitions", 200, func() resp {
			return call(t, "POST", "/reconciliations/"+r1.ID+"/transitions", req{tok: tok, body: map[string]any{"action": "allocate"}})
		}).json(t, &r1)
		const recoverable = 41230 + 30000 + 15000
		if r1.State != "allocated" || r1.Totals.RecoverableCents != recoverable || len(r1.Lines) != 1 {
			t.Fatalf("allocation %+v", r1)
		}
		ln := r1.Lines[0]
		// One lease over the whole exercise takes the whole recoverable total; two paid calls of three.
		if ln.AllocatedCents != recoverable || ln.ProvisionsCents != 20000 || ln.BalanceCents != recoverable-20000 || ln.Settlement != "fee" || ln.Spread {
			t.Errorf("line %+v", ln)
		}
		if want := int64(money.CeilToEuro(money.Cents((recoverable + 11) / 12))); ln.ProposedProvisionCents != want {
			t.Errorf("proposed provision %d; want %d", ln.ProposedProvisionCents, want)
		}
		audited(t, seen, "PUT /reconciliations/{id}/lines/{lineId}", 200, func() resp {
			return call(t, "PUT", "/reconciliations/"+r1.ID+"/lines/"+ln.ID, req{tok: tok, ifMatch: `W/"1"`, body: map[string]any{"apply_provision": true}})
		})
		audited(t, seen, "PUT /reconciliations/{id}/lines/{lineId}", 422, func() resp {
			return call(t, "PUT", "/reconciliations/"+r1.ID+"/lines/"+ln.ID, req{tok: tok, ifMatch: `W/"2"`, body: map[string]any{"settlement": "deposit"}})
		})
		mails := len(env.smtp.Messages())
		audited(t, seen, "POST /reconciliations/{id}/transitions", 200, func() resp {
			return call(t, "POST", "/reconciliations/"+r1.ID+"/transitions", req{tok: tok, body: map[string]any{"action": "issue"}})
		}).json(t, &r1)
		ln = r1.Lines[0]
		if r1.State != "issued" || !strings.HasPrefix(ln.Number, "RG-") {
			t.Fatalf("issued %+v", r1)
		}
		if len(env.smtp.Messages()) != mails+1 || !strings.Contains(env.smtp.Messages()[mails].Data, ln.Number) {
			t.Errorf("statement not e-mailed")
		}
		var lease struct {
			ProvisionCents int64 `json:"provision_cents"`
		}
		call(t, "GET", "/leases/"+l1.ID, req{tok: tok}).json(t, &lease)
		if lease.ProvisionCents != ln.ProposedProvisionCents {
			t.Errorf("new provision %d, want %d", lease.ProvisionCents, ln.ProposedProvisionCents)
		}
		if r := call(t, "PUT", "/charges/"+imported[0].ID, req{tok: tok, ifMatch: `W/"3"`, body: map[string]any{"asset_id": a1.ID,
			"charged_on": "2025-12-31", "category": "copro_statement", "amount_cents": 1, "recoverable_cents": 0}}); r.problemType(t) != charge.TypeChargeReconciled {
			t.Errorf("an issued statement's charge changed: %d %s", r.Status, r.Body)
		}
		if r := call(t, "POST", "/reconciliations/"+r1.ID+"/transitions", req{tok: tok, body: map[string]any{"action": "settle"}}); r.problemType(t) != charge.TypeNotSettled {
			t.Errorf("settle before the balance is carried: %d %s", r.Status, r.Body)
		}
	})

	var r2, r3 recon
	t.Run("late statement and departed tenant", func(t *testing.T) {
		call(t, "POST", "/reconciliations", req{tok: tok, body: map[string]any{"asset_id": a2.ID, "exercise_start": "2024-11-01", "exercise_end": "2025-10-31"}}).json(t, &r2)
		// charges.reconcile allocates it on demand (dry run first, then for real).
		var dry struct{ Status string }
		call(t, "POST", "/jobs/charges.reconcile/trigger", req{tok: tok, body: map[string]any{"period": a2.ID + "/2025", "dry_run": true}}).json(t, &dry)
		if r := call(t, "POST", "/jobs/charges.reconcile/trigger", req{tok: tok, body: map[string]any{"period": a2.ID + "/2025"}}); r.Status != 201 {
			t.Fatalf("charges.reconcile: %d %s", r.Status, r.Body)
		}
		call(t, "GET", "/reconciliations/"+r2.ID, req{tok: tok}).json(t, &r2)
		if r2.State != "allocated" || len(r2.Lines) != 1 {
			t.Fatalf("job allocation %+v", r2)
		}
		call(t, "POST", "/reconciliations/"+r2.ID+"/transitions", req{tok: tok, body: map[string]any{"action": "issue"}}).json(t, &r2)
		ln := r2.Lines[0]
		spreadAfter, _ := fixtureParams(t).Int64(params.KeyRegularisationSpread, today())
		late := today().After(period2025Oct31().AddMonths(int(spreadAfter)))
		if late && (!ln.Spread || len(ln.Instalments) != 12 || sum(ln.Instalments) != ln.BalanceCents) {
			t.Errorf("late statement not spread: %+v", ln)
		}
		call(t, "POST", "/reconciliations", req{tok: tok, body: map[string]any{"asset_id": a3.ID, "exercise_start": "2025-01-01", "exercise_end": "2025-12-31"}}).json(t, &r3)
		call(t, "POST", "/reconciliations/"+r3.ID+"/transitions", req{tok: tok, body: map[string]any{"action": "allocate"}}).json(t, &r3)
		if r3.Lines[0].Settlement != "deposit" || r3.Lines[0].ProposedProvisionCents != 0 {
			t.Errorf("a departed tenant settles against the deposit: %+v", r3.Lines[0])
		}
		call(t, "POST", "/reconciliations/"+r3.ID+"/transitions", req{tok: tok, body: map[string]any{"action": "issue"}}).json(t, &r3)
		if r := call(t, "POST", "/reconciliations/"+r3.ID+"/transitions", req{tok: tok, body: map[string]any{"action": "settle"}}); r.Status != 200 {
			t.Errorf("deposit settlement: %d %s", r.Status, r.Body)
		}
	})

	t.Run("next fee carries the balance", func(t *testing.T) {
		next := today().Period().Next().String()
		if r := call(t, "POST", "/billing/runs", req{tok: tok, body: map[string]any{"period": next}}); r.Status != 201 {
			t.Fatalf("billing %s: %d %s", next, r.Status, r.Body)
		}
		var list struct{ Items []billing.DocView }
		call(t, "GET", "/billing/documents?kind=rent_call&period="+next, req{tok: tok}).json(t, &list)
		got := map[string]billing.DocView{}
		for _, d := range list.Items {
			got[d.LeaseID.String()] = d
		}
		lineOf := func(d billing.DocView, kind string) (int64, string) {
			for _, l := range d.Lines {
				if l.Kind == kind {
					return int64(l.AmountCents), l.Detail
				}
			}
			return 0, ""
		}
		if amount, ref := lineOf(got[l1.ID], "regularisation"); amount != r1.Lines[0].BalanceCents || ref != r1.Lines[0].Number {
			t.Errorf("lease 1 regularisation line %d %q; want %d %s", amount, ref, r1.Lines[0].BalanceCents, r1.Lines[0].Number)
		}
		if amount, _ := lineOf(got[l1.ID], "provision"); amount != r1.Lines[0].ProposedProvisionCents {
			t.Errorf("new provision on the next fee: %d", amount)
		}
		if r2.Lines[0].Spread {
			if amount, ref := lineOf(got[l2.ID], "regularisation"); amount != r2.Lines[0].Instalments[0] || !strings.Contains(ref, "1/12") {
				t.Errorf("first instalment %d %q", amount, ref)
			}
		}
		audited(t, seen, "POST /reconciliations/{id}/transitions", 200, func() resp {
			return call(t, "POST", "/reconciliations/"+r1.ID+"/transitions", req{tok: tok, body: map[string]any{"action": "settle"}})
		})
	})

	t.Run("correction cancels the statement", func(t *testing.T) {
		audited(t, seen, "POST /reconciliations/{id}/transitions", 200, func() resp {
			return call(t, "POST", "/reconciliations/"+r1.ID+"/transitions", req{tok: tok, body: map[string]any{"action": "cancel", "reason": "relevé du syndic rectifié"}})
		}).json(t, &r1)
		if r1.State != "cancelled" || len(r1.Totals.Cancellations) != 1 || !strings.HasPrefix(r1.Totals.Cancellations[0], "RG-") {
			t.Errorf("cancelled %+v", r1)
		}
		if n := superCount(t, "SELECT count(*) FROM regularisation_postings WHERE reconciliation_id = $1 AND amount_cents = $2",
			r1.ID, -r1.Lines[0].BalanceCents); n != 1 {
			t.Errorf("the carried balance is not reversed: %d", n)
		}
		var again recon
		call(t, "POST", "/reconciliations", req{tok: tok, body: map[string]any{"asset_id": a1.ID, "exercise_start": "2025-01-01", "exercise_end": "2025-12-31"}}).json(t, &again)
		if again.CorrectsID == nil || *again.CorrectsID != r1.ID {
			t.Errorf("the new reconciliation does not correct the cancelled one: %+v", again)
		}
		if r := call(t, "PUT", "/charges/"+imported[0].ID, req{tok: tok, ifMatch: `W/"4"`, body: map[string]any{"asset_id": a1.ID,
			"charged_on": "2025-12-31", "category": "copro_statement", "label": "Eau froide", "amount_cents": 41230, "recoverable_cents": 41230,
			"status": "confirmed"}}); r.Status != 200 {
			t.Errorf("a cancelled statement releases its charges: %d %s", r.Status, r.Body)
		}
	})

	t.Run("isolation", func(t *testing.T) {
		if r := call(t, "GET", "/reconciliations/"+r1.ID, req{tok: tokL}); r.Status != 404 {
			t.Errorf("L reads K's reconciliation: %d", r.Status)
		}
		rlsZeroRows(t, owner.ID, ownerL.ID, subK, subL, a1.ID, map[string]string{
			"charges": "owner_id = $1", "reconciliations": "owner_id = $1", "reconciliation_lines": "owner_id = $1",
			"regularisation_postings": "owner_id = $1",
		})
	})
}

func sum(xs []int64) int64 {
	var s int64
	for _, x := range xs {
		s += x
	}
	return s
}

func period2025Oct31() period.Date { return period.NewDate(2025, 10, 31) }
