# Rsync 版本化恢复点 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: 仅在获得 task.py start 的单独授权后，使用 superpowers:executing-plans 在 inline 主会话逐任务执行。本 Child 明确禁止 implement/check sub-agent。

**Goal:** 为 Rsync Task 增加可预检、可调和、可证明的版本化 RecoveryPoint 发布，同时保留 legacy mutable 路径和独立的 rollback locator。

**Architecture:** 复用 Child 3 的 publication coordinator、lease、fence、worker 和 admission，将 Restic-only payload/dispatch 收敛成严格的 provider-tagged strategy。RsyncTreeStrategy 在 encrypted binding v2 指向的 managed root 内完成 fresh staging、canonical manifest、同 mount NOREPLACE rename 和精确 reconciliation；数据库只在可证明的 provider commit 后发布。

**Tech Stack:** Go 1.26、GORM、SQLite/PostgreSQL paired migrations、Linux openat2/renameat2/statx、rsync、Gin、React 18、TypeScript strict、Vite、Vitest、Tailwind/Radix UI。

---

## 0. Authority, Gates, And Baseline

- 当前状态是 planning。design.md 与本 implement.md 均已获用户批准；仍须单独明确授权 `task.py start`。
- 在用户明确授权 task.py start 前，不得修改 backend/web 产品文件、migration、生成 Swagger、提交、push 或建 PR。
- 当前工作分支是 codex/backup-assets-rsync-versioning；不得覆盖父任务已有的未提交 planning edits。
- 默认实现模式是 inline。不要为实现、代码审查或检查派发子代理。
- Child 4 只拥有 000064_backup_asset_rsync_publication_contract。父计划的后续 reservation 已是 000065--000071；执行开始前必须重新检查两个 migration 目录和 main。
- 功能代码与 Trellis 归档必须处于同一分支、同一 PR：完整实现/验证 → Phase 3.4 单个工作提交 → trellis-finish-work 自动归档/journal 提交 → push/PR/required CI/merge/post-merge monitor/main sync。

## 1. File Ownership And Dependency Order

| Area | Create | Modify |
| --- | --- | --- |
| Migration/latch | paired 000064 SQL、backend/internal/model/backup_asset_managed_history_latch.go | migration harness、managed history resolver、domain/model tests |
| Tagged publication | provider/rsync_publication.go and tests | provider contracts/registry、publication contracts、Restic strategy adapters |
| Linux tree primitive | provider/rsync_tree_linux.go, rsync_tree_other.go, rsync_preflight.go and tests | provider/rsync.go |
| Repository lifecycle | repository/rsync_publication_execution.go, rsync_publication_reconcile.go and tests | binding.go, connect.go, managed_history.go, publication execution/reconcile |
| Task runtime | task/executor/rsync_publication_executor.go and tests | executor factory, publication runner, interrupted recovery, runner, manager |
| Runtime/admission | no parallel state machine | runtime.go, admission.go, controller.go, publication_worker.go and tests |
| API/config | handlers/task_rsync_versioning_handler.go and tests | task_handler.go, router.go, config_handler.go, task service and tests |
| Frontend | components/task-rsync-versioning-dialog.tsx and test | domain.ts, tasks-api.ts/tests, task editor fragments/tests, page dialogs, zh.ts, en.ts |
| Documentation/quality | no public catalog/restore docs | docs/admin/backup-recovery.md for the admin migration workflow; Phase 3.3 records any reusable contract through trellis-update-spec |

Implementation order is fixed:

1. paired migration and durable latch;
2. strict tagged provider publication boundary;
3. binding v2, managed-history/admission and secure filesystem/preflight;
4. managed Rsync provider execution and coordinator/task integration;
5. exact reconciliation/worker/shutdown/reader;
6. task service, API/config import and frontend;
7. integration, race, migration parity and full gates.

## 2. Task 1: Establish The 000064 Latch Contract

**Files:**

- Create: backend/internal/database/migrations/sqlite/000064_backup_asset_rsync_publication_contract.up.sql
- Create: backend/internal/database/migrations/sqlite/000064_backup_asset_rsync_publication_contract.down.sql
- Create: backend/internal/database/migrations/postgres/000064_backup_asset_rsync_publication_contract.up.sql
- Create: backend/internal/database/migrations/postgres/000064_backup_asset_rsync_publication_contract.down.sql
- Create: backend/internal/model/backup_asset_managed_history_latch.go
- Modify: backend/internal/database/backup_asset_migrations_integration_test.go
- Modify: backend/internal/backupasset/repository/managed_history.go
- Modify: backend/internal/backupasset/repository/managed_history_test.go
- Modify: backend/internal/backupasset/domain.go
- Modify: backend/internal/backupasset/domain_test.go

- [ ] **Step 1: Add red dual-engine migration cases before SQL exists.**

Extend the existing migration harness with version-64 parents for SQLite and required PostgreSQL. The common contract must prove:

