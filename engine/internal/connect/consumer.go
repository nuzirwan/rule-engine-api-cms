package connect

import (
	"context"
	"time"
)

// Message represents a message consumed from an async provider (Kafka, RabbitMQ, etc.).
// It captures the standard fields that all message brokers provide.
type Message struct {
	// Key is the message key (partition key in Kafka, routing key in RabbitMQ).
	Key []byte
	// Value is the message payload.
	Value []byte
	// Topic is the topic/queue name the message was consumed from.
	Topic string
	// Partition is the partition number (Kafka-specific; 0 for non-partitioned systems).
	Partition int
	// Offset is the message offset within the partition (Kafka-specific; 0 for others).
	Offset int64
	// Timestamp is when the message was produced (broker timestamp if available).
	Timestamp time.Time
	// Headers are key-value metadata attached to the message.
	Headers map[string]string
}

// MessageHandler is the callback invoked for each consumed message. The handler
// returns an error if processing fails; the consumer implementation decides
// whether to retry, dead-letter, or skip based on the error class.
type MessageHandler func(ctx context.Context, msg Message) error

// Consumer is a connector that supports message subscription. It embeds Connector
// for type identification and Open/Close semantics, and adds Subscribe/Unsubscribe
// for async message consumption.
//
// A Consumer has LifecycleLongLived and CapSubscribe capability. The ConsumerManager
// calls Subscribe at startup and manages the goroutine lifecycle, restart backoff,
// and graceful shutdown.
type Consumer interface {
	Connector

	// Subscribe starts consuming messages from the configured topic(s) and invokes
	// the handler for each message. Subscribe blocks until ctx is cancelled or an
	// unrecoverable error occurs. The caller (ConsumerManager) runs Subscribe in a
	// goroutine and handles restarts on failure.
	//
	// The handler is called synchronously for each message; if the handler returns
	// an error, Subscribe may log it and continue (at-most-once) or retry/dead-letter
	// (at-least-once) depending on the connector's configuration.
	Subscribe(ctx context.Context, handler MessageHandler) error

	// Unsubscribe gracefully stops the consumer. It signals the Subscribe loop to
	// stop accepting new messages and waits for in-flight messages to complete
	// (up to the context deadline). After Unsubscribe returns, Subscribe will exit.
	Unsubscribe(ctx context.Context) error
}
