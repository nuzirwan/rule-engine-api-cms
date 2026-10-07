# Slice G — Provider-Agnostic Connector Platform (lifecycle, scale, cost)

Status: DESIGN (not built). Owner seam: `engine/internal/connect` (+ a small `engine/internal/flow`
trigger addition for async providers). Prerequisite reading: `docs/lld-contracts.md`,
`docs/lld/slice-b-connect.md` (if present), and the existing `connect.Connector` / `connect.Registry`
code. Engine Go module only; the CMS is unaffected.

Cited standards (source of truth): `[[config-driven-boundaries]]`, `[[simplicity-and-design]]`,
`[[performance-defaults]]`, `[[cost-awareness]]`, `[[caching-strategy]]`, `[[graceful-shutdown]]`,
`[[health-checks-liveness-readiness]]`, `[[retry-and-backoff]]`, `[[error-classification]]`,
`[[at-least-once-processing]]`, `[[versioning-and-compatibility]]`, `[[dependency-management]]`.

---

## 0. Why this exists (the real requirement)

We are building a **provider-agnostic platform**: the engine must absorb ANY backend — any SQL
database, any REST/HTTP service, Valkey/Redis, and future **message brokers (Kafka, RabbitMQ)**,
streaming/gRPC, or protocols not yet imagined — **without special-casing the core**. Adding a
provider must be "implement one interface + register it," never "edit the registry/interpreter."

The current `connect` layer is already close to this (dispatch is by `byType`; `drivers/registry.go`
documents "a new file + one entry in `All`"). But it bakes in three assumptions that break the
agnostic promise at company scale and for new interface types:

1. **Eager load-all at startup** — `registry.New` opens one pool/client per active connection def up
   front. For a demo (3 connections) this is fine; company-wide (dozens–hundreds of connections ×
   HA replicas) it exhausts database connection limits and over-provisions idle resources.
2. **One lifecycle for all** — every connector is assumed to be a cheap, poolable, request/response
   resource that can be "opened at boot." A Kafka/RabbitMQ **consumer** is the opposite: long-lived,
   always-on, with a poll loop and consumer-group/offset state. Opening it "at startup like a pool"
   is the wrong model, and there is no trigger to drive a message-consumed flow.
3. **Startup coupling** — a single dead backend in a def aborts `New` (whole-engine boot failure).

This slice makes connection handling **agnostic by contract**: a stable extension seam where the
core treats "a connector" uniformly, each provider declares HOW it behaves, and lifecycle/scale/cost
are handled generically per declared class.

This is NOT a hack or a bolt-on — it is the conventional maturation every production data-access
layer adopts (lazy pooling, connection proxies, graceful degradation). It is the planned closure of
the already-documented fan-out risk and the per-instance-breaker limitation (R10).

---

## 1. The agnostic contract (the stable seam)

Extend `connect.Connector` so a provider DECLARES its behavior; the core never branches on concrete
type. All four methods are the whole contract a new provider implements.

```go
type Lifecycle int
const (
    LifecyclePooled    Lifecycle = iota // request/response, borrow-per-call: SQL, REST, Valkey
    LifecycleLongLived                   // always-on, managed: Kafka/RabbitMQ consumers, streams
)

// Capability flags let the flow engine adapt to what a provider supports instead
// of assuming request/response. A provider ORs the capabilities it offers.
type Capability uint32
const (
    CapQueryExec  Capability = 1 << iota // query/exec (SQL), request/response (REST)
    CapKeyValue                           // get/set/del (Valkey)
    CapPublish                            // produce a message/event (Kafka/Rabbit publish)
    CapSubscribe                          // consume a stream/topic (drives an event trigger)
    CapIdempotentRetryable                // safe to retry a write (informs resilience)
    CapDedupStore                         // can back the idempotency SET-NX lock
)

type Connector interface {
    Type() string            // "postgres" | "mysql" | "rest" | "valkey" | "kafka" | ...
    Lifecycle() Lifecycle    // NEW — how the platform manages it
    Capabilities() Capability// NEW — what the flow engine may ask of it
    Open(ctx, def) (Client, error)
}
```

Backward-compat: existing drivers (pg/rest/valkey) return `LifecyclePooled` and their obvious
capabilities; no behavior change for them. This keeps `[[versioning-and-compatibility]]` — the
`Connector` seam is a frozen contract, extended additively.

**Adding a provider (the agnostic promise, concretely):** write `drivers/<type>.go` implementing the
four methods, add one line to `drivers.All()`. The registry, interpreter, flow schema, admin API,
and config store are untouched. A pooled provider (e.g. MySQL) is "done." A long-lived provider
(Kafka) additionally declares `CapSubscribe`, which the ConsumerManager (§3) picks up.

---

## 2. Lifecycle: lazy pooled provisioning (the fan-out fix)

For `LifecyclePooled` connectors, the registry stops opening at startup and opens **on first use**,
caching the client, and **idle-closes** after a TTL.

- `registry.New` stores defs + the type→Connector map ONLY. It opens nothing. (Boot is instant and
  cannot fail on a bad backend — see §4.)
