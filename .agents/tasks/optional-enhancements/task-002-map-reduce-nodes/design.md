# Design: Map/Reduce Node Types (TASK-002)

## Overview

This design adds `map` and `reduce` node types to complete the flow engine's collection operations. Both are leaf nodes (no children) that iterate an array from a context path, invoke a ZEN decision per element, and store the result. The implementation follows the existing `filter`/`find` pattern exactly: same spec structure, same validation rules, same budget enforcement.

The `map` node transforms each item via a ZEN decision and produces an output array of the same length. The `reduce` node folds an array into a single accumulator value by invoking ZEN with `{accumulator, current, index}` on each iteration.

## Existing Pattern Analysis

The `filter` and `find` handlers in `control_handlers.go` (lines 320–430) establish the pattern:

1. **Spec parsing**: `parseSpec[FilterSpec](n.Spec)` with strict unknown-field rejection
2. **Validation in handler**: `maxItems > 0` (required, no defaulting), `over != ""`, `jdmId != ""`, `saveAs != ""`
3. **Source resolution**: `c.GetPath(spec.Over)` → `[]any` type assertion
4. **Budget guard**: `len(items) > spec.MaxItems` → validation error before iteration
5. **Iteration with cancellation**: `ctx.Err()` check on each iteration
6. **ZEN invocation**: `dep.Decide.Evaluate(ctx, spec.JDMID, in)` with `projectItemInputs`
7. **Result storage**: `c.Data[spec.SaveAs] = result`
8. **Leaf behavior**: returns `Directive{}` with no children walked

Key helper (lines 421–430):
```go
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
```

## Struct Definitions

### MapSpec (spec.go)

```go
// MapSpec transforms each item in an array via a ZEN decision. For each item,
// the ZEN receives the item (or projected fields) and returns the transformed
// value. The output array has the same length as the input. It is a leaf node.
type MapSpec struct {
    Over     string   `json:"over"`     // path to source array (required)
    JDMID    string   `json:"jdmId"`    // ZEN decision for transformation (required)
    Input    []string `json:"input"`    // fields from each item to pass to ZEN (optional)
    SaveAs   string   `json:"saveAs"`   // where to store transformed array (required)
    MaxItems int      `json:"maxItems"` // budget guard, required > 0 (AC-17)
}
```

### ReduceSpec (spec.go)

```go
// ReduceSpec aggregates an array to a single value via a ZEN decision. For each
// item, the ZEN receives {"accumulator": <acc>, "current": <item>, "index": <n>}
// and returns the new accumulator. The final accumulator is stored at SaveAs.
// It is a leaf node.
type ReduceSpec struct {
    Over         string   `json:"over"`                   // path to source array (required)
    JDMID        string   `json:"jdmId"`                  // ZEN decision for reducer (required)
    Input        []string `json:"input"`                  // fields from current item to project (optional)
    SaveAs       string   `json:"saveAs"`                 // where to store final accumulator (required)
    MaxItems     int      `json:"maxItems"`               // budget guard, required > 0 (AC-17)
    InitialValue any      `json:"initialValue,omitempty"` // starting accumulator, defaults to nil
}
```

**Design decision**: `InitialValue` is `any` (JSON-decoded) rather than `json.RawMessage` because ZEN expects a Go value (`map[string]any` or primitive), and the spec decoder already handles the conversion.

## Node Type Constants (node.go)

Add after `TypeFind`:

```go
TypeMap    NodeType = "map"
TypeReduce NodeType = "reduce"
```

## Handler Implementations (control_handlers.go)

### mapHandler

```go
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
    
    // Validation: require maxItems > 0 (no defaulting, matches filter/find pattern)
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

    // Resolve source array
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

    // Transform each item
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
        result[i] = out // stores entire ZEN output map per item
    }

    // Store result
    if c.Data == nil {
        c.Data = map[string]any{}
    }
    c.Data[spec.SaveAs] = result
    return Directive{}, nil
}
```

**Key behaviors**:
- Reuses `projectItemInputs` from filter/find for input projection — consistent with existing pattern
- Returns entire ZEN output map per item (e.g., if ZEN returns `{"price": 110}`, the result array contains `[{"price": 110}, ...]`)
- Empty input array → empty output array (no error, no ZEN calls)
- Result length always equals input length
- **No defaulting of maxItems** — matches filter/find exactly (lines 323-324, 377-378)

