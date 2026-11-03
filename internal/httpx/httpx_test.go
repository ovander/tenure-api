package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/problem"
)

func TestErrorMapping(t *testing.T) {
	cases := []struct {
		err    error
		status int
		typ    string
	}{
		{fmt.Errorf("render: %w", invariant.ErrUnavailable), http.StatusServiceUnavailable, problem.Unavailable},
		{fmt.Errorf("x: %w", invariant.ErrNotFound), http.StatusNotFound, problem.NotFound},
		{errors.New("boom"), http.StatusInternalServerError, problem.Internal},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		Error(w, httptest.NewRequest(http.MethodPost, "/api/v1/x", nil), c.err)
		var b problem.Body
		if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
			t.Fatalf("%v: %v", c.err, err)
		}
		if w.Code != c.status || b.Type != c.typ {
			t.Errorf("%v: %d %q, want %d %q", c.err, w.Code, b.Type, c.status, c.typ)
		}
	}
}
