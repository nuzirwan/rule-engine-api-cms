package connect

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"nzr-rules-engine/internal/observ"
)

// ConsumerManagerConfig holds configuration for the consumer manager.
type ConsumerManagerConfig struct {
	// InitialBackoff is the starting backoff duration after a Subscribe failure.
	// Default: 1 second.
	InitialBackoff time.Duration

	// MaxBackoff is the maximum backoff duration between Subscribe retries.
	// Default: 60 seconds.
	MaxBackoff time.Duration

	// DrainTimeout is how long Stop waits for in-flight messages to complete
	// before forcing shutdown. Default: 30 seconds.
	DrainTimeout time.Duration
}

// DefaultConsumerManagerConfig returns the default consumer manager configuration.
func DefaultConsumerManagerConfig() ConsumerManagerConfig {
	return ConsumerManagerConfig{
		InitialBackoff: 1 * time.Second,
		MaxBackoff:     60 * time.Second,
		DrainTimeout:   30 * time.Second,
	}
}

// healthStatus represents the health state of a managed consumer.
type healthStatus int

const (
	healthUnknown healthStatus = iota
	healthStarting
	healthHealthy
	healthUnhealthy
)

// managedConsumer tracks a single Consumer's lifecycle and health.
type managedConsumer struct {
	mu       sync.RWMutex
	consumer Consumer
	key      string
	handler  MessageHandler
	health   healthStatus
	lastErr  error
	backoff  time.Duration
	cancel   context.CancelFunc
	done     chan struct{}
}

// ConsumerManager manages long-lived CapSubscribe connectors. It starts them at
// boot, tracks health, restarts with exponential backoff on failure, and provides
// graceful shutdown with drain.
type ConsumerManager struct {
	mu        sync.RWMutex
	consumers map[string]*managedConsumer
	handlers  map[string]MessageHandler
	defs      map[string]ConnectionDef
	byType    map[string]Connector
	secrets   SecretProvider
	config    ConsumerManagerConfig
	log       observ.Logger

	running atomic.Bool
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// NewConsumerManager creates a new consumer manager. It does NOT start any
// consumers — they are started when Start is called.
func NewConsumerManager(
	connectors []Connector,
	defs []ConnectionDef,
	secrets SecretProvider,
	config ConsumerManagerConfig,
	log observ.Logger,
) *ConsumerManager {
	if secrets == nil {
		secrets = NewEnvSecretProvider()
	}
	if config.InitialBackoff == 0 {
		config.InitialBackoff = DefaultConsumerManagerConfig().InitialBackoff
	}
	if config.MaxBackoff == 0 {
		config.MaxBackoff = DefaultConsumerManagerConfig().MaxBackoff
	}
	if config.DrainTimeout == 0 {
		config.DrainTimeout = DefaultConsumerManagerConfig().DrainTimeout
	}

	byType := make(map[string]Connector, len(connectors))
	for _, c := range connectors {
		byType[c.Type()] = c
	}

	defMap := make(map[string]ConnectionDef)
	for _, d := range defs {
		// Only track defs for connectors that support Subscribe
		if conn, ok := byType[d.Type]; ok && conn.Capabilities().Has(CapSubscribe) {
			defMap[d.Key] = d
		}
	}

	return &ConsumerManager{
		consumers: make(map[string]*managedConsumer),
		handlers:  make(map[string]MessageHandler),
		defs:      defMap,
		byType:    byType,
		secrets:   secrets,
		config:    config,
		log:       log,
	}
}

// RegisterHandler registers a message handler for a connection key. The handler
// is invoked for each message consumed from that connection. This must be called
// before Start for the handler to be active.
func (m *ConsumerManager) RegisterHandler(connectionKey string, handler MessageHandler) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.handlers[connectionKey] = handler
}

