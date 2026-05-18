# Research: Current Code Security Surfaces

- **Query**: Research Xirang's current code paths for the security baseline hardening task. Inspect backend/internal/model/models.go, backend/internal/api/handlers/task_handler.go, ssh_key_handler.go, batch_handler.go, backend/internal/task/drill.go, backend/internal/middleware/rbac.go, ownership/audit middleware, and relevant tests. Identify concrete current behavior, security gaps, and candidate implementation points for P0 layered hardening.
- **Scope**: internal
- **Date**: 2026-05-18

## Findings

### Files Found

| File Path | Description |
|---|---|
| `backend/internal/model/models.go` | Core data models, JSON exposure rules, sensitive-field GORM hooks, `Node.Sanitized()`, `Task`, `TaskRun`, `RestoreDrillEvidence`, `AuditLog`, `NodeOwner`. |
| `backend/internal/api/router.go` | Route registration and middleware ordering for auth, audit, RBAC, ownership, task/SSH key/batch/drill routes. |
| `backend/internal/api/handlers/task_handler.go` | Task CRUD/trigger/restore/batch/log handlers, task request validation, path-prefix and path-character validation, task dependency validation. |
| `backend/internal/api/handlers/ssh_key_handler.go` | SSH key create/update/list/get/export/test-connection/batch APIs and response DTO that excludes private keys. |
| `backend/internal/api/handlers/batch_handler.go` | Batch command creation, command length and dangerous-pattern checks, ownership set validation, batch read/delete behavior. |
| `backend/internal/api/handlers/helpers.go` | Shared ownership helpers, pagination, cron parsing, path prefix validation, shell-meta path validation. |
| `backend/internal/api/handlers/policy_handler.go` | Policy drill trigger handler, policy ownership union check, drill restore path validation used at policy write boundary. |
| `backend/internal/task/drill.go` | Restore drill execution path, sandbox path validation, drill script execution, cross-node transfer, temporary key handling, evidence persistence. |
| `backend/internal/task/manager.go` | `TriggerRestore` and `validateRestorePath` for destructive restore operations. |
| `backend/internal/task/executor/command_executor.go` | Direct command executor over SSH; logs command text and remote stdout/stderr. |
| `backend/internal/task/executor/executor.go` | `ShellEscape()` implementation used by rsync/restic/rclone/drill paths. |
| `backend/internal/task/executor/sudo.go` | Sudo wrapping semantics for system commands and user command strings. |
| `backend/internal/middleware/rbac.go` | Static role-to-permission matrix and RBAC/RequireRole middleware behavior. |
| `backend/internal/middleware/ownership.go` | Node and task ownership middleware for operator object-level access checks. |
| `backend/internal/middleware/audit.go` | Audit middleware, non-GET write capture, hash-chain persistence. |
| `backend/internal/api/handlers/task_handler_test.go` | Tests for task validation, ownership denial, local executor rejection, reference validation, schedule rollback, task log filters. |
| `backend/internal/api/handlers/batch_handler_test.go` | Tests for batch command ownership denial and delete transaction rollback on cleanup failure. |
| `backend/internal/task/drill_test.go` | Tests for drill config/path safety, evidence states, error sanitization, cleanup boundary, and async trigger metadata. |
| `backend/internal/api/handlers/helpers_test.go` | Tests for path-character validation and environment bypass. |
| `backend/internal/task/executor/executor_security_test.go` | Shell escaping tests against adversarial shell payloads. |
| `backend/internal/api/router_test.go` | Selected full-router RBAC tests, e.g. alert bulk resolve viewer denial/admin success. |
| `backend/internal/api/handlers/audit_handler_test.go` | Tests for audit log list filters and CSV export. |
| `backend/internal/api/handlers/backup_confidence_handler_test.go` | Tests asserting confidence response excludes sensitive/connection fields. |
| `.trellis/spec/backend/quality-guidelines.md` | Backend quality/security conventions: sanitize secrets, route RBAC/ownership, security-sensitive tests. |
| `.trellis/spec/backend/error-handling.md` | Error-response conventions, fail-closed ownership behavior, restore-drill evidence contract. |
| `.trellis/spec/backend/database-guidelines.md` | Model/hook encryption conventions and migration notes. |

