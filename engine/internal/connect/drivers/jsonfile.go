package drivers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"

	"nzr-rules-engine/internal/connect"
)

// jsonFileConnector builds clients for the "json-file" connection type.
// It allows flows to load JSON data from local files or HTTP(S) URLs mid-execution.
type jsonFileConnector struct{}

// newJSONFileConnector returns the json-file Connector.
func newJSONFileConnector() connect.Connector { return jsonFileConnector{} }

// Type implements connect.Connector.
func (jsonFileConnector) Type() string { return "json-file" }

// Lifecycle implements connect.Connector. JSON file reads are stateless, ephemeral ops.
func (jsonFileConnector) Lifecycle() connect.Lifecycle { return connect.LifecycleEphemeral }

// Capabilities implements connect.Connector. JSON file supports query/exec via read/write.
func (jsonFileConnector) Capabilities() connect.Capability { return connect.CapQueryExec }

// Open builds a jsonFileClient from the def's Settings.
// Settings:
//   - basePath (string): root directory for local file reads (default: cwd)
//   - allowHttp (bool): whether HTTP(S) URLs are permitted (default: false)
//   - cacheSeconds (int): cache TTL in seconds (0 = no caching)
func (jsonFileConnector) Open(ctx context.Context, def connect.ConnectionDef) (connect.Client, error) {
	basePath, _ := stringSetting(def.Settings, "basePath")
	allowHttp, _ := boolSetting(def.Settings, "allowHttp")
	cacheSeconds, _ := intSetting(def.Settings, "cacheSeconds")

	// Resolve basePath to absolute path if provided
	if basePath != "" {
		abs, err := filepath.Abs(basePath)
		if err != nil {
			return nil, connect.NewConnError(connect.Validation, def.Key, "", "resolve basePath", err)
		}
		basePath = abs
	}

	var cache *fileCache
	var cacheTTL time.Duration
	if cacheSeconds > 0 {
		cacheTTL = time.Duration(cacheSeconds) * time.Second
		cache = &fileCache{
			entries: make(map[string]*cacheEntry),
			ttl:     cacheTTL,
		}
	}

	// Shared HTTP client for URL fetches (only used when allowHttp is true)
	tr := &http.Transport{
		MaxIdleConns:        20,
		MaxIdleConnsPerHost: 5,
		IdleConnTimeout:     90 * time.Second,
	}
	httpClient := &http.Client{Transport: tr}

	return &jsonFileClient{
		key:        def.Key,
		basePath:   basePath,
		allowHttp:  allowHttp,
		cacheTTL:   cacheTTL,
		cache:      cache,
		httpClient: httpClient,
	}, nil
}

// jsonFileClient is the inner driver client for JSON file operations.
type jsonFileClient struct {
	key        string
	basePath   string        // root directory for local file reads
	allowHttp  bool          // whether HTTP(S) URLs are permitted
	cacheTTL   time.Duration // cache duration (0 = no caching)
	cache      *fileCache    // shared cache instance (nil if caching disabled)
	httpClient *http.Client  // shared HTTP client for URL fetches
}

// fileCache is a simple TTL cache for parsed JSON data.
type fileCache struct {
	mu      sync.RWMutex
	entries map[string]*cacheEntry
	ttl     time.Duration
}

// cacheEntry holds cached data with expiration.
type cacheEntry struct {
	data      []byte
	expiresAt time.Time
}

// get returns cached data if valid, nil otherwise.
func (c *fileCache) get(key string) []byte {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.entries[key]
	if !ok || time.Now().After(entry.expiresAt) {
		return nil
	}
	return entry.data
}

// set stores data in the cache with TTL.
func (c *fileCache) set(key string, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = &cacheEntry{
		data:      data,
		expiresAt: time.Now().Add(c.ttl),
	}
}

// Execute dispatches on op.Kind: read, write, or ping.
func (c *jsonFileClient) Execute(ctx context.Context, op connect.Operation) (any, error) {
	switch op.Kind {
	case "read":
		return c.readFile(ctx, op)
	case "write":
		return c.writeFile(ctx, op)
	case "ping":
		return map[string]any{"ok": true}, nil
	default:
		return nil, connect.NewConnError(connect.Validation, c.key, op.Kind, "unsupported operation kind for json-file", nil)
	}
}

