# Phase 3 Implementation Plan: Gateway Dispatcher

## Overview

Phase 3 transforms the engine into a gateway that routes flow execution requests to worker pods instead of executing flows inline. This enables the isolated worker architecture designed in Phase 2.

## Key Design Decisions

1. **No fallback**: When a worker is unavailable, return an error. The design explicitly prohibits fallback to inline execution for isolation guarantees.

2. **K8s Endpoints watching**: Use k8s.io/client-go to watch Endpoints resources for worker discovery. Workers are exposed via K8s Services named `worker-{group}`.

3. **Circuit breaker per group**: Reuse the sony/gobreaker pattern from `internal/connect/resilience.go`. One breaker per group prevents thundering herd on a failing worker.

4. **Reuse worker types**: The gateway uses `worker.ExecuteRequest` and `worker.ExecuteResponse` from `internal/worker/types.go` for wire format.

5. **Dispatch mode gating**: `dispatch.mode=inline` preserves existing behavior (Phase 2 baseline). `dispatch.mode=gateway` activates routing.

## File Changes

### New Files
```
engine/internal/config/dispatch.go        # DispatchConfig, DispatchMode types
engine/internal/gateway/                   # New package
  types.go                                 # HealthStatus, WorkerState
  registry.go                              # WorkerRegistry, K8s watcher
  client.go                                # WorkerClient with circuit breaker
  circuit.go                               # Per-group breaker config
  dispatcher.go                            # Dispatcher routing logic
  registry_test.go                         # Registry unit tests
  client_test.go                           # Client unit tests
  dispatcher_test.go                       # Dispatcher unit tests
engine/internal/config/dispatch_test.go   # Config unit tests
engine/internal/httpapi/dispatch_test.go  # Integration unit tests
```

### Modified Files
```
engine/go.mod                              # Add k8s.io/client-go
engine/internal/httpapi/server.go          # Gateway mode integration, /internal/execute
engine/cmd/engine/main.go                  # Build gateway components when mode=gateway
```

## Implementation Items

---

- [ ] 1. Add k8s.io/client-go dependency and create dispatch config types.
      DispatchMode (inline|gateway), DispatchConfig struct with env var parsing.
      Files: engine/go.mod, engine/internal/config/dispatch.go, engine/internal/config/dispatch_test.go
      Verify: `go mod tidy && go test ./internal/config/... -run TestDispatch` — passes.

---

- [ ] 2. Create gateway types: HealthStatus enum and WorkerState struct.
      HealthStatus (Unknown|Healthy|Unhealthy), WorkerState with Group, Endpoint, Ready, Replicas, LastRequest, Health.
      Files: engine/internal/gateway/types.go
      Verify: `go build ./internal/gateway` — compiles.

---

- [ ] 3. Create WorkerRegistry with K8s Endpoints watcher.
      Registry tracks workers per group, Watch() goroutine monitors K8s Endpoints, background health checks ping /healthz.
      Files: engine/internal/gateway/registry.go, engine/internal/gateway/registry_test.go
      Verify: `go test ./internal/gateway/... -run TestRegistry` — passes.

---

- [ ] 4. Create WorkerClient with circuit breaker and connection pooling.
      HTTP client with pooled Transport, per-group breakers using gobreaker pattern from internal/connect/resilience.go.
      Files: engine/internal/gateway/client.go, engine/internal/gateway/circuit.go, engine/internal/gateway/client_test.go
      Verify: `go test ./internal/gateway/... -run TestWorkerClient` — passes.

---

- [ ] 5. Create Dispatcher to route requests to workers.
      Dispatch(ctx, flowID, group, input) looks up worker, forwards via client, handles errors.
      Files: engine/internal/gateway/dispatcher.go, engine/internal/gateway/dispatcher_test.go
      Verify: `go test ./internal/gateway/...` — all tests pass.

---

- [ ] 6. Integrate gateway into httpapi/server.go.
      Add Dispatcher to Deps, modify genericFlowHandler to check dispatch.mode, add POST /internal/execute for cross-group calls.
      Files: engine/internal/httpapi/server.go, engine/internal/httpapi/dispatch_test.go
      Verify: `go test ./internal/httpapi/...` — passes.

---

- [ ] 7. Wire gateway in cmd/engine/main.go.
      Build K8s client, WorkerRegistry, WorkerClient, Dispatcher when mode=gateway. Pass to httpapi.Deps.
      Files: engine/cmd/engine/main.go
      Verify: `go build ./cmd/engine` — compiles.

---

- [ ] 8. Full verification: build and test all.
      Ensure all components integrate correctly.
      Files: all above
      Verify: `go build ./cmd/engine && go test ./...` — passes.

## Verification Commands

All commands run from `/home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-workers-phase3/engine`:

```bash
# Build verification
go build ./cmd/engine
go build ./cmd/worker

# Unit tests
go test ./internal/config/... -run TestDispatch -v
go test ./internal/gateway/... -v
go test ./internal/httpapi/... -v

# Full test suite
go test ./...

# Static analysis
go vet ./...

# Dependency verification
go mod tidy
```

## Key Implementation Notes

### K8s Client Building

```go
// Try in-cluster config first (running as pod), fallback to kubeconfig
func buildK8sClient() (kubernetes.Interface, error) {
    config, err := rest.InClusterConfig()
    if err != nil {
        // Not in cluster, try kubeconfig
        kubeconfig := os.Getenv("KUBECONFIG")
        if kubeconfig == "" {
            kubeconfig = filepath.Join(os.Getenv("HOME"), ".kube", "config")
        }
        config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
        if err != nil {
            return nil, nil // K8s unavailable, gateway mode won't work
        }
    }
    return kubernetes.NewForConfig(config)
}
```

### Dispatch Mode Gating

```go
// In genericFlowHandler
if deps.DispatchConfig != nil && deps.DispatchConfig.Mode == config.DispatchGateway {
    // Gateway mode: dispatch to worker
    group := fv.Group
    if group == "" {
        group = deps.DispatchConfig.DefaultGroup
    }
    resp, err := deps.Dispatcher.Dispatch(ctx, fv.FlowID, group, input)
    // ...
} else {
    // Inline mode: existing interpreter logic
    // ...
}
```

### Cross-Group Calls (/internal/execute)

Workers call back to gateway via POST /internal/execute when a flow contains an action that calls another flow. The gateway resolves the target flow's group and dispatches to the correct worker.

```
Worker-A (group: orders) → POST /internal/execute {flowId: "payment-process"}
                         ↓
Gateway resolves payment-process → group: payments
                         ↓
Gateway dispatches → Worker-B (group: payments)
```

## Configuration

Environment variables:
- `DISPATCH_MODE=gateway|inline` (default: inline)
- `DISPATCH_WORKER_NAMESPACE=flow-workers` (default)
- `DISPATCH_DEFAULT_GROUP=default` (default)
- `DISPATCH_STARTUP_TIMEOUT=30s` (default)
- `DISPATCH_REQUEST_TIMEOUT=60s` (default)

## Constraints

- **No K8s create/scale**: Gateway is read-only for K8s (watch Endpoints only). Scaling is Phase 4 (KEDA).
- **No fallback**: Worker unavailable = error. Design requirement for isolation.
- **CGO required**: ZEN decision engine requires CGO_ENABLED=1 for worker image.
