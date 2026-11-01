package problem

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestCatalogueComplete(t *testing.T) {
	for k, m := range Types() {
		if m.Status < 400 || m.FR == "" || m.EN == "" || m.FR == m.EN {
			t.Errorf("%s: %+v needs a status, a French and an English title", k, m)
		}
	}
}

func TestWrite(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("X-Request-ID", "abc")
	Write(rec, req, PreconditionFailed, "", "version 3", nil)
	var b Body
	_ = json.Unmarshal(rec.Body.Bytes(), &b)
	if rec.Code != 412 || b.Type != PreconditionFailed || b.Title != catalogue[PreconditionFailed].FR ||
		b.Instance != "urn:request:abc" || rec.Header().Get("Content-Type") != "application/problem+json" {
		t.Errorf("%d %+v", rec.Code, b)
	}
	rec = httptest.NewRecorder()
	Write(rec, nil, "no/such-type", "en", "secret detail", nil)
	b = Body{}
	_ = json.Unmarshal(rec.Body.Bytes(), &b)
	if rec.Code != 500 || b.Type != Internal || b.Detail != "" || b.Title != "Internal error." {
		t.Errorf("unknown type leaked: %d %+v", rec.Code, b)
	}
}
