# Implementation Plan: filter and find Collection Nodes

This plan adds two new collection node types — `filter` and `find` — to the nzr-rules-engine flow layer. Both use ZEN decision tables as predicates, consistent with existing condition/decision/switch nodes.

## Design Summary (validated against existing code)

**Existing patterns discovered:**

1. **Node types** are defined as `NodeType` string constants in `node.go` (e.g., `TypeForEach NodeType = "forEach"`).

2. **Spec structs** in `spec.go` use camelCase JSON tags matching the TypeScript config schema (e.g., `jdmId`, `saveAs`, `maxItems`). The `specValidators` map registers each type for strict decoding.

3. **Control handlers** in `control_handlers.go` follow this pattern:
   - Parse spec via `parseSpec[T](n.Spec)`
   - Validate required fields, return `validationf(...)` errors
   - Use `dep.Decide.Evaluate(ctx, jdmID, input)` to run ZEN decisions
   - Use `c.GetPath(path)` to read from merged view (Input/Data/Response)
   - Store results: `c.Data[saveAs] = value`

4. **forEach handler** pattern for iteration:
   - Read source array via `c.GetPath(spec.Over)` → `[]any`
   - Enforce `MaxItems` upfront before iterating
   - Charge work budget per iteration via `budgetFrom(ctx).chargeWork(1)`
   - Check `ctx.Err()` each iteration for cancellation (AC-17)

5. **Decision node** stores output under `SaveAs` in `c.Data`.

6. **projectInputs(c, spec.Input)** builds a flat input map from named paths for ZEN.

7. **Validation rules** in `validate.go`:
   - `controlTypes` map for nodes that MUST have children
   - `leafTypes` map for nodes that MUST NOT have children
   - Per-type validation in the switch statement inside `walk()`

8. **Handler registration** in `interpreter.go` `New()` adds handlers to the map.

**Design for filter/find:**

- Both are **leaf nodes** (no children) — they compute and store a result, like `decision`.
- `filter`: iterates `Over`, runs predicate per item, keeps items where `out["match"]` is truthy, stores filtered array.
- `find`: iterates `Over`, runs predicate per item, returns first item where `out["match"]` is truthy (or `nil`).
- Both enforce `MaxItems` as an upfront guard (array length must not exceed it).
- Both charge work budget per item evaluated (AC-17).
- Both check `ctx.Err()` per iteration for deadline/cancellation handling.

---

## Implementation Steps

- [ ] 1. Add TypeFilter and TypeFind constants to the node taxonomy in node.go.
      Add two new constants after `TypeForEach`:
      ```go
      TypeFilter   NodeType = "filter"
      TypeFind     NodeType = "find"
      ```
      Files: engine/internal/flow/node.go
      Verify: `cd engine && CGO_ENABLED=1 go build ./...` — compiles.

- [ ] 2. Add FilterSpec and FindSpec structs to spec.go, plus specValidators entries.
      Add after `DecisionSpec`:
      ```go
      // FilterSpec filters an array using a ZEN predicate (leaf).
      type FilterSpec struct {
          Over     string   `json:"over"`
          JDMID    string   `json:"jdmId"`
          Input    []string `json:"input"`
          SaveAs   string   `json:"saveAs"`
          MaxItems int      `json:"maxItems"`
      }

      // FindSpec finds the first matching item using a ZEN predicate (leaf).
      type FindSpec struct {
          Over     string   `json:"over"`
          JDMID    string   `json:"jdmId"`
          Input    []string `json:"input"`
          SaveAs   string   `json:"saveAs"`
          MaxItems int      `json:"maxItems"`
      }
      ```
      Add to `specValidators` map:
      ```go
      TypeFilter:   func(r json.RawMessage) error { _, e := parseSpec[FilterSpec](r); return e },
      TypeFind:     func(r json.RawMessage) error { _, e := parseSpec[FindSpec](r); return e },
      ```
      Files: engine/internal/flow/spec.go
      Verify: `cd engine && CGO_ENABLED=1 go build ./...` — compiles.

