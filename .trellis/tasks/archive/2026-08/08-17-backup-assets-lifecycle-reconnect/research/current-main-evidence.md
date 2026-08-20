# Child 14 Current-Main Evidence

## Evidence boundary

- Captured: 2026-08-17 in the dedicated Codex worktree
  `/home/murray/.codex/worktrees/0334/xirang`.
- Branch: `codex/backup-assets-lifecycle-reconnect`.
- `HEAD` and fetched `origin/main` were both
  `19140e80ac34ebab26bc1f8ed141965c5f4d3cdd` before task creation.
- Formal release `v0.48.0` resolves to
  `793af9f8c29ba6274d4f99c1e104fcc01a72752c`; it is not the Child 14 base.
- Scope sources are the live tree plus the parent `prd.md`, `design.md`, and
  `implement.md`. No old conversation or handoff was consulted.
- This evidence is planning-only. No product code, migration, generated API
  artifact, dependency, or deployment file was changed while collecting it.

## Migration and schema baseline

- SQLite and PostgreSQL have a paired backup-asset chain from `000062` through
  `000069_backup_asset_recovery`.
- `backend/internal/database/backup_asset_migrations_integration_test.go`
  records `backupAssetRecoveryVersion = 69` and has paired SQLite, required
  PostgreSQL, paired-file, pristine-down, and used-down coverage for `000069`.
- No `000070*` or `000071*` migration exists in either engine.
- Child 14 therefore owns paired
  `000070_backup_asset_lifecycle.{up,down}.sql`. Child 15 alone retains `000071`.
- Existing schema already contains the following facts and they must not be
  recreated:
  - `RecoveryPoint` states `observed`, `retired`, `preparing`, `verifying`,
    `committed`, `degraded`, `expiring`, `expired`, `failed`, and
    `purge_blocked`;
  - `RecoveryPoint` hold projection, retention deadline, retirement reason,
    retirement time, encrypted Provider and rollback locators;
  - `RecoveryPointLease` with attempt/fence/heartbeat/absolute-deadline fields;
  - permanent installation/repository managed-history latches created by
    `000064`;
  - closed audit segments, chained checkpoints, and detail-purge state.

## Missing Child 14 owners

The following planned core paths are absent on current main:

- `backend/internal/model/backup_asset_lifecycle.go`
- `backend/internal/backupasset/retention/`
- `backend/internal/api/handlers/backup_retention_handler.go`
- `web/src/features/backup-assets/repository-management-panel.tsx`
- `web/src/features/backup-assets/retention-policy-panel.tsx`

No retention-policy table, hold-record table, lifecycle attempt/tombstone,
import-review queue, exact RecoveryPoint deletion port, or backup-asset
retention worker exists.

## Legacy retention path

`backend/internal/task/retention.go` remains the legacy source of retention
effects:

- Rsync selects subdirectories by filesystem `ModTime()` and calls
  `os.RemoveAll`.
- Restic assembles `forget ... --prune` from task-policy age/GFS arguments.
- Rclone calls `delete ... --min-age`.
- The existing lineage guard correctly blocks these paths for managed history
  and preserves a pristine-legacy compatibility path.
- Existing tests prove blocking and pristine compatibility, but there is no
  exact RecoveryPoint selector/owner to which a managed Task can delegate.

Child 14 must keep the fail-closed lineage guard while moving managed retention
authority to deterministic immutable RecoveryPoint IDs. Provider deletion may
receive only a private exact point locator selected by that owner; it may not
receive age, path glob, `latest`, snapshot prefix, repository-wide selector, or
user-supplied remote/path material.

## Repository and reconnect baseline

The live Provider boundary is split across `provider/contracts.go`,
`provider/registry.go`, and Provider-specific files. The historical parent
manifest entry `provider/provider.go` does not exist.

The current registry exposes narrow read, catalog, publication, and restore
ports. It has no exact `PointDeleter` or repository purge port. Existing read
adapter tests deliberately reject mutation commands, so Child 14 deletion must
be a separately registered narrow capability and must not silently widen a
read adapter.

Repository lifecycle is partially present and should be reused:

- `repository.Service.Connect` derives an access binding from a non-archived
  Task, probes before writing, validates Provider identity, and rejects identity
  mismatch.
- A disconnected repository can reconnect through the same `Connect` endpoint;
  `TestDisconnectPreservesMutableEvidenceAndReconnectsWithRetainedSalt` proves
  repository ID and non-retired mutable-head ID reuse.
- `Reconcile` preserves last-good identity and marks only safe offline facts on
  failure. It rejects identity mismatch.
- `Disconnect` revokes the active binding and keeps repository, Catalog, and
  mutable-head evidence offline.
- The backend handler already exposes connect, list, detail, reconcile, and
  disconnect. The frontend API wrapper exposes only list and detail.

Consequently Child 14 will strengthen and expose the existing reconnect
contract rather than introduce a second reconnect state machine. Import
discovery, unknown-point review, accepted-manifest rebuild, and purge planning
remain absent.

## Existing contracts that remain authoritative

- `backend/internal/backupasset/domain.go` owns RecoveryPoint, retirement,
  hold-projection, and ordinary lease-holder enums.
- `backend/internal/backupasset/lease.go` owns acquire/renew/release/takeover
  fencing. Search adds the existing `search_index` holder.
- `backend/internal/backupasset/audit_action.go` already defines repository
  import/review/purge-plan/purge, retention-policy CRUD, and hold create/release
  actions. Child 14 uses them; it does not add aliases.
- `backend/internal/backupasset/audit.go` already closes segments, links
  checkpoints, verifies the chain, and provides `PurgeSegmentDetails`.
- `web/src/types/domain.ts` already defines repository and RecoveryPoint states,
  `RecoveryPointHoldState`, and overlay tombstone reasons.