### Code Patterns

#### 1. Sensitive model fields are encrypted by hooks and often hidden from JSON

- `model.SSHKey.PrivateKey` is `json:"-"`; `BeforeSave` encrypts non-empty private keys and `AfterFind` decrypts them (`backend/internal/model/models.go:27-39`, `backend/internal/model/models.go:573-595`).
- `model.Node.Password` and `model.Node.PrivateKey` still have JSON names with `omitempty`, but `Node.Sanitized()` blanks password/private key and nested SSH key private key before response (`backend/internal/model/models.go:41-52`, `backend/internal/model/models.go:54-87`, `backend/internal/model/models.go:597-631`).
- `Task.ExecutorConfig` is encrypted/decrypted through hooks and has `json:"executor_config,omitempty"` (`backend/internal/model/models.go:291-343`). Task handlers currently return `model.Task` directly after create/update and in lists/details; list/detail sanitize nested Node, but do not create a separate DTO for task command/executor config (`backend/internal/api/handlers/task_handler.go:123-130`, `backend/internal/api/handlers/task_handler.go:184-193`, `backend/internal/api/handlers/task_handler.go:276`, `backend/internal/api/handlers/task_handler.go:400`).
- `Integration.Endpoint`, `Integration.Secret`, `Integration.ProxyURL`, `AppCredential.Config`, and user TOTP/recovery fields are also hook-encrypted (`backend/internal/model/models.go:161-181`, `backend/internal/model/models.go:208-257`, `backend/internal/model/models.go:635-669`).

Current behavior:
- GORM reads hand handlers decrypted values after `AfterFind`.
- JSON protection is field-by-field; SSH key private keys and app credential configs are hidden by tags, while node secrets rely on callers using `Sanitized()`.
- Existing specs explicitly forbid returning nodes, SSH keys, integrations, or executor configs without sanitizing (`.trellis/spec/backend/quality-guidelines.md:17-24`, `.trellis/spec/backend/database-guidelines.md:74-86`).

Security gaps / P0 hardening surfaces:
- `Task.ExecutorConfig` can contain secret-bearing executor config and is present in `model.Task` JSON if non-empty. Task endpoints return `model.Task` in several places after decrypted `AfterFind` or freshly bound create/update values.
- Direct `model.Task` responses also expose `Command`, `RsyncSource`, and `RsyncTarget`, which may be expected operational data but are high-sensitivity command/path surfaces.
- Node secret safety depends on every caller remembering `Node.Sanitized()`; direct serialization remains a recurring risk because model tags expose `password` and `private_key` when non-empty.

Candidate implementation points:
- `backend/internal/model/models.go`: model-level JSON tags/sanitizer helpers for high-sensitivity task/node fields.
- `backend/internal/api/handlers/task_handler.go`: response DTO/sanitization immediately before every task response (`List`, `Get`, `Create`, `Update`, `BatchTrigger` result details if expanded later, `Logs` if log messages are sanitized).
- Existing test anchor: `backend/internal/api/handlers/backup_confidence_handler_test.go:240-244` already asserts a sensitive response does not contain `password`, `private_key`, `executor_config`, or host details.

#### 2. Route-level RBAC is centralized, but role permissions are broad for operators

- `rolePermissions` grants admin all major permissions, operator read/write/trigger for tasks, read for SSH keys, and write for alerts/logs/dashboards/service monitors; viewer is read-only for many resources (`backend/internal/middleware/rbac.go:9-79`).
- RBAC denies unknown/missing roles by returning 403 (`backend/internal/middleware/rbac.go:99-110`).
- `RequireRole("admin")` does exact-role checks for admin-only routes (`backend/internal/middleware/rbac.go:87-97`).