// Start begins all CapSubscribe connectors. For each connection definition that
// has a registered handler and a connector with CapSubscribe capability, it opens
// the consumer and starts a goroutine that calls Subscribe.
func (m *ConsumerManager) Start(ctx context.Context) error {
	if m.running.Load() {
		return nil // Already running
	}

	m.ctx, m.cancel = context.WithCancel(ctx)
	m.running.Store(true)

	m.mu.RLock()
	defs := make([]ConnectionDef, 0, len(m.defs))
	for _, d := range m.defs {
		defs = append(defs, d)
	}
	m.mu.RUnlock()

	for _, def := range defs {
		handler, hasHandler := m.handlers[def.Key]
		if !hasHandler {
			// No handler registered for this connection; skip
			if m.log != nil {
				m.log.Emit(ctx, "debug", "consumer_manager: no handler registered, skipping",
					map[string]any{"key": def.Key})
			}
			continue
		}

		consumer, err := m.openConsumer(ctx, def)
		if err != nil {
			if m.log != nil {
				m.log.Emit(ctx, "warn", "consumer_manager: failed to open consumer",
					map[string]any{"key": def.Key, "error": err.Error()})
			}
			// Don't fail Start; continue with other consumers
			continue
		}

		mc := &managedConsumer{
			consumer: consumer,
			key:      def.Key,
			handler:  handler,
			health:   healthStarting,
			backoff:  m.config.InitialBackoff,
			done:     make(chan struct{}),
		}

		consumerCtx, consumerCancel := context.WithCancel(m.ctx)
		mc.cancel = consumerCancel

		m.mu.Lock()
		m.consumers[def.Key] = mc
		m.mu.Unlock()

		m.wg.Add(1)
		go m.runConsumer(consumerCtx, mc, def)
	}

	return nil
}

// openConsumer creates a Consumer from a connection definition.
func (m *ConsumerManager) openConsumer(ctx context.Context, def ConnectionDef) (Consumer, error) {
	conn, ok := m.byType[def.Type]
	if !ok {
		return nil, newErr(Validation, def.Key, "", "no connector registered for type "+def.Type)
	}

	if !conn.Capabilities().Has(CapSubscribe) {
		return nil, newErr(Validation, def.Key, "", "connector does not support subscribe")
	}

	consumer, ok := conn.(Consumer)
	if !ok {
		return nil, newErr(Validation, def.Key, "", "connector does not implement Consumer interface")
	}

	if def.SecretRef != "" {
		sec, err := m.secrets.Resolve(ctx, def.SecretRef)
		if err != nil {
			return nil, wrapErr(Validation, def.Key, "", "resolve secret ref", err)
		}
		ctx = WithSecret(ctx, sec)
	}

	// Open the consumer (which may establish connection to broker)
	_, err := consumer.Open(ctx, def)
	if err != nil {
		return nil, wrapErr(classOf(err), def.Key, "", "open consumer", err)
	}

	return consumer, nil
}

// runConsumer is the main loop for a managed consumer. It calls Subscribe and
// handles restarts with exponential backoff on failure.
func (m *ConsumerManager) runConsumer(ctx context.Context, mc *managedConsumer, def ConnectionDef) {
	defer m.wg.Done()
	defer close(mc.done)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		mc.mu.Lock()
		mc.health = healthStarting
		consumer := mc.consumer
		mc.mu.Unlock()

		// Call Subscribe (blocks until error or context cancellation)
		err := consumer.Subscribe(ctx, mc.handler)

		if ctx.Err() != nil {
			// Context was cancelled; graceful shutdown
			return
		}

		// Subscribe returned an error; update health and backoff
		mc.mu.Lock()
		mc.health = healthUnhealthy
		mc.lastErr = err
		backoff := mc.backoff
		// Increase backoff (exponential with cap)
		mc.backoff = mc.backoff * 2
		if mc.backoff > m.config.MaxBackoff {
			mc.backoff = m.config.MaxBackoff
		}
		mc.mu.Unlock()

		if m.log != nil {
			m.log.Emit(ctx, "warn", "consumer_manager: subscribe failed, restarting",
				map[string]any{
					"key":     mc.key,
					"error":   err.Error(),
					"backoff": backoff.String(),
				})
		}

		// Wait for backoff before retrying
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
	}
}

