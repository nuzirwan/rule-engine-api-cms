package drivers

import (
	"context"
	"errors"
	"time"

	"github.com/valkey-io/valkey-go"

	"nzr-rules-engine/internal/connect"
)

// valkeyConnector builds valkey-go-backed clients for the "valkey" connection
// type. The valkey client owns one shared connection pool (one per key, AC-5);
// Registry.Client hands the same wrapped instance to every caller.
type valkeyConnector struct{}

// newValkeyConnector returns the valkey Connector.
func newValkeyConnector() connect.Connector { return valkeyConnector{} }

// Type implements connect.Connector.
func (valkeyConnector) Type() string { return "valkey" }

// Lifecycle implements connect.Connector. Valkey manages its own connection pool.
func (valkeyConnector) Lifecycle() connect.Lifecycle { return connect.LifecyclePooled }

// Capabilities implements connect.Connector. Valkey supports key-value ops and
// can serve as a dedup store for idempotency locks.
func (valkeyConnector) Capabilities() connect.Capability {
	return connect.CapKeyValue | connect.CapDedupStore
}

// Open builds a valkey client from the def's Settings and the resolved secret.
// Settings carry addr/addrs (InitAddress), db (SelectDB), and optional tls; the
// resolved secret supplies the password. No per-op timeout is set on the client
// — the resilience envelope's context governs the deadline (consistent with the
// rest driver, slice-b-connections.md §2.3).
func (valkeyConnector) Open(ctx context.Context, def connect.ConnectionDef) (connect.Client, error) {
	addrs := valkeyAddrs(def.Settings)
	if len(addrs) == 0 {
		return nil, connect.NewConnError(connect.Validation, def.Key, "", "valkey settings need addr or addrs", nil)
	}

	opt := valkey.ClientOption{
		InitAddress: addrs,
		// DisableCache keeps the thin footprint predictable; client-side caching
		// is a later optimization and must not change get/set/del semantics.
		DisableCache: true,
	}
	if db, ok := intSetting(def.Settings, "db"); ok {
		opt.SelectDB = db
	}
	if user, ok := stringSetting(def.Settings, "user"); ok {
		opt.Username = user
	}
	if sec, ok := connect.SecretFrom(ctx); ok && !sec.IsZero() {
		opt.Password = string(sec.Reveal())
	}

	client, err := valkey.NewClient(opt)
	if err != nil {
		return nil, connect.NewConnError(connect.Upstream, def.Key, "", "open valkey client", err)
	}
	return &valkeyClient{key: def.Key, client: client}, nil
}

// valkeyAddrs reads addr (string) or addrs ([]string / []any of strings).
func valkeyAddrs(s map[string]any) []string {
	if s == nil {
		return nil
	}
	if one, ok := stringSetting(s, "addr"); ok && one != "" {
		return []string{one}
	}
	switch raw := s["addrs"].(type) {
	case []string:
		return raw
	case []any:
		out := make([]string, 0, len(raw))
		for _, v := range raw {
			if str, ok := v.(string); ok && str != "" {
				out = append(out, str)
			}
		}
		return out
	default:
		return nil
	}
}

// valkeyClient is the inner driver client over a shared valkey connection pool.
type valkeyClient struct {
	key    string
	client valkey.Client
}

// Execute dispatches get/set/del/ping. An unsupported kind is a Validation error
// (never a panic).
func (c *valkeyClient) Execute(ctx context.Context, op connect.Operation) (any, error) {
	switch op.Kind {
	case "get":
		return c.get(ctx, op)
	case "set":
		return c.set(ctx, op)
	case "del":
		return c.del(ctx, op)
	case "ping":
		if err := c.client.Do(ctx, c.client.B().Ping().Build()).Error(); err != nil {
			return nil, c.classify("ping", err)
		}
		return map[string]any{"ok": true}, nil
	default:
		return nil, connect.NewConnError(connect.Validation, c.key, op.Kind, "unsupported operation kind for valkey", nil)
	}
}

// get reads a key. A missing key maps to NotFound (not an empty-but-ok), so the
// action node decides whether absence is acceptable (slice-b-connections.md §2.2).
func (c *valkeyClient) get(ctx context.Context, op connect.Operation) (any, error) {
	key, ok := stringSetting(op.Payload, "key")
	if !ok || key == "" {
		return nil, connect.NewConnError(connect.Validation, c.key, "get", "missing key in payload", nil)
	}
	val, err := c.client.Do(ctx, c.client.B().Get().Key(key).Build()).ToString()
	if err != nil {
		if valkey.IsValkeyNil(err) {
			return nil, connect.NewConnError(connect.NotFound, c.key, "get", "key not found", err)
		}
		return nil, c.classify("get", err)
	}
	return val, nil
}

