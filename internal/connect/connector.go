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
