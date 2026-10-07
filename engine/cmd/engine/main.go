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
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/valkey-io/valkey-go"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"nzr-rules-engine/internal/auth"
	"nzr-rules-engine/internal/config"
	"nzr-rules-engine/internal/connect"
	"nzr-rules-engine/internal/connect/drivers"
	"nzr-rules-engine/internal/decision"
	"nzr-rules-engine/internal/envfile"
	"nzr-rules-engine/internal/flow"
	"nzr-rules-engine/internal/gateway"
	"nzr-rules-engine/internal/httpapi"
	"nzr-rules-engine/internal/observ"
	"nzr-rules-engine/internal/scheduler"
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
	// the original in-memory seed path (no DB touched). adminStore is the SAME
	// *config.PgStore in config-store mode (satisfies httpapi.AdminStore) and nil
	// in in-memory mode (admin writes then return "requires config-store mode"). ---
	store, adminStore, cleanupStore, err := buildStore(ctx, seedPath, logger)
	if err != nil {
		return err
	}
	defer cleanupStore()

	// --- operator-auth for the /admin control plane (deny-by-default). When
	// ADMIN_ENABLED=true, construct the static-token authenticator from
	// ADMIN_TOKENS; a malformed / empty allow-list is a FATAL boot error. When
	// disabled/unset, operAuth stays nil and the admin plane mounts CLOSED. ---
	operAuth, err := buildOperatorAuth()
	if err != nil {
		return err
	}

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

	// Parse scheduler exec timeout from env (default 5m, max 30m).
	execTimeout := parseExecTimeout(logger)

	// --- dispatch config: determines if requests run inline or route to workers ---
	dispatchCfg, err := config.LoadDispatchConfig()
	if err != nil {
		return err
	}
	logger.Info("dispatch config loaded",
		slog.String("mode", string(dispatchCfg.Mode)),
		slog.String("namespace", dispatchCfg.Namespace),
		slog.String("defaultGroup", dispatchCfg.DefaultGroup),
	)

	// --- gateway components (mode=gateway only): k8s client, registry, dispatcher ---
	var dispatcher *gateway.Dispatcher
	var workerRegistry *gateway.WorkerRegistry
	if dispatchCfg.Mode == config.DispatchGateway {
		k8sClient, kerr := buildK8sClient(logger)
		if kerr != nil {
			return fmt.Errorf("gateway mode requires k8s access: %w", kerr)
		}

		workerRegistry = gateway.NewRegistry(k8sClient, dispatchCfg.Namespace, obsLog)
		workerRegistry.Watch(ctx)
		logger.Info("worker registry started", slog.String("namespace", dispatchCfg.Namespace))

		workerClient := gateway.NewWorkerClient(obsLog)
		dispatcher = gateway.NewDispatcher(workerRegistry, workerClient, gateway.DispatchConfig{
			RequestTimeout: dispatchCfg.RequestTimeout,
		}, obsLog)
		logger.Info("gateway dispatcher initialized")
	}

	srv, err := httpapi.NewServer(addr, store, interp, httpapi.Deps{
		Conns:          registry,
		Decide:         engine,
		Trace:          tracer,
		Log:            obsLog,
		Store:          store,
		Metrics:        metricsReg,
		Admin:          adminStore,
		OperAuth:       operAuth,
		ExecTimeout:    execTimeout,
		Dispatcher:     dispatcher,
		DispatchConfig: dispatchCfg,
	})
	if err != nil {
		return err
	}

	// --- scheduler (config-store mode only): a background goroutine that polls
	// enabled schedules and fires flows on their cron schedule. Uses the same
	// Valkey connection for distributed locking if available. ---
	var sched *scheduler.Scheduler
	if adminStore != nil {
		if schedStore, ok := adminStore.(config.ScheduleStore); ok {
			if flowStore, ok2 := adminStore.(scheduler.FlowResolver); ok2 {
				var locker scheduler.DistributedLocker
				if valkeyAddr := os.Getenv("VALKEY_ADDR"); valkeyAddr != "" {
					// Reuse an existing valkey client or create a new one for locking.
					vkClient, verr := valkey.NewClient(valkey.ClientOption{InitAddress: []string{valkeyAddr}, DisableCache: true})
					if verr != nil {
						logger.Warn("scheduler: failed to create valkey locker client", slog.Any("err", verr))
					} else {
						locker = scheduler.NewValkeyLocker(vkClient, obsLog)
						defer vkClient.Close()
					}
				}

				sched = scheduler.New(scheduler.Config{
					Store:        schedStore,
					FlowStore:    flowStore,
					Executor:     interp,
					FlowDeps:     flow.Deps{Conns: registry, Decide: engine, Trace: tracer, Log: obsLog},
					Locker:       locker,
					Log:          obsLog,
					Env:          "",
					ExecTimeout:  execTimeout,
					PollInterval: 30 * time.Second,
				})
				sched.Start(ctx)
				logger.Info("scheduler started", slog.Bool("distributed_lock", locker != nil))
			}
		}
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

	// Stop the scheduler after the HTTP server is drained.
	if sched != nil {
		sched.Stop()
		logger.Info("scheduler stopped")
	}

	// Stop the worker registry if running in gateway mode.
	if workerRegistry != nil {
		workerRegistry.Close()
		logger.Info("worker registry stopped")
	}

	return nil
}

