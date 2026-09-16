# Research: orphan TaskRun reconciliation contract

- Query: What is the safest product/domain contract and test seam for reconciling a persisted active `TaskRun` when `Manager` has no live owner, particularly the supplied production case where the affected scheduled Task and its authoritative TaskRun remained running after container replacement, no transfer process or newer active run existed, and no recovery point was produced?
- Scope: internal
- Date: 2026-08-25

## Findings

### Executive recommendation

Implement the narrow repair in formal, authenticated `Manager.Cancel`, not as a generic startup rewrite. The correct boundary is **formal Cancel + a temporary process-local trigger barrier + one authority-checked transaction whose writes use exact CAS predicates**. Once the barrier proves that the current Manager has no live owner, cancel every authoritative current-node TaskRun in the closed active set (`pending`, `running`, `retrying`). If the Task aggregate is active, cancel it in the same transaction; if it is already terminal, preserve it and allow success only when at least one orphan run was actually reconciled.

This is the minimal release-safe choice because it:

1. repairs the exact supplied production state through an explicit operator command;
2. preserves point-aware startup classification, which has more durable evidence than process liveness;
3. requires no schema or migration change;
4. closes the current Manager's trigger/cancel race without pretending to add a cross-replica lease; and
5. can be tested at the manager/database seam using the repository's existing transaction and GORM callback race patterns.

The existing v0.50.4 fallback is unsafe: the no-owner branch rereads only the Task and then calls `updateStatus`, leaving the active `TaskRun` unchanged and discarding the update error (`backend/internal/task/manager.go:872-887`). That orphan continues to count as active for node-write admission (`backend/internal/backupasset/runtime/admission.go:129-157`) even if the aggregate becomes canceled.

### Files found

| File | Relevance |
| --- | --- |
| `backend/internal/model/task.go` | Defines Task/TaskRun states, active status set, authoritative node snapshots, and TaskRun creation invariants. |
| `backend/internal/model/task_run_contract_test.go` | Executable contract for active statuses and authoritative/matching snapshots. |
| `backend/internal/task/state_machine.go` | Permitted aggregate state transitions, including running/retrying to canceled and terminal to pending. |
| `backend/internal/task/manager.go` | Reservation, atomic execution entry, process-local owner tracking, scheduling, Cancel/Pause/Resume, shutdown, and the defective fallback. |
| `backend/internal/task/runner.go` | Trigger gating, execution ownership, terminalization, retry behavior, and aggregate/run dual-write patterns. |
| `backend/internal/task/publication_interrupted.go` | Strongest existing transaction/CAS pattern for classifying interrupted active runs and protecting the Task aggregate from another active run. |
| `backend/internal/task/publication_interrupted_test.go` | Tests terminal preservation, live-owner skipping, other/newer active-run protection, no-point ambiguity, and bounded startup reconciliation. |
| `backend/internal/task/manager_test.go` | Existing cancellation races, execution-entry transaction barriers, node-snapshot checks, and sanitization tests. |
| `backend/internal/backupasset/runtime/runtime.go` | Startup readiness invokes point-aware interrupted-publication reconciliation and blocks on unresolved runs. |
| `backend/internal/backupasset/runtime/admission.go` | Active authoritative TaskRuns block node-scoped recovery writes. |
| `backend/internal/backupasset/repository/publication_execution.go` | Existing fail-closed validation of current Task/node ownership and active TaskRun snapshots. |
| `backend/internal/api/handlers/task_handler.go` | Current Cancel/Pause HTTP error mapping. |
| `backend/internal/api/handlers/task_run_handler.go` | TaskRun response path and last-error sanitization. |
| `backend/internal/task/runtime_sanitize.go` | Central defense-in-depth sanitizer for stored and returned runtime evidence. |
| `backend/cmd/server/main.go` | Startup order: backup-asset reconciliation/readiness occurs before schedule loading. |
| `backend/cmd/server/main_test.go` | Source-order test that protects startup reconciliation before scheduler activation. |
| `backend/internal/database/migrations/sqlite/000072_task_run_snapshot_compatibility.up.sql` | SQLite active-snapshot compatibility constraints/triggers. |
| `backend/internal/database/migrations/postgres/000072_task_run_snapshot_compatibility.up.sql` | PostgreSQL equivalent of active-snapshot compatibility constraints/triggers. |

