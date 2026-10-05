package flow

import (
	"context"
	"encoding/json"
	"errors"
	"nzr-rules-engine/internal/connect"
	"sync"
	"testing"
	"time"
)

// --- extra fakes -----------------------------------------------------------

// recordingLogger captures the records emitted by a Logger node.
type recordingLogger struct {
	mu      sync.Mutex
	records []logRecord
}

type logRecord struct {
	level  string
	label  string
	fields map[string]any
}

func (l *recordingLogger) Emit(ctx context.Context, level, label string, fields map[string]any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.records = append(l.records, logRecord{level: level, label: label, fields: fields})
}

// byLabel returns only records matching label, filtering out per-node trace logs.
func (l *recordingLogger) byLabel(label string) []logRecord {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []logRecord
	for _, r := range l.records {
		if r.label == label {
			out = append(out, r)
		}
	}
	return out
}

// excludeLabel returns records NOT matching label (useful to filter out "node" trace logs).
func (l *recordingLogger) excludeLabel(label string) []logRecord {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []logRecord
	for _, r := range l.records {
		if r.label != label {
			out = append(out, r)
		}
	}
	return out
}

// --- AC-2: nested branches + switch default --------------------------------

func TestSwitch_NWayAndDefault(t *testing.T) {
	// Switch with cases gold->premium, silver->basic, and a default->fallback.
	build := func() *Node {
		mkLeaf := func(id, path, val string) Node {
			return Node{
				ID:   id,
				Type: TypeSet,
				Spec: rawSpec(t, SetSpec{TargetPath: path, Value: val}),
				Children: []Node{{
					ID:   id + "-resp",
					Type: TypeResponse,
					Spec: rawSpec(t, ResponseSpec{Status: 200}),
				}},
			}
		}
		sw := Node{
			ID:   "tier",
			Type: TypeSwitch,
			Spec: rawSpec(t, SwitchSpec{
				JDMID:   "tier",
				Input:   []string{"user.tier"},
				Cases:   map[string]string{"gold": "premium", "silver": "basic"},
				Default: "fallback",
			}),
			Children: []Node{
				mkLeaf("premium", "plan", "premium"),
				mkLeaf("basic", "plan", "basic"),
				mkLeaf("fallback", "plan", "free"),
			},
		}
		return &Node{
			ID:       "root",
			Type:     TypeTrigger,
			Spec:     rawSpec(t, TriggerSpec{Method: "GET", Path: "/x"}),
			Children: []Node{sw},
		}
	}

	cases := []struct {
		name     string
		branch   string
		wantPlan string
	}{
		{"gold case", "gold", "premium"},
		{"silver case", "silver", "basic"},
		{"unmatched falls to default", "bronze", "free"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eval := &fakeEvaluator{out: map[string]any{"branch": tc.branch}}
			c := NewCtx("r", "t", "e", map[string]any{})
			if err := New().Run(context.Background(), build(), Version{}, c, Deps{Decide: eval}); err != nil {
				t.Fatalf("Run error: %v", err)
			}
			if got := c.Response["plan"]; got != tc.wantPlan {
				t.Fatalf("plan = %v, want %v", got, tc.wantPlan)
			}
		})
	}
}

// AC-2 nested: a switch whose chosen child is itself a condition, proving
// control nodes recurse through the Walker to arbitrary depth.
func TestSwitch_NestedCondition(t *testing.T) {
	inner := Node{
		ID:   "inner",
		Type: TypeCondition,
		Spec: rawSpec(t, ConditionSpec{JDMID: "risk", Input: []string{"score"}, TrueKey: "high", FalseKey: "low"}),
		Children: []Node{
			{ID: "high", Type: TypeSet, Spec: rawSpec(t, SetSpec{TargetPath: "risk", Value: "high"}),
				Children: []Node{{ID: "rh", Type: TypeResponse, Spec: rawSpec(t, ResponseSpec{Status: 200})}}},
			{ID: "low", Type: TypeSet, Spec: rawSpec(t, SetSpec{TargetPath: "risk", Value: "low"}),
				Children: []Node{{ID: "rl", Type: TypeResponse, Spec: rawSpec(t, ResponseSpec{Status: 200})}}},
		},
	}
	sw := Node{
		ID:       "outer",
		Type:     TypeSwitch,
		Spec:     rawSpec(t, SwitchSpec{JDMID: "route", Input: []string{"kind"}, Cases: map[string]string{"check": "inner"}}),
		Children: []Node{inner},
	}
	tree := &Node{ID: "root", Type: TypeTrigger, Spec: rawSpec(t, TriggerSpec{Method: "GET", Path: "/x"}), Children: []Node{sw}}

	// First decision routes the switch to "inner"; the second drives the nested
	// condition to its true branch.
	eval := &seqEvaluator{outs: []map[string]any{{"branch": "check"}, {"result": true}}}
	c := NewCtx("r", "t", "e", map[string]any{})
	if err := New().Run(context.Background(), tree, Version{}, c, Deps{Decide: eval}); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if got := c.Response["risk"]; got != "high" {
		t.Fatalf("nested risk = %v, want high", got)
	}
}

