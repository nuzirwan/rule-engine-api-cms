# TASK-002: Map/Reduce Collection Nodes

## Summary
Add `map` and `reduce` node types to the flow engine to complete collection operations. Currently we have `forEach`, `filter`, and `find`. Map transforms each item; reduce aggregates to a single value.

## Context
- `filter` and `find` nodes were added in commit `cdb84d0`
- They use ZEN decision tables as predicates
- Pattern: read collection from `source` path, apply predicate, store result via `saveAs`
- `forEach` iterates and executes child nodes per item
- Missing: `map` (transform) and `reduce` (aggregate)

## Requirements

### 1. Map Node
Transform each item in a collection using a ZEN decision table.

```json
{
  "type": "map",
  "id": "transform-orders",
  "source": "ctx.orders",
  "jdmId": "order-transformer",
  "saveAs": "ctx.transformedOrders",
  "maxItems": 1000
}
```

- Input: array at `source` path
- For each item: run ZEN decision with item as input
- Output: array of ZEN outputs (same length as input)
- Store result at `saveAs` path
- `maxItems` budget guard (AC-17 compliance)

### 2. Reduce Node
Aggregate a collection to a single value using a ZEN decision table.

```json
{
  "type": "reduce",
  "id": "sum-totals",
  "source": "ctx.orders",
  "jdmId": "order-reducer",
  "initialValue": {"total": 0, "count": 0},
  "saveAs": "ctx.summary",
  "maxItems": 1000
}
```

- Input: array at `source` path
- ZEN receives `{"accumulator": <acc>, "current": <item>, "index": <n>}`
- ZEN returns new accumulator value
- Final accumulator stored at `saveAs`
- `initialValue` is starting accumulator (defaults to `null`)
- `maxItems` budget guard

### 3. Validation Rules
- `source` must be a valid context path
- `jdmId` must exist and be valid
- `saveAs` must be a valid context path
- `maxItems` must be positive integer if specified (default 1000)
- `initialValue` (reduce only) must be valid JSON

## Files to Modify/Create

### Engine (`engine/internal/flow/`)
- `node.go` — add `TypeMap`, `TypeReduce` constants
- `spec.go` — add `MapSpec`, `ReduceSpec` structs
- `control_handlers.go` — add `mapHandler`, `reduceHandler` functions
- `interpreter.go` — register new handlers
- `validate.go` — add validation rules for map/reduce
- `control_handlers_test.go` — unit tests for map/reduce

## Acceptance Criteria
- [ ] `map` node transforms each item via ZEN decision
- [ ] `reduce` node aggregates collection via ZEN decision
- [ ] Both respect `maxItems` budget guard
- [ ] Both store results at `saveAs` path
- [ ] Validation catches invalid specs
- [ ] Unit tests cover: empty array, single item, multiple items, maxItems exceeded, invalid source path

## Testing
- Unit tests in `control_handlers_test.go`
- Test cases:
  - Map: transform prices (multiply by 1.1)
  - Map: extract fields (item → item.name)
  - Reduce: sum values
  - Reduce: find max
  - Reduce: build object from array
  - Edge: empty array
  - Edge: maxItems exceeded

## Example Use Cases

### Map: Add tax to prices
```json
// Input: [{"price": 100}, {"price": 200}]
// ZEN: returns {"price": input.price * 1.1}
// Output: [{"price": 110}, {"price": 220}]
```

### Reduce: Sum order totals
```json
// Input: [{"total": 100}, {"total": 200}, {"total": 50}]
// ZEN: returns {"sum": accumulator.sum + current.total, "count": accumulator.count + 1}
// Initial: {"sum": 0, "count": 0}
// Output: {"sum": 350, "count": 3}
```
