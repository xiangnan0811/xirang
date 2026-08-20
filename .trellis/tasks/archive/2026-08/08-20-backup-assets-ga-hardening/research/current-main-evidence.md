# Child 15 Current-Main Evidence

## Evidence boundary

- Captured: 2026-08-20 in the workspace `/home/murray/code/xirang`.
- Branch at capture: `codex/backup-assets-ga-hardening`, created from exact
  `origin/main` `61cb68d36bad0c4665959fb96d377e3cb36598c1`.
- Formal release `v0.49.1` resolves to
  `43a8d067f92dae37ba583f8348fd7441a073d0f9`. It is on the same `main` history
  and is not a separate rebase target.
- Scope sources are the live tree plus the parent `prd.md`, `design.md`, and
  `implement.md`. Parent `implement.md` §16 is a July 2026 sketch and is not an
  executable manifest.
- This evidence is planning-only. No product code, migration, generated API
  artifact, dependency, or deployment file was changed while collecting it.

## Migration and schema baseline

SQLite and PostgreSQL have a paired backup-asset chain from
`000062_backup_asset_foundation` through `000070_backup_asset_lifecycle`.
`backend/internal/database/backup_asset_migrations_integration_test.go`
records:

- `backupAssetExportVersion = 68`
- `backupAssetRecoveryVersion = 69`
- `backupAssetLifecycleVersion = 70`

No `000071*` file exists in either engine. Child 15 alone retains
`000071_backup_asset_ga`.

There is no `backend/internal/model/backup_asset_migration.go`, no
installation-class / readiness / repository-conflict table, and no
`scripts/check-backup-asset-migration.sh`.

## Feature gate

`backup_assets.enabled` remains:

- `CodeDefault: "false"` at `backend/internal/settings/service.go:296`
- `BACKUP_ASSETS_ENABLED=false` in `.env.deploy`,
  `backend/.env.production.example`, and `backend/.env.example`
- documented default `false` in `docs/env-vars.md` and
  `docs/admin/backup-recovery.md`

Admin/settings/import can already set the gate to `true`. The path is:

1. `settings.Service.ValidateBackupAssetEffectiveUpdate` validates foundation
   keys only; it does not consult installation inventory or volume readiness.
2. `backupasset/runtime.Runtime.TransitionFeature` prepares content
   enable/disable, then `AdmissionController.TransitionFeature` drains and
   switches admission mode.
3. Persistence happens only after that drain.

There is no readiness predicate that can reject `enabled=true` because
migrations, keys, export root durability, or repository-conflict review are
incomplete. Child 13 already owns a different readiness object:
`POST /api/v1/settings/backup-assets/recovery/downgrade-readiness`. That
endpoint is Recovery-schema-down fencing. Child 15 must not overload it.

## Compose, Worker, and export durability

Present on current main and **not** Child 15 recreation work:

- `deploy/worker/Dockerfile`, `entrypoint.sh`, and `seccomp.json`
- Compose profile `asset-worker` plus named volumes
  `asset-worker-worker-runtime`, `asset-worker-updater-runtime`,
  `asset-worker-bundles`, and `asset-worker-derived-store`
- Core mount of derived store at `/var/lib/xirang-asset-runtime/derived`
- CI jobs `asset-worker-closure` and `asset-worker` in
  `.github/workflows/ci.yml` (build, scan, smoke, runtime-closure; comments
  `asset-worker-closure-no-publish` and `asset-worker-no-publish`)
- Nginx `asset-content` Range/streaming/log-redaction locations in
  `deploy/nginx/templates/default.conf.template`

Absent:

- Compose named volume for `/var/lib/xirang-asset-runtime/export`
- `scripts/check-compose-config.sh` assertions for an export volume
- `BACKUP_ASSETS_EXPORT_ROOT` in `docs/env-vars.md` (the setting exists in
  `settings.Service` with CodeDefault
  `/var/lib/xirang-asset-runtime/export` and `RequiresRestart: true`)
- Worker mention in `.github/workflows/publish-images.yml`
- `scripts/check-backup-asset-migration.sh` and
  `scripts/test-backup-asset-load.sh`

`docs/deployment.md` shows `BACKUP_ASSETS_ENABLED=true` only inside the
optional `asset-worker` profile example. Official `.env.deploy` stays false.
`docs/deployment.md` §4 states the Worker is **not GA**, has no stable public
image, and will not be published to Docker Hub or GitHub Release.

## Frontend workspace and settings

Present:

- Routes `/app/backups/{overview,data,recovery}` in `web/src/router.tsx`
- Feature module `web/src/features/backup-assets/` including workspace,
  search, preview, export, archive, recovery, retention, and repository
  management
