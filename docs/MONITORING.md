# Monitoring — NZR Rules Engine

This guide covers how to run Prometheus + Grafana alongside the engine, what metrics are available, and how to interpret the pre-built dashboard.

---

## Overview

The engine exports Prometheus metrics at `GET /metrics`. Eight application-level metrics cover:

- Flow request rate and latency
- Flow error classification
- Connection call rate and latency
- Circuit breaker state
- In-flight request saturation
- Config hot-reload outcomes

The monitoring stack (Prometheus + Grafana) is supplied as a Docker Compose overlay that can be started independently of the main application stack.

---

## Prerequisites

- Docker and Docker Compose v2
- Main application stack running: `docker compose up -d`
  - This creates the `rule-engine-api_nzr-internal` Docker bridge network that Prometheus needs to reach the engine container.

---

## Quick Start

```bash
# From the repository root
docker compose -f docker-compose.yml -f docker-compose.monitoring.yml up -d
```

Grafana is now available at **http://localhost:3000**.  
Prometheus is available at **http://localhost:9090**.

The dashboard loads automatically via Grafana provisioning — no manual import needed.

---

## Accessing the Dashboard

1. Open **http://localhost:3000** in your browser.
2. Log in with `admin` / `admin` (or the password you set via `GRAFANA_ADMIN_PASSWORD`).
3. Navigate to **Dashboards → Browse → NZR Rules Engine → NZR Rules Engine**.

The dashboard auto-refreshes every 30 seconds and defaults to a 1-hour time window.

---

## Accessing Prometheus

Open **http://localhost:9090** to query metrics directly, check scrape target health, and inspect alert rules.

To verify the engine is being scraped successfully: go to **Status → Targets** and confirm the `nzr-engine` job shows `UP`.

---

## Metric Reference

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `nzr_flow_requests_total` | CounterVec | `flow_id`, `env`, `method`, `status` (`ok`\|`error`) | Total flow requests. Use this for request rate and error rate. |
| `nzr_flow_request_duration_seconds` | HistogramVec | `flow_id`, `env`, `method` | Flow request latency. Buckets: 1ms → 2s. |
| `nzr_flow_errors_total` | CounterVec | `flow_id`, `env`, `error_class` | Flow errors by class: `Timeout`, `NotFound`, `Validation`, `Upstream`, `Internal`. |
| `nzr_connection_calls_total` | CounterVec | `conn_key`, `type`, `status` (`ok`\|`error`) | Outbound connection calls. |
| `nzr_connection_call_duration_seconds` | HistogramVec | `conn_key`, `type` | Outbound connection latency. Buckets: 1ms → 2s. |
| `nzr_breaker_state` | GaugeVec | `conn_key` | Circuit breaker state: `0`=Closed, `1`=Half-Open, `2`=Open. |
| `nzr_inflight_requests` | Gauge | — | Number of flow requests currently in progress. |
| `nzr_config_reload_total` | CounterVec | `env`, `result` (`ok`\|`error`) | Config hot-reload attempts and outcomes. |

### What to watch

- **Request rate drops to zero** — engine may be down or not receiving traffic.
- **Error rate > 5%** — check `nzr_flow_errors_total` by `error_class` to identify the source.
- **p99 latency > 1s** — investigate slow connections via `nzr_connection_call_duration_seconds`.
- **`nzr_breaker_state == 2`** — a connection is in open/tripped state; the circuit breaker is blocking calls to protect the system.
- **`nzr_inflight_requests` growing without bound** — potential goroutine or connection leak.

---

## Alert Rules

Four Prometheus alert rules are pre-configured in `monitoring/prometheus/rules/alerts.yml`:

| Alert | Condition | Severity | Fire After |
|-------|-----------|----------|------------|
| `HighErrorRate` | Flow error rate > 5% | warning | 5 minutes |
| `HighP99Latency` | Flow p99 latency > 1s | warning | 5 minutes |
| `CircuitBreakerOpen` | Any `nzr_breaker_state == 2` | critical | Immediately |
| `HighMemoryUsage` | `process_resident_memory_bytes > 800MB` | warning | 5 minutes |

