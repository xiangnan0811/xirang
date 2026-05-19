# Research: Credential-use audit event design

- **Query**: Research credential-use audit event design for Xirang P1: what fields to store for SSH key usage, node credential usage, key tests, exports, command tasks, batch commands, terminal sessions, and drills without storing raw secrets. Compare current AuditLog hash-chain model and whether to extend it or add a dedicated credential/audit event table. Include migration/test implications.
- **Scope**: internal repo/spec research
- **Date**: 2026-05-19

## Findings

### Files Found

| File Path | Description |
|---|---|
| `backend/internal/model/models.go:26-38` | `SSHKey` model: managed key identity, `PrivateKey json:"-"`, `Fingerprint`, `LastUsedAt`. |
| `backend/internal/model/models.go:40-51` | `Node.Sanitized()` strips `Password`, `PrivateKey`, and nested `SSHKey.PrivateKey` before API responses. |
| `backend/internal/model/models.go:53-86` | `Node` model stores `AuthType`, password, inline private key, and optional managed `SSHKeyID`. |
| `backend/internal/model/models.go:291-319` | `Task` model stores executor type, command, rsync paths, batch ID, source, and encrypted executor config. |
| `backend/internal/model/models.go:345-363` | `TaskRun` model stores trigger type, chain run ID, status, timing, progress, and error. |
| `backend/internal/model/models.go:365-399` | `RestoreDrillEvidence` model stores drill identity, phase statuses/errors, sandbox path, and confidence flag. |
| `backend/internal/model/models.go:401-414` | Current `AuditLog` model: HTTP/WS envelope plus `PrevHash`/`EntryHash`, no resource or metadata fields. |
| `backend/internal/model/models.go:573-631` | GORM hooks encrypt/decrypt `SSHKey.PrivateKey`, `Node.Password`, and `Node.PrivateKey`. |
| `backend/internal/middleware/audit.go:20-55` | `AuditLogger` skips GET/HEAD/OPTIONS, logs mutating secured HTTP requests after handler execution. |
| `backend/internal/middleware/audit.go:58-91` | `SaveAuditLogWithHashChain` serializes writes, links to previous `entry_hash`, and hashes fixed audit fields. |
| `backend/internal/api/handlers/audit_handler.go:44-64` | Audit log list endpoint filters and paginates existing `AuditLog` rows. |
| `backend/internal/api/handlers/audit_handler.go:83-139` | Audit CSV export uses the same filters but exports only basic columns, not hash fields. |
| `backend/internal/database/migrations/sqlite/000001_baseline.up.sql:147-169` | SQLite baseline creates `audit_logs` with current columns and indexes. |
| `backend/internal/database/migrations/postgres/000001_baseline.up.sql:147-169` | PostgreSQL baseline creates `audit_logs` with current columns and indexes. |
| `backend/internal/api/router.go:124-129` | Secured `/api/v1` routes use auth middleware then `AuditLogger`; WebSocket routes are outside this group. |
| `backend/internal/api/router.go:221-229` | SSH key routes: CRUD, batch import/delete, export, and key connection test. |
| `backend/internal/api/handlers/ssh_key_handler.go:44-75` | SSH key API response derives public key but does not serialize private key. |
| `backend/internal/api/handlers/ssh_key_handler.go:356-455` | `POST /ssh-keys/:id/test-connection` uses one managed key against requested nodes and returns per-node outcome. |
| `backend/internal/api/handlers/ssh_key_handler.go:540-626` | `GET /ssh-keys/export` exports public-key-derived authorized_keys/json/csv data; GET is skipped by current audit middleware. |
| `backend/internal/api/router.go:147-166` | Node routes include test connection, doctor, file browser, Docker volumes, emergency backup, and ownership checks. |
| `backend/internal/api/handlers/node_handler.go:661-794` | Node connection test uses stored node credential, updates probe state, and updates `SSHKey.LastUsedAt` only for managed-key nodes. |
| `backend/internal/api/handlers/node_doctor_handler.go:63-90` | Node Doctor is a POST endpoint that runs allowlisted diagnostics via SSH. |
| `backend/internal/api/handlers/node_doctor_handler.go:124-170` | Doctor builds SSH auth, dials SSH, then checks sudo/tools/backup directories/disk. |
| `backend/internal/api/handlers/file_handler.go:75-120` | `GET /nodes/:id/files` uses SFTP with node credentials; current audit middleware skips it because it is GET. |
| `backend/internal/api/handlers/file_handler.go:138-208` | `GET /nodes/:id/files/content` uses SFTP to read file contents; current audit middleware skips it because it is GET. |
| `backend/internal/api/handlers/file_handler.go:277-303` | Shared SFTP dial helper builds SSH auth from node credentials. |
| `backend/internal/api/handlers/docker_handler.go:49-80` | `GET /nodes/:id/docker-volumes` uses SSH to run Docker commands; current audit middleware skips it because it is GET. |
| `backend/internal/api/handlers/docker_handler.go:82-98` | Docker helper builds SSH auth and dials via node credentials. |
| `backend/internal/api/handlers/config_handler.go:56-72` | Config export can include secrets and currently logs to structured logger, not `AuditLog`. |
| `backend/internal/api/handlers/config_handler.go:99-130` | Config export includes node password/private key and SSH key private key when `include_secrets=true`. |
| `backend/internal/api/handlers/config_handler.go:158-188` | Config export includes task executor config when `include_secrets=true`. |
| `backend/internal/api/router.go:269-290` | Task trigger, restore, batch trigger, and batch command routes. |
| `backend/internal/api/handlers/task_handler.go:208-276` | Task creation stores command/executor details and syncs schedules. |
| `backend/internal/api/handlers/task_handler.go:453-464` | Manual task trigger creates a run through task manager. |
| `backend/internal/api/handlers/task_handler.go:577-599` | Restore trigger starts destructive restore flow and returns run ID. |
| `backend/internal/api/handlers/task_handler.go:613-670` | Batch task trigger loops over task IDs and returns per-task trigger results. |
| `backend/internal/api/handlers/batch_handler.go:46-148` | Batch command creation stores one `command` task per node and triggers them. |
| `backend/internal/api/handlers/batch_handler.go:230-306` | Batch command deletion removes task/run/log/sample/alert rows for a batch. |
| `backend/internal/task/runner.go:75-177` | `triggerCore` creates `TaskRun` with trigger type and chain context before async execution. |
| `backend/internal/task/runner.go:205-347` | `runTask` loads task/node/SSHKey/policy, handles maintenance/locks, then invokes executor. |
| `backend/internal/task/executor/command_executor.go:15-120` | Command executor dials SSH with node credentials and runs `Task.Command`, logging command and output to task logs. |
| `backend/internal/task/executor/ssh_connect.go:15-39` | Shared executor SSH dial helper resolves auth and dials node. |
| `backend/internal/task/executor/ssh_connect.go:41-73` | Shared executor credential resolver supports managed key, inline key, and password. |
| `backend/internal/sshutil/ssh_auth.go:25-53` | Shared key content resolver identifies credential source as managed key, inline key, or none. |
| `backend/internal/sshutil/ssh_auth.go:55-121` | Shared SSH auth builder supports password and key auth; errors include source labels but not key contents. |
| `backend/internal/api/handlers/terminal_handler.go:104-130` | Terminal handler writes manual `AuditLog` entries for WS/failure paths outside HTTP middleware. |
| `backend/internal/api/handlers/terminal_handler.go:184-264` | Terminal records auth/node/SSH failure audit actions, but not node ID or credential source. |
| `backend/internal/api/handlers/terminal_handler.go:335-383` | Terminal records open/close audit entries with `Method="WS"` and action encoded in path. |
| `backend/internal/task/drill.go:69-126` | `TriggerDrill` creates a `TaskRun` with `TriggerType="drill"` and starts async execution. |
| `backend/internal/task/drill.go:160-259` | Drill execution creates `RestoreDrillEvidence` and sanitizes failure messages before storage. |
| `backend/internal/task/drill.go:301-488` | Drill phases use sandbox credentials for precheck/verify/post-verify/cleanup and update evidence status. |
| `backend/internal/task/drill.go:605-619` | Old cross-node drill transfer path is blocked before writing source credentials to sandbox. |
| `.trellis/spec/backend/quality-guidelines.md:39-44` | Spec requires sensitive data encryption, response stripping, and sanitization for user-visible evidence/command output. |
| `.trellis/spec/backend/quality-guidelines.md:170-174` | Spec requires route permissions to be granted deliberately and sensitive credential surfaces to fail closed. |
| `.trellis/spec/backend/logging-guidelines.md:68-76` | Spec forbids logging passwords, private keys, tokens, decrypted values, and unsafe command output. |
| `.trellis/spec/backend/database-guidelines.md:44-50` | Spec requires paired SQLite/PostgreSQL migrations and notes current latest migration is `000058_restore_drill_evidence`. |
| `.trellis/spec/backend/database-guidelines.md:76-86` | Spec warns not to expose secrets and not to manually encrypt/decrypt outside model/service boundaries. |
| `.trellis/spec/backend/error-handling.md:338-352` | Spec defines drill evidence table/fields and requires sanitized evidence errors without SSH secrets. |

