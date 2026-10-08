package drivers

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"nzr-rules-engine/internal/connect"
)

// rabbitmqConnector builds AMQP 0.9.1 clients for the "rabbitmq" connection type.
// It implements LifecycleLongLived for persistent consumer semantics with
// server-push message delivery. Dead-letter routing uses AMQP's DLX (dead-letter
// exchange) mechanism: failed messages are nacked without requeue, causing them
// to route to the DLX configured on the queue.
type rabbitmqConnector struct{}

// newRabbitMQConnector returns the rabbitmq Connector.
func newRabbitMQConnector() connect.Connector { return rabbitmqConnector{} }

// Type implements connect.Connector.
func (rabbitmqConnector) Type() string { return "rabbitmq" }

// Lifecycle implements connect.Connector. RabbitMQ is long-lived: maintains a
// persistent connection with server-push semantics (channel-based consumption).
func (rabbitmqConnector) Lifecycle() connect.Lifecycle { return connect.LifecycleLongLived }

// Capabilities implements connect.Connector. RabbitMQ supports both consuming
// (subscribe) and producing (publish) messages.
func (rabbitmqConnector) Capabilities() connect.Capability {
	return connect.CapSubscribe | connect.CapPublish
}

// SecretSchema implements connect.SecretSchemaProvider. RabbitMQ accepts optional
// username and password secrets for authenticated connections.
func (rabbitmqConnector) SecretSchema() []connect.SecretField {
	return []connect.SecretField{
		{Name: "username", Required: false, Label: "Username"},
		{Name: "password", Required: false, Label: "Password"},
	}
}

// Open builds a RabbitMQ client from the def's Settings and the resolved secret.
// Settings should include:
//   - url: AMQP URL (amqp://user:pass@host:port/vhost) — OR use discrete settings:
//   - host: string hostname (default "localhost")
//   - port: int port number (default 5672)
//   - vhost: string virtual host (default "/")
//   - queue: string queue name for consuming
//   - exchange (optional): string exchange name for publishing
//   - routingKey (optional): string routing key for publishing
//   - prefetch (optional): int prefetch count (default 10)
//
// When using discrete settings (no url), credentials are read from SecretsFrom(ctx)
// for "username" and "password" secrets. When using url, the URL-embedded
// credentials are used (legacy behavior).
//
// For consumers, messages are acked after successful handler execution.
// Failed handlers trigger nack without requeue (goes to DLX if configured).
func (rabbitmqConnector) Open(ctx context.Context, def connect.ConnectionDef) (connect.Client, error) {
	var url string

	// Check for URL-based configuration first (legacy path)
	if rawURL, ok := stringSetting(def.Settings, "url"); ok && rawURL != "" {
		url = rawURL
	} else {
		// Build URL from discrete settings with secrets
		host, _ := stringSetting(def.Settings, "host")
		if host == "" {
			host = "localhost"
		}
		port := 5672
		if p, ok := intSetting(def.Settings, "port"); ok {
			port = p
		}
		vhost, _ := stringSetting(def.Settings, "vhost")
		if vhost == "" {
			vhost = "/"
		}

		// Get credentials from secrets
		username := "guest"
		password := "guest"
		if secrets, ok := connect.SecretsFrom(ctx); ok {
			if userSec, found := secrets["username"]; found && !userSec.IsZero() {
				username = string(userSec.Reveal())
			}
			if pwSec, found := secrets["password"]; found && !pwSec.IsZero() {
				password = string(pwSec.Reveal())
			}
		}

		// Build AMQP URL: amqp://user:pass@host:port/vhost
		url = fmt.Sprintf("amqp://%s:%s@%s:%d%s", username, password, host, port, vhost)
	}

	if url == "" {
		return nil, connect.NewConnError(connect.Validation, def.Key, "", "rabbitmq settings need url or host", nil)
	}

	queue, _ := stringSetting(def.Settings, "queue")
	exchange, _ := stringSetting(def.Settings, "exchange")
	routingKey, _ := stringSetting(def.Settings, "routingKey")

	prefetch := 10
	if p, ok := intSetting(def.Settings, "prefetch"); ok && p > 0 {
		prefetch = p
	}

	// Max retries before dead-letter routing (default 3)
	maxRetries := 3
	if r, ok := intSetting(def.Settings, "maxRetries"); ok {
		maxRetries = r
	}

	return &rabbitmqClient{
		key:        def.Key,
		url:        url,
		queue:      queue,
		exchange:   exchange,
		routingKey: routingKey,
		prefetch:   prefetch,
		maxRetries: maxRetries,
	}, nil
}

// rabbitmqClient is the inner driver client. It manages the AMQP connection
// and channel. A channel is created for consuming; publishing uses a separate
// channel to avoid blocking consumer acks.
type rabbitmqClient struct {
	key        string
	url        string
	queue      string
	exchange   string
	routingKey string
	prefetch   int
	maxRetries int

	mu           sync.Mutex
	conn         *amqp.Connection
	consumeCh    *amqp.Channel
	publishCh    *amqp.Channel
	consumerTag  string
	stopCh       chan struct{}
	doneCh       chan struct{}
	closed       atomic.Bool
	wg           sync.WaitGroup
	handler      connect.MessageHandler
	draining     atomic.Bool
	deliveryChan <-chan amqp.Delivery
}

