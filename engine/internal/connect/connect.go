// Package connect is the connection registry seam (Slice B, lld-contracts.md):
// the flow interpreter reaches every external source through these interfaces,
// never through a concrete driver (dependency inversion).
//
// This file declares only the SUBSET of the frozen contract that the pure flow
// core (Slice A) needs to compile and be unit-tested against fakes: the Registry
// and Client interfaces and the Operation/ResiliencePolicy value types that an
// action node builds. The pooled registry, resilientClient, the postgres/rest
// connectors, policy merge, classified errors, Connector/ConnectionDef and the
// SecretProvider/Secret types are added by Slice B (FEAT-002) in separate files
// in this same package — they must NOT redeclare the symbols defined here.
package connect

import (
	"context"
	"time"
)

// Registry hands out live, pooled clients by connection key and supports
// hot-reload and health probing. One client is built per connection key and
// reused (pointer identity) across requests.
type Registry interface {
	// Client returns the live pooled client for a connection key (current version).
	Client(ctx context.Context, key string) (Client, error)
	// Reload reconciles the live clients against a new set of definitions.
	Reload(ctx context.Context, defs []ConnectionDef) error
	// HealthCheck fans out cheap probes for readiness gating.
	HealthCheck(ctx context.Context) error
	// Close drains and closes all connections. cmd/engine calls it on shutdown.
	Close() error
	// SecretProvider returns the registry's secret provider for resolving secret refs.
	SecretProvider() SecretProvider
}

// ConnectorLookup is the optional interface a registry may satisfy to expose
// connector factories by type. Used by the test-connection endpoint to open an
// ephemeral client without going through the defs table.
type ConnectorLookup interface {
	Connector(typ string) (Connector, bool)
}

// Client is a single pooled connection to one source. Execute honors the ctx
// deadline and the connection's resilience policy.
type Client interface {
	Execute(ctx context.Context, op Operation) (any, error)
	Close() error
}

// Operation is the per-node operation payload handed to a Client.
type Operation struct {
	Kind    string         // "query","exec","get","set","del","http"
	Payload map[string]any // query+params | key | method+path+body ...

	// Override is a per-node resilience override; nil => connection default (AC-7).
	Override *ResiliencePolicy
	// IdempotencyKey, when non-empty, makes a non-idempotent write safe to retry (R4).
	IdempotencyKey string
	// Required true => abort on failure; false => best-effort (R3).
	Required bool
	// UnwrapSingleRow controls result normalization: when true (the default via nil
	// for backward compat), a single-row query result is unwrapped to a map; when
	// explicitly false, results are always returned as an array.
	UnwrapSingleRow *bool
}

// ResiliencePolicy carries timeout/retry/breaker settings. Fields are fixed here
// so a field-level merge can tell "unset/zero" from an explicit override; a zero
// field means "inherit". The thin slice honors Timeout only.
type ResiliencePolicy struct {
	Timeout time.Duration
	Retry   struct {
		MaxAttempts int
		BaseBackoff time.Duration
		MaxBackoff  time.Duration
	}
	Breaker struct {
		FailureThreshold uint32
		FailureRatio     float64
		OpenTimeout      time.Duration
	}
}

// ConnectionDef describes one connection for Registry.Reload. The full field set
// (Settings, SecretRef, Resilience default) is owned by Slice B; the flow core
// only names the type through the Registry seam.
type ConnectionDef struct {
	Key, Type  string
	Settings   map[string]any
	SecretRef  string
	Resilience ResiliencePolicy
}
