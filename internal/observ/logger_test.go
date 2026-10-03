package observ

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// TestLoggerStampsScope proves Emit auto-stamps the canonical scope fields and
// the label from context.
func TestLoggerStampsScope(t *testing.T) {
	var buf bytes.Buffer
	lv := new(slog.LevelVar)
	lv.Set(slog.LevelInfo)
	log := NewJSONLogger(&buf, lv)

	ctx := WithScope(context.Background(), RequestScope{
		TraceID: "t-1", RequestID: "r-1", FlowID: "orders", FlowVersion: 3, Env: "prod",
	})
	log.Emit(ctx, "info", "node", map[string]any{FldNodeID: "n1"})

	line := decodeLine(t, buf.Bytes())
	for k, want := range map[string]any{
		FldTraceID: "t-1", FldRequestID: "r-1", FldFlowID: "orders",
		FldEnvironment: "prod", FldLabel: "node", FldNodeID: "n1",
	} {
		if line[k] != want {
			t.Fatalf("field %q = %v; want %v", k, line[k], want)
		}
	}
	if line[FldFlowVersion] != float64(3) {
		t.Fatalf("flow_version = %v; want 3", line[FldFlowVersion])
	}
}

// TestLoggerLevelGate proves a debug line is suppressed at an info floor and
// emitted once the floor is lowered without rebuilding the logger (AC-22).
func TestLoggerLevelGate(t *testing.T) {
	var buf bytes.Buffer
	lv := new(slog.LevelVar)
	lv.Set(slog.LevelInfo)
	log := NewJSONLogger(&buf, lv)

	log.Emit(context.Background(), "debug", "dbg", nil)
	if buf.Len() != 0 {
		t.Fatalf("debug line emitted at info floor: %s", buf.String())
	}

	lv.Set(slog.LevelDebug) // config toggle, no redeploy
	log.Emit(context.Background(), "debug", "dbg", nil)
	if buf.Len() == 0 {
		t.Fatalf("debug line not emitted after lowering floor")
	}
}

// TestLoggerRedactsFields proves a sensitive field never reaches the sink (AC-20).
func TestLoggerRedactsFields(t *testing.T) {
	var buf bytes.Buffer
	lv := new(slog.LevelVar)
	lv.Set(slog.LevelInfo)
	r := NewRedactor()
	const secret = "leak-me"
	RegisterSecret(r, secret)
	log := NewJSONLoggerWithRedactor(&buf, lv, r)

	log.Emit(context.Background(), "info", "evt", map[string]any{
		"authorization": "Bearer xyz",
		"note":          secret,
		"ok":            "fine",
	})

	if strings.Contains(buf.String(), "Bearer xyz") || strings.Contains(buf.String(), secret) {
		t.Fatalf("secret leaked in log: %s", buf.String())
	}
	line := decodeLine(t, buf.Bytes())
	if line["authorization"] != redactMask || line["note"] != redactMask {
		t.Fatalf("sensitive fields not masked: %v", line)
	}
	if line["ok"] != "fine" {
		t.Fatalf("non-secret field altered: %v", line["ok"])
	}
}

func decodeLine(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(trimLastLine(b), &m); err != nil {
		t.Fatalf("log not JSON: %v (%s)", err, string(b))
	}
	return m
}
