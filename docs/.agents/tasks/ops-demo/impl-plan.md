# Implementation Plan — Config-Driven Dynamic Routing + Ops Endpoints + FMC Demo Flows

Branch: `feat/ops-and-demo` · Worktree: `/home/nuzirwan/project/rule-engine-api/.worktrees/ops-demo`
All paths below are relative to that worktree. Build with `CGO_ENABLED=1` (ZEN links a C library).

## Design decisions (made during exploration, grounded in the code)

1. **`ActiveRoutes` enumerates routes from config, keyed by the registered ServeMux pattern.**
   The `flows` table already stores `method` + `path` where `path` is the Go 1.22 ServeMux pattern
   (`/orders/{id}`), and `ActiveFlow(ctx, env, method, path)` resolves by that exact pair. So the
   generic handler can register `method + " " + pattern` on the mux, read the matched pattern back,
   and call `ActiveFlow` with it — the pattern is the stable key the brief requires. `RouteInfo`
   carries `{FlowID, Method, Path}`; `ActiveRoutes` returns one entry per ACTIVE flow route.

2. **The generic handler lifts every `{name}` wildcard via `r.PathValue`.** The pattern is parsed
   once at registration for its wildcard names; at request time the handler calls `r.PathValue(name)`
   for each and puts it in `Ctx.Input[name]` (applying the existing `coercePathParam` so a numeric id
   still binds to an int SQL param). This replaces the hardcoded `id` lift in `ordersHandler`.

3. **Startup-from-config is the required bar; live-reload is deferred.** `NewHandler` queries
   `ActiveRoutes` once and builds the mux. The brief marks live-reload a nice-to-have "do not
   over-engineer" — the Valkey invalidation path already exists for flow *bodies*; rebuilding the
   whole mux on pointer moves is out of scope for this change. Routes are read at startup only.

4. **Ops routes stay code-registered on the same mux** (control/ops plane, not config-defined
   business APIs), per the brief. `/admin/*` — none exist in code today, so nothing to preserve
   there; only the dynamic business routes + the three ops routes are mounted.

5. **FMC flows use `decision` + `set` nodes (not `condition`).** The brief says `action` is set
   "only via the decision->Set node". The `decision` handler stores the whole ZEN output map under
   `saveAs` in `Ctx.Data`; a following `set` node mounts `dec.action` into the Response. Each flow
   is: Trigger -> Action(pg query, saveAs=row) -> Set(copy the six row fields into Response) ->
   Decision(ZEN over payment_status, saveAs=dec) -> Set(action from dec.action) -> Response. This
   keeps `action` sourced only from the decision output, and the row fields echoed from the DB read.

6. **`fmc-pg` is a separate data source from the config store.** Its DSN points at database
   `fmc_utility` (default `postgres://root:root@127.0.0.1:5432/fmc_utility?sslmode=disable`, overridable
   by env `FMC_PG_DSN`); the SQL is schema-qualified `fmc_order.order_status`. The config store itself
   lives in schema `rule_engine` (matcha), unrelated to this connection.

7. **Ops endpoints mount on the SAME mux** as the brief states, overriding the slice-e LLD sketch
   that proposed a separate ops listener. `Store.Ping` and `connect.Registry.HealthCheck` already
   exist — no seam additions needed for readyz.

## Plan

- [ ] 1. Add `RouteInfo` type and `ActiveRoutes(ctx, env) ([]RouteInfo, error)` to the frozen
      `config.Store` interface, and implement it on BOTH stores. memStore iterates its
      `active[env]` map, resolving each `activeRef` to its `FlowVersion` for `{FlowID, Method, Path}`.
      PgStore joins `active_pointers` (object_type='flow') -> `flows` to list `{object_id as flowID,
      method, path}` for every active flow, parameterized, ordered by path for determinism.
      Files: `internal/config/store.go` (interface + memStore impl + `RouteInfo` type),
      `internal/config/pgstore.go` (PgStore impl).
      Verify: `CGO_ENABLED=1 go build ./internal/config` exits 0; the compile-time `var _ Store`
      assertions for both stores still hold.

