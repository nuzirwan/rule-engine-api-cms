// Package observ provides the minimal tracing and logging primitives that the
// flow interpreter reaches through Deps (Slice E, lld-contracts.md). The thin
// slice ships a basic log/slog-backed implementation that is no-op-safe: a zero
// value never panics and every exported constructor returns a usable value.
//
// The interfaces (Tracer, Span, Logger) are the frozen seams; the slog-backed
// types here are one implementation of them. cmd/engine wires these; unit tests
// may substitute fakes.
package observ

import (
	"context"
	"log/slog"
)

// Tracer starts spans around a unit of work. Every flow node and the root run
// are wrapped in a span so a slow source is attributable to the exact node.
type Tracer interface {
	// StartSpan opens a span named name with the given attributes and returns a
	// child context carrying the span plus the Span handle to close it.
	StartSpan(ctx context.Context, name string, attrs map[string]any) (context.Context, Span)
}

// Span is one open span. End closes it, recording err when non-nil; Set adds or
// overwrites a single attribute while the span is open.
type Span interface {
	End(err error)
	Set(attr string, v any)
}

// Logger emits a structured log record. label names the event; fields carry the
// structured context (ids, keys) per the error-classification standard.
type Logger interface {
	Emit(ctx context.Context, level, label string, fields map[string]any)
}

// slogTracer is a Tracer backed by a *slog.Logger. A span logs its attributes at
// debug on start and its outcome (ok/error) at debug/error on End.
type slogTracer struct {
	log *slog.Logger
}

// NewTracer returns a Tracer that records spans to log. A nil log falls back to
// slog.Default so the returned Tracer is always safe to use.
func NewTracer(log *slog.Logger) Tracer {
	if log == nil {
		log = slog.Default()
	}
	return &slogTracer{log: log}
}

// StartSpan implements Tracer.
func (t *slogTracer) StartSpan(ctx context.Context, name string, attrs map[string]any) (context.Context, Span) {
	if ctx == nil {
		ctx = context.Background()
	}
	s := &slogSpan{log: t.log, name: name, attrs: cloneAttrs(attrs)}
	s.log.DebugContext(ctx, "span.start", slog.String("span", name), slog.Any("attrs", s.attrs))
	return ctx, s
}

// slogSpan is one span opened by slogTracer. It is not safe for concurrent Set.
type slogSpan struct {
	log   *slog.Logger
	name  string
	attrs map[string]any
	ended bool
}

// End implements Span. Calling End more than once is a no-op after the first.
func (s *slogSpan) End(err error) {
	if s.ended {
		return
	}
	s.ended = true
	if err != nil {
		s.log.Error("span.end", slog.String("span", s.name), slog.Any("attrs", s.attrs), slog.Any("err", err))
		return
	}
	s.log.Debug("span.end", slog.String("span", s.name), slog.Any("attrs", s.attrs))
}

// Set implements Span.
func (s *slogSpan) Set(attr string, v any) {
	if s.attrs == nil {
		s.attrs = make(map[string]any, 1)
	}
	s.attrs[attr] = v
}

// slogLogger is a Logger backed by a *slog.Logger.
type slogLogger struct {
	log *slog.Logger
}

// NewLogger returns a Logger that writes records to log. A nil log falls back to
// slog.Default so the returned Logger is always safe to use.
func NewLogger(log *slog.Logger) Logger {
	if log == nil {
		log = slog.Default()
	}
	return &slogLogger{log: log}
}

// Emit implements Logger. An unknown level is emitted at info.
func (l *slogLogger) Emit(ctx context.Context, level, label string, fields map[string]any) {
	if ctx == nil {
		ctx = context.Background()
	}
	l.log.Log(ctx, parseLevel(level), label, slog.Any("fields", fields))
}

// parseLevel maps a textual level to slog.Level, defaulting to info.
func parseLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// cloneAttrs copies attrs so a span owns its map independent of the caller.
func cloneAttrs(attrs map[string]any) map[string]any {
	if attrs == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(attrs))
	for k, v := range attrs {
		out[k] = v
	}
	return out
}