~~~go
func runBackupAssetMigration064Contract(t *testing.T, fixture migrationFixture) {
    t.Run("ApplyAndParity", fixture.test064ApplyAndParity)
    t.Run("BackfillsEveryResticManagedState", fixture.test064BackfillsResticHistory)
    t.Run("AllowsPristineMutableHead", fixture.test064AllowsPristineMutableHead)
    t.Run("ManagedTreeSourceFingerprintIsRepositoryScoped", fixture.test064ManagedTreeSourceUnique)
    t.Run("DownRejectsLatch", fixture.test064DownRejectsLatch)
    t.Run("DownRejectsManagedPointAndVersionedLink", fixture.test064DownRejectsManagedHistory)
    t.Run("DownRejectsPublicationAndParentLease", fixture.test064DownRejectsActiveLeases)
    t.Run("RejectedDownLeavesSchemaAndRowsUntouched", fixture.test064DownIsAtomic)
}
~~~

Use all RecoveryPoint states, nullable producing Task/TaskRun FKs and Restic native_snapshot history. Assert imported_baseline and xirang_manifest source fingerprints conflict only in the same repository, while the same fingerprint is legal in a different repository.

- [ ] **Step 2: Run the migration tests red.**

Run:

~~~bash
go -C backend test ./internal/database -run 'TestBackupAssetMigration064' -count=1
~~~

Expected: the SQLite parent fails because migration 000064 is absent. PostgreSQL may skip only when TEST_POSTGRES_DSN is not configured.

- [ ] **Step 3: Define the durable model and resolver tests.**

Add model/resolver tests for a single installation latch, one repository latch per opaque repository ID, native_snapshot backfill, managed Rsync history in preparing/failed/committed states, a retained versioned link, and active rsync_parent lease. The resolver must distinguish a pristine mutable_head-only repository from managed history.

- [ ] **Step 4: Implement the paired SQL contract.**

Create backup_asset_managed_history_latches with a scope check, non-cascading repository reference, opaque repository identity digest, first semantics/origin and UTC timestamps. Create partial unique indexes for the installation scope and repository scope, then create:

~~~sql
CREATE UNIQUE INDEX idx_recovery_points_managed_tree_source_unique
    ON recovery_points(repository_id, source_fingerprint)
    WHERE semantics IN ('xirang_manifest', 'imported_baseline')
      AND source_fingerprint <> '';
~~~

The up path backfills installation/repository latches from every existing native_snapshot row, never from mutable_head. Use stored UTC timestamps deterministically; do not insert local-time migration values when an existing row supplies the fact.

PostgreSQL down starts with a transaction and raises before DDL if a latch exists, a managed point exists in any state, a native_snapshot/versioned_hardlink/versioned_full_copy link exists, or an active point_publication/rsync_parent lease exists. SQLite runs an equivalent failing checked guard before any rebuild/drop statement. Both down paths preserve committed provider trees because they only alter schema after all guards pass.

- [ ] **Step 5: Implement the Go model and managed-history query changes.**

Expose only internal model fields. Update ManagedHistoryResolver to query latches before points/tombstones, count native_snapshot, xirang_manifest and imported_baseline across all states, and recognize both active publication holder types. Keep the public DTO free of latch identity/digest.

- [ ] **Step 6: Run focused tests and format.**

Run:

~~~bash
gofmt -w backend/internal/model/backup_asset_managed_history_latch.go backend/internal/backupasset/repository/managed_history.go backend/internal/backupasset/repository/managed_history_test.go
go -C backend test ./internal/database ./internal/backupasset ./internal/backupasset/repository -run 'Migration064|ManagedHistory|PublicationMode' -count=1
~~~

Expected: all executed focused tests pass; absent PostgreSQL configuration is reported as SKIP, not pass-by-omission.

## 3. Task 2: Generalize Publication Without Weakening Restic

**Files:**

- Modify: backend/internal/backupasset/provider/contracts.go
- Modify: backend/internal/backupasset/provider/contracts_test.go
- Modify: backend/internal/backupasset/provider/registry.go
- Modify: backend/internal/backupasset/provider/registry_test.go
- Modify: backend/internal/backupasset/publication/contracts.go
- Modify: backend/internal/backupasset/publication/contracts_test.go
- Modify: backend/internal/backupasset/repository/publication_execution.go
- Modify: backend/internal/backupasset/repository/publication_execution_test.go
- Modify: backend/internal/backupasset/repository/publication_reconcile.go
- Modify: backend/internal/backupasset/repository/publication_reconcile_test.go
- Modify: backend/internal/backupasset/provider/restic_publication.go
- Modify: backend/internal/backupasset/provider/restic_publication_test.go

- [ ] **Step 1: Add contract red tests for strict provider dispatch.**

Add tests proving a ResticAttemptV1 cannot decode as RsyncTreeAttemptV1, an unknown provider/version/field is rejected, registry lookup fails when a provider has no publisher strategy, and the Restic publication behavior remains compatible for its known-success, missing-summary and rewritten-tag fixtures.

- [ ] **Step 2: Run the provider/publication tests red.**

Run:

~~~bash
go -C backend test ./internal/backupasset/provider ./internal/backupasset/publication ./internal/backupasset/repository -run 'Tagged|Strategy|Restic.*Publication|PublicationExecution' -count=1
~~~

Expected: failures name the absent tagged strategy boundary rather than silently accepting a generic JSON value.

- [ ] **Step 3: Replace Restic-only payload flow with a closed tagged union.**

Introduce provider-specific V1 attempt and commit payloads plus a PublicationStrategy interface registered by backupasset.ProviderKind. The shared coordinator receives only opaque repository/point/attempt IDs, revision, lease fence, immutable deadline, audit context and a typed strategy result. It must not accept map[string]any, json.RawMessage, generic interface payloads or arbitrary provider field maps.

