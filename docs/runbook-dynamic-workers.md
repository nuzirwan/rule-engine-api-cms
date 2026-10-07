# Runbook: Dynamic Workers Operations

This runbook covers common operational issues for the NZR Rules Engine dynamic workers system and provides step-by-step troubleshooting procedures.

---

## Overview

The dynamic workers architecture consists of:

- **Gateway**: Central dispatcher that routes flow execution requests to worker pods. Manages scaling (via KEDA), circuit breaking, request queuing during cold starts, and config distribution.
- **Workers**: Ephemeral pods created per tenant/group that execute rule flows. Scale from 0 when idle and scale up based on request load.
- **KEDA**: Kubernetes Event-Driven Autoscaling that manages worker deployments based on Prometheus metrics.
- **Valkey/Redis**: Hot config cache for fast worker bootstrap.
- **PostgreSQL**: Persistent config store (flows, JDMs, connections).

Request flow: Client → Gateway → (scale if needed) → Worker → Flow execution → Response

---

## Quick Reference

### Key Metrics

| Metric | Description |
|--------|-------------|
| `gateway_dispatch_total{group,status}` | Total dispatch requests (KEDA trigger) |
| `gateway_dispatch_duration_seconds{group}` | Dispatch latency |
| `gateway_worker_ready{group}` | Worker readiness (1=ready, 0=not) |
| `gateway_worker_replicas{group}` | Current replica count |
| `gateway_circuit_breaker_state{group}` | 0=closed, 1=half-open, 2=open |
| `gateway_cold_start_duration_seconds{group}` | Cold start latency |
| `gateway_queue_length{group}` | Requests queued during cold start |
| `gateway_queue_timeout_total{group}` | Queued requests that timed out |
| `worker_requests_total{group,flow_id,status}` | Worker request count |
| `worker_request_duration_seconds{group,flow_id}` | Worker execution latency |
| `worker_config_version{group}` | Loaded config version |
| `worker_reload_total{group,status}` | Config reload attempts |

### Key Endpoints

| Endpoint | Port | Description |
|----------|------|-------------|
| Gateway `/metrics` | 8080 | Prometheus metrics |
| Gateway `/healthz` | 8080 | Liveness probe |
| Gateway `/readyz` | 8080 | Readiness probe |
| Worker `/metrics` | 8081 | Worker metrics |
| Worker `/healthz` | 8081 | Worker liveness |
| Worker `/version` | 8081 | Current config version |

### Useful kubectl Commands

```bash
# List all worker pods for a group
kubectl get pods -l app=flow-worker,group=<GROUP> -n <NAMESPACE>

# Watch pod status
kubectl get pods -l app=flow-worker -n <NAMESPACE> -w

# Get pod events
kubectl describe pod <POD_NAME> -n <NAMESPACE>

# Get pod logs
kubectl logs <POD_NAME> -n <NAMESPACE> --tail=100

# Get previous container logs (after crash)
kubectl logs <POD_NAME> -n <NAMESPACE> --previous

# Check KEDA ScaledObject status
kubectl get scaledobject -n <NAMESPACE>
kubectl describe scaledobject flow-worker-<GROUP> -n <NAMESPACE>

# Check HPA status (created by KEDA)
kubectl get hpa -n <NAMESPACE>
```

---

## Worker Not Starting

### Symptoms

- Pod stuck in `Pending` state
- Pod stuck in `ContainerCreating` state
- `gateway_worker_ready{group="X"} == 0` for extended period
- Requests timing out during cold start

### Diagnostic Steps

1. **Check pod status and events**:
   ```bash
   kubectl get pods -l app=flow-worker,group=<GROUP> -n <NAMESPACE>
   kubectl describe pod <POD_NAME> -n <NAMESPACE>
   ```

2. **Look for events section** — common issues appear here:
   ```bash
   kubectl get events --field-selector involvedObject.name=<POD_NAME> -n <NAMESPACE>
   ```

3. **Check resource quotas**:
   ```bash
   kubectl describe resourcequota -n <NAMESPACE>
   kubectl describe limitrange -n <NAMESPACE>
   ```

4. **Check node resources**:
   ```bash
   kubectl describe nodes | grep -A5 "Allocated resources"
   kubectl top nodes
   ```

