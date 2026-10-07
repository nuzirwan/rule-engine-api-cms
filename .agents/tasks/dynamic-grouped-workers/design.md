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

### K8s Manifest Generation

**Who creates the YAML?**

The engine CLI generates K8s manifests from group configuration. DevOps reviews and applies them.

```bash
# Generate manifests for all groups
engine groups generate-manifests --output=./k8s/workers/

# Generate for specific group
engine groups generate-manifests orders --output=./k8s/workers/orders/

# Output structure:
# k8s/workers/
# ├── orders/
# │   ├── deployment.yaml
# │   ├── service.yaml
# │   ├── keda-scaledobject.yaml  (if scaling.mode=dynamic)
# │   └── secret.yaml.template     (template, secrets filled by DevOps)
# ├── payments/
# │   └── ...
# └── kustomization.yaml
```

**Generation workflow:**

```
┌─────────────────────────────────────────────────────────────────┐
│ 1. CMS: DevOps creates/updates group in Strapi                  │
│    └─→ Publishes group configuration                            │
└─────────────────────────┬───────────────────────────────────────┘
                          │ Webhook
                          ▼
┌─────────────────────────────────────────────────────────────────┐
│ 2. Engine: Receives webhook, stores group in DB                 │
│    └─→ Group config now queryable via Admin API                 │
└─────────────────────────┬───────────────────────────────────────┘
                          │ CLI reads from DB
                          ▼
┌─────────────────────────────────────────────────────────────────┐
│ 3. CLI: DevOps runs `engine groups generate-manifests`          │
│    └─→ Generates YAML files from group config                   │
│    └─→ Templates contain placeholders for secrets               │
└─────────────────────────┬───────────────────────────────────────┘
                          │ Git commit
                          ▼
┌─────────────────────────────────────────────────────────────────┐
│ 4. GitOps: DevOps commits manifests to infra repo               │
│    └─→ Reviews changes in PR                                    │
│    └─→ Fills in secret values (or uses ExternalSecrets)         │
└─────────────────────────┬───────────────────────────────────────┘
                          │ ArgoCD / Flux sync
                          ▼
┌─────────────────────────────────────────────────────────────────┐
│ 5. K8s: GitOps tool applies manifests to cluster                │
│    └─→ Worker deployment created/updated                        │
│    └─→ KEDA ScaledObject created (if dynamic scaling)           │
└─────────────────────────────────────────────────────────────────┘
```

**CLI implementation:**

```go
// cmd/engine/groups_generate.go
func generateManifests(groupID, outputDir string) error {
    groups, err := store.ListGroups(ctx)
    if err != nil {
        return err
    }

    for _, group := range groups {
        if groupID != "" && group.ID != groupID {
            continue
        }

        groupDir := filepath.Join(outputDir, group.ID)
        if err := os.MkdirAll(groupDir, 0755); err != nil {
            return err
        }

        // Generate deployment
        deployment := generateDeployment(group, config.WorkerImage)
        if err := writeYAML(filepath.Join(groupDir, "deployment.yaml"), deployment); err != nil {
            return err
        }

        // Generate service
        service := generateService(group)
        if err := writeYAML(filepath.Join(groupDir, "service.yaml"), service); err != nil {
            return err
        }

        // Generate KEDA ScaledObject for dynamic scaling
        if group.Scaling.Mode == ScalingDynamic {
            scaledObject := generateKEDAScaledObject(group)
            if err := writeYAML(filepath.Join(groupDir, "keda-scaledobject.yaml"), scaledObject); err != nil {
                return err
            }
        }

        // Generate secret template (placeholders only)
        secretTemplate := generateSecretTemplate(group)
        if err := writeYAML(filepath.Join(groupDir, "secret.yaml.template"), secretTemplate); err != nil {
            return err
        }

        fmt.Printf("Generated manifests for group %s in %s\n", group.ID, groupDir)
    }

    // Generate kustomization.yaml
    return generateKustomization(outputDir, groups)
}
```

**Secret handling:**

Secrets are NOT auto-generated. The template contains placeholders:

```yaml
# secret.yaml.template (generated)
apiVersion: v1
kind: Secret
metadata:
  name: worker-orders
  namespace: flow-workers
type: Opaque
stringData:
  config-dsn: "REPLACE_WITH_CONFIG_DSN"    # PostgreSQL connection string
  # Connections allowed for this group:
  orders-db: "REPLACE_WITH_ORDERS_DB_DSN"
  inventory-api: "REPLACE_WITH_INVENTORY_API_KEY"
```

DevOps must either:
1. Fill in values manually and apply (not recommended for prod), OR
2. Use ExternalSecrets Operator to pull from Vault/AWS Secrets Manager:

