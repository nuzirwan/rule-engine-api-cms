// Package spikeflow is a THROWAWAY Phase-0 spike interpreter. It implements the
// THINNEST subset of Slice A's tree-walking model needed to exercise AC-S3's
// risky seam: Trigger -> Action(postgres read) -> Condition(ZEN eval) -> two
// branches each Action(REST call) -> Set -> Response, run synchronously.
//
// Deliberately omitted (NOT Slice A's full model): budget, parallel, forEach,
// switch, validation, tracing, resilience, version pinning, the full node
// taxonomy. This is a spike, not the v1 interpreter.
package spikeflow

import "context"

// NodeType is the small subset the spike needs.
type NodeType string

const (
	TypeTrigger   NodeType = "trigger"
	TypeAction    NodeType = "action"
	TypeCondition NodeType = "condition"
	TypeSet       NodeType = "set"
	TypeResponse  NodeType = "response"
)

// Node is a flow-tree node. Children are addressed by their index in Children,
// except Condition which selects a child by key via TrueKey/FalseKey.
type Node struct {
	ID       string
	Type     NodeType
	Children []*Node

	// Action
	Action ActionFunc // the pluggable side-effecting op (pg read / rest call)
	SaveAs string     // ctx.Data key for the action result

	// Condition
	Decide    DecideFunc // ZEN evaluation seam (shaped like decision.Evaluator)
	Inputs    []string   // ctx paths projected into the decision input
	TrueKey   string     // child ID walked when decision picks the true branch
	FalseKey  string     // child ID walked otherwise
	BranchKey string     // decision output field naming the branch (e.g. "shipping")

	// Set
	TargetPath string // dotted path written into ctx.Response
	FromPath   string // dotted path read from the merged view (one of From/Value)
	Value      any    // literal value
}

// ActionFunc performs one side effect and returns its result object.
// The spike backs this with a Postgres read and REST calls.
type ActionFunc func(ctx context.Context, c *Ctx) (map[string]any, error)

// DecideFunc is the ZEN seam: project input, evaluate, return the decision map.
// It mirrors decision.Evaluator's intent (JSON in, JSON out) without the jdmID
// indirection the spike does not need.
type DecideFunc func(ctx context.Context, input map[string]any) (map[string]any, error)
