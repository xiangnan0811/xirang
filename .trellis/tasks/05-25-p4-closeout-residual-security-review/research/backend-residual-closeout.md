# Backend residual closeout research

- **Query**: Research backend residual security surfaces for the active Trellis task `.trellis/tasks/05-25-p4-closeout-residual-security-review`; identify reviewed surfaces and rank any high-confidence, local-only, behavior-compatible residual leak slices.
- **Scope**: internal/backend
- **Date**: 2026-05-25

## Scope Reviewed

### Task and specs

| File Path | Notes |
|---|---|
| `.trellis/tasks/05-25-p4-closeout-residual-security-review/prd.md` | Current closeout task; rule excludes raw private keys, passwords, bearer tokens, step-up proofs, OTP/recovery values, executor config, terminal streams, command output/text, file contents, Docker output, diagnostic output, exported/imported secret material, raw SQL, endpoint/proxy values, hostnames, include-path contents, target-path contents, and host-sensitive strings from responses/logs/audit/docs/UI storage. |
| `.trellis/spec/backend/logging-guidelines.md` | Backend logging contract: structured access logs, no secret/decrypted values, no command/Docker/diagnostic/export/SFTP evidence in process logs. |
| `.trellis/spec/backend/error-handling.md` | `respondInternalError` should return generic client errors; no raw SQL/encryption/SSH key/token/command output/file content/Docker/diagnostic/export/stack details to clients. |
| `.trellis/spec/backend/quality-guidelines.md` | Shared sanitizer expectations for user-visible evidence and credential-use audit contracts. |
| `.trellis/spec/backend/database-guidelines.md` | Durable credential audit storage must keep only safe IDs, sanitized errors, and sanitized metadata. |

### Archived P4 research used as context

| File Path | Notes |
|---|---|
| `.trellis/tasks/archive/2026-05/05-23-p4-task-runtime-log-sanitization/research/task-runtime-log-sanitization.md` | Prior task runtime log and evidence sanitization research. |
| `.trellis/tasks/archive/2026-05/05-24-p4-snapshot-indexer-output-sanitization/research/snapshot-indexer-restic-output.md` | Prior restic snapshot indexer output hiding research. |
| `.trellis/tasks/archive/2026-05/05-24-p4-export-import-residual-hardening/research/export-import-residual.md` | Prior export/import review; selected anomaly snapshot parse-failure log hardening instead of payload changes. |
| `.trellis/tasks/archive/2026-05/05-24-p4-file-process-residual-hardening/research/file-process-residual.md` | Prior file-browser/file-process residual research. |
| `.trellis/tasks/archive/2026-05/05-24-p4-next-residual-security-hardening/research/task-response-residual.md` | Prior task response residual: `LastError` read sanitizer and nested policy hook clearing. |
| `.trellis/tasks/archive/2026-05/05-24-p4-next-residual-security-hardening/research/appcredential-hook-residual.md` | Prior AppCredential hook response-boundary research. |
| `.trellis/tasks/archive/2026-05/05-24-p4-docker-nginx-residual-hardening/research/docker-nginx-residual.md` | Prior Docker/Nginx residual research. |
| `.trellis/tasks/archive/2026-05/05-24-p4-residual-security-review/research/rclone-executor-residual.md` | Prior rclone/executor residual research. |
| `.trellis/tasks/archive/2026-05/05-24-p4-restic-repo-password-resolver/research/diagnostic-evidence-surfaces.md` | Prior diagnostic evidence research. |

### Current backend code surfaces

