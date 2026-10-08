package drivers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/segmentio/kafka-go"
	"github.com/segmentio/kafka-go/sasl"
	"github.com/segmentio/kafka-go/sasl/plain"
	"github.com/segmentio/kafka-go/sasl/scram"

	"nzr-rules-engine/internal/connect"
)

// kafkaConnector builds kafka-go-backed clients for the "kafka" connection type.
// It implements LifecycleLongLived for persistent consumer semantics with
// server-push message delivery. The consumer uses GroupID from settings for
// consumer group management; rebalance is handled internally by kafka-go.
type kafkaConnector struct{}

// newKafkaConnector returns the kafka Connector.
func newKafkaConnector() connect.Connector { return kafkaConnector{} }

// Type implements connect.Connector.
func (kafkaConnector) Type() string { return "kafka" }

// Lifecycle implements connect.Connector. Kafka is long-lived: maintains a
// persistent connection with server-push semantics (consumer group membership).
func (kafkaConnector) Lifecycle() connect.Lifecycle { return connect.LifecycleLongLived }

// Capabilities implements connect.Connector. Kafka supports both consuming
// (subscribe) and producing (publish) messages.
func (kafkaConnector) Capabilities() connect.Capability {
	return connect.CapSubscribe | connect.CapPublish
}

// SecretSchema implements connect.SecretSchemaProvider. Kafka accepts optional
// SASL username and password secrets for authenticated connections.
func (kafkaConnector) SecretSchema() []connect.SecretField {
	return []connect.SecretField{
		{Name: "username", Required: false, Label: "SASL Username"},
		{Name: "password", Required: false, Label: "SASL Password"},
	}
}

// Open builds a Kafka client from the def's Settings. Settings should include:
//   - brokers: []string or comma-separated string of broker addresses
//   - topic: string topic name for consuming/producing
//   - groupID: string consumer group ID (required for consumption)
//   - partition (optional): int partition number (default 0 for producer)
//   - saslMechanism (optional): string SASL mechanism ("PLAIN", "SCRAM-SHA-256", "SCRAM-SHA-512")
//
// When saslMechanism is set, secrets "username" and "password" are read from
// SecretsFrom(ctx) for authentication.
//
// For consumers, messages are committed after successful handler execution.
// Failed handlers trigger dead-letter routing to "{topic}-dlq".
func (kafkaConnector) Open(ctx context.Context, def connect.ConnectionDef) (connect.Client, error) {
	brokers := kafkaBrokers(def.Settings)
	if len(brokers) == 0 {
		return nil, connect.NewConnError(connect.Validation, def.Key, "", "kafka settings need brokers", nil)
	}

	topic, ok := stringSetting(def.Settings, "topic")
	if !ok || topic == "" {
		return nil, connect.NewConnError(connect.Validation, def.Key, "", "kafka settings need topic", nil)
	}

	groupID, _ := stringSetting(def.Settings, "groupID")

	partition := 0
	if p, ok := intSetting(def.Settings, "partition"); ok {
		partition = p
	}

	// Extract dead-letter topic (defaults to {topic}-dlq)
	dlqTopic, _ := stringSetting(def.Settings, "dlqTopic")
	if dlqTopic == "" {
		dlqTopic = topic + "-dlq"
	}

	// Max retries before dead-letter routing (default 3)
	maxRetries := 3
	if r, ok := intSetting(def.Settings, "maxRetries"); ok {
		maxRetries = r
	}

	// Build SASL dialer if mechanism is configured
	var dialer *kafka.Dialer
	if mechanism, ok := stringSetting(def.Settings, "saslMechanism"); ok && mechanism != "" {
		var username, password string
		if secrets, ok := connect.SecretsFrom(ctx); ok {
			if userSec, found := secrets["username"]; found && !userSec.IsZero() {
				username = string(userSec.Reveal())
			}
			if pwSec, found := secrets["password"]; found && !pwSec.IsZero() {
				password = string(pwSec.Reveal())
			}
		}
		saslMech, err := buildSASLMechanism(mechanism, username, password)
		if err != nil {
			return nil, connect.NewConnError(connect.Validation, def.Key, "", "invalid sasl mechanism", err)
		}
		dialer = &kafka.Dialer{
			SASLMechanism: saslMech,
		}
	}

	return &kafkaClient{
		key:        def.Key,
		brokers:    brokers,
		topic:      topic,
		groupID:    groupID,
		partition:  partition,
		dlqTopic:   dlqTopic,
		maxRetries: maxRetries,
		dialer:     dialer,
	}, nil
}

