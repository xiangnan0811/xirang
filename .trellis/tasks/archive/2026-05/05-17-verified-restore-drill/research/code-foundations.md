# Research: Verified Restore Drill Code Foundations

- **Query**: Research the existing code foundations and implementation constraints for task `.trellis/tasks/05-17-verified-restore-drill` (Verified Restore Drill Evidence). Focus on current restore drill code, models/migrations, APIs, UI entry points, safety boundaries, tests, and concrete gaps for implementing drill evidence.
- **Scope**: internal
- **Date**: 2026-05-17

## Findings

### Files Found

| File Path | Description |
|---|---|
| `.trellis/tasks/05-17-verified-restore-drill/prd.md` | Task requirements and acceptance criteria for structured restore drill evidence, policy latest status, Backup Confidence Center consumption, sandbox safety, and backend test coverage. |
| `.trellis/tasks/05-17-verified-restore-drill/task.json` | Task metadata; planning status, parent roadmap, base branch `feature/trust-ops-roadmap`. |
| `backend/internal/task/drill.go` | Main restore drill implementation: validates drill config, triggers asynchronous drill runs, restores backup to sandbox, runs verify scripts, optionally cleans up, schedules drill scans. |
| `backend/internal/task/manager.go` | Normal restore trigger path and restore safety checks; creates restore `TaskRun`s and uses node-level restore mutex tracking. |
| `backend/internal/task/runner.go` | Normal restore run execution; updates `TaskRun`, invokes restore executors and forced post-restore verifier, manages restore node lock cleanup. |
| `backend/internal/task/locks.go` | Task, strategy, and node lock helpers used by task/restore concurrency control. |
| `backend/internal/task/verifier/verifier.go` | Verification result structure and restore verification behavior. |
| `backend/internal/task/integrity_checker.go` | Periodic restic/rclone integrity checks for policies; related but separate from drill evidence. |
| `backend/internal/task/executor/executor.go` | Executor and `RestoreExecutor` interfaces used by restore/drill flows. |
| `backend/internal/model/models.go` | `Policy` drill config fields and generic `TaskRun` model; no dedicated drill evidence model exists. |
| `backend/internal/database/migrations/sqlite/000052_drill_config.up.sql` | SQLite migration adding drill config columns to `policies`. |
| `backend/internal/database/migrations/postgres/000052_drill_config.up.sql` | PostgreSQL migration adding drill config columns to `policies`. |
| `backend/internal/database/migrations/sqlite/000006_task_runs.up.sql` | Base `task_runs` table with trigger/status/timing/verify/error fields used for drill runs today. |
| `backend/internal/database/migrations/sqlite/000030_task_run_progress.up.sql` | Adds `progress` to `task_runs`. |
| `backend/internal/api/router.go` | Routes for policy drill trigger, task run history/logs, restore, and snapshot APIs. |
| `backend/internal/api/handlers/policy_handler.go` | Policy create/update/get handling for drill config, path/script validation, and manual drill trigger endpoint. |
| `backend/internal/api/handlers/task_handler.go` | Task restore endpoint and task list/get progress handling. |
| `backend/internal/api/handlers/task_run_handler.go` | Generic `TaskRun` history/detail/log APIs; no drill evidence response. |
| `backend/internal/api/handlers/snapshot_handler.go` | Restic snapshot browse/restore handlers and dangerous restore target path validation. |
| `backend/internal/task/drill_test.go` | Existing drill unit tests for validation, trigger setup, cron matching, and task selection. |
| `backend/internal/task/manager_test.go` | Existing restore mutex/concurrency coverage. |
| `backend/internal/task/verifier/verifier_test.go` | Verifier behavior tests for backup vs restore path handling. |
| `backend/internal/api/handlers/drill_trigger_test.go` | Handler tests for manual drill trigger success/failure/RBAC cases. |
| `backend/internal/api/handlers/policy_handler_test.go` | Handler tests for drill config create/update/get validation. |
| `web/src/types/domain.ts` | Frontend types include policy drill fields and `TaskRunTriggerType` includes `drill`. |
| `web/src/lib/api/policies-api.ts` | Policy API client maps drill fields and exposes `triggerDrill`. |
| `web/src/lib/api/task-runs-api.ts` | Task run API client maps trigger types but currently omits `drill` from mapper. |
| `web/src/lib/api/tasks-api.ts` | Task API client restore helpers and progress derivation. |
| `web/src/components/policy-editor-dialog.tsx` | Existing policy drill configuration UI and manual trigger button. |
| `web/src/pages/policies-page.tsx` | Policy list/editor orchestration; save path currently constructs `NewPolicyInput` without drill fields. |
| `web/src/pages/policies-page.card.tsx` | Mobile policy card; no latest drill state display. |
| `web/src/components/task-run-history.tsx` | Generic task run history UI; no drill-specific trigger label/icon. |
| `web/src/components/task-run-detail.tsx` | Generic run detail UI; no drill phase/sandbox/evidence display and drill falls back to manual label. |
| `web/src/pages/tasks-page.tsx` | Task page orchestration for run history/detail, snapshot and restore dialogs. |
| `web/src/pages/tasks-page.dialogs.tsx` | Task dialogs compose history/detail/snapshot/restore UIs; no drill-specific evidence view. |
| `web/src/components/restore-confirm-dialog.tsx` | Normal restore confirmation UI. |
| `web/src/components/snapshot-browser.tsx` | Restic snapshot browse/restore UI. |
| `web/src/lib/api/snapshots-api.ts` | Snapshot API client for listing files, diff/search, and snapshot restore. |

