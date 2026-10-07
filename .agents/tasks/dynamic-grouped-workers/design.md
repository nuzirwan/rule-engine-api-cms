# Dynamic Grouped Flow Workers — Design Document

## Overview

Extend the rules engine to support **grouped flow deployment** with **dynamic worker spawning**. The engine acts as a gateway/router that dispatches requests to isolated worker pods, where each worker handles a specific group of flows.

## Goals

1. **Isolation** — Flows in different groups run in separate processes
2. **Security** — Each group has access only to its own secrets/connections
3. **Independent scaling** — Hot groups scale up, idle groups scale to zero
4. **Cost efficiency** — No resources consumed by idle groups
5. **Blast radius** — Failure in one group doesn't affect others

## Non-Goals (v1)

- Per-request ephemeral pods (too slow for sync HTTP)
- WASM-based isolation (future consideration)
- Cross-group transactions
- Worker-to-worker communication

---

## Architecture

```
                    ┌─────────────────────────────────────────────────┐
                    │                 Load Balancer                    │
                    └──────────────────────┬──────────────────────────┘
                                           │
                    ┌──────────────────────▼──────────────────────────┐
                    │              Engine (Gateway Mode)               │
                    │                                                  │
                    │  ┌────────────────────────────────────────────┐ │
                    │  │  HTTP Handler                               │ │
                    │  │  - Receive request                          │ │
                    │  │  - Resolve flow by method+path              │ │
                    │  │  - Lookup group for flow                    │ │
                    │  └──────────────────┬─────────────────────────┘ │
                    │                     │                           │
                    │  ┌──────────────────▼─────────────────────────┐ │
                    │  │  Dispatcher                                 │ │
                    │  │  - Check worker health                      │ │
                    │  │  - Ensure worker running (spawn if needed)  │ │
                    │  │  - Forward request to worker                │ │
                    │  │  - Return response                          │ │
                    │  └──────────────────┬─────────────────────────┘ │
                    │                     │                           │
                    │  ┌──────────────────▼─────────────────────────┐ │
                    │  │  Worker Registry                            │ │
                    │  │  - Track active workers                     │ │
                    │  │  - Health status per worker                 │ │
                    │  │  - Endpoint discovery                       │ │
                    │  └────────────────────────────────────────────┘ │
                    └──────────────────────┬──────────────────────────┘
                                           │
           ┌───────────────────────────────┼───────────────────────────────┐
           │                               │                               │
    ┌──────▼───────┐               ┌───────▼──────┐               ┌───────▼──────┐
    │worker-orders │               │worker-payments│              │worker-webhooks│
    │              │               │               │               │              │
    │ Flows:       │               │ Flows:        │               │ Flows:       │
    │ - order-get  │               │ - pay-process │               │ - stripe-wh  │
    │ - order-create│              │ - pay-refund  │               │ - github-wh  │
    │ - order-list │               │ - txn-get     │               │              │
    │              │               │               │               │              │
    │ Secrets:     │               │ Secrets:      │               │ Secrets:     │
    │ - orders-db  │               │ - payments-db │               │ - wh-signing │
    │              │               │ - stripe-key  │               │              │
    │              │               │               │               │              │
    │ Replicas: 3  │               │ Replicas: 0→5 │               │ Replicas: 0→2│
    │ (always-on)  │               │ (scale-to-0)  │               │ (scale-to-0) │
    └──────────────┘               └───────────────┘               └──────────────┘
```

---

## Data Model

### Flow config (extended)

```go
// config/flow.go
type FlowVersion struct {
    FlowID   string   `json:"flowId"`
    Version  int      `json:"version"`
    Method   string   `json:"method"`
    Path     string   `json:"path"`
    Group    string   `json:"group"`    // NEW: group assignment
    Tree     Node     `json:"tree"`
    Fixtures []Fixture `json:"fixtures,omitempty"`
}
```

### Group config

```go
// config/group.go
type Group struct {
    ID          string        `json:"id"`
    Name        string        `json:"name"`
    Flows       []string      `json:"flows"`       // Flow IDs in this group
    Connections []string      `json:"connections"` // Allowed connection keys
    Scaling     ScalingConfig `json:"scaling"`
}

type ScalingConfig struct {
    Mode            ScalingMode `json:"mode"`            // static | dynamic | ephemeral
    MinReplicas     int         `json:"minReplicas"`     // 0 for scale-to-zero
    MaxReplicas     int         `json:"maxReplicas"`
    ScaleDownDelay  Duration    `json:"scaleDownDelay"`  // Idle time before scale down
    StartupTimeout  Duration    `json:"startupTimeout"`  // Max time to wait for worker
    Resources       Resources   `json:"resources"`       // CPU/memory limits
}

type ScalingMode string
const (
    ScalingStatic    ScalingMode = "static"    // Always minReplicas running
    ScalingDynamic   ScalingMode = "dynamic"   // Scale 0↔N with KEDA
    ScalingEphemeral ScalingMode = "ephemeral" // Spawn on demand, short TTL
)
```

