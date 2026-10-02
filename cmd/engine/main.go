// Command engine is the data-plane process: it wires the slices behind the
// frozen seams and serves the thin-slice HTTP edge. CGO_ENABLED=1 is REQUIRED
// because the decision engine links the embedded GoRules ZEN native library; a
// CGO_ENABLED=0 build compiles via the !cgo stub but refuses ZEN at runtime.
//
// Lifecycle (AC-24): load the config seed, build observ + secret provider +
// connect.Registry + decision.Engine + flow.Interpreter + the httpapi handler,
// serve, then on SIGINT/SIGTERM drain the server with a bounded timeout and
// close the registry pools and the decision engine (freeing the Rust-side ZEN
// graphs).
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
	seedPath := flag.String("seed", "internal/config/testdata/seed.json", "path to the config seed JSON")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if err := run(*addr, *seedPath, logger); err != nil {
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

	// --- config store from the seed ---
	store, err := config.LoadSeed(seedPath)
	if err != nil {
		return err
	}
	logger.Info("config seed loaded", slog.String("seed", seedPath))

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

	// --- interpreter + HTTP edge ---
	interp := flow.New()
	srv := httpapi.NewServer(addr, store, interp, httpapi.Deps{
		Conns:  registry,
		Decide: engine,
		Trace:  tracer,
		Log:    obsLog,
	})

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

// jdmLoader adapts config.Store to decision.JDMLoader so the engine resolves JDM
// bytes + version through the store without importing config directly (DIP).
type jdmLoader struct {
	store config.Store
}

// LoadJDM implements decision.JDMLoader.
func (l jdmLoader) LoadJDM(ctx context.Context, env, jdmID string) ([]byte, int, error) {
	return l.store.GetJDM(ctx, env, jdmID)
}
