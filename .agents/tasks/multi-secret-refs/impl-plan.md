# Implementation Plan: Multi-Secret References for Connections (TASK-009)

Based on the approved design at `/home/nuzirwan/project/rule-engine-api/.agents/tasks/multi-secret-refs/design.md`.

## Overview

This plan implements multi-secret support per the design:
1. Engine data model + context helpers
2. SecretSchemaProvider interface + connector implementations  
3. Registry resolution logic + schema endpoint
4. Test endpoint update
5. CMS schema change
6. CMS admin UI

All paths are absolute under `/home/nuzirwan/project/rule-engine-api/.worktrees/multi-secret-refs`.

---

## Phase 1: Engine Data Model + Context Helpers

- [ ] 1. Add `SecretRefs map[string]string` to ConnectionDef struct.
      Files: `engine/internal/connect/connect.go`
      Verify: `cd engine && go build ./...` — compiles.

- [ ] 2. Add WithSecrets/SecretsFrom context helpers following the existing WithSecret/SecretFrom pattern.
      Add `secretsCtxKey struct{}`, `WithSecrets(ctx, map[string]Secret)`, `SecretsFrom(ctx) (map[string]Secret, bool)`.
      Files: `engine/internal/connect/connector.go`
      Verify: `cd engine && go build ./... && go test ./internal/connect/...` — tests pass.

- [ ] 3. Update registry.open() to resolve SecretRefs map with fallback to legacy SecretRef.
      When SecretRefs is non-empty, iterate and resolve each ref, call WithSecrets. When only SecretRef is set, treat as {"password": ref}. Always call WithSecret for password key for backward compat.
      Files: `engine/internal/connect/registry.go`
      Verify: `cd engine && go test ./internal/connect/...` — tests pass.

- [ ] 4. Apply identical multi-secret resolution logic in pool.open().
      Files: `engine/internal/connect/pool.go`
      Verify: `cd engine && go test ./internal/connect/...` — tests pass.

---

## Phase 2: SecretSchemaProvider Interface + Connector Implementations

- [ ] 5. Add SecretField struct, SecretSchemaProvider interface, ConnectorRegistry interface.
      Files: `engine/internal/connect/connector.go`
      Verify: `cd engine && go build ./...` — compiles.

- [ ] 6. Add AllConnectors() method to registry and registryV2 with compile-time assertions.
      Files: `engine/internal/connect/registry.go`, `engine/internal/connect/registry_v2.go`
      Verify: `cd engine && go build ./...` — compiles.

- [ ] 7. Update postgres connector: add SecretSchema(), update Open() to prefer SecretsFrom.
      Files: `engine/internal/connect/drivers/postgres.go`
      Verify: `cd engine && go test ./internal/connect/drivers/...` — tests pass.

- [ ] 8. Update mysql connector: add SecretSchema(), update Open() to prefer SecretsFrom.
      Files: `engine/internal/connect/drivers/mysql.go`
      Verify: `cd engine && go test ./internal/connect/drivers/...` — tests pass.

- [ ] 9. Update valkey connector: add SecretSchema(), update Open() to prefer SecretsFrom.
      Files: `engine/internal/connect/drivers/valkey.go`
      Verify: `cd engine && go test ./internal/connect/drivers/...` — tests pass.

- [ ] 10. Update kafka connector: add SecretSchema(), add SASL auth support in Open() reading username/password from SecretsFrom when saslMechanism setting is present.
       Files: `engine/internal/connect/drivers/kafka.go`
       Verify: `cd engine && go test ./internal/connect/drivers/...` — tests pass.

- [ ] 11. Update rabbitmq connector: add SecretSchema(), implement discrete credential support in Open() alongside existing URL path.
       Files: `engine/internal/connect/drivers/rabbitmq.go`
       Verify: `cd engine && go test ./internal/connect/drivers/...` — tests pass.

