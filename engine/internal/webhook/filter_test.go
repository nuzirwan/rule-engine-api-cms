package webhook

import (
	"testing"
)

func TestMatchesFilter(t *testing.T) {
	payload := []byte(`{
		"type": "payment_intent.succeeded",
		"action": "completed",
		"status": "active",
		"count": 42
	}`)

	tests := []struct {
		name   string
		filter map[string][]string
		want   bool
	}{
		{
			name:   "empty filter matches all",
			filter: map[string][]string{},
			want:   true,
		},
		{
			name:   "nil filter matches all",
			filter: nil,
			want:   true,
		},
		{
			name: "single value match",
			filter: map[string][]string{
				"$.type": {"payment_intent.succeeded"},
			},
			want: true,
		},
		{
			name: "single value no match",
			filter: map[string][]string{
				"$.type": {"payment_intent.failed"},
			},
			want: false,
		},
		{
			name: "multiple allowed values - match first",
			filter: map[string][]string{
				"$.type": {"payment_intent.succeeded", "payment_intent.failed"},
			},
			want: true,
		},
		{
			name: "multiple allowed values - match second",
			filter: map[string][]string{
				"$.action": {"started", "completed"},
			},
			want: true,
		},
		{
			name: "multiple filter keys - all match",
			filter: map[string][]string{
				"$.type":   {"payment_intent.succeeded"},
				"$.action": {"completed"},
			},
			want: true,
		},
		{
			name: "multiple filter keys - one fails",
			filter: map[string][]string{
				"$.type":   {"payment_intent.succeeded"},
				"$.action": {"started"},
			},
			want: false,
		},
		{
			name: "missing field in payload",
			filter: map[string][]string{
				"$.nonexistent": {"value"},
			},
			want: false,
		},
		{
			name: "empty allowed values - match anything",
			filter: map[string][]string{
				"$.type": {},
			},
			want: true,
		},
		{
			name: "gjson syntax without $",
			filter: map[string][]string{
				"type": {"payment_intent.succeeded"},
			},
			want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := MatchesFilter(payload, tc.filter)
			if got != tc.want {
				t.Errorf("MatchesFilter() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMatchesFilterFromMap(t *testing.T) {
	payload := map[string]any{
		"type":   "order.created",
		"status": "pending",
	}

	tests := []struct {
		name   string
		filter map[string][]string
		want   bool
	}{
		{
			name:   "empty filter",
			filter: map[string][]string{},
			want:   true,
		},
		{
			name: "match",
			filter: map[string][]string{
				"$.type": {"order.created"},
			},
			want: true,
		},
		{
			name: "no match",
			filter: map[string][]string{
				"$.type": {"order.deleted"},
			},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := MatchesFilterFromMap(payload, tc.filter)
			if got != tc.want {
				t.Errorf("MatchesFilterFromMap() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMatchesFilterByEventType(t *testing.T) {
	tests := []struct {
		name         string
		eventType    string
		allowedTypes []string
		want         bool
	}{
		{
			name:         "empty allowed types",
			eventType:    "anything",
			allowedTypes: []string{},
			want:         true,
		},
		{
			name:         "nil allowed types",
			eventType:    "anything",
			allowedTypes: nil,
			want:         true,
		},
		{
			name:         "match first",
			eventType:    "payment.success",
			allowedTypes: []string{"payment.success", "payment.failed"},
			want:         true,
		},
		{
			name:         "match second",
			eventType:    "payment.failed",
			allowedTypes: []string{"payment.success", "payment.failed"},
			want:         true,
		},
		{
			name:         "no match",
			eventType:    "payment.pending",
			allowedTypes: []string{"payment.success", "payment.failed"},
			want:         false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := MatchesFilterByEventType(tc.eventType, tc.allowedTypes)
			if got != tc.want {
				t.Errorf("MatchesFilterByEventType() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestStringify(t *testing.T) {
	tests := []struct {
		input any
		want  string
	}{
		{"hello", "hello"},
		{float64(42), "42"},
		{float64(3.14), ""},  // Non-integer floats return empty (simplified)
		{true, "true"},
		{false, "false"},
		{nil, ""},
		{123, ""}, // int type not handled explicitly
	}

	for _, tc := range tests {
		got := stringify(tc.input)
		if got != tc.want {
			t.Errorf("stringify(%v) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
