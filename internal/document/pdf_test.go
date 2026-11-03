package document

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ovander/tenure-api/internal/invariant"
)

func gotenbergAnswering(t *testing.T, answers ...int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := int(calls.Add(1)) - 1
		status := answers[min(n, len(answers)-1)]
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = w.Write([]byte("%PDF-1.7\n%%EOF\n"))
		} else {
			_, _ = w.Write([]byte("Service Unavailable"))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func isUnavailable(err error) bool {
	var inv *invariant.Error
	return errors.As(err, &inv) && inv.Type == TypePDFUnavailable
}

// A busy or restarting gotenberg (503) is retried once; still unavailable,
// or unreachable, or timing out, it is the 503 problem; anything else stays
// an internal error (Q-101).
func TestConvertUnavailable(t *testing.T) {
	old := pdfRetryDelay
	pdfRetryDelay = 10 * time.Millisecond
	t.Cleanup(func() { pdfRetryDelay = old })
	ctx := context.Background()

	srv, calls := gotenbergAnswering(t, 503, 200)
	if out, err := (Gotenberg{URL: srv.URL}).Convert(ctx, []byte("<p>x</p>"), nil); err != nil || len(out) == 0 || calls.Load() != 2 {
		t.Errorf("503 then 200: %v after %d calls", err, calls.Load())
	}

	srv, calls = gotenbergAnswering(t, 503)
	if _, err := (Gotenberg{URL: srv.URL}).Convert(ctx, nil, nil); !isUnavailable(err) || calls.Load() != 2 {
		t.Errorf("503 twice: %v after %d calls", err, calls.Load())
	}

	for _, status := range []int{502, 504} {
		srv, calls = gotenbergAnswering(t, status)
		if _, err := (Gotenberg{URL: srv.URL}).Convert(ctx, nil, nil); !isUnavailable(err) || calls.Load() != 1 {
			t.Errorf("%d: %v after %d calls (no retry)", status, err, calls.Load())
		}
	}

	srv, _ = gotenbergAnswering(t, 400)
	if _, err := (Gotenberg{URL: srv.URL}).Convert(ctx, nil, nil); err == nil || isUnavailable(err) {
		t.Errorf("400 is a refused conversion, not an outage: %v", err)
	}

	srv, _ = gotenbergAnswering(t, 200)
	srv.Close() // unreachable
	if _, err := (Gotenberg{URL: srv.URL}).Convert(ctx, nil, nil); !isUnavailable(err) {
		t.Errorf("unreachable: %v", err)
	}

	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	t.Cleanup(slow.Close)
	if _, err := (Gotenberg{URL: slow.URL, Client: &http.Client{Timeout: 50 * time.Millisecond}}).Convert(ctx, nil, nil); !isUnavailable(err) {
		t.Errorf("timeout: %v", err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	srv, _ = gotenbergAnswering(t, 200)
	if _, err := (Gotenberg{URL: srv.URL}).Convert(cancelled, nil, nil); err == nil || isUnavailable(err) {
		t.Errorf("a cancelled request is not an outage: %v", err)
	}
}
