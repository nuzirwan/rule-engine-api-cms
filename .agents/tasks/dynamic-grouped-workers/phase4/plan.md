# Phase 4 Implementation Plan — Dynamic Scaling with KEDA

## Overview

Phase 4 adds dynamic scaling infrastructure allowing worker pods to scale from 0 to N based on traffic, using KEDA for scale-to-zero support. The work includes:

1. **Scaler** — Manages K8s Deployments for worker groups
2. **Manifest Generator** — Generates Deployment, Service, and KEDA ScaledObject YAML
3. **CLI Commands** — `engine groups generate-manifests` and `engine groups apply`
4. **Dispatcher Integration** — Wire Scaler into ensureWorker flow
5. **Metrics** — Ensure gateway_dispatch_total{group} metric exists for KEDA

**Worktree:** `/home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-workers-phase4`
**All paths below are relative to:** `/home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-workers-phase4/engine`

---

## Implementation Items

### 1. Add Gateway Metrics for KEDA

Add `gateway_dispatch_total{group}` counter to the metrics system. KEDA queries this metric to decide scaling.

**Files:**
- Create: `internal/gateway/metrics.go`
- Modify: `internal/observ/metrics.go` (add gateway-specific metrics)

**Types and Functions:**

```go
// internal/gateway/metrics.go
package gateway

import "github.com/prometheus/client_golang/prometheus"

// GatewayMetrics holds gateway-specific Prometheus metrics for KEDA scaling.
type GatewayMetrics struct {
    dispatchTotal      *prometheus.CounterVec  // gateway_dispatch_total{group,status}
    dispatchDuration   *prometheus.HistogramVec // gateway_dispatch_duration_seconds{group}
    workerReady        *prometheus.GaugeVec    // gateway_worker_ready{group} 0/1
    workerReplicas     *prometheus.GaugeVec    // gateway_worker_replicas{group}
    scaleOperations    *prometheus.CounterVec  // gateway_scale_operations_total{group,action}
}

func NewGatewayMetrics(reg prometheus.Registerer) *GatewayMetrics
func (m *GatewayMetrics) ObserveDispatch(group, status string, duration time.Duration)
func (m *GatewayMetrics) SetWorkerReady(group string, ready bool)
func (m *GatewayMetrics) SetWorkerReplicas(group string, replicas int)
func (m *GatewayMetrics) IncScaleOperation(group, action string)
```

**Verify:**
```bash
cd /home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-workers-phase4/engine
go build ./...
go test ./internal/gateway/...
```

---

### 2. Create K8s Manifest Templates

Create Go templates for Deployment, Service, and KEDA ScaledObject. Use text/template.

**Files:**
- Create: `internal/gateway/manifests.go`
- Create: `internal/gateway/manifests_test.go`
- Create: `internal/gateway/templates/deployment.yaml.tmpl`
- Create: `internal/gateway/templates/service.yaml.tmpl`
- Create: `internal/gateway/templates/scaledobject.yaml.tmpl`

**Types and Functions:**

```go
// internal/gateway/manifests.go
package gateway

import (
    "text/template"
    "embed"
)

//go:embed templates/*.tmpl
var templatesFS embed.FS

// ManifestConfig holds all values needed to generate manifests for a group.
type ManifestConfig struct {
    Group           string
    Namespace       string
    WorkerImage     string
    ServiceAccount  string
    MinReplicas     int
    MaxReplicas     int
    ScaleDownDelay  int      // seconds
    CPURequest      string
    CPULimit        string
    MemoryRequest   string
    MemoryLimit     string
    PrometheusAddr  string   // e.g. http://prometheus:9090
}

// ManifestGenerator generates K8s YAML manifests from group configuration.
type ManifestGenerator struct {
    deploymentTmpl   *template.Template
    serviceTmpl      *template.Template
    scaledObjectTmpl *template.Template
}

func NewManifestGenerator() (*ManifestGenerator, error)
func (g *ManifestGenerator) GenerateDeployment(cfg ManifestConfig) ([]byte, error)
func (g *ManifestGenerator) GenerateService(cfg ManifestConfig) ([]byte, error)
func (g *ManifestGenerator) GenerateScaledObject(cfg ManifestConfig) ([]byte, error)
func (g *ManifestGenerator) GenerateAll(cfg ManifestConfig, outputDir string) error
```

