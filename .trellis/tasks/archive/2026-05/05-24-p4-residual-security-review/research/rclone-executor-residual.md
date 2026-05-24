# Research: rclone executor residual security surface

- **Query**: Research residual P4 risk surface: rclone executor config/materialization and related task executor secret handling in `/Users/weibo/Code/xirang`; determine whether raw private keys, passwords, bearer tokens, executor config, command text/output, endpoints, hostnames, include/target paths, or host-sensitive strings are stored/returned/logged/audited/UI-persisted after completed P4 slices. Include files inspected, current protections, remaining risks, whether a minimal behavior-compatible slice exists, and a recommendation.
- **Scope**: internal
- **Date**: 2026-05-24

## Findings

### Files Found

| File Path | Description |
|---|---|
| `backend/internal/task/executor/rclone_executor.go` | rclone executor config parsing, sync/restore command construction, and streamed output handling. |
| `backend/internal/task/executor/restic_repository_access.go` | Restic repository password resolver/access object used as comparison for secret-bearing executor config. |
| `backend/internal/task/executor/restic_executor.go` | Restic execution and snapshot/file helper behavior inspected earlier as the main secret-bearing executor comparison. |
| `backend/internal/task/executor/command_executor.go` | Command executor runtime command/output handling. |
| `backend/internal/task/executor/executor.go` | Rsync executor implementation and shared `ShellEscape`; `rsync_executor.go` does not exist. |
| `backend/internal/task/executor/runtime_sanitize.go` | Executor runtime evidence sanitizer for stdout/stderr, paths, endpoints, hostnames, and output markers. |
| `backend/internal/task/executor/ssh_connect.go` | SSH credential resolution/audit behavior and `RunSSHCommandOutput` raw output return contract. |
| `backend/internal/task/runtime_sanitize.go` | Task-level runtime log/error sanitizer used before persistence. |
| `backend/internal/task/log_writer.go` | Task log persistence and WebSocket publish path. |
| `backend/internal/task/runner.go` | Task execution lifecycle, last-error persistence, verification alerts, and credential audit context. |
| `backend/internal/task/hook.go` | Pre/post hook execution over SSH and hook error sanitization. |
| `backend/internal/task/verifier/verifier.go` | Restic/rclone verification commands and sanitized verifier output/result handling. |
| `backend/internal/task/verifier/runtime_sanitize.go` | Verifier sanitizer mirroring executor/task sanitization. |
| `backend/internal/task/retention.go` | rclone/restic retention command output/error handling. |
| `backend/internal/task/integrity_checker.go` | rclone/restic periodic integrity command output/error handling. |
| `backend/internal/model/models.go` | `Task`, `TaskRun`, `Policy`, `AppCredential`, `Node`, `SSHKey`, and encryption/JSON tags. |
| `backend/internal/api/handlers/task_handler.go` | Task list/get/create/update/trigger API surfaces and executor config merge behavior. |
| `backend/internal/api/handlers/task_run_handler.go` | Task-run detail/log APIs, nested task preload, and legacy log response surface. |
| `backend/internal/api/handlers/batch_handler.go` | Batch command create/get APIs and returned task records. |
| `backend/internal/api/handlers/config_handler.go` | Config import/export boundaries. |
| `backend/internal/api/handlers/docker_handler.go` | Docker volume discovery command execution and response/audit behavior. |
| `backend/internal/api/handlers/file_handler.go` | Node file browser/list/preview and local backup-file browsing paths. |
| `backend/internal/api/handlers/snapshot_handler.go` | Snapshot list/file/restore APIs and restore audit metadata. |
| `backend/internal/api/handlers/snapshot_diff_handler.go` | Restic diff output parsing and changed-path response surface. |
| `backend/internal/api/handlers/snapshot_search_handler.go` | Snapshot file index search response surface. |
| `backend/internal/api/handlers/node_migrate_handler.go` | Node migration data-copy response and local path result messages. |
| `backend/internal/api/handlers/app_credential_handler.go` | AppCredential CRUD, password-preserving update, and policy hook cascade behavior. |
| `backend/internal/api/handlers/policy_handler.go` | App profile hook rendering at policy create/update and policy response shape. |
| `backend/internal/profile/profile.go` | Built-in app profile hook templates and rendering function. |
| `backend/internal/profile/app_profile_access.go` | AppCredential config resolver/access object and safe metadata. |
| `backend/internal/credentialaudit/audit.go` | Credential audit write-time metadata/error sanitization. |
| `backend/internal/api/handlers/credential_audit_handler.go` | Credential audit list/export response-time re-sanitization. |
| `backend/internal/middleware/audit.go` | HTTP audit middleware persistence shape. |
| `backend/internal/alerting/dispatcher.go` | Alert persistence for task/verification/retention/integrity failures. |
| `web/src/components/task-create-dialog.tsx` | Frontend task draft mapping and executor config construction. |
| `web/src/components/task-create-dialog.advanced.tsx` | Frontend command/rclone/restic form fields and edit display behavior. |
| `web/src/lib/api/tasks-api.ts` | Frontend task response mapper; inspected earlier. |
| `web/src/lib/api/task-runs-api.ts` | Frontend task-run response mapper; nested task fields are typed but not consumed by `mapTaskRun`. |
| `web/src/lib/api/batch-api.ts` | Frontend batch status mapper; inspected earlier and ignores task command. |
| `web/src/lib/ws/logs-socket.ts` | Frontend log WebSocket display path; inspected earlier. |
| `web/src/pages/tasks-page.table.tsx` | Frontend task table last-error display path; inspected earlier. |