Current route registrations:
- SSH key read routes allow admin/operator/viewer via `RBAC("ssh_keys:read")`; SSH key writes require admin via `RBAC("ssh_keys:write")` because only admin has that permission (`backend/internal/api/router.go:221-229`, `backend/internal/middleware/rbac.go:20-21`, `backend/internal/middleware/rbac.go:51`, `backend/internal/middleware/rbac.go:69`).
- Task create/update/delete/cancel/pause/resume/skip-next and batch command creation/deletion use `tasks:write`; both admin and operator have this permission (`backend/internal/api/router.go:269-290`, `backend/internal/middleware/rbac.go:17-19`, `backend/internal/middleware/rbac.go:48-50`).
- Task restore and restic snapshot restore are admin-only via `RequireRole("admin")` (`backend/internal/api/router.go:282-304`).
- Policy drill trigger uses `RBAC("tasks:trigger")`, which admin and operator both have (`backend/internal/api/router.go:267`, `backend/internal/middleware/rbac.go:19`, `backend/internal/middleware/rbac.go:50`).

Security gaps / P0 hardening surfaces:
- Operator can create/update direct `command` tasks and batch commands on owned nodes because `tasks:write` covers both backup task configuration and arbitrary command execution (`backend/internal/api/handlers/task_handler.go:866-881`, `backend/internal/api/handlers/batch_handler.go:45-71`).
- `tasks:trigger` is distinct from `tasks:write`, but the route matrix still lets operators trigger existing tasks and policy drills.
- Existing tests include selected RBAC route checks, but there is no comprehensive full-router matrix around task command creation, batch commands, SSH key export, and drill trigger in the inspected files.

Candidate implementation points:
- `backend/internal/middleware/rbac.go`: permission matrix changes or new permission keys for high-risk actions such as command execution, batch command execution, drill trigger, SSH key export/test-connection.
- `backend/internal/api/router.go`: route middleware split for high-risk endpoints (`/tasks` command creates/updates may need handler-level checks because route path is shared; `/batch-commands`, `/policies/:id/drill-trigger`, `/ssh-keys/export`, `/ssh-keys/:id/test-connection`).
- Full-router RBAC tests can follow existing patterns in `backend/internal/api/router_test.go:279-298` and package-specific RBAC tests.

#### 3. Ownership filtering exists for nodes/tasks, with mixed fail-closed behavior in helper functions

- `OwnershipNodeCheck` lets admin/viewer pass, requires operator ownership for path `:id`, and denies unknown roles (`backend/internal/middleware/ownership.go:20-60`).
- `OwnershipTaskCheck` lets admin/viewer pass, requires operator ownership through `tasks -> node_owners`, and denies unknown roles (`backend/internal/middleware/ownership.go:71-111`).
- `ownershipNodeFilter` in handlers returns an error for unknown/missing role, and its comment says the previous empty-role-as-admin shortcut was removed after security review (`backend/internal/api/handlers/helpers.go:18-47`).
- `authorizeNodeOwnership` and `authorizeNodeOwnershipSet` still treat empty role as allowed for compatibility with handler-only unit tests (`backend/internal/api/handlers/helpers.go:49-78`).
- `authorizePolicyOwnership` treats empty role/admin/viewer as allowed and allows operator if they own any node attached to the policy (union rule) (`backend/internal/api/handlers/helpers.go:96-116`).

Current behavior:
- Task list uses `ownershipNodeFilter`; operator lists only owned node tasks, unknown/missing role causes internal error (`backend/internal/api/handlers/task_handler.go:75-83`).
- Task create/update call `authorizeNodeOwnership` after reference validation, so operator can write only owned target node tasks, while direct handler tests without auth context are allowed (`backend/internal/api/handlers/task_handler.go:240-246`, `backend/internal/api/handlers/task_handler.go:361-367`).
- Batch create/get/delete call `authorizeNodeOwnershipSet`; operator must own every target node or every node in the batch (`backend/internal/api/handlers/batch_handler.go:84-100`, `backend/internal/api/handlers/batch_handler.go:186-196`, `backend/internal/api/handlers/batch_handler.go:260-270`).
- Policy drill trigger uses policy union ownership: owning any node in the policy is enough to trigger drill (`backend/internal/api/handlers/policy_handler.go:920-931`).

