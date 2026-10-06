# TASK-005: Connector Platform (Lazy Pool + Async Providers)

## Summary
Refactor connection handling to be provider-agnostic with lazy initialization, idle reaping, and support for async providers (Kafka, RabbitMQ). This is a large architectural change documented in `docs/lld/connector-platform.md`.

## Context
- Current: all connections opened at startup (eager-load)
- Problem: eager-load exhausts DB `max_connections` at scale
- Design: `docs/lld/connector-platform.md` (Slice G) — full design exists
- Two phases: Phase 1 (lazy pool), Phase 2 (async providers)

## Phase 1: Lazy Pool + Idle Reap

### Requirements

#### 1. Connector Interface
```go
type Connector interface {
    // Open establishes the connection (lazy, called on first use)
    Open(ctx context.Context) error
    // Close releases the connection
    Close(ctx context.Context) error
    // Health checks if connection is alive
    Health(ctx context.Context) error
    // Capabilities returns what this connector supports
    Capabilities() ConnectorCapabilities
}

type ConnectorCapabilities struct {
    SupportsQuery    bool
    SupportsExecute  bool
    SupportsStream   bool
    SupportsBatch    bool
}
```

#### 2. Lazy Initialization
- Connections not opened at startup
- First request to a connector triggers `Open()`
- `Open()` is idempotent (safe to call multiple times)
- Connection state: `uninitialized → opening → ready → closed`

#### 3. Connection Pool
- Pool per connection key
- Configurable `minWarm` — pre-warm N connections at startup for hot paths
- Configurable `maxIdle` — max idle connections to keep
- Configurable `idleTimeout` — close connections idle longer than this

#### 4. Idle Reaper
- Background goroutine
- Periodically checks idle connections
- Closes connections idle > `idleTimeout`
- Keeps at least `minWarm` connections alive

#### 5. Provider Implementations
- `PostgresConnector` — wraps pgxpool
- `RESTConnector` — wraps http.Client
- `ValkeyConnector` — wraps valkey-go client

### Files to Create

#### Engine (`engine/internal/connect/`)
- `connector.go` — Connector interface + ConnectorCapabilities
- `pool.go` — ConnectionPool with lazy init + idle reap
- `postgres_connector.go` — Postgres implementation
- `rest_connector.go` — REST implementation
- `valkey_connector.go` — Valkey implementation
- `registry_v2.go` — new registry using connectors

## Phase 2: Async Providers (Kafka/RabbitMQ)

### Requirements

#### 1. Consumer Interface
```go
type Consumer interface {
    Connector
    // Subscribe starts consuming messages
    Subscribe(ctx context.Context, handler MessageHandler) error
    // Unsubscribe stops consuming
    Unsubscribe(ctx context.Context) error
}

type MessageHandler func(ctx context.Context, msg Message) error
```

#### 2. Message Trigger Node
```json
{
  "type": "messageTrigger",
  "connectionKey": "order-events",
  "topic": "orders.created",
  "flowId": "process-new-order"
}
```

#### 3. Consumer Manager
- Manages long-lived consumer connections
- Graceful shutdown with drain
- Rebalance handling (Kafka)
- Dead-letter routing on handler failure

### Files to Create (Phase 2)
- `internal/connect/consumer.go` — Consumer interface
- `internal/connect/kafka_consumer.go` — Kafka implementation
- `internal/connect/rabbitmq_consumer.go` — RabbitMQ implementation
- `internal/flow/message_trigger.go` — message trigger node handler

## Acceptance Criteria

### Phase 1
- [ ] Connections open lazily on first use
- [ ] Idle connections are reaped after timeout
- [ ] `minWarm` connections pre-opened at startup
- [ ] Health checks work on lazy connections
- [ ] Existing flows work without config changes
- [ ] Metrics: connection_opens, connection_closes, pool_size, idle_count

### Phase 2
- [ ] Kafka consumer can trigger flows
- [ ] RabbitMQ consumer can trigger flows
- [ ] Graceful shutdown drains in-flight messages
- [ ] Dead-letter routing on failure
- [ ] Consumer rebalance doesn't lose messages

## Testing
- Unit tests for pool lifecycle
- Unit tests for idle reaper
- Integration tests with real Postgres (lazy open)
- Integration tests with Kafka (Phase 2)

## Migration Path
1. Implement new Connector interface alongside existing registry
2. Migrate one connector type at a time
3. Feature flag: `USE_CONNECTOR_POOL=true`
4. Remove old registry once all migrated

## Performance Considerations
- Cold-start latency: first request incurs connection open time
- Mitigation: `minWarm` for hot paths
- Trade-off: idle footprint vs cold-start latency
- Metrics to monitor: p99 latency spike on cold connections
