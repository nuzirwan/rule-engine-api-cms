# Implementation Plan: MySQL Connector

Add a MySQL database connector that mirrors the provider-agnostic pattern established by `postgres.go`. The MySQL connector implements the same `connect.Connector` interface and follows identical structure, demonstrating that the database abstraction is genuinely agnostic.

## Prerequisites

The `go-sql-driver/mysql` package is NOT in `go.mod` — it must be added with:
```bash
cd /home/nuzirwan/project/rule-engine-api/.worktrees/mysql-connector/engine
go get github.com/go-sql-driver/mysql
```

## Reference Implementation

**postgres.go patterns to mirror:**
- `pgConnector` struct (empty, stateless factory)
- `newPGConnector()` returns `connect.Connector` (not a concrete type)
- `Type()` returns the type string
- `Lifecycle()` returns `connect.LifecyclePooled`
- `Capabilities()` returns `connect.CapQueryExec`
- `Open()` builds DSN, parses config, injects secret as password, applies pool settings, returns `connect.Client`
- `pgClient` wraps `*pgxpool.Pool` with key field
- `Execute()` dispatches on `op.Kind`: query/exec/ping
- `query()` uses `sqlAndParams`, validates no mutation, returns `[]map[string]any`
- `exec()` uses `sqlAndParams`, validates no select, returns `{rowsAffected}`
- `Close()` releases the pool
- `classify()` maps driver errors to ConnError taxonomy

**Key differences for MySQL:**
- Uses `database/sql` + `github.com/go-sql-driver/mysql` instead of pgx
- Placeholder syntax: `?` positional (no conversion needed — the helper returns `[]any`)
- Pool settings: `SetMaxOpenConns`, `SetMaxIdleConns`, `SetConnMaxLifetime` on `*sql.DB`
- Error classification uses MySQL error numbers (1062 duplicate, 1045 access denied, etc.)
- DSN format: `user:password@tcp(host:port)/database?parseTime=true`

---

## Implementation Steps

- [ ] 1. **Add the MySQL driver dependency**
      Run `go get github.com/go-sql-driver/mysql` from the engine directory to add the driver to go.mod.
      Files: `go.mod`, `go.sum`
      Verify: `go mod tidy && go build ./...` — no errors, mysql driver in go.mod

