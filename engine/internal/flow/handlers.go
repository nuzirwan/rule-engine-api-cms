package flow

import (
	"context"
	"time"

	"nzr-rules-engine/internal/connect"
	"nzr-rules-engine/internal/observ"
)

// triggerHandler maps the declared TriggerSpec.Input (already lifted into
// Ctx.Input by httpapi before Run) and walks the single child body. A trigger
// must have exactly one child in the thin slice.
type triggerHandler struct{}

// Exec implements NodeHandler.
func (triggerHandler) Exec(ctx context.Context, c *Ctx, n Node, dep Deps, w Walker) (Directive, error) {
	if _, err := parseSpec[TriggerSpec](n.Spec); err != nil {
		return Directive{}, err
	}
	if len(n.Children) == 0 {
		return Directive{}, validationf("trigger %q must have at least one child", n.ID)
	}
	return walkChildren(ctx, n.Children, c, dep, w)
}

// walkChildren runs children in order, threading the same Ctx, and stops early
// when a child's Directive.Stop is set (a Response short-circuits upward). It is
// the linear-parent helper shared by trigger/action/set.
func walkChildren(ctx context.Context, children []Node, c *Ctx, dep Deps, w Walker) (Directive, error) {
	for i := range children {
		child := children[i]
		d, err := w.Walk(ctx, &child, c, dep)
		if err != nil {
			return d, err
		}
		if d.Stop {
			return d, nil
		}
	}
	return Directive{}, nil
}

// actionHandler performs one I/O operation through the connection registry and
// stores the result under SaveAs in Ctx.Data. The per-node timeout override is
// applied to the operation; OnError "continue" records the failure and keeps
// walking, otherwise a failure aborts the walk.
type actionHandler struct{}

// Exec implements NodeHandler.
func (actionHandler) Exec(ctx context.Context, c *Ctx, n Node, dep Deps, w Walker) (Directive, error) {
	spec, err := parseSpec[ActionSpec](n.Spec)
	if err != nil {
		return Directive{}, err
	}
	if spec.Connection == "" {
		return Directive{}, validationf("action %q missing connection", n.ID)
	}
	if dep.Conns == nil {
		return Directive{}, newErr(ClassInternal, "no connection registry wired")
	}

	client, err := dep.Conns.Client(ctx, spec.Connection)
	if err != nil {
		return Directive{}, classify(err)
	}

	op := spec.Operation
	if ov := timeoutOverride(spec.Resilience); ov != nil {
		op.Override = ov
	}

	// Copy the UnwrapSingleRow preference to the operation so the httpapi adapter
	// can use it in normalizeResult. This removes the demo-tuned heuristic that
	// assumed single-row queries (e.g. orders/{id}) should always be unwrapped.
	op.UnwrapSingleRow = spec.UnwrapSingleRow

	// Resolve the idempotency key template (R4). When IdempotencyKeyFrom is set
	// the resolved value flows into op.IdempotencyKey; the connect layer's
	// dedupGuard then protects the non-idempotent write with a SET-NX lock.
	if spec.IdempotencyKeyFrom != "" {
		if v, ok := c.GetPath(spec.IdempotencyKeyFrom); ok {
			if s, ok := v.(string); ok && s != "" {
				op.IdempotencyKey = s
			}
		}
	}

	// Dry-run write-suppression (AC-14): under a dry-run context a write op
	// performs NO I/O. The suppressed write is recorded into the trace collector
	// (if one is attached) as wrote:"suppressed", then the walk continues linearly
	// through the children. Reads (query/get/ping) are NOT suppressed — a dry-run
	// still reflects real reads so the author sees a faithful preview. The
	// dry-run flag and the collector both ride on the context (observ seam);
	// observ does not import flow, so there is no import cycle.
	if observ.IsDryRun(ctx) && isWriteOp(op.Kind) {
		if tc, ok := observ.CollectorFrom(ctx); ok {
			tc.Record(n.ID, string(n.Type), "", map[string]any{"wrote": "suppressed"})
		}
		return walkChildren(ctx, n.Children, c, dep, w)
	}

	result, err := client.Execute(ctx, op)
	if err != nil {
		if spec.OnError == OnErrorContinue {
			if dep.Log != nil {
				dep.Log.Emit(ctx, "warn", "action.continue_on_error", map[string]any{
					"node_id": n.ID,
					"err":     err.Error(),
				})
			}
			// Record the failure, then keep walking the linear children (R3).
			return walkChildren(ctx, n.Children, c, dep, w)
		}
		return Directive{}, classify(err)
	}

	if spec.SaveAs != "" {
		if c.Data == nil {
			c.Data = map[string]any{}
		}
		c.Data[spec.SaveAs] = result
	}
	// An action is linear: after its side effect, walk its children in order.
	return walkChildren(ctx, n.Children, c, dep, w)
}

