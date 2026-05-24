# Research: file/process residual P4 hardening

- **Query**: Identify the smallest local-only, behavior-compatible P4 hardening slice across file browser and process/node-log residual surfaces. Inspect backend/frontend code, tests, and Trellis specs around SFTP file browser list/content endpoints, credential audit metadata/error handling, node logs/process-log collection/parsing/response boundaries, file/download UI state, config export/import adjacency, and prior residual hardening patterns.
- **Scope**: internal
- **Date**: 2026-05-24

## Findings

### Files Found

| File Path | Description |
|---|---|
| `backend/internal/api/router.go` | Registers node file browser, node log config/query, alert logs, task backup files, and adjacent task/snapshot/config routes. |
| `backend/internal/api/handlers/file_handler.go` | SFTP file browser list/content handlers, local task backup file listing, SFTP dial helper, path validation, list/read limits, and file-browser credential audit writer. |
| `backend/internal/api/handlers/helpers.go` | Shared audit helpers: credential fallback, safe path hash, sanitized audit error messages, SSH outcome classification. |
| `backend/internal/credentialaudit/audit.go` | Central credential audit write boundary; sanitizes fields, metadata, and error output before persistence. |
| `backend/internal/api/handlers/credential_audit_handler.go` | Credential audit read/export boundary; re-sanitizes legacy metadata and error messages in list/CSV responses. |
| `backend/internal/api/handlers/node_logs_handler.go` | Node logs and alert-context log query endpoints; applies ownership filters, pagination bounds, path filter hashing, and response sanitization. |
| `backend/internal/api/handlers/node_log_config_handler.go` | Node log source configuration endpoint; validates whitelisted log paths and rejects dangerous prefixes/metacharacters. |
| `backend/internal/nodelogs/fetcher.go` | Builds remote journalctl/tail script, runs collection, parses output blocks, and advances cursors only on success. |
| `backend/internal/nodelogs/ssh_runner.go` | Production SSH runner for node-log collection; purpose-aware auth and sanitized credential audit on failures. |
| `backend/internal/nodelogs/parser.go` | Parses journalctl JSON and file chunks into log entries, sanitizing path/message at parse time. |
| `backend/internal/nodelogs/sanitize.go` | Node-log sanitizer; hashes paths and replaces messages/runtime evidence with placeholders. |
| `backend/internal/nodelogs/worker.go` | Worker collection boundary; sanitizes entries again before DB insert and logs fetch errors through sanitizer. |
| `backend/internal/model/models.go` | `Node`, `NodeLog`, and `NodeLogCursor` data shapes plus `Node.Sanitized()` and `DecodedLogPaths()`. |
| `backend/internal/task/runtime_sanitize.go` | Prior task/task-run runtime evidence sanitizer wrapper. |
| `backend/internal/runtimeevidence/sanitize.go` | Shared runtime evidence redaction patterns for task logs/errors. |
| `backend/internal/middleware/structured_logger.go` | HTTP process/access logger; logs URL path, not query string. |
| `web/src/lib/api/files-api.ts` | Typed file browser API client for node list/content and task backup files. |
| `web/src/components/file-browser.tsx` | File browser UI state for directory listing, navigation, errors, truncation, and file preview opening. |
| `web/src/components/file-preview-dialog.tsx` | File preview modal; fetches and renders file content with size/truncation warning. |
| `web/src/pages/nodes-page.dialogs.tsx` | Wires node file browser dialog to file APIs and root path selection. |
| `web/src/lib/api/node-logs.ts` | Typed node-log API client and query-string builder. |
| `web/src/pages/logs/logs-page.nodes.tsx` | Node-log query UI, filters, pagination, and table rendering. |
| `web/src/features/nodes-detail/log-config-tab.tsx` | Frontend node-log config UI and local validation. |
| `web/src/components/config-export-import.tsx` | Adjacent config export/import download/upload state and object URL revocation. |
| `web/src/pages/credential-audit-page.tsx` | Adjacent credential audit CSV download state and object URL revocation. |
| `web/src/lib/api/credential-audit-api.ts` | Frontend audit DTO mapping with metadata/error re-sanitization and known action list. |
| `web/src/types/domain.ts` | Frontend domain types for node-log query results and credential audit records. |
| `.trellis/spec/backend/quality-guidelines.md` | SSH key least-privilege scope contract, including file browser and node logs purpose strings. |
| `.trellis/spec/backend/error-handling.md` | Error response contract forbids exposing raw SQL, SSH secrets, command output, SFTP/file content, diagnostic evidence, and config payloads. |
| `.trellis/spec/backend/logging-guidelines.md` | Process logging contract forbids secrets, SFTP file contents, Docker output, node Doctor evidence, command output, executor config, and risky credential audit metadata. |
| `.trellis/spec/frontend/state-management.md` | Frontend local/server state guidance and storage-safety expectations for high-risk credential operation prompts. |
| `.trellis/spec/frontend/quality-guidelines.md` | Frontend API wrapper, local state, accessibility, and test expectations. |

