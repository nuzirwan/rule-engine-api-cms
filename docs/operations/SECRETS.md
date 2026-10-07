# Secrets Management — nzr-rules-engine Stack

This document covers the secrets inventory for the rules engine stack, their sensitivity
classification, recommended storage paths for AWS SSM Parameter Store and HashiCorp Vault,
rotation considerations, and integration patterns.

> **Policy**: secrets never live in source code, Docker images, or unencrypted config files.
> At rest, every 🔒 secret from `DEPLOYMENT.md` must be in a secret manager. The engine and CMS
> read secrets from environment variables that are injected at container start time by an init
> script, a sidecar, or a platform-native secret mount.

---

## Secrets Inventory

### Engine Secrets

| Secret Env Var | Component | Sensitivity | Notes |
|----------------|-----------|-------------|-------|
| `CONFIG_DSN` | Engine | **High** | Full Postgres DSN — includes host, user, and password. Compromise exposes the config store (all flows, connections, JDMs). |
| `ENGINE_DB_PASSWORD` | Engine | **High** | Postgres password for the engine's config-store database. Used by Docker Compose to build `CONFIG_DSN`. |
| `ADMIN_TOKENS` | Engine | **High** | SHA-256-hashed operator token allow-list. Compromise allows control-plane manipulation (publish/rollback flows). The plaintext tokens themselves never appear here; only hashes are stored. |
| `ORDERS_PG_DSN` | Engine | **High** | Postgres DSN for the orders data source — includes credentials for the application database. |

### CMS Secrets

| Secret Env Var | Component | Sensitivity | Notes |
|----------------|-----------|-------------|-------|
| `CMS_DB_PASSWORD` | CMS | **High** | Postgres password for the CMS data store. |
| `APP_KEYS` | CMS | **High** | Session cookie signing keys. Compromise allows session forgery. |
| `API_TOKEN_SALT` | CMS | **High** | Salt for API token hashing. Changing this invalidates all existing API tokens. |
| `ADMIN_JWT_SECRET` | CMS | **High** | Signs admin panel JWTs. Compromise allows admin panel session forgery. |
| `JWT_SECRET` | CMS | **High** | Signs end-user JWTs. Compromise allows user impersonation. |
| `TRANSFER_TOKEN_SALT` | CMS | **High** | Salt for data transfer tokens. Changing this invalidates all existing transfer tokens. |
| `ENCRYPTION_KEY` | CMS | **Critical** | Encrypts sensitive fields in the CMS database. Loss of this key makes encrypted data permanently unrecoverable. Must be backed up separately and rotated with re-encryption of all encrypted fields. |
| `ADMIN_API_OPERATOR_TOKEN` | CMS | **High** | Plaintext operator token sent to the engine admin API. Must be hash-matched in the engine's `ADMIN_TOKENS`. |
| `CMS_DB_SSL_KEY` | CMS | **High** | Client TLS private key for the CMS database connection. Only used when `CMS_DB_SSL=true`. |

---

## Recommended Secret Paths

### AWS SSM Parameter Store

Use the `SecureString` type for all secrets. Structure paths as
`/rule-engine/<env>/<component>/<secret>`.

```
# Engine
/rule-engine/prod/engine/config-dsn
/rule-engine/prod/engine/engine-db-password
/rule-engine/prod/engine/admin-tokens
/rule-engine/prod/engine/orders-pg-dsn

# CMS
/rule-engine/prod/cms/cms-db-password
/rule-engine/prod/cms/app-keys
/rule-engine/prod/cms/api-token-salt
/rule-engine/prod/cms/admin-jwt-secret
/rule-engine/prod/cms/jwt-secret
/rule-engine/prod/cms/transfer-token-salt
/rule-engine/prod/cms/encryption-key
/rule-engine/prod/cms/admin-api-operator-token
/rule-engine/prod/cms/cms-db-ssl-key

# Staging (same shape)
/rule-engine/staging/engine/config-dsn
/rule-engine/staging/cms/encryption-key
# ... etc
```

**IAM Policy** — scope the instance role to only the paths it needs:

```json
{
  "Effect": "Allow",
  "Action": ["ssm:GetParameter", "ssm:GetParameters", "ssm:GetParametersByPath"],
  "Resource": "arn:aws:ssm:<region>:<account-id>:parameter/rule-engine/prod/*"
}
```

Separate IAM roles for engine and CMS so each only reads its own subtree:

```
Engine role  →  /rule-engine/prod/engine/*
CMS role     →  /rule-engine/prod/cms/*
```

### HashiCorp Vault

Use the KV v2 secrets engine. Structure paths as
`secret/rule-engine/<env>/<component>/<secret>`.