Security gaps / P0 hardening surfaces:
- Helper-level empty-role bypass remains in `authorizeNodeOwnership`, `authorizeNodeOwnershipSet`, and `authorizePolicyOwnership`; production routes set auth context first, but direct handler calls or future miswired routes can bypass object checks.
- Policy ownership is union-based, so an operator owning one node in a multi-node policy can trigger a drill for the whole policy. The drill's sandbox node is loaded by policy target ID in task manager, not ownership-checked in the API handler (`backend/internal/task/drill.go:87-109`).
- Viewer bypasses object ownership in `OwnershipNodeCheck` and `OwnershipTaskCheck` by design (`backend/internal/middleware/ownership.go:25-28`, `backend/internal/middleware/ownership.go:74-77`), meaning viewers can read all nodes/tasks if they pass RBAC read permissions.

Candidate implementation points:
- `backend/internal/api/handlers/helpers.go`: remove or isolate empty-role compatibility shortcuts for production-only helpers; tests can set roles explicitly as `ownershipNodeFilter` already requires.
- `backend/internal/api/handlers/policy_handler.go`: drill trigger policy ownership boundary if P0 requires all policy nodes and/or sandbox node ownership for operators.
- `backend/internal/middleware/ownership.go`: viewer global-read behavior if baseline requires object-level read scoping for viewers.

Relevant tests:
- Task create rejects unowned operator node (`backend/internal/api/handlers/task_handler_test.go:455-504`).
- Task update with route `OwnershipTaskCheck` rejects moving task to unowned node (`backend/internal/api/handlers/task_handler_test.go:733-795`).
- Batch create/get/delete reject unowned operator targets (`backend/internal/api/handlers/batch_handler_test.go:16-65`, `backend/internal/api/handlers/batch_handler_test.go:67-111`, `backend/internal/api/handlers/batch_handler_test.go:113-166`).

#### 4. Audit middleware records write requests after auth, but only coarse request metadata

- `AuditLogger` skips GET/HEAD/OPTIONS and records non-read method, full path, status code, user ID, username, role, client IP, user agent (`backend/internal/middleware/audit.go:21-56`).
- Audit logs are saved with a serialized hash chain under `auditWriteMu`; `PrevHash` is previous last entry hash and `EntryHash` hashes record fields plus `PrevHash` (`backend/internal/middleware/audit.go:59-92`).
- Router attaches `AuditLogger` after `AuthMiddleware` and before API rate/body middleware on the secured group (`backend/internal/api/router.go:124-128`).
- WebSocket routes are outside secured middleware; project instructions say WS handlers write audit logs manually, but this research did not inspect WS handler code.

Current behavior:
- POST/PUT/PATCH/DELETE requests are audited even if RBAC/ownership returns 403 because audit runs before route handlers and writes after `c.Next()`.
- Audit record has no request body, target resource ID, task command digest, reason, or before/after fields.
- Audit table model exposes hash fields in JSON (`backend/internal/model/models.go:401-414`).

Security gaps / P0 hardening surfaces:
- High-risk actions such as command execution, SSH key export/test-connection, restore/drill trigger, and batch commands currently produce only coarse path/status audit records unless handlers add domain-specific logs elsewhere.
- Audit records do not distinguish action class or include sanitized action metadata; forensic value for command execution is limited to route/path/status.
- Hash chain can detect entry tampering only if verification exists elsewhere; no verification path was found in inspected files.

Candidate implementation points:
- `backend/internal/middleware/audit.go`: add safe action metadata support or request-scoped audit details without raw secrets.
- High-risk handlers (`task_handler.go`, `batch_handler.go`, `ssh_key_handler.go`, `policy_handler.go`) can set sanitized audit context before response.
- `backend/internal/api/handlers/audit_handler.go` tests can extend from existing list/export tests (`backend/internal/api/handlers/audit_handler_test.go:19-109`).

#### 5. Task creation supports direct command executor with minimal validation compared to batch commands

