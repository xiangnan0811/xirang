# Current `main` Evidence

## Snapshot

- Planning base: `b3fbd0b3dd1be8c6b509973b510460d4d350b849`
- Branch: `codex/backup-task-preview-connect-ui`
- Worktree: `/home/murray/code/xirang/.worktrees/backup-task-preview-connect-ui`
- Captured: 2026-08-29

## Existing Backend Contract

- `backend/internal/api/handlers/backup_repository_handler.go` exposes `POST /api/v1/backup-repositories/connect`; routing applies `backup_repositories:manage`.
- The handler accepts `task_id` plus optional repository metadata, but this feature can and must use task-only input.
- `backend/internal/backupasset/repository/connect.go` derives the binding from Task/Node/Credential, performs a bounded read-only provider probe outside the transaction, then locks and revalidates authoritative rows before persisting repository/link/access state.
- Managed Rsync is rejected by Connect and remains behind the existing activation flow. Legacy mutable Rsync can produce an observed mutable recovery point.
- `ConnectResult` currently carries `Repository` and optional `MutablePoint`; the outer Go struct has no JSON tags, so the frontend must accept the observed PascalCase field while remaining compatible with snake_case.
- Connect has no catalog-worker dependency today and does not check active TaskRuns. The UI may add an advisory active-run guard, but authorization and final validity remain server concerns.

## Existing Catalog Runtime

- `backend/internal/backupasset/runtime/catalog_worker.go` performs an initial asynchronous scan and then waits for a dynamic reconcile interval. It has no explicit wake path.
- `catalog.Indexer.ListCandidates` includes observed mutable heads without catalog coverage, so no new candidate model or public rebuild endpoint is required.
- `.trellis/spec/backend/backup-file-catalog.md` requires a periodic timer that survives coalesced wakes and resets only after a periodic pass; continuous wake traffic must not starve due candidates.
- Repository service construction precedes CatalogWorker construction in `runtime.go`. A narrow setter wired after worker construction avoids a constructor dependency cycle and follows the existing `SetRebuildPorts` composition style.
- The SearchWorker has an existing capacity-one `TryWake` pattern, but CatalogWorker needs stronger pending-wake and periodic-fairness semantics because a wake can arrive during an active scan.

## Existing Frontend Contract

- `web/src/lib/api/backup-repositories-api.ts` already exports `connectBackupRepository`, maps its request to `task_id`, and currently discards `MutablePoint` from the response.
- `web/src/lib/api/recovery-points-api.ts` exports the full recovery-point mapper and `getRecoveryPointCatalogStatus`. The full mapper expects a catalog projection, while the Connect snapshot does not contain one; a reusable core/snapshot mapper is required instead of an unsafe cast.
- `web/src/types/domain.ts` models catalog generation, coverage, content availability and permissions. Browsable means complete generation/coverage, available content and list permission. Preview permission is content authorization, not catalog readiness; zero entries can be a legitimate empty backup.
- `backupAssetsRecoveryPointHref(taskId, pointId)` already provides the exact deep link.
- `tasks-page.tsx` and its table/grid/dialog helpers already centralize Rsync versioning actions. The new action should use the same shared-prop pattern while remaining visually and semantically distinct from versioning.
- Task DTOs do not expose authoritative repository-link state; Rsync publication fallback is always present. Therefore the action must truthfully say “connect or refresh” instead of guessing that a task is unconnected.

## Baseline Verification

- `go mod download` completed successfully.
- `npm ci` completed successfully with 594 packages and zero reported vulnerabilities; lifecycle-script allowlist warnings were informational.
- `go test ./internal/backupasset/repository ./internal/api/handlers -run 'Connect|BackupRepository' -count=1` passed.
- `env -u NODE_ENV npx vitest run src/features/backup-assets/repository-management-panel.test.tsx` passed all 14 tests.

## Recovered Production Context

The prior task recorded a healthy NAS deployment on v0.52.3 and found only Task 3 browsable because it had an active managed repository link; Tasks 1 and 4–13 lacked an equivalent link. This is historical recovered context, not a live probe performed during this planning task. Implementation acceptance must gather fresh evidence before making any current-production claim.
