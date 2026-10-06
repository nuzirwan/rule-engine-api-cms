package connect

import (
	"context"
	"sync"

	"nzr-rules-engine/internal/observ"
)

// registry is the concurrency-safe table of built clients, keyed by connection
// key. It builds one client per ConnectionDef at New via the type->Connector
// factory, resolving each SecretRef, and returns the SAME *resilientClient for a
// key to every caller (pointer identity, AC-5). Reload reconciles the live table
// against a new desired set under the write lock; HealthCheck fans out cheap
// probes for readiness gating.
type registry struct {
	mu      sync.RWMutex
	clients map[string]*resilientClient // key -> live client (one per key, AC-5)
	defs    map[string]ConnectionDef    // key -> the def that built the client
	byType  map[string]Connector        // type -> connector factory
	secrets SecretProvider
	tracer  observ.Tracer
	log     observ.Logger
}

// compile-time assertion that registry satisfies the frozen Registry seam.
var _ Registry = (*registry)(nil)

// SecretProvider returns the registry's secret provider for use by other
// components that need to resolve secret refs (e.g. webhook signature verification).
func (r *registry) SecretProvider() SecretProvider {
	return r.secrets
}

// New builds a registry from the given connectors and connection defs.
// When USE_CONNECTOR_POOL=true, it returns a lazy-pool registry that defers
// connection opening to first use. Otherwise, it returns the eager-load registry
// that opens one pooled client per key at construction time.
// A nil tracer/logger is tolerated (the registry only calls them when non-nil).
func New(connectors []Connector, defs []ConnectionDef, secrets SecretProvider, tracer observ.Tracer, log observ.Logger) (Registry, error) {
	if UseConnectorPool() {
		return newRegistryV2(connectors, defs, secrets, tracer, log)
	}
	return newEagerRegistry(connectors, defs, secrets, tracer, log)
}

// newEagerRegistry builds the original eager-load registry, resolving each def's
// SecretRef via secrets and opening one pooled client per key.
func newEagerRegistry(connectors []Connector, defs []ConnectionDef, secrets SecretProvider, tracer observ.Tracer, log observ.Logger) (*registry, error) {
	if secrets == nil {
		secrets = NewEnvSecretProvider()
	}
	r := &registry{
		clients: make(map[string]*resilientClient, len(defs)),
		defs:    make(map[string]ConnectionDef, len(defs)),
		byType:  make(map[string]Connector, len(connectors)),
		secrets: secrets,
		tracer:  tracer,
		log:     log,
	}
	for _, c := range connectors {
		r.byType[c.Type()] = c
	}
	for _, def := range defs {
		client, err := r.open(context.Background(), def)
		if err != nil {
			// Close whatever opened so far so a failed New leaks no pools.
			r.closeAll()
			return nil, err
		}
		r.clients[def.Key] = client
		r.defs[def.Key] = def
	}
	r.wireDedup()
	return r, nil
}

// wireDedup selects a dedup-capable inner client (a valkey connection that
// satisfies DedupStore) and shares it with every resilient client so an opted-in
// non-idempotent write (Operation.IdempotencyKey) can take an atomic SET-NX lock
// across the process (R4, §6.2). If no dedup-capable connection exists, the key
// still marks an op retryable but no cross-process lock is taken. The caller must
// hold r.mu for write (New runs before publishing r; Reload holds the lock).
func (r *registry) wireDedup() {
	var store DedupStore
	for _, c := range r.clients {
		if ds, ok := c.inner.(DedupStore); ok {
			store = ds
			break
		}
	}
	var guard *dedupGuard
	if store != nil {
		guard = newDedupGuard(store, 0)
	}
	for _, c := range r.clients {
		c.dedup = guard
	}
}

