package connect

import (
	"context"
	"sync"
	"time"

	"nzr-rules-engine/internal/observ"
)

// PoolConfig holds configuration for the lazy connection pool.
type PoolConfig struct {
	// IdleTimeout is how long a connection can be idle before the reaper closes it.
	// Default: 5 minutes.
	IdleTimeout time.Duration

	// MinWarm is the minimum number of connections to pre-warm at Start.
	// These connections are opened eagerly and never reaped below this count.
	// Default: 0 (fully lazy).
	MinWarm int

	// MaxIdle is the maximum number of idle connections to keep. Connections
	// beyond this are closed immediately after use. Default: 10.
	MaxIdle int

	// ReapInterval is how often the reaper runs. Default: 30 seconds.
	ReapInterval time.Duration
}

// DefaultPoolConfig returns the default pool configuration.
func DefaultPoolConfig() PoolConfig {
	return PoolConfig{
		IdleTimeout:  5 * time.Minute,
		MinWarm:      0,
		MaxIdle:      10,
		ReapInterval: 30 * time.Second,
	}
}

// poolEntry holds a single pooled client and its metadata.
type poolEntry struct {
	mu       sync.Mutex
	client   Client
	lastUse  time.Time
	opening  bool       // true while Open is in progress (singleflight)
	openCond *sync.Cond // signals when opening completes
	closed   bool
}

// ConnectionPool manages lazy-initialized connections with idle reaping.
// It implements singleflight semantics: concurrent first-use opens for the same
// key result in exactly one Open call.
type ConnectionPool struct {
	mu      sync.RWMutex
	entries map[string]*poolEntry
	defs    map[string]ConnectionDef
	byType  map[string]Connector
	secrets SecretProvider
	config  PoolConfig
	log     observ.Logger

	// reaper control
	stopReaper chan struct{}
	reaperDone chan struct{}

	// nowFunc is injectable for testing; protected by nowMu
	nowMu   sync.RWMutex
	nowFunc func() time.Time
}

// NewConnectionPool creates a new lazy connection pool. It does NOT open any
// connections — they are opened on first use (AC-G2).
func NewConnectionPool(
	connectors []Connector,
	defs []ConnectionDef,
	secrets SecretProvider,
	config PoolConfig,
	log observ.Logger,
) *ConnectionPool {
	if secrets == nil {
		secrets = NewEnvSecretProvider()
	}
	if config.IdleTimeout == 0 {
		config.IdleTimeout = DefaultPoolConfig().IdleTimeout
	}
	if config.ReapInterval == 0 {
		config.ReapInterval = DefaultPoolConfig().ReapInterval
	}
	if config.MaxIdle == 0 {
		config.MaxIdle = DefaultPoolConfig().MaxIdle
	}

	byType := make(map[string]Connector, len(connectors))
	for _, c := range connectors {
		byType[c.Type()] = c
	}

	defMap := make(map[string]ConnectionDef, len(defs))
	for _, d := range defs {
		defMap[d.Key] = d
	}

	return &ConnectionPool{
		entries:    make(map[string]*poolEntry),
		defs:       defMap,
		byType:     byType,
		secrets:    secrets,
		config:     config,
		log:        log,
		stopReaper: make(chan struct{}),
		reaperDone: make(chan struct{}),
		nowFunc:    time.Now,
	}
}

// Start begins the idle reaper goroutine and optionally pre-warms connections
// for defs that specify minWarm > 0 (AC-G4). Errors from pre-warming are logged
// but do NOT fail Start (AC-G5: graceful startup).
func (p *ConnectionPool) Start(ctx context.Context) {
	// Pre-warm connections that request it
	p.mu.RLock()
	defs := make([]ConnectionDef, 0, len(p.defs))
	for _, d := range p.defs {
		defs = append(defs, d)
	}
	p.mu.RUnlock()

	for _, def := range defs {
		minWarm := extractMinWarm(def.Settings)
		if minWarm > 0 {
			// Pre-warm by calling Get once; the client is kept warm
			_, err := p.Get(ctx, def.Key)
			if err != nil && p.log != nil {
				p.log.Emit(ctx, "warn", "pool: failed to pre-warm connection",
					map[string]any{"key": def.Key, "error": err.Error()})
			}
		}
	}

	go p.runReaper()
}

// Get returns a client for the given key, opening it lazily on first use.
// Concurrent calls for the same key result in exactly one Open (singleflight).
func (p *ConnectionPool) Get(ctx context.Context, key string) (Client, error) {
	p.mu.RLock()
	def, ok := p.defs[key]
	if !ok {
		p.mu.RUnlock()
		return nil, newErr(Validation, key, "", "unknown connection key")
	}
	entry, exists := p.entries[key]
	p.mu.RUnlock()

	if !exists {
		// First access for this key: create entry under write lock
		p.mu.Lock()
		entry, exists = p.entries[key]
		if !exists {
			entry = &poolEntry{}
			entry.openCond = sync.NewCond(&entry.mu)
			p.entries[key] = entry
		}
		p.mu.Unlock()
	}

	// Now we have an entry; acquire it
	entry.mu.Lock()
	defer entry.mu.Unlock()

	// Wait if another goroutine is opening
	for entry.opening {
		entry.openCond.Wait()
	}

	// Check if client is ready
	if entry.client != nil && !entry.closed {
		entry.lastUse = p.now()
		return entry.client, nil
	}

	// Need to open: mark as opening (singleflight)
	entry.opening = true
	entry.closed = false
	entry.mu.Unlock()

	client, err := p.open(ctx, def)

	entry.mu.Lock()
	entry.opening = false
	if err != nil {
		entry.openCond.Broadcast()
		return nil, err
	}
	entry.client = client
	entry.lastUse = p.now()
	entry.openCond.Broadcast()

	return client, nil
}