### Current domain and ownership model

- `pending`, `running`, and `retrying` are all active TaskRun statuses; the remaining statuses are terminal (`backend/internal/model/task.go:13-68`). An authoritative snapshot is a positive `NodeIDSnapshot` (`backend/internal/model/task.go:71-75`).
- New TaskRuns freeze the current positive Task node and reject a missing, non-authoritative, or mismatched caller snapshot (`backend/internal/model/task.go:132-185`; `backend/internal/model/task_run_contract_test.go:50-118`). The migrations likewise reject an active legacy-unknown snapshot and protect snapshot immutability/status compatibility (`backend/internal/database/migrations/sqlite/000072_task_run_snapshot_compatibility.up.sql:1-49`; `backend/internal/database/migrations/postgres/000072_task_run_snapshot_compatibility.up.sql:1-78`).
- There is no unique database constraint limiting a Task to one active TaskRun; the TaskRun model's task/node/status indexes are non-unique (`backend/internal/model/task.go:133-143`). Formal Cancel should therefore select and CAS **all** authoritative current-node active rows rather than silently choosing `First`, maximum ID, or latest timestamp. Multiple such rows with no process-local owner are multiple orphan attempts under the same explicit cancellation authority, not a reason to leave node admission blocked.
- Durable execution entry CASes the reserved row from `pending` to `running` and the Task to `running` in one transaction, writing the same execution time to `TaskRun.StartedAt` and `Task.LastRunAt` (`backend/internal/task/manager.go:422-505`). That timestamp equality is useful evidence for the exact observed production row, but it is not a complete cancellation selector: `pending` has no start time, `retrying` is part of the closed active set, and more than one abandoned active row can exist because there is no uniqueness constraint.
- Process ownership itself is only in memory (`pendingRuns`/`chainRunner`), and `cancelTaskRunOwner` checks that local owner (`backend/internal/task/manager.go:902-915`). Graceful shutdown cancels and waits only for runners owned by that process (`backend/internal/task/manager.go:1040-1068`). Container replacement can therefore leave durable state without reconstructible in-memory ownership.
- Every normal trigger claims `pendingRuns` with `LoadOrStore` before it loads the Task or reserves a TaskRun (`backend/internal/task/manager.go:265-275`; `backend/internal/task/runner.go:80-105`). This is the correct process-local linearization point for a temporary Cancel barrier. The later per-Task mutex is too late because a trigger has already established ownership and may have reserved durable state before acquiring it (`backend/internal/task/runner.go:155-195,252-271`).
- A Task whose aggregate remains `running` rejects later triggers (`backend/internal/task/runner.go:107-150`). An enabled task is still loaded into cron scheduling (`backend/internal/task/manager.go:587-625`), but every scheduled trigger remains blocked until the aggregate is made terminal.

### Proposed formal Cancel contract

Add a package-local reconciliation helper for the no-live-owner branch. Its observable contract should be:

