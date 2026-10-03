package config

import (
	"context"
	"regexp"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// schemaNameRE is the ONLY identifier that is ever interpolated into SQL in this
// package (the config schema in CREATE SCHEMA / SET search_path). Everything else
// is parameterized. Restricting it to a plain SQL identifier closes the injection
// door: a value failing this pattern is rejected before it reaches any statement.
var schemaNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidateSchemaName enforces that s is a safe, unquoted Postgres identifier
// (^[A-Za-z_][A-Za-z0-9_]*$). It is the guard for the one place a schema name is
// interpolated into DDL (OpenSchemaPool); callers MUST pass only validated names.
func ValidateSchemaName(s string) error {
	if s == "" {
		return newErr(Validation, "config schema name is empty")
	}
	if !schemaNameRE.MatchString(s) {
		return newErr(Validation, "config schema name "+quote(s)+" is not a valid identifier")
	}
	return nil
}

// OpenSchemaPool builds a pgxpool whose EVERY connection runs in a dedicated
// Postgres schema, then ensures that schema exists. The flow:
//
//   - validate schema (the lone interpolated identifier);
//   - ParseConfig(dsn), then set AfterConnect to run `SET search_path TO <schema>`
//     so every pooled connection AND every reconnect lands in <schema> — robust
//     across pool churn, unlike a one-shot session setting;
//   - create the pool and, on one acquired connection, `CREATE SCHEMA IF NOT
//     EXISTS <schema>` so a brand-new database gets the schema before any
//     migration runs.
//
// Because the migration DDL is unqualified (CREATE TABLE flows ...), running
// migrations.Apply over this pool lands every table in <schema>.*, leaving public
// untouched. The caller owns Close.
func OpenSchemaPool(ctx context.Context, dsn, schema string) (*pgxpool.Pool, error) {
	if err := ValidateSchemaName(schema); err != nil {
		return nil, err
	}

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, wrapErr(Validation, "parse config dsn", err)
	}
	// Run on every connection (initial + reconnect) so the search_path is never
	// lost. The schema is validated above, so this interpolation is safe.
	setPath := "SET search_path TO " + schema
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, setPath)
		return err
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, wrapErr(Upstream, "open config pool", err)
	}

	// Ensure the dedicated schema exists before migrations run. Idempotent.
	if _, err := pool.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS "+schema); err != nil {
		pool.Close()
		return nil, wrapErr(Upstream, "create config schema", err)
	}
	return pool, nil
}

// SchemaHasConfigTables reports whether the config store's tables already exist
// in schema (checked via the sentinel `flows` table). It gates migrations.Apply
// on a second process start against an already-migrated schema so the unqualified
// CREATE TABLE DDL is not re-run (which would error). schema must already have
// passed ValidateSchemaName (it is parameterized here, not interpolated).
func SchemaHasConfigTables(ctx context.Context, pool *pgxpool.Pool, schema string) (bool, error) {
	var exists bool
	err := pool.QueryRow(ctx,
		`SELECT EXISTS (
		   SELECT 1 FROM information_schema.tables
		    WHERE table_schema=$1 AND table_name='flows')`, schema).Scan(&exists)
	if err != nil {
		return false, classifyPg("check config tables", err)
	}
	return exists, nil
}
