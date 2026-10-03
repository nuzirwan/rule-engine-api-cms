package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"nzr-rules-engine/internal/connect"
	"nzr-rules-engine/internal/observ"
)

// readyzTimeout bounds the readiness probes so a hung dependency cannot wedge
// /readyz; an unresponsive dep surfaces as "not ready" within the window.
const readyzTimeout = 2 * time.Second

// pinger is the subset of config.Store /readyz needs: a cheap reachability
// probe. A nil pinger means no config store is wired (in-memory mode); the
// config-store gate is then skipped.
type pinger interface {
	Ping(ctx context.Context) error
}

// Ops holds the collaborators the operational endpoints gate on. It reuses the
// handler's wired seams (the connection registry for upstream health, the config
// store for its own reachability) and scrapes the metrics gatherer. reg, store
// and gatherer are each nil-safe so the endpoints mount in every run mode.
type Ops struct {
	reg      connect.Registry
	store    pinger
	gatherer prometheus.Gatherer
	log      observ.Logger
}

// newOps builds the ops endpoints from the handler's Deps, reusing Conns for the
// registry health gate and Store for the config-store reachability gate.
func newOps(deps Deps) *Ops {
	var store pinger
	if deps.Store != nil {
		store = deps.Store
	}
	var gatherer prometheus.Gatherer = deps.Metrics
	if gatherer == nil {
		// A nil gatherer still mounts /metrics over an empty registry so the
		// endpoint shape is identical across run modes.
		gatherer = prometheus.NewRegistry()
	}
	return &Ops{
		reg:      deps.Conns,
		store:    store,
		gatherer: gatherer,
		log:      deps.Log,
	}
}

// mount registers the ops endpoints on mux: GET /livez, GET /readyz, GET /metrics.
func (o *Ops) mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /livez", o.livez)
	mux.HandleFunc("GET /readyz", o.readyz)
	mux.Handle("GET /metrics", promhttp.HandlerFor(o.gatherer, promhttp.HandlerOpts{}))
}

// livez is a liveness probe: the process is up and serving, always 200.
func (o *Ops) livez(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// readyz is a readiness probe gating on the connection registry health AND the
// config store reachability. A failing dependency returns 503 and names the
// unready dep; both healthy returns 200. The probes run under a short bounded
// timeout so a hung dependency does not wedge the endpoint.
func (o *Ops) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readyzTimeout)
	defer cancel()

	if o.reg != nil {
		if err := o.reg.HealthCheck(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"notReady": "registry"})
			return
		}
	}
	if o.store != nil {
		if err := o.store.Ping(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"notReady": "config_store"})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready"})
}
