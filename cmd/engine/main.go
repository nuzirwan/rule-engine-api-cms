// Command engine is the data-plane process: it wires the real v1 slices behind
// the frozen seams and serves the HTTP edge. CGO_ENABLED=1 is REQUIRED because
// the decision engine links the embedded GoRules ZEN native library; a
// CGO_ENABLED=0 build compiles via the !cgo stub but refuses ZEN at runtime.
//
// Lifecycle (AC-24): build the OTel provider + logger + metrics; select the
// config store (real Postgres PgStore when a config DSN is set, else the
// in-memory seed); build the connect.Registry (all drivers incl. valkey) + env
// SecretProvider; the decision.Engine over config.Store.GetJDM; the
// flow.Interpreter with the full node registry; the auth middleware (toggleable);
// and the httpapi handler. Serve, then on SIGINT/SIGTERM drain the server with a
// bounded timeout and close the registry pools + the decision engine + the pgx
// pool, and flush the OTel provider.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"nzr-rules-engine/internal/config"
	"nzr-rules-engine/internal/connect"
	"nzr-rules-engine/internal/connect/drivers"
	"nzr-rules-engine/internal/decision"
	"nzr-rules-engine/internal/flow"
	"nzr-rules-engine/internal/httpapi"
	"nzr-rules-engine/internal/observ"
)

// shutdownTimeout bounds how long graceful drain waits for in-flight requests.
const shutdownTimeout = 15 * time.Second

func main() {
	addr := flag.String("addr", ":8080", "HTTP listen address")
	seedPath := flag.String("seed", "internal/config/testdata/seed.json", "path to the config seed JSON (used when no -config-dsn)")
	configDSN := flag.String("config-dsn", envOr("CONFIG_DSN", ""), "Postgres DSN for the real config store; empty => in-memory seed")
	env := flag.String("env", envOr("ENV", ""), "environment this engine serves (per-env config/JDM)")

	// Auth config (toggleable): auth is ENABLED only when issuer+audience+authz
	// JDM and a key source are all set; otherwise it is skipped with a warning.
	authIssuer := flag.String("auth-issuer", envOr("AUTH_ISSUER", ""), "JWT issuer; set with -auth-audience/-auth-jwks-url/-auth-authz-jdm to enable auth")
	authAudience := flag.String("auth-audience", envOr("AUTH_AUDIENCE", ""), "JWT audience")
	authJWKSURL := flag.String("auth-jwks-url", envOr("AUTH_JWKS_URL", ""), "JWKS endpoint URL")
	authAuthzJDM := flag.String("auth-authz-jdm", envOr("AUTH_AUTHZ_JDM", ""), "ZEN JDM id for authorization")
	authRolesClaim := flag.String("auth-roles-claim", envOr("AUTH_ROLES_CLAIM", ""), "JWT claim carrying roles (default roles)")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg := runConfig{
		addr:      *addr,
		seedPath:  *seedPath,
		configDSN: *configDSN,
		env:       *env,
		auth: httpapi.AuthConfig{
			Issuer:     *authIssuer,
			Audience:   *authAudience,
			JWKSURL:    *authJWKSURL,
			AuthzJDMID: *authAuthzJDM,
			RolesClaim: *authRolesClaim,
		},
	}

	if err := run(cfg, logger); err != nil {
		logger.Error("engine exited with error", slog.Any("err", err))
		os.Exit(1)
	}
	logger.Info("engine stopped cleanly")
}

// runConfig carries the resolved process configuration from flags/env into run.
type runConfig struct {
	addr      string
	seedPath  string
	configDSN string
	env       string
	auth      httpapi.AuthConfig
}