### Code Patterns

#### Route and access boundaries

- Node file browser endpoints are protected by auth, `nodes:read`, and node ownership middleware: `GET /nodes/:id/files` and `GET /nodes/:id/files/content` in `backend/internal/api/router.go:164-165`.
- Node log config endpoints are protected by `logs:read`/`logs:write` plus ownership: `backend/internal/api/router.go:172-174`.
- Node logs query is protected by `logs:read`; alert-context logs by `alerts:read`: `backend/internal/api/router.go:176-180`.
- Local task backup file listing is admin-only: `GET /tasks/:id/backup-files` at `backend/internal/api/router.go:298`.
- Adjacent task and snapshot read routes remain under task RBAC/ownership: task run/log routes in `backend/internal/api/router.go:285-301`, snapshot routes in `backend/internal/api/router.go:317-320`.

#### SFTP file browser list/content endpoints

- File browser response limits are local and stable: preview max `1MB`, directory list max `500` entries in `backend/internal/api/handlers/file_handler.go:23-26`.
- `ListNodeFiles` flow: parse path, load node with SSH key, dial SFTP, validate path with `RealPath`, list directory, audit, and return clean path plus entries/truncation (`backend/internal/api/handlers/file_handler.go:76-143`).
- `GetNodeFileContent` flow: parse required path, load node, dial SFTP, validate path, reject directories, read at most `filePreviewMaxBytes+1`, audit preview byte count/truncation, and return clean path/content/size/truncation (`backend/internal/api/handlers/file_handler.go:160-265`).
- SFTP auth uses purpose-aware least-privilege helper `sshutil.BuildSSHAuthForPurpose(..., sshutil.PurposeFileBrowser)` before dial (`backend/internal/api/handlers/file_handler.go:355-380`).
- Remote path validation resolves both user input and allowed roots via SFTP `RealPath`, then compares resolved prefixes; it does not echo resolved paths in the out-of-scope error (`backend/internal/api/handlers/file_handler.go:389-455`). Allowed roots are `Node.BasePath` plus task `RsyncSource` values for the node (`backend/internal/api/handlers/file_handler.go:421-433`).
- Directory entries expose the normal file-browser payload (`name`, `path`, `is_dir`, `size`, `mode`, `mod_time`), and paths are expected UI data, not audit data (`backend/internal/api/handlers/file_handler.go:489-514`).
- Local task backup file listing uses `validateLocalPath` with clean/join/prefix checks and symlink resolution defense before `os.ReadDir` (`backend/internal/api/handlers/file_handler.go:281-329`, `backend/internal/api/handlers/file_handler.go:457-487`). This is adjacent and admin-only.

#### File-browser credential audit metadata/error handling