// kafkaClient is the inner driver client. It manages Kafka reader (consumer)
// and writer (producer) instances. The reader is created on Subscribe; the
// writer is created lazily on first publish or dead-letter routing.
type kafkaClient struct {
	key        string
	brokers    []string
	topic      string
	groupID    string
	partition  int
	dlqTopic   string
	maxRetries int
	dialer     *kafka.Dialer // optional SASL dialer for authenticated connections

	mu       sync.Mutex
	reader   *kafka.Reader
	writer   *kafka.Writer
	stopCh   chan struct{}
	doneCh   chan struct{}
	closed   atomic.Bool
	wg       sync.WaitGroup
	handler  connect.MessageHandler
	draining atomic.Bool
}

// Execute runs a Kafka operation: subscribe, unsubscribe, publish, or ping.
func (c *kafkaClient) Execute(ctx context.Context, op connect.Operation) (any, error) {
	if c.closed.Load() {
		return nil, connect.NewConnError(connect.Validation, c.key, op.Kind, "client is closed", nil)
	}

	switch op.Kind {
	case "subscribe":
		handler, ok := op.Payload["handler"].(connect.MessageHandler)
		if !ok || handler == nil {
			return nil, connect.NewConnError(connect.Validation, c.key, op.Kind, "subscribe requires handler in payload", nil)
		}
		return nil, c.subscribe(ctx, handler)
	case "unsubscribe":
		return nil, c.unsubscribe(ctx)
	case "publish":
		return c.publish(ctx, op)
	case "ping":
		return c.ping(ctx)
	default:
		return nil, connect.NewConnError(connect.Validation, c.key, op.Kind, "unsupported operation kind for kafka", nil)
	}
}

// subscribe starts consuming messages from the configured topic using a
// consumer group. Messages are passed to the handler; committed on success,
// sent to dead-letter on repeated failure after maxRetries.
func (c *kafkaClient) subscribe(ctx context.Context, handler connect.MessageHandler) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.reader != nil {
		return connect.NewConnError(connect.Validation, c.key, "subscribe", "already subscribed", nil)
	}
	if c.groupID == "" {
		return connect.NewConnError(connect.Validation, c.key, "subscribe", "groupID required for subscription", nil)
	}

	// Create the consumer reader. kafka-go handles rebalance internally via
	// consumer groups. When a rebalance occurs, in-flight messages complete
	// before partition release (coordinated by CommitMessages).
	readerCfg := kafka.ReaderConfig{
		Brokers:  c.brokers,
		Topic:    c.topic,
		GroupID:  c.groupID,
		MaxBytes: 10e6, // 10MB max per fetch
		// StartOffset only applies when no committed offset exists for the group
		StartOffset: kafka.FirstOffset,
	}
	if c.dialer != nil {
		readerCfg.Dialer = c.dialer
	}
	c.reader = kafka.NewReader(readerCfg)

	c.handler = handler
	c.stopCh = make(chan struct{})
	c.doneCh = make(chan struct{})
	c.draining.Store(false)

	c.wg.Add(1)
	go c.consumeLoop(ctx)

	return nil
}

// consumeLoop reads messages from Kafka and passes them to the handler.
// On handler success, the message offset is committed. On repeated failure
// (after maxRetries), the message is sent to the dead-letter topic.
func (c *kafkaClient) consumeLoop(ctx context.Context) {
	defer c.wg.Done()
	defer close(c.doneCh)

	for {
		select {
		case <-c.stopCh:
			return
		default:
		}

		// Use a short timeout for reads so we can check stop channel periodically
		readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		msg, err := c.reader.FetchMessage(readCtx)
		cancel()

		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				// Check if we should stop
				select {
				case <-c.stopCh:
					return
				default:
					continue
				}
			}
			if errors.Is(err, io.EOF) {
				return
			}
			// Log and continue on transient errors
			continue
		}

		// Process the message with retries
		c.processMessage(ctx, msg)
	}
}