### Common Causes and Resolution

| Cause | Symptoms | Resolution |
|-------|----------|------------|
| **Image pull failure** | `ImagePullBackOff`, `ErrImagePull` events | Verify image exists, check registry credentials, check network to registry |
| **Secret not found** | `CreateContainerConfigError` | Verify secrets exist: `kubectl get secret <SECRET_NAME> -n <NAMESPACE>` |
| **Insufficient resources** | `Insufficient cpu/memory` in events | Reduce resource requests or add node capacity |
| **Node selector mismatch** | `FailedScheduling` | Verify node labels match pod's `nodeSelector` |
| **PVC not bound** | `pod has unbound PersistentVolumeClaims` | Check PVC status and storage provisioner |
| **Init container failure** | Init container stuck/failing | Check init container logs: `kubectl logs <POD> -c <INIT_CONTAINER>` |

### Resolution Steps

1. **For image pull issues**:
   ```bash
   # Verify image exists
   docker pull <IMAGE_URL>
   
   # Check imagePullSecrets
   kubectl get pod <POD_NAME> -o jsonpath='{.spec.imagePullSecrets}' -n <NAMESPACE>
   kubectl get secret <SECRET_NAME> -o jsonpath='{.data.\.dockerconfigjson}' -n <NAMESPACE> | base64 -d
   ```

2. **For resource issues**:
   ```bash
   # Check current requests in deployment template
   kubectl get deployment flow-worker-<GROUP> -o yaml -n <NAMESPACE> | grep -A10 resources
   
   # Scale down other workloads or request node scale-up
   ```

3. **For missing secrets**:
   ```bash
   # List required secrets
   kubectl get deployment flow-worker-<GROUP> -o yaml -n <NAMESPACE> | grep secretKeyRef -A2
   
   # Create missing secret
   kubectl create secret generic <SECRET_NAME> --from-literal=<KEY>=<VALUE> -n <NAMESPACE>
   ```

---

## Worker Crash Loop

### Symptoms

- Pod in `CrashLoopBackOff` status
- `worker_reload_total{status="failure"}` increasing
- Restarts count incrementing
- 503 responses from gateway

### Diagnostic Steps

1. **Check pod status and restart count**:
   ```bash
   kubectl get pods -l app=flow-worker,group=<GROUP> -n <NAMESPACE>
   ```

2. **Get logs from crashed container**:
   ```bash
   # Current container logs
   kubectl logs <POD_NAME> -n <NAMESPACE>
   
   # Previous container logs (after restart)
   kubectl logs <POD_NAME> -n <NAMESPACE> --previous
   ```

3. **Check if OOM killed**:
   ```bash
   kubectl describe pod <POD_NAME> -n <NAMESPACE> | grep -A5 "Last State"
   # Look for "OOMKilled" in termination reason
   ```

4. **Check liveness probe failures**:
   ```bash
   kubectl describe pod <POD_NAME> -n <NAMESPACE> | grep -A10 "Liveness"
   kubectl get events --field-selector reason=Unhealthy -n <NAMESPACE>
   ```

5. **Check environment variables**:
   ```bash
   kubectl exec <POD_NAME> -n <NAMESPACE> -- env | grep -E "(CONFIG_DSN|GATEWAY_URL|GROUP)"
   ```

### Common Causes and Resolution

| Cause | Log Signature | Resolution |
|-------|---------------|------------|
| **Invalid CONFIG_DSN** | `failed to connect to database`, `connection refused` | Verify database connectivity and credentials |
| **Missing secrets** | `secret not found`, `key not found in secret` | Create/update required secrets |
| **OOM (Out of Memory)** | `OOMKilled` in describe output | Increase memory limits or optimize flow execution |
| **Panic in flow execution** | `panic:`, stack trace in logs | Fix flow configuration or report bug |
| **Gateway unreachable** | `failed to fetch config from gateway` | Check network policies, gateway health |
| **Invalid config** | `failed to parse flow`, `invalid JDM` | Validate configuration in database |

### Resolution Steps