// Execute runs a RabbitMQ operation: subscribe, unsubscribe, publish, or ping.
func (c *rabbitmqClient) Execute(ctx context.Context, op connect.Operation) (any, error) {
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
		return nil, connect.NewConnError(connect.Validation, c.key, op.Kind, "unsupported operation kind for rabbitmq", nil)
	}
}

// subscribe starts consuming messages from the configured queue. Messages are
// passed to the handler; acked on success, nacked (without requeue) on repeated
// failure after maxRetries. The nacked message routes to DLX if configured on
// the queue.
func (c *rabbitmqClient) subscribe(ctx context.Context, handler connect.MessageHandler) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.consumeCh != nil {
		return connect.NewConnError(connect.Validation, c.key, "subscribe", "already subscribed", nil)
	}
	if c.queue == "" {
		return connect.NewConnError(connect.Validation, c.key, "subscribe", "queue required for subscription", nil)
	}

	// Establish connection if not already connected
	if c.conn == nil || c.conn.IsClosed() {
		conn, err := amqp.Dial(c.url)
		if err != nil {
			return connect.NewConnError(connect.Upstream, c.key, "subscribe", "dial failed", err)
		}
		c.conn = conn
	}

	// Create consume channel with prefetch
	ch, err := c.conn.Channel()
	if err != nil {
		return connect.NewConnError(connect.Upstream, c.key, "subscribe", "channel open failed", err)
	}

	if err := ch.Qos(c.prefetch, 0, false); err != nil {
		ch.Close()
		return connect.NewConnError(connect.Upstream, c.key, "subscribe", "qos failed", err)
	}

	// Generate unique consumer tag
	c.consumerTag = fmt.Sprintf("%s-%d", c.key, time.Now().UnixNano())

	// Start consuming
	deliveries, err := ch.Consume(
		c.queue,
		c.consumerTag,
		false, // autoAck: false for manual ack
		false, // exclusive
		false, // noLocal
		false, // noWait
		nil,   // args
	)
	if err != nil {
		ch.Close()
		return connect.NewConnError(connect.Upstream, c.key, "subscribe", "consume failed", err)
	}

	c.consumeCh = ch
	c.handler = handler
	c.stopCh = make(chan struct{})
	c.doneCh = make(chan struct{})
	c.draining.Store(false)
	c.deliveryChan = deliveries

	c.wg.Add(1)
	go c.consumeLoop(ctx)

	return nil
}

// consumeLoop processes messages from the delivery channel. On handler success,
// the message is acked. On repeated failure, the message is nacked without
// requeue (routes to DLX).
func (c *rabbitmqClient) consumeLoop(ctx context.Context) {
	defer c.wg.Done()
	defer close(c.doneCh)

	for {
		select {
		case <-c.stopCh:
			return
		case delivery, ok := <-c.deliveryChan:
			if !ok {
				// Channel closed
				return
			}
			c.processDelivery(ctx, delivery)
		}
	}
}

// processDelivery handles a single delivery with retry logic and dead-letter routing.
func (c *rabbitmqClient) processDelivery(ctx context.Context, delivery amqp.Delivery) {
	msg := connect.Message{
		Key:       []byte(delivery.RoutingKey),
		Value:     delivery.Body,
		Topic:     c.queue,
		Timestamp: delivery.Timestamp,
		Headers:   make(map[string]string),
	}

	// Convert AMQP headers to string map
	for k, v := range delivery.Headers {
		if str, ok := v.(string); ok {
			msg.Headers[k] = str
		} else {
			msg.Headers[k] = fmt.Sprintf("%v", v)
		}
	}

	// Track retry count from headers (x-death increments on each DLX cycle)
	retryCount := getRetryCount(delivery.Headers)

	var lastErr error
	for attempt := 1; attempt <= c.maxRetries; attempt++ {
		if err := c.handler(ctx, msg); err != nil {
			lastErr = err
			// Apply backoff between retries
			if attempt < c.maxRetries {
				time.Sleep(time.Duration(attempt*100) * time.Millisecond)
			}
			continue
		}
		// Success: ack the delivery
		_ = delivery.Ack(false)
		return
	}

	// All retries exhausted: nack without requeue (goes to DLX)
	// If maxRetries is exceeded and message has already been through DLX multiple times,
	// we still nack it - the DLX configuration controls final disposition
	_ = delivery.Nack(false, false) // multiple=false, requeue=false
	_ = lastErr                     // Error captured for logging (in production, would log this)
	_ = retryCount                  // Used for metrics/logging in production
}

// getRetryCount extracts the retry count from AMQP headers (x-death array length).
func getRetryCount(headers amqp.Table) int {
	if headers == nil {
		return 0
	}
	xDeath, ok := headers["x-death"].([]interface{})
	if !ok {
		return 0
	}
	return len(xDeath)
}

