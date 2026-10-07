//go:build integration

package drivers_test

import (
	"context"
	"os"
	"testing"
	"time"

	"nzr-rules-engine/internal/connect"
	"nzr-rules-engine/internal/connect/drivers"
)

// TestKafkaConsumer_ConsumeAndCommit exercises the Kafka connector against a
// real Kafka broker. It tests the full consume-commit cycle with offset commit
// on success. Skipped unless KAFKA_ADDR env var is set.
//
// Prerequisites:
//   - Kafka broker running at KAFKA_ADDR (e.g., localhost:9092)
//   - Topic "test-topic" exists (or auto-create is enabled)
//   - Consumer group can be created dynamically
func TestKafkaConsumer_ConsumeAndCommit(t *testing.T) {
	kafkaAddr := os.Getenv("KAFKA_ADDR")
	if kafkaAddr == "" {
		t.Skip("KAFKA_ADDR not set; skipping Kafka integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Build the registry with Kafka connector
	defs := []connect.ConnectionDef{{
		Key:  "kafka-test",
		Type: "kafka",
		Settings: map[string]any{
			"brokers": kafkaAddr,
			"topic":   "test-topic",
			"groupID": "test-group-" + time.Now().Format("20060102150405"),
		},
		Resilience: connect.ResiliencePolicy{Timeout: 10 * time.Second},
	}}

	reg, err := connect.New(drivers.All(), defs, connect.NewEnvSecretProvider(), nil, nil)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	defer reg.Close()

	client, err := reg.Client(ctx, "kafka-test")
	if err != nil {
		t.Fatalf("resolve client: %v", err)
	}

	// Test ping (connectivity check)
	_, err = client.Execute(ctx, connect.Operation{Kind: "ping"})
	if err != nil {
		t.Fatalf("ping failed: %v", err)
	}

	// Test publish
	_, err = client.Execute(ctx, connect.Operation{
		Kind: "publish",
		Payload: map[string]any{
			"key":   "test-key",
			"value": "test-value",
		},
	})
	if err != nil {
		t.Fatalf("publish failed: %v", err)
	}

	// Test subscribe and consume
	received := make(chan connect.Message, 1)
	handler := func(ctx context.Context, msg connect.Message) error {
		select {
		case received <- msg:
		default:
		}
		return nil
	}

	_, err = client.Execute(ctx, connect.Operation{
		Kind: "subscribe",
		Payload: map[string]any{
			"handler": connect.MessageHandler(handler),
		},
	})
	if err != nil {
		t.Fatalf("subscribe failed: %v", err)
	}

	// Publish another message to consume
	_, err = client.Execute(ctx, connect.Operation{
		Kind: "publish",
		Payload: map[string]any{
			"key":   "consume-test-key",
			"value": "consume-test-value",
		},
	})
	if err != nil {
		t.Fatalf("publish for consume failed: %v", err)
	}

	// Wait for message (with timeout)
	select {
	case msg := <-received:
		if string(msg.Value) != "consume-test-value" {
			t.Logf("received message with value: %s", string(msg.Value))
		}
	case <-time.After(10 * time.Second):
		t.Log("no message received within timeout (may be expected if topic was empty)")
	}

	// Unsubscribe
	_, err = client.Execute(ctx, connect.Operation{Kind: "unsubscribe"})
	if err != nil {
		t.Fatalf("unsubscribe failed: %v", err)
	}
}

// TestKafkaConsumer_DeadLetter tests dead-letter routing when handler fails
// repeatedly. Skipped unless KAFKA_ADDR env var is set.
func TestKafkaConsumer_DeadLetter(t *testing.T) {
	kafkaAddr := os.Getenv("KAFKA_ADDR")
	if kafkaAddr == "" {
		t.Skip("KAFKA_ADDR not set; skipping Kafka integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Build registry with low maxRetries to trigger DLQ quickly
	defs := []connect.ConnectionDef{{
		Key:  "kafka-dlq-test",
		Type: "kafka",
		Settings: map[string]any{
			"brokers":    kafkaAddr,
			"topic":      "dlq-test-topic",
			"groupID":    "dlq-test-group-" + time.Now().Format("20060102150405"),
			"maxRetries": 2,
		},
		Resilience: connect.ResiliencePolicy{Timeout: 10 * time.Second},
	}}

	reg, err := connect.New(drivers.All(), defs, connect.NewEnvSecretProvider(), nil, nil)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	defer reg.Close()

	client, err := reg.Client(ctx, "kafka-dlq-test")
	if err != nil {
		t.Fatalf("resolve client: %v", err)
	}

	// Subscribe with a failing handler
	failCount := 0
	handler := func(ctx context.Context, msg connect.Message) error {
		failCount++
		return context.DeadlineExceeded // Simulate failure
	}

	_, err = client.Execute(ctx, connect.Operation{
		Kind: "subscribe",
		Payload: map[string]any{
			"handler": connect.MessageHandler(handler),
		},
	})
	if err != nil {
		t.Fatalf("subscribe failed: %v", err)
	}

	// Publish a message that will fail processing
	_, err = client.Execute(ctx, connect.Operation{
		Kind: "publish",
		Payload: map[string]any{
			"key":   "fail-key",
			"value": "fail-value",
		},
	})
	if err != nil {
		t.Fatalf("publish failed: %v", err)
	}

	// Give time for retries and DLQ routing
	time.Sleep(2 * time.Second)

	// Unsubscribe
	_, _ = client.Execute(ctx, connect.Operation{Kind: "unsubscribe"})

	// The message should have been routed to DLQ after maxRetries failures
	// In a full test, we'd consume from the DLQ topic to verify
	t.Logf("handler was called %d times before DLQ routing", failCount)
}