```yaml
# externalsecret.yaml (created by DevOps)
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: worker-orders
  namespace: flow-workers
spec:
  refreshInterval: 1h
  secretStoreRef:
    name: vault-backend
    kind: ClusterSecretStore
  target:
    name: worker-orders
  data:
  - secretKey: config-dsn
    remoteRef:
      key: flow-engine/config-dsn
  - secretKey: orders-db
    remoteRef:
      key: flow-engine/connections/orders-db
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
- Add `group_audit` table for rollback support
- Add `flow_group_audit` table for flow assignment tracking
- Admin API for group CRUD
- Assign flows to groups via API
- CMS group content type in Strapi
- Publish transform: CMS group → engine config

**Database Schema:**

```sql
-- groups: stores group configuration
CREATE TABLE groups (
    id              VARCHAR(64) PRIMARY KEY,        -- e.g., "orders", "payments"
    name            VARCHAR(128) NOT NULL,          -- Human-readable name
    description     TEXT,                           -- Optional description
    scaling_mode    VARCHAR(16) NOT NULL DEFAULT 'dynamic',  -- static | dynamic | ephemeral
    min_replicas    INT NOT NULL DEFAULT 0,
    max_replicas    INT NOT NULL DEFAULT 10,
    scale_down_delay_seconds INT NOT NULL DEFAULT 300,  -- 5 minutes default
    startup_timeout_seconds  INT NOT NULL DEFAULT 30,
    cpu_request     VARCHAR(16) DEFAULT '100m',
    cpu_limit       VARCHAR(16) DEFAULT '500m',
    memory_request  VARCHAR(16) DEFAULT '128Mi',
    memory_limit    VARCHAR(16) DEFAULT '512Mi',
    enabled         BOOLEAN NOT NULL DEFAULT true,  -- Can disable a group
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    
    CONSTRAINT chk_scaling_mode CHECK (scaling_mode IN ('static', 'dynamic', 'ephemeral')),
    CONSTRAINT chk_replicas CHECK (min_replicas >= 0 AND max_replicas >= min_replicas)
);

-- group_connections: many-to-many mapping of groups to allowed connections
CREATE TABLE group_connections (
    group_id        VARCHAR(64) NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    connection_key  VARCHAR(64) NOT NULL,           -- References connections in config
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    
    PRIMARY KEY (group_id, connection_key)
);

CREATE INDEX idx_group_connections_group ON group_connections(group_id);

-- group_audit: tracks all mutations to groups table (see Rollback Strategy section)
-- flow_group_audit: tracks flow-to-group assignments (see Rollback Strategy section)

-- Update flow_versions to include group assignment
ALTER TABLE flow_versions ADD COLUMN group_id VARCHAR(64) REFERENCES groups(id);
CREATE INDEX idx_flow_versions_group ON flow_versions(group_id);
```

**CMS Content Type (Strapi 5):**

```javascript
// src/api/group/content-types/group/schema.json
{
  "kind": "collectionType",
  "collectionName": "groups",
  "info": {
    "singularName": "group",
    "pluralName": "groups",
    "displayName": "Flow Group",
    "description": "Worker group configuration for flow isolation"
  },
  "options": {
    "draftAndPublish": true
  },
  "attributes": {
    "groupId": {
      "type": "string",
      "required": true,
      "unique": true,
      "regex": "^[a-z][a-z0-9-]*$",
      "maxLength": 64
    },
    "name": {
      "type": "string",
      "required": true,
      "maxLength": 128
    },
    "description": {
      "type": "text"
    },
    "enabled": {
      "type": "boolean",
      "default": true
    },
    "scalingMode": {
      "type": "enumeration",
      "enum": ["static", "dynamic", "ephemeral"],
      "default": "dynamic",
      "required": true
    },
    "minReplicas": {
      "type": "integer",
      "min": 0,
      "default": 0,
      "required": true
    },
    "maxReplicas": {
      "type": "integer",
      "min": 1,
      "default": 10,
      "required": true
    },
    "scaleDownDelaySeconds": {
      "type": "integer",
      "min": 60,
      "default": 300
    },
    "startupTimeoutSeconds": {
      "type": "integer",
      "min": 5,
      "default": 30
    },
    "resources": {
      "type": "component",
      "repeatable": false,
      "component": "config.resource-limits"
    },
    "connections": {
      "type": "relation",
      "relation": "manyToMany",
      "target": "api::connection.connection",
      "mappedBy": "groups"
    },
    "flows": {
      "type": "relation",
      "relation": "oneToMany",
      "target": "api::flow.flow",
      "mappedBy": "group"
    }
  }
}
```

```javascript
// src/components/config/resource-limits.json
{
  "collectionName": "components_config_resource_limits",
  "info": {
    "displayName": "Resource Limits",
    "description": "K8s resource requests and limits"
  },
  "attributes": {
    "cpuRequest": {
      "type": "string",
      "default": "100m",
      "regex": "^[0-9]+m?$"
    },
    "cpuLimit": {
      "type": "string",
      "default": "500m",
      "regex": "^[0-9]+m?$"
    },
    "memoryRequest": {
      "type": "string",
      "default": "128Mi",
      "regex": "^[0-9]+(Mi|Gi)$"
    },
    "memoryLimit": {
      "type": "string",
      "default": "512Mi",
      "regex": "^[0-9]+(Mi|Gi)$"
    }
  }
}
```

**Publish Transform (CMS → Engine):**

When a group is published in Strapi, the webhook triggers a transform:

```go
// publish/group_transform.go
type CMSGroup struct {
    ID                     int      `json:"id"`
    GroupID                string   `json:"groupId"`
    Name                   string   `json:"name"`
    Description            string   `json:"description"`
    Enabled                bool     `json:"enabled"`
    ScalingMode            string   `json:"scalingMode"`
    MinReplicas            int      `json:"minReplicas"`
    MaxReplicas            int      `json:"maxReplicas"`
    ScaleDownDelaySeconds  int      `json:"scaleDownDelaySeconds"`
    StartupTimeoutSeconds  int      `json:"startupTimeoutSeconds"`
    Resources              *CMSResourceLimits `json:"resources"`
    Connections            []CMSConnection    `json:"connections"`
    PublishedAt            *time.Time         `json:"publishedAt"`
}

