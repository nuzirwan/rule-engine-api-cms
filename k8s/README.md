# nzr-rules-engine — Kubernetes Manifests

Kustomize-based manifests for deploying the rules engine stack to Kubernetes.

## Directory Structure

```
k8s/
├── base/                       # Base manifests (Kustomize)
│   ├── kustomization.yaml      # Kustomize root — lists all resources
│   ├── configmap.yaml          # Non-secret configuration (engine + CMS)
│   ├── secrets.yaml            # Placeholder Secrets (replace before applying)
│   ├── engine-deployment.yaml  # Engine Deployment + Service + HPA
│   └── cms-deployment.yaml     # CMS Deployment + Service
├── overlays/
│   ├── dev/                    # Dev overlay: single replica, dev image tags
│   │   └── kustomization.yaml
│   └── prod/                   # Prod overlay: HA replicas, larger limits
│       └── kustomization.yaml
└── README.md
```

## Prerequisites

- Kubernetes 1.26+
- `kubectl` with kustomize plugin (kubectl 1.14+) **or** `kustomize` CLI
- Image registry accessible from the cluster (engine + CMS images pushed)
- Databases provisioned externally (see [Database Dependencies](#database-dependencies))

## Quick Start

### 1. Preview manifests (dry-run)

```bash
# Base only
kubectl kustomize k8s/base/

# Dev overlay
kubectl kustomize k8s/overlays/dev/

# Prod overlay
kubectl kustomize k8s/overlays/prod/
```

### 2. Set secrets

**Never apply the placeholder `secrets.yaml` in production.**

Replace placeholder values in `k8s/base/secrets.yaml` with real values, or use
one of the secret management patterns below.

```bash
# Quick local override (for dev/testing only)
kubectl create secret generic engine-secrets \
  --from-literal=CONFIG_DSN='postgres://rule_engine:password@postgres-engine:5432/rule_engine?sslmode=disable' \
  --from-literal=ADMIN_TOKENS='your-secure-token' \
  -n nzr-rules-engine

kubectl create secret generic cms-secrets \
  --from-literal=CMS_DB_PASSWORD='your-password' \
  --from-literal=APP_KEYS='key1,key2,key3,key4' \
  --from-literal=API_TOKEN_SALT='$(openssl rand -base64 32)' \
  --from-literal=ADMIN_JWT_SECRET='$(openssl rand -base64 32)' \
  --from-literal=JWT_SECRET='$(openssl rand -base64 32)' \
  --from-literal=TRANSFER_TOKEN_SALT='$(openssl rand -base64 32)' \
  --from-literal=ENCRYPTION_KEY='$(openssl rand -hex 32)' \
  --from-literal=ADMIN_API_OPERATOR_TOKEN='your-operator-token' \
  -n nzr-rules-engine
```

### 3. Apply

```bash
# Create namespace first
kubectl create namespace nzr-rules-engine

# Apply dev overlay
kubectl apply -k k8s/overlays/dev/

# Apply prod overlay
kubectl apply -k k8s/overlays/prod/
```

## Database Dependencies

The engine and CMS expect these services to be reachable by their service names:

| Service Name | Port | Consumer | Notes |
|---|---|---|---|
| `postgres-engine` | 5432 | Engine | Config store; `CONFIG_DSN` must target it |
| `postgres-cms` | 5432 | CMS | Strapi data store; `CMS_DB_HOST` |
| `valkey` | 6379 | Engine | Config cache; `VALKEY_ADDR` |

For production, use managed services (AWS RDS, Cloud SQL, ElastiCache) and
update the ConfigMap/Secrets with the actual endpoint addresses. For local
cluster testing, deploy Postgres and Valkey via Helm:

```bash
# Postgres for engine
helm install postgres-engine bitnami/postgresql \
  --set auth.database=rule_engine \
  --set auth.username=rule_engine \
  --set auth.password=rule_engine \
  -n nzr-rules-engine

# Postgres for CMS
helm install postgres-cms bitnami/postgresql \
  --set auth.database=strapi_cms \
  --set auth.username=strapi_cms \
  --set auth.password=strapi_cms \
  -n nzr-rules-engine

# Valkey (Redis-compatible)
helm install valkey bitnami/valkey -n nzr-rules-engine
```

## Secret Management (Production)

### AWS SSM Parameter Store

Store secrets as `SecureString` parameters at:

```
/rule-engine/{env}/engine/CONFIG_DSN
/rule-engine/{env}/engine/ADMIN_TOKENS
/rule-engine/{env}/cms/CMS_DB_PASSWORD
/rule-engine/{env}/cms/APP_KEYS
... etc
```

Use [External Secrets Operator (ESO)](https://external-secrets.io) with an
`ExternalSecret` CR to sync SSM params into Kubernetes Secrets automatically:

```yaml
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: engine-secrets
  namespace: nzr-rules-engine
spec:
  refreshInterval: 1h
  secretStoreRef:
    name: aws-ssm
    kind: ClusterSecretStore
  target:
    name: engine-secrets
  data:
    - secretKey: CONFIG_DSN
      remoteRef:
        key: /rule-engine/prod/engine/CONFIG_DSN
    - secretKey: ADMIN_TOKENS
      remoteRef:
        key: /rule-engine/prod/engine/ADMIN_TOKENS
```

### HashiCorp Vault

Store secrets at `secret/rule-engine/{env}/{component}/{secret-name}`.
Use Vault Agent sidecar or ESO with Vault as a `SecretStore`.

### Sealed Secrets

For GitOps workflows where secrets must live in the repo (encrypted):

```bash
# Encrypt a secret for Git storage
kubeseal --format yaml < k8s/base/secrets.yaml > k8s/base/sealed-secrets.yaml
```

## Health Checks

| Component | Liveness | Readiness | Notes |
|---|---|---|---|
| Engine | `GET /livez` | `GET /readyz` | Readiness gates on Postgres connectivity (2s timeout) |
| CMS | `GET /_health` | `GET /_health` | Strapi 5 built-in endpoint; 60s start period |

## Autoscaling (HPA)

The engine has an HPA configured for CPU (70%) and memory (80%) utilization.

| Env | Min Replicas | Max Replicas |
|---|---|---|
| dev | 1 | 1 |
| prod | 2 | 10 |

Tune `averageUtilization` thresholds based on actual p95 CPU/memory from
`/metrics` (Prometheus) after real traffic baselines are established.

## Resource Limits

| Component | CPU Request | CPU Limit | Memory Request | Memory Limit |
|---|---|---|---|---|
| Engine (base) | 100m | 500m | 128Mi | 512Mi |
| Engine (prod) | 250m | 1000m | 256Mi | 1Gi |
| CMS (base) | 200m | 500m | 256Mi | 1Gi |
| CMS (prod) | 500m | 1000m | 512Mi | 2Gi |

## Uploading CMS Media (Prod)

The CMS writes uploads to `/app/public/uploads`. The base manifest uses
`emptyDir` (ephemeral). The prod overlay replaces this with a
`PersistentVolumeClaim` named `cms-uploads`. Create it before applying:

```bash
kubectl apply -f - <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: cms-uploads
  namespace: nzr-rules-engine
spec:
  accessModes: [ReadWriteOnce]
  resources:
    requests:
      storage: 10Gi
EOF
```

For multi-replica CMS (if using shared storage), use `ReadWriteMany` with NFS
or a cloud-native shared filesystem (AWS EFS, Azure Files).

## Standards Applied

| Standard | Applied |
|---|---|
| `config-and-secrets` | All config via ConfigMap (env), all secrets via Secret refs — nothing hardcoded |
| `twelve-factor-app` | Factor III: config from env at runtime; no baked-in secrets |
| `security-and-authz` | Non-root containers (uid 1000/1001), `readOnlyRootFilesystem: true` on engine, `allowPrivilegeEscalation: false`, drop ALL capabilities |
| `health-checks-liveness-readiness` | Separate liveness (`/livez`) and readiness (`/readyz`) for engine; CMS on `/_health` |
| `graceful-shutdown` | `terminationGracePeriodSeconds: 30` on both deployments |
| `performance-defaults` | CPU/memory bounds on every container |
| `ci-cd-and-delivery` | Images tagged by version; Kustomize overlays for environment-specific promotion |
