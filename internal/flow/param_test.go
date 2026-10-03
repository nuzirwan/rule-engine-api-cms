package flow

import (
	"errors"
	"testing"

	"nzr-rules-engine/internal/connect"
)

// TestParseParam classifies bare strings, well-formed typed objects, and
// malformed shapes.
func TestParseParam(t *testing.T) {
	cases := []struct {
		name       string
		elem       any
		wantTmpl   string
		wantAs     ParamType
		wantTyped  bool
		wantWellOK bool
	}{
		{"bare string", "{{input.msisdn}}", "{{input.msisdn}}", ParamText, false, true},
		{"typed int", map[string]any{"value": "{{input.id}}", "as": "int"}, "{{input.id}}", ParamInt, true, true},
		{"typed numeric", map[string]any{"value": "{{input.amt}}", "as": "numeric"}, "{{input.amt}}", ParamNumeric, true, true},
		{"missing as", map[string]any{"value": "{{input.id}}"}, "", "", true, false},
		{"missing value", map[string]any{"as": "int"}, "", "", true, false},
		{"extra key", map[string]any{"value": "x", "as": "int", "oops": 1}, "", "", true, false},
		{"empty as", map[string]any{"value": "x", "as": ""}, "", "", true, false},
		{"non-string value", map[string]any{"value": 7, "as": "int"}, "", "", true, false},
		{"wrong type", 42, "", "", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmpl, as, typed, ok := ParseParam(tc.elem)
			if tmpl != tc.wantTmpl || as != tc.wantAs || typed != tc.wantTyped || ok != tc.wantWellOK {
				t.Fatalf("ParseParam(%v) = (%q,%q,%v,%v); want (%q,%q,%v,%v)",
					tc.elem, tmpl, as, typed, ok, tc.wantTmpl, tc.wantAs, tc.wantTyped, tc.wantWellOK)
			}
		})
	}
}

// TestConvertParam converts a resolved string to the declared Go type and
// classifies a bad conversion as a Validation error (never a panic).
func TestConvertParam(t *testing.T) {
	okCases := []struct {
		resolved string
		as       ParamType
		want     any
	}{
		{"628111015450", ParamText, "628111015450"},
		{"1500", ParamInt, int64(1500)},
		{"199.90", ParamNumeric, 199.90},
		{"true", ParamBool, true},
		{"anything", "", "anything"}, // empty as == text default
	}
	for _, tc := range okCases {
		got, err := ConvertParam("$1", tc.resolved, tc.as)
		if err != nil {
			t.Fatalf("ConvertParam(%q,%q) error: %v", tc.resolved, tc.as, err)
		}
		if got != tc.want {
			t.Fatalf("ConvertParam(%q,%q) = %#v want %#v", tc.resolved, tc.as, got, tc.want)
		}
	}

	badCases := []struct {
		resolved string
		as       ParamType
	}{
		{"not-a-number", ParamInt},
		{"12.3.4", ParamNumeric},
		{"maybe", ParamBool},
		{"x", "weird"}, // unknown bind type
	}
	for _, tc := range badCases {
		_, err := ConvertParam("$1", tc.resolved, tc.as)
		if err == nil {
			t.Fatalf("ConvertParam(%q,%q) expected a Validation error", tc.resolved, tc.as)
		}
		if !errors.Is(err, ErrValidation) {
			t.Fatalf("ConvertParam(%q,%q) error = %v; want ErrValidation", tc.resolved, tc.as, err)
		}
	}
}

// TestValidateTreeParamTypes proves ValidateTree rejects an unknown "as" and a
// malformed typed-param object at validate/publish time, and accepts a bare
// string and a valid typed object.
func TestValidateTreeParamTypes(t *testing.T) {
	mkTree := func(t *testing.T, params []any) Node {
		t.Helper()
		action := Node{
			ID:   "a",
			Type: TypeAction,
			Spec: raw(t, ActionSpec{
				ConnRef: ConnRef{Connection: "pg"},
				Operation: connect.Operation{
					Kind: "query",
					Payload: map[string]any{
						"sql":    "SELECT 1 WHERE x = $1",
						"params": params,
					},
				},
				SaveAs: "r",
			}),
			Children: []Node{resp(t, "r1")},
		}
		return Node{ID: "root", Type: TypeTrigger, Spec: raw(t, TriggerSpec{Method: "GET", Path: "/x"}), Children: []Node{action}}
	}

	hasCode := func(issues []ValidationIssue, code string) bool {
		for _, is := range issues {
			if is.Code == code {
				return true
			}
		}
		return false
	}

	// Valid: a bare string and a valid typed object -> no param issues.
	ok := ValidateTree(mkTree(t, []any{"{{input.msisdn}}", map[string]any{"value": "{{input.id}}", "as": "int"}}), nil)
	if hasCode(ok, "unknown_param_type") || hasCode(ok, "bad_param") {
		t.Fatalf("valid params flagged: %+v", ok)
	}

	// Unknown "as".
	bad := ValidateTree(mkTree(t, []any{map[string]any{"value": "{{input.id}}", "as": "int4"}}), nil)
	if !hasCode(bad, "unknown_param_type") {
		t.Fatalf("unknown as not flagged: %+v", bad)
	}

	// Malformed object (missing "as").
	mal := ValidateTree(mkTree(t, []any{map[string]any{"value": "{{input.id}}"}}), nil)
	if !hasCode(mal, "bad_param") {
		t.Fatalf("malformed param object not flagged: %+v", mal)
	}
}