### Current AuditLog hash-chain model

`AuditLog` is a request-envelope table, not a domain event table. It stores actor fields, HTTP method/path/status, client IP, user agent, hash-chain links, and timestamp (`backend/internal/model/models.go:401-414`). The audit middleware runs only on the authenticated `secured` group (`backend/internal/api/router.go:124-129`) and explicitly skips `GET`, `HEAD`, and `OPTIONS` (`backend/internal/middleware/audit.go:27-32`).

Hash-chain write behavior:

- `SaveAuditLogWithHashChain` uses a process-level mutex plus DB transaction, reads the last `entry_hash` by `id desc`, sets `PrevHash`, computes `EntryHash`, then creates the row (`backend/internal/middleware/audit.go:60-72`).
- The hash payload is fixed: `user_id`, `username`, `role`, `method`, `path`, `status_code`, `client_ip`, `user_agent`, `created_at`, and `prev_hash` (`backend/internal/middleware/audit.go:75-91`).
- Because resource IDs, metadata, task run IDs, node IDs, SSH key IDs, and credential source are absent, adding those as columns would not be tamper-evident unless `hashAuditLogEntry` is changed to include them.

Current API surface:

- `GET /audit-logs` filters by username, role, method, path keyword, status code, user ID, and date range (`backend/internal/api/handlers/audit_handler.go:141-176`).
- `GET /audit-logs/export` exports CSV columns `id, created_at, username, role, method, path, status_code, client_ip, user_agent` (`backend/internal/api/handlers/audit_handler.go:118-134`); it does not include hash columns or any future metadata unless changed.
- Only admin has `audit:read` (`backend/internal/middleware/rbac.go:8-42`).