- `taskRequest.Command` is trimmed but not inspected for dangerous patterns in `TaskHandler.Create` or `Update` (`backend/internal/api/handlers/task_handler.go:47-58`, `backend/internal/api/handlers/task_handler.go:716-723`, `backend/internal/api/handlers/task_handler.go:859-921`).
- `validateTaskRequest` allows executor types `rsync`, `command`, `restic`, `rclone`; for `command`, it only requires non-empty command text (`backend/internal/api/handlers/task_handler.go:866-881`).
- `BatchHandler.Create` trims command, enforces non-empty and max length 4096, and calls `isDangerousCommand` (`backend/internal/api/handlers/batch_handler.go:57-71`).
- `dangerousPatterns` block selected destructive forms: `rm` recursive root/system paths, `rm --recursive`, `mkfs`, `dd of=/dev/`, shutdown/reboot/halt/poweroff, `> /dev/[sh]d`, `wipefs`; env `BATCH_COMMAND_BLACKLIST` adds substring checks (`backend/internal/api/handlers/batch_handler.go:317-353`).
- Comment explicitly says dangerous command checks are a safety net, not a security boundary (`backend/internal/api/handlers/batch_handler.go:317-319`).
- `CommandExecutor.Run` executes `task.Command` directly via SSH `session.Start(command)`; if node sudo is enabled and user is non-root, it wraps the entire user command with `sudo sh -c <ShellEscape(command)>` (`backend/internal/task/executor/command_executor.go:16-64`, `backend/internal/task/executor/sudo.go:24-28`).
- `CommandExecutor` logs `执行命令: %s` with the full command and logs stdout/stderr lines verbatim (`backend/internal/task/executor/command_executor.go:62-99`).

Security gaps / P0 hardening surfaces:
- Direct task command creation/update has no max command length and no dangerous-pattern check; batch commands have both. This is a concrete behavioral difference between two command-execution entry points.
- Direct command text and remote output can be persisted to task logs by manager `emitLog` via executor `logf`; command strings or output may contain secrets.
- Operators can create direct command tasks and batch commands on owned nodes through `tasks:write`.

Candidate implementation points:
- `backend/internal/api/handlers/task_handler.go`: command validation branch inside `validateTaskRequest` can share/extend `batch_handler.go` command length and dangerous-pattern checks.
- `backend/internal/api/handlers/batch_handler.go`: existing `isDangerousCommand` is package-local in handlers and can be reused by task handler because it is same package.
- `backend/internal/task/executor/command_executor.go`: command/output logging boundary for sanitization or metadata-only logging.
- Tests: extend `task_handler_test.go` around command validation; add explicit tests for parity with `BatchHandler.Create` dangerous command blocking.

#### 6. Rsync/restic/rclone path validation exists at API and executor layers

- `validateTaskRequest` rejects unsupported executor types, invalid cron, empty sync paths, shell injection characters for non-remote rsync source/target, invalid JSON executor config, and disallowed path prefixes from `RSYNC_ALLOWED_SOURCE_PREFIXES` / `RSYNC_ALLOWED_TARGET_PREFIXES` (`backend/internal/api/handlers/task_handler.go:859-921`).
- `validatePathChars` rejects NUL, CR/LF, backticks, and `$(` unless `BACKUP_PATH_ALLOW_SHELL_META=true` (`backend/internal/api/handlers/helpers.go:238-271`).
- `validatePathByPrefix` enforces optional clean path prefix allowlists (`backend/internal/api/handlers/helpers.go:199-236`).
- `ShellEscape` always single-quotes shell args and escapes embedded single quotes (`backend/internal/task/executor/executor.go:361-365`).
- Security tests use a real shell to verify adversarial inputs round-trip as literals (`backend/internal/task/executor/executor_security_test.go:9-74`).

Current behavior:
- Remote path specs detected by `util.IsRemotePathSpec` skip local character and prefix validation (`backend/internal/api/handlers/task_handler.go:889-917`).
- Existing helper test documents `BACKUP_PATH_ALLOW_SHELL_META=true` bypass (`backend/internal/api/handlers/helpers_test.go:53-58`).

Security gaps / P0 hardening surfaces:
- Environment bypass for shell-meta validation is global and permits inputs that would otherwise be rejected at API boundary.
- Remote path specs are not covered by the same path character/prefix validation path.

Candidate implementation points:
- `backend/internal/api/handlers/helpers.go`: validation helper behavior if P0 removes or narrows bypasses.
- `backend/internal/api/handlers/task_handler.go`: remote path validation branch if baseline requires remote specs to reject control chars and command-substitution forms.

#### 7. Restore and drill paths have multiple validators with slightly different boundaries

