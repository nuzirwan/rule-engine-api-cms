package connect

import (
	"context"
	"time"
)

// DedupStore is the atomic lock primitive behind the idempotency-key mechanism
// for non-idempotent writes (R4, §6.2). It is a thin seam over a "SET key value
// NX PX ttl" + "DEL key" pair so the guard logic is testable without real I/O
// and so any store (valkey in v1) can back it. A driver that can provide an
// atomic set-if-absent satisfies this.
type DedupStore interface {
	// Acquire attempts to claim key for ttl. It returns acquired=true for the
	// FIRST writer (set succeeded) and acquired=false when the key already
	// exists (another writer holds it — first-writer-wins). A store error is
	// returned as err and MUST block the guarded write (pessimistic posture).
	Acquire(ctx context.Context, key string, ttl time.Duration) (acquired bool, err error)
	// Release deletes key so a legitimate retry can re-attempt after a failure.
	Release(ctx context.Context, key string) error
}

// defaultDedupTTL bounds a dedup lock so a crashed process cannot leave a
// permanent tombstone that suppresses a legitimate write forever
// (idempotency-and-dedup).
const defaultDedupTTL = 30 * time.Second

// dedupGuard applies the idempotency-key dedup lock around a guarded write. One
// guard wraps one DedupStore; the resilientClient holds it when a dedup-capable
// store (valkey) is configured.
type dedupGuard struct {
	store DedupStore
	ttl   time.Duration
}

// newDedupGuard builds a guard over store with the given TTL (defaultDedupTTL
// when ttl <= 0).
func newDedupGuard(store DedupStore, ttl time.Duration) *dedupGuard {
	if ttl <= 0 {
		ttl = defaultDedupTTL
	}
	return &dedupGuard{store: store, ttl: ttl}
}

// guard runs write under the dedup lock for op.IdempotencyKey. Semantics (§6.2,
// idempotency-and-dedup):
//   - FIRST writer wins: Acquire succeeds -> run write; if write FAILS, Release
//     the lock so a legitimate retry can re-attempt; if write SUCCEEDS, keep the
//     lock until TTL (a replay within the window is deduped).
//   - A SUBSEQUENT writer (lock already held) SKIPS the write and returns a
//     deduped short-circuit result — the first writer owns the effect.
//   - A store error BLOCKS the write (pessimistic): a dedup-lock failure is
//     surfaced as Upstream rather than risking a duplicate side effect.
func (g *dedupGuard) guard(ctx context.Context, connKey string, op Operation, write func() (any, error)) (any, error) {
	lockKey := dedupLockKey(connKey, op.Kind, op.IdempotencyKey)

	acquired, err := g.store.Acquire(ctx, lockKey, g.ttl)
	if err != nil {
		return nil, wrapErr(Upstream, connKey, op.Kind, "idempotency dedup lock unavailable", err)
	}
	if !acquired {
		// Another writer holds the lock: the effect is already in flight or done.
		return map[string]any{"deduped": true, "idempotencyKey": op.IdempotencyKey}, nil
	}

	res, werr := write()
	if werr != nil {
		// Release so a legitimate retry can re-attempt; a lock that outlives a
		// failed write would permanently suppress it.
		_ = g.store.Release(ctx, lockKey)
		return nil, werr
	}
	return res, nil
}

// dedupLockKey namespaces the lock under the connection + op kind so two
// different writes that happen to share an idempotency key do not collide.
func dedupLockKey(connKey, opKind, idemKey string) string {
	return "nzr:dedup:" + connKey + ":" + opKind + ":" + idemKey
}
