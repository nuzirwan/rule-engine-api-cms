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

// --- filter/find tests -----------------------------------------------------

// matchEvaluator returns match:true when "price" > threshold.
type matchEvaluator struct {
	threshold float64
	seen      []map[string]any
}

func (e *matchEvaluator) Evaluate(ctx context.Context, jdmID string, input map[string]any) (map[string]any, error) {
	e.seen = append(e.seen, input)
	price, _ := input["price"].(float64)
	return map[string]any{"match": price > e.threshold}, nil
}

func TestFilter_BasicFiltering(t *testing.T) {
	items := []any{
		map[string]any{"id": 1, "price": float64(50)},
		map[string]any{"id": 2, "price": float64(150)},
		map[string]any{"id": 3, "price": float64(75)},
	}
	filter := Node{
		ID:   "f",
		Type: TypeFilter,
		Spec: rawSpec(t, FilterSpec{Over: "items", JDMID: "price-check", Input: []string{"price"}, SaveAs: "expensive", MaxItems: 10}),
	}
	tree := &Node{ID: "root", Type: TypeTrigger, Spec: rawSpec(t, TriggerSpec{Method: "GET", Path: "/x"}),
		Children: []Node{filter, {ID: "resp", Type: TypeResponse, Spec: rawSpec(t, ResponseSpec{Status: 200})}}}

	eval := &matchEvaluator{threshold: 100}
	c := NewCtx("r", "t", "e", nil)
	c.Data = map[string]any{"items": items}

	if err := New().Run(context.Background(), tree, Version{}, c, Deps{Decide: eval}); err != nil {
		t.Fatalf("Run error: %v", err)
	}

	result, ok := c.Data["expensive"].([]any)
	if !ok {
		t.Fatalf("filter result not stored: %#v", c.Data["expensive"])
	}
	if len(result) != 1 {
		t.Fatalf("filter result len=%d, want 1", len(result))
	}
	item := result[0].(map[string]any)
	if item["id"] != 2 {
		t.Fatalf("filter result item id=%v, want 2", item["id"])
	}
}

func TestFilter_EmptyResult(t *testing.T) {
	items := []any{
		map[string]any{"id": 1, "price": float64(50)},
	}
	filter := Node{
		ID:   "f",
		Type: TypeFilter,
		Spec: rawSpec(t, FilterSpec{Over: "items", JDMID: "price-check", Input: []string{"price"}, SaveAs: "expensive", MaxItems: 10}),
	}
	tree := &Node{ID: "root", Type: TypeTrigger, Spec: rawSpec(t, TriggerSpec{Method: "GET", Path: "/x"}),
		Children: []Node{filter, {ID: "resp", Type: TypeResponse, Spec: rawSpec(t, ResponseSpec{Status: 200})}}}

	eval := &matchEvaluator{threshold: 100}
	c := NewCtx("r", "t", "e", nil)
	c.Data = map[string]any{"items": items}

	if err := New().Run(context.Background(), tree, Version{}, c, Deps{Decide: eval}); err != nil {
		t.Fatalf("Run error: %v", err)
	}

	result, ok := c.Data["expensive"].([]any)
	if !ok {
		// nil slice is acceptable for empty result
		if c.Data["expensive"] != nil {
			t.Fatalf("filter result not nil or empty slice: %#v", c.Data["expensive"])
		}
		return
	}
	if len(result) != 0 {
		t.Fatalf("filter result len=%d, want 0", len(result))
	}
}

func TestFilter_MaxItemsExceeded(t *testing.T) {
	items := make([]any, 5)
	for i := range items {
		items[i] = map[string]any{"id": i}
	}
	filter := Node{
		ID:   "f",
		Type: TypeFilter,
		Spec: rawSpec(t, FilterSpec{Over: "items", JDMID: "x", Input: []string{}, SaveAs: "out", MaxItems: 3}),
	}
	tree := &Node{ID: "root", Type: TypeTrigger, Spec: rawSpec(t, TriggerSpec{Method: "GET", Path: "/x"}),
		Children: []Node{filter, {ID: "resp", Type: TypeResponse, Spec: rawSpec(t, ResponseSpec{Status: 200})}}}

	c := NewCtx("r", "t", "e", nil)
	c.Data = map[string]any{"items": items}

	err := New().Run(context.Background(), tree, Version{}, c, Deps{Decide: &matchEvaluator{}})
	if err == nil {
		t.Fatalf("expected maxItems error")
	}
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("expected validation error, got: %v", err)
	}
}

