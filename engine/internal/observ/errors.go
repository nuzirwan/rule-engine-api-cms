package observ

import (
	"context"
	"errors"
	"fmt"
)

// ErrorClass is the observ-package view of the cross-seam error taxonomy
// (Timeout|NotFound|Validation|Upstream|Internal). This slice does not invent
// classes; it reads the class off an error and surfaces it on spans, logs and
// metrics so a failure is attributable without string-matching (per the
// error-classification standard). It is declared per-package so observ never
// imports flow, connect or decision — those packages each own an equivalent
// taxonomy and classify at their own seam.
type ErrorClass string

// The canonical class values. These strings are used verbatim as span
// attributes, log fields and metric labels so a failure reads the same across
// all three pillars.
const (
	ClassTimeout    ErrorClass = "Timeout"
	ClassNotFound   ErrorClass = "NotFound"
	ClassValidation ErrorClass = "Validation"
	ClassUpstream   ErrorClass = "Upstream"
	ClassInternal   ErrorClass = "Internal"
)

// Classified is implemented by any error that can report its own taxonomy class
// as the canonical string. observ reads this via errors.As so a classified
// error from any slice surfaces its true class on the span/log/metric without
// observ importing that slice. The flow/connect/decision packages classify at
// their own seam; a caller that wants its class reflected here wraps with
// Classify (or implements this interface).
type Classified interface {
	ErrorClass() string
}

// classifiedError is observ's own classified error. The auth middleware and any
// observ helper produce these so a failure crossing into a span/log carries its
// class without string matching. Error strings are lowercase with no trailing
// punctuation (per the Go idioms page).
type classifiedError struct {
	class ErrorClass
	msg   string
	cause error
}

// Error implements error.
func (e *classifiedError) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %v", e.msg, e.cause)
	}
	return e.msg
}

// Unwrap returns the wrapped cause for errors.Is/As traversal.
func (e *classifiedError) Unwrap() error { return e.cause }

// ErrorClass implements Classified so ClassOf resolves this error directly.
func (e *classifiedError) ErrorClass() string { return string(e.class) }

// Classify wraps cause as a classified error of class. A nil cause yields a
// bare classified error carrying only msg. Error strings stay lowercase with no
// trailing punctuation.
func Classify(class ErrorClass, msg string, cause error) error {
	return &classifiedError{class: class, msg: msg, cause: cause}
}

// Validationf builds a ClassValidation error from a format string. It is the
// observ-side helper the auth slice uses so a bad-token / bad-config fault reads
// as Validation on the span and log.
func Validationf(format string, args ...any) error {
	return &classifiedError{class: ClassValidation, msg: fmt.Sprintf(format, args...)}
}

// ClassOf resolves the taxonomy class of err for a span/log/metric. Resolution
// order: nil => Internal (callers only pass non-nil in practice); an error that
// implements Classified reports its own class; a context deadline/cancel is
// Timeout; otherwise Internal. observ never classifies by message text and never
// imports another slice to read its concrete error type — slices that want their
// class reflected here implement Classified (which their ErrClass wrappers can
// adopt additively without a seam change).
func ClassOf(err error) ErrorClass {
	if err == nil {
		return ClassInternal
	}
	var c Classified
	if errors.As(err, &c) {
		switch ErrorClass(c.ErrorClass()) {
		case ClassTimeout, ClassNotFound, ClassValidation, ClassUpstream, ClassInternal:
			return ErrorClass(c.ErrorClass())
		}
		// A classifier that reports a lowercase/alias form (e.g. "timeout",
		// "not_found") still resolves to the canonical class.
		return normalizeClass(c.ErrorClass())
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return ClassTimeout
	}
	return ClassInternal
}

// normalizeClass maps the lowercase/underscore spellings that other slices'
// ErrClass.String() emit onto the canonical taxonomy values, so a classifier
// that was written before adopting Classified still reads correctly.
func normalizeClass(s string) ErrorClass {
	switch s {
	case "Timeout", "timeout":
		return ClassTimeout
	case "NotFound", "not_found":
		return ClassNotFound
	case "Validation", "validation":
		return ClassValidation
	case "Upstream", "upstream":
		return ClassUpstream
	default:
		return ClassInternal
	}
}
