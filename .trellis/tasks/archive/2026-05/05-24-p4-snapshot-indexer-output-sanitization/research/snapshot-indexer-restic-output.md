# Research: snapshot-indexer-restic-output

- **Query**: Research the next P4 residual hardening slice for Xirang: snapshot indexer/restic find raw output error sanitization. Context: previous P4 tasks completed legacy task evidence read-boundary sanitization and WebSocket task-log backfill sanitization. Suspected code path is `backend/internal/snapshot/indexer.go` where `restic find` failure currently includes trimmed raw command output in an error. Requirements: local-only, behavior-compatible, no external Vault/KMS/SSH CA/session recording/command approval, preserve API/deployment/UI behavior, and do not expose raw secrets, executor config, command text/output, endpoints, hostnames, paths, include/target paths, Docker output, diagnostic output, raw SQL, or host-sensitive strings in responses/logs/audit/docs/UI storage.
- **Scope**: internal
- **Date**: 2026-05-24

## Findings

### Files Found

| File Path | Description |
|---|---|
| `backend/internal/snapshot/indexer.go` | Snapshot indexer implementation; constructs and runs `restic find --json --long --path=/ ... 2>&1`; current failure wraps raw trimmed command output into the returned error. |
| `backend/internal/snapshot/indexer_test.go` | Snapshot package tests for `EscapeLikePattern`, `parseResticFindOutput`, and restic repository access/env-prefix behavior; no current test covers `restic find` failure-output sanitization. |
| `backend/internal/api/handlers/snapshot_search_handler.go` | API search endpoint that calls `snapshot.EnsureIndexed`; if the first search triggers a background build, it returns an `indexing` status; direct `EnsureIndexed` errors go to `respondInternalError`. |
| `backend/internal/api/router.go` | Registers `GET /api/v1/tasks/:id/snapshots/search` behind `RBAC("tasks:read")` and `OwnershipTaskCheck`. |
| `backend/internal/task/executor/ssh_connect.go` | Defines `RunSSHCommandOutput`, a raw combined stdout/stderr SSH primitive; caller owns sanitization before persistence/response/logging. |
| `backend/internal/task/executor/restic_executor.go` | Restic snapshot/file/restore helper methods; comparable failure paths hide non-empty output with `sanitizeExecutorRuntimeOutput(output)` instead of embedding raw output. |
| `backend/internal/task/executor/runtime_sanitize.go` | Executor runtime sanitizer and output hider; `sanitizeExecutorRuntimeOutput` returns `[输出已隐藏]` for non-empty output, but it is package-private to `executor`. |
| `backend/internal/runtimeevidence/sanitize.go` | Shared runtime-evidence sanitizer used by task log/read-boundary and WebSocket backfill paths; redacts output markers, command lifecycle text, URLs/endpoints, paths, hosts, and host-sensitive fragments. |
| `backend/internal/task/runtime_sanitize.go` | Task package wrapper around `runtimeevidence.SanitizeTaskRuntimeEvidence`; includes non-empty raw output suppression through `sanitizeTaskRuntimeOutput`. |
| `backend/internal/task/log_writer.go` | Prior P4 task-log write path; `Manager.emitLog` sanitizes before DB persistence and WebSocket publication. |
| `backend/internal/api/handlers/task_run_handler.go` | Prior P4 read-boundary path; sanitizes legacy `TaskRun.LastError`, `TaskLog.Message`, and drill error evidence before API responses. |
| `backend/internal/ws/hub.go` | Prior P4 WebSocket backfill path; sanitizes backfilled task-log messages before sending `LogEvent`. |
| `backend/internal/model/models.go` | `SnapshotFileIndex` model stores indexed snapshot paths (`task_id`, `snapshot_id`, `path`, `size`, `mtime`); search API returns `path` by feature design. |
| `backend/internal/database/migrations/sqlite/000054_snapshot_file_index.up.sql` | SQLite schema for `snapshot_file_indices`, including unique `(task_id, snapshot_id, path)` and `path` index. |
| `backend/internal/database/migrations/postgres/000054_snapshot_file_index.up.sql` | PostgreSQL schema for `snapshot_file_indices`, matching SQLite. |
| `web/src/lib/api/snapshots-api.ts` | Frontend snapshot API client; `searchFiles` calls `/tasks/{id}/snapshots/search?q=...` and returns either results or indexing status. |
| `web/src/components/snapshot-search.tsx` | UI for snapshot search; stores/renders search results and shows API errors via toast; no extra sanitization beyond backend/client error handling. |
| `.trellis/spec/backend/error-handling.md` | Backend spec forbidding client exposure of command output, Docker output, diagnostic evidence, raw SQL/encryption/stack details, and other sensitive material. |
| `.trellis/spec/backend/logging-guidelines.md` | Backend spec forbidding logs of full command output, Docker output/volume names, diagnostic evidence, executor config, decrypted values, and secrets. |
| `.trellis/spec/backend/quality-guidelines.md` | Backend spec requiring shared sanitization for user-visible evidence/command-output-bearing text and output-marker redaction in credential-audit errors. |
| `.trellis/spec/backend/database-guidelines.md` | Credential audit storage contract forbidding raw credentials, decrypted executor config, terminal streams, command output, and file contents. |
| `.trellis/tasks/archive/2026-05/05-23-p4-task-runtime-log-sanitization/research/task-runtime-log-sanitization.md` | Prior P4 research noting task runtime evidence sanitization as the previous slice and deferring deeper restic/rclone executor-level output summarization. |
| `.trellis/tasks/archive/2026-05/05-24-p4-next-security-hardening/research/next-p4-slice.md` | Prior P4 selection research identifying snapshot file/diff/search path exposure as product behavior, while runtime evidence/output sanitization remained a separate concern. |
| `.trellis/tasks/archive/2026-05/05-24-p4-residual-security-review/research/rclone-executor-residual.md` | Residual review stating `RunSSHCommandOutput` returns raw output and current safety depends on each caller sanitizing, hiding, or discarding output before persistence/response. |

