package flow

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestExtractInput(t *testing.T) {
	tests := []struct {
		name       string
		pathParams map[string]string
		spec       TriggerInput
		body       string
		query      string
		headers    map[string]string
		want       map[string]any
		wantErr    bool
	}{
		{
			name:       "path params only",
			pathParams: map[string]string{"id": "123", "name": "test"},
			spec:       TriggerInput{Params: []string{"id", "name"}},
			want:       map[string]any{"id": "123", "name": "test"},
		},
		{
			name:       "query params extraction",
			pathParams: map[string]string{"id": "123"},
			spec:       TriggerInput{Query: []string{"page", "limit"}},
			query:      "page=1&limit=10&ignored=yes",
			want:       map[string]any{"id": "123", "page": "1", "limit": "10"},
		},
		{
			name:       "query params - missing optional",
			pathParams: map[string]string{},
			spec:       TriggerInput{Query: []string{"page", "limit"}},
			query:      "page=1",
			want:       map[string]any{"page": "1"},
		},
		{
			name:       "header extraction case-insensitive",
			pathParams: map[string]string{},
			spec:       TriggerInput{Headers: []string{"X-Tenant-Id", "Authorization"}},
			headers:    map[string]string{"x-tenant-id": "tenant-1", "authorization": "Bearer token"},
			want:       map[string]any{"X-Tenant-Id": "tenant-1", "Authorization": "Bearer token"},
		},
		{
			name:       "body extraction when spec.Body=true",
			pathParams: map[string]string{},
			spec:       TriggerInput{Body: true},
			body:       `{"user": {"name": "Alice", "age": 30}}`,
			want:       map[string]any{"body": map[string]any{"user": map[string]any{"name": "Alice", "age": float64(30)}}},
		},
		{
			name:       "body not extracted when spec.Body=false",
			pathParams: map[string]string{"id": "123"},
			spec:       TriggerInput{Body: false},
			body:       `{"ignored": true}`,
			want:       map[string]any{"id": "123"},
		},
		{
			name:       "empty body when spec.Body=true",
			pathParams: map[string]string{},
			spec:       TriggerInput{Body: true},
			body:       "",
			want:       map[string]any{},
		},
		{
			name:       "combined: path + query + headers + body",
			pathParams: map[string]string{"id": "123"},
			spec: TriggerInput{
				Params:  []string{"id"},
				Query:   []string{"expand"},
				Headers: []string{"X-Request-Id"},
				Body:    true,
			},
			query:   "expand=true",
			headers: map[string]string{"X-Request-Id": "req-abc"},
			body:    `{"data": "value"}`,
			want: map[string]any{
				"id":           "123",
				"expand":       "true",
				"X-Request-Id": "req-abc",
				"body":         map[string]any{"data": "value"},
			},
		},
		{
			name:       "invalid JSON body",
			pathParams: map[string]string{},
			spec:       TriggerInput{Body: true},
			body:       `{invalid`,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Build request
			var bodyReader io.Reader
			contentLength := int64(0)
			if tt.body != "" {
				bodyReader = strings.NewReader(tt.body)
				contentLength = int64(len(tt.body))
			}
			req := httptest.NewRequest(http.MethodPost, "/test?"+tt.query, bodyReader)
			req.ContentLength = contentLength
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}

			got, err := ExtractInput(req, tt.pathParams, tt.spec)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			// Compare maps
			gotJSON, _ := json.Marshal(got)
			wantJSON, _ := json.Marshal(tt.want)
			if string(gotJSON) != string(wantJSON) {
				t.Errorf("ExtractInput() = %s, want %s", gotJSON, wantJSON)
			}
		})
	}
}