### Code Patterns

#### rclone executor config/materialization

- `RcloneConfig` currently has only non-secret knobs: `bandwidth_limit` and `transfers` (`backend/internal/task/executor/rclone_executor.go:20-24`). The frontend creates the same shape for rclone tasks (`web/src/components/task-create-dialog.tsx:163-168`).
- `Task.ExecutorConfig` is encrypted at rest through GORM hooks and omitted from JSON (`backend/internal/model/models.go:304-308`, `backend/internal/model/models.go:326-347`). This protects rclone config even though current rclone config does not carry secret material.
- rclone command construction uses shell escaping for source/dest/bandwidth args and does not log the command string (`backend/internal/task/executor/rclone_executor.go:72-77`, `backend/internal/task/executor/rclone_executor.go:203-219`).
- rclone streamed stdout is passed through `sanitizeExecutorRuntimeEvidence(line)` before `logf` (`backend/internal/task/executor/rclone_executor.go:149-156`).
- rclone verification also does not log command text; verifier output/errors are sanitized before being logged or returned in `Result.Message` (`backend/internal/task/verifier/verifier.go:526-535`).
- rclone retention and integrity helpers build commands containing source/target paths, but raw combined output is sanitized before task log/alert persistence (`backend/internal/task/retention.go:180-190`, `backend/internal/task/integrity_checker.go:109-119`).

Current conclusion for rclone itself: no raw rclone executor config secret surface was found. The remaining rclone-related sensitive surfaces are task path/remote strings (`RsyncSource`, `RsyncTarget`) and runtime output text, not rclone-specific config materialization.

#### Runtime output and last-error protections

- Executor runtime sanitizer redacts output markers, URLs/endpoints, remote paths, named paths, absolute paths, Windows paths, IPv4/hostnames, and host-sensitive fragments (`backend/internal/task/executor/runtime_sanitize.go:11-28`, `backend/internal/task/executor/runtime_sanitize.go:30-53`).
- Task-level sanitizer hides command lifecycle messages and redacts output/path/host-sensitive fragments before persistence (`backend/internal/task/runtime_sanitize.go:12-30`, `backend/internal/task/runtime_sanitize.go:46-70`).
- `Manager.emitLog` stores `sanitizeTaskLogMessage(message)` into queued logs before DB persistence and WebSocket publish (`backend/internal/task/log_writer.go:60-67`, `backend/internal/task/log_writer.go:87-96`, `backend/internal/task/log_writer.go:120-132`).
- `runner.go` sanitizes executor errors before writing `TaskRun.LastError` and `Task.LastError` for normal task failures (`backend/internal/task/runner.go:540-569`, `backend/internal/task/runner.go:598-607`). Verification warning/failure messages are also sanitized before status/update/alert (`backend/internal/task/runner.go:442-462`).
- `RunSSHCommandOutput` intentionally returns raw combined stdout/stderr to callers (`backend/internal/task/executor/ssh_connect.go:162-185`); safety depends on every caller sanitizing before persistence/response. The rclone/restic retention, integrity, verifier, hook, and snapshot paths inspected either discard output or sanitize/hide output before persistence/response.

