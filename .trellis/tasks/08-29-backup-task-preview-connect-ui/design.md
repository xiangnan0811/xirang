# Design: 备份任务首次接入文件预览入口

## Selected Approach

Use a per-task “接入或刷新文件预览” action that calls the existing task-derived Connect API. After the durable connection transaction creates or resolves an observed mutable recovery point, the repository service sends a best-effort internal wake hint to CatalogWorker. The frontend polls the exact returned recovery point until its catalog is browsable, then deep-links to that point.

This preserves the existing authority boundary: the server derives all sensitive connection facts from the Task and owns probe/persistence decisions; the UI supplies only intent and renders progress.

## Rejected Alternatives

| Alternative | Why it is not selected |
| --- | --- |
| Require users to open repository management and enter a Task ID | The entry is undiscoverable from the failing task and encourages manual identifier handling. |
| Add repository-link state to every Task DTO first | It expands the public contract and cross-layer scope; current DTO cannot truthfully classify link state, while idempotent “connect or refresh” needs no guess. |
| Automatically connect every legacy task | It changes external state without an explicit per-task operator action and makes failures harder to attribute. |
| Reuse the Rsync versioning dialog | That flow migrates an already connected legacy link and can enable a materially different storage mode. |
| Add a public “rebuild catalog” endpoint | It exposes an unnecessary control surface; the need is an internal convergence hint after a known durable mutation. |
| Wait for the fixed catalog interval | It can leave the operator waiting about 15 minutes without actionable progress. |

## End-to-End Flow

```text
Admin selects task action
  -> confirm safety boundary
  -> POST connect {task_id}
  -> server performs bounded read-only probe
  -> server transaction resolves repository/link/mutable point
  -> commit succeeds
  -> server best-effort CatalogWorker.TryWake()
  -> response maps repository + mutable-point snapshot
  -> UI polls catalog status for that exact point
  -> complete + available + list permission
  -> open /backup-assets/tasks/:taskId/recovery-points/:pointId
```

If Connect fails, the dialog shows a sanitized failure. If catalog construction fails or is blocked, the repository connection remains intact and the dialog shows a stable recovery message. If the foreground deadline expires, the dialog stops polling and explains that background indexing continues.

## Backend Design

### Catalog wake contract

Add a narrow repository-side dependency such as:

```go
type CatalogWakeRequester interface {
    TryWake() bool
}
```

`repository.Service` stores an optional requester and exposes a lifecycle-safe `SetCatalogWake` used only by production composition. The precise name may follow package conventions, but the dependency must remain narrow and must not import the runtime package into repository.

After the Connect transaction commits, and only when the result contains a usable mutable point, call the requester once. Treat `false`, a full coalescing channel or a stopped worker as an accepted best-effort outcome. Do not let the wake outcome alter the successful API result. Place the request directly after durable commit so a newly committed point can converge even if later DTO/audit projection unexpectedly fails.

No wake is sent for provider-probe errors, validation/authorization rejection, transaction rollback, or a nil/retired mutable point. Repeated Connect remains safe and may request a refresh for the same mutable point.

### CatalogWorker scheduling

The worker owns a capacity-one wake signal and explicit pending-work semantics. Its run loop must distinguish three triggers:

- initial pass;
- coalesced external wake;
- periodic deadline.

A wake received while a pass is active remains pending and causes another pass after completion. A wake queued before `Run` is absorbed by the initial pass. Periodic cadence is tracked independently: wake-triggered passes do not reset/postpone the timer, and a due periodic pass is not starved by a stream of wake events. Only completion of the periodic pass resets its next deadline.

The implementation may use a sequential lifecycle-owned pass loop or an explicit `scanDone`/pending state machine, but tests must prove the externally observable contract. Shutdown cancels in-flight work, joins owned goroutines, clears active metrics and makes later `TryWake` return false or be safely ignored.

### Production wiring

