package config

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
)

// object_type value for webhooks in active_pointers / audit_log.
const objWebhook = "webhook"

// WebhookExists reports whether a webhook IDENTITY exists in env (one indexed PK
// read of the webhooks table). It gates per-object idempotent seeding.
func (s *PgStore) WebhookExists(ctx context.Context, env, webhookID string) (bool, error) {
	return s.existsByPK(ctx, env, `SELECT 1 FROM webhooks WHERE id=$1`, webhookID, "check webhook exists")
}

// CreateWebhook creates a new webhook identity and its first version atomically.
// Returns the created version number (always 1 for new webhooks). A duplicate
// id is a Validation error.
func (s *PgStore) CreateWebhook(ctx context.Context, env string, w Webhook) (int, error) {
	pool, err := s.pool(env)
	if err != nil {
		return 0, err
	}
	actor := s.actorFor(ctx)

	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, classifyPg("begin create webhook", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Insert webhook identity.
	_, err = tx.Exec(ctx,
		`INSERT INTO webhooks (id, name, provider, flow_id, env, created_by)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		w.ID, w.Name, w.Provider, w.FlowID, env, actor)
	if err != nil {
		return 0, classifyPg("insert webhook identity", err)
	}

	// Insert first version.
	mappingJSON, err := json.Marshal(w.Mapping)
	if err != nil {
		return 0, wrapErr(Validation, "encode mapping", err)
	}
	filterJSON, err := json.Marshal(w.Filter)
	if err != nil {
		return 0, wrapErr(Validation, "encode filter", err)
	}

	version := 1
	_, err = tx.Exec(ctx,
		`INSERT INTO webhook_versions (webhook_id, version, secret_ref, mapping, filter, created_by)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		w.ID, version, nullIfEmpty(w.SecretRef), mappingJSON, filterJSON, actor)
	if err != nil {
		return 0, classifyPg("insert webhook version", err)
	}

	// Audit log.
	_, err = tx.Exec(ctx,
		`INSERT INTO audit_log (actor, action, object_type, object_id, to_version)
		 VALUES ($1, 'create_version', $2, $3, $4)`,
		actor, objWebhook, w.ID, version)
	if err != nil {
		return 0, classifyPg("audit create webhook", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, classifyPg("commit create webhook", err)
	}
	return version, nil
}

// GetWebhook returns the active webhook configuration for the given id.
// A missing webhook or no active version is NotFound.
func (s *PgStore) GetWebhook(ctx context.Context, env, webhookID string) (Webhook, error) {
	pool, err := s.pool(env)
	if err != nil {
		return Webhook{}, err
	}

	var w Webhook
	var secretRef *string
	var mappingRaw, filterRaw []byte

	err = pool.QueryRow(ctx,
		`SELECT w.id, w.name, w.provider, w.flow_id, w.env, ap.version,
		        wv.secret_ref, wv.mapping, wv.filter
		   FROM active_pointers ap
		   JOIN webhooks w ON w.id = ap.object_id
		   JOIN webhook_versions wv ON wv.webhook_id = ap.object_id AND wv.version = ap.version
		  WHERE ap.object_type=$1 AND ap.object_id=$2`,
		objWebhook, webhookID).Scan(
		&w.ID, &w.Name, &w.Provider, &w.FlowID, &w.Env, &w.Version,
		&secretRef, &mappingRaw, &filterRaw)
	if errors.Is(err, pgx.ErrNoRows) {
		return Webhook{}, newErr(NotFound, "no active webhook "+quote(webhookID))
	}
	if err != nil {
		return Webhook{}, classifyPg("get active webhook", err)
	}

	if secretRef != nil {
		w.SecretRef = *secretRef
	}
	if len(mappingRaw) > 0 {
		if err := json.Unmarshal(mappingRaw, &w.Mapping); err != nil {
			return Webhook{}, wrapErr(Validation, "decode webhook mapping", err)
		}
	}
	if len(filterRaw) > 0 {
		if err := json.Unmarshal(filterRaw, &w.Filter); err != nil {
			return Webhook{}, wrapErr(Validation, "decode webhook filter", err)
		}
	}
	return w, nil
}

// GetActiveWebhook is an alias for GetWebhook, named for clarity in hot-path usage.
// It resolves the currently active version of a webhook by id.
func (s *PgStore) GetActiveWebhook(ctx context.Context, env, webhookID string) (Webhook, error) {
	return s.GetWebhook(ctx, env, webhookID)
}

// GetWebhookVersion returns a specific version of a webhook (not necessarily active).
func (s *PgStore) GetWebhookVersion(ctx context.Context, env, webhookID string, version int) (Webhook, error) {
	pool, err := s.pool(env)
	if err != nil {
		return Webhook{}, err
	}

	var w Webhook
	var secretRef *string
	var mappingRaw, filterRaw []byte

	err = pool.QueryRow(ctx,
		`SELECT w.id, w.name, w.provider, w.flow_id, w.env,
		        wv.version, wv.secret_ref, wv.mapping, wv.filter
		   FROM webhooks w
		   JOIN webhook_versions wv ON wv.webhook_id = w.id
		  WHERE w.id=$1 AND wv.version=$2`,
		webhookID, version).Scan(
		&w.ID, &w.Name, &w.Provider, &w.FlowID, &w.Env,
		&w.Version, &secretRef, &mappingRaw, &filterRaw)
	if errors.Is(err, pgx.ErrNoRows) {
		return Webhook{}, newErr(NotFound, "no such webhook version")
	}
	if err != nil {
		return Webhook{}, classifyPg("get webhook version", err)
	}

	if secretRef != nil {
		w.SecretRef = *secretRef
	}
	if len(mappingRaw) > 0 {
		if err := json.Unmarshal(mappingRaw, &w.Mapping); err != nil {
			return Webhook{}, wrapErr(Validation, "decode webhook mapping", err)
		}
	}
	if len(filterRaw) > 0 {
		if err := json.Unmarshal(filterRaw, &w.Filter); err != nil {
			return Webhook{}, wrapErr(Validation, "decode webhook filter", err)
		}
	}
	return w, nil
}

// ListWebhooks returns all webhooks for env with their active versions (if any).
func (s *PgStore) ListWebhooks(ctx context.Context, env string) ([]WebhookSummary, error) {
	pool, err := s.pool(env)
	if err != nil {
		return nil, err
	}

	rows, err := pool.Query(ctx,
		`SELECT w.id, w.name, w.provider, w.flow_id, w.env, w.created_at, ap.version
		   FROM webhooks w
		   LEFT JOIN active_pointers ap ON ap.object_type=$1 AND ap.object_id = w.id
		  WHERE w.env=$2
		  ORDER BY w.name, w.id`, objWebhook, env)
	if err != nil {
		return nil, classifyPg("list webhooks", err)
	}
	defer rows.Close()

	out := make([]WebhookSummary, 0)
	for rows.Next() {
		var ws WebhookSummary
		if err := rows.Scan(&ws.ID, &ws.Name, &ws.Provider, &ws.FlowID, &ws.Env, &ws.CreatedAt, &ws.ActiveVersion); err != nil {
			return nil, classifyPg("scan webhook", err)
		}
		out = append(out, ws)
	}
	if err := rows.Err(); err != nil {
		return nil, classifyPg("iterate webhooks", err)
	}
	return out, nil
}

// ListWebhookVersions returns version summaries for a single webhook, newest first.
func (s *PgStore) ListWebhookVersions(ctx context.Context, env, webhookID string) ([]WebhookVersionSummary, error) {
	pool, err := s.pool(env)
	if err != nil {
		return nil, err
	}

	// First verify the webhook exists.
	var exists int
	err = pool.QueryRow(ctx, `SELECT 1 FROM webhooks WHERE id=$1`, webhookID).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, newErr(NotFound, "no such webhook")
	}
	if err != nil {
		return nil, classifyPg("check webhook exists", err)
	}

	rows, err := pool.Query(ctx,
		`SELECT version, created_at, created_by
		   FROM webhook_versions WHERE webhook_id=$1
		  ORDER BY version DESC`, webhookID)
	if err != nil {
		return nil, classifyPg("list webhook versions", err)
	}
	defer rows.Close()

	out := make([]WebhookVersionSummary, 0)
	for rows.Next() {
		var vs WebhookVersionSummary
		var createdBy *string
		if err := rows.Scan(&vs.Version, &vs.CreatedAt, &createdBy); err != nil {
			return nil, classifyPg("scan webhook version", err)
		}
		if createdBy != nil {
			vs.CreatedBy = *createdBy
		}
		out = append(out, vs)
	}
	if err := rows.Err(); err != nil {
		return nil, classifyPg("iterate webhook versions", err)
	}
	return out, nil
}

// UpdateWebhook creates a new version of an existing webhook with updated configuration.
// Returns the new version number.
func (s *PgStore) UpdateWebhook(ctx context.Context, env string, w Webhook) (int, error) {
	pool, err := s.pool(env)
	if err != nil {
		return 0, err
	}
	actor := s.actorFor(ctx)

	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, classifyPg("begin update webhook", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Get current max version.
	var maxVersion int
	err = tx.QueryRow(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM webhook_versions WHERE webhook_id=$1`,
		w.ID).Scan(&maxVersion)
	if err != nil {
		return 0, classifyPg("get max webhook version", err)
	}
	if maxVersion == 0 {
		return 0, newErr(NotFound, "webhook not found")
	}

	newVersion := maxVersion + 1

	// Encode mapping and filter.
	mappingJSON, err := json.Marshal(w.Mapping)
	if err != nil {
		return 0, wrapErr(Validation, "encode mapping", err)
	}
	filterJSON, err := json.Marshal(w.Filter)
	if err != nil {
		return 0, wrapErr(Validation, "encode filter", err)
	}

	// Insert new version.
	_, err = tx.Exec(ctx,
		`INSERT INTO webhook_versions (webhook_id, version, secret_ref, mapping, filter, created_by)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		w.ID, newVersion, nullIfEmpty(w.SecretRef), mappingJSON, filterJSON, actor)
	if err != nil {
		return 0, classifyPg("insert webhook version", err)
	}

	// Audit log.
	_, err = tx.Exec(ctx,
		`INSERT INTO audit_log (actor, action, object_type, object_id, from_version, to_version)
		 VALUES ($1, 'create_version', $2, $3, $4, $5)`,
		actor, objWebhook, w.ID, maxVersion, newVersion)
	if err != nil {
		return 0, classifyPg("audit update webhook", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, classifyPg("commit update webhook", err)
	}
	return newVersion, nil
}

// DeleteWebhook removes a webhook and all its versions. This is a hard delete
// (the webhook identity row and all version rows are removed). Use with caution.
func (s *PgStore) DeleteWebhook(ctx context.Context, env, webhookID string) error {
	pool, err := s.pool(env)
	if err != nil {
		return err
	}
	actor := s.actorFor(ctx)

	tx, err := pool.Begin(ctx)
	if err != nil {
		return classifyPg("begin delete webhook", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Check existence and get current active version (if any) for audit.
	var activeVersion *int
	err = tx.QueryRow(ctx,
		`SELECT version FROM active_pointers WHERE object_type=$1 AND object_id=$2`,
		objWebhook, webhookID).Scan(&activeVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		activeVersion = nil
	} else if err != nil {
		return classifyPg("check active pointer", err)
	}

	// Delete in dependency order: active_pointers -> webhook_logs -> webhook_versions -> webhooks.
	_, err = tx.Exec(ctx,
		`DELETE FROM active_pointers WHERE object_type=$1 AND object_id=$2`,
		objWebhook, webhookID)
	if err != nil {
		return classifyPg("delete active pointer", err)
	}

	_, err = tx.Exec(ctx,
		`DELETE FROM webhook_logs WHERE webhook_id=$1`, webhookID)
	if err != nil {
		return classifyPg("delete webhook logs", err)
	}

	_, err = tx.Exec(ctx,
		`DELETE FROM webhook_versions WHERE webhook_id=$1`, webhookID)
	if err != nil {
		return classifyPg("delete webhook versions", err)
	}

	result, err := tx.Exec(ctx,
		`DELETE FROM webhooks WHERE id=$1`, webhookID)
	if err != nil {
		return classifyPg("delete webhook", err)
	}
	if result.RowsAffected() == 0 {
		return newErr(NotFound, "webhook not found")
	}

	// Audit log.
	_, err = tx.Exec(ctx,
		`INSERT INTO audit_log (actor, action, object_type, object_id, from_version)
		 VALUES ($1, 'delete', $2, $3, $4)`,
		actor, objWebhook, webhookID, activeVersion)
	if err != nil {
		return classifyPg("audit delete webhook", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return classifyPg("commit delete webhook", err)
	}
	return nil
}

// SetWebhookActive points the active pointer for a webhook at a specific version,
// enabling publish/rollback. The version must exist.
func (s *PgStore) SetWebhookActive(ctx context.Context, env, webhookID string, version int) error {
	pool, err := s.pool(env)
	if err != nil {
		return err
	}
	actor := s.actorFor(ctx)

	tx, err := pool.Begin(ctx)
	if err != nil {
		return classifyPg("begin set webhook active", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Verify version exists.
	var exists int
	err = tx.QueryRow(ctx,
		`SELECT 1 FROM webhook_versions WHERE webhook_id=$1 AND version=$2`,
		webhookID, version).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return newErr(NotFound, "webhook version not found")
	}
	if err != nil {
		return classifyPg("check webhook version", err)
	}

	// Get current active version for audit.
	var fromVersion *int
	err = tx.QueryRow(ctx,
		`SELECT version FROM active_pointers WHERE object_type=$1 AND object_id=$2`,
		objWebhook, webhookID).Scan(&fromVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		fromVersion = nil
	} else if err != nil {
		return classifyPg("get current active version", err)
	}

	// Upsert active pointer.
	_, err = tx.Exec(ctx,
		`INSERT INTO active_pointers (object_type, object_id, version, updated_by)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (object_type, object_id) DO UPDATE SET version=$3, updated_at=now(), updated_by=$4`,
		objWebhook, webhookID, version, actor)
	if err != nil {
		return classifyPg("set active pointer", err)
	}

	// Audit log.
	action := "publish"
	if fromVersion != nil && *fromVersion > version {
		action = "rollback"
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO audit_log (actor, action, object_type, object_id, from_version, to_version)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		actor, action, objWebhook, webhookID, fromVersion, version)
	if err != nil {
		return classifyPg("audit set webhook active", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return classifyPg("commit set webhook active", err)
	}
	return nil
}

// LogWebhook records a webhook invocation for audit/debug purposes.
func (s *PgStore) LogWebhook(ctx context.Context, env string, log WebhookLog) (int64, error) {
	pool, err := s.pool(env)
	if err != nil {
		return 0, err
	}

	headersJSON, err := json.Marshal(log.RequestHeaders)
	if err != nil {
		return 0, wrapErr(Validation, "encode request headers", err)
	}

	var id int64
	err = pool.QueryRow(ctx,
		`INSERT INTO webhook_logs (webhook_id, provider, event_type, flow_triggered, status, request_headers, payload_hash, error)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 RETURNING id`,
		log.WebhookID, log.Provider, nullIfEmpty(log.EventType), nullIfEmpty(log.FlowTriggered),
		log.Status, headersJSON, nullIfEmpty(log.PayloadHash), nullIfEmpty(log.Error)).Scan(&id)
	if err != nil {
		return 0, classifyPg("insert webhook log", err)
	}
	return id, nil
}

// GetWebhookLogs returns webhook invocation logs for a webhook, newest first.
// Limit defaults to 100 if <= 0.
func (s *PgStore) GetWebhookLogs(ctx context.Context, env, webhookID string, limit int) ([]WebhookLog, error) {
	pool, err := s.pool(env)
	if err != nil {
		return nil, err
	}

	if limit <= 0 {
		limit = 100
	}

	rows, err := pool.Query(ctx,
		`SELECT id, webhook_id, at, provider, coalesce(event_type,''), coalesce(flow_triggered,''),
		        status, request_headers, coalesce(payload_hash,''), coalesce(error,'')
		   FROM webhook_logs
		  WHERE webhook_id=$1
		  ORDER BY at DESC
		  LIMIT $2`, webhookID, limit)
	if err != nil {
		return nil, classifyPg("list webhook logs", err)
	}
	defer rows.Close()

	out := make([]WebhookLog, 0)
	for rows.Next() {
		var log WebhookLog
		var headersRaw []byte
		if err := rows.Scan(&log.ID, &log.WebhookID, &log.At, &log.Provider, &log.EventType,
			&log.FlowTriggered, &log.Status, &headersRaw, &log.PayloadHash, &log.Error); err != nil {
			return nil, classifyPg("scan webhook log", err)
		}
		if len(headersRaw) > 0 {
			if err := json.Unmarshal(headersRaw, &log.RequestHeaders); err != nil {
				return nil, wrapErr(Validation, "decode request headers", err)
			}
		}
		out = append(out, log)
	}
	if err := rows.Err(); err != nil {
		return nil, classifyPg("iterate webhook logs", err)
	}
	return out, nil
}

// nullIfEmpty returns nil if s is empty, otherwise a pointer to s.
// Used for optional text columns in Postgres.
func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
