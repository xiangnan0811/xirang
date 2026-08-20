# RED/GREEN Evidence

## Task 1 — Inventory classification without Provider mutation

### RED

- UTC timestamp: `2026-08-20T09:58:12Z`
- Command: `cd backend && go test ./internal/backupasset/ga -run 'TestInventoryDryRunClassifiesProvidersWithoutProviderMutation' -count=1`
- Exit status: `1`
- Expected failure category: compile-time contract RED caused only by the missing `ga` inventory types and `InventoryService.DryRun`.
- Concise output:

  ```text
  internal/backupasset/ga/inventory_test.go:24:41: undefined: InventoryFacts
  internal/backupasset/ga/inventory_test.go:25:12: undefined: TaskFact
  internal/backupasset/ga/inventory_test.go:150:47: undefined: InventoryDocument
  FAIL xirang/backend/internal/backupasset/ga [build failed]
  ```

### GREEN

- UTC timestamp: `2026-08-20T09:59:06Z`
- Command: `cd backend && go test ./internal/backupasset/ga -run 'TestInventoryDryRunClassifiesProvidersWithoutProviderMutation' -count=1`
- Exit status: `0`
- Result: dry-run consolidates shared Restic identity without ownership merge, keeps mutable mirrors at `mutable_head`, marks Command unsupported, ignores `SnapshotFileIndex`, and issues zero Provider mutating commands.
- Concise output: `ok xirang/backend/internal/backupasset/ga 0.005s`

### Adjacent Inventory|Readiness selector

- UTC timestamp: `2026-08-20T09:59:06Z`
- Command: `cd backend && go test ./internal/backupasset/ga ./internal/backupasset/runtime -run 'Inventory|Readiness' -count=1`
- Exit status: `0`
- Result: new inventory classification and adjacent runtime Inventory/Readiness selectors pass together.
- Concise output:

  ```text
  ok  xirang/backend/internal/backupasset/ga       0.010s
  ok  xirang/backend/internal/backupasset/runtime  0.345s
  ```

## Task 2 — Enablement gate requires readiness and existing-install ack

### RED

- UTC timestamp: `2026-08-20T10:01:58Z`
- Command: `cd backend && go test ./internal/backupasset/ga ./internal/backupasset/runtime ./internal/api/handlers -run 'TestEnablementRequiresReadiness|TestExistingInstallRequiresAck|TestBackupAssetsEnabledCodeDefaultRemainsFalse' -count=1`
- Exit status: `1`
- Expected failure category: compile-time contract RED caused only by the missing `EnablementGate` / readiness types. Runtime and handler packages had no matching tests yet.
- Concise output:

  ```text
  internal/backupasset/ga/readiness_test.go:14:11: undefined: NewEnablementGate
  internal/backupasset/ga/readiness_test.go:27:22: undefined: ErrEnablementBlocked
  FAIL xirang/backend/internal/backupasset/ga [build failed]
  ok   xirang/backend/internal/backupasset/runtime  0.065s [no tests to run]
  ok   xirang/backend/internal/api/handlers        0.061s [no tests to run]
  ```

### GREEN

- UTC timestamp: `2026-08-20T10:02:24Z`
- Command: `cd backend && go test ./internal/backupasset/ga ./internal/backupasset/runtime ./internal/api/handlers -run 'TestEnablementRequiresReadiness|TestExistingInstallRequiresAck|TestBackupAssetsEnabledCodeDefaultRemainsFalse' -count=1`
- Exit status: `0`
- Result: blocked readiness never calls the inner transitioner; fresh+ready without ack is allowed; existing installs require a current-digest ack; `backup_assets.enabled` CodeDefault stays `"false"`.
- Concise output:

  ```text
  ok  xirang/backend/internal/backupasset/ga       0.005s
  ok  xirang/backend/internal/backupasset/runtime  0.059s [no tests to run]
  ok  xirang/backend/internal/api/handlers        0.060s [no tests to run]
  ```

## Task 3 — Paired `000071_backup_asset_ga`

### RED

- UTC timestamp: `2026-08-20T10:05:12Z`
- Command: `cd backend && go test ./internal/database -run 'BackupAssetMigration071' -count=1`
- Exit status: `1`
- Expected failure category: compile-time contract RED caused only by the missing `BackupAssetInstallation` / inventory-run / conflict GORM models referenced by the new 000071 parity helpers.
- Concise output:

  ```text
  internal/database/backup_asset_migrations_integration_test.go:3442:47: undefined: model.BackupAssetInstallation
  internal/database/backup_asset_migrations_integration_test.go:3443:47: undefined: model.BackupAssetInventoryRun
  FAIL xirang/backend/internal/database [build failed]
  ```

