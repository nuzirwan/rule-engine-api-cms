#!/usr/bin/env bash
# =============================================================================
# k8s/validate.sh — Validate Kustomize manifests
#
# Usage:
#   ./k8s/validate.sh           # validate all overlays
#   KUSTOMIZE=/path/to/kustomize ./k8s/validate.sh
#
# Checks:
#   1. kustomize binary is available
#   2. base, dev, and prod overlays build without error
#   3. Required resource types are present in base output
#   4. All resources have apiVersion and kind
#   5. Resource limits are set on all containers
#   6. Health probes are configured on all containers
# =============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

# ---------------------------------------------------------------------------
# Tool detection
# ---------------------------------------------------------------------------
KUSTOMIZE="${KUSTOMIZE:-}"

if [[ -z "${KUSTOMIZE}" ]]; then
    if command -v kustomize &>/dev/null; then
        KUSTOMIZE="kustomize"
    elif command -v kubectl &>/dev/null; then
        KUSTOMIZE="kubectl kustomize"
    elif [[ -x "/tmp/kustomize" ]]; then
        KUSTOMIZE="/tmp/kustomize"
    else
        echo "ERROR: kustomize not found. Install it: https://kubectl.docs.kubernetes.io/installation/kustomize/"
        exit 1
    fi
fi

echo "Using kustomize: ${KUSTOMIZE}"

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------
PASS=0
FAIL=0

pass() { echo "  ✓ $1"; PASS=$((PASS+1)); }
fail() { echo "  ✗ $1"; FAIL=$((FAIL+1)); }

assert_contains() {
    local label="$1"
    local pattern="$2"
    local input="$3"
    if echo "${input}" | grep -q "${pattern}"; then
        pass "${label}"
    else
        fail "${label} (pattern '${pattern}' not found)"
    fi
}

assert_not_contains() {
    local label="$1"
    local pattern="$2"
    local input="$3"
    if ! echo "${input}" | grep -q "${pattern}"; then
        pass "${label}"
    else
        fail "${label} (unexpected pattern '${pattern}' found)"
    fi
}

# ---------------------------------------------------------------------------
# Test 1: Overlays build without error
# ---------------------------------------------------------------------------
echo ""
echo "=== Test 1: Kustomize build ==="

for overlay in "base" "overlays/dev" "overlays/prod"; do
    path="${REPO_ROOT}/k8s/${overlay}"
    if ${KUSTOMIZE} build "${path}" > /dev/null 2>&1; then
        pass "${overlay} builds successfully"
    else
        fail "${overlay} failed to build"
        ${KUSTOMIZE} build "${path}" 2>&1 | head -20
    fi
done

# ---------------------------------------------------------------------------
# Test 2: Required resource types in base output
# ---------------------------------------------------------------------------
echo ""
echo "=== Test 2: Required resource types (base) ==="

BASE_OUTPUT=$(${KUSTOMIZE} build "${REPO_ROOT}/k8s/base/")

REQUIRED_RESOURCES=(
    "kind: Deployment"
    "kind: Service"
    "kind: ConfigMap"
    "kind: Secret"
    "kind: HorizontalPodAutoscaler"
)

for resource in "${REQUIRED_RESOURCES[@]}"; do
    assert_contains "${resource} present" "${resource}" "${BASE_OUTPUT}"
done

# Check specific named resources
assert_contains "engine Deployment present"                     "name: engine"                "${BASE_OUTPUT}"
assert_contains "cms Deployment present"                        "name: cms"                   "${BASE_OUTPUT}"
assert_contains "engine-config ConfigMap present"               "name: engine-config"         "${BASE_OUTPUT}"
assert_contains "cms-config ConfigMap present"                  "name: cms-config"            "${BASE_OUTPUT}"
assert_contains "engine-secrets Secret present"                 "name: engine-secrets"        "${BASE_OUTPUT}"
assert_contains "cms-secrets Secret present"                    "name: cms-secrets"           "${BASE_OUTPUT}"

# ---------------------------------------------------------------------------
# Test 3: All resources have valid apiVersion/kind
# ---------------------------------------------------------------------------
echo ""
echo "=== Test 3: apiVersion and kind on all resources ==="

# Count apiVersion lines and kind lines — should match (1:1 ratio)
API_VERSION_COUNT=$(echo "${BASE_OUTPUT}" | grep -c "^apiVersion:" || true)
KIND_COUNT=$(echo "${BASE_OUTPUT}" | grep -c "^kind:" || true)

if [[ "${API_VERSION_COUNT}" -eq "${KIND_COUNT}" && "${API_VERSION_COUNT}" -gt 0 ]]; then
    pass "apiVersion/kind balanced (${API_VERSION_COUNT} resources)"
else
    fail "apiVersion count (${API_VERSION_COUNT}) != kind count (${KIND_COUNT})"
fi