```
# Enable KV v2 (one-time)
vault secrets enable -path=secret kv-v2

# Engine secrets
vault kv put secret/rule-engine/prod/engine \
  config-dsn="postgres://rule_engine:<pass>@db:5432/rule_engine?sslmode=require" \
  engine-db-password="<pass>" \
  admin-tokens="<hash>:op:strapi:flow.read,flow.write,flow.publish" \
  orders-pg-dsn="postgres://app:<pass>@orders-db:5432/orders?sslmode=require"

# CMS secrets
vault kv put secret/rule-engine/prod/cms \
  cms-db-password="<pass>" \
  app-keys="<key1>,<key2>" \
  api-token-salt="<hex>" \
  admin-jwt-secret="<base64>" \
  jwt-secret="<base64>" \
  transfer-token-salt="<hex>" \
  encryption-key="<32-hex-chars>" \
  admin-api-operator-token="<plaintext-token>"
```

**Vault Policy**:

```hcl
# engine-policy.hcl
path "secret/data/rule-engine/prod/engine" {
  capabilities = ["read"]
}

# cms-policy.hcl
path "secret/data/rule-engine/prod/cms" {
  capabilities = ["read"]
}
```

**AppRole auth** (recommended for containers):

```bash
vault auth enable approle
vault write auth/approle/role/nzr-engine \
  token_policies="engine-policy" \
  token_ttl=1h \
  token_max_ttl=4h
vault write auth/approle/role/nzr-cms \
  token_policies="cms-policy" \
  token_ttl=1h \
  token_max_ttl=4h
```

---

## Rotation Considerations

| Secret | Rotation Frequency | Impact on Rotation | Steps |
|--------|-------------------|--------------------|-------|
| `ENGINE_DB_PASSWORD` / `CONFIG_DSN` | Quarterly | Requires engine restart | 1. Update password in DB. 2. Update secret in Vault/SSM. 3. Rolling restart engine. |
| `CMS_DB_PASSWORD` | Quarterly | Requires CMS restart | 1. Update password in DB. 2. Update secret. 3. Rolling restart CMS. |
| `ORDERS_PG_DSN` | Quarterly | Requires engine restart | Same pattern as engine DB password. |
| `ADMIN_TOKENS` | On compromise | Requires engine restart | 1. Generate new token + hash. 2. Add new entry to `ADMIN_TOKENS`. 3. Update `ADMIN_API_OPERATOR_TOKEN` in CMS. 4. Restart engine. 5. Remove old hash entry. |
| `ADMIN_API_OPERATOR_TOKEN` | On compromise / quarterly | Requires CMS restart + engine token list update | Pair with `ADMIN_TOKENS` rotation above. |
| `APP_KEYS` | Annually | Invalidates active sessions | Users are signed out. Schedule during low-traffic window. |
| `ADMIN_JWT_SECRET` | Annually | Invalidates admin sessions | Strapi admin users are signed out. |
| `JWT_SECRET` | Annually | Invalidates user sessions | End-users are signed out. Schedule during low-traffic window. |
| `API_TOKEN_SALT` | On compromise | **Invalidates all API tokens** | All API token holders must regenerate their tokens. High-impact: notify stakeholders before rotating. |
| `TRANSFER_TOKEN_SALT` | On compromise | Invalidates all transfer tokens | Any in-progress data transfers will fail. |
| `ENCRYPTION_KEY` | Never (or only with re-encryption) | **Data loss risk if done incorrectly** | 1. Export and decrypt all encrypted fields using the old key. 2. Re-encrypt with the new key. 3. Rotate key in secret manager. 4. Restart CMS. **Do not rotate without a tested re-encryption procedure.** |
| `CMS_DB_SSL_KEY` | Annually | Requires CMS restart | Follow your PKI's certificate rotation process. |

### Rotation Procedure Summary

1. Update the secret value in Vault/SSM (the old value stays as a previous version).
2. Trigger a rolling restart of the affected service (zero-downtime for stateless services).
3. Verify the service is healthy (`/readyz`, `/_health`) after restart.
4. Delete the previous secret version once confirmed healthy.

---

## AWS SSM Parameter Store Integration

### Retrieve at Container Start (init script pattern)

```bash
#!/bin/bash
# scripts/load-secrets-ssm.sh
# Run as an entrypoint init script before starting the main process.
# Requires: AWS CLI v2, instance role with SSM read permissions.

set -euo pipefail

ENV="${DEPLOY_ENV:-prod}"
REGION="${AWS_REGION:-ap-southeast-1}"

get_param() {
  aws ssm get-parameter \
    --name "/rule-engine/${ENV}/${1}" \
    --with-decryption \
    --query Parameter.Value \
    --output text \
    --region "${REGION}"
}

# Engine secrets
export CONFIG_DSN=$(get_param "engine/config-dsn")
export ADMIN_TOKENS=$(get_param "engine/admin-tokens")
export ORDERS_PG_DSN=$(get_param "engine/orders-pg-dsn")

exec "$@"
```

Usage in `Dockerfile` (engine):

```dockerfile
COPY scripts/load-secrets-ssm.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
ENTRYPOINT ["/entrypoint.sh"]
CMD ["/app/engine"]
```

### Retrieve with AWS Secrets Manager (alternative)