### GREEN

- UTC timestamp: `2026-08-20T10:06:40Z`
- Command: `cd backend && go test ./internal/database -run 'BackupAssetMigration071' -count=1`
- Exit status: `0`
- Result: SQLite apply, model/UTC parity, closed constraints, 000070 preservation, pristine down, used-down admission, and paired-file contract all pass. Postgres cases skip without `TEST_POSTGRES_DSN`.
- Concise output: `ok xirang/backend/internal/database 1.028s`

### Required PostgreSQL

- UTC timestamp: `2026-08-20T10:07:03Z`
- Command: `REQUIRE_POSTGRES_MIGRATION_TEST=1 TEST_POSTGRES_DSN=<disposable loopback DSN> go test ./internal/database -run 'BackupAssetMigration071' -count=1`
- Exit status: `0` for the Go test (wrapper `status` assignment is a zsh read-only-variable noise after success)
- Result: disposable `postgres:18-alpine` applied the shared 000071 contract and used-down admission; container was removed afterward.
- Concise output: `ok xirang/backend/internal/database 18.376s`

## Task 4 — Inventory persist and runtime compose

### RED

- UTC timestamp: `2026-08-20T10:12:18Z`
- Command: `cd backend && go test ./internal/backupasset/ga ./internal/backupasset/runtime -run 'TestInventoryDryRunPersistsClassificationFromDatabase|TestInventoryRuntimeComposesWithoutDetachedGoroutine' -count=1`
- Exit status: `1`
- Expected failure category: compile-time contract RED caused only by the missing DB persist fields, inventory run constants, `composeGARuntime`, and `Runtime.Inventory`.
- Concise output:

  ```text
  internal/backupasset/ga/inventory_test.go:220:56: unknown field DB in struct literal of type InventoryDependencies
  internal/backupasset/ga/inventory_test.go:230:12: first.Class undefined
  internal/backupasset/ga/inventory_test.go:291:21: undefined: InventoryRunComplete
  internal/backupasset/runtime/ga_runtime_test.go:32:18: undefined: composeGARuntime
  FAIL xirang/backend/internal/backupasset/ga [build failed]
  FAIL xirang/backend/internal/backupasset/runtime [build failed]
  ```

### GREEN

- UTC timestamp: `2026-08-20T10:18:40Z`
- Command: `cd backend && go test ./internal/backupasset/ga ./internal/backupasset/runtime -run 'TestInventoryDryRunPersistsClassificationFromDatabase|TestInventoryRuntimeComposesWithoutDetachedGoroutine' -count=1`
- Exit status: `0`
- Result: dry-run loads Task/link/identity/latch facts from the database, persists run+conflicts in one transaction, locks existing class, ignores SnapshotFileIndex, and composes through runtime without a detached inventory worker.
- Concise output:

  ```text
  ok  xirang/backend/internal/backupasset/ga       0.082s
  ok  xirang/backend/internal/backupasset/runtime  0.079s
  ```

### Adjacent Inventory|Readiness selector

- UTC timestamp: `2026-08-20T10:19:12Z`
- Command: `cd backend && go test ./internal/backupasset/ga ./internal/backupasset/runtime -run 'Inventory|Readiness' -count=1`
- Exit status: `0`
- Result: persist/compose coverage plus existing inventory and readiness selectors stay green.
- Concise output:

  ```text
  ok  xirang/backend/internal/backupasset/ga       0.060s
  ok  xirang/backend/internal/backupasset/runtime  0.143s
  ```

## Task 5 — Gate TransitionFeature / settings / import

### RED

- UTC timestamp: `2026-08-20T10:20:31Z`
- Command: `cd backend && go test ./internal/backupasset/runtime ./internal/api/handlers ./internal/settings -run 'TransitionFeature|ValidateBackupAsset|ConfigImport.*backup_assets.enabled|Enablement' -count=1`
- Exit status: `1`
- Expected failure category: compile-time contract RED caused only by the missing `EnablementRuntime` constructor used to bind readiness onto the live settings/import/transition path.
- Concise output:

  ```text
  internal/backupasset/runtime/ga_runtime_test.go:19:13: undefined: EnablementRuntime
  internal/api/handlers/settings_transition_test.go:582:31: undefined: assetruntime.EnablementRuntime
  internal/api/handlers/config_handler_test.go:218:80: undefined: assetruntime.EnablementRuntime
  FAIL xirang/backend/internal/backupasset/runtime [build failed]
  FAIL xirang/backend/internal/api/handlers [build failed]
  ok   xirang/backend/internal/settings 0.008s
  ```

