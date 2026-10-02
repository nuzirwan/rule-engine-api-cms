package connect

import (
	"errors"
	"fmt"
)

// ErrClass is the connect-package view of the cross-seam error taxonomy. Every
// error that leaves this slice is a *ConnError carrying one of these classes so
// a caller branches via errors.Is/As, never by matching on message strings (see
// the error-classification standard). It mirrors the flow package's taxonomy but
// is declared per-package so connect never imports flow.
type ErrClass int

const (
	// Internal is an unexpected fault inside this slice; the zero value.
	Internal ErrClass = iota
	// Timeout is a deadline exceeded / context cancellation (transient).
	Timeout
	// NotFound is a missing resource (unknown key, no rows, HTTP 404).
	NotFound
	// Validation is bad input or config: never retried, surfaced to the author.
	Validation
	// Upstream is a downstream dependency failure (reset, 5xx, breaker open; transient).
	Upstream
)

// String renders the class for logs and spans.
func (c ErrClass) String() string {
	switch c {
	case Timeout:
		return "timeout"
	case NotFound:
		return "not_found"
	case Validation:
		return "validation"
	case Upstream:
		return "upstream"
	default:
		return "internal"
	}
}

// Sentinel errors are the match targets for errors.Is. A ConnError reports its
// class through Is so callers test the class without touching message text.
var (
	// ErrInternal is the sentinel for an internal fault.
	ErrInternal = errors.New("internal error")
	// ErrTimeout is the sentinel for a timeout / cancellation.
	ErrTimeout = errors.New("timeout")
	// ErrNotFound is the sentinel for a missing resource.
	ErrNotFound = errors.New("not found")
	// ErrValidation is the sentinel for a validation failure.
	ErrValidation = errors.New("validation error")
	// ErrUpstream is the sentinel for a downstream dependency failure.
	ErrUpstream = errors.New("upstream error")
)

// sentinelFor returns the sentinel error for a class.
func sentinelFor(c ErrClass) error {
	switch c {
	case Timeout:
		return ErrTimeout
	case NotFound:
		return ErrNotFound
	case Validation:
		return ErrValidation
	case Upstream:
		return ErrUpstream
	default:
		return ErrInternal
	}
}

// ConnError is a classified error produced by a connector, client, or the
// registry. It carries the connection Key and Op kind (both safe to log) and
// wraps the originating cause with %w. A secret value is NEVER placed in a
// ConnError — only the key that names the connection. Error strings are
// lowercase with no trailing punctuation.
type ConnError struct {
	Class ErrClass
	Key   string // connection key (safe to log)
	Op    string // operation kind (safe to log)
	msg   string
	cause error
}

// Error implements error. The connection key and op are included when set so a
// failure is attributable; the wrapped cause is appended when present.
func (e *ConnError) Error() string {
	loc := e.Key
	if e.Op != "" {
		if loc != "" {
			loc += "/" + e.Op
		} else {
			loc = e.Op
		}
	}
	msg := e.msg
	if loc != "" {
		msg = fmt.Sprintf("%s [%s]", msg, loc)
	}
	if e.cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Class, msg, e.cause)
	}
	return fmt.Sprintf("%s: %s", e.Class, msg)
}

// Unwrap returns the originating cause for errors.Is/As traversal. When no cause
// was supplied it returns the class sentinel so errors.Is still matches the class.
func (e *ConnError) Unwrap() error {
	if e.cause != nil {
		return e.cause
	}
	return sentinelFor(e.Class)
}

// Is reports whether e matches the class sentinel for its class, so
// errors.Is(e, ErrValidation) is true for a Validation ConnError regardless of
// the wrapped cause.
func (e *ConnError) Is(target error) bool {
	return target == sentinelFor(e.Class)
}

// NewConnError builds a classified ConnError for callers OUTSIDE this package
// (the drivers subpackage) so a driver reports a classified failure without
// re-declaring the taxonomy. A nil cause is allowed. The key/op are safe to log;
// a secret value must never be passed in msg or cause.
func NewConnError(class ErrClass, key, op, msg string, cause error) *ConnError {
	return &ConnError{Class: class, Key: key, Op: op, msg: msg, cause: cause}
}

// newErr builds a classified ConnError without a cause.
func newErr(class ErrClass, key, op, msg string) *ConnError {
	return &ConnError{Class: class, Key: key, Op: op, msg: msg}
}

// wrapErr builds a classified ConnError wrapping cause with %w semantics.
func wrapErr(class ErrClass, key, op, msg string, cause error) *ConnError {
	return &ConnError{Class: class, Key: key, Op: op, msg: msg, cause: cause}
}

// classOf returns the taxonomy class of err. A *ConnError reports its own class;
// otherwise the sentinels are consulted via errors.Is and an unclassified error
// is treated as internal.
func classOf(err error) ErrClass {
	if err == nil {
		return Internal
	}
	var ce *ConnError
	if errors.As(err, &ce) {
		return ce.Class
	}
	switch {
	case errors.Is(err, ErrTimeout):
		return Timeout
	case errors.Is(err, ErrNotFound):
		return NotFound
	case errors.Is(err, ErrValidation):
		return Validation
	case errors.Is(err, ErrUpstream):
		return Upstream
	default:
		return Internal
	}
}
