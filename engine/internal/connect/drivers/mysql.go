package drivers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"

	"nzr-rules-engine/internal/connect"
)

// mysqlConnector builds *sql.DB-backed clients for the "mysql" connection type.
type mysqlConnector struct{}

// newMySQLConnector returns the mysql Connector.
func newMySQLConnector() connect.Connector { return mysqlConnector{} }

// Type implements connect.Connector.
func (mysqlConnector) Type() string { return "mysql" }

// Lifecycle implements connect.Connector. MySQL uses database/sql pool internally.
func (mysqlConnector) Lifecycle() connect.Lifecycle { return connect.LifecyclePooled }

// Capabilities implements connect.Connector. MySQL supports query/exec.
func (mysqlConnector) Capabilities() connect.Capability { return connect.CapQueryExec }

// SecretSchema implements connect.SecretSchemaProvider. MySQL accepts an
// optional database password secret.
func (mysqlConnector) SecretSchema() []connect.SecretField {
	return []connect.SecretField{
		{Name: "password", Required: false, Label: "Database Password"},
	}
}

// Open builds a *sql.DB from the def's Settings and the resolved secret, then
// wraps it in a mysqlClient. The pool is the shared resource (one per key, AC-5).
// Settings may carry a ready-made "dsn", or discrete host/port/database/user
// plus pool settings; the resolved secret supplies the password.
func (mysqlConnector) Open(ctx context.Context, def connect.ConnectionDef) (connect.Client, error) {
	dsn := buildMySQLDSN(def)

	// If secret present, rebuild DSN with password injected.
	// Prefer SecretsFrom (multi-secret) with fallback to SecretFrom (legacy).
	if secrets, ok := connect.SecretsFrom(ctx); ok {
		if pwSec, found := secrets["password"]; found && !pwSec.IsZero() {
			dsn = injectMySQLPassword(dsn, string(pwSec.Reveal()))
		}
	} else if sec, ok := connect.SecretFrom(ctx); ok && !sec.IsZero() {
		dsn = injectMySQLPassword(dsn, string(sec.Reveal()))
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, connect.NewConnError(connect.Validation, def.Key, "", "parse mysql dsn", err)
	}

	applyMySQLPoolSettings(db, def.Settings)

	// Ping to verify connectivity
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, connect.NewConnError(connect.Upstream, def.Key, "", "open mysql connection", err)
	}

	return &mysqlClient{key: def.Key, db: db}, nil
}

// buildMySQLDSN assembles a connection string from Settings. A ready-made "dsn"
// setting wins; otherwise discrete host/port/database/user are composed.
// The password is NOT part of the composed DSN — Open injects it from the secret
// so the credential never lives in a loggable string.
func buildMySQLDSN(def connect.ConnectionDef) string {
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
		port = "3306"
	}
	database, _ := stringSetting(s, "database")
	user, _ := stringSetting(s, "user")
	if user == "" {
		user = "root"
	}
	// Format: user@tcp(host:port)/database?parseTime=true
	// Password is injected separately after secret resolution
	return fmt.Sprintf("%s@tcp(%s:%s)/%s?parseTime=true", user, host, port, database)
}

// injectMySQLPassword rebuilds the DSN with the password inserted after the user.
// MySQL DSN format is user:password@tcp(host:port)/database
func injectMySQLPassword(dsn, password string) string {
	// Find the @ separator
	atIdx := strings.Index(dsn, "@")
	if atIdx < 0 {
		// No @ found, cannot inject password
		return dsn
	}
	user := dsn[:atIdx]
	rest := dsn[atIdx:]
	return user + ":" + password + rest
}

// applyMySQLPoolSettings maps the optional pool settings onto the *sql.DB.
func applyMySQLPoolSettings(db *sql.DB, s map[string]any) {
	if v, ok := intSetting(s, "maxOpenConns"); ok {
		db.SetMaxOpenConns(v)
	}
	if v, ok := intSetting(s, "maxIdleConns"); ok {
		db.SetMaxIdleConns(v)
	}
	if raw, ok := stringSetting(s, "connMaxLifetime"); ok {
		if d, err := time.ParseDuration(raw); err == nil {
			db.SetConnMaxLifetime(d)
		}
	}
}

// mysqlClient is the inner driver client. It holds one shared *sql.DB.
type mysqlClient struct {
	key string
	db  *sql.DB
}