- [ ] 12. Update rest connector: add SecretSchema() returning nil, update Open() to inject resolved secrets as HTTP headers.
       Files: `engine/internal/connect/drivers/rest.go`
       Verify: `cd engine && go test ./internal/connect/drivers/...` — tests pass.

---

## Phase 3: Schema Endpoint

- [ ] 13. Add GET /admin/connectors/schema endpoint and optional GET /admin/connectors/{type}/schema.
       Add connectorSchemaResponse, connectorSchema types. Add listConnectorSchemas and getConnectorSchema handlers.
       Files: `engine/internal/httpapi/admin_handlers.go`
       Verify: `cd engine && go build ./...` — compiles.

- [ ] 14. Register schema routes in admin.mount().
       Files: `engine/internal/httpapi/admin.go`
       Verify: `cd engine && go test ./internal/httpapi/...` — tests pass.

---

## Phase 4: Test Endpoint Update

- [ ] 15. Update testConnectionRequest to accept secrets map, update handler to convert and use WithSecrets.
       Files: `engine/internal/httpapi/admin_handlers.go`
       Verify: `cd engine && go test ./internal/httpapi/...` — tests pass.

- [ ] 16. Update createConnectionRequest to include SecretRefs, update handler and list/get endpoints.
       Files: `engine/internal/httpapi/admin_handlers.go`
       Verify: `cd engine && go test ./...` — all engine tests pass.

---

## Phase 5: CMS Schema Change

- [ ] 17. Add secretRefs JSON field to connection content-type schema.
       Files: `cms/src/api/connection/content-types/connection/schema.json`
       Verify: `cd cms && npx tsc --noEmit` — type-checks.

- [ ] 18. Add validateSecretRefs to lifecycles validation hooks.
       Files: `cms/src/api/connection/content-types/connection/lifecycles.ts`
       Verify: `cd cms && npx tsc --noEmit` — type-checks.

- [ ] 19. Update TypeScript types for multi-secret support.
       Files: `cms/types/engine.ts`
       Verify: `cd cms && npx tsc --noEmit` — type-checks.

- [ ] 20. Update publish transform to include secretRefs in engine payload.
       Files: `cms/src/plugins/rule-engine/server/src/services/publish-transform.ts`
       Verify: `cd cms && npx tsc --noEmit && npm run test -- --run` — tests pass.

- [ ] 21. Update admin-client to add getConnectorSchemas() and update testConnection to accept secrets map.
       Files: `cms/src/plugins/rule-engine/server/src/services/admin-client.ts`
       Verify: `cd cms && npx tsc --noEmit` — type-checks.

---

## Phase 6: CMS Admin UI

- [ ] 22. Create KeyValueRepeater component for REST's dynamic secret refs.
       Files: `cms/src/plugins/rule-engine/admin/src/components/KeyValueRepeater/index.tsx`
       Verify: `cd cms && npx tsc --noEmit` — type-checks.

- [ ] 23. Create SecretRefEditor component that fetches schema and renders appropriate editor.
       Files: `cms/src/plugins/rule-engine/admin/src/components/SecretRefEditor/index.tsx`
       Verify: `cd cms && npx tsc --noEmit` — type-checks.

- [ ] 24. Update TestConnectionModal to show multiple secret inputs based on connector schema.
       Files: `cms/src/plugins/rule-engine/admin/src/components/TestConnectionModal/index.tsx`
       Verify: `cd cms && npx tsc --noEmit` — type-checks.

- [ ] 25. Update ConnectionsPage to show secretRefs count and pass secretRefs to modal.
       Files: `cms/src/plugins/rule-engine/admin/src/pages/ConnectionsPage.tsx`
       Verify: `cd cms && npx tsc --noEmit && npm run test -- --run` — all CMS tests pass.

---

## Final Verification

- [ ] 26. Run full engine test suite: `cd engine && go test ./...`
- [ ] 27. Run full CMS test suite: `cd cms && npm run test -- --run`