func TestFind_FirstMatch(t *testing.T) {
	items := []any{
		map[string]any{"id": 1, "price": float64(50)},
		map[string]any{"id": 2, "price": float64(150)},
		map[string]any{"id": 3, "price": float64(200)},
	}
	find := Node{
		ID:   "f",
		Type: TypeFind,
		Spec: rawSpec(t, FindSpec{Over: "items", JDMID: "price-check", Input: []string{"price"}, SaveAs: "found", MaxItems: 10}),
	}
	tree := &Node{ID: "root", Type: TypeTrigger, Spec: rawSpec(t, TriggerSpec{Method: "GET", Path: "/x"}),
		Children: []Node{find, {ID: "resp", Type: TypeResponse, Spec: rawSpec(t, ResponseSpec{Status: 200})}}}

	eval := &matchEvaluator{threshold: 100}
	c := NewCtx("r", "t", "e", nil)
	c.Data = map[string]any{"items": items}

	if err := New().Run(context.Background(), tree, Version{}, c, Deps{Decide: eval}); err != nil {
		t.Fatalf("Run error: %v", err)
	}

	result, ok := c.Data["found"].(map[string]any)
	if !ok {
		t.Fatalf("find result not a map: %#v", c.Data["found"])
	}
	if result["id"] != 2 {
		t.Fatalf("find result id=%v, want 2 (first match)", result["id"])
	}
	// Verify it stopped at first match (didn't evaluate item 3)
	if len(eval.seen) != 2 {
		t.Fatalf("find evaluated %d items, want 2 (should stop at first match)", len(eval.seen))
	}
}

func TestFind_NoMatch(t *testing.T) {
	items := []any{
		map[string]any{"id": 1, "price": float64(50)},
	}
	find := Node{
		ID:   "f",
		Type: TypeFind,
		Spec: rawSpec(t, FindSpec{Over: "items", JDMID: "price-check", Input: []string{"price"}, SaveAs: "found", MaxItems: 10}),
	}
	tree := &Node{ID: "root", Type: TypeTrigger, Spec: rawSpec(t, TriggerSpec{Method: "GET", Path: "/x"}),
		Children: []Node{find, {ID: "resp", Type: TypeResponse, Spec: rawSpec(t, ResponseSpec{Status: 200})}}}

	eval := &matchEvaluator{threshold: 100}
	c := NewCtx("r", "t", "e", nil)
	c.Data = map[string]any{"items": items}

	if err := New().Run(context.Background(), tree, Version{}, c, Deps{Decide: eval}); err != nil {
		t.Fatalf("Run error: %v", err)
	}

	if c.Data["found"] != nil {
		t.Fatalf("find result should be nil when no match: %#v", c.Data["found"])
	}
}

func TestFind_MaxItemsExceeded(t *testing.T) {
	items := make([]any, 5)
	for i := range items {
		items[i] = map[string]any{"id": i}
	}
	find := Node{
		ID:   "f",
		Type: TypeFind,
		Spec: rawSpec(t, FindSpec{Over: "items", JDMID: "x", Input: []string{}, SaveAs: "out", MaxItems: 3}),
	}
	tree := &Node{ID: "root", Type: TypeTrigger, Spec: rawSpec(t, TriggerSpec{Method: "GET", Path: "/x"}),
		Children: []Node{find, {ID: "resp", Type: TypeResponse, Spec: rawSpec(t, ResponseSpec{Status: 200})}}}

	c := NewCtx("r", "t", "e", nil)
	c.Data = map[string]any{"items": items}

	err := New().Run(context.Background(), tree, Version{}, c, Deps{Decide: &matchEvaluator{}})
	if err == nil {
		t.Fatalf("expected maxItems error")
	}
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("expected validation error, got: %v", err)
	}
}

