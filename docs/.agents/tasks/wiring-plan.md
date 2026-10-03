# Implementation Plan — Wiring/Integration Join (nzr-rules-engine)

Wire the four already-built v1 packages into the running HTTP server. The packages
(`internal/{config,connect,decision,observ,auth,flow}`) compile and are tested in
isolation; `cmd/engine` + `internal/httpapi` still use the thin-slice wiring. This
is the ONE increment allowed to edit `cmd/engine` and `internal/httpapi`, plus a
single cross-cutting edit in `internal/flow` (dry-run write-suppression).

All work happens in the worktree. Every command runs with
`git -C /home/nuzirwan/project/rule-engine-api/.worktrees/wiring` or from that
directory; every path below is absolute under
`/home/nuzirwan/project/rule-engine-api/.worktrees/wiring/`.

## Design decisions (grounded in the code read during exploration)

- **Observability provider.** `internal/observ` ships BOTH a slog-backed
  `NewTracer(*slog.Logger)`/`NewLogger(*slog.Logger)` (thin slice) AND the real
  OTel path: `NewOTelTracer(tp trace.TracerProvider)`, `NewTracerProvider(exp)`,
  `ShutdownProvider(ctx, tp)`, plus `NewMetrics(reg)`. Wire the OTel tracer over a
  `NewTracerProvider` built from a batch OTLP/stdout exporter, keep the slog
  `NewLogger` for structured logs, and build `NewMetrics` on a fresh
  `prometheus.Registry`. Rationale: the task says "observ OTel provider+Logger+
  metrics"; the real seams already exist, so no observ code changes.
