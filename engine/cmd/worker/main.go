// Command worker is the isolated worker binary that executes flows for a single
// group. Each worker process loads only its assigned group's flows, JDMs, and
// connections, exposing an HTTP API for flow execution. Workers are spawned by
// the gateway based on group configuration and scale independently.
//
// CGO_ENABLED=1 is REQUIRED because the decision engine links the embedded
// GoRules ZEN native library; a CGO_ENABLED=0 build compiles via the !cgo stub
// but refuses ZEN at runtime.
//
// CLI flags:
//   - --group (required): the group ID this worker serves
//   - --addr: HTTP listen address (default :8080)
//   - --config-dsn: Postgres connection string for the config store
//   - --env: path to an optional .env file (default .env)
//
// Environment variables (override flags where noted):
//   - WORKER_GROUP: overrides --group
//   - WORKER_ADDR: overrides --addr
//   - CONFIG_DSN: overrides --config-dsn
//   - CONFIG_SCHEMA: Postgres schema for config tables (default "rule_engine")
//
// Lifecycle: load group config + flows + JDMs + connections, start HTTP server,
// start hot-reload goroutine (polls every 30s for version changes), handle
// SIGHUP for forced reload, then on SIGINT/SIGTERM drain in-flight requests
// (15s timeout), stop reloader, close registry pools and decision engine.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"nzr-rules-engine/internal/config"
	"nzr-rules-engine/internal/envfile"
	"nzr-rules-engine/internal/observ"
	"nzr-rules-engine/internal/worker"
	"nzr-rules-engine/migrations"
)

// shutdownTimeout bounds how long graceful drain waits for in-flight requests.
const shutdownTimeout = 15 * time.Second

// defaultConfigSchema is the dedicated Postgres schema the config store uses.
const defaultConfigSchema = "rule_engine"

// defaultEnv is the logical environment when none is configured. Workers always
// run in a single env (no multi-tenant env routing within a worker).
const defaultEnv = ""

func main() {
	groupID := flag.String("group", "", "group ID this worker serves (required)")
	addr := flag.String("addr", ":8080", "HTTP listen address")
	configDSN := flag.String("config-dsn", "", "Postgres connection string for config store (required)")
	envPath := flag.String("env", ".env", "path to an optional .env file")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	// Load the optional .env BEFORE reading any config env (process env wins).
	if err := envfile.Load(*envPath); err != nil {
		logger.Error("load env file", slog.Any("err", err))
		os.Exit(1)
	}

	// Environment variables override CLI flags.
	group := *groupID
	if v := os.Getenv("WORKER_GROUP"); v != "" {
		group = v
	}
	listenAddr := *addr
	if v := os.Getenv("WORKER_ADDR"); v != "" {
		listenAddr = v
	}
	dsn := *configDSN
	if v := os.Getenv("CONFIG_DSN"); v != "" {
		dsn = v
	}

	// Validate required flags.
	if group == "" {
		logger.Error("--group or WORKER_GROUP is required")
		os.Exit(1)
	}
	if dsn == "" {
		logger.Error("--config-dsn or CONFIG_DSN is required")
		os.Exit(1)
	}

	if err := run(group, listenAddr, dsn, logger); err != nil {
		logger.Error("worker exited with error", slog.Any("err", err))
		os.Exit(1)
	}
	logger.Info("worker stopped cleanly")
}