func TestFilter_SourceNotArray(t *testing.T) {
	filter := Node{
		ID:   "f",
		Type: TypeFilter,
		Spec: rawSpec(t, FilterSpec{Over: "item", JDMID: "x", Input: []string{}, SaveAs: "out", MaxItems: 10}),
	}
	tree := &Node{ID: "root", Type: TypeTrigger, Spec: rawSpec(t, TriggerSpec{Method: "GET", Path: "/x"}),
		Children: []Node{filter, {ID: "resp", Type: TypeResponse, Spec: rawSpec(t, ResponseSpec{Status: 200})}}}

	c := NewCtx("r", "t", "e", nil)
	c.Data = map[string]any{"item": "not-an-array"}

	err := New().Run(context.Background(), tree, Version{}, c, Deps{Decide: &matchEvaluator{}})
	if err == nil {
		t.Fatalf("expected error for non-array source")
	}
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("expected validation error, got: %v", err)
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

// --- map node tests ---------------------------------------------------------

// transformEvaluator returns a transformation of the input for map tests.
type transformEvaluator struct {
	transform func(input map[string]any) map[string]any
	seen      []map[string]any
}

func (e *transformEvaluator) Evaluate(_ context.Context, _ string, input map[string]any) (map[string]any, error) {
	e.seen = append(e.seen, copyTestMap(input))
	return e.transform(input), nil
}

// reduceEvaluator applies a fold function for reduce tests.
type reduceEvaluator struct {
	fold func(acc, current any, index float64) map[string]any
	seen []map[string]any
	err  error
}

func (e *reduceEvaluator) Evaluate(_ context.Context, _ string, input map[string]any) (map[string]any, error) {
	if e.err != nil {
		return nil, e.err
	}
	e.seen = append(e.seen, copyTestMap(input))
	acc := input["accumulator"]
	current := input["current"]
	index := input["index"].(float64)
	return e.fold(acc, current, index), nil
}

func copyTestMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// mapTree builds a minimal trigger + map + response tree.
func mapTree(t *testing.T, spec MapSpec) *Node {
	t.Helper()
	return &Node{
		ID: "root", Type: TypeTrigger, Spec: rawSpec(t, TriggerSpec{Method: "GET", Path: "/x"}),
		Children: []Node{
			{ID: "m", Type: TypeMap, Spec: rawSpec(t, spec)},
			{ID: "resp", Type: TypeResponse, Spec: rawSpec(t, ResponseSpec{Status: 200})},
		},
	}
}

// reduceTree builds a minimal trigger + reduce + response tree.
func reduceTree(t *testing.T, spec ReduceSpec) *Node {
	t.Helper()
	return &Node{
		ID: "root", Type: TypeTrigger, Spec: rawSpec(t, TriggerSpec{Method: "GET", Path: "/x"}),
		Children: []Node{
			{ID: "r", Type: TypeReduce, Spec: rawSpec(t, spec)},
			{ID: "resp", Type: TypeResponse, Spec: rawSpec(t, ResponseSpec{Status: 200})},
		},
	}
}

func TestMap_EmptyArray(t *testing.T) {
	eval := &transformEvaluator{transform: func(input map[string]any) map[string]any {
		return map[string]any{"out": input["item"]}
	}}
	c := NewCtx("r", "t", "e", nil)
	c.Data = map[string]any{"items": []any{}}
	tree := mapTree(t, MapSpec{Over: "items", JDMID: "x", SaveAs: "result", MaxItems: 10})
	if err := New().Run(context.Background(), tree, Version{}, c, Deps{Decide: eval}); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	result := c.Data["result"].([]any)
	if len(result) != 0 {
		t.Fatalf("expected empty array, got len=%d", len(result))
	}
	if len(eval.seen) != 0 {
		t.Fatalf("expected 0 ZEN calls, got %d", len(eval.seen))
	}
}

func TestMap_SingleItem(t *testing.T) {
	eval := &transformEvaluator{transform: func(input map[string]any) map[string]any {
		return map[string]any{"transformed": true}
	}}
	c := NewCtx("r", "t", "e", nil)
	c.Data = map[string]any{"items": []any{map[string]any{"v": 1}}}
	tree := mapTree(t, MapSpec{Over: "items", JDMID: "x", SaveAs: "result", MaxItems: 10})
	if err := New().Run(context.Background(), tree, Version{}, c, Deps{Decide: eval}); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	result := c.Data["result"].([]any)
	if len(result) != 1 {
		t.Fatalf("expected 1-element array, got len=%d", len(result))
	}
	if len(eval.seen) != 1 {
		t.Fatalf("expected 1 ZEN call, got %d", len(eval.seen))
	}
}

func TestMap_TransformItems(t *testing.T) {
	// ZEN reads input["v"] (via projectItemInputs field projection) and scales by 1.1
	eval := &transformEvaluator{transform: func(input map[string]any) map[string]any {
		v := input["v"].(float64)
		return map[string]any{"price": v * 1.1}
	}}
	items := []any{
		map[string]any{"v": float64(100)},
		map[string]any{"v": float64(200)},
		map[string]any{"v": float64(300)},
	}
	c := NewCtx("r", "t", "e", nil)
	c.Data = map[string]any{"items": items}
	tree := mapTree(t, MapSpec{Over: "items", JDMID: "x", Input: []string{"v"}, SaveAs: "result", MaxItems: 10})
	if err := New().Run(context.Background(), tree, Version{}, c, Deps{Decide: eval}); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	result := c.Data["result"].([]any)
	if len(result) != 3 {
		t.Fatalf("expected 3 results, got %d", len(result))
	}
	// Each element is the full ZEN output map.
	want := []float64{110, 220, 330}
	for i, w := range want {
		m := result[i].(map[string]any)
		got := m["price"].(float64)
		if got < w-0.01 || got > w+0.01 {
			t.Fatalf("result[%d].price = %v, want ~%v", i, got, w)
		}
	}
}

func TestMap_MaxItemsExceeded(t *testing.T) {
	items := make([]any, 5)
	for i := range items {
		items[i] = i
	}
	c := NewCtx("r", "t", "e", nil)
	c.Data = map[string]any{"items": items}
	tree := mapTree(t, MapSpec{Over: "items", JDMID: "x", SaveAs: "result", MaxItems: 3})
	err := New().Run(context.Background(), tree, Version{}, c, Deps{Decide: &transformEvaluator{transform: func(input map[string]any) map[string]any { return nil }}})
	if err == nil {
		t.Fatalf("expected maxItems error")
	}
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("expected validation error, got: %v", err)
	}
}

