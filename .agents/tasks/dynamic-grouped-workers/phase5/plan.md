# Phase 5 Implementation Plan — Observability & Hardening

## Overview

Phase 5 adds comprehensive observability and production hardening to the dynamic grouped workers system. Building on Phase 4's metrics (GatewayMetrics, Scaler), this phase enhances metrics, adds distributed tracing, request queueing during cold start, Prometheus alerting rules, Grafana dashboards, security hardening, network policies, structured logging, and runbook documentation.

**Worktree:** `/home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-workers-phase5`  
**Go module root:** `/home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-workers-phase5/engine`  
**Artifact directory:** `/home/nuzirwan/project/rule-engine-api/.agents/tasks/dynamic-grouped-workers/phase5`

---

## Implementation Items

### 1. Enhance Gateway Metrics

Extend `internal/gateway/metrics.go` with additional metrics for comprehensive observability.

**Current metrics (Phase 4):**
- `gateway_dispatch_total{group,status}` — dispatch count
- `gateway_dispatch_duration_seconds{group}` — dispatch latency
- `gateway_worker_ready{group}` — readiness gauge
- `gateway_worker_replicas{group}` — replica count
- `gateway_scale_operations_total{group,action}` — scale events

**New metrics to add:**
- `gateway_requests_total{group,method,path,status}` — full request count with HTTP details
- `gateway_request_duration_seconds{group}` — end-to-end request latency histogram
- `gateway_worker_health{group,worker_id}` — per-worker health (0/1)
- `gateway_circuit_breaker_state{group}` — 0=closed, 1=open, 2=half-open
- `gateway_cold_start_duration_seconds{group}` — time from scale-0 to ready
- `gateway_queue_length{group}` — queued requests during cold start
- `gateway_queue_timeout_total{group}` — queue timeout counter

**Files:**
- Modify: `engine/internal/gateway/metrics.go`
- Modify: `engine/internal/gateway/metrics_test.go`

**Verify:**
```bash
cd /home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-workers-phase5/engine
go build ./...
go test ./internal/gateway/... -v -run TestMetrics
```

---

### 2. Create Worker Metrics

Create new metrics package for worker observability.

**Files:**
- Create: `engine/internal/worker/metrics.go`
- Create: `engine/internal/worker/metrics_test.go`

**Metrics:**
- `worker_requests_total{group,flow_id,status}` — request count
- `worker_request_duration_seconds{group,flow_id}` — request latency
- `worker_config_version{group}` — loaded config version
- `worker_flows_loaded{group}` — count of loaded flows
- `worker_jdms_loaded{group}` — count of loaded JDMs
- `worker_connections_active{group,connection}` — active connections
- `worker_reload_total{group,status}` — hot reload success/failure

**Types and Methods:**

```go
type WorkerMetrics struct {
    requestsTotal     *prometheus.CounterVec
    requestDuration   *prometheus.HistogramVec
    configVersion     *prometheus.GaugeVec
    flowsLoaded       *prometheus.GaugeVec
    jdmsLoaded        *prometheus.GaugeVec
    connectionsActive *prometheus.GaugeVec
    reloadTotal       *prometheus.CounterVec
}

func NewWorkerMetrics(reg prometheus.Registerer) *WorkerMetrics
func (m *WorkerMetrics) ObserveRequest(group, flowID, status string, d time.Duration)
func (m *WorkerMetrics) SetConfigVersion(group string, version int)
func (m *WorkerMetrics) SetFlowsLoaded(group string, count int)
func (m *WorkerMetrics) SetJDMsLoaded(group string, count int)
func (m *WorkerMetrics) SetConnectionsActive(group, connection string, active int)
func (m *WorkerMetrics) IncReload(group, status string)
```

**Verify:**
```bash
cd /home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-workers-phase5/engine
go test ./internal/worker/... -v -run TestMetrics
```

---

### 3. Wire Worker Metrics into Handler and Reload

Update worker components to emit metrics.

**Files:**
- Modify: `engine/internal/worker/worker.go` — emit metrics on LoadGroup
- Modify: `engine/internal/worker/handler.go` — emit metrics on Execute
- Modify: `engine/internal/worker/reload.go` — emit reload metrics

