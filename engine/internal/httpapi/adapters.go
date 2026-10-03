package httpapi

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"nzr-rules-engine/internal/connect"
	"nzr-rules-engine/internal/decision"
	"nzr-rules-engine/internal/flow"
)

// This file holds the two per-request EDGE adapters that bridge the declarative
// seed to the frozen flow seams without touching the pure flow core:
//
//   - templatingRegistry wraps connect.Registry so an Operation payload's
//     "{{path}}" placeholders are resolved against the live request Ctx just
//     before Execute. The seed carries static templates (params ["{{input.id}}"],
//     bodies referencing "{{order}}") that only the request + accumulated Ctx can
//     fill; flow passes the Operation through verbatim, so resolution belongs at
//     this edge.
//   - branchingEvaluator wraps decision.Evaluator so a JDM output that names a
//     single branch value (the order JDM returns {"shipping":"expedited|standard"})
//     is promoted to the "branch" field flow.conditionHandler selects on. The
//     seed's condition keys (trueKey/falseKey) already match the JDM's output
//     values, so the bridge is a mechanical promotion, not a product decision.
//
// Both close over per-request state and are built fresh in the handler.

// templatePattern matches a "{{ dotted.path }}" placeholder, capturing the path.
var templatePattern = regexp.MustCompile(`\{\{\s*([^}]+?)\s*\}\}`)

// templatingRegistry decorates a connect.Registry so every Client it returns
// resolves templates in the Operation against ctxView before delegating.
type templatingRegistry struct {
	inner connect.Registry
	ctx   *flow.Ctx
}

// compile-time assertion that templatingRegistry satisfies connect.Registry.
var _ connect.Registry = (*templatingRegistry)(nil)

// newTemplatingRegistry wraps inner so Operations resolve against c.
func newTemplatingRegistry(inner connect.Registry, c *flow.Ctx) connect.Registry {
	return &templatingRegistry{inner: inner, ctx: c}
}

// Client returns a template-resolving client over the inner pooled client.
func (r *templatingRegistry) Client(ctx context.Context, key string) (connect.Client, error) {
	inner, err := r.inner.Client(ctx, key)
	if err != nil {
		return nil, err
	}
	return &templatingClient{inner: inner, ctx: r.ctx}, nil
}

// Reload delegates to the inner registry (no per-request behavior).
func (r *templatingRegistry) Reload(ctx context.Context, defs []connect.ConnectionDef) error {
	return r.inner.Reload(ctx, defs)
}

// HealthCheck delegates to the inner registry.
func (r *templatingRegistry) HealthCheck(ctx context.Context) error {
	return r.inner.HealthCheck(ctx)
}

// templatingClient resolves templates in an Operation's payload against the
// request Ctx, then delegates Execute to the inner pooled client.
type templatingClient struct {
	inner connect.Client
	ctx   *flow.Ctx
}

// Execute resolves "{{path}}" placeholders in op.Payload against the merged Ctx
// view, binds each positional SQL param to its DECLARED type (config-driven
// typing — see flow.ParseParam/ConvertParam), runs the inner client, then
// normalizes the result so the pure flow core's GetPath can read it. A
// placeholder whose path does not resolve is left as-is so the driver can reject
// it with a classified error rather than silently sending an empty value. A
// typed-param conversion failure (e.g. "as":"int" over a non-integer value) is a
// classified Validation error surfaced before the query runs.
func (c *templatingClient) Execute(ctx context.Context, op connect.Operation) (any, error) {
	payload, err := c.resolvePayload(op.Payload)
	if err != nil {
		return nil, err
	}
	op.Payload = payload
	res, err := c.inner.Execute(ctx, op)
	if err != nil {
		return res, err
	}
	return normalizeResult(res), nil
}

// resolvePayload resolves every template in the payload and, for a SQL op, binds
// the positional "params" array to the declared types. The non-params parts
// (sql, body, key, ...) resolve as before. The params array is handled specially
// so a typed-param object {"value","as"} is converted to its Go type instead of
// being left as a map the driver cannot bind.
func (c *templatingClient) resolvePayload(payload map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(payload))
	for k, v := range payload {
		if k == "params" {
			params, err := c.resolveParams(v)
			if err != nil {
				return nil, err
			}
			out[k] = params
			continue
		}
		out[k] = resolveTemplates(v, c.ctx)
	}
	return out, nil
}

