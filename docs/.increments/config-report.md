# Increment report — real internal/config (Slice D: config store + versioning + cache)

Branch: `feat/config-store` (worktree-isolated). Replaces the thin-slice in-memory
stub with the real Postgres-backed `config.Store` + golang-migrate-style migrations
+ versioning model + Valkey-backed `config.Cache` with pub/sub invalidation. The
in-memory `memStore` + seed are kept (the thin slice and DB-free unit tests use
them); both implementations satisfy the frozen `config.Store` seam, so the later
wiring step swaps one for the other without a caller change.

Implements the frozen contracts in `docs/lld-contracts.md` and the design in
`docs/lld/slice-d-configstore.md`, grounded in the engineering-standards wiki:
`data-and-migrations` (versioned/ordered migrations, parameterized queries, bounded
pools, indexes per query path, expand/contract), `versioning-and-compatibility`
(immutable numbered versions, contract-test checksum anchor), `caching-strategy`
(bounded TTL + jitter, namespaced/versioned keys, explicit invalidation, defined
cache-down behavior, single-flight on the hot key), `config-and-secrets`
(secret_ref only, never a value; fail-fast construction), `error-classification`
(typed/sentinel errors via errors.Is, never string matching).

## What was built

- `migrations/0001_init.{up,down}.sql` — full schema: `flows/jdms/connections`
  identities, immutable `*_versions`, `flow_fixtures`, `active_pointers`,
  `audit_log`, and the hot-path indexes. `0002_add_flow_version_note.{up,down}.sql`
  — an example additive (expand-only) migration demonstrating the discipline.
- `migrations/migrations.go` — embeds the SQL and applies/rolls back in order over
  any pgx-compatible `Execer` (no golang-migrate library dependency; keeps the
  "stdlib + pgx + a valkey client only" footprint).
- `internal/config/pgstore.go` — `PgStore` over `pgxpool` (one pool per env):
  cache-aside `ActiveFlow` (single-flight on miss), `GetJDM`, `Connections`
  (secret_ref only), append-only `PutFlowVersion` (version = max+1 under
  `SELECT … FOR UPDATE`), `SetActive` (publish/rollback = pointer move + audit in
  one tx, refuses un-validated versions, post-commit cache invalidate), `Ping`.
  Store-internal (admin/Strapi-driven): `MarkValidated`, `PromoteVersion`,
  `AuditTrail`. Parameterized SQL only; multi-write invariants wrapped in a tx.
