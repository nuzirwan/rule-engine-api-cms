# Connectors Reference — connection types and configuration

The rule engine uses a connection registry to manage database, cache, HTTP, and
message queue connections. Each connection is defined with a type, settings, and
optional resilience policy. This guide covers all supported connector types.

**Related architecture:** [slice-b-connections.md](../architecture/lld/slice-b-connections.md), [connector-platform.md](../architecture/lld/connector-platform.md)

## Connection Structure

Every connection follows this shape:

```json
{
  "key": "pg-main",
  "type": "postgres",
  "settings": { /* driver-specific */ },
  "secretRef": "env:PG_PASSWORD",
  "resilience": {
    "timeout": "5s",
    "retry": {
      "maxAttempts": 3,
      "baseBackoff": "100ms",
      "maxBackoff": "2s"
    },
    "breaker": {
      "failureThreshold": 5,
      "failureRatio": 0.0,
      "openTimeout": "10s"
    }
  }
}
```

| Field      | Type   | Description                                        |
|------------|--------|----------------------------------------------------|
| `key`      | string | Unique identifier referenced by action nodes       |
| `type`     | string | Driver type: `postgres`, `mysql`, `valkey`, `rest`, `kafka` |
| `settings` | object | Driver-specific configuration                      |
| `secretRef`| string | Secret reference (e.g., `env:VAR_NAME`)            |
| `resilience` | object | Timeout, retry, and circuit breaker config       |

## Supported Connection Types

| Type       | Capabilities        | Lifecycle  |
|------------|---------------------|------------|
| `postgres` | query, exec         | pooled     |
| `mysql`    | query, exec         | pooled     |
| `valkey`   | get, set, del       | pooled     |
| `rest`/`http` | http             | ephemeral  |
| `kafka`    | publish, subscribe  | pooled     |

---

## Postgres Connector

Connects to PostgreSQL databases with connection pooling.

### DSN-Based Configuration

```json
{
  "key": "pg-main",
  "type": "postgres",
  "settings": {
    "dsn": "postgres://user:password@localhost:5432/mydb?sslmode=disable"
  }
}
```

### Discrete Configuration

```json
{
  "key": "pg-main",
  "type": "postgres",
  "settings": {
    "host": "localhost",
    "port": "5432",
    "database": "mydb",
    "user": "postgres",
    "sslmode": "disable",
    "pool": {
      "maxConns": 10,
      "minConns": 2
    }
  },
  "secretRef": "env:PG_PASSWORD"
}
```

| Setting          | Type   | Default | Description                    |
|------------------|--------|---------|--------------------------------|
| `dsn`            | string | -       | Full connection string         |
| `host`           | string | -       | Database host                  |
| `port`           | string | `5432`  | Database port                  |
| `database`       | string | -       | Database name                  |
| `user`           | string | -       | Username                       |
| `sslmode`        | string | `prefer`| SSL mode                       |
| `pool.maxConns`  | int    | 10      | Maximum pool connections       |
| `pool.minConns`  | int    | 2       | Minimum pool connections       |

### Query Operation

```json
{
  "id": "fetch-user",
  "type": "action",
  "spec": {
    "connection": "pg-main",
    "operation": {
      "kind": "query",
      "payload": {
        "sql": "SELECT id, name, email FROM users WHERE id = $1",
        "params": [{ "value": "{{input.user_id}}", "as": "int" }]
      }
    },
    "saveAs": "user",
    "unwrapSingleRow": true
  }
}
```

### Exec Operation

```json
{
  "id": "create-order",
  "type": "action",
  "spec": {
    "connection": "pg-main",
    "operation": {
      "kind": "exec",
      "payload": {
        "sql": "INSERT INTO orders (customer_id, total) VALUES ($1, $2) RETURNING id",
        "params": [
          { "value": "{{input.customer_id}}", "as": "int" },
          { "value": "{{input.total}}", "as": "numeric" }
        ]
      }
    },
    "saveAs": "newOrder"
  }
}
```

### Typed Parameters

Parameters can be bare strings (default text binding) or typed objects:

```json
{
  "params": [
    "{{input.name}}",
    { "value": "{{input.id}}", "as": "int" },
    { "value": "{{input.amount}}", "as": "numeric" },
    { "value": "{{input.active}}", "as": "bool" }
  ]
}
```

Supported `as` types: `text` (default), `int`, `numeric`, `bool`.

---

## MySQL Connector

Connects to MySQL databases with connection pooling. Configuration mirrors Postgres.

```json
{
  "key": "mysql-main",
  "type": "mysql",
  "settings": {
    "dsn": "user:password@tcp(localhost:3306)/mydb"
  }
}
```

Or discrete:

```json
{
  "key": "mysql-main",
  "type": "mysql",
  "settings": {
    "host": "localhost",
    "port": "3306",
    "database": "mydb",
    "user": "root"
  },
  "secretRef": "env:MYSQL_PASSWORD"
}
```

---

## Valkey Connector

Connects to Valkey (Redis-compatible) for caching and key-value operations.

### Configuration

```json
{
  "key": "cache",
  "type": "valkey",
  "settings": {
    "addr": "localhost:6379",
    "db": 0,
    "tls": false
  },
  "secretRef": "env:VALKEY_PASSWORD"
}
```

| Setting | Type   | Default | Description                |
|---------|--------|---------|----------------------------|
| `addr`  | string | -       | Valkey server address      |
| `db`    | int    | 0       | Database number            |
| `tls`   | bool   | false   | Enable TLS                 |

### Get Operation

```json
{
  "id": "get-cached",
  "type": "action",
  "spec": {
    "connection": "cache",
    "operation": {
      "kind": "get",
      "payload": {
        "key": "order:{{input.order_id}}"
      }
    },
    "saveAs": "cachedOrder"
  }
}
```

### Set Operation

```json
{
  "id": "cache-result",
  "type": "action",
  "spec": {
    "connection": "cache",
    "operation": {
      "kind": "set",
      "payload": {
        "key": "order:{{data.order.id}}",
        "value": "{{data.order}}",
        "ttl": 3600,
        "nx": true
      }
    }
  }
}
```

| Payload Field | Type   | Description                          |
|---------------|--------|--------------------------------------|
| `key`         | string | Cache key                            |
| `value`       | any    | Value to store                       |
| `ttl`         | int    | Time-to-live in seconds              |
| `nx`          | bool   | Only set if not exists               |

### Delete Operation

```json
{
  "id": "invalidate-cache",
  "type": "action",
  "spec": {
    "connection": "cache",
    "operation": {
      "kind": "del",
      "payload": {
        "key": "order:{{input.order_id}}"
      }
    }
  }
}
```

Or delete multiple keys:

```json
{
  "operation": {
    "kind": "del",
    "payload": {
      "keys": ["key1", "key2", "key3"]
    }
  }
}
```

---

## REST/HTTP Connector

Connects to external HTTP APIs.

### Configuration

```json
{
  "key": "payment-api",
  "type": "rest",
  "settings": {
    "baseURL": "https://api.payment.example.com",
    "headers": {
      "X-API-Key": "{{secret}}",
      "Content-Type": "application/json"
    },
    "timeout": "30s"
  },
  "secretRef": "env:PAYMENT_API_KEY"
}
```

| Setting     | Type            | Description                      |
|-------------|-----------------|----------------------------------|
| `baseURL`   | string          | Base URL for all requests        |
| `headers`   | map[string]string | Default headers                |
| `timeout`   | duration        | Request timeout                  |

### HTTP Operation

```json
{
  "id": "process-payment",
  "type": "action",
  "spec": {
    "connection": "payment-api",
    "operation": {
      "kind": "http",
      "payload": {
        "method": "POST",
        "path": "/v1/charges",
        "body": {
          "amount": "{{data.order.total}}",
          "currency": "USD",
          "source": "{{input.payment_token}}"
        },
        "headers": {
          "Idempotency-Key": "{{input.request_id}}"
        },
        "query": {
          "expand": "customer"
        }
      }
    },
    "saveAs": "paymentResult",
    "resilience": {
      "timeoutMs": 10000,
      "retries": 2
    }
  }
}
```