// Stop gracefully shuts down all managed consumers. It signals each consumer to
// stop, waits for in-flight messages to drain (up to DrainTimeout), then closes
// all consumers.
func (m *ConsumerManager) Stop(ctx context.Context) error {
	if !m.running.Load() {
		return nil // Not running
	}

	m.running.Store(false)

	// Create a drain context with timeout
	drainCtx, drainCancel := context.WithTimeout(ctx, m.config.DrainTimeout)
	defer drainCancel()

	// Signal all consumers to stop by calling Unsubscribe
	m.mu.RLock()
	consumers := make([]*managedConsumer, 0, len(m.consumers))
	for _, mc := range m.consumers {
		consumers = append(consumers, mc)
	}
	m.mu.RUnlock()

	// Unsubscribe all consumers concurrently
	var unsubWg sync.WaitGroup
	for _, mc := range consumers {
		unsubWg.Add(1)
		go func(mc *managedConsumer) {
			defer unsubWg.Done()
			if err := mc.consumer.Unsubscribe(drainCtx); err != nil && m.log != nil {
				m.log.Emit(ctx, "warn", "consumer_manager: unsubscribe failed",
					map[string]any{"key": mc.key, "error": err.Error()})
			}
		}(mc)
	}
	unsubWg.Wait()

	// Cancel the manager context to stop all runConsumer goroutines
	if m.cancel != nil {
		m.cancel()
	}

	// Wait for all consumer goroutines to finish
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// All consumers stopped gracefully
	case <-drainCtx.Done():
		if m.log != nil {
			m.log.Emit(ctx, "warn", "consumer_manager: drain timeout exceeded, forcing shutdown", nil)
		}
	}

	// Clear the consumers map
	m.mu.Lock()
	m.consumers = make(map[string]*managedConsumer)
	m.mu.Unlock()

	return nil
}

// Health returns the aggregate health status of all managed consumers. A manager
// is healthy if all consumers are healthy. If any consumer is unhealthy, the
// method returns an error describing which consumers are failing.
func (m *ConsumerManager) Health(ctx context.Context) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var unhealthy []string
	for key, mc := range m.consumers {
		mc.mu.RLock()
		health := mc.health
		mc.mu.RUnlock()

		if health != healthHealthy && health != healthStarting {
			unhealthy = append(unhealthy, key)
		}
	}

	if len(unhealthy) > 0 {
		return newErr(Upstream, "", "", "unhealthy consumers: "+joinKeys(unhealthy))
	}

	return nil
}

// ConsumerHealth holds health information for a single consumer.
type ConsumerHealth struct {
	Key       string
	Healthy   bool
	LastError error
}

// HealthDetails returns detailed health information for each managed consumer.
func (m *ConsumerManager) HealthDetails(ctx context.Context) []ConsumerHealth {
	m.mu.RLock()
	defer m.mu.RUnlock()

	details := make([]ConsumerHealth, 0, len(m.consumers))
	for key, mc := range m.consumers {
		mc.mu.RLock()
		details = append(details, ConsumerHealth{
			Key:       key,
			Healthy:   mc.health == healthHealthy || mc.health == healthStarting,
			LastError: mc.lastErr,
		})
		mc.mu.RUnlock()
	}

	return details
}

// SetHealthy marks a consumer as healthy. Called by the consumer's Subscribe loop
// when it successfully processes a message. This resets the backoff.
func (m *ConsumerManager) SetHealthy(connectionKey string) {
	m.mu.RLock()
	mc, ok := m.consumers[connectionKey]
	m.mu.RUnlock()

	if !ok {
		return
	}

	mc.mu.Lock()
	mc.health = healthHealthy
	mc.backoff = m.config.InitialBackoff // Reset backoff on success
	mc.lastErr = nil
	mc.mu.Unlock()
}

// joinKeys joins a slice of strings with commas.
func joinKeys(keys []string) string {
	if len(keys) == 0 {
		return ""
	}
	result := keys[0]
	for i := 1; i < len(keys); i++ {
		result += ", " + keys[i]
	}
	return result
}
