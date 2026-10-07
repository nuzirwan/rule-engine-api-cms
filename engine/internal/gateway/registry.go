package gateway

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"nzr-rules-engine/internal/observ"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	listersv1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
)

const (
	// workerServicePrefix is the naming convention for worker services: worker-{group}.
	workerServicePrefix = "worker-"
	// workerPort is the HTTP port workers listen on.
	workerPort = 8080
	// healthCheckInterval is how often we probe each worker's /healthz endpoint.
	healthCheckInterval = 10 * time.Second
	// healthCheckTimeout is the HTTP timeout for a single /healthz probe.
	healthCheckTimeout = 5 * time.Second
	// informerResyncPeriod is how often the informer does a full re-list from the API server.
	informerResyncPeriod = 30 * time.Second
)

// WorkerRegistry tracks active workers per group with their health state and endpoints.
// It watches K8s Endpoints resources to discover worker pod IPs for services named worker-{group}.
type WorkerRegistry struct {
	mu        sync.RWMutex
	workers   map[string]*WorkerState
	k8sClient kubernetes.Interface
	namespace string
	log       observ.Logger

	// stopCh signals the Watch goroutine and health checker to stop.
	stopCh chan struct{}
	// wg waits for background goroutines to exit on Close.
	wg sync.WaitGroup

	// httpClient is the client used for health checks; injectable for testing.
	httpClient *http.Client
}

// NewRegistry creates a new WorkerRegistry that will watch endpoints in the given namespace.
func NewRegistry(k8sClient kubernetes.Interface, namespace string, log observ.Logger) *WorkerRegistry {
	return &WorkerRegistry{
		workers:   make(map[string]*WorkerState),
		k8sClient: k8sClient,
		namespace: namespace,
		log:       log,
		stopCh:    make(chan struct{}),
		httpClient: &http.Client{
			Timeout: healthCheckTimeout,
		},
	}
}

// GetWorker returns the WorkerState for the given group, and whether it exists.
func (r *WorkerRegistry) GetWorker(group string) (*WorkerState, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	w, ok := r.workers[group]
	if !ok {
		return nil, false
	}
	// Return a copy to prevent callers from mutating internal state.
	copy := *w
	return &copy, true
}

// UpdateHealth updates the health status for the given group's worker.
func (r *WorkerRegistry) UpdateHealth(group string, health HealthStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if w, ok := r.workers[group]; ok {
		w.Health = health
	}
}

// listWorkers returns all known worker group names.
func (r *WorkerRegistry) listWorkers() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	groups := make([]string, 0, len(r.workers))
	for g := range r.workers {
		groups = append(groups, g)
	}
	return groups
}

// MarkLastRequest updates the LastRequest timestamp for the given group.
func (r *WorkerRegistry) MarkLastRequest(group string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if w, ok := r.workers[group]; ok {
		w.LastRequest = time.Now()
	}
}

// Close stops the Watch goroutine and health checker, waiting for them to exit.
func (r *WorkerRegistry) Close() {
	close(r.stopCh)
	r.wg.Wait()
}

// Watch starts background goroutines to watch K8s Endpoints and probe worker health.
// It uses client-go informers to watch Endpoints in the namespace. For each Endpoints
// named worker-{group}, it extracts pod IPs and builds the service endpoint URL.
// Call Close() to stop the watcher.
func (r *WorkerRegistry) Watch(ctx context.Context) {
	factory := informers.NewSharedInformerFactoryWithOptions(
		r.k8sClient,
		informerResyncPeriod,
		informers.WithNamespace(r.namespace),
	)

	endpointsInformer := factory.Core().V1().Endpoints()
	lister := endpointsInformer.Lister().Endpoints(r.namespace)

	// Register event handlers
	_, err := endpointsInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			if ep, ok := obj.(*corev1.Endpoints); ok {
				r.handleEndpointsUpdate(ep)
			}
		},
		UpdateFunc: func(_, newObj interface{}) {
			if ep, ok := newObj.(*corev1.Endpoints); ok {
				r.handleEndpointsUpdate(ep)
			}
		},
		DeleteFunc: func(obj interface{}) {
			if ep, ok := obj.(*corev1.Endpoints); ok {
				r.handleEndpointsDelete(ep)
			}
		},
	})
	if err != nil {
		r.log.Emit(ctx, "error", "registry.watch.handler_add_failed", map[string]any{"error": err.Error()})
		return
	}

	// Start the informer
	factory.Start(r.stopCh)

	// Wait for the informer cache to sync
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		if !cache.WaitForCacheSync(r.stopCh, endpointsInformer.Informer().HasSynced) {
			r.log.Emit(ctx, "error", "registry.watch.sync_failed", nil)
			return
		}
		r.log.Emit(ctx, "info", "registry.watch.synced", map[string]any{"namespace": r.namespace})

		// Initial population from lister
		r.syncFromLister(ctx, lister)
	}()

	// Start background health checker
	r.wg.Add(1)
	go r.healthCheckLoop(ctx)
}