### Code Patterns

#### Current drill execution model

`backend/internal/task/drill.go` is the central implementation. `TriggerDrill(policyID)` loads the policy with source nodes, verifies drill is enabled and has a sandbox target node, validates config, finds a task for the policy, creates a `TaskRun` with `trigger_type = "drill"`, then starts `executeDrill` asynchronously.

Current drill run creation pattern in `backend/internal/task/drill.go`:

```go
run := model.TaskRun{
    TaskID:      task.ID,
    TriggerType: "drill",
    Status:      "pending",
    StartedAt:   &now,
}
```

`executeDrill` currently records drill progress primarily through `TaskRun` updates and task logs. The staged flow is:

1. mark run `running`
2. sandbox connectivity precheck using `runDrillSSHScript(..., "true")`
3. restore backup to sandbox through `restoreBackupToSandbox`
4. run `DrillPreVerify`, `DrillVerify`, and `DrillPostVerify` scripts
5. optionally cleanup `DrillRestorePath`
6. mark `TaskRun` final status, finish time, duration, and last error

#### Existing evidence shape

The persistent record for drills today is generic `TaskRun` plus `TaskLog` messages. `backend/internal/model/models.go` defines `TaskRun` with generic fields including `TriggerType`, `Status`, `StartedAt`, `FinishedAt`, `DurationMs`, `VerifyStatus`, `Progress`, and `LastError`. There is no dedicated `DrillEvidence`, `RestoreDrill`, or phase-level evidence model/table found.

#### Policy drill configuration storage

`backend/internal/model/models.go` stores drill configuration directly on `Policy`:

```go
DrillEnabled      bool      `gorm:"not null;default:false" json:"drill_enabled"`
DrillCron         string    `gorm:"size:128;not null;default:''" json:"drill_cron"`
DrillTargetNodeID *uint     `gorm:"index" json:"drill_target_node_id"`
DrillRestorePath  string    `gorm:"size:512;not null;default:'/tmp/xirang-drill'" json:"drill_restore_path"`
DrillPreVerify    string    `gorm:"type:text;not null;default:''" json:"drill_pre_verify"`
DrillVerify       string    `gorm:"type:text;not null;default:''" json:"drill_verify"`
DrillPostVerify   string    `gorm:"type:text;not null;default:''" json:"drill_post_verify"`
DrillAutoCleanup  bool      `gorm:"not null;default:true" json:"drill_auto_cleanup"`
```