// set writes a key with optional ttl and NX. SET is naturally idempotent
// (idempotency-and-dedup). With nx=true a missed set (key already present)
// returns {ok:false}; otherwise {ok:true}.
func (c *valkeyClient) set(ctx context.Context, op connect.Operation) (any, error) {
	key, ok := stringSetting(op.Payload, "key")
	if !ok || key == "" {
		return nil, connect.NewConnError(connect.Validation, c.key, "set", "missing key in payload", nil)
	}
	val, err := valkeyValueString(op.Payload["value"])
	if err != nil {
		return nil, connect.NewConnError(connect.Validation, c.key, "set", "unsupported value type", err)
	}

	nx, _ := op.Payload["nx"].(bool)
	ttl := valkeyTTL(op.Payload["ttl"])

	if nx {
		// SET key val NX [PX ttl]: the builder orders NX before the expiration.
		var resp valkey.ValkeyResult
		if ttl > 0 {
			resp = c.client.Do(ctx, c.client.B().Set().Key(key).Value(val).Nx().PxMilliseconds(ttl.Milliseconds()).Build())
		} else {
			resp = c.client.Do(ctx, c.client.B().Set().Key(key).Value(val).Nx().Build())
		}
		if err := resp.Error(); err != nil {
			if valkey.IsValkeyNil(err) {
				// NX miss: the key already existed, the set was a no-op.
				return map[string]any{"ok": false}, nil
			}
			return nil, c.classify("set", err)
		}
		return map[string]any{"ok": true}, nil
	}

	var resp valkey.ValkeyResult
	if ttl > 0 {
		resp = c.client.Do(ctx, c.client.B().Set().Key(key).Value(val).PxMilliseconds(ttl.Milliseconds()).Build())
	} else {
		resp = c.client.Do(ctx, c.client.B().Set().Key(key).Value(val).Build())
	}
	if err := resp.Error(); err != nil {
		return nil, c.classify("set", err)
	}
	return map[string]any{"ok": true}, nil
}

// del removes one key ("key") or many ("keys") and returns {deleted}.
func (c *valkeyClient) del(ctx context.Context, op connect.Operation) (any, error) {
	keys := valkeyKeys(op.Payload)
	if len(keys) == 0 {
		return nil, connect.NewConnError(connect.Validation, c.key, "del", "missing key(s) in payload", nil)
	}
	n, err := c.client.Do(ctx, c.client.B().Del().Key(keys...).Build()).AsInt64()
	if err != nil {
		return nil, c.classify("del", err)
	}
	return map[string]any{"deleted": n}, nil
}

// Close releases the connection pool.
func (c *valkeyClient) Close() error {
	c.client.Close()
	return nil
}

// Acquire implements connect.DedupStore via SET key val NX PX ttl — an atomic
// set-if-absent used by the idempotency-key dedup lock (R4, §6.2). It returns
// acquired=true for the first writer (set succeeded) and false on an NX miss
// (another writer holds the lock).
func (c *valkeyClient) Acquire(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	if ttl <= 0 {
		ttl = time.Second
	}
	err := c.client.Do(ctx, c.client.B().Set().Key(key).Value("1").Nx().PxMilliseconds(ttl.Milliseconds()).Build()).Error()
	if err != nil {
		if valkey.IsValkeyNil(err) {
			return false, nil // NX miss: already held
		}
		return false, c.classify("dedup-acquire", err)
	}
	return true, nil
}

// Release implements connect.DedupStore via DEL so a failed guarded write frees
// the lock for a legitimate retry.
func (c *valkeyClient) Release(ctx context.Context, key string) error {
	if err := c.client.Do(ctx, c.client.B().Del().Key(key).Build()).Error(); err != nil {
		return c.classify("dedup-release", err)
	}
	return nil
}

// classify maps a valkey-go error to the shared taxonomy: a ctx deadline =>
// Timeout, everything else => Upstream (the server is down/unreachable or
// returned a protocol error). A nil-reply is handled by callers as NotFound.
func (c *valkeyClient) classify(opKind string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return connect.NewConnError(connect.Timeout, c.key, opKind, "valkey deadline exceeded", err)
	}
	return connect.NewConnError(connect.Upstream, c.key, opKind, "valkey error", err)
}

// valkeyValueString coerces a payload value to the string valkey stores. Strings
// pass through; a []byte is used directly; other scalar JSON types are rendered
// with their natural string form. A nil or composite value is rejected as a
// Validation error by the caller.
func valkeyValueString(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return x, nil
	case []byte:
		return string(x), nil
	case nil:
		return "", errors.New("nil value")
	default:
		b, err := jsonMarshal(x)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
}

// valkeyKeys reads a single "key" or a "keys" list from the payload.
func valkeyKeys(p map[string]any) []string {
	if p == nil {
		return nil
	}
	if one, ok := stringSetting(p, "key"); ok && one != "" {
		return []string{one}
	}
	switch raw := p["keys"].(type) {
	case []string:
		return raw
	case []any:
		out := make([]string, 0, len(raw))
		for _, v := range raw {
			if str, ok := v.(string); ok && str != "" {
				out = append(out, str)
			}
		}
		return out
	default:
		return nil
	}
}

// valkeyTTL parses a ttl payload value: a time.Duration, an integer count of
// milliseconds (JSON number => float64), or a duration string ("5s"). A zero/
// invalid ttl means "no expiry".
func valkeyTTL(v any) time.Duration {
	switch x := v.(type) {
	case time.Duration:
		return x
	case int:
		return time.Duration(x) * time.Millisecond
	case int64:
		return time.Duration(x) * time.Millisecond
	case float64:
		return time.Duration(x) * time.Millisecond
	case string:
		if d, err := time.ParseDuration(x); err == nil {
			return d
		}
	}
	return 0
}
