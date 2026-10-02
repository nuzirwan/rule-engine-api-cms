# nzr-rules-engine — project state & handoff

Single source of truth for picking up work in a fresh session. Everything below is committed on
the `mainline` branch. Read this first, then the docs it points to.

Last updated: 2026-10-02 · mainline HEAD at handoff: `4fd28a2`

## What this project is
A config-driven API engine (Go, data plane) + a planned Strapi CMS (control plane, separate
build). APIs are defined as config (flow trees + GoRules ZEN decisions), not code. Full rationale
in the docs below.

## Read these to get context (in order)
- `docs/hld.md` — High-Level Design: architecture, 8 ADRs, risk register (R0–R14), acceptance
  criteria (AC-S1..S3 spike gate + AC-1..28 v1), phased scope.
- `docs/lld.md` — LLD overview: package layout, how slices compose, AC coverage map, build order.
- `docs/lld-contracts.md` — the FROZEN Go interfaces every package implements. Build against these.
- `docs/lld/slice-{a,b,c,d,e}-*.md` — detailed per-package designs.
- `docs/phase0-spike-report.md` — proven build facts (zen-go/v2 @ v2.1.2, CGO, **glibc base image
  required**, API mapping in Slice C's "zen-go v2 mapping" block).

## Environment (verified)
Go 1.26 (go.mod floor 1.22) · gcc/CGO present · **CGO_ENABLED=1 required** for ZEN · Docker up
(ephemeral postgres:16 for integration tests) · stdlib net/http only (no chi/gin).

## Build / test commands
```
CGO_ENABLED=1 go build ./...
CGO_ENABLED=1 go vet ./...
CGO_ENABLED=1 go test ./...
CGO_ENABLED=1 go test -tags 'integration cgo' ./internal/httpapi   # needs Docker
CGO_ENABLED=0 go build ./internal/decision                         # stub must compile
```

## DONE (on mainline)
- Design: HLD + ADRs + 5 LLD slices + reconciled contracts.
- Phase 0 spike: GO (zen-go CGO + ZEN condition + nested flow proven). Throwaway packages removed.
- **v1 thin slice (reviewed APPROVED, merged):** real packages `internal/{flow,connect,decision,
  config,observ,httpapi}` + `cmd/engine` serve `GET /orders/{id}` end-to-end
  (Trigger→Postgres read→ZEN condition→branch REST→Set→Response) over real HTTP against real
  Postgres + REST stub, two branches. Build/vet/unit/integration all green.

### Thin-slice deliberate SHORTCUTS to replace in later increments
- `internal/config` is **in-memory**, seeded from `testdata/seed.json` — the real Postgres Store +
  migrations + versioning + cache are not built yet.
- `internal/connect` resilience is **timeout-only** (retry/breaker minimal/stubbed); **valkey
  connector not built**.
- `internal/flow` has only trigger/action/condition/set/response handlers; **switch/parallel/
  forEach/decision/logger are Validation-returning stubs**.
- **No auth** (AuthN/AuthZ) yet. No observability depth (OTel), no admin validate/dry-run endpoints.
- `Interpreter.Run` takes `(ctx, tree *flow.Node, ver flow.Version, c *Ctx, dep Deps)` (not
  `config.FlowVersion`) to break a flow↔config import cycle — documented seam deviation, AC-11
  intent preserved.

## NEXT — remaining v1, parallelizable (disjoint package ownership)
These depend only on the frozen contracts, so they can run concurrently off `mainline`, each
owning its own package dir; a final sequential step wires them in `cmd/engine`:
1. `internal/config` — real Postgres Store + golang-migrate migrations + config versioning
   (immutable versions, active_version pointer, rollback, audit) + Valkey cache w/ pub/sub
   invalidation. (Slice D) — **recommended first; it's the backbone others read.**
2. `internal/connect` — valkey connector + full retry+jitter+circuit-breaker resilience. (Slice B)
3. `internal/observ` (OTel tracing + RED metrics) + `internal/auth` (JWT/JWKS + AuthZ via ZEN).
   (Slice E + C)
4. `internal/flow` — remaining node types: switch/parallel/forEach/decision/logger + loop budgets.
   (Slice A)
5. Admin endpoints `POST /admin/flows/validate` + `/admin/flows/dry-run` + flow-test fixtures.
   (Slice D/§6b) — includes the dry-run write-suppression seam (`observ.WithDryRun`/`IsDryRun`;
   Slice A actionHandler must honor it).
Then: sequential wiring/integration join in `cmd/engine` + `internal/httpapi`.

Merge-contention rule for parallel runs: each worktree edits only its own package; nothing but
the final join step touches `go.mod` / `cmd/engine`.

## How to resume in a fresh session (paste this)
> Continue the nzr-rules-engine build. Read docs/STATE.md on the mainline branch of
> /home/nuzirwan/project/rule-engine-api for full context, then <pick an increment from "NEXT">.

## Workflow notes
- Substantial builds go through workflows (worktree-isolated, coder+semantic_reviewer gate,
  rebase+ff-merge onto mainline). The orchestrator removes worktrees after.
- Learnings are curated in the engineering-standards MCP wiki (ask before saving).