// resolveParams binds each element of the positional params array: a bare string
// resolves as a template and binds as TEXT (its natural type); a typed-param
// object {"value","as"} resolves then converts to the declared Go type via
// flow.ConvertParam. The declared "as" — not the value's shape — decides the
// bind type, so a digit-only identifier in a text column is never silently
// turned into a number. A non-list params is passed through for the driver to
// reject with its own Validation error.
func (c *templatingClient) resolveParams(raw any) (any, error) {
	list, ok := raw.([]any)
	if !ok {
		return raw, nil
	}
	out := make([]any, len(list))
	for i, elem := range list {
		tmpl, as, typed, wellFormed := flow.ParseParam(elem)
		if !wellFormed {
			// Should have been caught by ValidateTree; refuse at runtime too.
			return nil, flow.MalformedParamError(posLabel(i))
		}
		resolved := resolveTemplateString(tmpl, c.ctx)
		if !typed {
			// Bare string: bind as text in whatever native form it resolved to.
			out[i] = resolved
			continue
		}
		v, err := flow.ConvertParam(posLabel(i), stringify(resolved), as)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

// posLabel renders a 0-based param index as its positional SQL label ($1..).
func posLabel(i int) string { return "$" + stringify(i+1) }

// normalizeResult bridges the connect driver output shapes to the shapes
// flow.Ctx.GetPath descends (map[string]any / []any). The postgres driver
// returns rows as []map[string]any; a GET-one-resource read (orders/{id}) yields
// a single row, so a one-row result is unwrapped to that row's map so a
// condition can project "order.amount". A multi-row result is converted to a
// []any of maps so index paths ("rows.0.x") still resolve. Any other result
// (e.g. the rest driver's {status,headers,body}) is returned unchanged.
func normalizeResult(res any) any {
	rows, ok := res.([]map[string]any)
	if !ok {
		return res
	}
	switch len(rows) {
	case 0:
		return map[string]any{}
	case 1:
		return rows[0]
	default:
		out := make([]any, len(rows))
		for i, r := range rows {
			out[i] = r
		}
		return out
	}
}

// Close delegates to the inner client. The registry owns the real pool
// lifecycle; this per-request wrapper must not close the shared client.
func (c *templatingClient) Close() error { return nil }

// resolveTemplates walks an arbitrary JSON-shaped value and resolves any string
// template it finds against the Ctx merged view. Maps and slices are rebuilt so
// the caller's stored payload is never mutated.
func resolveTemplates(v any, c *flow.Ctx) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = resolveTemplates(val, c)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = resolveTemplates(val, c)
		}
		return out
	case string:
		return resolveTemplateString(t, c)
	default:
		return v
	}
}

// resolveTemplateString resolves a template string. When the whole string is a
// single "{{path}}" the resolved value is returned with its native type (so an
// integer id stays an int for a positional SQL param). A string containing a
// template among other text yields a string with the match substituted.
func resolveTemplateString(s string, c *flow.Ctx) any {
	m := templatePattern.FindStringSubmatch(s)
	if m == nil {
		return s
	}
	// Whole-string single placeholder: preserve the resolved value's type.
	if m[0] == strings.TrimSpace(s) && strings.Count(s, "{{") == 1 {
		if val, ok := lookupPath(c, m[1]); ok {
			return val
		}
		return s
	}
	// Embedded placeholder(s): string substitution, missing paths left intact.
	return templatePattern.ReplaceAllStringFunc(s, func(match string) string {
		sub := templatePattern.FindStringSubmatch(match)
		if val, ok := lookupPath(c, sub[1]); ok {
			return stringify(val)
		}
		return match
	})
}

// lookupPath resolves a template path against the Ctx. The seed addresses the
// request body/params under an explicit "input." namespace, but Ctx.Input is
// merged at the root of GetPath (precedence Response>Data>Input), so an
// "input." prefix is stripped before lookup. Other namespaces ("order.",
// "decision.") are keys produced under Ctx.Data and resolve directly.
func lookupPath(c *flow.Ctx, path string) (any, bool) {
	if rest, ok := strings.CutPrefix(path, "input."); ok {
		return c.GetPath(rest)
	}
	return c.GetPath(path)
}

// stringify renders a resolved value for embedding in a larger string.
func stringify(v any) string {
	switch t := v.(type) {
	case string:
		return t
	default:
		return fmt.Sprintf("%v", t)
	}
}

// branchingEvaluator decorates a decision.Evaluator so a JDM output carrying a
// single branch value is promoted to the "branch" field the condition handler
// selects on. If the output already has a "branch" or a "result" field, it is
// left untouched (the handler already understands those).
type branchingEvaluator struct {
	inner decision.Evaluator
}

// compile-time assertion that branchingEvaluator satisfies decision.Evaluator.
var _ decision.Evaluator = (*branchingEvaluator)(nil)

// newBranchingEvaluator wraps inner with the branch-promotion bridge.
func newBranchingEvaluator(inner decision.Evaluator) decision.Evaluator {
	return &branchingEvaluator{inner: inner}
}

// Evaluate runs the inner evaluator, then promotes a sole string output value to
// "branch" when the output names no branch/result itself.
func (e *branchingEvaluator) Evaluate(ctx context.Context, jdmID string, input map[string]any) (map[string]any, error) {
	out, err := e.inner.Evaluate(ctx, jdmID, input)
	if err != nil {
		return nil, err
	}
	if out == nil {
		return out, nil
	}
	if _, ok := out["branch"]; ok {
		return out, nil
	}
	if _, ok := out["result"]; ok {
		return out, nil
	}
	if b, ok := soleStringValue(out); ok {
		out["branch"] = b
	}
	return out, nil
}

// soleStringValue returns the single string value of a one-entry map. The order
// JDM emits exactly {"shipping": "<branch>"}; this reads that value as the branch
// key regardless of the output field name.
func soleStringValue(m map[string]any) (string, bool) {
	if len(m) != 1 {
		return "", false
	}
	for _, v := range m {
		if s, ok := v.(string); ok {
			return s, true
		}
	}
	return "", false
}