The corresponding migration is `000052_drill_config` for SQLite and PostgreSQL.

#### Restore executor pattern

`backend/internal/task/executor/executor.go` defines `RestoreExecutor`:

```go
type RestoreExecutor interface {
    RunRestore(ctx context.Context, task model.Task, logf LogFunc, progressf ProgressFunc) (int, error)
}
```

Normal restore execution in `backend/internal/task/runner.go` uses restore-capable executors and then forced verification through `verifier.Verify(..., isRestore=true)`.

#### Verification result pattern

`backend/internal/task/verifier/verifier.go` returns a structured `Result`:

```go
type Result struct {
    Status          string
    Message         string
    FileCountSrc    int
    FileCountDst    int
    TotalSizeSrc    int64
    TotalSizeDst    int64
    SampledFiles    int
    MismatchedFiles int
}
```

Restore mode skips deep verification for restic/rclone with a passed result message indicating executor-level error detection; other executor types use remote-to-remote verification.

#### API patterns

`backend/internal/api/router.go` exposes existing drill and related run endpoints:

```go
secured.POST("/policies/:id/drill-trigger", middleware.RBAC("tasks:trigger"), policyHandler.TriggerDrill)
secured.GET("/tasks/:id/runs", middleware.RBAC("tasks:read"), middleware.OwnershipTaskCheck(dep.DB), taskRunHandler.ListByTask)
secured.GET("/task-runs/:id", middleware.RBAC("tasks:read"), taskRunHandler.Get)
secured.GET("/task-runs/:id/logs", middleware.RBAC("tasks:read"), taskRunHandler.Logs)
```

`backend/internal/api/handlers/policy_handler.go` returns manual drill trigger responses as:

```go
respondOK(c, gin.H{"task_run_id": taskRunID, "message": "恢复演练已触发"})
```

No structured drill evidence endpoint or policy latest drill summary response was found.

#### Frontend policy drill UI pattern

`web/src/components/policy-editor-dialog.tsx` already exposes configuration fields for enabling drills, drill cron, sandbox node selection, restore path, pre/verify/post scripts, auto cleanup, and manual trigger for an existing policy. Manual trigger calls `apiClient.triggerDrill` and displays the returned task run ID.

#### Generic run UI pattern

`web/src/components/task-run-history.tsx` and `web/src/components/task-run-detail.tsx` render generic task run records and logs. They handle restore but do not currently render drill-specific labels, icons, phase results, sandbox path/node, cleanup result, or failed step.

### Safety Boundaries

#### Drill config validation

`backend/internal/task/drill.go` validates that the sandbox node is not one of the source nodes, that a restore path exists, and that the path is absolute, not root, has no `..`, and is not an exact forbidden system directory.

`backend/internal/api/handlers/policy_handler.go` validates policy drill input before persistence. `validateDrillRestorePath` rejects non-absolute paths, `..`, exact forbidden directories, and forbidden subpaths such as `/etc/...`.

#### Cleanup behavior

Current cleanup in `backend/internal/task/drill.go` shells out to `rm -rf` with `executor.ShellEscape(restorePath)` after checking the path is absolute, length greater than one, and does not contain `..`:

```go
cleanupCmd := fmt.Sprintf("rm -rf %s", executor.ShellEscape(restorePath))
if strings.HasPrefix(restorePath, "/") && len(restorePath) > 1 && !strings.Contains(restorePath, "..") {
    if err := m.runDrillSSHScript(context.Background(), sandboxNode, cleanupCmd); err != nil {
        m.emitLog(task.ID, runIDPtr, "warn", "清理失败（不影响演习结果）: "+err.Error(), "")
    } else {
        m.emitLog(task.ID, runIDPtr, "info", "清理完成", "")
    }
}
```

Cleanup failure is currently logged as a warning and does not affect final drill status.

#### Normal restore safety

