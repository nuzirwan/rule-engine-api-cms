package config

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// object_type and audit actions for groups.
const (
	objGroup = "group"

	actGroupCreate = "INSERT"
	actGroupUpdate = "UPDATE"
	actGroupDelete = "DELETE"
)

// defaultGroupID is the reserved group ID that cannot be deleted.
const defaultGroupID = "default"

// ErrCannotDeleteDefault is the sentinel for attempting to delete the default group.
var ErrCannotDeleteDefault = errors.New("cannot delete default group")

// ErrGroupHasFlows is the sentinel for deleting a group that still has flows assigned.
var ErrGroupHasFlows = errors.New("group has flows assigned")

// GetGroup retrieves a group by ID, joined with its connections. Returns
// NotFound if the group does not exist.
func (s *PgStore) GetGroup(ctx context.Context, env, groupID string) (Group, error) {
	pool, err := s.pool(env)
	if err != nil {
		return Group{}, err
	}

	// Query group row.
	var g Group
	var scalingMode string
	var scaleDownDelaySec, startupTimeoutSec int
	var cpuReq, cpuLim, memReq, memLim *string
	err = pool.QueryRow(ctx,
		`SELECT id, name, description, version, scaling_mode, min_replicas, max_replicas,
		        scale_down_delay_seconds, startup_timeout_seconds,
		        cpu_request, cpu_limit, memory_request, memory_limit,
		        enabled, created_at, updated_at
		   FROM groups WHERE id=$1`, groupID).Scan(
		&g.ID, &g.Name, &g.Description, &g.Version, &scalingMode,
		&g.Scaling.MinReplicas, &g.Scaling.MaxReplicas,
		&scaleDownDelaySec, &startupTimeoutSec,
		&cpuReq, &cpuLim, &memReq, &memLim,
		&g.Enabled, &g.CreatedAt, &g.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Group{}, newErr(NotFound, "group not found: "+groupID)
	}
	if err != nil {
		return Group{}, classifyPg("get group", err)
	}

	// Map DB fields to struct.
	g.Scaling.Mode = ScalingMode(scalingMode)
	g.Scaling.ScaleDownDelay = formatSeconds(scaleDownDelaySec)
	g.Scaling.StartupTimeout = formatSeconds(startupTimeoutSec)
	if cpuReq != nil || cpuLim != nil || memReq != nil || memLim != nil {
		g.Scaling.Resources = &Resources{}
		if cpuReq != nil {
			g.Scaling.Resources.CPURequest = *cpuReq
		}
		if cpuLim != nil {
			g.Scaling.Resources.CPULimit = *cpuLim
		}
		if memReq != nil {
			g.Scaling.Resources.MemoryRequest = *memReq
		}
		if memLim != nil {
			g.Scaling.Resources.MemoryLimit = *memLim
		}
	}

	// Query connections.
	rows, err := pool.Query(ctx,
		`SELECT connection_key FROM group_connections WHERE group_id=$1 ORDER BY connection_key`, groupID)
	if err != nil {
		return Group{}, classifyPg("get group connections", err)
	}
	defer rows.Close()

	var conns []string
	for rows.Next() {
		var ck string
		if err := rows.Scan(&ck); err != nil {
			return Group{}, classifyPg("scan group connection", err)
		}
		conns = append(conns, ck)
	}
	if err := rows.Err(); err != nil {
		return Group{}, classifyPg("iterate group connections", err)
	}
	g.Connections = conns

	return g, nil
}

// GetGroupVersion returns only the version column for a group (lightweight query
// for hot-reload polling). Returns NotFound if the group does not exist.
func (s *PgStore) GetGroupVersion(ctx context.Context, env, groupID string) (int, error) {
	pool, err := s.pool(env)
	if err != nil {
		return 0, err
	}

	var version int
	err = pool.QueryRow(ctx,
		`SELECT version FROM groups WHERE id=$1`, groupID).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, newErr(NotFound, "group not found: "+groupID)
	}
	if err != nil {
		return 0, classifyPg("get group version", err)
	}
	return version, nil
}

// ListGroups returns all groups as summaries, ordered by name.
func (s *PgStore) ListGroups(ctx context.Context, env string) ([]GroupSummary, error) {
	pool, err := s.pool(env)
	if err != nil {
		return nil, err
	}

	rows, err := pool.Query(ctx,
		`SELECT id, name, enabled, version, updated_at
		   FROM groups ORDER BY name`)
	if err != nil {
		return nil, classifyPg("list groups", err)
	}
	defer rows.Close()

	var out []GroupSummary
	for rows.Next() {
		var gs GroupSummary
		if err := rows.Scan(&gs.ID, &gs.Name, &gs.Enabled, &gs.Version, &gs.UpdatedAt); err != nil {
			return nil, classifyPg("scan group summary", err)
		}
		out = append(out, gs)
	}
	if err := rows.Err(); err != nil {
		return nil, classifyPg("iterate groups", err)
	}
	return out, nil
}