- `Manager.TriggerRestore` defaults empty target path to task source, validates with `validateRestorePath`, then creates restore `TaskRun` and launches restore goroutine (`backend/internal/task/manager.go:286-315`).
- `validateRestorePath` requires absolute path, rejects `..`, rejects many shell metacharacters, and forbids exact `/`, `/etc`, `/usr`, `/bin`, `/sbin`, `/boot`; it does not reject subpaths of those dirs (`backend/internal/task/manager.go:318-340`).
- Snapshot restore handler has a separate `validateRestoreTargetPath` that requires absolute, rejects `/`, and rejects exact or subpaths under `/bin`, `/sbin`, `/usr`, `/lib`, `/lib64`, `/boot`, `/dev`, `/proc`, `/sys`, `/run`, `/etc`, `/var/run` (`backend/internal/api/handlers/snapshot_handler.go:13-35`).
- Policy handler has `validateDrillRestorePath` requiring absolute, rejecting `..`, shell metacharacters, and exact/subpaths under `/`, `/etc`, `/usr`, `/bin`, `/sbin`, `/boot`, `/dev`, `/proc`, `/sys`, `/run`, `/var/run` (`backend/internal/api/handlers/policy_handler.go:950-975`).
- Drill manager `validateDrillSandboxPath` defaults blank to `/tmp/xirang-drill`, delegates to `validateRestorePath`, then forbids exact/subpaths of `/`, `/etc`, `/usr`, `/bin`, `/sbin`, `/boot`, `/dev`, `/proc`, `/sys`, `/run`, `/var/run` (`backend/internal/task/drill.go:35-68`).

Current behavior:
- Task restore route is admin-only (`backend/internal/api/router.go:282`).
- Drill trigger route is available to admin/operator via `tasks:trigger` and policy ownership (`backend/internal/api/router.go:267`, `backend/internal/api/handlers/policy_handler.go:925-947`).
- Drill execution validates sandbox path before precheck/restore and before cleanup; cleanup uses `rm -rf` only after boundary validation and `ShellEscape` (`backend/internal/task/drill.go:306-310`, `backend/internal/task/drill.go:425-455`).

Security gaps / P0 hardening surfaces:
- Restore path safety rules differ across `manager.go`, `snapshot_handler.go`, `policy_handler.go`, and `drill.go`.
- `validateRestorePath` used by task restore forbids only exact system dirs, while snapshot/drill validators also forbid subpaths. For example, `/etc/app` is rejected by drill/snapshot validators but not by `validateRestorePath` based on inspected logic.
- Drill route access for operators plus union policy ownership makes drill path validation especially important for multi-node policies/sandbox nodes.

Candidate implementation points:
- Consolidate or align restore path boundary in `backend/internal/task/manager.go`, `backend/internal/task/drill.go`, `backend/internal/api/handlers/snapshot_handler.go`, and `backend/internal/api/handlers/policy_handler.go`.
- Tests: `backend/internal/task/drill_test.go:115-147` currently covers exact forbidden drill paths; add subpath matrix wherever baseline requires parity.

#### 8. Drill execution writes source private key to sandbox temporarily for cross-node transfer

- `transferFilesToSandbox` validates destination path, creates destination dir, then gets source node private key from `node.SSHKey.PrivateKey` or `node.PrivateKey` (`backend/internal/task/drill.go:611-630`, `backend/internal/task/drill.go:70-78`).
- It writes the source key into `/tmp/xirang-drill-key-<unixnano>.pem` on the sandbox node with `printf '%s\n' <ShellEscape(srcKey)> > <tmpKeyPath> && chmod 600 <tmpKeyPath>` (`backend/internal/task/drill.go:632-645`).
- A defer best-effort removes the temp key from sandbox (`backend/internal/task/drill.go:635-638`).
- The sandbox then executes rsync pull with `StrictHostKeyChecking=no` and `UserKnownHostsFile=/dev/null` to source host (`backend/internal/task/drill.go:647-663`).

Security gaps / P0 hardening surfaces:
- During drill transfer, a decrypted source private key is materialized on the sandbox filesystem, even though cleaned up best-effort.
- The rsync SSH command disables host key checking for source host from sandbox.
- Error return can include rsync output (`backend/internal/task/drill.go:661-664`); downstream failure handling sanitizes via `util.SanitizeMessage` in many paths, but the command execution boundary itself returns raw output.

