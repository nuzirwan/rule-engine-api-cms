package config

import (
	"context"
	"errors"

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
	err = pool.QueryRow(ctx,
		`SELECT fv.tree, f.method, f.path
		   FROM flow_versions fv JOIN flows f ON f.id = fv.flow_id
		  WHERE fv.flow_id=$1 AND fv.version=$2`, flowID, version).Scan(&treeRaw, &method, &path)
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

	return FlowVersion{
		FlowID:   flowID,
		Version:  version,
		Method:   method,
		Path:     path,
		Tree:     tree,
		Fixtures: fixtures,
	}, nil
}
