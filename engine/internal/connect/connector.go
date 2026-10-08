package connect

import "context"

// Connector builds a live, pooled driver Client for one source type. One
// Connector exists per type (postgres, rest/http, ...). Adding a new type is a
// new file in drivers/ registered in drivers/registry.go — the Registry, the
// interpreter, and the flow schema are unchanged (AC-8).
type Connector interface {
	// Type returns the connection type this connector serves (e.g. "postgres").
	Type() string
	// Open builds a driver Client from a definition. The resolved secret, when
	// the def carries a SecretRef, is passed via WithSecret on the context by the
	// registry so a connector reads it without the registry importing driver
	// internals.
	Open(ctx context.Context, def ConnectionDef) (Client, error)

	// Lifecycle returns how the connector manages its resources. Introduced for
	// Phase 1 lazy pool support. Default implementations return LifecyclePooled.
	Lifecycle() Lifecycle

	// Capabilities returns the capability flags this connector supports. Used by
	// the pool and registry to determine how to manage the connection.
	Capabilities() Capability
}

// Lifecycle describes how a connector manages its connection resources.
type Lifecycle int

const (
	// LifecyclePooled means the connector manages a connection pool internally
	// (postgres pgxpool, valkey client pool). The pool opens lazily and reaps
	// idle connections. This is the default for Phase 1 connectors.
	LifecyclePooled Lifecycle = iota

	// LifecycleLongLived is for consumers (Kafka, RabbitMQ) that maintain a
	// persistent connection with server-push semantics. Phase 2.
	LifecycleLongLived

	// LifecycleEphemeral is for stateless connectors (REST/HTTP) that open a
	// connection per request. No pooling at the connector level.
	LifecycleEphemeral
)

// String returns a human-readable name for the lifecycle.
func (l Lifecycle) String() string {
	switch l {
	case LifecyclePooled:
		return "pooled"
	case LifecycleLongLived:
		return "long-lived"
	case LifecycleEphemeral:
		return "ephemeral"
	default:
		return "unknown"
	}
}

// Capability is a bitmask of connector capabilities. A connector declares what
// operations it supports so the pool and registry can make routing decisions.
type Capability uint32

const (
	// CapQueryExec indicates the connector supports query/exec operations
	// (SQL databases, REST endpoints that return data).
	CapQueryExec Capability = 1 << iota

	// CapKeyValue indicates the connector supports get/set/del key-value ops
	// (valkey, redis, memcached).
	CapKeyValue

	// CapDedupStore indicates the connector can serve as a DedupStore for
	// idempotency-key locking (valkey SET NX).
	CapDedupStore

	// CapSubscribe indicates the connector supports message subscription
	// (Kafka consumer, RabbitMQ). Phase 2.
	CapSubscribe

	// CapPublish indicates the connector supports message publishing
	// (Kafka producer, RabbitMQ). Phase 2.
	CapPublish
)

// Has returns true if c includes all capabilities in mask.
func (c Capability) Has(mask Capability) bool {
	return c&mask == mask
}

// String returns a human-readable list of capabilities.
func (c Capability) String() string {
	if c == 0 {
		return "none"
	}
	var caps []string
	if c.Has(CapQueryExec) {
		caps = append(caps, "query-exec")
	}
	if c.Has(CapKeyValue) {
		caps = append(caps, "key-value")
	}
	if c.Has(CapDedupStore) {
		caps = append(caps, "dedup-store")
	}
	if c.Has(CapSubscribe) {
		caps = append(caps, "subscribe")
	}
	if c.Has(CapPublish) {
		caps = append(caps, "publish")
	}
	if len(caps) == 0 {
		return "unknown"
	}
	result := caps[0]
	for i := 1; i < len(caps); i++ {
		result += "|" + caps[i]
	}
	return result
}

// secretCtxKey carries a resolved Secret from the registry into a Connector.Open
// without widening the Connector seam. It is unexported so only this package can
// place or read it.
type secretCtxKey struct{}

// WithSecret returns a context carrying the resolved secret for a pending Open.
func WithSecret(ctx context.Context, s Secret) context.Context {
	return context.WithValue(ctx, secretCtxKey{}, s)
}

// SecretFrom returns the resolved secret placed on ctx by the registry, if any.
// A driver Open calls this to obtain the credential bytes via Secret.Reveal.
func SecretFrom(ctx context.Context) (Secret, bool) {
	s, ok := ctx.Value(secretCtxKey{}).(Secret)
	return s, ok
}

// secretsCtxKey carries multiple resolved secrets (keyed by role, e.g. "password",
// "apiKey") from the registry into a Connector.Open. It is unexported so only
// this package can place or read it.
type secretsCtxKey struct{}

// WithSecrets returns a context carrying multiple resolved secrets for a pending
// Open. The map keys are the role names (e.g. "password", "apiKey").
func WithSecrets(ctx context.Context, secrets map[string]Secret) context.Context {
	return context.WithValue(ctx, secretsCtxKey{}, secrets)
}

// SecretsFrom returns the resolved secrets map placed on ctx by the registry, if
// any. A driver Open calls this to obtain multiple credentials by role name.
func SecretsFrom(ctx context.Context) (map[string]Secret, bool) {
	s, ok := ctx.Value(secretsCtxKey{}).(map[string]Secret)
	return s, ok
}
