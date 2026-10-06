# TASK-007: Metrics Dashboard

## Summary
Create a Grafana dashboard JSON for the Prometheus metrics the engine already exports, plus documentation for setting up monitoring.

## Context
- Engine exports Prometheus metrics at `/metrics`
- Metrics include: request count, latency histograms, error rates, breaker state
- No dashboard exists to visualize them
- Operators need to set up their own Grafana

## Requirements

### 1. Grafana Dashboard JSON
Pre-built dashboard with panels for:

#### Overview Row
- Total requests (counter)
- Error rate (%)
- p50/p95/p99 latency
- Active flows count

#### Request Metrics Row
- Requests per second (by method, path, status)
- Latency histogram (by endpoint)
- Error breakdown (4xx vs 5xx)

#### Flow Execution Row
- Flow executions per second
- Flow latency by flow_id
- Node execution counts by type
- Flow errors by flow_id

#### Connection Health Row
- Circuit breaker states (closed/half-open/open)
- Connection pool utilization
- Connection errors by type
- Query latency by connection

#### System Row
- Go runtime metrics (goroutines, heap, GC)
- Process CPU/memory
- Open file descriptors

### 2. Alert Rules (optional)
Prometheus alerting rules for:
- Error rate > 5% for 5 minutes
- p99 latency > 1s for 5 minutes
- Circuit breaker open
- Memory > 80% of limit

### 3. Documentation
- `docs/MONITORING.md` — setup guide
- How to run Prometheus + Grafana
- How to import the dashboard
- Metric descriptions and what to watch

### 4. Docker Compose Extension
Optional `docker-compose.monitoring.yml` that adds:
- Prometheus (scrapes engine /metrics)
- Grafana (with dashboard pre-loaded)

## Files to Create

### Monitoring Config
- `monitoring/grafana/dashboards/engine.json` — main dashboard
- `monitoring/grafana/provisioning/dashboards/default.yaml` — auto-load config
- `monitoring/grafana/provisioning/datasources/prometheus.yaml` — datasource config
- `monitoring/prometheus/prometheus.yml` — scrape config
- `monitoring/alertmanager/alertmanager.yml` — alert routing (optional)

### Documentation
- `docs/MONITORING.md` — setup guide

### Docker Compose
- `docker-compose.monitoring.yml` — Prometheus + Grafana services

## Acceptance Criteria
- [ ] Grafana dashboard JSON imports successfully
- [ ] All panels show data when engine is running
- [ ] Dashboard covers request, flow, connection, and system metrics
- [ ] Documentation explains setup clearly
- [ ] docker-compose.monitoring.yml works standalone

## Dashboard Panels (detailed)

### Panel 1: Request Rate
```
sum(rate(nzr_http_requests_total[5m])) by (method, path, status)
```

### Panel 2: Latency Percentiles
```
histogram_quantile(0.50, rate(nzr_http_request_duration_seconds_bucket[5m]))
histogram_quantile(0.95, rate(nzr_http_request_duration_seconds_bucket[5m]))
histogram_quantile(0.99, rate(nzr_http_request_duration_seconds_bucket[5m]))
```

### Panel 3: Error Rate
```
sum(rate(nzr_http_requests_total{status=~"5.."}[5m])) / sum(rate(nzr_http_requests_total[5m]))
```

### Panel 4: Circuit Breaker State
```
nzr_breaker_state
```
(0 = closed, 1 = half-open, 2 = open)

### Panel 5: Flow Execution
```
sum(rate(nzr_flow_executions_total[5m])) by (flow_id)
```

## Testing
- Manual: import dashboard, verify all panels render
- Manual: generate load, verify metrics update
- Manual: trip breaker, verify panel shows open state