type CMSResourceLimits struct {
    CPURequest    string `json:"cpuRequest"`
    CPULimit      string `json:"cpuLimit"`
    MemoryRequest string `json:"memoryRequest"`
    MemoryLimit   string `json:"memoryLimit"`
}

func TransformCMSGroup(cms *CMSGroup) (*config.Group, error) {
    if cms.PublishedAt == nil {
        return nil, fmt.Errorf("group %s is not published", cms.GroupID)
    }

    // Validate scaling constraints
    if cms.MinReplicas > cms.MaxReplicas {
        return nil, fmt.Errorf("minReplicas (%d) cannot exceed maxReplicas (%d)", 
            cms.MinReplicas, cms.MaxReplicas)
    }
    
    if cms.ScalingMode == "static" && cms.MinReplicas == 0 {
        return nil, fmt.Errorf("static scaling mode requires minReplicas > 0")
    }

    // Extract connection keys
    connectionKeys := make([]string, len(cms.Connections))
    for i, conn := range cms.Connections {
        connectionKeys[i] = conn.Key
    }

    // Set defaults for resources
    resources := config.Resources{
        CPURequest:    "100m",
        CPULimit:      "500m",
        MemoryRequest: "128Mi",
        MemoryLimit:   "512Mi",
    }
    if cms.Resources != nil {
        if cms.Resources.CPURequest != "" {
            resources.CPURequest = cms.Resources.CPURequest
        }
        if cms.Resources.CPULimit != "" {
            resources.CPULimit = cms.Resources.CPULimit
        }
        if cms.Resources.MemoryRequest != "" {
            resources.MemoryRequest = cms.Resources.MemoryRequest
        }
        if cms.Resources.MemoryLimit != "" {
            resources.MemoryLimit = cms.Resources.MemoryLimit
        }
    }

    return &config.Group{
        ID:          cms.GroupID,
        Name:        cms.Name,
        Description: cms.Description,
        Enabled:     cms.Enabled,
        Connections: connectionKeys,
        Scaling: config.ScalingConfig{
            Mode:           config.ScalingMode(cms.ScalingMode),
            MinReplicas:    cms.MinReplicas,
            MaxReplicas:    cms.MaxReplicas,
            ScaleDownDelay: time.Duration(cms.ScaleDownDelaySeconds) * time.Second,
            StartupTimeout: time.Duration(cms.StartupTimeoutSeconds) * time.Second,
            Resources:      resources,
        },
    }, nil
}
```

**Webhook Handler:**

```go
// httpapi/webhook_cms.go
func (h *Handler) HandleCMSGroupPublish(w http.ResponseWriter, r *http.Request) {
    var payload struct {
        Event string    `json:"event"`  // "entry.publish" | "entry.unpublish"
        Model string    `json:"model"`  // "group"
        Entry CMSGroup  `json:"entry"`
    }
    
    if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
        http.Error(w, "invalid payload", http.StatusBadRequest)
        return
    }
    
    if payload.Model != "group" {
        w.WriteHeader(http.StatusOK) // Ignore non-group events
        return
    }

    ctx := r.Context()
    
    switch payload.Event {
    case "entry.publish":
        group, err := publish.TransformCMSGroup(&payload.Entry)
        if err != nil {
            h.logger.Error("transform failed", "error", err, "groupId", payload.Entry.GroupID)
            http.Error(w, err.Error(), http.StatusBadRequest)
            return
        }
        
        if err := h.groupStore.Upsert(ctx, group, "cms-webhook", "CMS publish"); err != nil {
            h.logger.Error("upsert failed", "error", err, "groupId", group.ID)
            http.Error(w, "internal error", http.StatusInternalServerError)
            return
        }
        
        h.logger.Info("group published", "groupId", group.ID)
        
    case "entry.unpublish":
        // Disable group but don't delete (preserve audit trail)
        if err := h.groupStore.Disable(ctx, payload.Entry.GroupID, "cms-webhook", "CMS unpublish"); err != nil {
            h.logger.Error("disable failed", "error", err, "groupId", payload.Entry.GroupID)
            http.Error(w, "internal error", http.StatusInternalServerError)
            return
        }
        
        h.logger.Info("group disabled", "groupId", payload.Entry.GroupID)
    }
    
    w.WriteHeader(http.StatusOK)
}
```

**Deliverables:**
- `config/group.go` — Group and ScalingConfig types
- `config/pgstore_group.go` — PostgreSQL store with audit logging
- `httpapi/admin_groups.go` — Admin API endpoints
- `httpapi/webhook_cms.go` — CMS webhook handler
- `publish/group_transform.go` — CMS → engine transform
- Migration: `20240115_create_groups.sql`
- Migration: `20240115_create_group_audit.sql`
- Migration: `20240115_add_flow_group.sql`
- Strapi content type: `group`
- Strapi component: `config.resource-limits`

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

## Rollback Strategy

### Overview

Every mutation to groups, flows, or workers is auditable and reversible. The system maintains audit trails and supports point-in-time restoration.

### Group Audit Table Schema

```sql
-- group_audit stores every mutation to the groups table
CREATE TABLE group_audit (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    group_id        VARCHAR(64) NOT NULL,       -- References groups.id
    operation       VARCHAR(16) NOT NULL,       -- INSERT, UPDATE, DELETE
    old_data        JSONB,                       -- Previous state (NULL for INSERT)
    new_data        JSONB,                       -- New state (NULL for DELETE)
    changed_by      VARCHAR(128) NOT NULL,      -- User/system that made the change
    changed_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    reason          TEXT,                        -- Optional reason for change
    request_id      VARCHAR(64),                -- Correlation ID for tracing
    
    CONSTRAINT chk_operation CHECK (operation IN ('INSERT', 'UPDATE', 'DELETE'))
);