### Code Patterns

#### Snapshot indexer `restic find` command path

`backend/internal/snapshot/indexer.go` builds the full snapshot file index for restic tasks:

- `EnsureIndexed(ctx, db, taskID)` checks whether any `SnapshotFileIndex` row exists, loads the task with `Node` and `Node.SSHKey`, and starts `BuildIndex` in a goroutine when no index exists (`backend/internal/snapshot/indexer.go:66-99`). The goroutine currently discards the returned error (`indexer.go:92-96`), so background failures are not written to task logs/audit by this code path.
- `BuildIndex(ctx, db, task)` guards duplicate builds with `indexingJobs`, requires `ExecutorType == "restic"`, lists snapshots, skips already-indexed snapshots, and wraps per-snapshot indexing errors as `索引快照 <shortID> 失败: %w` (`indexer.go:120-164`).
- `indexSnapshot(ctx, db, task, snapshotID)` dials SSH with purpose `sshutil.PurposeSnapshot`, resolves restic repository access from `Task.ExecutorConfig`, builds `RESTIC_PASSWORD=... restic find --json --long --path=/ <snapshot> -r <repo> 2>&1`, then calls `executor.RunSSHCommandOutput` (`indexer.go:171-195`).
- The current failure branch is the raw-output surface:

```go
output, err := executor.RunSSHCommandOutput(ctx, client, cmd)
if err != nil {
    return fmt.Errorf("restic find 执行失败: %w, 输出: %s", err, strings.TrimSpace(output))
}
```

This includes trimmed combined stdout/stderr in the error (`backend/internal/snapshot/indexer.go:194-197`). Because the command uses `2>&1`, `output` may contain restic diagnostics, repository paths/endpoints, remote filesystem paths from JSON matches, hostnames, or other command-output strings.

#### Raw output primitive and comparable restic helpers

