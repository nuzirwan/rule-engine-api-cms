package flow

import (
	"context"
	"sync"
)

// switchHandler is the N-way branch. It projects the named ctx paths into a
// decision input, evaluates the JDM, looks up the returned "branch" value in
// Cases, falls back to Default, and walks the one chosen child via the Walker.
// It records the taken key in Directive.Branch for the trace. A branch with no
// matching case and no Default is a linear fall-through (nothing to walk).
type switchHandler struct{}

// Exec implements NodeHandler.
func (switchHandler) Exec(ctx context.Context, c *Ctx, n Node, dep Deps, w Walker) (Directive, error) {
	spec, err := parseSpec[SwitchSpec](n.Spec)
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

	key := switchBranch(out, spec)
	if key == "" {
		// No case matched and no Default: linear fall-through, nothing to walk.
		return Directive{Branch: ""}, nil
	}

	child := findChild(n.Children, key)
	if child == nil {
		return Directive{}, validationf("switch %q selected unknown child %q", n.ID, key)
	}
	d, err := w.Walk(ctx, child, c, dep)
	if err != nil {
		return Directive{Branch: key}, err
	}
	d.Branch = key
	return d, nil
}

// switchBranch resolves the chosen child ID: the decision output's string
// "branch" field is looked up in Cases; if it is absent or unmatched the Default
// child ID is used (which may be "").
func switchBranch(out map[string]any, spec SwitchSpec) string {
	if b, ok := out["branch"].(string); ok {
		if childID, ok := spec.Cases[b]; ok {
			return childID
		}
	}
	return spec.Default
}

// decisionHandler computes a value through a ZEN decision and stores the whole
// output map under SaveAs in Ctx.Data. It never branches and has no children to
// walk (a leaf); its children, if any, are rejected by ValidateTree.
type decisionHandler struct{}

// Exec implements NodeHandler.
func (decisionHandler) Exec(ctx context.Context, c *Ctx, n Node, dep Deps, w Walker) (Directive, error) {
	spec, err := parseSpec[DecisionSpec](n.Spec)
	if err != nil {
		return Directive{}, err
	}
	if dep.Decide == nil {
		return Directive{}, newErr(ClassInternal, "no decision evaluator wired")
	}
	if spec.SaveAs == "" {
		return Directive{}, validationf("decision %q missing saveAs", n.ID)
	}

	in := projectInputs(c, spec.Input)
	out, err := dep.Decide.Evaluate(ctx, spec.JDMID, in)
	if err != nil {
		return Directive{}, classify(err)
	}

	if c.Data == nil {
		c.Data = map[string]any{}
	}
	c.Data[spec.SaveAs] = out
	// A decision is a leaf: nothing to walk after storing the output.
	return Directive{}, nil
}

// loggerHandler emits a structured log record through Deps.Log, capturing the
// named ctx paths (redaction is honored by the observ sink), at the configured
// level, subject to SampleRate. It never alters the response and never stops the
// walk — a flow runs identically with every Logger node removed.
type loggerHandler struct{}

// Exec implements NodeHandler.
func (loggerHandler) Exec(ctx context.Context, c *Ctx, n Node, dep Deps, w Walker) (Directive, error) {
	spec, err := parseSpec[LoggerSpec](n.Spec)
	if err != nil {
		return Directive{}, err
	}
	if dep.Log == nil || !shouldSample(spec.SampleRate, n.ID) {
		return Directive{}, nil
	}

	fields := make(map[string]any, len(spec.Capture)+1)
	if spec.Label != "" {
		fields["label"] = spec.Label
	}
	for _, p := range spec.Capture {
		if v, ok := c.GetPath(p); ok {
			fields[p] = v
		}
	}
	level := spec.Level
	if level == "" {
		level = "info"
	}
	dep.Log.Emit(ctx, level, "flow.logger", fields)
	return Directive{}, nil
}