- File-browser audit metadata uses hashed paths (`path_hash`) and counts/sizes/truncation; it does not include raw paths, filenames, content, or command output (`backend/internal/api/handlers/file_handler.go:96-99`, `backend/internal/api/handlers/file_handler.go:121-136`, `backend/internal/api/handlers/file_handler.go:181-184`, `backend/internal/api/handlers/file_handler.go:204-217`, `backend/internal/api/handlers/file_handler.go:224-257`).
- `writeFileBrowserAudit` derives credential kind/source/SSH key ID using resolved credential data plus node fallback, sets action `file_browser.list` or `file_browser.preview`, purpose `file_browser`, node ID, outcome, and safe metadata (`backend/internal/api/handlers/file_handler.go:333-353`).
- Audit error messages are stage-only via `credentialAuditSafeError`, producing strings like `<stage> failed` instead of raw SFTP/SSH errors (`backend/internal/api/handlers/helpers.go:103-112`).
- `safePathHash` returns the first 16 hex chars of SHA-256 over the trimmed path (`backend/internal/api/handlers/helpers.go:94-101`).
- Central audit writer sanitizes metadata before persistence; denied key/value markers include private/password/token/secret/credential/config/output/stream/command/content/payload and bearer/authorization markers (`backend/internal/credentialaudit/audit.go:144-176`, `backend/internal/credentialaudit/audit.go:208-280`).
- Credential audit list/CSV re-sanitizes legacy rows, including metadata and stack/output-like error messages (`backend/internal/api/handlers/credential_audit_handler.go:42-84`, `backend/internal/api/handlers/credential_audit_handler.go:294-315`, `backend/internal/api/handlers/credential_audit_handler.go:359-523`).

#### Process/access logging adjacency

- HTTP structured logging stores `c.Request.URL.Path`, not `RequestURI` or query string, so file paths in `?path=` and node-log filters are not emitted by the standard process access logger (`backend/internal/middleware/structured_logger.go:13-28`).
- Backend logging guidelines explicitly forbid SFTP file contents, raw command output, diagnostic evidence, executor config, and credential audit metadata containing raw remote evidence (`.trellis/spec/backend/logging-guidelines.md:68-81`).
- File handler still has direct raw `Err(err)` process-log calls around SFTP dial/path/read/open/read failures (`backend/internal/api/handlers/file_handler.go:95`, `backend/internal/api/handlers/file_handler.go:109`, `backend/internal/api/handlers/file_handler.go:120`, `backend/internal/api/handlers/file_handler.go:223`, `backend/internal/api/handlers/file_handler.go:236`). These do not affect API behavior, but they are a local residual process-log surface compared with the node-log worker pattern below.
- Node-log worker logs fetch failures through `sanitizeNodeLogError(err)` rather than raw `Err(err)` (`backend/internal/nodelogs/worker.go:43-49`).

#### Node logs / process log collection, parsing, and response boundaries

- Node-log query applies ownership filtering, caps `node_ids` at 200, caps `page_size` at 500, bounds default time window to 1 hour, uses parameterized filters, and escapes SQL LIKE metacharacters for keyword search (`backend/internal/api/handlers/node_logs_handler.go:34-135`).
- Path filtering hashes/sanitizes the caller-provided path first and queries the sanitized path key (`backend/internal/api/handlers/node_logs_handler.go:81-83`).
- Query and alert-log responses copy rows and sanitize `Path` and `Message` again before returning (`backend/internal/api/handlers/node_logs_handler.go:131-135`, `backend/internal/api/handlers/node_logs_handler.go:190-205`).
- Alert-context logs are constrained to the alert node and a ±5-minute window, with max 500 rows plus `has_more` (`backend/internal/api/handlers/node_logs_handler.go:147-191`).
- Node-log source configuration allows at most 20 paths, requires absolute paths, rejects wildcards, rejects shell metacharacters `$`, backtick, backslash, newline, carriage return, double quote, single quote, and rejects denied prefixes `/etc/`, `/proc/`, `/sys/`, `/dev/`, `/boot/`, `/root/` (`backend/internal/api/handlers/node_log_config_handler.go:32-57`).
- The fetcher only runs when journalctl or file paths are configured, builds a fixed script, runs with `FetchTimeout=15s` and `MaxFetchBytes=10MB`, and does not update cursors on SSH/fetch error (`backend/internal/nodelogs/fetcher.go:29-44`, `backend/internal/nodelogs/types.go:63-70`).
- Script generation is server-side allowlisted: journalctl with fixed fields and configured file paths through `stat` + `tail`. Cursor/path arguments are single-quoted via `shellQuote` (`backend/internal/nodelogs/fetcher.go:100-140`).
- Production runner uses purpose-aware auth `PurposeNodeLogs`; failure audits include only action `node_logs.collect`, purpose `node_logs`, node/key identity, stage, and max bytes, with `ErrorMessage` set to `<stage> failed` (`backend/internal/nodelogs/ssh_runner.go:27-84`, `backend/internal/nodelogs/ssh_runner.go:86-112`).
- Journal parsing stores sanitized systemd unit path and sanitized message placeholder; malformed JSON lines are skipped (`backend/internal/nodelogs/parser.go:42-79`).
- File parsing consumes only complete newline-terminated lines, hashes the configured path, stores placeholder message text, and advances offset only through the final complete line (`backend/internal/nodelogs/parser.go:81-110`).
- Sanitizer behavior: `SanitizeLogPath` returns `[日志路径:<16-hex>]`; `SanitizeLogMessage` returns `[日志内容已隐藏]` for non-empty evidence after redaction; `sanitizeNodeLogEvidence` redacts URLs, output markers, remote/named/absolute/Windows paths, IPs, hostnames, and host-sensitive fragments (`backend/internal/nodelogs/sanitize.go:13-66`, `backend/internal/nodelogs/sanitize.go:82-133`).
- Worker applies `sanitizeLogEntries` again before `CreateInBatches`, adding defense in depth even if a parser path changes (`backend/internal/nodelogs/worker.go:51-58`, `backend/internal/nodelogs/sanitize.go:75-80`).
- No separate process-list/process-detail API was found in the searched backend/frontend paths. The process-like surface in scope is node OS log collection and backend process/access logging.

