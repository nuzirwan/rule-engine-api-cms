package flow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"nzr-rules-engine/internal/connect"
)

// --- fakes -----------------------------------------------------------------

// fakeRegistry returns a fixed fakeClient for any key, recording the keys asked.
type fakeRegistry struct {
	client   connect.Client
	err      error
	askedFor []string
}

func (r *fakeRegistry) Client(ctx context.Context, key string) (connect.Client, error) {
	r.askedFor = append(r.askedFor, key)
	if r.err != nil {
		return nil, r.err
	}
	return r.client, nil
}
func (r *fakeRegistry) Reload(ctx context.Context, defs []connect.ConnectionDef) error { return nil }
func (r *fakeRegistry) HealthCheck(ctx context.Context) error                          { return nil }

// fakeClient returns a canned result and records the ops it executed.
type fakeClient struct {
	result any
	err    error
	ops    []connect.Operation
}

func (c *fakeClient) Execute(ctx context.Context, op connect.Operation) (any, error) {
	c.ops = append(c.ops, op)
	if c.err != nil {
		return nil, c.err
	}
	return c.result, nil
}
func (c *fakeClient) Close() error { return nil }

// fakeEvaluator returns a canned decision output and records inputs seen.
type fakeEvaluator struct {
	out  map[string]any
	err  error
	seen []map[string]any
}

func (e *fakeEvaluator) Evaluate(ctx context.Context, jdmID string, input map[string]any) (map[string]any, error) {
	e.seen = append(e.seen, input)
	if e.err != nil {
		return nil, e.err
	}
	return e.out, nil
}

// --- tree builder ----------------------------------------------------------

func raw(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}
	return b
}

// buildTree returns Trigger -> Action -> Condition{expedite|standard}, each
// branch Set(shipping) -> Response. The condition children are keyed by node ID.
func buildTree(t *testing.T) *Node {
	t.Helper()
	expedite := Node{
		ID:   "expedite",
		Type: TypeSet,
		Spec: raw(t, SetSpec{TargetPath: "shipping", Value: "expedited"}),
		Children: []Node{{
			ID:   "resp-exp",
			Type: TypeResponse,
			Spec: raw(t, ResponseSpec{Status: 200}),
		}},
	}
	standard := Node{
		ID:   "standard",
		Type: TypeSet,
		Spec: raw(t, SetSpec{TargetPath: "shipping", Value: "standard"}),
		Children: []Node{{
			ID:   "resp-std",
			Type: TypeResponse,
			Spec: raw(t, ResponseSpec{Status: 200}),
		}},
	}
	cond := Node{
		ID:       "cond",
		Type:     TypeCondition,
		Spec:     raw(t, ConditionSpec{JDMID: "order", Input: []string{"order.amount", "order.status"}, TrueKey: "expedite", FalseKey: "standard"}),
		Children: []Node{expedite, standard},
	}
	action := Node{
		ID:       "load",
		Type:     TypeAction,
		Spec:     raw(t, ActionSpec{ConnRef: ConnRef{Connection: "orders-pg"}, Operation: connect.Operation{Kind: "query"}, SaveAs: "order"}),
		Children: []Node{cond},
	}
	trigger := &Node{
		ID:       "root",
		Type:     TypeTrigger,
		Spec:     raw(t, TriggerSpec{Method: "GET", Path: "/orders/{id}"}),
		Children: []Node{action},
	}
	return trigger
}

func TestInterpreter_BranchesByDecision(t *testing.T) {
	cases := []struct {
		name         string
		decision     map[string]any
		wantShipping string
		wantBranch   string
		wantRespID   string
	}{
		{
			name:         "true branch expedites",
			decision:     map[string]any{"branch": "expedite"},
			wantShipping: "expedited",
			wantBranch:   "expedite",
		},
		{
			name:         "false branch is standard",
			decision:     map[string]any{"branch": "standard"},
			wantShipping: "standard",
			wantBranch:   "standard",
		},
		{
			name:         "truthy result picks trueKey",
			decision:     map[string]any{"result": true},
			wantShipping: "expedited",
			wantBranch:   "expedite",
		},
		{
			name:         "falsey result picks falseKey",
			decision:     map[string]any{"result": false},
			wantShipping: "standard",
			wantBranch:   "standard",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg := &fakeRegistry{client: &fakeClient{result: map[string]any{"amount": 1500, "status": "paid"}}}
			eval := &fakeEvaluator{out: tc.decision}
			dep := Deps{Conns: reg, Decide: eval}

			c := NewCtx("req", "trace", "test", map[string]any{})
			ip := New()
			if err := ip.Run(context.Background(), buildTree(t), Version{FlowID: "f", Version: 1}, c, dep); err != nil {
				t.Fatalf("Run error: %v", err)
			}

			if got := c.Response["shipping"]; got != tc.wantShipping {
				t.Fatalf("shipping = %v, want %v", got, tc.wantShipping)
			}
			// The action result was saved for the condition to project from.
			if _, ok := c.Data["order"]; !ok {
				t.Fatal("action result not saved under Data[order]")
			}
			// The condition received the projected inputs under their leaf keys.
			if len(eval.seen) != 1 {
				t.Fatalf("evaluator called %d times, want 1", len(eval.seen))
			}
			if eval.seen[0]["amount"] != 1500 || eval.seen[0]["status"] != "paid" {
				t.Fatalf("projected inputs = %#v", eval.seen[0])
			}
		})
	}
}

