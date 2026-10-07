# Implementation Plan: Replace JSON File Connector with Load Node

## Summary

Remove the `json-file` connector and build a `load` node type that loads data mid-flow from three mutually-exclusive sources: inline data, local files, or URLs. The load node stores results into `Ctx.Data` under `saveAs` and supports optional JSONPath extraction.

## Design Decisions

1. **JSONPath library**: Use `github.com/tidwall/gjson` which is already in the module (used by jsonfile.go). GJSON's query syntax is simpler than JSONPath but powerful enough — and there's already a `normalizeJSONPath` helper in the codebase that converts `$.foo.bar` to `foo.bar` for GJSON.

2. **Handler location**: Add `handleLoad` to `control_handlers.go` following the pattern of filter/find/map/reduce — all are data-manipulation leaf nodes that read from Ctx and write to Ctx.Data.

3. **Node registration**: Add `TypeLoad` constant to `node.go` and register `loadHandler{}` in the interpreter's `New()` function in `interpreter.go`.

4. **Spec validation**: Add `LoadSpec` to `spec.go` and add it to `specValidators` map for unknown-field rejection.

5. **Source mode validation**: Exactly one of `data`, `path`, or `url` must be present. Validation happens at execution time in the handler (consistent with other handlers).

6. **HTTP timeout**: Use 30-second timeout for URL fetches via `http.Client` with a `Timeout` field.

7. **Context storage**: Store under `Ctx.Data[saveAs]` like filter/find/map/reduce do — NOT into Response.

## Implementation Plan

- [ ] 1. Add the TypeLoad constant and LoadSpec struct to the flow package.
      Define `TypeLoad NodeType = "load"` in node.go alongside other type constants.
      Define `LoadSpec` in spec.go with fields: Data (any), Path (string), URL (string), JSONPath (string), SaveAs (string).
      Add LoadSpec to the specValidators map.
      Files: engine/internal/flow/node.go, engine/internal/flow/spec.go
      Verify: `cd engine && go build ./...` — compiles without errors.

- [ ] 2. Implement the loadHandler in control_handlers.go.
      Create a `loadHandler struct{}` with `Exec` method implementing NodeHandler.
      Logic:
      - Parse LoadSpec from node.Spec
      - Validate saveAs is non-empty
      - Count how many of data/path/url are set; error if not exactly one
      - If data: use directly (already parsed from JSON)
      - If path: read file with os.ReadFile, parse as JSON
      - If url: fetch with http.Client (30s timeout), parse response as JSON
      - If jsonPath set: apply GJSON extraction using normalizeJSONPath pattern
      - Store result in c.Data[spec.SaveAs]
      Add a local normalizeJSONPath helper (copy pattern from webhook/mapping.go or drivers/jsonfile.go).
      Files: engine/internal/flow/control_handlers.go
      Verify: `cd engine && go build ./...` — compiles without errors.

- [ ] 3. Register the loadHandler in the interpreter.
      Add `TypeLoad: loadHandler{}` to the handlers map in New() function.
      Files: engine/internal/flow/interpreter.go
      Verify: `cd engine && go build ./...` — compiles without errors.

- [ ] 4. Write unit tests for the load node handler.
      Add tests to control_handlers_test.go covering:
      - Inline data mode: basic object, array, nested structure
      - Local file mode: valid JSON file, file not found, invalid JSON
      - URL mode: mock HTTP server returning valid JSON, HTTP error, invalid JSON
      - JSONPath extraction: extract subset from inline/file/url sources
      - Validation errors: missing saveAs, no source specified, multiple sources specified
      Files: engine/internal/flow/control_handlers_test.go
      Verify: `cd engine && go test ./internal/flow/... -run TestLoad` — all new tests pass.

- [ ] 5. Remove the JSON file connector from the driver registry.
      Delete the `newJSONFileConnector()` call from the All() function in registry.go.
      Files: engine/internal/connect/drivers/registry.go
      Verify: `cd engine && go build ./...` — compiles without errors.

