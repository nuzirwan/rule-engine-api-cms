// Package gateway provides the gateway dispatcher for routing requests to worker
// pods. It manages worker discovery via K8s Endpoints, tracks worker health, and
// dispatches execute requests to the appropriate worker based on group.
package gateway

import "time"

// HealthStatus indicates the current health state of a worker endpoint.
type HealthStatus int

const (
	// HealthUnknown is the default state when a worker has not been probed yet.
	HealthUnknown HealthStatus = iota
	// HealthHealthy indicates the worker's /healthz endpoint returned 2xx.
	HealthHealthy
	// HealthUnhealthy indicates the worker's /healthz endpoint returned non-2xx or failed.
	HealthUnhealthy
)

// String returns a human-readable representation of the health status.
func (h HealthStatus) String() string {
	switch h {
	case HealthHealthy:
		return "healthy"
	case HealthUnhealthy:
		return "unhealthy"
	default:
		return "unknown"
	}
}

// WorkerState tracks the current state of a worker endpoint for a specific group.
type WorkerState struct {
	// Group is the worker group identifier (e.g., "default", "high-priority").
	Group string
	// Endpoint is the HTTP URL to reach this worker (e.g., http://worker-default.flow-workers.svc:8080).
	Endpoint string
	// Ready indicates whether the worker is ready to receive requests (endpoints subset addresses present).
	Ready bool
	// Replicas is the number of ready pod IPs backing the service endpoint.
	Replicas int
	// LastRequest is the timestamp of the last request dispatched to this worker.
	LastRequest time.Time
	// Health is the result of the last /healthz probe.
	Health HealthStatus
}
