//go:build integration

package itest

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/params"
	"github.com/ovander/tenure-api/internal/period"
)

// signedPDF stands for a scanned signed copy: a minimal PDF.
var signedPDF = []byte("%PDF-1.4\n1 0 obj << /Type /Catalog >> endobj\ntrailer << /Root 1 0 R >>\n%%EOF\n")

// upload posts a multipart upload, the form fields first and the file last.
func upload(t *testing.T, tok, scope string, fields map[string]string, name string, content []byte) resp {
	t.Helper()
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	for _, k := range []string{"class", "asset_id", "lease_id", "period", "signs_document_id"} {
		if v, ok := fields[k]; ok {
			_ = mw.WriteField(k, v)
		}
	}
	fw, err := mw.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fw.Write(content)
	_ = mw.Close()
	return uploadRaw(t, tok, scope, mw.FormDataContentType(), &b)
}

func uploadRaw(t *testing.T, tok, scope, contentType string, body io.Reader) resp {
	t.Helper()
	r, _ := http.NewRequest("POST", env.server.URL+"/api/v1/documents", body)
	r.Header.Set("Content-Type", contentType)
	r.Header.Set("Authorization", "Bearer "+tok)
	if scope != "" {
		r.Header.Set(auth.ScopeHeader, scope)
	}
	res, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	out, _ := io.ReadAll(res.Body)
	return resp{Status: res.StatusCode, Header: res.Header, Body: out}
}

// fixtureParams loads the legal parameter fixtures, the seed of legal_params.
func fixtureParams(t *testing.T) params.Set {
	t.Helper()
	files, _ := filepath.Glob("../../testdata/params/*.json")
	var rs []io.Reader
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		rs = append(rs, bytes.NewReader(b))
	}
	ps, err := params.Load(rs...)
	if err != nil {
		t.Fatal(err)
	}
	return ps
}

func today() period.Date {
	loc, _ := time.LoadLocation("Europe/Paris")
	return period.FromTime(time.Now().In(loc))
}

// recordDiagnostics records, for every asset, each diagnostic its family
// needs, done a month ago, the DPE at the minimum class to let.
func recordDiagnostics(t *testing.T, tok string, assets ...string) {
	t.Helper()
	ps := fixtureParams(t)
	rule, err := ps.Diagnostics(today())
	if err != nil {
		t.Fatal(err)
	}
	minClass, err := ps.MinDPEClass(today())
	if err != nil {
		t.Fatal(err)
	}
	done := today().AddMonths(-1).String()
	for _, a := range assets {
		var asset struct{ Kind string }
		call(t, "GET", "/assets/"+a, req{tok: tok}).json(t, &asset)
		family := "dwelling"
		if asset.Kind == "box" {
			family = "box"
		}
		var have []struct{ Kind string }
		call(t, "GET", "/assets/"+a+"/diagnostics", req{tok: tok}).json(t, &have)
		got := map[string]bool{}
		for _, d := range have {
			got[d.Kind] = true
		}
		for _, k := range rule.Required[family] {
			if got[k] {
				continue
			}
			body := map[string]any{"kind": k, "done_on": done}
			if k == "dpe" {
				body["result"] = minClass
			}
			if r := call(t, "POST", "/assets/"+a+"/diagnostics", req{tok: tok, body: body}); r.Status != 201 {
				t.Fatalf("diagnostic %s of %s: %d %s", k, a, r.Status, r.Body)
			}
		}
	}
}

type leaseDoc struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Number     string `json:"number"`
	DocumentID string `json:"document_id"`
}

type docView struct {
	ID              string  `json:"id"`
	Class           string  `json:"class"`
	Source          string  `json:"source"`
	ContentType     string  `json:"content_type"`
	SignsDocumentID string  `json:"signs_document_id"`
	RetainUntil     *string `json:"retain_until"`
}

// readyToActivate does what activating a draft lease needs (RENT-SPEC-008,
// Q-22): the diagnostics of its assets, its contract, the signed copy of the
// contract and the entry inventory.
func readyToActivate(t *testing.T, tok, leaseID string) leaseDoc {
	t.Helper()
	var l struct {
		AssetIDs []string `json:"asset_ids"`
	}
	call(t, "GET", "/leases/"+leaseID, req{tok: tok}).json(t, &l)
	recordDiagnostics(t, tok, l.AssetIDs...)
	var ct leaseDoc
	r := call(t, "POST", "/leases/"+leaseID+"/contract", req{tok: tok})
	if r.Status != 201 {
		t.Fatalf("contract: %d %s", r.Status, r.Body)
	}
	r.json(t, &ct)
	if r := upload(t, tok, "", map[string]string{"class": "contract", "lease_id": leaseID, "signs_document_id": ct.DocumentID},
		"bail-signe.pdf", signedPDF); r.Status != 201 {
		t.Fatalf("signed copy: %d %s", r.Status, r.Body)
	}
	var inv docView
	r = upload(t, tok, "", map[string]string{"class": "inventory", "lease_id": leaseID}, "edl-entree.pdf", signedPDF)
	if r.Status != 201 {
		t.Fatalf("inventory upload: %d %s", r.Status, r.Body)
	}
	r.json(t, &inv)
	if r := call(t, "POST", "/leases/"+leaseID+"/inventories", req{tok: tok, body: map[string]any{"kind": "entry",
		"done_on": today().String(), "document_id": inv.ID}}); r.Status != 201 {
		t.Fatalf("entry inventory: %d %s", r.Status, r.Body)
	}
	return ct
}

func mustUUID(t *testing.T, s string) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func multipartWriter(w io.Writer) *multipart.Writer { return multipart.NewWriter(w) }
