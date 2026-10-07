# Flow Input Validation — Design Investigation

## Summary

**Current state:** The `TriggerInput` struct declares which request parts to capture (params, query, headers, body), but only path params are actually extracted at runtime (`genericFlowHandler` in server.go lines 203-206). Query strings, headers, and body are **not extracted** for regular flows — `TriggerInput` is currently a **contract surface only**, not a runtime binding. Furthermore, there is **no schema validation** for incoming request data — no required/type/format checks exist.

**Recommendation:** Implement **Option A (Enhanced TriggerInput)** with inline validation schema. This is the cleanest fit: validation is declared alongside extraction, config-driven, and uses the existing error taxonomy. Hook validation into `genericFlowHandler` at the HTTP edge, before `Ctx` creation.

---

## 1. Current State Analysis

### 1.1 What's Declared vs. Implemented

| TriggerInput Field | Declared | Implemented at Runtime |
|--------------------|----------|------------------------|
| `Params []string` | ✓ | **Partial** — Path params extracted, but only those in the URL pattern |
| `Query []string` | ✓ | ✗ — Never read from `r.URL.Query()` |
| `Headers []string` | ✓ | ✗ — Never read from `r.Header` |
| `Body bool` | ✓ | ✗ — Never parsed from `r.Body` |

**Evidence — `genericFlowHandler` (server.go:169-230):**
```go
// Lines 203-206: ONLY path params are extracted
input := make(map[string]any, len(params))
for name, val := range params {
    input[name] = val  // params comes from matchRoute, the URL pattern match
}
c := flow.NewCtx(requestID(r), traceID(r), defaultEnv, input)
```

The declared `TriggerInput.Query`, `Headers`, and `Body` fields are **never read**. They exist only in the spec parsing and are ignored at runtime.

### 1.2 Webhook Handler — A Different Pattern

The webhook handler (`webhook.go:49-181`) does extract body and headers:
```go
body, err := io.ReadAll(io.LimitReader(r.Body, 10<<20))  // line 63
headers := extractHeaders(r)                              // line 71
input := webhook.MapPayload(body, wh.Mapping)             // line 132
input["payload"] = event.Payload                          // line 140
```

But this is driven by a completely different config (`WebhookConfig.Mapping`), not `TriggerInput`. Webhooks also do provider-specific signature verification but **no schema validation** of the payload contents.

### 1.3 Existing Validation Patterns

The codebase has a strong validation pattern for **action params** (the SQL bind parameters):

**`param.go` (lines 86-116) — `ConvertParam`:**
```go
func ConvertParam(name, resolved string, as ParamType) (any, error) {
    switch as {
    case ParamText, "":
        return resolved, nil
    case ParamInt:
        n, err := strconv.ParseInt(strings.TrimSpace(resolved), 10, 64)
        if err != nil {
            return nil, validationf("param %s: value %q is not an int", name, resolved)
        }
        return n, nil
    // ... ParamNumeric, ParamBool similarly
    }
}
```

This pattern:
1. Declares the expected type in config (`{"value":"{{input.x}}","as":"int"}`)
2. Validates at runtime with a `ClassValidation` error on failure
3. Returns a clear error message naming the offending value
4. Is enforced at publish time by `checkParamTypes` in `validate.go:263-285`

This same pattern should extend to input validation.

### 1.4 Error Handling Chain

The error taxonomy is well-established:
- `flow.ErrValidation` → 400 Bad Request (server.go:322-323)
- Classified errors carry structured messages but coarse client-facing bodies

**`statusForFlow` (server.go:320-333):**
```go
func statusForFlow(err error) (int, string) {
    switch {
    case errors.Is(err, flow.ErrValidation):
        return http.StatusBadRequest, "invalid request"
    // ...
    }
}
```

Input validation errors would naturally flow through this existing classification.

---

## 2. Design Options

### Option A: Enhanced TriggerInput with Inline Schema (Recommended)

Extend `TriggerInput` to declare validation rules inline:

```go
type TriggerInput struct {
    Params  []InputField `json:"params,omitempty"`
    Query   []InputField `json:"query,omitempty"`
    Headers []InputField `json:"headers,omitempty"`
    Body    *BodySchema  `json:"body,omitempty"`  // was bool
}

type InputField struct {
    Name     string   `json:"name"`               // required
    Type     string   `json:"type,omitempty"`     // "string"|"int"|"number"|"bool" (default: string)
    Required bool     `json:"required,omitempty"`
    Pattern  string   `json:"pattern,omitempty"`  // regex for strings
    Enum     []any    `json:"enum,omitempty"`     // allowed values
    Default  any      `json:"default,omitempty"`  // when absent and not required
}

type BodySchema struct {
    Required bool                    `json:"required,omitempty"`
    Fields   map[string]FieldSchema  `json:"fields,omitempty"` // top-level fields to validate
}

type FieldSchema struct {
    Type     string   `json:"type,omitempty"`
    Required bool     `json:"required,omitempty"`
    Pattern  string   `json:"pattern,omitempty"`
    Enum     []any    `json:"enum,omitempty"`
}
```

