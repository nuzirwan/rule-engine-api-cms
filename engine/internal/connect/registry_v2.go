package connect

import (
	"context"
	"os"
	"sync"

	"nzr-rules-engine/internal/observ"
)

// registryV2 is the lazy-pool implementation of Registry. It defers connection
// opening to first use (AC-G2) and reaps idle connections via the underlying
// ConnectionPool (AC-G3). It coexists with the eager-load registry and is
// selected by the USE_CONNECTOR_POOL=true feature flag.
type registryV2 struct {
	pool    *ConnectionPool
	mu      sync.RWMutex
	byType  map[string]Connector
	secrets SecretProvider
	tracer  observ.Tracer
	log     observ.Logger
}

// compile-time assertion that registryV2 satisfies the frozen Registry seam.
var _ Registry = (*registryV2)(nil)

// compile-time assertion that registryV2 satisfies ConnectorRegistry.
var _ ConnectorRegistry = (*registryV2)(nil)

// newRegistryV2 builds a lazy-pool registry. It opens NO connections at
// construction time — they open on first Client() call (AC-G2).
func newRegistryV2(connectors []Connector, defs []ConnectionDef, secrets SecretProvider, tracer observ.Tracer, log observ.Logger) (*registryV2, error) {
	if secrets == nil {
		secrets = NewEnvSecretProvider()
	}

	byType := make(map[string]Connector, len(connectors))
	for _, c := range connectors {
		byType[c.Type()] = c
	}

	config := DefaultPoolConfig()
	pool := NewConnectionPool(connectors, defs, secrets, config, log)
	pool.Start(context.Background())

	// Wire dedup if a valkey connection is configured
	r := &registryV2{
		pool:    pool,
		byType:  byType,
		secrets: secrets,
		tracer:  tracer,
		log:     log,
	}

	return r, nil
}

// Client returns the live pooled client for a key, opening lazily on first use.
// It returns the same *resilientClient instance for every caller (AC-5).
func (r *registryV2) Client(ctx context.Context, key string) (Client, error) {
	client, err := r.pool.Get(ctx, key)
	if err != nil {
		return nil, err
	}

	// Wire dedup on first successful client retrieval
	// This is deferred because we need a live client to check for DedupStore
	r.wireDedup(client)

	return client, nil
}

// wireDedup selects a dedup-capable inner client (a valkey connection that
// satisfies DedupStore) and shares it with every resilient client.
func (r *registryV2) wireDedup(client Client) {
	rc, ok := client.(*resilientClient)
	if !ok {
		return
	}
	// If already wired, skip
	if rc.dedup != nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// Check if this client's inner is a DedupStore
	if ds, ok := rc.inner.(DedupStore); ok {
		rc.dedup = newDedupGuard(ds, 0)
	}
}

// Reload reconciles the live clients against a new set of definitions.
// Changed keys are evicted; unchanged keys keep their warm pool.
func (r *registryV2) Reload(ctx context.Context, defs []ConnectionDef) error {
	r.pool.UpdateDefs(defs)
	return nil
}

// HealthCheck fans out cheap probes to every live client. Unlike the eager
// registry, this only probes connections that have been opened (lazy).
func (r *registryV2) HealthCheck(ctx context.Context) error {
	// For the lazy pool, we probe connections that are currently open.
	// We don't want to open connections just for health checks.
	stats := r.pool.Stats()
	if stats.OpenConnections == 0 {
		// No connections open yet — nothing to probe. This is healthy for a
		// lazy pool (no traffic has arrived).
		return nil
	}

	// Probe each def that might have an open connection
	r.pool.mu.RLock()
	entries := make(map[string]*poolEntry, len(r.pool.entries))
	for k, e := range r.pool.entries {
		entries[k] = e
	}
	r.pool.mu.RUnlock()

	var firstErr error
	var mu sync.Mutex
	var wg sync.WaitGroup

	for key, entry := range entries {
		entry.mu.Lock()
		client := entry.client
		closed := entry.closed
		entry.mu.Unlock()

		if client == nil || closed {
			continue
		}

		wg.Add(1)
		go func(key string, c Client) {
			defer wg.Done()
			_, err := c.Execute(ctx, Operation{Kind: opKindPing})
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = wrapErr(classOf(err), key, opKindPing, "health probe failed", err)
				}
				mu.Unlock()
			}
		}(key, client)
	}
	wg.Wait()

	return firstErr
}

// Close stops the pool's reaper and closes all connections.
func (r *registryV2) Close() error {
	return r.pool.Close()
}

// SecretProvider returns the registry's secret provider for use by other
// components that need to resolve secret refs (e.g. webhook signature verification).
func (r *registryV2) SecretProvider() SecretProvider {
	return r.secrets
}

// UseConnectorPool returns true if the USE_CONNECTOR_POOL env var is set to "true".
func UseConnectorPool() bool {
	return os.Getenv("USE_CONNECTOR_POOL") == "true"
}

// Connector returns the connector factory for the given connection type.
// This is used by the test-connection endpoint to open an ephemeral client
// without going through the defs table.
func (r *registryV2) Connector(typ string) (Connector, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.byType[typ]
	return c, ok
}

// AllConnectors returns a copy of the registered connectors keyed by type.
// Used by admin endpoints to enumerate all connector types.
func (r *registryV2) AllConnectors() map[string]Connector {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]Connector, len(r.byType))
	for k, v := range r.byType {
		out[k] = v
	}
	return out
}
