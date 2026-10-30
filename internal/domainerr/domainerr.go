// Package domainerr defines the error vocabulary shared by domain modules.
//
// Each module declares its own sentinel errors with a stable Code. Transport
// layers map Kind to a status and expose Code to clients; callers wrap
// sentinels with fmt.Errorf("context: %w", err) and match with errors.Is.
package domainerr

import (
	"errors"
	"fmt"
)

// Kind classifies a business failure independently of any transport.
type Kind string

const (
	// KindInvalid means the input violates a format or business rule.
	KindInvalid Kind = "invalid"
	// KindNotFound means a referenced resource does not exist.
	KindNotFound Kind = "not_found"
	// KindConflict means the request competes with current state, e.g. the
	// last asset is already allocated or an idempotency key is reused.
	KindConflict Kind = "conflict"
	// KindForbidden means the actor lacks permission or ownership.
	KindForbidden Kind = "forbidden"
	// KindInvalidState means the action is not allowed from the current state.
	KindInvalidState Kind = "invalid_state"
)

// Error is a business error with a stable machine-readable code.
type Error struct {
	Kind    Kind
	Code    string
	Message string
}

// New declares a sentinel business error.
func New(kind Kind, code, message string) *Error {
	return &Error{Kind: kind, Code: code, Message: message}
}

func (e *Error) Error() string {
	return e.Code + ": " + e.Message
}

// ErrValidation is the sentinel behind every FieldError.
var ErrValidation = New(KindInvalid, "validation_failed", "input validation failed")

// FieldError reports which input field failed validation.
type FieldError struct {
	Field  string
	Reason string
}

func (e *FieldError) Error() string {
	return fmt.Sprintf("%s: %s: %s", ErrValidation.Code, e.Field, e.Reason)
}

func (e *FieldError) Unwrap() error { return ErrValidation }

// Invalid returns a validation error for a single field.
func Invalid(field, reason string) error {
	return &FieldError{Field: field, Reason: reason}
}

// KindOf returns the Kind of the first business error in err's chain, or ""
// when err is not a business error (and should be treated as internal).
func KindOf(err error) Kind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return ""
}