#### Command text and executor config API behavior

- `Task.Command`, `Task.RsyncSource`, and `Task.RsyncTarget` are JSON fields; `Task.ExecutorConfig` is not (`backend/internal/model/models.go:304-308`).
- Normal task list/get responses return `model.Task`, so command/source/target remain visible as part of current API/UI behavior (`backend/internal/api/handlers/task_handler.go:131-169`, `backend/internal/api/handlers/task_handler.go:188-202`). The list keyword search also searches command/source/target (`backend/internal/api/handlers/task_handler.go:110-114`).
- `BatchHandler.Get` loads full `model.Task` rows and returns `tasks` after sanitizing nested `Node`, but does not mask task `Command` (`backend/internal/api/handlers/batch_handler.go:210-256`). The frontend batch mapper inspected earlier ignores the command, so this is an API response surface rather than a currently consumed UI field.
- `TaskRunHandler.Get` preloads a nested `Task` with `rsync_source` and `rsync_target` selected (`backend/internal/api/handlers/task_run_handler.go:97-100`) and returns `taskRunDetailResponse` (`backend/internal/api/handlers/task_run_handler.go:132`). In the current model, `TaskRun.Task` itself is tagged `json:"-"` (`backend/internal/model/models.go:350-354`), so this preload is currently not serialized through default JSON. The frontend response type still allows nested `task.rsyn_source/target`, but `mapTaskRun` ignores nested task fields (`web/src/lib/api/task-runs-api.ts:39-62`, `web/src/lib/api/task-runs-api.ts:182-197`).
- Task-run logs are returned directly from stored `TaskLog` rows (`backend/internal/api/handlers/task_run_handler.go:191-208`). New rows are write-time sanitized; legacy rows created before the P4 sanitization line remain a response-time concern if they contain raw evidence.

#### Config import/export boundaries

- Default config export omits `executor_config` unless `include_secrets=true` (`backend/internal/api/handlers/config_handler.go:177-208`).
- The same default export includes task `command`, `rsync_source`, and `rsync_target` (`backend/internal/api/handlers/config_handler.go:180-193`), and imports them back into tasks (`backend/internal/api/handlers/config_handler.go:533-543`, `backend/internal/api/handlers/config_handler.go:595-608`).
- Node passwords/private keys and SSH private keys are included only when `include_secrets=true` (`backend/internal/api/handlers/config_handler.go:113-150`).
- Export/import credential audit metadata records counts and `with_sensitive`; it does not include raw config bodies (`backend/internal/api/handlers/config_handler.go:228-243`).

#### Docker/runtime output surface

- Docker volume discovery maps only volume name/driver/mountpoint to the API response (`backend/internal/api/handlers/docker_handler.go:23-28`, `backend/internal/api/handlers/docker_handler.go:184-195`).
- Docker command failure branches collapse raw Docker output into generic warnings such as "Docker 未安装" / "无权访问 Docker" / "执行 docker volume ls 失败" (`backend/internal/api/handlers/docker_handler.go:151-164`).
- Docker credential audit metadata includes stage/count/has_warning only and uses safe error text (`backend/internal/api/handlers/docker_handler.go:92-115`).
- Residual: volume `Name` and `Mountpoint` are returned unredacted by design of the Docker volume browser (`backend/internal/api/handlers/docker_handler.go:85-89`, `backend/internal/api/handlers/docker_handler.go:191-195`).