| Payload Field | Type            | Description                    |
|---------------|-----------------|--------------------------------|
| `method`      | string          | HTTP method                    |
| `path`        | string          | URL path (appended to baseURL) |
| `body`        | object          | Request body (JSON)            |
| `headers`     | map[string]string | Additional headers           |
| `query`       | map[string]string | Query parameters             |

---

## Kafka Connector

Connects to Kafka for message publishing and consumption.

### Configuration

```json
{
  "key": "kafka-events",
  "type": "kafka",
  "settings": {
    "brokers": ["kafka-1:9092", "kafka-2:9092"],
    "topic": "events",
    "groupId": "rule-engine-consumer",
    "sasl": {
      "mechanism": "PLAIN",
      "username": "admin"
    }
  },
  "secretRef": "env:KAFKA_PASSWORD"
}
```

| Setting          | Type     | Description                      |
|------------------|----------|----------------------------------|
| `brokers`        | []string | Kafka broker addresses           |
| `topic`          | string   | Default topic                    |
| `groupId`        | string   | Consumer group ID                |
| `sasl.mechanism` | string   | SASL mechanism (PLAIN, SCRAM)    |
| `sasl.username`  | string   | SASL username                    |

### Publish Operation

```json
{
  "id": "publish-event",
  "type": "action",
  "spec": {
    "connection": "kafka-events",
    "operation": {
      "kind": "publish",
      "payload": {
        "topic": "order-events",
        "key": "{{data.order.id}}",
        "value": {
          "type": "order.created",
          "order_id": "{{data.order.id}}",
          "timestamp": "{{now}}"
        },
        "headers": {
          "correlation-id": "{{input.request_id}}"
        }
      }
    }
  }
}
```

---

## Resilience Configuration

Every connection can specify resilience policies that wrap operations.

### Execution Order

```
timeout → circuit breaker → retry → actual operation
```

1. **Timeout** (outermost): bounds the entire operation including retries
2. **Circuit Breaker**: per-connection-key, short-circuits on sustained failure
3. **Retry**: exponential backoff for transient failures

### Full Resilience Config

```json
{
  "resilience": {
    "timeout": "5s",
    "retry": {
      "maxAttempts": 3,
      "baseBackoff": "100ms",
      "maxBackoff": "2s"
    },
    "breaker": {
      "failureThreshold": 5,
      "failureRatio": 0.0,
      "openTimeout": "10s"
    }
  }
}
```

| Setting                    | Default | Description                              |
|----------------------------|---------|------------------------------------------|
| `timeout`                  | -       | Overall operation timeout                |
| `retry.maxAttempts`        | 3       | Maximum retry attempts                   |
| `retry.baseBackoff`        | 100ms   | Initial backoff duration                 |
| `retry.maxBackoff`         | 2s      | Maximum backoff duration                 |
| `breaker.failureThreshold` | 5       | Failures before opening circuit          |
| `breaker.failureRatio`     | 0.0     | Failure ratio threshold (0 = count only) |
| `breaker.openTimeout`      | 10s     | Time before half-open attempt            |

### Per-Action Override

Action nodes can override connection-level resilience:

```json
{
  "spec": {
    "connection": "pg-main",
    "operation": { /* ... */ },
    "resilience": {
      "timeoutMs": 10000,
      "retries": 5,
      "breakerOff": true
    }
  }
}
```

---

## Secret References

Connections support secret references for credentials:

```json
{
  "secretRef": "env:DATABASE_PASSWORD"
}
```

The `env:` prefix reads from environment variables. The secret value is injected
into settings where `{{secret}}` appears.

---

## Best Practices

1. **Use connection pooling** — Postgres, MySQL, and Valkey connections are pooled; tune `maxConns` based on load.

2. **Set appropriate timeouts** — Match timeouts to expected operation duration; long-running queries need longer timeouts.

3. **Configure circuit breakers** — Prevent cascade failures by opening the circuit on sustained errors.

4. **Use typed parameters** — Declare SQL parameter types explicitly to avoid type coercion issues.

5. **Externalize secrets** — Use `secretRef` with environment variables; never hardcode credentials.

6. **Test with dry-run** — Validate connection operations using dry-run mode before deploying to production.
