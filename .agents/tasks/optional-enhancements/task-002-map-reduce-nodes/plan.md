# Implementation Plan: Map/Reduce Node Types (TASK-002)

This plan adds `map` and `reduce` node types to the flow engine following the existing `filter`/`find` pattern. The implementation is a single cohesive change to the `flow` package — all files are in `engine/internal/flow/`.

**Verification command**: `cd /home/nuzirwan/project/rule-engine-api/.worktrees/map-reduce-nodes/engine && CGO_ENABLED=1 go build ./... && go test ./...`

---

## Implementation Plan

- [ ] 1. Add `TypeMap` and `TypeReduce` constants to node.go.
      Add after `TypeFind` (line ~33):
      ```go
      TypeMap    NodeType = "map"
      TypeReduce NodeType = "reduce"
      ```
      Files: `engine/internal/flow/node.go`
      Verify: `go build ./...` — compiles without errors.

- [ ] 2. Add `MapSpec` and `ReduceSpec` structs and register spec validators in spec.go.
      Add after `FindSpec` (around line 150):
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
      Add to `specValidators` map (around line 175):
      ```go
      TypeMap:    func(r json.RawMessage) error { _, e := parseSpec[MapSpec](r); return e },
      TypeReduce: func(r json.RawMessage) error { _, e := parseSpec[ReduceSpec](r); return e },
      ```
      Files: `engine/internal/flow/spec.go`
      Verify: `go build ./...` — compiles without errors.

- [ ] 3. Add `mapHandler` and `reduceHandler` implementations to control_handlers.go.
      Add after `findHandler` (around line 420, before `projectItemInputs`):
      
      **mapHandler** — follows the filter/find pattern exactly:
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
      
      **reduceHandler**:
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
      
              // Build ZEN input: accumulator, current item (projected via projectItemInputs), index as float64
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
      Files: `engine/internal/flow/control_handlers.go`
      Verify: `go build ./...` — compiles without errors.

- [ ] 4. Register handlers in interpreter.go.
      Add in `New()` handlers map, after `TypeFind: findHandler{}` (around line 59):
      ```go
      TypeMap:    mapHandler{},
      TypeReduce: reduceHandler{},
      ```
      Files: `engine/internal/flow/interpreter.go`
      Verify: `go build ./...` — compiles without errors.

- [ ] 5. Add leaf type registration and validation cases in validate.go.
      Add to `leafTypes` map (around line 50):
      ```go
      TypeMap:    true,
      TypeReduce: true,
      ```
      Add validation switch cases after `TypeFind` (around line 180):
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
      Files: `engine/internal/flow/validate.go`
      Verify: `go build ./...` — compiles without errors.