// UpsertGroup creates or updates a group. It validates the group first, captures
// pre-mutation state BEFORE update (critical for rollback), inserts or updates
// the groups row with version=version+1, replaces connections via
// replaceGroupConnections, and records audit in group_audit. changedBy and reason
// are audit metadata.
func (s *PgStore) UpsertGroup(ctx context.Context, env string, group *Group, changedBy, reason string) error {
	// Validate first — fail fast before touching the store.
	if err := ValidateGroup(group); err != nil {
		return err
	}

	pool, err := s.pool(env)
	if err != nil {
		return err
	}

	return withTx(ctx, pool, func(tx pgx.Tx) error {
		// Check if group exists.
		var existingID string
		err := tx.QueryRow(ctx, `SELECT id FROM groups WHERE id=$1 FOR UPDATE`, group.ID).Scan(&existingID)
		exists := !errors.Is(err, pgx.ErrNoRows)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return classifyPg("lock group", err)
		}

		// Capture pre-mutation state BEFORE update (critical for rollback).
		var oldData json.RawMessage
		if exists {
			oldData, err = s.getGroupAuditData(ctx, tx, group.ID)
			if err != nil {
				return err
			}
		}

		// Parse durations to seconds.
		scaleDownDelaySec := parseSeconds(group.Scaling.ScaleDownDelay, 300)
		startupTimeoutSec := parseSeconds(group.Scaling.StartupTimeout, 30)

		// Map Resources to nullable strings.
		var cpuReq, cpuLim, memReq, memLim *string
		if group.Scaling.Resources != nil {
			if group.Scaling.Resources.CPURequest != "" {
				cpuReq = &group.Scaling.Resources.CPURequest
			}
			if group.Scaling.Resources.CPULimit != "" {
				cpuLim = &group.Scaling.Resources.CPULimit
			}
			if group.Scaling.Resources.MemoryRequest != "" {
				memReq = &group.Scaling.Resources.MemoryRequest
			}
			if group.Scaling.Resources.MemoryLimit != "" {
				memLim = &group.Scaling.Resources.MemoryLimit
			}
		}

		var operation string
		if exists {
			operation = actGroupUpdate
			// UPDATE with version bump.
			_, err := tx.Exec(ctx,
				`UPDATE groups SET
					name=$2, description=$3, version=version+1,
					scaling_mode=$4, min_replicas=$5, max_replicas=$6,
					scale_down_delay_seconds=$7, startup_timeout_seconds=$8,
					cpu_request=$9, cpu_limit=$10, memory_request=$11, memory_limit=$12,
					enabled=$13, updated_at=NOW()
				 WHERE id=$1`,
				group.ID, group.Name, group.Description,
				string(group.Scaling.Mode), group.Scaling.MinReplicas, group.Scaling.MaxReplicas,
				scaleDownDelaySec, startupTimeoutSec,
				cpuReq, cpuLim, memReq, memLim,
				group.Enabled)
			if err != nil {
				return classifyPg("update group", err)
			}
		} else {
			operation = actGroupCreate
			// INSERT with version=1.
			_, err := tx.Exec(ctx,
				`INSERT INTO groups (
					id, name, description, version,
					scaling_mode, min_replicas, max_replicas,
					scale_down_delay_seconds, startup_timeout_seconds,
					cpu_request, cpu_limit, memory_request, memory_limit,
					enabled, created_at, updated_at
				 ) VALUES ($1,$2,$3,1,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,NOW(),NOW())`,
				group.ID, group.Name, group.Description,
				string(group.Scaling.Mode), group.Scaling.MinReplicas, group.Scaling.MaxReplicas,
				scaleDownDelaySec, startupTimeoutSec,
				cpuReq, cpuLim, memReq, memLim,
				group.Enabled)
			if err != nil {
				return classifyPg("insert group", err)
			}
		}

		// Replace connections (this also bumps version if connections changed).
		if err := s.replaceGroupConnections(ctx, tx, group.ID, group.Connections, changedBy, reason); err != nil {
			return err
		}

		// Capture post-mutation state for audit.
		newData, err := s.getGroupAuditData(ctx, tx, group.ID)
		if err != nil {
			return err
		}

		// Record audit.
		_, err = tx.Exec(ctx,
			`INSERT INTO group_audit (group_id, operation, old_data, new_data, changed_by, reason)
			 VALUES ($1, $2, $3, $4, $5, $6)`,
			group.ID, operation, oldData, newData, changedBy, reason)
		if err != nil {
			return classifyPg("audit group upsert", err)
		}

		return nil
	})
}

