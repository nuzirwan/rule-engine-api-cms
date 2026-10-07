# JSON File Connector for Mid-Flow Data Loading

This change adds a `json-file` connector to the rule engine that enables flows to load JSON data from local files or HTTP(S) URLs mid-execution, then query/manipulate it with existing find/filter/map/reduce nodes. The connector follows the ephemeral lifecycle pattern from the REST connector—stateless, one-shot operations—and implements both `read` (with optional jsonPath selection via GJSON) and `write` actions. Security relies on three layers: early rejection of `..` in paths, basePath confinement with symlink resolution, and explicit opt-in for HTTP URLs.

**Watch for:** The basePath confinement logic is the most security-critical piece. The implementation properly handles symlink resolution before comparing paths, but there's one edge case worth noting: when `basePath` is empty (cwd mode), there's no confinement check at all—any absolute path is accepted. This is documented but may be surprising. (confirmed)

**Verdict**: APPROVED

## High-level view

The connector slots into the existing driver pattern cleanly: `jsonFileConnector` implements the three-method `Connector` interface, `jsonFileClient` implements `Client` with `Execute`/`Close`. Type returns `"json-file"`, lifecycle is ephemeral, capabilities indicate query/exec support. The `boolSetting` helper added to helpers.go completes the settings extraction trio (string/int/bool) and is straightforward.

Path security uses defense-in-depth: `strings.Contains(path, "..")` catches the obvious traversal attempts before any filesystem call, then `filepath.EvalSymlinks` + prefix check ensures the resolved path stays within `basePath`. HTTP is gated behind `allowHttp: true` in settings—rejected with a clear validation error otherwise. Tests exercise both the positive and negative cases for all three security controls.

The caching layer is a simple TTL-based in-memory cache keyed on the resolved path (or URL). Cache invalidation happens on write. The implementation is reasonable for the stated use case (mid-flow data loading), though there's no eviction beyond TTL expiry—long-running processes with many unique paths would accumulate entries.

Error handling follows the ConnError taxonomy consistently: NotFound for missing files/404s, Validation for traversal attempts and invalid JSON, Upstream for I/O errors and 5xx responses, Timeout for context deadline exceeded.

<details>
<summary>Issues (3)</summary>

1. **No basePath confinement when basePath is empty** — When no basePath is configured, the connector operates in "cwd mode" and accepts any path including absolute paths anywhere on the filesystem. This is likely intentional (flexibility for trusted deployments) but should be documented in the usage examples. (confirmed)

2. **Unbounded cache growth** — The fileCache has no eviction policy beyond TTL expiry. For long-running engine processes loading many distinct files, this could accumulate entries indefinitely. Consider adding a max-entries limit or LRU eviction. (likely, non-blocking)

3. **Write operation returns resolved absolute path** — The write response includes `{"ok": true, "path": resolvedPath}` where resolvedPath is the absolute filesystem path. This exposes server-side path structure to flow authors. Consider returning the original relative path instead. (confirmed, non-blocking)

</details>

<details>
<summary>Details</summary>

## Path resolution and basePath confinement

The path resolution logic in `resolvePath()` handles three cases:

1. **Absolute path**: cleaned via `filepath.Clean`, then validated against basePath
2. **Relative path with basePath**: joined via `filepath.Join`, then validated
3. **Relative path without basePath**: resolved to absolute via `filepath.Abs`, no confinement check

The symlink handling is correct: `filepath.EvalSymlinks` is called on both the target path and the basePath before comparing prefixes, so a symlink pointing outside basePath is caught:

```go
realPath, err := filepath.EvalSymlinks(resolved)
// ...
realBasePath, err := filepath.EvalSymlinks(c.basePath)
// ...
if !strings.HasPrefix(realPath, realBasePath+string(filepath.Separator)) && realPath != realBasePath {
    return "", connect.NewConnError(connect.Validation, c.key, "read", "path escapes basePath", nil)
}
```

The `+string(filepath.Separator)` suffix prevents `/data` matching `/data-other` as a prefix.

For write operations on non-existent files, `EvalSymlinks` returns `os.IsNotExist`, and the code falls back to the cleaned path—safe because the `..` check has already run.

## Test coverage assessment

The test suite is comprehensive for the stated requirements:

**Covered:**
- Interface compliance (compile-time assertions)
- Type/Lifecycle/Capabilities accessors
- Registration in `All()`
- Local file read: valid JSON, with jsonPath, nested paths, non-existent file, invalid JSON
- Path traversal rejection: `..` prefix, `..` in middle
- basePath confinement with absolute paths
- HTTP: allowed/disallowed, 404, 500
- Write: valid, pretty, nested directory creation, URL rejection, traversal rejection, missing path/data
- JSONPath normalization edge cases
- Cache TTL behavior
- Ping operation
- Unsupported operation kind

**Not tested:**
- Symlink escape (symlink pointing outside basePath) — the verification doc claims this is tested but I don't see a dedicated test case. The code handles it correctly, but explicit test coverage would strengthen confidence.
- Permission denied errors — only path validation is tested, not actual filesystem permission failures.
- HTTP with non-JSON response (invalid content-type or malformed JSON) — the code validates JSON with `json.Valid()` and returns a Validation error, but no test exercises this path for HTTP responses.

</details>

<details>
<summary>File map</summary>

| File | Change |
|------|--------|
| `engine/internal/connect/drivers/helpers.go` | Added `boolSetting()` helper for boolean settings extraction |
| `engine/internal/connect/drivers/jsonfile.go` | New 440-line connector implementing read/write operations with path security |
| `engine/internal/connect/drivers/jsonfile_test.go` | 697-line test suite covering interface compliance, operations, and security |
| `engine/internal/connect/drivers/registry.go` | Added `newJSONFileConnector()` to `All()` slice |

Full diff: `git diff HEAD~1` in the json-file-connector worktree.

</details>