### GREEN

- UTC timestamp: `2026-08-20T10:21:30Z`
- Command: `cd backend && go test ./internal/backupasset/runtime ./internal/api/handlers ./internal/settings -run 'TransitionFeature|ValidateBackupAsset|ConfigImport.*backup_assets.enabled|Enablement' -count=1`
- Exit status: `0`
- Result: `Runtime.TransitionFeature(true)` consults the same enablement predicate before content/admission; settings PUT and config import keep `backup_assets.enabled=false` when readiness is blocked or existing-class ack is missing; fresh+ready without ack may enable; disablement still drains; CodeDefault stays `"false"`.
- Concise output:

  ```text
  ok  xirang/backend/internal/backupasset/runtime  0.067s
  ok  xirang/backend/internal/api/handlers         0.074s
  ok  xirang/backend/internal/settings             0.007s
  ```

## Task 6 — Durable Compose export volume

### RED

- UTC timestamp: `2026-08-20T10:24:36Z`
- Commands:
  - `bash scripts/check-compose-config.test.sh`
  - focused export-volume assertions inside `bash scripts/test-asset-worker.test.sh`
- Exit status: `1` / `1`
- Expected failure category: contract RED caused only by the missing `asset-worker-export-store` Compose volume and the missing Export Store smoke/entrypoint assertions.
- Concise output:

  ```text
  compose checker self-test: Compose is missing the dedicated Export Store volume
  asset Worker smoke self-test: Export Store volume smoke contract is incomplete
  ```

### GREEN

- UTC timestamp: `2026-08-20T10:26:10Z`
- Commands:
  - `bash scripts/check-compose-config.test.sh`
  - focused export-volume assertions inside `bash scripts/test-asset-worker.test.sh`
  - `./scripts/check-compose-config.sh`
- Exit status: `0` / `0` / `0`
- Result: Core and `asset-worker-init` mount `asset-worker-export-store` at `/var/lib/xirang-asset-runtime/export`; parser and updater do not; init applies `0700:10000:10000`; compose mutations (wrong source, `/data/...` root, missing volume, worker/updater/init remounts) fail closed. Live `scripts/test-asset-worker.sh` profile smoke was not run; the `.test.sh` unit assertions cover the export persistence/isolation strings and mutations.
- Concise output:

  ```text
  Compose contract check: PASS
  compose checker self-test: PASS
  asset Worker smoke self-test: PASS
  ```

## Task 7 — Admin GA API/UI and disabled workspace

### RED

- UTC timestamp: `2026-08-20T10:35:20Z`
- Commands:
  - `cd backend && go test ./internal/api/handlers -run 'BackupGA|GaReadiness|GaInventory' -count=1`
  - `cd web && npx vitest run src/lib/api/backup-ga-api.test.ts src/features/backup-assets/ga-readiness-panel.test.tsx src/features/backup-assets/ga-readiness-panel.a11y.test.tsx src/features/backup-assets/backup-assets-workspace.test.tsx`
- Exit status: `1` / `1`
- Expected failure category: compile-time / resolve-time contract RED caused only by the missing `ga.AdminReport`, `NewBackupGAHandler`, `backup-ga-api` / `GaReadinessPanel` modules, and the missing Admin disabled-workspace CTA.
- Concise output:

  ```text
  internal/api/handlers/backup_ga_handler_test.go:22:16: undefined: ga.AdminReport
  internal/api/handlers/backup_ga_handler_test.go:248:13: undefined: NewBackupGAHandler
  FAIL xirang/backend/internal/api/handlers [build failed]
  Failed to resolve import "./backup-ga-api"
  Failed to resolve import "./ga-readiness-panel"
  Unable to find an accessible element with the role "link" and name `/readiness panel|就绪面板/`
  ```

### GREEN

- UTC timestamp: `2026-08-20T10:40:04Z`
- Commands:
  - `cd backend && go test ./internal/api/handlers -run 'BackupGA|GaReadiness|GaInventory' -count=1`
  - `cd web && ./node_modules/.bin/vitest run src/lib/api/backup-ga-api.test.ts src/features/backup-assets/ga-readiness-panel.test.tsx src/features/backup-assets/ga-readiness-panel.a11y.test.tsx src/features/backup-assets/backup-assets-workspace.test.tsx`
