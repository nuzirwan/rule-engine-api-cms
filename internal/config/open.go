package config

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"nzr-rules-engine/migrations"
)

// OpenPool parses dsn, opens a pgx connection pool, and verifies reachability
// with a Ping. A malformed DSN is a Validation error (fail fast at startup); an
// open/ping failure is Upstream (Postgres unreachable). The caller owns Close.
//
// This keeps DSN->pool wiring in the config package (where PgStore lives) so
// cmd/engine and the integration test share one place to build the pool for the
// real Store.
func OpenPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, wrapErr(Validation, "parse config dsn", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, wrapErr(Upstream, "open config pool", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, wrapErr(Upstream, "ping config pool", err)
	}
	return pool, nil
}

// Migrate applies the embedded config-store migrations over pool. It is a thin
// delegation to migrations.Apply so the wiring layer runs the schema at startup
// before first use. A failure is an Upstream error (the store is not usable).
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	if err := migrations.Apply(ctx, pool); err != nil {
		return wrapErr(Upstream, "apply config migrations", err)
	}
	return nil
}