- [ ] 2. Add a unit test for `ActiveRoutes` on memStore: load `testdata/seed.json`, assert the active
      route set contains the seeded routes with correct `{FlowID, Method, Path}` and that an inactive
      flow version is NOT listed.
      Files: `internal/config/store_test.go`.
      Verify: `CGO_ENABLED=1 go test ./internal/config` passes.

- [ ] 3. Rewrite `internal/httpapi.NewHandler` as a config-driven dynamic router. Extend the local
      `Store` interface with `ActiveRoutes`. `NewHandler` calls `ActiveRoutes(defaultEnv)`, and for
      each `RouteInfo` registers `method + " " + path` on the mux with ONE generic handler closure
      that closes over `(store, interp, deps, pattern, wildcardNames)`. The generic handler:
      resolves+pins via `store.ActiveFlow(ctx, defaultEnv, method, pattern)`, builds `Ctx.Input` by
      lifting each `{name}` wildcard via `r.PathValue(name)` (through `coercePathParam`), runs the
      interpreter, encodes `c.Response`. Delete `ordersHandler` and the hardcoded
      `mux.HandleFunc("GET /orders/{id}", ...)`. Add a helper `parseWildcards(pattern string) []string`.
      `NewHandler` returning an error on an `ActiveRoutes` failure requires a signature change — return
      `(http.Handler, error)`; update `NewServer` and both test call sites accordingly.
      Files: `internal/httpapi/server.go`.
      Verify: `CGO_ENABLED=1 go build ./internal/httpapi` exits 0.

- [ ] 4. Add the ops endpoints and MOUNT them on the same mux. New `internal/httpapi/ops.go`:
      `GET /livez` always 200; `GET /readyz` returns 200 when `connect.Registry.HealthCheck(ctx)` AND
      `config.Store.Ping(ctx)` both succeed, else 503 with `{"notReady":"<dep>"}`; `GET /metrics`
      serves `promhttp.HandlerFor(reg, ...)` over the injected `prometheus.Registerer`/`Gatherer`.
      Thread the registry, a pingable store, and a `prometheus.Gatherer` into `NewHandler`/`NewServer`
      via a small `Ops` struct field on `Deps` (or new params) with minimal churn; `cmd/engine` passes
      the real registry, store, and a `prometheus.Registry`. Bound the readyz checks with a short
      context timeout.
      Files: `internal/httpapi/ops.go` (new), `internal/httpapi/server.go` (mount + wiring),
      `cmd/engine/main.go` (pass real deps + build a `prometheus.Registry`/`observ.NewMetrics`).
      Verify: `CGO_ENABLED=1 go build ./...` exits 0.

- [ ] 5. Unit-test the ops endpoints and the dynamic router. ops: `/livez`=200; `/readyz`=200 with
      healthy fakes and 503 when a fake registry or store Ping is forced unhealthy (assert the named
      dep in the body); `/metrics`=200 containing an expected metric name (e.g. `nzr_flow_requests_total`
      after registering `observ.NewMetrics`). Router: update `TestHandlerRouting`/`TestHandlerConfigError`
      for the new `(handler, err)` signature; add a test proving a MULTI-wildcard pattern lifts each
      `{name}` into `Ctx.Input` (fake store returns a route with path `/order/msisdn/{msisdn}`), and a
      test proving a path with NO active flow row yields 404.
      Files: `internal/httpapi/server_test.go`, `internal/httpapi/ops_test.go` (new).
      Verify: `CGO_ENABLED=1 go test ./internal/httpapi` passes; `CGO_ENABLED=1 go vet ./...` clean;
      `CGO_ENABLED=0 go build ./internal/decision` exits 0.

