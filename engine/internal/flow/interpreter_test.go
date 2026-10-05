package flow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"nzr-rules-engine/internal/connect"
	"nzr-rules-engine/internal/decision"
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
			ID:   "seq",
			Type: TypeSequence,
			Spec: raw(t, SequenceSpec{}),
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

func TestInterpreter_ConnErrorClassCrossesSeam(t *testing.T) {
	// A *connect.ConnError returned by a driver must keep its class across the
	// flow seam so httpapi maps Upstream->502, NotFound->404, Timeout->504 rather
	// than collapsing everything to 500/internal.
	cases := []struct {
		name  string
		class connect.ErrClass
		want  error
	}{
		{"upstream", connect.Upstream, ErrUpstream},
		{"not_found", connect.NotFound, ErrNotFound},
		{"timeout", connect.Timeout, ErrTimeout},
		{"validation", connect.Validation, ErrValidation},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ce := connect.NewConnError(tc.class, "orders-pg", "query", "driver failed", nil)
			reg := &fakeRegistry{client: &fakeClient{err: ce}}
			tree := &Node{
				ID:   "root",
				Type: TypeTrigger,
				Spec: raw(t, TriggerSpec{Method: "GET", Path: "/x"}),
				Children: []Node{{
					ID:   "load",
					Type: TypeAction,
					Spec: raw(t, ActionSpec{ConnRef: ConnRef{Connection: "orders-pg"}, Operation: connect.Operation{Kind: "query"}, SaveAs: "x"}),
				}},
			}
			err := New().Run(context.Background(), tree, Version{}, NewCtx("r", "t", "e", nil), Deps{Conns: reg})
			if err == nil {
				t.Fatal("expected the driver error to abort the walk")
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("class not preserved across seam: errors.Is(err, %v)==false; err=%v", tc.want, err)
			}
			if tc.want != ErrInternal && errors.Is(err, ErrInternal) {
				t.Fatalf("class collapsed to internal: %v", err)
			}
		})
	}
}

func TestInterpreter_DecisionErrorClassCrossesSeam(t *testing.T) {
	// A *decision.DecisionError returned by the evaluator must keep its class
	// across the flow seam (NotFound JDM -> 404, malformed JDM/input -> 400).
	cases := []struct {
		name string
		err  error
		want error
	}{
		{"not_found", &decision.DecisionError{Class: decision.NotFound, JDMID: "order"}, ErrNotFound},
		{"validation", &decision.DecisionError{Class: decision.Validation, JDMID: "order"}, ErrValidation},
		{"timeout", &decision.DecisionError{Class: decision.Timeout, JDMID: "order"}, ErrTimeout},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg := &fakeRegistry{client: &fakeClient{result: map[string]any{"amount": 1500, "status": "paid"}}}
			eval := &fakeEvaluator{err: tc.err}
			err := New().Run(context.Background(), buildTree(t), Version{}, NewCtx("r", "t", "e", map[string]any{}), Deps{Conns: reg, Decide: eval})
			if err == nil {
				t.Fatal("expected the decision error to abort the walk")
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("class not preserved across seam: errors.Is(err, %v)==false; err=%v", tc.want, err)
			}
			if errors.Is(err, ErrInternal) {
				t.Fatalf("class collapsed to internal: %v", err)
			}
		})
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

func TestInterpreter_IdempotencyKeyFromResolved(t *testing.T) {
	// When IdempotencyKeyFrom is set, the resolved value flows into op.IdempotencyKey.
	fc := &fakeClient{result: map[string]any{}}
	reg := &fakeRegistry{client: fc}
	tree := &Node{
		ID:   "root",
		Type: TypeTrigger,
		Spec: raw(t, TriggerSpec{Method: "POST", Path: "/orders"}),
		Children: []Node{{
			ID:   "create",
			Type: TypeAction,
			Spec: raw(t, ActionSpec{
				ConnRef:            ConnRef{Connection: "pg"},
				Operation:          connect.Operation{Kind: "exec"},
				SaveAs:             "res",
				IdempotencyKeyFrom: "requestId",
			}),
		}},
	}
	c := NewCtx("r", "t", "e", map[string]any{"requestId": "idem-abc123"})
	if err := New().Run(context.Background(), tree, Version{}, c, Deps{Conns: reg}); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if len(fc.ops) != 1 {
		t.Fatalf("expected 1 op, got %d", len(fc.ops))
	}
	if fc.ops[0].IdempotencyKey != "idem-abc123" {
		t.Fatalf("IdempotencyKey = %q, want %q", fc.ops[0].IdempotencyKey, "idem-abc123")
	}
}

func TestInterpreter_IdempotencyKeyFromMissing(t *testing.T) {
	// When IdempotencyKeyFrom path is missing, IdempotencyKey stays empty (no error).
	fc := &fakeClient{result: map[string]any{}}
	reg := &fakeRegistry{client: fc}
	tree := &Node{
		ID:   "root",
		Type: TypeTrigger,
		Spec: raw(t, TriggerSpec{Method: "POST", Path: "/orders"}),
		Children: []Node{{
			ID:   "create",
			Type: TypeAction,
			Spec: raw(t, ActionSpec{
				ConnRef:            ConnRef{Connection: "pg"},
				Operation:          connect.Operation{Kind: "exec"},
				IdempotencyKeyFrom: "missingKey",
			}),
		}},
	}
	c := NewCtx("r", "t", "e", map[string]any{"other": "value"})
	if err := New().Run(context.Background(), tree, Version{}, c, Deps{Conns: reg}); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if len(fc.ops) != 1 || fc.ops[0].IdempotencyKey != "" {
		t.Fatalf("missing path should leave IdempotencyKey empty, got %q", fc.ops[0].IdempotencyKey)
	}
}