- [ ] 6. Add unit tests for map and reduce handlers in control_handlers_test.go.
      Add after the existing filter/find tests (around line 530). Tests follow the existing `matchEvaluator` pattern:
      
      **Test helpers** (add near top of file with other test helpers):
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
          index := input["index"].(float64)
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
      
      **Map tests**:
      - `TestMap_TransformItems`: 3 items, ZEN multiplies price by 1.1, result is array of ZEN output maps
      - `TestMap_EmptyArray`: 0 items → empty array `[]`, no ZEN calls
      - `TestMap_SingleItem`: 1 item → 1-element array, 1 ZEN call
      - `TestMap_MaxItemsExceeded`: 5 items, maxItems=3 → `ErrValidation` before any ZEN call
      - `TestMap_SourceNotFound`: Source path missing → `ErrValidation`
      - `TestMap_SourceNotArray`: Source is string → `ErrValidation`
      - `TestMap_CtxCancelled`: Cancelled context → `ErrTimeout`
      - `TestMap_ZENError`: ZEN returns error → error propagated
      - `TestMap_InputProjection`: `input: ["price"]` on map items → ZEN receives only price field
      
      **Reduce tests**:
      - `TestReduce_SumValues`: `[{"v":1},{"v":2},{"v":3}]`, ZEN sums → final `{"sum":6}`
      - `TestReduce_EmptyArrayWithInitial`: 0 items, `initialValue: {"sum": 0}` → `{"sum": 0}`, no ZEN calls
      - `TestReduce_EmptyArrayNoInitial`: 0 items, no initial → `nil`, no ZEN calls
      - `TestReduce_SingleItem`: 1 item → 1 ZEN call
      - `TestReduce_MaxItemsExceeded`: 5 items, maxItems=3 → `ErrValidation` before any ZEN call
      - `TestReduce_SourceNotFound`: Source path missing → `ErrValidation`
      - `TestReduce_SourceNotArray`: Source is string → `ErrValidation`
      - `TestReduce_CtxCancelled`: Cancelled context → `ErrTimeout`
      - `TestReduce_ZENError`: ZEN returns error → error propagated
      - `TestReduce_ZENInputShape`: Verify input contains `{"accumulator":..., "current":{"item":...}, "index":<float64>}`
      - `TestReduce_IndexIsFloat64`: Verify index is `float64(0)`, `float64(1)`, etc.
      
      Files: `engine/internal/flow/control_handlers_test.go`
      Verify: `go test ./...` — all tests pass including new map/reduce tests.

- [ ] 7. Final verification: build and test the entire engine.
      Files: All modified files
      Verify: `cd /home/nuzirwan/project/rule-engine-api/.worktrees/map-reduce-nodes/engine && CGO_ENABLED=1 go build ./... && go test ./...` — build succeeds, all tests pass.

---

## Key Implementation Details

### Pattern Consistency (from existing filter/find code)

The handlers MUST follow the existing filter/find pattern exactly:

1. **Spec parsing**: `parseSpec[MapSpec](n.Spec)` with unknown-field rejection
2. **Validation in handler**: `maxItems > 0` required (no defaulting), `over != ""`, `jdmId != ""`, `saveAs != ""`
3. **Source resolution**: `c.GetPath(spec.Over)` → `[]any` type assertion
4. **Budget guard**: `len(items) > spec.MaxItems` → validation error BEFORE iteration
5. **Iteration with cancellation**: `ctx.Err()` check on each iteration
6. **ZEN invocation**: `dep.Decide.Evaluate(ctx, spec.JDMID, in)` using `projectItemInputs`
7. **Result storage**: `c.Data[spec.SaveAs] = result`
8. **Leaf behavior**: returns `Directive{}` (no children walked)

### Reusing `projectItemInputs` (lines 421-430 of control_handlers.go)

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

- `map` handler: calls `projectItemInputs(item, spec.Input)` for ZEN input, same as filter/find
- `reduce` handler: uses `projectItemInputs(item, spec.Input)` for the `current` field, so a non-map item `5` becomes `{"current": {"item": 5}, "accumulator": ..., "index": ...}`

### Reduce ZEN Input Contract

```go
in := map[string]any{
    "accumulator": acc,
    "current":     projectItemInputs(item, spec.Input),
    "index":       float64(i), // float64 for JSON compatibility
}
```

### Error Messages (match filter/find exactly)

- `map "x" requires maxItems > 0`
- `map "x" requires over`
- `map "x" requires jdmId`
- `map "x" requires saveAs`
- `map "x" source path "items" not found`
- `map "x" source "items" is not an array`
- `map "x" over 5000 items exceeds maxItems 1000`
- `map cancelled` (ClassTimeout)

Same pattern for `reduce`.

### Edge Cases

1. **Empty array**: Map → `[]`, Reduce → `initialValue` (or `nil`). No ZEN calls in either case.
2. **Nil items**: Passed to ZEN via `projectItemInputs` (wraps as `{"item": nil}`)
3. **ZEN returns non-map**: Stored as-is in map result array; becomes reduce accumulator
4. **ZEN returns nil**: Map stores `nil` in array; reduce uses `nil` as next accumulator