- [ ] 6. Add the two demo flows + shared JDM + `fmc-pg` connection to the seed. Flow A: `GET
      /order/{order_id}`; Flow B: `GET /order/msisdn/{msisdn}`. Both: Trigger(params) ->
      Action(fmc-pg query, saveAs=row) -> Set(copy order_id/msisdn/order_status/payment_status/
      payment_amount/product_name from row into Response) -> Decision(jdmId=fmc-payment, input
      [row.payment_status], saveAs=dec) -> Set(targetPath=action, from=dec.action) -> Response.
      Flow A SQL: `SELECT order_id, msisdn, order_status, payment_status, payment_amount, product_name
      FROM fmc_order.order_status WHERE order_id = {{input.order_id}}`. Flow B SQL: same SELECT
      `WHERE msisdn = {{input.msisdn}} ORDER BY last_updated DESC LIMIT 1`. Shared JDM `fmc-payment`:
      decisionTable, input field `payment_status`, output field `action`, rules: `== 'PAID'` =>
      `'proceed_fulfillment'`, else => `'await_payment'`. Connection `fmc-pg` type postgres, settings
      dsn = the FMC default. Keep or drop `orders-expedite` (either is fine). Both flows `active:true`.
      Validate the trees satisfy `flow.ValidateTree` structural rules (single terminal Response per
      path, decision/set are leaves-with-single-child-chain, etc.).
      Files: `internal/config/testdata/seed.json`.
      Verify: `CGO_ENABLED=1 go test ./internal/config` passes (seed loads; `ActiveRoutes` lists both
      new routes). A quick `ValidateTree` assertion test may be added here.

- [ ] 7. Add a build-tagged integration test proving config-driven routing end-to-end on EPHEMERAL
      containers only. Stand up ephemeral postgres:16 (the FMC data DB), create `fmc_order.order_status`
      with columns (order_id text, msisdn text, order_status text, payment_status text, payment_amount
      numeric, product_name text, last_updated timestamptz) and rows: one PAID, one PENDING, and one
      msisdn with two rows (different last_updated). Seed the config store (memStore via SeedFromBytes
      is sufficient for the router proof; or PgStore to also cover config-store mode), override the
      `fmc-pg` DSN to the ephemeral DB, build the REAL registry + decision engine + dynamic handler.
      Assert: `GET /order/{PAID id}` => action `proceed_fulfillment`; `GET /order/{PENDING id}` =>
      `await_payment`; `GET /order/msisdn/{msisdn}` returns the latest row; `/livez`=200; `/readyz`=200.
      CRUCIAL config-driven test: add a NEW flow row with a NEW path to the store, rebuild the handler
      via `NewHandler`, and show the new route is served with ZERO Go route code; and that a path with
      no flow row returns 404.
      Files: `internal/httpapi/fmc_integration_test.go` (new, `//go:build integration && cgo`), reusing
      the `startPostgres` harness pattern already in `internal/httpapi/integration_test.go`.
      Verify: `CGO_ENABLED=1 go test -tags 'integration cgo' ./internal/httpapi` passes (Docker up).

- [ ] 8. Write the live-run doc `docs/examples-fmc-order.md`: env setup (CONFIG_DSN -> matcha
      rule_engine schema, FMC_PG_DSN -> fmc_utility, VALKEY_ADDR), start the engine, and curl examples:
      `GET /order/ORD-TEST-TRK` => proceed_fulfillment, `GET /order/ORD-TEST-BAD` => await_payment,
      `GET /order/msisdn/628111015450`. Emphasize the endpoints exist because of CONFIG rows, not code.
      Files: `docs/examples-fmc-order.md` (new).
      Verify: doc renders; commands match the seed's routes and the generic router's behavior.

## Final verification (run all before claiming done)

- `CGO_ENABLED=1 go build ./...` — exit 0
- `CGO_ENABLED=1 go vet ./...` — clean
- `CGO_ENABLED=1 go test ./...` — all pass
- `CGO_ENABLED=0 go build ./internal/decision` — exit 0 (ZEN stub compiles)
- `CGO_ENABLED=1 go test -tags 'integration cgo' ./internal/httpapi` — ops + routing + FMC integration pass (Docker up, ephemeral only)
