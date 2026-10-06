package webhook

import (
	"strings"

	"github.com/tidwall/gjson"
)

// MapPayload extracts values from payload using the mapping config and builds
// a context map for the flow. Mapping is map[string]string where:
// - key is the target context path (e.g. "ctx.eventType", "input.customerId")
// - value is a JSONPath-like query (e.g. "$.type", "$.data.object.customer")
//
// GJSON syntax: $ is optional, use dot notation. Arrays: $.items.0 or $.items.#.id
func MapPayload(payload []byte, mapping map[string]string) map[string]any {
	result := make(map[string]any)
	if len(mapping) == 0 {
		return result
	}

	for targetPath, jsonPath := range mapping {
		val := extractJSONPath(payload, jsonPath)
		if val != nil {
			setPath(result, targetPath, val)
		}
	}
	return result
}

// extractJSONPath extracts a value from JSON using GJSON.
// Handles JSONPath-style queries: $.foo.bar → foo.bar for GJSON.
func extractJSONPath(data []byte, path string) any {
	// Convert JSONPath syntax to GJSON syntax.
	query := normalizeJSONPath(path)
	if query == "" {
		return nil
	}

	result := gjson.GetBytes(data, query)
	if !result.Exists() {
		return nil
	}
	return result.Value()
}

// normalizeJSONPath converts JSONPath syntax to GJSON syntax.
// - Strips leading $. or $ prefix
// - GJSON natively supports: foo.bar, foo.0, foo.#.id, etc.
func normalizeJSONPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || path == "$" {
		return ""
	}
	// Strip leading $.
	if strings.HasPrefix(path, "$.") {
		return path[2:]
	}
	// Strip leading $ (bare root reference).
	if strings.HasPrefix(path, "$") {
		return path[1:]
	}
	return path
}

// setPath sets a value at a dotted path in a map, creating intermediate maps as needed.
// Path: "ctx.foo.bar" creates {ctx: {foo: {bar: value}}}.
func setPath(m map[string]any, path string, value any) {
	parts := strings.Split(path, ".")
	if len(parts) == 0 {
		return
	}

	current := m
	for i := 0; i < len(parts)-1; i++ {
		key := parts[i]
		if key == "" {
			continue
		}
		if _, exists := current[key]; !exists {
			current[key] = make(map[string]any)
		}
		if next, ok := current[key].(map[string]any); ok {
			current = next
		} else {
			// Overwrite scalar with map if needed.
			nm := make(map[string]any)
			current[key] = nm
			current = nm
		}
	}

	lastKey := parts[len(parts)-1]
	if lastKey != "" {
		current[lastKey] = value
	}
}

// MapPayloadFromMap extracts values from an already-parsed payload map.
// Useful when the payload has already been deserialized.
func MapPayloadFromMap(payload map[string]any, mapping map[string]string) map[string]any {
	result := make(map[string]any)
	if len(mapping) == 0 || payload == nil {
		return result
	}

	for targetPath, jsonPath := range mapping {
		val := getPath(payload, normalizeJSONPath(jsonPath))
		if val != nil {
			setPath(result, targetPath, val)
		}
	}
	return result
}

// getPath retrieves a value from a nested map using dot notation.
func getPath(m map[string]any, path string) any {
	if path == "" || m == nil {
		return nil
	}

	parts := strings.Split(path, ".")
	var current any = m

	for _, key := range parts {
		if key == "" {
			continue
		}
		switch c := current.(type) {
		case map[string]any:
			var ok bool
			current, ok = c[key]
			if !ok {
				return nil
			}
		default:
			return nil
		}
	}
	return current
}