func TestInterpreter_IdempotencyKeyFromNonString(t *testing.T) {
	// When IdempotencyKeyFrom resolves to a non-string, IdempotencyKey stays empty.
	fc := &fakeClient{result: map[string]any{}}
	reg := &fakeRegistry{client: fc}
	tree := &Node{
		ID:   "root",
		Type: TypeTrigger,
		Spec: raw(t, TriggerSpec{Method: "POST", Path: "/orders"}),
		Children: []Node{{
			ID:   "create",
			Type: TypeAction,
			Spec: raw(t, ActionSpec{
				ConnRef:            ConnRef{Connection: "pg"},
				Operation:          connect.Operation{Kind: "exec"},
				IdempotencyKeyFrom: "numericValue",
			}),
		}},
	}
	c := NewCtx("r", "t", "e", map[string]any{"numericValue": 12345})
	if err := New().Run(context.Background(), tree, Version{}, c, Deps{Conns: reg}); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if len(fc.ops) != 1 || fc.ops[0].IdempotencyKey != "" {
		t.Fatalf("non-string should leave IdempotencyKey empty, got %q", fc.ops[0].IdempotencyKey)
	}
}

func TestInterpreter_ConditionBranchField(t *testing.T) {
	// When BranchField is set, the condition reads from that specific field
	// instead of the generic "branch" or "result" convention.
	reg := &fakeRegistry{client: &fakeClient{result: map[string]any{"amount": 1500}}}

	// Decision output has a custom field "shipping" with the branch value
	eval := &fakeEvaluator{out: map[string]any{"shipping": "expedite"}}

	tree := &Node{
		ID:   "root",
		Type: TypeTrigger,
		Spec: raw(t, TriggerSpec{Method: "GET", Path: "/x"}),
		Children: []Node{{
			ID:   "load",
			Type: TypeAction,
			Spec: raw(t, ActionSpec{ConnRef: ConnRef{Connection: "pg"}, Operation: connect.Operation{Kind: "query"}, SaveAs: "data"}),
			Children: []Node{{
				ID:   "cond",
				Type: TypeCondition,
				Spec: raw(t, ConditionSpec{
					JDMID:       "order",
					Input:       []string{"data.amount"},
					TrueKey:     "expedite",
					FalseKey:    "standard",
					BranchField: "shipping", // explicitly read from "shipping" field
				}),
				Children: []Node{
					{
						ID:   "expedite",
						Type: TypeSet,
						Spec: raw(t, SetSpec{TargetPath: "result", Value: "expedited"}),
						Children: []Node{{
							ID: "resp-exp", Type: TypeResponse, Spec: raw(t, ResponseSpec{Status: 200}),
						}},
					},
					{
						ID:   "standard",
						Type: TypeSet,
						Spec: raw(t, SetSpec{TargetPath: "result", Value: "standard"}),
						Children: []Node{{
							ID: "resp-std", Type: TypeResponse, Spec: raw(t, ResponseSpec{Status: 200}),
						}},
					},
				},
			}},
		}},
	}

	c := NewCtx("r", "t", "e", nil)
	if err := New().Run(context.Background(), tree, Version{}, c, Deps{Conns: reg, Decide: eval}); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if c.Response["result"] != "expedited" {
		t.Errorf("expected expedited branch taken, got result=%v", c.Response["result"])
	}
}

func TestInterpreter_ConditionBranchFieldMismatch(t *testing.T) {
	// When BranchField is set but the value doesn't match a branch key,
	// it falls through to falseKey.
	reg := &fakeRegistry{client: &fakeClient{result: map[string]any{}}}
	eval := &fakeEvaluator{out: map[string]any{"shipping": "unknown"}} // not "expedite" or "standard"

	tree := &Node{
		ID:   "root",
		Type: TypeTrigger,
		Spec: raw(t, TriggerSpec{Method: "GET", Path: "/x"}),
		Children: []Node{{
			ID:   "cond",
			Type: TypeCondition,
			Spec: raw(t, ConditionSpec{
				JDMID:       "order",
				Input:       []string{},
				TrueKey:     "expedite",
				FalseKey:    "standard",
				BranchField: "shipping",
			}),
			Children: []Node{
				{ID: "expedite", Type: TypeSet, Spec: raw(t, SetSpec{TargetPath: "result", Value: "expedited"}), Children: []Node{{ID: "r1", Type: TypeResponse, Spec: raw(t, ResponseSpec{Status: 200})}}},
				{ID: "standard", Type: TypeSet, Spec: raw(t, SetSpec{TargetPath: "result", Value: "standard"}), Children: []Node{{ID: "r2", Type: TypeResponse, Spec: raw(t, ResponseSpec{Status: 200})}}},
			},
		}},
	}

	c := NewCtx("r", "t", "e", nil)
	if err := New().Run(context.Background(), tree, Version{}, c, Deps{Conns: reg, Decide: eval}); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if c.Response["result"] != "standard" {
		t.Errorf("expected standard branch (mismatch fallback), got result=%v", c.Response["result"])
	}
}