1. **Acquire or signal at one process-local boundary.** Before loading durable state, attempt an atomic `pendingRuns.LoadOrStore(taskID, barrier)`. If a real `pendingRunOwnership` already exists, it won the race: signal it and retain the live-runner cancellation path. If the barrier wins, triggers fail their existing ownership claim until the transaction exits. Use a distinct barrier type so a concurrent Cancel cannot mistake another cancellation barrier for a live runner, and release the exact barrier on every exit (prefer compare-and-delete semantics).
2. **Stop process-local retry ownership under the barrier.** Stop/delete any retry timer and retry-chain context before durable reconciliation. A timer that fires afterward reaches `triggerCore`, loses the `pendingRuns` claim to the barrier, and cannot reserve a new row (`backend/internal/task/runner.go:80-105`).
3. **One transaction.** Run the Task reload, candidate selection, and every TaskRun/Task write under `db.WithContext(ctx).Transaction`, locking the Task row where supported and retaining exact CAS predicates as the final cross-engine authority. Cross-resource writes are required to be transactional (`.trellis/spec/backend/database-guidelines.md:21-38`; `.trellis/spec/backend/quality-guidelines.md:35-55`).
4. **Reload and validate authority.** Require a positive current `Task.node_id`. Select every TaskRun for that `task_id` whose snapshot is positive, exactly equals the reloaded Task node, and whose status belongs to `model.TaskRunActiveStatuses()`. Legacy-unknown, node-mismatched, already-terminal, and other-Task rows are not candidates and remain untouched.
5. **Require repair evidence for a terminal Task.** If the Task is already terminal, successful Cancel requires at least one selected authoritative active row; otherwise retain the existing unsupported-cancel result. This makes terminal-state Cancel a narrowly bounded repair, not a new idempotent meaning for every terminal Task.
6. **CAS every selected TaskRun.** For each observed candidate, update by exact `id`, `task_id`, `node_id_snapshot`, and observed active `status`. Set `status=canceled`, one shared `finished_at=now.UTC()`, and a fixed privacy-safe reason. Pending rows retain `started_at=nil` and duration zero; started rows preserve `started_at` and receive a non-negative duration derived from the shared finish time. Require exactly one affected row for every candidate.
7. **CAS only an active Task aggregate.** If the reloaded Task is pending/running/retrying, update by exact `id`, current `node_id`, and exact observed status to canceled, preserving enabled/disabled scheduling policy and the existing Cancel fields. If the Task is terminal—including the production `status=canceled && enabled=false` shape—do not rewrite status, `last_error`, scheduling fields, or terminal evidence.
8. **Rollback on every drift or database error.** Any candidate CAS affecting zero rows, Task authority/status drift, or database failure rolls back all candidate and aggregate updates. Multiple selected authoritative active rows are handled as a set and all commit or all roll back; no partial subset may become terminal.

The existing interrupted-publication reconciler demonstrates most of the transaction/CAS shape: it reloads an active TaskRun, CASes it terminal, and guards an aggregate update by observed Task state (`backend/internal/task/publication_interrupted.go:62-99`). Its `NOT EXISTS` rule intentionally avoids changing the aggregate while another attempt may own publication. Formal operator Cancel has different authority: after winning the process-local barrier it must cancel the whole authoritative active set, including multiple persisted orphan rows, so that the product path can prove zero active runs.

### State interaction matrix

| Durable state encountered by no-owner formal Cancel | Required behavior | Reason |
| --- | --- | --- |
| Active Task aggregate plus one or more authoritative current-node active rows | Cancel all selected rows and the aggregate atomically | Explicit Cancel owns the whole current-node active set; no uniqueness constraint guarantees a single row. |
| Active Task aggregate with no authoritative current-node active row | Fixed conflict/leave Task unchanged | Reconciliation must not manufacture or hide a missing durable run outcome. |
| Terminal `canceled`, disabled Task plus authoritative `running` row matching its node (and, in the old-Pause shape, `started_at == last_run_at`) | Cancel the run and preserve the Task byte-for-byte | This is the possible old `Pause(cancelRunning=true)` failure shape. The operator may have issued the already-provided Pause command, and Resume before zero active rows would violate the acceptance sequence. |
| Any other terminal Task plus at least one authoritative current-node active row | Cancel selected active rows and preserve the terminal aggregate | Success is repair-scoped to real orphan evidence; it does not redefine ordinary terminal Cancel. |
| Terminal Task with no authoritative current-node active row | Return the existing unsupported-cancel result | Prevents terminal Cancel from becoming a general idempotent no-op. |
| Matching `pending`, `running`, or `retrying` TaskRun | CAS it to canceled | These are exactly the model's closed active set (`backend/internal/model/task.go:26-68`). Pending timing remains nil/zero; started rows retain start evidence and get non-negative duration. |
| Multiple matching authoritative active rows | CAS every observed row in the same transaction | Multiple abandoned attempts all block admission. Selecting one would leave the product invariant broken; each exact CAS prevents racing drift. |
| Active legacy-unknown or node-mismatched row | Leave untouched; it is outside this command's authority | The snapshot contract reserves those rows for a different authority class (`.trellis/spec/backend/database-guidelines.md:127-176`). If no matching authoritative candidate exists for a terminal Task, Cancel remains unsupported. |
| A current-process trigger or runner wins `pendingRuns` first | Signal the real ownership and do not run fallback reconciliation | Preserves existing live-owner compensation as authoritative. |
| Cancel barrier wins `pendingRuns` first | Trigger cannot inspect/reserve until the barrier is released | Closes the newly-owned-run race before durable inspection. |
| TaskRun already terminal | Never overwrite it | Terminal outcomes are immutable for reconciliation; existing tests cover this (`backend/internal/task/publication_interrupted_test.go:120-158`). |

