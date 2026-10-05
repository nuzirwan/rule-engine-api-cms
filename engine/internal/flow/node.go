package flow

import (
	"context"
	"encoding/json"

	"nzr-rules-engine/internal/connect"
	"nzr-rules-engine/internal/decision"
	"nzr-rules-engine/internal/observ"
)

// NodeType names a node kind in the flow tree. The eleven types are the frozen
// taxonomy; the thin slice executes trigger/action/condition/set/response and
// refuses the deferred ones (switch/parallel/forEach/decision/logger/sequence)
// with a validation error at runtime.
type NodeType string

// The node type constants (lld-contracts.md).
const (
	TypeTrigger   NodeType = "trigger"
	TypeAction    NodeType = "action"
	TypeCondition NodeType = "condition"
	TypeSwitch    NodeType = "switch"
	TypeSequence  NodeType = "sequence"
	TypeParallel  NodeType = "parallel"
	TypeForEach   NodeType = "forEach"
	TypeDecision  NodeType = "decision"
	TypeSet       NodeType = "set"
	TypeLogger    NodeType = "logger"
	TypeResponse  NodeType = "response"
	TypeFilter    NodeType = "filter"
	TypeFind      NodeType = "find"
)

// Node is one node in the flow tree. Control nodes carry Children; leaves do not.
// Spec is the type-specific config, parsed by the handler for the node's Type.
type Node struct {
	ID       string          `json:"id"`
	Type     NodeType        `json:"type"`
	Spec     json.RawMessage `json:"spec"`
	Children []Node          `json:"children,omitempty"`
}

// Directive tells the interpreter how a node resolved. Branch names the child
// key a control node selected (informational for the trace); Stop true unwinds
// the walk so a Response short-circuits every ancestor.
type Directive struct {
	Branch string
	Stop   bool
}

// NodeHandler executes one node, mutating c and returning the next directive.
// Control-node handlers descend into children via the Walker passed in, never by
// importing the Interpreter (that keeps the handler->interpreter dependency
// inverted and cycle-free). Leaf handlers ignore w.
type NodeHandler interface {
	Exec(ctx context.Context, c *Ctx, n Node, dep Deps, w Walker) (Directive, error)
}

// Walker is the single recursion seam: a handler calls w.Walk(child) to execute
// a child subtree. The Interpreter is the sole implementer, so there is no
// handler->interpreter import cycle and no injected func pointer.
type Walker interface {
	Walk(ctx context.Context, child *Node, c *Ctx, dep Deps) (Directive, error)
}

// Deps is a handler's view of the other slices (dependency inversion): the pure
// flow core reaches connections, decisions, tracing and logging only through
// these interfaces.
type Deps struct {
	Conns  connect.Registry
	Decide decision.Evaluator
	Trace  observ.Tracer
	Log    observ.Logger
}