**Template: deployment.yaml.tmpl**
```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: worker-{{.Group}}
  namespace: {{.Namespace}}
  labels:
    app: flow-worker
    group: {{.Group}}
spec:
  replicas: {{.MinReplicas}}
  selector:
    matchLabels:
      app: flow-worker
      group: {{.Group}}
  template:
    metadata:
      labels:
        app: flow-worker
        group: {{.Group}}
    spec:
      serviceAccountName: {{.ServiceAccount}}
      containers:
      - name: worker
        image: {{.WorkerImage}}
        args:
        - --group={{.Group}}
        - --addr=:8080
        ports:
        - containerPort: 8080
        env:
        - name: CONFIG_DSN
          valueFrom:
            secretKeyRef:
              name: worker-{{.Group}}
              key: config-dsn
        resources:
          requests:
            cpu: {{.CPURequest}}
            memory: {{.MemoryRequest}}
          limits:
            cpu: {{.CPULimit}}
            memory: {{.MemoryLimit}}
        livenessProbe:
          httpGet:
            path: /healthz
            port: 8080
          initialDelaySeconds: 5
          periodSeconds: 10
        readinessProbe:
          httpGet:
            path: /readyz
            port: 8080
          initialDelaySeconds: 2
          periodSeconds: 5
```

**Template: service.yaml.tmpl**
```yaml
apiVersion: v1
kind: Service
metadata:
  name: worker-{{.Group}}
  namespace: {{.Namespace}}
spec:
  selector:
    app: flow-worker
    group: {{.Group}}
  ports:
  - port: 8080
    targetPort: 8080
```

**Template: scaledobject.yaml.tmpl**
```yaml
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata:
  name: worker-{{.Group}}-scaler
  namespace: {{.Namespace}}
spec:
  scaleTargetRef:
    name: worker-{{.Group}}
  minReplicaCount: {{.MinReplicas}}
  maxReplicaCount: {{.MaxReplicas}}
  cooldownPeriod: {{.ScaleDownDelay}}
  triggers:
  - type: prometheus
    metadata:
      serverAddress: {{.PrometheusAddr}}
      query: sum(rate(gateway_dispatch_total{group="{{.Group}}"}[1m]))
      threshold: "10"
```

**Verify:**
```bash
cd /home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-workers-phase4/engine
go build ./...
go test ./internal/gateway/... -run TestManifest
```

---

### 3. Create Scaler with State Machine

The Scaler manages K8s Deployments for worker groups, handling scale-up/down based on scaling mode.

**Files:**
- Create: `internal/gateway/scaler.go`
- Create: `internal/gateway/scaler_test.go`

**Types and Functions:**

```go
// internal/gateway/scaler.go
package gateway

import (
    "context"
    "time"
    
    "k8s.io/client-go/kubernetes"
    appsv1 "k8s.io/api/apps/v1"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "nzr-rules-engine/internal/config"
    "nzr-rules-engine/internal/observ"
)

// Scaler manages K8s deployments for worker groups. It reads group config from
// the store and scales deployments according to the scaling mode.
type Scaler struct {
    k8s       kubernetes.Interface
    namespace string
    store     GroupReader        // interface to read group config
    log       observ.Logger
    metrics   *GatewayMetrics
    
    // startupTimeout is how long to wait for a pod to become ready.
    startupTimeout time.Duration
}

// GroupReader is the interface for reading group configuration.
type GroupReader interface {
    GetGroup(ctx context.Context, env, groupID string) (config.Group, error)
}

// ScalerConfig holds Scaler construction options.
type ScalerConfig struct {
    K8sClient      kubernetes.Interface
    Namespace      string
    Store          GroupReader
    Log            observ.Logger
    Metrics        *GatewayMetrics
    StartupTimeout time.Duration
}

func NewScaler(cfg ScalerConfig) *Scaler

// EnsureReady ensures the worker for the group is ready to receive requests.
// Behavior depends on scaling mode:
//   - static: verify deployment has >= minReplicas ready
//   - dynamic: scale to at least 1 replica if currently at 0, wait for ready
//   - ephemeral: not supported yet (returns error)
func (s *Scaler) EnsureReady(ctx context.Context, group string) error

// ScaleUp sets the deployment replicas to at least the given minimum.
func (s *Scaler) ScaleUp(ctx context.Context, group string, minReplicas int32) error

// ScaleDown allows the deployment to scale to 0 (removes any minimum override).
// For static mode, this is a no-op (static groups don't scale to 0).
func (s *Scaler) ScaleDown(ctx context.Context, group string) error

// GetDeploymentStatus returns the current replica count and ready count.
func (s *Scaler) GetDeploymentStatus(ctx context.Context, group string) (replicas int32, ready int32, err error)
```