| File Path | Reviewed Area |
|---|---|
| `backend/internal/runtimeevidence/sanitize.go` | Shared task runtime evidence sanitizer. |
| `backend/internal/task/runtime_sanitize.go` | Task package write/read wrappers for runtime evidence. |
| `backend/internal/task/log_writer.go` | Task log write and WebSocket publish boundary. |
| `backend/internal/api/handlers/task_handler.go` | Task list/get/trigger/restore/log response boundaries and task credential audit writes. |
| `backend/internal/api/handlers/task_run_handler.go` | Task run/log/drill evidence response sanitizers. |
| `backend/internal/ws/hub.go` | WebSocket log backfill sanitizer. |
| `backend/internal/snapshot/indexer.go` | Snapshot indexer restic output failure path. |
| `backend/internal/anomaly/snapshot_diff.go` | Snapshot diff command output handling, parse-failure logging, finding details. |
| `backend/internal/anomaly/ewma.go` | EWMA anomaly finding messages/details. |
| `backend/internal/anomaly/disk_forecast.go` | Disk forecast anomaly finding messages/details. |
| `backend/internal/anomaly/raise.go` | AnomalyEvent persistence boundary. |
| `backend/internal/anomaly/engine.go` | Detector process-log error/panic paths. |
| `backend/internal/api/handlers/anomaly_handler.go` | Anomaly event read boundary. |
| `backend/internal/alerting/dispatcher.go` | Alert raise, delivery, payload, and storage-space alert paths. |
| `backend/internal/alerting/retry.go` | Alert delivery retry and `last_error` sanitization. |
| `backend/internal/api/handlers/alert_handler.go` | Alert and alert-delivery API response boundaries. |
| `backend/internal/api/handlers/alert_delivery_handler.go` | Manual alert-delivery retry API. |
| `backend/internal/probe/prober.go` | Background node probe failure alert path. |
| `backend/internal/api/handlers/node_handler.go` | Manual SSH test failure alert/log/audit path. |
| `backend/internal/api/handlers/node_doctor_handler.go` | Node Doctor diagnostic evidence classification/sanitization. |
| `backend/internal/api/handlers/node_migrate_preflight_handler.go` | Migration preflight host/path/diagnostic response sanitization. |
| `backend/internal/task/storage_monitor.go` | Local storage-space monitoring alert input. |
| `backend/internal/task/retention.go` | Retention process logs/task logs/alerts. |
| `backend/internal/task/integrity_checker.go` | Integrity check process logs/task logs/alerts. |
| `backend/internal/task/drill.go` | Restore drill evidence/log/audit/alert paths. |
| `backend/internal/task/executor/runtime_sanitize.go` | Executor-side output/path/host sanitizer. |
| `backend/internal/task/executor/ssh_connect.go` | Runtime credential audit safe errors for executor SSH use. |
| `backend/internal/credentialaudit/audit.go` | Credential audit write-time sanitization. |
| `backend/internal/api/handlers/credential_audit_handler.go` | Credential audit read-time legacy sanitization. |
| `backend/internal/util/sanitize.go` and `backend/internal/util/telegram.go` | Shared message/error/delivery sanitizers. |
| `backend/internal/model/models.go` | Alert, AlertDelivery, Task, TaskRun, TaskLog, AnomalyEvent, CredentialAuditEvent data shapes. |

## Findings

### Existing P4 hardening verified in current code

1. **Task runtime evidence is sanitized at both write and read boundaries.**
   - `backend/internal/task/log_writer.go` sanitizes before queueing/publishing: `message: sanitizeTaskLogMessage(message)` in `emitLog`.
   - `backend/internal/task/runtime_sanitize.go:9-19` routes task log/read/last-error fields through `sanitizeTaskRuntimeEvidence`.
   - `backend/internal/runtimeevidence/sanitize.go:24-48` redacts output markers, URLs, command lifecycle text, remote paths, absolute paths, Windows paths, IPv4 addresses, hostnames, and host-sensitive tokens.
   - `backend/internal/api/handlers/task_handler.go:69-86` sanitizes task `LastError` for list/detail and clears nested `Policy.PreHook`/`PostHook` before response.
   - `backend/internal/api/handlers/task_run_handler.go:24-56` sanitizes `TaskRun.LastError`, `TaskLog.Message`, and restore drill error fields for API reads.
   - `backend/internal/ws/hub.go` backfill builds `LogEvent.Message` with `runtimeevidence.SanitizeTaskRuntimeEvidence(item.Message)`.

2. **Executor and task process-output paths hide command output instead of persisting it.**
   - `backend/internal/task/executor/runtime_sanitize.go:23-52` hides non-empty output as `[输出已隐藏]` and redacts path/host/endpoint fragments in streamed executor evidence.
   - `backend/internal/task/integrity_checker.go:77-82` and `:113-118` build task/alert errors through `sanitizeTaskLastError(...)` and process logs with `sanitizeTaskRuntimeError(err)` plus `sanitizeTaskRuntimeOutput(output)`.
   - `backend/internal/task/retention.go:151-157` and `:184-190` follow the same pattern for restic/rclone retention failures.
   - `backend/internal/task/drill.go:717-749` returns sanitized restore/script failures with hidden output.