// run builds the process graph, serves until a termination signal, then shuts
// down gracefully. It returns the first fatal error (or nil on a clean stop).
func run(cfg runConfig, logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// --- observability: OTel tracer provider + slog logger + RED metrics ---
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(discardExporter{}))
	tracer := observ.NewOTelTracer(tp)
	obsLog := observ.NewLogger(logger)
	_ = observ.NewMetrics(prometheus.NewRegistry())
	defer func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if ferr := observ.ShutdownProvider(shutCtx, tp); ferr != nil {
			logger.Error("otel provider shutdown", slog.Any("err", ferr))
		}
	}()

	// --- config store selection: real Postgres PgStore when a DSN is set, else
	//     the in-memory seed (local/dev + existing integration test). ---
	store, pool, err := buildStore(ctx, cfg, logger)
	if err != nil {
		return err
	}
	if pool != nil {
		defer pool.Close()
	}

	// --- connection registry over the active connection defs for this env ---
	defs, err := store.Connections(ctx, cfg.env)
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

	// --- auth middleware (toggleable): enabled only when fully configured ---
	authMW, authEnabled, err := httpapi.BuildAuthMiddleware(cfg.auth, engine, obsLog)
	if err != nil {
		return err
	}
	if authEnabled {
		logger.Info("auth ENABLED", slog.String("issuer", cfg.auth.Issuer), slog.String("audience", cfg.auth.Audience))
	} else {
		logger.Warn("auth DISABLED: no issuer/audience/jwks-url/authz-jdm configured; the flow route is unauthenticated")
	}

	// --- interpreter + HTTP edge (flow route + admin endpoints) ---
	interp := flow.New()
	srv := httpapi.NewServer(cfg.addr, store, interp, httpapi.Deps{
		Conns:  registry,
		Decide: engine,
		Trace:  tracer,
		Log:    obsLog,
		Auth:   authMW,
	})

	// Serve in the background; a non-graceful listen failure aborts the process.
	serveErr := make(chan error, 1)
	go func() {
		logger.Info("http server listening", slog.String("addr", cfg.addr))
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

	// Graceful drain with a bounded timeout; the deferred closes release pools,
	// the ZEN graphs, the pgx pool, and flush the OTel provider after the server
	// stops accepting requests.
	shutCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if serr := srv.Shutdown(shutCtx); serr != nil {
		return serr
	}
	logger.Info("http server drained")
	return nil
}

// buildStore selects the config store: when cfg.configDSN is set it opens a pgx
// pool, applies the embedded migrations, and builds the real PgStore over a
// one-entry {env: pool} map; otherwise it loads the in-memory seed. It returns
// the store, the pool (nil for the in-memory path, so the caller skips Close),
// and the first error.
func buildStore(ctx context.Context, cfg runConfig, logger *slog.Logger) (config.Store, *pgxpool.Pool, error) {
	if cfg.configDSN == "" {
		store, err := config.LoadSeed(cfg.seedPath)
		if err != nil {
			return nil, nil, err
		}
		logger.Info("config store: in-memory seed", slog.String("seed", cfg.seedPath), slog.String("env", cfg.env))
		return store, nil, nil
	}

	pool, err := config.OpenPool(ctx, cfg.configDSN)
	if err != nil {
		return nil, nil, err
	}
	if merr := config.Migrate(ctx, pool); merr != nil {
		pool.Close()
		return nil, nil, merr
	}
	store, err := config.NewPgStore(map[string]*pgxpool.Pool{cfg.env: pool})
	if err != nil {
		pool.Close()
		return nil, nil, err
	}
	logger.Info("config store: postgres", slog.String("env", cfg.env))
	return store, pool, nil
}

// discardExporter is a minimal sdktrace.SpanExporter that discards spans. It
// keeps the OTel tracer provider real — so the whole-walk trace exists with a
// trace_id (AC-21) — without pulling an OTLP/stdout exporter dependency into the
// module. A production deploy swaps in a batch OTLP exporter via the same
// NewTracerProvider seam; the exporter choice is a startup detail, not a seam
// change (per the plan's observability note).
type discardExporter struct{}

// ExportSpans implements sdktrace.SpanExporter: it accepts and drops the batch.
func (discardExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error { return nil }

// Shutdown implements sdktrace.SpanExporter.
func (discardExporter) Shutdown(context.Context) error { return nil }

// envOr returns the environment variable named key, or def when it is unset/empty.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
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