`backend/internal/task/manager.go` validates normal restore target paths with `validateRestorePath`, blocks unsupported executor types, requires at least one successful task run, and registers restore node mutex state synchronously before launching the restore goroutine.

`backend/internal/task/runner.go` removes `restoreNodes` state on restore completion and avoids mutating original `Task.Status` for restore runs.

#### Snapshot restore safety

`backend/internal/api/handlers/snapshot_handler.go` validates target paths using `filepath.Clean`, requires absolute non-root paths, and rejects dangerous path prefixes including `/etc`, `/usr`, `/bin`, `/boot`, `/dev`, `/proc`, `/sys`, `/run`, and `/var/run`.

### Tests

Current backend coverage exists for baseline drill behavior but not structured evidence:

- `backend/internal/task/drill_test.go` covers drill config validation, disabled/missing sandbox/not found trigger failures, cron matching, associated task selection, and creation of `TaskRun` with `trigger_type = "drill"`.
- `backend/internal/api/handlers/drill_trigger_test.go` covers trigger endpoint success, disabled drill, not found, missing manager, RBAC denial, and ignored POST body behavior.
- `backend/internal/api/handlers/policy_handler_test.go` covers drill config create/update/get validation including missing cron and sandbox-equals-source rejection.
- `backend/internal/task/manager_test.go` covers restore mutex/concurrency behavior.
- `backend/internal/task/verifier/verifier_test.go` covers backup vs restore verifier path handling.

Frontend tests found around tasks/backups do not currently cover drill trigger type rendering or drill evidence UI.

### Related Specs

- `.trellis/tasks/05-17-verified-restore-drill/prd.md` — authoritative requirements for restore drill evidence, UI visibility, Backup Confidence Center consumption, sandbox cleanup boundary, and tests.
- `.trellis/tasks/05-17-verified-restore-drill/task.json` — task metadata, status, parent roadmap, and base branch.

### External References

No external references were needed; this research was limited to internal code foundations.

### Concrete Gaps for Implementing Drill Evidence

- No dedicated drill evidence model/table exists.
- No migration exists for structured drill evidence or per-phase evidence.
- No API returns structured drill evidence with restore, verification, and cleanup phase results.
- Policy responses do not include latest drill status/time/duration/failed step.
- Drill runs persist only generic `TaskRun` status/duration/last_error plus unstructured logs.
- No explicit failed-step field or enum exists for drill phases.
- No persisted drill evidence captures policy/task/snapshot, sandbox node, sandbox path, restore start/end, verify result, cleanup result, or cleanup error as structured data.
- Snapshot identity is not persisted for current drill runs.
- Cleanup result is not persisted structurally; cleanup failure is warning-only and does not affect final status.
- `DrillPostVerify` failure is warning-only and does not affect final drill status.
- Frontend `web/src/lib/api/task-runs-api.ts` has `TaskRunTriggerType` support in the type definition but maps backend `trigger_type="drill"` to `manual` because `mapTriggerType` omits `drill`.
- `TaskRunHistory` and `TaskRunDetail` do not display drill-specific trigger labels or phase evidence.
- Policy list/card UI does not show latest drill state.
- `web/src/pages/policies-page.tsx` currently constructs `NewPolicyInput` without copying drill fields from `PolicyDraft`, so policy editor drill changes are not sent through that save path.
- Existing tests do not cover successful structured evidence, failure evidence, or cleanup sandbox boundary semantics required by the acceptance criteria.

## Caveats / Not Found

- No dedicated `DrillEvidence`, `RestoreDrill`, or equivalent model was found.
- No drill evidence API endpoint was found.
- No policy latest drill summary fields were found in backend response or frontend type definitions.
- No Backup Confidence Center implementation was found in this research scope; the PRD only requires exposing evidence for later consumption.
- The active Trellis task reported by `.trellis/scripts/task.py current --source` was `.trellis/tasks/05-17-trust-demo-feedback`, not `.trellis/tasks/05-17-verified-restore-drill`; this file follows the explicit requested task path.
