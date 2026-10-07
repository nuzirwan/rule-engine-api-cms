package drivers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nzr-rules-engine/internal/connect"
)

// Compile-time interface compliance assertions
var _ connect.Connector = jsonFileConnector{}
var _ connect.Client = (*jsonFileClient)(nil)

func TestJSONFileConnectorType(t *testing.T) {
	c := newJSONFileConnector()
	if got := c.Type(); got != "json-file" {
		t.Errorf("Type() = %q; want %q", got, "json-file")
	}
}

func TestJSONFileConnectorLifecycle(t *testing.T) {
	c := newJSONFileConnector()
	if got := c.Lifecycle(); got != connect.LifecycleEphemeral {
		t.Errorf("Lifecycle() = %v; want LifecycleEphemeral", got)
	}
}

func TestJSONFileConnectorCapabilities(t *testing.T) {
	c := newJSONFileConnector()
	if got := c.Capabilities(); got != connect.CapQueryExec {
		t.Errorf("Capabilities() = %v; want CapQueryExec", got)
	}
}

func TestJSONFileConnectorInAll(t *testing.T) {
	all := All()
	found := false
	for _, c := range all {
		if c.Type() == "json-file" {
			found = true
			break
		}
	}
	if !found {
		types := make([]string, len(all))
		for i, c := range all {
			types[i] = c.Type()
		}
		t.Errorf("json-file connector not found in All(); got types: %s", strings.Join(types, ", "))
	}
}

