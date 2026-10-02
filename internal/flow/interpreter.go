package flow

import (
	"context"

	"nzr-rules-engine/internal/observ"
)

// spanScope bundles a span with the context that carries it.
type spanScope struct {
	ctx  context.Context
	span observ.Span
}

// startSpan opens a span through the Deps tracer. The caller guards dep.Trace
// for nil before calling.
func startSpan(ctx context.Context, dep Deps, name string, attrs map[string]any) spanScope {
	c, s := dep.Trace.StartSpan(ctx, name, attrs)
	return spanScope{ctx: c, span: s}
}

// DefaultMaxDepth bounds recursion so a pathological tree cannot blow the stack.
// Full time/work budgeting is out of thin-slice scope; the depth guard stays as
// the runtime backstop.
const DefaultMaxDepth = 64

// Version identifies the pinned flow version for span attribution. The caller
// (httpapi) resolves and pins a config.FlowVersion once at request start and
// passes its tree plus this metadata into Run; the interpreter never resolves a
// version itself (AC-11). Keeping Run free of a config import also keeps the
// pure flow core free of any infrastructure dependency.
type Version struct {
	FlowID  string
	Version int
}

// Interpreter holds the node dispatch table and nothing request-specific, so a
// single instance is safe to share across goroutines. It is the sole
// implementer of Walker.
type Interpreter struct {
	handlers map[NodeType]NodeHandler
}

// New builds an Interpreter with every node type registered. The five functional
// handlers execute; the deferred types are registered as stubs that refuse at
// runtime with a validation error so the dispatch table matches the full
// taxonomy and the walk never panics on them.
func New() *Interpreter {
	return &Interpreter{handlers: map[NodeType]NodeHandler{
		TypeTrigger:   triggerHandler{},
		TypeAction:    actionHandler{},
		TypeCondition: conditionHandler{},
		TypeSet:       setHandler{},
		TypeResponse:  responseHandler{},
		TypeSwitch:    deferredHandler{name: "switch"},
		TypeSequence:  deferredHandler{name: "sequence"},
		TypeParallel:  deferredHandler{name: "parallel"},
		TypeForEach:   deferredHandler{name: "forEach"},
		TypeDecision:  deferredHandler{name: "decision"},
		TypeLogger:    deferredHandler{name: "logger"},
	}}
}

// budget carries the per-request recursion guard. It rides on the call stack
// rather than the handler signature so the Walker seam stays narrow.
type budget struct {
	depth    int
	maxDepth int
}

// runState is threaded on the context so walk can read the budget without
// widening the Walker interface.
type runStateKey struct{}

// Run walks exactly the provided tree snapshot to completion. The caller has
// already resolved and pinned the version (AC-11) and built the initial Ctx. The
// returned error is always classified at the seam.
func (ip *Interpreter) Run(ctx context.Context, tree *Node, ver Version, c *Ctx, dep Deps) error {
	if tree == nil {
		return newErr(ClassValidation, "nil flow tree")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	b := &budget{maxDepth: DefaultMaxDepth}
	ctx = context.WithValue(ctx, runStateKey{}, b)

	var span observ.Span
	if dep.Trace != nil {
		sp := startSpan(ctx, dep, "flow.run", map[string]any{
			"flow_id":      ver.FlowID,
			"flow_version": ver.Version,
		})
		ctx, span = sp.ctx, sp.span
	}

	_, err := ip.walk(ctx, tree, c, dep)
	err = classify(err)
	if span != nil {
		span.End(err)
	}
	return err
}

// Walk is the single recursion seam (Walker). A control handler calls it to run
// a chosen child subtree; it re-enters the same dispatch below. It is exported to
// satisfy the Walker interface; handlers depend only on that interface.
func (ip *Interpreter) Walk(ctx context.Context, child *Node, c *Ctx, dep Deps) (Directive, error) {
	return ip.walk(ctx, child, c, dep)
}

// walk is the recursion body: budget + cancellation check, a per-node span, then
// dispatch to the handler, passing the Interpreter itself as the Walker.
func (ip *Interpreter) walk(ctx context.Context, node *Node, c *Ctx, dep Deps) (Directive, error) {
	if node == nil {
		return Directive{}, newErr(ClassValidation, "nil node")
	}
	if err := ctx.Err(); err != nil {
		return Directive{}, wrapErr(ClassTimeout, "walk cancelled", err)
	}

	b, _ := ctx.Value(runStateKey{}).(*budget)
	if b != nil {
		if b.depth >= b.maxDepth {
			return Directive{}, newErr(ClassValidation, "max depth exceeded")
		}
		b.depth++
		defer func() { b.depth-- }()
	}

	var span observ.Span
	if dep.Trace != nil {
		sp := startSpan(ctx, dep, "node."+string(node.Type), map[string]any{
			"node_id":   node.ID,
			"node_type": string(node.Type),
		})
		ctx, span = sp.ctx, sp.span
	}

	handler, ok := ip.handlers[node.Type]
	if !ok {
		err := validationf("unknown node type %q", string(node.Type))
		if span != nil {
			span.End(err)
		}
		return Directive{}, err
	}

	directive, err := handler.Exec(ctx, c, *node, dep, ip)
	if err != nil {
		err = classify(err)
		if span != nil {
			span.End(err)
		}
		return directive, err
	}
	if span != nil {
		span.Set("branch_taken", directive.Branch)
		span.End(nil)
	}
	return directive, nil
}