### Dispatch config (engine)

```go
// config/dispatch.go
type DispatchConfig struct {
    Mode           DispatchMode `json:"mode"`           // inline | gateway
    WorkerImage    string       `json:"workerImage"`    // Docker image for workers
    Namespace      string       `json:"namespace"`      // K8s namespace for workers
    ServiceAccount string       `json:"serviceAccount"` // K8s SA for workers
    DefaultGroup   string       `json:"defaultGroup"`   // Fallback group for ungrouped flows
}

type DispatchMode string
const (
    DispatchInline  DispatchMode = "inline"  // Current behavior: run in engine
    DispatchGateway DispatchMode = "gateway" // New: dispatch to workers
)
```

---

## Components

### 1. Engine (Gateway Mode)

When `dispatch.mode = "gateway"`, the engine becomes a routing layer.

**Request flow:**
```
1. HTTP request arrives
2. Resolve flow by method+path (existing logic)
3. Lookup flow's group
4. Dispatcher ensures worker is ready
5. Forward request to worker: POST http://worker-{group}:8080/execute
6. Return worker's response to client
```

**New packages:**
```
engine/internal/
  gateway/
    dispatcher.go     # Request dispatcher
    registry.go       # Worker registry (health, endpoints)
    scaler.go         # K8s scaling operations
    client.go         # HTTP client to workers
```

### 2. Worker Binary

A slim version of the engine that:
- Loads only flows for its assigned group
- Loads only connections allowed for that group
- Exposes `POST /execute` endpoint
- Reports health at `GET /healthz`

**New cmd:**
```
engine/cmd/worker/main.go
```

**Worker API:**
```
POST /execute
{
  "flowId": "order-get",
  "requestId": "req-123",
  "traceId": "trace-456",
  "input": { ... }
}

Response:
{
  "status": 200,
  "response": { ... },
  "error": null
}
```

### 3. Worker Registry

Tracks active workers and their state.

```go
// gateway/registry.go
type WorkerRegistry struct {
    mu      sync.RWMutex
    workers map[string]*WorkerState // group → state
    k8s     kubernetes.Interface
}

type WorkerState struct {
    Group       string
    Endpoint    string       // e.g., "http://worker-orders:8080"
    Ready       bool
    Replicas    int
    LastRequest time.Time
    Health      HealthStatus
}

func (r *WorkerRegistry) GetWorker(group string) (*WorkerState, bool)
func (r *WorkerRegistry) UpdateHealth(group string, health HealthStatus)
func (r *WorkerRegistry) Watch(ctx context.Context) // K8s endpoint watcher
```

### 4. Dispatcher

Routes requests to workers, handling spawn/scale as needed.

```go
// gateway/dispatcher.go
type Dispatcher struct {
    registry *WorkerRegistry
    scaler   *Scaler
    client   *http.Client
    config   DispatchConfig
}

func (d *Dispatcher) Dispatch(ctx context.Context, flow *FlowVersion, input map[string]any) (*ExecuteResponse, error) {
    group := flow.Group
    if group == "" {
        group = d.config.DefaultGroup
    }
    
    // Ensure worker is ready
    worker, err := d.ensureWorker(ctx, group)
    if err != nil {
        return nil, fmt.Errorf("worker not ready: %w", err)
    }
    
    // Forward request
    return d.forward(ctx, worker, flow.FlowID, input)
}

func (d *Dispatcher) ensureWorker(ctx context.Context, group string) (*WorkerState, error) {
    worker, exists := d.registry.GetWorker(group)
    
    if !exists || !worker.Ready {
        // Scale up or wait for ready
        if err := d.scaler.EnsureReady(ctx, group); err != nil {
            return nil, err
        }
        // Re-fetch after scaling
        worker, _ = d.registry.GetWorker(group)
    }
    
    return worker, nil
}
```

### 5. Scaler

Manages K8s deployments/scaling for workers.

