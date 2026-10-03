# nzr-rules-engine — v1 engine

A data-plane rules engine that walks a declarative flow tree per request: read a
source, run a GoRules **ZEN** decision over the data, branch, call a downstream,
and stitch a response. The engine now wires the real v1 slices behind the frozen
seams — the config store, the auth middleware, the OTel provider, and the full
node handler set — into the running HTTP server, plus the control-plane admin
`validate`/`dry-run` endpoints.

Module path: `nzr-rules-engine` · Go 1.22+ · stdlib `net/http` only (no chi/gin).

## The one route

```
GET /orders/{id}
```

The handler resolves and **pins** the active flow version once (version pinning
at the request boundary), builds the request context, and runs the interpreter.
The seeded flow is:

```
Trigger -> Action(postgres: SELECT amount,status FROM orders WHERE id=$1)
        -> Condition(ZEN "order" JDM over {amount,status})
             -> expedited: Action(POST /expedite) -> Set(shipping) -> Response(200)
             -> standard : Action(POST /standard) -> Set(shipping) -> Response(200)
```

The decision table is `amount > 1000 AND status == 'paid' => "expedited"`, else
`"standard"`. A high-value paid order expedites; a low-value paid order ships
standard.

Classified engine errors map to HTTP status at the edge: `Validation -> 400`,
`NotFound -> 404`, `Timeout -> 504`, `Upstream -> 502`, `Internal -> 500`.

## Build and run

**CGO is required.** The decision engine links the embedded ZEN native library
(`github.com/gorules/zen-go/v2`, which links `-lzen_ffi`), so a C toolchain
(`gcc`/`cc`) must be present and `CGO_ENABLED=1` must be set to build or run the
engine. A `CGO_ENABLED=0` build still *compiles* the whole tree via the
`//go:build !cgo` stub in `internal/decision`, but that binary **refuses ZEN at
runtime** with a Validation-class error — it never silently no-ops.

```sh
# Build the whole tree (CGO on).
CGO_ENABLED=1 go build ./...

# Run the engine (serves :8080, loads the committed seed).
CGO_ENABLED=1 go run ./cmd/engine
# or choose the address / seed:
CGO_ENABLED=1 go run ./cmd/engine -addr :9090 -seed internal/config/testdata/seed.json
```

The process starts the HTTP server and blocks on `SIGINT`/`SIGTERM`. On a
termination signal it drains in-flight requests with a bounded timeout, then (in
order) closes the connection pools, closes the decision engine (freeing the ZEN
graphs), closes the Postgres config pool (if one was opened), and flushes the
OTel tracer provider.

### Config store: Postgres or in-memory seed

The engine selects its `config.Store` at startup:

- **Postgres (real store).** Set `-config-dsn` (or the `CONFIG_DSN` env var) to a
  Postgres DSN. The engine opens a pool, applies the embedded migrations, and
  serves config from the real versioned store for the environment named by
  `-env`/`ENV` (default the empty env). A startup log line records
  `config store: postgres`.