CREATE INDEX idx_group_audit_group_id ON group_audit(group_id);
CREATE INDEX idx_group_audit_changed_at ON group_audit(changed_at DESC);
CREATE INDEX idx_group_audit_operation ON group_audit(operation);
```

### Flow Assignment Audit Table Schema

```sql
-- flow_group_audit tracks flow-to-group assignment changes
CREATE TABLE flow_group_audit (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    flow_id         VARCHAR(64) NOT NULL,
    old_group       VARCHAR(64),                -- Previous group (NULL if unassigned)
    new_group       VARCHAR(64),                -- New group (NULL if unassigned)
    changed_by      VARCHAR(128) NOT NULL,
    changed_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    reason          TEXT,
    request_id      VARCHAR(64)
);

CREATE INDEX idx_flow_group_audit_flow_id ON flow_group_audit(flow_id);
CREATE INDEX idx_flow_group_audit_changed_at ON flow_group_audit(changed_at DESC);
```

### Rollback Procedures

#### 1. Group Configuration Rollback

**Trigger:** Group scaling/connection misconfiguration causing worker failures.

```bash
# List recent changes to a group
GET /admin/groups/{group_id}/audit?limit=10

# Response
{
  "audits": [
    {
      "id": "audit-123",
      "operation": "UPDATE",
      "old_data": { "scaling": { "maxReplicas": 5 } },
      "new_data": { "scaling": { "maxReplicas": 20 } },
      "changed_by": "admin@example.com",
      "changed_at": "2024-01-15T10:30:00Z"
    }
  ]
}