1. **For database connection issues**:
   ```bash
   # Test connectivity from within cluster
   kubectl run -it --rm debug --image=postgres:15 --restart=Never -- \
     psql -h <DB_HOST> -U <DB_USER> -d <DB_NAME> -c "SELECT 1"
   
   # Verify CONFIG_DSN format
   # Expected: postgres://user:pass@host:5432/dbname?sslmode=disable
   ```

2. **For OOM issues**:
   ```bash
   # Check current memory usage
   kubectl top pod <POD_NAME> -n <NAMESPACE>
   
   # Increase memory limit in deployment
   kubectl patch deployment flow-worker-<GROUP> -n <NAMESPACE> \
     -p '{"spec":{"template":{"spec":{"containers":[{"name":"worker","resources":{"limits":{"memory":"512Mi"}}}]}}}}'
   ```

3. **For gateway connectivity**:
   ```bash
   # Test from worker pod
   kubectl exec <POD_NAME> -n <NAMESPACE> -- curl -v http://engine-gateway:8080/healthz
   
   # Check network policies
   kubectl get networkpolicy -n <NAMESPACE>
   ```

---

## High Latency

### Symptoms

- `gateway_dispatch_duration_seconds` p99 > SLO (2s)
- `worker_request_duration_seconds` elevated
- `GatewayHighLatency` alert firing
- User complaints about slow responses

### Metrics to Check

```promql
# Gateway dispatch latency (p99)
histogram_quantile(0.99, sum by (group, le) (rate(gateway_dispatch_duration_seconds_bucket[5m])))

# Worker execution latency (p99)
histogram_quantile(0.99, sum by (group, flow_id, le) (rate(worker_request_duration_seconds_bucket[5m])))

# Cold start latency (p95)
histogram_quantile(0.95, sum by (group, le) (rate(gateway_cold_start_duration_seconds_bucket[5m])))

# Request queue length (indicates cold start bottleneck)
gateway_queue_length

# Queue timeout rate
rate(gateway_queue_timeout_total[5m])
```

### Diagnostic Steps

1. **Determine latency source**:
   ```promql
   # Is it cold starts?
   histogram_quantile(0.95, rate(gateway_cold_start_duration_seconds_bucket[5m])) > 5
   
   # Is it worker execution?
   histogram_quantile(0.99, rate(worker_request_duration_seconds_bucket[5m])) > 1
   ```

2. **Check for cold starts**:
   ```bash
   # Worker replica count over time
   kubectl get hpa flow-worker-<GROUP> -n <NAMESPACE> -w
   
   # If replicas frequently go to 0, cold starts are the issue
   ```

3. **Check CPU throttling**:
   ```bash
   # Check current CPU usage vs limits
   kubectl top pod -l app=flow-worker,group=<GROUP> -n <NAMESPACE>
   
   # In Prometheus
   # rate(container_cpu_cfs_throttled_seconds_total[5m])
   ```

4. **Check connection latency**:
   ```promql
   # External connection latency
   histogram_quantile(0.99, rate(nzr_connection_call_duration_seconds_bucket[5m]))
   ```

### Resolution Steps

1. **For cold start latency**:
   ```bash
   # Set minimum replicas to avoid scale-to-zero
   kubectl patch scaledobject flow-worker-<GROUP> -n <NAMESPACE> \
     -p '{"spec":{"minReplicaCount":1}}'
   
   # Or configure idle timeout in KEDA
   ```

2. **For CPU throttling**:
   ```bash
   # Increase CPU limits
   kubectl patch deployment flow-worker-<GROUP> -n <NAMESPACE> \
     -p '{"spec":{"template":{"spec":{"containers":[{"name":"worker","resources":{"limits":{"cpu":"1000m"}}}]}}}}'
   ```

3. **For slow flow execution**:
   - Review flow logic for N+1 queries
   - Check connection pool settings
   - Add caching for frequently accessed data
   - Profile slow flows with tracing

4. **For network latency**:
   - Check if worker and gateway are in same zone
   - Review network policies for unnecessary hops
   - Check DNS resolution time

---

## Config Not Reloading

### Symptoms

- `worker_config_version{group="X"}` shows stale version
- `worker_reload_total{status="failure"}` increasing
- Flow changes not taking effect
- Version endpoint returns old version

### Diagnostic Steps