#### File browser / preview / snapshot content paths

- Node file browser list/preview validates requested paths through SFTP `RealPath`, then restricts access to `Node.BasePath` and task source roots, with symlink escape protection and non-leaking validation errors (`backend/internal/api/handlers/file_handler.go:389-455`).
- Node file list responses include real `Path` and each entry `Path` (`backend/internal/api/handlers/file_handler.go:28-51`, `backend/internal/api/handlers/file_handler.go:138-142`, `backend/internal/api/handlers/file_handler.go:501-512`).
- File preview returns `Path`, raw `Content` up to 1MB, size, and truncation flag (`backend/internal/api/handlers/file_handler.go:145-160`, `backend/internal/api/handlers/file_handler.go:233-264`). This is intentionally a file-preview feature and remains a raw file-content response surface.
- File browser credential audit metadata hashes paths instead of storing raw paths (`backend/internal/api/handlers/file_handler.go:96-99`, `backend/internal/api/handlers/file_handler.go:129-136`, `backend/internal/api/handlers/file_handler.go:333-352`).
- Local task backup file listing returns full local paths inside `RsyncTarget` (`backend/internal/api/handlers/file_handler.go:281-328`, `backend/internal/api/handlers/file_handler.go:516-545`).
- Snapshot APIs return restic snapshots/files/diff/search paths by feature design (`backend/internal/api/handlers/snapshot_handler.go:61-83`, `backend/internal/api/handlers/snapshot_handler.go:99-131`, `backend/internal/api/handlers/snapshot_diff_handler.go:20-40`, `backend/internal/api/handlers/snapshot_diff_handler.go:130-240`, `backend/internal/api/handlers/snapshot_search_handler.go:22-28`, `backend/internal/api/handlers/snapshot_search_handler.go:75-87`).
- Snapshot restore audit metadata stores include count, target-set boolean, and shortened snapshot ID, not raw include paths or target path (`backend/internal/api/handlers/snapshot_handler.go:139-167`, `backend/internal/api/handlers/snapshot_handler.go:219-226`).

#### Legacy evidence at rest

- New task logs and new task/task-run last errors are sanitized before persistence (`backend/internal/task/log_writer.go:60-67`, `backend/internal/task/runner.go:540-569`).
- Task-run logs are returned directly from persisted rows (`backend/internal/api/handlers/task_run_handler.go:191-208`). Normal task/task-run responses expose persisted `last_error` fields as model JSON (`backend/internal/model/models.go:317-318`, `backend/internal/model/models.go:365-366`).
- Credential audit has both write-time filtering and response/export re-sanitization for legacy rows (`backend/internal/credentialaudit/audit.go:208-281`, `backend/internal/api/handlers/credential_audit_handler.go:116-155`, `backend/internal/api/handlers/credential_audit_handler.go:158-238`). The task log/last-error APIs do not currently have equivalent response-time re-sanitization in the handlers inspected.

#### AppCredential rendered hook behavior

- AppCredential `Config` is encrypted at rest and omitted from JSON; `SanitizedConfig()` removes only `password` (`backend/internal/model/models.go:152-196`). AppCredential responses include sanitized config and `has_password` (`backend/internal/api/handlers/app_credential_handler.go:46-78`, `backend/internal/api/handlers/app_credential_handler.go:127-174`).
- App profile access stores config in an unexported map and exposes only provider/kind/source metadata as safe metadata (`backend/internal/profile/app_profile_access.go:20-25`, `backend/internal/profile/app_profile_access.go:63-89`).
- Built-in profile hook templates embed `.password`, `.host`, `.container_name`, and fixed `/tmp/xirang-*` paths into rendered shell commands (`backend/internal/profile/profile.go:47-127`). `RenderHooks` returns the rendered pre/post hook strings (`backend/internal/profile/profile.go:145-174`).
- Policy create/update renders hooks from AppCredential config when `app_profile` is set and the user did not manually provide hooks, then stores those rendered hooks in `Policy.PreHook`/`Policy.PostHook` (`backend/internal/api/handlers/policy_handler.go:242-283`, `backend/internal/api/handlers/policy_handler.go:567-605`).
- AppCredential update cascades by re-rendering old and new hook strings; if the policy still matches old rendered values, it saves the newly rendered values (`backend/internal/api/handlers/app_credential_handler.go:283-332`).
- Policy responses include `pre_hook`, `post_hook`, drill hook strings, `app_profile`, and `app_credential_id` (`backend/internal/api/handlers/policy_handler.go:1090-1120`).

