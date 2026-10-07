# Phase 2 Implementation Plan: Worker Binary

## Verification Results (Implementation Complete)

All verification commands run from `/home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-workers-phase2/engine`:

```
=== Build worker ===
$ go build ./cmd/worker
Build: OK

=== Test worker package ===
$ go test ./internal/worker/...
ok      nzr-rules-engine/internal/worker        0.115s
Tests: OK

=== Go vet (worker) ===
$ go vet ./internal/worker/... ./cmd/worker/...
Vet: OK

=== Docker build ===
$ docker build -f Dockerfile.worker -t nzr-flow-worker:test .
Successfully built and tagged nzr-flow-worker:test
```

## Implementation Summary

All deliverables implemented:
1. ✅ `engine/cmd/worker/main.go` — CLI flags --group, --addr, --config-dsn; startup loads group config/flows/JDMs/connections; HTTP server with graceful shutdown
2. ✅ `engine/internal/worker/handler.go` — POST /execute, GET /healthz, GET /readyz, GET /debug/config endpoints
3. ✅ `engine/internal/worker/worker.go` — Worker struct, LoadGroup(ctx), Execute(ctx, flowID, input); reuses flow.Interpreter
4. ✅ `engine/internal/worker/reload.go` — background goroutine polling every 30s; SIGHUP forced reload; atomic state swap
5. ✅ `engine/Dockerfile.worker` — multi-stage build, entrypoint /worker, expose 8080
6. ✅ `engine/internal/worker/types.go` — ExecuteRequest, ExecuteResponse, ErrorDetail types

Additional files:
- `engine/internal/config/pgstore_worker.go` — GetActiveFlowsForGroup, GetConnectionsForGroup, GetJDMsForFlows, WorkerStore interface
- `engine/internal/worker/worker_test.go` — unit tests for Worker
- `engine/internal/worker/handler_test.go` — unit tests for HTTP handler
- `engine/internal/worker/reload_test.go` — unit tests for Reloader

---

This plan implements the isolated worker binary that runs as a pod per flow group. The worker loads only its assigned group's flows, JDMs, and connections, and exposes an HTTP API for flow execution.

## Overview

The worker binary is a slim version of the engine that:
- Loads flows/JDMs/connections for ONE group only
- Exposes `POST /execute` to run flows, plus health endpoints
- Hot-reloads configuration when the group version changes
- Gracefully shuts down on SIGTERM

## Prerequisites (Phase 1 Deliverables Verified)

From Phase 1, the following already exist:
- `config.Group` type with scaling config (`internal/config/group.go`)
- `PgStore.GetGroup(ctx, env, groupID)` → returns `Group` with `.Connections` list
- `PgStore.GetGroupVersion(ctx, env, groupID)` → returns version int for polling
- `flow_versions.group_id` column populated for flows assigned to groups

## Implementation Items

---

- [ ] 1. Add `GetActiveFlowsForGroup` method to PgStore for loading flows filtered by group.
      The worker needs to load all active flows assigned to its group. This method joins active_pointers with flow_versions filtered by group_id, returning full FlowVersion structs with trees decoded.
      Files: engine/internal/config/pgstore_admin.go (or new pgstore_worker.go)
      Verify: `go test ./internal/config/... -run TestGetActiveFlowsForGroup` — new test passes.

---

- [ ] 2. Add `GetConnectionsForGroup` method to PgStore for loading connections filtered by group's allowed keys.
      The worker loads only connections specified in the group's `Connections` slice. This method takes the connection keys and returns their active connection defs.
      Files: engine/internal/config/pgstore_admin.go (or same file as step 1)
      Verify: `go test ./internal/config/... -run TestGetConnectionsForGroup` — new test passes.

---

- [ ] 3. Create worker types package with Execute request/response types.
      Define `ExecuteRequest` (FlowID, RequestID, TraceID, Input) and `ExecuteResponse` (Status, Response, Error) plus `ErrorDetail` struct. These are the wire format for the worker's `/execute` endpoint.
      Files: engine/internal/worker/types.go
      Verify: `go build ./internal/worker` — compiles without error.

---

- [ ] 4. Create Worker struct and LoadGroup method.
      Worker struct holds: loaded FlowVersions (map by flowID), JDM bytes (from flow trees), connection Registry, group config, loaded version. LoadGroup(ctx, groupID) method calls store methods from steps 1-2, builds a connector.Registry with only the group's connections, and compiles any JDMs referenced in the flows.
      Files: engine/internal/worker/worker.go
      Verify: `go build ./internal/worker` — compiles without error.

---

- [ ] 5. Create Worker.Execute method that runs a flow.
      Execute(ctx, flowID, input) looks up the flow in loaded map, creates a flow.Ctx, calls flow.Interpreter.Run with the worker's Deps (registry, decision engine), returns the response. Returns error if flowID not found in loaded set.
      Files: engine/internal/worker/worker.go (same file, add method)
      Verify: `go test ./internal/worker/... -run TestWorkerExecute` — unit test with mock store passes.

---

- [ ] 6. Create HTTP handler with /execute, /healthz, /readyz, /debug/config endpoints.
      POST /execute: decode ExecuteRequest, call worker.Execute, encode ExecuteResponse. GET /healthz: always 200 (liveness). GET /readyz: 200 only when worker.Ready() is true (flows loaded, connections healthy). GET /debug/config: return loaded flow IDs and version, protected by internal auth check.
      Files: engine/internal/worker/handler.go
      Verify: `go test ./internal/worker/... -run TestHandler` — handler tests pass.

---