**Pros:**
- Follows the existing `ConvertParam` pattern (type declared in config)
- Self-contained — validation rules live with the flow, travel with the version
- Publish-time validation catches bad patterns/enums before deployment
- No additional config artifacts to manage

**Cons:**
- Extends `TriggerSpec` (a spec change, though additive/backward-compatible)
- Schema is flow-embedded, not reusable across flows (acceptable for v1)

### Option B: Separate Validation Node Type

Add a new `validate` node that runs after trigger:

```go
type ValidateSpec struct {
    Schema map[string]FieldSchema `json:"schema"`
    OnFail string                  `json:"onFail"` // "error" or branch key
}
```

**Pros:**
- Node-based, composable — can appear anywhere in the tree
- Could validate mid-flow data, not just input

**Cons:**
- Validation runs AFTER `Ctx.Input` is built — can't reject before creating context
- More verbose for the common case (validate at entry)
- A new node type is heavyweight for what is fundamentally input gating

### Option C: Reference a JDM for Validation

Reuse the existing ZEN infrastructure:
```go
type TriggerInput struct {
    // ...existing...
    ValidationJDM string `json:"validationJdm,omitempty"`  // JDM id for input validation
}
```

The JDM outputs `{"valid": true/false, "errors": [...]}`.

**Pros:**
- Zero new validation code — ZEN already handles complex logic
- Consistent with the "pure decisions" pattern (ADR-001)
- Rules can be arbitrarily complex (cross-field validation, etc.)

**Cons:**
- JDM is overkill for simple type/required checks
- Creates a publish-time dependency: must publish JDM before flow
- JDM errors are harder to interpret ("decision failed" vs "field X is required")
- Performance overhead: compile + evaluate for every request

### Option D: OpenAPI Schema Reference

Store schemas separately and reference by name:
```go
type TriggerInput struct {
    // ...
    SchemaRef string `json:"schemaRef,omitempty"`  // reference to a stored schema
}
```

**Pros:**
- Schemas reusable across multiple flows
- Familiar to developers with OpenAPI experience

**Cons:**
- New config entity to manage (schema store, versioning, publish)
- More complex publish-time validation (must resolve ref)
- Overkill for the current scale

---

## 3. Recommended Approach: Option A

### 3.1 Why Option A

1. **Follows established patterns:** Mirrors `ConvertParam` — type in config, validate at runtime, `ClassValidation` on failure.

2. **Minimal new surface:** No new node types, no new config stores, no JDM dependency.

3. **Self-documenting:** The flow spec declares its contract; inspection tools can report what inputs a flow expects.

4. **Backward compatible:** The old `[]string` arrays can be supported as shorthand:
   - `"params": ["id"]` → `[{"name": "id", "type": "string"}]`
   - The strict decoder needs a custom unmarshal to handle both shapes.

5. **Publish-time catchable:** `ValidateTree` can verify patterns compile, enums are valid, etc.

### 3.2 Where Validation Hooks In

**At the HTTP edge, in `genericFlowHandler`, BEFORE `flow.NewCtx`:**

```go
func genericFlowHandler(store Store, interp *flow.Interpreter, deps Deps) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        // ... route matching, flow resolution (lines 173-192) ...

        // NEW: Extract AND validate input against TriggerSpec.Input
        input, err := extractAndValidate(r, params, fv.Tree.Spec)
        if err != nil {
            writeError(w, http.StatusBadRequest, err.Error())
            return
        }

        c := flow.NewCtx(requestID(r), traceID(r), defaultEnv, input)
        // ... rest unchanged ...
    }
}
```

**Why at the HTTP edge, not in `triggerHandler.Exec`:**
- Fail fast — reject before allocating `Ctx`, starting spans, etc.
- Clear HTTP semantics — 400 for bad input is the edge's job
- The flow engine stays pure — it trusts `Ctx.Input` is already valid
- Consistent with the existing "httpapi does extraction" contract (lld-contracts.md line 38-40)

### 3.3 Implementation Touchpoints

| File | Change |
|------|--------|
| `engine/internal/flow/spec.go` | Extend `TriggerInput` with schema types |
| `engine/internal/flow/validate.go` | Add `validateInputSchema` checks in `ValidateTree` |
| `engine/internal/flow/input.go` (NEW) | Input extraction and validation logic |
| `engine/internal/flow/errors.go` | Add input validation error helpers |
| `engine/internal/httpapi/server.go` | Call extraction/validation in `genericFlowHandler` |
| `engine/internal/httpapi/webhook.go` | Optionally apply same validation for webhook inputs |
| `cms/types/engine.ts` | Update TypeScript types for `TriggerInput` schema |