- **In-memory seed (default).** With no DSN, the engine loads the JSON seed named
  by `-seed` into the in-memory store (today's local/dev behavior). A startup log
  line records `config store: in-memory seed`.

```sh
# Real Postgres config store (applies migrations on boot):
CGO_ENABLED=1 CONFIG_DSN='postgres://user:pw@host:5432/cfg?sslmode=disable' \
  go run ./cmd/engine -env prod
```

### Auth: toggleable JWKS + ZEN authorization

The AuthN→AuthZ middleware is mounted in front of the flow route **only when
fully configured**; otherwise it is skipped and the route serves unauthenticated
(local dev + the no-auth integration test keep working). A clear startup log line
records `auth ENABLED` or `auth DISABLED`.

Auth is ENABLED when the issuer, audience, authz JDM id, and a key source are all
set:

| flag | env | meaning |
| --- | --- | --- |
| `-auth-issuer` | `AUTH_ISSUER` | expected JWT `iss` |
| `-auth-audience` | `AUTH_AUDIENCE` | expected JWT `aud` |
| `-auth-jwks-url` | `AUTH_JWKS_URL` | JWKS endpoint, fetched + parsed (RSA/EC) by kid |
| `-auth-authz-jdm` | `AUTH_AUTHZ_JDM` | ZEN JDM id evaluated for allow/deny (deny-by-default) |
| `-auth-roles-claim` | `AUTH_ROLES_CLAIM` | JWT claim carrying roles (default `roles`) |

AuthN validates the bearer JWT against the rotating JWKS (rotation without
restart); AuthZ evaluates the authz JDM over `{user,roles,resource,action}` and
proceeds only on an explicit `allow==true`. A missing/invalid token is `401`; a
deny (or evaluator error) is `403`.

### Admin endpoints (control plane)

Registered on the same mux, side-effect-free, and NOT behind the flow auth chain:

- `POST /admin/flows/validate` — structural `ValidateTree` + runs each fixture
  through the interpreter with mocked sources. **Publish-blocking**: returns
  `{ok:false, structural:[...], fixtures:[...]}` on any failure and always HTTP
  `200` (a failed fixture is never a `5xx`).
- `POST /admin/flows/dry-run` — resolves the active flow for the request's route
  and runs it with writes **suppressed** (`observ.WithDryRun`): write actions
  are not executed (no row, no external POST) and are recorded `wrote:"suppressed"`.
  Returns the node-by-node trace, the stitched response, and any errors.

### Observability

`cmd/engine` builds the OTel tracer provider (so the whole request walk is one
trace with a `trace_id`), the slog structured logger, and the RED Prometheus
metric set. The per-request scope (`trace_id`/`flow_id`/`flow_version`/`env`) is
stamped and enriched the moment the flow version is pinned, so every span and log
line downstream carries it. The default span exporter discards spans; a
production deploy swaps in an OTLP batch exporter through the same
`observ.NewTracerProvider` seam (exporter choice is a startup detail, not a seam
change).

### Container base image — must be glibc

The vendored ZEN native library links **dynamically against glibc**
(`libc.so.6`, `libm.so.6`, `libgcc_s.so.1`). The container image **MUST be
glibc-based** — `debian-slim` or `distroless` — **NOT Alpine/musl** (per ADR-003
and the Phase 0 spike report, `docs/phase0-spike-report.md`). Alpine/musl is not
supported out of the box; a musl image would require rebuilding `libzen_ffi` for
musl. Build the binary with `CGO_ENABLED=1` and ship it on a matching glibc
runtime.

## Tests

```sh
# Unit tests (fakes only — no Docker, no network). CGO on so the whole tree,
# including the real decision package, is exercised.
CGO_ENABLED=1 go test ./...

# Real-HTTP end-to-end integration test (Docker REQUIRED). Starts an ephemeral
# postgres:16 and an in-process httptest REST stub, drives the real handler +
# real registry + real ZEN engine, and asserts: both order branch cases; the
# auth chain (401 no token / 200 valid+allowed / 403 valid+denied); admin
# validate (seed ok:true, broken flow ok:false); and admin dry-run (write
# suppressed, zero external POSTs).
CGO_ENABLED=1 go test -tags 'integration cgo' ./internal/httpapi
```

The integration test is guarded by `//go:build integration && cgo`, so a plain
`go test ./...` never requires Docker. It needs the Docker daemon running: it
runs `docker run -P postgres:16`, resolves the mapped port, polls until the
server accepts connections, then tears the container down.

## Layout

```
cmd/engine/        process wiring (store select, auth toggle, OTel) + graceful shutdown
migrations/        embedded config-store SQL + a dependency-free applier
internal/
  flow/            interpreter, ctx accumulator, node handlers (pure core) + dry-run suppression
  connect/         connection registry + resilience
    drivers/       postgres + rest + valkey connectors
  decision/        ZEN decision engine (cgo) + non-cgo stub + compiled cache
  config/          config store: in-memory seed + real Postgres PgStore (+ pool/migrate helpers)
  observ/          OTel tracer + slog logger + RED metrics + dry-run trace collector
  auth/            JWKS authenticator (rotation) + net/http AuthN/AuthZ + ZEN authorizer
  httpapi/         stdlib ServeMux edge, Ctx construction, adapters, auth wiring, admin endpoints
```

See `docs/` for the HLD, ADRs, and the per-slice LLDs this slice realizes.