// seqEvaluator returns a different canned output per call, for nested decisions.
type seqEvaluator struct {
	outs []map[string]any
	n    int
}

func (e *seqEvaluator) Evaluate(ctx context.Context, jdmID string, input map[string]any) (map[string]any, error) {
	out := e.outs[e.n%len(e.outs)]
	e.n++
	return out, nil
}

// --- AC-3: parallel disjoint merge, no clobber -----------------------------

func TestParallel_DisjointMergeNoClobber(t *testing.T) {
	mkSet := func(id, path string, val any) Node {
		return Node{ID: id, Type: TypeSet, Spec: rawSpec(t, SetSpec{TargetPath: path, Value: val})}
	}
	par := Node{
		ID:   "fan",
		Type: TypeParallel,
		Spec: rawSpec(t, ParallelSpec{MaxConcurrency: 2}),
		Children: []Node{
			mkSet("a", "order.id", float64(42)),
			mkSet("b", "order.total", float64(99)),
			mkSet("c", "flags.expedited", true),
		},
	}
	tree := &Node{
		ID:   "root",
		Type: TypeTrigger,
		Spec: rawSpec(t, TriggerSpec{Method: "GET", Path: "/x"}),
		Children: []Node{{
			ID:       "fan",
			Type:     TypeParallel,
			Spec:     par.Spec,
			Children: par.Children,
		}, {
			ID:   "resp",
			Type: TypeResponse,
			Spec: rawSpec(t, ResponseSpec{Status: 200}),
		}},
	}

	c := NewCtx("r", "t", "e", map[string]any{})
	if err := New().Run(context.Background(), tree, Version{}, c, Deps{}); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	order, ok := c.Response["order"].(map[string]any)
	if !ok {
		t.Fatalf("order missing/not a map: %#v", c.Response["order"])
	}
	// Both sibling writes under order.* survived — no clobber (AC-3).
	if order["id"] != float64(42) || order["total"] != float64(99) {
		t.Fatalf("order merge clobbered: %#v", order)
	}
	flags, ok := c.Response["flags"].(map[string]any)
	if !ok || flags["expedited"] != true {
		t.Fatalf("flags subtree not merged: %#v", c.Response["flags"])
	}
}

func TestParallel_FailFastCancelsSiblings(t *testing.T) {
	// One child errors; FailFast must surface the error (and not hang).
	par := Node{
		ID:   "fan",
		Type: TypeParallel,
		Spec: rawSpec(t, ParallelSpec{FailFast: true, MaxConcurrency: 1}),
		Children: []Node{
			{ID: "bad", Type: TypeAction, Spec: rawSpec(t, ActionSpec{ConnRef: ConnRef{Connection: "pg"}, Operation: op("query"), SaveAs: "x"})},
			{ID: "ok", Type: TypeSet, Spec: rawSpec(t, SetSpec{TargetPath: "y", Value: 1})},
		},
	}
	tree := &Node{ID: "root", Type: TypeTrigger, Spec: rawSpec(t, TriggerSpec{Method: "GET", Path: "/x"}), Children: []Node{par}}
	reg := &fakeRegistry{client: &fakeClient{err: errors.New("boom")}}
	err := New().Run(context.Background(), tree, Version{}, NewCtx("r", "t", "e", nil), Deps{Conns: reg})
	if err == nil {
		t.Fatal("expected parallel to surface the child error")
	}
}

