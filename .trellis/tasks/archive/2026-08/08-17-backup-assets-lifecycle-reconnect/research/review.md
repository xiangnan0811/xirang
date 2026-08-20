# Child 14 Task 16 — Independent high-risk review log

Recorded 2026-08-18. Findings use absolute `file:line` evidence. Critical and
Important items required a new RED → same-selector GREEN before Task 17.
Minor residuals do not block delivery.

`backup_assets.enabled` remains `CodeDefault: "false"` at
`backend/internal/settings/service.go:296`. `backend/cmd/server/main.go` was
not edited. Child 15, `000071`, deploy, lockfiles, and `.codex/**` are
untouched.

## Critical

### C1. `owner_cleanup_unproven` resumed at Cleaning

- Status: **fixed**
- Evidence: `backend/internal/backupasset/retention/coordinator.go:1113-1117`
- Problem: revoke failure and cleanup failure share the closed reason
  `owner_cleanup_unproven`. Resuming at Cleaning skipped idempotent revoke +
  drain. `000070` cannot add a new CHECK value; that would be `000071` /
  Child 15.
- Fix: `blockedResumePhase` resumes this reason at `LifecyclePhaseRevoking`.
  Cleanup-only failures remain safe because revoke + drain are idempotent.
- Selector: `TestLifecycleRevokeFailureResumesAtRevokingNotCleaning`
- GREEN: `cd backend && go test ./internal/backupasset/retention -run '^TestLifecycleRevokeFailureResumesAtRevokingNotCleaning$' -count=1` → exit 0

### C2. Retention StartupPass before owner graphs + wrong Recovery/Export fallbacks

- Status: **fixed**
- Evidence:
  - order: `backend/internal/backupasset/runtime/runtime.go:2083-2158`
  - Recovery: `backend/internal/backupasset/runtime/retention_lifecycle.go:607-626`
  - Export: `backend/internal/backupasset/runtime/retention_lifecycle.go:583-605`
  - Processing: `backend/internal/backupasset/runtime/retention_lifecycle.go:567-581`
- Problem: Retention `StartupPass` could run before Search/Processing/Export/
  Recovery graphs were ready. Fallback proofs used columns/tables that do not
  exist (`backup_asset_recovery_jobs.recovery_point_id`,
  `backup_asset_export_jobs.recovery_point_id`) and counted historical
  Processing rows. Missing tables were treated as settled.
- Fix: Startup order is admission → content → catalog/health → publication +
  TaskRun readiness → search → processing → export → recovery → retention.
  Fallbacks fail closed on missing schema; Recovery joins plans; Export uses
  items + active execution states; Processing counts only `is_current` or a
  live `current_attempt_id`.
- Selectors:
  - `TestRuntimeOwnerFallbackProofsFailClosedWithoutSchema`
  - `TestRuntimeOwnerFallbackProofsHonorCurrentSourceRows`
  - `TestRuntimeStartupManagedModeRequiresInterruptedRunReadiness`
- GREEN: `cd backend && go test ./internal/backupasset/runtime ./internal/backupasset/retention -count=1` → exit 0

## Important

### I3. Managed Task retention facade never installed in production

- Status: **fixed** without editing `main.go`
- Evidence: `backend/internal/backupasset/runtime/runtime.go:2065-2080` and
  `backend/internal/backupasset/runtime/retention_lifecycle.go:366-372`
- Problem: `composeRetentionRuntime` built `ManagedTaskRetentionFacade` but
  nothing installed it on the Task manager. Editing `main.go` is forbidden.
- Fix: `composeRetentionRuntime` returns the facade; `SetInterruptedRunReadiness`
  type-asserts `SetManagedRecoveryPointRetention` and installs it. Existing
  production wiring already calls
  `assetRuntime.SetInterruptedRunReadiness(taskManager)` at
  `backend/cmd/server/main.go:221` and was not edited. `task` does not import
  `runtime`.
- Selector: `TestRuntimeInterruptedRunReadinessInstallsManagedTaskRetention`
- GREEN: `cd backend && go test ./internal/backupasset/runtime -run '^TestRuntimeInterruptedRunReadinessInstallsManagedTaskRetention$' -count=1` → exit 0

### I4. Explicit purge execute is not atomic after bind

- Status: **fixed** (Alan P2, no `000071`)
- Evidence: `backend/internal/backupasset/retention/worker.go` `Execute` now
  binds the plan and runs every `ClaimTx` inside one coordinator transaction.
- Problem: `PurgeService.Execute` previously bound the plan, then claimed
  items sequentially. A mid-list claim failure left earlier points claimed.
- Fix: one transaction for bind + all claims + consume. A claim failure rolls
  the whole execute back; the plan stays `ready` and zero attempts remain.
  Selector: `TestPurgeExecuteIsAtomicAcrossClaimFailures`.