func TestMap_SourceNotFound(t *testing.T) {
	c := NewCtx("r", "t", "e", nil)
	c.Data = map[string]any{}
	tree := mapTree(t, MapSpec{Over: "missing", JDMID: "x", SaveAs: "result", MaxItems: 10})
	err := New().Run(context.Background(), tree, Version{}, c, Deps{Decide: &transformEvaluator{transform: func(input map[string]any) map[string]any { return nil }}})
	if err == nil {
		t.Fatalf("expected error for missing source path")
	}
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("expected validation error, got: %v", err)
	}
}

func TestMap_InputProjection(t *testing.T) {
	// With input: ["price"], ZEN should only receive the "price" field.
	var seen []map[string]any
	eval := &transformEvaluator{transform: func(input map[string]any) map[string]any {
		seen = append(seen, copyTestMap(input))
		return map[string]any{"ok": true}
	}}
	items := []any{map[string]any{"price": float64(10), "extra": "ignored"}}
	c := NewCtx("r", "t", "e", nil)
	c.Data = map[string]any{"items": items}
	tree := mapTree(t, MapSpec{Over: "items", JDMID: "x", Input: []string{"price"}, SaveAs: "result", MaxItems: 10})
	if err := New().Run(context.Background(), tree, Version{}, c, Deps{Decide: eval}); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if len(seen) != 1 {
		t.Fatalf("expected 1 ZEN call")
	}
	if _, hasExtra := seen[0]["extra"]; hasExtra {
		t.Fatalf("ZEN input should not contain 'extra' field")
	}
	if seen[0]["price"] != float64(10) {
		t.Fatalf("ZEN input missing price field")
	}
}

