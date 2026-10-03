package flow

import (
	"strconv"
	"strings"
)

// This file is the single home for TYPED POSITIONAL PARAMS in an action's SQL
// operation (config-driven typing). A param in an action payload's "params"
// array is EITHER:
//
//   - a bare template/string  -> bound as TEXT (the default): "{{input.msisdn}}"
//   - a typed object          -> bound as the DECLARED type:
//     { "value": "{{input.id}}", "as": "int" }
//
// The declared "as" — never the resolved value's string shape — decides the
// bind type, so a digit-only identifier in a TEXT column (e.g. an msisdn) is NOT
// silently coerced to a number. The engine resolves the template to a string at
// the edge, then ConvertParam turns it into the Go type pgx encodes for the
// column. A conversion failure is a classified Validation error (surfaced as a
// 400), never a panic and never a silent coercion. ValidateTree rejects an
// unknown "as" (and a malformed typed-param object) at publish/validate time so
// bad config never reaches runtime.

// ParamType is a supported typed-param bind type. The v1 set is intentionally
// small; an unknown value is rejected at validation time.
type ParamType string

// The supported "as" bind types (v1).
const (
	ParamText    ParamType = "text"    // the string as-is (default)
	ParamInt     ParamType = "int"     // strconv.ParseInt -> int64
	ParamNumeric ParamType = "numeric" // strconv.ParseFloat -> float64 (decimal/numeric column)
	ParamBool    ParamType = "bool"    // strconv.ParseBool -> bool
)

// validParamTypes is the closed set ValidateTree checks an "as" against.
var validParamTypes = map[ParamType]bool{
	ParamText:    true,
	ParamInt:     true,
	ParamNumeric: true,
	ParamBool:    true,
}

// IsValidParamType reports whether as names a supported bind type. An empty
// string is NOT valid here — callers treat a missing "as" as the default before
// reaching this check (a typed-param object must declare "as" explicitly).
func IsValidParamType(as ParamType) bool { return validParamTypes[as] }

// ParseParam classifies one element of an action payload's "params" array. It
// returns the raw template (the string to resolve against Ctx), the declared
// bind type, whether the element was a typed-param object, and whether the shape
// is well-formed. A bare string is {tmpl, ParamText, typed:false, ok:true}. A
// typed object {"value":..,"as":..} with a string value and a non-empty "as" is
// {value, as, typed:true, ok:true}. Any other shape is ok:false (malformed).
func ParseParam(elem any) (tmpl string, as ParamType, typed bool, ok bool) {
	switch v := elem.(type) {
	case string:
		return v, ParamText, false, true
	case map[string]any:
		rawVal, hasVal := v["value"]
		rawAs, hasAs := v["as"]
		// A typed-param object must carry exactly value+as and nothing else, so a
		// typo'd key cannot slip through as a silent default.
		if !hasVal || !hasAs || len(v) != 2 {
			return "", "", true, false
		}
		sval, okv := rawVal.(string)
		sas, oka := rawAs.(string)
		if !okv || !oka || strings.TrimSpace(sas) == "" {
			return "", "", true, false
		}
		return sval, ParamType(sas), true, true
	default:
		return "", "", false, false
	}
}

// MalformedParamError is the classified Validation error for a params element
// that is neither a bare string nor a well-formed {value,as} object. It is a
// runtime backstop; ValidateTree catches the same shape at publish time.
func MalformedParamError(name string) error {
	return validationf("param %s: not a string or a {value,as} object", name)
}

// ConvertParam converts a template-resolved string into the Go value pgx should
// encode for the declared bind type. A failed conversion (or an unknown type) is
// a ClassValidation error naming the offending value, so the edge returns a 400
// instead of letting pgx reject a mis-typed wire value with an opaque upstream
// error. name is the positional label (e.g. "$1") used only in the message.
func ConvertParam(name, resolved string, as ParamType) (any, error) {
	switch as {
	case ParamText, "":
		return resolved, nil
	case ParamInt:
		n, err := strconv.ParseInt(strings.TrimSpace(resolved), 10, 64)
		if err != nil {
			return nil, validationf("param %s: value %q is not an int", name, resolved)
		}
		return n, nil
	case ParamNumeric:
		f, err := strconv.ParseFloat(strings.TrimSpace(resolved), 64)
		if err != nil {
			return nil, validationf("param %s: value %q is not numeric", name, resolved)
		}
		return f, nil
	case ParamBool:
		b, err := strconv.ParseBool(strings.TrimSpace(resolved))
		if err != nil {
			return nil, validationf("param %s: value %q is not a bool", name, resolved)
		}
		return b, nil
	default:
		return nil, validationf("param %s: unknown bind type %q", name, string(as))
	}
}
