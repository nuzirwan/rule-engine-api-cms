package config

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/singleflight"

	"nzr-rules-engine/internal/connect"
)

// object_type values in active_pointers / audit_log.
const (
	objFlow       = "flow"
	objJDM        = "jdm"
	objConnection = "connection"
)

// audit actions.
const (
	actCreateVersion = "create_version"
	actPublish       = "publish"
	actRollback      = "rollback"
	actPromote       = "promote"
)

// PgStore is the real Postgres-backed config.Store (Slice D §3). It is a thin
// adapter over pgxpool holding one pool per env (the engine process serves one
// env; a Strapi-side writer may hold several). All queries are parameterized
// ([[data-and-migrations]]). An optional Cache fronts the hot resolve path
// (cache-aside); a nil cache means every read hits Postgres directly — the store
// is correct cache-down ([[caching-strategy]]).
type PgStore struct {
	pools map[string]*pgxpool.Pool // env -> pool
	cache Cache                    // may be nil (store works cache-down)
	actor string                   // audit actor for store-originated writes
	ttl   time.Duration            // cache TTL for Set (0 => cache default/jitter)
	sf    singleflight.Group       // collapses concurrent misses on the hot key
}

// compile-time assertion that *PgStore satisfies the frozen Store seam.
var _ Store = (*PgStore)(nil)

// PgOption configures a PgStore.
type PgOption func(*PgStore)

// WithCache fronts the store with a Cache (cache-aside on the resolve path).
func WithCache(c Cache) PgOption { return func(s *PgStore) { s.cache = c } }

// WithActor sets the audit actor recorded for store-originated writes (defaults
// to "engine").
func WithActor(a string) PgOption { return func(s *PgStore) { s.actor = a } }

// WithCacheTTL sets the TTL passed to Cache.Set (0 lets the cache choose, which
// applies its jittered default).
func WithCacheTTL(d time.Duration) PgOption { return func(s *PgStore) { s.ttl = d } }

// NewPgStore builds a store over a map of env -> pool. At least one pool is
// required; an empty map is a Validation (fail-fast) error.
func NewPgStore(pools map[string]*pgxpool.Pool, opts ...PgOption) (*PgStore, error) {
	if len(pools) == 0 {
		return nil, newErr(Validation, "pgstore needs at least one env pool")
	}
	s := &PgStore{pools: pools, actor: "engine"}
	for _, o := range opts {
		o(s)
	}
	return s, nil
}

// pool returns the pool for env or a NotFound error (env not served here).
func (s *PgStore) pool(env string) (*pgxpool.Pool, error) {
	p, ok := s.pools[env]
	if !ok {
		return nil, newErr(NotFound, "no config pool for env "+quote(env))
	}
	return p, nil
}

// ActiveFlow resolves the active flow version for (env, method, path) on the hot
// path: cache-aside with single-flight on miss, then the indexed resolve chain in
// Postgres. A missing route or missing pointer is NotFound; a malformed stored
// tree is Validation (never a 500 — AC-15).
func (s *PgStore) ActiveFlow(ctx context.Context, env, method, path string) (FlowVersion, error) {
	key := keyFlow(env, method, path)

	// 1. cache-aside read.
	if s.cache != nil {
		if b, ok, err := s.cache.Get(ctx, key); err == nil && ok {
			return decodeFlow(b) // defensive: bad cached bytes => Validation, not 500
		}
	}

	// 2. miss: resolve from the store, collapsing concurrent misses on this key
	//    so a hot route does not stampede Postgres ([[caching-strategy]]).
	v, err, _ := s.sf.Do(key, func() (any, error) {
		return s.resolveFlowFromDB(ctx, env, method, path)
	})
	if err != nil {
		return FlowVersion{}, err
	}
	fv := v.(FlowVersion)

	// 3. populate the cache (best-effort; a Set failure never fails the request).
	if s.cache != nil {
		if b, encErr := encodeFlow(fv); encErr == nil {
			_ = s.cache.Set(ctx, key, b, s.ttl)
		}
	}
	return fv, nil
}