**Changes:**

In `worker.go` LoadGroup():
```go
// After successful load:
if w.metrics != nil {
    w.metrics.SetConfigVersion(w.groupID, group.Version)
    w.metrics.SetFlowsLoaded(w.groupID, len(flows))
    w.metrics.SetJDMsLoaded(w.groupID, len(jdmBytes))
    for _, conn := range connDefs {
        w.metrics.SetConnectionsActive(w.groupID, conn.Key, 1)
    }
}
```

In `handler.go` handleExecute():
```go
// After Execute returns:
if h.metrics != nil {
    status := "ok"
    if err != nil {
        status = "error"
    }
    h.metrics.ObserveRequest(h.worker.GroupID(), req.FlowID, status, time.Since(start))
}
```

**Verify:**
```bash
cd /home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-workers-phase5/engine
go build ./cmd/worker
go test ./internal/worker/... -v
```

---

### 4. Add Distributed Tracing

Create tracing utilities and wire into gateway and worker.

**Files:**
- Create: `engine/internal/gateway/tracing.go`
- Create: `engine/internal/gateway/tracing_test.go`
- Modify: `engine/internal/gateway/dispatcher.go`
- Modify: `engine/internal/gateway/scaler.go`
- Modify: `engine/internal/gateway/client.go`
- Modify: `engine/internal/worker/handler.go`

**Tracing implementation:**

```go
// internal/gateway/tracing.go
package gateway

import (
    "context"
    "net/http"

    "go.opentelemetry.io/otel"
    "go.opentelemetry.io/otel/propagation"
    "go.opentelemetry.io/otel/trace"
)

// InjectTraceContext propagates the current span's trace context into HTTP headers.
func InjectTraceContext(ctx context.Context, req *http.Request) {
    otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))
}

// ExtractTraceContext extracts trace context from HTTP headers.
func ExtractTraceContext(ctx context.Context, req *http.Request) context.Context {
    return otel.GetTextMapPropagator().Extract(ctx, propagation.HeaderCarrier(req.Header))
}
```

In `dispatcher.go` Dispatch():
```go
ctx, span := d.tracer.StartSpan(ctx, "gateway.dispatch", map[string]any{
    "group":   group,
    "flow_id": flowID,
})
defer span.End(err)
```

In `client.go` doExecute():
```go
// Replace manual header propagation with:
InjectTraceContext(ctx, httpReq)
```

In `worker/handler.go` handleExecute():
```go
ctx = gateway.ExtractTraceContext(ctx, r)
ctx, span := h.tracer.StartSpan(ctx, "worker.execute", map[string]any{
    "flow_id": req.FlowID,
    "group":   h.worker.GroupID(),
})
defer span.End(err)
```

**Verify:**
```bash
cd /home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-workers-phase5/engine
go build ./...
go test ./internal/gateway/... -v -run TestTracing
```

---

### 5. Implement Request Queueing During Cold Start

Create request queue to buffer requests during worker scale-up.

**Files:**
- Create: `engine/internal/gateway/queue.go`
- Create: `engine/internal/gateway/queue_test.go`
- Modify: `engine/internal/gateway/dispatcher.go`
- Modify: `engine/internal/gateway/scaler.go`

**Types and methods:**

```go
// internal/gateway/queue.go
package gateway

type RequestQueue struct {
    queues  map[string]chan *queuedRequest
    maxSize int
    timeout time.Duration
    mu      sync.RWMutex
}

type queuedRequest struct {
    ctx        context.Context
    flowID     string
    input      map[string]any
    responseCh chan *queueResponse
}

type queueResponse struct {
    response *worker.ExecuteResponse
    err      error
}

func NewRequestQueue(maxSize int, timeout time.Duration) *RequestQueue
func (q *RequestQueue) Enqueue(ctx context.Context, group, flowID string, input map[string]any) (*worker.ExecuteResponse, error)
func (q *RequestQueue) Drain(group string, dispatch func(string, map[string]any) (*worker.ExecuteResponse, error))
func (q *RequestQueue) QueueLength(group string) int
```