- `registry.Client(ctx, key)` returns the cached client if open; otherwise opens it under a
  **per-key single-flight lock** (so a burst of concurrent first-requests opens exactly one pool,
  not N), caches, and returns it.
- An **idle reaper** goroutine closes a pooled client whose last-use age exceeds `IdleTTL`
  (per-connection config, default e.g. 5m). The next request reopens lazily.
- `Reload` (zero-restart config change) is unchanged in spirit: a changed def evicts the cached
  client so the next use reopens with new settings.

Net: an instance holds pools ONLY for connections its routes actually touch, and releases them when
traffic stops. This is the single biggest scale win and is uniform across every pooled provider.

---

## 3. Lifecycle: long-lived providers (makes new async interfaces possible)

`LifecycleLongLived` connectors are NOT opened per request. A new `ConsumerManager`:

- starts each `CapSubscribe` connection at boot (or on config publish/Reload), runs its poll/consume
  loop, tracks health, and restarts with backoff on failure (`[[retry-and-backoff]]`);
- on each received message, drives a flow via a NEW **`messageTrigger`** node type (distinct from the
  HTTP `trigger`) — this is the `engine/internal/flow` addition. Delivery semantics follow
  `[[at-least-once-processing]]` (ack after the flow's required writes commit; seek-back on failure).

This is what lets Kafka/RabbitMQ roll in cleanly: the pooled model is left untouched, and async
providers get a lifecycle that fits them. Publish-only use (`CapPublish`, e.g. a flow action that
emits an event) stays in the pooled/on-demand path — only *consuming* needs the manager + trigger.

---

## 4. Graceful startup & degradation (operability)

- Boot never opens backends (lazy), so a dead data source cannot fail startup.
- A lazy-open failure returns a classified `Upstream`/`Timeout` error to that ONE request
  (`[[error-classification]]`), marks the connection unhealthy, and retries with backoff; other
  connections are unaffected.
- `/readyz` gates on the CONFIG store only (already fixed); data-source health is advisory per-connection,
  surfaced but not fatal (`[[health-checks-liveness-readiness]]`). A long-lived consumer that is down
  is reported unhealthy without failing the whole engine.
- Breakers remain per-connection; cross-instance breaker sharing stays a documented limitation (R10).

---

## 5. Performance cost (explicit — the tradeoffs, quantified)

Per `[[performance-defaults]]`: measure, make the common path fast, and be honest about the costs a
change introduces. This design trades a little latency for a large resource reduction.

| Dimension | Eager-load-all (today) | Lazy + lifecycle (this design) |
|---|---|---|
| **Idle resource footprint** | High: ~`Σ(maxConns)` sockets per instance held open regardless of use; × replicas. At 100 conns × 5 × 4 replicas ≈ 2000 DB connections idle. | Low: only in-use connections hold pools; idle reaped. Footprint ∝ actual working set, not catalog size. |
| **First-request latency** | Zero (pool pre-warmed). | One-time open cost on the FIRST request to a cold connection (TCP+TLS+auth+pool init: ~1–50ms depending on provider). Amortized to ~0 thereafter while warm. **This is the main cost.** |
| **Steady-state latency** | Borrow-from-pool (sub-ms). | Identical — borrow-from-pool once warm. No per-request regression. |
| **Boot time** | Slow + fragile (opens everything; a slow/dead backend stalls or fails boot). | Fast + robust (opens nothing). |
| **DB-side memory** | Multi-GB at scale (Postgres ~5–15MB/backend × thousands). | Proportional to live working set; bounded further by a proxy (§6). |
| **Throughput ceiling** | Bounded by pool sizes, same. | Same; plus optional per-instance global budget prevents one instance over-opening. |

Mitigating the one real cost (cold-start latency): keep a small per-connection **`minWarm`** for
hot connections (pre-open `minWarm` pools at boot for a flagged subset), so latency-critical paths
never pay cold-start while the long tail stays lazy. This is a per-connection config knob, not a
global mode — the common/hot path is fast, the rare path is cheap (`[[performance-defaults]]`).

Caching note: config resolution already uses Valkey (`[[caching-strategy]]`); this slice does not
change that. It only changes when the *data-source* connections themselves are opened.

---

## 6. Deployment concerns (under the contract, not in it)

These are ops-layer, pluggable beneath the agnostic contract — the engine config just points a
connection's DSN at a proxy instead of the DB:

- **Connection proxy** — PgBouncer (Postgres) / ProxySQL (MySQL), transaction-pooling mode. The
  engine's lazy pools multiplex onto a small fixed set of real DB backends. At company scale this is
  effectively mandatory and protects `max_connections` regardless of engine behavior.
- **Per-connection pool sizing** — stop defaulting `maxConns`; size per source in the connection def
  (`pool.maxConns/minConns/minWarm/idleTTL`). A read-mostly dashboard conn ≠ a hot write path.
- **Per-instance global budget** — a registry cap on total open pools/sockets so no single instance
  can over-open across all providers.
- **Sharding by domain (optional)** — partition connections across engine deployments so an instance
  loads only what its routes use. Trades the "any instance serves any route" simplicity; adopt only
  if the proxy + lazy loading prove insufficient.

---

## 7. Business matters (why this is worth it)

Beyond raw performance, the agnostic connector platform is justified in business terms:

- **Time-to-add-a-provider (velocity / cost of change).** Agnostic-by-contract means a new backend
  type is a bounded, low-risk unit of work (one driver file + registration + tests) instead of a
  core refactor. This is the platform's core value proposition — new integrations without
  re-engineering, mirroring the config-driven "new API without code" promise
  (`[[config-driven-boundaries]]`). Estimate: a new pooled provider ≈ days, not weeks; measured
  against the alternative of per-integration bespoke services.
- **Infrastructure cost (`[[cost-awareness]]`).** Eager-load-all forces over-provisioned database
  connection limits (bigger DB instances, higher licensing/managed-service tiers sized for peak
  idle connections). Lazy + proxy right-sizes this — direct $ savings on database tiers and fewer
  "too many connections" incidents.
- **Reliability / SLA (`[[sli-slo-sla]]`, `[[health-checks-liveness-readiness]]`).** Graceful startup
  means one team's broken backend does not take down the shared platform at boot — critical when
  many teams' connections live in one engine. Isolated per-connection health protects the blast
  radius and the platform's availability SLO.
- **Operability (`[[graceful-shutdown]]`, `[[observability-and-logging]]`).** Per-connection
  lifecycle + health + metrics give ops clear signals (which connection is cold/hot/unhealthy) and
  clean drain on shutdown. Lower operational toil at scale.
- **Risk & governance.** Secrets stay `secretRef`-only through the existing `SecretProvider`
  (`[[config-and-secrets]]`) regardless of provider — adding Kafka does not open a new secret path.
  Capability declaration makes it auditable what each provider is allowed to do.
- **Future-proofing.** The lifecycle + capability split means the messaging/streaming roadmap
  (event-driven flows) is an additive provider + the ConsumerManager, not a re-architecture — the
  platform's optionality has quantifiable strategic value.

---

## 8. Acceptance criteria

- **AC-G1 (agnostic add):** a brand-new pooled provider (prove with MySQL) is added by one driver
  file + one `All()` entry; registry/interpreter/flow-schema/admin/config untouched; served end-to-end.
- **AC-G2 (lazy open):** `New` opens zero backends; a pool opens on first `Client()` use; concurrent
  first-use opens exactly one pool (single-flight); verified by connection-count assertion.
- **AC-G3 (idle reap):** a pooled client idle beyond `IdleTTL` is closed and transparently reopened
  on next use.
- **AC-G4 (minWarm):** a connection flagged `minWarm>0` is pre-opened at boot and never pays
  cold-start; unflagged ones stay lazy.
- **AC-G5 (graceful startup):** a dead backend def does NOT fail boot; its first use returns a
  classified error; other connections serve normally.
- **AC-G6 (long-lived lifecycle):** a `LifecycleLongLived`+`CapSubscribe` provider is managed by the
  ConsumerManager (started/health-tracked/restarted), NOT opened per request; a received message
  drives a `messageTrigger` flow with at-least-once semantics.
- **AC-G7 (capabilities):** the flow engine rejects asking a provider for a capability it does not
  declare (e.g. subscribe on a SQL provider) with a Validation error, not a panic.
- **AC-G8 (budget):** a per-instance pool/socket budget cap is enforced; exceeding it is a classified
  error, not an unbounded open.
- **AC-G9 (perf):** steady-state (warm) latency shows no regression vs eager-load; cold-start cost is
  measured and documented; idle footprint drops to the working set.
- **AC-G10 (compat):** existing pg/rest/valkey drivers and all current flows pass unchanged
  (the seam extension is additive).

---

## 9. Phasing (recommended build order)

- **Phase 1 — pooled-type scale (do before company-wide rollout; no new interface needed):**
  AC-G2/G3/G4/G5/G8/G9/G10 + the `Lifecycle()`/`Capabilities()` seam (pooled only) + per-connection
  sizing + PgBouncer/ProxySQL deployment (§6). Makes the CURRENT provider set (SQL/REST/Valkey) scale
  company-wide. Prove the agnostic add with MySQL (AC-G1).
- **Phase 2 — async providers (gated on a messaging iface being greenlit):** the ConsumerManager +
  `messageTrigger` + at-least-once wiring (AC-G6/G7). Only when Kafka/RabbitMQ is actually on the
  roadmap.

Each phase is a worktree-isolated increment with the full CGO gate; the `Connector` contract change
lands first (Phase 1) since it is the frozen seam everything else builds on.

---

## 10. Out of scope / documented limitations

- Cross-instance shared circuit breakers (R10) — still per-instance; a shared-breaker design is a
  separate increment.
- Non-atomic cross-source writes (R3) and saga/compensation — unchanged by this slice.
- The connection proxy (PgBouncer/ProxySQL) is a deployment artifact; this slice specifies the
  engine side and the config pointing at it, not the proxy's own provisioning.