1. **Check current config version**:
   ```bash
   # From worker pod
   kubectl exec <POD_NAME> -n <NAMESPACE> -- curl -s http://localhost:8081/version
   
   # Expected config version (from database)
   # Query your config management system
   ```

2. **Check reload metrics**:
   ```promql
   # Recent reload failures
   rate(worker_reload_total{status="failure"}[5m])
   
   # Reload success rate
   sum(rate(worker_reload_total{status="success"}[5m])) / sum(rate(worker_reload_total[5m]))
   ```

3. **Check worker logs for reload errors**:
   ```bash
   kubectl logs <POD_NAME> -n <NAMESPACE> | grep -i reload
   kubectl logs <POD_NAME> -n <NAMESPACE> | grep -i error
   ```

4. **Verify gateway connectivity**:
   ```bash
   kubectl exec <POD_NAME> -n <NAMESPACE> -- curl -v http://engine-gateway:8080/config/<GROUP>
   ```

### Common Causes and Resolution

| Cause | Symptoms | Resolution |
|-------|----------|------------|
| **Network to gateway** | Connection refused/timeout in logs | Check network policies, gateway health |
| **Config parsing error** | `failed to parse` in logs | Validate config syntax in database |
| **Valkey connection** | `failed to connect to valkey` | Check Valkey connectivity and credentials |
| **Version mismatch** | Gateway has newer version | Restart worker or trigger manual reload |

### Resolution Steps

1. **Trigger manual reload**:
   ```bash
   # Send SIGHUP to worker process (if supported)
   kubectl exec <POD_NAME> -n <NAMESPACE> -- kill -HUP 1
   
   # Or restart the pod
   kubectl delete pod <POD_NAME> -n <NAMESPACE>
   ```

2. **Fix config parsing issues**:
   ```bash
   # Get raw config and validate
   kubectl exec <GATEWAY_POD> -n <NAMESPACE> -- curl -s http://localhost:8080/config/<GROUP> | jq .
   
   # Check for JSON syntax errors, missing required fields
   ```

3. **Verify Valkey connectivity**:
   ```bash
   kubectl exec <POD_NAME> -n <NAMESPACE> -- nc -zv <VALKEY_HOST> 6379
   
   # Check Valkey for cached config
   kubectl exec <VALKEY_POD> -n <NAMESPACE> -- redis-cli GET config:<GROUP>
   ```

---

## Circuit Breaker Open

### Symptoms

- `gateway_circuit_breaker_state{group="X"} == 2`
- `GatewayCircuitOpen` alert firing
- 503 Service Unavailable responses
- Requests failing fast without reaching workers

### Diagnostic Steps

1. **Check circuit breaker state**:
   ```promql
   # Current state per group (0=closed, 1=half-open, 2=open)
   gateway_circuit_breaker_state
   ```

2. **Check what triggered the circuit to open**:
   ```promql
   # Error rate that caused the trip
   sum by (group) (rate(gateway_dispatch_total{status="error"}[5m]))
   /
   sum by (group) (rate(gateway_dispatch_total[5m]))
   
   # Worker health status
   gateway_worker_health
   ```

3. **Check worker health**:
   ```bash
   kubectl get pods -l app=flow-worker,group=<GROUP> -n <NAMESPACE>
   kubectl logs <POD_NAME> -n <NAMESPACE> --tail=50
   ```

4. **Check gateway logs for circuit breaker events**:
   ```bash
   kubectl logs <GATEWAY_POD> -n <NAMESPACE> | grep -i "circuit\|breaker"
   ```

### Recovery Steps

1. **Wait for automatic recovery** (recommended):
   - Circuit breaker transitions to half-open after configured timeout (typically 30-60s)
   - A successful probe request closes the circuit
   - Monitor: `gateway_circuit_breaker_state` should go 2 → 1 → 0

2. **Fix underlying worker issues first**:
   ```bash
   # Check worker health
   kubectl exec <GATEWAY_POD> -n <NAMESPACE> -- curl http://flow-worker-<GROUP>:8081/healthz
   
   # If unhealthy, investigate worker crash loop or high latency sections
   ```

3. **Manual restart if needed**:
   ```bash
   # Restart all workers for the group
   kubectl rollout restart deployment flow-worker-<GROUP> -n <NAMESPACE>
   
   # Wait for healthy pods
   kubectl rollout status deployment flow-worker-<GROUP> -n <NAMESPACE>
   ```