- [ ] 2. **Create mysql.go with mysqlConnector and mysqlClient**
      Create `/home/nuzirwan/project/rule-engine-api/.worktrees/mysql-connector/engine/internal/connect/drivers/mysql.go` with:
      
      **mysqlConnector struct** (empty, stateless):
      - `newMySQLConnector() connect.Connector` — returns `mysqlConnector{}`
      - `Type() string` — returns `"mysql"`
      - `Lifecycle() connect.Lifecycle` — returns `connect.LifecyclePooled`
      - `Capabilities() connect.Capability` — returns `connect.CapQueryExec`
      - `Open(ctx, def) (connect.Client, error)`:
        1. Call `buildMySQLDSN(def)` to assemble DSN (without password)
        2. Call `sql.Open("mysql", dsn)` to create `*sql.DB`
        3. If secret present via `connect.SecretFrom(ctx)`, rebuild DSN with password injected
        4. Apply pool settings: `db.SetMaxOpenConns`, `db.SetMaxIdleConns`, `db.SetConnMaxLifetime`
        5. Call `db.PingContext(ctx)` to verify connectivity
        6. Return `&mysqlClient{key: def.Key, db: db}`
      
      **buildMySQLDSN(def ConnectionDef) string**:
      - If `dsn` setting exists, use it directly
      - Otherwise compose: `user@tcp(host:port)/database?parseTime=true`
      - Host defaults to `localhost`, port to `3306`, user to `root`
      - Password is NOT in DSN — injected separately after secret resolution
      
      **applyMySQLPoolSettings(db *sql.DB, settings map[string]any)**:
      - Read `maxOpenConns` from settings, call `db.SetMaxOpenConns(v)`
      - Read `maxIdleConns` from settings, call `db.SetMaxIdleConns(v)`
      - Read `connMaxLifetime` as string (e.g., "1h"), parse with `time.ParseDuration`, call `db.SetConnMaxLifetime(d)`
      
      **mysqlClient struct**:
      - Fields: `key string`, `db *sql.DB`
      
      **mysqlClient.Execute(ctx, op) (any, error)**:
      - Switch on `op.Kind`:
        - `"query"`: call `c.query(ctx, op)`
        - `"exec"`: call `c.exec(ctx, op)`
        - `"ping"`: call `c.db.PingContext(ctx)`, return `{"ok": true}` or classified error
        - default: return Validation error "unsupported operation kind for mysql"
      
      **mysqlClient.query(ctx, op) (any, error)**:
      - Call `sqlAndParams(c.key, "query", op.Payload)` to extract SQL and params
      - If `isMutation(sql)`, return Validation error
      - Call `c.db.QueryContext(ctx, sql, params...)`
      - Iterate rows, read column names via `rows.Columns()`
      - Build `[]map[string]any` result — use `rows.Scan` with `[]any` slice matching column count
      - Handle NULL values appropriately (scan into `*any` or `sql.RawBytes`)
      - Return result slice
      
      **mysqlClient.exec(ctx, op) (any, error)**:
      - Call `sqlAndParams(c.key, "exec", op.Payload)` to extract SQL and params
      - If `isSelect(sql)`, return Validation error
      - Call `c.db.ExecContext(ctx, sql, params...)`
      - Get `result.RowsAffected()`
      - Return `map[string]any{"rowsAffected": n}`
      
      **mysqlClient.Close() error**:
      - Return `c.db.Close()`
      
      **mysqlClient.classify(opKind string, err error) error**:
      - If `err == nil`, return nil
      - If `err == sql.ErrNoRows`, return NotFound
      - If `errors.Is(err, context.DeadlineExceeded)` or `context.Canceled`, return Timeout
      - Check for `*mysql.MySQLError`:
        - Error 1062 (duplicate entry) → Validation
        - Error 1045 (access denied) → Validation
        - Error 1146 (table doesn't exist) → NotFound
        - Error 1054 (unknown column) → Validation
        - Error 1064 (syntax error) → Validation
        - Other → Upstream
      - Check for `net.Error` → Upstream (network error)
      - Default → Upstream
      
      Files: `/home/nuzirwan/project/rule-engine-api/.worktrees/mysql-connector/engine/internal/connect/drivers/mysql.go`
      Verify: `go build ./...` — compiles without errors

- [ ] 3. **Register the MySQL connector in registry.go**
      Add `newMySQLConnector()` to the `All()` function return slice.
      
      Change:
      ```go
      return []connect.Connector{
          newPGConnector(),
          newValkeyConnector(),
          rest,
          restAlias,
          newKafkaConnector(),
          newRabbitMQConnector(),
      }
      ```
      To:
      ```go
      return []connect.Connector{
          newPGConnector(),
          newMySQLConnector(),  // <-- add this line
          newValkeyConnector(),
          rest,
          restAlias,
          newKafkaConnector(),
          newRabbitMQConnector(),
      }
      ```
      
      Files: `/home/nuzirwan/project/rule-engine-api/.worktrees/mysql-connector/engine/internal/connect/drivers/registry.go`
      Verify: `go build ./...` — compiles without errors

- [ ] 4. **Create mysql_test.go with unit tests**
      Create `/home/nuzirwan/project/rule-engine-api/.worktrees/mysql-connector/engine/internal/connect/drivers/mysql_test.go`:
      
      ```go
      package drivers
      
      import (
          "testing"
          
          "nzr-rules-engine/internal/connect"
      )
      
      // Compile-time interface compliance assertion
      var _ connect.Connector = mysqlConnector{}
      var _ connect.Client = (*mysqlClient)(nil)
      
      func TestMySQLConnectorType(t *testing.T) {
          c := newMySQLConnector()
          if got := c.Type(); got != "mysql" {
              t.Errorf("Type() = %q; want %q", got, "mysql")
          }
      }
      
      func TestMySQLConnectorLifecycle(t *testing.T) {
          c := newMySQLConnector()
          if got := c.Lifecycle(); got != connect.LifecyclePooled {
              t.Errorf("Lifecycle() = %v; want LifecyclePooled", got)
          }
      }
      
      func TestMySQLConnectorCapabilities(t *testing.T) {
          c := newMySQLConnector()
          if got := c.Capabilities(); got != connect.CapQueryExec {
              t.Errorf("Capabilities() = %v; want CapQueryExec", got)
          }
      }
      
      func TestBuildMySQLDSN(t *testing.T) {
          tests := []struct {
              name     string
              settings map[string]any
              wantDSN  string // substring check, since password not included
          }{
              {
                  name:     "explicit dsn passthrough",
                  settings: map[string]any{"dsn": "custom:pass@tcp(db:3306)/mydb"},
                  wantDSN:  "custom:pass@tcp(db:3306)/mydb",
              },
              {
                  name:     "discrete settings compose",
                  settings: map[string]any{"host": "dbhost", "port": "3307", "database": "testdb", "user": "admin"},
                  wantDSN:  "admin@tcp(dbhost:3307)/testdb",
              },
              {
                  name:     "defaults applied",
                  settings: map[string]any{"database": "app"},
                  wantDSN:  "root@tcp(localhost:3306)/app",
              },
          }
          for _, tt := range tests {
              t.Run(tt.name, func(t *testing.T) {
                  def := connect.ConnectionDef{Key: "test", Type: "mysql", Settings: tt.settings}
                  got := buildMySQLDSN(def)
                  if got != tt.wantDSN && !strings.Contains(got, tt.wantDSN) {
                      t.Errorf("buildMySQLDSN() = %q; want %q", got, tt.wantDSN)
                  }
              })
          }
      }
      ```
      
      Files: `/home/nuzirwan/project/rule-engine-api/.worktrees/mysql-connector/engine/internal/connect/drivers/mysql_test.go`
      Verify: `go test ./internal/connect/drivers/...` — all tests pass

- [ ] 5. **Run full verification**
      Execute build and test commands to confirm everything integrates correctly.
      
      Files: none (verification only)
      Verify:
      - `cd /home/nuzirwan/project/rule-engine-api/.worktrees/mysql-connector/engine && go build ./...` — builds successfully
      - `cd /home/nuzirwan/project/rule-engine-api/.worktrees/mysql-connector/engine && go test ./internal/connect/...` — all tests pass

---

## DSN Configuration Format

The MySQL connector accepts these settings in `ConnectionDef.Settings`:

```json
{
  "type": "mysql",
  "dsn": "user:password@tcp(host:3306)/database?parseTime=true",
  "maxOpenConns": 10,
  "maxIdleConns": 5,
  "connMaxLifetime": "1h"
}
```

Or discrete settings (password via SecretRef):
```json
{
  "type": "mysql",
  "host": "localhost",
  "port": "3306",
  "database": "mydb",
  "user": "app",
  "maxOpenConns": 25,
  "maxIdleConns": 10,
  "connMaxLifetime": "30m"
}
```

## Verification Commands

From `/home/nuzirwan/project/rule-engine-api/.worktrees/mysql-connector/engine`:

```bash
# Build all packages
go build ./...

# Run connect package tests (including new mysql tests)
go test ./internal/connect/...

# Run specific mysql driver tests
go test -v ./internal/connect/drivers/... -run MySQL
```

---

## Verification Results (Implementation Complete)

**Date:** Implementation session

### Commands Executed

1. **Build all packages:**
   ```bash
   cd /home/nuzirwan/project/rule-engine-api/.worktrees/mysql-connector/engine && go build ./...
   ```
   **Result:** Exit code 0 (success, no errors)

2. **Run connect package tests:**
   ```bash
   cd /home/nuzirwan/project/rule-engine-api/.worktrees/mysql-connector/engine && go test ./internal/connect/...
   ```
   **Result:**
   ```
   ok  	nzr-rules-engine/internal/connect	1.530s
   ok  	nzr-rules-engine/internal/connect/drivers	0.020s
   ```
   Exit code 0 (all tests pass)

3. **Run MySQL-specific tests (verbose):**
   ```bash
   cd /home/nuzirwan/project/rule-engine-api/.worktrees/mysql-connector/engine && go test -v ./internal/connect/drivers/... -run MySQL
   ```
   **Result:**
   ```
   === RUN   TestMySQLConnectorType
   --- PASS: TestMySQLConnectorType (0.00s)
   === RUN   TestMySQLConnectorLifecycle
   --- PASS: TestMySQLConnectorLifecycle (0.00s)
   === RUN   TestMySQLConnectorCapabilities
   --- PASS: TestMySQLConnectorCapabilities (0.00s)
   === RUN   TestBuildMySQLDSN
   === RUN   TestBuildMySQLDSN/explicit_dsn_passthrough
   === RUN   TestBuildMySQLDSN/discrete_settings_compose
   === RUN   TestBuildMySQLDSN/defaults_applied
   === RUN   TestBuildMySQLDSN/host_only
   === RUN   TestBuildMySQLDSN/port_only
   === RUN   TestBuildMySQLDSN/user_only
   === RUN   TestBuildMySQLDSN/empty_settings_uses_defaults
   --- PASS: TestBuildMySQLDSN (0.00s)
   === RUN   TestInjectMySQLPassword
   === RUN   TestInjectMySQLPassword/inject_into_simple_dsn
   === RUN   TestInjectMySQLPassword/inject_into_dsn_with_options
   === RUN   TestInjectMySQLPassword/dsn_without_@_unchanged
   --- PASS: TestInjectMySQLPassword (0.00s)
   === RUN   TestMySQLConnectorInAll
   --- PASS: TestMySQLConnectorInAll (0.00s)
   PASS
   ok  	nzr-rules-engine/internal/connect/drivers	0.021s
   ```
   Exit code 0 (all MySQL tests pass)

### Deliverables Confirmed

- [x] `mysql.go` created with `mysqlConnector` implementing `connect.Connector`
- [x] `Type()` returns `"mysql"`
- [x] `Lifecycle()` returns `connect.LifecyclePooled`
- [x] `Capabilities()` returns `connect.CapQueryExec`
- [x] `Open()` builds DSN, injects secret password, applies pool settings
- [x] `mysqlClient` implements `connect.Client` with `Execute()` and `Close()`
- [x] `Execute()` dispatches `query`, `exec`, `ping` operations
- [x] `classify()` maps errors to `NotFound`, `Timeout`, `Validation`, `Upstream`
- [x] Registry updated: `newMySQLConnector()` in `All()` slice
- [x] Test file with compile-time assertions and table-driven tests
- [x] `drivers.All()` includes connector with `Type() == "mysql"` (verified by test)
