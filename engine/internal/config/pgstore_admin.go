package config

import (
	"context"
	"encoding/json"
	"errors"
	"nzr-rules-engine/internal/connect"

	"github.com/jackc/pgx/v5"
)

// This file holds store-internal reads driven by the admin surface and the
// per-object idempotent seeder (slice-f-admin-api.md §6.3 / §2.6). They are NOT
// on the frozen Store seam — they mirror the existing store-internal
// MarkValidated/PromoteVersion/AuditTrail methods and are satisfied structurally
// by *PgStore.

// FlowExists reports whether a flow IDENTITY exists in env (one indexed PK read
// of the flows table). It gates per-object idempotent seeding on flow identity
// rather than the active route, because a created-but-not-yet-published flow has
// no active route yet and must not be re-created on restart (§6.2). A no-row
// result is (false, nil); a real DB error is surfaced (Upstream via classifyPg).
func (s *PgStore) FlowExists(ctx context.Context, env, flowID string) (bool, error) {
	return s.existsByPK(ctx, env, `SELECT 1 FROM flows WHERE id=$1`, flowID, "check flow exists")
}

// JDMExists reports whether a JDM identity exists in env (one indexed PK read of
// the jdms table). Semantics mirror FlowExists.
func (s *PgStore) JDMExists(ctx context.Context, env, jdmID string) (bool, error) {
	return s.existsByPK(ctx, env, `SELECT 1 FROM jdms WHERE id=$1`, jdmID, "check jdm exists")
}

// ConnectionExists reports whether a connection identity exists in env (one
// indexed PK read of the connections table). Semantics mirror FlowExists.
func (s *PgStore) ConnectionExists(ctx context.Context, env, key string) (bool, error) {
	return s.existsByPK(ctx, env, `SELECT 1 FROM connections WHERE key=$1`, key, "check connection exists")
}

// existsByPK runs a single-row existence probe. pgx.ErrNoRows => (false, nil);
// any other driver error is classified and surfaced so a seed never silently
// treats a transient outage as "absent" and double-writes.
func (s *PgStore) existsByPK(ctx context.Context, env, query, id, op string) (bool, error) {
	pool, err := s.pool(env)
	if err != nil {
		return false, err
	}
	var one int
	err = pool.QueryRow(ctx, query, id).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, classifyPg(op, err)
	}
	return true, nil
}

// GetFlowVersion loads a SPECIFIC flow version body + its fixtures by
// (flowID, version), decoding the stored tree exactly as ActiveFlow does. A
// missing row is NotFound. It is the stored-mode read for /admin/flows/validate
// (and any future "inspect version N"); it does NOT consult the cache or the
// active pointer (ActiveFlow stays the hot, active-only path, §2.6).
func (s *PgStore) GetFlowVersion(ctx context.Context, env, flowID string, version int) (FlowVersion, error) {
	pool, err := s.pool(env)
	if err != nil {
		return FlowVersion{}, err
	}

	var treeRaw []byte
	var method, path string
	var groupID *string
	err = pool.QueryRow(ctx,
		`SELECT fv.tree, f.method, f.path, fv.group_id
		   FROM flow_versions fv JOIN flows f ON f.id = fv.flow_id
		  WHERE fv.flow_id=$1 AND fv.version=$2`, flowID, version).Scan(&treeRaw, &method, &path, &groupID)
	if errors.Is(err, pgx.ErrNoRows) {
		return FlowVersion{}, newErr(NotFound, "no such flow version")
	}
	if err != nil {
		return FlowVersion{}, classifyPg("read flow version", err)
	}

	tree, err := decodeTree(treeRaw) // malformed tree => Validation, never a 500
	if err != nil {
		return FlowVersion{}, err
	}

	fixtures, err := s.loadFixtures(ctx, pool, flowID, version)
	if err != nil {
		return FlowVersion{}, err
	}

	fv := FlowVersion{
		FlowID:   flowID,
		Version:  version,
		Method:   method,
		Path:     path,
		Tree:     tree,
		Fixtures: fixtures,
	}
	if groupID != nil {
		fv.Group = *groupID
	}
	return fv, nil
}

// ListFlows returns all flows for env with their active versions (if any).
func (s *PgStore) ListFlows(ctx context.Context, env string) ([]FlowSummary, error) {
	pool, err := s.pool(env)
	if err != nil {
		return nil, err
	}

	rows, err := pool.Query(ctx,
		`SELECT f.id, f.method, f.path, ap.version, f.updated_at
		   FROM flows f
		   LEFT JOIN active_pointers ap ON ap.object_type=$1 AND ap.object_id = f.id
		  ORDER BY f.path, f.method`, objFlow)
	if err != nil {
		return nil, classifyPg("list flows", err)
	}
	defer rows.Close()

	out := make([]FlowSummary, 0)
	for rows.Next() {
		var fs FlowSummary
		if err := rows.Scan(&fs.ID, &fs.Method, &fs.Path, &fs.ActiveVersion, &fs.UpdatedAt); err != nil {
			return nil, classifyPg("scan flow", err)
		}
		out = append(out, fs)
	}
	if err := rows.Err(); err != nil {
		return nil, classifyPg("iterate flows", err)
	}
	return out, nil
}