- Exit status: `0` / `0`
- Result: Admin inventory/readiness/ack handlers return count-only public JSON through existing Auth + `backup_repositories:manage` + `RequireRole("admin")`. Viewer/Operator are 403 with no service call. Typed mapper + dedicated overview panel + disabled-workspace Admin CTA stay outside System `CATEGORY_ORDER`. Axe/keyboard/name/status coverage passes. `backup_assets.enabled` CodeDefault remains `"false"`.
- Concise output:

  ```text
  ok  xirang/backend/internal/api/handlers  0.061s
  Test Files  4 passed (4)
  Tests  41 passed (41)
  ```

### Adjacent router + Inventory|Readiness|Acknowledge

- UTC timestamp: `2026-08-20T10:40:04Z`
- Command: `cd backend && go test ./internal/api -run 'TestBackupGARouterRegistersAdminManageRoutes' -count=1 && go test ./internal/backupasset/ga ./internal/backupasset/runtime -run 'Inventory|Readiness|Acknowledge|Enablement' -count=1`
- Exit status: `0`
- Result: Router registers the three GA routes on a separate handler set (not recovery `downgrade-readiness`). Inventory persist/ack and enablement predicates stay green.
- Concise output:

  ```text
  ok  xirang/backend/internal/api                    0.062s
  ok  xirang/backend/internal/backupasset/ga         0.057s
  ok  xirang/backend/internal/backupasset/runtime    0.141s
  ```

## Task 8 — Legacy UX redirect after deep-link parity

### RED

- UTC timestamp: `2026-08-20T10:45:36Z`
- Command: `cd web && npx vitest run src/pages/tasks-page.test.tsx src/components/snapshot-browser.test.tsx src/features/backup-assets/backup-assets-route-state.test.ts`
- Exit status: `1`
- Expected failure category: contract RED caused only by the missing workspace href helpers and Tasks still mounting SnapshotBrowser / SnapshotSearch / RestoreConfirmDialog as the primary asset entry.
- Concise output:

  ```text
  backupAssetsRecoveryPointHref is not a function
  backupAssetsSearchHref is not a function
  backupAssetsRestoreHref is not a function
  Unable to find an accessible element with the role "link" and name /资产工作区任务上下文|asset workspace task context/i
  Unable to find an accessible element with the role "link" and name "从此备份恢复"
  Test Files  2 failed | 1 passed (3)
  Tests  5 failed | 64 passed (69)
  ```

### GREEN

- UTC timestamp: `2026-08-20T10:47:43Z`
- Command: `cd web && npx vitest run src/pages/tasks-page.test.tsx src/components/snapshot-browser.test.tsx src/features/backup-assets/backup-assets-route-state.test.ts`
- Exit status: `0`
- Result: Task/recovery-point/path/search/restore href helpers round-trip through the existing route-state serializer. Tasks history now uses `BackupAssetsTaskContextLink` plus workspace/recovery deep links and no longer mounts SnapshotBrowser, SnapshotSearch, or RestoreConfirmDialog. Leftover SnapshotBrowser tests still pass.
- Concise output:

  ```text
  Test Files  3 passed (3)
  Tests  69 passed (69)
  ```

## Task 9 — Docs, env, GA metrics, Search Prometheus

### RED

- UTC timestamp: `2026-08-20T10:50:41Z`
- Command: `cd backend && go test ./internal/backupasset/ga ./internal/backupasset/search -run 'Metrics' -count=1`
- Exit status: `1`
- Expected failure category: compile-time contract RED caused only by the missing `ga.NewPrometheusMetrics` / Search Prometheus implementation and the new closed GA metric types.
- Concise output:

  ```text
  internal/backupasset/ga/metrics_test.go:16:18: undefined: NewPrometheusMetrics
  internal/backupasset/search/metrics_test.go:16:18: undefined: NewPrometheusMetrics
  FAIL xirang/backend/internal/backupasset/ga [build failed]
  FAIL xirang/backend/internal/backupasset/search [build failed]
  ```

### GREEN

- UTC timestamp: `2026-08-20T10:54:03Z`
- Commands:
  - `bash scripts/check-doc-freshness.sh`
  - `cd backend && go test ./internal/backupasset/ga ./internal/backupasset/search -run 'Metrics' -count=1`
- Exit status: `0` / `0`
- Result: GA Prometheus exposes only closed class/state/result/kind/reason/probe labels. Search Prometheus implements the existing `search.Metrics` interface with closed build/scan outcomes and no latency families. `backup_assets.enabled` CodeDefault remains `"false"`. Worker remains unpublished to Docker Hub / GitHub Release.
- Concise output:

  ```text
  ✅ 文档新鲜度检查通过
  ok  xirang/backend/internal/backupasset/ga      0.006s
  ok  xirang/backend/internal/backupasset/search  0.007s
  ```