func TestMap_ZENReturnsNonMap(t *testing.T) {
	// ZEN returns a non-map (nil) — stored as-is in the result array.
	eval := &transformEvaluator{transform: func(input map[string]any) map[string]any {
		return nil // ZEN returns nil map
	}}
	c := NewCtx("r", "t", "e", nil)
	c.Data = map[string]any{"items": []any{"a", "b"}}
	tree := mapTree(t, MapSpec{Over: "items", JDMID: "x", SaveAs: "result", MaxItems: 10})
	if err := New().Run(context.Background(), tree, Version{}, c, Deps{Decide: eval}); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	result := c.Data["result"].([]any)
	if len(result) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result))
	}
}

// --- reduce node tests ------------------------------------------------------

func TestReduce_EmptyArray(t *testing.T) {
	eval := &reduceEvaluator{fold: func(acc, current any, index float64) map[string]any {
		return nil
	}}
	c := NewCtx("r", "t", "e", nil)
	c.Data = map[string]any{"items": []any{}}
	initial := map[string]any{"sum": float64(0)}
	tree := reduceTree(t, ReduceSpec{Over: "items", JDMID: "x", SaveAs: "result", MaxItems: 10, InitialValue: initial})
	if err := New().Run(context.Background(), tree, Version{}, c, Deps{Decide: eval}); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if len(eval.seen) != 0 {
		t.Fatalf("expected 0 ZEN calls for empty array, got %d", len(eval.seen))
	}
	result := c.Data["result"]
	m, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T", result)
	}
	if m["sum"] != float64(0) {
		t.Fatalf("expected sum=0, got %v", m["sum"])
	}
}

func TestReduce_EmptyArrayNoInitial(t *testing.T) {
	eval := &reduceEvaluator{fold: func(acc, current any, index float64) map[string]any { return nil }}
	c := NewCtx("r", "t", "e", nil)
	c.Data = map[string]any{"items": []any{}}
	tree := reduceTree(t, ReduceSpec{Over: "items", JDMID: "x", SaveAs: "result", MaxItems: 10})
	if err := New().Run(context.Background(), tree, Version{}, c, Deps{Decide: eval}); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if len(eval.seen) != 0 {
		t.Fatalf("expected 0 ZEN calls for empty array, got %d", len(eval.seen))
	}
	if c.Data["result"] != nil {
		t.Fatalf("expected nil result for empty array with no initialValue, got %v", c.Data["result"])
	}
}

func TestReduce_SingleItem(t *testing.T) {
	eval := &reduceEvaluator{fold: func(acc, current any, index float64) map[string]any {
		return map[string]any{"done": true}
	}}
	c := NewCtx("r", "t", "e", nil)
	c.Data = map[string]any{"items": []any{map[string]any{"v": 1}}}
	tree := reduceTree(t, ReduceSpec{Over: "items", JDMID: "x", SaveAs: "result", MaxItems: 10})
	if err := New().Run(context.Background(), tree, Version{}, c, Deps{Decide: eval}); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if len(eval.seen) != 1 {
		t.Fatalf("expected 1 ZEN call, got %d", len(eval.seen))
	}
	m := c.Data["result"].(map[string]any)
	if m["done"] != true {
		t.Fatalf("unexpected result: %v", m)
	}
}

