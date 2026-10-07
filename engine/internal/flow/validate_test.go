package flow

import (
	"encoding/json"
	"testing"
)

// vtRefs is a RefResolver that accepts a fixed set of connection and jdm keys.
type vtRefs struct {
	conns map[string]bool
	jdms  map[string]bool
}

func (r vtRefs) HasConnection(k string) bool { return r.conns[k] }
func (r vtRefs) HasJDM(id string) bool       { return r.jdms[id] }

// hasCode reports whether issues contains an issue with the given code.
func hasCode(issues []ValidationIssue, code string) bool {
	for _, is := range issues {
		if is.Code == code {
			return true
		}
	}
	return false
}

// resp is a terminal Response leaf used in valid-tree fixtures.
func resp(t *testing.T, id string) Node {
	t.Helper()
	return Node{ID: id, Type: TypeResponse, Spec: raw(t, ResponseSpec{Status: 200})}
}

func TestValidateTree(t *testing.T) {
	refs := vtRefs{
		conns: map[string]bool{"pg": true},
		jdms:  map[string]bool{"j": true},
	}

	cases := []struct {
		name     string
		tree     func(t *testing.T) Node
		wantCode string // "" => expect a valid tree (no issues)
	}{
		{
			name: "valid linear flow",
			tree: func(t *testing.T) Node {
				return Node{ID: "root", Type: TypeTrigger, Spec: raw(t, TriggerSpec{Method: "GET", Path: "/x"}),
					Children: []Node{
						{ID: "s", Type: TypeSet, Spec: raw(t, SetSpec{TargetPath: "a", Value: 1})},
						resp(t, "r"),
					}}
			},
			wantCode: "",
		},
		{
			name: "root not a trigger",
			tree: func(t *testing.T) Node {
				return Node{ID: "root", Type: TypeSet, Spec: raw(t, SetSpec{TargetPath: "a", Value: 1})}
			},
			wantCode: "root_not_trigger",
		},
		{
			name: "control node without children",
			tree: func(t *testing.T) Node {
				return Node{ID: "root", Type: TypeTrigger, Spec: raw(t, TriggerSpec{Method: "GET", Path: "/x"})}
			},
			wantCode: "control_no_children",
		},
		{
			name: "leaf with children",
			tree: func(t *testing.T) Node {
				return Node{ID: "root", Type: TypeTrigger, Spec: raw(t, TriggerSpec{Method: "GET", Path: "/x"}),
					Children: []Node{{
						ID: "s", Type: TypeSet, Spec: raw(t, SetSpec{TargetPath: "a", Value: 1}),
						Children: []Node{resp(t, "r")},
					}}}
			},
			wantCode: "leaf_has_children",
		},
		{
			name: "forEach maxItems must be > 0",
			tree: func(t *testing.T) Node {
				return Node{ID: "root", Type: TypeTrigger, Spec: raw(t, TriggerSpec{Method: "GET", Path: "/x"}),
					Children: []Node{{
						ID: "loop", Type: TypeForEach, Spec: raw(t, ForEachSpec{Over: "items", As: "it", MaxItems: 0}),
						Children: []Node{resp(t, "r")},
					}}}
			},
			wantCode: "foreach_maxitems",
		},
		{
			name: "parallel overlapping sibling writes rejected",
			tree: func(t *testing.T) Node {
				return Node{ID: "root", Type: TypeTrigger, Spec: raw(t, TriggerSpec{Method: "GET", Path: "/x"}),
					Children: []Node{
						{
							ID: "fan", Type: TypeParallel, Spec: raw(t, ParallelSpec{}),
							Children: []Node{
								{ID: "a", Type: TypeSet, Spec: raw(t, SetSpec{TargetPath: "order.id", Value: 1})},
								{ID: "b", Type: TypeSet, Spec: raw(t, SetSpec{TargetPath: "order", Value: 2})},
							},
						},
						resp(t, "r"),
					}}
			},
			wantCode: "parallel_overlap",
		},
		{
			name: "parallel disjoint sibling writes accepted",
			tree: func(t *testing.T) Node {
				return Node{ID: "root", Type: TypeTrigger, Spec: raw(t, TriggerSpec{Method: "GET", Path: "/x"}),
					Children: []Node{
						{
							ID: "fan", Type: TypeParallel, Spec: raw(t, ParallelSpec{}),
							Children: []Node{
								{ID: "a", Type: TypeSet, Spec: raw(t, SetSpec{TargetPath: "order.id", Value: 1})},
								{ID: "b", Type: TypeSet, Spec: raw(t, SetSpec{TargetPath: "order.total", Value: 2})},
							},
						},
						resp(t, "r"),
					}}
			},
			wantCode: "",
		},
		{
			name: "missing response on a path",
			tree: func(t *testing.T) Node {
				return Node{ID: "root", Type: TypeTrigger, Spec: raw(t, TriggerSpec{Method: "GET", Path: "/x"}),
					Children: []Node{{ID: "s", Type: TypeSet, Spec: raw(t, SetSpec{TargetPath: "a", Value: 1})}}}
			},
			wantCode: "missing_response",
		},
		{
			name: "two responses on a path",
			tree: func(t *testing.T) Node {
				return Node{ID: "root", Type: TypeTrigger, Spec: raw(t, TriggerSpec{Method: "GET", Path: "/x"}),
					Children: []Node{resp(t, "r1"), resp(t, "r2")}}
			},
			wantCode: "multiple_responses",
		},
		{
			name: "switch default references unknown child",
			tree: func(t *testing.T) Node {
				return Node{ID: "root", Type: TypeTrigger, Spec: raw(t, TriggerSpec{Method: "GET", Path: "/x"}),
					Children: []Node{{
						ID: "sw", Type: TypeSwitch, Spec: raw(t, SwitchSpec{JDMID: "j", Default: "ghost"}),
						Children: []Node{resp(t, "r")},
					}}}
			},
			wantCode: "unknown_child_key",
		},
		{
			name: "dangling connection reference",
			tree: func(t *testing.T) Node {
				return Node{ID: "root", Type: TypeTrigger, Spec: raw(t, TriggerSpec{Method: "GET", Path: "/x"}),
					Children: []Node{
						{ID: "a", Type: TypeAction, Spec: raw(t, ActionSpec{ConnRef: ConnRef{Connection: "nope"}, Operation: op("query"), SaveAs: "x"})},
						resp(t, "r"),
					}}
			},
			wantCode: "dangling_connection",
		},
		{
			name: "duplicate node id",
			tree: func(t *testing.T) Node {
				return Node{ID: "root", Type: TypeTrigger, Spec: raw(t, TriggerSpec{Method: "GET", Path: "/x"}),
					Children: []Node{
						{ID: "dup", Type: TypeSet, Spec: raw(t, SetSpec{TargetPath: "a", Value: 1})},
						{ID: "dup", Type: TypeResponse, Spec: raw(t, ResponseSpec{Status: 200})},
					}}
			},
			wantCode: "duplicate_id",
		},
		{
			name: "decision missing saveAs",
			tree: func(t *testing.T) Node {
				return Node{ID: "root", Type: TypeTrigger, Spec: raw(t, TriggerSpec{Method: "GET", Path: "/x"}),
					Children: []Node{
						{ID: "d", Type: TypeDecision, Spec: raw(t, DecisionSpec{JDMID: "j"})},
						resp(t, "r"),
					}}
			},
			wantCode: "decision_no_saveas",
		},
		{
			name: "invalid input schema rejected",
			tree: func(t *testing.T) Node {
				return Node{ID: "root", Type: TypeTrigger, Spec: json.RawMessage(`{"method":"GET","path":"/x","input":{"schema":{"type":123}}}`),
					Children: []Node{resp(t, "r")}}
			},
			wantCode: "bad_input_schema",
		},
		{
			name: "valid input schema accepted",
			tree: func(t *testing.T) Node {
				return Node{ID: "root", Type: TypeTrigger, Spec: json.RawMessage(`{"method":"GET","path":"/x","input":{"schema":{"type":"object","properties":{"id":{"type":"string"}}}}}`),
					Children: []Node{resp(t, "r")}}
			},
			wantCode: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			issues := ValidateTree(tc.tree(t), refs)
			if tc.wantCode == "" {
				if len(issues) != 0 {
					t.Fatalf("expected a valid tree, got issues: %#v", issues)
				}
				return
			}
			if !hasCode(issues, tc.wantCode) {
				t.Fatalf("expected issue code %q, got: %#v", tc.wantCode, issues)
			}
		})
	}
}