// open creates a new client for the given def.
func (p *ConnectionPool) open(ctx context.Context, def ConnectionDef) (Client, error) {
	conn, ok := p.byType[def.Type]
	if !ok {
		return nil, newErr(Validation, def.Key, "", "no connector registered for type "+def.Type)
	}
	if def.SecretRef != "" {
		sec, err := p.secrets.Resolve(ctx, def.SecretRef)
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

// UpdateDefs updates the pool's connection definitions. Changed keys are evicted
// so the next Get reopens with the new config. Unchanged keys keep their warm client.
func (p *ConnectionPool) UpdateDefs(defs []ConnectionDef) {
	desired := make(map[string]ConnectionDef, len(defs))
	for _, d := range defs {
		desired[d.Key] = d
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// Identify changed or removed keys
	var toClose []Client
	for key, oldDef := range p.defs {
		newDef, exists := desired[key]
		if !exists {
			// Removed: evict
			if entry, ok := p.entries[key]; ok {
				entry.mu.Lock()
				if entry.client != nil {
					toClose = append(toClose, entry.client)
					entry.client = nil
					entry.closed = true
				}
				entry.mu.Unlock()
				delete(p.entries, key)
			}
			delete(p.defs, key)
		} else if defSignature(oldDef) != defSignature(newDef) {
			// Changed: evict and update def
			if entry, ok := p.entries[key]; ok {
				entry.mu.Lock()
				if entry.client != nil {
					toClose = append(toClose, entry.client)
					entry.client = nil
					entry.closed = true
				}
				entry.mu.Unlock()
			}
			p.defs[key] = newDef
		}
	}

	// Add new keys
	for key, def := range desired {
		if _, exists := p.defs[key]; !exists {
			p.defs[key] = def
		}
	}

	// Close evicted clients outside the lock
	go func() {
		for _, c := range toClose {
			_ = c.Close()
		}
	}()
}

// runReaper periodically closes idle connections.
func (p *ConnectionPool) runReaper() {
	ticker := time.NewTicker(p.config.ReapInterval)
	defer ticker.Stop()
	defer close(p.reaperDone)

	for {
		select {
		case <-p.stopReaper:
			return
		case <-ticker.C:
			p.reapIdle()
		}
	}
}

// reapIdle closes connections that have been idle beyond IdleTimeout.
func (p *ConnectionPool) reapIdle() {
	now := p.now()
	cutoff := now.Add(-p.config.IdleTimeout)

	p.mu.RLock()
	keys := make([]string, 0, len(p.entries))
	for k := range p.entries {
		keys = append(keys, k)
	}
	p.mu.RUnlock()

	var toClose []Client
	for _, key := range keys {
		p.mu.RLock()
		entry, ok := p.entries[key]
		def := p.defs[key]
		p.mu.RUnlock()
		if !ok {
			continue
		}

		entry.mu.Lock()
		if entry.client != nil && !entry.opening && entry.lastUse.Before(cutoff) {
			// Check minWarm: don't reap below the minimum
			minWarm := extractMinWarm(def.Settings)
			if minWarm == 0 {
				toClose = append(toClose, entry.client)
				entry.client = nil
				entry.closed = true
			}
		}
		entry.mu.Unlock()
	}

	for _, c := range toClose {
		_ = c.Close()
	}
}

// Close stops the reaper and closes all pooled connections.
func (p *ConnectionPool) Close() error {
	close(p.stopReaper)
	<-p.reaperDone

	p.mu.Lock()
	defer p.mu.Unlock()

	var firstErr error
	for key, entry := range p.entries {
		entry.mu.Lock()
		if entry.client != nil {
			if err := entry.client.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
			entry.client = nil
			entry.closed = true
		}
		entry.mu.Unlock()
		delete(p.entries, key)
	}
	return firstErr
}

// Stats returns pool statistics for observability.
func (p *ConnectionPool) Stats() PoolStats {
	p.mu.RLock()
	defer p.mu.RUnlock()

	stats := PoolStats{
		TotalDefs: len(p.defs),
	}
	for _, entry := range p.entries {
		entry.mu.Lock()
		if entry.client != nil && !entry.closed {
			stats.OpenConnections++
		}
		entry.mu.Unlock()
	}
	return stats
}

// now returns the current time, using the injectable nowFunc for testing.
func (p *ConnectionPool) now() time.Time {
	p.nowMu.RLock()
	fn := p.nowFunc
	p.nowMu.RUnlock()
	return fn()
}

// setNowFunc sets the time function for testing. Must be called with care to
// avoid races with the reaper goroutine.
func (p *ConnectionPool) setNowFunc(fn func() time.Time) {
	p.nowMu.Lock()
	p.nowFunc = fn
	p.nowMu.Unlock()
}

// PoolStats holds pool statistics.
type PoolStats struct {
	TotalDefs       int
	OpenConnections int
}

// extractMinWarm extracts the minWarm setting from a connection def's pool config.
func extractMinWarm(settings map[string]any) int {
	pool, ok := settings["pool"].(map[string]any)
	if !ok {
		return 0
	}
	if v, ok := pool["minWarm"].(int); ok {
		return v
	}
	if v, ok := pool["minWarm"].(float64); ok {
		return int(v)
	}
	return 0
}

// extractIdleTimeout extracts the idleTimeout setting from a connection def's pool config.
func extractIdleTimeout(settings map[string]any) time.Duration {
	pool, ok := settings["pool"].(map[string]any)
	if !ok {
		return 0
	}
	if v, ok := pool["idleTimeout"].(string); ok {
		d, err := time.ParseDuration(v)
		if err == nil {
			return d
		}
	}
	return 0
}
