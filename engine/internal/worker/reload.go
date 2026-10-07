package worker

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"nzr-rules-engine/internal/observ"
)

// ReloadConfig configures the hot-reload behavior.
type ReloadConfig struct {
	// PollInterval is how often to check for version changes. Default: 30s.
	PollInterval time.Duration

	// OnReload is called after a successful reload (for testing/observability).
	OnReload func(version int)

	// OnError is called when a reload attempt fails (for testing/observability).
	OnError func(err error)
}

// Reloader manages hot-reload for a worker. It polls for group version changes
// and reloads the worker's configuration when the version changes. It also
// handles SIGHUP for forced reloads.
type Reloader struct {
	worker *Worker
	store  interface {
		GetGroupVersion(ctx context.Context, env, groupID string) (int, error)
	}
	env    string
	log    observ.Logger
	cfg    ReloadConfig
	stopCh chan struct{}
	doneCh chan struct{}
}

// NewReloader creates a new Reloader for the given worker.
func NewReloader(w *Worker, store interface {
	GetGroupVersion(ctx context.Context, env, groupID string) (int, error)
}, env string, log observ.Logger, cfg ReloadConfig) *Reloader {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 30 * time.Second
	}
	return &Reloader{
		worker: w,
		store:  store,
		env:    env,
		log:    log,
		cfg:    cfg,
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
}

// Start begins the hot-reload loop in a background goroutine. It returns
// immediately; call Stop to terminate the loop.
func (r *Reloader) Start(ctx context.Context) {
	go r.run(ctx)
}

// Stop signals the reload loop to stop and waits for it to finish.
func (r *Reloader) Stop() {
	close(r.stopCh)
	<-r.doneCh
}

// run is the main reload loop. It polls for version changes and handles SIGHUP.
func (r *Reloader) run(ctx context.Context) {
	defer close(r.doneCh)

	ticker := time.NewTicker(r.cfg.PollInterval)
	defer ticker.Stop()

	// Set up SIGHUP handler for forced reload.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGHUP)
	defer signal.Stop(sigCh)

	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stopCh:
			return
		case <-sigCh:
			r.logInfo(ctx, "SIGHUP received, forcing reload", nil)
			r.reload(ctx, true)
		case <-ticker.C:
			r.checkAndReload(ctx)
		}
	}
}

// checkAndReload checks if the group version has changed and reloads if so.
func (r *Reloader) checkAndReload(ctx context.Context) {
	currentVersion := r.worker.LoadedVersion()
	if currentVersion == 0 {
		// Not yet loaded, skip version check.
		return
	}

	newVersion, err := r.store.GetGroupVersion(ctx, r.env, r.worker.GroupID())
	if err != nil {
		r.logWarn(ctx, "failed to check group version", map[string]any{"error": err.Error()})
		return
	}

	if newVersion > currentVersion {
		r.logInfo(ctx, "group version changed, reloading", map[string]any{
			"loaded_version": currentVersion,
			"new_version":    newVersion,
		})
		r.reload(ctx, false)
	}
}

// reload performs the actual reload operation.
func (r *Reloader) reload(ctx context.Context, forced bool) {
	if err := r.worker.LoadGroup(ctx); err != nil {
		r.logError(ctx, "failed to reload group", map[string]any{
			"error":  err.Error(),
			"forced": forced,
		})
		if r.cfg.OnError != nil {
			r.cfg.OnError(err)
		}
		return
	}

	newVersion := r.worker.LoadedVersion()
	r.logInfo(ctx, "group reloaded successfully", map[string]any{
		"version": newVersion,
		"forced":  forced,
	})

	if r.cfg.OnReload != nil {
		r.cfg.OnReload(newVersion)
	}
}

// Logging helpers that nil-check the logger.

func (r *Reloader) logInfo(ctx context.Context, msg string, fields map[string]any) {
	if r.log != nil {
		r.log.Emit(ctx, LevelInfo, msg, fields)
	}
}

func (r *Reloader) logWarn(ctx context.Context, msg string, fields map[string]any) {
	if r.log != nil {
		r.log.Emit(ctx, LevelWarn, msg, fields)
	}
}

func (r *Reloader) logError(ctx context.Context, msg string, fields map[string]any) {
	if r.log != nil {
		r.log.Emit(ctx, LevelError, msg, fields)
	}
}
