package worker

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"nzr-rules-engine/internal/config"
	"nzr-rules-engine/internal/connect"
	"nzr-rules-engine/internal/connect/drivers"
	"nzr-rules-engine/internal/decision"
	"nzr-rules-engine/internal/flow"
	"nzr-rules-engine/internal/observ"
)

// Log level constants (matching observ.parseLevel).
const (
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"
	LevelDebug = "debug"
)

// Worker holds the loaded configuration for a single group and executes flows.
// It is the core runtime for the worker binary, managing flows, JDMs, and
// connections in isolation from other groups.
type Worker struct {
	// Immutable after construction.
	groupID string
	env     string
	store   config.WorkerStore
	tracer  observ.Tracer
	log     observ.Logger
	metrics *WorkerMetrics

	// Mutable state protected by mu. The atomic swap pattern (LoadGroup builds a
	// new state, then swaps) ensures in-flight requests are not interrupted.
	mu    sync.RWMutex
	state *loadedState

	// ready is set to 1 when the first successful LoadGroup completes. It is
	// checked by /readyz to gate traffic until the worker is initialized.
	ready atomic.Int32
}

// loadedState holds the configuration snapshot loaded from the store. It is
// replaced atomically on hot-reload; in-flight requests continue using their
// captured state reference until completion.
type loadedState struct {
	groupVersion int
	flows        map[string]*config.FlowVersion // flowID -> FlowVersion
	jdmBytes     map[string][]byte              // jdmID -> JDM content
	connKeys     []string                       // connection keys for debug output
	registry     connect.Registry               // connection pools for this group
	decide       *decision.Engine               // JDM evaluator
	interp       *flow.Interpreter              // flow interpreter (stateless)
}

// Config holds the construction options for a Worker.
type Config struct {
	GroupID string
	Env     string
	Store   config.WorkerStore
	Tracer  observ.Tracer
	Log     observ.Logger
	Metrics *WorkerMetrics
}

// New creates a Worker for the given group. The worker is not ready until
// LoadGroup is called successfully.
func New(cfg Config) *Worker {
	return &Worker{
		groupID: cfg.GroupID,
		env:     cfg.Env,
		store:   cfg.Store,
		tracer:  cfg.Tracer,
		log:     cfg.Log,
		metrics: cfg.Metrics,
	}
}

// LoadGroup loads (or reloads) the group's configuration from the store. It
// fetches the group, its flows, connections, and JDMs, builds a new loadedState,
// and atomically swaps it in. The old state's resources are closed after the swap.
func (w *Worker) LoadGroup(ctx context.Context) error {
	// 1. Fetch the group and its connection list.
	group, err := w.store.GetGroup(ctx, w.env, w.groupID)
	if err != nil {
		return fmt.Errorf("get group: %w", err)
	}

	// 2. Fetch all active flows for this group.
	flows, err := w.store.GetActiveFlowsForGroup(ctx, w.env, w.groupID)
	if err != nil {
		return fmt.Errorf("get flows for group: %w", err)
	}

	// 3. Fetch connections for this group.
	connDefs, err := w.store.GetConnectionsForGroup(ctx, w.env, group.Connections)
	if err != nil {
		return fmt.Errorf("get connections for group: %w", err)
	}

	// 4. Fetch all JDMs referenced by the flows.
	jdmBytes, err := w.store.GetJDMsForFlows(ctx, w.env, flows)
	if err != nil {
		return fmt.Errorf("get jdms for flows: %w", err)
	}

	// 5. Build the connection registry with only this group's connections.
	// Each worker creates its own pools (no sharing across workers).
	registry, err := connect.New(
		drivers.All(), // connectors from the drivers package
		connDefs,
		nil, // secrets — nil uses env-based secret provider
		w.tracer,
		w.log,
	)
	if err != nil {
		return fmt.Errorf("build connection registry: %w", err)
	}

	// 6. Build the decision engine with a loader that returns preloaded JDM bytes.
	loader := &preloadedJDMLoader{jdms: jdmBytes}
	decide := decision.New(
		loader,
		decision.WithLogger(w.log),
		decision.WithTracer(w.tracer),
	)

	// 7. Build the flow map.
	flowMap := make(map[string]*config.FlowVersion, len(flows))
	for i := range flows {
		flowMap[flows[i].FlowID] = &flows[i]
	}

	// 8. Build the new state.
	newState := &loadedState{
		groupVersion: group.Version,
		flows:        flowMap,
		jdmBytes:     jdmBytes,
		connKeys:     group.Connections,
		registry:     registry,
		decide:       decide,
		interp:       flow.New(),
	}

	// 9. Atomically swap the state. Close the old state's resources AFTER the swap.
	w.mu.Lock()
	oldState := w.state
	w.state = newState
	w.mu.Unlock()

	// Close old resources (LIFO order) outside the lock.
	if oldState != nil {
		if oldState.decide != nil {
			_ = oldState.decide.Close()
		}
		if oldState.registry != nil {
			_ = oldState.registry.Close()
		}
	}

	// Mark ready after the first successful load.
	w.ready.Store(1)

	// Emit metrics after successful load.
	if w.metrics != nil {
		w.metrics.SetConfigVersion(w.groupID, group.Version)
		w.metrics.SetFlowsLoaded(w.groupID, len(flows))
		w.metrics.SetJDMsLoaded(w.groupID, len(jdmBytes))
		// Set connections active (1 for each connection key since pools are initialized).
		for _, connKey := range group.Connections {
			w.metrics.SetConnectionsActive(w.groupID, connKey, 1)
		}
	}

	if w.log != nil {
		w.log.Emit(ctx, LevelInfo, "group loaded", map[string]any{
			"group":       w.groupID,
			"version":     group.Version,
			"flows":       len(flows),
			"connections": len(connDefs),
			"jdms":        len(jdmBytes),
		})
	}

	return nil
}

