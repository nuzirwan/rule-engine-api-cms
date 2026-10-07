package config

import (
	"context"
	"encoding/json"

	"nzr-rules-engine/internal/connect"
	"nzr-rules-engine/internal/flow"
)

// This file holds store methods consumed by the worker binary (Phase 2). These
// read flows and connections filtered by group for isolated execution.

// jdmRefSpec is used to extract JDMID from node specs during JDM collection.
// Multiple spec types (DecisionSpec, ConditionSpec, SwitchSpec, FilterSpec, etc.)
// all have a jdmId field, so we parse just that field.
type jdmRefSpec struct {
	JDMID string `json:"jdmId"`
}

// GetActiveFlowsForGroup returns all active flows assigned to a group. It joins
// active_pointers → flow_versions (filtered by group_id) → flows, decodes trees,
// and returns full FlowVersion structs. Returns an empty slice if the group has
// no active flows.
func (s *PgStore) GetActiveFlowsForGroup(ctx context.Context, env, groupID string) ([]FlowVersion, error) {
	pool, err := s.pool(env)
	if err != nil {
		return nil, err
	}

	// Query all active flows for this group. The join ensures we only get flows
	// that are both active AND assigned to the group.
	rows, err := pool.Query(ctx, `
		SELECT fv.flow_id, fv.version, fv.tree, f.method, f.path
		  FROM active_pointers ap
		  JOIN flow_versions fv ON fv.flow_id = ap.object_id AND fv.version = ap.version
		  JOIN flows f ON f.id = fv.flow_id
		 WHERE ap.object_type = $1
		   AND fv.group_id = $2
		 ORDER BY fv.flow_id`, objFlow, groupID)
	if err != nil {
		return nil, classifyPg("get active flows for group", err)
	}
	defer rows.Close()

	var out []FlowVersion
	for rows.Next() {
		var flowID string
		var version int
		var treeRaw []byte
		var method, path string

		if err := rows.Scan(&flowID, &version, &treeRaw, &method, &path); err != nil {
			return nil, classifyPg("scan flow for group", err)
		}

		tree, err := decodeTree(treeRaw)
		if err != nil {
			return nil, wrapErr(Validation, "decode tree for flow "+flowID, err)
		}

		out = append(out, FlowVersion{
			FlowID:  flowID,
			Version: version,
			Method:  method,
			Path:    path,
			Group:   groupID,
			Tree:    tree,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, classifyPg("iterate flows for group", err)
	}

	return out, nil
}

// GetConnectionsForGroup returns the connection definitions for a specific set of
// keys. The worker passes the keys from group.Connections and receives only those
// defs. Returns NotFound for any missing key.
func (s *PgStore) GetConnectionsForGroup(ctx context.Context, env string, keys []string) ([]connect.ConnectionDef, error) {
	if len(keys) == 0 {
		return nil, nil
	}

	pool, err := s.pool(env)
	if err != nil {
		return nil, err
	}

	// Query active connection defs for the given keys. We use a subquery with
	// active_pointers to get only active versions.
	rows, err := pool.Query(ctx, `
		SELECT cv.key, cv.type, cv.settings, cv.secret_ref, cv.resilience
		  FROM connection_versions cv
		  JOIN active_pointers ap ON ap.object_type = $1 
		       AND ap.object_id = cv.key 
		       AND ap.version = cv.version
		 WHERE cv.key = ANY($2)
		 ORDER BY cv.key`, objConnection, keys)
	if err != nil {
		return nil, classifyPg("get connections for group", err)
	}
	defer rows.Close()

	out := make([]connect.ConnectionDef, 0, len(keys))
	foundKeys := make(map[string]bool, len(keys))

	for rows.Next() {
		var def connect.ConnectionDef
		var settingsRaw, resilienceRaw []byte

		if err := rows.Scan(&def.Key, &def.Type, &settingsRaw, &def.SecretRef, &resilienceRaw); err != nil {
			return nil, classifyPg("scan connection for group", err)
		}

		if len(settingsRaw) > 0 {
			if err := json.Unmarshal(settingsRaw, &def.Settings); err != nil {
				return nil, wrapErr(Validation, "decode settings for connection "+def.Key, err)
			}
		}
		if len(resilienceRaw) > 0 {
			if err := json.Unmarshal(resilienceRaw, &def.Resilience); err != nil {
				return nil, wrapErr(Validation, "decode resilience for connection "+def.Key, err)
			}
		}

		out = append(out, def)
		foundKeys[def.Key] = true
	}
	if err := rows.Err(); err != nil {
		return nil, classifyPg("iterate connections for group", err)
	}

	// Check if any requested keys are missing.
	for _, key := range keys {
		if !foundKeys[key] {
			return nil, newErr(NotFound, "connection not found: "+key)
		}
	}

	return out, nil
}

// GetJDMsForFlows returns JDM bytes for all JDM IDs referenced by the given flows.
// It scans decision nodes in the flow trees, collects unique JDM IDs, and returns
// their bytes keyed by ID. The caller (worker) uses this to preload all JDMs at
// startup rather than loading lazily per request.
func (s *PgStore) GetJDMsForFlows(ctx context.Context, env string, flows []FlowVersion) (map[string][]byte, error) {
	// Collect unique JDM IDs from all flow trees.
	jdmIDs := make(map[string]struct{})
	for _, fv := range flows {
		collectJDMIDs(&fv.Tree, jdmIDs)
	}

	if len(jdmIDs) == 0 {
		return nil, nil
	}

	// Convert to slice for query.
	ids := make([]string, 0, len(jdmIDs))
	for id := range jdmIDs {
		ids = append(ids, id)
	}

	pool, err := s.pool(env)
	if err != nil {
		return nil, err
	}

	// Query active JDM versions.
	rows, err := pool.Query(ctx, `
		SELECT jv.jdm_id, jv.content
		  FROM jdm_versions jv
		  JOIN active_pointers ap ON ap.object_type = $1 
		       AND ap.object_id = jv.jdm_id 
		       AND ap.version = jv.version
		 WHERE jv.jdm_id = ANY($2)`, objJDM, ids)
	if err != nil {
		return nil, classifyPg("get jdms for flows", err)
	}
	defer rows.Close()

	out := make(map[string][]byte, len(ids))
	for rows.Next() {
		var id string
		var content []byte
		if err := rows.Scan(&id, &content); err != nil {
			return nil, classifyPg("scan jdm", err)
		}
		out[id] = content
	}
	if err := rows.Err(); err != nil {
		return nil, classifyPg("iterate jdms", err)
	}

	// Check if any JDM is missing (validation error, not runtime).
	for id := range jdmIDs {
		if _, ok := out[id]; !ok {
			return nil, newErr(NotFound, "jdm not found: "+id)
		}
	}

	return out, nil
}

// collectJDMIDs recursively walks a flow tree and collects JDM IDs from nodes
// that reference JDMs (decision, condition, switch, filter, find, map, reduce).
// The JDM ID is stored in the node's Spec JSON under "jdmId".
func collectJDMIDs(node *flow.Node, ids map[string]struct{}) {
	if node == nil {
		return
	}

	// Check if this node type might reference a JDM.
	switch node.Type {
	case flow.TypeDecision, flow.TypeCondition, flow.TypeSwitch,
		flow.TypeFilter, flow.TypeFind, flow.TypeMap, flow.TypeReduce:
		// Parse the spec to extract jdmId.
		if len(node.Spec) > 0 {
			var spec jdmRefSpec
			if err := json.Unmarshal(node.Spec, &spec); err == nil && spec.JDMID != "" {
				ids[spec.JDMID] = struct{}{}
			}
		}
	}

	// Recurse into children.
	for i := range node.Children {
		collectJDMIDs(&node.Children[i], ids)
	}
}

// WorkerStore is the interface for worker-specific store methods. It extends
// Store with methods needed for worker group loading and reload detection.
type WorkerStore interface {
	// GetGroup retrieves a group by ID with its connections.
	GetGroup(ctx context.Context, env, groupID string) (Group, error)
	// GetGroupVersion returns only the version column for hot-reload polling.
	GetGroupVersion(ctx context.Context, env, groupID string) (int, error)
	// GetActiveFlowsForGroup returns all active flows assigned to a group.
	GetActiveFlowsForGroup(ctx context.Context, env, groupID string) ([]FlowVersion, error)
	// GetConnectionsForGroup returns connection defs for the specified keys.
	GetConnectionsForGroup(ctx context.Context, env string, keys []string) ([]connect.ConnectionDef, error)
	// GetJDMsForFlows returns JDM bytes for all JDMs referenced by the flows.
	GetJDMsForFlows(ctx context.Context, env string, flows []FlowVersion) (map[string][]byte, error)
	// GetJDM returns a single JDM's bytes and version (for lazy loading).
	GetJDM(ctx context.Context, env, id string) ([]byte, int, error)
	// Ping reports store reachability.
	Ping(ctx context.Context) error
}

// compile-time assertion that *PgStore satisfies WorkerStore.
var _ WorkerStore = (*PgStore)(nil)