### reduceHandler

```go
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
    
    // Validation: require maxItems > 0 (no defaulting, matches filter/find pattern)
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

    // Resolve source array
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

    // Fold with accumulator
    acc := spec.InitialValue // nil if not specified
    for i, item := range items {
        if cerr := ctx.Err(); cerr != nil {
            return Directive{}, wrapErr(ClassTimeout, "reduce cancelled", cerr)
        }

        // Build ZEN input: accumulator, current item (projected via projectItemInputs for consistency), and index as float64
        in := map[string]any{
            "accumulator": acc,
            "current":     projectItemInputs(item, spec.Input),
            "index":       float64(i), // float64 for JSON compatibility
        }
        out, err := dep.Decide.Evaluate(ctx, spec.JDMID, in)
        if err != nil {
            return Directive{}, classify(err)
        }
        
        // The ZEN output IS the new accumulator
        acc = out
    }

    // Store final accumulator
    if c.Data == nil {
        c.Data = map[string]any{}
    }
    c.Data[spec.SaveAs] = acc
    return Directive{}, nil
}
```

**Key behaviors**:
- ZEN receives `{"accumulator": ..., "current": ..., "index": ...}` — the contract per task spec
- **`current` uses `projectItemInputs`** for consistency with map/filter/find: non-map items are wrapped as `{"item": <value>}`, projected maps extract only the specified fields
- **`index` is `float64`** for JSON compatibility (Go's `encoding/json` decodes JSON numbers as `float64`)
- ZEN output becomes the new accumulator (entire map, not a single field)
- Empty input array → `initialValue` is stored directly (or `nil` if unspecified), **no ZEN calls occur**
- **No defaulting of maxItems** — matches filter/find exactly

**Design decision**: `reduce` reuses `projectItemInputs` for the `current` field instead of introducing a separate helper. This ensures consistent ZEN input contracts across all collection operations. A non-map item like `5` becomes `{"current": {"item": 5}, "accumulator": ..., "index": ...}` — the ZEN author accesses it as `current.item`.

## Interpreter Registration (interpreter.go)

In `New()`, add after `TypeFind: findHandler{}`:

```go
TypeMap:    mapHandler{},
TypeReduce: reduceHandler{},
```

## Spec Validators (spec.go)

Add to `specValidators` map:

```go
TypeMap:    func(r json.RawMessage) error { _, e := parseSpec[MapSpec](r); return e },
TypeReduce: func(r json.RawMessage) error { _, e := parseSpec[ReduceSpec](r); return e },
```

## Validation Rules (validate.go)

### Leaf Type Registration

Add to `leafTypes` map:

```go
TypeMap:    true,
TypeReduce: true,
```

### ValidateTree Switch Cases

Add cases after `TypeFind`:

```go
case TypeMap:
    if spec, err := parseSpec[MapSpec](n.Spec); err == nil {
        checkJDM(add, n.ID, refs, spec.JDMID)
        if spec.MaxItems <= 0 {
            add(n.ID, "map_maxitems", "map maxItems must be > 0")
        }
        if spec.Over == "" {
            add(n.ID, "map_no_over", "map missing over")
        }
        if spec.SaveAs == "" {
            add(n.ID, "map_no_saveas", "map missing saveAs")
        }
    }
case TypeReduce:
    if spec, err := parseSpec[ReduceSpec](n.Spec); err == nil {
        checkJDM(add, n.ID, refs, spec.JDMID)
        if spec.MaxItems <= 0 {
            add(n.ID, "reduce_maxitems", "reduce maxItems must be > 0")
        }
        if spec.Over == "" {
            add(n.ID, "reduce_no_over", "reduce missing over")
        }
        if spec.SaveAs == "" {
            add(n.ID, "reduce_no_saveas", "reduce missing saveAs")
        }
    }
```

This matches filter/find validation exactly (lines 156-175 in validate.go): `maxItems <= 0` is a validation error with no defaulting.

## Error Handling

All errors follow the existing classification (per `errors.go`):

| Condition | Error Class | Example Message |
|-----------|-------------|-----------------|
| Missing required field | `ClassValidation` | `map "x" requires over` |
| maxItems not > 0 | `ClassValidation` | `map "x" requires maxItems > 0` |
| Source path not found | `ClassValidation` | `map "x" source path "items" not found` |
| Source not an array | `ClassValidation` | `map "x" source "items" is not an array` |
| maxItems exceeded | `ClassValidation` | `map "x" over 5000 items exceeds maxItems 1000` |
| Context cancelled | `ClassTimeout` | `map cancelled: context canceled` |
| ZEN evaluation failure | Pass-through | ZEN error already classified by `classify(err)` |
| No evaluator wired | `ClassInternal` | `no decision evaluator wired` |

The `validationf` helper (used by filter/find) creates `ClassValidation` errors. The `wrapErr(ClassTimeout, ...)` pattern handles cancellation. ZEN errors pass through `classify(err)` which preserves the original class.

## Test Matrix (control_handlers_test.go)

### Map Tests

| Test | Setup | Assertion |
|------|-------|-----------|
| `TestMap_TransformItems` | 3 items `[{"p":100},{"p":200},{"p":300}]`, ZEN returns `{"price": input.item.p * 1.1}` | Result is `[{"price":110},{"price":220},{"price":330}]` — full ZEN output maps |
| `TestMap_EmptyArray` | 0 items | Empty array `[]` stored, no error, `len(eval.seen)==0` |
| `TestMap_SingleItem` | 1 item | Single-element array stored, 1 ZEN call |
| `TestMap_MaxItemsExceeded` | 5 items, maxItems=3 | `ErrValidation` before any ZEN call |
| `TestMap_SourceNotFound` | Source path missing | `ErrValidation` |
| `TestMap_SourceNotArray` | Source is string | `ErrValidation` |
| `TestMap_CtxCancelled` | Cancelled context | `ErrTimeout` |
| `TestMap_ZENError` | ZEN returns error | Error propagated |
| `TestMap_InputProjection` | `input: ["price"]` on map items | ZEN receives only price field via `projectItemInputs` |
| `TestMap_ZENReturnsNonMap` | ZEN returns primitive or nil | Result array contains the primitive/nil values |

### Reduce Tests

| Test | Setup | Assertion |
|------|-------|-----------|
| `TestReduce_SumValues` | `[{"v":1},{"v":2},{"v":3}]`, ZEN sums `acc.sum + current.item.v` | Final acc = `{"sum":6}` |
| `TestReduce_FindMax` | `[{"n":3},{"n":1},{"n":5}]`, ZEN keeps max | Final acc = `{"max":5}` |
| `TestReduce_BuildObject` | Array of `[{"k":"a","v":1},{"k":"b","v":2}]` | Final acc = `{"a":1,"b":2}` |
| `TestReduce_EmptyArray` | 0 items, `initialValue: {"sum": 0}` | Result is `{"sum": 0}`, `len(eval.seen)==0` (no ZEN calls) |
| `TestReduce_EmptyArrayNoInitial` | 0 items, no initial | `nil` stored, `len(eval.seen)==0` |
| `TestReduce_SingleItem` | 1 item | One ZEN call, result stored |
| `TestReduce_MaxItemsExceeded` | 5 items, maxItems=3 | `ErrValidation` before any ZEN call |
| `TestReduce_SourceNotFound` | Source path missing | `ErrValidation` |
| `TestReduce_SourceNotArray` | Source is string | `ErrValidation` |
| `TestReduce_CtxCancelled` | Cancelled context | `ErrTimeout` |
| `TestReduce_ZENError` | ZEN returns error | Error propagated |
| `TestReduce_ZENInputShape` | Any items | Verify `eval.seen` contains `{"accumulator":..., "current":{"item":...}, "index":<float64>}` |
| `TestReduce_IndexIsFloat64` | 3 items | Verify `input["index"]` is `float64(0)`, `float64(1)`, `float64(2)` |

### Test Helpers

```go
// transformEvaluator returns a transformation of the input for map tests.
type transformEvaluator struct {
    transform func(input map[string]any) map[string]any
    seen      []map[string]any
}

func (e *transformEvaluator) Evaluate(ctx context.Context, jdmID string, input map[string]any) (map[string]any, error) {
    e.seen = append(e.seen, copyMap(input))
    return e.transform(input), nil
}

// reduceEvaluator applies a fold function for reduce tests.
type reduceEvaluator struct {
    fold func(acc, current any, index float64) map[string]any
    seen []map[string]any
}

func (e *reduceEvaluator) Evaluate(ctx context.Context, jdmID string, input map[string]any) (map[string]any, error) {
    e.seen = append(e.seen, copyMap(input))
    acc := input["accumulator"]
    current := input["current"]
    index := input["index"].(float64) // asserts float64 per design
    return e.fold(acc, current, index), nil
}

func copyMap(m map[string]any) map[string]any {
    out := make(map[string]any, len(m))
    for k, v := range m {
        out[k] = v
    }
    return out
}
```

## Files Modified

| File | Changes |
|------|---------|
| `node.go` | Add `TypeMap`, `TypeReduce` constants |
| `spec.go` | Add `MapSpec`, `ReduceSpec` structs; add to `specValidators` |
| `control_handlers.go` | Add `mapHandler`, `reduceHandler` (reuse `projectItemInputs`) |
| `interpreter.go` | Register `TypeMap` and `TypeReduce` in `New()` |
| `validate.go` | Add to `leafTypes`; add validation cases |
| `control_handlers_test.go` | Add map/reduce test cases and helpers |

## Edge Cases

1. **Empty array**: Map produces `[]`, reduce produces `initialValue` (or `nil`). **No ZEN calls.**
2. **Nil items in array**: Each item is passed to ZEN via `projectItemInputs` (wraps as `{"item": nil}`); ZEN handles nil input.
3. **Nested paths**: `spec.Over = "data.orders"` resolved via `c.GetPath`.
4. **Non-map items with input projection**: `projectItemInputs` returns `{"item": <value>}` regardless of `Input` fields when item is not a map.
5. **Large initialValue**: JSON-decoded to `map[string]any` or primitive; passed to first ZEN call's accumulator field.
6. **ZEN returns non-map**: The handlers store whatever ZEN returns. For map, each element is the ZEN output (could be primitive). For reduce, the final accumulator is the last ZEN output (could be primitive).
7. **ZEN returns nil**: Stored as-is; map stores `nil` in result array, reduce uses `nil` as next accumulator.

## Testability

All handlers are unit-testable via fake evaluators (as filter/find are tested). Integration tests would use real ZEN decision tables but are out of scope for this engine-only change.

The handlers don't access external resources directly — they use `dep.Decide.Evaluate`, which is a mock in tests. This matches the existing test pattern in `control_handlers_test.go`.

---

## Review Finding Responses

| Finding | Resolution |
|---------|------------|
| **#1 (MEDIUM)**: mapHandler validation inconsistency — defaulting to 1000 | **Addressed**: Removed all defaulting logic. Both handlers now require `maxItems > 0` explicitly, matching filter/find pattern exactly (lines 323-324, 377-378). |
| **#2 (MEDIUM)**: reduce index uses int, should be float64 | **Addressed**: Changed to `"index": float64(i)` for JSON compatibility. Added `TestReduce_IndexIsFloat64` to verify. |
| **#3 (MEDIUM)**: projectItemForReduce differs from projectItemInputs | **Addressed**: Removed `projectItemForReduce`. Reduce now uses `projectItemInputs` for the `current` field, so non-map items become `{"item": <value>}`. This ensures consistent ZEN input contracts across all collection operations. |
| **#4 (NIT)**: map output stores entire ZEN output map | **Addressed**: Clarified in design and test description that each array element is the complete ZEN output map (e.g., `[{"price": 110}, {"price": 220}]`), not unwrapped values. Updated `TestMap_TransformItems` to verify this explicitly. |
| **#5 (NIT)**: Missing test case for ZEN returns non-map | **Addressed**: Added `TestMap_ZENReturnsNonMap` to verify behavior when ZEN output is a primitive or nil. |
| **#6 (NIT)**: Reduce empty array behavior under-specified | **Addressed**: Clarified `TestReduce_EmptyArray` and `TestReduce_EmptyArrayNoInitial`: both verify `len(eval.seen)==0` (no ZEN calls) and the exact value stored (initialValue or nil). |