# Restore to a specific audit point
POST /admin/groups/{group_id}/rollback
{
  "target_audit_id": "audit-122",    # Restore to state BEFORE this audit
  "reason": "maxReplicas=20 caused OOM"
}
```

**Engine-side rollback logic:**
```go
func (s *GroupStore) Rollback(ctx context.Context, groupID, targetAuditID, reason, changedBy string) error {
    tx, err := s.db.BeginTx(ctx, nil)
    if err != nil {
        return err
    }
    defer tx.Rollback()

    // 1. Get the target audit record
    var oldData json.RawMessage
    err = tx.QueryRowContext(ctx, `
        SELECT old_data FROM group_audit 
        WHERE id = $1 AND group_id = $2
    `, targetAuditID, groupID).Scan(&oldData)
    if err != nil {
        return fmt.Errorf("audit record not found: %w", err)
    }

    // 2. Get current state for audit trail
    currentData, err := s.getGroupJSON(ctx, tx, groupID)
    if err != nil {
        return err
    }

    // 3. Restore the old state
    if oldData == nil {
        // Target audit was INSERT → delete the group
        _, err = tx.ExecContext(ctx, `DELETE FROM groups WHERE id = $1`, groupID)
    } else {
        // Restore to old_data
        err = s.updateGroupFromJSON(ctx, tx, groupID, oldData)
    }
    if err != nil {
        return err
    }

    // 4. Record rollback in audit
    _, err = tx.ExecContext(ctx, `
        INSERT INTO group_audit (group_id, operation, old_data, new_data, changed_by, reason, request_id)
        VALUES ($1, 'ROLLBACK', $2, $3, $4, $5, $6)
    `, groupID, currentData, oldData, changedBy, reason, ctx.Value("requestID"))
    if err != nil {
        return err
    }

    return tx.Commit()
}
```

#### 2. Flow Assignment Rollback

**Trigger:** Flow assigned to wrong group, causing connection access failures.

```bash
# Rollback flow to previous group
POST /admin/flows/{flow_id}/rollback-group
{
  "target_audit_id": "flow-audit-456",
  "reason": "Flow needs access to payments-db, not orders-db"
}
```

#### 3. Worker Deployment Rollback

**Trigger:** Bad worker image deployed, workers crash-looping.

```bash
# Kubernetes-native rollback (preferred)
kubectl rollout undo deployment/worker-orders -n flow-workers

# Or restore to specific revision
kubectl rollout undo deployment/worker-orders --to-revision=3 -n flow-workers
```

**Engine CLI support:**
```bash
# List deployment history
engine groups deployment-history orders

# Rollback to previous deployment
engine groups rollback orders --to-revision=3 --reason="OOM in v1.2.4"
```

#### 4. Gateway Configuration Rollback

**Trigger:** Gateway dispatch config change breaks routing.

Gateway config is immutable at runtime — requires redeployment. Rollback:
```bash
# Revert config change in source control
git revert <commit-hash>

# Redeploy gateway
kubectl rollout restart deployment/engine-gateway -n flow-engine
```

### Recovery Time Objectives (RTO)

| Component | RTO | Recovery Method |
|-----------|-----|-----------------|
| Group config | < 1 minute | API rollback (instant DB restore) |
| Flow assignment | < 1 minute | API rollback |
| Worker image | < 3 minutes | K8s rollout undo |
| Gateway config | < 5 minutes | Git revert + redeploy |
| Full group deletion | < 5 minutes | Restore from audit + redeploy workers |

### Recovery Point Objectives (RPO)

| Component | RPO | Data Loss Risk |
|-----------|-----|----------------|
| Group config | 0 | Audit captures every mutation |
| Flow assignment | 0 | Audit captures every change |
| Worker state | N/A | Workers are stateless |
| In-flight requests | ~30s | Requests during rollback may fail |

### Automatic Rollback Triggers

The system supports automatic rollback when health checks fail post-deployment:

```yaml
# In group config
scaling:
  mode: dynamic
  autoRollback:
    enabled: true
    healthCheckWindow: 60s      # Monitor for 60s after deployment
    failureThreshold: 5          # Rollback after 5 consecutive failures
    rollbackTarget: previous     # Restore to last known good state
```

**Implementation:**
```go
type DeploymentWatcher struct {
    registry *WorkerRegistry
    scaler   *Scaler
    store    *GroupStore
}

func (w *DeploymentWatcher) WatchDeployment(ctx context.Context, group string) {
    cfg := w.store.GetGroup(group)
    if !cfg.Scaling.AutoRollback.Enabled {
        return
    }

    ticker := time.NewTicker(5 * time.Second)
    defer ticker.Stop()

    failureCount := 0
    startTime := time.Now()

    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            if time.Since(startTime) > cfg.Scaling.AutoRollback.HealthCheckWindow {
                // Health check window passed, deployment is stable
                return
            }

            worker, _ := w.registry.GetWorker(group)
            if worker == nil || !worker.Ready {
                failureCount++
                if failureCount >= cfg.Scaling.AutoRollback.FailureThreshold {
                    w.triggerRollback(ctx, group, "health check failures exceeded threshold")
                    return
                }
            } else {
                failureCount = 0 // Reset on success
            }
        }
    }
}
```

---

## Deployment Rules

### Pre-Deployment Checklist

Before deploying any group or worker changes, verify:

| # | Check | Command/Action | Required |
|---|-------|----------------|----------|
| 1 | Config syntax valid | `engine config validate` | ✅ |
| 2 | All referenced flows exist | `engine groups validate {group_id}` | ✅ |
| 3 | All referenced connections exist | `engine groups validate {group_id}` | ✅ |
| 4 | Worker image exists and is pullable | `docker pull {image}` | ✅ |
| 5 | K8s namespace exists | `kubectl get ns {namespace}` | ✅ |
| 6 | K8s secrets created for group | `kubectl get secret worker-{group}` | ✅ |
| 7 | Resource quotas allow deployment | `kubectl describe quota -n {namespace}` | ✅ |
| 8 | No conflicting deployments in progress | Check CI/CD pipeline status | ✅ |
| 9 | Rollback plan documented | Written in deployment ticket | ✅ |
| 10 | Monitoring dashboards ready | Grafana group dashboard exists | ⚠️ |

**CLI validation command:**
```bash
# Validates group config, flow references, connection references
engine groups validate orders