#### Frontend file browser and download/UI state

- File API wrappers use `URLSearchParams` for `path` and support `AbortSignal` for list and content calls (`web/src/lib/api/files-api.ts:27-51`).
- `FileBrowser` aborts stale directory loads with an `AbortController`, clears directory errors before load, keeps state component-local, and does not use browser storage (`web/src/components/file-browser.tsx:65-95`).
- Clicking a file sets `previewPath` and opens `FilePreviewDialog`; content fetch is delegated to the parent `fetchContent` callback (`web/src/components/file-browser.tsx:101-107`, `web/src/components/file-browser.tsx:250-257`).
- `FilePreviewDialog` clears content/error when opened and then fetches preview content, but its current prop signature does not accept an `AbortSignal`, and it does not clear preview content on close/unmount (`web/src/components/file-preview-dialog.tsx:16-21`, `web/src/components/file-preview-dialog.tsx:36-52`). This is the smallest frontend residual state surface found: file content can remain in component memory after the preview dialog closes or after a late fetch resolves while closed. It is not persisted to `localStorage`/`sessionStorage`.
- The preview renders content only in the dialog body while not loading and not erroring; large content is already capped by backend and marked as truncated (`web/src/components/file-preview-dialog.tsx:81-88`).
- Node page wiring selects a root path from `basePath`, `/root`, or `/home/<username>` and calls the file API wrappers (`web/src/pages/nodes-page.dialogs.tsx:159-206`).
- Config export/import adjacent download state uses object URLs and calls `URL.revokeObjectURL(url)` after click (`web/src/components/config-export-import.tsx:32-40`). Sensitive export/import flows use step-up and grant prompts without storing grant data in browser storage (`web/src/components/config-export-import.tsx:119-159`).
- Credential audit CSV export also revokes object URLs after click (`web/src/pages/credential-audit-page.tsx:194-209`).

#### Frontend node-log UI state

- Node-log API wrapper serializes filters to query params and uses typed request wrapper (`web/src/lib/api/node-logs.ts:21-47`).
- `NodeLogsPanel` keeps filter/result/loading state component-local, calls `apiClient.queryNodeLogs`, and renders sanitized backend data directly (`web/src/pages/logs/logs-page.nodes.tsx:84-105`, `web/src/pages/logs/logs-page.nodes.tsx:365-423`).
- Frontend node-log path validation currently mirrors denied prefixes and wildcard checks but does not mirror the backend shell metacharacter deny-list (`web/src/features/nodes-detail/log-config-tab.tsx:11-25`). Backend remains the security boundary, so this is a UX consistency gap rather than a backend residual data leak.
- Frontend domain types expose node-log entry fields (`path`, `message`) as strings; backend already ensures those are sanitized placeholders/hashes before response (`web/src/types/domain.ts:841-868`).

