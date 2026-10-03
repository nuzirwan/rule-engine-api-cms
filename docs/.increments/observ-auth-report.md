# Increment report — internal/observ (OTel + RED metrics) + internal/auth (JWT/JWKS + ZEN AuthZ)

Branch: `feat/observ-auth` (worktree `.worktrees/observ-auth`, off `mainline` 7f8b32a).
Scope touched: **only** `internal/observ/**`, `internal/auth/**`, plus `go.mod`/`go.sum`
(unavoidable — adds OTel, Prometheus, golang-jwt, x/sync). No edits to `cmd/engine`,
`internal/httpapi`, `internal/config`, `internal/connect`, `internal/flow`, or `lld-contracts.md`.

## What was built

### internal/observ — deepened from the thin slog slice
The FROZEN `Tracer`/`Span`/`Logger` seams (`observ.go`) are unchanged in signature; the basic
slog `Tracer`/`Logger` constructors are kept. Added behind/alongside them:

- **OTel Tracer/Span** (`tracer.go`, `provider.go`) over `go.opentelemetry.io/otel` — every
  span derives from the span already on `ctx`, so one `trace_id` spans the whole request walk
  (AC-21). `End(err)` stamps `status` + `error_class`; attribute values are redacted (AC-20).
  `NewTracerProvider` (batch) + `NewTestTracerProvider` (sync, in-memory exporter) + `ShutdownProvider`.
- **Canonical field set + request scope** (`fields.go`): `trace_id/request_id/flow_id/
  flow_version/node_id/node_type/environment/label/duration_ms/branch_taken` + `error_class/status`,
  with `WithScope`/`ScopeFrom`/`EnrichScope` (resolver stamps flow scope).
- **Central Redactor** (`redact.go`): deny-list by key convention + value fingerprinting +
  nested map/slice recursion + `ScrubString` for error strings — no secret value reaches a log,
  span, or dry-run sink (AC-20).
- **slog JSON Logger** (`logger.go`): `NewJSONLogger`/`NewJSONLoggerWithRedactor`, auto-stamps
  scope, dynamic `LevelVar` gate (config toggle without redeploy, AC-22), redaction before emit.
- **RED Prometheus metrics** (`metrics.go`): per-flow + per-connection rate/errors/duration,
  `nzr_breaker_state` gauge, inflight gauge, config-reload counter. Registered on an injected
  registry (DIP).
- **Per-node trace helper** (`tracenode.go`): `TraceNode` wraps a node exec in a child span,
  emits the per-node debug line, feeds the dry-run collector — decoupled from `flow` (takes
  `nodeID/nodeType` + an `exec` returning `NodeOutcome`) to avoid a `flow`↔`observ` import cycle.
- **Dry-run collector + context helpers** (`collector.go`): `TraceCollector` (frozen contract
  signature `Record(nodeID, nodeType, branch, attrs)`), `WithCollector`/`CollectorFrom`,
  `WithDryRun`/`IsDryRun`, `TraceRecord`/`TraceStep` — redacts recorded attrs (AC-20).
- **Error classification** (`errors.go`): `ErrorClass` taxonomy, `ClassOf` (reads a `Classified`
  error / context errors — no cross-slice import, no string matching), `Classify`/`Validationf`.

### internal/auth — new package (AuthN + AuthZ), pure Go
Implements the frozen `auth.*` seams (`auth.go`): `Authenticator`, `Authorizer`, `Principal`,
`AuthzInput`, `Decision`.

- **AuthN — JWT via JWKS** (`jwks.go`): `jwksVerifier` validates RS/ES-signed JWTs with
  `kid` lookup, **singleflight** refresh on a cache miss, **atomic keyset swap**, exp/nbf/iss/aud
  checks via `golang-jwt/jwt/v5` with an **injectable clock**. A miss for an absent `kid` always
  refreshes (rotation without restart, AC-18); a bounded TTL + a MinRefresh floor (bypassed for a
  genuinely-absent kid) blunt a JWKS DoS. Fails **closed** on fetch failure (Upstream-classified).
- **AuthZ — ZEN decision** (`authz.go`): `zenAuthorizer` projects `{user,roles,resource,action,
  attrs}` and calls `decision.Evaluator`. Allow **only** on explicit `allow==true`; absent/
  non-bool/false → deny; an `Evaluate` error is never an allow (deny-by-default, AC-19).
- **net/http middleware** (`middleware.go`): `Authn` injects `Principal` into ctx, 401
  deny-by-default on any failure; `Authz` returns 403 on deny / fail-closed on evaluator error;
  generic client bodies, detail only in audit logs; no token/secret in logs (AC-20).
- **Classified errors** (`errors.go`): sentinels (`ErrNoBearer`, `ErrTokenInvalid`,
  `ErrUnknownKey`, `ErrJWKSUnavailable`) implementing `observ.Classified`.

