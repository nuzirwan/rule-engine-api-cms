# Implementation Plan: Test Connection Feature

This plan adds a "Test Connection" feature that lets users verify connection settings work before publishing. The user enters the actual password in a modal (NOT stored anywhere), and the system tests connectivity live.

## Architecture Overview

**Security Design:**
- CMS stores `secretRef` (e.g. `env:PG_PASSWORD`) — a reference string, NOT the actual secret
- The engine resolves secrets at runtime via SecretProvider
- Connectors receive resolved secrets via `connect.WithSecret` context
- **Passwords are NEVER stored in CMS by design**
- Test Connection password is used only for the one test call and discarded

**Component Flow:**
1. User opens Test Connection modal in CMS admin UI
2. User enters password (masked input)
3. CMS frontend calls `/rule-engine/connections/test`
4. CMS plugin controller proxies to engine's `/admin/connections/test`
5. Engine builds temporary ConnectionDef, injects secret via context, opens connector, pings, closes
6. Result flows back through the chain

---

## Part 1: Engine Endpoint

- [ ] 1. Add `NewPlainSecret(value string) Secret` function to `engine/internal/connect/connector.go`.
      Creates a Secret from a plaintext string for the test endpoint (production uses SecretProvider).
      Files: engine/internal/connect/connector.go
      Verify: `cd /home/nuzirwan/project/rule-engine-api/.worktrees/test-connection/engine && go build ./...`

- [ ] 2. Add `Connector(typ string) (Connector, bool)` method to registry and update Registry interface.
      Allows looking up a connector by type for the test endpoint without going through the full client pool.
      Files: engine/internal/connect/registry.go, engine/internal/connect/seam.go (if interface is there)
      Verify: `cd /home/nuzirwan/project/rule-engine-api/.worktrees/test-connection/engine && go build ./...`

- [ ] 3. Add testConnection handler in admin_handlers.go.
      Request: `{ type, settings, secret }`. Response: `{ success, message?, error? }`.
      Implementation: look up connector by type, build ConnectionDef, inject secret via WithSecret,
      call Open(), ping, close, return result.
      Files: engine/internal/httpapi/admin_handlers.go
      Verify: `cd /home/nuzirwan/project/rule-engine-api/.worktrees/test-connection/engine && go build ./...`

- [ ] 4. Register route `POST /admin/connections/test` in admin.go mount().
      Files: engine/internal/httpapi/admin.go
      Verify: `cd /home/nuzirwan/project/rule-engine-api/.worktrees/test-connection/engine && go build ./...`

- [ ] 5. Add tests for testConnection endpoint.
      Test cases: missing type -> 400, unknown type -> 400, successful ping, failed ping.
      Files: engine/internal/httpapi/admin_test.go
      Verify: `cd /home/nuzirwan/project/rule-engine-api/.worktrees/test-connection/engine && go test ./...`

---

## Part 2: CMS Plugin Controller

- [ ] 6. Add TypeScript types for test connection request/response.
      TestConnectionRequest: { type, settings, secret }. TestConnectionResponse: { success, message?, error? }.
      Files: cms/types/engine.ts
      Verify: `cd /home/nuzirwan/project/rule-engine-api/.worktrees/test-connection/cms && npx tsc --noEmit`

- [ ] 7. Add `testConnection` method to AdminClient.
      Calls `POST /admin/connections/test` on engine.
      Files: cms/src/plugins/rule-engine/server/src/services/admin-client.ts
      Verify: `cd /home/nuzirwan/project/rule-engine-api/.worktrees/test-connection/cms && npx tsc --noEmit`

- [ ] 8. Create connection-test.ts controller with testConnection handler.
      Validates input, creates AdminClient, proxies to engine, handles errors per §5.5.
      Files: cms/src/plugins/rule-engine/server/src/controllers/connection-test.ts
      Verify: `cd /home/nuzirwan/project/rule-engine-api/.worktrees/test-connection/cms && npx tsc --noEmit`

- [ ] 9. Register controller in plugin index and add route.
      Route: POST /connections/test -> connection-test.testConnection.
      Files: cms/src/plugins/rule-engine/server/src/index.ts, cms/src/plugins/rule-engine/server/src/routes/index.ts
      Verify: `cd /home/nuzirwan/project/rule-engine-api/.worktrees/test-connection/cms && npx tsc --noEmit`

- [ ] 10. Add controller tests.
      Test successful proxy, failed connection, missing fields, engine errors.
      Files: cms/src/plugins/rule-engine/server/tests/connection-test.test.ts
      Verify: `cd /home/nuzirwan/project/rule-engine-api/.worktrees/test-connection/cms && npm run test -- --run`

---

## Part 3: CMS Admin UI

- [ ] 11. Create TestConnectionModal component.
      Shows connection type/settings (read-only), password input, Test button, result indicator.
      Uses Dialog from @strapi/design-system following EnvironmentManager pattern.
      Files: cms/src/plugins/rule-engine/admin/src/components/TestConnectionModal/index.tsx
      Verify: `cd /home/nuzirwan/project/rule-engine-api/.worktrees/test-connection/cms && npx tsc --noEmit`

- [ ] 12. Create ConnectionsPage listing connections with Test button.
      Fetches connections via API, renders table, opens TestConnectionModal per connection.
      (Alternative to content-manager injection which is more complex.)
      Files: cms/src/plugins/rule-engine/admin/src/pages/ConnectionsPage.tsx
      Verify: `cd /home/nuzirwan/project/rule-engine-api/.worktrees/test-connection/cms && npx tsc --noEmit`

- [ ] 13. Register ConnectionsPage in plugin navigation.
      Add menu item and route in plugin's admin index.tsx.
      Files: cms/src/plugins/rule-engine/admin/src/index.tsx
      Verify: `cd /home/nuzirwan/project/rule-engine-api/.worktrees/test-connection/cms && npx tsc --noEmit`

---

## Final Verification

- [ ] 14. Run full verification suite.
      Engine: `go build ./... && go test ./...`
      CMS: `npx tsc --noEmit && npm run test -- --run`
      Verify: Both pass with no errors.

---

## Key Implementation Details

### Engine testConnection Handler Pattern
```go
func (a *Admin) testConnection(w http.ResponseWriter, r *http.Request) {
    var req testConnectionRequest
    if err := decodeJSON(r, &req); err != nil {
        writeError(w, http.StatusBadRequest, "invalid request body")
        return
    }
    // Validate, look up connector, build def, inject secret, open, ping, close
}
```

### CMS AdminClient Method Pattern
```typescript
testConnection(req: TestConnectionRequest): Promise<TestConnectionResponse> {
  return this.request<TestConnectionResponse>('POST', '/admin/connections/test', req);
}
```

### Modal State Pattern (from EnvironmentManager)
```typescript
const [isOpen, setIsOpen] = useState(false);
const [password, setPassword] = useState('');
const [isLoading, setIsLoading] = useState(false);
const [result, setResult] = useState<{success: boolean; message?: string; error?: string} | null>(null);
```
