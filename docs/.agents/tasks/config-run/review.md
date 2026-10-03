# Config-store run mode for cmd/engine (Postgres + Valkey)

Wires the already-built `config.PgStore` + `config.ValkeyCache` into the runnable `cmd/engine` binary so the engine can run against a real Postgres as its own config backing store, with config tables isolated in a dedicated `rule_engine` schema (never `public`). Selection is driven by `CONFIG_DSN`: set it and the binary opens a schema-scoped pool, applies migrations, optionally fronts a Valkey cache, and seeds `seed.json` into Postgres idempotently through new transactional write methods; leave it unset and the original in-memory seed path runs byte-for-byte with no DB touched. The seed load goes through the store (`PutFlowVersion`/`PutJDMVersion`/`PutConnectionVersion`), not raw SQL in `main`, and a new ephemeral-container integration test proves schema isolation, seeding, idempotency, and end-to-end serving.

Watch for: nothing blocking. The schema name is the sole interpolated SQL identifier and is validated (`^[A-Za-z_][A-Za-z0-9_]*$`) before any use (confirmed); secret values never reach the DB (confirmed); re-running the seed is idempotent via a check-active-then-skip gate plus a migrations existence gate (confirmed). One minor observation on the migrations gate relying on a single sentinel table (possible, non-blocking).

**Verdict**: APPROVED

## High-level view

Schema isolation is implemented the way the plan describes: `OpenSchemaPool` validates the schema name, builds a pgxpool whose `AfterConnect` runs `SET search_path TO <schema>` on every connection (initial and reconnect), then runs `CREATE SCHEMA IF NOT EXISTS` on an acquired connection before any migration. Because the migration DDL is unqualified `CREATE TABLE`, the tables land in `<schema>.*` and `public` is untouched. The schema name is the one and only interpolated identifier, and it cannot reach a statement without passing `ValidateSchemaName`.

The write methods are real PgStore methods, transactional and parameterized, mirroring `PutFlowVersion`: identity upsert with `ON CONFLICT DO NOTHING`, a `FOR UPDATE` lock on the identity row, `max(version)+1`, a version insert with checksum and actor, an active-pointer move with `ON CONFLICT DO UPDATE`, and `create_version` + `publish` audit rows. `PutConnectionVersion` writes `secret_ref` only (NULL when empty) and there is no column for a secret value. No raw SQL for these writes lives in `cmd/engine`.

Idempotency has two gates: `SeedPgStore` resolves the first flow's active route and returns `(false, nil)` if it already resolves (skipping all writes), and `buildStore` gates `migrations.Apply` behind a `flows`-table existence check so a second process start does not re-run the bare DDL. The integration test asserts the second seed reports `seeded==false` with exactly one version per object.

Run-mode selection branches cleanly on `CONFIG_DSN` inside `buildStore`: unset takes the exact original `LoadSeed` path with a no-op cleanup; set stands up the Postgres path with LIFO cleanup of cache subscriber → valkey client → pool. The `.env` loader sets a key only when unset (process env wins), treats a missing file as nil, and is wired before any config env read. `ENGINE_ADDR` overrides the `-addr` flag.

The new integration test is ephemeral-only: two `postgres:16` containers (config store + orders data table) and one valkey, with an explicit file comment that it never reads `CONFIG_DSN`/`ORDERS_PG_DSN` from the environment. The report records real build/vet/test output including the `CGO_ENABLED=0` decision-stub build and the new test passing, and STATE.md's overstated wiring claim is corrected.

<details>
<summary>Issues (1)</summary>

1. **Migrations gate uses a single sentinel table** — `SchemaHasConfigTables` checks only `flows`; a schema left half-migrated (flows present, later tables missing) would skip `Apply` and not self-heal. Non-blocking for this increment since migrations are all-or-nothing in one `Apply` call, but worth a note if partial-migration states become possible.

</details>

<details>
<summary>Details</summary>

### Schema isolation via AfterConnect + CREATE SCHEMA before migrations

`OpenSchemaPool` (internal/config/connect_pg.go) sets `cfg.AfterConnect` to `SET search_path TO <schema>` and runs `CREATE SCHEMA IF NOT EXISTS <schema>` before `migrations.Apply`. The schema name reaches SQL only after `ValidateSchemaName` passes. Because `0001_init.up.sql` uses unqualified `CREATE TABLE flows (...)` etc. (confirmed across all nine tables), the search_path pins every table into `<schema>`. The integration test asserts `flows`, `flow_versions`, `jdm_versions`, `connection_versions`, `active_pointers`, and `audit_log` all land in `rule_engine` with a `public` count of 0 — this directly covers must-have 1.

`ValidateSchemaName` enforces `^[A-Za-z_][A-Za-z0-9_]*$` and its unit test rejects empty, `1x`, `a-b`, `a;b`, `public schema`, `a.b`, `drop table`, and quote-bearing variants, while accepting `rule_engine`, `_x`, `A1`.

### Transactional write methods, secrets kept out of the DB

`PutJDMVersion` and `PutConnectionVersion` (internal/config/pgstore.go) are full transactional methods built with `withTx`, mirroring `PutFlowVersion`. `PutConnectionVersion` marshals settings and resilience to jsonb and maps an empty `SecretRef` to SQL NULL — a secret *value* is never written, and the test asserts `connection_versions` has zero columns named `secret`/`password`/`secret_value`, plus `ship-rest.SecretRef == ""`. This covers must-have 2. The seed load in `SeedPgStore` and the branch in `buildStore` contain no raw SQL for these writes.

