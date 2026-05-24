# Research: snapshot adjacent surfaces

- **Query**: Research adjacent residual P4 surfaces around snapshot indexing and restore/search APIs in Xirang, focusing on whether there are other small behavior-compatible leaks in snapshot indexer/restic find errors or snapshot search/index status responses. Context: active task is `.trellis/tasks/05-24-p4-snapshot-indexer-output-sanitization`. Requirements: local-only, minimal compatible slice; do not expand into AppCredential policy hook redesign, file browser content/path behavior, Docker/Nginx deployment logging, Vault/KMS/SSH CA/session recording/command approval.
- **Scope**: internal
- **Date**: 2026-05-24

## Findings

### Files Found

| File Path | Description |
|---|---|
| `backend/internal/snapshot/indexer.go` | Snapshot indexing package: in-memory indexing job tracking, index status helper, async index trigger, restic `find` execution, NDJSON parser, LIKE escaping. |
| `backend/internal/api/handlers/snapshot_search_handler.go` | `GET /tasks/:id/snapshots/search` handler; triggers indexing on first search and returns either index-building status or search results. |
| `backend/internal/api/router.go` | Snapshot routes and middleware registration for list/files/restore/diff/search. |
| `backend/internal/api/handlers/snapshot_handler.go` | Snapshot list, file list, and snapshot restore handlers. |
| `backend/internal/task/executor/restic_executor.go` | Restic list snapshots, list files, restore files, backup/restore execution, and command-output error handling. |
| `backend/internal/task/executor/runtime_sanitize.go` | Executor runtime output/evidence redaction helpers used by restic list/files/restore paths. |
| `backend/internal/api/handlers/response.go` | Unified response helpers; `respondInternalError` logs internal error and returns generic 500 message to clients. |
| `backend/internal/api/handlers/snapshot_diff_handler.go` | Adjacent snapshot diff endpoint; error path wraps command error without restic output. |
| `backend/internal/model/models.go` | `SnapshotFileIndex` schema model storing `task_id`, `snapshot_id`, `path`, `size`, `mtime`. |
| `backend/internal/database/migrations/sqlite/000054_snapshot_file_index.up.sql` | SQLite table/index migration for `snapshot_file_indices`. |
| `backend/internal/database/migrations/postgres/000054_snapshot_file_index.up.sql` | PostgreSQL table/index migration for `snapshot_file_indices`. |
| `web/src/lib/api/snapshots-api.ts` | Frontend API wrapper for list snapshots/files, restore snapshot, and snapshot search. |
| `web/src/components/snapshot-search.tsx` | Frontend snapshot search component; displays indexing status `message` or search results. |
| `web/src/components/snapshot-browser.tsx` | Frontend snapshot browser/restore UI that consumes list/files/restore APIs. |
| `web/src/components/snapshot-diff-viewer.tsx` | Frontend diff UI; adjacent snapshot surface inspected but not the focus of this slice. |
| `.trellis/spec/backend/error-handling.md` | Backend error-response contract: internal errors generic; do not expose command output or sensitive details. |
| `.trellis/spec/backend/logging-guidelines.md` | Logging contract: do not log full command output, secrets, raw endpoints, executor config, or diagnostic evidence. |
| `.trellis/spec/backend/quality-guidelines.md` | Least-privilege and credential-grant contracts covering snapshot purposes and sanitized denial/errors. |

### Code Patterns

#### Snapshot indexer `restic find` error surface

- `backend/internal/snapshot/indexer.go:189-197` builds a remote `restic find --json --long --path=/ ... 2>&1` command and returns the captured combined output directly when the command fails:

```go
output, err := executor.RunSSHCommandOutput(ctx, client, cmd)
if err != nil {
    return fmt.Errorf("restic find 执行失败: %w, 输出: %s", err, strings.TrimSpace(output))
}
```

- `backend/internal/snapshot/indexer.go:158-160` wraps that error with the snapshot short ID (`索引快照 %s 失败: %w`). `backend/internal/snapshot/indexer.go:133-137` also wraps snapshot listing failures, but the underlying `ResticExecutor.ListSnapshots` path already uses output redaction.
- `backend/internal/snapshot/indexer.go:91-99` is the only current call path from the search handler to `BuildIndex`, and it runs asynchronously with `_ = BuildIndex(...)`; the error is discarded and is not returned to the search API response in the current route path.
- `backend/internal/snapshot/indexer.go:167-168` exposes `IncrementalIndex` as a synchronous wrapper around `BuildIndex`, but `rg` found no current call sites outside `indexer.go`.

#### Snapshot search response and index status behavior

- `backend/internal/api/handlers/snapshot_search_handler.go:42-88` implements `GET /tasks/:id/snapshots/search`.
- Request validation and task type checks are local and fixed-message: empty `q` returns `q 参数不能为空` (`snapshot_search_handler.go:48-52`), missing task returns `任务不存在` (`:54-58`), non-restic task returns `仅 restic 类型任务支持快照搜索` (`:59-62`).
- `snapshot.EnsureIndexed` is called at `snapshot_search_handler.go:64-69`. If indexing is not ready, the API returns only a fixed status/message payload (`snapshot_search_handler.go:70-72`):

```go
respondOK(c, gin.H{"status": "indexing", "message": "首次搜索，正在构建索引，请稍后重试"})
```

