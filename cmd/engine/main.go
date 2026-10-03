// Command engine is the data-plane process: it wires the slices behind the
// frozen seams and serves the thin-slice HTTP edge. CGO_ENABLED=1 is REQUIRED
// because the decision engine links the embedded GoRules ZEN native library; a
// CGO_ENABLED=0 build compiles via the !cgo stub but refuses ZEN at runtime.
//
// Run modes (selected after loading the optional .env):
//   - CONFIG-STORE mode (CONFIG_DSN set): connect to a real Postgres as the
//     engine's OWN config backing store. Config tables live in a dedicated schema
//     (CONFIG_SCHEMA, default "rule_engine"), never public. On start: create the
//     schema + set search_path, apply migrations, build the Valkey cache
//     (VALKEY_ADDR, optional) with its invalidation consumer, build a PgStore,
//     and seed the committed seed.json INTO Postgres idempotently through the
//     store. Then serve from the PgStore.
//   - IN-MEMORY mode (CONFIG_DSN unset): the original path — LoadSeed into a
//     memStore. No database is touched.
//
// Lifecycle (AC-24): build observ + secret provider + connect.Registry +
// decision.Engine + flow.Interpreter + the httpapi handler, serve, then on
// SIGINT/SIGTERM drain the server with a bounded timeout and close the registry
// pools, the decision engine (freeing the Rust-side ZEN graphs), and (in
// config-store mode) the cache, valkey client, and config pool in LIFO order.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/valkey-io/valkey-go"

	"nzr-rules-engine/internal/config"
	"nzr-rules-engine/internal/connect"
	"nzr-rules-engine/internal/connect/drivers"
	"nzr-rules-engine/internal/decision"
	"nzr-rules-engine/internal/envfile"
	"nzr-rules-engine/internal/flow"
	"nzr-rules-engine/internal/httpapi"
	"nzr-rules-engine/internal/observ"
	"nzr-rules-engine/migrations"
)

// shutdownTimeout bounds how long graceful drain waits for in-flight requests.
const shutdownTimeout = 15 * time.Second

// defaultConfigSchema is the dedicated Postgres schema the config store uses when
// CONFIG_SCHEMA is unset. Config tables never live in public.
const defaultConfigSchema = "rule_engine"

func main() {
	addr := flag.String("addr", ":8080", "HTTP listen address (overridden by ENGINE_ADDR when set)")
	seedPath := flag.String("seed", "internal/config/testdata/seed.json", "path to the config seed JSON")
	envPath := flag.String("env", ".env", "path to an optional .env file (KEY=VALUE); missing is fine")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	// Load the optional .env BEFORE reading any config env (process env wins).
	if err := envfile.Load(*envPath); err != nil {
		logger.Error("load env file", slog.Any("err", err))
		os.Exit(1)
	}

	// ENGINE_ADDR, when set, overrides the -addr flag default.
	listenAddr := *addr
	if v := os.Getenv("ENGINE_ADDR"); v != "" {
		listenAddr = v
	}

	if err := run(listenAddr, *seedPath, logger); err != nil {
		logger.Error("engine exited with error", slog.Any("err", err))
		os.Exit(1)
	}
	logger.Info("engine stopped cleanly")
}

// run builds the process graph, serves until a termination signal, then shuts
// down gracefully. It returns the first fatal error (or nil on a clean stop).
func run(addr, seedPath string, logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	tracer := observ.NewTracer(logger)
	obsLog := observ.NewLogger(logger)

	// --- config store: Postgres config-store mode when CONFIG_DSN is set, else
	// the original in-memory seed path (no DB touched). ---
	store, cleanupStore, err := buildStore(ctx, seedPath, logger)
	if err != nil {
		return err
	}
	defer cleanupStore()

	// --- connection registry over the seeded defs ---
	defs, err := store.Connections(ctx, "")
	if err != nil {
		return err
	}
	secrets := connect.NewEnvSecretProvider()
	registry, err := connect.New(drivers.All(), defs, secrets, tracer, obsLog)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := registry.Close(); cerr != nil {
			logger.Error("registry close", slog.Any("err", cerr))
		}
	}()
	logger.Info("connection registry built", slog.Int("connections", len(defs)))

	// --- decision engine (ZEN via cgo), loading JDM through config.Store ---
	engine := decision.New(
		jdmLoader{store: store},
		decision.WithLogger(obsLog),
		decision.WithTracer(tracer),
	)
	defer func() {
		if cerr := engine.Close(); cerr != nil {
			logger.Error("decision engine close", slog.Any("err", cerr))
		}
	}()

	// --- metrics: a dedicated registry fronts /metrics; observ.NewMetrics
	// registers the RED collectors on it. The same registry is the Gatherer the
	// ops endpoint scrapes. ---
	metricsReg := prometheus.NewRegistry()
	_ = observ.NewMetrics(metricsReg)

	// --- interpreter + HTTP edge (config-driven router + ops endpoints) ---
	interp := flow.New()
	srv, err := httpapi.NewServer(addr, store, interp, httpapi.Deps{
		Conns:   registry,
		Decide:  engine,
		Trace:   tracer,
		Log:     obsLog,
		Store:   store,
		Metrics: metricsReg,
	})
	if err != nil {
		return err
	}

	// Serve in the background; a non-graceful listen failure aborts the process.
	serveErr := make(chan error, 1)
	go func() {
		logger.Info("http server listening", slog.String("addr", addr))
		if lerr := srv.ListenAndServe(); lerr != nil && !errors.Is(lerr, http.ErrServerClosed) {
			serveErr <- lerr
			return
		}
		serveErr <- nil
	}()

	// Block until a termination signal or a serve failure.
	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received, draining")
	case lerr := <-serveErr:
		return lerr
	}

	// Graceful drain with a bounded timeout; the deferred closes release pools
	// and the ZEN graphs after the server stops accepting requests.
	shutCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if serr := srv.Shutdown(shutCtx); serr != nil {
		return serr
	}
	logger.Info("http server drained")
	return nil
}

