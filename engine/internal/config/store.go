// Package config holds the configuration store seam (Slice D, lld-contracts.md):
// the resolver, admin, and connection registry read flow versions, JDM bytes,
// and connection defs through config.Store.
//
// Two implementations satisfy the frozen Store seam:
//   - memStore (store.go / seed.go): the in-memory store seeded from a JSON file,
//     used by the thin slice and by unit tests that need no database.
//   - PgStore (pgstore.go): the real Postgres-backed store with the immutable
//     versioning model, active-pointer publish/rollback, audit log, and an
//     optional Valkey Cache (cache.go) with pub/sub invalidation.
//
// Both are selected at wiring time (a later step); this package builds and tests
// standalone. config imports flow (one-directional); flow never imports config.
package config

import (
	"context"
	"sort"
	"sync"
	"time"

	"nzr-rules-engine/internal/connect"
	"nzr-rules-engine/internal/flow"
)

// FlowFixture travels with a flow version for admin test/dry-run. The thin slice
// does not execute fixtures but carries them so the type matches the contract.
type FlowFixture struct {
	Name  string         `json:"name"`
	Input map[string]any `json:"input"`
	Want  map[string]any `json:"want,omitempty"`
}

// FlowSummary is the list response shape for GET /admin/flows (one row per flow).
type FlowSummary struct {
	ID            string     `json:"id"`
	Method        string     `json:"method"`
	Path          string     `json:"path"`
	ActiveVersion *int       `json:"activeVersion"` // nil if unpublished
	UpdatedAt     *time.Time `json:"updatedAt,omitempty"`
}

// VersionSummary is one version in the list returned by GET /admin/flows/{id}/versions.
type VersionSummary struct {
	Version   int       `json:"version"`
	Validated bool      `json:"validated"`
	CreatedAt time.Time `json:"createdAt"`
	CreatedBy string    `json:"createdBy,omitempty"`
}

// JDMSummary is the list response shape for GET /admin/jdms (one row per JDM).
type JDMSummary struct {
	ID        string     `json:"id"`
	UpdatedAt *time.Time `json:"updatedAt,omitempty"`
}

// FlowVersion is one published version of a flow. Tree is the root flow.Node, so
// config imports flow (one-directional: flow never imports config). It mirrors
// the frozen Store contract (lld-contracts.md).
type FlowVersion struct {
	FlowID   string        `json:"flowId"`
	Version  int           `json:"version"`
	Method   string        `json:"method"`
	Path     string        `json:"path"`
	Group    string        `json:"group,omitempty"` // group_id from flow_versions; empty if ungrouped
	Tree     flow.Node     `json:"tree"`
	Fixtures []FlowFixture `json:"fixtures,omitempty"`
	// CompiledInputSchema is the pre-compiled JSON Schema for input validation.
	// It is transient (not serialized) — rebuilt from Tree on decode.
	CompiledInputSchema any `json:"-"`
}

// RouteInfo describes one active route: the flow it resolves to and the stable
// method+path pattern it is registered under. The HTTP edge enumerates these at
// startup to build its router from config instead of hardcoded registrations.
type RouteInfo struct {
	FlowID string
	Method string
	Path   string
}

// Store is the config seam consumed by the resolver, admin, and registry. The
// method set matches lld-contracts.md exactly so the real store substitutes
// without a caller change. ActiveRoutes is a deliberate seam addition (Slice E)
// so the HTTP edge builds its router from the active config.
type Store interface {
	ActiveFlow(ctx context.Context, env, method, path string) (FlowVersion, error)
	ActiveRoutes(ctx context.Context, env string) ([]RouteInfo, error)
	GetJDM(ctx context.Context, env, id string) (jdm []byte, version int, err error)
	Connections(ctx context.Context, env string) ([]connect.ConnectionDef, error)
	PutFlowVersion(ctx context.Context, env string, f FlowVersion) (version int, err error)
	SetActive(ctx context.Context, env, flowID string, version int) error
	Ping(ctx context.Context) error
}

// Cache is the hot-config cache seam (lld-contracts.md). The Valkey-backed
// implementation (cache.go) also publishes a change event on Invalidate so every
// instance drops its matching keys; that publish is internal behavior of the
// implementation, not an extra seam method. A nil Cache is valid: the real store
// then reads Postgres directly (cache is an optimization, never the source of
// truth — [[caching-strategy]]).
type Cache interface {
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Set(ctx context.Context, key string, v []byte, ttl time.Duration) error
	Invalidate(ctx context.Context, keyPattern string) error // publishes change event
}

// WebhookStore is the interface for webhook admin operations. It is NOT on the
// frozen Store seam — the hot-path receiver uses GetActiveWebhook via the
// concrete PgStore. This interface exists for testing admin handlers.
type WebhookStore interface {
	// Hot path (used by webhook receiver):
	GetActiveWebhook(ctx context.Context, env, webhookID string) (Webhook, error)

	// Admin methods:
	CreateWebhook(ctx context.Context, env string, w Webhook) (version int, err error)
	GetWebhook(ctx context.Context, env, webhookID string) (Webhook, error)
	GetWebhookVersion(ctx context.Context, env, webhookID string, version int) (Webhook, error)
	ListWebhooks(ctx context.Context, env string) ([]WebhookSummary, error)
	ListWebhookVersions(ctx context.Context, env, webhookID string) ([]WebhookVersionSummary, error)
	UpdateWebhook(ctx context.Context, env string, w Webhook) (version int, err error)
	DeleteWebhook(ctx context.Context, env, webhookID string) error
	SetWebhookActive(ctx context.Context, env, webhookID string, version int) error
	LogWebhook(ctx context.Context, env string, log WebhookLog) (int64, error)
	GetWebhookLogs(ctx context.Context, env, webhookID string, limit int) ([]WebhookLog, error)
}