// buildStore selects the run mode. With CONFIG_DSN set it stands up the Postgres
// config store (dedicated schema, migrations, optional Valkey cache) and seeds
// seed.json into it idempotently; otherwise it loads the in-memory seed exactly
// as before. The returned cleanup closes any resources opened for config-store
// mode in LIFO order (cache subscriber, valkey client, pool); it is a no-op in
// in-memory mode.
func buildStore(ctx context.Context, seedPath string, logger *slog.Logger) (config.Store, httpapi.AdminStore, func(), error) {
	dsn := os.Getenv("CONFIG_DSN")
	if dsn == "" {
		// --- in-memory mode: the EXACT original path. No DB touched. The admin
		// store is NIL here (the memStore lacks the admin method-set): admin writes
		// return the "requires config-store mode" error; the data plane serves. ---
		store, err := config.LoadSeed(seedPath)
		if err != nil {
			return nil, nil, nil, err
		}
		logger.Info("config seed loaded (in-memory mode)", slog.String("seed", seedPath))
		return store, nil, func() {}, nil
	}

	// --- config-store mode ---
	schema := os.Getenv("CONFIG_SCHEMA")
	if schema == "" {
		schema = defaultConfigSchema
	}
	if err := config.ValidateSchemaName(schema); err != nil {
		return nil, nil, nil, err
	}

	// Open a pool pinned to the dedicated schema and ensure the schema exists.
	pool, err := config.OpenSchemaPool(ctx, dsn, schema)
	if err != nil {
		return nil, nil, nil, err
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
		return nil, nil, nil, err
	}
	if !hasTables {
		if err := migrations.Apply(ctx, pool); err != nil {
			cleanup()
			return nil, nil, nil, err
		}
	}

	// Optional Valkey cache + invalidation consumer.
	var cache config.Cache
	if addr := os.Getenv("VALKEY_ADDR"); addr != "" {
		client, cerr := valkey.NewClient(valkey.ClientOption{InitAddress: []string{addr}, DisableCache: true})
		if cerr != nil {
			cleanup()
			return nil, nil, nil, cerr
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
		return nil, nil, nil, err
	}

	// Seed seed.json INTO the PgStore idempotently (only if not already seeded).
	raw, err := os.ReadFile(seedPath)
	if err != nil {
		cleanup()
		return nil, nil, nil, err
	}
	seeded, err := config.SeedPgStore(ctx, store, raw)
	if err != nil {
		cleanup()
		return nil, nil, nil, err
	}

	logger.Info("config store connected (config-store mode)",
		slog.String("dsn", redactDSN(dsn)),
		slog.String("schema", schema),
		slog.Bool("cache", cache != nil),
		slog.Bool("seeded", seeded),
		slog.String("seed", seedPath),
	)
	// Same *config.PgStore instance serves BOTH the hot-path config.Store and the
	// admin method-set (httpapi.AdminStore) — slice-f-admin-api.md §2.1a.
	return store, store, cleanup, nil
}

// buildOperatorAuth constructs the operator-plane authenticator from the
// operator-only config keys (never the public JWT keys). ADMIN_ENABLED=true
// builds a StaticTokenOperatorAuth from ADMIN_TOKENS and treats a malformed /
// empty allow-list as a FATAL boot error. When disabled/unset it returns a nil
// authenticator so the admin plane mounts CLOSED (deny-by-default).
func buildOperatorAuth() (auth.OperatorAuthenticator, error) {
	if strings.ToLower(strings.TrimSpace(os.Getenv("ADMIN_ENABLED"))) != "true" {
		return nil, nil // plane disabled => mount-closed
	}
	tokens := os.Getenv("ADMIN_TOKENS")
	oa, err := auth.NewStaticTokenOperatorAuth(tokens)
	if err != nil {
		return nil, fmt.Errorf("admin operator auth: %w", err)
	}
	return oa, nil
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

// parseExecTimeout reads SCHEDULER_EXEC_TIMEOUT from env, defaults to 5m, max 30m.
func parseExecTimeout(logger *slog.Logger) time.Duration {
	timeout := 5 * time.Minute
	if v := os.Getenv("SCHEDULER_EXEC_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			logger.Warn("invalid SCHEDULER_EXEC_TIMEOUT, using default 5m", slog.String("value", v), slog.Any("err", err))
		} else if d > 30*time.Minute {
			logger.Warn("SCHEDULER_EXEC_TIMEOUT exceeds max 30m, capping", slog.String("value", v))
			timeout = 30 * time.Minute
		} else if d > 0 {
			timeout = d
		}
	}
	return timeout
}

// buildK8sClient creates a Kubernetes client for the gateway dispatcher.
// It tries in-cluster config first (when running inside K8s), then falls back to
// kubeconfig from KUBECONFIG env var or default location for local development.
// Returns an error if neither method works (gateway mode requires K8s access).
func buildK8sClient(logger *slog.Logger) (kubernetes.Interface, error) {
	// Try in-cluster config first (running inside K8s pod)
	cfg, err := rest.InClusterConfig()
	if err == nil {
		client, cerr := kubernetes.NewForConfig(cfg)
		if cerr == nil {
			logger.Info("k8s client created (in-cluster)")
			return client, nil
		}
		logger.Warn("k8s client in-cluster config found but client creation failed", slog.Any("err", cerr))
	}

	// Fall back to kubeconfig (local dev)
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		// Try default location
		home, herr := os.UserHomeDir()
		if herr == nil {
			kubeconfig = home + "/.kube/config"
		}
	}

	if kubeconfig != "" {
		cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err == nil {
			client, cerr := kubernetes.NewForConfig(cfg)
			if cerr == nil {
				logger.Info("k8s client created (kubeconfig)", slog.String("kubeconfig", kubeconfig))
				return client, nil
			}
			logger.Warn("k8s client kubeconfig found but client creation failed", slog.Any("err", cerr))
		}
	}

	return nil, fmt.Errorf("no k8s config found: tried in-cluster and kubeconfig (KUBECONFIG=%s)", kubeconfig)
}
