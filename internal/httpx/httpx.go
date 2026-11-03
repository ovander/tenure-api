// Package httpx holds the HTTP helpers every module handler uses: strict JSON
// decoding, responses, ETag and If-Match, paging, and the mapping of service
// errors to problem details. It imports no module, so module handlers can use
// it while internal/httpapi wires them into the router.
package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/ovander/backendkit/ctxutil"

	"github.com/ovander/tenure-api/internal/auth"
	"github.com/ovander/tenure-api/internal/invariant"
	"github.com/ovander/tenure-api/internal/problem"
)

// Lang returns the caller's language for messages.
func Lang(r *http.Request) string {
	if p, ok := auth.FromContext(r.Context()); ok {
		return p.Language
	}
	return "fr"
}

// RequestID returns the request id set by the RequestID middleware.
func RequestID(r *http.Request) string { return ctxutil.GetRequestID(r.Context()) }

// Decode reads a JSON body into v, refusing another content type (415),
// unknown fields and trailing data (400). It writes the problem and returns
// false on failure.
func Decode(w http.ResponseWriter, r *http.Request, v any) bool {
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mt != "application/json" {
		problem.Write(w, r, problem.UnsupportedMedia, Lang(r), "", nil)
		return false
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			problem.Write(w, r, problem.PayloadTooLarge, Lang(r), "", nil)
			return false
		}
		problem.Write(w, r, problem.BadRequest, Lang(r), "corps JSON invalide : "+err.Error(), nil)
		return false
	}
	if _, err := dec.Token(); err != io.EOF {
		problem.Write(w, r, problem.BadRequest, Lang(r), "données après l’objet JSON", nil)
		return false
	}
	return true
}

// JSON writes v with status.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ETag is the entity tag of a resource version.
func ETag(version int) string { return fmt.Sprintf(`W/"%d"`, version) }

// Resource writes a single resource with its ETag.
func Resource(w http.ResponseWriter, status, version int, v any) {
	w.Header().Set("ETag", ETag(version))
	JSON(w, status, v)
}

// IfMatch returns the version an update expects, from If-Match. A missing
// header is 428 and a malformed one 412; it writes the problem and returns
// false then.
func IfMatch(w http.ResponseWriter, r *http.Request) (int, bool) {
	h := strings.TrimSpace(r.Header.Get("If-Match"))
	if h == "" {
		problem.Write(w, r, problem.PreconditionRequired, Lang(r), "", nil)
		return 0, false
	}
	h = strings.TrimPrefix(h, "W/")
	v, err := strconv.Atoi(strings.Trim(h, `"`))
	if err != nil || v <= 0 {
		problem.Write(w, r, problem.PreconditionFailed, Lang(r), "", nil)
		return 0, false
	}
	return v, true
}

// Page is a page request: ?page= (from 1) and ?size= (1 to 100, default 20).
type Page struct {
	Number int
	Size   int
}

// Offset returns the row offset of the page.
func (p Page) Offset() int { return (p.Number - 1) * p.Size }

// ParsePage reads the page parameters, writing a 400 problem for invalid ones.
func ParsePage(w http.ResponseWriter, r *http.Request) (Page, bool) {
	p := Page{Number: 1, Size: 20}
	q := r.URL.Query()
	for key, dst := range map[string]*int{"page": &p.Number, "size": &p.Size} {
		if v := q.Get(key); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				problem.Write(w, r, problem.BadRequest, Lang(r), key+" doit être un entier positif", nil)
				return p, false
			}
			*dst = n
		}
	}
	if p.Size > 100 {
		p.Size = 100
	}
	return p, true
}

// List is a page of items.
type List[T any] struct {
	Items []T `json:"items"`
	Page  int `json:"page"`
	Size  int `json:"size"`
	Total int `json:"total"`
}

// NewList returns a page of items, never with a null items array.
func NewList[T any](items []T, p Page, total int) List[T] {
	if items == nil {
		items = []T{}
	}
	return List[T]{Items: items, Page: p.Number, Size: p.Size, Total: total}
}

// UUIDParam parses a path parameter as a UUID, writing 404 when it is not one.
func UUIDParam(w http.ResponseWriter, r *http.Request, value string) (uuid.UUID, bool) {
	id, err := uuid.Parse(value)
	if err != nil {
		problem.Write(w, r, problem.NotFound, Lang(r), "", nil)
		return uuid.Nil, false
	}
	return id, true
}

// Error maps a service error to its problem: a business rule to its type, a
// field list to 422 with errors[], not-found to 404, a stale version to 412,
// forbidden to 403, an unavailable dependency to a logged 503, anything else
// to a logged 500.
func Error(w http.ResponseWriter, r *http.Request, err error) {
	lang := Lang(r)
	var inv *invariant.Error
	var fields invariant.Fields
	switch {
	case errors.As(err, &inv):
		if _, ok := problem.Lookup(inv.Type); ok {
			problem.Write(w, r, inv.Type, lang, inv.Detail, nil)
			return
		}
		slog.ErrorContext(r.Context(), "unregistered problem type", "type", inv.Type)
	case errors.As(err, &fields):
		fe := make([]problem.FieldError, len(fields))
		for i, f := range fields {
			fe[i] = problem.FieldError{Field: f.Field, Message: f.Message}
		}
		problem.Write(w, r, problem.Validation, lang, "", fe)
		return
	case errors.Is(err, invariant.ErrNotFound):
		problem.Write(w, r, problem.NotFound, lang, "", nil)
		return
	case errors.Is(err, invariant.ErrStale):
		problem.Write(w, r, problem.PreconditionFailed, lang, "", nil)
		return
	case errors.Is(err, invariant.ErrForbidden):
		problem.Write(w, r, problem.Forbidden, lang, "", nil)
		return
	case errors.Is(err, invariant.ErrUnavailable):
		slog.WarnContext(r.Context(), "dependency unavailable", "error", err, "request_id", RequestID(r))
		problem.Write(w, r, problem.Unavailable, lang, "", nil)
		return
	}
	slog.ErrorContext(r.Context(), "request failed", "error", err, "request_id", RequestID(r))
	problem.Write(w, r, problem.Internal, lang, "", nil)
}
