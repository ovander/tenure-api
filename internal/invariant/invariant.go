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
