# Increment report — Config-store run mode

Branch `feat/config-run-mode`, worktree `.worktrees/config-run`.

## What this increment did

Wired the already-built, unit/integration-tested `config.PgStore` + `config.ValkeyCache` into the
runnable `cmd/engine` binary so the engine can run against a real Postgres + Valkey as its OWN
config backing store. Config tables live in a dedicated Postgres schema (default `rule_engine`),
never `public`. When `CONFIG_DSN` is unset the binary keeps the original in-memory seed path
byte-for-byte (no DB touched).

### Files added
- `internal/envfile/envfile.go` (+ `_test.go`) — dependency-free `.env` loader. `KEY=VALUE`,
  missing file is nil, comments/blanks skipped, surrounding quotes stripped, process env wins,
  malformed line (no `=`) errors with the line number.
- `internal/config/connect_pg.go` (+ `_test.go`) — `ValidateSchemaName`
  (`^[A-Za-z_][A-Za-z0-9_]*$`, the ONLY interpolated identifier), `OpenSchemaPool` (pgxpool with
  `AfterConnect` → `SET search_path TO <schema>`, then `CREATE SCHEMA IF NOT EXISTS`), and
  `SchemaHasConfigTables` (gates re-running the bare `CREATE TABLE` DDL on a second start).
- `internal/config/seed_pg.go` — `SeedPgStore(ctx, store, raw)`: idempotent seed THROUGH the store
  (skip when the first flow's route is already active). Active flows go
  `PutFlowVersion → MarkValidated → SetActive` (PgStore blocks publishing an un-validated version,
  unlike memStore).
- `internal/config/configrun_integration_test.go` — build-tagged `integration && cgo`; two
  ephemeral postgres:16 + one ephemeral valkey; asserts (a)–(d). NEVER reads CONFIG_DSN/ORDERS_PG_DSN
  from the environment.
- `.env.example` — documents CONFIG_DSN / CONFIG_SCHEMA / VALKEY_ADDR / ENGINE_ADDR / ORDERS_PG_DSN /
  SHIP_REST_BASE_URL / RUN_REST_STUB / REST_STUB_PORT (no real secrets).

### Files changed
- `internal/config/pgstore.go` — added `PutJDMVersion` and `PutConnectionVersion` (transactional,
  parameterized, mirroring `PutFlowVersion`: identity upsert → `FOR UPDATE` lock → `max+1` → insert
  version → move active pointer → create_version + publish audit rows). Connection versions store
  `secret_ref` only (never a secret value).
- `cmd/engine/main.go` — `.env` load before reading config env; `ENGINE_ADDR` overrides `-addr`;
  `-env` flag; `buildStore` selects config-store mode (OpenSchemaPool → gated migrations.Apply →
  optional ValkeyCache+consumer → PgStore → SeedPgStore) vs the original in-memory path; redacts
  the DSN password in the connect log; LIFO cleanup of cache/client/pool.
- `docs/STATE.md` — corrected the overstated "Wiring join" claim (cmd/engine only ran LoadSeed; it
  did NOT select PgStore-vs-seed behind CONFIG_DSN) and added a "Config-store run mode — DONE"
  subsection; bumped Last updated.

## Design notes
- Schema isolation via pool `AfterConnect` (`SET search_path TO <schema>`), robust across
  reconnects; migration SQL stays env-agnostic (unqualified `CREATE TABLE`).
- Idempotency: top-level seed-skip gate (first flow already active ⇒ skip); write methods also
  individually safe (identity `ON CONFLICT DO NOTHING`, version `max+1` under `FOR UPDATE`, pointer
  `ON CONFLICT DO UPDATE`); `migrations.Apply` gated behind a `flows`-table existence check.
- The one env pool map is `{"": pool}` to match the committed `testdata/seed.json` (`env: ""`).

## Verification — REAL output

Toolchain: `go version go1.26.2 linux/amd64`. Docker UP (ephemeral postgres:16 + valkey only; no
external/user DB touched).

### `CGO_ENABLED=1 go build ./...`
```
exit=0
```

### `CGO_ENABLED=1 go vet ./...`
```
exit=0   (no output)
```

### `CGO_ENABLED=1 go test ./...`
```
?   	nzr-rules-engine/cmd/engine	[no test files]
ok  	nzr-rules-engine/internal/auth	(cached)
ok  	nzr-rules-engine/internal/config	(cached)
ok  	nzr-rules-engine/internal/connect	(cached)
?   	nzr-rules-engine/internal/connect/drivers	[no test files]
ok  	nzr-rules-engine/internal/decision	(cached)
ok  	nzr-rules-engine/internal/envfile	0.010s
ok  	nzr-rules-engine/internal/flow	(cached)
ok  	nzr-rules-engine/internal/httpapi	(cached)
ok  	nzr-rules-engine/internal/observ	(cached)
?   	nzr-rules-engine/migrations	[no test files]
```

### `CGO_ENABLED=0 go build ./internal/decision` (stub compiles)
```
stub exit=0
```

### New integration test — `CGO_ENABLED=1 go test -tags 'integration cgo' -run TestConfigRunModeSeedsIntoSchema ./internal/config`
```
=== RUN   TestConfigRunModeSeedsIntoSchema
--- PASS: TestConfigRunModeSeedsIntoSchema (11.05s)
PASS
ok  	nzr-rules-engine/internal/config	11.061s
```
Asserts (a) config tables in `rule_engine`, 0 in `public`; (b) flow active + JDM + both
connections (ship-rest secret_ref empty, no secret-value column); (c) second seed `seeded==false`
with exactly one version per object; (d) `GET /orders/1`→expedited (`/expedite`),
`GET /orders/2`→standard (`/standard`), config resolved from the PgStore.

### Full integration suites — `CGO_ENABLED=1 go test -tags 'integration cgo' ./internal/config ./internal/httpapi`
```
ok  	nzr-rules-engine/internal/config	86.535s
ok  	nzr-rules-engine/internal/httpapi	6.178s
```
Existing AC-9..AC-16 config tests + the new config-run test, and the httpapi end-to-end test
(confirms the in-memory seed path is untouched), all green.

## Constraint compliance
- Ephemeral containers ONLY; no external/user DB connected during build or tests.
- stdlib + existing deps only (`pgx/v5`, `pgxpool`, `valkey-go`); `.env` loader hand-rolled.
- Parameterized SQL throughout; the schema name is the sole interpolated identifier and passes
  `ValidateSchemaName` first.
- Secrets stay out of the DB (secret_ref only); DSN password redacted in logs.