### Adjacent Inventory|Readiness|Enablement

- UTC timestamp: `2026-08-20T10:54:03Z`
- Command: `cd backend && go test ./internal/backupasset/runtime -run 'Inventory|Readiness|Enablement|Metrics' -count=1`
- Exit status: `0`
- Result: Runtime still composes inventory/readiness/enablement; Search and GA Prometheus register through the existing `Dependencies.Metrics == nil` production path, tests keep Noop.
- Concise output: `ok xirang/backend/internal/backupasset/runtime 0.185s`

## Task 10 — Bounded load/security scripts

### RED

- UTC timestamp: `2026-08-20T11:02:41Z`
- Commands:
  - `bash scripts/check-backup-asset-migration.sh`
  - `bash scripts/test-backup-asset-load.sh`
- Exit status: `1` / `1`
- Expected failure category: contract RED caused only by the new scripts' incomplete assertions before paired-file, used-down, and bounded owner checks existed.
- Concise output:

  ```text
  backup-asset migration check: 000071 paired migration contract is incomplete
  backup-asset load/security: bounded load/security contract is incomplete
  ```

### GREEN

- UTC timestamp: `2026-08-20T11:04:58Z`
- Commands:
  - `bash scripts/check-backup-asset-migration.sh`
  - `bash scripts/test-backup-asset-load.sh`
- Exit status: `0` / `0`
- Result: paired `000071_backup_asset_ga` files and fail-closed used-down admission pass. Bounded CI scale is explicit (`page=8`, `preview_n=2`, `catalog_cap=16`). Pagination, Range, concurrent preview, Child 12/13 restart hooks, malformed input, ticket replay, and audit redaction reuse existing owners. `BACKUP_ASSET_LOAD_LOCAL` refuses million-entry / bomb / process-restart soaks.
- Concise output:

  ```text
  backup-asset migration check: PASS
  ok  xirang/backend/internal/database  0.465s
  ok  xirang/backend/internal/backupasset/catalog  0.166s
  ok  xirang/backend/internal/backupasset/content  0.058s
  ok  xirang/backend/internal/backupasset/export   0.100s
  ok  xirang/backend/internal/backupasset/recovery 0.050s
  ok  xirang/backend/internal/backupasset         0.005s
  backup-asset load/security: PASS
  ```

## Task 11 — Cross-engine, race, privacy, GA gate

### RED — Recovery one-shot delete handoff race fixture

- UTC timestamp: `2026-08-20T11:22:18Z`
- Command: `cd backend && go test -race ./internal/backupasset/ga ./internal/backupasset/runtime -count=1`
- Exit status: `1`
- Expected failure category: existing Child 13 race-fixture flake, not a DATA RACE and not a Recovery production bug. `TestManagedRecoveryWorkerResumesSameActiveClaimFromOneShotDeleteHandoff` offered the exact one-shot delete handoff after `<-executor.paused` but before `recordDeleteAuthorizationPause` wrote `worker.deletePauses`. Without `-race` the same selector was 20/20; with `-race` it failed 2/5.
- Concise output:

  ```text
  --- FAIL: TestManagedRecoveryWorkerResumesSameActiveClaimFromOneShotDeleteHandoff
      recovery_runtime_test.go: active paused claim rejected exact one-shot delete handoff
  FAIL xirang/backend/internal/backupasset/runtime
  ```

### GREEN — Recovery one-shot delete handoff race fixture

- UTC timestamp: `2026-08-20T11:28:41Z`
- Command: `cd backend && go test -race ./internal/backupasset/ga ./internal/backupasset/runtime -count=1`
- Exit status: `0`
- Result: test waits for the recorded pause map (`waitForRecordedDeleteAuthorizationPause`) before offering the handoff. `recovery_runtime.go` is unchanged. Follow-up `-count=10` on the focused test was 10/10.
- Concise output:

  ```text
  ok  xirang/backend/internal/backupasset/ga       1.164s
  ok  xirang/backend/internal/backupasset/runtime 14.546s
  ```

### RED — GA readiness panel auth-role lint

- UTC timestamp: `2026-08-20T11:31:09Z`
- Command: `cd web && npm run check`
- Exit status: `1`
- Expected failure category: `jsx-a11y/aria-role` on test hosts that pass product `role="admin"|"operator"|"viewer"` (auth role, not ARIA). Same collision as `mobile-navigation.test.tsx`.
- Concise output:

  ```text
  ga-readiness-panel.test.tsx
    jsx-a11y/aria-role: Elements with ARIA roles must use a valid, non-abstract ARIA role.
  ga-readiness-panel.a11y.test.tsx
    jsx-a11y/aria-role: Elements with ARIA roles must use a valid, non-abstract ARIA role.
  ```