Current coverage gaps relevant to credential-use events:

- Mutating secured HTTP routes produce an `AuditLog`, but only the route pattern/path and status. Example: `POST /nodes/:id/test-connection` is audited as a POST route, without `node_id`, auth type, SSH key ID, latency, or outcome detail.
- Sensitive GET routes are skipped by middleware. This includes `GET /ssh-keys/export`, node file browser/list/read, Docker volume list, snapshot list/files/diff/search, and config export.
- WebSocket terminal routes are outside `secured`; terminal writes manual `AuditLog` rows with `Method="WS"` and action encoded in path, but currently no node ID, session ID, credential source, or duration (`backend/internal/api/handlers/terminal_handler.go:104-130`, `backend/internal/api/handlers/terminal_handler.go:335-383`).
- Background/system usage (node probe loop, metric collection, retention, integrity checks, cron task/drill triggers) has no user request context and does not write `AuditLog` rows by default.

### Credential storage and safe identifiers already available

- Managed SSH key identity: `SSHKey.ID`, `Name`, `Username`, `KeyType`, `Fingerprint`, `LastUsedAt`; raw `PrivateKey` is excluded from JSON and encrypted/decrypted through hooks (`backend/internal/model/models.go:26-38`, `backend/internal/model/models.go:573-595`).
- Node credential identity: `Node.AuthType`, `SSHKeyID`, and presence of inline `PrivateKey` or `Password`; raw node secrets are stripped from API responses by `Node.Sanitized()` and encrypted/decrypted through hooks (`backend/internal/model/models.go:40-86`, `backend/internal/model/models.go:597-631`).
- Task secret config: `Task.ExecutorConfig` is encrypted/decrypted through hooks and excluded from JSON (`backend/internal/model/models.go:291-343`). It can carry restic repository passwords or other executor secrets.
- Shared auth-source labels exist in `sshutil.ResolveKeyContent`: `ssh_key_id=<id>`, `ssh_key_ref`, `node.private_key`, or empty (`backend/internal/sshutil/ssh_auth.go:25-53`). Executor-side resolver has similar source labels (`backend/internal/task/executor/executor.go:514-533`). These labels are safe to store if they do not include raw key content.

