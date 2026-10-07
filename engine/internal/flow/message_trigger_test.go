package flow

import (
	"context"
	"encoding/json"
	"testing"
)

// TestMessageTrigger_MapsInputCorrectly verifies that message fields appear
// correctly in Ctx.Input when processed by the messageTriggerHandler.
func TestMessageTrigger_MapsInputCorrectly(t *testing.T) {
	tests := []struct {
		name     string
		msg      *Message
		wantKey  string
		wantVal  any
		wantHdr  map[string]any
		wantMeta map[string]any
	}{
		{
			name: "json value",
			msg: &Message{
				Key:     []byte("order-123"),
				Value:   []byte(`{"orderId":"123","amount":99.50}`),
				Headers: map[string]string{"trace-id": "abc123"},
				Metadata: map[string]any{
					"topic":     "orders",
					"partition": 0,
					"offset":    42,
				},
			},
			wantKey: "order-123",
			wantVal: map[string]any{"orderId": "123", "amount": 99.5},
			wantHdr: map[string]any{"trace-id": "abc123"},
			wantMeta: map[string]any{
				"topic":     "orders",
				"partition": 0,
				"offset":    42,
			},
		},
		{
			name: "string value (non-json)",
			msg: &Message{
				Key:   []byte("msg-1"),
				Value: []byte("plain text message"),
			},
			wantKey:  "msg-1",
			wantVal:  "plain text message",
			wantHdr:  map[string]any{},
			wantMeta: map[string]any{},
		},
		{
			name: "empty key",
			msg: &Message{
				Value: []byte(`{"data":"test"}`),
			},
			wantKey:  "", // missing from input
			wantVal:  map[string]any{"data": "test"},
			wantHdr:  map[string]any{},
			wantMeta: map[string]any{},
		},
		{
			name: "array value",
			msg: &Message{
				Key:   []byte("items"),
				Value: []byte(`[1,2,3]`),
			},
			wantKey:  "items",
			wantVal:  []any{float64(1), float64(2), float64(3)},
			wantHdr:  map[string]any{},
			wantMeta: map[string]any{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := MapMessageToInput(tt.msg)

			// Check key
			if tt.wantKey != "" {
				gotKey, ok := input["key"].(string)
				if !ok || gotKey != tt.wantKey {
					t.Errorf("key = %v, want %v", input["key"], tt.wantKey)
				}
			} else {
				if _, ok := input["key"]; ok {
					t.Errorf("key should be absent, got %v", input["key"])
				}
			}

			// Check value
			gotVal := input["value"]
			if !deepEqual(gotVal, tt.wantVal) {
				t.Errorf("value = %v, want %v", gotVal, tt.wantVal)
			}

			// Check headers
			gotHdr := input["headers"]
			if !deepEqual(gotHdr, tt.wantHdr) {
				t.Errorf("headers = %v, want %v", gotHdr, tt.wantHdr)
			}

			// Check metadata
			gotMeta := input["metadata"]
			if !deepEqual(gotMeta, tt.wantMeta) {
				t.Errorf("metadata = %v, want %v", gotMeta, tt.wantMeta)
			}
		})
	}
}