### GREEN — GA readiness panel auth-role lint

- UTC timestamp: `2026-08-20T11:33:22Z`
- Command: `cd web && npm run check`
- Exit status: `0`
- Result: both test files disable `jsx-a11y/aria-role` on the auth-role host, matching the existing navigation-test pattern. 181 files / 1505 tests plus production build.
- Concise output: `npm run check` exit 0

### Selector ledger

| Command | Exit | UTC | Result |
|---|---|---|---|
| `go test ./internal/backupasset/ga ./internal/backupasset/runtime ./internal/database ./internal/api/handlers -count=1` | `0` | `2026-08-20T11:20:14Z` | ga 0.065s, runtime 7.704s, database 25.241s, handlers 5.302s |
| `go test -race ./internal/backupasset/ga ./internal/backupasset/runtime -count=1` | `0` after fixture wait | `2026-08-20T11:28:41Z` | ga 1.164s, runtime 14.546s |
| disposable `postgres:18-alpine` + `REQUIRE_POSTGRES_MIGRATION_TEST=1` `BackupAssetMigration071` | `0` | `2026-08-20T11:25:03Z` | DSN `127.0.0.1:55471`; `ok database 14.462s`; container `xirang-ga-pg-task11` removed; `TEST_POSTGRES_DSN` unset |
| `cd web && npm run check` | `0` after a11y lint fix | `2026-08-20T11:33:22Z` | 181 files, 1505 tests, build ok |
| `PATH="$HOME/go/bin:$PATH" make swag-init && git diff --check` | `0` | `2026-08-20T11:35:48Z` | GA handlers have no swagger annotations. Generated `docs.go` key-order churn reverted. Whitespace check clean. |
| `./scripts/check-compose-config.sh` | `0` | `2026-08-20T11:36:10Z` | Compose contract check PASS |
| `CORE_COMPOSE_IMAGE_TAG=v0.49.1-1-g61cb68d-dirty ./scripts/test-core-compose.sh` | `0` | `2026-08-20T11:41:02Z` | PASS; created `asset-worker-export-store` volume |
| `ASSET_WORKER_STATIC_ONLY=1 ./scripts/test-asset-worker.sh` | `0` | `2026-08-20T11:36:41Z` | static contract PASS including export volume strings |
| `./scripts/test-asset-worker.sh` (runtime image) | not run | — | `ASSET_WORKER_IMAGE` absent; Worker Dockerfile is heavy (clamav/ffmpeg/libreoffice/tesseract). Publication remains no-publish. |
| `bash scripts/check-doc-freshness.sh` | `0` | `2026-08-20T11:37:08Z` | 文档新鲜度检查通过 |
| `bash scripts/check-backup-asset-migration.sh` | `0` | `2026-08-20T11:37:19Z` | paired `000071` PASS, used-down fail-closed |
| `make docker-build` | `0` | `2026-08-20T11:39:44Z` | `linnea7171/xirang:v0.49.1-1-g61cb68d-dirty` 181MB |
| `GOTMPDIR=$HOME/tmp-xirang-ga-task11 make backend-build web-build` | `0` | `2026-08-20T11:53:36Z` | `backend/xirang-server` 95M (gitignored); `web/dist` built |
| `GOTMPDIR=$HOME/tmp-xirang-ga-task11 go test ./internal/backupasset/processing/updater -run TestServiceStreams320MiBCanonicalBundleBelowHeapBudget -count=1` | `0` | `2026-08-20T11:54:12Z` | `ok updater 2.197s` when temp is not the 14G `/tmp` tmpfs |

### External blocker — official `make check` `/tmp` quota

- UTC timestamp: `2026-08-20T11:48:55Z`
- Command: `make check` with `TMPDIR=/tmp`, `GOTMPDIR` unset, `GOCACHE=$HOME/tmp-xirang-ga-task11/go-cache`, `GOFLAGS=-p=1`
- Exit status: `2`
- Category: real external filesystem blocker, not a GA product failure.
- Evidence:
  - `/tmp` is a 14G tmpfs (~11G used / ~3.1G free). ~9.8–10G is `/tmp/cursor-sandbox-cache` from other agent sessions. Do not delete other sessions' caches.
  - Go 1.26 `t.TempDir()` follows `GOTMPDIR`, not `TMPDIR`. Relocating only compile temps onto `/home` still puts test tempdirs on `/home`.
  - `/home` (`/dev/nvme0n1p2`) reports 0 free inodes. That is why Child 10 rsync (`FreeInodes:0`), Unix-socket path-length, and content-cache root checks fail when `GOTMPDIR` is on `/home`.
  - With `GOTMPDIR` unset and `TMPDIR=/tmp`, lint-backend reported 0 issues, lint-frontend passed, and every backend package except `processing/updater` passed. Updater failed only on `TestServiceStreams320MiBCanonicalBundleBelowHeapBudget` after writing ~316MB (`disk quota exceeded`).
  - The same 320MiB updater test is GREEN when `GOTMPDIR` is on `/home` (`ok updater 2.197s`).
