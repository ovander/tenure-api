package document

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ovander/tenure-api/internal/invariant"
)

// TestGotenbergErrors pins how the engine's answers map: a busy or
// unreachable engine is ErrPDFUnavailable (503 http/unavailable, fail closed),
// anything else stays an internal error.
func TestGotenbergErrors(t *testing.T) {
	cases := []struct {
		name        string
		status      int
		body        string
		unavailable bool
	}{
		// gotenberg's --api-timeout, hit while it starts or restarts Chromium
		// or LibreOffice (#45).
		{"timed out", http.StatusServiceUnavailable, "Service Unavailable", true},
		{"queue full", http.StatusTooManyRequests, "Too Many Requests", true},
		{"bad request", http.StatusBadRequest, "Invalid form data", false},
		{"engine error", http.StatusInternalServerError, "Internal Server Error", false},
		{"not a PDF", http.StatusOK, "<html>", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/forms/chromium/convert/html" {
					t.Errorf("path %s", r.URL.Path)
				}
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(c.body))
			}))
			defer srv.Close()
			_, err := Gotenberg{URL: srv.URL}.Convert(context.Background(), []byte("<p>x</p>"), nil)
			if err == nil {
				t.Fatal("no error")
			}
			if got := errors.Is(err, ErrPDFUnavailable); got != c.unavailable {
				t.Errorf("%v: ErrPDFUnavailable = %v, want %v", err, got, c.unavailable)
			}
			if got := errors.Is(err, invariant.ErrUnavailable); got != c.unavailable {
				t.Errorf("%v: invariant.ErrUnavailable = %v, want %v", err, got, c.unavailable)
			}
		})
	}

	t.Run("unreachable", func(t *testing.T) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := l.Addr().String()
		_ = l.Close()
		_, err = Gotenberg{URL: "http://" + addr}.Convert(context.Background(), []byte("<p>x</p>"), nil)
		if !errors.Is(err, ErrPDFUnavailable) {
			t.Errorf("%v; want ErrPDFUnavailable", err)
		}
	})

	t.Run("caller cancelled", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		defer srv.Close()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := Gotenberg{URL: srv.URL}.Convert(ctx, []byte("<p>x</p>"), nil)
		if err == nil || errors.Is(err, ErrPDFUnavailable) {
			t.Errorf("%v; want a non-unavailable error", err)
		}
	})

	t.Run("ok", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("%PDF-1.7\n"))
		}))
		defer srv.Close()
		if _, err := (Gotenberg{URL: srv.URL}).Convert(context.Background(), []byte("<p>x</p>"), nil); err != nil {
			t.Error(err)
		}
	})
}