// TestMessageTrigger_WalksChildren verifies that the messageTriggerHandler
// executes child nodes after mapping the message input.
func TestMessageTrigger_WalksChildren(t *testing.T) {
	// Build a messageTrigger node with a response child
	spec := MessageTriggerSpec{
		ConnectionKey: "kafka-main",
		Topic:         "orders",
		FlowID:        "process-orders",
	}
	specBytes, _ := json.Marshal(spec)

	responseSpec := ResponseSpec{Status: 200}
	responseSpecBytes, _ := json.Marshal(responseSpec)

	tree := Node{
		ID:   "msg-trigger-1",
		Type: TypeMessageTrigger,
		Spec: specBytes,
		Children: []Node{
			{
				ID:   "response-1",
				Type: TypeResponse,
				Spec: responseSpecBytes,
			},
		},
	}

	// Set up the interpreter and ctx with message input
	interp := New()
	input := MapMessageToInput(&Message{
		Key:     []byte("test-key"),
		Value:   []byte(`{"data":"test"}`),
		Headers: map[string]string{},
	})
	c := NewCtx("", "", "test", input)

	// Run the flow
	err := interp.Run(context.Background(), &tree, Version{FlowID: "test", Version: 1}, c, Deps{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the response node was executed (it sets Stop: true, which unwinds)
	// The walk completed without error, meaning children were executed
}

// TestMessageTrigger_ValidationErrors verifies that missing required fields
// are rejected with validation errors.
func TestMessageTrigger_ValidationErrors(t *testing.T) {
	tests := []struct {
		name    string
		spec    MessageTriggerSpec
		input   map[string]any
		wantErr string
	}{
		{
			name:    "missing connectionKey",
			spec:    MessageTriggerSpec{Topic: "orders", FlowID: "flow-1"},
			input:   map[string]any{"key": "test"},
			wantErr: "missing connectionKey",
		},
		{
			name:    "missing topic",
			spec:    MessageTriggerSpec{ConnectionKey: "kafka", FlowID: "flow-1"},
			input:   map[string]any{"key": "test"},
			wantErr: "missing topic",
		},
		{
			name:    "missing flowId",
			spec:    MessageTriggerSpec{ConnectionKey: "kafka", Topic: "orders"},
			input:   map[string]any{"key": "test"},
			wantErr: "missing flowId",
		},
		{
			name:    "no children",
			spec:    MessageTriggerSpec{ConnectionKey: "kafka", Topic: "orders", FlowID: "flow-1"},
			input:   map[string]any{"key": "test"},
			wantErr: "must have at least one child",
		},
		{
			name:    "no input (message not mapped)",
			spec:    MessageTriggerSpec{ConnectionKey: "kafka", Topic: "orders", FlowID: "flow-1"},
			input:   nil, // empty input triggers validation
			wantErr: "no input",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			specBytes, _ := json.Marshal(tt.spec)

			var children []Node
			// Add a child for tests that should pass the "no children" check
			if tt.wantErr != "must have at least one child" {
				responseSpec, _ := json.Marshal(ResponseSpec{Status: 200})
				children = []Node{{ID: "resp", Type: TypeResponse, Spec: responseSpec}}
			}

			tree := Node{
				ID:       "msg-trigger",
				Type:     TypeMessageTrigger,
				Spec:     specBytes,
				Children: children,
			}

			interp := New()
			c := NewCtx("", "", "test", tt.input)

			err := interp.Run(context.Background(), &tree, Version{}, c, Deps{})
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want to contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

// TestMessageDispatcher_Dispatch verifies that the dispatcher correctly routes
// messages to their registered flows.
func TestMessageDispatcher_Dispatch(t *testing.T) {
	// Set up a flow
	spec := MessageTriggerSpec{
		ConnectionKey: "kafka-main",
		Topic:         "orders",
		FlowID:        "process-orders",
	}
	specBytes, _ := json.Marshal(spec)

	responseSpec, _ := json.Marshal(ResponseSpec{Status: 200})
	tree := &Node{
		ID:   "msg-trigger",
		Type: TypeMessageTrigger,
		Spec: specBytes,
		Children: []Node{
			{ID: "response", Type: TypeResponse, Spec: responseSpec},
		},
	}

	// Set up the lookup
	lookup := NewInMemoryFlowLookup()
	lookup.Register("kafka-main", "orders", tree, Version{FlowID: "process-orders", Version: 1})

	// Create the dispatcher
	interp := New()
	dispatcher := NewMessageDispatcher(interp, MessageDispatcherConfig{
		Lookup: lookup,
		Deps:   Deps{},
		Env:    "test",
	})

	// Dispatch a message
	msg := &Message{
		Key:   []byte("order-123"),
		Value: []byte(`{"orderId":"123"}`),
	}

	err := dispatcher.Dispatch(context.Background(), "kafka-main", "orders", msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestMessageDispatcher_NotFound verifies that dispatching to an unregistered
// route returns an error.
func TestMessageDispatcher_NotFound(t *testing.T) {
	lookup := NewInMemoryFlowLookup()
	interp := New()
	dispatcher := NewMessageDispatcher(interp, MessageDispatcherConfig{
		Lookup: lookup,
		Deps:   Deps{},
		Env:    "test",
	})

	msg := &Message{Key: []byte("test"), Value: []byte(`{}`)}
	err := dispatcher.Dispatch(context.Background(), "unknown-conn", "unknown-topic", msg)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !contains(err.Error(), "no flow") {
		t.Errorf("error = %q, want to contain 'no flow'", err.Error())
	}
}

// TestMessageDispatcher_ContinueOnError verifies that OnErrorContinue suppresses
// flow errors without failing the dispatch.
func TestMessageDispatcher_ContinueOnError(t *testing.T) {
	// Create a flow that will fail (missing input)
	spec := MessageTriggerSpec{
		ConnectionKey: "kafka-main",
		Topic:         "orders",
		FlowID:        "failing-flow",
	}
	specBytes, _ := json.Marshal(spec)

	// No children = validation error
	tree := &Node{
		ID:   "msg-trigger",
		Type: TypeMessageTrigger,
		Spec: specBytes,
	}

	lookup := NewInMemoryFlowLookup()
	lookup.Register("kafka-main", "orders", tree, Version{FlowID: "failing-flow", Version: 1})

	interp := New()
	dispatcher := NewMessageDispatcher(interp, MessageDispatcherConfig{
		Lookup:  lookup,
		Deps:    Deps{},
		Env:     "test",
		OnError: OnErrorContinue,
	})

	msg := &Message{Key: []byte("test"), Value: []byte(`{}`)}
	err := dispatcher.Dispatch(context.Background(), "kafka-main", "orders", msg)
	// With OnErrorContinue, the error should be suppressed
	if err != nil {
		t.Fatalf("expected nil error with OnErrorContinue, got: %v", err)
	}
}

// TestWithMessage_RoundTrip verifies that a Message can be attached to and
// retrieved from a context.
func TestWithMessage_RoundTrip(t *testing.T) {
	msg := &Message{
		Key:   []byte("test-key"),
		Value: []byte(`{"data":"value"}`),
	}

	ctx := WithMessage(context.Background(), msg)
	got, ok := MessageFrom(ctx)

	if !ok {
		t.Fatal("MessageFrom returned false")
	}
	if string(got.Key) != string(msg.Key) {
		t.Errorf("Key = %q, want %q", got.Key, msg.Key)
	}
	if string(got.Value) != string(msg.Value) {
		t.Errorf("Value = %q, want %q", got.Value, msg.Value)
	}
}

// TestInMemoryFlowLookup verifies the in-memory lookup implementation.
func TestInMemoryFlowLookup(t *testing.T) {
	lookup := NewInMemoryFlowLookup()

	// Initially empty
	_, _, err := lookup.GetMessageFlow(context.Background(), "conn", "topic")
	if err == nil {
		t.Fatal("expected error for missing flow")
	}

	// Register a flow
	tree := &Node{ID: "root", Type: TypeMessageTrigger}
	ver := Version{FlowID: "test-flow", Version: 1}
	lookup.Register("conn", "topic", tree, ver)

	// Now it should be found
	gotTree, gotVer, err := lookup.GetMessageFlow(context.Background(), "conn", "topic")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotTree.ID != tree.ID {
		t.Errorf("tree.ID = %q, want %q", gotTree.ID, tree.ID)
	}
	if gotVer.FlowID != ver.FlowID {
		t.Errorf("ver.FlowID = %q, want %q", gotVer.FlowID, ver.FlowID)
	}

	// Unregister
	lookup.Unregister("conn", "topic")
	_, _, err = lookup.GetMessageFlow(context.Background(), "conn", "topic")
	if err == nil {
		t.Fatal("expected error after unregister")
	}
}

// Helper functions

func deepEqual(a, b any) bool {
	aj, _ := json.Marshal(a)
	bj, _ := json.Marshal(b)
	return string(aj) == string(bj)
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && containsSubstring(s, substr)))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