### Credential-use event fields to store without raw secrets

A credential-use event needs enough identity and correlation to answer "who or what used which credential for which operation, against which node/task, with what outcome" without copying passwords, private keys, decrypted executor config, terminal input, command output, or raw exported secret material.

Common event fields:

| Field | Shape | Purpose / notes |
|---|---|---|
| `id` | integer | Primary key. |
| `event_type` | string enum | Examples: `ssh_key.test_connection`, `ssh_key.export`, `node.credential.test_connection`, `node.credential.sftp_read`, `task.command.execute`, `batch_command.create`, `terminal.open`, `terminal.close`, `drill.phase_execute`, `config.export`. |
| `occurred_at` | timestamp UTC | Event time. Use UTC consistently with current audit writes. |
| `actor_type` | `user` / `system` | `user` for HTTP/WS actor; `system` for cron/prober/retention/integrity/drill scheduler. |
| `user_id`, `username`, `role` | nullable actor fields | Same shape as `AuditLog`; null/zero for system actors. |
| `client_ip`, `user_agent` | nullable strings | Request context for HTTP/WS events. Terminal currently lacks `UserAgent`; adding it would align with HTTP audit. |
| `http_method`, `route_path`, `status_code` | nullable request fields | Preserve route context without requiring all credential events to be HTTP-shaped. |
| `outcome` | `success` / `failure` / `denied` / `skipped` / `partial` | Normalized result. |
| `failure_stage` | nullable string | Examples: `auth_build`, `host_key`, `dial`, `session`, `pty`, `command_start`, `restore`, `verify`, `cleanup`. |
| `error_code` | nullable string | Stable code where available; avoid raw DB/encryption/SSH secret detail. |
| `error_message` | nullable sanitized text | Only sanitized with existing shared sanitizer where command output or remote errors may contain secrets. |
| `duration_ms` / `latency_ms` | nullable integer | Timing for SSH dial/session/test/terminal/command/drill phase. |
| `node_id`, `node_name` | nullable | Node identity. Prefer IDs for joins; names are already user-visible but should still be bounded/sanitized. |
| `credential_kind` | enum | `managed_ssh_key`, `node_inline_private_key`, `node_password`, `executor_config_secret`, `app_credential`, `none`. |
| `credential_source` | string | Safe source label such as `ssh_key_id=7`, `node.private_key`, `node.password`, `task.executor_config`; no raw secret values. |
| `ssh_key_id` | nullable integer | Present for managed SSH keys. |
| `ssh_key_fingerprint`, `ssh_key_type` | nullable strings | Safe managed-key identity. Existing `Fingerprint` is derived from private key content; it is already returned by API responses. |
| `remote_username` | nullable string | SSH login user; it is already part of node/key responses. If treated as sensitive in a future policy, store a hash instead. |
| `task_id`, `task_run_id`, `trigger_type`, `chain_run_id` | nullable | Correlate task execution and chain/cron/manual/drill/restore events. |
| `policy_id` | nullable | Backup/drill policy context. |
| `batch_id` | nullable string | Batch command correlation. |
| `executor_type` | nullable string | `rsync`, `restic`, `rclone`, `command`. |
| `operation` | string | Narrow purpose: `ssh_handshake`, `sftp_list`, `sftp_read`, `command_exec`, `terminal_pty`, `drill_precheck`, `public_key_export`, `secret_config_export`. |
| `command_sha256` | nullable string | Hash of a command/script if needed for correlation; do not store raw command when it may contain secrets. |
| `command_preview` | nullable sanitized/truncated string | Optional human aid after `util.SanitizeMessage`; omit entirely for high-risk paths if sanitization cannot be guaranteed. |
| `path_hash` / `path_preview` | nullable | For file/restore paths, use bounded sanitized path or hash depending on sensitivity. Do not store file contents. |
| `resource_counts` | JSON/text | Counts only: node_count, key_count, task_count, success_count, failure_count, exported_count, bytes_read/truncated. |
| `metadata_json` | sanitized JSON/text | Event-specific fields; no raw secrets, no raw terminal stream, no raw command output, no decrypted executor config. |
| `prev_hash`, `entry_hash` | optional string | If the table itself is tamper-evident, hash a canonical serialized payload including metadata and previous hash. |

Per-flow fields:

