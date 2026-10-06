package httpapi

import (
	"context"
	"errors"
	"testing"

	"nzr-rules-engine/internal/connect"
	"nzr-rules-engine/internal/flow"
)

// recordingClient is a connect.Client that captures the last Operation it was
// asked to Execute, so a test can assert the bound positional param VALUES and
// their Go TYPES after the templating edge resolved them.
type recordingClient struct {
	last connect.Operation
}

func (c *recordingClient) Execute(ctx context.Context, op connect.Operation) (any, error) {
	c.last = op
	return []map[string]any{}, nil
}
func (c *recordingClient) Close() error { return nil }

// recordingRegistry hands out the same recordingClient for any key so the
// templatingRegistry wraps it.
type recordingRegistry struct{ client *recordingClient }

func (r recordingRegistry) Client(ctx context.Context, key string) (connect.Client, error) {
	return r.client, nil
}
func (r recordingRegistry) Reload(ctx context.Context, defs []connect.ConnectionDef) error {
	return nil
}
func (r recordingRegistry) HealthCheck(ctx context.Context) error { return nil }
func (r recordingRegistry) Close() error                          { return nil }

var _ connect.Registry = recordingRegistry{}

// execViaEdge resolves the given params array through the per-request templating
// edge against ctxInput, returning the Operation the inner client received (so
// the test sees the bound Go values) or the classified error.
func execViaEdge(t *testing.T, ctxInput map[string]any, params []any) (connect.Operation, error) {
	t.Helper()
	rec := &recordingClient{}
	c := flow.NewCtx("", "", "", ctxInput)
	reg := newTemplatingRegistry(recordingRegistry{client: rec}, c)
	client, err := reg.Client(context.Background(), "pg")
	if err != nil {
		t.Fatalf("Client: %v", err)
	}
	_, execErr := client.Execute(context.Background(), connect.Operation{
		Kind: "query",
		Payload: map[string]any{
			"sql":    "SELECT 1 WHERE x = $1",
			"params": params,
		},
	})
	return rec.last, execErr
}

// TestTypedParamIntBinding proves a param declared "as":"int" is converted to an
// int64 (what pgx encodes for an int column), while a bare string param binds as
// a string (text column) — the DECLARED type decides, not the value's shape.
func TestTypedParamIntBinding(t *testing.T) {
	op, err := execViaEdge(t,
		map[string]any{"id": "1500", "msisdn": "628111015450"},
		[]any{
			map[string]any{"value": "{{input.id}}", "as": "int"},
			"{{input.msisdn}}",
		},
	)
	if err != nil {
		t.Fatalf("execViaEdge error: %v", err)
	}
	got := op.Payload["params"].([]any)
	if v, ok := got[0].(int64); !ok || v != 1500 {
		t.Fatalf("param0 = %#v want int64(1500)", got[0])
	}
	// The digit-only msisdn in a text column stays a STRING (no shape coercion).
	if v, ok := got[1].(string); !ok || v != "628111015450" {
		t.Fatalf("param1 = %#v want string \"628111015450\"", got[1])
	}
}

// TestTypedParamConversionError proves a non-integer value declared "as":"int"
// is a classified Validation error BEFORE the query runs (not a panic, not a
// silent coercion). Through the handler this surfaces as a 400.
func TestTypedParamConversionError(t *testing.T) {
	_, err := execViaEdge(t,
		map[string]any{"id": "not-a-number"},
		[]any{map[string]any{"value": "{{input.id}}", "as": "int"}},
	)
	if err == nil {
		t.Fatal("expected a Validation error for a non-integer int param")
	}
	if !errors.Is(err, flow.ErrValidation) {
		t.Fatalf("error = %v; want flow.ErrValidation", err)
	}
}

// TestBareParamBindsAsText proves a plain string param (the FMC order_id /
// msisdn case) resolves against Ctx and binds as its string value unchanged.
func TestBareParamBindsAsText(t *testing.T) {
	op, err := execViaEdge(t,
		map[string]any{"order_id": "ORD-TEST-TRK"},
		[]any{"{{input.order_id}}"},
	)
	if err != nil {
		t.Fatalf("execViaEdge error: %v", err)
	}
	got := op.Payload["params"].([]any)
	if v, ok := got[0].(string); !ok || v != "ORD-TEST-TRK" {
		t.Fatalf("param0 = %#v want string \"ORD-TEST-TRK\"", got[0])
	}
}