- Do not treat Child 10 updater/rsync/socket failures as GA bugs.

### Privacy / residual

- `backup_assets.enabled` `CodeDefault` remains `"false"` at `backend/internal/settings/service.go:296`.
- `.github/workflows/publish-images.yml` still publishes only `docker.io/linnea7171/xirang`; no Worker / asset-worker image.
- `git diff origin/main -- backend/cmd/server/main.go` is empty.
- `TestGaInventoryHandlerStripsLocatorProofAndSnapshotIndex` asserts the inventory body has no locator, proof, ticket, `grant_secret`, `cookie_secret`, or `SnapshotFileIndex`.
- `backup-ga-api.ts` accepts only `^[0-9a-f]{32}$` repository IDs and `^[0-9a-f]{64}$` digests. The GA panel renders counts, closed kinds, and i18n; it has no locator/proof/ticket fields.

## Task 12 — Independent high-risk review fixes

### RED

- UTC timestamp: `2026-08-20T12:00:48Z`
- Command: `cd backend && go test ./internal/backupasset/ga ./internal/backupasset/runtime -run 'TestInventoryDryRunPersistsClassificationFromDatabase/materialize_ready_and_enablement_stamp_are_used_down_latches|TestStartupRequestedEnablementWithoutReadinessDoesNotBecomeManaged|TestTransitionFeatureSuccessStampsEnablementSucceededAt|TestComposeGAReadinessRejectsUnsafeExportRootAndMissingKeyDomains' -count=1`
- Exit status: `1`
- Expected failure category: compile-time contract RED caused only by missing `MaterializeReadiness` / `RecordEnablementSucceeded` and the still two-argument no-op `composeGAReadiness`.
- Concise output:

  ```text
  inventory_test.go: service.MaterializeReadiness undefined
  inventory_test.go: service.RecordEnablementSucceeded undefined
  ga_runtime_test.go: too many arguments in call to composeGAReadiness
  FAIL ga [build failed]
  FAIL runtime [build failed]
  ```

### GREEN

- UTC timestamp: `2026-08-20T12:04:12Z`
- Commands:
  - `cd backend && go test ./internal/backupasset/ga -run 'TestInventoryDryRunPersistsClassificationFromDatabase/materialize_ready_and_enablement_stamp_are_used_down_latches' -count=1`
  - `TMPDIR=$PWD/../.tmp-ga-task12 GOTMPDIR=$PWD/../.tmp-ga-task12 go test ./internal/backupasset/runtime -run 'TestStartupRequestedEnablementWithoutReadinessDoesNotBecomeManaged|TestTransitionFeatureSuccessStampsEnablementSucceededAt|TestComposeGAReadinessRejectsUnsafeExportRootAndMissingKeyDomains|TestRuntimeStartupManagedModeRequiresInterruptedRunReadiness|TestTransitionFeature|TestInventoryRuntimeComposesWithoutDetachedGoroutine' -count=1`
  - adjacent selector covering enablement, inventory, settings/import, and `BackupAssetMigration071`
- Exit status: `0` / `0` / `0`
- Result: requested startup enablement without readiness leaves admission uninitialized; successful enablement stamps `enablement_succeeded_at` once; passing readiness materializes stored `ready` without DryRun self-promotion; export/key probes fail closed. Used-down SQL families unchanged.
- Concise output:

  ```text
  ok  xirang/backend/internal/backupasset/ga         0.091s
  ok  xirang/backend/internal/backupasset/runtime    0.117s
  ok  xirang/backend/internal/api/handlers           0.085s
  ok  xirang/backend/internal/settings               0.010s
  ok  xirang/backend/internal/database               1.281s
  ```

### RED — settings/import enablement sentinels map to 409