### I5. Config v2 exported holds then dropped them on import

- Status: **fixed**
- Evidence:
  - reject: `backend/internal/api/handlers/config_backup_assets.go:229-231`
  - empty export: `backend/internal/api/handlers/config_backup_assets.go:236-241`
  - import does not persist holds: `backend/internal/api/handlers/config_backup_assets.go:358-394`
- Problem: export emitted hold rows; import ignored them. That is silent
  legal-hold loss.
- Fix: export always emits an empty `recovery_point_holds` array. Any
  non-empty hold list is `errConfigAssetGraphInvalid` (HTTP 400). Holds
  remain DB/key-owned disaster-recovery facts, not config-restore facts.
- Selectors: `TestConfigImportV2RejectsUnrestorableHolds`,
  `TestConfigExportV2DefaultDocumentAndSafeStableRefs`,
  `TestConfigExportV2AuditRecordsCountsWithoutEnvelope`
- GREEN: `cd backend && go test ./internal/api/handlers -run 'ConfigExportV2|ConfigImportV2' -count=1` → exit 0

### I6. Manifest drift

- Status: **fixed by plan amendment**
- Evidence: `.trellis/tasks/08-17-backup-assets-lifecycle-reconnect/implement.md` §2
- Problem: Task 14 created disaster-recovery files that were missing from the
  create list. The plan listed `middleware/rbac_test.go` and
  `task/retention_worker_test.go`; live coverage is
  `backend/internal/api/backup_asset_rbac_test.go` and retention/task tests
  already in the manifest.
- Fix: amended the create/modify lists and recorded that `main.go` stays
  untouched. This is a plan amendment, not a hidden code comment.

## Minor / residuals (do not block)

- `ListRetentionPolicies` parses `cursor` and never sets `NextCursor`
  (`backend/internal/api/handlers/backup_retention_handler.go`).
- SQLite vs PostgreSQL `000070` shape differences remain covered by the
  paired migration and required Postgres gates, not by inventing `000071`.
- `retentionAuditFake` is unsynchronized; the Task 15 race suite passed.
- Frontend eslint warnings remain on the lifecycle panels (`tabIndex`, hook
  deps). Zero eslint errors. Lockfiles were not changed.
- Doc-freshness still warns about `router.go` vs `backend/README_backend.md`
  and migration-doc drift that also exists on bare `HEAD~1`. No invented
  README or `000071` change.
- No extra spy proves retention is skipped when publication is unresolved;
  `TestRuntimeStartupManagedModeRequiresInterruptedRunReadiness` already
  returns before retention when readiness is missing.

## Session interrupt (2026-08-19)

The first Task 16 spec/quality pair (`2165054e`, `c91d1c6f`) died after
loading skills and returned no verdict. The earlier 10-item high-risk
pass (`a461bb8d`) also died before findings. Those runs are discarded.

Task 15 independent reviews did finish and are not re-run:
- spec: process deviation for missing `red-green.md` / untracked files
  (evidence is now recorded; untracked remains Task 17)
- quality: Approve with residuals; publication-block test leftover stays
  residual after the C2 reorder

A narrower Task 16 re-review split the leftover work. Lifecycle/runtime
(C1/C2/I3) returned **Approve with residuals** on 2026-08-19: no Critical
or Important, no new RED→GREEN before Task 17. C1/C2/I3 claims stand.
Added residuals from that pass:
- Content fallback `proveNoOutstandingContent` is still fail-open when
  `db` is nil or the delivery-grant table is missing
  (`retention_lifecycle.go:550-552`).
- Cleanup-site `owner_cleanup_unproven` shares the revoke resume switch;
  only the revoke path has a dedicated resume test.

Contract/manifest (I4/I5/I6) returned **compliant** on 2026-08-19: no
Critical or Important. I4 stays an acceptable residual. I5 claims hold
(empty hold export; non-empty holds are 400 at validate time). I6 claimed
amendment is complete. The reviewer's `.gitignore` fence gap was already
closed at `implement.md:229` before this write-up; no extra amend.

`make check` recreated `backend/xirang-server`. It is gitignored alongside
`backend/xirang` / `backend/server` and must stay out of the Task 17 commit.

## Rejected as defects

- Untracked product files: Task 17 makes one delivery commit. Untracked is
  not a product defect.
- “Wire the facade in `main.go`”: forbidden by the approved plan. The
  readiness hook is the authorized installation path.
- Recreating hold restore in config v2: holds are not config-restorable;
  restoring them without proofs would violate the DR matrix.

## Controller verification after fixes

```text
cd backend && go test ./internal/backupasset/runtime ./internal/backupasset/retention ./internal/api/handlers ./internal/task -count=1
# ok runtime 7.444s; retention 0.708s; handlers 4.816s; task 3.009s
```

Task 15 focused + race selectors were independently re-run after these
fixes. See `research/red-green.md`.
