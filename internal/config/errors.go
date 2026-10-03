package config

import (
	"errors"
	"fmt"
)

// ErrClass is the config-package view of the cross-seam error taxonomy, declared
// per-package so config carries no dependency on another slice's error type.
type ErrClass int

const (
	// Internal is an unexpected fault; the zero value.
	Internal ErrClass = iota
	// NotFound is a missing flow version, JDM, or route.
	NotFound
	// Validation is a malformed seed/config row or bad admin input.
	Validation
	// Timeout is a config-store query that exceeded its deadline (transient).
	Timeout
	// Upstream is a config dependency being unreachable: Postgres (degrade/
	// readiness not-ready) or Valkey (degrade to store, keep serving). (§7)
	Upstream
)

// String renders the class for logs and spans.
func (c ErrClass) String() string {
	switch c {
	case NotFound:
		return "not_found"
	case Validation:
		return "validation"
	case Timeout:
		return "timeout"
	case Upstream:
		return "upstream"
	default:
		return "internal"
	}
}

// Sentinel errors are the match targets for errors.Is.
var (
	// ErrInternal is the sentinel for an internal fault.
	ErrInternal = errors.New("internal error")
	// ErrNotFound is the sentinel for a missing resource.
	ErrNotFound = errors.New("not found")
	// ErrValidation is the sentinel for a validation failure.
	ErrValidation = errors.New("validation error")
	// ErrTimeout is the sentinel for a config-store deadline exceeded.
	ErrTimeout = errors.New("timeout")
	// ErrUpstream is the sentinel for an unreachable config dependency.
	ErrUpstream = errors.New("upstream error")
	// ErrUnvalidated is the sentinel for a publish attempt on a flow version that
	// has not passed validate (publish-blocking, AC-13). It is classified
	// Validation but carries this dedicated marker so the admin edge can map it to
	// 422 (not a generic 400) via errors.Is, without matching message text.
	ErrUnvalidated = errors.New("flow version not validated")
	// ErrRouteConflict is the sentinel for a flow (method,path) collision: a
	// different flow already owns the route (Postgres UNIQUE violation on
	// flows_method_path_key, SQLSTATE 23505). It is classified Validation so it
	// crosses the seam as a 4xx, and the dedicated sentinel lets the admin edge
	// map it to 409 (route already owned) rather than a misleading 502
	// (slice-f-admin-api.md §2.3). errors.Is(err, ErrRouteConflict) matches it.
	ErrRouteConflict = errors.New("route already owned by another flow")
)

// sentinelFor returns the sentinel error for a class.
func sentinelFor(c ErrClass) error {
	switch c {
	case NotFound:
		return ErrNotFound
	case Validation:
		return ErrValidation
	case Timeout:
		return ErrTimeout
	case Upstream:
		return ErrUpstream
	default:
		return ErrInternal
	}
}

// ConfigError is a classified error produced by the config store or seed loader.
// Error strings are lowercase with no trailing punctuation.
type ConfigError struct {
	Class ErrClass
	msg   string
	cause error
}

// Error implements error.
func (e *ConfigError) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Class, e.msg, e.cause)
	}
	return fmt.Sprintf("%s: %s", e.Class, e.msg)
}

// Unwrap returns the cause for errors.Is/As traversal, falling back to the class
// sentinel so errors.Is still matches the class.
func (e *ConfigError) Unwrap() error {
	if e.cause != nil {
		return e.cause
	}
	return sentinelFor(e.Class)
}

// Is reports whether e matches the class sentinel for its class.
func (e *ConfigError) Is(target error) bool {
	return target == sentinelFor(e.Class)
}

// newErr builds a classified ConfigError without a cause.
func newErr(class ErrClass, msg string) *ConfigError {
	return &ConfigError{Class: class, msg: msg}
}

// wrapErr builds a classified ConfigError wrapping cause with %w semantics.
func wrapErr(class ErrClass, msg string, cause error) *ConfigError {
	return &ConfigError{Class: class, msg: msg, cause: cause}
}