Current conclusion for AppCredential hooks: AppCredential config itself is encrypted and password is omitted from AppCredential API responses, but rendered hook persistence can materialize password/host/container/path values into `Policy.PreHook`/`Policy.PostHook` and return them through policy responses. This is a remaining raw secret/evidence surface independent of rclone.

#### Audit surfaces

- Credential audit write-time filtering denies sensitive metadata keys/values containing private/password/token/secret/credential/config/output/stream/command/content/payload and similar markers (`backend/internal/credentialaudit/audit.go:208-281`). Error messages also redact after output/stdout/stderr markers (`backend/internal/credentialaudit/audit.go:291-310`).
- Credential audit list/export re-sanitizes responses and legacy error/metadata records (`backend/internal/api/handlers/credential_audit_handler.go:116-155`, `backend/internal/api/handlers/credential_audit_handler.go:158-238`).
- HTTP audit middleware records method/path/status/client IP/user agent and does not persist request or response bodies (`backend/internal/middleware/audit.go:21-56`). This avoids body-level command/config/secret persistence in HTTP audit logs.

### Current Protections

1. `Task.ExecutorConfig` is encrypted at rest and not serialized in normal task JSON.
2. rclone executor config currently has no password/token/key-bearing fields.
3. rclone command output, verifier output, retention output, and integrity output are sanitized or hidden before task log/alert persistence.
4. Command executor streams stdout/stderr through executor sanitization and logs only lifecycle text rather than raw command text.
5. Task log writer sanitizes new task logs before DB persistence and WebSocket publish.
6. Task runner sanitizes new executor/verifier failure messages before writing task/task-run last errors and raising task/verification alerts.
7. Credential audit events filter sensitive metadata at write time and re-sanitize on list/export.
8. HTTP audit logs do not store request/response bodies.
9. File browser credential audit stores path hashes/counts rather than raw file paths, and file path validation avoids symlink escape leakage in errors.
10. AppCredential config is encrypted at rest and password is omitted from AppCredential API responses.

### Remaining Risks

| Surface | Current exposure | Behavior compatibility impact if masked | Notes |
|---|---|---:|---|
| AppCredential rendered hooks in `Policy.PreHook` / `Policy.PostHook` | Rendered shell command can persist and return app password/host/container/path values through policy APIs. | Medium | Highest raw-secret residual found. Masking responses preserves field names but editing workflows may depend on visible hook text. Avoiding persistence would require broader behavior change. |
| Legacy `TaskLog.Message` / task and task-run `LastError` | Existing rows from before sanitization may be returned verbatim by logs/task/run APIs. | Low to medium | Future writes are sanitized; response-time masking would preserve response shape and address legacy at-rest evidence without DB migration. |
| Batch status API | Returns full `Task` rows; command text can be serialized in `tasks[].command`. | Low | Frontend batch mapper ignores command, so a response-time mask is likely small and behavior-compatible for current UI. |
| Config export default | Omits `executor_config`, passwords, and private keys by default, but exports task command/source/target. | Medium to high | Changing export content could affect round-trip import/export semantics. |
| Normal task list/get | Returns command/source/target as current task management UI behavior. | High | Masking would likely break create/edit/detail behavior unless replaced with separate edit-only retrieval semantics. |
| File browser preview | Returns raw file content and real paths by feature design. | High | Raw content is the product feature; not a small compatible slice unless policy scope changes. |
| Snapshot list/file/diff/search | Returns host/path/file path evidence by feature design. | High | Path exposure is integral to snapshot browser/diff/search UX. |
| Docker volume discovery | Returns volume names and mountpoints by feature design. | Medium/high | Mountpoints are path-sensitive, but hiding them would change visible Docker browser behavior. |
| Node migration data result | Can return local old/new backup dirs in messages. | Low/medium | Narrow masking possible, but this is less tied to rclone/executor secrets than hooks/logs. |