#### Config export/import adjacency

- Backend config export omits node passwords/private keys and SSH private keys unless `include_secrets=true`; it still exports node host/username/base_path and task command/path fields as configuration data (`backend/internal/api/handlers/config_handler.go:59-255`).
- Backend config import caps request body at 10MB, decodes JSON into selected config sections, updates/creates SSH keys/nodes/policies/tasks/settings in a transaction, and writes count-only credential audit metadata (`backend/internal/api/handlers/config_handler.go:271-702`).
- Sensitive config export is admin/step-up gated in routing and writes safe audit metadata; config import writes safe count-only audit metadata (`backend/internal/api/router.go:328-329`, `backend/internal/api/handlers/config_handler.go:228-243`, `backend/internal/api/handlers/config_handler.go:677-691`).
- No direct dependency from file browser or node-log hardening to config export/import was found beyond the shared credential audit and step-up/storage-safety patterns.

#### Prior residual hardening patterns

- Task run/log response boundaries sanitize legacy stored runtime evidence before returning task run details/logs/drill evidence (`backend/internal/api/handlers/task_run_handler.go:24-62`).
- Task logs sanitize before persistence/publish via `sanitizeTaskLogMessage` (`backend/internal/task/log_writer.go:60-80`).
- Runtime evidence redaction centralizes URL, command lifecycle, output marker, remote path, absolute path, host/IP, and host-sensitive token redaction (`backend/internal/runtimeevidence/sanitize.go:24-48`).
- Runtime sanitizer tests cover legacy stored evidence, command lifecycle text, query-string tokens, hostnames, remote paths, and raw output suppression (`backend/internal/task/runtime_sanitize_test.go:17-87`).
- Credential audit writer and read/export handlers sanitize both write-time and legacy read-time evidence (`backend/internal/credentialaudit/audit.go:144-176`, `backend/internal/api/handlers/credential_audit_handler.go:294-315`).
- HTTP process logging stores only path, not query string, matching the prior Nginx/access-log query-string hardening pattern (`backend/internal/middleware/structured_logger.go:15-28`).

### Tests Found

| File Path | Relevant Coverage |
|---|---|
| `backend/internal/api/handlers/file_handler_validate_test.go` | SFTP path validation via RealPath, symlink escape rejection, dev-only bypass, task `RsyncSource` roots, SFTP client interface smoke, and file-browser audit metadata not persisting raw path/content/output (`:120-378`). |
| `backend/internal/api/handlers/node_logs_handler_test.go` | Node-log query filtering/pagination, response sanitization of legacy raw evidence, sanitized path filter behavior, alert-log response sanitization, and settings validation (`:91-445`). |
| `backend/internal/api/handlers/node_log_config_handler_test.go` | Log config defaults, path whitelist persistence, rejected paths not echoed, denied prefixes, shell metacharacters, wildcards, count and retention bounds, RBAC (`:62-218`). |
| `backend/internal/nodelogs/parser_test.go` | Journal/file parse sanitization, malformed placeholder rehashing, worker defense-in-depth sanitizer, sanitized error evidence, malformed JSON skip, partial line offset behavior (`:9-239`). |
| `backend/internal/nodelogs/fetcher_test.go` | Empty/no source behavior, cursor handling, file rotation, SSH errors, script quoting against command substitution, journal disabled behavior (`:24-221`). |
| `backend/internal/nodelogs/worker_test.go` | Worker inserts sanitized logs, advances cursor on success, preserves cursor on fetch failure (`:14-137`). |
| `backend/internal/credentialaudit/audit_test.go` | Audit write timestamp, output redaction in error messages, metadata key/value filtering and field bounds (`:15-161`). |
| `backend/internal/api/handlers/credential_audit_handler_test.go` | Audit list filtering, legacy metadata/error sanitization, endpoint/secret redaction, CSV export using sanitized DTO (`:19-235`). |
| `backend/internal/ws/hub_test.go` | Prior task-log backfill read-boundary sanitization without mutating rows (`:74-149`). |
| `backend/internal/task/runtime_sanitize_test.go` | Prior task runtime evidence redaction, including URL query-string tokens and command/output markers (`:17-87`). |
| `web/src/pages/logs/logs-page.nodes.test.tsx` | NodeLogsPanel filter UI, query invocation shape, pagination behavior (`:68-138`). |
| `web/src/features/nodes-detail/log-config-tab.test.tsx` | Log config UI load/save and relative-path validation (`:50-107`). |
| `web/src/components/config-export-import.test.tsx` | Adjacent config import/export behavior (not read in full for this topic, discovered by filename). |
| `web/src/lib/api/credential-audit-api.test.ts` | Frontend audit mapper/query tests (discovered by filename and API mapper read). |

