package observ

import (
	"context"
	"io"
	"log/slog"
)

// scopedLogger is a Logger backed by log/slog writing JSON to an event stream
// (12-factor XI: logs to stdout). It auto-stamps the canonical scope fields from
// context so every line carries trace_id/request_id/flow_id/flow_version/
// environment (AC-21), gates on a dynamic level so an off debug line costs ~0,
// and routes every caller field through a central Redactor so no secret reaches
// the handler (AC-20).
type scopedLogger struct {
	base     *slog.Logger
	levelVar *slog.LevelVar
	redactor Redactor
}

// NewJSONLogger returns a Logger that writes JSON records to w, gated by lv so
// config can flip the effective level live without a redeploy (AC-22). A nil w
// is not permitted by callers; a nil lv defaults to info. Fields are redacted
// before emit (AC-20).
func NewJSONLogger(w io.Writer, lv *slog.LevelVar) Logger {
	return NewJSONLoggerWithRedactor(w, lv, NewRedactor())
}

// NewJSONLoggerWithRedactor is NewJSONLogger with an explicit Redactor so the
// composition root shares one Redactor (and its registered secret values) across
// the Logger, Tracer and dry-run collector.
func NewJSONLoggerWithRedactor(w io.Writer, lv *slog.LevelVar, r Redactor) Logger {
	if lv == nil {
		lv = new(slog.LevelVar)
		lv.Set(slog.LevelInfo)
	}
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lv})
	return &scopedLogger{base: slog.New(h), levelVar: lv, redactor: orNopRedactor(r)}
}

// Emit implements Logger. It gates on the dynamic level first (so a disabled
// debug line is nearly free), auto-stamps the request scope, stamps the label,
// then appends the caller fields after redaction.
func (l *scopedLogger) Emit(ctx context.Context, level, label string, fields map[string]any) {
	if ctx == nil {
		ctx = context.Background()
	}
	lvl := parseLevel(level)
	if !l.base.Enabled(ctx, lvl) {
		return
	}
	attrs := make([]slog.Attr, 0, len(fields)+8)
	if rs, ok := ScopeFrom(ctx); ok {
		// trace_id comes from the active span context when present so a log line
		// and its span share the exact value (correlation id == trace id); it
		// falls back to the scope's own id otherwise.
		traceID := TraceIDFromContext(ctx)
		if traceID == "" {
			traceID = rs.TraceID
		}
		if traceID != "" {
			attrs = append(attrs, slog.String(FldTraceID, traceID))
		}
		if rs.RequestID != "" {
			attrs = append(attrs, slog.String(FldRequestID, rs.RequestID))
		}
		if rs.FlowID != "" {
			attrs = append(attrs, slog.String(FldFlowID, rs.FlowID))
		}
		if rs.FlowVersion != 0 {
			attrs = append(attrs, slog.Int(FldFlowVersion, rs.FlowVersion))
		}
		if rs.Env != "" {
			attrs = append(attrs, slog.String(FldEnvironment, rs.Env))
		}
	}
	if label != "" {
		attrs = append(attrs, slog.String(FldLabel, label))
	}
	for k, v := range l.redactor.Scrub(fields) {
		attrs = append(attrs, slog.Any(k, v))
	}
	l.base.LogAttrs(ctx, lvl, "flow", attrs...)
}

// SetLevel changes the logger's effective level at runtime. It is used by the
// config-toggle path (AC-22) to lower the global floor to debug without a
// redeploy; it is a no-op if the logger was not built with a LevelVar.
func (l *scopedLogger) SetLevel(level string) {
	if l.levelVar != nil {
		l.levelVar.Set(parseLevel(level))
	}
}