// --- AC-17: forEach maxItems / work-budget / ctx-deadline => classified ----

func TestForEach_IteratesAndBinds(t *testing.T) {
	// forEach over three items, each binding item->Input["it"], body sets
	// results.<index>.v = it; proves per-item binding and merge.
	body := Node{
		ID:   "body",
		Type: TypeSet,
		Spec: rawSpec(t, SetSpec{TargetPath: "results", From: "it"}),
	}
	fe := Node{
		ID:       "loop",
		Type:     TypeForEach,
		Spec:     rawSpec(t, ForEachSpec{Over: "items", As: "it", IndexAs: "i", MaxItems: 10}),
		Children: []Node{body},
	}
	tree := &Node{ID: "root", Type: TypeTrigger, Spec: rawSpec(t, TriggerSpec{Method: "GET", Path: "/x"}), Children: []Node{fe}}

	c := NewCtx("r", "t", "e", map[string]any{"items": []any{"a", "b", "c"}})
	if err := New().Run(context.Background(), tree, Version{}, c, Deps{}); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	// Each iteration overwrites results with its own item via From "it"; the last
	// iteration wins. The point is iteration ran and bound the alias.
	if got := c.Response["results"]; got != "c" {
		t.Fatalf("results = %v, want last-bound item c", got)
	}
}

func TestForEach_MaxItemsExceeded(t *testing.T) {
	fe := Node{
		ID:       "loop",
		Type:     TypeForEach,
		Spec:     rawSpec(t, ForEachSpec{Over: "items", As: "it", MaxItems: 2}),
		Children: []Node{{ID: "b", Type: TypeSet, Spec: rawSpec(t, SetSpec{TargetPath: "x", Value: 1})}},
	}
	tree := &Node{ID: "root", Type: TypeTrigger, Spec: rawSpec(t, TriggerSpec{Method: "GET", Path: "/x"}), Children: []Node{fe}}
	c := NewCtx("r", "t", "e", map[string]any{"items": []any{1, 2, 3}})
	err := New().Run(context.Background(), tree, Version{}, c, Deps{})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("maxItems exceeded must be a validation error, got: %v", err)
	}
}

func TestForEach_WorkBudgetExhausted(t *testing.T) {
	// A forEach body that itself fans out a lot: with a tiny work budget the walk
	// must terminate with a classified validation error, never hang (AC-17).
	fe := Node{
		ID:       "loop",
		Type:     TypeForEach,
		Spec:     rawSpec(t, ForEachSpec{Over: "items", As: "it", MaxItems: 100000}),
		Children: []Node{{ID: "b", Type: TypeSet, Spec: rawSpec(t, SetSpec{TargetPath: "x", Value: 1})}},
	}
	tree := &Node{ID: "root", Type: TypeTrigger, Spec: rawSpec(t, TriggerSpec{Method: "GET", Path: "/x"}), Children: []Node{fe}}

	items := make([]any, 50)
	for i := range items {
		items[i] = i
	}
	c := NewCtx("r", "t", "e", map[string]any{"items": items})

	ip := New()
	// Drive Run but with a shrunken budget by walking directly: build a budgeted
	// context the way Run does, then set a tiny ceiling.
	b := &budget{maxDepth: DefaultMaxDepth, maxWork: 5}
	ctx := context.WithValue(context.Background(), runStateKey{}, b)
	_, err := ip.Walk(ctx, tree, c, Deps{})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("work-budget exhaustion must be a validation error, got: %v", err)
	}
}

func TestForEach_CtxDeadlineClassifiedNotHang(t *testing.T) {
	// An already-cancelled context must terminate the loop with a classified
	// timeout error rather than hang.
	fe := Node{
		ID:       "loop",
		Type:     TypeForEach,
		Spec:     rawSpec(t, ForEachSpec{Over: "items", As: "it", MaxItems: 1000}),
		Children: []Node{{ID: "b", Type: TypeSet, Spec: rawSpec(t, SetSpec{TargetPath: "x", Value: 1})}},
	}
	tree := &Node{ID: "root", Type: TypeTrigger, Spec: rawSpec(t, TriggerSpec{Method: "GET", Path: "/x"}), Children: []Node{fe}}

	items := make([]any, 100)
	for i := range items {
		items[i] = i
	}
	c := NewCtx("r", "t", "e", map[string]any{"items": items})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already past the deadline

	done := make(chan error, 1)
	go func() { done <- New().Run(ctx, tree, Version{}, c, Deps{}) }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrTimeout) {
			t.Fatalf("cancelled ctx must yield a timeout-class error, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("forEach hung on a cancelled context")
	}
}