**Integration:**

In `dispatcher.go`:
```go
// If scaler scales from 0, queue the request
if replicas == 0 && d.queue != nil {
    return d.queue.Enqueue(ctx, group, flowID, input)
}
```

In `scaler.go` waitForReady():
```go
// After worker becomes ready:
if d.queue != nil {
    d.queue.Drain(group, func(flowID string, input map[string]any) (*worker.ExecuteResponse, error) {
        return d.forward(ctx, workerState, flowID, input)
    })
}
```

**Verify:**
```bash
cd /home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-workers-phase5/engine
go test ./internal/gateway/... -v -run TestQueue
```

---

### 6. Enhance Structured Logging

Update all log lines to include request_id, trace_id, group.

**Files:**
- Modify: `engine/internal/gateway/dispatcher.go`
- Modify: `engine/internal/gateway/scaler.go`
- Modify: `engine/internal/gateway/client.go`
- Modify: `engine/internal/worker/worker.go`
- Modify: `engine/internal/worker/handler.go`

**Pattern:**
```go
d.log.Emit(ctx, "info", "gateway.dispatch.success", map[string]any{
    "group":      group,
    "flow_id":    flowID,
    "request_id": requestID,
    "trace_id":   traceID,
    "duration_ms": duration.Milliseconds(),
})
```

**Verify:**
```bash
cd /home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-workers-phase5/engine
go build ./...
```

---

### 7. Create Prometheus Alerting Rules

Create alerting rules for worker monitoring.

**Files:**
- Create: `deploy/prometheus/alerts.yaml`

**Content:**
```yaml
groups:
  - name: flow-workers
    rules:
      - alert: WorkerGroupDown
        expr: sum(up{job="flow-worker"}) by (group) == 0
        for: 1m
        labels:
          severity: critical
        annotations:
          summary: "Worker group {{ $labels.group }} is down"
          description: "All workers for group {{ $labels.group }} have been down for more than 1 minute."

      - alert: WorkerHighErrorRate
        expr: rate(worker_requests_total{status="error"}[5m]) > 0.1
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "High error rate for worker group {{ $labels.group }}"
          description: "Worker group {{ $labels.group }} has error rate > 10% for 5 minutes."

      - alert: WorkerColdStartSlow
        expr: histogram_quantile(0.95, sum(rate(gateway_cold_start_duration_seconds_bucket[5m])) by (le, group)) > 10
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "Slow cold starts for group {{ $labels.group }}"
          description: "95th percentile cold start time exceeds 10 seconds."

      - alert: GatewayCircuitOpen
        expr: gateway_circuit_breaker_state == 1
        for: 1m
        labels:
          severity: critical
        annotations:
          summary: "Circuit breaker open for group {{ $labels.group }}"
          description: "Gateway circuit breaker to worker group {{ $labels.group }} is open."
```

**Verify:**
```bash
cd /home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-workers-phase5
kubectl apply --dry-run=client -f deploy/prometheus/alerts.yaml 2>&1 || echo "Manual YAML validation"
```

---

### 8. Create Grafana Dashboard

Create a comprehensive Grafana dashboard for flow workers.

**Files:**
- Create: `deploy/grafana/dashboards/flow-workers.json`

**Panels:**
1. Request Rate per Group — `sum by (group) (rate(gateway_requests_total[5m]))`
2. Latency Percentiles — p50, p95, p99 of `gateway_request_duration_seconds`
3. Error Rate per Group — `sum by (group) (rate(gateway_requests_total{status="error"}[5m]))`
4. Replica Count over Time — `gateway_worker_replicas`
5. Cold Start Duration — `gateway_cold_start_duration_seconds`
6. Circuit Breaker State — `gateway_circuit_breaker_state`
7. Scale Operations Timeline — `rate(gateway_scale_operations_total[5m])`
8. Worker Config Versions — `worker_config_version`

**Verify:**
```bash
cd /home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-workers-phase5
python3 -m json.tool deploy/grafana/dashboards/flow-workers.json > /dev/null
```

---

### 9. Add Security Hardening to Deployment Template

Update the worker deployment template with security best practices.