- Typed `feature_disabled` mapping in `web/src/lib/api/backup-assets-error.ts`
  and a workspace empty/blocked region in
  `backup-assets-workspace.test.tsx`
- Task-context links from `SnapshotBrowser` / `SnapshotSearch` tests into
  `/app/backups/data?taskId=`

Absent / incomplete:

- `web/src` does not read `backup_assets.enabled` as a settings value. Data
  and recovery tabs stay in the nav; the workspace fail-closes after API
  errors.
- `settings-page.system.tsx` `CATEGORY_ORDER` is
  `security, node_monitor, retention, storage, alert, anomaly`. Category
  `backup_assets` is never rendered. There is no Admin migration/preflight
  panel.
- README.md does not mention backup assets.

## Legacy snapshot UX

Still live and still the Tasks-dialog path:

- `GET /tasks/:id/snapshots`, `.../files`, `.../diff`, `.../search`
- `POST /tasks/:id/snapshots/:sid/restore` (Admin + snapshot-restore
  step-up + credential grant) in `backend/internal/api/router.go`
- `model.SnapshotFileIndex` and `snapshot_search_handler.go` `%LIKE%` search
- UI: `web/src/components/snapshot-browser.tsx`, `snapshot-search.tsx`,
  `restore-confirm-dialog.tsx`, wired from `tasks-page.dialogs.tsx`

New asset search tests already forbid `SnapshotFileIndex` as a search
source. Child 7 left the legacy index in place until GA. Lineage guards from
Child 3 still wrap the legacy snapshot routes.

## Owners already delivered by Children 1–14

Child 15 must reuse, not recreate:

| Area | Live owner |
|---|---|
| Domain, gate, leases, audit, step-up | Child 1 |
| Repository identity, connect/disconnect | Child 2 / 14 |
| Restic/Rsync/Rclone publication | Children 3–5 |
| Catalog | Child 6 |
| Search overlays | Child 7 |
| Content tickets/Range/cache | Child 8 |
| Workspace UI | Child 9 |
| Worker protocol + Derived store | Child 10 |
| Worker capabilities + Compose/CI Worker | Child 11 |
| Export jobs (process durability, not Compose volume) | Child 12 |
| Controlled recovery + Recovery downgrade-readiness | Child 13 |
| Retention, hold, import/rebuild, purge, config v2, Task archive | Child 14 |
| Command provider | typed unsupported (`domain.go` ProviderCommand) |

Child 14 import/rebuild/reconnect is the only Provider-facing cutover path.
Child 15 inventory is a dry-run classification and conflict record on top of
those facts. It must not invent a second connect/import state machine or
mutate Provider bytes.

## Parent §16 stale file list

Files the July 15 parent plan told Child 15 to **create** that already exist:

- `backend/internal/api/handlers/snapshot_handler_test.go`
- `backend/internal/api/handlers/snapshot_diff_handler_test.go`
- `deploy/worker/Dockerfile`, `entrypoint.sh`, `seccomp.json`

Files it told Child 15 to **modify** that have since been rewritten by later
children and must be re-inspected before edit: backups workspace pages,
settings handler, router snapshot routes, Worker CI, public docs.

## Observability

Prometheus implementations exist for publication, catalog, content,
processing, export, recovery, and retention. `search/metrics.go` exposes a
`Metrics` interface and `NoopMetrics` only; there is no search Prometheus
implementation. There is no `xirang_backup_asset_repository_*` family, no
dedicated mutable-head observed/retired/age gauges, and no backup-asset
rules in `backend/internal/alerting`.

Child 15 still needs installation-readiness, inventory-result, enablement
reject, and export-root probe metrics. Path/entry/user labels remain
forbidden. Parent design §18.3 repository-health gauges and a full alerting
product are residuals, not a reason to recreate the owners above.

## Planning conclusion

The focused Child 15 boundary is:

1. paired `000071` installation/readiness/conflict schema;
2. dry-run inventory that never changes Provider bytes;
3. enablement gate that rejects `backup_assets.enabled=true` until readiness
   passes;
4. durable Compose export volume and tests;
5. Admin migration/readiness UI plus truthful disabled workspace;
6. legacy snapshot UX redirect after deep-link parity, with compatibility
   routes until a documented removal;
7. docs/env/compose truth, bounded load/security scripts, and the parent GA
   gate.

Out of the first implementation waves: flipping `CodeDefault` to `true`,
publishing a public Worker image, recreating Worker/derived volumes, and
reopening Children 1–14 owners.