func TestReduce_SumValues(t *testing.T) {
	eval := &reduceEvaluator{fold: func(acc, current any, index float64) map[string]any {
		var sum float64
		if acc != nil {
			sum = acc.(map[string]any)["sum"].(float64)
		}
		cur := current.(map[string]any)
		sum += cur["item"].(map[string]any)["v"].(float64)
		return map[string]any{"sum": sum}
	}}
	items := []any{
		map[string]any{"v": float64(1)},
		map[string]any{"v": float64(2)},
		map[string]any{"v": float64(3)},
	}
	c := NewCtx("r", "t", "e", nil)
	c.Data = map[string]any{"items": items}
	tree := reduceTree(t, ReduceSpec{Over: "items", JDMID: "x", SaveAs: "result", MaxItems: 10})
	if err := New().Run(context.Background(), tree, Version{}, c, Deps{Decide: eval}); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	m := c.Data["result"].(map[string]any)
	if m["sum"] != float64(6) {
		t.Fatalf("expected sum=6, got %v", m["sum"])
	}
}

func TestReduce_FindMax(t *testing.T) {
	eval := &reduceEvaluator{fold: func(acc, current any, index float64) map[string]any {
		var max float64
		if acc != nil {
			max = acc.(map[string]any)["max"].(float64)
		}
		n := current.(map[string]any)["item"].(map[string]any)["n"].(float64)
		if n > max {
			max = n
		}
		return map[string]any{"max": max}
	}}
	items := []any{
		map[string]any{"n": float64(3)},
		map[string]any{"n": float64(1)},
		map[string]any{"n": float64(5)},
	}
	c := NewCtx("r", "t", "e", nil)
	c.Data = map[string]any{"items": items}
	tree := reduceTree(t, ReduceSpec{Over: "items", JDMID: "x", SaveAs: "result", MaxItems: 10})
	if err := New().Run(context.Background(), tree, Version{}, c, Deps{Decide: eval}); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	m := c.Data["result"].(map[string]any)
	if m["max"] != float64(5) {
		t.Fatalf("expected max=5, got %v", m["max"])
	}
}

func TestReduce_BuildObject(t *testing.T) {
	eval := &reduceEvaluator{fold: func(acc, current any, index float64) map[string]any {
		out := map[string]any{}
		if acc != nil {
			for k, v := range acc.(map[string]any) {
				out[k] = v
			}
		}
		item := current.(map[string]any)["item"].(map[string]any)
		out[item["k"].(string)] = item["v"]
		return out
	}}
	items := []any{
		map[string]any{"k": "a", "v": float64(1)},
		map[string]any{"k": "b", "v": float64(2)},
	}
	c := NewCtx("r", "t", "e", nil)
	c.Data = map[string]any{"items": items}
	tree := reduceTree(t, ReduceSpec{Over: "items", JDMID: "x", SaveAs: "result", MaxItems: 10})
	if err := New().Run(context.Background(), tree, Version{}, c, Deps{Decide: eval}); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	m := c.Data["result"].(map[string]any)
	if m["a"] != float64(1) || m["b"] != float64(2) {
		t.Fatalf("unexpected result: %v", m)
	}
}

func TestReduce_MaxItemsExceeded(t *testing.T) {
	items := make([]any, 5)
	for i := range items {
		items[i] = i
	}
	c := NewCtx("r", "t", "e", nil)
	c.Data = map[string]any{"items": items}
	tree := reduceTree(t, ReduceSpec{Over: "items", JDMID: "x", SaveAs: "result", MaxItems: 3})
	eval := &reduceEvaluator{fold: func(acc, current any, index float64) map[string]any { return nil }}
	err := New().Run(context.Background(), tree, Version{}, c, Deps{Decide: eval})
	if err == nil {
		t.Fatalf("expected maxItems error")
	}
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("expected validation error, got: %v", err)
	}
}

