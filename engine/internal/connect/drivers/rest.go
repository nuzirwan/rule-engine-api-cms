package drivers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"nzr-rules-engine/internal/connect"
)

// restConnector builds a single shared *http.Client (tuned Transport) for the
// "rest" and "http" connection types. The two types bind to the same driver;
// only the registered alias differs. The http.Client.Timeout is left UNSET so
// the resilience context (connect.resilientClient) governs the per-request
// deadline and composes with retry/breaker (slice-b-connections.md §2.3).
type restConnector struct {
	alias string
}

// newRESTConnector returns a rest/http Connector registered under alias.
func newRESTConnector(alias string) connect.Connector { return restConnector{alias: alias} }

// Type implements connect.Connector; it returns the registered alias.
func (c restConnector) Type() string { return c.alias }

// Lifecycle implements connect.Connector. REST is ephemeral — one connection per request.
func (c restConnector) Lifecycle() connect.Lifecycle { return connect.LifecycleEphemeral }

// Capabilities implements connect.Connector. REST supports query/exec via HTTP.
func (c restConnector) Capabilities() connect.Capability { return connect.CapQueryExec }

// Open builds the shared client from Settings (baseURL, default headers, and
// transport tunables). A resolved secret, when present, is exposed as a default
// bearer/credential header value only if the def names a header for it; by
// default no secret is placed in headers (the thin slice REST stub needs none).
func (c restConnector) Open(ctx context.Context, def connect.ConnectionDef) (connect.Client, error) {
	baseURL, _ := stringSetting(def.Settings, "baseURL")

	tr := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	}
	if v, ok := intSetting(def.Settings, "maxIdleConnsPerHost"); ok {
		tr.MaxIdleConnsPerHost = v
	}

	defaultHeaders := stringMapSetting(def.Settings, "headers")

	return &restClient{
		key:            def.Key,
		baseURL:        strings.TrimRight(baseURL, "/"),
		defaultHeaders: defaultHeaders,
		httpClient:     &http.Client{Transport: tr}, // Timeout unset: ctx governs
	}, nil
}

// restClient is the inner driver client over a shared *http.Client.
type restClient struct {
	key            string
	baseURL        string
	defaultHeaders map[string]string
	httpClient     *http.Client
}

// Execute handles the "http" op kind (and "ping" as a lightweight GET of the
// base/health path). It returns {status, headers, body}; a 4xx maps to
// Validation (404 => NotFound), a 5xx/429 to Upstream, a ctx deadline to Timeout.
func (c *restClient) Execute(ctx context.Context, op connect.Operation) (any, error) {
	switch op.Kind {
	case "http":
		return c.do(ctx, op)
	case "ping":
		return c.ping(ctx)
	default:
		return nil, connect.NewConnError(connect.Validation, c.key, op.Kind, "unsupported operation kind for rest", nil)
	}
}

// do issues one HTTP request built from the payload.
func (c *restClient) do(ctx context.Context, op connect.Operation) (any, error) {
	p := op.Payload
	method, _ := stringSetting(p, "method")
	if method == "" {
		method = http.MethodGet
	}
	method = strings.ToUpper(method)

	path, _ := stringSetting(p, "path")
	target, err := c.resolveURL(path, stringMapSetting(p, "query"))
	if err != nil {
		return nil, connect.NewConnError(connect.Validation, c.key, "http", "invalid request url", err)
	}

	var body io.Reader
	if raw, ok := p["body"]; ok && raw != nil {
		encoded, merr := json.Marshal(raw)
		if merr != nil {
			return nil, connect.NewConnError(connect.Validation, c.key, "http", "encode request body", merr)
		}
		body = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, connect.NewConnError(connect.Validation, c.key, "http", "build request", err)
	}
	for k, v := range c.defaultHeaders {
		req.Header.Set(k, v)
	}
	for k, v := range stringMapSetting(p, "headers") {
		req.Header.Set(k, v)
	}
	if body != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, c.classifyTransport("http", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, connect.NewConnError(connect.Upstream, c.key, "http", "read response body", err)
	}

	result := map[string]any{
		"status":  resp.StatusCode,
		"headers": flattenHeaders(resp.Header),
		"body":    decodeBody(resp.Header.Get("Content-Type"), raw),
	}
	if cerr := c.classifyStatus(resp.StatusCode); cerr != nil {
		return result, cerr
	}
	return result, nil
}