- Search results are returned from the `snapshot_file_indices` table with distinct `snapshot_id`, `path`, `size`, and `mtime`, limited to 200 rows (`snapshot_search_handler.go:75-87`). These fields are also the frontend `SearchResult` contract (`web/src/lib/api/snapshots-api.ts:20-25`) and are rendered in `SnapshotSearch` (`web/src/components/snapshot-search.tsx:174-194`).
- LIKE search uses parameter binding and explicit escaping: `snapshot.EscapeLikePattern(q)` (`snapshot_search_handler.go:75-80`), implemented in `backend/internal/snapshot/indexer.go:260-267` and tested in `backend/internal/snapshot/indexer_test.go:10-50` plus handler-level tests at `backend/internal/api/handlers/snapshot_search_handler_test.go:107-128`.
- `backend/internal/snapshot/indexer.go:33-62` defines `GetIndexStatus(ctx, db, taskID)` returning `(indexed, total, indexing, err)`, but `rg` found no API route or frontend call using `GetIndexStatus`. Current frontend status type only supports `{ status, message }` (`web/src/lib/api/snapshots-api.ts:27-30`), and `SnapshotSearch` displays that message (`web/src/components/snapshot-search.tsx:56-63`, `:146-151`).

#### Restore/search API error response surfaces

- Search handler errors that are not explicit validation/not-found cases use `respondInternalError` (`snapshot_search_handler.go:65-68`, `:78-84`). `respondInternalError` logs the error server-side and returns a generic response body (`backend/internal/api/handlers/response.go:87-96`): `Message: "服务器内部错误"`.
- Snapshot list and file list handlers call restic executor methods and also route executor failures through `respondInternalError` (`backend/internal/api/handlers/snapshot_handler.go:76-82`, `:125-131`). The executor methods redact command output in error strings:
  - `ResticExecutor.ListSnapshots`: `sanitizeExecutorRuntimeOutput(output)` at `backend/internal/task/executor/restic_executor.go:301-304`.
  - `ResticExecutor.ListFiles`: `sanitizeExecutorRuntimeOutput(output)` at `backend/internal/task/executor/restic_executor.go:333-336`.
- Snapshot restore handler routes restic restore failures through `respondInternalError` and writes only a stage-based credential audit error (`backend/internal/api/handlers/snapshot_handler.go:219-223`, `:152-166`). `ResticExecutor.RestoreFiles` redacts output with `sanitizeExecutorRuntimeOutput(output)` at `backend/internal/task/executor/restic_executor.go:376-379`.
- `sanitizeExecutorRuntimeOutput` returns `"[输出已隐藏]"` for any non-empty command output (`backend/internal/task/executor/runtime_sanitize.go:23-28`). `sanitizeExecutorRuntimeEvidence` additionally redacts URLs, paths, hosts, and output markers (`runtime_sanitize.go:30-52`). Tests assert these behaviors in `backend/internal/task/executor/runtime_sanitize_test.go:8-60`.
- Adjacent snapshot diff handler inspected: `backend/internal/api/handlers/snapshot_diff_handler.go:123-127` returns `执行 restic diff 失败: %w` without appending command output, and the handler uses `respondInternalError`; normal diff response includes parsed changed paths by feature design (`snapshot_diff_handler.go:130-131`, `:145-183`).

#### Route and middleware context

- Snapshot search/list/files/diff read routes are registered with `middleware.RBAC("tasks:read")` and `middleware.OwnershipTaskCheck(dep.DB)` at `backend/internal/api/router.go:317-321`.
- Snapshot restore is registered as admin-only with step-up and credential-grant enforcement at `backend/internal/api/router.go:319`.
- Snapshot indexer SSH uses purpose-aware helpers: `executor.DialSSHForNodePurpose(ctx, task.Node, sshutil.PurposeSnapshot)` in `backend/internal/snapshot/indexer.go:172-176`; restic list/files/restore use the same snapshot purpose in `restic_executor.go:293-296`, `:321-324`, and `:364-367`.

### External References

None. The requested scope was local-only.

### Related Specs

- `.trellis/spec/backend/error-handling.md:68-73` — unexpected server errors should use `respondInternalError`; do not expose command output, SFTP/file content, diagnostic evidence, exported payloads, raw SQL/encryption details, or stack-like details to clients.
- `.trellis/spec/backend/logging-guidelines.md:68-80` — do not log secrets, raw endpoints, full command output, SFTP content, Docker output/volume names, node Doctor evidence, migration preflight command output, executor config, or credential-audit metadata containing raw remote evidence.
- `.trellis/spec/backend/quality-guidelines.md:224-251` — SSH key least-privilege scope applies to snapshots; denial errors returned to API clients must be concise and sanitized, without private keys, passwords, usernames plus hosts, raw endpoints, executor config, command output, stack/SQL/encryption details.
- `.trellis/spec/backend/quality-guidelines.md:451-469` — credential grants should store/respond with bounded safe fields and never store/output secrets, step-up proofs, commands, terminal streams, command output, file contents, exported payloads, raw SQL, endpoint/proxy values, or host-sensitive strings.

## Caveats / Not Found

- No current route or frontend API for `snapshot.GetIndexStatus` was found; the only current search status response is the fixed `{status:"indexing", message:"首次搜索，正在构建索引，请稍后重试"}` payload.
- No current synchronous call site for `snapshot.BuildIndex` or `snapshot.IncrementalIndex` outside `backend/internal/snapshot/indexer.go` was found. The current search-triggered background index path discards `BuildIndex` errors instead of surfacing them to clients.
- No raw restic command output was found in current snapshot search/index status API responses. The residual raw-output surface found locally is the `restic find` error string inside `backend/internal/snapshot/indexer.go`; exposure depends on a caller logging/returning that error, because the current async search trigger drops it.
- Snapshot search results intentionally return indexed file paths, snapshot IDs, sizes, and mtimes to authorized task readers. Broader file-browser content/path behavior was not analyzed per scope boundary.
- AppCredential policy hook redesign, Docker/Nginx logging, Vault/KMS/SSH CA/session recording/command approval, and deployment behavior were not researched per explicit exclusions.