~~~go
type PublicationStrategy interface {
    Kind() backupasset.ProviderKind
    Prepare(context.Context, PublicationPrepareRequest) (PreparedPublication, error)
    Execute(context.Context, PreparedPublication, PublicationProgress) (ProviderExecutionResult, error)
    RecordCommit(context.Context, PreparedPublication, ProviderExecutionResult) (ProviderCommit, error)
    VerifyOrBuildManifest(context.Context, PreparedPublication, ProviderCommit) (ManifestResult, error)
    Reconcile(context.Context, PublicationReconcileRequest) (PublicationReconcileResult, error)
}
~~~

provider/contracts.go owns PublicationStrategy and the strict Restic/Rsync V1 payloads; publication/contracts.go owns only coordinator request/outcome types. Rename the current Restic-only payload structs as part of this single owner migration so no Go name collision remains.

- [ ] **Step 4: Adapt Restic through the new boundary before adding Rsync.**

Wrap existing Restic tag/summary/manifest logic in the Restic strategy. Preserve its exact two-tag rule, fence validation, safe errors, known-exit-zero behavior, worker and reconciler semantics. Existing Restic fixtures remain the regression corpus; add typed-envelope assertions beside their current tests.

- [ ] **Step 5: Run compatibility tests.**

Run:

~~~bash
go -C backend test ./internal/backupasset/provider ./internal/backupasset/publication ./internal/backupasset/repository ./internal/task -run 'Restic|Publication|Lineage|Interrupted' -count=1
~~~

Expected: existing Restic lineage tests pass unchanged in behavior; Rsync strategy remains unavailable until Task 5.

## 4. Task 3: Binding V2, Managed Identity, And Admission Floors

**Files:**

- Modify: backend/internal/backupasset/repository/binding.go
- Modify: backend/internal/backupasset/repository/binding_test.go
- Modify: backend/internal/backupasset/repository/connect.go
- Modify: backend/internal/backupasset/repository/connect_test.go
- Modify: backend/internal/backupasset/repository/managed_history.go
- Modify: backend/internal/backupasset/repository/lineage_guard.go
- Modify: backend/internal/backupasset/repository/lineage_guard_test.go
- Modify: backend/internal/backupasset/runtime/admission.go
- Modify: backend/internal/backupasset/runtime/admission_test.go
- Modify: backend/internal/backupasset/runtime/controller.go
- Modify: backend/internal/backupasset/runtime/admission_controller_test.go

- [ ] **Step 1: Add v2 codec and mutable-fallback red tests.**

Cover v1 legacy round-trip, v2 managed Rsync round-trip, v1-only decode rejection of a v2 document, unknown v2 fields, root-marker/Task/link identity drift, encrypted locator serialization, and an installation latch that blocks ambiguous legacy fallback without blocking an unrelated pristine task with an exact mutable binding.

- [ ] **Step 2: Implement bindingDocument v2 as a separate strict schema.**

v2 contains provider=rsync, identity class=xirang_managed_repository, layout revision, opaque repository/link IDs, encrypted managed-root locator, marker digest, mode and identity salt. It excludes Task.RsyncTarget from managed root ownership. New code supports v1 and v2 through exact version dispatch; old or unsupported documents fail closed.

- [ ] **Step 3: Tighten repository connect/link behavior.**

Retain Child 2 mutable Rsync connect behavior for legacy links. Add a dedicated migration/activation path for managed Rsync so ensureTaskLink cannot silently turn a mutable link into a versioned link. Require expected revision, preflight identity and fence for mode changes. Retain EncryptedLegacyLocator unchanged.

- [ ] **Step 4: Expand admission and downgrade preparation tests.**

Test that feature disable never routes a managed link to the legacy executor, reader, restore, anomaly or retention path; test drain/revoke/join before rollback preparation; test that rollback only restores the legacy locator to a paused Task after proving physical separation from managed root.

- [ ] **Step 5: Implement resolver/admission/controller changes and run focused tests.**

Run:

~~~bash
gofmt -w backend/internal/backupasset/repository/binding.go backend/internal/backupasset/repository/connect.go backend/internal/backupasset/repository/managed_history.go backend/internal/backupasset/repository/lineage_guard.go backend/internal/backupasset/runtime/admission.go backend/internal/backupasset/runtime/controller.go
go -C backend test ./internal/backupasset/repository ./internal/backupasset/runtime -run 'Binding|Connect|ManagedHistory|Lineage|Admission|Downgrade' -count=1
~~~

Expected: v1 compatibility remains green, all versioned/mismatch paths fail closed.

## 5. Task 4: Implement Linux Managed-Tree Primitives And Preflight

**Files:**

- Create: backend/internal/backupasset/provider/rsync_tree_linux.go
- Create: backend/internal/backupasset/provider/rsync_tree_other.go
- Create: backend/internal/backupasset/provider/rsync_tree_test.go
- Create: backend/internal/backupasset/provider/rsync_tree_linux_test.go
- Create: backend/internal/backupasset/provider/rsync_preflight.go
- Create: backend/internal/backupasset/provider/rsync_preflight_test.go
- Modify: backend/internal/backupasset/provider/rsync.go
- Modify: backend/internal/backupasset/provider/rsync_test.go

