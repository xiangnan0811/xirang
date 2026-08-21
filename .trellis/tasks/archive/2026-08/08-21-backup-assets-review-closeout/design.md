# Design — 备份资产审查全量收口

## Boundaries

Child 17 改产品面、CI 门、文档和本 Child 台账。不改父任务状态，不改 Worker 发布，不翻转 CodeDefault。

| Surface | Change |
|---|---|
| Legacy snapshot read HTTP | Keep routes registered; handlers return 410 Gone |
| Legacy snapshot restore | Keep route; add FeatureLive gate on top of Admin + step-up |
| Enablement | `TransitionFeature(true)` also runs `startupSearch`; rollback on failure |
| Handler config | `Enabled` from `FeatureLive()`, not requested search config |
| Search audit | Fail the HTTP request when `audit.Write` fails |
| Preview renew | Session-scoped in-memory reveal proof reuse |
| CI | Race + coverage floor + blocking vuln + Playwright + MSW/console |
| Docs / ledger | Child 17 research only; parent notes version truth |

## Contracts

### 410 Gone

Add `respondGone` in `response.go` if missing. Body uses the same error envelope as other handlers (`code`, safe message). Message states the Catalog/Search replacement, not an internal stack.

Auth still runs first: unauthenticated → 401; authenticated any role → 410. Do not 404.

### Restore

Order: Auth → Admin role → FeatureLive → existing step-up / credential grant → restore handler. FeatureLive false → 403 or 410 (prefer 403 with `backup_assets_not_live` so 410 stays reserved for retired read APIs). Tests cover Viewer, Operator, Admin-not-live, Admin-live-without-step-up, Admin-live-with-step-up.

### Atomic enablement

`TransitionFeature(true)` after admission+content success must call the same `startupSearch` path `StartupPass` uses. If search fails: `SetReady(false)`, `PrepareDisable`, do not persist enabled (or persist then immediately revert — prefer **do not persist**). `StartupPass` remains the boot path; it must stay idempotent if search is already ready.

Worker start stays in `StartupPass` / optional processing. Settings/UI expose processing readiness separately from FeatureLive.

Handler config source:

```
live, err := runtime.FeatureLive()
Enabled: live
```

Requested-only true must not open HTTP.

### Settings UX

Workspace entry and search hooks already key off FeatureLive in Child 16. After R3/R4 they should agree. If a persist path can still save requested=true while FeatureLive is false (ack missing), the settings page shows the existing enablement inventory / blocked reasons — do not route into a 503 search workspace.

### Search audit fail-closed

`writeAudit` returns error to the handler. On write failure after a successful search execution: discard result payload, respond 503, increment a metric. Same for secret_reveal. Do not add an outbox.

Nil audit in unit tests: production router always injects audit. Tests that pass nil must either inject a spy or treat nil as fail-closed (prefer spy; nil audit in production wiring is a boot defect).

### F7 proof reuse

Frontend holds `revealProofRef` keyed by `{userId, sessionEpoch, assetRef, action}`. `issueContentTicket` uses it when `revealOnce` is false **or** when renewing preview. `renewPreview` calls issue with `revealOnce: false` and the cached proof. Clear on logout, role change, asset change, 401/403 on reveal.

Backend ticket handler already requires proof for classified assets; reuse is frontend-only. Do not put proof in URLs.

### Preview chrome

Replace the 18rem preview pane with a flex-fill pane (min-height from the workspace layout, not a fixed short rem). List/grid keys: `recoveryPointId + entryId` (already required by Child 16 type-safety; verify both surfaces).

Restic Range: add a handler/provider test that a Restic fixture either serves a valid 206 or the content plane disables Range for that provider. UI copy follows the capability flag.

### CI

- New workflow job or step: `go test -race` on `./internal/backupasset/...` and backup-asset handler packages.
- Coverage: `go test -coverpkg` on backupasset with a checked-in floor file (e.g. `backend/coverage-floors.txt`) so the number is reviewable.
- `govulncheck` / `npm audit`: remove `continue-on-error` for high+; exceptions in `backend/govulncheck-allowlist` / `web/npm-audit-allowlist.json` if a current finding would otherwise block unrelated work — empty allowlist preferred.
- Playwright: `web/e2e/` smoke, CI matrix chromium/firefox/webkit. Fixture backend or MSW+static; no production URL.
- Vitest setup file: MSW error + console trap.

### Load / cloud / SLO

CI calls `BACKUP_ASSET_LOAD_LOCAL=ci-bounded`. Operator `BACKUP_ASSET_LOAD_LOCAL=million-catalog` actually builds/runs or fails. Docs state Native AWS is unsupported until live suite. Add alert rule YAML or settings-backed rule for search 5xx and audit write failures if the alerting package already accepts static rules; otherwise document PromQL and a settings hook — do not invent a new alerting product.

## Compatibility

Feature is not generally launched. 410 is acceptable. Old UI SnapshotBrowser is already hidden; leftover API clients get 410. Swagger and admin docs update in the same PR.

## Rollout / rollback

1. Merge Child 17 to main via PR. CodeDefault stays false.
2. Existing production stays off unless Alan enables after dry-run inventory + ack + restart (R3 should remove the extra search-only restart).
3. Rollback: revert the PR or set `backup_assets.enabled=false`. 410 on old reads remains; that is intended.
4. Do not publish Worker.

## Trade-offs

| Choice | Why |
|---|---|
| 410 not Admin-only compatibility | Feature not launched; compatibility would keep the Viewer leak if anyone forgets a check |
| Fail-closed audit, no outbox | No outbox in repo; inventing one is out of scope and slower than closing the leak |
| Search starts in TransitionFeature | Closes F4 without forcing a process restart for the required plane |
| Playwright in this Child | Review asked for real browsers; jsdom cannot certify preview/layout |
| AWS stays unsupported | Cannot certify without credentials/live suite; lying is worse than a smaller matrix |