**EnsureReady State Machine:**

```
EnsureReady(ctx, group):
  1. cfg := store.GetGroup(group)
  2. switch cfg.Scaling.Mode:
     
     case ScalingModeStatic:
       - Call ensureDeployment(ctx, group, cfg.Scaling.MinReplicas)
       - Static deployments always have minReplicas > 0
       - Wait for ready pods >= minReplicas or timeout
       
     case ScalingModeDynamic:
       - Get current deployment replicas
       - If replicas == 0:
           - ScaleUp(ctx, group, 1)  // Scale to at least 1
           - Log "gateway.scaler.scaling_up" {group, fromReplicas: 0, toReplicas: 1}
           - metrics.IncScaleOperation(group, "scale_up")
       - Wait for at least 1 ready pod or timeout
       - If timeout: return error (worker not ready)
       
     case ScalingModeEphemeral:
       - Return error "ephemeral mode not yet supported"
       
  3. Return nil on success
```

**Wait for Ready Logic:**

```go
func (s *Scaler) waitForReady(ctx context.Context, group string, minReady int32) error {
    deadline := time.Now().Add(s.startupTimeout)
    ticker := time.NewTicker(500 * time.Millisecond)
    defer ticker.Stop()
    
    for {
        select {
        case <-ctx.Done():
            return ctx.Err()
        case <-ticker.C:
            if time.Now().After(deadline) {
                return fmt.Errorf("timeout waiting for worker %s to be ready", group)
            }
            _, ready, err := s.GetDeploymentStatus(ctx, group)
            if err != nil {
                continue // Deployment might not exist yet
            }
            if ready >= minReady {
                return nil
            }
        }
    }
}
```

**Verify:**
```bash
cd /home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-workers-phase4/engine
go build ./...
go test ./internal/gateway/... -run TestScaler
```

---

### 4. Extend DispatchConfig

Add PrometheusAddress and ManifestOutputDir to dispatch config.

**Files:**
- Modify: `internal/config/dispatch.go`
- Modify: `internal/config/dispatch_test.go`

**Changes to DispatchConfig:**

```go
// DispatchConfig holds the gateway dispatcher configuration loaded from env vars.
type DispatchConfig struct {
    // ... existing fields ...
    
    // PrometheusAddress is the Prometheus server URL for KEDA triggers.
    // Default: "http://prometheus:9090"
    PrometheusAddress string
    
    // ManifestOutputDir is the default output directory for generated manifests.
    // Default: "./k8s/workers"
    ManifestOutputDir string
}
```

**Env vars:**
- `DISPATCH_PROMETHEUS_ADDRESS` — default `http://prometheus:9090`
- `DISPATCH_MANIFEST_OUTPUT_DIR` — default `./k8s/workers`

**Verify:**
```bash
cd /home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-workers-phase4/engine
go build ./...
go test ./internal/config/... -run TestDispatch
```

---

### 5. Integrate Scaler into Dispatcher

Update Dispatcher to call Scaler.EnsureReady when worker not ready and mode=dynamic.

**Files:**
- Modify: `internal/gateway/dispatcher.go`
- Modify: `internal/gateway/dispatcher_test.go`

**Changes:**

1. Add `scaler *Scaler` and `metrics *GatewayMetrics` fields to Dispatcher struct
2. Update NewDispatcher to accept optional Scaler and Metrics
3. Modify Dispatch method to:
   - Record metrics on every dispatch
   - Call scaler.EnsureReady when worker not ready

**Updated Dispatch Flow:**

```go
func (d *Dispatcher) Dispatch(ctx context.Context, flowID, group string, input map[string]any) (*worker.ExecuteResponse, error) {
    start := time.Now()
    
    // Lookup worker via registry
    workerState, ok := d.registry.GetWorker(group)
    
    // If not found or not ready, try to ensure via scaler (dynamic mode)
    if (!ok || !workerState.Ready) && d.scaler != nil {
        if err := d.scaler.EnsureReady(ctx, group); err != nil {
            d.recordMetrics(group, "error", time.Since(start))
            return nil, &DispatchError{
                Code:    ErrCodeWorkerNotReady,
                Message: fmt.Sprintf("failed to ensure worker ready for group %s: %v", group, err),
                Group:   group,
            }
        }
        // Re-fetch worker state after scaling
        workerState, ok = d.registry.GetWorker(group)
    }
    
    if !ok {
        d.recordMetrics(group, "error", time.Since(start))
        return nil, &DispatchError{
            Code:    ErrCodeWorkerNotFound,
            Message: fmt.Sprintf("no worker found for group %s", group),
            Group:   group,
        }
    }
    
    if !workerState.Ready {
        d.recordMetrics(group, "error", time.Since(start))
        return nil, &DispatchError{
            Code:    ErrCodeWorkerNotReady,
            Message: fmt.Sprintf("worker for group %s is not ready", group),
            Group:   group,
        }
    }
    
    // ... existing dispatch logic ...
    
    resp, err := d.client.Execute(ctx, group, workerState.Endpoint, req)
    status := "ok"
    if err != nil {
        status = "error"
    }
    d.recordMetrics(group, status, time.Since(start))
    
    return resp, err
}

func (d *Dispatcher) recordMetrics(group, status string, duration time.Duration) {
    if d.metrics != nil {
        d.metrics.ObserveDispatch(group, status, duration)
    }
}
```