// syncFromLister performs an initial sync of all worker endpoints from the lister.
func (r *WorkerRegistry) syncFromLister(ctx context.Context, lister listersv1.EndpointsNamespaceLister) {
	endpoints, err := lister.List(labels.Everything())
	if err != nil {
		r.log.Emit(ctx, "error", "registry.sync.list_failed", map[string]any{"error": err.Error()})
		return
	}
	for _, ep := range endpoints {
		r.handleEndpointsUpdate(ep)
	}
}

// handleEndpointsUpdate processes an Endpoints add or update event.
func (r *WorkerRegistry) handleEndpointsUpdate(ep *corev1.Endpoints) {
	group := extractGroupFromServiceName(ep.Name)
	if group == "" {
		return // Not a worker service
	}

	// Count ready addresses across all subsets
	readyCount := 0
	for _, subset := range ep.Subsets {
		readyCount += len(subset.Addresses)
	}

	// Build the service endpoint URL
	endpoint := fmt.Sprintf("http://%s.%s.svc:%d", ep.Name, r.namespace, workerPort)

	r.mu.Lock()
	defer r.mu.Unlock()

	existing, exists := r.workers[group]
	if exists {
		// Preserve health and last request timestamp from existing state
		existing.Endpoint = endpoint
		existing.Ready = readyCount > 0
		existing.Replicas = readyCount
	} else {
		r.workers[group] = &WorkerState{
			Group:    group,
			Endpoint: endpoint,
			Ready:    readyCount > 0,
			Replicas: readyCount,
			Health:   HealthUnknown,
		}
	}
}

// handleEndpointsDelete processes an Endpoints delete event.
func (r *WorkerRegistry) handleEndpointsDelete(ep *corev1.Endpoints) {
	group := extractGroupFromServiceName(ep.Name)
	if group == "" {
		return // Not a worker service
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.workers, group)
}

// extractGroupFromServiceName extracts the group name from a service named "worker-{group}".
// Returns empty string if the service name doesn't match the pattern.
func extractGroupFromServiceName(name string) string {
	if !strings.HasPrefix(name, workerServicePrefix) {
		return ""
	}
	return strings.TrimPrefix(name, workerServicePrefix)
}

// healthCheckLoop periodically probes /healthz on all known workers.
func (r *WorkerRegistry) healthCheckLoop(ctx context.Context) {
	defer r.wg.Done()

	ticker := time.NewTicker(healthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-r.stopCh:
			return
		case <-ticker.C:
			r.probeAllWorkers(ctx)
		}
	}
}

// probeAllWorkers checks /healthz on all registered workers.
func (r *WorkerRegistry) probeAllWorkers(ctx context.Context) {
	groups := r.listWorkers()
	for _, group := range groups {
		r.probeWorker(ctx, group)
	}
}

// probeWorker checks /healthz on a single worker and updates its health status.
func (r *WorkerRegistry) probeWorker(ctx context.Context, group string) {
	r.mu.RLock()
	worker, ok := r.workers[group]
	if !ok {
		r.mu.RUnlock()
		return
	}
	endpoint := worker.Endpoint
	r.mu.RUnlock()

	healthURL := endpoint + "/healthz"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
	if err != nil {
		r.UpdateHealth(group, HealthUnhealthy)
		return
	}

	resp, err := r.httpClient.Do(req)
	if err != nil {
		r.UpdateHealth(group, HealthUnhealthy)
		r.log.Emit(ctx, "debug", "registry.health.probe_failed", map[string]any{
			"group": group,
			"error": err.Error(),
		})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		r.UpdateHealth(group, HealthHealthy)
	} else {
		r.UpdateHealth(group, HealthUnhealthy)
		r.log.Emit(ctx, "debug", "registry.health.unhealthy", map[string]any{
			"group":  group,
			"status": resp.StatusCode,
		})
	}
}