func TestCompileSchema(t *testing.T) {
	tests := []struct {
		name    string
		schema  string
		wantNil bool
		wantErr bool
	}{
		{
			name:    "empty schema returns nil",
			schema:  "",
			wantNil: true,
		},
		{
			name: "valid simple schema",
			schema: `{
				"type": "object",
				"properties": {
					"id": {"type": "string"}
				}
			}`,
		},
		{
			name: "valid schema with required",
			schema: `{
				"type": "object",
				"required": ["id"],
				"properties": {
					"id": {"type": "string"}
				}
			}`,
		},
		{
			name:    "invalid JSON",
			schema:  `{invalid`,
			wantErr: true,
		},
		{
			name:    "invalid schema - bad type value",
			schema:  `{"type": 123}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var raw json.RawMessage
			if tt.schema != "" {
				raw = json.RawMessage(tt.schema)
			}

			schema, err := CompileSchema(raw)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				if !errors.Is(err, ErrValidation) {
					t.Errorf("expected ClassValidation error, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantNil && schema != nil {
				t.Error("expected nil schema")
			}
			if !tt.wantNil && schema == nil {
				t.Error("expected non-nil schema")
			}
		})
	}
}

func TestValidateInput(t *testing.T) {
	tests := []struct {
		name      string
		schema    string
		input     map[string]any
		wantErr   bool
		wantCode  string
		wantField string
	}{
		{
			name:   "nil schema skips validation",
			schema: "",
			input:  map[string]any{"anything": "goes"},
		},
		{
			name: "valid input passes",
			schema: `{
				"type": "object",
				"properties": {
					"id": {"type": "string"}
				}
			}`,
			input: map[string]any{"id": "123"},
		},
		{
			name: "required field missing",
			schema: `{
				"type": "object",
				"required": ["id"],
				"properties": {
					"id": {"type": "string"}
				}
			}`,
			input:     map[string]any{},
			wantErr:   true,
			wantCode:  "required",
			wantField: "",
		},
		{
			name: "type mismatch - expected string got number",
			schema: `{
				"type": "object",
				"properties": {
					"id": {"type": "string"}
				}
			}`,
			input:     map[string]any{"id": 123},
			wantErr:   true,
			wantCode:  "type",
			wantField: "id",
		},
		{
			name: "pattern fail",
			schema: `{
				"type": "object",
				"properties": {
					"code": {"type": "string", "pattern": "^[A-Z]{3}$"}
				}
			}`,
			input:     map[string]any{"code": "abc"},
			wantErr:   true,
			wantCode:  "pattern",
			wantField: "code",
		},
		{
			name: "enum fail",
			schema: `{
				"type": "object",
				"properties": {
					"status": {"type": "string", "enum": ["active", "inactive"]}
				}
			}`,
			input:     map[string]any{"status": "pending"},
			wantErr:   true,
			wantCode:  "enum",
			wantField: "status",
		},
		{
			name: "nested body validation - required missing",
			schema: `{
				"type": "object",
				"properties": {
					"body": {
						"type": "object",
						"required": ["user"],
						"properties": {
							"user": {
								"type": "object",
								"required": ["name"],
								"properties": {
									"name": {"type": "string"}
								}
							}
						}
					}
				}
			}`,
			input:     map[string]any{"body": map[string]any{}},
			wantErr:   true,
			wantCode:  "required",
			wantField: "body",
		},
		{
			name: "nested body validation - type mismatch",
			schema: `{
				"type": "object",
				"properties": {
					"body": {
						"type": "object",
						"properties": {
							"count": {"type": "integer"}
						}
					}
				}
			}`,
			input:     map[string]any{"body": map[string]any{"count": "not-a-number"}},
			wantErr:   true,
			wantCode:  "type",
			wantField: "body.count",
		},
		{
			name: "minimum constraint fail",
			schema: `{
				"type": "object",
				"properties": {
					"age": {"type": "integer", "minimum": 18}
				}
			}`,
			input:     map[string]any{"age": 16},
			wantErr:   true,
			wantCode:  "minimum",
			wantField: "age",
		},
		{
			name: "maxLength constraint fail",
			schema: `{
				"type": "object",
				"properties": {
					"code": {"type": "string", "maxLength": 3}
				}
			}`,
			input:     map[string]any{"code": "TOOLONG"},
			wantErr:   true,
			wantCode:  "max_length",
			wantField: "code",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var schema *jsonschema.Schema
			if tt.schema != "" {
				var err error
				schema, err = CompileSchema(json.RawMessage(tt.schema))
				if err != nil {
					t.Fatalf("failed to compile schema: %v", err)
				}
			}

			err := ValidateInput(tt.input, schema)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
					return
				}
				if !errors.Is(err, ErrValidation) {
					t.Errorf("expected error to wrap ErrValidation, got %v", err)
				}
				verrs, ok := err.(InputValidationErrors)
				if !ok {
					t.Fatalf("expected InputValidationErrors, got %T", err)
				}
				if len(verrs) == 0 {
					t.Error("expected at least one validation error")
					return
				}
				if tt.wantCode != "" && verrs[0].Code != tt.wantCode {
					t.Errorf("error code = %q, want %q", verrs[0].Code, tt.wantCode)
				}
				if tt.wantField != "" && verrs[0].Field != tt.wantField {
					t.Errorf("error field = %q, want %q", verrs[0].Field, tt.wantField)
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestExtractAndValidate(t *testing.T) {
	tests := []struct {
		name       string
		pathParams map[string]string
		spec       TriggerInput
		schema     string
		body       string
		wantErr    bool
	}{
		{
			name:       "no schema - extraction only",
			pathParams: map[string]string{"id": "123"},
			spec:       TriggerInput{Params: []string{"id"}},
			schema:     "",
		},
		{
			name:       "valid with schema",
			pathParams: map[string]string{"id": "123"},
			spec:       TriggerInput{Params: []string{"id"}},
			schema: `{
				"type": "object",
				"required": ["id"],
				"properties": {
					"id": {"type": "string"}
				}
			}`,
		},
		{
			name:       "validation fails - missing required",
			pathParams: map[string]string{},
			spec:       TriggerInput{},
			schema: `{
				"type": "object",
				"required": ["id"],
				"properties": {
					"id": {"type": "string"}
				}
			}`,
			wantErr: true,
		},
		{
			name:       "body validation with schema",
			pathParams: map[string]string{},
			spec:       TriggerInput{Body: true},
			schema: `{
				"type": "object",
				"properties": {
					"body": {
						"type": "object",
						"required": ["name"],
						"properties": {
							"name": {"type": "string"}
						}
					}
				}
			}`,
			body:    `{"name": "Alice"}`,
			wantErr: false,
		},
		{
			name:       "body validation fails",
			pathParams: map[string]string{},
			spec:       TriggerInput{Body: true},
			schema: `{
				"type": "object",
				"properties": {
					"body": {
						"type": "object",
						"required": ["name"],
						"properties": {
							"name": {"type": "string"}
						}
					}
				}
			}`,
			body:    `{"age": 30}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var schema *jsonschema.Schema
			if tt.schema != "" {
				var err error
				schema, err = CompileSchema(json.RawMessage(tt.schema))
				if err != nil {
					t.Fatalf("failed to compile schema: %v", err)
				}
			}

			var bodyReader io.Reader
			contentLength := int64(0)
			if tt.body != "" {
				bodyReader = bytes.NewBufferString(tt.body)
				contentLength = int64(len(tt.body))
			}
			req := httptest.NewRequest(http.MethodPost, "/test", bodyReader)
			req.ContentLength = contentLength

			_, err := ExtractAndValidate(req, tt.pathParams, tt.spec, schema)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}