| Flow | Event type(s) | Specific fields to store | Raw data to exclude |
|---|---|---|---|
| SSH key connection tests | `ssh_key.test_connection` | `ssh_key_id`, key fingerprint/type, requested `node_ids`, `node_count`, per-node summary `{node_id, outcome, latency_ms, failure_stage/error_code}`, actor/request fields. | `SSHKey.PrivateKey`, derived signer, full SSH error if it may leak key text. |
| SSH key public export | `ssh_key.export` | `format`, `scope`, requested IDs, exported key IDs/count, `contains_private_key=false`, actor/request fields. Current `/ssh-keys/export` exports authorized_keys/json/csv public material only (`backend/internal/api/handlers/ssh_key_handler.go:577-625`). | Private keys. Full public key text can also be omitted; store key IDs/fingerprints/counts. |
| Config export with secrets | `config.export` | `include_secrets`, `contains_secret_material`, counts of nodes/keys/tasks/settings, actor/request fields. This is distinct from SSH-key public export because it can include node passwords/private keys, SSH key private keys, and executor config (`backend/internal/api/handlers/config_handler.go:99-130`, `backend/internal/api/handlers/config_handler.go:158-188`). | Export payload, node password/private key, SSH key private key, executor config, system setting secret values. |
| Node connection test | `node.credential.test_connection` | `node_id`, `auth_type`, credential kind/source, `ssh_key_id`/fingerprint when present, latency, disk probe success flags, `last_used_at_updated`. Current code updates managed key `LastUsedAt` on success (`backend/internal/api/handlers/node_handler.go:779-784`). | Password/private key, disk command output. |
| Node Doctor | `node.credential.doctor` and optional per-check metadata | `node_id`, credential kind/source, check names and statuses (`auth`, `ssh`, `known_hosts`, `sudo`, `tools`, `backup_dir`, `disk`), sanitized evidence. Doctor already sanitizes evidence before returning (`backend/internal/api/handlers/node_doctor_handler.go:181-187`). | Password/private key, raw remote outputs that may contain secrets. |
| File browser SFTP list/read | `node.credential.sftp_list`, `node.credential.sftp_read` | `node_id`, credential kind/source, requested/validated path hash or bounded path, read/list operation, bytes returned, `truncated`, outcome. | File contents, passwords/private keys. |
| Docker volumes via SSH | `node.credential.docker_volumes` | `node_id`, credential kind/source, outcome, volume count or warning code. | Raw command output if it includes unexpected secret-like content. |
| Node migrate preflight | `node.credential.migrate_preflight` | target/source node IDs, credential kind/source for target node, check statuses, tool/path counts. | Raw private key/password and unbounded command output. |
| Command task execution | `task.command.execute` plus `task.credential.use` | `task_id`, `task_run_id`, `node_id`, `executor_type="command"`, trigger type, credential kind/source, `command_sha256`, optional sanitized/truncated command preview, `use_sudo`, outcome, exit code, duration. Current command executor logs the command and output to task logs (`backend/internal/task/executor/command_executor.go:56-64`, `backend/internal/task/executor/command_executor.go:67-119`), so audit metadata should be stricter than task logs. | Raw command when it may contain secrets, stdout/stderr, terminal-like streams. |
| Backup/restore task execution | `task.credential.use`, `task.backup.execute`, `task.restore.execute` | `task_id`, `task_run_id`, `node_id`, `policy_id`, executor type, trigger type, credential kind/source, source/target path hash or bounded path, restic/rclone secret presence flags, outcome, duration, throughput. | Restic repository password, executor config, command output, repository credentials. |
| Batch command creation/triggering | `batch_command.create`, `batch_command.trigger` | `batch_id`, node IDs/count, created task IDs, run IDs, `command_sha256`, optional sanitized/truncated command preview, retain flag, success/failure counts. Current handler creates `Task{ExecutorType:"command", Source:"batch", BatchID:...}` per node (`backend/internal/api/handlers/batch_handler.go:103-119`). | Raw command output and raw secrets embedded in the command. |
| Terminal session | `terminal.open`, `terminal.close`, `terminal.failure` | `session_id`, `node_id`, credential kind/source, `ssh_key_id`/fingerprint, action/failure stage, status, opened/closed timestamps, duration, actor/IP/user-agent. Current manual audit records only WS action path and status (`backend/internal/api/handlers/terminal_handler.go:335-383`). | Terminal input, terminal output, PTY stream, passwords/private keys. |
| Restore drill | `drill.trigger`, `drill.phase_execute`, `drill.complete` | `policy_id`, `task_id`, `task_run_id`, `source_task_run_id`, `snapshot_ref`, `source_node_id`, `sandbox_node_id`, `sandbox_path` hash/bounded value, phase (`sandbox_precheck`, `restore`, `pre_verify`, `verify`, `post_verify`, `cleanup`), credential kind/source per node, script hash/optional sanitized preview, outcome, evidence ID, confidence flag. Existing evidence already stores identity and sanitized phase errors (`backend/internal/task/drill.go:176-193`, `backend/internal/task/drill.go:218-258`). | Raw drill scripts if secret-bearing, SSH credentials, raw output, copied key files. |

