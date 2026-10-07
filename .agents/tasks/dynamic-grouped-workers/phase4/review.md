# KEDA Dynamic Scaling Infrastructure for Grouped Workers

Phase 4 adds the runtime scaling layer that enables worker pods to scale from 0 to N based on traffic. The implementation provides a Scaler state machine (static/dynamic/ephemeral modes), manifest generation via CLI, gateway metrics for KEDA triggers, and dispatcher integration that calls EnsureReady before dispatch.

**Watch for:** The Scaler only updates deployments—it never creates them (confirmed). The EnsureReady polling loop correctly handles the dynamic cold-start case. One concern: the `TestDispatcher_WithScaler_ScalesUp` test pre-populates the registry before dispatch, which means it doesn't exercise the real sequence where the registry discovers the worker *after* scaling (likely, test limitation not production bug).

**Verdict**: APPROVED

---

## High-level view

The **Scaler** implements a clean state machine: static mode waits for minReplicas ready, dynamic mode scales from 0 to 1 and polls until ready or timeout, ephemeral mode returns `ErrEphemeralNotSupported`. The Scaler reads group config from the store and only modifies existing deployments via the K8s API—it never creates them.

The **ManifestGenerator** uses embedded Go templates to produce Deployment, Service, and KEDA ScaledObject YAML. The CLI (`engine groups generate-manifests`) reads groups from the database and passes the `dynamicMode` flag to decide whether to emit the ScaledObject. Idempotency is achieved by overwriting files on regeneration.

The **Dispatcher** gains `NewDispatcherWithScaler` which calls `scaler.EnsureReady` when the worker is not found or not ready in the registry. Metrics are recorded on every dispatch via `GatewayMetrics.ObserveDispatch`. The `gateway_dispatch_total{group}` counter is the KEDA trigger—KEDA queries this to decide when to scale from zero.

The **DispatchConfig** adds `PrometheusAddress` and `ManifestOutputDir` with sensible defaults. The main.go wiring asserts that gateway mode requires CONFIG_DSN (so the Scaler can call `GetGroup`).

---

<details>
<summary>Issues (3)</summary>

1. **Test doesn't exercise post-scale registry discovery (possible)** — `TestDispatcher_WithScaler_ScalesUp` pre-adds the worker to the registry before dispatch. In production, the registry watches K8s endpoints and discovers the worker *after* the Scaler scales up. The test confirms the dispatch path works, but doesn't verify the discovery-after-scaling flow. Consider adding an integration-level test or documenting this as a known gap.

2. **No ScaledObject for static mode is correct but undocumented (possible)** — `GenerateAll` takes a `dynamicMode bool` to decide whether to emit the ScaledObject. This matches the design (static groups don't scale to zero), but the CLI prints "(with KEDA ScaledObject)" only for dynamic mode. If a user generates manifests for a static group and expects KEDA, they won't see it. The behavior is correct; the CLI output could be clearer.

3. **Ephemeral mode stub returns error (confirmed, by design)** — `EnsureReady` for ephemeral mode returns `ErrEphemeralNotSupported`. This is documented in the design as "not yet supported." No blocking concern, but worth noting that configuring a group with ephemeral mode will fail at runtime.

</details>

---

<details>
<summary>Details</summary>

## Scaler state machine

Static mode polls until `ready >= minReplicas` without modifying replicas. Dynamic mode checks if already warm (`ready >= 1`) and returns immediately; if `replicas == 0`, it scales to 1 and waits. `ScaleUp` guards against no-op when already at target. `ScaleDown` is a no-op for static mode. Tests cover all three modes plus the timeout scenario.

## Manifest generation

The ScaledObject template uses KEDA v1alpha1 with `scaleTargetRef.name: worker-{group}` matching the Deployment convention. The Prometheus trigger queries `gateway_dispatch_total{group="{group}"}` which aligns with the metric label. Tests verify YAML validity and idempotency.

## Dispatcher integration

The `Dispatch` method calls `scaler.EnsureReady` when the worker is not found or not ready, then re-fetches from the registry. There's a potential race: `waitForReady` could return before the registry's endpoint watch discovers the new pod. In practice, pod startup time should exceed the registry's watch latency, but this isn't tested—the test pre-populates the registry.

## CLI and metrics

`groups generate-manifests` requires CONFIG_DSN, loads groups from the database, skips disabled groups, and shells out to kubectl for `apply`. The `gateway_dispatch_total{group,status}` counter is the KEDA trigger metric, incremented on every dispatch.

</details>

---

<details>
<summary>File map (17 files)</summary>

| File | Change |
|------|--------|
| `engine/cmd/engine/groups.go` | New CLI for `generate-manifests` and `apply` subcommands |
| `engine/cmd/engine/main.go` | Wire `groups` subcommand, create Scaler and GatewayMetrics in gateway mode |
| `engine/go.mod` | Add `prometheus/client_model` and `gopkg.in/yaml.v3` dependencies |
| `engine/go.sum` | Checksums for new deps |
| `engine/internal/config/dispatch.go` | Add `PrometheusAddress` and `ManifestOutputDir` fields |
| `engine/internal/config/dispatch_test.go` | Tests for new config fields |
| `engine/internal/gateway/dispatcher.go` | Add `NewDispatcherWithScaler`, call scaler in Dispatch |
| `engine/internal/gateway/dispatcher_test.go` | Tests for scaler integration and metrics |
| `engine/internal/gateway/manifests.go` | `ManifestGenerator` with embedded templates |
| `engine/internal/gateway/manifests_test.go` | Tests for YAML generation and idempotency |
| `engine/internal/gateway/metrics.go` | `GatewayMetrics` for KEDA triggers |
| `engine/internal/gateway/metrics_test.go` | Tests for all metric operations |
| `engine/internal/gateway/scaler.go` | `Scaler` with EnsureReady state machine |
| `engine/internal/gateway/scaler_test.go` | Tests for all scaling modes and edge cases |
| `engine/internal/gateway/templates/deployment.yaml.tmpl` | K8s Deployment template |
| `engine/internal/gateway/templates/scaledobject.yaml.tmpl` | KEDA ScaledObject template |
| `engine/internal/gateway/templates/service.yaml.tmpl` | K8s Service template |

[Full diff](git -C /home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-workers-phase4 diff mainline...dynamic-workers-phase4)

</details>
