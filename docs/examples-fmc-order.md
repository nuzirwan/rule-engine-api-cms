# FMC Order Demo — config-driven flows (no route code)

This is a short live-run guide for the two FMC order endpoints. The key point:
**these endpoints exist because of CONFIG rows in the seed/store, not because of
any Go route code.** The HTTP router resolves every request against the live
config store, so adding another API = adding another flow row — no code, no
redeploy.

```
GET /order/{order_id}        -> look up one order by its id
GET /order/msisdn/{msisdn}   -> look up the latest order for a subscriber msisdn
```

Both flows read a row from `fmc_order.order_status`, echo six fields, run the
SHARED `fmc-payment` ZEN decision table on `payment_status`, and set `action`
ONLY via a `decision -> set` node:

- `payment_status == 'PAID'`  → `action = "proceed_fulfillment"`
- anything else (fallthrough) → `action = "await_payment"`

## Environment

The engine reads three connection targets from the environment at startup. The
seed carries safe DEFAULT DSNs; the live run overrides them:

| Env var      | Points at                                        | Notes                                            |
|--------------|--------------------------------------------------|--------------------------------------------------|
| `CONFIG_DSN` | the config-store Postgres (schema `rule_engine`) | where flows/JDMs/connections live                |
| `FMC_PG_DSN` | the `fmc_utility` database (schema `fmc_order`)  | the DATA source the two flows query via `fmc-pg` |
| `VALKEY_ADDR`| the Valkey address                               | hot-config cache + invalidation                  |

The `fmc-pg` connection's default DSN in the seed is
`postgres://root:root@127.0.0.1:5432/fmc_utility?sslmode=disable`; set
`FMC_PG_DSN` to override it for the live run. The config store itself is a
SEPARATE, unrelated database (schema `rule_engine`).

## Run it

```sh
export CONFIG_DSN='postgres://.../rule_engine?sslmode=disable'
export FMC_PG_DSN='postgres://.../fmc_utility?sslmode=disable'
export VALKEY_ADDR='127.0.0.1:6379'

# start the engine (or run the built binary)
go run ./cmd/engine
```

Then call the endpoints:

```sh
# PAID order -> proceed_fulfillment
curl localhost:8080/order/ORD-TEST-TRK

# PENDING order -> await_payment
curl localhost:8080/order/ORD-TEST-BAD

# latest row for a subscriber msisdn (ORDER BY last_updated DESC LIMIT 1)
curl localhost:8080/order/msisdn/628111015450
```

A PAID order responds like:

```json
{
  "order_id": "ORD-TEST-TRK",
  "msisdn": "628000000001",
  "order_status": "confirmed",
  "payment_status": "PAID",
  "payment_amount": 199.90,
  "product_name": "Fiber 100Mbps",
  "action": "proceed_fulfillment"
}
```

## Ops endpoints (same mux)

```sh
curl localhost:8080/livez     # 200 "ok"
curl localhost:8080/readyz    # 200 {"status":"ready"} when the registry + config store are healthy
curl localhost:8080/metrics   # Prometheus exposition (nzr_flow_* / nzr_connection_*)
```

## Config, not code

Nothing in `internal/httpapi` registers `/order/...`. The router registers a
single catch-all and resolves each request against the active flow rows in the
store on EVERY request. Publishing a new flow row makes its endpoint live on the
next request with no restart; deactivating one yields a 404. Adding a third API
is a third flow row, not a code change.

## Declaring SQL param types in config (typed params)

A flow's SQL binds positional params (`$1`, `$2`, …) from the `params` array of
an action's `operation.Payload`. **The bind type is declared in config, never
guessed from the value's shape.** A path segment is lifted into the request
input as a plain string; the param declaration — not the string looking numeric
— decides how it is bound to Postgres.

A `params` element is EITHER:

- a **bare string** (default binding = `text`):

  ```json
  "params": ["{{input.msisdn}}"]
  ```

- or a **typed object** `{ "value": <template>, "as": <type> }`:

  ```json
  "params": [{ "value": "{{input.id}}", "as": "int" }]
  ```

Supported `as` types (v1): `text` (default), `int`, `numeric`, `bool`. An
unknown `as`, or a malformed param object, is rejected at flow
validation/publish time (not at request time). At runtime the engine resolves
the template to a string and converts it to the declared Go type before pgx
encodes it; a value that does not fit the declared type (e.g. `"as":"int"` over
`"abc"`) is a `400` Validation error naming the param — never a panic and never
a silent coercion.

Why this matters for the FMC flows: `fmc_order.order_status.order_id` and
`.msisdn` are BOTH `text` columns, so both flows use the default bare-string
binding with a plain `WHERE col = $1` and NO cast — a digit-only msisdn like
`628111015450` is bound as text and matches correctly. A flow that queries a
genuinely integer column instead declares `"as":"int"` on that param (as the
kept `orders-expedite` demo does for its `orders.id` column), so the SQL stays
clean (`WHERE id = $1`) and the parameter — not the column — carries the type.
