package config

import "testing"

func TestValidateSchemaName(t *testing.T) {
	valid := []string{"rule_engine", "_x", "A1", "public", "a_b_c", "X_123"}
	for _, s := range valid {
		if err := ValidateSchemaName(s); err != nil {
			t.Errorf("ValidateSchemaName(%q) = %v; want nil", s, err)
		}
	}
	invalid := []string{"", "1x", "a-b", "a;b", "public schema", "a.b", "drop table", "a'b", `a"b`}
	for _, s := range invalid {
		if err := ValidateSchemaName(s); err == nil {
			t.Errorf("ValidateSchemaName(%q) = nil; want error", s)
		}
	}
}