// ListFlowVersions returns version summaries for a single flow, newest first.
func (s *PgStore) ListFlowVersions(ctx context.Context, env, flowID string) ([]VersionSummary, error) {
	pool, err := s.pool(env)
	if err != nil {
		return nil, err
	}

	// First verify the flow exists.
	var exists int
	err = pool.QueryRow(ctx, `SELECT 1 FROM flows WHERE id=$1`, flowID).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, newErr(NotFound, "no such flow")
	}
	if err != nil {
		return nil, classifyPg("check flow exists", err)
	}

	rows, err := pool.Query(ctx,
		`SELECT version, validated, created_at, created_by
		   FROM flow_versions WHERE flow_id=$1
		  ORDER BY version DESC`, flowID)
	if err != nil {
		return nil, classifyPg("list flow versions", err)
	}
	defer rows.Close()

	out := make([]VersionSummary, 0)
	for rows.Next() {
		var vs VersionSummary
		var createdBy *string
		if err := rows.Scan(&vs.Version, &vs.Validated, &vs.CreatedAt, &createdBy); err != nil {
			return nil, classifyPg("scan flow version", err)
		}
		if createdBy != nil {
			vs.CreatedBy = *createdBy
		}
		out = append(out, vs)
	}
	if err := rows.Err(); err != nil {
		return nil, classifyPg("iterate flow versions", err)
	}
	return out, nil
}

// ListJDMs returns all JDMs for env with their updated timestamps.
func (s *PgStore) ListJDMs(ctx context.Context, env string) ([]JDMSummary, error) {
	pool, err := s.pool(env)
	if err != nil {
		return nil, err
	}

	rows, err := pool.Query(ctx,
		`SELECT id, updated_at FROM jdms ORDER BY id`)
	if err != nil {
		return nil, classifyPg("list jdms", err)
	}
	defer rows.Close()

	out := make([]JDMSummary, 0)
	for rows.Next() {
		var js JDMSummary
		if err := rows.Scan(&js.ID, &js.UpdatedAt); err != nil {
			return nil, classifyPg("scan jdm", err)
		}
		out = append(out, js)
	}
	if err := rows.Err(); err != nil {
		return nil, classifyPg("iterate jdms", err)
	}
	return out, nil
}

// GetActiveFlowByID resolves the active flow version by flow ID (not by method+path).
// This is used by webhooks which store a flow_id reference rather than a route pattern.
// A missing flow or no active version is NotFound.
func (s *PgStore) GetActiveFlowByID(ctx context.Context, env, flowID string) (FlowVersion, error) {
	pool, err := s.pool(env)
	if err != nil {
		return FlowVersion{}, err
	}

	// Get the active version number from active_pointers.
	var activeVersion int
	err = pool.QueryRow(ctx,
		`SELECT version FROM active_pointers WHERE object_type=$1 AND object_id=$2`,
		objFlow, flowID).Scan(&activeVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return FlowVersion{}, newErr(NotFound, "no active flow for "+quote(flowID))
	}
	if err != nil {
		return FlowVersion{}, classifyPg("get active flow version", err)
	}

	// Delegate to GetFlowVersion which already handles tree decoding and fixtures.
	return s.GetFlowVersion(ctx, env, flowID, activeVersion)
}

// GetConnection returns a single connection def by key, or NotFound.
func (s *PgStore) GetConnection(ctx context.Context, env, key string) (connect.ConnectionDef, error) {
	pool, err := s.pool(env)
	if err != nil {
		return connect.ConnectionDef{}, err
	}

	var (
		ctype         string
		settingsRaw   []byte
		secretRef     *string
		resilienceRaw []byte
	)
	err = pool.QueryRow(ctx,
		`SELECT c.type, cv.settings, cv.secret_ref, cv.resilience
		   FROM active_pointers ap
		   JOIN connections c  ON c.key = ap.object_id
		   JOIN connection_versions cv ON cv.conn_key = ap.object_id AND cv.version = ap.version
		  WHERE ap.object_type=$1 AND ap.object_id=$2`, objConnection, key).Scan(&ctype, &settingsRaw, &secretRef, &resilienceRaw)
	if errors.Is(err, pgx.ErrNoRows) {
		return connect.ConnectionDef{}, newErr(NotFound, "no such connection")
	}
	if err != nil {
		return connect.ConnectionDef{}, classifyPg("get connection", err)
	}

	var settings map[string]any
	if len(settingsRaw) > 0 {
		if err := json.Unmarshal(settingsRaw, &settings); err != nil {
			return connect.ConnectionDef{}, wrapErr(Validation, "decode connection settings (bad config)", err)
		}
	}
	var res connect.ResiliencePolicy
	if len(resilienceRaw) > 0 {
		if err := json.Unmarshal(resilienceRaw, &res); err != nil {
			return connect.ConnectionDef{}, wrapErr(Validation, "decode connection resilience (bad config)", err)
		}
	}
	def := connect.ConnectionDef{
		Key:        key,
		Type:       ctype,
		Settings:   settings,
		Resilience: res,
	}
	if secretRef != nil {
		def.SecretRef = *secretRef
	}
	return def, nil
}