# Output:
# ✅ Group 'orders' config valid
# ✅ Flow 'order-get' exists
# ✅ Flow 'order-create' exists  
# ✅ Flow 'order-list' exists
# ✅ Connection 'orders-db' exists
# ✅ Connection 'inventory-api' exists
# ⚠️  Worker image not verified (run with --verify-image)
```

### Deployment Order

Deploy components in this order to prevent broken dependencies:

```
Phase 1: Infrastructure (one-time or when changing)
┌─────────────────────────────────────────────────────┐
│ 1. K8s namespace         kubectl apply -f ns.yaml   │
│ 2. K8s secrets           kubectl apply -f secrets/  │
│ 3. K8s ServiceAccount    kubectl apply -f sa.yaml   │
│ 4. KEDA ScaledObject     kubectl apply -f keda/     │
└─────────────────────────────────────────────────────┘
                          │
                          ▼
Phase 2: Database (before any code deployment)
┌─────────────────────────────────────────────────────┐
│ 5. Run migrations        engine db migrate          │
│    - groups table                                   │
│    - group_audit table                              │
│    - flow_group_audit table                         │
└─────────────────────────────────────────────────────┘
                          │
                          ▼
Phase 3: Configuration (before worker deployment)
┌─────────────────────────────────────────────────────┐
│ 6. Create/update groups  PUT /admin/groups/{id}     │
│ 7. Assign flows          PATCH /admin/flows/{id}    │
│ 8. Publish from CMS      (if using Strapi)          │
└─────────────────────────────────────────────────────┘
                          │
                          ▼
Phase 4: Worker Deployment
┌─────────────────────────────────────────────────────┐
│ 9. Deploy worker         kubectl apply -f worker/   │
│ 10. Wait for ready       kubectl rollout status     │
│ 11. Verify health        curl worker:8080/healthz   │
└─────────────────────────────────────────────────────┘
                          │
                          ▼
Phase 5: Gateway Activation
┌─────────────────────────────────────────────────────┐
│ 12. Update gateway       Set dispatch.mode=gateway  │
│ 13. Rolling restart      kubectl rollout restart    │
│ 14. Verify routing       curl gateway/test-flow     │
└─────────────────────────────────────────────────────┘
```

### Canary Deployment Strategy

For worker image updates, use canary deployment to minimize blast radius:

```yaml
# Worker deployment with canary annotations
apiVersion: apps/v1
kind: Deployment
metadata:
  name: worker-orders
  annotations:
    deployment.kubernetes.io/revision: "5"
spec:
  replicas: 5
  strategy:
    type: RollingUpdate
    rollingUpdate:
      maxSurge: 1           # Add 1 new pod at a time
      maxUnavailable: 0     # Never reduce below current count
