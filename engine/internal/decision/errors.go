package decision

import (
	"context"
	"errors"
	"fmt"
)

// ErrClass is the decision-package view of the cross-seam error taxonomy. Every
// error that leaves this slice is a *DecisionError carrying one of these classes
// so a caller branches via errors.Is/As, never by matching message strings (see
// the error-classification standard). It is declared per-package so decision
// never imports flow or connect.
type ErrClass int

const (
	// Internal is an unexpected fault (recovered panic, impossible state); zero value.
	Internal ErrClass = iota
	// Timeout is a ctx deadline/cancel observed around an evaluation.
	Timeout
	// NotFound is a missing JDM for an id/version.
	NotFound
	// Validation is a malformed JDM, a bad input shape, or the no-CGO stub.
	Validation
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
	default:
		return "internal"
	}
}

// Sentinel errors are the match targets for errors.Is. A DecisionError reports
// its class through Is so callers test the class without touching message text.
var (
	// ErrInternal is the sentinel for an internal fault.
	ErrInternal = errors.New("internal error")
	// ErrTimeout is the sentinel for a timeout / cancellation.
	ErrTimeout = errors.New("timeout")
	// ErrNotFound is the sentinel for a missing JDM.
	ErrNotFound = errors.New("not found")
	// ErrValidation is the sentinel for a validation failure.
	ErrValidation = errors.New("validation error")
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
	default:
		return ErrInternal
	}
}

// DecisionError is a classified error produced by the decision engine. It wraps
// the originating cause with %w and carries the JDM id/version (safe to log).
// Error strings are lowercase with no trailing punctuation.
type DecisionError struct {
	Class   ErrClass
	JDMID   string
	Version int
	msg     string
	cause   error
}

// Error implements error.
func (e *DecisionError) Error() string {
	loc := e.JDMID
	if e.Version != 0 {
		loc = fmt.Sprintf("%s@%d", e.JDMID, e.Version)
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

// Unwrap returns the cause for errors.Is/As traversal, falling back to the class
// sentinel so errors.Is still matches the class when there is no wrapped cause.
func (e *DecisionError) Unwrap() error {
	if e.cause != nil {
		return e.cause
	}
	return sentinelFor(e.Class)
}

// Is reports whether e matches the class sentinel for its class.
func (e *DecisionError) Is(target error) bool {
	return target == sentinelFor(e.Class)
}

// newErr builds a classified DecisionError without a cause.
func newErr(class ErrClass, jdmID string, version int, msg string) *DecisionError {
	return &DecisionError{Class: class, JDMID: jdmID, Version: version, msg: msg}
}

// wrapErr builds a classified DecisionError wrapping cause with %w semantics.
func wrapErr(class ErrClass, jdmID string, version int, msg string, cause error) *DecisionError {
	return &DecisionError{Class: class, JDMID: jdmID, Version: version, msg: msg, cause: cause}
}

// classifyLoadErr maps a JDMLoader error into the taxonomy. An already-classified
// *DecisionError passes through unchanged; a ctx deadline/cancel => Timeout; any
// other loader error is a miss => NotFound (the thin-slice in-memory loader only
// ever reports a miss, so this preserves today's behavior while a future
// DB-backed loader keeps its own classified Timeout/Validation).
func classifyLoadErr(jdmID string, err error) error {
	if err == nil {
		return nil
	}
	var de *DecisionError
	if errors.As(err, &de) {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return wrapErr(Timeout, jdmID, 0, "load jdm", err)
	}
	return wrapErr(NotFound, jdmID, 0, "load jdm", err)
}

// classifyEvalErr maps an error observed around a compile/evaluate into the
// taxonomy. A ctx deadline/cancel => Timeout; anything else from zen-go is a
// malformed-graph / bad-input fault => Validation. A recovered panic is mapped
// to Internal by the caller before reaching here.
func classifyEvalErr(jdmID string, version int, err error) error {
	if err == nil {
		return nil
	}
	var de *DecisionError
	if errors.As(err, &de) {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return wrapErr(Timeout, jdmID, version, "evaluation cancelled", err)
	}
	return wrapErr(Validation, jdmID, version, "evaluate decision", err)
}