### Code Patterns

#### Current automatic audit is route-level only

The middleware skips read-only methods and stores a fixed HTTP envelope after handler execution:

```go
skip := c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead || c.Request.Method == http.MethodOptions
```

`backend/internal/middleware/audit.go:27-32`

That means the following credential-using GET endpoints are not automatically in `AuditLog`: `/ssh-keys/export`, `/nodes/:id/files`, `/nodes/:id/files/content`, `/nodes/:id/docker-volumes`, restic snapshot read endpoints, and `/config/export`.

#### Hash chain currently does not cover resource metadata

The hash payload is a fixed string of actor/request fields:

```go
payload := fmt.Sprintf(
    "%d|%s|%s|%s|%s|%d|%s|%s|%s|%s",
    record.UserID,
    record.Username,
    record.Role,
    record.Method,
    record.Path,
    record.StatusCode,
    record.ClientIP,
    record.UserAgent,
    record.CreatedAt.UTC().Format(time.RFC3339Nano),
    record.PrevHash,
)
```

`backend/internal/middleware/audit.go:75-88`

If credential event details are added to `AuditLog` as new columns or JSON metadata, they must be included in the canonical hash payload to be hash-chain protected.

#### Terminal is manually audited but without credential-specific context

Terminal writes manual rows because WS routes are outside HTTP auth middleware. Current failure and open/close entries encode only action in `Path` and status in `StatusCode`:

- failure helper: `backend/internal/api/handlers/terminal_handler.go:104-130`
- open entry: `backend/internal/api/handlers/terminal_handler.go:335-347`
- close entry: `backend/internal/api/handlers/terminal_handler.go:371-382`

This creates an existing insertion point for terminal credential-use events, but node/session/credential fields need a schema or metadata target.

#### Existing credential source resolution can feed audit identity

Managed key and inline-key source labels already exist in `sshutil.ResolveKeyContent`:

- Managed key with ID: `ssh_key_id=<id>` (`backend/internal/sshutil/ssh_auth.go:37-45`)
- Inline key: `node.private_key` (`backend/internal/sshutil/ssh_auth.go:49-51`)

Password auth can be represented as `node.password` from `Node.AuthType == "password"` without storing the password.

#### Existing evidence/error handling already requires sanitization

Drill execution sanitizes error messages before storing evidence:

- `util.SanitizeMessage(lastError)` before updating `TaskRun` (`backend/internal/task/drill.go:198-205`)
- `util.SanitizeMessage(errorMsg)` before evidence error fields (`backend/internal/task/drill.go:218-255`)

This pattern matches spec requirements for user-visible evidence and command output (`.trellis/spec/backend/quality-guidelines.md:39-44`) and should be reused for any credential event metadata/error fields.

### Comparison: extend `audit_logs` vs add a dedicated credential event table

#### Option A: Extend current `audit_logs`

Possible shape:

- Add columns such as `event_type`, `resource_type`, `resource_id`, `node_id`, `ssh_key_id`, `task_id`, `task_run_id`, `policy_id`, `batch_id`, `credential_kind`, `credential_source`, `outcome`, `metadata_json`.
- Update `hashAuditLogEntry` to include the new fields and canonicalized metadata.
- Add manual writes for sensitive GET routes and background/system flows or change middleware to allow route-specific GET audit.

Fit with current model:

- Fits existing admin audit list/export endpoints and one chronological hash chain.
- Keeps request-level and credential-level rows in one table; terminal already writes manual `AuditLog` rows.
- Requires changing hash payload carefully. Existing rows have hashes computed without the new fields; new verification logic would need versioning/default-empty handling if introduced.