---

## 4. Schema Surface Sketch

### 4.1 Flow Config Example

```json
{
  "id": "get-order",
  "type": "trigger",
  "spec": {
    "method": "GET",
    "path": "/orders/{id}",
    "input": {
      "params": [
        { "name": "id", "type": "string", "required": true, "pattern": "^[0-9a-f]{8}$" }
      ],
      "query": [
        { "name": "include", "type": "string", "enum": ["items", "history", "all"] },
        { "name": "limit", "type": "int", "default": 10 }
      ],
      "headers": [
        { "name": "X-Tenant-Id", "type": "string", "required": true }
      ]
    }
  }
}
```

### 4.2 POST with Body

```json
{
  "id": "create-order",
  "type": "trigger",
  "spec": {
    "method": "POST",
    "path": "/orders",
    "input": {
      "body": {
        "required": true,
        "fields": {
          "customer_id": { "type": "string", "required": true },
          "amount": { "type": "number", "required": true },
          "currency": { "type": "string", "enum": ["USD", "EUR", "IDR"], "default": "USD" }
        }
      }
    }
  }
}
```

### 4.3 Backward-Compatible Shorthand

The existing `"params": ["id"]` still works and means `[{"name": "id", "type": "string"}]`.

---

## 5. Error Response Shape

Validation errors should be structured but client-safe:

```json
{
  "error": "invalid request",
  "details": [
    { "field": "params.id", "code": "pattern", "message": "must match ^[0-9a-f]{8}$" },
    { "field": "query.limit", "code": "type", "message": "must be an integer" },
    { "field": "body.customer_id", "code": "required", "message": "is required" }
  ]
}
```

Internally, this is a `ClassValidation` error with structured context:

```go
type InputValidationError struct {
    Field   string // "params.id", "query.limit", "body.customer_id"
    Code    string // "required", "type", "pattern", "enum"
    Message string
}

func (e *InputValidationError) Error() string {
    return fmt.Sprintf("input %s: %s", e.Field, e.Message)
}
```

---

## 6. Questions Answered

### Q1: Current gap — what's declared vs implemented?

**TriggerInput declares four input sources; only path params are extracted.** The gap is:
- Query params: declared but never read
- Headers: declared but never read
- Body: declared but never parsed

### Q2: Design options?

Four viable options analyzed; **Option A (enhanced TriggerInput)** recommended for minimal footprint, established patterns, and self-contained config.

### Q3: Integration points?

**HTTP edge (`genericFlowHandler`)** is the right hook — validate before `Ctx` creation, return 400 for bad input. The flow engine trusts validated input.

### Q4: Error handling?

Use existing `ClassValidation` → 400 mapping. Extend with structured details array for developer-friendly messages.

### Q5: Can the `ConvertParam` pattern extend?

**Yes.** The type/required/pattern/enum model in the schema mirrors how `ConvertParam` handles `ParamType`. The validation logic is a generalization of that pattern.

---

## 7. Next Steps (Implementation Order)

1. **Extend `TriggerInput` in spec.go** — new types, backward-compatible unmarshal
2. **Add `extractAndValidate` in new `input.go`** — the extraction + validation logic
3. **Update `ValidateTree`** — catch bad patterns/enums at publish
4. **Wire into `genericFlowHandler`** — the HTTP edge call site
5. **Update CMS types** — TypeScript `TriggerInput` schema types
6. **Add tests** — table-driven: required missing, type mismatch, pattern fail, enum fail, defaults applied
7. **Update templates** — enrich existing `crud-rest-postgres.json` etc. with validation examples

---

## Appendix: Files Examined

- `engine/internal/flow/spec.go` — TriggerInput, TriggerSpec definitions (lines 38-53)
- `engine/internal/httpapi/server.go` — genericFlowHandler (lines 169-230), statusForFlow (320-333)
- `engine/internal/flow/handlers.go` — triggerHandler.Exec (lines 14-25)
- `engine/internal/flow/param.go` — ConvertParam, ParseParam (lines 45-116)
- `engine/internal/flow/validate.go` — ValidateTree, checkParamTypes (lines 63-285)
- `engine/internal/flow/errors.go` — ErrClass, ClassValidation (lines 10-42)
- `engine/internal/httpapi/webhook.go` — webhookHandler body/header extraction (lines 49-181)
- `engine/internal/httpapi/adapters.go` — lookupPath, resolveTemplates (lines 210-267)
- `docs/lld-contracts.md` — Ctx contract, TriggerInput contract surface
- `docs/lld/slice-a-interpreter.md` — TriggerInput design spec
