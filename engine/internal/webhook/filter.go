package webhook

import (
	"github.com/tidwall/gjson"
)

// MatchesFilter checks if an event matches the filter criteria.
// Filter is map[string][]string where:
// - key is a JSONPath-like query (e.g. "$.type", "$.action")
// - value is a list of allowed values
//
// An empty filter matches all events (returns true).
// All filter keys must match for the event to pass (AND logic).
// Each key passes if the extracted value matches ANY of its allowed values (OR logic).
func MatchesFilter(payload []byte, filter map[string][]string) bool {
	// Empty filter matches everything.
	if len(filter) == 0 {
		return true
	}

	for jsonPath, allowedValues := range filter {
		// Skip empty allowed lists (matches anything for this key).
		if len(allowedValues) == 0 {
			continue
		}

		// Extract value from payload.
		query := normalizeJSONPath(jsonPath)
		if query == "" {
			// Empty path never matches when allowed values are specified.
			return false
		}

		result := gjson.GetBytes(payload, query)
		if !result.Exists() {
			// Key not found in payload - doesn't match.
			return false
		}

		// Compare extracted value against allowed values.
		extracted := stringify(result.Value())
		matched := false
		for _, allowed := range allowedValues {
			if extracted == allowed {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

// MatchesFilterFromMap checks filter against an already-parsed payload map.
func MatchesFilterFromMap(payload map[string]any, filter map[string][]string) bool {
	if len(filter) == 0 {
		return true
	}

	for jsonPath, allowedValues := range filter {
		if len(allowedValues) == 0 {
			continue
		}

		val := getPath(payload, normalizeJSONPath(jsonPath))
		if val == nil {
			return false
		}

		extracted := stringify(val)
		matched := false
		for _, allowed := range allowedValues {
			if extracted == allowed {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

// MatchesFilterByEventType is a convenience function for filtering by event type.
// It's the common case: filter = {"$.type": ["event.one", "event.two"]}.
func MatchesFilterByEventType(eventType string, allowedTypes []string) bool {
	if len(allowedTypes) == 0 {
		return true
	}
	for _, allowed := range allowedTypes {
		if eventType == allowed {
			return true
		}
	}
	return false
}

// stringify converts a value to string for comparison.
func stringify(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case float64:
		// Handle integers stored as float64 (JSON numbers).
		if val == float64(int64(val)) {
			return formatInt(int64(val))
		}
		// Non-integer floats - simplified handling.
		return ""
	case bool:
		if val {
			return "true"
		}
		return "false"
	case nil:
		return ""
	default:
		return ""
	}
}

// formatInt converts an int64 to string.
func formatInt(i int64) string {
	if i == 0 {
		return "0"
	}
	neg := false
	if i < 0 {
		neg = true
		i = -i
	}
	var buf [20]byte
	idx := len(buf)
	for i > 0 {
		idx--
		buf[idx] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		idx--
		buf[idx] = '-'
	}
	return string(buf[idx:])
}
