package observ

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestDryRunFlag proves WithDryRun/IsDryRun carry the write-suppression flag and
// that live traffic (no flag) reads false.
func TestDryRunFlag(t *testing.T) {
	if IsDryRun(context.Background()) {
		t.Fatalf("live context reported dry-run")
	}
	if !IsDryRun(WithDryRun(context.Background())) {
		t.Fatalf("dry-run context reported live")
	}
}

// TestCollectorRecordsOrderedSteps proves the collector records steps in order,
// lifts duration_ms onto the step, and redacts attrs (AC-20).
func TestCollectorRecordsOrderedSteps(t *testing.T) {
	r := NewRedactor()
	const secret = "shhh"
	RegisterSecret(r, secret)
	c := NewTraceCollector(r)

	c.Record("n1", "action", "", map[string]any{FldDurationMs: int64(7), "token": "x", "note": secret})
	c.Record("n2", "response", "DONE", nil)

	rec := c.Result(context.Background(), true)
	if !rec.DryRun {
		t.Fatalf("record not marked dry-run")
	}
	if len(rec.Steps) != 2 {
		t.Fatalf("steps = %d; want 2", len(rec.Steps))
	}
	if rec.Steps[0].NodeID != "n1" || rec.Steps[1].NodeID != "n2" {
		t.Fatalf("steps out of order: %+v", rec.Steps)
	}
	if rec.Steps[0].DurationMs != 7 {
		t.Fatalf("duration_ms = %d; want 7", rec.Steps[0].DurationMs)
	}
	if rec.Steps[1].Branch != "DONE" {
		t.Fatalf("branch = %q; want DONE", rec.Steps[1].Branch)
	}

	// Serialize the record and assert no secret leaks into the dry-run body.
	js, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(js), secret) {
		t.Fatalf("secret leaked in dry-run record: %s", js)
	}
	if !strings.Contains(string(js), redactMask) {
		t.Fatalf("masked token not present: %s", js)
	}
}

// TestCollectorFromContext proves WithCollector/CollectorFrom round-trip.
func TestCollectorFromContext(t *testing.T) {
	if _, ok := CollectorFrom(context.Background()); ok {
		t.Fatalf("live context reported a collector")
	}
	c := NewTraceCollector(nil)
	ctx := WithCollector(context.Background(), c)
	got, ok := CollectorFrom(ctx)
	if !ok || got == nil {
		t.Fatalf("collector not found on context")
	}
}