**Verify:**
```bash
cd /home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-workers-phase4/engine
go build ./...
go test ./internal/gateway/... -run TestDispatcher
```

---

### 6. Create CLI Commands

Add `groups` subcommands to the engine CLI.

**Files:**
- Create: `cmd/engine/groups.go`

**Commands:**

```
engine groups generate-manifests [--group=<name>] --output-dir=<path>
  Generates K8s manifests (Deployment, Service, KEDA ScaledObject) for all groups
  or a specific group. Requires CONFIG_DSN to read group configuration.

engine groups apply --group=<name> [--dry-run]
  Applies manifests for a group via kubectl apply. Thin wrapper that calls
  `kubectl apply -f <output-dir>/<group>/`.
```

**Implementation:**

```go
// cmd/engine/groups.go
package main

import (
    "context"
    "flag"
    "fmt"
    "os"
    "os/exec"
    "path/filepath"
    
    "nzr-rules-engine/internal/config"
    "nzr-rules-engine/internal/gateway"
)

type groupsCmd struct {
    generateManifests *flag.FlagSet
    apply             *flag.FlagSet
    
    // generate-manifests flags
    genGroup     string
    genOutputDir string
    
    // apply flags
    applyGroup  string
    applyDryRun bool
}

func newGroupsCmd() *groupsCmd {
    cmd := &groupsCmd{
        generateManifests: flag.NewFlagSet("generate-manifests", flag.ExitOnError),
        apply:             flag.NewFlagSet("apply", flag.ExitOnError),
    }
    
    cmd.generateManifests.StringVar(&cmd.genGroup, "group", "", "specific group to generate (all if empty)")
    cmd.generateManifests.StringVar(&cmd.genOutputDir, "output-dir", "./k8s/workers", "output directory")
    
    cmd.apply.StringVar(&cmd.applyGroup, "group", "", "group to apply (required)")
    cmd.apply.BoolVar(&cmd.applyDryRun, "dry-run", false, "run kubectl apply --dry-run=client")
    
    return cmd
}

func (c *groupsCmd) run(args []string) error {
    if len(args) < 1 {
        return fmt.Errorf("usage: engine groups <generate-manifests|apply>")
    }
    
    switch args[0] {
    case "generate-manifests":
        c.generateManifests.Parse(args[1:])
        return c.runGenerateManifests()
    case "apply":
        c.apply.Parse(args[1:])
        return c.runApply()
    default:
        return fmt.Errorf("unknown groups subcommand: %s", args[0])
    }
}

func (c *groupsCmd) runGenerateManifests() error {
    // Load dispatch config for defaults
    dispatchCfg, err := config.LoadDispatchConfig()
    if err != nil {
        return err
    }
    
    // Connect to config store
    dsn := os.Getenv("CONFIG_DSN")
    if dsn == "" {
        return fmt.Errorf("CONFIG_DSN required for manifest generation")
    }
    
    ctx := context.Background()
    store, cleanup, err := buildConfigStore(ctx, dsn)
    if err != nil {
        return err
    }
    defer cleanup()
    
    // Get groups to process
    var groups []config.Group
    if c.genGroup != "" {
        g, err := store.GetGroup(ctx, "", c.genGroup)
        if err != nil {
            return fmt.Errorf("group %s not found: %w", c.genGroup, err)
        }
        groups = []config.Group{g}
    } else {
        groups, err = store.ListGroups(ctx, "")
        if err != nil {
            return fmt.Errorf("failed to list groups: %w", err)
        }
    }
    
    // Create manifest generator
    gen, err := gateway.NewManifestGenerator()
    if err != nil {
        return err
    }
    
    // Generate manifests for each group
    for _, g := range groups {
        cfg := gateway.ManifestConfig{
            Group:          g.ID,
            Namespace:      dispatchCfg.Namespace,
            WorkerImage:    dispatchCfg.WorkerImage,
            ServiceAccount: dispatchCfg.ServiceAccount,
            MinReplicas:    g.Scaling.MinReplicas,
            MaxReplicas:    g.Scaling.MaxReplicas,
            ScaleDownDelay: parseScaleDownDelay(g.Scaling.ScaleDownDelay),
            CPURequest:     defaultString(resourceOrDefault(g.Scaling.Resources, "cpuRequest"), "100m"),
            CPULimit:       defaultString(resourceOrDefault(g.Scaling.Resources, "cpuLimit"), "500m"),
            MemoryRequest:  defaultString(resourceOrDefault(g.Scaling.Resources, "memoryRequest"), "128Mi"),
            MemoryLimit:    defaultString(resourceOrDefault(g.Scaling.Resources, "memoryLimit"), "512Mi"),
            PrometheusAddr: dispatchCfg.PrometheusAddress,
        }
        
        groupDir := filepath.Join(c.genOutputDir, g.ID)
        if err := gen.GenerateAll(cfg, groupDir); err != nil {
            return fmt.Errorf("failed to generate manifests for %s: %w", g.ID, err)
        }
        
        fmt.Printf("Generated manifests for group %s in %s\n", g.ID, groupDir)
    }
    
    return nil
}

func (c *groupsCmd) runApply() error {
    if c.applyGroup == "" {
        return fmt.Errorf("--group is required")
    }
    
    dispatchCfg, _ := config.LoadDispatchConfig()
    outputDir := dispatchCfg.ManifestOutputDir
    if outputDir == "" {
        outputDir = "./k8s/workers"
    }
    
    groupDir := filepath.Join(outputDir, c.applyGroup)
    
    args := []string{"apply", "-f", groupDir}
    if c.applyDryRun {
        args = append(args, "--dry-run=client")
    }
    
    cmd := exec.Command("kubectl", args...)
    cmd.Stdout = os.Stdout
    cmd.Stderr = os.Stderr
    
    return cmd.Run()
}
```

