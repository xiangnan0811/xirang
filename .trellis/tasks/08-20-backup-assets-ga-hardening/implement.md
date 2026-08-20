# Child 15 Implementation Plan

> Planning status: approved 2026-08-20. `task.py start` completed.
> Parent `implement.md` §16 remains superseded.

## 1. Baseline and execution discipline

- Implementation base: exact `origin/main`
  `61cb68d36bad0c4665959fb96d377e3cb36598c1` (re-verify immediately before
  `task.py start`; if `main` moved, rebase this package's evidence first).
- Dedicated branch: `codex/backup-assets-ga-hardening`.
- Formal release at planning: `v0.49.1`.
- Before Phase 2, rerun `trellis-before-dev` and
  `superpowers:test-driven-development`.
- For every task below:
  1. add only the named focused test;
  2. run the exact selector and append command, exit status, failure
     category, and minimal output to `research/red-green.md`;
  3. implement the bounded behavior;
  4. rerun the exact same selector and append GREEN evidence;
  5. run the adjacent package selector before moving on.
- A compile-time RED is valid only when caused by the just-added contract
  test. Missing PostgreSQL DSN is not a skipped pass for required engine
  tests.
- Child 13 historical provenance exceptions are not inherited.
- No commit, push, PR, merge, parent archive, CodeDefault flip, or Worker
  publish occurs until its explicit delivery step.

## 2. Rebaselined file manifest

Anticipated product/test/docs manifest. If implementation needs a path
outside it, stop, amend `design.md` and this list, and review before
editing that path.

### Create

```text
backend/internal/model/backup_asset_migration.go
backend/internal/model/backup_asset_migration_test.go
backend/internal/database/migrations/sqlite/000071_backup_asset_ga.up.sql
backend/internal/database/migrations/sqlite/000071_backup_asset_ga.down.sql
backend/internal/database/migrations/postgres/000071_backup_asset_ga.up.sql
backend/internal/database/migrations/postgres/000071_backup_asset_ga.down.sql

backend/internal/backupasset/ga/contracts.go
backend/internal/backupasset/ga/inventory.go
backend/internal/backupasset/ga/inventory_test.go
backend/internal/backupasset/ga/readiness.go
backend/internal/backupasset/ga/readiness_test.go
backend/internal/backupasset/ga/metrics.go
backend/internal/backupasset/ga/metrics_test.go
backend/internal/backupasset/runtime/ga_runtime.go
backend/internal/backupasset/runtime/ga_runtime_test.go

backend/internal/api/handlers/backup_ga_handler.go
backend/internal/api/handlers/backup_ga_handler_test.go

web/src/lib/api/backup-ga-api.ts
web/src/lib/api/backup-ga-api.test.ts
web/src/features/backup-assets/ga-readiness-panel.tsx
web/src/features/backup-assets/ga-readiness-panel.test.tsx
web/src/features/backup-assets/ga-readiness-panel.a11y.test.tsx

scripts/check-backup-asset-migration.sh
scripts/test-backup-asset-load.sh
```

### Modify

```text
backend/internal/database/backup_asset_migrations_integration_test.go
.github/workflows/ci.yml
.trellis/spec/backend/database-guidelines.md
backend/internal/backupasset/runtime/runtime.go
backend/internal/backupasset/runtime/runtime_test.go
backend/internal/backupasset/runtime/recovery_runtime_test.go
backend/internal/backupasset/runtime/controller.go
backend/internal/settings/service.go
backend/internal/settings/service_test.go
backend/internal/api/handlers/settings_handler.go
backend/internal/api/handlers/settings_handler_test.go
backend/internal/api/handlers/settings_transition_test.go
backend/internal/api/handlers/config_handler.go
backend/internal/api/handlers/config_handler_test.go
backend/internal/api/router.go
backend/internal/api/router_test.go
backend/internal/backupasset/audit_action.go
backend/internal/backupasset/search/metrics.go
backend/internal/backupasset/search/metrics_test.go

web/src/pages/backups-page.overview.tsx
web/src/pages/backups-page.test.tsx
web/src/pages/tasks-page.dialogs.tsx
web/src/pages/tasks-page.test.tsx
web/src/pages/tasks-page.tsx
web/src/features/backup-assets/backup-assets-route-state.ts
web/src/features/backup-assets/backup-assets-route-state.test.ts
web/src/features/backup-assets/backup-assets-workspace.tsx
web/src/features/backup-assets/backup-assets-workspace.test.tsx
web/src/i18n/locales/zh.ts
web/src/i18n/locales/en.ts

docker-compose.yml
deploy/worker/entrypoint.sh
scripts/check-compose-config.sh
scripts/check-compose-config.test.sh
scripts/test-asset-worker.sh
scripts/test-asset-worker.test.sh
.env.deploy
backend/.env.production.example
docs/admin/backup-recovery.md
docs/admin/security.md
docs/deployment.md
docs/env-vars.md
README.md
backend/README_backend.md
.trellis/spec/backend/error-handling.md
.trellis/spec/backend/deployment-runtime.md
.trellis/spec/backend/quality-guidelines.md
.trellis/spec/frontend/type-safety.md
.trellis/spec/guides/cross-layer-thinking-guide.md
```

### Do not modify unless an approved amendment says so

```text
backend/cmd/server/main.go
backup_assets.enabled CodeDefault
deploy/worker/Dockerfile
deploy/worker/seccomp.json
.github/workflows/publish-images.yml
.github/workflows/ci.yml Worker no-publish comments (keep no-publish)
.lock files / go.mod / package-lock.json
.codex/**
Children 1–14 archive research
000062–000070 SQL
```

`backend/cmd/server/main.go` stays untouched if GA can compose inside
`backupasset/runtime` the way Child 14 retention did. If a required wiring
appears, amend this manifest first.

## 3. Internal tasks

### Task 1 — Inventory classification tests

Write RED tests that:

- Restic/Rsync/Rclone Tasks become candidates;
- shared Restic identity consolidates without cross-task ownership merge;
- mutable mirrors stay `mutable_head`;
- Command is `command_unsupported`;
- `SnapshotFileIndex` is ignored;
- dry-run issues zero Provider mutating commands.

Selector (exact name to capture on RED):

```bash
cd backend && go test ./internal/backupasset/ga -run 'TestInventoryDryRunClassifiesProvidersWithoutProviderMutation' -count=1
```

### Task 2 — Enablement-gate tests

Write RED tests that settings/import/transition cannot become managed when
readiness is blocked, and that existing-class enablement without ack fails.
Fresh + ready without ack succeeds. CodeDefault remains `"false"`.

```bash
cd backend && go test ./internal/backupasset/ga ./internal/backupasset/runtime ./internal/api/handlers -run 'TestEnablementRequiresReadiness|TestExistingInstallRequiresAck|TestBackupAssetsEnabledCodeDefaultRemainsFalse' -count=1
```

### Task 3 — Paired `000071`

Implement schema + integration tests on SQLite and required PostgreSQL:
apply, pristine down, used-down admission, UTC, model parity.

```bash
cd backend && go test ./internal/database -run 'BackupAssetMigration071' -count=1
REQUIRE_POSTGRES_MIGRATION_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" go test ./internal/database -run 'BackupAssetMigration071' -count=1
```

### Task 4 — Inventory owner GREEN

Implement `ga.InventoryService` against Task 1 tests. Compose through
runtime without a detached goroutine. Adjacent:

```bash
cd backend && go test ./internal/backupasset/ga ./internal/backupasset/runtime -run 'Inventory|Readiness' -count=1
```

### Task 5 — Gate TransitionFeature / settings / import

Implement the Task 2 predicate on the existing transition path. Do not add
a second feature flag. Prove config import cannot persist `true` when
blocked.

```bash
cd backend && go test ./internal/backupasset/runtime ./internal/api/handlers ./internal/settings -run 'TransitionFeature|ValidateBackupAsset|ConfigImport.*backup_assets.enabled|Enablement' -count=1
```

### Task 6 — Export Compose volume

Add the named volume, Core mount, initializer permissions in
`deploy/worker/entrypoint.sh` `initialize_volumes` (same `0700` +
`10000:10000` pattern as derived), compose-config mutations, and profile
smoke persistence. Parser/updater non-mount. Do not edit the Worker
Dockerfile, seccomp profile, or publication workflows.

```bash
bash scripts/check-compose-config.test.sh
# focused export-volume assertions inside scripts/test-asset-worker.test.sh
```

Do not change Worker image publication.

### Task 7 — Admin API/UI and disabled workspace

Thin handlers, typed frontend mapper, Admin panel, overview CTA, i18n,
axe. Viewer/Operator denied.

```bash
cd backend && go test ./internal/api/handlers -run 'BackupGA|GaReadiness|GaInventory' -count=1
cd web && npx vitest run src/lib/api/backup-ga-api.test.ts src/features/backup-assets/ga-readiness-panel.test.tsx src/features/backup-assets/ga-readiness-panel.a11y.test.tsx src/features/backup-assets/backup-assets-workspace.test.tsx
```

### Task 8 — Legacy UX redirect after parity

Add deep-link parity tests first. Then stop mounting SnapshotBrowser /
SnapshotSearch / RestoreConfirmDialog as the Tasks asset entry. Keep
legacy HTTP routes.

```bash
cd web && npx vitest run src/pages/tasks-page.test.tsx src/components/snapshot-browser.test.tsx src/features/backup-assets/backup-assets-route-state.test.ts
```

### Task 9 — Docs, env, metrics

Document export volume, readiness, new vs existing enablement. Add
`BACKUP_ASSETS_EXPORT_ROOT` to `docs/env-vars.md`. Keep Worker unpublished
to Docker Hub/GitHub Release and keep CodeDefault false. Optional
`.env.deploy` comment for new installs must still pass through the gate.

Add GA readiness/inventory/export-root/enablement-reject metrics. Implement
Prometheus for `backupasset/search` against its existing interface (it is
Noop-only today). Do not add `internal/alerting` rules in this task.

```bash
bash scripts/check-doc-freshness.sh
cd backend && go test ./internal/backupasset/ga ./internal/backupasset/search -run 'Metrics' -count=1
```

### Task 10 — Bounded load/security scripts

Add `scripts/check-backup-asset-migration.sh` and
`scripts/test-backup-asset-load.sh` with an explicit CI scale (small).
Cover pagination, Range, concurrent preview, restart-safe export/recovery
hooks already owned by Children 12/13, malformed input, ticket replay, and
audit redaction.

```bash
bash scripts/check-backup-asset-migration.sh
bash scripts/test-backup-asset-load.sh
```

### Task 11 — Cross-engine, race, privacy, GA gate

```bash
cd backend && go test ./internal/backupasset/ga ./internal/backupasset/runtime ./internal/database ./internal/api/handlers -count=1
cd backend && go test -race ./internal/backupasset/ga ./internal/backupasset/runtime -count=1
REQUIRE_POSTGRES_MIGRATION_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" go test ./internal/database -run 'BackupAssetMigration071' -count=1
cd web && npm run check
cd .. && make swag-init && git diff --check
make check
./scripts/check-compose-config.sh
./scripts/test-core-compose.sh
./scripts/test-asset-worker.sh
bash scripts/check-doc-freshness.sh
bash scripts/check-backup-asset-migration.sh
```

`make docker-build` and local Worker image build remain in this task.
Publish workflows stay no-publish.

Privacy scan must show no raw locator/proof/ticket in GA handler/UI.

Confirm `CodeDefault` is still `"false"` and
`.github/workflows/publish-images.yml` still has no Worker image.

Task 11 amendment: `recovery_runtime_test.go` may wait for the recorded
delete-pause map before offering the exact one-shot handoff. See
`design.md` §11. Do not edit `recovery_runtime.go`.

Task 11 frontend lint: GA readiness panel tests may disable
`jsx-a11y/aria-role` on the product auth-role host
(`role="admin"|"operator"|"viewer"`), matching
`mobile-navigation.test.tsx`. No new path.

### Task 12 — Independent high-risk review

Review: used-down `000071`; enablement vs drain ordering; inventory
byte-preservation; export volume isolation; legacy route leftover safety;
docs truth; no CodeDefault flip; no Worker publish.

Record `research/review.md`. Fix Critical/Important with new RED→GREEN.

### Task 13 — PR, CI, merge, post-merge

Only after Tasks 11–12:

1. inspect the exact diff against this manifest;
2. one conventional commit on `codex/backup-assets-ga-hardening`;
3. push, open PR, monitor every required CI job;
4. squash-merge only when green;
5. observe Release Please / Docker / Hub description; record whether a
   GitHub Release or image publish was expected;
6. fast-forward local `main`; archive this child;
7. leave the parent `planning` until an explicit parent-final-acceptance
   instruction. Do not archive the parent in this task.

## 4. Rollback

Set `backup_assets.enabled=false` through the existing drain. Keep
`000071` additive after use. Restore Tasks legacy UI only if those routes
are still safe. Never delete Provider points to undo GA.

## 5. Follow-up before `task.py start`

- User has approved this planning summary in a later message.
- `origin/main` still matches the recorded baseline, or evidence is
  refreshed.
- Working tree on `codex/backup-assets-ga-hardening` contains only this
  planning package.