- [ ] **Step 1: Write temp-filesystem tests before syscall code.**

Use t.TempDir and a second mount fixture where available. Cover fresh empty staging, rejected nonempty/reused staging, hardlink probe success, EPERM/EXDEV simulation, same-mount NOREPLACE rename, existing-final collision, source/root ancestor overlap, symlink escape, root replacement, fsync failure injection, capacity/inode/nlink estimate handling, and non-Linux fail-closed behavior.

- [ ] **Step 2: Run the filesystem preflight tests red.**

Run:

~~~bash
go -C backend test ./internal/backupasset/provider -run 'Rsync(Tree|Preflight)' -count=1
~~~

Expected: tests fail because RsyncTreePreflight and trusted-dirfd operations are absent.

- [ ] **Step 3: Implement trusted-dirfd tree operations.**

On Linux use openat2-style BENEATH, NO_SYMLINKS and NO_XDEV resolution; relative *at operations for mkdir/stat/link/unlink/rename; statx mount identity when available; renameat2(RENAME_NOREPLACE); and file/directory fsync. If a primitive is unsupported, returns EXDEV, reports a mount/root drift, or cannot prove no-replace behavior, return a stable fail-closed code. The non-Linux build supplies the same API and always reports unsupported.

- [ ] **Step 4: Implement versioned preflight evidence.**

Preflight creates only disposable owned probes. It validates repository.json identity, source/root overlap, mount/rename/fsync capability, hardlink viability, free blocks/inodes/quota signal/nlink safety margin, permitted metadata capability, and a physically empty unique staging root. It returns an opaque expiring ID bound to Task revision, selected mode, repository marker digest, binding identity and capability revision. Never expose raw paths, mount IDs or inode values from this object.

- [ ] **Step 5: Run filesystem and race-sensitive tests.**

Run:

~~~bash
go -C backend test ./internal/backupasset/provider -run 'Rsync(Tree|Preflight)' -count=1
go -C backend test -race ./internal/backupasset/provider -run 'Rsync(Tree|Preflight)' -count=1
~~~

Expected: Linux tests prove actual link/rename behavior when supported; unavailable fixture cases explicitly skip only their environmental probe.

## 6. Task 5: Build The Managed Rsync Command And Provider Strategy

**Files:**

- Create: backend/internal/backupasset/provider/rsync_publication.go
- Create: backend/internal/backupasset/provider/rsync_publication_test.go
- Create: backend/internal/backupasset/provider/testdata/rsync/manifest-empty.jsonl
- Create: backend/internal/backupasset/provider/testdata/rsync/manifest-tree.jsonl
- Modify: backend/internal/backupasset/provider/contracts.go
- Modify: backend/internal/backupasset/provider/registry.go
- Modify: backend/internal/backupasset/provider/registry_test.go
- Modify: backend/internal/backupasset/provider/runner.go
- Modify: backend/internal/backupasset/provider/runner_test.go

- [ ] **Step 1: Add red argv, manifest and fidelity tests.**

Assert complete argv snapshots for full-copy and hardlink mode. The only dynamic values are internally built transport, bandwidth, preflight-approved ACL/xattr flags, parent tree and fresh staging. Assert rejection of user extra args and every forbidden class: verbose/itemized path output, inplace, append, partial/resume, ignore-existing, external temp, delete, backup/dest variants, copy-links/dirlinks, files-from, user transport/rsync-path, ownership remap, dry-run and unknown flag.

Also test size+mtime content drift requires --checksum, exit code 23/24 never emits a provider commit, source deletion does not leak a parent file, full-copy nlink equals in-tree count, and hardlink silent-copy fallback is caught by full eligible-file inode validation.

- [ ] **Step 2: Run the strategy tests red.**

Run:

~~~bash
go -C backend test ./internal/backupasset/provider -run 'Rsync.*(Publication|Command|Manifest|Fidelity)' -count=1
~~~

Expected: failure identifies the absent RsyncTreeStrategy rather than the legacy Rsync reader.

- [ ] **Step 3: Implement the strict command builder.**

Build argv without a shell from this allowlist:

~~~text
rsync --archive --checksum --hard-links --numeric-ids --fsync
--protect-args --info=progress2 --no-devices --no-specials
[--acls] [--xattrs] [--bwlimit=<digits>k]
[-e internally-built-ssh] [--rsync-path internally-built-sudo-rsync]
[--link-dest=internally-derived-parent-tree] --
internally-constructed-source/ fresh-staging-tree/
~~~

Clear RSYNC_OLD_ARGS, RSYNC_PROTECT_ARGS, RSYNC_RSH, TMPDIR and other transfer-affecting environment. Keep raw stdout/stderr bounded and in-memory; translate only stable progress/failure code into result telemetry.

- [ ] **Step 4: Implement canonical tree evidence and provider commit.**

After exit=0 and Join, build canonical manifest.jsonl, fidelity evidence and a keyed source fingerprint. Validate every eligible inode relationship for hardlink mode and every regular-file link count for full-copy. Fsync tree and marker files, write authenticated attempt.json/commit.json, atomically move staging to points/<point-id>, fsync points/root parent, then return RsyncTreeCommitV1. No nonzero exit, incomplete manifest, failed fidelity probe, fence failure or rename ambiguity returns commit evidence.

