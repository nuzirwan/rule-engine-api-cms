package flow

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
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

// filterHandler filters an array, keeping items where the ZEN predicate returns
// a truthy "match" field. The filtered array is stored under SaveAs in Ctx.Data.
// It is a leaf node (no children); its budget guard (maxItems) prevents runaway
// iteration (AC-17).
type filterHandler struct{}

// Exec implements NodeHandler.
func (filterHandler) Exec(ctx context.Context, c *Ctx, n Node, dep Deps, w Walker) (Directive, error) {
	spec, err := parseSpec[FilterSpec](n.Spec)
	if err != nil {
		return Directive{}, err
	}
	if spec.MaxItems <= 0 {
		return Directive{}, validationf("filter %q requires maxItems > 0", n.ID)
	}
	if spec.Over == "" {
		return Directive{}, validationf("filter %q requires over", n.ID)
	}
	if spec.JDMID == "" {
		return Directive{}, validationf("filter %q requires jdmId", n.ID)
	}
	if spec.SaveAs == "" {
		return Directive{}, validationf("filter %q requires saveAs", n.ID)
	}
	if dep.Decide == nil {
		return Directive{}, newErr(ClassInternal, "no decision evaluator wired")
	}

	raw, ok := c.GetPath(spec.Over)
	if !ok {
		return Directive{}, validationf("filter %q source path %q not found", n.ID, spec.Over)
	}
	items, ok := raw.([]any)
	if !ok {
		return Directive{}, validationf("filter %q source %q is not an array", n.ID, spec.Over)
	}
	if len(items) > spec.MaxItems {
		return Directive{}, validationf("filter %q over %d items exceeds maxItems %d", n.ID, len(items), spec.MaxItems)
	}

	var result []any
	for _, item := range items {
		if cerr := ctx.Err(); cerr != nil {
			return Directive{}, wrapErr(ClassTimeout, "filter cancelled", cerr)
		}

		in := projectItemInputs(item, spec.Input)
		out, err := dep.Decide.Evaluate(ctx, spec.JDMID, in)
		if err != nil {
			return Directive{}, classify(err)
		}

		if isTruthy(out["match"]) {
			result = append(result, item)
		}
	}

	if c.Data == nil {
		c.Data = map[string]any{}
	}
	c.Data[spec.SaveAs] = result
	return Directive{}, nil
}

// findHandler finds the first item in an array where the ZEN predicate returns
// a truthy "match" field. The found item (or nil) is stored under SaveAs in
// Ctx.Data. It is a leaf node (no children); its budget guard (maxItems) prevents
// runaway iteration (AC-17).
type findHandler struct{}

// Exec implements NodeHandler.
func (findHandler) Exec(ctx context.Context, c *Ctx, n Node, dep Deps, w Walker) (Directive, error) {
	spec, err := parseSpec[FindSpec](n.Spec)
	if err != nil {
		return Directive{}, err
	}
	if spec.MaxItems <= 0 {
		return Directive{}, validationf("find %q requires maxItems > 0", n.ID)
	}
	if spec.Over == "" {
		return Directive{}, validationf("find %q requires over", n.ID)
	}
	if spec.JDMID == "" {
		return Directive{}, validationf("find %q requires jdmId", n.ID)
	}
	if spec.SaveAs == "" {
		return Directive{}, validationf("find %q requires saveAs", n.ID)
	}
	if dep.Decide == nil {
		return Directive{}, newErr(ClassInternal, "no decision evaluator wired")
	}

	raw, ok := c.GetPath(spec.Over)
	if !ok {
		return Directive{}, validationf("find %q source path %q not found", n.ID, spec.Over)
	}
	items, ok := raw.([]any)
	if !ok {
		return Directive{}, validationf("find %q source %q is not an array", n.ID, spec.Over)
	}
	if len(items) > spec.MaxItems {
		return Directive{}, validationf("find %q over %d items exceeds maxItems %d", n.ID, len(items), spec.MaxItems)
	}

	var found any // nil if no match
	for _, item := range items {
		if cerr := ctx.Err(); cerr != nil {
			return Directive{}, wrapErr(ClassTimeout, "find cancelled", cerr)
		}

		in := projectItemInputs(item, spec.Input)
		out, err := dep.Decide.Evaluate(ctx, spec.JDMID, in)
		if err != nil {
			return Directive{}, classify(err)
		}

		if isTruthy(out["match"]) {
			found = item
			break // first match wins
		}
	}

	if c.Data == nil {
		c.Data = map[string]any{}
	}
	c.Data[spec.SaveAs] = found
	return Directive{}, nil
}