### Minimal Behavior-Compatible Slice Assessment

- **Not selected as best slice: rclone executor config/materialization.** Inspection did not find a current raw rclone config secret. The rclone config shape is non-secret, encrypted at rest, omitted from JSON, and runtime output paths are already sanitized.
- **Smallest clear API-only slice:** response-time masking for legacy task logs and `LastError` values would preserve field names and future operation semantics while reducing legacy raw runtime evidence. This is local-only and behavior-compatible, but may be less directly connected to rclone config materialization.
- **Smallest narrow response slice with unused frontend field:** masking `BatchHandler.Get` task `command` values would preserve the batch response shape while removing an API-returned command text surface currently ignored by the frontend. This is narrow, local-only, and likely low churn.
- **Highest remaining raw-secret value:** AppCredential rendered hook persistence/response. It can store and return passwords rendered into policy hooks today. However, fully fixing storage/materialization may be broader because hooks are used as executable policy behavior and policy edit responses currently include hook text. A response-only mask would reduce return exposure but would not address raw persisted hooks.

### Recommendation

For the overall P4 residual review, the strongest remaining raw secret surface is **AppCredential rendered hook persistence/response**: AppCredential password is encrypted and omitted from credential APIs, but the rendered policy hook can materialize that same password into `Policy.PreHook`/`Policy.PostHook` and the policy API returns those fields. If the implementation phase must select exactly one highest-value slice, prefer a minimal local-only AppCredential hook hardening slice if it can preserve policy execution and edit behavior; otherwise select the narrow response-time legacy evidence masking slice for task logs/last errors.

For the specific rclone executor question, no rclone-specific secret-bearing executor config/materialization issue was found. Do not spend the one implementation slice on rclone config unless later inspection adds secret-bearing rclone fields.

### Related Specs

| Spec / Task Path | Description |
|---|---|
| `.trellis/tasks/05-24-p4-residual-security-review/prd.md` | Active task PRD requiring review of rclone, Docker/runtime output, file browser/preview, config import/export, legacy evidence at-rest, and AppCredential rendered hook behavior. |
| `.trellis/tasks/archive/2026-05/05-22-p4-credential-broker-foundation/` | Prior P4 SSH credential provider seam foundation. |
| `.trellis/tasks/archive/2026-05/05-23-p4-restic-credential-resolver/` | Prior P4 restic repository password resolver work. |
| `.trellis/tasks/archive/2026-05/05-23-p4-task-runtime-log-sanitization/` | Prior P4 task runtime evidence sanitization work. |
| `.trellis/tasks/archive/2026-05/05-24-p4-next-security-hardening/` | Prior P4 node/diagnostic evidence sanitization work. |
| `.trellis/tasks/archive/2026-05/05-24-p4-restic-repo-password-resolver/` | Prior P4 restic repo password resolver completion. |

## Caveats / Not Found

- `backend/internal/task/executor/rsync_executor.go` was not found; rsync executor code is in `backend/internal/task/executor/executor.go`.
- `backend/internal/api/handlers/file_browser_handler.go` was not found; file browser implementation is in `backend/internal/api/handlers/file_handler.go`.
- This is an internal code inspection only; no tests or verification commands were run because no code was changed.
- The task-run nested `Task` preload selects `rsync_source`/`rsync_target`, but `model.TaskRun.Task` is currently tagged `json:"-"`; the selected fields are therefore not serialized by the current default JSON path. The frontend type still allows those fields, but mapper ignores them.
- File browser preview, snapshot file/diff/search paths, Docker volume mountpoints, normal task command/source/target, and config export command/source/target are acknowledged product/API behavior surfaces. Masking them may be security-positive but is not obviously behavior-compatible without a more specific product decision.