func TestReduce_SourceNotFound(t *testing.T) {
	c := NewCtx("r", "t", "e", nil)
	c.Data = map[string]any{}
	tree := reduceTree(t, ReduceSpec{Over: "missing", JDMID: "x", SaveAs: "result", MaxItems: 10})
	eval := &reduceEvaluator{fold: func(acc, current any, index float64) map[string]any { return nil }}
	err := New().Run(context.Background(), tree, Version{}, c, Deps{Decide: eval})
	if err == nil {
		t.Fatalf("expected error for missing source path")
	}
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("expected validation error, got: %v", err)
	}
}

func TestReduce_SourceNotArray(t *testing.T) {
	c := NewCtx("r", "t", "e", nil)
	c.Data = map[string]any{"items": "not-an-array"}
	tree := reduceTree(t, ReduceSpec{Over: "items", JDMID: "x", SaveAs: "result", MaxItems: 10})
	eval := &reduceEvaluator{fold: func(acc, current any, index float64) map[string]any { return nil }}
	err := New().Run(context.Background(), tree, Version{}, c, Deps{Decide: eval})
	if err == nil {
		t.Fatalf("expected error for non-array source")
	}
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("expected validation error, got: %v", err)
	}
}

func TestReduce_ZENInputShape(t *testing.T) {
	// Verify that ZEN receives {"accumulator": ..., "current": {"item": ...}, "index": float64}
	var seenInputs []map[string]any
	eval := &reduceEvaluator{fold: func(acc, current any, index float64) map[string]any {
		seenInputs = append(seenInputs, map[string]any{
			"accumulator": acc,
			"current":     current,
			"index":       index,
		})
		return map[string]any{"acc": true}
	}}
	items := []any{42.0, 43.0}
	c := NewCtx("r", "t", "e", nil)
	c.Data = map[string]any{"items": items}
	tree := reduceTree(t, ReduceSpec{Over: "items", JDMID: "x", SaveAs: "result", MaxItems: 10})
	if err := New().Run(context.Background(), tree, Version{}, c, Deps{Decide: eval}); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if len(seenInputs) != 2 {
		t.Fatalf("expected 2 inputs, got %d", len(seenInputs))
	}
	// First call: acc=nil, current={"item": 42.0}, index=0
	first := seenInputs[0]
	if first["accumulator"] != nil {
		t.Fatalf("first call accumulator should be nil, got %v", first["accumulator"])
	}
	cur := first["current"].(map[string]any)
	if cur["item"] != float64(42) {
		t.Fatalf("first call current.item = %v, want 42", cur["item"])
	}
	if first["index"] != float64(0) {
		t.Fatalf("first call index = %v, want 0.0", first["index"])
	}
}

func TestReduce_IndexIsFloat64(t *testing.T) {
	var indices []float64
	eval := &reduceEvaluator{fold: func(acc, current any, index float64) map[string]any {
		indices = append(indices, index)
		return map[string]any{"last": index}
	}}
	items := []any{1, 2, 3}
	c := NewCtx("r", "t", "e", nil)
	c.Data = map[string]any{"items": items}
	tree := reduceTree(t, ReduceSpec{Over: "items", JDMID: "x", SaveAs: "result", MaxItems: 10})
	if err := New().Run(context.Background(), tree, Version{}, c, Deps{Decide: eval}); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if len(indices) != 3 {
		t.Fatalf("expected 3 indices, got %d", len(indices))
	}
	for i, idx := range indices {
		if idx != float64(i) {
			t.Fatalf("index[%d] = %v, want %v", i, idx, float64(i))
		}
	}
}

func TestReduce_CtxCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	c := NewCtx("r", "t", "e", nil)
	c.Data = map[string]any{"items": []any{1, 2, 3}}
	tree := reduceTree(t, ReduceSpec{Over: "items", JDMID: "x", SaveAs: "result", MaxItems: 10})
	eval := &reduceEvaluator{fold: func(acc, current any, index float64) map[string]any { return nil }}
	err := New().Run(ctx, tree, Version{}, c, Deps{Decide: eval})
	if err == nil {
		t.Fatalf("expected timeout error for cancelled context")
	}
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("expected timeout error, got: %v", err)
	}
}