// resolveFlowFromDB runs the indexed resolve chain: route -> pointer -> version
// body -> fixtures. It is the cache-miss path.
func (s *PgStore) resolveFlowFromDB(ctx context.Context, env, method, path string) (FlowVersion, error) {
	pool, err := s.pool(env)
	if err != nil {
		return FlowVersion{}, err
	}

	var flowID string
	err = pool.QueryRow(ctx,
		`SELECT id FROM flows WHERE method=$1 AND path=$2`, method, path).Scan(&flowID)
	if errors.Is(err, pgx.ErrNoRows) {
		return FlowVersion{}, newErr(NotFound, "no flow for route "+method+" "+path)
	}
	if err != nil {
		return FlowVersion{}, classifyPg("resolve route", err)
	}

	var version int
	err = pool.QueryRow(ctx,
		`SELECT version FROM active_pointers WHERE object_type=$1 AND object_id=$2`,
		objFlow, flowID).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return FlowVersion{}, newErr(NotFound, "no active version for flow "+flowID)
	}
	if err != nil {
		return FlowVersion{}, classifyPg("resolve pointer", err)
	}

	var treeRaw []byte
	var method2, path2 string
	err = pool.QueryRow(ctx,
		`SELECT fv.tree, f.method, f.path
		   FROM flow_versions fv JOIN flows f ON f.id = fv.flow_id
		  WHERE fv.flow_id=$1 AND fv.version=$2`, flowID, version).Scan(&treeRaw, &method2, &path2)
	if errors.Is(err, pgx.ErrNoRows) {
		return FlowVersion{}, newErr(NotFound, "flow version body missing for "+flowID)
	}
	if err != nil {
		return FlowVersion{}, classifyPg("resolve version body", err)
	}

	tree, err := decodeTree(treeRaw) // AC-15: malformed tree => Validation, no 500
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
		Method:   method2,
		Path:     path2,
		Tree:     tree,
		Fixtures: fixtures,
	}, nil
}

// loadFixtures reads the fixtures that travel with a flow version.
func (s *PgStore) loadFixtures(ctx context.Context, pool *pgxpool.Pool, flowID string, version int) ([]FlowFixture, error) {
	rows, err := pool.Query(ctx,
		`SELECT name, body FROM flow_fixtures WHERE flow_id=$1 AND version=$2 ORDER BY name`,
		flowID, version)
	if err != nil {
		return nil, classifyPg("load fixtures", err)
	}
	defer rows.Close()

	var out []FlowFixture
	for rows.Next() {
		var name string
		var body []byte
		if err := rows.Scan(&name, &body); err != nil {
			return nil, classifyPg("scan fixture", err)
		}
		var fx FlowFixture
		if err := json.Unmarshal(body, &fx); err != nil {
			return nil, wrapErr(Validation, "decode fixture (bad config)", err)
		}
		fx.Name = name
		out = append(out, fx)
	}
	if err := rows.Err(); err != nil {
		return nil, classifyPg("iterate fixtures", err)
	}
	return out, nil
}

// GetJDM returns the active JDM bytes + version for (env, id), cache-aside.
func (s *PgStore) GetJDM(ctx context.Context, env, id string) ([]byte, int, error) {
	key := keyJDM(env, id)
	if s.cache != nil {
		if b, ok, err := s.cache.Get(ctx, key); err == nil && ok {
			return decodeJDM(b)
		}
	}

	pool, err := s.pool(env)
	if err != nil {
		return nil, 0, err
	}

	var version int
	err = pool.QueryRow(ctx,
		`SELECT version FROM active_pointers WHERE object_type=$1 AND object_id=$2`,
		objJDM, id).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, 0, newErr(NotFound, "no active jdm for id "+id)
	}
	if err != nil {
		return nil, 0, classifyPg("resolve jdm pointer", err)
	}

	var jdm []byte
	err = pool.QueryRow(ctx,
		`SELECT jdm FROM jdm_versions WHERE jdm_id=$1 AND version=$2`, id, version).Scan(&jdm)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, 0, newErr(NotFound, "jdm version body missing for "+id)
	}
	if err != nil {
		return nil, 0, classifyPg("resolve jdm body", err)
	}

	if s.cache != nil {
		if b, encErr := encodeJDM(jdm, version); encErr == nil {
			_ = s.cache.Set(ctx, key, b, s.ttl)
		}
	}
	return jdm, version, nil
}

