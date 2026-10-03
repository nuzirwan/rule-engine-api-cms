package flow

import (
	"errors"
	"fmt"
	"nzr-rules-engine/internal/connect"
	"nzr-rules-engine/internal/decision"
)

// ErrClass is the cross-seam error taxonomy. Every error that crosses a slice
// boundary is classified so the caller can branch via errors.Is/As rather than
// matching on message strings (see the error-classification standard).
type ErrClass int

const (
	// ClassInternal is an unexpected engine fault; the default for an unclassified error.
	ClassInternal ErrClass = iota
	// ClassTimeout is a deadline exceeded / context cancellation.
	ClassTimeout
	// ClassNotFound is a missing resource (connection key, flow, row).
	ClassNotFound
	// ClassValidation is bad input or config: never retried, surfaced to the author.
	ClassValidation
	// ClassUpstream is a downstream dependency failure (source/service).
	ClassUpstream
)

// String renders the class for logs and spans.
func (c ErrClass) String() string {
	switch c {
	case ClassTimeout:
		return "timeout"
	case ClassNotFound:
		return "not_found"
	case ClassValidation:
		return "validation"
	case ClassUpstream:
		return "upstream"
	default:
		return "internal"
	}
}

// Sentinel errors are the match targets for errors.Is. Classified errors wrap
// the sentinel for their class so callers test the class without string matching.
var (
	// ErrInternal is the sentinel for an internal engine fault.
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
	case ClassTimeout:
		return ErrTimeout
	case ClassNotFound:
		return ErrNotFound
	case ClassValidation:
		return ErrValidation
	case ClassUpstream:
		return ErrUpstream
	default:
		return ErrInternal
	}
}

// FlowError is a classified error produced by the interpreter. It wraps both the
// class sentinel (so errors.Is(err, ErrValidation) works) and the originating
// cause (so errors.Unwrap/As reaches it). Error strings are lowercase with no
// trailing punctuation.
type FlowError struct {
	Class ErrClass
	msg   string
	cause error
}

// Error implements error.
func (e *FlowError) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Class, e.msg, e.cause)
	}
	return fmt.Sprintf("%s: %s", e.Class, e.msg)
}

// Unwrap returns the originating cause for errors.Is/As traversal. When no cause
// was supplied it returns the class sentinel so errors.Is still matches the class.
func (e *FlowError) Unwrap() error {
	if e.cause != nil {
		return e.cause
	}
	return sentinelFor(e.Class)
}

// Is reports whether e matches the class sentinel for its class, so
// errors.Is(e, ErrValidation) is true for a ClassValidation FlowError regardless
// of the wrapped cause.
func (e *FlowError) Is(target error) bool {
	return target == sentinelFor(e.Class)
}

// newErr builds a classified FlowError without a cause.
func newErr(class ErrClass, msg string) *FlowError {
	return &FlowError{Class: class, msg: msg}
}

// wrapErr builds a classified FlowError wrapping cause with %w semantics.
func wrapErr(class ErrClass, msg string, cause error) *FlowError {
	return &FlowError{Class: class, msg: msg, cause: cause}
}

// validationf builds a ClassValidation error from a format string.
func validationf(format string, args ...any) *FlowError {
	return newErr(ClassValidation, fmt.Sprintf(format, args...))
}

// classOf returns the taxonomy class of err. A *FlowError reports its own class;
// a *connect.ConnError or *decision.DecisionError crossing the seam has its
// per-package class translated into the flow taxonomy (so a driver's Upstream /
// NotFound / Timeout / Validation is preserved rather than collapsed to
// internal); otherwise the sentinels and context errors are consulted via
// errors.Is, and an unclassified error is treated as internal.
func classOf(err error) ErrClass {
	if err == nil {
		return ClassInternal
	}
	var fe *FlowError
	if errors.As(err, &fe) {
		return fe.Class
	}
	var ce *connect.ConnError
	if errors.As(err, &ce) {
		return classFromConnect(ce.Class)
	}
	var de *decision.DecisionError
	if errors.As(err, &de) {
		return classFromDecision(de.Class)
	}
	switch {
	case errors.Is(err, ErrTimeout):
		return ClassTimeout
	case errors.Is(err, ErrNotFound):
		return ClassNotFound
	case errors.Is(err, ErrValidation):
		return ClassValidation
	case errors.Is(err, ErrUpstream):
		return ClassUpstream
	default:
		return ClassInternal
	}
}

// classFromConnect maps the connect package's error class onto the flow taxonomy
// so a driver-returned *connect.ConnError keeps its true class across the seam.
func classFromConnect(c connect.ErrClass) ErrClass {
	switch c {
	case connect.Timeout:
		return ClassTimeout
	case connect.NotFound:
		return ClassNotFound
	case connect.Validation:
		return ClassValidation
	case connect.Upstream:
		return ClassUpstream
	default:
		return ClassInternal
	}
}

// classFromDecision maps the decision package's error class onto the flow
// taxonomy. The decision slice has no Upstream class; a malformed JDM / bad input
// is Validation and a missing JDM is NotFound.
func classFromDecision(c decision.ErrClass) ErrClass {
	switch c {
	case decision.Timeout:
		return ClassTimeout
	case decision.NotFound:
		return ClassNotFound
	case decision.Validation:
		return ClassValidation
	default:
		return ClassInternal
	}
}

// classify ensures an error crossing the engine seam is a classified FlowError.
// A nil error stays nil; an already-classified error passes through; any other
// error is wrapped under the class inferred from its sentinels (internal by
// default) so the caller never sees an unclassified error at the seam.
func classify(err error) error {
	if err == nil {
		return nil
	}
	var fe *FlowError
	if errors.As(err, &fe) {
		return err
	}
	return wrapErr(classOf(err), "flow failed", err)
}