**Files:**
- Modify: `engine/internal/gateway/templates/deployment.yaml.tmpl`
- Modify: `engine/internal/gateway/manifests.go`
- Modify: `engine/internal/gateway/manifests_test.go`

**Add to deployment template:**
```yaml
spec:
  template:
    spec:
      securityContext:
        runAsNonRoot: true
        runAsUser: 1000
      containers:
      - name: worker
        securityContext:
          readOnlyRootFilesystem: true
          allowPrivilegeEscalation: false
          capabilities:
            drop:
              - ALL
```

**Verify:**
```bash
cd /home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-workers-phase5/engine
go test ./internal/gateway/... -v -run TestManifest
```

---

### 10. Create Network Policies

Create Kubernetes network policies for worker isolation.

**Files:**
- Create: `deploy/k8s/network-policies.yaml`

**Content:**
```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: worker-isolation
  namespace: flow-workers
spec:
  podSelector:
    matchLabels:
      app: flow-worker
  policyTypes:
    - Ingress
    - Egress
  ingress:
    - from:
        - podSelector:
            matchLabels:
              app: engine
  egress:
    - to:
        - podSelector:
            matchLabels:
              app: postgres-engine
    - to:
        - podSelector:
            matchLabels:
              app: engine
```

**Verify:**
```bash
cd /home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-workers-phase5
kubectl apply --dry-run=client -f deploy/k8s/network-policies.yaml
```

---

### 11. Create Runbook Documentation

Create operational runbook for dynamic workers.

**Files:**
- Create: `docs/runbook-dynamic-workers.md`

**Sections:**
1. Overview — architecture diagram, component relationships
2. Quick Reference — key endpoints, commands, metrics
3. Worker Not Starting — troubleshooting guide
4. Worker Crash Loop — debugging steps
5. High Latency — diagnosis procedures
6. Config Not Reloading — fixes
7. Circuit Breaker Recovery — steps
8. Scale-to-Zero Issues — resolution
9. Useful Commands — kubectl, curl, PromQL examples
10. Alert Response — mapping alerts to runbook sections

**Verify:**
```bash
test -f /home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-workers-phase5/docs/runbook-dynamic-workers.md
```

---

## Implementation Order

Execute in this order to maintain buildable state:

1. **Gateway Metrics Enhancement** (item 1) — foundation for observability
2. **Worker Metrics** (items 2-3) — parallel to gateway, no dependencies
3. **Distributed Tracing** (item 4) — builds on metrics
4. **Request Queueing** (item 5) — depends on dispatcher
5. **Structured Logging** (item 6) — integrates with all above
6. **Deploy Configs** (items 7-10) — YAML files, no Go deps
7. **Runbook** (item 11) — documentation, do last

---

## Recording Verification Results

The implementer MUST record what build/test commands were run and their results in:
`/home/nuzirwan/project/rule-engine-api/.agents/tasks/dynamic-grouped-workers/phase5/verification.md`

Example format:
```markdown
# Verification Results

## Build
```
$ go build ./cmd/engine && go build ./cmd/worker
(exit 0)
```

## Tests
```
$ go test ./internal/gateway/... -v
=== RUN TestNewGatewayMetrics
--- PASS: TestNewGatewayMetrics (0.00s)
...
PASS
ok  	nzr-rules-engine/internal/gateway	6.123s
(exit 0)
```
```

---

## Key Design Decisions

1. **Enhance, don't replace Phase 4 metrics** — existing metrics continue to work; new metrics are additive.

2. **OTEL propagation via standard headers** — use W3C traceparent + fallback to X-Trace-Id for compatibility.

3. **Request queue per group** — each group has its own queue channel, preventing one group's cold start from blocking others.

4. **Queue timeout matches startup timeout** — if worker doesn't start within timeout, queued requests fail with meaningful error.

5. **Security hardening defaults** — runAsNonRoot, readOnlyRootFilesystem are ON by default; users can override via values.

6. **Network policies are additive** — base policy + per-group policies if needed.

7. **Dashboard JSON, not provisioning** — Grafana dashboard is a static JSON file that can be imported; no helm/provisioning required.