func TestInterpreter_DeferredNodeIsValidation(t *testing.T) {
	tree := &Node{
		ID:   "root",
		Type: TypeTrigger,
		Spec: raw(t, TriggerSpec{Method: "GET", Path: "/x"}),
		Children: []Node{{
			ID:   "fan",
			Type: TypeParallel,
			Spec: raw(t, ParallelSpec{}),
		}},
	}
	c := NewCtx("req", "trace", "test", nil)
	err := New().Run(context.Background(), tree, Version{}, c, Deps{})
	if err == nil {
		t.Fatal("expected a validation error for a deferred node, got nil")
	}
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("deferred-node error is not Validation-class: %v", err)
	}
}

func TestInterpreter_UnknownNodeTypeIsValidation(t *testing.T) {
	tree := &Node{
		ID:   "root",
		Type: NodeType("bogus"),
		Spec: json.RawMessage(`{}`),
	}
	err := New().Run(context.Background(), tree, Version{}, NewCtx("r", "t", "e", nil), Deps{})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("unknown-type error is not Validation-class: %v", err)
	}
}

func TestInterpreter_ActionErrorFailsByDefault(t *testing.T) {
	reg := &fakeRegistry{client: &fakeClient{err: errors.New("boom")}}
	tree := &Node{
		ID:   "root",
		Type: TypeTrigger,
		Spec: raw(t, TriggerSpec{Method: "GET", Path: "/x"}),
		Children: []Node{{
			ID:   "load",
			Type: TypeAction,
			Spec: raw(t, ActionSpec{ConnRef: ConnRef{Connection: "pg"}, Operation: connect.Operation{Kind: "query"}, SaveAs: "x"}),
		}},
	}
	err := New().Run(context.Background(), tree, Version{}, NewCtx("r", "t", "e", nil), Deps{Conns: reg})
	if err == nil {
		t.Fatal("expected action failure to abort the walk")
	}
	// An unclassified cause is classified at the seam (internal by default).
	var fe *FlowError
	if !errors.As(err, &fe) {
		t.Fatalf("error not classified as *FlowError: %v", err)
	}
}

func TestInterpreter_ActionOnErrorContinue(t *testing.T) {
	// An action whose Execute fails but is marked OnError:"continue" records the
	// failure and does NOT abort the walk, so Run returns nil.
	reg := &fakeRegistry{client: &fakeClient{err: errors.New("boom")}}
	tree := &Node{
		ID:   "root",
		Type: TypeTrigger,
		Spec: raw(t, TriggerSpec{Method: "GET", Path: "/x"}),
		Children: []Node{{
			ID:   "load",
			Type: TypeAction,
			Spec: raw(t, ActionSpec{
				ConnRef:   ConnRef{Connection: "pg"},
				Operation: connect.Operation{Kind: "query"},
				SaveAs:    "x",
				OnError:   OnErrorContinue,
			}),
		}},
	}
	if err := New().Run(context.Background(), tree, Version{}, NewCtx("r", "t", "e", nil), Deps{Conns: reg}); err != nil {
		t.Fatalf("OnError continue should swallow the failure, got: %v", err)
	}
}

func TestInterpreter_TimeoutOverrideApplied(t *testing.T) {
	fc := &fakeClient{result: map[string]any{}}
	reg := &fakeRegistry{client: fc}
	ms := 250
	tree := &Node{
		ID:   "root",
		Type: TypeTrigger,
		Spec: raw(t, TriggerSpec{Method: "GET", Path: "/x"}),
		Children: []Node{{
			ID:   "load",
			Type: TypeAction,
			Spec: raw(t, ActionSpec{
				ConnRef:    ConnRef{Connection: "pg"},
				Operation:  connect.Operation{Kind: "query"},
				SaveAs:     "x",
				Resilience: &ResilienceOverride{TimeoutMS: &ms},
			}),
		}},
	}
	if err := New().Run(context.Background(), tree, Version{}, NewCtx("r", "t", "e", nil), Deps{Conns: reg}); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if len(fc.ops) != 1 || fc.ops[0].Override == nil {
		t.Fatalf("timeout override not forwarded to the operation: %#v", fc.ops)
	}
	if fc.ops[0].Override.Timeout.Milliseconds() != int64(ms) {
		t.Fatalf("override timeout = %v, want %dms", fc.ops[0].Override.Timeout, ms)
	}
}