Candidate implementation points:
- `backend/internal/task/drill.go`: temp-key transfer mechanism, host-key checking options, and error-output sanitization boundary.
- Tests: existing drill tests cover cleanup command escaping and script-content log suppression (`backend/internal/task/drill_test.go:594-630`, `backend/internal/task/drill_test.go:707-763`), but not temporary key materialization or host-key options.

#### 9. SSH key API avoids private key response leakage, but read/export/test surfaces remain sensitive

- `sshKeyResponseItem` includes ID, name, username, key type, fingerprint, derived public key, timestamps, and excludes private key (`backend/internal/api/handlers/ssh_key_handler.go:43-74`).
- `List`, `Get`, `Create`, `Update`, `Export` JSON/CSV use `toSSHKeyResponse` or derived public key only (`backend/internal/api/handlers/ssh_key_handler.go:106-142`, `backend/internal/api/handlers/ssh_key_handler.go:174-185`, `backend/internal/api/handlers/ssh_key_handler.go:259-263`, `backend/internal/api/handlers/ssh_key_handler.go:531-575`).
- Fingerprint is currently `SHA256` of the prepared private key string, not of the derived public key (`backend/internal/api/handlers/ssh_key_handler.go:76-80`, `backend/internal/api/handlers/ssh_key_handler.go:174-180`).
- `TestConnection` loads an SSH key, accepts arbitrary node IDs, fetches matching nodes, forces `testNode.AuthType = "key"`, assigns the SSH key, builds auth, and dials each node with a 10-second timeout (`backend/internal/api/handlers/ssh_key_handler.go:315-414`).
- SSH key export supports authorized_keys/json/csv, optional `scope=in_use`, optional comma-separated IDs (`backend/internal/api/handlers/ssh_key_handler.go:486-580`).

Current route access:
- `GET /ssh-keys`, `GET /ssh-keys/:id`, and `GET /ssh-keys/export` require `ssh_keys:read`, available to admin/operator/viewer (`backend/internal/api/router.go:221-226`, `backend/internal/middleware/rbac.go:20`, `backend/internal/middleware/rbac.go:51`, `backend/internal/middleware/rbac.go:69`).
- SSH key create/update/delete/batch/test-connection require `ssh_keys:write`, admin only by current permission matrix (`backend/internal/api/router.go:222-229`, `backend/internal/middleware/rbac.go:21`, `backend/internal/middleware/rbac.go:44-64`, `backend/internal/middleware/rbac.go:65-78`).

Security gaps / P0 hardening surfaces:
- Operators/viewers can export all public keys and metadata through `ssh_keys:read`; no ownership scope applies to SSH keys.
- `TestConnection` has no node ownership check inside handler; route is admin-only now via `ssh_keys:write`, but if permissions change this handler would need object-level node filtering.
- `TestConnection` returns host, port, latency, and raw dial/build-auth error text per node (`backend/internal/api/handlers/ssh_key_handler.go:351-410`).
- Fingerprint derived from private-key content changes with private-key formatting and is not the usual public-key fingerprint model.

Candidate implementation points:
- `backend/internal/api/router.go` and `backend/internal/middleware/rbac.go`: SSH key export/list/read role access boundaries.
- `backend/internal/api/handlers/ssh_key_handler.go`: ownership filtering for node-targeted key test, error sanitization, and fingerprint derivation.
- Add focused SSH key handler tests; none were found in inspected `backend/internal/api/handlers/*test*.go` for SSH key response/export/test-connection behavior.

#### 10. Batch command path has better pre-execution checks than direct task command path

- `BatchHandler.Create` rejects empty command, commands longer than 4096 chars, and dangerous patterns before any task rows are created (`backend/internal/api/handlers/batch_handler.go:57-71`).
- It validates every requested node exists and is allowed before creating tasks (`backend/internal/api/handlers/batch_handler.go:79-100`).
- It creates one `Task` per node with `ExecutorType: "command"`, `Source: "batch"`, `Status: "pending"`, common `BatchID`, then triggers each via `manager.TriggerManual` (`backend/internal/api/handlers/batch_handler.go:102-146`).
- If trigger fails for a task, the task remains created and response `run_ids` entry is 0 (`backend/internal/api/handlers/batch_handler.go:120-134`).
- `Retain` is read and returned but does not affect deletion/retention in inspected code (`backend/internal/api/handlers/batch_handler.go:46-51`, `backend/internal/api/handlers/batch_handler.go:136-146`).

