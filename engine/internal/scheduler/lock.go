package scheduler

import (
	"context"
	"strconv"
	"time"

	"github.com/valkey-io/valkey-go"

	"nzr-rules-engine/internal/observ"
)

// DistributedLocker is the lock interface the scheduler depends on.
// A nil implementation means single-instance mode (no Valkey).
type DistributedLocker interface {
	// TryAcquire attempts SET key token NX PX ttlMs.
	// Returns (true, nil) on success, (false, nil) when key already exists,
	// (false, err) on transport error.
	TryAcquire(ctx context.Context, key, token string, ttl time.Duration) (bool, error)

	// Release deletes the key only if its value matches token (Lua CAS delete).
	// Errors are logged at warn level and swallowed — TTL is the safety backstop.
	Release(ctx context.Context, key, token string) error

	// Heartbeat renews the lock every ttl/3 until ctx is cancelled or lock loss
	// is detected. On lock loss (Lua conditional renew returns 0), it logs at
	// warn level and returns — the caller's execCtx expires on its own deadline.
	Heartbeat(ctx context.Context, key, token string, ttl time.Duration)
}

// ValkeyLocker implements DistributedLocker using Valkey (Redis protocol).
type ValkeyLocker struct {
	client valkey.Client
	log    observ.Logger
}

// NewValkeyLocker creates a ValkeyLocker backed by the given valkey client.
func NewValkeyLocker(client valkey.Client, log observ.Logger) *ValkeyLocker {
	return &ValkeyLocker{client: client, log: log}
}

// TryAcquire attempts to acquire a distributed lock using SET NX PX.
func (l *ValkeyLocker) TryAcquire(ctx context.Context, key, token string, ttl time.Duration) (bool, error) {
	cmd := l.client.B().Set().Key(key).Value(token).Nx().PxMilliseconds(ttl.Milliseconds()).Build()
	err := l.client.Do(ctx, cmd).Error()
	if valkey.IsValkeyNil(err) {
		// Key already exists — lock not acquired (normal contention).
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// releaseLua is the Lua script for CAS delete: only delete if value matches.
const releaseLua = `
if redis.call("get", KEYS[1]) == ARGV[1] then
    return redis.call("del", KEYS[1])
else
    return 0
end`

// Release deletes the lock key only if its value matches the token.
// Errors are logged and swallowed — the TTL is the safety backstop.
func (l *ValkeyLocker) Release(ctx context.Context, key, token string) error {
	err := l.client.Do(ctx,
		l.client.B().Eval().Script(releaseLua).Numkeys(1).Key(key).Arg(token).Build(),
	).Error()
	if err != nil {
		l.log.Emit(ctx, "warn", "scheduler: lock release error", map[string]any{
			"key": key,
			"err": err.Error(),
		})
	}
	return nil // always nil — TTL backstop covers release failures
}

// renewLua conditionally extends the TTL if the value matches.
const renewLua = `
if redis.call("get", KEYS[1]) == ARGV[1] then
    return redis.call("pexpire", KEYS[1], ARGV[2])
else
    return 0
end`

// Heartbeat renews the lock at ttl/3 intervals until ctx is cancelled or
// the lock is lost. On lock loss, it logs a warning and returns.
func (l *ValkeyLocker) Heartbeat(ctx context.Context, key, token string, ttl time.Duration) {
	ticker := time.NewTicker(ttl / 3)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			result, err := l.client.Do(ctx,
				l.client.B().Eval().Script(renewLua).Numkeys(1).Key(key).
					Arg(token, strconv.FormatInt(ttl.Milliseconds(), 10)).Build(),
			).ToInt64()
			if err != nil || result == 0 {
				// Lock was lost or Valkey error. Log at warn and return.
				// The caller's execCtx expires on its own timeout; there is no
				// mechanism to cancel the in-flight interpreter.Run externally.
				l.log.Emit(ctx, "warn", "scheduler: lock heartbeat lost", map[string]any{
					"key":    key,
					"err":    errStr(err),
					"result": result,
				})
				return
			}
		}
	}
}

// errStr safely converts an error to string, returning empty string for nil.
func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