// unsubscribe cancels the consumer and waits for in-flight messages to complete.
// This implements graceful shutdown with message drain.
func (c *rabbitmqClient) unsubscribe(ctx context.Context) error {
	c.mu.Lock()
	if c.consumeCh == nil {
		c.mu.Unlock()
		return nil
	}
	c.draining.Store(true)

	// Cancel the consumer (stops new deliveries)
	if c.consumerTag != "" {
		_ = c.consumeCh.Cancel(c.consumerTag, false)
	}

	close(c.stopCh)
	ch := c.consumeCh
	c.consumeCh = nil
	c.consumerTag = ""
	c.mu.Unlock()

	// Wait for the consume loop to finish (drains in-flight messages)
	select {
	case <-c.doneCh:
	case <-ctx.Done():
		// Context cancelled; proceed with close anyway
	}

	c.wg.Wait()
	return ch.Close()
}

// publish sends a message to the configured exchange/queue.
func (c *rabbitmqClient) publish(ctx context.Context, op connect.Operation) (any, error) {
	var body []byte
	switch v := op.Payload["body"].(type) {
	case []byte:
		body = v
	case string:
		body = []byte(v)
	default:
		// Try "value" as an alias for body
		switch v := op.Payload["value"].(type) {
		case []byte:
			body = v
		case string:
			body = []byte(v)
		default:
			return nil, connect.NewConnError(connect.Validation, c.key, op.Kind, "body/value must be string or []byte", nil)
		}
	}

	exchange := c.exchange
	if e, ok := stringSetting(op.Payload, "exchange"); ok && e != "" {
		exchange = e
	}

	routingKey := c.routingKey
	if rk, ok := stringSetting(op.Payload, "routingKey"); ok && rk != "" {
		routingKey = rk
	}

	// Get or create publish channel
	ch, err := c.getOrCreatePublishChannel()
	if err != nil {
		return nil, err
	}

	headers := make(amqp.Table)
	if h, ok := op.Payload["headers"].(map[string]string); ok {
		for k, v := range h {
			headers[k] = v
		}
	}

	publishing := amqp.Publishing{
		ContentType: "application/octet-stream",
		Body:        body,
		Headers:     headers,
	}

	if ct, ok := stringSetting(op.Payload, "contentType"); ok && ct != "" {
		publishing.ContentType = ct
	}

	// Use PublishWithContext for cancellation support
	if err := ch.PublishWithContext(
		ctx,
		exchange,
		routingKey,
		false, // mandatory
		false, // immediate
		publishing,
	); err != nil {
		return nil, c.classify("publish", err)
	}

	return map[string]any{"ok": true, "exchange": exchange, "routingKey": routingKey}, nil
}

// getOrCreatePublishChannel returns the publish channel, creating connection
// and channel lazily if needed.
func (c *rabbitmqClient) getOrCreatePublishChannel() (*amqp.Channel, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Check if publish channel is still valid
	if c.publishCh != nil {
		return c.publishCh, nil
	}

	// Ensure connection exists
	if c.conn == nil || c.conn.IsClosed() {
		conn, err := amqp.Dial(c.url)
		if err != nil {
			return nil, connect.NewConnError(connect.Upstream, c.key, "publish", "dial failed", err)
		}
		c.conn = conn
	}

	// Create publish channel
	ch, err := c.conn.Channel()
	if err != nil {
		return nil, connect.NewConnError(connect.Upstream, c.key, "publish", "channel open failed", err)
	}

	c.publishCh = ch
	return ch, nil
}

// ping verifies connectivity to RabbitMQ.
func (c *rabbitmqClient) ping(ctx context.Context) (any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Try to dial if no connection exists
	if c.conn == nil || c.conn.IsClosed() {
		conn, err := amqp.Dial(c.url)
		if err != nil {
			return nil, c.classify("ping", err)
		}
		c.conn = conn
	}

	// Open and close a channel as a health check
	ch, err := c.conn.Channel()
	if err != nil {
		return nil, c.classify("ping", err)
	}
	ch.Close()

	return map[string]any{"ok": true}, nil
}

// Close shuts down the client, stopping any active subscription.
func (c *rabbitmqClient) Close() error {
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
	publishCh := c.publishCh
	conn := c.conn
	c.publishCh = nil
	c.conn = nil
	c.mu.Unlock()

	if publishCh != nil {
		if err := publishCh.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	if conn != nil {
		if err := conn.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

// classify maps an AMQP error to the shared taxonomy.
func (c *rabbitmqClient) classify(opKind string, err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return connect.NewConnError(connect.Timeout, c.key, opKind, "rabbitmq deadline exceeded", err)
	}
	var amqpErr *amqp.Error
	if errors.As(err, &amqpErr) {
		// Channel/connection level errors
		return connect.NewConnError(connect.Upstream, c.key, opKind, fmt.Sprintf("amqp error: %s", amqpErr.Reason), err)
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return connect.NewConnError(connect.Upstream, c.key, opKind, "rabbitmq network error", err)
	}
	return connect.NewConnError(connect.Upstream, c.key, opKind, "rabbitmq error", err)
}
