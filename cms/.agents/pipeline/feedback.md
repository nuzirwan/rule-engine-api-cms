# Pipeline Regression Feedback

## Run: REGRESS stage — feat(audit-viewer) commit 39f52fe

---

### TEST SUITE — ✅ PASS (88/88)

All 7 test files, 88 tests passed:

| File | Tests |
|---|---|
| serialize.test.ts | 8 |
| publish-transform.test.ts | 17 |
| audit-controller.test.ts | 13 |
| publish-controller.test.ts | 12 |
| smoke.test.ts | 7 |
| publish-sequence.test.ts | 18 |
| connection-denylist-guard.test.ts | 13 |

---

### TYPESCRIPT (tsconfig.json) — ✅ PASS

No errors. Main app TypeScript check clean.

---

### TYPESCRIPT (tsconfig.server.json) — ⚠️ PRE-EXISTING FAILURES (2 errors, exit 1)

These errors are **NOT introduced by the latest commit**. Both pre-date `39f52fe` and have been present since `631ce18` (FEAT-003).

#### Error 1 — TS6059: `types/engine.ts` outside `rootDir`

- **File**: `tsconfig.server.json`
- **Cause**: `rootDir` is set to `src/plugins/rule-engine/server` but the include pattern `types/**/*.ts` pulls in `types/engine.ts` which lives at `cms/types/engine.ts` — outside that rootDir. Server-side source files also import it directly.
- **Suspected cause**: The `tsconfig.server.json` config was written before `types/engine.ts` was added (the tsconfig scaffold is from commit `f4a579e`; the types file from `631ce18`). The `rootDir` was never updated to accommodate the shared types.
- **Fix belongs in**: CODE — fix `tsconfig.server.json` by either widening `rootDir` to `"."` (or `"src"`) and using `composite`/`paths`, or move shared types under the `server` root, or use `paths` aliasing.

#### Error 2 — TS2307: Cannot find module `'../../../../../../../types/engine'` in `seed-entries.ts`

- **File**: `src/plugins/rule-engine/server/tests/fixtures/seed-entries.ts` line 12
- **Cause**: Import path has 7 `../` levels but the file is only 6 levels below `cms/`. Correct path should be `../../../../../../types/engine`.
- **Suspected cause**: Typo when the fixture was authored in `631ce18` — one extra `../` segment.
- **Fix belongs in**: CODE — change `'../../../../../../../types/engine'` to `'../../../../../../types/engine'` in that file.

> **Note**: Because `tsconfig.server.json` excludes `**/*.test.ts`, Error 2 only surfaces because `seed-entries.ts` is a fixture (not a test file itself), but it is excluded from vitest runs via separate path handling — which is why all 88 tests still pass despite this path error.

---

**Summary**: No regressions introduced by the latest commit. The two TypeScript errors in `tsconfig.server.json` are structural pre-existing issues. The test suite is fully green.