- [ ] **Step 5: Register the strategy and run focused tests.**

Run:

~~~bash
gofmt -w backend/internal/backupasset/provider/rsync_publication.go backend/internal/backupasset/provider/rsync_tree_linux.go backend/internal/backupasset/provider/rsync_preflight.go
go -C backend test ./internal/backupasset/provider -run 'Rsync.*(Publication|Command|Manifest|Fidelity|Tree|Preflight)' -count=1
~~~

Expected: provider registry exposes RsyncTreeStrategy only for rsync; Restic and reader capability tests remain green.

## 7. Task 6: Integrate Fenced Rsync Publication With Repository And Task Execution

**Files:**

- Create: backend/internal/backupasset/repository/rsync_publication_execution.go
- Create: backend/internal/backupasset/repository/rsync_publication_execution_test.go
- Create: backend/internal/task/executor/rsync_publication_executor.go
- Create: backend/internal/task/executor/rsync_publication_executor_test.go
- Modify: backend/internal/backupasset/repository/publication_execution.go
- Modify: backend/internal/backupasset/repository/publication_commit_test.go
- Modify: backend/internal/task/executor/evidence.go
- Modify: backend/internal/task/executor/executor.go
- Modify: backend/internal/task/executor/executor_test.go
- Modify: backend/internal/task/publication_runner.go
- Modify: backend/internal/task/publication_runner_test.go
- Modify: backend/internal/task/runner.go
- Modify: backend/internal/task/manager.go
- Modify: backend/internal/task/manager_test.go

- [ ] **Step 1: Add red coordinator tests for the three independent facts.**

Test a successful Rsync transfer with provider commit but DB still preparing, provider commit followed by verifying, transfer success plus provider publication failure, transfer nonzero plus failed point, and a managed result that skips legacy verifier.Verify. Assert TaskRun transfer status is never rewritten by publication status.

- [ ] **Step 2: Add lease/deadline red tests.**

For hardlink attempts, assert child point_publication and parent rsync_parent leases are both held through copy/manifest/rename/owned cleanup. Parent must be same repository/link lineage and exact marker/manifest. The effective operation deadline is min(child immutable deadline, parent service-issued deadline); renewal cannot extend either.

- [ ] **Step 3: Implement Rsync publication session creation.**

In a fenced transaction create/retrieve the stable preparing RecoveryPoint, persist strict RsyncTreeAttemptV1 evidence, reserve child lease, obtain/validate parent lease when required, and hand the typed attempt to the managed executor. First seed full-copy must be explicit: imported_baseline has imported_baseline semantics, first_new_point creates the first xirang_manifest full-copy seed, and subsequent hardlink attempts use that committed parent.

- [ ] **Step 4: Implement task-executor selection and verifier bypass.**

The factory chooses RsyncPublicationExecutor only when the TaskRepositoryLink/binding has a valid managed versioned mode; legacy RsyncExecutor.Run remains untouched for legacy_mutable. providerRunResult.Managed becomes the explicit gate that prevents mutable verifier, snapshot indexer, legacy restore/anomaly/retention fallback from reading Task.RsyncTarget for this run.

- [ ] **Step 5: Implement fenced RecordProviderCommit and terminal cleanup.**

Record only matching provider/repository/point/attempt/marker/manifest/fence/deadline evidence. Move database state to verifying in a short transaction, upsert durable latches, enqueue worker work, and preserve transfer success independently. On any rejected/uncertain path release leases only after command/stream joins; cleanup can remove only exact owned staging.

- [ ] **Step 6: Run repository/task focused tests.**

Run:

~~~bash
go -C backend test ./internal/backupasset/repository ./internal/task/executor ./internal/task -run 'Rsync.*(Publication|Managed)|PublicationRunner|Verifier|Lease|TaskRun' -count=1
~~~

Expected: old legacy Rsync executor tests still pass; managed test cases show no path/command output in task-safe fields.

## 8. Task 7: Reconciliation, Worker, Shutdown And Interrupted Runs

**Files:**

- Create: backend/internal/backupasset/repository/rsync_publication_reconcile.go
- Create: backend/internal/backupasset/repository/rsync_publication_reconcile_test.go
- Modify: backend/internal/backupasset/repository/publication_reconcile.go
- Modify: backend/internal/backupasset/repository/publication_reconcile_test.go
- Modify: backend/internal/backupasset/runtime/publication_worker.go
- Modify: backend/internal/backupasset/runtime/publication_worker_test.go
- Modify: backend/internal/backupasset/runtime/runtime.go
- Modify: backend/internal/backupasset/runtime/runtime_test.go
- Modify: backend/internal/backupasset/runtime/controller.go
- Modify: backend/internal/task/publication_interrupted.go
- Modify: backend/internal/task/publication_interrupted_test.go

- [ ] **Step 1: Add exact-fact reconciliation tests.**

Cover no marker, owned staging only, partial rc=23/24, marker before rename, rename before DB record, exact final marker with DB preparing, DB verifying with missing/mismatched marker, EEXIST exact versus conflict, stale fence, deadline expiry, root drift, worker restart and already committed idempotence. Each test must prove mtime, newest directory and TaskRun finish time are ignored.

- [ ] **Step 2: Run reconciliation tests red.**

Run:

~~~bash
go -C backend test ./internal/backupasset/repository ./internal/backupasset/runtime ./internal/task -run 'Rsync.*Reconcile|PublicationWorker|Interrupted' -count=1
~~~

Expected: missing Rsync reconciler causes the intended tests to fail.

- [ ] **Step 3: Implement strategy-based worker dispatch.**

Keep one durable worker queue. Candidate selection is provider-tagged and reconstructs only strict stored attempt data. Rsync reconciliation validates exact marker, repository/link identity, commit/manifest digest, point deadline and current fence. It never scans arbitrary final directories and never promotes an outcome after lease loss or deadline ambiguity.

- [ ] **Step 4: Implement cancellation and shutdown ordering.**

Admission closes before cancellation. Wait for transfer, manifest, reader and renewal goroutines to Join before lease release. A forced shutdown leaves only exact marker-owned staging for later reconciliation/cleanup; it does not remove final points, points root, repository marker or legacy target.

- [ ] **Step 5: Generalize interrupted TaskRun reporting.**

Replace Restic executor-type filtering with provider/mode-aware managed-publication selection. Keep the existing compare-and-swap protection that refuses to overwrite a live/newer TaskRun. Ensure a Rsync provider commit after process interruption reports a safe warning state without changing transfer evidence.

- [ ] **Step 6: Run race and focused suites.**

Run:

~~~bash
go -C backend test -race ./internal/backupasset/repository ./internal/backupasset/runtime ./internal/task -run 'Rsync|Publication|Admission|Interrupted' -count=1
~~~

Expected: no race report, no automatic promotion of stale/unknown provider outcome.

## 9. Task 8: Exact Managed Rsync Reader And Legacy Guard

**Files:**

- Modify: backend/internal/backupasset/provider/rsync.go
- Modify: backend/internal/backupasset/provider/rsync_test.go
- Modify: backend/internal/backupasset/repository/query.go
- Modify: backend/internal/backupasset/repository/query_test.go
- Modify: backend/internal/backupasset/repository/lineage_guard.go
- Modify: backend/internal/backupasset/repository/lineage_guard_test.go

- [x] **Step 1: Add reader red tests.**

Test v1 mutable adapter unchanged, v2 committed-point locator round-trip, malformed point ID, marker/root identity mismatch, traversal, symlink, cross-mount, root replacement, unfinished/verifying point and attempt to use legacy mutable browse/restore for a managed link. The committed reader must resolve only points/<id>/tree.

- [x] **Step 2: Implement a committed-tree adapter alongside mutable-head adapter.**

Do not alter mutable-head semantics. Add a separate Rsync committed-point reader that requires a committed RecoveryPoint and encrypted v2 locator; reuse the existing strict fileaccess policy rather than LegacyPolicy. It returns stable capability errors until a future Child owns public browse/restore UX.

- [x] **Step 3: Extend legacy guard selection.**

Legacy list/read/restore/anomaly/retention code routes to a fail-closed managed guard whenever link/mode/latch indicates managed Rsync history. An exact committed reader can be used internally only under the same admission token and bounded handle lifetime.

- [x] **Step 4: Run reader and guard tests.**

Run:

~~~bash
go -C backend test ./internal/backupasset/provider ./internal/backupasset/repository ./internal/fileaccess -run 'Rsync|Managed|Lineage|Path' -count=1
~~~

Expected: no public legacy path can expose a managed final tree or follow a symlink.

## 10. Task 9: Task Validation, Preflight Service, And Migration Workflow

**Files:**

- Create: backend/internal/task/rsync_versioning.go
- Create: backend/internal/task/rsync_versioning_test.go
- Modify: backend/internal/task/service.go
- Modify: backend/internal/task/service_test.go
- Modify: backend/internal/task/manager.go
- Modify: backend/internal/task/manager_test.go
- Modify: backend/internal/backupasset/repository/service.go
- Modify: backend/internal/backupasset/repository/service_test.go
- Modify: backend/internal/backupasset/runtime/runtime.go

- [ ] **Step 1: Add red strict-config tests.**

Add cases for omitted mode defaulting to legacy_mutable, legal versioned_hardlink/full_copy, unknown mode, unknown JSON field, raw flags, invalid schema version, versioned mode without a matching preflight, stale Task revision, expired preflight, root/binding drift and attempting normal PUT migration of an already linked legacy task.

- [ ] **Step 2: Define RsyncPublicationConfigV1 and safe service requests.**

The persisted task config contains only schema version and publication mode; managed root, locator, marker, command flags and preflight evidence remain encrypted/internal. Define typed requests/results for:

~~~text
CreateRsyncVersioningPreflight(taskID, expectedRevision, requestedMode)
ActivateRsyncVersioning(taskID, expectedRevision, preflightID, migrationChoice)
PrepareRsyncVersioningRollback(taskID, expectedRevision)
~~~

Each result exposes opaque IDs, safe status/reason code, capability revision and estimate buckets only.

- [ ] **Step 3: Implement full migration semantics.**

imported_baseline creates a new full-copy attempt from legacy target and publishes it through the same provider/DB fence contract. first_new_point only activates a managed root and records seed_full_copy_required when long-term mode is hardlink. Both preserve EncryptedLegacyLocator. Activation is an expected-revision transaction and leaves the Task paused until admission is active.