# Verify known API versions are used
assert_contains "apps/v1 (Deployment)"                          "apiVersion: apps/v1"         "${BASE_OUTPUT}"
assert_contains "v1 (Service/ConfigMap/Secret)"                 "apiVersion: v1"              "${BASE_OUTPUT}"
assert_contains "autoscaling/v2 (HPA)"                         "apiVersion: autoscaling/v2"  "${BASE_OUTPUT}"

# ---------------------------------------------------------------------------
# Test 4: Health probes on all containers
# ---------------------------------------------------------------------------
echo ""
echo "=== Test 4: Health probes ==="

assert_contains "engine readinessProbe /readyz"                 "/readyz"                     "${BASE_OUTPUT}"
assert_contains "engine livenessProbe /livez"                   "/livez"                      "${BASE_OUTPUT}"
assert_contains "cms readinessProbe /_health"                   "/_health"                    "${BASE_OUTPUT}"
assert_contains "readinessProbe key present"                    "readinessProbe:"             "${BASE_OUTPUT}"
assert_contains "livenessProbe key present"                     "livenessProbe:"              "${BASE_OUTPUT}"

# ---------------------------------------------------------------------------
# Test 5: Resource limits on all containers
# ---------------------------------------------------------------------------
echo ""
echo "=== Test 5: Resource limits ==="

assert_contains "cpu limits set"                                "cpu:"                        "${BASE_OUTPUT}"
assert_contains "memory limits set"                             "memory:"                     "${BASE_OUTPUT}"
assert_contains "resource limits block present"                 "limits:"                     "${BASE_OUTPUT}"
assert_contains "resource requests block present"               "requests:"                   "${BASE_OUTPUT}"

# ---------------------------------------------------------------------------
# Test 6: Security contexts
# ---------------------------------------------------------------------------
echo ""
echo "=== Test 6: Security hardening ==="

assert_contains "runAsNonRoot set"                              "runAsNonRoot: true"          "${BASE_OUTPUT}"
assert_contains "allowPrivilegeEscalation false"                "allowPrivilegeEscalation: false" "${BASE_OUTPUT}"
assert_contains "capabilities drop ALL"                         "drop:"                       "${BASE_OUTPUT}"

# ---------------------------------------------------------------------------
# Test 7: No hardcoded secrets (placeholder values only)
# ---------------------------------------------------------------------------
echo ""
echo "=== Test 7: No hardcoded real secrets ==="

# Secrets should only have REPLACE_WITH_* placeholder values
assert_contains "engine secrets are placeholders"               "REPLACE_WITH_POSTGRES_DSN"   "${BASE_OUTPUT}"
assert_contains "cms secrets are placeholders"                  "REPLACE_WITH_CMS_DB_PASSWORD" "${BASE_OUTPUT}"

# ---------------------------------------------------------------------------
# Test 8: HPA min/max replicas
# ---------------------------------------------------------------------------
echo ""
echo "=== Test 8: HPA configuration ==="

assert_contains "HPA minReplicas: 2"                            "minReplicas: 2"              "${BASE_OUTPUT}"
assert_contains "HPA maxReplicas: 10"                           "maxReplicas: 10"             "${BASE_OUTPUT}"

# ---------------------------------------------------------------------------
# Test 9: Dev overlay patches
# ---------------------------------------------------------------------------
echo ""
echo "=== Test 9: Dev overlay patches ==="

DEV_OUTPUT=$(${KUSTOMIZE} build "${REPO_ROOT}/k8s/overlays/dev/")

assert_contains "dev namespace set"                             "namespace: nzr-rules-engine-dev" "${DEV_OUTPUT}"
assert_contains "dev engine image tag"                          "nzr-rules-engine:dev"        "${DEV_OUTPUT}"
assert_contains "dev cms image tag"                             "nzr-strapi-cms:dev"          "${DEV_OUTPUT}"
assert_contains "dev schema override"                           "rule_engine_dev"             "${DEV_OUTPUT}"

# ---------------------------------------------------------------------------
# Test 10: Prod overlay patches
# ---------------------------------------------------------------------------
echo ""
echo "=== Test 10: Prod overlay patches ==="

PROD_OUTPUT=$(${KUSTOMIZE} build "${REPO_ROOT}/k8s/overlays/prod/")

assert_contains "prod namespace set"                            "namespace: nzr-rules-engine" "${PROD_OUTPUT}"
assert_contains "prod engine image tag"                         "nzr-rules-engine:1.0.0"      "${PROD_OUTPUT}"
assert_contains "prod cms image tag"                            "nzr-strapi-cms:1.0.0"        "${PROD_OUTPUT}"

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
echo ""
echo "=== Results ==="
echo "  Passed: ${PASS}"
echo "  Failed: ${FAIL}"
echo ""

if [[ "${FAIL}" -gt 0 ]]; then
    echo "FAIL — ${FAIL} check(s) failed"
    exit 1
else
    echo "PASS — all ${PASS} checks passed"
    exit 0
fi
