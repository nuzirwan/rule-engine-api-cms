package drivers

import (
	"testing"

	"nzr-rules-engine/internal/connect"
)

// TestSecretSchemaProvider verifies all connectors implement SecretSchemaProvider
// and return expected schema structures.
func TestSecretSchemaProvider(t *testing.T) {
	connectors := All()

	tests := []struct {
		connType       string
		expectedFields []string // expected secret field names, empty for nil schema
		isDynamic      bool     // true if SecretSchema() returns nil (dynamic secrets)
	}{
		{"postgres", []string{"password"}, false},
		{"mysql", []string{"password"}, false},
		{"valkey", []string{"password"}, false},
		{"kafka", []string{"username", "password"}, false},
		{"rabbitmq", []string{"username", "password"}, false},
		{"rest", nil, true},  // REST has dynamic secrets
		{"http", nil, true},  // http is an alias for REST
	}

	// Build a map of connectors by type
	connByType := make(map[string]connect.Connector)
	for _, c := range connectors {
		connByType[c.Type()] = c
	}

	for _, tt := range tests {
		t.Run(tt.connType, func(t *testing.T) {
			conn, ok := connByType[tt.connType]
			if !ok {
				t.Fatalf("connector %q not found in All()", tt.connType)
			}

			// All connectors should implement SecretSchemaProvider
			provider, ok := conn.(connect.SecretSchemaProvider)
			if !ok {
				t.Fatalf("connector %q does not implement SecretSchemaProvider", tt.connType)
			}

			schema := provider.SecretSchema()

			if tt.isDynamic {
				if schema != nil {
					t.Errorf("expected nil schema for dynamic secrets, got %v", schema)
				}
				return
			}

			if schema == nil {
				t.Fatalf("expected non-nil schema, got nil")
			}

			// Verify expected fields are present
			fieldNames := make(map[string]bool)
			for _, f := range schema {
				fieldNames[f.Name] = true
				if f.Label == "" {
					t.Errorf("field %q has empty label", f.Name)
				}
			}

			for _, expectedField := range tt.expectedFields {
				if !fieldNames[expectedField] {
					t.Errorf("expected field %q not found in schema", expectedField)
				}
			}
		})
	}
}

// TestPostgresSecretSchema verifies postgres connector schema details.
func TestPostgresSecretSchema(t *testing.T) {
	conn := newPGConnector()
	provider := conn.(connect.SecretSchemaProvider)
	schema := provider.SecretSchema()

	if len(schema) != 1 {
		t.Fatalf("expected 1 field, got %d", len(schema))
	}

	f := schema[0]
	if f.Name != "password" {
		t.Errorf("expected name 'password', got %q", f.Name)
	}
	if f.Required {
		t.Error("expected password to not be required")
	}
	if f.Label != "Database Password" {
		t.Errorf("expected label 'Database Password', got %q", f.Label)
	}
}

// TestKafkaSecretSchema verifies kafka connector schema details.
func TestKafkaSecretSchema(t *testing.T) {
	conn := newKafkaConnector()
	provider := conn.(connect.SecretSchemaProvider)
	schema := provider.SecretSchema()

	if len(schema) != 2 {
		t.Fatalf("expected 2 fields, got %d", len(schema))
	}

	// Verify we have both username and password
	fields := make(map[string]connect.SecretField)
	for _, f := range schema {
		fields[f.Name] = f
	}

	if _, ok := fields["username"]; !ok {
		t.Error("missing username field")
	}
	if _, ok := fields["password"]; !ok {
		t.Error("missing password field")
	}
}

// TestRabbitMQSecretSchema verifies rabbitmq connector schema details.
func TestRabbitMQSecretSchema(t *testing.T) {
	conn := newRabbitMQConnector()
	provider := conn.(connect.SecretSchemaProvider)
	schema := provider.SecretSchema()

	if len(schema) != 2 {
		t.Fatalf("expected 2 fields, got %d", len(schema))
	}

	// Verify we have both username and password
	fields := make(map[string]connect.SecretField)
	for _, f := range schema {
		fields[f.Name] = f
	}

	if _, ok := fields["username"]; !ok {
		t.Error("missing username field")
	}
	if _, ok := fields["password"]; !ok {
		t.Error("missing password field")
	}
}

// TestRESTSecretSchemaIsDynamic verifies REST connector returns nil for dynamic secrets.
func TestRESTSecretSchemaIsDynamic(t *testing.T) {
	conn := newRESTConnector("rest")
	provider := conn.(connect.SecretSchemaProvider)
	schema := provider.SecretSchema()

	if schema != nil {
		t.Errorf("expected nil schema for REST dynamic secrets, got %v", schema)
	}
}