func TestJSONFileReadLocal(t *testing.T) {
	// Create temp directory with test files
	tmpDir := t.TempDir()

	// Create test JSON files
	validJSON := map[string]any{
		"name": "test",
		"products": []any{
			map[string]any{"id": 1, "name": "Widget"},
			map[string]any{"id": 2, "name": "Gadget"},
		},
	}
	validJSONData, _ := json.Marshal(validJSON)
	if err := os.WriteFile(filepath.Join(tmpDir, "data.json"), validJSONData, 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	// Create invalid JSON file
	if err := os.WriteFile(filepath.Join(tmpDir, "invalid.json"), []byte("{invalid json}"), 0644); err != nil {
		t.Fatalf("failed to write invalid test file: %v", err)
	}

	// Create subdir
	subDir := filepath.Join(tmpDir, "subdir")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("failed to create subdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "nested.json"), validJSONData, 0644); err != nil {
		t.Fatalf("failed to write nested test file: %v", err)
	}

	// Open connector with basePath
	connector := newJSONFileConnector()
	def := connect.ConnectionDef{
		Key:      "test-json",
		Type:     "json-file",
		Settings: map[string]any{"basePath": tmpDir},
	}
	client, err := connector.Open(context.Background(), def)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer client.Close()

	tests := []struct {
		name       string
		payload    map[string]any
		wantErr    bool
		errClass   connect.ErrClass
		checkValue func(any) bool
	}{
		{
			name:    "read valid JSON file",
			payload: map[string]any{"path": "data.json"},
			wantErr: false,
			checkValue: func(v any) bool {
				m, ok := v.(map[string]any)
				if !ok {
					return false
				}
				return m["name"] == "test"
			},
		},
		{
			name:    "read with jsonPath",
			payload: map[string]any{"path": "data.json", "jsonPath": "$.products"},
			wantErr: false,
			checkValue: func(v any) bool {
				arr, ok := v.([]any)
				return ok && len(arr) == 2
			},
		},
		{
			name:    "read with nested jsonPath",
			payload: map[string]any{"path": "data.json", "jsonPath": "$.products.0.name"},
			wantErr: false,
			checkValue: func(v any) bool {
				return v == "Widget"
			},
		},
		{
			name:    "read nested file",
			payload: map[string]any{"path": "subdir/nested.json"},
			wantErr: false,
			checkValue: func(v any) bool {
				m, ok := v.(map[string]any)
				return ok && m["name"] == "test"
			},
		},
		{
			name:     "file not found",
			payload:  map[string]any{"path": "nonexistent.json"},
			wantErr:  true,
			errClass: connect.NotFound,
		},
		{
			name:     "path traversal rejected",
			payload:  map[string]any{"path": "../etc/passwd"},
			wantErr:  true,
			errClass: connect.Validation,
		},
		{
			name:     "double dot in middle rejected",
			payload:  map[string]any{"path": "subdir/../../../etc/passwd"},
			wantErr:  true,
			errClass: connect.Validation,
		},
		{
			name:     "missing path",
			payload:  map[string]any{},
			wantErr:  true,
			errClass: connect.Validation,
		},
		{
			name:     "empty path",
			payload:  map[string]any{"path": ""},
			wantErr:  true,
			errClass: connect.Validation,
		},
		{
			name:     "invalid JSON file",
			payload:  map[string]any{"path": "invalid.json"},
			wantErr:  true,
			errClass: connect.Validation,
		},
		{
			name:    "non-existent jsonPath returns nil",
			payload: map[string]any{"path": "data.json", "jsonPath": "$.nonexistent"},
			wantErr: false,
			checkValue: func(v any) bool {
				return v == nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			op := connect.Operation{Kind: "read", Payload: tt.payload}
			result, err := client.Execute(context.Background(), op)

			if tt.wantErr {
				if err == nil {
					t.Errorf("Execute() expected error, got nil")
					return
				}
				var connErr *connect.ConnError
				if errors.As(err, &connErr) {
					if connErr.Class != tt.errClass {
						t.Errorf("Execute() error class = %v, want %v", connErr.Class, tt.errClass)
					}
				} else {
					t.Errorf("Execute() error is not ConnError: %v", err)
				}
				return
			}

			if err != nil {
				t.Errorf("Execute() unexpected error = %v", err)
				return
			}

			if tt.checkValue != nil && !tt.checkValue(result) {
				t.Errorf("Execute() result check failed, got %v", result)
			}
		})
	}
}

func TestJSONFileReadHTTP(t *testing.T) {
	// Create test server
	validJSON := map[string]any{"message": "hello from server"}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/valid.json":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(validJSON)
		case "/notfound.json":
			w.WriteHeader(http.StatusNotFound)
		case "/error.json":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	tests := []struct {
		name      string
		allowHttp bool
		path      string
		wantErr   bool
		errClass  connect.ErrClass
	}{
		{
			name:      "HTTP allowed, valid URL",
			allowHttp: true,
			path:      ts.URL + "/valid.json",
			wantErr:   false,
		},
		{
			name:      "HTTP disabled, URL rejected",
			allowHttp: false,
			path:      ts.URL + "/valid.json",
			wantErr:   true,
			errClass:  connect.Validation,
		},
		{
			name:      "HTTP allowed, 404",
			allowHttp: true,
			path:      ts.URL + "/notfound.json",
			wantErr:   true,
			errClass:  connect.NotFound,
		},
		{
			name:      "HTTP allowed, 500",
			allowHttp: true,
			path:      ts.URL + "/error.json",
			wantErr:   true,
			errClass:  connect.Upstream,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			connector := newJSONFileConnector()
			def := connect.ConnectionDef{
				Key:  "test-json",
				Type: "json-file",
				Settings: map[string]any{
					"basePath":  t.TempDir(),
					"allowHttp": tt.allowHttp,
				},
			}
			client, err := connector.Open(context.Background(), def)
			if err != nil {
				t.Fatalf("Open() error = %v", err)
			}
			defer client.Close()

			op := connect.Operation{Kind: "read", Payload: map[string]any{"path": tt.path}}
			_, err = client.Execute(context.Background(), op)

			if tt.wantErr {
				if err == nil {
					t.Errorf("Execute() expected error, got nil")
					return
				}
				var connErr *connect.ConnError
				if errors.As(err, &connErr) {
					if connErr.Class != tt.errClass {
						t.Errorf("Execute() error class = %v, want %v", connErr.Class, tt.errClass)
					}
				}
				return
			}

			if err != nil {
				t.Errorf("Execute() unexpected error = %v", err)
			}
		})
	}
}