3. **Snapshot/anomaly paths reviewed so far avoid raw restic output/path details.**
   - `backend/internal/snapshot/indexer.go` uses `newResticFindFailureError` to replace restic find output with `[输出已隐藏]` when present.
   - `backend/internal/anomaly/snapshot_diff.go:267-274` logs snapshot JSON parse failures with `output_present` only, not raw `snapOutput`.
   - `backend/internal/anomaly/snapshot_diff.go:428-451` and `:457-475` persist only counts, sizes, baseline values, snapshot IDs, and suffix-hit counts in `Finding.Details`; changed file paths are not included.
   - `backend/internal/anomaly/ewma.go:159-180` stores metric values/sample counts/window/settings values only; message includes node name and metric, not host/path/command output.
   - `backend/internal/anomaly/disk_forecast.go:128-156` stores node name plus forecast metrics and numeric details only.

4. **Diagnostic evidence surfaces appear narrowed.**
   - `backend/internal/api/handlers/node_doctor_handler.go` uses `sanitizeDoctorEvidence(...)` when appending checks and classifies sudo output instead of echoing raw output.
   - `backend/internal/api/handlers/node_migrate_preflight_handler.go` sanitizes policy source paths, node hosts, and path check messages before response.
   - Credential audit writes from diagnostic/file/docker/snapshot/terminal paths route through `credentialaudit.Write`; `backend/internal/credentialaudit/audit.go:144-176` sanitizes scalar fields, `error_message`, and metadata before persistence, and `:291-300` redacts output after output markers.
   - `backend/internal/api/handlers/credential_audit_handler.go:465-479` sanitizes legacy `error_message` at read time, including output markers, stack-like content, endpoints, bearer tokens, private keys, tokens, API keys, secrets, and passwords.

5. **HTTP access/audit logs avoid query/body payloads.**
   - `backend/internal/middleware/structured_logger.go` logs `c.Request.URL.Path`, method/status/latency/client IP, not raw query/body.
   - `backend/internal/middleware/audit.go` stores method, route path, status, client IP, and user agent; it does not persist request body, query string, or response body.
   - `backend/internal/api/handlers/response.go` returns generic `服务器内部错误` to clients through `respondInternalError`; the server-side log still records `Err(err)`, so upstream boundaries remain important.

6. **Storage-space alert raw target-path concern is currently mitigated at the caller.**
   - `backend/internal/alerting/dispatcher.go:280-292` still concatenates the `targetPath` argument into `ErrorCode` and `Message`.
   - The only current caller, `backend/internal/task/storage_monitor.go:90-94`, passes `sanitizeTaskLogMessage(path)` to both `RaiseStorageSpaceAlert(...)` and `ResolveAlertsByErrorCode(...)`.
   - Because `sanitizeTaskLogMessage` routes through `runtimeevidence.SanitizeTaskRuntimeEvidence`, absolute paths are replaced with `[路径已隐藏]` (`backend/internal/runtimeevidence/sanitize.go:43`), so the current storage monitor path does not persist the raw local path.

### Residual candidates found

#### Candidate 1: Alert/AlertDelivery read boundary for legacy stored evidence

**Evidence:**

- `model.Alert.Message` and `model.AlertDelivery.LastError` are user-visible fields:
  - `backend/internal/model/models.go:264-283` defines `Alert.Message` and `Alert.ErrorCode` as JSON fields.
  - `backend/internal/model/models.go:285-293` defines `AlertDelivery.LastError` as JSON field.
- Current alert APIs return stored alert rows directly:
  - `backend/internal/api/handlers/alert_handler.go:137-143` returns paginated `[]model.Alert` from `List` without response sanitization.
  - `backend/internal/api/handlers/alert_handler.go:170-183` returns `model.Alert` from `Get` without response sanitization.
  - `backend/internal/api/handlers/alert_handler.go:250-276` and `:291-314` return saved `alert` from `Ack`/`Resolve` without response sanitization.
