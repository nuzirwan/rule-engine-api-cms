// Package migrations embeds the engine-owned config-store SQL migrations and
// provides a minimal, dependency-free applier.
//
// The files follow the golang-migrate naming convention (NNNN_name.up.sql /
// NNNN_name.down.sql, applied in ascending numeric order). Production deploys may
// run these with the `migrate` CLI; Apply/Rollback here let tests and embedded
// callers run them over an existing pgx-compatible connection without pulling the
// golang-migrate library into the module (keeping the dependency surface small
// per the module's "stdlib + pgx + a valkey client only" rule).
package migrations

import (
	"context"
	"embed"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

//go:embed *.sql
var files embed.FS

// Execer is the minimal write surface the applier needs. *pgxpool.Pool, pgx.Conn
// and pgx.Tx all satisfy it, so callers pass whichever they hold. The applier
// ignores the returned command tag.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// step is one migration direction (an ordered pair of version + SQL body).
type step struct {
	version string
	sql     string
}

// ups returns the up migrations sorted ascending by version prefix.
func ups() ([]step, error) { return collect(".up.sql") }

// downs returns the down migrations sorted descending by version prefix.
func downs() ([]step, error) {
	s, err := collect(".down.sql")
	if err != nil {
		return nil, err
	}
	// reverse: newest down applied first
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
	return s, nil
}

// collect reads every embedded file with the given suffix into ordered steps.
func collect(suffix string) ([]step, error) {
	entries, err := files.ReadDir(".")
	if err != nil {
		return nil, err
	}
	var out []step
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, suffix) {
			continue
		}
		body, err := files.ReadFile(name)
		if err != nil {
			return nil, err
		}
		out = append(out, step{version: name, sql: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

// Apply runs every up migration in ascending order against ex. Each file is one
// migration concern; the applier executes the whole file in a single Exec (the
// DDL here is independent statements Postgres accepts in one batch).
func Apply(ctx context.Context, ex Execer) error {
	steps, err := ups()
	if err != nil {
		return fmt.Errorf("load up migrations: %w", err)
	}
	for _, s := range steps {
		if _, err := ex.Exec(ctx, s.sql); err != nil {
			return fmt.Errorf("apply %s: %w", s.version, err)
		}
	}
	return nil
}

// Rollback runs every down migration in descending order against ex. Used by
// tests to prove the schema tears down cleanly (migrations AC in Slice D §8).
func Rollback(ctx context.Context, ex Execer) error {
	steps, err := downs()
	if err != nil {
		return fmt.Errorf("load down migrations: %w", err)
	}
	for _, s := range steps {
		if _, err := ex.Exec(ctx, s.sql); err != nil {
			return fmt.Errorf("rollback %s: %w", s.version, err)
		}
	}
	return nil
}
