package decision

import "context"

// envCtxKey carries the request environment on the context so Evaluate resolves
// a JDM within the right env (per-env isolation, ADR-006) without widening the
// public Evaluate signature. httpapi/middleware stamps it per request.
type envCtxKey struct{}

// WithEnv returns a context carrying env for JDM resolution.
func WithEnv(ctx context.Context, env string) context.Context {
	return context.WithValue(ctx, envCtxKey{}, env)
}

// EnvFromContext returns the request environment stamped on ctx, or "" when
// absent (the default/empty env used by the thin-slice seed).
func EnvFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if env, ok := ctx.Value(envCtxKey{}).(string); ok {
		return env
	}
	return ""
}