### Related Specs

- `.trellis/spec/backend/quality-guidelines.md` — SSH key least-privilege scope scenario applies to file browser and node logs. Known purpose strings include `file_browser` and `node_logs`; new managed-key SSH use sites must call purpose-aware helpers; denial errors must be sanitized (`:224-275`).
- `.trellis/spec/backend/error-handling.md` — API errors must use response helpers and must not expose raw SQL, encryption, SSH private keys, tokens, command output, SFTP/file content, Docker output, diagnostic evidence, exported config payloads, or stack-like details (`:54-74`).
- `.trellis/spec/backend/logging-guidelines.md` — Structured process logs should use stable fields and must not log secrets, raw endpoints, SFTP file contents, command output, node Doctor evidence, executor config, or risky credential audit metadata (`:37-81`).
- `.trellis/spec/frontend/state-management.md` — Page/dialog state should stay local; high-risk credential operation prompts must not store grant IDs/reasons/status in browser storage; user-visible denial details must be sanitized/bounded (`:47-52`, `:84-103`).
- `.trellis/spec/frontend/quality-guidelines.md` — Use typed API wrappers, explicit loading/empty/error states, accessible controls, and tests for behavior changes (`:18-28`, `:32-44`, `:52-73`).

### External References

- Not used. The requested scope is local/internal and behavior compatibility depends on repository-specific handlers, specs, and tests.

## Smallest Local-Only P4 Slice Recommendation

The smallest behavior-compatible slice is **not** a broad file-browser/node-log API redesign. Existing backend response and audit boundaries for SFTP file browser and node logs already follow the prior residual-hardening pattern: purpose-aware SSH helpers, bounded reads, hashed paths/count-only metadata, generic audit errors, write-time and read-time sanitization, ownership/RBAC, and query-string-safe access logging.

Recommended minimal slice:

1. **File preview UI residual state hardening**: thread `AbortSignal` through `FilePreviewDialog`/`FileBrowser` to `getNodeFileContent`, abort in-flight content fetches on close/unmount, and clear `content`, `size`, `truncated`, and `error` when the preview closes. This is local-only, preserves visible behavior, and directly addresses file-content residual state in memory.
2. **File-browser process-log hardening**: replace raw SFTP/file-handler process-log `Err(err)` calls with safe structured fields (`node_id`, `stage`, `path_hash` where applicable) and generic messages. API responses and credential audit output remain unchanged, while process logs align with the node-log worker’s sanitized-error pattern and backend logging spec.
3. **Regression tests only where behavior is observable locally**: add/update tests for file preview abort/clear state and, if log capture is available, assert file-browser audit/process-log paths do not include raw file path/content/output. Node-log parser/query/audit boundaries already have substantial coverage; avoid changing node-log storage/response semantics unless a new failing case is found.

## Caveats / Not Found

- No separate process-list/process-detail API was found; “process” scope appears to mean backend process/access logs and node OS log collection.
- SFTP list/content responses intentionally return file paths and preview content to authorized users; redacting those responses would change product behavior and is outside the smallest compatible slice.
- Node-log messages are intentionally collapsed to `[日志内容已隐藏]`; keyword search therefore operates on sanitized stored text, which is existing behavior and not a new P4 slice.
- Frontend node-log path validation does not mirror the backend shell-metacharacter deny-list, but backend enforcement is already present; mirroring it would be UX consistency rather than a required security boundary for the smallest slice.
- Config export/import adjacency uses count-only audit metadata and object URL revocation; no direct file/node-log residual dependency was found there.