In `backend/internal/backupasset/runtime/runtime.go`:

1. construct the repository service as today;
2. construct the catalog indexer and worker;
3. inject the worker through the narrow setter before returning the runtime;
4. preserve one worker instance and existing start/stop ownership.

A composition test must fail if the production repository service is returned without the catalog wake requester.

No schema, migration, public route or Swagger change is expected.

## Frontend Design

### API and domain boundary

Extract a recovery-point core/snapshot mapper from the existing full mapper. The snapshot type represents only fields actually emitted by `backupasset.RecoveryPointDTO` and does not pretend to include catalog status. The full recovery-point mapper builds on the shared snapshot mapper and then maps its catalog projection.

Extend `BackupRepositoryMutationResult` with a nullable `mutablePoint` snapshot. Its raw mapper accepts both `MutablePoint` and `mutable_point`, accepts absent/null as null for mutation variants that omit it, and rejects a present malformed value. Request mapping remains exactly `{ task_id: taskId }`.

### Eligibility and shared task action

Define one helper used by table and grid views:

```text
visible = admin
       && executor == rsync
       && rsyncPublication.mode == legacy_mutable
       && rsyncPublication.state == legacy
       && rsyncPublication.reasonCode == legacy

disabled = task status is running/retrying
        || task.hasActiveRun
```

Paused and disabled tasks remain eligible. The action is hidden for Viewer/Operator, Rclone and managed/versioned Rsync. It uses a file/folder discovery icon and an exact aria label, not the GitBranch/versioning icon.

`TasksPage` owns the selected task and open state. `TasksPageDialogs` renders a focused `TaskPreviewConnectDialog`, matching the existing page-local dialog composition without putting network state in the table/grid components.

### Dialog state machine

```text
idle
  -> connecting
      -> blocked/failed
      -> indexing(pointId)
          -> ready(pointId)
          -> blocked/failed
          -> timed_out_background(pointId)
```

On confirm, call Connect. A valid returned mutable point is mandatory for polling; otherwise fail closed with localized recovery guidance. Poll `getRecoveryPointCatalogStatus(token, pointId, signal)` every two seconds for at most 60 attempts while the dialog remains open. A separate two-minute wall-clock deadline bounds even a catalog request that never settles; deadline expiry aborts the request and enters `timed_out_background`, while lifecycle/task/token/close aborts remain silent.

Ready requires:

- generation state complete;
- coverage complete;
- content availability available;
- `permissions.list` true.

Do not require preview permission or a positive indexed entry count. A complete empty backup opens the workspace and shows its normal empty state.

Use an AbortController plus timer cleanup. Closing, changing task/token or unmounting aborts the active request, clears the deadline, and prevents stale/late state updates. A timeout never invokes Disconnect and never claims indexing failed.

### Error, privacy and accessibility

- Reuse the central request wrapper and sanitized `getErrorMessage`; do not log raw server responses.
- Map catalog failed/partial/unavailable states to stable zh/en user messages without raw provider or database details.
- Safety copy explicitly states: bounded read-only probe; creates/refreshes preview association and catalog observation; does not enable multi-versioning or modify/delete backup files.
- Use existing Dialog/Button/Tooltip primitives, restore focus on close, announce progress via `aria-live`, and expose disabled reasons to keyboard and assistive-technology users.

Implementation note: the repository has no shared `components/ui` Tooltip primitive. Reuse the existing accessible inline-tooltip pattern (`role="tooltip"` linked by `aria-describedby`, visible on keyboard focus as well as hover). This corrects the implementation mechanism without changing the product or accessibility contract.

## Expected File Manifest

Create:

- `web/src/components/task-preview-connect-dialog.tsx`
- `web/src/components/task-preview-connect-dialog.test.tsx`

Modify:

- `backend/internal/backupasset/runtime/catalog_worker.go`
- `backend/internal/backupasset/runtime/catalog_worker_test.go`
- `backend/internal/backupasset/runtime/runtime.go`
- `backend/internal/backupasset/runtime/runtime_test.go`
- `backend/internal/backupasset/repository/service.go`
- `backend/internal/backupasset/repository/connect.go`
- `backend/internal/backupasset/repository/connect_test.go`
- `web/src/types/domain.ts`
- `web/src/lib/api/recovery-points-api.ts`
- `web/src/lib/api/recovery-points-api.test.ts`
- `web/src/lib/api/backup-repositories-api.ts`
- `web/src/lib/api/backup-repositories-api.test.ts`
- `web/src/lib/api/tasks-api.ts`
- `web/src/lib/api/tasks-api.test.ts`
- `web/src/pages/tasks-page.tsx`
- `web/src/pages/tasks-page.dialogs.tsx`
- `web/src/pages/tasks-page.utils.ts`
- `web/src/pages/tasks-page.table.tsx`
- `web/src/pages/tasks-page.grid.tsx`
- `web/src/pages/tasks-page.test.tsx`
- `web/e2e/backup-assets-gate.spec.ts`
- `web/src/i18n/locales/zh.ts`
- `web/src/i18n/locales/en.ts`

Implementation note: the reviewed frontend boundary fix authorizes `tasks-api.ts` and its focused tests so an unknown nonempty executor is not collapsed to historical Rsync. This is mapper hardening only; it does not change the backend Task API, public routes, or Swagger contract.

Implementation note: the second reviewed accessibility fix authorizes the existing backup-assets Playwright gate for first/last table-row tooltip geometry in zh/en under keyboard focus and hover. It verifies the tooltip remains inside the horizontal scrollport without introducing a shared primitive or changing the grid implementation.

The first RED may prove that a smaller existing test file is the correct ownership point. Any further expansion into schema, route/Swagger, deploy, backend Task API or provider mutation requires a design amendment and renewed approval before proceeding.

## Test Strategy

### Backend RED/GREEN

- CatalogWorker: a long timer is interrupted by wake; repeated wakes coalesce without blocking; wake during scan is not lost; sustained wakes cannot postpone an overdue periodic pass; a pre-Run wake folds into the initial pass; shutdown cancels/joins and leaves no active gauge.
- Repository Connect: committed mutable point requests exactly one wake; repeat Connect is safe; probe/transaction failure and nil/retired point do not wake; a rejected/full wake cannot fail Connect.
- Runtime composition: the production repository service receives the exact CatalogWorker instance, with no dependency cycle or duplicate worker.

### Frontend RED/GREEN

- API mapper: one internally consistent PascalCase or snake_case mutation envelope, absent/null point, duplicate/mixed/malformed fail-closed behavior and task-only request body; unknown nonempty executors remain unknown while absent historical executor data retains the Rsync default.
- Recovery-point mapper: shared core parsing, strict enum/time handling and no raw snake_case leakage.
- Dialog: safety copy, request, progress transitions, failed/blocked/empty-complete/two-minute wall-clock timeout behavior, task/token/close cancellation with timer cleanup and late-result suppression, localized close name, and exact ready deep link.
- Tasks page: table and grid visibility for Admin canonical legacy Rsync; hidden for Viewer/Operator, managed/malformed Rsync and Rclone; paused allowed; running/retrying/active runs disabled with tooltip parity; table tooltip geometry remains inside the horizontal scrollport for first and last rows in zh/en under keyboard focus and hover; action opens the correct task dialog.

## Verification and Rollback

Run focused tests first, then backend race/repeat checks, frontend `env -u NODE_ENV npm run check`, backend/full project gates and privacy/diff scans. Delivery follows PR + required CI + post-merge monitoring.

Rollback is a normal code revert: removing the UI action/wiring leaves the pre-existing periodic CatalogWorker path intact. Because there is no migration, provider mutation or public endpoint, rollback does not require data repair.