// open resolves the connector + secret for a def and builds the wrapped client.
func (r *registry) open(ctx context.Context, def ConnectionDef) (*resilientClient, error) {
	conn, ok := r.byType[def.Type]
	if !ok {
		return nil, newErr(Validation, def.Key, "", "no connector registered for type "+def.Type)
	}
	if def.SecretRef != "" {
		sec, err := r.secrets.Resolve(ctx, def.SecretRef)
		if err != nil {
			return nil, wrapErr(Validation, def.Key, "", "resolve secret ref", err)
		}
		ctx = WithSecret(ctx, sec)
	}
	inner, err := conn.Open(ctx, def)
	if err != nil {
		return nil, wrapErr(classOf(err), def.Key, "", "open connection", err)
	}
	return newResilientClient(def.Key, inner, def.Resilience), nil
}

// Client returns the live pooled client for a key — a map read on the hot path.
// It returns the same *resilientClient instance for every caller (AC-5).
func (r *registry) Client(ctx context.Context, key string) (Client, error) {
	r.mu.RLock()
	c, ok := r.clients[key]
	r.mu.RUnlock()
	if !ok {
		return nil, newErr(Validation, key, "", "unknown connection key")
	}
	return c, nil
}

// Reload reconciles the live table against defs under the write lock, computing
// an added/removed/changed/unchanged diff keyed by a per-def content signature.
// A changed key opens a replacement, swaps it in, and closes the old client
// after the swap so in-flight ops finish. A failing replacement keeps the old
// client and the error names the failed key; other keys still reconcile.
func (r *registry) Reload(ctx context.Context, defs []ConnectionDef) error {
	desired := make(map[string]ConnectionDef, len(defs))
	for _, d := range defs {
		desired[d.Key] = d
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	var toClose []*resilientClient
	var firstErr error

	// Added or changed keys.
	for key, def := range desired {
		cur, exists := r.defs[key]
		if exists && defSignature(cur) == defSignature(def) {
			continue // unchanged: keep the warm pool
		}
		client, err := r.open(ctx, def)
		if err != nil {
			if firstErr == nil {
				firstErr = err // keep the old client for this key
			}
			continue
		}
		if old, ok := r.clients[key]; ok {
			toClose = append(toClose, old)
		}
		r.clients[key] = client
		r.defs[key] = def
	}

	// Removed keys: anything live but no longer desired.
	for key := range r.defs {
		if _, want := desired[key]; want {
			continue
		}
		if old, ok := r.clients[key]; ok {
			toClose = append(toClose, old)
		}
		delete(r.clients, key)
		delete(r.defs, key)
	}

	// Re-select the dedup store: a reload may have added or removed the valkey
	// connection that backs the idempotency lock.
	r.wireDedup()

	for _, c := range toClose {
		_ = c.Close()
	}
	return firstErr
}

// HealthCheck fans out a cheap probe to every live client in parallel under the
// caller's context and returns the first classified failure (or nil if all
// pass). A probe reuses the client's pool; it opens no side connections.
func (r *registry) HealthCheck(ctx context.Context) error {
	r.mu.RLock()
	clients := make(map[string]*resilientClient, len(r.clients))
	for k, c := range r.clients {
		clients[k] = c
	}
	r.mu.RUnlock()

	errs := make(chan error, len(clients))
	var wg sync.WaitGroup
	for key, c := range clients {
		wg.Add(1)
		go func(key string, c *resilientClient) {
			defer wg.Done()
			_, err := c.Execute(ctx, Operation{Kind: opKindPing})
			if err != nil {
				errs <- wrapErr(classOf(err), key, opKindPing, "health probe failed", err)
				return
			}
			errs <- nil
		}(key, c)
	}
	wg.Wait()
	close(errs)

	var firstErr error
	for err := range errs {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Close drains and closes every live client. cmd/engine calls it on shutdown.
func (r *registry) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closeAllLocked()
}

// closeAll closes every client without assuming the caller holds the lock.
func (r *registry) closeAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	_ = r.closeAllLocked()
}

// closeAllLocked closes and clears every client; the caller must hold the lock.
func (r *registry) closeAllLocked() error {
	var firstErr error
	for key, c := range r.clients {
		if err := c.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		delete(r.clients, key)
		delete(r.defs, key)
	}
	return firstErr
}

// opKindPing is the health-probe operation kind each driver recognizes as a
// cheap liveness check (SELECT 1 for postgres, a HEAD/GET for rest).
const opKindPing = "ping"