- [ ] 3. Add filterHandler and findHandler implementations in control_handlers.go.
      
      **filterHandler** (after forEachHandler):
      ```go
      // filterHandler iterates the array at Over, evaluates the predicate JDM for
      // each item binding the item to Input plus the spec.Input paths, and keeps
      // items where the output's "match" field is truthy. It stores the filtered
      // array under SaveAs in Ctx.Data. It enforces MaxItems upfront and charges
      // the work budget per item (AC-17).
      type filterHandler struct{}

      func (filterHandler) Exec(ctx context.Context, c *Ctx, n Node, dep Deps, w Walker) (Directive, error) {
          spec, err := parseSpec[FilterSpec](n.Spec)
          if err != nil {
              return Directive{}, err
          }
          if spec.MaxItems <= 0 {
              return Directive{}, validationf("filter %q requires maxItems > 0", n.ID)
          }
          if spec.Over == "" || spec.JDMID == "" || spec.SaveAs == "" {
              return Directive{}, validationf("filter %q requires over, jdmId, and saveAs", n.ID)
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

          b := budgetFrom(ctx)
          baseIn := projectInputs(c, spec.Input)
          var result []any

          for i, item := range items {
              if cerr := ctx.Err(); cerr != nil {
                  return Directive{}, wrapErr(ClassTimeout, "filter cancelled", cerr)
              }
              if b != nil {
                  if werr := b.chargeWork(1); werr != nil {
                      return Directive{}, werr
                  }
              }

              in := copyMap(baseIn)
              in["item"] = item
              in["index"] = i

              out, err := dep.Decide.Evaluate(ctx, spec.JDMID, in)
              if err != nil {
                  return Directive{}, classify(err)
              }
              if truthy(out["match"]) {
                  result = append(result, item)
              }
          }

          if c.Data == nil {
              c.Data = map[string]any{}
          }
          c.Data[spec.SaveAs] = result
          return Directive{}, nil
      }
      ```

      **findHandler** (after filterHandler):
      ```go
      // findHandler iterates the array at Over, evaluates the predicate JDM for
      // each item, and returns the first item where the output's "match" field is
      // truthy (or nil if no match). It stores the result under SaveAs in Ctx.Data.
      // It enforces MaxItems upfront and charges the work budget per item (AC-17).
      type findHandler struct{}

      func (findHandler) Exec(ctx context.Context, c *Ctx, n Node, dep Deps, w Walker) (Directive, error) {
          spec, err := parseSpec[FindSpec](n.Spec)
          if err != nil {
              return Directive{}, err
          }
          if spec.MaxItems <= 0 {
              return Directive{}, validationf("find %q requires maxItems > 0", n.ID)
          }
          if spec.Over == "" || spec.JDMID == "" || spec.SaveAs == "" {
              return Directive{}, validationf("find %q requires over, jdmId, and saveAs", n.ID)
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

          b := budgetFrom(ctx)
          baseIn := projectInputs(c, spec.Input)
          var found any // nil if no match

          for i, item := range items {
              if cerr := ctx.Err(); cerr != nil {
                  return Directive{}, wrapErr(ClassTimeout, "find cancelled", cerr)
              }
              if b != nil {
                  if werr := b.chargeWork(1); werr != nil {
                      return Directive{}, werr
                  }
              }

              in := copyMap(baseIn)
              in["item"] = item
              in["index"] = i

              out, err := dep.Decide.Evaluate(ctx, spec.JDMID, in)
              if err != nil {
                  return Directive{}, classify(err)
              }
              if truthy(out["match"]) {
                  found = item
                  break
              }
          }

          if c.Data == nil {
              c.Data = map[string]any{}
          }
          c.Data[spec.SaveAs] = found
          return Directive{}, nil
      }

      // copyMap returns a shallow copy of m.
      func copyMap(m map[string]any) map[string]any {
          out := make(map[string]any, len(m)+2)
          for k, v := range m {
              out[k] = v
          }
          return out
      }
      ```
      Files: engine/internal/flow/control_handlers.go
      Verify: `cd engine && CGO_ENABLED=1 go build ./...` — compiles.

- [ ] 4. Register filterHandler and findHandler in interpreter.go New().
      Add to the handlers map inside `New()`:
      ```go
      TypeFilter:   filterHandler{},
      TypeFind:     findHandler{},
      ```
      Files: engine/internal/flow/interpreter.go
      Verify: `cd engine && CGO_ENABLED=1 go build ./...` — compiles.

- [ ] 5. Add validation rules for filter and find in validate.go.
      
      Add to `leafTypes` map:
      ```go
      TypeFilter:  true,
      TypeFind:    true,
      ```

      Add validation cases in the `walk()` switch statement (after `TypeForEach` case):
      ```go
      case TypeFilter:
          if spec, err := parseSpec[FilterSpec](n.Spec); err == nil {
              checkJDM(add, n.ID, refs, spec.JDMID)
              if spec.MaxItems <= 0 {
                  add(n.ID, "filter_maxitems", "filter maxItems must be > 0")
              }
              if spec.Over == "" {
                  add(n.ID, "filter_no_over", "filter missing over")
              }
              if spec.SaveAs == "" {
                  add(n.ID, "filter_no_saveas", "filter missing saveAs")
              }
          }
      case TypeFind:
          if spec, err := parseSpec[FindSpec](n.Spec); err == nil {
              checkJDM(add, n.ID, refs, spec.JDMID)
              if spec.MaxItems <= 0 {
                  add(n.ID, "find_maxitems", "find maxItems must be > 0")
              }
              if spec.Over == "" {
                  add(n.ID, "find_no_over", "find missing over")
              }
              if spec.SaveAs == "" {
                  add(n.ID, "find_no_saveas", "find missing saveAs")
              }
          }
      ```
      Files: engine/internal/flow/validate.go
      Verify: `cd engine && CGO_ENABLED=1 go build ./...` — compiles.