// run builds the worker, serves until termination signal, then shuts down.
func run(groupID, addr, dsn string, logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	tracer := observ.NewTracer(logger)
	obsLog := observ.NewLogger(logger)

	// Build the config store connection.
	store, cleanup, err := buildStore(ctx, dsn, logger)
	if err != nil {
		return err
	}
	defer cleanup()

	// Build the worker.
	w := worker.New(worker.Config{
		GroupID: groupID,
		Env:     defaultEnv,
		Store:   store,
		Tracer:  tracer,
		Log:     obsLog,
	})

	// Initial load of group configuration.
	logger.Info("loading group configuration", slog.String("group", groupID))
	if err := w.LoadGroup(ctx); err != nil {
		return fmt.Errorf("initial load failed: %w", err)
	}
	logger.Info("group loaded successfully",
		slog.String("group", groupID),
		slog.Int("version", w.LoadedVersion()))

	// Start the hot-reload goroutine.
	reloader := worker.NewReloader(w, store, defaultEnv, obsLog, worker.ReloadConfig{
		PollInterval: 30 * time.Second,
	})
	reloader.Start(ctx)

	// Build and start HTTP server.
	handler := worker.NewHandler(w)
	mux := http.NewServeMux()
	handler.Mount(mux)

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Start server in background.
	errCh := make(chan error, 1)
	go func() {
		logger.Info("worker listening", slog.String("addr", addr), slog.String("group", groupID))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	// Wait for termination signal or server error.
	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received, draining")
	case err := <-errCh:
		if err != nil {
			return err
		}
	}

	// Graceful shutdown: drain in-flight requests.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("server shutdown error", slog.Any("err", err))
	}

	// Stop the reloader.
	reloader.Stop()
	logger.Info("reloader stopped")

	// Close the worker (releases pools and decision engine).
	if err := w.Close(); err != nil {
		logger.Error("worker close error", slog.Any("err", err))
	}
	logger.Info("worker closed")

	return nil
}

// buildStore connects to Postgres and builds a PgStore for the worker.
func buildStore(ctx context.Context, dsn string, logger *slog.Logger) (config.WorkerStore, func(), error) {
	schema := os.Getenv("CONFIG_SCHEMA")
	if schema == "" {
		schema = defaultConfigSchema
	}

	// Parse DSN and set search_path to the config schema.
	parsedDSN, err := url.Parse(dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid config dsn: %w", err)
	}
	q := parsedDSN.Query()
	q.Set("search_path", schema)
	parsedDSN.RawQuery = q.Encode()
	dsnWithSchema := parsedDSN.String()

	// Build pool config.
	poolCfg, err := pgxpool.ParseConfig(dsnWithSchema)
	if err != nil {
		return nil, nil, fmt.Errorf("parse config dsn: %w", err)
	}

	// Worker pools are smaller than the engine since they serve a single group.
	poolCfg.MaxConns = 10
	poolCfg.MinConns = 2

	// Connect to Postgres.
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, nil, fmt.Errorf("connect to config db: %w", err)
	}

	// Ping to verify connection.
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, nil, fmt.Errorf("ping config db: %w", err)
	}

	// Ensure schema exists and apply migrations.
	if err := ensureSchema(ctx, pool, schema); err != nil {
		pool.Close()
		return nil, nil, fmt.Errorf("ensure schema: %w", err)
	}
	if err := migrations.Apply(ctx, pool); err != nil {
		pool.Close()
		return nil, nil, fmt.Errorf("apply migrations: %w", err)
	}

	// Build the PgStore.
	pools := map[string]*pgxpool.Pool{defaultEnv: pool}
	store, err := config.NewPgStore(pools)
	if err != nil {
		pool.Close()
		return nil, nil, fmt.Errorf("build config store: %w", err)
	}

	logger.Info("config store connected",
		slog.String("dsn", redactDSN(dsn)),
		slog.String("schema", schema))

	cleanup := func() {
		pool.Close()
	}

	return store, cleanup, nil
}

// ensureSchema creates the config schema if it doesn't exist.
func ensureSchema(ctx context.Context, pool *pgxpool.Pool, schema string) error {
	_, err := pool.Exec(ctx, fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", schema))
	return err
}

// redactDSN parses a Postgres DSN and blanks the password for logging.
func redactDSN(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return "<unparseable dsn>"
	}
	if u.User != nil {
		if _, hasPw := u.User.Password(); hasPw {
			u.User = url.UserPassword(u.User.Username(), "REDACTED")
		}
	}
	return u.Redacted()
}