// timeoutOverride turns a per-node ResilienceOverride into a connect override
// carrying only the timeout (the sole resilience field the thin slice honors).
// It returns nil when there is no timeout override, so the connection default
// stands.
func timeoutOverride(r *ResilienceOverride) *connect.ResiliencePolicy {
	if r == nil || r.TimeoutMS == nil {
		return nil
	}
	p := &connect.ResiliencePolicy{}
	p.Timeout = time.Duration(*r.TimeoutMS) * time.Millisecond
	return p
}

// conditionHandler projects the named ctx paths into a decision input, evaluates
// the JDM, picks TrueKey/FalseKey from the decision output, walks only the
// chosen child, and records the taken key in Directive.Branch for the trace.
type conditionHandler struct{}

// Exec implements NodeHandler.
func (conditionHandler) Exec(ctx context.Context, c *Ctx, n Node, dep Deps, w Walker) (Directive, error) {
	spec, err := parseSpec[ConditionSpec](n.Spec)
	if err != nil {
		return Directive{}, err
	}
	if dep.Decide == nil {
		return Directive{}, newErr(ClassInternal, "no decision evaluator wired")
	}

	in := projectInputs(c, spec.Input)
	out, err := dep.Decide.Evaluate(ctx, spec.JDMID, in)
	if err != nil {
		return Directive{}, classify(err)
	}

	key := chooseBranch(out, spec.TrueKey, spec.FalseKey, spec.BranchField)
	if key == "" {
		// A false branch with no FalseKey is a linear fall-through: nothing to walk.
		return Directive{Branch: ""}, nil
	}

	child := findChild(n.Children, key)
	if child == nil {
		return Directive{}, validationf("condition %q references unknown child %q", n.ID, key)
	}
	d, err := w.Walk(ctx, child, c, dep)
	if err != nil {
		return Directive{Branch: key}, err
	}
	// Preserve a downstream Stop while reporting the branch this node took.
	d.Branch = key
	return d, nil
}

// chooseBranch selects the branch key from a decision output. When branchField
// is set, it reads that specific field from the output. Otherwise it falls back
// to the generic "branch" or "result" convention: a string "branch" field naming
// trueKey/falseKey wins; otherwise a truthy "result" picks trueKey, a falsey one
// picks falseKey.
func chooseBranch(out map[string]any, trueKey, falseKey, branchField string) string {
	// When BranchField is explicitly set, read from that field. This removes the
	// demo-tuned heuristic that auto-promoted a sole string value.
	if branchField != "" {
		if b, ok := out[branchField].(string); ok {
			switch b {
			case trueKey:
				return trueKey
			case falseKey:
				return falseKey
			}
		}
		// BranchField was set but didn't match a branch key; treat as falsey.
		return falseKey
	}

	// Fallback: check for a "branch" field (backward compat with branchingEvaluator)
	if b, ok := out["branch"].(string); ok {
		switch b {
		case trueKey:
			return trueKey
		case falseKey:
			return falseKey
		}
	}
	if truthy(out["result"]) {
		return trueKey
	}
	return falseKey
}