### Scheduler, Resume, Pause, and startup implications

- Formal Cancel must preserve `enabled`. When it transitions an active Task aggregate, retaining existing next-run semantics is compatible with the state machine (`backend/internal/task/state_machine.go:46-92`). When the Task is already terminal, the repair should not modify scheduling fields at all.
- If the old-release Pause command has been issued, its shape is specifically `status=canceled && enabled=false` with an authoritative `running` row whose start matches `last_run_at`. Supporting that shape in formal Cancel is necessary and safe: the operator command supplies explicit authority, the Task remains paused, and only the exact current-node active set becomes terminal. Production should not Resume until the post-Cancel query proves zero active rows.
- `Resume` only enables and reschedules a disabled Task; it does not inspect or reconcile TaskRuns (`backend/internal/task/manager.go:975-1004`). Calling it before formal Cancel would re-enable the paused production Task while the orphan still blocks/contaminates active-run state, so the required sequence is Cancel, prove zero active runs, then Resume.
- `Pause(cancelRunning=true)` explains how the old shape was produced: it disables/removes the schedule and then discards the error from its nested Cancel (`backend/internal/task/manager.go:934-973`). Changing Pause error semantics is worthwhile follow-up hardening, but is not required to repair the already-paused row and is outside this release's selected narrow command boundary.
- Existing startup reconciliation is deliberately point-aware. A provider-committed durable point can yield `warning`; a durable failed point can yield `failed`; no point or only a preparing point remains unresolved (`backend/internal/task/publication_interrupted.go:46-59,101-150`). Automatically rewriting all no-point rows to canceled/failed would discard this classification boundary.
- The startup query treats all active Restic runs as publication candidates, but selects Rsync/Rclone only when a managed point exists (`backend/internal/task/publication_interrupted.go:152-160`). Consequently, a no-point Rsync/Rclone run can be invisible to this readiness pass, while a no-point Restic run remains unresolved and blocks readiness. This asymmetry is exactly why a generic startup sweep is not a release-minimal change.
- Startup ordering is also material: backup-asset startup/readiness completes before schedules are loaded (`backend/cmd/server/main.go:206-240`), and a test protects that ordering (`backend/cmd/server/main_test.go:11-40`). Running a generic sweep first could erase point evidence; running it later cannot help a no-point Restic row already blocking readiness. Startup auto-healing therefore needs an integrated evidence/ownership design rather than reusing formal Cancel casually.

### Privacy-safe terminal evidence

- For an explicit operator Cancel, retain the existing fixed user-facing reason `任务已取消`; it describes authority and contains no container ID, host, path, command, stdout/stderr, or raw error.
- If future startup logic classifies a no-point interruption, use the existing stable interruption vocabulary—particularly `process_interrupted_before_provider_commit`—rather than calling an inferred crash a user cancellation (`backend/internal/task/publication_interrupted.go:23-26`). That future classification still needs a durable ownership proof.
- The TaskRun API sanitizes `last_error` on list/detail responses (`backend/internal/api/handlers/task_run_handler.go:24-37,99-175`), and runtime sanitization protects stored/returned evidence (`backend/internal/task/runtime_sanitize.go:13-19`), but fixed messages/codes should remain the primary boundary. Sanitization is defense in depth, not permission to store raw evidence.
- The Cancel handler currently maps domain errors to `400` using `err.Error()` (`backend/internal/api/handlers/task_handler.go:548-558`). The selected release keeps the route and response schema unchanged, so every newly reachable reconciliation error must be a fixed privacy-safe product message; raw DB/process text must remain internal, consistent with `.trellis/spec/backend/error-handling.md:23-35,55-75,314-327`.

### Approach comparison