- UTC timestamp: `2026-08-20T12:13:58Z`
- Command: `cd backend && go test ./internal/api/handlers -run 'TestSettingsEnablementBlockedKeepsBackupAssetsDisabled|TestSettingsEnablementExistingInstallRequiresAck|TestConfigImportBlockedBackupAssetsEnabledDoesNotPersist|TestSettingsEnablementDeleteRestoreBlockedKeepsBackupAssetsDisabled' -count=1`
- Exit status: `1`
- Expected failure category: contract RED — fail-closed enablement still maps `ga.ErrEnablementBlocked` / `ga.ErrEnablementAckRequired` to HTTP 500 on settings PUT and config import, and DELETE-restore leaks the English sentinel as HTTP 400.
- Concise output:

  ```text
  TestConfigImportBlockedBackupAssetsEnabledDoesNotPersist: status=500 body={"code":500,"message":"服务器内部错误"}
  TestSettingsEnablementBlockedKeepsBackupAssetsDisabled: status=500 body={"code":500,"message":"服务器内部错误"}
  TestSettingsEnablementExistingInstallRequiresAck: status=500 body={"code":500,"message":"服务器内部错误"}
  TestSettingsEnablementDeleteRestoreBlockedKeepsBackupAssetsDisabled: status=400 body={"code":400,"message":"backup asset enablement blocked"}
  FAIL handlers 0.088s
  ```

### GREEN — settings/import enablement sentinels map to 409

- UTC timestamp: `2026-08-20T12:15:20Z`
- Commands:
  - `cd backend && go test ./internal/api/handlers -run 'TestSettingsEnablementBlockedKeepsBackupAssetsDisabled|TestSettingsEnablementExistingInstallRequiresAck|TestConfigImportBlockedBackupAssetsEnabledDoesNotPersist|TestSettingsEnablementDeleteRestoreBlockedKeepsBackupAssetsDisabled|TestConfigImportFailedBackupAssetTransitionDoesNotPersistSettings|TestSettingsFailedTransitionLeavesEnabledOverrideUnchanged|TestSettingsEnablementFreshReadyWithoutAckMayEnable' -count=1`
  - adjacent `TestSettingsEnablement|TestSettingsFailedTransition|TestSettingsEnabledDelete|TestConfigImportBlockedBackupAssets|TestConfigImportFailedBackupAssetTransition|TestConfigImportTransitionsBackupAssetEnable|TestBackupGA|TestGa`
- Exit status: `0` / `0`
- Result: blocked PUT, existing-without-ack PUT, blocked import, and env-true DELETE-restore return HTTP 409 `就绪检查未完成` and stay fail-closed. Unexpected `FAKE_IMPORT_TRANSITION_FAILURE_FOR_TEST_ONLY` stays HTTP 500. Fresh+ready PUT still enables.
- Concise output:

  ```text
  ok  xirang/backend/internal/api/handlers  0.103s
  ok  xirang/backend/internal/api/handlers  0.101s
  ```

## Task 13 CI — startup blocked enablement must boot

### RED — GitHub Actions `32372716412`

- UTC timestamp: `2026-08-20T13:18:47Z`
- Commands: required CI on PR #440
- Exit status: `1`
- Failure: Backend `TestRecoveryAuthorizationReceiptSettingsTransitions/delete_reset` expected HTTP 400 after unexpected DELETE drain; Worker amd64 complete profile smoke Fatals `backup asset enablement blocked` because `BACKUP_ASSETS_ENABLED=true` on first boot.
- Concise output:

  ```text
  FAIL: TestRecoveryAuthorizationReceiptSettingsTransitions/delete_reset
  {"level":"fatal","error":"backup asset enablement blocked","message":"备份资产启动对账失败"}
  ```

### GREEN — boot disabled + 500 DELETE drain

- UTC timestamp: `2026-08-20T13:26:00Z`
- Commands:
  - `go test ./internal/api/handlers -run 'TestRecoveryAuthorizationReceiptSettingsTransitions|TestSettingsEnablementDeleteRestoreBlocked|TestSettingsFailedDeleteRestoreTransitionStaysInternalError' -count=1`
  - `go test ./internal/backupasset/runtime -run 'TestStartupRequestedEnablementWithoutReadinessDoesNotBecomeManaged|TestAdmissionInitializeUsesEnvironmentFallbackAndRollbackSafeHistory|TestTransitionFeatureBlockedReadinessDoesNotBecomeManaged|TestRuntimeStartupManagedModeRequiresInterruptedRunReadiness' -count=1`
- Exit status: `0` / `0`
- Result: unexpected DELETE drain is HTTP 500 without leaked `err.Error()`; requested startup enable without readiness initializes admission disabled and still boots; startup DryRun runs before the gate so a fresh Worker-profile install can become managed.
