# Task 12 — Independent high-risk review

Reviewed: 2026-08-20 on `codex/backup-assets-ga-hardening` at
`/home/murray/code/xirang`. Evidence is live code, paired SQL, and the
focused selectors below. Parent `implement.md` §16 was not used.

Counts after this review (including fixes landed in this task):

| Severity | Found | Remaining open |
|---|---|---|
| Critical | 2 | 0 |
| Important | 3 | 0 |
| Suggestion | 4 | 4 |

Do not treat this as Task 13. No commit, push, PR, merge, or parent archive.

## Contract ledger

### 1. `000071` used-down fail-closed

SQL on both engines rejects down when an installation row is `ready` or
`acknowledged`, `enablement_succeeded_at` is non-null, or any conflict row
exists. Pristine empty down still applies. Integration seeds cover all four
families (`ReadyInstallation`, `AcknowledgedInstallation`,
`RepositoryConflict`, `SuccessfulEnablement`).

**Critical (fixed):** production never wrote `enablement_succeeded_at`. A
fresh install that completed inventory (stored `unknown`, no conflicts) and
then enabled could drop `000071` and erase the only durable enablement proof.

**Important (fixed):** production never wrote stored `ready`. Inventory persist
intentionally stays `unknown` (`inventory must not promote readiness`). Used-down
`ready` was therefore dead unless a test seeded the column. Computed-ready
inventory proof could be deleted by schema down.

Fixes: `InventoryService.MaterializeReadiness` writes `ready` after a passing
computed snapshot; `RecordEnablementSucceeded` stamps `enablement_succeeded_at`
once inside the enable persist callback. Dry-run persist still does not
self-promote.

### 2. Enablement vs drain ordering

`Runtime.TransitionFeature(true)` calls `authorizeEnablement` /
`EvaluateEnablement` before content prepare and before
`AdmissionController.TransitionFeature`. Blocked/ack-missing paths do not
persist and do not emit admission-true. Disablement still prepares content
disable and calls inner `TransitionFeature(false)`.

**Critical (fixed):** `AdmissionController.Initialize` still selects
`AdmissionManaged` from `Foundation.FeatureEnabled()` alone. `StartupPass`
called Initialize first, so `BACKUP_ASSETS_ENABLED=true` or a DB override
became managed without inventory/ack. That skipped the documented new-install
gate.

Fix: `StartupPass` now runs `authorizeRequestedStartupEnablement` before
`Initialize`. Requested enable + blocked readiness returns
`ga.ErrEnablementBlocked` and leaves admission uninitialized.

AdmissionController isolated tests keep the old env→managed behavior. The
gate is the Runtime predicate, not the controller.

### 3. Inventory never mutates Provider bytes

`DryRun` loads Task / link / identity / latch facts, classifies in Go, and
persists run + conflicts. `ProviderMutationSurface` is composed as
`forbiddenGAMutations` and is not invoked (`_ = service.mutations`). Handler
fakes prove inventory does not call import/rebuild/purge. SnapshotFileIndex
paths are ignored and scanned out of the document.

### 4. Export volume isolation

`docker-compose.yml` mounts `asset-worker-export-store` on Core and
`asset-worker-init` at `/var/lib/xirang-asset-runtime/export`. Parser
(`asset-worker`) and updater do not list that source or target.
`scripts/check-compose-config.sh` asserts the same isolation. Dockerfile /
seccomp / publish workflows were not edited.

### 5. Legacy HTTP and leftover UI

Router still registers lineage-guarded

- `GET /tasks/:id/snapshots`
- `GET /tasks/:id/snapshots/:sid/files`
- `POST /tasks/:id/snapshots/:sid/restore`
- `GET /tasks/:id/snapshots/diff`
- `GET /tasks/:id/snapshots/search`

Files `snapshot-browser.tsx`, `snapshot-search.tsx`, and
`restore-confirm-dialog.tsx` still exist. `tasks-page.dialogs.tsx` and
`tasks-page.tsx` no longer mount those components; history uses
`BackupAssetsTaskContextLink` plus workspace/recovery hrefs.

### 6. Docs truth

Public docs keep Worker unpublished, CodeDefault `false`, official image
`linnea7171/xirang`, and port `10761`. `.env.deploy` still sets
`BACKUP_ASSETS_ENABLED=false` and says a new-install `true` still has to pass
the gate. README matches that paragraph.

### 7. `backup_assets.enabled` CodeDefault

`backend/internal/settings/service.go` remains `CodeDefault: "false"`.
`TestBackupAssetsEnabledCodeDefaultRemainsFalse` still passes.

### 8. `publish-images.yml`