- [ ] 6. Delete the JSON file connector implementation and tests.
      Remove jsonfile.go and jsonfile_test.go from the drivers package.
      Files: engine/internal/connect/drivers/jsonfile.go (delete), engine/internal/connect/drivers/jsonfile_test.go (delete)
      Verify: `cd engine && go build ./... && go test ./internal/connect/...` — builds and remaining driver tests pass.

- [ ] 7. Remove JSON file connector documentation from connectors.md.
      Remove the entire "JSON File Connector" section from docs/examples/connectors.md.
      Also remove "json-file" from the connection types table and supported types table.
      Files: docs/examples/connectors.md
      Verify: Manual review that json-file references are removed from the doc.

- [ ] 8. Create documentation for the load node.
      Create docs/examples/load-node.md with:
      - Overview explaining the three source modes
      - LoadSpec reference table
      - Examples for each mode (inline, local file, URL)
      - JSONPath extraction example
      - Example combining load with collection nodes (load products, filter by price)
      - Error handling notes
      Files: docs/examples/load-node.md
      Verify: File exists with comprehensive examples.

- [ ] 9. Run full test suite and verify build.
      Ensure all existing tests still pass after the changes.
      Files: (none modified)
      Verify: `cd engine && go test ./...` — all tests pass.

## LoadSpec Design

```go
// LoadSpec loads data mid-flow from inline, local file, or URL source.
// Exactly one of Data, Path, or URL must be set. The loaded (and optionally
// JSONPath-extracted) value is stored in Ctx.Data under SaveAs.
type LoadSpec struct {
    Data     any    `json:"data,omitempty"`     // inline JSON data (used directly)
    Path     string `json:"path,omitempty"`     // local file path to read
    URL      string `json:"url,omitempty"`      // HTTP(S) URL to fetch
    JSONPath string `json:"jsonPath,omitempty"` // optional GJSON path to extract subset
    SaveAs   string `json:"saveAs"`             // context key to store result (required)
}
```

## Load Node Spec Examples

Inline data:
```json
{ "type": "load", "spec": { "data": [1, 2, 3], "saveAs": "items" } }
```

Local file:
```json
{ "type": "load", "spec": { "path": "/data/products.json", "saveAs": "products" } }
```

URL:
```json
{ "type": "load", "spec": { "url": "https://api.example.com/data.json", "saveAs": "apiData" } }
```

With JSONPath:
```json
{ "type": "load", "spec": { "path": "/data/config.json", "jsonPath": "$.settings.rules", "saveAs": "rules" } }
```

## Handler Implementation Notes

The handler follows the same pattern as filterHandler/findHandler:
- Leaf node (ignores Walker w)
- Stores result in c.Data[spec.SaveAs]
- Returns empty Directive{} on success
- Returns classified error on failure

For URL fetching, use a simple approach:
```go
client := &http.Client{Timeout: 30 * time.Second}
resp, err := client.Get(spec.URL)
```

For JSONPath, copy the normalizeJSONPath pattern from jsonfile.go:
```go
func normalizeJSONPath(path string) string {
    path = strings.TrimSpace(path)
    if path == "" || path == "$" {
        return ""
    }
    if strings.HasPrefix(path, "$.") {
        return path[2:]
    }
    if strings.HasPrefix(path, "$") {
        return path[1:]
    }
    return path
}
```

Then use gjson.Get:
```go
if spec.JSONPath != "" {
    query := normalizeJSONPath(spec.JSONPath)
    result := gjson.GetBytes(data, query)
    if !result.Exists() {
        return nil // or empty value, matching existing behavior
    }
    return result.Value()
}
```

## Test Coverage Matrix

| Scenario | Mode | JSONPath | Expected |
|----------|------|----------|----------|
| Inline object | data | no | stores object |
| Inline array | data | no | stores array |
| Inline with JSONPath | data | yes | extracts subset |
| Valid file | path | no | parses and stores |
| File not found | path | no | validation error |
| Invalid JSON file | path | no | validation error |
| Valid URL | url | no | fetches and stores |
| HTTP 404 | url | no | validation error |
| HTTP 500 | url | no | validation error |
| Invalid JSON response | url | no | validation error |
| URL with JSONPath | url | yes | extracts subset |
| Missing saveAs | any | - | validation error |
| No source | none | - | validation error |
| Multiple sources | data+path | - | validation error |
