# nzr-rules-engine — v1 thin vertical slice

A data-plane rules engine that walks a declarative flow tree per request: read a
source, run a GoRules **ZEN** decision over the data, branch, call a downstream,
and stitch a response. This repository is the **thin vertical slice** — one real
route proving the whole pipeline end to end against a real Postgres and a real
HTTP downstream, with the decision driven by the embedded ZEN engine.

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

The Go engine lives under `engine/` (its own `go.mod`); run all Go commands from
there. The Strapi CMS lives under `cms/`. From the repo root you can also use the
`Makefile` targets (`make build`, `make test`, `make vet`, `make test-integration`),
which `cd` into `engine/` for you.

```sh
# Build the whole engine tree (CGO on).
cd engine && CGO_ENABLED=1 go build ./...

# Run the engine (serves :8080, loads the committed seed).
cd engine && CGO_ENABLED=1 go run ./cmd/engine
# or choose the address / seed:
cd engine && CGO_ENABLED=1 go run ./cmd/engine -addr :9090 -seed internal/config/testdata/seed.json
```

The process starts the HTTP server and blocks on `SIGINT`/`SIGTERM`. On a
termination signal it drains in-flight requests with a bounded timeout, then
closes the connection pools and the decision engine (freeing the ZEN graphs).

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
cd engine && CGO_ENABLED=1 go test ./...

# Real-HTTP end-to-end integration test (Docker REQUIRED). Starts an ephemeral
# postgres:16 and an in-process httptest REST stub, drives the real handler +
# real registry + real ZEN engine, and asserts both branch cases.
cd engine && CGO_ENABLED=1 go test -tags 'integration cgo' ./internal/httpapi
```

The integration test is guarded by `//go:build integration && cgo`, so a plain
`go test ./...` never requires Docker. It needs the Docker daemon running: it
runs `docker run -P postgres:16`, resolves the mapped port, polls until the
server accepts connections, then tears the container down.

## Layout

Two separate components at the repo root — the Go engine (data plane) and the
Strapi CMS (control plane / authoring UI):

```
engine/            the Go rules engine (own go.mod, module nzr-rules-engine)
  cmd/engine/      process wiring + graceful shutdown
  internal/
    flow/          interpreter, ctx accumulator, node handlers (pure core)
    connect/       connection registry + resilience
      drivers/     postgres + valkey + rest connectors
    decision/      ZEN decision engine (cgo) + non-cgo stub + compiled cache
    config/        config store (in-memory seed + Postgres), seeded from JSON
    observ/        slog-backed tracer + logger + metrics
    auth/          JWT (data plane) + operator-token (admin plane) auth
    httpapi/       stdlib ServeMux edge, Ctx construction, admin API, adapters
  migrations/      embedded config-store SQL migrations
cms/               the Strapi 5 CMS (own package.json, own Postgres db+schema)
docs/              HLD, ADRs, per-slice LLDs, state/handoff
```

See `docs/` for the HLD, ADRs, and the per-slice LLDs this slice realizes.