| Approach | Advantages | Risks / cost | Verdict |
| --- | --- | --- | --- |
| **A. Trigger-barrier plus transactional reconciliation inside explicit formal Cancel** | Exact active and old-Pause production repairs; operator authority; closes local trigger race; handles the entire closed active set and multiple rows; no migration | Not automatic; does not create cross-replica durable liveness proof; requires exact barrier cleanup and CAS rollback tests | **Recommended minimal release-safe fix.** |
| **B. Generic startup reconciliation of no-live-owner runs** | Automatic recovery after restart; scheduler/admission could self-heal | Process-local absence does not prove global absence; point evidence and provider-specific query behavior complicate ordering; retry timers are not durable; could race another replica or misclassify a committed transfer | Defer until ownership and evidence precedence are explicitly designed. |
| **C. Durable execution lease/fence or manager boot epoch** | Gives a durable definition of owner death; supports safe automatic takeover/reconciliation and multi-replica fencing | Schema and paired migrations, lease renewal/expiry semantics, clock/failure tests, runner integration, and rollout compatibility; substantially larger release risk | Best long-term architecture if automatic healing/multi-replica operation is required, not a v0.50.4 patch. |

### Focused test seam

The missing regression seam is a **fresh Manager over persisted state**: existing cancellation tests exercise a live pending/running runner, but do not seed either the observed running aggregate or a possible terminal/paused Task with an active TaskRun and then construct a manager whose owner maps are empty (`backend/internal/task/manager_test.go:694-880,1421-1489,2121-2191`). Existing transaction-race tests already use GORM callbacks/barriers around execution-entry commit and should be reused rather than introducing timing sleeps (`backend/internal/task/manager_test.go:904-1081`).

Recommended focused tests:

1. `TestCancelTerminalPausedTaskReconcilesAuthoritativeRunningOrphan`
   - Seed the exact production shape: `Task.status=canceled`, `enabled=false`, `last_run_at=T`; authoritative `TaskRun.status=running`, `started_at=T`; fresh Manager with empty ownership maps.
   - First retain this as the mandatory RED against the old code. After implementation, assert Cancel succeeds, the run is canceled with finish/duration/static reason, and every Task aggregate/scheduling field remains unchanged.
2. `TestCancelActiveTaskAndAuthoritativeRunsCommitAtomically`
   - Cover active aggregate statuses and the full TaskRun active set (`pending`, `running`, `retrying`). Assert all matching rows share one finish time, timing semantics are status-appropriate, and the Task becomes canceled in the same commit.
3. `TestCancelMultipleAuthoritativeOrphansCancelsWholeSet`
   - Seed two or more matching current-node active rows. Assert all are canceled or none are; no active matching row remains.
4. `TestCancelOrphanReconciliationLeavesOtherAuthorityClassesUntouched`
   - Include legacy-unknown, node-mismatched, other-Task, and already-terminal rows. Assert none changes. A terminal Task with only those rows retains the existing unsupported result.
5. `TestCancelTriggerBarrierLinearizesOwnershipRace`
   - Deterministically cover both orders: trigger owns `pendingRuns` first, so Cancel signals the live owner; Cancel barrier owns it first, so trigger cannot reserve a row. Assert the barrier is removed after success and every error path. Add concurrent-Cancel coverage so a barrier is not misclassified as a runner.
6. `TestCancelOrphanReconciliationRollsBackOnAnyCASDrift`
   - Using GORM callbacks/barriers, change one observed run status/snapshot or the active Task status after selection. Assert all earlier row updates and the aggregate update roll back.

The model contract tests already cover status membership and create-time snapshot validation (`backend/internal/model/task_run_contract_test.go:12-118`). Interrupted-publication tests provide reusable assertions for terminal immutability, current live-owner skipping, other/newer active-run shielding, no-point ambiguity, and bounded reconciliation (`backend/internal/task/publication_interrupted_test.go:44-297`). No broad gate is necessary during research; implementation should run the focused task package tests first, then the repository-prescribed backend/full gates before release.

### Minimal release scope

Suggested bounded implementation surface:

- `backend/internal/task/manager.go`: add the temporary `pendingRuns` cancellation barrier, route no-owner active and terminal-with-orphan cases through one transactional helper, and make all transaction/CAS errors observable.
- `backend/internal/task/manager_test.go`: add the exact terminal/paused RED, full active-set/multiple-row cases, barrier races, authority exclusions, and rollback tests above.
- Keep the existing HTTP route/schema, scheduler API, Pause behavior, frontend, database schema, and startup/publication reconciliation unchanged for this release.

Do not include a startup sweep, lease schema, migration, retry restoration, broad scheduler rewrite, or cleanup-job repurposing in the minimal fix. The existing cleanup only deletes expired rows and is not an ownership reconciler (`backend/internal/task/manager.go:1111-1154`).

## Code patterns

- **Atomic durable ownership entry:** exact TaskRun and Task CASes in one transaction (`backend/internal/task/manager.go:422-505`).
- **Process-local ownership linearization:** `pendingRuns.LoadOrStore` before Task load/reservation (`backend/internal/task/manager.go:265-275`; `backend/internal/task/runner.go:80-105`).
- **Transactional reconciliation/CAS:** exact run CAS and guarded aggregate update (`backend/internal/task/publication_interrupted.go:62-99`), adapted here to reconcile the entire operator-authorized active set.
- **Authoritative snapshot validation:** positive, matching live Task node at creation (`backend/internal/model/task.go:153-185`) and fail-closed repository filtering (`backend/internal/backupasset/repository/publication_execution.go:453-485`).
- **Race testing without sleeps:** GORM callbacks/barriers around transaction commit (`backend/internal/task/manager_test.go:904-1081`).
- **Terminal-state preservation and newer-run shielding:** `backend/internal/task/publication_interrupted_test.go:120-158,275-297`.
- **Privacy-safe runtime errors:** `backend/internal/task/runtime_sanitize.go:13-19`; fixed interruption codes in `backend/internal/task/publication_interrupted.go:23-26`.

## External references

None. The recommendation depends on repository-specific Task/TaskRun state, node-snapshot, publication, and startup contracts; no external source was needed.

## Related specs

- `.trellis/spec/backend/database-guidelines.md:21-38` — context-aware database access, explicit error handling, and transactions for multi-row/multi-table work.
- `.trellis/spec/backend/database-guidelines.md:127-207` — closed `TaskRun.NodeIDSnapshot` contract and correct active matching query.
- `.trellis/spec/backend/error-handling.md:23-35,55-75,314-327` — sentinel/wrapped errors, conflict responses, no raw internal evidence, and no swallowed database errors.
- `.trellis/spec/backend/quality-guidelines.md:35-55,91-106,244-317` — transactional cross-resource writes, package tests, DB failure cases, and synchronized async TaskRun assertions.
- `.trellis/spec/backend/quality-guidelines.md:947-1059,1570-1582` — transfer and RecoveryPoint publication are separate durable outcomes; database errors and worker shutdown must be explicit.
- `.trellis/spec/guides/cross-layer-thinking-guide.md:18-49,73-87` — map state/data boundaries and use CAS for concurrent mutation.
- `.trellis/workflow.md:352-379` — persist research under the task before implementation planning.

## Caveats / Not Found

- The production facts (affected scheduled Task and authoritative TaskRun, container replacement, absent transfer, no newer active run, and no produced point) were supplied in the research request; they are not independently reproduced from repository fixtures or production telemetry here.
- There is no durable manager owner ID, boot epoch, heartbeat, lease, or fencing token in the inspected Task/TaskRun schema. Therefore automatic startup inference that a run has no live owner is not proven safe for multiple processes sharing a database.
- No active-TaskRun uniqueness constraint was found. Formal Cancel therefore has to reconcile every observed authoritative current-node active row transactionally; choosing only one would leave admission blocked.
- The retry timer is process-local; no startup restoration of a `retrying` Task was found. That is adjacent durability debt but should not be conflated with the running-orphan Cancel fix.
- Existing live-run terminal paths often update the Task and TaskRun sequentially rather than in one transaction (`backend/internal/task/runner.go:550-569,652-677`). This research recommends the stronger atomic rule specifically for reconciliation, where no live runner remains to complete the second write.
- No tests or full quality gates were run, per the research-only instruction.