// Connections returns the active connection defs for env (cache-aside), joining
// each active connection pointer to its version body. NEVER reads a secret value
// — only secret_ref ([[config-and-secrets]], AC-20).
func (s *PgStore) Connections(ctx context.Context, env string) ([]connect.ConnectionDef, error) {
	key := keyConns(env)
	if s.cache != nil {
		if b, ok, err := s.cache.Get(ctx, key); err == nil && ok {
			return decodeConns(b)
		}
	}

	pool, err := s.pool(env)
	if err != nil {
		return nil, err
	}

	rows, err := pool.Query(ctx,
		`SELECT c.key, c.type, cv.settings, cv.secret_ref, cv.resilience
		   FROM active_pointers ap
		   JOIN connections c  ON c.key = ap.object_id
		   JOIN connection_versions cv ON cv.conn_key = ap.object_id AND cv.version = ap.version
		  WHERE ap.object_type=$1
		  ORDER BY c.key`, objConnection)
	if err != nil {
		return nil, classifyPg("list connections", err)
	}
	defer rows.Close()

	var defs []connect.ConnectionDef
	for rows.Next() {
		var (
			ckey, ctype   string
			settingsRaw   []byte
			secretRef     *string
			resilienceRaw []byte
		)
		if err := rows.Scan(&ckey, &ctype, &settingsRaw, &secretRef, &resilienceRaw); err != nil {
			return nil, classifyPg("scan connection", err)
		}
		var settings map[string]any
		if len(settingsRaw) > 0 {
			if err := json.Unmarshal(settingsRaw, &settings); err != nil {
				return nil, wrapErr(Validation, "decode connection settings (bad config)", err)
			}
		}
		var res connect.ResiliencePolicy
		if len(resilienceRaw) > 0 {
			if err := json.Unmarshal(resilienceRaw, &res); err != nil {
				return nil, wrapErr(Validation, "decode connection resilience (bad config)", err)
			}
		}
		def := connect.ConnectionDef{
			Key:        ckey,
			Type:       ctype,
			Settings:   settings,
			Resilience: res,
		}
		if secretRef != nil {
			def.SecretRef = *secretRef
		}
		defs = append(defs, def)
	}
	if err := rows.Err(); err != nil {
		return nil, classifyPg("iterate connections", err)
	}

	if s.cache != nil {
		if b, encErr := encodeConns(defs); encErr == nil {
			_ = s.cache.Set(ctx, key, b, s.ttl)
		}
	}
	return defs, nil
}

