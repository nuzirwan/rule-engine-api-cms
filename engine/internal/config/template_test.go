package config

import (
	"strings"
	"testing"
)

func TestLoadBuiltinTemplates(t *testing.T) {
	templates, err := LoadBuiltinTemplates()
	if err != nil {
		t.Fatalf("LoadBuiltinTemplates failed: %v", err)
	}

	// We expect at least 3 templates
	if len(templates) < 3 {
		t.Errorf("expected at least 3 templates, got %d", len(templates))
	}

	// Verify each template has required fields
	for _, tmpl := range templates {
		if tmpl.ID == "" {
			t.Error("template has empty ID")
		}
		if tmpl.Name == "" {
			t.Errorf("template %q has empty Name", tmpl.ID)
		}
		if tmpl.Category == "" {
			t.Errorf("template %q has empty Category", tmpl.ID)
		}
		if len(tmpl.Variables) == 0 {
			t.Errorf("template %q has no variables", tmpl.ID)
		}
		if len(tmpl.Flows) == 0 {
			t.Errorf("template %q has no flows", tmpl.ID)
		}
	}
}

func TestSubstituteTemplate_OK(t *testing.T) {
	templates, err := LoadBuiltinTemplates()
	if err != nil {
		t.Fatalf("LoadBuiltinTemplates failed: %v", err)
	}

	// Find the crud-rest-postgres template
	var crudTemplate *Template
	for i := range templates {
		if templates[i].ID == "crud-rest-postgres" {
			crudTemplate = &templates[i]
			break
		}
	}
	if crudTemplate == nil {
		t.Fatal("crud-rest-postgres template not found")
	}

	// Substitute with valid vars
	vars := map[string]string{
		"ENTITY":     "user",
		"TABLE":      "users",
		"CONNECTION": "pg-main",
		"ID_FIELD":   "id",
	}

	flows, err := SubstituteTemplate(*crudTemplate, vars)
	if err != nil {
		t.Fatalf("SubstituteTemplate failed: %v", err)
	}

	// Should get 5 flows for CRUD
	if len(flows) != 5 {
		t.Errorf("expected 5 flows, got %d", len(flows))
	}

	// Verify substitution happened
	for _, f := range flows {
		if strings.Contains(f.FlowID, "{{") {
			t.Errorf("flow %q still contains unsubstituted placeholder", f.FlowID)
		}
		if strings.Contains(f.Path, "{{ENTITY}}") {
			t.Errorf("path %q still contains unsubstituted placeholder", f.Path)
		}
		// FlowID should contain "user"
		if !strings.Contains(f.FlowID, "user") {
			t.Errorf("flow ID %q should contain 'user'", f.FlowID)
		}
	}
}

func TestSubstituteTemplate_MissingRequired(t *testing.T) {
	templates, err := LoadBuiltinTemplates()
	if err != nil {
		t.Fatalf("LoadBuiltinTemplates failed: %v", err)
	}

	var crudTemplate *Template
	for i := range templates {
		if templates[i].ID == "crud-rest-postgres" {
			crudTemplate = &templates[i]
			break
		}
	}
	if crudTemplate == nil {
		t.Fatal("crud-rest-postgres template not found")
	}

	// Missing required ENTITY variable
	vars := map[string]string{
		"TABLE": "users",
	}

	_, err = SubstituteTemplate(*crudTemplate, vars)
	if err == nil {
		t.Fatal("expected error for missing required variable")
	}

	// Should mention the missing variable
	if !strings.Contains(err.Error(), "ENTITY") {
		t.Errorf("error should mention ENTITY, got: %v", err)
	}
}

func TestSubstituteTemplate_DefaultApplied(t *testing.T) {
	templates, err := LoadBuiltinTemplates()
	if err != nil {
		t.Fatalf("LoadBuiltinTemplates failed: %v", err)
	}

	var crudTemplate *Template
	for i := range templates {
		if templates[i].ID == "crud-rest-postgres" {
			crudTemplate = &templates[i]
			break
		}
	}
	if crudTemplate == nil {
		t.Fatal("crud-rest-postgres template not found")
	}

	// Only provide required vars, rely on defaults for CONNECTION and ID_FIELD
	vars := map[string]string{
		"ENTITY": "order",
		"TABLE":  "orders",
	}

	flows, err := SubstituteTemplate(*crudTemplate, vars)
	if err != nil {
		t.Fatalf("SubstituteTemplate failed: %v", err)
	}

	if len(flows) == 0 {
		t.Error("expected flows to be created with defaults")
	}
}

func TestSubstituteTemplate_UnsafeValue(t *testing.T) {
	templates, err := LoadBuiltinTemplates()
	if err != nil {
		t.Fatalf("LoadBuiltinTemplates failed: %v", err)
	}

	var crudTemplate *Template
	for i := range templates {
		if templates[i].ID == "crud-rest-postgres" {
			crudTemplate = &templates[i]
			break
		}
	}
	if crudTemplate == nil {
		t.Fatal("crud-rest-postgres template not found")
	}

	// Value with quote that would break JSON
	vars := map[string]string{
		"ENTITY": `user"injection`,
		"TABLE":  "users",
	}

	_, err = SubstituteTemplate(*crudTemplate, vars)
	if err == nil {
		t.Fatal("expected error for unsafe JSON value")
	}

	if !strings.Contains(err.Error(), "unsafe") {
		t.Errorf("error should mention unsafe, got: %v", err)
	}
}

func TestSubstituteTemplate_AllTemplates(t *testing.T) {
	templates, err := LoadBuiltinTemplates()
	if err != nil {
		t.Fatalf("LoadBuiltinTemplates failed: %v", err)
	}

	// Test all templates can be substituted with sample values
	testVars := map[string]map[string]string{
		"crud-rest-postgres": {
			"ENTITY":     "product",
			"TABLE":      "products",
			"CONNECTION": "pg-main",
			"ID_FIELD":   "id",
		},
		"webhook-handler": {
			"WEBHOOK_ID":           "stripe-payment",
			"VALIDATOR_CONNECTION": "validator-svc",
			"TARGET_CONNECTION":    "processor-svc",
			"TARGET_PATH":          "/process",
		},
		"data-sync": {
			"SYNC_ID":           "user-sync",
			"SOURCE_CONNECTION": "source-pg",
			"TARGET_CONNECTION": "target-pg",
			"SOURCE_TABLE":      "users",
			"TARGET_TABLE":      "users_backup",
			"ID_FIELD":          "id",
		},
	}

	for _, tmpl := range templates {
		vars, ok := testVars[tmpl.ID]
		if !ok {
			t.Logf("no test vars for template %q, skipping", tmpl.ID)
			continue
		}

		flows, err := SubstituteTemplate(tmpl, vars)
		if err != nil {
			t.Errorf("SubstituteTemplate(%q) failed: %v", tmpl.ID, err)
			continue
		}

		if len(flows) != len(tmpl.Flows) {
			t.Errorf("template %q: expected %d flows, got %d", tmpl.ID, len(tmpl.Flows), len(flows))
		}

		// Each flow should have valid structure
		for _, f := range flows {
			if f.FlowID == "" {
				t.Errorf("template %q: flow has empty flowId", tmpl.ID)
			}
			if f.Method == "" {
				t.Errorf("template %q flow %q: empty method", tmpl.ID, f.FlowID)
			}
			if f.Path == "" {
				t.Errorf("template %q flow %q: empty path", tmpl.ID, f.FlowID)
			}
		}
	}
}