func TestJSONFileWrite(t *testing.T) {
	tmpDir := t.TempDir()

	connector := newJSONFileConnector()
	def := connect.ConnectionDef{
		Key:      "test-json",
		Type:     "json-file",
		Settings: map[string]any{"basePath": tmpDir},
	}
	client, err := connector.Open(context.Background(), def)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer client.Close()

	tests := []struct {
		name     string
		payload  map[string]any
		wantErr  bool
		errClass connect.ErrClass
		verify   func() bool
	}{
		{
			name: "write valid JSON",
			payload: map[string]any{
				"path": "output.json",
				"data": map[string]any{"result": 42},
			},
			wantErr: false,
			verify: func() bool {
				data, err := os.ReadFile(filepath.Join(tmpDir, "output.json"))
				if err != nil {
					return false
				}
				var v map[string]any
				if err := json.Unmarshal(data, &v); err != nil {
					return false
				}
				return v["result"] == float64(42)
			},
		},
		{
			name: "write with pretty",
			payload: map[string]any{
				"path":   "pretty.json",
				"data":   map[string]any{"key": "value"},
				"pretty": true,
			},
			wantErr: false,
			verify: func() bool {
				data, err := os.ReadFile(filepath.Join(tmpDir, "pretty.json"))
				if err != nil {
					return false
				}
				// Pretty output should contain newlines
				return strings.Contains(string(data), "\n")
			},
		},
		{
			name: "write to nested path creates dirs",
			payload: map[string]any{
				"path": "new/nested/dir/file.json",
				"data": map[string]any{"nested": true},
			},
			wantErr: false,
			verify: func() bool {
				_, err := os.Stat(filepath.Join(tmpDir, "new/nested/dir/file.json"))
				return err == nil
			},
		},
		{
			name: "write to URL rejected",
			payload: map[string]any{
				"path": "http://example.com/file.json",
				"data": map[string]any{},
			},
			wantErr:  true,
			errClass: connect.Validation,
		},
		{
			name: "write path traversal rejected",
			payload: map[string]any{
				"path": "../outside.json",
				"data": map[string]any{},
			},
			wantErr:  true,
			errClass: connect.Validation,
		},
		{
			name: "write missing path",
			payload: map[string]any{
				"data": map[string]any{},
			},
			wantErr:  true,
			errClass: connect.Validation,
		},
		{
			name: "write missing data",
			payload: map[string]any{
				"path": "nodata.json",
			},
			wantErr:  true,
			errClass: connect.Validation,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			op := connect.Operation{Kind: "write", Payload: tt.payload}
			_, err := client.Execute(context.Background(), op)

			if tt.wantErr {
				if err == nil {
					t.Errorf("Execute() expected error, got nil")
					return
				}
				var connErr *connect.ConnError
				if errors.As(err, &connErr) {
					if connErr.Class != tt.errClass {
						t.Errorf("Execute() error class = %v, want %v", connErr.Class, tt.errClass)
					}
				}
				return
			}

			if err != nil {
				t.Errorf("Execute() unexpected error = %v", err)
				return
			}

			if tt.verify != nil && !tt.verify() {
				t.Errorf("Execute() verification failed")
			}
		})
	}
}

func TestJSONPathApplication(t *testing.T) {
	tests := []struct {
		name     string
		jsonPath string
		want     string
	}{
		{name: "strip $.", jsonPath: "$.products", want: "products"},
		{name: "strip $", jsonPath: "$products", want: "products"},
		{name: "no prefix", jsonPath: "products", want: "products"},
		{name: "nested path", jsonPath: "$.products.0.name", want: "products.0.name"},
		{name: "empty returns empty", jsonPath: "", want: ""},
		{name: "just $ returns empty", jsonPath: "$", want: ""},
		{name: "with spaces", jsonPath: "  $.foo  ", want: "foo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeJSONPath(tt.jsonPath)
			if got != tt.want {
				t.Errorf("normalizeJSONPath(%q) = %q, want %q", tt.jsonPath, got, tt.want)
			}
		})
	}
}

func TestFileCache(t *testing.T) {
	// Create temp directory with a test file
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "cached.json")
	originalData := map[string]any{"version": 1}
	data, _ := json.Marshal(originalData)
	if err := os.WriteFile(testFile, data, 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	// Open connector with caching enabled (short TTL for testing)
	connector := newJSONFileConnector()
	def := connect.ConnectionDef{
		Key:  "test-json",
		Type: "json-file",
		Settings: map[string]any{
			"basePath":     tmpDir,
			"cacheSeconds": 2, // 2 second TTL
		},
	}
	client, err := connector.Open(context.Background(), def)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer client.Close()

	// First read
	op := connect.Operation{Kind: "read", Payload: map[string]any{"path": "cached.json"}}
	result1, err := client.Execute(context.Background(), op)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	// Modify the file
	newData := map[string]any{"version": 2}
	data, _ = json.Marshal(newData)
	if err := os.WriteFile(testFile, data, 0644); err != nil {
		t.Fatalf("failed to update test file: %v", err)
	}

	// Second read (should return cached value)
	result2, err := client.Execute(context.Background(), op)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	// Both should have version 1 (cached)
	m1 := result1.(map[string]any)
	m2 := result2.(map[string]any)
	if m1["version"] != float64(1) || m2["version"] != float64(1) {
		t.Errorf("expected cached value version 1, got %v and %v", m1["version"], m2["version"])
	}

	// Wait for cache to expire
	time.Sleep(3 * time.Second)

	// Third read (should return new value)
	result3, err := client.Execute(context.Background(), op)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	m3 := result3.(map[string]any)
	if m3["version"] != float64(2) {
		t.Errorf("expected new value version 2, got %v", m3["version"])
	}
}

