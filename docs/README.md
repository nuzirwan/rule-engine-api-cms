# Documentation

## Project State

- [STATE.md](./STATE.md) — Current project status and handoff notes

## Architecture

Design documents for the rule engine system.

- [hld.md](./architecture/hld.md) — High-Level Design
- [lld.md](./architecture/lld.md) — Low-Level Design (overview)
- [lld-contracts.md](./architecture/lld-contracts.md) — Shared contracts between slices
- [lld/](./architecture/lld/) — Detailed slice designs:
  - [slice-a-interpreter.md](./architecture/lld/slice-a-interpreter.md) — Flow interpreter
  - [slice-b-connections.md](./architecture/lld/slice-b-connections.md) — Connection registry
  - [slice-c-zen-auth.md](./architecture/lld/slice-c-zen-auth.md) — ZEN integration & auth
  - [slice-d-configstore.md](./architecture/lld/slice-d-configstore.md) — Config store
  - [slice-e-observ-ops.md](./architecture/lld/slice-e-observ-ops.md) — Observability & ops
  - [slice-f-admin-api.md](./architecture/lld/slice-f-admin-api.md) — Admin API
  - [slice-g-scheduler.md](./architecture/lld/slice-g-scheduler.md) — Scheduler & cron
  - [slice-h-webhook.md](./architecture/lld/slice-h-webhook.md) — Webhook providers
  - [slice-i-gateway.md](./architecture/lld/slice-i-gateway.md) — Gateway & dispatcher
  - [slice-j-worker.md](./architecture/lld/slice-j-worker.md) — Background workers
  - [connector-platform.md](./architecture/lld/connector-platform.md) — Connector platform
- [phase0-spike-report.md](./architecture/phase0-spike-report.md) — Phase 0 de-risking spike results

## Operations

Deployment, monitoring, and operational guides.

- [DEPLOYMENT.md](./operations/DEPLOYMENT.md) — Deployment guide
- [DOCKER.md](./operations/DOCKER.md) — Docker setup
- [MONITORING.md](./operations/MONITORING.md) — Monitoring configuration
- [SECRETS.md](./operations/SECRETS.md) — Secrets management
- [runbook-dynamic-workers.md](./operations/runbook-dynamic-workers.md) — Dynamic workers runbook

## Examples

Usage examples and sample configurations.

- [examples-fmc-order.md](./examples/examples-fmc-order.md) — FMC order flow example
- [flow-nodes.md](./examples/flow-nodes.md) — Complete node types reference (trigger, condition, switch, sequence, parallel, forEach, set, decision, filter, find, map, reduce, action, logger, response)
- [connectors.md](./examples/connectors.md) — Connection configuration guide (Postgres, MySQL, Valkey, REST/HTTP, Kafka)
- [decision-tables.md](./examples/decision-tables.md) — ZEN JDM decision tables for conditions and computed values
- [resilience.md](./examples/resilience.md) — Resilience patterns (circuit breaker, retry, timeout, rate limiting, idempotency, saga)
- [webhooks.md](./examples/webhooks.md) — Webhook provider configuration (Stripe, GitHub, generic)
- [scheduled-tasks.md](./examples/scheduled-tasks.md) — Cron scheduling and scheduled task configuration
- [authentication.md](./examples/authentication.md) — AuthN (JWT/JWKS) and AuthZ (ZEN-based) configuration
- [dry-run-validation.md](./examples/dry-run-validation.md) — Testing flows without side effects and validation