```

**Canary rollout steps:**

1. **Deploy canary (10% traffic)**
   ```bash
   # Scale canary deployment
   kubectl apply -f worker-orders-canary.yaml  # 1 replica, new image
   
   # Monitor for 5 minutes
   engine groups canary-status orders --watch
   ```

2. **Verify canary health**
   ```bash
   # Check error rate
   promql 'sum(rate(flow_requests_total{group="orders",status="error"}[5m])) / sum(rate(flow_requests_total{group="orders"}[5m]))'
   
   # Expected: < 1% error rate
   ```

3. **Promote or rollback**
   ```bash
   # If healthy, promote to full rollout
   engine groups canary-promote orders
   
   # If unhealthy, abort canary
   engine groups canary-abort orders
   ```

### Blue-Green Deployment Strategy

For major version changes or risky deployments:

```
┌─────────────────────────────────────────────────────┐
│                    Gateway                           │
│         dispatch.groups.orders.endpoint              │
│                     │                                │
│         ┌───────────┴───────────┐                   │
│         ▼                       ▼                    │
│  ┌─────────────┐         ┌─────────────┐           │
│  │ worker-orders│         │worker-orders│           │
│  │    (blue)    │         │   (green)   │           │
│  │   v1.2.3     │         │   v1.3.0    │           │
│  │  ACTIVE ✅   │         │  STANDBY    │           │
│  └─────────────┘         └─────────────┘           │
└─────────────────────────────────────────────────────┘
```

**Blue-green deployment steps:**

1. **Deploy green environment**
   ```bash
   kubectl apply -f worker-orders-green.yaml
   kubectl rollout status deployment/worker-orders-green
   ```

2. **Verify green health**
   ```bash
   # Direct test against green
   curl http://worker-orders-green:8080/healthz
   
   # Run smoke tests
   engine groups test orders --endpoint=worker-orders-green:8080
   ```

3. **Switch traffic**
   ```bash
   # Update gateway routing
   kubectl patch configmap gateway-config -p '{"data":{"orders.endpoint":"worker-orders-green:8080"}}'
   
   # Rolling restart gateway to pick up new config
   kubectl rollout restart deployment/engine-gateway
   ```

4. **Verify and cleanup**
   ```bash
   # Monitor error rate
   # If stable for 10 minutes, delete blue
   kubectl delete deployment worker-orders-blue
   ```

### Deployment Windows

| Environment | Window | Approval |
|-------------|--------|----------|
| Development | Anytime | Self-service |
| Staging | Anytime | Team lead |
| Production | Mon-Thu 10:00-16:00 UTC | Platform team |
| Production (emergency) | Anytime | On-call + manager |

### Deployment Freeze Periods

No deployments during:
- Last 2 weeks of quarter (freeze for financial close)
- Major company events
- Declared incident response periods
- Friday 14:00 UTC through Monday 06:00 UTC (weekend freeze)

### Post-Deployment Verification

After every deployment, verify:

```bash
# 1. Health check passes
curl -f http://worker-{group}:8080/healthz

# 2. Ready check passes
curl -f http://worker-{group}:8080/readyz

# 3. Test flow execution
engine flows execute {test-flow-id} --input='{"test":true}'

# 4. Check metrics
promql 'up{job="worker-{group}"} == 1'

# 5. Check logs for errors
kubectl logs -l group={group} --since=5m | grep -i error
```

**Automated verification in CI:**
```yaml
# .github/workflows/deploy.yaml
deploy:
  steps:
    - name: Deploy worker
      run: kubectl apply -f k8s/worker-${{ matrix.group }}.yaml
    
    - name: Wait for rollout
      run: kubectl rollout status deployment/worker-${{ matrix.group }} --timeout=5m
    
    - name: Verify health
      run: |
        for i in {1..10}; do
          if curl -sf http://worker-${{ matrix.group }}:8080/healthz; then
            echo "Health check passed"
            exit 0
          fi
          sleep 5
        done
        echo "Health check failed"
        exit 1
    
    - name: Run smoke tests
      run: engine groups test ${{ matrix.group }} --smoke
    
    - name: Rollback on failure
      if: failure()
      run: kubectl rollout undo deployment/worker-${{ matrix.group }}
```

---

## Design Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| **Fallback to inline?** | **No** | If worker unavailable, fail the request. No silent degradation — explicit failure is better than unpredictable behavior. |
| **Cross-group calls?** | **Yes** | Flow A can call flow B in a different group. The gateway routes cross-group calls, enabling composition across boundaries. |
| **Auto-grouping by path?** | **No** | Explicit group assignment only. Operators control which flows go where — no magic based on path prefixes. |
| **Shared connections?** | **No** | Each group has its own connection instances. Strict isolation for security boundaries. |
| **Ungrouped flows?** | **Default group** | Flows without explicit group are assigned to `default` group. The default group always exists and scales like any other group. |

### Shared Connections Clarification

"No shared connections" means:

1. **Connection credentials are not shared** — each group has its own copy of connection secrets (e.g., `worker-orders` and `worker-payments` each have their own `payments-db` secret, even if they point to the same database).

2. **Connection pools are not shared** — each worker maintains its own connection pool. If `orders` and `payments` both access `payments-db`, they each have independent pools.

3. **Why?** — Security isolation. If group A is compromised, its credentials can be rotated without affecting group B. Blast radius is limited to one group.

4. **What if two groups need the same database?** — Both groups list the connection key in their `connections` array. DevOps provisions separate credentials for each (e.g., `payments-db-orders-user` and `payments-db-payments-user` in the database, mapped to the same `payments-db` connection key in different secrets).

```
┌─────────────────────────────────────────────────────────────────┐
│                        PostgreSQL                                │
│                                                                  │
│  ┌──────────────────┐          ┌──────────────────┐            │
│  │ orders-user      │          │ payments-user    │            │
│  │ (limited perms)  │          │ (limited perms)  │            │
│  └────────┬─────────┘          └────────┬─────────┘            │
└───────────┼──────────────────────────────┼──────────────────────┘
            │                              │
            │ K8s Secret: worker-orders    │ K8s Secret: worker-payments
            │ payments-db=orders-user      │ payments-db=payments-user
            │                              │