Security gaps / P0 hardening surfaces:
- Trigger failures leave command tasks persisted; response does not include per-node error details.
- Dangerous command filtering is regex/substring based and documented as a safety net, not a boundary.
- The same command is persisted into every created task; downstream task logs can also include command text/output.

Candidate implementation points:
- `backend/internal/api/handlers/batch_handler.go`: per-node failure reporting/audit metadata, retention semantics, and shared command validation.
- `backend/internal/task/executor/command_executor.go`: downstream command log sanitization.

#### 11. Existing tests cover several denial/sanitization paths but leave P0 baseline gaps

Covered by inspected tests:
- Task request validation rejects invalid cron, missing rsync path, disallowed path prefix, unsupported `local` executor, empty command content, unknown node/policy references (`backend/internal/api/handlers/task_handler_test.go:307-422`, `backend/internal/api/handlers/task_handler_test.go:424-453`, `backend/internal/api/handlers/task_handler_test.go:679-731`).
- Operator task/batch ownership denial is covered (`backend/internal/api/handlers/task_handler_test.go:455-504`, `backend/internal/api/handlers/task_handler_test.go:733-795`, `backend/internal/api/handlers/batch_handler_test.go:16-166`).
- Drill rejects unsafe configs and records sanitized failure evidence (`backend/internal/task/drill_test.go:44-177`, `backend/internal/task/drill_test.go:633-705`, `backend/internal/task/drill_test.go:738-763`).
- Drill logs should not include verify script content tokens (`backend/internal/task/drill_test.go:707-736`).
- ShellEscape is tested with adversarial inputs (`backend/internal/task/executor/executor_security_test.go:9-74`).
- Path char validation rejects NUL/newline/backticks/`$(` and documents env bypass (`backend/internal/api/handlers/helpers_test.go:25-58`).

Not found in inspected tests:
- Direct task command dangerous-pattern rejection or command length limits.
- Full-router RBAC matrix for task direct command creation/update versus normal backup task writes.
- SSH key handler tests for no private key leakage, export role access, test-connection ownership/error sanitization.
- Audit hash-chain verification or domain-specific audit metadata for high-risk actions.
- Consistency tests across task restore, snapshot restore, policy drill restore, and drill runtime path validators.
- Test coverage that `Task.ExecutorConfig` is never exposed by task list/detail/create/update responses.

### External References

No external references were used; this was an internal codebase research task.

### Related Specs

- `.trellis/spec/backend/quality-guidelines.md` — Security-sensitive backend conventions: sanitize secret-bearing responses, protect routes with auth/RBAC/ownership, and test denial cases (`lines 17-31`, `lines 35-55`, `lines 59-74`).
- `.trellis/spec/backend/error-handling.md` — Unified response helpers, fail-closed ownership behavior, and restore-drill evidence contract including path rejection and sanitized evidence (`lines 37-73`, `lines 329-405`).
- `.trellis/spec/backend/database-guidelines.md` — Sensitive-field encryption via model hooks, model centralization, and avoiding raw model responses containing secrets (`lines 14-18`, `lines 74-86`).
- `.trellis/spec/guides/cross-layer-thinking-guide.md` — Boundary-focused validation and data-flow mapping guidance for changes crossing API/service/database/frontend layers (`lines 20-49`, `lines 73-85`).

## Caveats / Not Found

- This research did not inspect all handlers that may indirectly expose `Task`, `Node`, `SSHKey`, `AuditLog`, or `RestoreDrillEvidence`; it focused on the requested files and adjacent tests.
- This research did not run tests or execute the application; findings are from static inspection only.
- WebSocket audit behavior was not inspected, though router comments state WS routes authenticate in-protocol and write audit logs manually.
- No dedicated SSH key handler test file was found under the inspected test paths.
- Current task path was resolved as `.trellis/tasks/05-18-security-baseline-hardening`; output was written under that task's `research/` directory as requested.