// buildStore selects the run mode. With CONFIG_DSN set it stands up the Postgres
// config store (dedicated schema, migrations, optional Valkey cache) and seeds
// seed.json into it idempotently; otherwise it loads the in-memory seed exactly
// as before. The returned cleanup closes any resources opened for config-store
// mode in LIFO order (cache subscriber, valkey client, pool); it is a no-op in
// in-memory mode.
func buildStore(ctx context.Context, seedPath string, logger *slog.Logger) (config.Store, func(), error) {
	dsn := os.Getenv("CONFIG_DSN")
	if dsn == "" {
		// --- in-memory mode: the EXACT original path. No DB touched. ---
		store, err := config.LoadSeed(seedPath)
		if err != nil {
			return nil, nil, err
		}
		logger.Info("config seed loaded (in-memory mode)", slog.String("seed", seedPath))
		return store, func() {}, nil
	}

	// --- config-store mode ---
	schema := os.Getenv("CONFIG_SCHEMA")
	if schema == "" {
		schema = defaultConfigSchema
	}
	if err := config.ValidateSchemaName(schema); err != nil {
		return nil, nil, err
	}

	// Open a pool pinned to the dedicated schema and ensure the schema exists.
	pool, err := config.OpenSchemaPool(ctx, dsn, schema)
	if err != nil {
		return nil, nil, err
	}
	// Track resources for LIFO cleanup; close them if a later step fails.
	var cleanups []func()
	cleanup := func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}
	cleanups = append(cleanups, pool.Close)

	// Apply migrations only if the config tables are not already present (a second
	// process start against an already-migrated schema must not re-run the bare
	// CREATE TABLE DDL).
	hasTables, err := config.SchemaHasConfigTables(ctx, pool, schema)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	if !hasTables {
		if err := migrations.Apply(ctx, pool); err != nil {
			cleanup()
			return nil, nil, err
		}
	}

	// Optional Valkey cache + invalidation consumer.
	var cache config.Cache
	if addr := os.Getenv("VALKEY_ADDR"); addr != "" {
		client, cerr := valkey.NewClient(valkey.ClientOption{InitAddress: []string{addr}, DisableCache: true})
		if cerr != nil {
			cleanup()
			return nil, nil, cerr
		}
		cleanups = append(cleanups, client.Close)
		vc := config.NewValkeyCache(client)
		vc.StartInvalidationConsumer(ctx)
		cleanups = append(cleanups, func() { _ = vc.Close() })
		cache = vc
		logger.Info("config cache enabled", slog.String("valkey", addr))
	}

	opts := []config.PgOption{}
	if cache != nil {
		opts = append(opts, config.WithCache(cache))
	}
	store, err := config.NewPgStore(map[string]*pgxpool.Pool{"": pool}, opts...)
	if err != nil {
		cleanup()
		return nil, nil, err
	}

	// Seed seed.json INTO the PgStore idempotently (only if not already seeded).
	raw, err := os.ReadFile(seedPath)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	seeded, err := config.SeedPgStore(ctx, store, raw)
	if err != nil {
		cleanup()
		return nil, nil, err
	}

	logger.Info("config store connected (config-store mode)",
		slog.String("dsn", redactDSN(dsn)),
		slog.String("schema", schema),
		slog.Bool("cache", cache != nil),
		slog.Bool("seeded", seeded),
		slog.String("seed", seedPath),
	)
	return store, cleanup, nil
}

// redactDSN parses a Postgres DSN and blanks the password so a connection log
// never leaks a secret. A DSN that does not parse is reduced to a safe marker
// rather than echoed verbatim.
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

// jdmLoader adapts config.Store to decision.JDMLoader so the engine resolves JDM
// bytes + version through the store without importing config directly (DIP).
type jdmLoader struct {
	store config.Store
}

// LoadJDM implements decision.JDMLoader.
func (l jdmLoader) LoadJDM(ctx context.Context, env, jdmID string) ([]byte, int, error) {
	return l.store.GetJDM(ctx, env, jdmID)
}