- `executor.RunSSHCommandOutput` returns `session.CombinedOutput(cmd)` verbatim plus any SSH/command error (`backend/internal/task/executor/ssh_connect.go:162-185`). It does not sanitize by design; caller code must decide whether to parse, persist, return, log, hide, or redact.
- Restic helper methods in `backend/internal/task/executor/restic_executor.go` already hide output on similar helper failures:
  - `ListSnapshots` returns `获取快照列表失败: %w, 输出: %s` with `sanitizeExecutorRuntimeOutput(output)` (`restic_executor.go:301-304`).
  - `ListFiles` returns `获取文件列表失败: %w, 输出: %s` with `sanitizeExecutorRuntimeOutput(output)` (`restic_executor.go:333-336`).
  - `RestoreFiles` returns `恢复失败: %w, 输出: %s` with `sanitizeExecutorRuntimeOutput(output)` (`restic_executor.go:376-379`).
- `sanitizeExecutorRuntimeOutput` is intentionally simple: if output is non-empty, return `[输出已隐藏]`; otherwise return `""` (`backend/internal/task/executor/runtime_sanitize.go:23-28`). Tests assert it suppresses raw output containing paths, hostnames, and fake tokens (`backend/internal/task/executor/runtime_sanitize_test.go:47-60`).
- The snapshot indexer package cannot currently call `sanitizeExecutorRuntimeOutput` because it is unexported to the `executor` package. The shared `runtimeevidence.SanitizeTaskRuntimeEvidence` is exported and redacts output markers, command lifecycle text, URLs/endpoints, paths, hostnames, IPv4s, and host-sensitive fragments (`backend/internal/runtimeevidence/sanitize.go:24-49`), but it does not collapse an arbitrary standalone non-empty output string to `[输出已隐藏]` unless an output marker is present.

#### API/log/audit propagation for the suspected path

- The search endpoint calls `snapshot.EnsureIndexed` and sends any synchronous error to `respondInternalError` (`backend/internal/api/handlers/snapshot_search_handler.go:64-68`). `respondInternalError` logs the raw `err` server-side with `logger.Module("api").Error().Err(err).Str("path", c.FullPath())` and returns a generic `服务器内部错误` response (`backend/internal/api/handlers/response.go:87-96`). Thus the raw `restic find` output is not directly returned to the client through the normal internal-error response, but it can be logged if the error is returned synchronously to a handler or future caller.
- The normal first-search path starts indexing asynchronously and returns `{"status":"indexing","message":"首次搜索，正在构建索引，请稍后重试"}` without exposing the indexer error (`snapshot_search_handler.go:70-72`). The goroutine discards `BuildIndex` errors (`indexer.go:92-96`).
- No credential-audit write was found in the snapshot indexer build/search path. Snapshot restore has credential audit metadata that stores include count, target-set boolean, and shortened snapshot ID rather than raw include paths or target path (`backend/internal/api/handlers/snapshot_handler.go:139-167`, `219-226`), but that is separate from the indexer.
- Snapshot search results intentionally return indexed `path` values (`SearchResult.Path`) from `snapshot_file_indices` (`snapshot_search_handler.go:22-28`, `75-87`), and the frontend renders those paths in `SnapshotSearch` (`web/src/components/snapshot-search.tsx:173-195`). This path exposure is part of the snapshot search/browser product behavior and is distinct from failure-output exposure.

#### Prior P4 sanitizer precedent

- Task log writes now sanitize before persistence and WebSocket publication: `Manager.emitLog` applies `sanitizeTaskLogMessage(message)` before queueing/storing/publishing (`backend/internal/task/log_writer.go:60-67`, `120-132`).
- Task-run read endpoints sanitize legacy stored evidence before responses: `sanitizeTaskRunForResponse`, `sanitizeTaskLogForResponse`, and `sanitizeRestoreDrillEvidenceForResponse` call `task.SanitizeRuntimeEvidenceForRead` (`backend/internal/api/handlers/task_run_handler.go:24-61`).
- WebSocket task-log backfill sanitizes stored messages before returning `LogEvent` (`backend/internal/ws/hub.go:322-333`).
- The shared runtime-evidence sanitizer covers URLs/endpoints, output markers, command lifecycle text, remote/named/absolute/Windows paths, IPs, hostnames, and host-sensitive fragments (`backend/internal/runtimeevidence/sanitize.go:11-49`). Tests in `backend/internal/task/runtime_sanitize_test.go` cover legacy read-boundary hiding for command text, stdout/output markers, endpoint tokens, paths, hostnames, and host-sensitive labels.