`.github/workflows/publish-images.yml` publishes only
`docker.io/linnea7171/xirang`. No Worker / `xirang-asset-worker` image.

### 9. `backend/cmd/server/main.go`

`git diff origin/main -- backend/cmd/server/main.go` is empty. GA composes
inside `backupasset/runtime`.

### 10. `CATEGORY_ORDER`

`web/src/pages/settings-page.system.tsx` is still
`security, node_monitor, retention, storage, alert, anomaly`. No
`backup_assets`. Admin GA lives on the dedicated panel.

### 11. GA public JSON/UI leak surface

`publicBackupGAReport` emits counts, closed conflict kinds, opaque 32-hex
repository IDs, and 64-hex digests. Candidates, identity keys, locators,
proofs, tickets, and `SnapshotFileIndex` are stripped. Frontend mapper accepts
only those closed fields. Handler test
`TestGaInventoryHandlerStripsLocatorProofAndSnapshotIndex` remains green.

### 12. Fresh never returns after existing

`lockInstallationClass` returns `existing` whenever a persisted row is
`existing`. Persist + empty rerun fixture
`existing_class_never_reverses_to_fresh` still passes. Class may move
`fresh` → `existing` only.

### 13. AdmissionController is not the enablement gate

`FeatureTransitioner()` returns `*Runtime`, not `*AdmissionController`.
Settings/import/startup go through `authorizeEnablement` /
`EvaluateEnablement`. `EnablementGate` remains a unit-test wrapper; production
does not make the controller the gate.

## Fixes (RED → GREEN)

See `research/red-green.md` Task 12.

1. Startup requested-enable without readiness must not initialize admission.
2. Successful enablement stamps `enablement_succeeded_at` once.
3. Passing computed readiness materializes stored `ready` without changing
   DryRun persist.
4. `composeGAReadiness` uses `ValidateBackupAssetFoundationConfig` and
   `Keyring.EnsureRequiredDomains` instead of no-op probes.
5. Settings PUT, settings DELETE-restore, and config import map
   `ga.ErrEnablementBlocked` / `ga.ErrEnablementAckRequired` to HTTP 409
   `就绪检查未完成`, matching dedicated GA routes. Unexpected transition
   failures stay HTTP 500.

`TestRuntimeStartupManagedModeRequiresInterruptedRunReadiness` now injects a
passing GA snapshot so it still proves the Child 13 TaskRun-readiness error,
not a weakened assertion.

## Residual risks / suggestions

1. `AdmissionController.Initialize` by itself still treats
   `FeatureEnabled()==true` as managed. Safe only because production
   `StartupPass` authorizes first. Do not call Initialize from a new path
   without the predicate.
2. Rsync/Rclone versioning still type-asserts `service.admission` to
   `FeatureTransitioner` for already-enabled activations (`ensureEnabled`
   first). Not a first-enable bypass; do not copy that into a new enable
   path.
3. Used-down still allows `unknown`/`blocked` with no conflicts and no
   enablement stamp (failed inventory, never enabled). That matches design
   3.4; complete passing inventory now materializes `ready`.
4. `KeysReady` may create missing required domain keys during a readiness
   read. Fail-closed if encryption is unavailable. `ExportRootValid` is the
   whole live foundation config, so an unrelated invalid `backup_assets.*`
   value also reports export-not-valid.
5. Task 11 leftovers: official `make check` can still fail on this host’s
   14G `/tmp` quota; live Worker image smoke was static-only. Not GA
   product defects.
6. Ack audit log hard-codes `conflicts=0`. Counts stay off the public
   payload; the log line is incomplete, not a leak.
7. **Important (fixed):** settings PUT and config import used to map
   enablement sentinels through `respondInternalError` (HTTP 500). DELETE
   leaked the English sentinel as HTTP 400. Shared helper
   `respondBackupAssetEnablementConflict` now returns the stable 409
   message on PUT, DELETE-restore, import, and dedicated GA routes.

## Verification (this task)

```text
go test ./internal/backupasset/ga ./internal/backupasset/runtime \
  ./internal/api/handlers ./internal/settings ./internal/database \
  -run 'TestEnablementRequiresReadiness|TestExistingInstallRequiresAck|TestBackupAssetsEnabledCodeDefaultRemainsFalse|TestTransitionFeature|TestInventoryDryRun|TestInventoryRuntime|TestStartupRequestedEnablement|TestComposeGAReadiness|TestGaInventory|TestGaReadiness|BackupAssetMigration071|ConfigImport.*backup_assets.enabled|ValidateBackupAsset' \
  -count=1
```

Exit 0. PostgreSQL 000071 was not re-run in this review slice; Task 11 already
recorded required-DSN GREEN. Task 13 is not started.