// truthy reports the boolean sense of a decision value: a bool as-is, a non-zero
// number, or a non-empty string. Everything else (nil, empty) is false.
func truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case float64:
		return t != 0
	case int:
		return t != 0
	case string:
		return t != "" && t != "false"
	default:
		return v != nil
	}
}

// projectInputs reads each named path from the merged view into a flat input map
// keyed by the path's last segment. A missing path is simply omitted.
func projectInputs(c *Ctx, paths []string) map[string]any {
	in := make(map[string]any, len(paths))
	for _, p := range paths {
		if v, ok := c.GetPath(p); ok {
			in[lastSegment(p)] = v
		}
	}
	return in
}

// lastSegment returns the final dotted segment of a path, used as the input key.
func lastSegment(path string) string {
	segs := splitPath(path)
	if len(segs) == 0 {
		return path
	}
	return segs[len(segs)-1]
}

// findChild returns the child whose ID matches key, or nil.
func findChild(children []Node, key string) *Node {
	for i := range children {
		if children[i].ID == key {
			return &children[i]
		}
	}
	return nil
}

// setHandler resolves a literal or a merged-view path and mounts it into
// Response via SetPath (lodash.set, no sibling clobber). OmitEmpty skips the
// mount when the resolved value is nil/empty.
type setHandler struct{}

// Exec implements NodeHandler.
func (setHandler) Exec(ctx context.Context, c *Ctx, n Node, dep Deps, w Walker) (Directive, error) {
	spec, err := parseSpec[SetSpec](n.Spec)
	if err != nil {
		return Directive{}, err
	}
	if spec.TargetPath == "" {
		return Directive{}, validationf("set %q missing targetPath", n.ID)
	}
	if (spec.From == "") == (spec.Value == nil) {
		return Directive{}, validationf("set %q must set exactly one of value/from", n.ID)
	}

	if spec.From != "" {
		v, ok := c.GetPath(spec.From)
		if !ok {
			if !spec.OmitEmpty {
				return Directive{}, validationf("set %q source path %q not found", n.ID, spec.From)
			}
			// Missing + OmitEmpty: skip the mount, keep walking children.
			return walkChildren(ctx, n.Children, c, dep, w)
		}
		if !(spec.OmitEmpty && isEmpty(v)) {
			if err := c.SetPath(spec.TargetPath, v); err != nil {
				return Directive{}, classify(err)
			}
		}
		return walkChildren(ctx, n.Children, c, dep, w)
	}

	if !(spec.OmitEmpty && isEmpty(spec.Value)) {
		if err := c.SetPath(spec.TargetPath, spec.Value); err != nil {
			return Directive{}, classify(err)
		}
	}
	// A set is linear: after mounting, walk its children in order.
	return walkChildren(ctx, n.Children, c, dep, w)
}

// isEmpty reports whether v is nil, an empty string, an empty slice or an empty
// map — the values OmitEmpty suppresses.
func isEmpty(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case []any:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	default:
		return false
	}
}

// responseHandler finalizes the walk. It returns Directive{Stop:true} so every
// ancestor unwinds; the stitched Response is already accumulated in Ctx.
type responseHandler struct{}

// Exec implements NodeHandler.
func (responseHandler) Exec(ctx context.Context, c *Ctx, n Node, dep Deps, _ Walker) (Directive, error) {
	if _, err := parseSpec[ResponseSpec](n.Spec); err != nil {
		return Directive{}, err
	}
	return Directive{Stop: true}, nil
}

// deferredHandler stands in for a node type that is part of the frozen taxonomy
// but not implemented in the thin slice. It refuses at runtime with a validation
// error (never a panic) so the dispatch table matches the full taxonomy.
type deferredHandler struct{ name string }

// Exec implements NodeHandler.
func (h deferredHandler) Exec(ctx context.Context, c *Ctx, n Node, dep Deps, _ Walker) (Directive, error) {
	return Directive{}, validationf("node type %q not implemented in thin slice", h.name)
}
