package drivers

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"nzr-rules-engine/internal/connect"
)

// pgConnector builds pgxpool-backed clients for the "postgres" connection type.
type pgConnector struct{}

// newPGConnector returns the postgres Connector.
func newPGConnector() connect.Connector { return pgConnector{} }

// Type implements connect.Connector.
func (pgConnector) Type() string { return "postgres" }

// Open builds a pgxpool from the def's Settings and the resolved secret, then
// wraps it in a pgClient. The pool is the shared resource (one per key, AC-5).
// Settings may carry a ready-made "dsn", or discrete host/port/database/user
// plus a pool.* block; the resolved secret supplies the password.
func (pgConnector) Open(ctx context.Context, def connect.ConnectionDef) (connect.Client, error) {
	dsn := buildDSN(def)
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, connect.NewConnError(connect.Validation, def.Key, "", "parse postgres dsn", err)
	}
	// Inject the password from the resolved secret onto the parsed config so it
	// never has to be interpolated into the DSN string (and so a redacted DSN is
	// safe to log). pgxpool.ParseConfig has already parsed any dsn-embedded user.
	if sec, ok := connect.SecretFrom(ctx); ok && !sec.IsZero() {
		cfg.ConnConfig.Password = string(sec.Reveal())
	}
	applyPoolSettings(cfg, def.Settings)

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, connect.NewConnError(connect.Upstream, def.Key, "", "open postgres pool", err)
	}
	return &pgClient{key: def.Key, pool: pool}, nil
}

// buildDSN assembles a connection string from Settings. A ready-made "dsn"
// setting wins; otherwise discrete host/port/database/user/sslmode are composed.
// The password is NOT part of the DSN — Open injects it from the secret onto the
// parsed config so the credential never lives in a loggable string.
func buildDSN(def connect.ConnectionDef) string {
	s := def.Settings
	if raw, ok := stringSetting(s, "dsn"); ok && raw != "" {
		return raw
	}
	host, _ := stringSetting(s, "host")
	if host == "" {
		host = "localhost"
	}
	port, _ := stringSetting(s, "port")
	if port == "" {
		port = "5432"
	}
	database, _ := stringSetting(s, "database")
	user, _ := stringSetting(s, "user")
	if user == "" {
		user = "postgres"
	}
	sslmode, ok := stringSetting(s, "sslmode")
	if !ok {
		sslmode = "disable"
	}
	return fmt.Sprintf("postgres://%s@%s:%s/%s?sslmode=%s",
		user, host, port, database, sslmode)
}

// applyPoolSettings maps the optional pool.* block onto the pgxpool config.
func applyPoolSettings(cfg *pgxpool.Config, s map[string]any) {
	pool, ok := s["pool"].(map[string]any)
	if !ok {
		return
	}
	if v, ok := intSetting(pool, "maxConns"); ok {
		cfg.MaxConns = int32(v)
	}
	if v, ok := intSetting(pool, "minConns"); ok {
		cfg.MinConns = int32(v)
	}
}

// pgClient is the inner driver client. It holds one shared pgxpool.
type pgClient struct {
	key  string
	pool *pgxpool.Pool
}

// Execute runs a query/exec operation with POSITIONAL params only ($1..). A
// SELECT under exec or a mutation under query is a Validation error; a ping is a
// cheap SELECT 1 health probe.
func (c *pgClient) Execute(ctx context.Context, op connect.Operation) (any, error) {
	switch op.Kind {
	case "query":
		return c.query(ctx, op)
	case "exec":
		return c.exec(ctx, op)
	case "ping":
		if err := c.pool.Ping(ctx); err != nil {
			return nil, c.classify("ping", err)
		}
		return map[string]any{"ok": true}, nil
	default:
		return nil, connect.NewConnError(connect.Validation, c.key, op.Kind, "unsupported operation kind for postgres", nil)
	}
}

// query runs a read and returns rows as []map[string]any.
func (c *pgClient) query(ctx context.Context, op connect.Operation) (any, error) {
	sql, params, err := sqlAndParams(c.key, "query", op.Payload)
	if err != nil {
		return nil, err
	}
	if isMutation(sql) {
		return nil, connect.NewConnError(connect.Validation, c.key, "query", "mutating statement under query kind", nil)
	}
	rows, err := c.pool.Query(ctx, sql, params...)
	if err != nil {
		return nil, c.classify("query", err)
	}
	defer rows.Close()

	fields := rows.FieldDescriptions()
	out := make([]map[string]any, 0)
	for rows.Next() {
		vals, verr := rows.Values()
		if verr != nil {
			return nil, c.classify("query", verr)
		}
		row := make(map[string]any, len(fields))
		for i, fd := range fields {
			row[fd.Name] = vals[i]
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, c.classify("query", err)
	}
	return out, nil
}

// exec runs a mutation and returns {rowsAffected}.
func (c *pgClient) exec(ctx context.Context, op connect.Operation) (any, error) {
	sql, params, err := sqlAndParams(c.key, "exec", op.Payload)
	if err != nil {
		return nil, err
	}
	if isSelect(sql) {
		return nil, connect.NewConnError(connect.Validation, c.key, "exec", "select statement under exec kind", nil)
	}
	tag, err := c.pool.Exec(ctx, sql, params...)
	if err != nil {
		return nil, c.classify("exec", err)
	}
	return map[string]any{"rowsAffected": tag.RowsAffected()}, nil
}

// Close releases the pool.
func (c *pgClient) Close() error {
	c.pool.Close()
	return nil
}

// classify maps a pgx/pgconn error to the shared taxonomy: no-rows => NotFound,
// a ctx deadline => Timeout, a network reset / unavailable server => Upstream,
// everything else => Upstream for a driver-side fault (never a secret).
func (c *pgClient) classify(opKind string, err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return connect.NewConnError(connect.NotFound, c.key, opKind, "no rows in result set", err)
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return connect.NewConnError(connect.Timeout, c.key, opKind, "query deadline exceeded", err)
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return connect.NewConnError(connect.Upstream, c.key, opKind, "postgres network error", err)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		// A bad SQL shape surfaces as a server-side syntax/constraint error:
		// treat 42xxx (syntax/undefined) as Validation, else Upstream.
		if strings.HasPrefix(pgErr.Code, "42") {
			return connect.NewConnError(connect.Validation, c.key, opKind, "postgres rejected statement", err)
		}
		return connect.NewConnError(connect.Upstream, c.key, opKind, "postgres server error", err)
	}
	return connect.NewConnError(connect.Upstream, c.key, opKind, "postgres error", err)
}