- `internal/config/cache.go` — `ValkeyCache` over valkey-go (RESP; no ORM, matching
  the sibling connect slice's client choice): bounded TTL + jitter, namespaced +
  versioned keys, DEL + PUBLISH on `Invalidate`, a startup subscriber that drops
  matching keys fleet-wide (R5, AC-16), degrade-to-store on transport error,
  TTL backstop. The cross-instance eventual-consistency boundary (Slice D §4,
  review #5) is documented in a code comment on the type.
- `internal/config/codec.go` / `keys.go` — defensive encode/decode (a malformed
  tree/jdm is a classified `Validation`, never a 500 — AC-15), checksum anchor,
  and the `cfg:v1:…` key scheme + invalidation channel.
- `internal/config/errors.go` — extended the config taxonomy with `Timeout` and
  `Upstream` to match Slice D §7.

## Dependencies added (go.mod — only what this package needs)

- `github.com/valkey-io/valkey-go v1.0.78` (Valkey RESP client; same pin as the
  connect slice). It requires `go 1.25.0`, so the module `go` directive was raised
  from `1.22` to `1.25.0` (toolchain is Go 1.26). This matches the sibling
  `feat/connect-resilience` worktree exactly, avoiding two RESP clients in the
  eventually-merged module.
- `golang.org/x/sync v0.22.0` (promoted from indirect) for `singleflight` on the
  hot resolve path.

## Verification (real output)

```
$ CGO_ENABLED=1 go build ./internal/config/...
build exit: 0

$ CGO_ENABLED=1 go vet ./internal/config/...
vet exit: 0

$ CGO_ENABLED=1 go test ./internal/config/...
ok  	nzr-rules-engine/internal/config	0.010s
unit exit: 0

$ CGO_ENABLED=1 go test -tags 'integration cgo' -count=1 ./internal/config/...
ok  	nzr-rules-engine/internal/config	67.726s
```

Integration run (ephemeral `postgres:16` + `valkey/valkey:8-alpine`, torn down per
test), verbose, all PASS:

```
--- PASS: TestAC9_PutFlowVersionImmutable (6.26s)
--- PASS: TestAC10_PublishAndRollback (5.50s)
--- PASS: TestAC11_ResolvedSnapshotIsPinned (6.20s)
--- PASS: TestAC12_PromoteVersionAcrossEnvs (5.66s)
--- PASS: TestAC13_PublishBlockedOnUnvalidated (10.56s)
--- PASS: TestAC15_MalformedConfigRejectedOnLoad (23.90s)
--- PASS: TestAC16_CacheInvalidationAcrossInstances (4.81s)
--- PASS: TestAC16_TTLBackstop (3.14s)
--- PASS: TestResolveCacheAsideAndDegrade (14.36s)
--- PASS: TestConnectionsNeverStoreSecretValue (11.45s)
--- PASS: TestMigrationsUpDown (13.67s)
PASS
ok  	nzr-rules-engine/internal/config	105.532s
```

(Unit tests `TestDecodeTreeMalformed`, `TestFlowEncodeDecodeRoundTrip`,
`TestChecksumStable`, `TestKeyScheme…`, plus the retained seed tests also pass in
both runs.)

## AC → test map (AC-9..AC-16)

| AC | Test | Asserts |
| --- | --- | --- |
| AC-9  | `TestAC9_PutFlowVersionImmutable` | second put gets v2; v1 row byte-identical & retrievable; a create_version audit row per version |
| AC-10 | `TestAC10_PublishAndRollback` | pointer advances on publish, moves back on rollback; both audited; re-resolve returns the new tree (no redeploy) |
| AC-11 | `TestAC11_ResolvedSnapshotIsPinned` | a resolved `FlowVersion` is self-contained and carries the pinned version; a later `SetActive` does not mutate it (hand-off to Slice A) |
| AC-12 | `TestAC12_PromoteVersionAcrossEnvs` | exact version + its fixtures copied to the staging DB; staging pointer unchanged; promote audited in the destination |
| AC-13 | `TestAC13_PublishBlockedOnUnvalidated` | `SetActive` on an un-validated version is refused (`Validation`); succeeds after `MarkValidated` |
| AC-14 | — | dry-run write-suppression lives in the admin endpoints (`/admin/flows/dry-run`), a separate NEXT increment (Slice D §6b); not in this store/cache increment |
| AC-15 | `TestAC15_MalformedConfigRejectedOnLoad` + `TestDecodeTreeMalformed`/`TestDecodeFlowMalformed` | a broken stored tree is classified `Validation` on load — no 500, no panic |
| AC-16 | `TestAC16_CacheInvalidationAcrossInstances` + `TestAC16_TTLBackstop` | a publish on one instance drops the reader instance's key within one pub/sub round-trip; TTL bounds staleness if a message is missed |
| secrets | `TestConnectionsNeverStoreSecretValue` | only `secret_ref` is read/stored; schema has no secret-value column |
| migrations | `TestMigrationsUpDown` | up builds every table + the additive note column; down tears down cleanly |

## Scope notes / deliberate boundaries

- AC-14 and `/admin/flows/validate` + `/admin/flows/dry-run` endpoints are the
  admin-surface increment (Slice D §6b), kept out of this store/cache slice. The
  store already exposes the pieces they need: `MarkValidated` (publish gate),
  fixtures travelling with a version, and the dry-run write-suppression seam is
  Slice A's `observ.WithDryRun`/`IsDryRun` obligation (noted in lld-contracts.md).
- Import direction respected: `config` imports `flow` (via `FlowVersion.Tree`),
  never the reverse. No change to `cmd/engine` or `internal/httpapi` (the wiring
  join is a later sequential step).
- Cross-instance activation is eventually consistent by design (documented in the
  `ValkeyCache` type comment, Slice D §4 review #5): per-request pinning means no
  request tears; no global activation lock (R10).