func TestJSONFilePing(t *testing.T) {
	connector := newJSONFileConnector()
	def := connect.ConnectionDef{
		Key:      "test-json",
		Type:     "json-file",
		Settings: map[string]any{},
	}
	client, err := connector.Open(context.Background(), def)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer client.Close()

	op := connect.Operation{Kind: "ping", Payload: nil}
	result, err := client.Execute(context.Background(), op)
	if err != nil {
		t.Errorf("ping should not error: %v", err)
	}
	m, ok := result.(map[string]any)
	if !ok || m["ok"] != true {
		t.Errorf("ping should return {ok: true}, got %v", result)
	}
}

func TestJSONFileUnsupportedKind(t *testing.T) {
	connector := newJSONFileConnector()
	def := connect.ConnectionDef{
		Key:      "test-json",
		Type:     "json-file",
		Settings: map[string]any{},
	}
	client, err := connector.Open(context.Background(), def)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer client.Close()

	op := connect.Operation{Kind: "invalid", Payload: nil}
	_, err = client.Execute(context.Background(), op)
	if err == nil {
		t.Errorf("expected error for unsupported kind")
		return
	}
	var connErr *connect.ConnError
	if errors.As(err, &connErr) {
		if connErr.Class != connect.Validation {
			t.Errorf("expected Validation error, got %v", connErr.Class)
		}
	}
}

func TestJSONFileBasePathConfinement(t *testing.T) {
	// Create two temp directories
	allowedDir := t.TempDir()
	outsideDir := t.TempDir()

	// Create a file outside the allowed basePath
	outsideFile := filepath.Join(outsideDir, "secret.json")
	if err := os.WriteFile(outsideFile, []byte(`{"secret": "value"}`), 0644); err != nil {
		t.Fatalf("failed to write outside file: %v", err)
	}

	// Open connector with basePath set to allowedDir
	connector := newJSONFileConnector()
	def := connect.ConnectionDef{
		Key:      "test-json",
		Type:     "json-file",
		Settings: map[string]any{"basePath": allowedDir},
	}
	client, err := connector.Open(context.Background(), def)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer client.Close()

	// Try to read the outside file using absolute path
	op := connect.Operation{Kind: "read", Payload: map[string]any{"path": outsideFile}}
	_, err = client.Execute(context.Background(), op)
	if err == nil {
		t.Errorf("expected error when reading outside basePath")
		return
	}
	var connErr *connect.ConnError
	if errors.As(err, &connErr) {
		if connErr.Class != connect.Validation {
			t.Errorf("expected Validation error, got %v", connErr.Class)
		}
	}
}

func TestBoolSetting(t *testing.T) {
	tests := []struct {
		name     string
		settings map[string]any
		key      string
		wantVal  bool
		wantOk   bool
	}{
		{
			name:     "true value",
			settings: map[string]any{"flag": true},
			key:      "flag",
			wantVal:  true,
			wantOk:   true,
		},
		{
			name:     "false value",
			settings: map[string]any{"flag": false},
			key:      "flag",
			wantVal:  false,
			wantOk:   true,
		},
		{
			name:     "missing key",
			settings: map[string]any{"other": true},
			key:      "flag",
			wantVal:  false,
			wantOk:   false,
		},
		{
			name:     "nil settings",
			settings: nil,
			key:      "flag",
			wantVal:  false,
			wantOk:   false,
		},
		{
			name:     "wrong type",
			settings: map[string]any{"flag": "true"},
			key:      "flag",
			wantVal:  false,
			wantOk:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			val, ok := boolSetting(tt.settings, tt.key)
			if val != tt.wantVal || ok != tt.wantOk {
				t.Errorf("boolSetting() = (%v, %v), want (%v, %v)", val, ok, tt.wantVal, tt.wantOk)
			}
		})
	}
}