```go
// gateway/scaler.go
type Scaler struct {
    k8s       kubernetes.Interface
    namespace string
    groups    map[string]*Group
}

func (s *Scaler) EnsureReady(ctx context.Context, group string) error {
    cfg := s.groups[group]
    
    switch cfg.Scaling.Mode {
    case ScalingStatic:
        return s.ensureDeployment(ctx, group, cfg.Scaling.MinReplicas)
    case ScalingDynamic:
        return s.scaleUp(ctx, group, 1) // Scale to at least 1
    case ScalingEphemeral:
        return s.spawnEphemeral(ctx, group)
    }
    return nil
}

func (s *Scaler) ensureDeployment(ctx context.Context, group string, replicas int) error
func (s *Scaler) scaleUp(ctx context.Context, group string, min int) error
func (s *Scaler) spawnEphemeral(ctx context.Context, group string) error
```

---

## Kubernetes Resources

### Worker Deployment (generated per group)

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: worker-orders
  namespace: flow-workers
  labels:
    app: flow-worker
    group: orders
spec:
  replicas: 2  # Or 0 for scale-to-zero
  selector:
    matchLabels:
      app: flow-worker
      group: orders
  template:
    metadata:
      labels:
        app: flow-worker
        group: orders
    spec:
      serviceAccountName: flow-worker
      containers:
      - name: worker
        image: nzr-flow-worker:latest
        ports:
        - containerPort: 8080
        env:
        - name: GROUP
          value: orders
        - name: CONFIG_DSN
          valueFrom:
            secretKeyRef:
              name: worker-orders
              key: config-dsn
        envFrom:
        - secretRef:
            name: worker-orders-secrets
        resources:
          requests:
            cpu: 100m
            memory: 128Mi
          limits:
            cpu: 500m
            memory: 512Mi
        livenessProbe:
          httpGet:
            path: /healthz
            port: 8080
          initialDelaySeconds: 5
        readinessProbe:
          httpGet:
            path: /readyz
            port: 8080
          initialDelaySeconds: 2
---
apiVersion: v1
kind: Service
metadata:
  name: worker-orders
  namespace: flow-workers
spec:
  selector:
    app: flow-worker
    group: orders
  ports:
  - port: 8080
    targetPort: 8080
```

### KEDA ScaledObject (for dynamic scaling)

```yaml
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata:
  name: worker-orders-scaler
  namespace: flow-workers
spec:
  scaleTargetRef:
    name: worker-orders
  minReplicaCount: 0
  maxReplicaCount: 10
  cooldownPeriod: 300
  triggers:
  - type: prometheus
    metadata:
      serverAddress: http://prometheus:9090
      query: sum(rate(gateway_dispatch_total{group="orders"}[1m]))
      threshold: "10"
```

---

## API Changes

### Admin API: Group management

```
# Create/update group
PUT /admin/groups/{group_id}
{
  "name": "Orders",
  "flows": ["order-get", "order-create", "order-list"],
  "connections": ["orders-db", "inventory-api"],
  "scaling": {
    "mode": "dynamic",
    "minReplicas": 0,
    "maxReplicas": 10,
    "scaleDownDelay": "5m"
  }
}

# List groups
GET /admin/groups

# Get group
GET /admin/groups/{group_id}

# Delete group
DELETE /admin/groups/{group_id}

# Assign flow to group
PATCH /admin/flows/{flow_id}
{
  "group": "orders"
}
```

### Metrics (new)

```
# Gateway metrics
gateway_dispatch_total{group, status}           # Requests dispatched per group
gateway_dispatch_duration_seconds{group}        # Dispatch latency
gateway_worker_ready{group}                     # Worker readiness (0/1)
gateway_worker_replicas{group}                  # Current replica count
gateway_scale_operations_total{group, action}   # Scale up/down events