- `backup_repositories:purge` and the step-up action `repository.purge` already
  exist. Hold release has no purpose-exact step-up action yet.

## Dependent cleanup owners

Child 14 must coordinate through owner-specific ports rather than delete their
tables directly:

- Content Broker already owns session/result grant revoke and drain, but lacks
  a RecoveryPoint-wide source lifecycle operation.
- Catalog Indexer owns active build reconciliation and needs an exact-point
  retire/purge operation.
- Search owns generations, documents, postings, and content projections and
  needs exact-point invalidation.
- Processing `DerivedLifecycle` owns artifact-set revocation; an exact-point
  coordinator is missing.
- Export `Lifecycle` already owns `FailSourceExpired`, delivery revoke/drain,
  key destruction, ciphertext purge, and source lease release; a bounded
  exact-point entry point is missing.
- Overlay `Service.ReconcileSource` already converts saved searches to broken,
  favorites/tags to opaque tombstones, and deletes recent access. Reuse it.
- Recovery owns active source leases/jobs. Child 14 may cancel/fence jobs that
  read the expiring point, but must not take over RecoveryResult cleanup.

## Child 13 boundary

`backend/internal/backupasset/recovery/result_lifecycle.go` and the owner in
`backend/internal/backupasset/runtime/recovery_runtime.go` independently own
RecoveryResult and recovery-workspace revoke, drain, validation, deletion, and
tombstoning. Child 14 may call a narrow Recovery source-interest cancellation
port before point retirement/purge. It must not change, merge, bypass, or
terminalize the RecoveryResult cleanup state machine, and it must not edit
Child 13 historical research.

## Task deletion baseline

- `model.Task` already has nullable `ArchivedAt`.
- `TaskHandler.Delete` currently counts dependents, directly executes
  `db.Delete(&model.Task{}, id)`, then removes the schedule.
- The only focused delete regression asserts that a DB delete failure does not
  remove the schedule.
- `TaskRepositoryLink` already stores immutable Task/node name snapshots and a
  nullable Task ID.

Child 14 must replace HTTP hard-delete with transactional archive + unlink,
disable future scheduling, remove the schedule after commit, and preserve
Repository/RecoveryPoint/run/audit history. Physical Provider deletion remains
an explicit lifecycle action and never follows Task archive.

## Config transfer baseline

- Config export currently emits `version: "1.0"` and nodes, SSH keys, policies,
  Tasks, and settings.
- Config import accepts the wrapped data form or legacy direct data and does not
  enforce a top-level version.
- Existing sensitive export is already protected by Admin, purpose-exact
  config-export step-up, and its credential-grant middleware.
- Existing import deliberately disconnects foreign managed Rsync/Rclone task
  configuration and performs no Provider mutation.
- Repository/link/access-binding metadata is not exported or imported.

Child 14 adds a v2 stable-reference graph, keeps v1 compatibility, defaults
imported repositories/bindings to disconnected, and performs no probe or
Provider mutation until an explicit reconnect. Numeric source DB IDs, proofs,
grants, tickets, and raw private locators remain outside the ordinary envelope.
Reconnectable secret bindings may appear only in the existing explicitly
sensitive export channel.

## Frontend baseline

- `backup-repositories-api.ts` maps safe repository DTOs and provides list/get
  only.
- `backup-assets-workspace.tsx` embeds a large read-only
  `RepositoryManagementView` rather than using a dedicated panel.
- Its test explicitly asserts that reconnect and purge controls are absent.
- `backups-page.data.tsx` already has token, role, user ID, and
  `ensureStepUpProof`, so the lifecycle panels can receive a narrow Admin
  runtime without bypassing auth context.
- Existing domain types already carry lifecycle/hold projection values; only
  policy, hold-record, import-review, impact-plan, and mutation-result DTOs are
  new.

## Runtime and feature gate

- `backupasset/runtime.Runtime` is the composition root for repository,
  catalog, search, content, processing, export, and recovery owners. Its
  startup/run/shutdown ordering is the correct home for the new lifecycle
  coordinator and worker.
- `backend/cmd/server/main.go` already runs the asset runtime as a lifecycle
  worker. Child 14 should add the new owner inside that runtime instead of
  creating an unrelated unjoined goroutine.
- `backup_assets.enabled` has code default `false`, production example default
  `false`, and documentation saying false. It remains false throughout Child 14.

## Child 15-owned live capabilities and exclusions

Current main already contains:

- `deploy/worker/Dockerfile`, entrypoint, and seccomp profile;
- the Compose `asset-worker` profile and durable Worker volumes;
- dual-architecture Worker runtime-closure, build, scan, and smoke CI jobs.

Those are a Child 15 rebaseline concern. Child 14 will not modify `.github/`,
`deploy/`, Docker/Compose metadata, public image publication, GA enablement,
legacy snapshot UI removal, release tagging, or dependency lockfiles. The
recorded npm-audit residual risk does not authorize dependency upgrades.

## Planning conclusion

The focused implementation boundary is:

1. paired `000070` lifecycle schema and fail-closed down admission;
2. policy/hold/import-review/lifecycle-attempt/tombstone records;
3. one RecoveryPoint-ID selector/coordinator and one joined retention worker;
4. owner-specific revoke/drain/cleanup ports, preserving RecoveryResult
   ownership;
5. separately registered exact Provider deletion;
6. Task archive/unlink;
7. reuse of current reconnect plus new import/rebuild/review and purge planning;
8. typed Admin APIs/UI, config v2, DR behavior/docs, dual-engine and fault gates;
9. independent review, PR/CI, merge/post-merge observation, and task archive.

Implementation remains unauthorized until the user approves all Child 14
planning artifacts and explicitly permits `task.py start`.