// readFile reads and parses a JSON file, optionally applying a jsonPath selector.
// Payload:
//   - path (string, required): relative path (resolved against basePath) or absolute URL (if allowHttp)
//   - jsonPath (string, optional): GJSON path to extract a subset of the data
func (c *jsonFileClient) readFile(ctx context.Context, op connect.Operation) (any, error) {
	path, ok := stringSetting(op.Payload, "path")
	if !ok || strings.TrimSpace(path) == "" {
		return nil, connect.NewConnError(connect.Validation, c.key, "read", "missing path in payload", nil)
	}
	path = strings.TrimSpace(path)

	jsonPath, _ := stringSetting(op.Payload, "jsonPath")

	// Determine if it's a URL or local file
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return c.readFromURL(ctx, path, jsonPath)
	}
	return c.readFromLocal(ctx, path, jsonPath)
}

// readFromURL fetches JSON from an HTTP(S) URL.
func (c *jsonFileClient) readFromURL(ctx context.Context, url, jsonPath string) (any, error) {
	if !c.allowHttp {
		return nil, connect.NewConnError(connect.Validation, c.key, "read", "HTTP URLs not allowed; set allowHttp: true", nil)
	}

	// Check cache first
	if c.cache != nil {
		if cached := c.cache.get(url); cached != nil {
			return c.applyJSONPath(cached, jsonPath)
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, connect.NewConnError(connect.Validation, c.key, "read", "build request", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, c.classifyTransport("read", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, connect.NewConnError(connect.NotFound, c.key, "read", fmt.Sprintf("URL not found: %s", url), nil)
	}
	if resp.StatusCode >= 400 {
		return nil, connect.NewConnError(connect.Upstream, c.key, "read", fmt.Sprintf("HTTP error %d", resp.StatusCode), nil)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, connect.NewConnError(connect.Upstream, c.key, "read", "read response body", err)
	}

	// Validate JSON
	if !json.Valid(data) {
		return nil, connect.NewConnError(connect.Validation, c.key, "read", "invalid JSON response", nil)
	}

	// Cache the result
	if c.cache != nil {
		c.cache.set(url, data)
	}

	return c.applyJSONPath(data, jsonPath)
}

// readFromLocal reads JSON from a local file.
func (c *jsonFileClient) readFromLocal(ctx context.Context, path, jsonPath string) (any, error) {
	// Security: reject paths containing ".." before any resolution
	if strings.Contains(path, "..") {
		return nil, connect.NewConnError(connect.Validation, c.key, "read", "path traversal not allowed", nil)
	}

	// Resolve the path
	resolvedPath, err := c.resolvePath(path)
	if err != nil {
		return nil, err
	}

	// Check cache first (using resolved path as key)
	if c.cache != nil {
		if cached := c.cache.get(resolvedPath); cached != nil {
			return c.applyJSONPath(cached, jsonPath)
		}
	}

	// Check context deadline
	if err := ctx.Err(); err != nil {
		return nil, connect.NewConnError(connect.Timeout, c.key, "read", "context deadline exceeded", err)
	}

	// Read the file
	data, err := os.ReadFile(resolvedPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, connect.NewConnError(connect.NotFound, c.key, "read", fmt.Sprintf("file not found: %s", path), err)
		}
		if os.IsPermission(err) {
			return nil, connect.NewConnError(connect.Validation, c.key, "read", fmt.Sprintf("permission denied: %s", path), err)
		}
		return nil, connect.NewConnError(connect.Upstream, c.key, "read", "read file", err)
	}

	// Validate JSON
	if !json.Valid(data) {
		return nil, connect.NewConnError(connect.Validation, c.key, "read", "invalid JSON in file", nil)
	}

	// Cache the result
	if c.cache != nil {
		c.cache.set(resolvedPath, data)
	}

	return c.applyJSONPath(data, jsonPath)
}

// writeFile writes data to a JSON file.
// Payload:
//   - path (string, required): relative path resolved against basePath
//   - data (any, required): the value to JSON-encode and write
//   - pretty (bool, optional): indent the output (default: false)
func (c *jsonFileClient) writeFile(ctx context.Context, op connect.Operation) (any, error) {
	path, ok := stringSetting(op.Payload, "path")
	if !ok || strings.TrimSpace(path) == "" {
		return nil, connect.NewConnError(connect.Validation, c.key, "write", "missing path in payload", nil)
	}
	path = strings.TrimSpace(path)

	// Reject URLs for write (local-only)
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return nil, connect.NewConnError(connect.Validation, c.key, "write", "write to URL not supported", nil)
	}

	// Security: reject paths containing ".."
	if strings.Contains(path, "..") {
		return nil, connect.NewConnError(connect.Validation, c.key, "write", "path traversal not allowed", nil)
	}

	data, ok := op.Payload["data"]
	if !ok {
		return nil, connect.NewConnError(connect.Validation, c.key, "write", "missing data in payload", nil)
	}

	pretty, _ := boolSetting(op.Payload, "pretty")

	// Resolve the path
	resolvedPath, err := c.resolvePath(path)
	if err != nil {
		return nil, err
	}

	// Encode data as JSON
	var encoded []byte
	if pretty {
		encoded, err = json.MarshalIndent(data, "", "  ")
	} else {
		encoded, err = json.Marshal(data)
	}
	if err != nil {
		return nil, connect.NewConnError(connect.Validation, c.key, "write", "encode JSON", err)
	}

	// Check context deadline
	if err := ctx.Err(); err != nil {
		return nil, connect.NewConnError(connect.Timeout, c.key, "write", "context deadline exceeded", err)
	}

	// Ensure parent directory exists
	dir := filepath.Dir(resolvedPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, connect.NewConnError(connect.Upstream, c.key, "write", "create directory", err)
	}

	// Write the file
	if err := os.WriteFile(resolvedPath, encoded, 0644); err != nil {
		if os.IsPermission(err) {
			return nil, connect.NewConnError(connect.Validation, c.key, "write", fmt.Sprintf("permission denied: %s", path), err)
		}
		return nil, connect.NewConnError(connect.Upstream, c.key, "write", "write file", err)
	}

	// Invalidate cache if present
	if c.cache != nil {
		c.cache.mu.Lock()
		delete(c.cache.entries, resolvedPath)
		c.cache.mu.Unlock()
	}

	return map[string]any{"ok": true, "path": resolvedPath}, nil
}

// resolvePath resolves a relative path against basePath and validates it's within bounds.
func (c *jsonFileClient) resolvePath(path string) (string, error) {
	var resolved string

	if filepath.IsAbs(path) {
		resolved = filepath.Clean(path)
	} else if c.basePath != "" {
		resolved = filepath.Join(c.basePath, path)
	} else {
		// No basePath, resolve relative to cwd
		abs, err := filepath.Abs(path)
		if err != nil {
			return "", connect.NewConnError(connect.Validation, c.key, "read", "resolve path", err)
		}
		resolved = abs
	}

	// Resolve symlinks to get the real path
	realPath, err := filepath.EvalSymlinks(resolved)
	if err != nil {
		// If file doesn't exist yet (write case), use the cleaned path
		if os.IsNotExist(err) {
			realPath = resolved
		} else {
			return "", connect.NewConnError(connect.Validation, c.key, "read", "resolve symlinks", err)
		}
	}

	// Security: ensure the resolved path is within basePath (if basePath is set)
	if c.basePath != "" {
		// Also resolve basePath symlinks for comparison
		realBasePath, err := filepath.EvalSymlinks(c.basePath)
		if err != nil {
			// basePath might not exist, use the original
			realBasePath = c.basePath
		}

		// Ensure realPath starts with realBasePath
		if !strings.HasPrefix(realPath, realBasePath+string(filepath.Separator)) && realPath != realBasePath {
			return "", connect.NewConnError(connect.Validation, c.key, "read", "path escapes basePath", nil)
		}
	}

	return realPath, nil
}

// applyJSONPath extracts data using a GJSON path, or returns the full document if path is empty.
func (c *jsonFileClient) applyJSONPath(data []byte, jsonPath string) (any, error) {
	if jsonPath == "" {
		var v any
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, connect.NewConnError(connect.Validation, c.key, "read", "parse JSON", err)
		}
		return v, nil
	}

	// Normalize JSONPath: strip leading $. or $
	query := normalizeJSONPath(jsonPath)
	result := gjson.GetBytes(data, query)
	if !result.Exists() {
		// Path not found returns nil (not an error)
		return nil, nil
	}
	return result.Value(), nil
}

// normalizeJSONPath converts JSONPath syntax to GJSON syntax.
// Strips leading $. or $ prefix.
func normalizeJSONPath(path string) string {
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

// classifyTransport maps a transport-level error to the shared taxonomy.
func (c *jsonFileClient) classifyTransport(opKind string, err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return connect.NewConnError(connect.Timeout, c.key, opKind, "request deadline exceeded", err)
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return connect.NewConnError(connect.Timeout, c.key, opKind, "request timed out", err)
	}
	return connect.NewConnError(connect.Upstream, c.key, opKind, "http transport error", err)
}

// Close releases resources; the shared transport's idle conns are reaped by IdleConnTimeout.
func (c *jsonFileClient) Close() error {
	if c.httpClient != nil {
		c.httpClient.CloseIdleConnections()
	}
	return nil
}