// Execute runs a query/exec operation with positional params (?). A SELECT under
// exec or a mutation under query is a Validation error; a ping is a cheap health
// probe.
func (c *mysqlClient) Execute(ctx context.Context, op connect.Operation) (any, error) {
	switch op.Kind {
	case "query":
		return c.query(ctx, op)
	case "exec":
		return c.exec(ctx, op)
	case "ping":
		if err := c.db.PingContext(ctx); err != nil {
			return nil, c.classify("ping", err)
		}
		return map[string]any{"ok": true}, nil
	default:
		return nil, connect.NewConnError(connect.Validation, c.key, op.Kind, "unsupported operation kind for mysql", nil)
	}
}

// query runs a read and returns rows as []map[string]any.
func (c *mysqlClient) query(ctx context.Context, op connect.Operation) (any, error) {
	sqlStr, params, err := sqlAndParams(c.key, "query", op.Payload)
	if err != nil {
		return nil, err
	}
	if isMutation(sqlStr) {
		return nil, connect.NewConnError(connect.Validation, c.key, "query", "mutating statement under query kind", nil)
	}
	rows, err := c.db.QueryContext(ctx, sqlStr, params...)
	if err != nil {
		return nil, c.classify("query", err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, c.classify("query", err)
	}

	out := make([]map[string]any, 0)
	for rows.Next() {
		// Create a slice of interface{} to hold each column value
		vals := make([]any, len(cols))
		valPtrs := make([]any, len(cols))
		for i := range vals {
			valPtrs[i] = &vals[i]
		}

		if err := rows.Scan(valPtrs...); err != nil {
			return nil, c.classify("query", err)
		}

		row := make(map[string]any, len(cols))
		for i, col := range cols {
			val := vals[i]
			// Convert []byte to string for MySQL text types
			if b, ok := val.([]byte); ok {
				row[col] = string(b)
			} else {
				row[col] = val
			}
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, c.classify("query", err)
	}
	return out, nil
}

// exec runs a mutation and returns {rowsAffected}.
func (c *mysqlClient) exec(ctx context.Context, op connect.Operation) (any, error) {
	sqlStr, params, err := sqlAndParams(c.key, "exec", op.Payload)
	if err != nil {
		return nil, err
	}
	if isSelect(sqlStr) {
		return nil, connect.NewConnError(connect.Validation, c.key, "exec", "select statement under exec kind", nil)
	}
	result, err := c.db.ExecContext(ctx, sqlStr, params...)
	if err != nil {
		return nil, c.classify("exec", err)
	}
	affected, _ := result.RowsAffected()
	return map[string]any{"rowsAffected": affected}, nil
}

// Close releases the db connection pool.
func (c *mysqlClient) Close() error {
	return c.db.Close()
}

// classify maps a MySQL error to the shared taxonomy: no-rows => NotFound,
// a ctx deadline => Timeout, a network error => Upstream, MySQL errors mapped
// by error number.
func (c *mysqlClient) classify(opKind string, err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return connect.NewConnError(connect.NotFound, c.key, opKind, "no rows in result set", err)
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return connect.NewConnError(connect.Timeout, c.key, opKind, "query deadline exceeded", err)
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		return connect.NewConnError(connect.Upstream, c.key, opKind, "mysql network error", err)
	}

	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) {
		// Map MySQL error numbers to taxonomy classes
		// See: https://dev.mysql.com/doc/mysql-errors/8.0/en/server-error-reference.html
		switch mysqlErr.Number {
		case 1045: // ER_ACCESS_DENIED_ERROR
			return connect.NewConnError(connect.Validation, c.key, opKind, "mysql access denied", err)
		case 1054: // ER_BAD_FIELD_ERROR (unknown column)
			return connect.NewConnError(connect.Validation, c.key, opKind, "mysql unknown column", err)
		case 1062: // ER_DUP_ENTRY (duplicate entry)
			return connect.NewConnError(connect.Validation, c.key, opKind, "mysql duplicate entry", err)
		case 1064: // ER_PARSE_ERROR (syntax error)
			return connect.NewConnError(connect.Validation, c.key, opKind, "mysql syntax error", err)
		case 1146: // ER_NO_SUCH_TABLE
			return connect.NewConnError(connect.NotFound, c.key, opKind, "mysql table not found", err)
		default:
			return connect.NewConnError(connect.Upstream, c.key, opKind, "mysql server error", err)
		}
	}

	return connect.NewConnError(connect.Upstream, c.key, opKind, "mysql error", err)
}