Implications:

- Migration is smaller than a new table but touches a baseline domain table used by many tests.
- `AuditHandler` list/export needs new filters/columns if credential events must be searchable/exportable.
- Current `path` and `method` filters are not enough for credential-specific queries; new indexes would be needed for `event_type`, `node_id`, `ssh_key_id`, `task_id`, `task_run_id`, `batch_id`, and `occurred_at/created_at`.
- Sensitive GET routes remain missed unless manual `AuditLog` writes are added. Broadly auditing all GETs would change noise/volume for many read endpoints.
- Background events have no HTTP method/path/status; either nullable HTTP fields or synthetic `Method="SYSTEM"`/paths would be needed.

#### Option B: Add a dedicated `credential_audit_events` / `credential_use_events` table

Possible shape:

- A tailored table with common fields listed above plus event-specific sanitized `metadata_json`.
- Optional `prev_hash`/`entry_hash` for a separate tamper-evident chain, or an `audit_log_id` link to the request-envelope row where one exists.
- Optional list/export endpoints under admin-only RBAC, or include in existing audit UI through a union/API layer.

Fit with current model:

- Separates HTTP request audit (`audit_logs`) from credential-use domain events.
- Handles user and system actors without forcing all rows into HTTP method/path semantics.
- Allows high-cardinality/search indexes for node/key/task/batch/drill fields without overloading `audit_logs`.

Implications:

- Requires new model, paired SQLite/PostgreSQL migrations, writer/helper package, route(s), and UI/API mapping if exposed.
- If a separate hash chain is used, chronological integrity is per-table unless linked to `audit_logs`. If `audit_log_id` is used, current `AuditLog` rows still do not include credential event hash in their own hash payload.
- Tests need to cover both creation of request `AuditLog` rows and credential event rows for HTTP/WS paths.
- Background/system flows can write directly with `actor_type="system"` and no request fields.

#### Hybrid shape observed from current architecture

A hybrid can preserve `AuditLog` as the request envelope and write a dedicated credential event row with `audit_log_id` or request correlation fields where available. For WebSocket and background work, credential events can exist without a request `AuditLog`. If hash-chain integrity is required for credential events, the dedicated table should carry its own `prev_hash`/`entry_hash` computed from canonical event payload, not just rely on the current HTTP hash chain.

### Migration implications

Current migration contract:

- Both SQLite and PostgreSQL migrations are required and must stay in lockstep (`.trellis/spec/backend/database-guidelines.md:44-50`).
- Current latest migration in spec and repository is `000058_restore_drill_evidence`; a new schema change would use the next version, likely `000059_*`, with paired `.up.sql` and `.down.sql` files under both engines.

If extending `audit_logs`:

- Add nullable columns to both engines with safe defaults where useful (`event_type`, IDs, `metadata_json`, `outcome`, etc.).
- Add indexes for fields used by filters: `event_type`, `node_id`, `ssh_key_id`, `task_id`, `task_run_id`, `batch_id`, `created_at`.
- Update `model.AuditLog` JSON/GORM fields.
- Update `hashAuditLogEntry` to include new fields; consider a hash version field if future verification needs to distinguish legacy entries.
- Update Audit list/export tests and docs/API types for new fields.

If adding a dedicated table:

- Add a model such as `CredentialAuditEvent` with snake_case JSON/GORM names and optional hash-chain columns.
- Use TEXT JSON for SQLite and either TEXT or JSONB for PostgreSQL depending on query needs; using TEXT simplifies cross-engine parity.
- Suggested indexes: `idx_credential_events_occurred_at`, `idx_credential_events_event_type`, `idx_credential_events_user_id`, `idx_credential_events_node_id`, `idx_credential_events_ssh_key_id`, `idx_credential_events_task_id`, `idx_credential_events_task_run_id`, `idx_credential_events_policy_id`, `idx_credential_events_batch_id`, `idx_credential_events_outcome`, and hash indexes if hash chained.
- Add down migrations that drop indexes/table in reverse order.
- If exposed through API, add admin-only route permissions and router tests per sensitive-surface RBAC spec (`.trellis/spec/backend/quality-guidelines.md:170-201`).

### Test implications

Cross-cutting tests:

- Event writer unit tests should assert no raw PEM private key, password, token-like string, restic repository password, executor config, terminal input/output, or raw command output appears in any persisted event field or metadata.
- Hash-chain tests should assert `prev_hash` links to the prior event and `entry_hash` changes when event metadata changes. Current code has audit handler tests but no direct hash-chain tests found (`backend/internal/api/handlers/audit_handler_test.go:18-108`).
- Migration/startup coverage should include both SQLite and PostgreSQL migration files for the new version, following existing drill evidence migration expectations (`.trellis/spec/backend/error-handling.md:374-380`).
- RBAC/router tests are needed for any new credential event list/export endpoint and for sensitive export routes.

Flow-specific tests:

| Flow | Tests to add/update |
|---|---|
| SSH key test | Assert `POST /ssh-keys/:id/test-connection` records key ID/fingerprint, requested node IDs/counts, outcome summary, and no private key. Current handler tests would need DB event assertions around `backend/internal/api/handlers/ssh_key_handler.go:356-455`. |
| SSH key export | Assert `GET /ssh-keys/export` records export format/scope/count and `contains_private_key=false`; current middleware skip means this must be manual or route-specific. |
| Config export with secrets | Assert `GET /config/export?include_secrets=true` records `contains_secret_material=true` and counts, but not exported secret values. Current code only structured-logs this (`backend/internal/api/handlers/config_handler.go:65-72`). |
| Node test/doctor | Assert node ID, credential kind/source, managed SSH key ID when present, latency/outcome, and no password/private key. For node test, assert `SSHKey.LastUsedAt` and event write stay consistent (`backend/internal/api/handlers/node_handler.go:779-784`). |
| SFTP/file browser | Assert GET file list/read emits an event with path metadata and no file content. |
| Batch command | Assert batch create records batch ID, node/task/run IDs, command hash/sanitized preview, and no raw secret-like command payload. |
| Command task execution | Assert task-run execution records `task_run_id`, executor type, credential kind/source, outcome/exit code, and no stdout/stderr. Async tests should follow the project async task-run assertion guidance (`.trellis/spec/backend/quality-guidelines.md:76-83`). |
| Terminal | Assert auth failure, node-not-found, SSH dial failure, open, and close each record credential/session context where available; assert no terminal keystrokes/output. |
| Drills | Assert trigger/phase/complete events include policy/task/run/source/sandbox IDs, phase, evidence ID, and sanitized errors; assert cross-node transfer block does not persist source credentials (`backend/internal/task/drill.go:605-619`). |
| Background/system flows | If included, assert `actor_type="system"`, no user fields, and stable operation labels for cron/prober/retention/integrity. |

### Related Specs

- `.trellis/spec/backend/quality-guidelines.md:39-44` — secrets must remain encrypted/stripped; evidence and command output need shared sanitization.
- `.trellis/spec/backend/quality-guidelines.md:170-201` — sensitive credential surfaces should fail closed and need full-router RBAC tests.
- `.trellis/spec/backend/logging-guidelines.md:68-76` — passwords, private keys, tokens, decrypted values, and unsafe command output must not be logged.
- `.trellis/spec/backend/database-guidelines.md:44-50` — paired SQLite/PostgreSQL migrations; latest migration is currently `000058_restore_drill_evidence`.
- `.trellis/spec/backend/database-guidelines.md:76-86` — do not expose raw secret model values or manually encrypt/decrypt in handlers.
- `.trellis/spec/backend/error-handling.md:338-352` — restore drill evidence identity/phase fields and sanitized error constraints.
- `.trellis/spec/frontend/type-safety.md:274-316` — frontend drill evidence mappers preserve `trigger_type="drill"` and map snake_case evidence fields if credential events are surfaced to frontend later.

### External References

- None used. This research is based on Xirang repository code and Trellis specs only.

## Caveats / Not Found

- No existing dedicated credential-use audit/event table was found.
- No existing `AuditLog` metadata column or domain-specific event fields were found.
- No direct tests for `SaveAuditLogWithHashChain` or hash-chain verification were found; existing audit tests cover list/export behavior only.
- `SSHKey.LastUsedAt` is updated only in node connection test when a node uses a managed SSH key; key test, task execution, terminal, SFTP, probes, and other credential uses do not update it in the searched code.
- Current `AuditLogger` skip behavior means sensitive GET routes are not captured unless they manually log or middleware behavior changes.
- Terminal currently avoids recording terminal stream contents; this is a safe pattern to preserve for credential-use auditing.