// shouldSample reports whether a Logger node should emit given its SampleRate.
// A nil rate means always (1.0); a rate >= 1 always emits; a rate <= 0 never
// emits. For a fractional rate the decision is deterministic per node ID (a
// stable hash) so a given node either logs for the whole request-stream slice
// or not, avoiding a dependency on a RNG seam the thin slice does not have.
func shouldSample(rate *float64, nodeID string) bool {
	if rate == nil || *rate >= 1 {
		return true
	}
	if *rate <= 0 {
		return false
	}
	// Deterministic FNV-1a hash of the node ID folded into [0,1).
	var h uint32 = 2166136261
	for i := 0; i < len(nodeID); i++ {
		h ^= uint32(nodeID[i])
		h *= 16777619
	}
	frac := float64(h%1000) / 1000.0
	return frac < *rate
}

// parallelHandler fans the children out concurrently, each child writing into
// its own cloned writable view, then merges the disjoint Response paths back
// into the shared Ctx under one lock (the only concurrent-write point per the
// Ctx contract). Concurrency is bounded by MaxConcurrency (0 => len(children)).
// FailFast cancels the remaining siblings on the first error. ValidateTree
// guarantees sibling writes are disjoint so the merge is order-independent and
// clobber-free; the lock still serializes the SetPath calls defensively.
type parallelHandler struct{}

// Exec implements NodeHandler.
func (parallelHandler) Exec(ctx context.Context, c *Ctx, n Node, dep Deps, w Walker) (Directive, error) {
	spec, err := parseSpec[ParallelSpec](n.Spec)
	if err != nil {
		return Directive{}, err
	}
	if len(n.Children) == 0 {
		return Directive{}, validationf("parallel %q must have at least one child", n.ID)
	}

	limit := spec.MaxConcurrency
	if limit <= 0 || limit > len(n.Children) {
		limit = len(n.Children)
	}

	gctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		mu       sync.Mutex // guards the shared Ctx merge + firstErr
		firstErr error
		stop     bool
		wg       sync.WaitGroup
		sem      = make(chan struct{}, limit)
	)

	for i := range n.Children {
		// A child may walk a Response node; capture whether any did so Stop can
		// propagate upward after the join.
		child := n.Children[i]
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			if gctx.Err() != nil {
				return
			}
			// Each child writes into a scratch view: a shallow clone sharing the
			// read-only Input/Data but with a fresh Response map, so concurrent
			// SetPath calls never race on the shared Response.
			scratch := c.cloneWritable()
			d, cerr := w.Walk(gctx, &child, scratch, dep)

			mu.Lock()
			defer mu.Unlock()
			if cerr != nil {
				if firstErr == nil {
					firstErr = cerr
					if spec.FailFast {
						cancel() // cancel the remaining siblings
					}
				}
				return
			}
			// Merge the child's disjoint Response writes into the shared Ctx.
			if merr := mergeResponse(c, scratch.Response); merr != nil && firstErr == nil {
				firstErr = merr
			}
			if d.Stop {
				stop = true
			}
		}()
	}
	wg.Wait()

	if firstErr != nil {
		return Directive{}, classify(firstErr)
	}
	return Directive{Stop: stop}, nil
}

// mergeResponse applies each leaf of a child's scratch Response into the shared
// Ctx via SetPath, so no-clobber + type-conflict semantics are preserved and the
// shared tree stays wired. Validation guarantees sibling paths are disjoint, so
// the merge order does not matter.
func mergeResponse(dst *Ctx, src map[string]any) error {
	for path, v := range flattenLeaves(src, "") {
		if err := dst.SetPath(path, v); err != nil {
			return err
		}
	}
	return nil
}