One asymmetry worth noting (not a defect): `PutJDMVersion` guards the version insert with `ON CONFLICT (jdm_id, version) DO NOTHING`, while `PutConnectionVersion` and `PutFlowVersion` do not add that clause on the version row. This is fine because version numbers are computed under a `FOR UPDATE` lock, so a collision cannot occur within normal operation; the extra clause on JDM is harmless given its auto-assign-or-honor-explicit-version path.

### Idempotent seeding: skip gate plus migrations existence gate

`SeedPgStore` resolves `ActiveFlow(env, firstFlow.Method, firstFlow.Path)`; a nil error means already-seeded → `(false, nil)`, a non-NotFound error surfaces (so a Postgres outage is not mistaken for "absent"), and NotFound proceeds with seeding. Active flows go `PutFlowVersion → MarkValidated → SetActive`, correctly handling the PgStore gate that `memStore` lacks. `buildStore` separately gates `migrations.Apply` on `SchemaHasConfigTables`. The test's second-seed assertions (`seeded==false`, one version per object) cover must-have 3.

The only observation: `SchemaHasConfigTables` keys off the single `flows` sentinel. If a prior run left the schema partially migrated, `Apply` would be skipped. Since `migrations.Apply` runs the init migration as one unit, this is not reachable in the current design, so it is a possible future concern rather than a current bug.

### Run-mode selection and the .env loader

`buildStore` returns the original `LoadSeed` memStore with a no-op cleanup when `CONFIG_DSN` is empty — the in-memory path is unchanged and no DB is touched, matching must-have 4. The config-store branch accumulates cleanups and tears them down LIFO (cache `Close` → valkey client `Close` → pool `Close`), and closes already-acquired resources if a later step fails. `envfile.Load` runs before any config env read, sets a key only when `os.LookupEnv` reports it unset, returns nil for a missing file, strips one pair of matching quotes, and errors on a line with no `=` (naming the line number). `ENGINE_ADDR` overrides the `-addr` default. The `-env` flag defaults to `.env`. The envfile unit tests cover missing file, comments/blanks/quotes, process-env-wins, malformed line, and parsing the committed `.env.example`.

`redactDSN` blanks the password via `net/url` before logging, and reduces an unparseable DSN to a safe marker rather than echoing it.

### Ephemeral-only integration test

`configrun_integration_test.go` carries `//go:build integration && cgo` and a file comment stating it uses only ephemeral containers and never reads `CONFIG_DSN`/`ORDERS_PG_DSN` from the environment. It reuses the proven `startPostgres`/`startValkey`/`newValkeyClient` harness, stands up a second ephemeral postgres for the orders data table, overrides the seed's `orders-pg` DSN and `ship-rest` baseURL at runtime (cloning settings maps so the stored config is not mutated), builds the real registry + decision engine + httpapi handler over the PgStore, and asserts `GET /orders/1 → expedited (/expedite)` and `GET /orders/2 → standard (/standard)` with exactly one matching REST hit each. This proves config resolves from the PgStore and covers must-haves 5 and 6(d).

### Recorded verification evidence

The report records real output for `CGO_ENABLED=1 go build ./...` (exit 0), `go vet ./...` (clean), `go test ./...` (envfile and config green, in-memory path intact), the `CGO_ENABLED=0 go build ./internal/decision` stub build (exit 0), the targeted new integration test (`--- PASS: TestConfigRunModeSeedsIntoSchema (11.05s)`), and the full `integration cgo` config + httpapi suites green. STATE.md was corrected: the earlier "Wiring join" claim that `cmd/engine` already switched PgStore-vs-seed behind `CONFIG_DSN` is explicitly retracted, and a "Config-store run mode — DONE" subsection documents this increment. This covers must-have 6. Per the step instructions I did not re-run the suites; the diff and recorded evidence are internally consistent with the code I read, and no specific doubt warranted a spot-check.

</details>

<details>
<summary>File map</summary>

- `internal/config/connect_pg.go` — `ValidateSchemaName`, `OpenSchemaPool` (AfterConnect search_path + CREATE SCHEMA), `SchemaHasConfigTables` migration gate.
- `internal/config/connect_pg_test.go` — unit tests for the schema-name validator.
- `internal/config/pgstore.go` — new `PutJDMVersion` + `PutConnectionVersion` transactional write methods; secret_ref only.
- `internal/config/seed_pg.go` — `SeedPgStore`: idempotent seed-through-the-store with the MarkValidated step.
- `internal/config/configrun_integration_test.go` — ephemeral-container integration test, assertions (a)–(d).
- `internal/envfile/envfile.go` + `_test.go` — dependency-free `.env` loader (process env wins).
- `cmd/engine/main.go` — `.env` load, `ENGINE_ADDR`/`-env`, `buildStore` run-mode selection, DSN redaction, LIFO cleanup.
- `.env.example` — documents CONFIG_* / VALKEY_ADDR / ENGINE_ADDR / data-plane keys; no secrets.
- `docs/STATE.md` — corrected the overstated wiring claim; added the config-store run-mode subsection.
- `docs/.agents/tasks/config-run/plan.md`, `docs/.increments/config-run-report.md` — plan + report.

Full diff: `git -C /home/nuzirwan/project/rule-engine-api/.worktrees/config-run diff mainline...feat/config-run-mode`

</details>