For secrets that need automatic rotation, use AWS Secrets Manager instead of SSM. The path
convention stays the same: `rule-engine/prod/engine/config-dsn`.

### ECS Task Definition Pattern

Inject secrets directly into container environment using `secrets` in the task definition:

```json
{
  "containerDefinitions": [{
    "name": "nzr-engine",
    "secrets": [
      {
        "name": "CONFIG_DSN",
        "valueFrom": "arn:aws:ssm:ap-southeast-1:123456789:parameter/rule-engine/prod/engine/config-dsn"
      },
      {
        "name": "ADMIN_TOKENS",
        "valueFrom": "arn:aws:ssm:ap-southeast-1:123456789:parameter/rule-engine/prod/engine/admin-tokens"
      }
    ]
  }]
}
```

---

## HashiCorp Vault Integration

### Vault Agent Sidecar Pattern

Run Vault Agent as a sidecar to inject secrets into a shared in-memory filesystem.

```hcl
# vault-agent-config.hcl
auto_auth {
  method "aws" {
    config = {
      type = "iam"
      role = "nzr-engine"
    }
  }
}

template {
  contents = <<EOT
{{ with secret "secret/rule-engine/prod/engine" }}
export CONFIG_DSN="{{ .Data.data.config-dsn }}"
export ADMIN_TOKENS="{{ .Data.data.admin-tokens }}"
export ORDERS_PG_DSN="{{ .Data.data.orders-pg-dsn }}"
{{ end }}
EOT
  destination = "/vault/secrets/engine.env"
  command     = "/bin/sh -c 'kill -HUP $(pgrep engine) 2>/dev/null || true'"
}
```

Source the rendered env file in the container entrypoint:

```bash
#!/bin/bash
# Wait for Vault Agent to render the secrets file
while [ ! -f /vault/secrets/engine.env ]; do sleep 1; done
source /vault/secrets/engine.env
exec /app/engine "$@"
```

### Engine SecretProvider Extension Point

The engine has a `SecretProvider` interface (`internal/connect/secret.go`) with `Resolve()` and
`Watch()` methods. Today only `envProvider` is implemented (reads `os.Getenv`). To add native
Vault or SSM support without an init script:

1. Implement `SecretProvider` in a new file (e.g. `internal/connect/vault_provider.go`).
2. Parse a scheme-prefixed reference in connection defs:
   - `vault:secret/rule-engine/prod/engine/orders-dsn`
   - `ssm:/rule-engine/prod/engine/orders-dsn`
3. Register the provider via the scheme in `cmd/engine/main.go`.
4. The `Watch()` method can trigger connection pool refresh on secret rotation.

This is the recommended path for production: connections keep their secret references in the
config store as scheme-URI strings; the provider resolves the actual value at runtime and
refreshes on rotation — no restart required.

---

## Kubernetes Integration

### Secret Objects

```yaml
# k8s/secrets/engine-secrets.yaml
apiVersion: v1
kind: Secret
metadata:
  name: nzr-engine-secrets
  namespace: rule-engine
type: Opaque
# Values must be base64-encoded
# Use External Secrets Operator or Sealed Secrets — never commit plaintext here
stringData: {}
```

Use [External Secrets Operator](https://external-secrets.io/) to sync from Vault or SSM:

```yaml
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: nzr-engine-secrets
  namespace: rule-engine
spec:
  refreshInterval: 1h
  secretStoreRef:
    name: vault-backend
    kind: ClusterSecretStore
  target:
    name: nzr-engine-secrets
  data:
    - secretKey: CONFIG_DSN
      remoteRef:
        key: secret/rule-engine/prod/engine
        property: config-dsn
    - secretKey: ADMIN_TOKENS
      remoteRef:
        key: secret/rule-engine/prod/engine
        property: admin-tokens
```

### ConfigMap (non-secrets)

Non-sensitive configuration can live in a ConfigMap:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: nzr-engine-config
  namespace: rule-engine
data:
  CONFIG_SCHEMA: "rule_engine"
  ENGINE_ADDR: ":8080"
  ADMIN_ENABLED: "true"
  VALKEY_ADDR: "valkey:6379"
```

---

## What NOT to Do

- ❌ Never commit `.env`, `.env.docker`, or any file containing real secret values to Git.
- ❌ Never log a secret value — the engine already redacts `CONFIG_DSN` in logs via `redactDSN()`. Do not add logging for other secret vars.
- ❌ Never pass secrets as Docker build args (`ARG`) — they appear in image layer history.
- ❌ Never use the same `ENCRYPTION_KEY` across environments — a staging key compromise must not endanger production data.
- ❌ Never set `CMS_DB_SSL_REJECT_UNAUTHORIZED=false` in production.
- ❌ Never share the engine `ADMIN_TOKENS` allow-list across environments.

---

## See Also

- `DEPLOYMENT.md` — complete env var reference with required/optional classification
- `DOCKER.md` — Docker Compose setup and multi-environment usage
- `engine/internal/connect/secret.go` — `SecretProvider` interface (extension point for Vault/SSM)