// processMessage handles a single message with retry logic and dead-letter routing.
func (c *kafkaClient) processMessage(ctx context.Context, msg kafka.Message) {
	cm := connect.Message{
		Key:       msg.Key,
		Value:     msg.Value,
		Topic:     msg.Topic,
		Partition: msg.Partition,
		Offset:    msg.Offset,
		Timestamp: msg.Time,
		Headers:   make(map[string]string),
	}
	for _, h := range msg.Headers {
		cm.Headers[h.Key] = string(h.Value)
	}

	var lastErr error
	for attempt := 1; attempt <= c.maxRetries; attempt++ {
		if err := c.handler(ctx, cm); err != nil {
			lastErr = err
			// Apply backoff between retries
			if attempt < c.maxRetries {
				time.Sleep(time.Duration(attempt*100) * time.Millisecond)
			}
			continue
		}
		// Success: commit the offset
		if err := c.reader.CommitMessages(ctx, msg); err != nil {
			// Log commit failure but don't retry the message
			_ = err
		}
		return
	}

	// All retries exhausted: send to dead-letter topic
	c.sendToDeadLetter(ctx, msg, lastErr)
	// Commit the original message to avoid reprocessing
	_ = c.reader.CommitMessages(ctx, msg)
}

// sendToDeadLetter publishes a failed message to the dead-letter topic.
func (c *kafkaClient) sendToDeadLetter(ctx context.Context, msg kafka.Message, err error) {
	writer := c.getOrCreateWriter()
	if writer == nil {
		return
	}

	headers := make([]kafka.Header, 0, len(msg.Headers)+3)
	headers = append(headers, msg.Headers...)
	headers = append(headers,
		kafka.Header{Key: "x-original-topic", Value: []byte(msg.Topic)},
		kafka.Header{Key: "x-original-partition", Value: []byte(fmt.Sprintf("%d", msg.Partition))},
		kafka.Header{Key: "x-error", Value: []byte(err.Error())},
	)

	dlqMsg := kafka.Message{
		Topic:   c.dlqTopic,
		Key:     msg.Key,
		Value:   msg.Value,
		Headers: headers,
	}

	// Best-effort write to DLQ; don't block the consumer on DLQ failures
	dlqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	_ = writer.WriteMessages(dlqCtx, dlqMsg)
	cancel()
}

// getOrCreateWriter returns the writer, creating it lazily if needed.
func (c *kafkaClient) getOrCreateWriter() *kafka.Writer {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.writer != nil {
		return c.writer
	}

	w := &kafka.Writer{
		Addr:         kafka.TCP(c.brokers...),
		Balancer:     &kafka.LeastBytes{},
		RequiredAcks: kafka.RequireOne,
		Async:        false, // Sync writes for reliability
	}
	if c.dialer != nil {
		w.Transport = &kafka.Transport{
			SASL: c.dialer.SASLMechanism,
		}
	}
	c.writer = w
	return c.writer
}

// unsubscribe stops consuming and waits for in-flight messages to complete.
// This implements graceful shutdown with message drain.
func (c *kafkaClient) unsubscribe(ctx context.Context) error {
	c.mu.Lock()
	if c.reader == nil {
		c.mu.Unlock()
		return nil
	}
	c.draining.Store(true)
	close(c.stopCh)
	reader := c.reader
	c.reader = nil
	c.mu.Unlock()

	// Wait for the consume loop to finish (drains in-flight messages)
	select {
	case <-c.doneCh:
	case <-ctx.Done():
		// Context cancelled; proceed with close anyway
	}

	c.wg.Wait()
	return reader.Close()
}

