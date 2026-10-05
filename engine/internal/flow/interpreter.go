package flow

import (
	"context"
	"sync/atomic"

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

// DefaultWorkBudget bounds the total units of work a single request may consume
// (slice-a §6): one unit per node executed and one per forEach iteration. It is
// the global backstop against a tree that is shallow but fans out or iterates
// pathologically. 100_000 units is far above any legitimate flow.
const DefaultWorkBudget int64 = 100_000

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
		TypeSwitch:    switchHandler{},
		TypeSequence:  deferredHandler{name: "sequence"},
		TypeParallel:  parallelHandler{},
		TypeForEach:   forEachHandler{},
		TypeDecision:  decisionHandler{},
		TypeLogger:    loggerHandler{},
	}}
}

// budget carries the per-request recursion and work guards. It rides on the
// call stack rather than the handler signature so the Walker seam stays narrow.
//
// The work counter is a single per-request ceiling shared across every branch,
// including the goroutines a parallel/forEach fan-out spawns, so it is debited
// atomically. maxDepth is a read-only ceiling; the *current* depth is NOT kept
// here — it rides on the context (per goroutine stack) via depthFrom, so
// concurrent sibling branches each track their own recursion depth without
// racing on a shared counter.
type budget struct {
	maxDepth int
	work     atomic.Int64 // units consumed so far (atomic: fan-out shares it)
	maxWork  int64        // ceiling (DefaultWorkBudget)
}

// chargeWork debits n units from the shared work budget, returning a
// ClassValidation error once the budget is exhausted. It is called once per node
// walked and once per forEach iteration (slice-a §6) and is safe to call from
// the concurrent children of a parallel/forEach node.
func (b *budget) chargeWork(n int64) error {
	if b.work.Add(n) > b.maxWork {
		return newErr(ClassValidation, "request work budget exhausted")
	}
	return nil
}

// depthKey carries the current recursion depth on the context. Each walk pushes
// depth+1 for its subtree; a parallel/forEach child inherits the parent depth
// and descends independently, so sibling branches never share a depth counter.
type depthKey struct{}

// depthFrom reads the current recursion depth from the context (0 if unset).
func depthFrom(ctx context.Context) int {
	d, _ := ctx.Value(depthKey{}).(int)
	return d
}

// runState is threaded on the context so walk can read the budget without
// widening the Walker interface.
type runStateKey struct{}

// budgetFrom pulls the per-request budget off the context so a handler (forEach)
// can charge work per iteration without widening the Walker interface. It is
// nil-safe: a context with no budget yields nil.
func budgetFrom(ctx context.Context) *budget {
	b, _ := ctx.Value(runStateKey{}).(*budget)
	return b
}

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
	b := &budget{maxDepth: DefaultMaxDepth, maxWork: DefaultWorkBudget}
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

// walk is the recursion body: budget + cancellation check, a per-node span via
// TraceNode, then dispatch to the handler, passing the Interpreter itself as the
// Walker. TraceNode creates the span, records the duration/branch, and feeds the
// dry-run collector when one is attached — so dry-run trace == live trace shape.
func (ip *Interpreter) walk(ctx context.Context, node *Node, c *Ctx, dep Deps) (Directive, error) {
	if node == nil {
		return Directive{}, newErr(ClassValidation, "nil node")
	}
	if err := ctx.Err(); err != nil {
		return Directive{}, wrapErr(ClassTimeout, "walk cancelled", err)
	}

	b, _ := ctx.Value(runStateKey{}).(*budget)
	if b != nil {
		depth := depthFrom(ctx)
		if depth >= b.maxDepth {
			return Directive{}, newErr(ClassValidation, "max depth exceeded")
		}
		// Every node executed costs one work unit (slice-a §6); forEach charges
		// additionally per iteration.
		if err := b.chargeWork(1); err != nil {
			return Directive{}, err
		}
		// Descend one level for this subtree; carried on the context so concurrent
		// sibling branches each track their own depth.
		ctx = context.WithValue(ctx, depthKey{}, depth+1)
	}

	handler, ok := ip.handlers[node.Type]
	if !ok {
		return Directive{}, validationf("unknown node type %q", string(node.Type))
	}

	// Use TraceNode for tracing and dry-run collection. When dep.Trace or dep.Log
	// is nil, TraceNode still records to the collector if one is attached.
	var directive Directive
	var execErr error
	out := observ.TraceNode(ctx, dep.Trace, dep.Log, node.ID, string(node.Type),
		func(tctx context.Context) observ.NodeOutcome {
			directive, execErr = handler.Exec(tctx, c, *node, dep, ip)
			if execErr != nil {
				execErr = classify(execErr)
			}
			return observ.NodeOutcome{Branch: directive.Branch, Err: execErr}
		})

	return directive, out.Err
}