// projectItemInputs builds the ZEN input map from an array item. If the item is
// a map, only the specified input fields are projected; otherwise the whole item
// is passed as "item".
func projectItemInputs(item any, inputFields []string) map[string]any {
	m, isMap := item.(map[string]any)
	if !isMap || len(inputFields) == 0 {
		return map[string]any{"item": item}
	}
	out := make(map[string]any, len(inputFields))
	for _, k := range inputFields {
		if v, ok := m[k]; ok {
			out[k] = v
		}
	}
	return out
}

// isTruthy reports whether v is truthy: true, non-zero number, or non-empty string.
func isTruthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case int:
		return t != 0
	case int64:
		return t != 0
	case float64:
		return t != 0
	case string:
		return t != "" && t != "false" && t != "0"
	default:
		return v != nil
	}
}

// mapHandler transforms each item in an array via a ZEN decision. For each item,
// the ZEN output is collected into the result array (same length as input). The
// result is stored under SaveAs in Ctx.Data. It is a leaf node (no children);
// its budget guard (maxItems) prevents runaway iteration (AC-17).
type mapHandler struct{}

// Exec implements NodeHandler.
func (mapHandler) Exec(ctx context.Context, c *Ctx, n Node, dep Deps, w Walker) (Directive, error) {
	spec, err := parseSpec[MapSpec](n.Spec)
	if err != nil {
		return Directive{}, err
	}
	if spec.MaxItems <= 0 {
		return Directive{}, validationf("map %q requires maxItems > 0", n.ID)
	}
	if spec.Over == "" {
		return Directive{}, validationf("map %q requires over", n.ID)
	}
	if spec.JDMID == "" {
		return Directive{}, validationf("map %q requires jdmId", n.ID)
	}
	if spec.SaveAs == "" {
		return Directive{}, validationf("map %q requires saveAs", n.ID)
	}
	if dep.Decide == nil {
		return Directive{}, newErr(ClassInternal, "no decision evaluator wired")
	}

	raw, ok := c.GetPath(spec.Over)
	if !ok {
		return Directive{}, validationf("map %q source path %q not found", n.ID, spec.Over)
	}
	items, ok := raw.([]any)
	if !ok {
		return Directive{}, validationf("map %q source %q is not an array", n.ID, spec.Over)
	}
	if len(items) > spec.MaxItems {
		return Directive{}, validationf("map %q over %d items exceeds maxItems %d", n.ID, len(items), spec.MaxItems)
	}

	result := make([]any, len(items))
	for i, item := range items {
		if cerr := ctx.Err(); cerr != nil {
			return Directive{}, wrapErr(ClassTimeout, "map cancelled", cerr)
		}
		in := projectItemInputs(item, spec.Input)
		out, err := dep.Decide.Evaluate(ctx, spec.JDMID, in)
		if err != nil {
			return Directive{}, classify(err)
		}
		result[i] = out
	}

	if c.Data == nil {
		c.Data = map[string]any{}
	}
	c.Data[spec.SaveAs] = result
	return Directive{}, nil
}

// reduceHandler aggregates an array to a single value via a ZEN decision. Each
// iteration calls ZEN with {"accumulator": <acc>, "current": <item>, "index": <n>}
// and ZEN returns the new accumulator. The final accumulator is stored under
// SaveAs in Ctx.Data. It is a leaf node (no children); its budget guard (maxItems)
// prevents runaway iteration (AC-17).
type reduceHandler struct{}

