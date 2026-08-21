# Implement — 备份资产审查全量收口

Do not run `task.py start` until Alan approves the latest planning summary.

Process: implement on `feat/backup-assets-review-closeout` → independent re-review of the work branch → only then commit/PR. Do not treat an implementer canvas as authority.

## Wave 0 — bookkeeping

- [x] `task.py set-branch` / `set-scope` (backup-assets)
- [x] Parent notes: formal release is v0.50.1; Child 17 active; parent stays planning. Do not paste the review essay into the parent.
- [x] Parent `meta.program_delivery`: instantiated 17, delivered 16, active_child = this dir
- [x] Seed `research/risk-ledger.md` and `research/acceptance-protocol.md` if not already present

## Wave 1 — P0 leftover snapshot HTTP

- [x] Add `respondGone` if missing
- [x] List / files / search / diff handlers return 410 for authenticated callers
- [x] Router tests: Viewer, Operator, Admin → 410 on all four reads; unauthenticated → 401
- [x] Restore: FeatureLive gate + Viewer/Operator/Admin-not-live cases
- [x] Rewrite `docs/admin/backup-recovery.md` old search URL
- [x] `make swag-init`
- [x] Update `.trellis/spec/backend/quality-guidelines.md`: leftover snapshot **read** HTTP is 410

## Wave 2 — atomic enablement + FeatureLive handlers

- [x] `TransitionFeature(true)` calls `startupSearch`; failure rolls back persist and content ready
- [x] Runtime tests: hot enable search ready without `StartupPass`; search fail does not persist
- [x] `runtimeBackupAssetHandlerConfigSource` uses `FeatureLive()`
- [x] Handler tests: requested-true / FeatureLive-false → closed (`TestRuntimeBackupAssetHandlerConfigSourceRequestedTrueLiveFalse`, HTTP matrix)
- [x] Settings UI: no searchable workspace until FeatureLive
- [x] Spec: handler Enabled = FeatureLive

## Wave 3 — audit fail-closed + F7 + preview chrome

- [x] Search `writeAudit` error fails the request; secret_reveal covered
- [x] F7: renewPreview reuses in-memory proof; clear on session/asset/role change
- [x] Preview pane no longer 18rem; list/grid keys verified
- [x] Restic Range: test or disable + copy
- [x] Spec: frontend type-safety for proof reuse

## Wave 4 — CI + load + SLO

- [x] `go test -race` job for backupasset + related handlers
- [x] Coverage floor file + CI check
- [x] `govulncheck` / `npm audit` high+ blocking with explicit allowlist files
- [x] Vitest global MSW `onUnhandledRequest: "error"` + console allowlist
- [x] Playwright e2e matrix chromium/firefox/webkit with live browse/search/preview assertions
- [x] Load script CI-bounded 10k catalog + SIGKILL reconcile; million/operator docs
- [x] Native AWS removed from supported matrix
- [x] Search 5xx / audit-fail / FeatureLive metric → documented SLO + one alert hook

## Wave 5 — ledger, docs, local gates

- [x] Fill `research/risk-ledger.md` statuses from the branch, not from intent
- [x] `research/acceptance-protocol.md` fields complete; production = `not_executed` until Alan runs it
- [x] Scoped backend + `cd web && npm run check` — see `research/local-gates.md` (gates 1, 4). Full `make backend-test` / `go test ./...` still CI.
- [x] Local race + coverage floor + govulncheck + npm audit high+ — `research/local-gates.md` (gates 2, 3, 5, 7). Playwright chromium/firefox passed; WebKit cannot launch on this Arch host (ICU 66 / libffi 7). Ubuntu CI remains the WebKit proof.
- [x] Independent re-review of the work branch written to `research/re-review-N.md`

## Validation

```bash
cd backend && go test ./internal/backupasset/... ./internal/api/...
cd backend && go test -race ./internal/backupasset/...
cd web && npm run check
cd web && npx playwright test
make swag-init
python3 ./.trellis/scripts/task.py validate .trellis/tasks/08-21-backup-assets-review-closeout
```

## Risky files

- `backend/internal/api/router.go` — do not drop Auth on leftover routes
- `backend/internal/backupasset/runtime/runtime.go` — persist/rollback races
- `.github/workflows/ci.yml` — do not silently re-soft-green other jobs
- `deploy/` / `publish-images.yml` — do not touch Worker publish

## Rollback

Revert the PR or set `backup_assets.enabled=false`. 410 on old reads is kept.

## Follow-up before `task.py start`

- Planning summary approved by Alan in a later message
- No product-code edits in the approval-waiting turn
