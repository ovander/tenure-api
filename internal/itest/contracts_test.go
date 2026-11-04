//go:build integration

package itest

import (
	"bytes"
	"context"
	"io"
	"regexp"
	"strings"
	"testing"

	"github.com/minio/minio-go/v7"

	"github.com/ovander/tenure-api/internal/document"
	"github.com/ovander/tenure-api/internal/params"
)

const (
	subG = "4001" // owner of the contracts scenario
	subH = "4002" // another owner
)

// eicar is the antivirus test file, assembled at run time so the repository
// holds no file a scanner would flag.
func eicar() []byte {
	return []byte(`X5O!P%@AP[4\PZX54(P^)7CC)7}$` + "EICAR-STANDARD-" + "ANTIVIRUS-TEST-FILE!$H+H*")
}

func quarantined(t *testing.T) int {
	t.Helper()
	n := 0
	for o := range env.s3.ListObjects(context.Background(), bucket, minio.ListObjectsOptions{Prefix: document.QuarantinePrefix, Recursive: true}) {
		if o.Err != nil {
			t.Fatal(o.Err)
		}
		n++
	}
	return n
}

func TestContracts(t *testing.T) {
	tokG, tokH := token(t, subG), token(t, subH)
	ps := fixtureParams(t)
	var owner, ownerH, apt, box, box2, box3, student, worker, l1, l2 idv
	call(t, "PUT", "/me", req{tok: tokG, body: map[string]any{"legal_name": "Bailleur G", "address": "4 place Bellecour, Lyon"}}).json(t, &owner)
	call(t, "PUT", "/me", req{tok: tokH, body: map[string]any{"legal_name": "Bailleur H", "address": "1 rue H"}}).json(t, &ownerH)
	call(t, "POST", "/assets", req{tok: tokG, body: map[string]any{"kind": "apartment", "address": "4 place Bellecour, Lyon", "lot": "7",
		"surface_hundredths_m2": 5210}}).json(t, &apt)
	for _, dst := range []*idv{&box, &box2, &box3} {
		call(t, "POST", "/assets", req{tok: tokG, body: map[string]any{"kind": "box", "address": "4 place Bellecour, Lyon"}}).json(t, dst)
	}
	call(t, "POST", "/tenants", req{tok: tokG, body: map[string]any{"names": []string{"Léa Moreau"}, "occupation": "student"}}).json(t, &student)
	call(t, "POST", "/tenants", req{tok: tokG, body: map[string]any{"names": []string{"Marc Roux"}, "occupation": "other"}}).json(t, &worker)
	const rent = 80000
	maxMonths, err := ps.Int64(params.KeyDepositMaxUnfurnished, today())
	if err != nil {
		t.Fatal(err)
	}
	cap := int64(rent) * maxMonths
	body := func(kind string, deposit int64, tenant string, assets ...string) map[string]any {
		return map[string]any{"kind": kind, "asset_ids": assets, "tenants": []map[string]any{{"tenant_id": tenant}},
			"start_on": today().AddDays(10).String(), "term_months": 36, "rent_cents": rent, "provision_cents": 7000, "deposit_cents": deposit,
			"due_day": 5, "irl_ref_quarter": "2025-Q2", "irl_ref_value": 14668, "revision_month": 1,
			"clauses": []map[string]any{{"code": "insurance_certificate"}}}
	}

	t.Run("deposit cap", func(t *testing.T) {
		audited(t, seen, "POST /leases", 422, func() resp {
			r := call(t, "POST", "/leases", req{tok: tokG, body: body("unfurnished", cap+1, worker.ID, apt.ID, box.ID)})
			if r.problemType(t) != "lease/deposit-above-cap" {
				t.Errorf("deposit above the cap: %s", r.Body)
			}
			return r
		})
		call(t, "POST", "/leases", req{tok: tokG, body: body("unfurnished", cap, worker.ID, apt.ID, box.ID)}).json(t, &l1)
		call(t, "POST", "/leases", req{tok: tokG, body: body("box", 0, student.ID, box2.ID)}).json(t, &l2)
		if l1.ID == "" || l2.ID == "" {
			t.Fatal("drafts not created")
		}
	})

	var dpe, erp idv
	t.Run("diagnostics block generation", func(t *testing.T) {
		var b struct {
			Generation, Activation []struct {
				Type, Title string
				AssetID     string `json:"asset_id"`
				Diagnostic  string `json:"diagnostic"`
			}
		}
		call(t, "GET", "/leases/"+l1.ID+"/blockers", req{tok: tokG}).json(t, &b)
		if len(b.Generation) == 0 || b.Generation[0].Type != "lease/diagnostic-missing" || b.Generation[0].Title == "" {
			t.Errorf("generation blockers: %+v", b.Generation)
		}
		// Each diagnostic blocker names its asset and kind (Q-105).
		for _, g := range b.Generation {
			if g.AssetID == "" || g.Diagnostic == "" {
				t.Errorf("blocker without its subject: %+v", g)
			}
		}
		audited(t, seen, "POST /leases/{id}/contract", 409, func() resp {
			r := call(t, "POST", "/leases/"+l1.ID+"/contract", req{tok: tokG})
			if r.problemType(t) != "lease/diagnostic-missing" {
				t.Errorf("no diagnostics: %s", r.Body)
			}
			return r
		})
		rule, _ := ps.Diagnostics(today())
		audited(t, seen, "POST /assets/{id}/diagnostics", 201, func() resp {
			return call(t, "POST", "/assets/"+apt.ID+"/diagnostics", req{tok: tokG, body: map[string]any{"kind": "erp",
				"done_on": today().AddMonths(-rule.ValidityMonths["erp"]).String()}})
		}).json(t, &erp)
		audited(t, seen, "POST /assets/{id}/diagnostics", 409, func() resp {
			return call(t, "POST", "/assets/"+apt.ID+"/diagnostics", req{tok: tokG, body: map[string]any{"kind": "erp", "done_on": today().String()}})
		})
		audited(t, seen, "POST /assets/{id}/diagnostics", 201, func() resp {
			return call(t, "POST", "/assets/"+apt.ID+"/diagnostics", req{tok: tokG, body: map[string]any{"kind": "dpe",
				"done_on": today().AddMonths(-2).String(), "result": "G"}})
		}).json(t, &dpe)
		recordDiagnostics(t, tokG, apt.ID, box.ID, box2.ID, box3.ID)
		r := call(t, "POST", "/leases/"+l1.ID+"/contract", req{tok: tokG})
		if r.Status != 409 || r.problemType(t) != "lease/diagnostic-expired" {
			t.Errorf("expired ERP: %d %s", r.Status, r.Body)
		}
		audited(t, seen, "PUT /diagnostics/{id}", 200, func() resp {
			return call(t, "PUT", "/diagnostics/"+erp.ID, req{tok: tokG, ifMatch: `W/"1"`, body: map[string]any{"kind": "erp",
				"done_on": today().AddMonths(-1).String()}})
		})
		if r := call(t, "POST", "/leases/"+l1.ID+"/contract", req{tok: tokG}); r.problemType(t) != "lease/dpe-class-forbidden" {
			t.Errorf("class G dwelling: %d %s", r.Status, r.Body)
		}
		minClass, _ := ps.MinDPEClass(today())
		call(t, "PUT", "/diagnostics/"+dpe.ID, req{tok: tokG, ifMatch: `W/"1"`, body: map[string]any{"kind": "dpe",
			"done_on": today().AddMonths(-2).String(), "result": minClass}})
	})

	var deposit, surety idv
	t.Run("guarantee rules", func(t *testing.T) {
		audited(t, seen, "POST /leases/{id}/guarantees", 201, func() resp {
			return call(t, "POST", "/leases/"+l1.ID+"/guarantees", req{tok: tokG, body: map[string]any{"kind": "insurance", "status": "valid",
				"guarantor": map[string]any{"insurer": "Assureur X", "policy": "GLI-000"}}})
		})
		audited(t, seen, "POST /leases/{id}/guarantees", 409, func() resp {
			r := call(t, "POST", "/leases/"+l1.ID+"/guarantees", req{tok: tokG, body: map[string]any{"kind": "surety", "amount_cents": 1000000,
				"guarantor": map[string]any{"name": "Anne Roux", "address": "9 rue Y"}}})
			if r.problemType(t) != "guarantee/surety-with-insurance" {
				t.Errorf("surety with insurance: %s", r.Body)
			}
			return r
		})
		audited(t, seen, "POST /leases/{id}/guarantees", 422, func() resp {
			return call(t, "POST", "/leases/"+l1.ID+"/guarantees", req{tok: tokG, body: map[string]any{"kind": "deposit", "amount_cents": cap + 1}})
		})
		call(t, "POST", "/leases/"+l1.ID+"/guarantees", req{tok: tokG, body: map[string]any{"kind": "deposit", "amount_cents": cap,
			"valid_from": today().String()}}).json(t, &deposit)
		// A student may have a surety alongside a rent insurance; Visale replaces a surety.
		call(t, "POST", "/leases/"+l2.ID+"/guarantees", req{tok: tokG, body: map[string]any{"kind": "insurance", "status": "valid"}})
		if r := call(t, "POST", "/leases/"+l2.ID+"/guarantees", req{tok: tokG, body: map[string]any{"kind": "surety", "amount_cents": 500000,
			"guarantor": map[string]any{"name": "Paul Moreau", "address": "2 rue Z", "solidary": true}}}); r.Status != 201 {
			t.Fatalf("student surety with insurance: %d %s", r.Status, r.Body)
		} else {
			r.json(t, &surety)
		}
		if r := call(t, "POST", "/leases/"+l2.ID+"/guarantees", req{tok: tokG, body: map[string]any{"kind": "visale",
			"guarantor": map[string]any{"visa_number": "V000"}}}); r.problemType(t) != "guarantee/visale-with-surety" {
			t.Errorf("visale with surety: %d %s", r.Status, r.Body)
		}
		if r := call(t, "POST", "/leases/"+l2.ID+"/guarantees", req{tok: tokG, body: map[string]any{"kind": "surety",
			"guarantor": map[string]any{"name": "Sans adresse"}}}); r.Status != 422 {
			t.Errorf("surety without an address: %d %s", r.Status, r.Body)
		}
	})

	var ct leaseDoc
	ctNumber := regexp.MustCompile(`^CT-` + document.AssetCode(mustUUID(t, apt.ID)) + `-[0-9]{4}-[0-9]{4}$`)
	t.Run("contract and activation", func(t *testing.T) {
		audited(t, seen, "POST /leases/{id}/contract", 201, func() resp {
			return call(t, "POST", "/leases/"+l1.ID+"/contract", req{tok: tokG})
		}).json(t, &ct)
		if !ctNumber.MatchString(ct.Number) {
			t.Errorf("contract number %q", ct.Number)
		}
		var doc docView
		call(t, "GET", "/documents/"+ct.DocumentID, req{tok: tokG}).json(t, &doc)
		if doc.Class != "contract" || doc.Source != "generated" || doc.RetainUntil != nil {
			t.Errorf("contract document %+v: a lease document's retention starts at the lease end (Q-68)", doc)
		}
		activate := func(want int, typ string) {
			t.Helper()
			audited(t, seen, "POST /leases/{id}/transitions", want, func() resp {
				r := call(t, "POST", "/leases/"+l1.ID+"/transitions", req{tok: tokG, body: map[string]any{"action": "activate"}})
				if typ != "" && r.problemType(t) != typ {
					t.Errorf("activation: want %s, got %s", typ, r.Body)
				}
				return r
			})
		}
		activate(409, "lease/contract-not-signed")
		// A change of the draft makes the contract stale.
		b := body("unfurnished", cap, worker.ID, apt.ID, box.ID)
		b["provision_cents"] = 7500
		call(t, "PUT", "/leases/"+l1.ID, req{tok: tokG, ifMatch: `W/"1"`, body: b})
		activate(409, "lease/contract-missing")
		call(t, "POST", "/leases/"+l1.ID+"/contract", req{tok: tokG}).json(t, &ct)
		// A signed copy must sign a generated document of its class and lease.
		var edl docView
		upload(t, tokG, "", map[string]string{"class": "inventory", "lease_id": l1.ID}, "edl.pdf", signedPDF).json(t, &edl)
		if r := upload(t, tokG, "", map[string]string{"class": "contract", "lease_id": l1.ID, "signs_document_id": edl.ID}, "x.pdf", signedPDF); r.problemType(t) != document.TypeSignedTargetMiss {
			t.Errorf("signing an upload: %d %s", r.Status, r.Body)
		}
		audited(t, seen, "POST /documents", 201, func() resp {
			return upload(t, tokG, "", map[string]string{"class": "contract", "lease_id": l1.ID, "signs_document_id": ct.DocumentID},
				"bail-signé.pdf", signedPDF)
		})
		activate(409, "lease/entry-inventory-missing")
		if r := call(t, "POST", "/leases/"+l1.ID+"/inventories", req{tok: tokG, body: map[string]any{"kind": "entry", "done_on": today().String(),
			"document_id": ct.DocumentID}}); r.problemType(t) != "inventory/document-invalid" {
			t.Errorf("a contract as the inventory: %d %s", r.Status, r.Body)
		}
		audited(t, seen, "POST /leases/{id}/inventories", 201, func() resp {
			return call(t, "POST", "/leases/"+l1.ID+"/inventories", req{tok: tokG, body: map[string]any{"kind": "entry",
				"done_on": today().String(), "document_id": edl.ID}})
		})
		audited(t, seen, "POST /leases/{id}/inventories", 409, func() resp {
			return call(t, "POST", "/leases/"+l1.ID+"/inventories", req{tok: tokG, body: map[string]any{"kind": "entry",
				"done_on": today().String(), "document_id": edl.ID}})
		})
		var b2 struct{ Generation, Activation []struct{ Type string } }
		call(t, "GET", "/leases/"+l1.ID+"/blockers", req{tok: tokG}).json(t, &b2)
		if len(b2.Generation)+len(b2.Activation) != 0 {
			t.Errorf("blockers left: %+v", b2)
		}
		activate(200, "")
		var docs []leaseDoc
		call(t, "GET", "/leases/"+l1.ID+"/documents", req{tok: tokG}).json(t, &docs)
		if len(docs) != 2 || docs[1].Number != ct.Number {
			t.Errorf("lease documents: %+v", docs)
		}
	})

	t.Run("guarantee documents", func(t *testing.T) {
		var dg, ca leaseDoc
		audited(t, seen, "POST /guarantees/{id}/document", 201, func() resp {
			return call(t, "POST", "/guarantees/"+deposit.ID+"/document", req{tok: tokG})
		}).json(t, &dg)
		audited(t, seen, "POST /guarantees/{id}/document", 201, func() resp {
			return call(t, "POST", "/guarantees/"+surety.ID+"/document", req{tok: tokG})
		}).json(t, &ca)
		if !strings.HasPrefix(dg.Number, "DG-") || dg.Kind != "deposit_receipt" || !strings.HasPrefix(ca.Number, "CA-") || ca.Kind != "surety_act" {
			t.Errorf("guarantee documents %+v %+v", dg, ca)
		}
		var gs []struct {
			ID         string
			DocumentID string `json:"document_id"`
		}
		call(t, "GET", "/leases/"+l1.ID+"/guarantees", req{tok: tokG}).json(t, &gs)
		for _, g := range gs {
			if g.ID == deposit.ID && g.DocumentID != dg.DocumentID {
				t.Errorf("deposit guarantee document %s, want %s", g.DocumentID, dg.DocumentID)
			}
		}
	})

	t.Run("amendments", func(t *testing.T) {
		var before struct {
			DepositCents int64 `json:"deposit_cents"`
			Version      int
		}
		call(t, "GET", "/leases/"+l1.ID, req{tok: tokG}).json(t, &before)
		var res struct {
			Lease struct {
				RentCents    int64    `json:"rent_cents"`
				DepositCents int64    `json:"deposit_cents"`
				AssetIDs     []string `json:"asset_ids"`
			}
			Amendment leaseDoc
		}
		audited(t, seen, "POST /leases/{id}/amendments", 201, func() resp {
			return call(t, "POST", "/leases/"+l1.ID+"/amendments", req{tok: tokG, body: map[string]any{"kind": "rent",
				"effective_on": today().AddDays(30).String(), "rent_cents": rent / 2, "reason": "travaux à la charge du locataire"}})
		}).json(t, &res)
		if res.Lease.RentCents != rent/2 || res.Lease.DepositCents != before.DepositCents || !strings.HasPrefix(res.Amendment.Number, "AV-") {
			t.Errorf("rent amendment: %+v (deposit before %d)", res, before.DepositCents)
		}
		var gs []struct {
			ID          string
			AmountCents int64 `json:"amount_cents"`
		}
		call(t, "GET", "/leases/"+l1.ID+"/guarantees", req{tok: tokG}).json(t, &gs)
		for _, g := range gs {
			if g.ID == deposit.ID && g.AmountCents != cap {
				t.Errorf("a rent change altered the deposit held: %d", g.AmountCents)
			}
		}
		call(t, "POST", "/leases/"+l1.ID+"/amendments", req{tok: tokG, body: map[string]any{"kind": "asset_add",
			"effective_on": today().AddDays(30).String(), "asset_id": box3.ID, "rent_cents": rent/2 + 5000}}).json(t, &res)
		if len(res.Lease.AssetIDs) != 3 || res.Lease.RentCents != rent/2+5000 {
			t.Errorf("box amendment: %+v", res.Lease)
		}
		audited(t, seen, "POST /leases/{id}/amendments", 409, func() resp {
			return call(t, "POST", "/leases/"+l2.ID+"/amendments", req{tok: tokG, body: map[string]any{"kind": "rent",
				"effective_on": today().String(), "rent_cents": 1}})
		})
	})

	t.Run("uploads", func(t *testing.T) {
		docs := superCount(t, "SELECT count(*) FROM documents WHERE owner_id = $1", owner.ID)
		rejects := superCount(t, "SELECT count(*) FROM audit_events WHERE owner_id = $1 AND action = 'document.reject'", owner.ID)
		r := upload(t, tokG, "", map[string]string{"class": "supporting", "lease_id": l1.ID}, "facture.pdf", eicar())
		if r.Status != 422 || r.problemType(t) != document.TypeInfected {
			t.Errorf("infected upload: %d %s", r.Status, r.Body)
		}
		if n := superCount(t, "SELECT count(*) FROM audit_events WHERE owner_id = $1 AND action = 'document.reject'", owner.ID); n != rejects+1 {
			t.Errorf("infected upload not logged in the audit trail: %d", n-rejects)
		}
		cases := []struct {
			name, typ string
			fields    map[string]string
			file      string
			content   []byte
		}{
			{"html declared as pdf", document.TypeTypeNotAllowed, map[string]string{"class": "other"}, "x.pdf", []byte("<html><script>alert(1)</script></html>")},
			{"empty file", document.TypeTypeNotAllowed, map[string]string{"class": "other"}, "x.pdf", nil},
			{"generated class", document.TypeClassNotUpload, map[string]string{"class": "rent_call"}, "x.pdf", signedPDF},
			{"another owner's lease", document.TypeLinkInvalid, map[string]string{"class": "other", "lease_id": l1.ID}, "x.pdf", signedPDF},
		}
		for _, c := range cases {
			tok := tokG
			if c.name == "another owner's lease" {
				tok = tokH
			}
			if r := upload(t, tok, "", c.fields, c.file, c.content); r.problemType(t) != c.typ {
				t.Errorf("%s: %d %s; want %s", c.name, r.Status, r.Body, c.typ)
			}
		}
		big := io.MultiReader(bytes.NewReader([]byte("%PDF-1.4\n")), io.LimitReader(zeros{}, document.MaxUploadBytes))
		pr, pw := io.Pipe()
		mw := multipartWriter(pw)
		go func() {
			_ = mw.WriteField("class", "other")
			fw, _ := mw.CreateFormFile("file", "big.pdf")
			_, _ = io.Copy(fw, big)
			_ = mw.Close()
			_ = pw.Close()
		}()
		if r := uploadRaw(t, tokG, "", mw.FormDataContentType(), pr); r.Status != 413 {
			t.Errorf("file above 25 MB: %d %s", r.Status, r.Body)
		}
		var csv docView
		upload(t, tokG, "", map[string]string{"class": "supporting"}, "releve.csv", []byte("date;montant\n2026-01-05;850,00\n")).json(t, &csv)
		if csv.ContentType != "text/csv" || csv.Source != "upload" {
			t.Errorf("csv upload %+v", csv)
		}
		if n := superCount(t, "SELECT count(*) FROM documents WHERE owner_id = $1", owner.ID); n != docs+1 {
			t.Errorf("refused uploads left %d documents", n-docs-1)
		}
		if n := quarantined(t); n != 0 {
			t.Errorf("%d objects left in quarantine", n)
		}
		var link struct{ URL string }
		call(t, "GET", "/documents/"+csv.ID+"/content", req{tok: tokG}).json(t, &link)
		if !strings.Contains(link.URL, "response-content-type=text%2Fcsv") || !strings.Contains(link.URL, "attachment") {
			t.Errorf("an upload is served as an attachment of its sniffed type: %s", link.URL)
		}
	})

	t.Run("isolation", func(t *testing.T) {
		if r := call(t, "GET", "/leases/"+l1.ID+"/blockers", req{tok: tokH}); r.Status != 404 {
			t.Errorf("H reads G's blockers: %d", r.Status)
		}
		if r := call(t, "POST", "/leases/"+l1.ID+"/contract", req{tok: tokH}); r.Status != 404 {
			t.Errorf("H generates G's contract: %d", r.Status)
		}
		rlsZeroRows(t, owner.ID, ownerH.ID, subG, subH, apt.ID, map[string]string{
			"asset_diagnostics": "owner_id = $1", "inventories": "owner_id = $1", "lease_documents": "owner_id = $1",
		})
	})
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}