// ping issues a GET of the base URL as a cheap readiness probe.
func (c *restClient) ping(ctx context.Context) (any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/", nil)
	if err != nil {
		return nil, connect.NewConnError(connect.Upstream, c.key, "ping", "build probe request", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, c.classifyTransport("ping", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode >= 500 {
		return nil, connect.NewConnError(connect.Upstream, c.key, "ping", "rest probe unhealthy", nil)
	}
	return map[string]any{"ok": true, "status": resp.StatusCode}, nil
}

// Close is a no-op; the shared transport's idle conns are reaped by IdleConnTimeout.
func (c *restClient) Close() error {
	c.httpClient.CloseIdleConnections()
	return nil
}

// resolveURL joins the base URL with path (absolute path wins) and appends query.
func (c *restClient) resolveURL(path string, query map[string]string) (string, error) {
	full := path
	if !strings.HasPrefix(path, "http://") && !strings.HasPrefix(path, "https://") {
		if path != "" && !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		full = c.baseURL + path
	}
	u, err := url.Parse(full)
	if err != nil {
		return "", err
	}
	if len(query) > 0 {
		q := u.Query()
		for k, v := range query {
			q.Set(k, v)
		}
		u.RawQuery = q.Encode()
	}
	return u.String(), nil
}

// classifyStatus maps an HTTP status to the shared taxonomy.
func (c *restClient) classifyStatus(status int) error {
	switch {
	case status < 400:
		return nil
	case status == http.StatusNotFound:
		return connect.NewConnError(connect.NotFound, c.key, "http", "resource not found", nil)
	case status == http.StatusTooManyRequests:
		return connect.NewConnError(connect.Upstream, c.key, "http", "rate limited", nil)
	case status >= 500:
		return connect.NewConnError(connect.Upstream, c.key, "http", "upstream server error", nil)
	default: // other 4xx
		return connect.NewConnError(connect.Validation, c.key, "http", "upstream rejected request", nil)
	}
}

// classifyTransport maps a transport-level error: a ctx deadline => Timeout, any
// other network failure => Upstream.
func (c *restClient) classifyTransport(opKind string, err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return connect.NewConnError(connect.Timeout, c.key, opKind, "request deadline exceeded", err)
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return connect.NewConnError(connect.Timeout, c.key, opKind, "request timed out", err)
	}
	return connect.NewConnError(connect.Upstream, c.key, opKind, "rest transport error", err)
}

// decodeBody decodes a JSON body into a map/slice; otherwise returns a string.
func decodeBody(contentType string, raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	if strings.Contains(contentType, "application/json") {
		var v any
		if err := json.Unmarshal(raw, &v); err == nil {
			return v
		}
	}
	return string(raw)
}

// flattenHeaders collapses multi-value headers to their first value.
func flattenHeaders(h http.Header) map[string]any {
	out := make(map[string]any, len(h))
	for k, v := range h {
		if len(v) > 0 {
			out[k] = v[0]
		}
	}
	return out
}

// stringMapSetting reads a map[string]string from settings/payload, tolerating a
// map[string]any whose values are strings (the JSON-decoded shape).
func stringMapSetting(s map[string]any, key string) map[string]string {
	if s == nil {
		return nil
	}
	raw, ok := s[key]
	if !ok || raw == nil {
		return nil
	}
	switch m := raw.(type) {
	case map[string]string:
		return m
	case map[string]any:
		out := make(map[string]string, len(m))
		for k, v := range m {
			if str, ok := v.(string); ok {
				out[k] = str
			}
		}
		return out
	default:
		return nil
	}
}
