package flow

import (
	"context"
	"encoding/json"
	"testing"

	"nzr-rules-engine/internal/connect"
	"nzr-rules-engine/internal/observ"
)

// spyClient is a connect.Client that records every Execute call. A test can
// assert whether a write op reached the client (it must NOT under dry-run).
type spyClient struct {
	t         *testing.T
	calls     []connect.Operation
	failWrite bool // if true, a write op reaching Execute fails the test
	result    any
}

func (c *spyClient) Execute(ctx context.Context, op connect.Operation) (any, error) {
	c.calls = append(c.calls, op)
	if c.failWrite && isWriteOp(op.Kind) {
		c.t.Fatalf("write op %q reached Execute under dry-run (must be suppressed)", op.Kind)
	}
	return c.result, nil
}

func (c *spyClient) Close() error { return nil }

// spyRegistry hands out the same spyClient for every key.
type spyRegistry struct{ client *spyClient }

func (r *spyRegistry) Client(ctx context.Context, key string) (connect.Client, error) {
	return r.client, nil
}
func (r *spyRegistry) Reload(ctx context.Context, defs []connect.ConnectionDef) error { return nil }
func (r *spyRegistry) HealthCheck(ctx context.Context) error                          { return nil }
func (r *spyRegistry) Close() error                                                   { return nil }
func (r *spyRegistry) SecretProvider() connect.SecretProvider                         { return connect.NewEnvSecretProvider() }

// actionNode builds a single-action flow: trigger -> action(kind) -> response,
// so the walk runs the action then terminates.
func actionNode(t *testing.T, kind string) Node {
	t.Helper()
	spec, err := json.Marshal(map[string]any{
		"connection": "c1",
		"operation":  map[string]any{"kind": kind, "payload": map[string]any{}},
		"saveAs":     "out",
	})
	if err != nil {
		t.Fatalf("marshal action spec: %v", err)
	}
	resp := Node{ID: "resp", Type: TypeResponse, Spec: json.RawMessage(`{"status":200}`)}
	action := Node{ID: "act", Type: TypeAction, Spec: spec, Children: []Node{resp}}
	return Node{ID: "trig", Type: TypeTrigger,
		Spec: json.RawMessage(`{"method":"POST","path":"/x","input":{}}`), Children: []Node{action}}
}

// TestActionDryRunSuppressesWrites proves the AC-14 fix: a write op under a
// dry-run context performs NO Execute call and records wrote:"suppressed" into
// the attached collector; a read op is NOT suppressed; and without dry-run the
// real Execute runs.
func TestActionDryRunSuppressesWrites(t *testing.T) {
	writeKinds := []string{"exec", "set", "del", "http"}
	readKinds := []string{"query", "get"}

	for _, kind := range writeKinds {
		t.Run("dryrun suppresses write "+kind, func(t *testing.T) {
			spy := &spyClient{t: t, failWrite: true}
			collector := observ.NewTraceCollector(nil)
			ctx := observ.WithCollector(observ.WithDryRun(context.Background()), collector)

			ip := New()
			tree := actionNode(t, kind)
			c := NewCtx("", "", "", nil)
			if err := ip.Run(ctx, &tree, Version{}, c, Deps{Conns: &spyRegistry{client: spy}}); err != nil {
				t.Fatalf("run: %v", err)
			}
			// The write op never reached Execute.
			for _, call := range spy.calls {
				if isWriteOp(call.Kind) {
					t.Fatalf("write op %q executed under dry-run", call.Kind)
				}
			}
			// A suppressed-write record is in the trace.
			rec := collector.Result(ctx, true)
			found := false
			for _, step := range rec.Steps {
				if step.NodeID == "act" && step.Attrs["wrote"] == "suppressed" {
					found = true
				}
			}
			if !found {
				t.Fatalf("no suppressed-write trace record for action %q: %+v", kind, rec.Steps)
			}
		})
	}

	for _, kind := range readKinds {
		t.Run("dryrun does NOT suppress read "+kind, func(t *testing.T) {
			spy := &spyClient{t: t, result: map[string]any{"ok": true}}
			ctx := observ.WithDryRun(context.Background())

			ip := New()
			tree := actionNode(t, kind)
			c := NewCtx("", "", "", nil)
			if err := ip.Run(ctx, &tree, Version{}, c, Deps{Conns: &spyRegistry{client: spy}}); err != nil {
				t.Fatalf("run: %v", err)
			}
			if len(spy.calls) != 1 || spy.calls[0].Kind != kind {
				t.Fatalf("read op %q should execute under dry-run; calls=%+v", kind, spy.calls)
			}
		})
	}

	t.Run("no dry-run runs the real write", func(t *testing.T) {
		spy := &spyClient{t: t, result: map[string]any{"rows": 1}}
		ip := New()
		tree := actionNode(t, "exec")
		c := NewCtx("", "", "", nil)
		if err := ip.Run(context.Background(), &tree, Version{}, c, Deps{Conns: &spyRegistry{client: spy}}); err != nil {
			t.Fatalf("run: %v", err)
		}
		if len(spy.calls) != 1 || spy.calls[0].Kind != "exec" {
			t.Fatalf("write op should execute without dry-run; calls=%+v", spy.calls)
		}
	})
}