┌───────────▼───────────┐      ┌───────────▼───────────┐
│   worker-orders       │      │   worker-payments     │
│   Pool: payments-db   │      │   Pool: payments-db   │
│   (10 connections)    │      │   (10 connections)    │
└───────────────────────┘      └───────────────────────┘
```

### Group Lifecycle Edge Cases

| Scenario | Behavior |
|----------|----------|
| Delete group with active workers | Reject. Must scale to 0 first, then delete. |
| Delete group with assigned flows | Reject. Must reassign flows first. |
| Disable group with active requests | Existing requests complete. New requests get `503 group_disabled`. |
| Flow assigned to non-existent group | Reject assignment. Group must exist before flow assignment. |
| Worker starts before flows assigned | Worker starts healthy but `/execute` returns `404 flow_not_found`. |
| Group config update while workers running | Workers continue with old config until restart. Gateway uses new config immediately. |
| KEDA scales to 0 while request in-flight | Request completes (graceful shutdown). Next request triggers cold start. |

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

**Cross-group call error handling:**

| Scenario | Behavior | Client sees |
|----------|----------|-------------|
| Target group disabled | Fail immediately | `503 Service Unavailable` with `group_disabled` error code |
| Target worker not ready (cold start) | Wait up to `startupTimeout` | Delayed response or `504 Gateway Timeout` |
| Target worker crash | Circuit breaker opens after threshold | `503 Service Unavailable` with `circuit_open` error code |
| Target flow not found | Fail immediately | `404 Not Found` with `flow_not_found` error code |
| Network timeout | Retry with backoff (max 3) | `504 Gateway Timeout` after retries exhausted |

**Circuit breaker configuration:**

```go
// gateway/circuit_breaker.go
type CircuitBreakerConfig struct {
    FailureThreshold   int           // Failures before opening (default: 5)
    SuccessThreshold   int           // Successes before closing (default: 3)
    Timeout            time.Duration // Time in open state before half-open (default: 30s)
    MaxConcurrent      int           // Max concurrent requests (default: 100)
}

// Per-group circuit breaker state
type CircuitBreaker struct {
    state          State // closed | open | half-open
    failures       int
    successes      int
    lastFailure    time.Time
    mu             sync.Mutex
}

func (cb *CircuitBreaker) Allow() bool {
    cb.mu.Lock()
    defer cb.mu.Unlock()
    
    switch cb.state {
    case StateClosed:
        return true
    case StateOpen:
        if time.Since(cb.lastFailure) > cb.config.Timeout {
            cb.state = StateHalfOpen
            return true
        }
        return false
    case StateHalfOpen:
        return true
    }
    return false
}
```

### Default Group Behavior

The `default` group is auto-created on first engine startup and cannot be deleted:

```go
// config/pgstore_group.go
func (s *GroupStore) EnsureDefaultGroup(ctx context.Context) error {
    defaultGroup := &Group{
        ID:          "default",
        Name:        "Default Group",
        Description: "Catch-all group for flows without explicit assignment",
        Enabled:     true,
        Connections: []string{}, // Empty — flows must explicitly request connections
        Scaling: ScalingConfig{
            Mode:           ScalingDynamic,
            MinReplicas:    0,
            MaxReplicas:    10,
            ScaleDownDelay: 5 * time.Minute,
            StartupTimeout: 30 * time.Second,
            Resources: Resources{
                CPURequest:    "100m",
                CPULimit:      "500m",
                MemoryRequest: "128Mi",
                MemoryLimit:   "512Mi",
            },
        },
    }

    return s.UpsertIfNotExists(ctx, defaultGroup, "system", "auto-create default group")
}

// Delete is blocked for default group
func (s *GroupStore) Delete(ctx context.Context, groupID, changedBy, reason string) error {
    if groupID == "default" {
        return fmt.Errorf("cannot delete the default group")
    }
    // ... rest of delete logic
}
```

**Default group connection access:**

Flows in the default group have access to NO connections by default. To use a connection:
1. Assign the flow to a group that has the connection, OR
2. Add the connection to the default group's allowed connections

This prevents accidental credential exposure when a new flow is added without explicit group assignment.

---

## Success Criteria

- [ ] 100 flows distributed across 10 groups
- [ ] Idle groups scale to zero within 5 minutes
- [ ] Cold start latency < 5 seconds
- [ ] No cross-group blast radius (one group crash doesn't affect others)
- [ ] Per-group metrics visible in Grafana
- [ ] Zero-downtime group deployment