- [ ] 6. Add unit tests for filter and find handlers in control_handlers_test.go.
      
      Add test cases covering:
      - **TestFilter_FiltersMatchingItems**: 5 items, predicate matches 2, result has 2.
      - **TestFilter_EmptyResult**: No items match, result is empty array `[]any{}` (not nil).
      - **TestFilter_MaxItemsExceeded**: Array exceeds maxItems, returns validation error.
      - **TestFilter_MissingSourcePath**: Over path not found, returns validation error.
      - **TestFind_FirstMatch**: 5 items, returns first matching item.
      - **TestFind_NoMatch**: No items match, result is `nil`.
      - **TestFind_MaxItemsExceeded**: Array exceeds maxItems, returns validation error.
      - **TestFind_MissingSourcePath**: Over path not found, returns validation error.
      - **TestFilter_CtxCancellation**: Cancelled context returns timeout error (AC-17).
      - **TestFind_CtxCancellation**: Cancelled context returns timeout error (AC-17).

      Example test pattern (following existing patterns in the file):
      ```go
      func TestFilter_FiltersMatchingItems(t *testing.T) {
          filter := Node{
              ID:   "filterActive",
              Type: TypeFilter,
              Spec: rawSpec(t, FilterSpec{
                  Over:     "users",
                  JDMID:    "isActive",
                  Input:    []string{},
                  SaveAs:   "activeUsers",
                  MaxItems: 10,
              }),
          }
          tree := &Node{
              ID:       "root",
              Type:     TypeTrigger,
              Spec:     rawSpec(t, TriggerSpec{Method: "GET", Path: "/x"}),
              Children: []Node{filter, {ID: "resp", Type: TypeResponse, Spec: rawSpec(t, ResponseSpec{Status: 200})}},
          }
          // Evaluator returns match:true for items with status="active"
          eval := &predicateEvaluator{matchFn: func(in map[string]any) bool {
              item, _ := in["item"].(map[string]any)
              return item["status"] == "active"
          }}
          c := NewCtx("r", "t", "e", map[string]any{
              "users": []any{
                  map[string]any{"id": 1, "status": "active"},
                  map[string]any{"id": 2, "status": "inactive"},
                  map[string]any{"id": 3, "status": "active"},
              },
          })
          if err := New().Run(context.Background(), tree, Version{}, c, Deps{Decide: eval}); err != nil {
              t.Fatalf("Run error: %v", err)
          }
          got, ok := c.Data["activeUsers"].([]any)
          if !ok || len(got) != 2 {
              t.Fatalf("expected 2 active users, got: %#v", c.Data["activeUsers"])
          }
      }

      // predicateEvaluator returns {"match": matchFn(input)} for testing filter/find.
      type predicateEvaluator struct {
          matchFn func(map[string]any) bool
      }

      func (e *predicateEvaluator) Evaluate(ctx context.Context, jdmID string, input map[string]any) (map[string]any, error) {
          return map[string]any{"match": e.matchFn(input)}, nil
      }
      ```
      Files: engine/internal/flow/control_handlers_test.go
      Verify: `cd engine && CGO_ENABLED=1 go test ./internal/flow/...` — all tests pass.

- [ ] 7. Run full test suite and verify build.
      Files: (none — verification only)
      Verify: `cd engine && CGO_ENABLED=1 go test ./...` — all tests pass, no regressions.

---

## Notes

- **Predicate convention**: The ZEN decision receives `{"item": <current item>, "index": <0-based index>, ...spec.Input projections}` and must return `{"match": true/false}`. This matches how `condition` reads `result` and `switch` reads `branch` — a simple field convention.

- **Empty filter result**: Returns `[]any{}` (empty slice), not `nil`, so downstream code can safely iterate.

- **Find no-match result**: Returns `nil`, stored as `c.Data[saveAs] = nil`. Downstream can check `if val == nil`.

- **MaxItems is a hard cap**: Checked upfront before iteration, not during. This matches `forEach` behavior.

- **No children**: Both are leaves like `decision`. Validation enforces this via `leafTypes`.