- [ ] **Step 4: Add safe task summary projection.**

Expose mode, state, reason code and capability revision in Task service output. Unknown persisted values project to blocked/unsupported. Do not expose root, path, inode, mount, locator, marker/manifest/fence digest, argv/output or credentials.

- [ ] **Step 5: Run task/service tests.**

Run:

~~~bash
gofmt -w backend/internal/task/rsync_versioning.go backend/internal/task/service.go backend/internal/backupasset/repository/service.go
go -C backend test ./internal/task ./internal/backupasset/repository ./internal/backupasset/runtime -run 'RsyncVersioning|Task.*Validation|Preflight|Baseline|Rollback|Safe.*Summary' -count=1
~~~

Expected: legacy task create/update compatibility passes; any unsafe versioned mutation receives a stable rejected code.

## 11. Task 10: Authenticated API, Config Import, And Generated Contract

**Files:**

- Create: backend/internal/api/handlers/task_rsync_versioning_handler.go
- Create: backend/internal/api/handlers/task_rsync_versioning_handler_test.go
- Modify: backend/internal/api/handlers/task_handler.go
- Modify: backend/internal/api/handlers/task_handler_test.go
- Modify: backend/internal/api/handlers/config_handler.go
- Modify: backend/internal/api/handlers/config_handler_test.go
- Modify: backend/internal/api/router.go
- Modify: backend/internal/api/router_test.go
- Modify: backend/internal/api/docs/docs.go through make swag-init if endpoint annotations require it

- [ ] **Step 1: Add handler red tests.**

Exercise all three exact endpoints:

~~~text
POST /api/v1/tasks/:id/rsync-versioning/preflights
POST /api/v1/tasks/:id/rsync-versioning/activate
POST /api/v1/tasks/:id/rsync-versioning/rollback-preparations
~~~

Test unauthenticated, non-owner, non-Admin, valid Admin, bad ID, stale expected revision, expired preflight, mode mismatch and response redaction. Assert audit contains only action/opaque IDs/mode/result/correlation and never a root, locator, command or secret.

- [ ] **Step 2: Implement routes with existing auth/RBAC/ownership helpers.**

Use response.go helpers, existing Task ownership checks and Admin authorization. Parse only typed JSON requests with DisallowUnknownFields behavior. Map service sentinel errors to stable client codes; never send err.Error for a 500 or provider/path error.

- [ ] **Step 3: Make config import fail closed.**

When config import sees a managed Rsync config/link it imports the Task paused/disconnected and requires a fresh local preflight before activation. It must not deserialize roots or trust a foreign preflight ID. Legacy imports retain current behavior.

- [ ] **Step 4: Update API docs only from annotated source and run handler tests.**

Run:

~~~bash
make swag-init
go -C backend test ./internal/api/handlers ./internal/api -run 'RsyncVersioning|Task|Config|Router' -count=1
~~~

Expected: all new route contracts have auth/RBAC/ownership coverage and safe DTO assertions.

## 12. Task 11: Frontend Typed Mapping And Migration UI

**Files:**

- Create: web/src/components/task-rsync-versioning-dialog.tsx
- Create: web/src/components/task-rsync-versioning-dialog.test.tsx
- Modify: web/src/types/domain.ts
- Modify: web/src/lib/api/tasks-api.ts
- Modify: web/src/lib/api/tasks-api.test.ts
- Modify: web/src/components/task-create-dialog.tsx
- Modify: web/src/components/task-create-dialog.basics.tsx
- Modify: web/src/components/task-create-dialog.test.tsx
- Modify: web/src/pages/tasks-page.dialogs.tsx
- Modify: web/src/pages/tasks-page.tsx
- Modify: web/src/pages/tasks-page.test.tsx
- Modify: web/src/i18n/locales/zh.ts
- Modify: web/src/i18n/locales/en.ts

- [ ] **Step 1: Add API mapper and type red tests.**

Define literal unions:

~~~ts
export type RsyncPublicationMode =
  | "legacy_mutable"
  | "versioned_hardlink"
  | "versioned_full_copy";

export type RsyncPublicationState =
  | "legacy"
  | "preflight_required"
  | "ready"
  | "preparing"
  | "verifying"
  | "committed"
  | "failed"
  | "blocked"
  | "rollback_prepared";
~~~

Test snake_case mapping, omitted legacy compatibility, unknown mode/state mapping to blocked/unsupported, endpoint request serialization, redaction of unsafe raw fields and API error code projection.

- [ ] **Step 2: Implement API wrappers and domain mapping.**

Keep raw API types private to tasks-api.ts. Export typed wrappers for preflight, activation and rollback preparation using the central request client. Components consume only camelCase TaskRecord.rsyncPublication and never inspect executorConfig.

- [ ] **Step 3: Add task editor publication control.**

Use existing Select/Badge/AlertDialog primitives and labels. Legacy remains the default for compatibility. Versioned modes present status and safe reason, require preflight before activation and never offer a free-form Rsync flag field. A hardlink failure can lead to an explicit full-copy selection only.

- [ ] **Step 4: Add the existing-task migration dialog.**

Wire a focused dialog through TasksPageDialogs and the task-page action surface. It runs preflight, presents imported_baseline and first_new_point with the exact user-visible semantics, uses expected revision, shows bounded capacity/inode estimate categories, and renders loading/error/blocked/rollback-prepared states. It does not show root, command output, locator or credentials.