## Standards applied
Grounded in the engineering-standards wiki: `distributed-tracing` (one trace_id, OTel, W3C,
correlation==trace, bounded attribute cardinality), `security-and-authz` (authenticate-then-
authorize, deny-by-default, fail-closed, secrets never in logs, pin deps), `config-and-secrets`
(typed/validated-at-startup, secretRef-only), `error-classification` (typed/sentinel, never string
match), `observability-and-logging` (structured slog, levels, redaction), and the Go idioms page
(sentinel errors + `errors.Is/As`, `%w`, lowercase error strings, table-driven tests, inject
clocks/deps, nil-safe constructors). Content was rephrased for compliance with licensing restrictions.

## Verification (real output)

```
$ CGO_ENABLED=1 go build ./internal/observ ./internal/auth
exit=0

$ CGO_ENABLED=1 go vet ./internal/observ ./internal/auth
exit=0

$ CGO_ENABLED=1 go test ./internal/observ ./internal/auth
ok  	nzr-rules-engine/internal/observ	0.016s
ok  	nzr-rules-engine/internal/auth	0.376s
exit=0
```

Race detector (both packages):
```
$ CGO_ENABLED=1 go test -race ./internal/observ ./internal/auth
ok  	nzr-rules-engine/internal/observ	1.071s
ok  	nzr-rules-engine/internal/auth	1.731s
```

Per-test (`go test -count=1 -v`), mapped to ACs:

```
internal/observ:
  PASS TestRedactorScrubsByKey / ScrubsByValue / ScrubString / DoesNotMutateInput   (AC-20)
  PASS TestOneTraceIDSpansWalk                                                       (AC-21, AC-2 branch_taken)
  PASS TestSpanRecordsErrorClass                                                     (error seam)
  PASS TestSpanNeverLeaksSecret                                                      (AC-20)
  PASS TestTraceNodeFeedsCollectorAndLogger                                          (AC-21 + dry-run)
  PASS TestLoggerStampsScope / LevelGate / RedactsFields                             (AC-22, AC-20)
  PASS TestFlowMetrics / BreakerGauge / ConnMetricsAndInflight                       (RED metrics)
  PASS TestDryRunFlag / CollectorRecordsOrderedSteps / CollectorFromContext          (dry-run, AC-20)
  PASS TestClassOf / ClassifyWrapsCause                                              (error classification)

internal/auth:
  PASS TestTokenValidityMatrix       valid/expired/not-yet-valid/bad-sig/wrong-iss/wrong-aud/
                                     unknown-kid-missing/unknown-kid-rotation/missing/garbled   (AC-18)
  PASS TestRotationRefreshesOnce     single singleflight fetch + atomic swap, cache reuse        (AC-18)
  PASS TestJWKSUnavailableFailsClosed                                                            (fail-closed)
  PASS TestVerifierConfigValidation  missing issuer/audience fail fast                           (config-and-secrets)
  PASS TestAuthZMatrix               allow / deny / allow-absent-deny / non-bool-deny /
                                     nil-output-deny / evaluator-error-never-allow               (AC-19)
  PASS TestAuthZProjection           core keys projected, Attrs cannot override
  PASS TestNewAuthorizerValidation
  PASS TestAuthnMiddleware           success injects Principal; missing/err -> 401, next skipped (AC-18)
  PASS TestAuthzMiddleware           allow 200 / deny 403 / eval-error 403 fail-closed / no-princ 401 (AC-19)
  PASS TestNoSecretLeakInAuditLogs   raw bearer never in any emitted field                       (AC-20)
```

All tests use fakes (fake `decision.Evaluator`, fake/static `KeySource`, injected clock,
in-memory OTel exporter, in-memory Prometheus registry). No network, no real ZEN.

## Notes / known issues (out of scope)
- `go.mod` go directive moved to `go 1.26.0` (a transitive OTel dep requires it; env is Go 1.26).
- **Pre-existing, unrelated failure:** `internal/decision` CGO tests
  (`TestEvaluateDeterministic/Branches`, `TestCompileOncePerVersion`) fail reading
  `../zenspike/testdata/order.jdm.json` — the `zenspike` spike package was removed in `4fd28a2`
  but those tests still reference its fixture. Confirmed broken on `mainline` before this
  increment; `internal/decision` is outside this increment's allowed scope, so it was left untouched.

## Seam notes for the integration join
- `TraceNode` signature is decoupled from `flow` (uses `NodeOutcome{Branch,Err}` + a string
  nodeID/nodeType) to avoid a `flow`↔`observ` import cycle — Slice A adapts its `flow.Directive`
  at the call site.
- `observ.ClassOf` reads a class via the additive `observ.Classified` interface (and context
  errors), so `flow`/`connect`/`decision` error wrappers can opt in without a seam change; it
  never imports those packages.
- Redactor/Logger/Tracer/Collector share one `Redactor` via the `*WithRedactor` constructors so
  `RegisterSecret` (called by the Registry when resolving a secretRef) covers all sinks.
- `auth.KeySource` is the JWKS seam; `NewStaticKeySource` is the test/offline double. A real
  JWKS-endpoint fetcher is a thin future adapter (not required by this increment's tests).
```
