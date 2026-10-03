package config

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand"
	"sync"
	"time"

	"github.com/valkey-io/valkey-go"
)

// ValkeyCache is the Valkey-backed config.Cache (AC-16, R5), driven by the
// valkey-go RESP client (no ORM — consistent with the connect slice's client
// choice).
//
// Design (Slice D §4, grounded in [[caching-strategy]]):
//   - Every Set uses a bounded TTL with jitter so entries never live forever and
//     a batch of keys does not expire together (thundering-herd guard).
//   - Keys are namespaced + versioned (keys.go). The cache is never the source of
//     truth; on any Get/Set transport error the caller degrades to the store, the
//     error is logged at warn, and serving continues.
//   - Invalidate deletes the matching keys locally AND publishes a change event on
//     cfg:v1:invalidate; every instance subscribes at startup and drops its
//     matching keys on receipt, closing the one-writer/many-readers gap (R5)
//     within one pub/sub round-trip. TTL is the backstop if a message is missed.
//
// === Cross-instance consistency boundary (Slice D §4, review #5) ===
// Activation (the active_pointers row) is strongly consistent IN the store (a
// single-row update inside a transaction). Its propagation to the fleet is
// EVENTUALLY consistent, bounded by the invalidate round-trip and, as a hard
// upper bound if a message is missed, the TTL. Between "publish commits +
// invalidate published" and "every instance has dropped its key and re-read",
// instance X may serve version N while instance Y serves N+1. Each instance is
// internally consistent (one coherent version per request, pinned — AC-11); the
// fleet is not instantaneously uniform. This is an accepted, documented v1
// property, not a correctness bug: flows are pinned per request so no request
// tears, and there is deliberately NO cross-instance activation lock (R10 — a
// global lock on the hot path trades availability + latency for a uniformity the
// system does not need). Callers needing "my publish is globally live" must treat
// publish as asynchronous (poll/confirm), not synchronous.
type ValkeyCache struct {
	client   valkey.Client
	ttl      time.Duration
	jitter   time.Duration
	chanName string
	log      warnLogger
	rng      *rand.Rand
	rngMu    sync.Mutex

	subOnce   sync.Once
	cancelSub context.CancelFunc
	subDone   chan struct{}
}

// warnLogger is the cache's tiny logging seam: cache-down conditions log at warn
// and serving continues. A nil logger is valid (no-op).
type warnLogger interface {
	Warn(msg string, kv ...any)
}

// nopLogger discards warnings; the zero value is a usable logger.
type nopLogger struct{}

func (nopLogger) Warn(string, ...any) {}

// invalidateMsg is the pub/sub payload: which pattern changed (and optionally
// which object, for observability).
type invalidateMsg struct {
	Env        string `json:"env,omitempty"`
	ObjectType string `json:"objectType,omitempty"`
	ObjectID   string `json:"objectId,omitempty"`
	Pattern    string `json:"pattern"`
}

// CacheOption configures a ValkeyCache.
type CacheOption func(*ValkeyCache)

// WithTTL sets the base TTL for cached entries (default 60s).
func WithTTL(d time.Duration) CacheOption { return func(c *ValkeyCache) { c.ttl = d } }

// WithJitter sets the maximum extra random TTL added per entry (default 10s).
func WithJitter(d time.Duration) CacheOption { return func(c *ValkeyCache) { c.jitter = d } }

// WithWarnLogger wires a logger for cache-down warnings.
func WithWarnLogger(l warnLogger) CacheOption { return func(c *ValkeyCache) { c.log = l } }

