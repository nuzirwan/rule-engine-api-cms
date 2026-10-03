package config

import "context"

// auditActorKey carries the per-request audit actor on the context. The admin
// edge sets it to the authenticated operator subject before invoking a store
// write so each mutation is attributed to the operator who made it, without any
// method-signature change (slice-f-admin-api.md §3.5, option B). This mirrors the
// codebase's env-on-context / dry-run-on-context conventions.
type auditActorKey struct{}

// WithAuditActor returns a context carrying subject as the audit actor for any
// store write performed under it. An empty subject is ignored so the store falls
// back to its construction-time actor (WithActor, default "engine").
func WithAuditActor(ctx context.Context, subject string) context.Context {
	if subject == "" {
		return ctx
	}
	return context.WithValue(ctx, auditActorKey{}, subject)
}

// auditActorFrom reads the ctx-carried audit actor, or "" when none is set.
func auditActorFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	s, _ := ctx.Value(auditActorKey{}).(string)
	return s
}

// actorFor returns the effective audit actor for a store write: the ctx-carried
// operator subject when present, else the store's construction-time actor.
func (s *PgStore) actorFor(ctx context.Context) string {
	if a := auditActorFrom(ctx); a != "" {
		return a
	}
	return s.actor
}