- Current delivery APIs return stored delivery rows directly:
  - `backend/internal/api/handlers/alert_handler.go:497-503` returns `[]model.AlertDelivery` from `Deliveries` without response sanitization.
  - `backend/internal/api/handlers/alert_handler.go:552-567` and `:643-682` return new delivery records; new failures are sanitized, but the response shape still exposes `LastError` as-is.
- Current writes are sanitized for delivery errors, but code comments document that older behavior was weaker:
  - `backend/internal/alerting/dispatcher.go:444-449` says earlier `util.SanitizeDeliveryError` only sanitized Telegram and allowed webhook/Feishu/DingTalk `bearer token / access_token` values into `alert_deliveries.last_error`.
  - `backend/internal/alerting/retry.go:224-234` says stored `LastError` is readable by users with `alerts:deliveries` permission and now delegates to `util.SanitizeError`.
  - `backend/internal/util/sanitize.go:28-45` and `backend/internal/util/telegram.go:66-75` show the current shared sanitizer redacts URLs, query/path tokens, bot tokens, and token/secret/password patterns.

**Assessment:** High-confidence legacy read residual. Current write-time delivery errors are sanitized, but upgraded deployments can retain historical `alert_deliveries.last_error` values from before the unified sanitizer. Current API read paths expose those legacy values directly. This is local-only and behavior-compatible to address at the response boundary because it can preserve JSON shape while sanitizing string contents.

#### Candidate 2: Node probe / manual SSH test failure messages can persist raw host/path evidence in alerts and process logs

**Evidence:**

- `RaiseNodeProbeFailure` stores the caller-provided message directly in `Alert.Message`:
  - `backend/internal/alerting/dispatcher.go:160-174` assigns `Message: message` without sanitization.
- Background probe passes raw SSH/probe error text into that alert:
  - `backend/internal/probe/prober.go:147-170` calls `sshutil.ProbeNode(...)` and, after failure threshold, raises `fmt.Sprintf("节点连续探测失败 %d 次: %v", newFailures, err)`.
- Manual node test also passes raw error text into alerts and process logs:
  - `backend/internal/api/handlers/node_handler.go:674-686` raises `fmt.Sprintf("连接失败：%v", err)` and logs `SSH 连接测试失败(node_id=%d): %v` for auth-build errors.
  - `backend/internal/api/handlers/node_handler.go:707-720` does the same for host-key callback errors.
  - `backend/internal/api/handlers/node_handler.go:741-759` does the same for SSH dial errors.
- The possible raw error sources include hostnames/IPs and local known_hosts paths:
  - `backend/internal/sshutil/probe.go:29-49` wraps auth, host-key, and SSH dial failures; dial failures wrap `ssh.Dial("tcp", address, ...)` errors where `address` is `node.Host:node.Port`.
  - `backend/internal/sshutil/ssh_auth.go:94-103` includes `path=%s` for `SSH_KNOWN_HOSTS_PATH` expansion/preparation failures.
  - `backend/internal/sshutil/ssh_auth.go:145-153` wraps network dial and SSH handshake failures.
- Alert messages are sent externally unchanged:
  - `backend/internal/alerting/dispatcher.go:568-579` copies `alert.Message` into notification payload `Message`.
- Credential audit for the same manual node-test paths is less exposed because `credentialaudit.Write` sanitizes `ErrorMessage` at write time (`backend/internal/credentialaudit/audit.go:171`, `:291-300`) and the API read boundary sanitizes legacy audit rows (`backend/internal/api/handlers/credential_audit_handler.go:465-479`).

**Assessment:** High-confidence current write/log residual for host/path evidence. The payload is not raw credentials, but it can include host/IP and local known_hosts path fragments, which are explicitly within this task's residual rule. A bounded slice could sanitize node probe/test alert messages and process-log error strings while preserving alert creation, status transitions, response schema, and notification behavior shape.

#### Candidate 3: Anomaly engine panic stack logging

**Evidence:**

- `backend/internal/anomaly/engine.go:76-83` logs `Interface("panic", r)` and `Str("stack", string(debug.Stack()))` for detector goroutine recovery.
- `backend/internal/anomaly/engine.go:98-105` logs the panic object for per-tick recovery without stack.