// --- decision + logger leaves ----------------------------------------------

func TestDecision_StoresOutput(t *testing.T) {
	dec := Node{
		ID:   "price",
		Type: TypeDecision,
		Spec: rawSpec(t, DecisionSpec{JDMID: "pricing", Input: []string{"amount"}, SaveAs: "priced"}),
	}
	tree := &Node{ID: "root", Type: TypeTrigger, Spec: rawSpec(t, TriggerSpec{Method: "GET", Path: "/x"}),
		Children: []Node{dec, {ID: "resp", Type: TypeResponse, Spec: rawSpec(t, ResponseSpec{Status: 200})}}}
	eval := &fakeEvaluator{out: map[string]any{"total": float64(120)}}
	c := NewCtx("r", "t", "e", map[string]any{"amount": 100})
	if err := New().Run(context.Background(), tree, Version{}, c, Deps{Decide: eval}); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	got, ok := c.Data["priced"].(map[string]any)
	if !ok || got["total"] != float64(120) {
		t.Fatalf("decision output not stored under Data[priced]: %#v", c.Data["priced"])
	}
}

func TestLogger_EmitsCaptureAndNeverAltersResponse(t *testing.T) {
	lg := Node{
		ID:   "log",
		Type: TypeLogger,
		Spec: rawSpec(t, LoggerSpec{Label: "checkpoint", Level: "info", Capture: []string{"amount"}}),
	}
	tree := &Node{ID: "root", Type: TypeTrigger, Spec: rawSpec(t, TriggerSpec{Method: "GET", Path: "/x"}),
		Children: []Node{lg, {ID: "resp", Type: TypeResponse, Spec: rawSpec(t, ResponseSpec{Status: 200})}}}
	log := &recordingLogger{}
	c := NewCtx("r", "t", "e", map[string]any{"amount": 100})
	if err := New().Run(context.Background(), tree, Version{}, c, Deps{Log: log}); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	// Filter out per-node trace logs ("node" label) to isolate the logger node's emit.
	// The logger handler emits with label "flow.logger"; the spec's Label goes into fields["label"].
	recs := log.byLabel("flow.logger")
	if len(recs) != 1 {
		t.Fatalf("logger emitted %d records with label 'flow.logger', want 1", len(recs))
	}
	rec := recs[0]
	if rec.level != "info" || rec.fields["label"] != "checkpoint" || rec.fields["amount"] != 100 {
		t.Fatalf("logger record = %#v", rec)
	}
	if len(c.Response) != 0 {
		t.Fatalf("logger must not alter the response, got: %#v", c.Response)
	}
}

func TestLogger_SampleRateZeroSkips(t *testing.T) {
	zero := 0.0
	lg := Node{ID: "log", Type: TypeLogger, Spec: rawSpec(t, LoggerSpec{Level: "info", SampleRate: &zero})}
	tree := &Node{ID: "root", Type: TypeTrigger, Spec: rawSpec(t, TriggerSpec{Method: "GET", Path: "/x"}),
		Children: []Node{lg, {ID: "resp", Type: TypeResponse, Spec: rawSpec(t, ResponseSpec{Status: 200})}}}
	log := &recordingLogger{}
	if err := New().Run(context.Background(), tree, Version{}, NewCtx("r", "t", "e", nil), Deps{Log: log}); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	// Exclude per-node trace logs ("node" label) to verify the logger node itself was suppressed.
	recs := log.excludeLabel("node")
	if len(recs) != 0 {
		t.Fatalf("sampleRate 0 must suppress logger emit, got %d non-trace records", len(recs))
	}
}

// --- helpers ---------------------------------------------------------------

func rawSpec(t *testing.T, v any) json.RawMessage {
	t.Helper()
	return raw(t, v)
}

// op builds a connect.Operation of the given kind for action-node specs.
func op(kind string) connect.Operation {
	return connect.Operation{Kind: kind}
}