- [ ] **Step 5: Add localization and accessibility tests.**

Add matching zh/en keys. Test dialog labels, disabled activation until matching preflight, explicit choice selection, unknown state display, error state and keyboard/dialog accessibility. Ensure managed Rsync does not expose the legacy restore action in history UI.

- [ ] **Step 6: Run frontend focused checks.**

Run:

~~~bash
cd web && npm run test -- --run tasks-api task-create-dialog task-rsync-versioning-dialog tasks-page
cd web && npm run typecheck
cd web && npm run lint
~~~

Expected: tests, strict type checking and lint pass without direct fetch, raw snake_case component access or new accessibility errors.

## 13. Task 12: Cross-Layer Verification, Documentation, And Finish Sequence

**Files:**

- Modify: docs/admin/backup-recovery.md
- Inspect: .trellis/spec/backend/ and .trellis/spec/guides/ during Phase 3.3 with trellis-update-spec; record a no-change decision in the task journal if no codebase-backed reusable contract was learned

- [ ] **Step 1: Run focused cross-layer regression suites.**

Run:

~~~bash
go -C backend test ./internal/backupasset/... ./internal/task/... ./internal/api/... ./internal/database -count=1
go -C backend test -race ./internal/backupasset/... ./internal/task/... -count=1
cd web && npm run check
~~~

Expected: no lease/fence race, no migration parity regression, no managed/legacy fallback regression, no frontend type/lint/test/build failure.

- [ ] **Step 2: Run the full repository quality gates.**

Run:

~~~bash
make check
git diff --check
rg -n '000064_backup_asset_(search|content|processing|export|recovery|lifecycle|ga)|000065_backup_asset_rsync' backend/internal/database/migrations .trellis/tasks/07-12-backup-data-explorer-design .trellis/tasks/07-15-backup-assets-rsync-versioning
~~~

Expected: make check passes; diff check is empty; the reservation scan has no stale nonhistorical mapping. Run dependency/security/doc freshness commands required by current CI if make check does not cover them.

- [ ] **Step 3: Verify final behavior with temporary fixtures only.**

Use temporary fixture roots to prove hardlink/full-copy, baseline, rollback-preparation and redaction workflows. Do not test retention/purge, physical point deletion, cross-filesystem copy fallback or arbitrary legacy-directory cleanup because they are out of scope and unsafe.

- [ ] **Step 4: Update documentation truth only for implemented behavior.**

Update docs/admin/backup-recovery.md to state that Xirang-managed tree immutability is not storage WORM, source consistency is outside Rsync, imported_baseline is a new full-copy, and rollback preparation does not delete points. Do not document unimplemented public browsing/restore or retention.

- [ ] **Step 5: Perform Phase 3.4 only after every required gate is green.**

Stage all functional changes together and create one work commit:

~~~bash
git add backend web docs .trellis/spec .trellis/tasks/07-12-backup-data-explorer-design
git add .trellis/tasks/07-15-backup-assets-rsync-versioning
git commit -m "feat: add rsync versioned recovery points"
~~~

Do not commit unrelated user changes. Confirm the commit contains all four 000064 files, no generated runtime directory and no secrets.

- [ ] **Step 6: Finish the same branch and integrate.**

After the work commit, invoke trellis-finish-work, which archives the Child and records the journal in its own automatic commit. Then push the same branch, create one PR containing functional and archive commits, monitor every required CI job, fix/push any failure on this branch, merge only with all required checks green, monitor Release Please and Docker workflows, and sync local main to origin/main after merge.

## 14. Plan Self-Review

| Design requirement | Planned tasks |
| --- | --- |
| paired 000064 latch/backfill/down guard/reservation | Task 1 |
| strict provider-tagged coordinator contract | Task 2 |
| binding v2, managed root, legacy admission/rollback | Task 3 |
| preflight, filesystem containment and command allowlist | Tasks 4--5 |
| hardlink/full-copy manifest/inode correctness and provider commit | Tasks 5--6 |
| independent transfer/DB/provider states, lease/deadline/fence | Tasks 6--7 |
| crash/restart/shutdown reconciliation | Task 7 |
| exact committed-point reader and legacy guard | Task 8 |
| task/API/config import boundaries | Tasks 9--10 |
| frontend mapping, wizard, i18n and accessibility | Task 11 |
| full tests, docs, commit/PR/finish workflow | Task 12 |

Self-check completed:

- 自审未发现未决标记、延期实现标记或未指明的代码路径。
- Every new public API has typed request/response, auth/RBAC/ownership and redaction tests.
- Every mutable provider operation has preflight, admission, fence/deadline, cancellation/join and destructive-behavior constraints.
- No task creates a public catalog/restore feature, retention/purge engine, Rclone versioning path or second publication state machine.
- Future implementation must re-read the relevant Trellis backend/frontend specs with trellis-before-dev before editing product code.

## 15. Next Authorization Gate

This plan is complete but the task remains planning. The next action is user review of this file. Only an explicit authorization to run:

~~~bash
python3 ./.trellis/scripts/task.py start .trellis/tasks/07-15-backup-assets-rsync-versioning
~~~

permits implementation. Design approval and implement-plan approval do not imply that command.