// jdmEntry is a JDM's bytes plus its version, keyed by (env,id).
type jdmEntry struct {
	bytes   []byte
	version int
}

// memStore is the in-memory Store. It is keyed by env so a dev flow is never
// resolved in prod; the thin-slice seed uses the empty "" env. All reads/writes
// are guarded by a single RWMutex (the thin slice has no hot-reload churn).
type memStore struct {
	mu sync.RWMutex

	// flows[env][flowID][version] -> FlowVersion
	flows map[string]map[string]map[int]FlowVersion
	// active[env][routeKey] -> {flowID, version} chooses the active version per route.
	active map[string]map[string]activeRef
	// jdms[env][id] -> bytes+version
	jdms map[string]map[string]jdmEntry
	// conns[env] -> connection defs
	conns map[string][]connect.ConnectionDef
}

// activeRef points the active route at a specific flow version.
type activeRef struct {
	FlowID  string
	Version int
}

// compile-time assertion that *memStore satisfies the frozen Store seam.
var _ Store = (*memStore)(nil)

// NewMemStore returns an empty in-memory store. Use Seed/LoadSeed to populate it.
func NewMemStore() *memStore {
	return &memStore{
		flows:  make(map[string]map[string]map[int]FlowVersion),
		active: make(map[string]map[string]activeRef),
		jdms:   make(map[string]map[string]jdmEntry),
		conns:  make(map[string][]connect.ConnectionDef),
	}
}

// routeKey builds the active-flow lookup key from method + path.
func routeKey(method, path string) string { return method + " " + path }

// ActiveFlow resolves the active flow version for (env, method, path). A missing
// route is a NotFound error.
func (s *memStore) ActiveFlow(ctx context.Context, env, method, path string) (FlowVersion, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	ref, ok := s.active[env][routeKey(method, path)]
	if !ok {
		return FlowVersion{}, newErr(NotFound, "no active flow for route "+routeKey(method, path))
	}
	fv, ok := s.flows[env][ref.FlowID][ref.Version]
	if !ok {
		return FlowVersion{}, newErr(NotFound, "active flow version missing for "+ref.FlowID)
	}
	return fv, nil
}

// ActiveRoutes enumerates the active routes for env, resolving each active
// pointer to its flow version so the HTTP edge can register one handler per
// route. The result is sorted by Path then Method for a deterministic registration
// order. An env with no active routes returns an empty (non-nil) slice, nil error.
func (s *memStore) ActiveRoutes(ctx context.Context, env string) ([]RouteInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]RouteInfo, 0, len(s.active[env]))
	for _, ref := range s.active[env] {
		fv, ok := s.flows[env][ref.FlowID][ref.Version]
		if !ok {
			continue // active pointer with no matching version body: skip
		}
		out = append(out, RouteInfo{FlowID: ref.FlowID, Method: fv.Method, Path: fv.Path})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Method < out[j].Method
	})
	return out, nil
}

// GetJDM returns the JDM bytes + version for (env, id). A missing JDM is NotFound.
func (s *memStore) GetJDM(ctx context.Context, env, id string) ([]byte, int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, ok := s.jdms[env][id]
	if !ok {
		return nil, 0, newErr(NotFound, "no jdm for id "+id)
	}
	// Return a copy so a caller cannot mutate the stored bytes.
	out := make([]byte, len(entry.bytes))
	copy(out, entry.bytes)
	return out, entry.version, nil
}

// Connections returns the connection defs for env (empty slice if none).
func (s *memStore) Connections(ctx context.Context, env string) ([]connect.ConnectionDef, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	defs := s.conns[env]
	out := make([]connect.ConnectionDef, len(defs))
	copy(out, defs)
	return out, nil
}

// PutFlowVersion stores a flow version, assigning the next version number for
// its flow id in env, and returns the assigned version. It does not publish
// (SetActive does); a freshly put version is inactive until set active.
func (s *memStore) PutFlowVersion(ctx context.Context, env string, f FlowVersion) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if f.FlowID == "" {
		return 0, newErr(Validation, "flow version missing flowId")
	}
	if s.flows[env] == nil {
		s.flows[env] = make(map[string]map[int]FlowVersion)
	}
	if s.flows[env][f.FlowID] == nil {
		s.flows[env][f.FlowID] = make(map[int]FlowVersion)
	}
	version := f.Version
	if version == 0 {
		version = len(s.flows[env][f.FlowID]) + 1
		f.Version = version
	}
	s.flows[env][f.FlowID][version] = f
	return version, nil
}

// SetActive points the active route (method+path of the stored version) at a
// specific flow version, enabling publish/rollback. The version must exist.
func (s *memStore) SetActive(ctx context.Context, env, flowID string, version int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	fv, ok := s.flows[env][flowID][version]
	if !ok {
		return newErr(NotFound, "cannot activate missing flow version")
	}
	if s.active[env] == nil {
		s.active[env] = make(map[string]activeRef)
	}
	s.active[env][routeKey(fv.Method, fv.Path)] = activeRef{FlowID: flowID, Version: version}
	return nil
}

// Ping reports store reachability for /readyz. The in-memory store is always
// reachable once constructed.
func (s *memStore) Ping(ctx context.Context) error { return nil }
