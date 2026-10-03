package flow

import (
	"context"
	"testing"

	"nzr-rules-engine/internal/connect"
	"nzr-rules-engine/internal/observ"
)

// buildWriteFlow returns Trigger -> Action(write) -> Response, where the action
// is a write op (Kind "http") saving under "decision". It is the minimal tree
// exercising the dry-run write-suppression path end to end.
func buildWriteFlow(t *testing.T) *Node {
	t.Helper()
	resp := Node{ID: "resp", Type: TypeResponse, Spec: raw(t, ResponseSpec{Status: 200})}
	action := Node{
		ID:   "post",
		Type: TypeAction,
		Spec: raw(t, ActionSpec{
			ConnRef:   ConnRef{Connection: "ship-rest"},
			Operation: connect.Operation{Kind: "http"},
			SaveAs:    "decision",
		}),
		Children: []Node{resp},
	}
	return &Node{
		ID:       "root",
		Type:     TypeTrigger,
		Spec:     raw(t, TriggerSpec{Method: "POST", Path: "/x"}),
		Children: []Node{action},
	}
}

// TestActionHandler_DryRunSuppressesWrite proves that under observ.WithDryRun a
// write op is NOT executed (the fake client's Execute is never called), the
// attached collector records a wrote:"suppressed" step, and a suppressed marker
// is stored under SaveAs — while WITHOUT dry-run the same flow DOES execute the
// write.
func TestActionHandler_DryRunSuppressesWrite(t *testing.T) {
	cases := []struct {
		name          string
		dryRun        bool
		wantExecCalls int
		wantSuppress  bool
	}{
		{name: "dry-run suppresses the write", dryRun: true, wantExecCalls: 0, wantSuppress: true},
		{name: "live run executes the write", dryRun: false, wantExecCalls: 1, wantSuppress: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc := &fakeClient{result: map[string]any{"body": map[string]any{"ok": true}}}
			reg := &fakeRegistry{client: fc}
			dep := Deps{Conns: reg}

			coll := observ.NewTraceCollector(nil)
			ctx := context.Background()
			if tc.dryRun {
				ctx = observ.WithDryRun(ctx)
				ctx = observ.WithCollector(ctx, coll)
			}

			c := NewCtx("req", "trace", "test", map[string]any{})
			if err := New().Run(ctx, buildWriteFlow(t), Version{FlowID: "f", Version: 1}, c, dep); err != nil {
				t.Fatalf("Run error: %v", err)
			}

			if got := len(fc.ops); got != tc.wantExecCalls {
				t.Fatalf("client Execute calls = %d, want %d", got, tc.wantExecCalls)
			}

			if tc.wantSuppress {
				// SaveAs carries the suppressed marker.
				saved, ok := c.Data["decision"].(map[string]any)
				if !ok || saved["wrote"] != "suppressed" {
					t.Fatalf("SaveAs value = %#v, want wrote:suppressed", c.Data["decision"])
				}
				// The collector recorded the suppressed step.
				rec := coll.Result(ctx, true)
				found := false
				for _, s := range rec.Steps {
					if s.NodeID == "post" && s.Attrs["wrote"] == "suppressed" {
						found = true
					}
				}
				if !found {
					t.Fatalf("collector did not record wrote:suppressed for the write node: %#v", rec.Steps)
				}
			}
		})
	}
}