#### Tests already near the target package

- `backend/internal/snapshot/indexer_test.go` covers parsing and escaping behavior but not the error string from `indexSnapshot` failure. The target function performs a real SSH dial, so direct failure-output testing may need a small helper or package-local sanitizer test rather than a live SSH path.
- Existing fake-secret fixture naming convention uses `FAKE_..._FOR_TEST_ONLY`, documented in `.trellis/spec/backend/quality-guidelines.md:523-559`. Current snapshot tests already follow that convention for restic repository password fixtures (`backend/internal/snapshot/indexer_test.go:121-143`).

### External References

No external references were used; this is local-only code/spec/Trellis research.

### Related Specs

- `.trellis/spec/backend/error-handling.md` — API error responses must not expose raw SQL, encryption, SSH private key, token, command output, SFTP/file content, Docker output, diagnostic evidence, exported config payloads, or stack-like details (`error-handling.md:68-74`). Doctor/restore-drill sections additionally prohibit command text/output, endpoints, hostnames, and raw paths in evidence/error fields (`error-handling.md:107-109`, `354-355`).
- `.trellis/spec/backend/logging-guidelines.md` — logs must not contain passwords, private keys, tokens, decrypted values, full command output that may contain credentials, Docker command output/volume names, node Doctor evidence, migration preflight command output, executor config, or raw remote evidence (`logging-guidelines.md:68-81`).
- `.trellis/spec/backend/quality-guidelines.md` — user-visible evidence, delivery errors, drill output, incident messages, or notification payloads that may contain command output should use the shared sanitizer (`quality-guidelines.md:41-44`); credential-audit errors redact after output markers such as `输出:`, `output:`, `stdout:`, and `stderr:` (`quality-guidelines.md:401-413`).
- `.trellis/spec/backend/database-guidelines.md` — credential audit rows must store sanitized error text/metadata and must not store raw credentials, decrypted executor config, terminal streams, command output, or file contents (`database-guidelines.md:85-89`).
- `.trellis/tasks/archive/2026-05/05-23-p4-task-runtime-log-sanitization/research/task-runtime-log-sanitization.md` — prior research explicitly deferred deeper restic/rclone executor-level output summarization after shared task-log sanitization.
- `.trellis/tasks/archive/2026-05/05-24-p4-residual-security-review/research/rclone-executor-residual.md` — residual review notes `RunSSHCommandOutput` is a raw-output primitive and safety depends on each caller sanitizing before persistence/response; it lists restic snapshot/file helpers as sanitized but the snapshot indexer raw-output branch remains a specific exception.

## Caveats / Not Found

- No external docs were needed because the task is an internal local-only hardening slice.
- No evidence was found that the current `restic find` indexer failure is returned verbatim to API clients on the normal first-search path; direct client response is generic when `respondInternalError` is used. The raw-output concern is primarily the returned error object and server log/future-caller surface.
- The indexer currently discards asynchronous `BuildIndex` errors, so failed background indexing may be invisible to users/operators except through the `IsIndexing`/search behavior. This research records behavior only and does not propose workflow changes.
- Snapshot search/browser result paths and `snapshot_file_indices.path` storage are existing feature behavior. Sanitizing the `restic find` failure output can be scoped separately from changing stored/indexed snapshot path semantics.
- `sanitizeExecutorRuntimeOutput` is the closest behavior-compatible precedent but is package-private; `runtimeevidence.SanitizeTaskRuntimeEvidence` is exported but does not replace arbitrary non-empty raw output with a single placeholder unless an output marker is present.