**Note on memory alert:** The default threshold is 800MB. Adjust this value in `monitoring/prometheus/rules/alerts.yml` to match 80% of your container's memory limit. For example, if your engine container has a 2GB limit, set the threshold to `1600000000` (1.6GB).

Alerts are visible in the Prometheus UI at **http://localhost:9090/alerts**. To integrate with PagerDuty, Slack, or other receivers, add an Alertmanager deployment and configure routes in `monitoring/alertmanager/alertmanager.yml` (not included in this release — Prometheus will log a warning about the missing alertmanager but continue scraping normally).

---

## Go Runtime Metrics (System Row)

The **System (Go Runtime)** row in the dashboard shows stub panels that will render "No Data" by default. This is because the engine uses a private Prometheus registry (`prometheus.NewRegistry()`) without registering Go runtime or process collectors.

To enable these panels, add two lines to `engine/cmd/engine/main.go`:

```go
import "github.com/prometheus/client_golang/prometheus/collectors"

// In the registry setup section, after creating the registry:
reg.MustRegister(collectors.NewGoCollector())
reg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
```

After that change, the Goroutines, Heap Allocated, GC Pause, CPU, and Open FDs panels will populate automatically.

---

## Production Notes

### Change the Grafana admin password

Set `GRAFANA_ADMIN_PASSWORD` in your environment or `.env.docker` before starting the stack:

```bash
export GRAFANA_ADMIN_PASSWORD=your-secure-password
docker compose -f docker-compose.yml -f docker-compose.monitoring.yml up -d
```

Never rely on the default `admin` password in production.

### Change Prometheus data retention

The default retention is 15 days. To change it, update the `--storage.tsdb.retention.time` flag in `docker-compose.monitoring.yml`:

```yaml
command:
  - --storage.tsdb.retention.time=30d   # or 7d, 90d, etc.
```

### Custom Docker Compose project name

Docker Compose prefixes the project directory name to network names. If your main stack is started with a custom project name (e.g., `docker compose -p myproject`), update the `name:` field under `networks.nzr-internal` in `docker-compose.monitoring.yml`:

```yaml
networks:
  nzr-internal:
    external: true
    name: myproject_nzr-internal   # match your project name
```

### Securing the /metrics endpoint

The engine's `/metrics` endpoint is unauthenticated. In internet-facing deployments, restrict access using:
- A reverse proxy (nginx, Caddy) with IP allowlisting for the Prometheus scraper
- Network-level controls (security groups, firewall rules)
- mTLS between Prometheus and the engine (requires scrape config changes)

---

## Worker Metrics

Workers export their own metrics at `/metrics`, separate from the engine. The `monitoring/prometheus/prometheus.yml` includes a commented `nzr-workers` scrape job.

### Docker Compose (local dev)

Add worker targets manually to the `nzr-workers` job:

```yaml
- job_name: nzr-workers
  static_configs:
    - targets:
        - nzr-worker-1:8080
        - nzr-worker-2:8080
```

### Kubernetes

Replace `static_configs` with Kubernetes service discovery:

```yaml
- job_name: nzr-workers
  kubernetes_sd_configs:
    - role: pod
  relabel_configs:
    - source_labels: [__meta_kubernetes_pod_label_app]
      action: keep
      regex: nzr-worker
    - source_labels: [__meta_kubernetes_pod_container_port_number]
      action: keep
      regex: "8080"
```

### Worker Metrics Available

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `worker_requests_total` | CounterVec | `group`, `flow_id`, `status` | Total requests processed by worker |
| `worker_request_duration_seconds` | HistogramVec | `group`, `flow_id` | Worker request latency |
| `worker_config_version` | GaugeVec | `group` | Current config version loaded |
| `worker_flows_loaded` | GaugeVec | `group` | Number of flows loaded |
| `worker_jdms_loaded` | GaugeVec | `group` | Number of JDMs loaded |
| `worker_connections_active` | GaugeVec | `group`, `connection` | Active connections per worker |
| `worker_reload_total` | CounterVec | `group`, `status` | Config reload attempts |

Worker metrics are visualized in a separate dashboard (`flow-workers.json`) in the `deploy/grafana/dashboards/` directory.
