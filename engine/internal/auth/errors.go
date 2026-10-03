package auth

import (
	"errors"
	"fmt"

	"nzr-rules-engine/internal/observ"
)

// Sentinel errors are the match targets for errors.Is so callers (and tests)
// branch on the failure kind without matching message text (per the Go idioms
// page). Error strings are lowercase with no trailing punctuation.
var (
	// ErrNoBearer is returned when the Authorization header is missing or not a
	// Bearer token.
	ErrNoBearer = errors.New("missing or malformed bearer token")
	// ErrTokenInvalid is returned for a token that fails parsing, signature,
	// exp/nbf, issuer or audience validation.
	ErrTokenInvalid = errors.New("token invalid")
	// ErrUnknownKey is returned when the token's kid is absent from the keyset
	// even after a refresh (rotation could not resolve it).
	ErrUnknownKey = errors.New("signing key not found")
	// ErrJWKSUnavailable is returned when the JWKS endpoint cannot be reached to
	// resolve a key; AuthN fails closed.
	ErrJWKSUnavailable = errors.New("jwks unavailable")
)

// authError is a classified AuthN/AuthZ error. It implements observ.Classified
// so its taxonomy class surfaces on the span/log without observ importing auth.
// AuthN faults are Validation (→ 401); a JWKS fetch failure is Upstream/Timeout
// (→ fail closed). Error strings stay lowercase with no trailing punctuation.
type authError struct {
	class observ.ErrorClass
	msg   string
	cause error
}

// Error implements error.
func (e *authError) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %v", e.msg, e.cause)
	}
	return e.msg
}

// Unwrap returns the wrapped cause for errors.Is/As traversal.
func (e *authError) Unwrap() error { return e.cause }

// ErrorClass implements observ.Classified.
func (e *authError) ErrorClass() string { return string(e.class) }

// validationErr builds a Validation-classified auth error wrapping cause.
func validationErr(msg string, cause error) error {
	return &authError{class: observ.ClassValidation, msg: msg, cause: cause}
}

// upstreamErr builds an Upstream-classified auth error (JWKS fetch failure).
func upstreamErr(msg string, cause error) error {
	return &authError{class: observ.ClassUpstream, msg: msg, cause: cause}
}