# Worker metrics (existing flow metrics, tagged with group)
flow_requests_total{group, flow_id, status}
flow_duration_seconds{group, flow_id}
```

---

## Implementation Phases

### Phase 1: Data model + group assignment
- Add `group` field to flow config
- Add `groups` table to config store
- Admin API for group CRUD
- Assign flows to groups via API

**Deliverables:**
- `config/group.go`
- `config/pgstore_group.go`
- `httpapi/admin_groups.go`
- Migration for `groups` table

### Phase 2: Worker binary
- Create `cmd/worker` — slim engine that loads one group
- Worker HTTP API: `/execute`, `/healthz`, `/readyz`
- Dockerfile for worker image
- Load flows/connections filtered by group

**Deliverables:**
- `cmd/worker/main.go`
- `engine/Dockerfile.worker`
- Worker image build in CI

### Phase 3: Gateway mode dispatcher
- Add `dispatch.mode` config to engine
- Implement `Dispatcher` — routes to workers via HTTP
- Implement `WorkerRegistry` — tracks worker endpoints
- K8s endpoint watching for service discovery

**Deliverables:**
- `gateway/dispatcher.go`
- `gateway/registry.go`
- `gateway/client.go`
- Integration with `httpapi/server.go`

### Phase 4: Dynamic scaling
- Implement `Scaler` — creates/scales K8s deployments
- KEDA integration for scale-to-zero
- Generate K8s manifests per group
- CLI tool: `engine groups deploy`

**Deliverables:**
- `gateway/scaler.go`
- `cmd/engine/groups.go` (CLI)
- KEDA ScaledObject templates
- Deployment templates

### Phase 5: Observability + hardening
- Gateway metrics (dispatch latency, scale events)
- Distributed tracing across gateway→worker
- Request queueing during cold start
- Graceful degradation (fallback to inline?)
- Documentation

**Deliverables:**
- Prometheus metrics
- Trace propagation
- Runbook
- Updated docs

---

## Configuration

### Engine config (gateway mode)

```yaml
# config.yaml or env vars
dispatch:
  mode: gateway                              # inline | gateway
  workerImage: nzr-flow-worker:v1.2.3
  namespace: flow-workers
  serviceAccount: flow-worker
  defaultGroup: default                      # Ungrouped flows go here
  startupTimeout: 30s                        # Max wait for worker ready
  requestTimeout: 60s                        # Timeout for worker request

groups:
  orders:
    scaling:
      mode: static
      minReplicas: 2
      maxReplicas: 10
  payments:
    scaling:
      mode: dynamic
      minReplicas: 0
      maxReplicas: 20
      scaleDownDelay: 5m
  webhooks:
    scaling:
      mode: dynamic
      minReplicas: 0
      maxReplicas: 5
```

### Environment variables

```bash
# Engine (gateway mode)
DISPATCH_MODE=gateway
DISPATCH_WORKER_IMAGE=nzr-flow-worker:latest
DISPATCH_NAMESPACE=flow-workers
DISPATCH_DEFAULT_GROUP=default

# Worker
GROUP=orders                    # Which group this worker serves
CONFIG_DSN=postgres://...       # Same config store as engine
WORKER_ADDR=:8080
```

---

## Migration Path

### Backward compatibility

- `dispatch.mode: inline` (default) — current behavior, no changes
- Flows without `group` field use `defaultGroup`
- Existing deployments continue to work

### Gradual rollout

1. Deploy engine with `dispatch.mode: inline` (no change)
2. Define groups, assign flows to groups
3. Deploy worker images
4. Switch engine to `dispatch.mode: gateway`
5. Traffic flows through gateway → workers
6. Monitor, tune scaling thresholds
7. Scale down inline engine replicas

---

## Risks & Mitigations

| Risk | Mitigation |
|------|------------|
| Cold start latency | Pre-warm critical groups; request queueing |
| Worker crash loop | Circuit breaker in dispatcher; fallback to inline |
| Network overhead | Keep gateway + workers in same AZ; connection pooling |
| Config drift | Workers reload config on interval; version check |
| Secret sprawl | Central secret store; group-scoped access policies |

---

## Design Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| **Fallback to inline?** | **No** | If worker unavailable, fail the request. No silent degradation — explicit failure is better than unpredictable behavior. |
| **Cross-group calls?** | **Yes** | Flow A can call flow B in a different group. The gateway routes cross-group calls, enabling composition across boundaries. |
| **Auto-grouping by path?** | **No** | Explicit group assignment only. Operators control which flows go where — no magic based on path prefixes. |
| **Shared connections?** | **No** | Each group has its own connection instances. Strict isolation for security boundaries. |
| **Ungrouped flows?** | **Default group** | Flows without explicit group are assigned to `default` group. The default group always exists and scales like any other group. |

### Cross-group call flow

When a flow in group A needs to call a flow in group B:

```
worker-orders (group A)
    │
    │  Flow "order-create" needs to call "payment-reserve"
    │
    ▼
┌─────────────────────────────────────────────────────┐
│  Action node: type=flow, flowId="payment-reserve"   │
│  → Worker makes HTTP call to gateway                │
│  → Gateway routes to worker-payments (group B)      │
│  → Response flows back                              │
└─────────────────────────────────────────────────────┘
    │
    ▼
worker-payments (group B)
    │
    │  Executes "payment-reserve" flow
    │
    ▼
Response back to worker-orders
```

This requires a new action type: `type: "flow"` that calls another flow via the gateway.

---

## Success Criteria

- [ ] 100 flows distributed across 10 groups
- [ ] Idle groups scale to zero within 5 minutes
- [ ] Cold start latency < 5 seconds
- [ ] No cross-group blast radius (one group crash doesn't affect others)
- [ ] Per-group metrics visible in Grafana
- [ ] Zero-downtime group deployment
