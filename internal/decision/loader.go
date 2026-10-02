package decision

import "context"

// JDMLoader resolves the active JDM bytes + version for an id within a request's
// environment. decision depends on this abstraction, not on config.Store
// directly (dependency inversion) — a thin adapter over config.Store.GetJDM
// satisfies it so a dev JDM is never evaluated against prod (per-env isolation,
// ADR-006). The version is the cache key component: an immutable (id,version)
// compiles to a byte-identical graph.
type JDMLoader interface {
	LoadJDM(ctx context.Context, env, jdmID string) (jdm []byte, version int, err error)
}