- [ ] 7. Create hot-reload goroutine that polls group version.
      Background goroutine polls store.GetGroupVersion every 30s. On version change: reload flows/JDMs/connections atomically (swap loaded state). SIGHUP handler for forced reload. Use sync.RWMutex for atomic swap so in-flight requests are not interrupted.
      Files: engine/internal/worker/reload.go
      Verify: `go test ./internal/worker/... -run TestHotReload` — reload test passes.

---

- [ ] 8. Create worker main binary with CLI flags and startup.
      CLI flags: --group (required), --addr (default :8080), --config-dsn (required). Load env file, build PgStore, call LoadGroup, build HTTP server, serve with graceful shutdown on SIGTERM (drain timeout 15s). Wire observability (tracer, logger, metrics).
      Files: engine/cmd/worker/main.go
      Verify: `go build ./cmd/worker` — binary compiles without error.

---

- [ ] 9. Create Dockerfile.worker for the worker image.
      Multi-stage build: same builder stage as engine Dockerfile (CGO required for ZEN), same runtime base (debian:bookworm-slim). Entrypoint /worker, expose 8080. Copy only the worker binary. Non-root user. Health check against /healthz.
      Files: engine/Dockerfile.worker
      Verify: `docker build -f Dockerfile.worker -t worker-test .` (run from engine dir) — image builds successfully.

---

- [ ] 10. Add unit tests for worker package (integration-style with test store).
      Test LoadGroup with a fake store returning flows/connections. Test Execute with a minimal flow tree. Test handler endpoints. Test reload logic with version bump.
      Files: engine/internal/worker/worker_test.go, engine/internal/worker/handler_test.go, engine/internal/worker/reload_test.go
      Verify: `go test ./internal/worker/...` — all tests pass.

---

## Verification Section

All commands run from `/home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-workers-phase2/engine`:

### Build Verification
```bash
# Build worker binary
go build ./cmd/worker

# Build Docker image
docker build -f Dockerfile.worker -t nzr-flow-worker:test .
```

### Test Verification
```bash
# Run all worker package tests
go test ./internal/worker/...

# Run config package tests (includes new group methods)
go test ./internal/config/...

# Run full test suite
go test ./...
```

### Manual Test
```bash
# Start worker with test group (requires Postgres with config tables)
./worker --group=default --addr=:8080 --config-dsn="postgres://..."

# Test endpoints
curl -sf http://localhost:8080/healthz
curl -sf http://localhost:8080/readyz

# Execute a flow (replace with actual flow ID)
curl -X POST http://localhost:8080/execute \
  -H "Content-Type: application/json" \
  -d '{"flowId":"order-get","requestId":"test-1","input":{"id":"123"}}'
```

## Key Implementation Notes

### Reuse Existing Code
- **flow.Interpreter**: Worker creates its own `flow.New()` and calls `interp.Run()` exactly as httpapi does
- **config.PgStore**: Worker connects to the same Postgres and uses the same store methods
- **connect.Registry**: Worker builds its own registry with `connect.New(drivers.All(), defs, secrets, tracer, log)`
- **decision.Engine**: Worker builds its own decision engine with `decision.New(jdmLoader, opts...)`

### Filter by Group
- `GetActiveFlowsForGroup(ctx, env, groupID)` returns only flows where `flow_versions.group_id = groupID`
- `GetConnectionsForGroup(ctx, env, keys)` returns only connections where `key IN (...)` from group.Connections

### JDM Compilation
- Worker scans loaded flows for decision nodes referencing JDM IDs
- Worker calls `store.GetJDM(ctx, env, jdmID)` for each referenced JDM
- decision.Engine handles compilation on first Evaluate call (lazy compile)

### Connection Isolation
- Each worker builds its OWN connection pools via `connect.New()`
- No connection sharing between workers (isolation guarantee)
- Worker's registry only contains the group's allowed connections

### Graceful Shutdown
- On SIGTERM: stop accepting new requests, drain in-flight requests (15s timeout)
- Close registry pools, decision engine, config pool in LIFO order
- Same pattern as cmd/engine/main.go

### Trace Propagation
- Extract X-Request-Id and X-Trace-Id from execute request headers (or body fields)
- Pass into flow.NewCtx so spans are linked
- Worker's tracer exports to same collector as gateway

## File Structure After Implementation

```
engine/
├── cmd/
│   ├── engine/
│   │   └── main.go         # existing engine binary
│   └── worker/
│       └── main.go         # NEW: worker binary
├── internal/
│   ├── config/
│   │   ├── pgstore.go      # existing
│   │   ├── pgstore_admin.go # add GetActiveFlowsForGroup, GetConnectionsForGroup
│   │   └── pgstore_group.go # existing Phase 1 group methods
│   ├── worker/             # NEW package
│   │   ├── types.go        # ExecuteRequest, ExecuteResponse
│   │   ├── worker.go       # Worker struct, LoadGroup, Execute
│   │   ├── handler.go      # HTTP handlers
│   │   ├── reload.go       # Hot-reload goroutine
│   │   ├── worker_test.go  # Unit tests
│   │   ├── handler_test.go # Handler tests
│   │   └── reload_test.go  # Reload tests
│   └── ...                 # other existing packages
├── Dockerfile              # existing engine Dockerfile
└── Dockerfile.worker       # NEW: worker Dockerfile
```

## Dependencies

No new external dependencies. Uses existing:
- `github.com/jackc/pgx/v5` — Postgres driver
- `github.com/gorules/zen-go/v2` — JDM evaluation (via decision package)
- Standard library `net/http` for server (no chi/gin)
- `log/slog` for structured logging

## Constraints

- **No K8s client**: Worker does not interact with Kubernetes (that's the gateway's job in Phase 3)
- **No routing**: Worker does not route requests to other workers — it only executes flows it owns
- **No cross-group communication**: Worker knows only about its own group
- **CGO required**: ZEN decision engine requires CGO_ENABLED=1