// PutFlowVersion inserts a NEW immutable flow version (AC-9). The version number
// is max(version)+1 computed inside a transaction with the parent flows row
// locked FOR UPDATE so two concurrent authors cannot collide. The row lands with
// validated=false; its fixtures are inserted alongside, and an audit_log row
// records the creation. Prior versions are never touched.
func (s *PgStore) PutFlowVersion(ctx context.Context, env string, f FlowVersion) (int, error) {
	if f.FlowID == "" {
		return 0, newErr(Validation, "flow version missing flowId")
	}
	if f.Method == "" || f.Path == "" {
		return 0, newErr(Validation, "flow version missing method/path")
	}
	pool, err := s.pool(env)
	if err != nil {
		return 0, err
	}

	treeRaw, err := json.Marshal(f.Tree)
	if err != nil {
		return 0, wrapErr(Validation, "encode flow tree", err)
	}

	var version int
	err = withTx(ctx, pool, func(tx pgx.Tx) error {
		// Ensure the parent identity exists and lock it so the version counter is
		// serialized per flow.
		if _, err := tx.Exec(ctx,
			`INSERT INTO flows (id, method, path, created_by)
			 VALUES ($1,$2,$3,$4)
			 ON CONFLICT (id) DO NOTHING`, f.FlowID, f.Method, f.Path, s.actor); err != nil {
			return classifyPg("ensure flow identity", err)
		}
		if _, err := tx.Exec(ctx,
			`SELECT id FROM flows WHERE id=$1 FOR UPDATE`, f.FlowID); err != nil {
			return classifyPg("lock flow identity", err)
		}

		var maxV *int
		if err := tx.QueryRow(ctx,
			`SELECT max(version) FROM flow_versions WHERE flow_id=$1`, f.FlowID).Scan(&maxV); err != nil {
			return classifyPg("read max version", err)
		}
		version = 1
		if maxV != nil {
			version = *maxV + 1
		}

		if _, err := tx.Exec(ctx,
			`INSERT INTO flow_versions (flow_id, version, tree, checksum, validated, created_by)
			 VALUES ($1,$2,$3,$4,false,$5)`,
			f.FlowID, version, treeRaw, checksum(treeRaw), s.actor); err != nil {
			return classifyPg("insert flow version", err)
		}

		for _, fx := range f.Fixtures {
			body, mErr := json.Marshal(fx)
			if mErr != nil {
				return wrapErr(Validation, "encode fixture", mErr)
			}
			if _, err := tx.Exec(ctx,
				`INSERT INTO flow_fixtures (flow_id, version, name, body)
				 VALUES ($1,$2,$3,$4)`, f.FlowID, version, fx.Name, body); err != nil {
				return classifyPg("insert fixture", err)
			}
		}

		if _, err := tx.Exec(ctx,
			`INSERT INTO audit_log (actor, action, object_type, object_id, to_version)
			 VALUES ($1,$2,$3,$4,$5)`,
			s.actor, actCreateVersion, objFlow, f.FlowID, version); err != nil {
			return classifyPg("audit create version", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return version, nil
}

// SetActive publishes OR rolls back a flow by moving the single active_pointers
// row and appending an audit_log row in the same transaction (AC-10). It REFUSES
// an un-validated version (publish is blocked on a failed test — AC-13). The
// action is 'rollback' when the new version is below the current pointer, else
// 'publish'. Cache invalidation fires AFTER commit (never inside the tx) so a
// rolled-back tx cannot leave the fleet with a dropped-but-still-DB-current key.
func (s *PgStore) SetActive(ctx context.Context, env, flowID string, version int) error {
	pool, err := s.pool(env)
	if err != nil {
		return err
	}

	var action string
	err = withTx(ctx, pool, func(tx pgx.Tx) error {
		var validated bool
		err := tx.QueryRow(ctx,
			`SELECT validated FROM flow_versions WHERE flow_id=$1 AND version=$2`,
			flowID, version).Scan(&validated)
		if errors.Is(err, pgx.ErrNoRows) {
			return newErr(NotFound, "cannot activate missing flow version")
		}
		if err != nil {
			return classifyPg("read version validated", err)
		}
		if !validated {
			return newErr(Validation, "cannot publish un-validated flow version (publish blocked)")
		}

		var from *int
		if err := tx.QueryRow(ctx,
			`SELECT version FROM active_pointers WHERE object_type=$1 AND object_id=$2`,
			objFlow, flowID).Scan(&from); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return classifyPg("read current pointer", err)
		}

		action = actPublish
		if from != nil && version < *from {
			action = actRollback
		}

		if _, err := tx.Exec(ctx,
			`INSERT INTO active_pointers (object_type, object_id, version, updated_by)
			 VALUES ($1,$2,$3,$4)
			 ON CONFLICT (object_type, object_id)
			 DO UPDATE SET version=EXCLUDED.version, updated_at=now(), updated_by=EXCLUDED.updated_by`,
			objFlow, flowID, version, s.actor); err != nil {
			return classifyPg("move active pointer", err)
		}

		if _, err := tx.Exec(ctx,
			`INSERT INTO audit_log (actor, action, object_type, object_id, from_version, to_version)
			 VALUES ($1,$2,$3,$4,$5,$6)`,
			s.actor, action, objFlow, flowID, from, version); err != nil {
			return classifyPg("audit set active", err)
		}
		return nil
	})
	if err != nil {
		return err
	}

	// After commit: invalidate the fleet's cached flow keys for this env (R5,
	// AC-16). Best-effort; TTL bounds any missed invalidate.
	if s.cache != nil {
		_ = s.cache.Invalidate(ctx, flowDeletePattern(env))
	}
	return nil
}

// PutJDMVersion inserts a NEW immutable JDM version and points the active JDM
// pointer at it, mirroring PutFlowVersion's transactional shape: ensure the jdms
// identity row, lock it FOR UPDATE, compute max(version)+1 (or honor a positive
// explicit version), insert the version body with its checksum + actor, then
// move the active_pointers row for objJDM and append create_version + publish
// audit rows. If version<=0 the next version is auto-assigned. Re-running with a
// doc already present simply lands a new version and re-points; the top-level
// seed-skip gate (SeedPgStore) is the primary idempotency guard.
func (s *PgStore) PutJDMVersion(ctx context.Context, env, jdmID string, doc []byte, version int) (int, error) {
	if jdmID == "" {
		return 0, newErr(Validation, "jdm version missing jdmId")
	}
	if len(doc) == 0 {
		return 0, newErr(Validation, "jdm version missing doc")
	}
	pool, err := s.pool(env)
	if err != nil {
		return 0, err
	}

	var assigned int
	err = withTx(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`INSERT INTO jdms (id, created_by) VALUES ($1,$2)
			 ON CONFLICT (id) DO NOTHING`, jdmID, s.actor); err != nil {
			return classifyPg("ensure jdm identity", err)
		}
		if _, err := tx.Exec(ctx,
			`SELECT id FROM jdms WHERE id=$1 FOR UPDATE`, jdmID); err != nil {
			return classifyPg("lock jdm identity", err)
		}

		assigned = version
		if assigned <= 0 {
			var maxV *int
			if err := tx.QueryRow(ctx,
				`SELECT max(version) FROM jdm_versions WHERE jdm_id=$1`, jdmID).Scan(&maxV); err != nil {
				return classifyPg("read max jdm version", err)
			}
			assigned = 1
			if maxV != nil {
				assigned = *maxV + 1
			}
		}

		if _, err := tx.Exec(ctx,
			`INSERT INTO jdm_versions (jdm_id, version, jdm, checksum, created_by)
			 VALUES ($1,$2,$3,$4,$5)
			 ON CONFLICT (jdm_id, version) DO NOTHING`,
			jdmID, assigned, doc, checksum(doc), s.actor); err != nil {
			return classifyPg("insert jdm version", err)
		}

		if _, err := tx.Exec(ctx,
			`INSERT INTO active_pointers (object_type, object_id, version, updated_by)
			 VALUES ($1,$2,$3,$4)
			 ON CONFLICT (object_type, object_id)
			 DO UPDATE SET version=EXCLUDED.version, updated_at=now(), updated_by=EXCLUDED.updated_by`,
			objJDM, jdmID, assigned, s.actor); err != nil {
			return classifyPg("move jdm pointer", err)
		}

		if _, err := tx.Exec(ctx,
			`INSERT INTO audit_log (actor, action, object_type, object_id, to_version)
			 VALUES ($1,$2,$3,$4,$5)`,
			s.actor, actCreateVersion, objJDM, jdmID, assigned); err != nil {
			return classifyPg("audit create jdm version", err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO audit_log (actor, action, object_type, object_id, to_version)
			 VALUES ($1,$2,$3,$4,$5)`,
			s.actor, actPublish, objJDM, jdmID, assigned); err != nil {
			return classifyPg("audit publish jdm version", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return assigned, nil
}

// PutConnectionVersion inserts a NEW immutable connection version and points the
// active connection pointer at it, mirroring PutFlowVersion's transactional
// shape. It stores def.Settings and def.Resilience as jsonb and def.SecretRef as
// the ONLY secret surface (connection_versions has no column for a secret value —
// a value is never written here, [[config-and-secrets]]). version is always
// auto-assigned (max+1). Returns the assigned version.
func (s *PgStore) PutConnectionVersion(ctx context.Context, env string, def connect.ConnectionDef) (int, error) {
	if def.Key == "" || def.Type == "" {
		return 0, newErr(Validation, "connection version missing key or type")
	}
	pool, err := s.pool(env)
	if err != nil {
		return 0, err
	}

	settingsRaw, err := json.Marshal(def.Settings)
	if err != nil {
		return 0, wrapErr(Validation, "encode connection settings", err)
	}
	if len(settingsRaw) == 0 || string(settingsRaw) == "null" {
		settingsRaw = []byte("{}")
	}
	resilienceRaw, err := json.Marshal(def.Resilience)
	if err != nil {
		return 0, wrapErr(Validation, "encode connection resilience", err)
	}

	// A connection stores secret_ref ONLY; the empty string maps to SQL NULL so
	// the column stays a clean "no secret pointer" rather than an empty string.
	var secretRef *string
	if def.SecretRef != "" {
		sr := def.SecretRef
		secretRef = &sr
	}

	var assigned int
	err = withTx(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`INSERT INTO connections (key, type, created_by) VALUES ($1,$2,$3)
			 ON CONFLICT (key) DO NOTHING`, def.Key, def.Type, s.actor); err != nil {
			return classifyPg("ensure connection identity", err)
		}
		if _, err := tx.Exec(ctx,
			`SELECT key FROM connections WHERE key=$1 FOR UPDATE`, def.Key); err != nil {
			return classifyPg("lock connection identity", err)
		}

		var maxV *int
		if err := tx.QueryRow(ctx,
			`SELECT max(version) FROM connection_versions WHERE conn_key=$1`, def.Key).Scan(&maxV); err != nil {
			return classifyPg("read max connection version", err)
		}
		assigned = 1
		if maxV != nil {
			assigned = *maxV + 1
		}

		if _, err := tx.Exec(ctx,
			`INSERT INTO connection_versions
			   (conn_key, version, settings, secret_ref, resilience, checksum, created_by)
			 VALUES ($1,$2,$3,$4,$5,$6,$7)`,
			def.Key, assigned, settingsRaw, secretRef, resilienceRaw, checksum(settingsRaw), s.actor); err != nil {
			return classifyPg("insert connection version", err)
		}

		if _, err := tx.Exec(ctx,
			`INSERT INTO active_pointers (object_type, object_id, version, updated_by)
			 VALUES ($1,$2,$3,$4)
			 ON CONFLICT (object_type, object_id)
			 DO UPDATE SET version=EXCLUDED.version, updated_at=now(), updated_by=EXCLUDED.updated_by`,
			objConnection, def.Key, assigned, s.actor); err != nil {
			return classifyPg("move connection pointer", err)
		}

		if _, err := tx.Exec(ctx,
			`INSERT INTO audit_log (actor, action, object_type, object_id, to_version)
			 VALUES ($1,$2,$3,$4,$5)`,
			s.actor, actCreateVersion, objConnection, def.Key, assigned); err != nil {
			return classifyPg("audit create connection version", err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO audit_log (actor, action, object_type, object_id, to_version)
			 VALUES ($1,$2,$3,$4,$5)`,
			s.actor, actPublish, objConnection, def.Key, assigned); err != nil {
			return classifyPg("audit publish connection version", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return assigned, nil
}

// Ping reports store reachability for /readyz (seam addition, Slice E). It pings
// every env pool; the first unreachable pool is an Upstream error.
func (s *PgStore) Ping(ctx context.Context) error {
	for env, p := range s.pools {
		if err := p.Ping(ctx); err != nil {
			return wrapErr(Upstream, "ping config store for env "+quote(env), err)
		}
	}
	return nil
}

// ---- store-internal methods (not on the Store seam; driven by admin/Strapi) ----

// MarkValidated flips a flow version's validated flag to true after
// /admin/flows/validate has passed structure + fixtures (AC-13, §6b). Only a
// validated version may be published (SetActive refuses otherwise).
func (s *PgStore) MarkValidated(ctx context.Context, env, flowID string, version int) error {
	pool, err := s.pool(env)
	if err != nil {
		return err
	}
	tag, err := pool.Exec(ctx,
		`UPDATE flow_versions SET validated=true WHERE flow_id=$1 AND version=$2`, flowID, version)
	if err != nil {
		return classifyPg("mark validated", err)
	}
	if tag.RowsAffected() == 0 {
		return newErr(NotFound, "cannot validate missing flow version")
	}
	return nil
}

// PromoteVersion copies an exact flow version AND its fixtures from srcEnv to
// dstEnv under the same version number, then writes an audit_log(promote) row in
// the destination (AC-12). It does NOT advance the destination pointer —
// promotion stages the version; a separate SetActive activates it, so envs can
// hold different active versions ("the thing you tested is the thing you
// promote"). The copied version lands validated=true (it was validated in src).
func (s *PgStore) PromoteVersion(ctx context.Context, srcEnv, dstEnv, flowID string, version int) error {
	src, err := s.pool(srcEnv)
	if err != nil {
		return err
	}
	dst, err := s.pool(dstEnv)
	if err != nil {
		return err
	}

	// Read the source version + its identity metadata.
	var treeRaw []byte
	var method, path, sum string
	err = src.QueryRow(ctx,
		`SELECT fv.tree, fv.checksum, f.method, f.path
		   FROM flow_versions fv JOIN flows f ON f.id = fv.flow_id
		  WHERE fv.flow_id=$1 AND fv.version=$2`, flowID, version).Scan(&treeRaw, &sum, &method, &path)
	if errors.Is(err, pgx.ErrNoRows) {
		return newErr(NotFound, "source flow version not found for promotion")
	}
	if err != nil {
		return classifyPg("read source version", err)
	}

	type fxRow struct {
		name string
		body []byte
	}
	frows, err := src.Query(ctx,
		`SELECT name, body FROM flow_fixtures WHERE flow_id=$1 AND version=$2`, flowID, version)
	if err != nil {
		return classifyPg("read source fixtures", err)
	}
	var fixtures []fxRow
	for frows.Next() {
		var fr fxRow
		if err := frows.Scan(&fr.name, &fr.body); err != nil {
			frows.Close()
			return classifyPg("scan source fixture", err)
		}
		fixtures = append(fixtures, fr)
	}
	frows.Close()
	if err := frows.Err(); err != nil {
		return classifyPg("iterate source fixtures", err)
	}

	// Write them into the destination env in one transaction.
	return withTx(ctx, dst, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`INSERT INTO flows (id, method, path, created_by)
			 VALUES ($1,$2,$3,$4) ON CONFLICT (id) DO NOTHING`,
			flowID, method, path, s.actor); err != nil {
			return classifyPg("ensure dst flow identity", err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO flow_versions (flow_id, version, tree, checksum, validated, created_by)
			 VALUES ($1,$2,$3,$4,true,$5)
			 ON CONFLICT (flow_id, version) DO NOTHING`,
			flowID, version, treeRaw, sum, s.actor); err != nil {
			return classifyPg("insert promoted version", err)
		}
		for _, fr := range fixtures {
			if _, err := tx.Exec(ctx,
				`INSERT INTO flow_fixtures (flow_id, version, name, body)
				 VALUES ($1,$2,$3,$4) ON CONFLICT (flow_id, version, name) DO NOTHING`,
				flowID, version, fr.name, fr.body); err != nil {
				return classifyPg("insert promoted fixture", err)
			}
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO audit_log (actor, action, object_type, object_id, to_version, reason)
			 VALUES ($1,$2,$3,$4,$5,$6)`,
			s.actor, actPromote, objFlow, flowID, version, "promote "+srcEnv+"->"+dstEnv); err != nil {
			return classifyPg("audit promote", err)
		}
		return nil
	})
}

// AuditEntry is one row of the append-only audit trail.
type AuditEntry struct {
	Action      string
	ObjectType  string
	ObjectID    string
	FromVersion *int
	ToVersion   *int
	Actor       string
	At          time.Time
	Reason      string
}

// AuditTrail returns the audit history for an object, newest first (§6c).
func (s *PgStore) AuditTrail(ctx context.Context, env, objectType, objectID string) ([]AuditEntry, error) {
	pool, err := s.pool(env)
	if err != nil {
		return nil, err
	}
	rows, err := pool.Query(ctx,
		`SELECT action, object_type, object_id, from_version, to_version, actor, at, coalesce(reason,'')
		   FROM audit_log WHERE object_type=$1 AND object_id=$2 ORDER BY at DESC, id DESC`,
		objectType, objectID)
	if err != nil {
		return nil, classifyPg("read audit trail", err)
	}
	defer rows.Close()

	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.Action, &e.ObjectType, &e.ObjectID,
			&e.FromVersion, &e.ToVersion, &e.Actor, &e.At, &e.Reason); err != nil {
			return nil, classifyPg("scan audit row", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, classifyPg("iterate audit rows", err)
	}
	return out, nil
}

// ---- helpers ----

// withTx runs fn inside a transaction, committing on success and rolling back on
// any error (including a panic). It keeps the multi-write version/pointer/audit
// invariants atomic ([[data-and-migrations]] transactions around invariants).
func withTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return classifyPg("begin tx", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return classifyPg("commit tx", err)
	}
	return nil
}

// classifyPg maps a pgx/driver error to the config taxonomy (§7). A deadline is
// Timeout; everything else from the driver is Upstream (Postgres unreachable or
// failing) unless already a classified *ConfigError. A ConfigError passes
// through unchanged so an inner Validation/NotFound keeps its class.
func classifyPg(op string, err error) error {
	var ce *ConfigError
	if errors.As(err, &ce) {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return wrapErr(Timeout, op, err)
	}
	return wrapErr(Upstream, op, err)
}

// quote wraps s in quotes for readable error messages, rendering the empty env as
// the explicit default marker.
func quote(s string) string {
	if s == "" {
		return `""(default)`
	}
	return `"` + s + `"`
}