// DeleteGroup deletes a group after validation. It rejects deletion of the
// 'default' group (403) and rejects deletion when flows are assigned (409).
// Captures pre-mutation state, deletes from groups (CASCADE deletes
// group_connections), and records audit.
func (s *PgStore) DeleteGroup(ctx context.Context, env, groupID, changedBy, reason string) error {
	// Cannot delete the default group.
	if groupID == defaultGroupID {
		return wrapErr(Validation, "cannot delete default group", ErrCannotDeleteDefault)
	}

	pool, err := s.pool(env)
	if err != nil {
		return err
	}

	return withTx(ctx, pool, func(tx pgx.Tx) error {
		// Lock the group row.
		var existingID string
		err := tx.QueryRow(ctx, `SELECT id FROM groups WHERE id=$1 FOR UPDATE`, groupID).Scan(&existingID)
		if errors.Is(err, pgx.ErrNoRows) {
			return newErr(NotFound, "group not found: "+groupID)
		}
		if err != nil {
			return classifyPg("lock group for delete", err)
		}

		// Check if any flows are assigned to this group.
		flowCount, err := s.countFlowsByGroup(ctx, tx, groupID)
		if err != nil {
			return err
		}
		if flowCount > 0 {
			return wrapErr(Validation, "group has flows assigned", ErrGroupHasFlows)
		}

		// Capture pre-mutation state BEFORE delete.
		oldData, err := s.getGroupAuditData(ctx, tx, groupID)
		if err != nil {
			return err
		}

		// DELETE from groups — CASCADE deletes group_connections.
		tag, err := tx.Exec(ctx, `DELETE FROM groups WHERE id=$1`, groupID)
		if err != nil {
			return classifyPg("delete group", err)
		}
		if tag.RowsAffected() == 0 {
			return newErr(NotFound, "group not found: "+groupID)
		}

		// Record audit (new_data is NULL for DELETE).
		_, err = tx.Exec(ctx,
			`INSERT INTO group_audit (group_id, operation, old_data, new_data, changed_by, reason)
			 VALUES ($1, $2, $3, NULL, $4, $5)`,
			groupID, actGroupDelete, oldData, changedBy, reason)
		if err != nil {
			return classifyPg("audit group delete", err)
		}

		return nil
	})
}

// replaceGroupConnections replaces the connections for a group within an existing
// transaction. It gets the current connections, skips if unchanged, deletes all
// existing, inserts new connections, increments groups.version (critical for
// hot-reload detection of connection-only changes), and records in
// group_connections_audit.
func (s *PgStore) replaceGroupConnections(ctx context.Context, tx pgx.Tx, groupID string, connections []string, changedBy, reason string) error {
	// Get current connections.
	rows, err := tx.Query(ctx,
		`SELECT connection_key FROM group_connections WHERE group_id=$1 ORDER BY connection_key`, groupID)
	if err != nil {
		return classifyPg("get current connections", err)
	}

	var oldConns []string
	for rows.Next() {
		var ck string
		if err := rows.Scan(&ck); err != nil {
			rows.Close()
			return classifyPg("scan current connection", err)
		}
		oldConns = append(oldConns, ck)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return classifyPg("iterate current connections", err)
	}

	// Check if unchanged — skip if same (order-independent comparison).
	if connectionsEqual(oldConns, connections) {
		return nil
	}

	// Delete all existing connections for this group.
	if _, err := tx.Exec(ctx, `DELETE FROM group_connections WHERE group_id=$1`, groupID); err != nil {
		return classifyPg("delete old connections", err)
	}

	// Insert new connections.
	for _, ck := range connections {
		if _, err := tx.Exec(ctx,
			`INSERT INTO group_connections (group_id, connection_key, created_at)
			 VALUES ($1, $2, NOW())`, groupID, ck); err != nil {
			return classifyPg("insert connection", err)
		}
	}

	// Increment groups.version (critical for hot-reload detection).
	if _, err := tx.Exec(ctx,
		`UPDATE groups SET version=version+1, updated_at=NOW() WHERE id=$1`, groupID); err != nil {
		return classifyPg("bump group version for connection change", err)
	}

	// Record audit for BULK_REPLACE.
	_, err = tx.Exec(ctx,
		`INSERT INTO group_connections_audit
		   (group_id, operation, old_connections, new_connections, changed_by, reason)
		 VALUES ($1, 'BULK_REPLACE', $2, $3, $4, $5)`,
		groupID, oldConns, connections, changedBy, reason)
	if err != nil {
		return classifyPg("audit connection replace", err)
	}

	return nil
}

