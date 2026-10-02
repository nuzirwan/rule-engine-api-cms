package decision

import (
	"context"
	"sync"
)

// Compiled is our OWN adapter interface wrapping a zen-go compiled decision. The
// public decision.Evaluator seam is defined in terms of maps; this internal
// handle lets the cgo/!cgo split and the cache stay driver-agnostic. Eval honors
// ctx AROUND the call (zen-go cannot cancel mid-eval); Close frees the Rust-side
// graph.
type Compiled interface {
	Eval(ctx context.Context, input map[string]any) (map[string]any, error)
	Close()
}

// compiledKey identifies a compiled graph by (id, version). Immutable versions
// mean an entry never goes stale within its key: a publish creates a NEW version
// (a new key), so there is no update-in-place and no read-your-write race.
type compiledKey struct {
	ID      string
	Version int
}

// compileFunc parses JDM bytes into a reusable Compiled handle. The Engine
// injects the real cgo-backed compiler; tests inject a counting fake.
type compileFunc func(jdm []byte) (Compiled, error)

// compiledCache caches compiled graphs keyed by (id,version). getOrCompile uses
// double-checked locking so exactly one compile happens per key even under
// concurrent first use. Close frees every cached graph on shutdown.
type compiledCache struct {
	mu      sync.RWMutex
	items   map[compiledKey]Compiled
	compile compileFunc
}

// newCompiledCache builds a cache that compiles via compile.
func newCompiledCache(compile compileFunc) *compiledCache {
	return &compiledCache{
		items:   make(map[compiledKey]Compiled),
		compile: compile,
	}
}

// getOrCompile returns the cached graph for (id,version), compiling jdm once on
// a miss. The fast path takes only an RLock; on a miss it upgrades to a write
// lock and re-checks so a concurrent compile does not race (one compile per key).
func (c *compiledCache) getOrCompile(id string, version int, jdm []byte) (Compiled, error) {
	key := compiledKey{ID: id, Version: version}

	c.mu.RLock()
	if cur, ok := c.items[key]; ok {
		c.mu.RUnlock()
		return cur, nil
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	if cur, ok := c.items[key]; ok { // re-check under the write lock
		return cur, nil
	}
	compiled, err := c.compile(jdm)
	if err != nil {
		return nil, err
	}
	c.items[key] = compiled
	return compiled, nil
}

// Close frees every cached graph and empties the cache. Safe to call once at
// shutdown; subsequent getOrCompile calls recompile lazily.
func (c *compiledCache) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, compiled := range c.items {
		compiled.Close()
		delete(c.items, key)
	}
}