- **config.Store selection.** Build the real `config.PgStore` when a config DSN is
  provided (new `-config-dsn` flag OR `CONFIG_DSN` env), else fall back to the
  in-memory `config.LoadSeed(seedPath)` store (today's behavior). `NewPgStore`
  takes `map[env]*pgxpool.Pool`; the engine serves ONE env (default `""`), so
  build a one-entry map `{env: pool}`. Apply embedded migrations via
  `migrations.Apply(ctx, pool)` at startup before first use. Rationale: STATE.md
  NEXT item 1 ("behind an env flag or config, falling back to the in-memory seed
  for local").
- **Auth middleware toggle.** Mount `auth.NewMiddleware(authn, authz, ra, log)`
  with `.Authn` then `.Authz` in front of the flow route ONLY when JWKS issuer +
  audience + JWKS URL are configured; otherwise skip the chain and log a clear
  startup warning that auth is DISABLED. The real JWKS fetcher is not built
  (`auth.NewVerifier` takes a `KeySource`; only `NewStaticKeySource` exists), so
  wire a small `httpKeySource` that fetches+parses a JWKS URL into
  `map[string]crypto.PublicKey`, OR — when only a static JWKS file/env is given —
  use `NewStaticKeySource`. AuthZ uses `auth.NewAuthorizer(engine, authzJDMID)`
  over the SAME decision engine. The `ResourceActionFunc` maps the matched route
  pattern → resource and HTTP method → verb (GET→read, else write). Rationale:
  slice-c §4-5; deny-by-default is already in the middleware.
- **Admin endpoints share the mux + interpreter.** `/admin/flows/validate` and
  `/admin/flows/dry-run` register on the SAME `http.ServeMux` as the flow route,
  are NOT behind the flow auth chain (control-plane facing; keep them unauthed in
  v1 like the thin slice), and reuse the one `flow.Interpreter`. validate runs
  `flow.ValidateTree(tree, refs)` + each fixture through the interpreter with a
  mocked registry/evaluator (no real I/O); dry-run runs the stored/active flow with
  `observ.WithDryRun(ctx)` set and `observ.WithCollector(ctx, collector)` attached.
  Rationale: slice-d §5/§6b.
- **Dry-run write-suppression (cross-cutting flow edit).** `internal/flow` ALREADY
  imports `internal/observ` (interpreter.go, node.go) and the dry-run shim's
  "observ is off-limits" comment is stale. `observ.IsDryRun`/`observ.WithDryRun`
  exist. Change `flow.actionHandler.Exec` to: if `observ.IsDryRun(ctx)` AND the op
  is a write (`isWriteOp(op.Kind)` — already in dryrun.go), SKIP `client.Execute`,
  record `wrote:"suppressed"` onto the dry-run collector via
  `observ.CollectorFrom(ctx)`, store a suppressed marker under `SaveAs`, and
  continue the walk. Delete the now-redundant package-local `isDryRun`/`withDryRun`
  shim (keep `isWriteOp`), delegating to the observ seam. Rationale: task item (4)
  + lld-contracts dry-run stitch obligation (AC-14).
- **Scope + trace stamping.** The flow handler stamps `observ.WithScope` at the
  edge (trace_id/request_id) and `observ.EnrichScope(ctx, flowID, version, env)`
  the moment the version is pinned, and `decision.WithEnv(ctx, env)` for per-env
  JDM. The interpreter already opens a `flow.run` root span and a per-node span, so
  a trace spanning the whole walk comes for free once the OTel tracer is wired.
- **No change to any built package other than the one flow dry-run edit.** All
  constructors used (`connect.New`, `drivers.All`, `decision.New`, `flow.New`,
  `config.NewPgStore`/`LoadSeed`, `observ.*`, `auth.*`, `migrations.Apply`) already
  exist with the signatures this plan calls.

Build/test commands (from Makefile + STATE.md): `CGO_ENABLED=1 go build ./...`,
`CGO_ENABLED=1 go vet ./...`, `CGO_ENABLED=1 go test ./...`,
`CGO_ENABLED=0 go build ./internal/decision`,
`CGO_ENABLED=1 go test -tags 'integration cgo' ./internal/httpapi` (Docker up).

---

# Implementation Plan

- [ ] 1. Honor dry-run write-suppression in the flow action handler (cross-cutting wiring edit).
      Change `actionHandler.Exec` in `internal/flow/handlers.go`: after resolving the client and
      building `op`, if `observ.IsDryRun(ctx)` and `isWriteOp(op.Kind)` is true, do NOT call
      `client.Execute`; instead record a suppressed step on the dry-run collector
      (`observ.CollectorFrom(ctx)` → `Record(n.ID, "action", "", map[string]any{"wrote":"suppressed"})`),
      store `map[string]any{"wrote":"suppressed"}` under `SaveAs` if set, and `walkChildren` as
      normal. Reads ("query","get","ping") still execute. Replace the package-local
      `isDryRun`/`withDryRun` in `internal/flow/dryrun.go` with delegation to `observ.IsDryRun`
      (keep `isWriteOp`); update the file comment to note the seam has landed. Add `internal/observ`
      to the handlers.go imports.
      Files: `internal/flow/handlers.go`, `internal/flow/dryrun.go`
      Verify: `CGO_ENABLED=1 go build ./internal/flow` exits 0; `CGO_ENABLED=1 go test ./internal/flow` passes.

- [ ] 2. Add the dry-run suppression unit test in the flow package.
      Table-driven test asserting: with `observ.WithDryRun(ctx)` + an attached
      `observ.NewTraceCollector(nil)` via `observ.WithCollector`, a flow whose action node is a
      write op (Kind "http"/"exec") does NOT invoke the fake registry's client Execute and the
      collector records a step with `wrote:"suppressed"`; and WITHOUT dry-run the same flow DOES
      call Execute. Use the existing fake Deps pattern from `internal/flow/interpreter_test.go`
      (fake Registry whose Client returns a fake Client counting Execute calls).
      Files: `internal/flow/dryrun_test.go` (new)
      Verify: `CGO_ENABLED=1 go test ./internal/flow -run DryRun` passes.

- [ ] 3. Add a config helper to open a pgx pool from a DSN and run migrations.
      Add `OpenPool(ctx, dsn string) (*pgxpool.Pool, error)` (parse DSN via `pgxpool.ParseConfig`,
      `pgxpool.NewWithConfig`, `Ping`) and `Migrate(ctx, pool) error` delegating to
      `migrations.Apply(ctx, pool)` in a new `internal/config/open.go`. These keep DSN→pool +
      migration wiring in the config package (where the store lives) rather than in cmd/engine,
      and are reused by the integration test. Classify failures as Validation/Upstream per the
      package's `wrapErr`.
      Files: `internal/config/open.go` (new)
      Verify: `CGO_ENABLED=1 go build ./internal/config` exits 0; `CGO_ENABLED=1 go vet ./internal/config` clean.

- [ ] 4. Build the httpapi auth chain plumbing (toggleable) and the resource/action mapper.
      Add `internal/httpapi/auth.go`: an `AuthConfig` struct (Issuer, Audience, JWKSURL,
      AuthzJDMID, RolesClaim), a `BuildAuthMiddleware(cfg AuthConfig, decide decision.Evaluator,
      log observ.Logger) (*auth.Middleware, bool, error)` that returns `(nil, false, nil)` when the
      config is incomplete (auth disabled) and otherwise builds `auth.NewVerifier` over an
      `httpKeySource` (fetches+parses the JWKS URL to `map[string]crypto.PublicKey`) and
      `auth.NewAuthorizer(decide, cfg.AuthzJDMID)`, wrapping them with `auth.NewMiddleware(authn,
      authz, routeResourceAction, log)`. Add `routeResourceAction(r)` mapping the matched pattern
      to a resource string and HTTP method to a verb (GET→read, else write). Keep the JWKS fetch
      minimal (stdlib net/http GET + parse RSA/EC JWK to crypto.PublicKey); if JWKS parsing proves
      heavy, accept a static JWKS JSON via the config and use `auth.NewStaticKeySource` — pick ONE
      and document it in a comment.
      Files: `internal/httpapi/auth.go` (new)
      Verify: `CGO_ENABLED=1 go build ./internal/httpapi` exits 0; `CGO_ENABLED=1 go vet ./internal/httpapi` clean.

- [ ] 5. Register the auth chain in front of the flow route in the httpapi server.
      Modify `internal/httpapi/server.go`: `NewHandler` / `NewServer` gain an optional
      `*auth.Middleware` (and the "enabled" bool) parameter (extend `Deps` with an
      `Auth *auth.Middleware` field so the signature stays stable, or add a `NewHandlerWithAuth`
      — prefer the Deps field). When auth is enabled, wrap the `GET /orders/{id}` handler with
      `mw.Authn(mw.Authz(flowHandler))`; when disabled, register the bare handler. Stamp
      `observ.WithScope` (trace_id from `observ.TraceIDFromContext` / request id) at the start of
      the flow handler and `observ.EnrichScope(ctx, fv.FlowID, fv.Version, defaultEnv)` right after
      `store.ActiveFlow` pins the version. Keep the existing classified-error→HTTP-status mapping
      and the per-request templating/branching adapters unchanged.
      Files: `internal/httpapi/server.go`
      Verify: `CGO_ENABLED=1 go build ./internal/httpapi` exits 0; `CGO_ENABLED=1 go test ./internal/httpapi` (unit) passes — existing `server_test.go` tests still green (update the `NewHandler(store, interp, Deps{})` call sites if the signature changed).

- [ ] 6. Build the admin endpoints handler (validate + dry-run) on the shared mux.
      Add `internal/httpapi/admin.go` registering `POST /admin/flows/validate` and
      `POST /admin/flows/dry-run` on the SAME ServeMux in `NewHandler`. validate: decode the request
      body `{env, flow:{flowId,method,path,tree,fixtures}}`, run `flow.ValidateTree(tree, refs)`
      (refs backed by the store: `HasConnection` via `store.Connections`, `HasJDM` via a GetJDM
      probe), then run each fixture through the interpreter with a MOCKED registry + evaluator
      (fixture `mocks`), collecting pass/fail + diffs; respond 200 with
      `{ok, structural:[...], fixtures:[...]}` — NEVER 5xx for a failed fixture (publish-blocking is
      `ok:false`). dry-run: decode `{env, flowId, version?, input, mocks?}`, resolve the flow
      (active via `store.ActiveFlow` or a specific version), build `flow.Ctx` from `input`, attach
      `observ.WithDryRun(ctx)` + `observ.WithCollector(ctx, observ.NewTraceCollector(nil))`, run the
      interpreter (reads hit real sources unless mocked; writes suppressed by step 1), and respond
      with `{trace: collector.Result(ctx,true).Steps, response, errors}`. Register admin routes
      unauthed (control-plane facing). Reuse the per-request templating registry / branching
      evaluator adapters from `adapters.go` where real sources are used.
      Files: `internal/httpapi/admin.go` (new), `internal/httpapi/server.go` (wire routes into NewHandler)
      Verify: `CGO_ENABLED=1 go build ./internal/httpapi` exits 0; `CGO_ENABLED=1 go test ./internal/httpapi` (unit) passes.

- [ ] 7. Add admin-endpoint unit tests (no Docker, no CGO).
      Table-driven `internal/httpapi/admin_test.go`: validate with a structurally-bad tree returns
      `ok:false` with a structural issue and HTTP 200 (never 5xx); validate with a failing fixture
      returns `ok:false` with a fixture diff and HTTP 200; dry-run over a Trigger→Action(write)→
      Response flow with a fake registry asserts the write node records `wrote:"suppressed"`, the
      fake client's Execute was NOT called for the write, and the trace lists the nodes in order.
      Use fakes (fake Store, fake Registry, fake Evaluator) mirroring `server_test.go`.
      Files: `internal/httpapi/admin_test.go` (new)
      Verify: `CGO_ENABLED=1 go test ./internal/httpapi -run 'Admin|Validate|DryRun'` passes.

- [ ] 8. Rewire cmd/engine: real dependency construction + config selection + graceful shutdown.
      Rewrite `cmd/engine/main.go` `run(...)` to: build the OTel tracer provider
      (`observ.NewTracerProvider` over a batch exporter — stdout/OTLP), `observ.NewOTelTracer(tp)`,
      the slog `observ.NewLogger`, and `observ.NewMetrics(prometheus.NewRegistry())`; select the
      config store — if `-config-dsn`/`CONFIG_DSN` set, `config.OpenPool`+`config.Migrate`+
      `config.NewPgStore(map[string]*pgxpool.Pool{env: pool}, ...)`, else `config.LoadSeed(seedPath)`
      with a startup log naming which store was chosen; build `connect.New(drivers.All(),
      store.Connections(ctx, env), connect.NewEnvSecretProvider(), tracer, obsLog)` (valkey driver
      is already in `drivers.All()`); build `decision.New(jdmLoader{store}, WithLogger, WithTracer)`;
      build the auth middleware via `httpapi.BuildAuthMiddleware(authCfg, engine, obsLog)` and log
      ENABLED/DISABLED; build `flow.New()` and `httpapi.NewServer(addr, store, interp, deps)` with
      `deps.Auth` set. Serve in a goroutine; on SIGINT/SIGTERM `srv.Shutdown(bounded ctx)` then the
      deferred teardown closing the registry pools, `engine.Close()`, the pgx pool (if any), and
      `observ.ShutdownProvider(ctx, tp)` to flush traces. Add flags/env: `-config-dsn`, `-env`,
      auth config (issuer/audience/jwks-url/authz-jdm) read from flags or env.
      Files: `cmd/engine/main.go`
      Verify: `CGO_ENABLED=1 go build ./...` exits 0; `CGO_ENABLED=1 go vet ./...` clean;
      `CGO_ENABLED=0 go build ./internal/decision` exits 0 (stub still compiles).

- [ ] 9. Extend the config seed with an authz JDM + make auth exercisable in tests.
      Add an `authz` JDM to `internal/config/testdata/seed.json` (a tiny ZEN decision returning
      `{"allow": true}` for the test principal/roles, matching the format of the existing `order`
      JDM) so an integration test can turn auth ON. Do NOT change the active `orders` route or the
      `order` JDM. If the seed schema needs an id for the authz JDM, use `authz` and reference it as
      `AuthzJDMID` in the integration test config.
      Files: `internal/config/testdata/seed.json`
      Verify: `CGO_ENABLED=1 go test ./internal/config` passes (seed still loads: ActiveFlow + GetJDM('order') + GetJDM('authz') + Connections).

- [ ] 10. Add the end-to-end wiring integration test (auth + multi-node flow + admin dry-run).
      Extend `internal/httpapi/integration_test.go` (build tags `integration && cgo`) reusing the
      proven `startPostgres` harness + httptest REST stub. Cover: (a) the existing two-branch
      orders flow still passes through the REAL handler; (b) AUTH ON — build the auth middleware with
      a `NewStaticKeySource` keyset + an RS256 token signed by that key and the `authz` JDM, assert a
      valid bearer reaches the flow (200) and a missing/invalid bearer is 401, and (with an allow:false
      authz input) a 403; (c) a `POST /admin/flows/dry-run` over the orders flow returns a node trace
      where the REST POST action is recorded `wrote:"suppressed"` AND the httptest stub recorded ZERO
      hits for that run (write suppressed), while reads still ran; (d) a `POST /admin/flows/validate`
      with the seed's flow returns `ok:true`. Point `orders-pg` DSN at the ephemeral postgres and
      `ship-rest` baseURL at the stub via the existing `overrideEndpoints`.
      Files: `internal/httpapi/integration_test.go`
      Verify: `CGO_ENABLED=1 go test -tags 'integration cgo' ./internal/httpapi` passes all cases (Docker up).

- [ ] 11. Update README + Makefile for the new wiring surface.
      Document in `README.md`: the config-store selection (`-config-dsn`/`CONFIG_DSN` → PgStore +
      migrations, else in-memory seed), the auth toggle (issuer/audience/jwks-url/authz-jdm; DISABLED
      with a startup warning when unset), the admin endpoints (`POST /admin/flows/validate`,
      `POST /admin/flows/dry-run`) and their contracts, OTel/metrics wiring, and graceful shutdown
      order. Keep the CGO_ENABLED=1 + glibc-image notes. Confirm the Makefile `test-integration`
      target still points at `./internal/httpapi`.
      Files: `README.md`, `Makefile` (only if a target needs adjusting)
      Verify: `CGO_ENABLED=1 go build ./...` exits 0 (docs change is non-code; this re-confirms the tree still builds).

- [ ] 12. Full gate — build, vet, unit, stub-build, and Docker integration.
      Run the whole acceptance gate and fix any integration seams surfaced.
      Files: none (verification step)
      Verify, all must pass: `CGO_ENABLED=1 go build ./...`; `CGO_ENABLED=1 go vet ./...`;
      `CGO_ENABLED=1 go test ./...`; `CGO_ENABLED=0 go build ./internal/decision`;
      `CGO_ENABLED=1 go test -tags 'integration cgo' ./internal/httpapi` (Docker up).

## Notes / assumptions

- OTel exporter: no OTLP collector is assumed available in CI, so default the exporter to a stdout
  span exporter (or a no-op/batcher over stdout) unless an `OTEL_EXPORTER_OTLP_ENDPOINT` is set.
  The tracer must be wired either way so the whole-walk trace exists; the exporter choice is a
  startup detail, not a seam change.
- Ops endpoints (`/livez`,`/readyz`,`/metrics`) are described in slice-e but are OUT of this task's
  explicit scope (items 1-4). If the implementer wires `observ.NewMetrics`, exposing `/metrics` and
  a `/readyz` gated on `registry.HealthCheck` + `store.Ping` is a small, in-spirit addition on the
  same mux — include it only if it does not expand the gate; otherwise leave for a later increment.
- The auth JWKS fetcher is the one genuinely new piece of logic (no real fetcher exists on
  mainline). Keeping it minimal (stdlib fetch + JWK→crypto.PublicKey) OR using a static JWKS source
  is an acceptable v1 choice; the deny-by-default middleware and verifier are already built.
- Do NOT rewrite the built packages. The only edit outside `cmd/engine` + `internal/httpapi` is the
  dry-run honoring in `internal/flow` (steps 1-2), which the task explicitly permits as a
  cross-cutting wiring concern.
