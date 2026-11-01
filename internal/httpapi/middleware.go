package httpapi

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/ovander/backendkit/ctxutil"

	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/problem"
)

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// logFields is filled by the inner middleware once the caller is known, so
// the outer access log can name the subject and scope.
type logFields struct{ sub, scope, role string }

type logKey struct{}

// accessLog logs every request as JSON with request id, subject, scope, route,
// status and duration (RENT-BE-001 §8); no personal data beyond ids.
func accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		f := &logFields{}
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r.WithContext(context.WithValue(r.Context(), logKey{}, f)))
		route := chi.RouteContext(r.Context()).RoutePattern()
		slog.InfoContext(r.Context(), "request", "request_id", ctxutil.GetRequestID(r.Context()), "method", r.Method,
			"route", route, "status", rec.status, "duration_ms", time.Since(start).Milliseconds(),
			"sub", f.sub, "scope", f.scope, "role", f.role)
	})
}

// identify copies the principal into the access-log fields.
func identify(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f, ok := r.Context().Value(logKey{}).(*logFields); ok {
			if p, ok := auth.FromContext(r.Context()); ok {
				f.sub = p.Sub
				if p.Active != nil {
					f.scope, f.role = p.Active.OwnerID.String(), string(p.Active.Role)
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// recoverer turns a panic into a logged 500 problem without a stack trace in
// the response.
func recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				slog.ErrorContext(r.Context(), "panic", "value", v, "request_id", ctxutil.GetRequestID(r.Context()))
				problem.Write(w, r, problem.Internal, "", "", nil)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// clientIP returns the client address: the peer, or, when the peer is a
// trusted proxy, the right-most X-Forwarded-For entry that is not one.
func clientIP(r *http.Request, trusted []*net.IPNet) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	isTrusted := func(s string) bool {
		ip := net.ParseIP(strings.TrimSpace(s))
		for _, n := range trusted {
			if ip != nil && n.Contains(ip) {
				return true
			}
		}
		return false
	}
	if !isTrusted(host) {
		return host
	}
	parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(parts) - 1; i >= 0; i-- {
		if p := strings.TrimSpace(parts[i]); p != "" && !isTrusted(p) {
			return p
		}
	}
	return host
}

// failedAuthLimiter locks out an address after max 401 responses within
// window (RENT-BE-001 §8b: 10 failed authentications per 15 minutes per IP).
type failedAuthLimiter struct {
	max     int
	window  time.Duration
	trusted []*net.IPNet
	now     func() time.Time
	mu      sync.Mutex
	fails   map[string][]time.Time
}

func newFailedAuthLimiter(max int, window time.Duration, trusted []*net.IPNet) *failedAuthLimiter {
	return &failedAuthLimiter{max: max, window: window, trusted: trusted, now: time.Now, fails: map[string][]time.Time{}}
}

func (l *failedAuthLimiter) recent(ip string, now time.Time) []time.Time {
	kept := l.fails[ip][:0]
	for _, t := range l.fails[ip] {
		if now.Sub(t) < l.window {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(l.fails, ip)
		return nil
	}
	l.fails[ip] = kept
	return kept
}

func (l *failedAuthLimiter) handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r, l.trusted)
		l.mu.Lock()
		blocked := len(l.recent(ip, l.now())) >= l.max
		l.mu.Unlock()
		if blocked {
			slog.WarnContext(r.Context(), "auth lockout", "ip", ip)
			w.Header().Set("Retry-After", "900")
			problem.Write(w, r, problem.TooManyFailedAuth, "", "", nil)
			return
		}
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		if rec.status == http.StatusUnauthorized {
			l.mu.Lock()
			l.fails[ip] = append(l.recent(ip, l.now()), l.now())
			l.mu.Unlock()
		}
	})
}

// byMethod sends reads and writes to different limiters (600 reads and 60
// writes per minute per subject, RENT-BE-001 §8b).
func byMethod(read, write func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		rh, wh := read(next), write(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet || r.Method == http.MethodHead {
				rh.ServeHTTP(w, r)
				return
			}
			wh.ServeHTTP(w, r)
		})
	}
}

// problemOn429 rewrites the rate limiter's 429 into a problem.
func problemOn429(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(&limitWriter{ResponseWriter: w, r: r}, r)
	})
}

type limitWriter struct {
	http.ResponseWriter
	r        *http.Request
	replaced bool
}

func (l *limitWriter) WriteHeader(code int) {
	if code == http.StatusTooManyRequests && !l.replaced {
		l.replaced = true
		slog.WarnContext(l.r.Context(), "rate limited", "route", l.r.URL.Path)
		problem.Write(l.ResponseWriter, l.r, problem.TooManyRequests, "", "", nil)
		return
	}
	l.ResponseWriter.WriteHeader(code)
}

func (l *limitWriter) Write(b []byte) (int, error) {
	if l.replaced {
		return len(b), nil
	}
	return l.ResponseWriter.Write(b)
}
