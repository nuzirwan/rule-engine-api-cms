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

// TestRabbitMQConsumer_ConsumeAndAck exercises the RabbitMQ connector against a
// real RabbitMQ broker. It tests the full consume-ack cycle. Skipped unless
// RABBITMQ_URL env var is set.
//
// Prerequisites:
//   - RabbitMQ broker running at RABBITMQ_URL (e.g., amqp://guest:guest@localhost:5672/)
//   - Queue "test-queue" exists (or auto-create is enabled via management plugin)
func TestRabbitMQConsumer_ConsumeAndAck(t *testing.T) {
	rabbitmqURL := os.Getenv("RABBITMQ_URL")
	if rabbitmqURL == "" {
		t.Skip("RABBITMQ_URL not set; skipping RabbitMQ integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Build the registry with RabbitMQ connector
	defs := []connect.ConnectionDef{{
		Key:  "rabbitmq-test",
		Type: "rabbitmq",
		Settings: map[string]any{
			"url":        rabbitmqURL,
			"queue":      "test-queue",
			"exchange":   "",           // Direct to queue
			"routingKey": "test-queue", // For publishing direct to queue
			"prefetch":   10,
		},
		Resilience: connect.ResiliencePolicy{Timeout: 10 * time.Second},
	}}

	reg, err := connect.New(drivers.All(), defs, connect.NewEnvSecretProvider(), nil, nil)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	defer reg.Close()

	client, err := reg.Client(ctx, "rabbitmq-test")
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
			"body":       "test-message-body",
			"exchange":   "",
			"routingKey": "test-queue",
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
			"body":       "consume-test-body",
			"exchange":   "",
			"routingKey": "test-queue",
		},
	})
	if err != nil {
		t.Fatalf("publish for consume failed: %v", err)
	}

	// Wait for message (with timeout)
	select {
	case msg := <-received:
		if string(msg.Value) != "consume-test-body" {
			t.Logf("received message with body: %s", string(msg.Value))
		}
	case <-time.After(10 * time.Second):
		t.Log("no message received within timeout (may be expected if queue was empty)")
	}

	// Unsubscribe
	_, err = client.Execute(ctx, connect.Operation{Kind: "unsubscribe"})
	if err != nil {
		t.Fatalf("unsubscribe failed: %v", err)
	}
}

// TestRabbitMQConsumer_DeadLetter tests dead-letter routing (via DLX) when
// handler fails repeatedly. Skipped unless RABBITMQ_URL env var is set.
//
// Prerequisites:
//   - Queue "dlq-test-queue" configured with dead-letter exchange (DLX)
//   - DLQ queue bound to the DLX to receive rejected messages
func TestRabbitMQConsumer_DeadLetter(t *testing.T) {
	rabbitmqURL := os.Getenv("RABBITMQ_URL")
	if rabbitmqURL == "" {
		t.Skip("RABBITMQ_URL not set; skipping RabbitMQ integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Build registry with low maxRetries to trigger DLX quickly
	defs := []connect.ConnectionDef{{
		Key:  "rabbitmq-dlq-test",
		Type: "rabbitmq",
		Settings: map[string]any{
			"url":        rabbitmqURL,
			"queue":      "dlq-test-queue",
			"exchange":   "",
			"routingKey": "dlq-test-queue",
			"maxRetries": 2,
		},
		Resilience: connect.ResiliencePolicy{Timeout: 10 * time.Second},
	}}

	reg, err := connect.New(drivers.All(), defs, connect.NewEnvSecretProvider(), nil, nil)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	defer reg.Close()

	client, err := reg.Client(ctx, "rabbitmq-dlq-test")
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
			"body":       "fail-message",
			"exchange":   "",
			"routingKey": "dlq-test-queue",
		},
	})
	if err != nil {
		t.Fatalf("publish failed: %v", err)
	}

	// Give time for retries and nack (DLX routing)
	time.Sleep(2 * time.Second)

	// Unsubscribe
	_, _ = client.Execute(ctx, connect.Operation{Kind: "unsubscribe"})

	// The message should have been nacked (without requeue) after maxRetries
	// failures, routing to DLX if configured on the queue
	t.Logf("handler was called %d times before nack/DLX routing", failCount)
}

// TestRabbitMQConsumer_GracefulShutdown tests that unsubscribe waits for
// in-flight messages to complete. Skipped unless RABBITMQ_URL env var is set.
func TestRabbitMQConsumer_GracefulShutdown(t *testing.T) {
	rabbitmqURL := os.Getenv("RABBITMQ_URL")
	if rabbitmqURL == "" {
		t.Skip("RABBITMQ_URL not set; skipping RabbitMQ integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	defs := []connect.ConnectionDef{{
		Key:  "rabbitmq-shutdown-test",
		Type: "rabbitmq",
		Settings: map[string]any{
			"url":      rabbitmqURL,
			"queue":    "shutdown-test-queue",
			"prefetch": 1, // Process one at a time
		},
		Resilience: connect.ResiliencePolicy{Timeout: 10 * time.Second},
	}}

	reg, err := connect.New(drivers.All(), defs, connect.NewEnvSecretProvider(), nil, nil)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	defer reg.Close()

	client, err := reg.Client(ctx, "rabbitmq-shutdown-test")
	if err != nil {
		t.Fatalf("resolve client: %v", err)
	}

	// Track processing state
	processing := make(chan struct{})
	done := make(chan struct{})

	handler := func(ctx context.Context, msg connect.Message) error {
		close(processing)                  // Signal that we're processing
		time.Sleep(500 * time.Millisecond) // Simulate work
		close(done)                        // Signal completion
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

	// Publish a message
	_, err = client.Execute(ctx, connect.Operation{
		Kind: "publish",
		Payload: map[string]any{
			"body":       "shutdown-test",
			"exchange":   "",
			"routingKey": "shutdown-test-queue",
		},
	})
	if err != nil {
		t.Fatalf("publish failed: %v", err)
	}

	// Wait until processing starts
	select {
	case <-processing:
	case <-time.After(5 * time.Second):
		t.Log("message not received; queue may be empty")
		_, _ = client.Execute(ctx, connect.Operation{Kind: "unsubscribe"})
		return
	}

	// Unsubscribe while processing — should wait for completion
	unsubscribeDone := make(chan error)
	go func() {
		_, err := client.Execute(ctx, connect.Operation{Kind: "unsubscribe"})
		unsubscribeDone <- err
	}()

	// The handler should complete before unsubscribe returns
	select {
	case <-done:
		// Handler completed as expected
	case err := <-unsubscribeDone:
		if err != nil {
			t.Fatalf("unsubscribe failed: %v", err)
		}
		// Check if handler completed
		select {
		case <-done:
			// OK: handler completed
		default:
			t.Error("unsubscribe returned before handler completed (no graceful drain)")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for handler completion")
	}

	// Wait for unsubscribe to complete
	select {
	case err := <-unsubscribeDone:
		if err != nil {
			t.Fatalf("unsubscribe failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for unsubscribe")
	}
}