4. **Restart gateway to reset circuit breaker** (last resort):
   ```bash
   kubectl rollout restart deployment engine-gateway -n <NAMESPACE>
   ```

### Prevention

- Tune circuit breaker thresholds in gateway config:
  - `failure_threshold`: Number of failures to trip (default: 5)
  - `success_threshold`: Successes in half-open to close (default: 2)
  - `timeout`: Time in open state before half-open (default: 30s)
- Set appropriate request timeouts
- Configure retries with backoff for transient failures

---

## Scale-to-Zero Issues

### Symptoms

- `WorkerColdStartSlow` alert firing
- `gateway_cold_start_duration_seconds` p95 > 10s
- `gateway_queue_timeout_total` increasing
- First request after idle period times out
- `gateway_queue_length` spikes during scale-up

### Diagnostic Steps

1. **Check cold start latency**:
   ```promql
   # p95 cold start duration
   histogram_quantile(0.95, sum by (group, le) (rate(gateway_cold_start_duration_seconds_bucket[5m])))
   
   # Cold start frequency
   rate(gateway_cold_start_duration_seconds_count[1h])
   ```

2. **Check KEDA ScaledObject configuration**:
   ```bash
   kubectl get scaledobject flow-worker-<GROUP> -n <NAMESPACE> -o yaml
   kubectl describe scaledobject flow-worker-<GROUP> -n <NAMESPACE>
   ```

3. **Check HPA status**:
   ```bash
   kubectl get hpa -l scaledobject.keda.sh/name=flow-worker-<GROUP> -n <NAMESPACE>
   kubectl describe hpa -l scaledobject.keda.sh/name=flow-worker-<GROUP> -n <NAMESPACE>
   ```

4. **Time the scale-up components**:
   ```bash
   # Watch pod creation
   kubectl get pods -l app=flow-worker,group=<GROUP> -n <NAMESPACE> -w
   
   # Check individual phase timings in events
   kubectl describe pod <POD_NAME> -n <NAMESPACE> | grep -A20 Events
   ```

### Common Causes and Resolution

| Cause | Symptoms | Resolution |
|-------|----------|------------|
| **KEDA misconfiguration** | ScaledObject in error state | Fix trigger config, verify Prometheus connectivity |
| **Slow image pull** | Long ContainerCreating phase | Use image pull policy `IfNotPresent`, pre-pull images |
| **Slow container startup** | Long time from Running to Ready | Optimize init logic, reduce config size |
| **Insufficient resources** | Pod stuck in Pending | Add node capacity or reduce requests |
| **Readiness probe too strict** | Pod flapping Ready status | Tune probe `initialDelaySeconds`, `periodSeconds` |

### Resolution Steps

1. **Keep minimum replicas to avoid cold start**:
   ```bash
   kubectl patch scaledobject flow-worker-<GROUP> -n <NAMESPACE> \
     --type=merge -p '{"spec":{"minReplicaCount":1}}'
   ```

2. **Optimize image pull**:
   ```yaml
   # In deployment spec
   spec:
     containers:
     - name: worker
       imagePullPolicy: IfNotPresent  # Avoid Always
   ```
   
   ```bash
   # Pre-pull images on all nodes (DaemonSet approach)
   kubectl apply -f - <<EOF
   apiVersion: apps/v1
   kind: DaemonSet
   metadata:
     name: image-prepull
     namespace: <NAMESPACE>
   spec:
     selector:
       matchLabels:
         app: image-prepull
     template:
       metadata:
         labels:
           app: image-prepull
       spec:
         initContainers:
         - name: prepull
           image: <WORKER_IMAGE>
           command: ["echo", "Image pulled"]
         containers:
         - name: pause
           image: gcr.io/google_containers/pause:3.2
   EOF
   ```

3. **Tune readiness probe**:
   ```yaml
   readinessProbe:
     httpGet:
       path: /healthz
       port: 8081
     initialDelaySeconds: 5    # Give app time to start
     periodSeconds: 5
     failureThreshold: 3
   ```