**Assessment:** Lower-confidence residual. Current built-in detectors reviewed above do not intentionally panic with raw remote output, credentials, task output, or paths, and the stack is process-internal rather than API/audit/UI storage. This is not the strongest closeout slice unless a concrete detector panic path carrying sensitive evidence is found.

### Reviewed areas where no current backend slice was found

1. **Export/import:** Prior research found no behavior-compatible export/import payload hardening slice; current config export remains intentionally capable of `include_secrets=true` for admins and audits sensitive export/import without payload contents.
2. **AppCredential hooks/policy response:** Prior task response hardening clears nested policy hooks on task responses; broad policy hook/API redesign remains behavior-changing and out of this task.
3. **Restic/rclone executor output:** Current executor/task paths sanitize output placeholders and runtime evidence at log/last-error/read boundaries.
4. **Snapshot diff:** Current findings persist counts/metrics/snapshot IDs, not changed paths or restic output.
5. **Storage monitor:** The alerting function accepts a target-path string, but current caller supplies a path-sanitized placeholder.
6. **Broad `err.Error()` handler search:** Many handlers return validation/service errors directly, but the reviewed backend residual-prone cases either use validation-only messages, sanitized helpers, or generic internal errors. The confirmed remaining backend candidates are concentrated around alert read boundaries and node probe/test alert/log evidence rather than all `err.Error()` callsites.

## Candidate Slices

| Rank | Candidate Slice | Confidence | Compatibility | Files Involved | Why Ranked Here |
|---|---|---:|---:|---|---|
| 1 | Alert read-boundary sanitizer for `Alert.Message`/`Alert.ErrorCode` and `AlertDelivery.LastError` responses, covering legacy rows and retry response payloads. | High | High | `backend/internal/api/handlers/alert_handler.go`, possibly tests under `backend/internal/api/handlers/` | Direct API exposure of legacy delivery errors is documented by current comments; response-only sanitizer can preserve schema and DB behavior. |
| 2 | Node probe/manual SSH test alert and process-log sanitization for raw SSH/dial/known_hosts errors. | High | Medium-High | `backend/internal/probe/prober.go`, `backend/internal/api/handlers/node_handler.go`, optionally `backend/internal/alerting/dispatcher.go` | Current writes/logs can include host/IP/path evidence; sanitizing may reduce operator detail but keeps behavior and state transitions compatible. |
| 3 | Anomaly engine panic stack redaction. | Low-Medium | High | `backend/internal/anomaly/engine.go` | Internal process log only; no concrete raw evidence path confirmed in current detectors. |

## Exclusions

Excluded as architecture-level or behavior-changing per the PRD:

- External Vault/KMS/secret broker integration.
- SSH CA, host trust rollout, certificate lifecycle, or revocation.
- Terminal/session recording, playback, retention, or object storage.
- Command-level approval, command parsing, allow/deny policy, or inspection.
- WebAuthn/passkeys/device trust or configurable step-up/grant policy UI.
- Broad executor command-construction redesign without a concrete local residual leak.
- Broad policy/AppCredential hook redesign or hiding first-class policy fields beyond already-selected response-boundary hardening.
- Config export/import payload semantics changes for admin `include_secrets=true`.

## Recommendation

Backend closeout should **not** record "no backend residual slice remains" yet. At least one high-confidence, local-only, behavior-compatible backend residual exists:

1. Highest-ranked: add alert read-boundary sanitization for `Alert` and `AlertDelivery` responses, primarily to prevent legacy `alert_deliveries.last_error` rows from surfacing historical webhook/bot/access-token material and to align alerts with the task-runtime read-boundary pattern.
2. Next-ranked if the closeout implementation prefers current write/log leakage over legacy response leakage: sanitize node probe/manual SSH test alert messages and process logs before persistence/logging.

If the main task must select exactly one minimal executable slice, the alert read-boundary sanitizer is the most bounded and behavior-compatible candidate from this backend audit.

## Caveats / Not Found

- This audit focused on backend residual surfaces. Frontend browser-state and credential-executor surfaces are expected to be covered by separate research agents for the same Trellis task.
- Broad `rg` searches for `err.Error()` and logging patterns produced large result sets; targeted inspection followed for backend surfaces most likely to carry raw evidence. No claim is made that every validation-only error path was manually expanded line by line.
- No external references were needed; all findings are from internal code and Trellis/archive context.