// getGroupAuditData fetches the group + connections and marshals as
// GroupAuditData JSON for audit storage.
func (s *PgStore) getGroupAuditData(ctx context.Context, tx pgx.Tx, groupID string) (json.RawMessage, error) {
	// Query group row.
	var g Group
	var scalingMode string
	var scaleDownDelaySec, startupTimeoutSec int
	var cpuReq, cpuLim, memReq, memLim *string
	err := tx.QueryRow(ctx,
		`SELECT id, name, description, version, scaling_mode, min_replicas, max_replicas,
		        scale_down_delay_seconds, startup_timeout_seconds,
		        cpu_request, cpu_limit, memory_request, memory_limit,
		        enabled, created_at, updated_at
		   FROM groups WHERE id=$1`, groupID).Scan(
		&g.ID, &g.Name, &g.Description, &g.Version, &scalingMode,
		&g.Scaling.MinReplicas, &g.Scaling.MaxReplicas,
		&scaleDownDelaySec, &startupTimeoutSec,
		&cpuReq, &cpuLim, &memReq, &memLim,
		&g.Enabled, &g.CreatedAt, &g.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, newErr(NotFound, "group not found for audit: "+groupID)
	}
	if err != nil {
		return nil, classifyPg("get group for audit", err)
	}

	// Map DB fields to struct.
	g.Scaling.Mode = ScalingMode(scalingMode)
	g.Scaling.ScaleDownDelay = formatSeconds(scaleDownDelaySec)
	g.Scaling.StartupTimeout = formatSeconds(startupTimeoutSec)
	if cpuReq != nil || cpuLim != nil || memReq != nil || memLim != nil {
		g.Scaling.Resources = &Resources{}
		if cpuReq != nil {
			g.Scaling.Resources.CPURequest = *cpuReq
		}
		if cpuLim != nil {
			g.Scaling.Resources.CPULimit = *cpuLim
		}
		if memReq != nil {
			g.Scaling.Resources.MemoryRequest = *memReq
		}
		if memLim != nil {
			g.Scaling.Resources.MemoryLimit = *memLim
		}
	}

	// Query connections.
	rows, err := tx.Query(ctx,
		`SELECT connection_key FROM group_connections WHERE group_id=$1 ORDER BY connection_key`, groupID)
	if err != nil {
		return nil, classifyPg("get group connections for audit", err)
	}
	defer rows.Close()

	var conns []string
	for rows.Next() {
		var ck string
		if err := rows.Scan(&ck); err != nil {
			return nil, classifyPg("scan group connection for audit", err)
		}
		conns = append(conns, ck)
	}
	if err := rows.Err(); err != nil {
		return nil, classifyPg("iterate group connections for audit", err)
	}
	g.Connections = conns

	// Build audit data.
	ad := GroupAuditData{
		Group:       &g,
		Connections: conns,
	}
	raw, err := json.Marshal(ad)
	if err != nil {
		return nil, wrapErr(Validation, "marshal group audit data", err)
	}
	return raw, nil
}

// countFlowsByGroup counts the number of flow_versions rows assigned to the
// given group. Used to reject DeleteGroup when flows are assigned.
func (s *PgStore) countFlowsByGroup(ctx context.Context, tx pgx.Tx, groupID string) (int, error) {
	var count int
	err := tx.QueryRow(ctx,
		`SELECT COUNT(*) FROM flow_versions WHERE group_id=$1`, groupID).Scan(&count)
	if err != nil {
		return 0, classifyPg("count flows by group", err)
	}
	return count, nil
}

// CountFlowsByGroup is the exported version for handlers. It counts how many
// flow_versions rows are assigned to a group.
func (s *PgStore) CountFlowsByGroup(ctx context.Context, env, groupID string) (int, error) {
	pool, err := s.pool(env)
	if err != nil {
		return 0, err
	}
	var count int
	err = pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM flow_versions WHERE group_id=$1`, groupID).Scan(&count)
	if err != nil {
		return 0, classifyPg("count flows by group", err)
	}
	return count, nil
}

// ---- helpers ----

// formatSeconds converts seconds to a duration string like "5m" or "30s".
func formatSeconds(sec int) string {
	d := time.Duration(sec) * time.Second
	if d == 0 {
		return ""
	}
	if d >= time.Minute && d%time.Minute == 0 {
		return d.String()
	}
	return d.String()
}

// parseSeconds parses a duration string like "5m" or "300s" to seconds, or
// returns defaultVal on empty or parse error.
func parseSeconds(s string, defaultVal int) int {
	if s == "" {
		return defaultVal
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return defaultVal
	}
	return int(d.Seconds())
}

// connectionsEqual checks if two connection slices are equal (order-independent).
func connectionsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	aSet := make(map[string]struct{}, len(a))
	for _, v := range a {
		aSet[v] = struct{}{}
	}
	for _, v := range b {
		if _, ok := aSet[v]; !ok {
			return false
		}
	}
	return true
}