// NewValkeyCache builds a ValkeyCache over a live valkey-go client. The caller
// owns the client's lifecycle; Close here stops the subscription goroutine but
// does not close the client.
func NewValkeyCache(client valkey.Client, opts ...CacheOption) *ValkeyCache {
	c := &ValkeyCache{
		client:   client,
		ttl:      60 * time.Second,
		jitter:   10 * time.Second,
		chanName: invalidateChannel,
		log:      nopLogger{},
		rng:      rand.New(rand.NewSource(time.Now().UnixNano())),
		subDone:  make(chan struct{}),
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// compile-time assertion that *ValkeyCache satisfies the Cache seam.
var _ Cache = (*ValkeyCache)(nil)

// Get fetches a cached value. A miss returns (nil,false,nil). A transport error
// returns (nil,false,err) so the caller degrades to the store; it is NOT reported
// as a cache miss (which would mask an outage as a cold cache).
func (c *ValkeyCache) Get(ctx context.Context, key string) ([]byte, bool, error) {
	b, err := c.client.Do(ctx, c.client.B().Get().Key(key).Build()).AsBytes()
	if err != nil {
		if valkey.IsValkeyNil(err) {
			return nil, false, nil
		}
		c.log.Warn("config cache get failed; degrading to store", "key", key, "err", err)
		return nil, false, c.classify("cache get", err)
	}
	return b, true, nil
}

// Set stores v under key with a jittered TTL. A transport error is logged and
// swallowed (returning nil): a failed cache write must never fail a request —
// the next read simply misses and re-populates ([[caching-strategy]]).
func (c *ValkeyCache) Set(ctx context.Context, key string, v []byte, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = c.jitteredTTL()
	}
	cmd := c.client.B().Set().Key(key).Value(valkey.BinaryString(v)).PxMilliseconds(ttl.Milliseconds()).Build()
	if err := c.client.Do(ctx, cmd).Error(); err != nil {
		c.log.Warn("config cache set failed; continuing", "key", key, "err", err)
		return nil
	}
	return nil
}

// Invalidate deletes every key matching keyPattern locally and publishes the
// change on the invalidation channel so every instance drops its matching keys
// (R5, AC-16). A failure to delete or publish is logged and swallowed: the TTL
// backstop still bounds staleness, so invalidation is best-effort over the
// strongly-consistent store write that preceded it.
func (c *ValkeyCache) Invalidate(ctx context.Context, keyPattern string) error {
	c.deleteMatching(ctx, keyPattern)

	msg, err := json.Marshal(invalidateMsg{Pattern: keyPattern})
	if err != nil {
		c.log.Warn("config cache invalidate marshal failed", "pattern", keyPattern, "err", err)
		return nil
	}
	pub := c.client.B().Publish().Channel(c.chanName).Message(string(msg)).Build()
	if err := c.client.Do(ctx, pub).Error(); err != nil {
		c.log.Warn("config cache invalidate publish failed; relying on TTL", "pattern", keyPattern, "err", err)
	}
	return nil
}

// deleteMatching removes every key matching pattern. A plain key (no glob) is
// deleted directly; a glob is expanded via KEYS (bounded to the cfg namespace,
// run off the hot path on publish/rollback only).
func (c *ValkeyCache) deleteMatching(ctx context.Context, pattern string) {
	if !hasGlob(pattern) {
		if err := c.client.Do(ctx, c.client.B().Del().Key(pattern).Build()).Error(); err != nil {
			c.log.Warn("config cache del failed", "key", pattern, "err", err)
		}
		return
	}
	keys, err := c.client.Do(ctx, c.client.B().Keys().Pattern(pattern).Build()).AsStrSlice()
	if err != nil {
		c.log.Warn("config cache keys scan failed", "pattern", pattern, "err", err)
		return
	}
	if len(keys) == 0 {
		return
	}
	if err := c.client.Do(ctx, c.client.B().Del().Key(keys...).Build()).Error(); err != nil {
		c.log.Warn("config cache del failed", "pattern", pattern, "err", err)
	}
}

// StartInvalidationConsumer subscribes to the invalidation channel and drops this
// instance's matching keys on every received message. It runs in a background
// goroutine until ctx is cancelled or Close is called. Every engine instance
// calls this at startup so a publish on any instance fans out to the whole fleet
// (R5, AC-16). Idempotent: only the first call starts a subscriber.
func (c *ValkeyCache) StartInvalidationConsumer(ctx context.Context) {
	c.subOnce.Do(func() {
		subCtx, cancel := context.WithCancel(ctx)
		c.cancelSub = cancel
		go func() {
			defer close(c.subDone)
			// Dedicated client for the blocking SUBSCRIBE so Do-path commands keep
			// a free connection. valkey-go's Receive blocks until subCtx is done.
			err := c.client.Receive(subCtx, c.client.B().Subscribe().Channel(c.chanName).Build(),
				func(msg valkey.PubSubMessage) {
					var im invalidateMsg
					if derr := json.Unmarshal([]byte(msg.Message), &im); derr != nil {
						c.log.Warn("config cache invalidate decode failed", "err", derr)
						return
					}
					if im.Pattern == "" {
						return
					}
					c.deleteMatching(subCtx, im.Pattern)
				})
			if err != nil && !errors.Is(err, context.Canceled) {
				c.log.Warn("config cache invalidation subscriber stopped", "err", err)
			}
		}()
	})
}

// Ping reports cache reachability (for a degraded-readiness signal).
func (c *ValkeyCache) Ping(ctx context.Context) error {
	if err := c.client.Do(ctx, c.client.B().Ping().Build()).Error(); err != nil {
		return c.classify("cache ping", err)
	}
	return nil
}

// Close stops the subscription goroutine (if started). The underlying client is
// owned by the caller and is NOT closed here.
func (c *ValkeyCache) Close() error {
	if c.cancelSub != nil {
		c.cancelSub()
		<-c.subDone
	}
	return nil
}

// jitteredTTL returns the base TTL plus a random [0,jitter) so a batch of entries
// written together do not all expire at the same instant.
func (c *ValkeyCache) jitteredTTL() time.Duration {
	if c.jitter <= 0 {
		return c.ttl
	}
	c.rngMu.Lock()
	extra := time.Duration(c.rng.Int63n(int64(c.jitter)))
	c.rngMu.Unlock()
	return c.ttl + extra
}

// classify maps a valkey-go error to the config taxonomy: a ctx deadline is
// Timeout, everything else is Upstream (cache unreachable / protocol error).
func (c *ValkeyCache) classify(op string, err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return wrapErr(Timeout, op, err)
	}
	return wrapErr(Upstream, op, err)
}

// hasGlob reports whether pattern contains a Valkey glob metacharacter.
func hasGlob(pattern string) bool {
	for _, r := range pattern {
		switch r {
		case '*', '?', '[', ']':
			return true
		}
	}
	return false
}
