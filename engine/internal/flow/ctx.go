package flow

import (
	"strconv"
	"strings"
)

// Ctx is the per-request accumulator threaded through the whole flow walk. It is
// the contract type from lld-contracts.md. It is NOT safe for concurrent writes
// except within a Parallel node's merge step.
type Ctx struct {
	RequestID string
	TraceID   string
	Env       string
	Input     map[string]any // request body/params/headers (read-only after trigger)
	Data      map[string]any // gathered source data, keyed by node output
	Response  map[string]any // final response; written only via SetPath
}

// NewCtx builds a Ctx for a request. A nil input is treated as empty. Data and
// Response start empty and are filled during the walk.
func NewCtx(reqID, traceID, env string, input map[string]any) *Ctx {
	if input == nil {
		input = map[string]any{}
	}
	return &Ctx{
		RequestID: reqID,
		TraceID:   traceID,
		Env:       env,
		Input:     input,
		Data:      map[string]any{},
		Response:  map[string]any{},
	}
}

// cloneWritable returns a shallow clone of c sharing the read-only Input and
// Data but with a fresh Response map, for use by parallel/forEach children that
// need a scratch writable view. After the child finishes, its Response is merged
// back into the shared Ctx by the parent handler under a lock.
func (c *Ctx) cloneWritable() *Ctx {
	return &Ctx{
		RequestID: c.RequestID,
		TraceID:   c.TraceID,
		Env:       c.Env,
		Input:     c.Input,
		Data:      c.Data,
		Response:  map[string]any{},
	}
}

// GetPath reads a dotted path from the merged view with precedence
// Response -> Data -> Input (most-derived wins). Dotted segments descend
// map[string]any; numeric segments index []any. It returns (value, true) on the
// first root that resolves the whole path, else (nil, false).
func (c *Ctx) GetPath(path string) (any, bool) {
	segs := splitPath(path)
	if len(segs) == 0 {
		return nil, false
	}
	for _, root := range []map[string]any{c.Response, c.Data, c.Input} {
		if root == nil {
			continue
		}
		if v, ok := dig(root, segs); ok {
			return v, true
		}
	}
	return nil, false
}

// dig descends segs through node, following map keys for string segments and
// slice indices for numeric segments. It returns false if any segment misses or
// descends into a scalar.
func dig(node any, segs []string) (any, bool) {
	cur := node
	for _, seg := range segs {
		switch n := cur.(type) {
		case map[string]any:
			v, ok := n[seg]
			if !ok {
				return nil, false
			}
			cur = v
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(n) {
				return nil, false
			}
			cur = n[i]
		default:
			return nil, false
		}
	}
	return cur, true
}

// SetPath mounts v at a dotted targetPath into Response with lodash.set
// semantics:
//   - dotted paths "a.b.c" and numeric indices "items.0.id";
//   - missing intermediate containers are created (a map for a string child, a
//     slice grown for a numeric child);
//   - only the LEAF segment is assigned, so existing sibling keys/elements are
//     never clobbered;
//   - a type conflict (an intermediate exists but is a scalar, or the wrong
//     container kind for the next segment) returns a ClassValidation error
//     rather than silently overwriting.
func (c *Ctx) SetPath(targetPath string, v any) error {
	segs := splitPath(targetPath)
	if len(segs) == 0 {
		return validationf("empty targetPath")
	}
	if c.Response == nil {
		c.Response = map[string]any{}
	}

	// The root is always the Response map; the first segment must therefore be a
	// string key, not an index.
	if isIndex(segs[0]) {
		return validationf("path conflict at %q: response root is an object", segs[0])
	}

	// Descend with an explicit "current container" plus a setBack closure that
	// writes a (possibly reallocated) slice back into its parent so growth is
	// wired into the tree. For the root map, setBack is a no-op.
	var current any = c.Response
	setBack := func(any) {}

	for i := 0; i < len(segs)-1; i++ {
		seg := segs[i]
		next := segs[i+1]
		childIsIndex := isIndex(next)

		switch cont := current.(type) {
		case map[string]any:
			if isIndex(seg) {
				return validationf("path conflict at %q: expected object key", seg)
			}
			child, ok := cont[seg]
			if !ok || child == nil {
				child = newContainer(childIsIndex)
				cont[seg] = child
			} else if !isContainer(child) {
				return validationf("path conflict at %q", seg)
			} else if childIsIndex != isSlice(child) {
				return validationf("path conflict at %q", seg)
			}
			current = child
			key := seg
			parent := cont
			setBack = func(v any) { parent[key] = v }

		case []any:
			idx, err := strconv.Atoi(seg)
			if err != nil || idx < 0 {
				return validationf("invalid index %q", seg)
			}
			grown, child, err := ensureIndex(cont, idx, childIsIndex)
			if err != nil {
				return err
			}
			// Writing a grown slice back into its parent keeps the tree wired.
			setBack(grown)
			current = child
			sl := grown
			index := idx
			setBack = func(v any) { sl[index] = v }

		default:
			return validationf("path conflict at %q", seg)
		}
	}

	// Assign only the leaf.
	leaf := segs[len(segs)-1]
	switch cont := current.(type) {
	case map[string]any:
		if isIndex(leaf) {
			return validationf("path conflict at %q: expected object key", leaf)
		}
		cont[leaf] = v
	case []any:
		idx, err := strconv.Atoi(leaf)
		if err != nil || idx < 0 {
			return validationf("invalid index %q", leaf)
		}
		grown := growSlice(cont, idx+1)
		grown[idx] = v
		setBack(grown)
	default:
		return validationf("path conflict at %q", leaf)
	}
	return nil
}

// ensureIndex guarantees that index i exists in slice sl (growing it if needed)
// and that the element at i is a container of the kind the next segment needs,
// creating one when the slot is empty. It returns the (possibly reallocated)
// slice and the child container, or a validation error on a type conflict.
func ensureIndex(sl []any, i int, childIsIndex bool) (grown []any, child any, err error) {
	grown = growSlice(sl, i+1)
	cur := grown[i]
	if cur == nil {
		cur = newContainer(childIsIndex)
		grown[i] = cur
	} else if !isContainer(cur) {
		return nil, nil, validationf("path conflict at index %d", i)
	} else if childIsIndex != isSlice(cur) {
		return nil, nil, validationf("path conflict at index %d", i)
	}
	return grown, cur, nil
}

// growSlice returns sl extended to at least n elements, preserving existing
// elements. It reuses sl when it already has capacity/length.
func growSlice(sl []any, n int) []any {
	if len(sl) >= n {
		return sl
	}
	out := make([]any, n)
	copy(out, sl)
	return out
}

// newContainer returns an empty slice when the next segment is an index, else an
// empty map.
func newContainer(childIsIndex bool) any {
	if childIsIndex {
		return []any{}
	}
	return map[string]any{}
}

// isContainer reports whether v is a map[string]any or []any.
func isContainer(v any) bool {
	switch v.(type) {
	case map[string]any, []any:
		return true
	default:
		return false
	}
}

// isSlice reports whether v is a []any.
func isSlice(v any) bool {
	_, ok := v.([]any)
	return ok
}

// isIndex reports whether seg is a non-negative integer index segment.
func isIndex(seg string) bool {
	if seg == "" {
		return false
	}
	for _, r := range seg {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// splitPath splits a dotted path into non-empty segments. An empty or
// all-separator path yields no segments.
func splitPath(path string) []string {
	if path == "" {
		return nil
	}
	raw := strings.Split(path, ".")
	out := raw[:0]
	for _, s := range raw {
		if s == "" {
			continue
		}
		out = append(out, s)
	}
	return out
}