4. **Increase gateway queue timeout**:
   - Configure `cold_start_timeout` in gateway config
   - This allows more time for pods to become ready

5. **Optimize worker startup**:
   - Lazy-load JDMs instead of preloading all
   - Cache compiled flows
   - Reduce config payload size

---

## Useful Commands

### kubectl Commands

```bash
# Get all flow worker pods across namespaces
kubectl get pods -A -l app=flow-worker

# Get detailed pod information
kubectl describe pod <POD_NAME> -n <NAMESPACE>

# Stream logs
kubectl logs -f <POD_NAME> -n <NAMESPACE>

# Get logs from all pods in a group
kubectl logs -l app=flow-worker,group=<GROUP> -n <NAMESPACE> --all-containers

# Execute command in pod
kubectl exec -it <POD_NAME> -n <NAMESPACE> -- /bin/sh

# Port forward to access metrics locally
kubectl port-forward svc/engine-gateway 8080:8080 -n <NAMESPACE>

# Force delete stuck pod
kubectl delete pod <POD_NAME> -n <NAMESPACE> --grace-period=0 --force

# Check resource usage
kubectl top pods -l app=flow-worker -n <NAMESPACE>
```

### curl Commands

```bash
# Gateway health check
curl http://localhost:8080/healthz

# Gateway metrics
curl http://localhost:8080/metrics

# Worker health check (from within cluster)
curl http://flow-worker-<GROUP>:8081/healthz

# Worker version endpoint
curl http://flow-worker-<GROUP>:8081/version

# Worker metrics
curl http://flow-worker-<GROUP>:8081/metrics
```

### Prometheus Queries

```promql
# Request rate by group
sum by (group) (rate(gateway_dispatch_total[5m]))

# Error rate by group
sum by (group) (rate(gateway_dispatch_total{status="error"}[5m]))
/ sum by (group) (rate(gateway_dispatch_total[5m]))

# p99 dispatch latency by group
histogram_quantile(0.99, sum by (group, le) (rate(gateway_dispatch_duration_seconds_bucket[5m])))

# p99 cold start latency
histogram_quantile(0.99, sum by (group, le) (rate(gateway_cold_start_duration_seconds_bucket[5m])))

# Worker replica count
gateway_worker_replicas

# Workers not ready
gateway_worker_ready == 0

# Circuit breaker open
gateway_circuit_breaker_state == 2

# Config reload failures
rate(worker_reload_total{status="failure"}[5m])

# Queue timeout rate
rate(gateway_queue_timeout_total[5m])
```

---

## Alert Response

Map of alert rules to runbook sections for quick navigation:

| Alert | Severity | Runbook Section |
|-------|----------|-----------------|
| `WorkerGroupDown` | Critical | [Worker Not Starting](#worker-not-starting), [Worker Crash Loop](#worker-crash-loop) |
| `WorkerHighErrorRate` | Warning | [Worker Crash Loop](#worker-crash-loop), [Circuit Breaker Open](#circuit-breaker-open) |
| `WorkerColdStartSlow` | Warning | [Scale-to-Zero Issues](#scale-to-zero-issues) |
| `GatewayCircuitOpen` | Critical | [Circuit Breaker Open](#circuit-breaker-open) |
| `GatewayQueueBacklog` | Warning | [Scale-to-Zero Issues](#scale-to-zero-issues), [High Latency](#high-latency) |
| `GatewayHighLatency` | Warning | [High Latency](#high-latency) |

### Alert Response Workflow

1. **Acknowledge the alert** in your alerting system
2. **Navigate to the relevant runbook section** using the table above
3. **Follow diagnostic steps** to identify root cause
4. **Apply resolution steps** appropriate to the cause
5. **Verify recovery** using the metrics and commands provided
6. **Document findings** for post-incident review

---

## Escalation

If the issue cannot be resolved using this runbook:

1. Collect diagnostic information:
   - Pod descriptions and events
   - Container logs (current and previous)
   - Relevant metrics screenshots
   - Timeline of events

2. Escalate to the platform team with:
   - Summary of symptoms
   - Steps already attempted
   - Diagnostic data collected

3. For critical production issues:
   - Consider rolling back recent deployments
   - Scale up alternative worker groups if available
   - Engage on-call engineering support
