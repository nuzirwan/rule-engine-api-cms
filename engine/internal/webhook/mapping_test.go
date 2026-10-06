package webhook

import (
	"reflect"
	"testing"
)

func TestMapPayload(t *testing.T) {
	payload := []byte(`{
		"id": "evt_123",
		"type": "payment_intent.succeeded",
		"data": {
			"object": {
				"id": "pi_456",
				"amount": 1000,
				"customer": "cus_789"
			}
		},
		"items": [
			{"name": "first", "qty": 1},
			{"name": "second", "qty": 2}
		]
	}`)

	tests := []struct {
		name    string
		mapping map[string]string
		want    map[string]any
	}{
		{
			name: "simple field",
			mapping: map[string]string{
				"eventId": "$.id",
			},
			want: map[string]any{
				"eventId": "evt_123",
			},
		},
		{
			name: "nested field",
			mapping: map[string]string{
				"customerId": "$.data.object.customer",
			},
			want: map[string]any{
				"customerId": "cus_789",
			},
		},
		{
			name: "multiple fields",
			mapping: map[string]string{
				"eventType": "$.type",
				"amount":    "$.data.object.amount",
			},
			want: map[string]any{
				"eventType": "payment_intent.succeeded",
				"amount":    float64(1000),
			},
		},
		{
			name: "nested target path",
			mapping: map[string]string{
				"ctx.eventType": "$.type",
				"ctx.data.id":   "$.data.object.id",
			},
			want: map[string]any{
				"ctx": map[string]any{
					"eventType": "payment_intent.succeeded",
					"data": map[string]any{
						"id": "pi_456",
					},
				},
			},
		},
		{
			name: "array access",
			mapping: map[string]string{
				"firstName": "$.items.0.name",
			},
			want: map[string]any{
				"firstName": "first",
			},
		},
		{
			name: "missing field",
			mapping: map[string]string{
				"missing": "$.nonexistent",
			},
			want: map[string]any{},
		},
		{
			name: "gjson syntax without $",
			mapping: map[string]string{
				"eventId": "id",
			},
			want: map[string]any{
				"eventId": "evt_123",
			},
		},
		{
			name:    "empty mapping",
			mapping: map[string]string{},
			want:    map[string]any{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := MapPayload(payload, tc.mapping)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("MapPayload() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMapPayloadFromMap(t *testing.T) {
	payload := map[string]any{
		"id":   "evt_123",
		"type": "test.event",
		"data": map[string]any{
			"nested": "value",
		},
	}

	mapping := map[string]string{
		"eventType":  "$.type",
		"nestedData": "$.data.nested",
	}

	got := MapPayloadFromMap(payload, mapping)
	want := map[string]any{
		"eventType":  "test.event",
		"nestedData": "value",
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("MapPayloadFromMap() = %v, want %v", got, want)
	}
}

func TestSetPath(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		value  any
		want   map[string]any
	}{
		{
			name:  "simple key",
			path:  "key",
			value: "value",
			want:  map[string]any{"key": "value"},
		},
		{
			name:  "nested key",
			path:  "a.b.c",
			value: 123,
			want: map[string]any{
				"a": map[string]any{
					"b": map[string]any{
						"c": 123,
					},
				},
			},
		},
		{
			name:  "empty path parts",
			path:  "a..b",
			value: "x",
			want: map[string]any{
				"a": map[string]any{
					"b": "x",
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := make(map[string]any)
			setPath(got, tc.path, tc.value)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("setPath() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNormalizeJSONPath(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"$.foo.bar", "foo.bar"},
		{"$foo.bar", "foo.bar"},
		{"foo.bar", "foo.bar"},
		{"$", ""},
		{"", ""},
		{"  $.foo  ", "foo"},
	}

	for _, tc := range tests {
		got := normalizeJSONPath(tc.input)
		if got != tc.want {
			t.Errorf("normalizeJSONPath(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