// flattenLeaves walks a nested map/slice Response into a flat map of dotted path
// -> leaf value, so each leaf can be re-applied via SetPath on the shared Ctx.
// Empty containers are preserved as leaves so an explicitly-set empty object is
// not lost.
func flattenLeaves(node any, prefix string) map[string]any {
	out := map[string]any{}
	switch n := node.(type) {
	case map[string]any:
		if len(n) == 0 && prefix != "" {
			out[prefix] = n
			return out
		}
		for k, v := range n {
			key := k
			if prefix != "" {
				key = prefix + "." + k
			}
			for p, lv := range flattenLeaves(v, key) {
				out[p] = lv
			}
		}
	case []any:
		if len(n) == 0 && prefix != "" {
			out[prefix] = n
			return out
		}
		for i, v := range n {
			key := itoa(i)
			if prefix != "" {
				key = prefix + "." + key
			}
			for p, lv := range flattenLeaves(v, key) {
				out[p] = lv
			}
		}
	default:
		if prefix != "" {
			out[prefix] = node
		}
	}
	return out
}

// forEachHandler iterates the array at Over, binds each item under As (and the
// index under IndexAs) into a per-iteration writable view, and walks the body
// subtree once per item via the Walker. It enforces MaxItems (per-node cap) plus
// the per-request work budget and ctx deadline (AC-17): either guard tripping
// terminates with a clear classified error, never a hang. Response writes from
// each iteration merge back into the shared Ctx.
type forEachHandler struct{}

// Exec implements NodeHandler.
func (forEachHandler) Exec(ctx context.Context, c *Ctx, n Node, dep Deps, w Walker) (Directive, error) {
	spec, err := parseSpec[ForEachSpec](n.Spec)
	if err != nil {
		return Directive{}, err
	}
	if spec.MaxItems <= 0 {
		return Directive{}, validationf("forEach %q requires maxItems > 0", n.ID)
	}
	if spec.Over == "" || spec.As == "" {
		return Directive{}, validationf("forEach %q requires over and as", n.ID)
	}
	if len(n.Children) == 0 {
		return Directive{}, validationf("forEach %q must have a body child", n.ID)
	}

	raw, ok := c.GetPath(spec.Over)
	if !ok {
		return Directive{}, validationf("forEach %q source path %q not found", n.ID, spec.Over)
	}
	items, ok := raw.([]any)
	if !ok {
		return Directive{}, validationf("forEach %q source %q is not an array", n.ID, spec.Over)
	}
	if len(items) > spec.MaxItems {
		return Directive{}, validationf("forEach %q over %d items exceeds maxItems %d", n.ID, len(items), spec.MaxItems)
	}

	b := budgetFrom(ctx)
	stop := false
	for i := range items {
		// Time-budget / cancellation: terminate with a classified timeout rather
		// than hang (AC-17).
		if cerr := ctx.Err(); cerr != nil {
			return Directive{}, wrapErr(ClassTimeout, "forEach cancelled", cerr)
		}
		// Work-budget: each iteration costs one unit on top of the node walks it
		// spawns, so a shallow-but-iterative tree is still bounded (AC-17).
		if b != nil {
			if werr := b.chargeWork(1); werr != nil {
				return Directive{}, werr
			}
		}

		iter := c.cloneWritable()
		if iter.Input == nil {
			iter.Input = map[string]any{}
		}
		// Bind the item (and optional index) into a per-iteration Input overlay so
		// the body can read them via GetPath without mutating the shared Input.
		iter.Input = overlayInput(c.Input, spec.As, items[i], spec.IndexAs, i)

		for j := range n.Children {
			body := n.Children[j]
			d, derr := w.Walk(ctx, &body, iter, dep)
			if derr != nil {
				return Directive{}, derr
			}
			if d.Stop {
				stop = true
				break
			}
		}

		if merr := mergeResponse(c, iter.Response); merr != nil {
			return Directive{}, merr
		}
		if stop {
			break
		}
	}
	return Directive{Stop: stop}, nil
}

// overlayInput returns a shallow copy of base with the item alias (and optional
// index alias) added, leaving the shared Input untouched.
func overlayInput(base map[string]any, as string, item any, indexAs string, idx int) map[string]any {
	out := make(map[string]any, len(base)+2)
	for k, v := range base {
		out[k] = v
	}
	out[as] = item
	if indexAs != "" {
		out[indexAs] = idx
	}
	return out
}

// itoa is a tiny non-allocating-ish int->string for slice index path segments.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