// Exec implements NodeHandler.
func (reduceHandler) Exec(ctx context.Context, c *Ctx, n Node, dep Deps, w Walker) (Directive, error) {
	spec, err := parseSpec[ReduceSpec](n.Spec)
	if err != nil {
		return Directive{}, err
	}
	if spec.MaxItems <= 0 {
		return Directive{}, validationf("reduce %q requires maxItems > 0", n.ID)
	}
	if spec.Over == "" {
		return Directive{}, validationf("reduce %q requires over", n.ID)
	}
	if spec.JDMID == "" {
		return Directive{}, validationf("reduce %q requires jdmId", n.ID)
	}
	if spec.SaveAs == "" {
		return Directive{}, validationf("reduce %q requires saveAs", n.ID)
	}
	if dep.Decide == nil {
		return Directive{}, newErr(ClassInternal, "no decision evaluator wired")
	}

	raw, ok := c.GetPath(spec.Over)
	if !ok {
		return Directive{}, validationf("reduce %q source path %q not found", n.ID, spec.Over)
	}
	items, ok := raw.([]any)
	if !ok {
		return Directive{}, validationf("reduce %q source %q is not an array", n.ID, spec.Over)
	}
	if len(items) > spec.MaxItems {
		return Directive{}, validationf("reduce %q over %d items exceeds maxItems %d", n.ID, len(items), spec.MaxItems)
	}

	var acc any = spec.InitialValue
	for i, item := range items {
		if cerr := ctx.Err(); cerr != nil {
			return Directive{}, wrapErr(ClassTimeout, "reduce cancelled", cerr)
		}
		in := map[string]any{
			"accumulator": acc,
			"current":     projectItemInputs(item, spec.Input),
			"index":       float64(i),
		}
		out, err := dep.Decide.Evaluate(ctx, spec.JDMID, in)
		if err != nil {
			return Directive{}, classify(err)
		}
		acc = out
	}

	if c.Data == nil {
		c.Data = map[string]any{}
	}
	c.Data[spec.SaveAs] = acc
	return Directive{}, nil
}

// loadHandler loads JSON data from inline data, a local file, or a URL mid-flow.
// The loaded data (optionally extracted via JSONPath) is stored under SaveAs in
// Ctx.Data. It is a leaf node (no children). Exactly one of Data, Path, or URL
// must be provided.
type loadHandler struct{}

// loadHTTPTimeout is the timeout for HTTP requests to load data from URLs.
const loadHTTPTimeout = 30 * time.Second

// sharedLoadHTTPClient is a shared HTTP client for load node URL fetches. It is
// initialized once and reused across all load operations to benefit from
// connection pooling.
var (
	sharedLoadHTTPClientOnce sync.Once
	sharedLoadHTTPClient     *http.Client
)

// getLoadHTTPClient returns the shared HTTP client, initializing it on first use.
func getLoadHTTPClient() *http.Client {
	sharedLoadHTTPClientOnce.Do(func() {
		sharedLoadHTTPClient = &http.Client{
			Timeout: loadHTTPTimeout,
			Transport: &http.Transport{
				MaxIdleConns:        20,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     90 * time.Second,
			},
		}
	})
	return sharedLoadHTTPClient
}

// Exec implements NodeHandler.
func (loadHandler) Exec(ctx context.Context, c *Ctx, n Node, dep Deps, w Walker) (Directive, error) {
	spec, err := parseSpec[LoadSpec](n.Spec)
	if err != nil {
		return Directive{}, err
	}
	if spec.SaveAs == "" {
		return Directive{}, validationf("load %q requires saveAs", n.ID)
	}

	// Validate exactly one source is provided
	sources := 0
	if spec.Data != nil {
		sources++
	}
	if spec.Path != "" {
		sources++
	}
	if spec.URL != "" {
		sources++
	}
	if sources == 0 {
		return Directive{}, validationf("load %q requires one of data, path, or url", n.ID)
	}
	if sources > 1 {
		return Directive{}, validationf("load %q requires exactly one of data, path, or url", n.ID)
	}

	var data any

	switch {
	case spec.Data != nil:
		// Inline data: use directly (already parsed from JSON spec)
		data = spec.Data
		// Apply JSONPath if specified
		if spec.JSONPath != "" {
			data, err = applyLoadJSONPath(spec.Data, spec.JSONPath)
			if err != nil {
				return Directive{}, wrapErr(ClassValidation, "load jsonPath", err)
			}
		}

	case spec.Path != "":
		// Local file: read and parse
		data, err = loadFromFile(ctx, spec.Path, spec.JSONPath)
		if err != nil {
			return Directive{}, err
		}

	case spec.URL != "":
		// URL: fetch and parse
		data, err = loadFromURL(ctx, spec.URL, spec.JSONPath)
		if err != nil {
			return Directive{}, err
		}
	}

	if c.Data == nil {
		c.Data = map[string]any{}
	}
	c.Data[spec.SaveAs] = data
	return Directive{}, nil
}