**Wire into main.go:**

```go
// In main() before flag.Parse()
if len(os.Args) > 1 && os.Args[1] == "groups" {
    cmd := newGroupsCmd()
    if err := cmd.run(os.Args[2:]); err != nil {
        fmt.Fprintf(os.Stderr, "error: %v\n", err)
        os.Exit(1)
    }
    os.Exit(0)
}
```

**Verify:**
```bash
cd /home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-workers-phase4/engine
go build ./cmd/engine
./engine groups generate-manifests --output-dir /tmp/k8s --help
```

---

### 7. Wire Scaler and Metrics into main.go

Update cmd/engine/main.go to create Scaler and Metrics in gateway mode.

**Files:**
- Modify: `cmd/engine/main.go`

**Changes:**

```go
// In run(), after creating workerRegistry:
var gatewayMetrics *gateway.GatewayMetrics
var scaler *gateway.Scaler
if dispatchCfg.Mode == config.DispatchGateway {
    // ... existing k8sClient and workerRegistry creation ...
    
    // Create gateway metrics
    gatewayMetrics = gateway.NewGatewayMetrics(metricsReg)
    
    // Create scaler
    scaler = gateway.NewScaler(gateway.ScalerConfig{
        K8sClient:      k8sClient,
        Namespace:      dispatchCfg.Namespace,
        Store:          store, // PgStore implements GroupReader
        Log:            obsLog,
        Metrics:        gatewayMetrics,
        StartupTimeout: dispatchCfg.StartupTimeout,
    })
    
    // Pass scaler to dispatcher
    dispatcher = gateway.NewDispatcher(workerRegistry, workerClient, gateway.DispatchConfig{
        RequestTimeout: dispatchCfg.RequestTimeout,
    }, obsLog, scaler, gatewayMetrics)
}
```

**Verify:**
```bash
cd /home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-workers-phase4/engine
go build ./cmd/engine
```

---

### 8. Add Unit Tests

Create comprehensive tests for all new components.

**Files:**
- Create: `internal/gateway/metrics_test.go`
- Create: `internal/gateway/manifests_test.go`
- Create: `internal/gateway/scaler_test.go`
- Modify: `internal/gateway/dispatcher_test.go`

**Test Cases:**

