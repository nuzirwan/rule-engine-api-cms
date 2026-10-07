# Implementation Plan: Engine-CMS Capability Sync

This plan addresses the 8 concrete changes from the brief, organized into 3 commits matching the feature boundaries.

## Verified Engine Field Names

From engine source investigation:

### MySQL (mysql.go buildMySQLDSN + applyMySQLPoolSettings)
- `host` (string, default "localhost")
- `port` (string, default "3306") 
- `database` (string)
- `user` (string, default "root")
- `maxOpenConns` (int)
- `maxIdleConns` (int)
- `connMaxLifetime` (string, duration format)

### Kafka (kafka.go kafkaBrokers + Open)
- `brokers` ([]string or comma-separated string)
- `topic` (string, required)
- `groupID` (string, required for subscription)
- `partition` (int, default 0)
- `dlqTopic` (string, default "{topic}-dlq")
- `maxRetries` (int, default 3)

### RabbitMQ (rabbitmq.go Open)
- `url` (string, required - AMQP URL format)
- `queue` (string)
- `exchange` (string)
- `routingKey` (string)
- `prefetch` (int, default 10)
- `maxRetries` (int, default 3)

### Resilience (resilience.go ResiliencePolicy struct + merge logic)
- `Retry.MaxAttempts` (int, default 3)
- `Retry.BaseBackoff` (time.Duration, default 20ms)
- `Retry.MaxBackoff` (time.Duration, default 2s)
- `Breaker.FailureThreshold` (uint32, default 5)
- `Breaker.FailureRatio` (float64, optional - trips when ratio >= this)
- `Breaker.OpenTimeout` (time.Duration, default 10s)

---

## Commit 1: feat(cms): add mysql/kafka/rabbitmq/http connector types

- [ ] 1. Create `/home/nuzirwan/project/rule-engine-api/.worktrees/engine-cms-sync/cms/src/components/connection/mysql-settings.json`
      Following postgres-settings.json pattern. Fields: host (string), port (integer, default 3306), database (string), user (string), maxOpenConns (integer), maxIdleConns (integer), connMaxLifetime (string).
      Verify: `cat file | jq .` parses without error

- [ ] 2. Create `/home/nuzirwan/project/rule-engine-api/.worktrees/engine-cms-sync/cms/src/components/connection/kafka-settings.json`
      Fields: brokers (string), topic (string), groupID (string), partition (integer), dlqTopic (string), maxRetries (integer).
      Verify: `cat file | jq .` parses without error

- [ ] 3. Create `/home/nuzirwan/project/rule-engine-api/.worktrees/engine-cms-sync/cms/src/components/connection/rabbitmq-settings.json`
      Fields: url (string), queue (string), exchange (string), routingKey (string), prefetch (integer, default 10), maxRetries (integer).
      Verify: `cat file | jq .` parses without error

- [ ] 4. Update `/home/nuzirwan/project/rule-engine-api/.worktrees/engine-cms-sync/cms/src/api/connection/content-types/connection/schema.json`
      Add `mysql`, `http`, `kafka`, `rabbitmq` to the type enum. Current: `["postgres", "valkey", "rest"]` → New: `["postgres", "valkey", "rest", "mysql", "http", "kafka", "rabbitmq"]`
      Files: cms/src/api/connection/content-types/connection/schema.json
      Verify: JSON parses; enum has 7 values

- [ ] 5. Update the same connection schema.json dynamiczone
      Add to settings.components array: `"connection.mysql-settings"`, `"connection.kafka-settings"`, `"connection.rabbitmq-settings"`
      Current: `["connection.postgres-settings", "connection.rest-settings", "connection.valkey-settings"]`
      New: adds the 3 new components (6 total)
      Verify: `cd cms && npm run build` completes without errors

- [ ] 6. Commit: `git add . && git commit -m "feat(cms): add mysql/kafka/rabbitmq/http connector types"`

---

## Commit 2: feat(cms): add missing flow node types to palette

- [ ] 7. Update `/home/nuzirwan/project/rule-engine-api/.worktrees/engine-cms-sync/cms/src/plugins/rule-engine/admin/src/components/FlowCanvasField/index.tsx`
      Modify NODE_PALETTE array:
      Current (11 items): `['trigger', 'action', 'condition', 'switch', 'sequence', 'parallel', 'forEach', 'decision', 'set', 'logger', 'response']`
      New (16 items): Add `'messageTrigger'` after trigger, add `'filter', 'find', 'map', 'reduce'` after response.
      Final: `['trigger', 'messageTrigger', 'action', 'condition', 'switch', 'sequence', 'parallel', 'forEach', 'decision', 'set', 'logger', 'response', 'filter', 'find', 'map', 'reduce']`
      Verify: `cd cms && npm run build` completes without TypeScript errors

- [ ] 8. Commit: `git add . && git commit -m "feat(cms): add missing flow node types to palette"`

---

## Commit 3: feat(cms): add circuit breaker and backoff to resilience policy

- [ ] 9. Update `/home/nuzirwan/project/rule-engine-api/.worktrees/engine-cms-sync/cms/src/components/connection/retry-policy.json`
      Add to attributes: `baseBackoffMs` (integer, min 0), `maxBackoffMs` (integer, min 0)
      These map to engine Retry.BaseBackoff/MaxBackoff in ms (transform layer converts).
      Verify: JSON parses

- [ ] 10. Create `/home/nuzirwan/project/rule-engine-api/.worktrees/engine-cms-sync/cms/src/components/connection/breaker-policy.json`
       collectionName: `components_connection_breaker_policies`, displayName: `breaker-policy`
       Attributes: `failureThreshold` (integer, min 1), `failureRatio` (float, min 0, max 1), `openTimeoutMs` (integer, min 1000)
       Verify: JSON parses

- [ ] 11. Update `/home/nuzirwan/project/rule-engine-api/.worktrees/engine-cms-sync/cms/src/components/connection/resilience-policy.json`
       Add to attributes: `"breaker": { "type": "component", "repeatable": false, "component": "connection.breaker-policy" }`
       Verify: `cd cms && npm run build` completes without errors

- [ ] 12. Commit: `git add . && git commit -m "feat(cms): add circuit breaker and backoff to resilience policy"`

---

## Admin Client Check (Item 8 from brief)

Reviewed `/home/nuzirwan/project/rule-engine-api/.worktrees/engine-cms-sync/cms/src/plugins/rule-engine/server/src/services/admin-client.ts`:

- `createConnection()` spreads the connection object into the request body with `env` prepended
- The settings component values flow through as-is (dynamiczone values are serialized by Strapi)
- No transformation specific to settings component types
- The new settings components (mysql/kafka/rabbitmq) will publish correctly — the admin client is type-agnostic for the settings payload

**Conclusion**: No admin-client changes required. The new connector types will serialize correctly.