// loadFromFile reads and parses a JSON file, optionally applying a JSONPath.
func loadFromFile(ctx context.Context, path, jsonPath string) (any, error) {
	// Check context deadline
	if err := ctx.Err(); err != nil {
		return nil, wrapErr(ClassTimeout, "load file cancelled", err)
	}

	// Read the file
	fileData, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, wrapErr(ClassValidation, "load file not found: "+path, err)
		}
		if os.IsPermission(err) {
			return nil, wrapErr(ClassValidation, "load file permission denied: "+path, err)
		}
		return nil, wrapErr(ClassUpstream, "load file read error", err)
	}

	// Validate JSON
	if !json.Valid(fileData) {
		return nil, newErr(ClassValidation, "load file contains invalid JSON: "+path)
	}

	return applyLoadJSONPathBytes(fileData, jsonPath)
}

// loadFromURL fetches and parses JSON from a URL, optionally applying a JSONPath.
func loadFromURL(ctx context.Context, url, jsonPath string) (any, error) {
	// Validate URL scheme
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return nil, newErr(ClassValidation, "load url must be http or https: "+url)
	}

	// Create request with context
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, wrapErr(ClassValidation, "load url invalid", err)
	}
	req.Header.Set("Accept", "application/json")

	// Execute request
	client := getLoadHTTPClient()
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, wrapErr(ClassTimeout, "load url request cancelled", ctx.Err())
		}
		return nil, wrapErr(ClassUpstream, "load url request failed", err)
	}
	defer resp.Body.Close()

	// Check status
	if resp.StatusCode == http.StatusNotFound {
		return nil, newErr(ClassValidation, "load url not found: "+url)
	}
	if resp.StatusCode >= 400 {
		return nil, newErr(ClassUpstream, "load url http error: "+resp.Status)
	}

	// Read body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, wrapErr(ClassUpstream, "load url read body failed", err)
	}

	// Validate JSON
	if !json.Valid(body) {
		return nil, newErr(ClassValidation, "load url response is not valid JSON")
	}

	return applyLoadJSONPathBytes(body, jsonPath)
}

// applyLoadJSONPathBytes extracts data using a JSONPath from raw bytes, or parses
// the full document if jsonPath is empty.
func applyLoadJSONPathBytes(data []byte, jsonPath string) (any, error) {
	if jsonPath == "" {
		var v any
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, wrapErr(ClassValidation, "load json parse", err)
		}
		return v, nil
	}

	// Normalize JSONPath: strip leading $. or $
	query := normalizeLoadJSONPath(jsonPath)
	result := gjson.GetBytes(data, query)
	if !result.Exists() {
		// Path not found returns nil (not an error)
		return nil, nil
	}
	return result.Value(), nil
}

// applyLoadJSONPath extracts data using a JSONPath from an already-parsed value.
// For inline data, we need to re-marshal to bytes to use gjson.
func applyLoadJSONPath(data any, jsonPath string) (any, error) {
	if jsonPath == "" {
		return data, nil
	}

	// Marshal to JSON bytes for gjson processing
	bytes, err := json.Marshal(data)
	if err != nil {
		return nil, wrapErr(ClassValidation, "load jsonPath marshal", err)
	}

	return applyLoadJSONPathBytes(bytes, jsonPath)
}

// normalizeLoadJSONPath converts JSONPath syntax to GJSON syntax.
// Strips leading $. or $ prefix.
func normalizeLoadJSONPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || path == "$" {
		return ""
	}
	// Strip leading $.
	if strings.HasPrefix(path, "$.") {
		return path[2:]
	}
	// Strip leading $ (bare root reference)
	if strings.HasPrefix(path, "$") {
		return path[1:]
	}
	return path
}