**metrics_test.go:**
- TestNewGatewayMetrics — creates metrics without panic
- TestObserveDispatch — increments counter and histogram
- TestSetWorkerReady — sets gauge correctly
- TestIncScaleOperation — increments scale operation counter

**manifests_test.go:**
- TestGenerateDeployment — outputs valid YAML with correct values
- TestGenerateService — outputs valid YAML
- TestGenerateScaledObject — outputs valid YAML only for dynamic mode
- TestGenerateAll — creates files in output directory

**scaler_test.go:**
- TestEnsureReady_StaticMode — verifies deployment exists with minReplicas
- TestEnsureReady_DynamicMode_ScaleFromZero — scales up from 0
- TestEnsureReady_DynamicMode_AlreadyRunning — no-op when already running
- TestEnsureReady_EphemeralMode — returns error (not supported)
- TestScaleUp — sets replicas correctly
- TestScaleDown — no-op for static, allows 0 for dynamic
- TestWaitForReady_Timeout — returns error on timeout

**dispatcher_test.go updates:**
- TestDispatch_WithScaler_ScalesUp — calls scaler when worker not ready
- TestDispatch_WithScaler_AlreadyReady — skips scaler when worker ready
- TestDispatch_RecordsMetrics — metrics incremented on dispatch

**Verify:**
```bash
cd /home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-workers-phase4/engine
go test ./internal/gateway/... -v
```

---

## Verification Checklist

After implementation, run these commands from `/home/nuzirwan/project/rule-engine-api/.worktrees/dynamic-workers-phase4/engine`:

```bash
# 1. Build compiles
go build ./cmd/engine

# 2. All gateway tests pass
go test ./internal/gateway/... -v

# 3. Config tests pass
go test ./internal/config/... -run TestDispatch

# 4. CLI help works
./engine groups generate-manifests --help

# 5. Generate manifests (mock test)
./engine groups generate-manifests --output-dir /tmp/k8s 2>&1 || echo "Expected: needs CONFIG_DSN"

# 6. Verify generated YAML structure (manual inspection)
ls -la /tmp/k8s/  # If CONFIG_DSN was set
```

---

## Implementation Order

Execute in this order to maintain buildable state at each step:

1. **Metrics** (item 1) — No dependencies, enables later components
2. **Dispatch Config Extension** (item 4) — Simple extension
3. **Manifest Templates** (item 2) — Self-contained, uses embed
4. **Scaler** (item 3) — Depends on metrics
5. **Dispatcher Integration** (item 5) — Depends on scaler and metrics
6. **CLI Commands** (item 6) — Depends on manifests and config store
7. **Wire into main.go** (item 7) — Integration, depends on all above
8. **Unit Tests** (item 8) — Test all components

---

## Recording Verification Results

The implementer MUST record what build/test commands were run and their results. After completing implementation:

1. Create a file: `/home/nuzirwan/project/rule-engine-api/.agents/tasks/dynamic-grouped-workers/phase4/verification.md`
2. Record:
   - Each command run
   - The exit code and relevant output
   - Any test failures and how they were resolved
   - The final state (all tests passing)

Example format:
```markdown
# Verification Results

## Build
```
$ go build ./cmd/engine
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

## CLI
```
$ ./engine groups generate-manifests --help
Usage: engine groups generate-manifests [flags]
  --group string    specific group to generate
  --output-dir string    output directory (default "./k8s/workers")
(exit 0)
```
```

---

## Key Design Decisions

1. **Scaler only reads/updates existing deployments** — Manifest generation is separate from runtime scaling. The CLI generates YAML; GitOps applies it; the Scaler manages replicas at runtime.

2. **EnsureReady waits for pod ready** — The Scaler polls deployment status until at least one pod is ready or timeout. This ensures cold-start requests don't fail immediately.

3. **Manifest generation is idempotent** — Re-running `generate-manifests` overwrites existing files safely. DevOps can re-run after config changes.

4. **KEDA is external** — We generate ScaledObject YAML; the KEDA controller (assumed installed) handles the actual scaling decisions based on Prometheus metrics.

5. **Prometheus trigger** — KEDA queries `gateway_dispatch_total{group}` to decide when to scale up from 0. Threshold of 10 requests/minute triggers scale-up.

6. **Template-based generation** — Go's text/template keeps templates readable and maintainable. Embedded via `//go:embed`.

7. **CLI over API** — Manifest generation is a CLI tool, not an API endpoint. This follows GitOps patterns where infra changes go through Git, not runtime APIs.
