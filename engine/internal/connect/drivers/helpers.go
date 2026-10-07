package drivers

import (
	"encoding/json"
	"strings"

	"nzr-rules-engine/internal/connect"
)

// jsonMarshal is a thin alias so drivers encode a composite value consistently
// (used by the valkey SET value coercion).
func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

// stringSetting reads a string setting by key, tolerating absent/non-string.
func stringSetting(s map[string]any, key string) (string, bool) {
	if s == nil {
		return "", false
	}
	v, ok := s[key]
	if !ok {
		return "", false
	}
	str, ok := v.(string)
	return str, ok
}

// intSetting reads an int setting by key. JSON numbers decode as float64, so a
// float with no fractional part is accepted as an int.
func intSetting(s map[string]any, key string) (int, bool) {
	if s == nil {
		return 0, false
	}
	switch v := s[key].(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	default:
		return 0, false
	}
}

// boolSetting reads a bool setting by key, tolerating absent/non-bool.
func boolSetting(s map[string]any, key string) (bool, bool) {
	if s == nil {
		return false, false
	}
	v, ok := s[key]
	if !ok {
		return false, false
	}
	b, ok := v.(bool)
	return b, ok
}

// sqlAndParams extracts the "sql" string and optional positional "params" slice
// from an operation payload. A missing/blank sql is a Validation error. Params
// are always passed positionally to pgx ($1..) — never string-interpolated.
func sqlAndParams(key, opKind string, payload map[string]any) (string, []any, error) {
	sql, ok := stringSetting(payload, "sql")
	if !ok || strings.TrimSpace(sql) == "" {
		return "", nil, connect.NewConnError(connect.Validation, key, opKind, "missing sql in payload", nil)
	}
	var params []any
	if raw, ok := payload["params"]; ok && raw != nil {
		p, ok := raw.([]any)
		if !ok {
			return "", nil, connect.NewConnError(connect.Validation, key, opKind, "params must be a list", nil)
		}
		params = p
	}
	return sql, params, nil
}

// firstKeyword returns the first SQL keyword, upper-cased, ignoring leading
// whitespace and comment-free prefixes (sufficient for the thin slice's guard).
func firstKeyword(sql string) string {
	sql = strings.TrimSpace(sql)
	if sql == "" {
		return ""
	}
	fields := strings.Fields(sql)
	if len(fields) == 0 {
		return ""
	}
	return strings.ToUpper(fields[0])
}

// isSelect reports whether sql is a read (SELECT/WITH/VALUES/SHOW).
func isSelect(sql string) bool {
	switch firstKeyword(sql) {
	case "SELECT", "WITH", "VALUES", "SHOW", "TABLE":
		return true
	default:
		return false
	}
}

// isMutation reports whether sql changes data/schema (anything not a read).
func isMutation(sql string) bool {
	kw := firstKeyword(sql)
	if kw == "" {
		return false
	}
	return !isSelect(sql)
}