// publish sends a message to the configured topic.
func (c *kafkaClient) publish(ctx context.Context, op connect.Operation) (any, error) {
	key, _ := op.Payload["key"].([]byte)
	if key == nil {
		if keyStr, ok := stringSetting(op.Payload, "key"); ok {
			key = []byte(keyStr)
		}
	}

	var value []byte
	switch v := op.Payload["value"].(type) {
	case []byte:
		value = v
	case string:
		value = []byte(v)
	default:
		return nil, connect.NewConnError(connect.Validation, c.key, op.Kind, "value must be string or []byte", nil)
	}

	topic := c.topic
	if t, ok := stringSetting(op.Payload, "topic"); ok && t != "" {
		topic = t
	}

	var headers []kafka.Header
	if h, ok := op.Payload["headers"].(map[string]string); ok {
		for k, v := range h {
			headers = append(headers, kafka.Header{Key: k, Value: []byte(v)})
		}
	}

	writer := c.getOrCreateWriter()
	msg := kafka.Message{
		Topic:   topic,
		Key:     key,
		Value:   value,
		Headers: headers,
	}

	if err := writer.WriteMessages(ctx, msg); err != nil {
		return nil, c.classify("publish", err)
	}

	return map[string]any{"ok": true, "topic": topic}, nil
}

// ping verifies connectivity to the Kafka cluster.
func (c *kafkaClient) ping(ctx context.Context) (any, error) {
	var conn *kafka.Conn
	var err error
	if c.dialer != nil {
		conn, err = c.dialer.DialContext(ctx, "tcp", c.brokers[0])
	} else {
		conn, err = kafka.DialContext(ctx, "tcp", c.brokers[0])
	}
	if err != nil {
		return nil, c.classify("ping", err)
	}
	defer conn.Close()

	// Fetch brokers as a lightweight health check
	_, err = conn.Brokers()
	if err != nil {
		return nil, c.classify("ping", err)
	}

	return map[string]any{"ok": true}, nil
}

// Close shuts down the client, stopping any active subscription.
func (c *kafkaClient) Close() error {
	if !c.closed.CompareAndSwap(false, true) {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var errs []error

	if err := c.unsubscribe(ctx); err != nil {
		errs = append(errs, err)
	}

	c.mu.Lock()
	writer := c.writer
	c.writer = nil
	c.mu.Unlock()

	if writer != nil {
		if err := writer.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

// classify maps a Kafka error to the shared taxonomy.
func (c *kafkaClient) classify(opKind string, err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return connect.NewConnError(connect.Timeout, c.key, opKind, "kafka deadline exceeded", err)
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return connect.NewConnError(connect.Upstream, c.key, opKind, "kafka network error", err)
	}
	return connect.NewConnError(connect.Upstream, c.key, opKind, "kafka error", err)
}

// kafkaBrokers extracts broker addresses from settings, supporting both
// []string and comma-separated string formats.
func kafkaBrokers(s map[string]any) []string {
	if s == nil {
		return nil
	}
	// Try []string first
	if brokers, ok := s["brokers"].([]string); ok && len(brokers) > 0 {
		return brokers
	}
	// Try []interface{} (from JSON decode)
	if brokers, ok := s["brokers"].([]any); ok && len(brokers) > 0 {
		result := make([]string, 0, len(brokers))
		for _, b := range brokers {
			if str, ok := b.(string); ok && str != "" {
				result = append(result, str)
			}
		}
		if len(result) > 0 {
			return result
		}
	}
	// Try single string (comma-separated)
	if broker, ok := stringSetting(s, "brokers"); ok && broker != "" {
		return splitCSV(broker)
	}
	// Fallback: single broker
	if broker, ok := stringSetting(s, "broker"); ok && broker != "" {
		return []string{broker}
	}
	return nil
}

// splitCSV splits a comma-separated string and trims whitespace.
func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	var result []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			part := trimSpace(s[start:i])
			if part != "" {
				result = append(result, part)
			}
			start = i + 1
		}
	}
	return result
}

// trimSpace removes leading and trailing whitespace.
func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}

// buildSASLMechanism creates a SASL mechanism based on the mechanism name.
func buildSASLMechanism(mechanism, username, password string) (sasl.Mechanism, error) {
	switch mechanism {
	case "PLAIN":
		return &plain.Mechanism{
			Username: username,
			Password: password,
		}, nil
	case "SCRAM-SHA-256":
		return scram.Mechanism(scram.SHA256, username, password)
	case "SCRAM-SHA-512":
		return scram.Mechanism(scram.SHA512, username, password)
	default:
		return nil, errors.New("unsupported SASL mechanism: " + mechanism)
	}
}
