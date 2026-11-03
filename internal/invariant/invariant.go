// Package invariant holds the typed business-rule error every service returns
// (RENT-BE-001 §8: ErrInvariant{Type, Detail}). Type is the stable
// problem-detail type, e.g. "lease/consistent-ownership-shares"; the HTTP
// layer maps it to a 409 or 422 and to the French (or English) message shown
// to the user. Detail is for logs and tests, in English, without personal data.
package invariant

import (
	"errors"
	"fmt"
)

// Error is a violated business rule.
type Error struct {
	Type   string `json:"type"`
	Detail string `json:"detail"`
}

// Error implements error.
func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Type, e.Detail) }

// New returns an Error of type t.
func New(t, detail string) *Error { return &Error{Type: t, Detail: detail} }

// Is reports whether err is or wraps an Error of type t.
func Is(err error, t string) bool {
	var e *Error
	return errors.As(err, &e) && e.Type == t
}

// Errors every service returns besides business rules; the HTTP layer maps
// them to 404, 412, 403 and 503. ErrUnavailable is a dependency that could
// not serve the request now (the request failed closed and may be retried).
var (
	ErrNotFound    = errors.New("not found")
	ErrStale       = errors.New("version mismatch")
	ErrForbidden   = errors.New("forbidden")
	ErrUnavailable = errors.New("dependency unavailable")
)

// Field is one invalid input field.
type Field struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Fields is a list of invalid input fields, returned as one error (422
// validation/invalid with errors[]). Messages are French: they are shown
// inline next to the field.
type Fields []Field

// Error implements error.
func (f Fields) Error() string { return fmt.Sprintf("%d invalid field(s)", len(f)) }

// Add appends a field error.
func (f *Fields) Add(field, message string) { *f = append(*f, Field{field, message}) }

// Err returns f as an error, or nil when empty.
func (f Fields) Err() error {
	if len(f) == 0 {
		return nil
	}
	return f
}