// Execute runs a flow by ID with the given input. It returns the flow's response
// or an error if execution fails.
func (w *Worker) Execute(ctx context.Context, flowID string, reqID, traceID string, input map[string]any) (map[string]any, error) {
	// Get a consistent view of the loaded state.
	w.mu.RLock()
	state := w.state
	w.mu.RUnlock()

	if state == nil {
		return nil, fmt.Errorf("worker not loaded")
	}

	// Look up the flow.
	fv, ok := state.flows[flowID]
	if !ok {
		return nil, fmt.Errorf("flow not found: %s", flowID)
	}

	// Build the flow context.
	fc := flow.NewCtx(reqID, traceID, w.env, input)

	// Build the flow dependencies.
	deps := flow.Deps{
		Conns:  state.registry,
		Decide: state.decide,
		Trace:  w.tracer,
		Log:    w.log,
	}

	// Run the flow.
	ver := flow.Version{FlowID: fv.FlowID, Version: fv.Version}
	if err := state.interp.Run(ctx, &fv.Tree, ver, fc, deps); err != nil {
		return nil, err
	}

	return fc.Response, nil
}

// Ready returns true if the worker has successfully loaded its configuration
// and is ready to serve requests.
func (w *Worker) Ready() bool {
	return w.ready.Load() == 1 && w.connectionsHealthy()
}

// connectionsHealthy checks if the connection registry passes health checks.
func (w *Worker) connectionsHealthy() bool {
	w.mu.RLock()
	state := w.state
	w.mu.RUnlock()

	if state == nil || state.registry == nil {
		return false
	}

	// Try a health check with a short timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	return state.registry.HealthCheck(ctx) == nil
}

// GroupID returns the worker's assigned group ID.
func (w *Worker) GroupID() string { return w.groupID }

// Metrics returns the worker's metrics instance (may be nil).
func (w *Worker) Metrics() *WorkerMetrics { return w.metrics }

// LoadedVersion returns the currently loaded group version, or 0 if not loaded.
func (w *Worker) LoadedVersion() int {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.state == nil {
		return 0
	}
	return w.state.groupVersion
}

// DebugConfig returns the current loaded configuration for debugging.
func (w *Worker) DebugConfig() DebugConfig {
	w.mu.RLock()
	state := w.state
	w.mu.RUnlock()

	if state == nil {
		return DebugConfig{GroupID: w.groupID, Ready: false}
	}

	flowIDs := make([]string, 0, len(state.flows))
	for id := range state.flows {
		flowIDs = append(flowIDs, id)
	}

	jdmIDs := make([]string, 0, len(state.jdmBytes))
	for id := range state.jdmBytes {
		jdmIDs = append(jdmIDs, id)
	}

	return DebugConfig{
		GroupID:        w.groupID,
		GroupVersion:   state.groupVersion,
		FlowIDs:        flowIDs,
		ConnectionKeys: state.connKeys,
		JDMIDs:         jdmIDs,
		Ready:          w.Ready(),
	}
}

// Close releases all resources held by the worker. It should be called on
// graceful shutdown after draining in-flight requests.
func (w *Worker) Close() error {
	w.mu.Lock()
	state := w.state
	w.state = nil
	w.mu.Unlock()

	if state == nil {
		return nil
	}

	var firstErr error
	if state.decide != nil {
		if err := state.decide.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if state.registry != nil {
		if err := state.registry.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	w.ready.Store(0)
	return firstErr
}

// preloadedJDMLoader satisfies decision.JDMLoader by returning preloaded JDM
// bytes. The worker loads all JDMs at startup and passes them to the decision
// engine through this loader.
type preloadedJDMLoader struct {
	jdms map[string][]byte
}

// LoadJDM implements decision.JDMLoader. It returns the preloaded JDM bytes
// and version 0 (the decision engine's cache key includes the version, so we
// use a constant version since the bytes are already loaded).
func (l *preloadedJDMLoader) LoadJDM(ctx context.Context, env, jdmID string) ([]byte, int, error) {
	b, ok := l.jdms[jdmID]
	if !ok {
		return nil, 0, fmt.Errorf("jdm not found: %s", jdmID)
	}
	return b, 0, nil
}
