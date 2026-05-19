# Research: P1b credential audit extended code surfaces

- **Query**: Research current code surfaces for P1b credential audit expansion. Cover existing P1 credentialaudit package/events, SFTP/file list and read paths, Docker volume discovery over SSH, config export include_secrets behavior, node doctor and migration preflight diagnostics, probes/background workers, current Settings risk aggregation, and recommended implementation boundaries/tests.
- **Scope**: internal
- **Date**: 2026-05-19

## Findings

### Files Found

| File Path | Description |
|---|---|
| `backend/internal/credentialaudit/audit.go` | Credential audit writer/context API, outcome constants, metadata/error sanitization. |
| `backend/internal/credentialaudit/audit_test.go` | Writer tests for CreatedAt, output redaction, metadata/key sanitization and field bounds. |
| `backend/internal/model/models.go` | `model.CredentialAuditEvent` table model and field comments. |
| `backend/internal/database/migrations/sqlite/000059_ssh_key_scope_credential_audit.up.sql` | SQLite migration adding SSH key scope fields and `credential_audit_events`. |
| `backend/internal/database/migrations/postgres/000059_ssh_key_scope_credential_audit.up.sql` | PostgreSQL migration adding SSH key scope fields and `credential_audit_events`. |
| `backend/internal/sshutil/scope.go` | SSH credential purpose constants and scope validation helpers. |
| `backend/internal/sshutil/ssh_auth.go` | SSH auth builder for purpose-scoped credentials; returns safe credential source/kind. |
| `backend/internal/api/handlers/helpers.go` | `writeCredentialAuditFromGin` and `credentialAuditOutcome` helper used by handlers. |
| `backend/internal/api/handlers/ssh_key_handler.go` | Existing SSH key test/export credential-audit events. |
| `backend/internal/api/handlers/node_handler.go` | Existing node SSH test credential-audit events; migration preflight tests live in `node_handler_test.go`. |
| `backend/internal/api/handlers/terminal_handler.go` | Existing terminal open/failure/close credential-audit events. |
| `backend/internal/api/handlers/task_handler.go` | Existing task manual/restore/batch trigger credential-audit events. |
| `backend/internal/api/handlers/batch_handler.go` | Existing batch command creation credential-audit event. |
| `backend/internal/api/handlers/policy_handler.go` | Existing restore-drill trigger credential-audit events. |
| `backend/internal/task/runner.go` | Runtime task credential-audit context injection. |
| `backend/internal/task/executor/ssh_connect.go` | Runtime SSH dial audit writes via `WriteRuntime` when context carries credential audit. |
| `backend/internal/task/executor/executor.go` | Rsync credential audit and remote target readiness over SSH. |
| `backend/internal/task/drill.go` | Restore-drill runtime credential audit context and phase events. |
| `backend/internal/api/handlers/file_handler.go` | SFTP node file listing/content preview and local backup-file listing. |
| `backend/internal/api/handlers/file_handler_validate_test.go` | Path allowlist, symlink, dev bypass, and `RealPath` validation tests for file browser. |
| `web/src/lib/api/files-api.ts` | Frontend API client for node file list/content and task backup-file list. |
| `web/src/components/file-browser.tsx` | Frontend file browser and file preview trigger surface. |
| `backend/internal/api/handlers/docker_handler.go` | Docker volume discovery over SSH. |
| `web/src/lib/api/docker-api.ts` | Frontend API client for Docker volume discovery. |
| `web/src/components/docker-volumes-panel.tsx` | Frontend Docker volume display and mountpoint selection surface. |
| `backend/internal/api/handlers/config_handler.go` | Config export/import handler and `include_secrets` handling. |
| `backend/internal/api/handlers/config_handler_test.go` | Config export/import tests; preserves SSH key scope metadata. |
| `web/src/lib/api/config-api.ts` | Frontend config export/import client; supports `includeSecrets`. |
| `web/src/components/config-export-import.tsx` | UI export/import card; current export call omits `includeSecrets`. |
| `backend/internal/api/handlers/node_doctor_handler.go` | Node Fleet Doctor diagnostics over SSH. |
| `backend/internal/api/handlers/node_doctor_handler_test.go` | Doctor tests for body rejection, command allowlist, sensitive evidence sanitization, thresholds. |
| `backend/internal/api/handlers/node_migrate_preflight_handler.go` | Node migration preflight diagnostics over SSH and local filesystem. |
| `backend/internal/probe/prober.go` | Background SSH node prober and metric collector. |
| `backend/internal/sshutil/probe.go` | Shared `ProbeNode` SSH probe helper. |
| `backend/internal/uptime/prober.go` | Background HTTP/TCP service monitor prober; no SSH credential use. |
| `backend/internal/nodelogs/scheduler.go` | Background node-log scheduler. |
| `backend/internal/nodelogs/fetcher.go` | Node-log remote script builder for `journalctl` and tailed files. |
| `backend/internal/nodelogs/ssh_runner.go` | Node-log SSH runner using `PurposeNodeLogs`. |
| `backend/internal/task/retention.go` | Restic/rclone retention workers using `PurposeRetention`. |
| `backend/internal/task/integrity_checker.go` | Restic/rclone integrity checks using `PurposeIntegrityCheck`. |
| `backend/internal/snapshot/indexer.go` | Restic snapshot indexing over SSH using `PurposeSnapshot`. |
| `backend/internal/api/handlers/snapshot_diff_handler.go` | Snapshot diff handler over SSH using `PurposeSnapshotDiff`. |
| `backend/internal/anomaly/snapshot_diff.go` | Background anomaly snapshot diff over SSH using `PurposeSnapshotDiff`. |
| `backend/internal/api/handlers/settings_handler.go` | Settings security risk summary and credential-audit aggregation. |
| `backend/internal/api/handlers/settings_handler_test.go` | Backend risk summary tests including no-secret response assertions. |
| `web/src/lib/api/settings-api.ts` | Frontend security risk summary mapper/types. |
| `web/src/lib/api/settings-api.test.ts` | Frontend mapper tests for risk summary. |
| `web/src/pages/settings-page.system.tsx` | Settings UI advisory security risk cards. |
| `.trellis/spec/backend/quality-guidelines.md` | Contracts for Settings security risk summary and credential-use audit events. |

### Code Patterns

#### Existing P1 `credentialaudit` package and events

- `backend/internal/credentialaudit/audit.go:15-19` defines outcomes: `success`, `failure`, `blocked`.
- `backend/internal/credentialaudit/audit.go:21-39` defines `credentialaudit.Event` with actor, action, purpose, safe credential source/kind, optional IDs, outcome, metadata, client IP and user-agent.
- Runtime propagation exists via:
  - `WithRuntimeContext` at `backend/internal/credentialaudit/audit.go:48-53`.
  - `RuntimeEvent` at `backend/internal/credentialaudit/audit.go:55-64`.
  - `WriteRuntime` at `backend/internal/credentialaudit/audit.go:66-73`.
- Direct writes go through `Write` at `backend/internal/credentialaudit/audit.go:143-175`, which:
  - Defaults empty outcome to success (`:147-150`).
  - Sanitizes and bounds actor/action/purpose/source fields (`:156-173`).
  - Marshals sanitized metadata (`:151-155`).
- Metadata sanitation drops forbidden keys containing `private`, `password`, `token`, `secret`, `credential`, `config`, `output`, `stream`, or `command` at `backend/internal/credentialaudit/audit.go:207-265`.
- Error-message sanitation redacts after output markers `输出:`, `output:`, `stdout:`, `stderr:` at `backend/internal/credentialaudit/audit.go:276-295`.
- Gin handler helper is `writeCredentialAuditFromGin` in `backend/internal/api/handlers/helpers.go:28-35`; outcome aggregation helper is `credentialAuditOutcome` at `:37-47`.
- Table/model contract:
  - `model.CredentialAuditEvent` is documented as never storing raw secrets, terminal streams, command output, or executor config at `backend/internal/model/models.go:421-443`.
  - SQLite migration creates the table and indexes at `backend/internal/database/migrations/sqlite/000059_ssh_key_scope_credential_audit.up.sql:9-43`.
  - PostgreSQL migration creates the table and indexes at `backend/internal/database/migrations/postgres/000059_ssh_key_scope_credential_audit.up.sql:11-45`.
- SSH credential purposes are enumerated in `backend/internal/sshutil/scope.go:13-55`, including existing P1/P1b-relevant purposes: `file_browser`, `docker_volumes`, `node_logs`, `probe`, `node_migration`, `snapshot`, `snapshot_diff`, `integrity_check`, and `retention`.
- Purpose-scoped auth resolution:
  - `BuildSSHAuthForPurpose` delegates to `BuildSSHAuthWithCredential` at `backend/internal/sshutil/ssh_auth.go:76-78`.
  - `BuildSSHAuthWithKeyForPurpose` returns `ResolvedCredential` labels for password, SSH key, or node private key at `backend/internal/sshutil/ssh_auth.go:87-118`.
  - Managed SSH keys are validated for purpose/node/tag scope in `ResolveKeyContentForPurpose` at `backend/internal/sshutil/ssh_auth.go:34-67`.

Current P1 action strings are documented in `.trellis/spec/backend/quality-guidelines.md:380-405` and implemented across handlers/runtime paths:

| Action | Current implementation surface |
|---|---|
| `ssh_key.test_connection` | `backend/internal/api/handlers/ssh_key_handler.go:563-576` |
| `ssh_key.export` | `backend/internal/api/handlers/ssh_key_handler.go:714-727` |
| `node.credential.test_connection` | `backend/internal/api/handlers/node_handler.go:687-699`, `:721-733`, `:760-773`, `:830-844` |
| `terminal.failure` | `backend/internal/api/handlers/terminal_handler.go:248-260`, `:272-284`, `:303-316`, `:330-342`, `:362-374`, `:388-399` |
| `terminal.open` | `backend/internal/api/handlers/terminal_handler.go:476-488` |
| `terminal.close` | `backend/internal/api/handlers/terminal_handler.go:520-533` |
| `task.manual_trigger` | `backend/internal/api/handlers/task_handler.go:467-487` |
| `task.restore_trigger` | `backend/internal/api/handlers/task_handler.go:616-639` |
| `task.batch_trigger` | `backend/internal/api/handlers/task_handler.go:716-727` |
| `batch_command.create` | `backend/internal/api/handlers/batch_handler.go:150-163` |
| `task.credential.use` | Runtime context at `backend/internal/task/runner.go:848-867`; SSH dial writes at `backend/internal/task/executor/ssh_connect.go:101-139`; rsync writes at `backend/internal/task/executor/executor.go:557-589`; drill context at `backend/internal/task/drill.go:535-557` |
| `drill.trigger` | `backend/internal/api/handlers/policy_handler.go:946-968` |
| `drill.phase` | `backend/internal/task/drill.go:559-590` |

#### SFTP/file list and read paths

- Routes:
  - `GET /nodes/:id/files` uses `middleware.RBAC("nodes:read")` and `OwnershipNodeCheck` at `backend/internal/api/router.go:160`.
  - `GET /nodes/:id/files/content` uses the same RBAC/ownership at `backend/internal/api/router.go:161`.
  - `GET /tasks/:id/backup-files` is admin-only at `backend/internal/api/router.go:283`.
- `ListNodeFiles` in `backend/internal/api/handlers/file_handler.go:75-121`:
  - Loads node with SSH key (`:86-90`).
  - Dials SFTP (`:92-99`).
  - Validates requested path with `validateNodePath` before listing (`:101-109`).
  - Returns `FileListResponse` (`:116-120`).
- `GetNodeFileContent` in `backend/internal/api/handlers/file_handler.go:138-209`:
  - Requires `path` query (`:144-148`).
  - Dials SFTP (`:156-163`).
  - Validates path (`:165-170`).
  - Rejects directories (`:172-180`).
  - Reads up to `filePreviewMaxBytes` (1 MiB) and marks truncation (`:190-208`).
- `dialSFTP` uses `sshutil.BuildSSHAuthForPurpose(..., sshutil.PurposeFileBrowser)` at `backend/internal/api/handlers/file_handler.go:277-303`.
- `validateNodePath` roots browsing to node `BasePath` and the node's task `RsyncSource` values, resolving paths and roots through SFTP `RealPath` to catch symlink escapes at `backend/internal/api/handlers/file_handler.go:311-377`.
- Local backup-file listing uses `validateLocalPath` over `Task.RsyncTarget` at `backend/internal/api/handlers/file_handler.go:225-273` and `:379-409`; this path is local filesystem access and does not use SSH credentials.
- Frontend calls:
  - `listNodeFiles` and `getNodeFileContent` in `web/src/lib/api/files-api.ts:25-51`.
  - `listTaskBackupFiles` in `web/src/lib/api/files-api.ts:53-65`.
  - `FileBrowser` navigates directories and opens preview dialog for files at `web/src/components/file-browser.tsx:65-107` and renders preview at `:250-258`.
- Current credential-audit behavior: no `credentialaudit` import or write call was found in `backend/internal/api/handlers/file_handler.go`.

#### Docker volume discovery over SSH

- Route: `GET /nodes/:id/docker-volumes` uses `middleware.RBAC("nodes:read")` and `OwnershipNodeCheck` at `backend/internal/api/router.go:162`.
- Handler flow in `backend/internal/api/handlers/docker_handler.go`:
  - Loads node with SSH key at `:56-60`.
  - Dials SSH via `dialSSHForDocker` at `:62-68`.
  - `dialSSHForDocker` uses `sshutil.BuildSSHAuthForPurpose(..., sshutil.PurposeDockerVolumes)` at `:83-99`.
  - Runs `docker volume ls --format '{{json .}}'` at `:108-128`.
  - Parses JSON lines at `:130-142`.
  - For entries without mountpoint, calls `inspectVolumeMountpoint` at `:148-160`.
  - `inspectVolumeMountpoint` validates names with `safeDockerName` and runs `docker volume inspect '<name>' --format '{{.Mountpoint}}'` at `:165-184`.
- Frontend calls `listDockerVolumes` in `web/src/lib/api/docker-api.ts:14-29`.
- `DockerVolumesPanel` displays name/driver/mountpoint and can select a mountpoint path at `web/src/components/docker-volumes-panel.tsx:78-127`.
- Current credential-audit behavior: no `credentialaudit` import or write call was found in `backend/internal/api/handlers/docker_handler.go`.

#### Config export `include_secrets` behavior

- Route: `GET /config/export` is admin-only through `middleware.RequireRole("admin")` at `backend/internal/api/router.go:313`.
- Handler reads `include_secrets=true` at `backend/internal/api/handlers/config_handler.go:58-60`.
- If `include_secrets` is true, the handler checks the Gin context role is `admin` and rejects otherwise at `backend/internal/api/handlers/config_handler.go:61-66`.
- Sensitive export is logged to the regular audit logger (`logger.Module("audit")`) at `backend/internal/api/handlers/config_handler.go:67-74`; this is not a `credentialaudit.Write` event.
- Exported data with default `include_secrets=false` still includes non-secret config such as node name/host/port/username/auth_type/tags/base_path/ssh_key_id (`backend/internal/api/handlers/config_handler.go:101-113`), SSH key metadata/scope but not private key (`:121-139`), policy paths (`:141-163`), task commands/paths/executor type (`:165-197`), and DB-backed system settings (`:199-208`).
- `include_secrets=true` additionally includes:
  - Node `password` and `private_key` at `backend/internal/api/handlers/config_handler.go:114-117`.
  - SSH key `private_key` at `backend/internal/api/handlers/config_handler.go:135-137`.
  - Task `executor_config` at `backend/internal/api/handlers/config_handler.go:193-195`.
- Frontend client supports `includeSecrets` and appends `?include_secrets=true` at `web/src/lib/api/config-api.ts:34-39`.
- Current UI export card calls `apiClient.exportConfig(token)` without the second argument at `web/src/components/config-export-import.tsx:20-37`, so the UI currently requests `include_secrets=false`.
- Current tests in `backend/internal/api/handlers/config_handler_test.go` cover export/import round trip and SSH key scope metadata preservation, but no `include_secrets` or secret-exclusion assertion was found by searching `config_handler_test.go`, `config-api.test.ts`, and `config-export-import.tsx`.

#### Node doctor diagnostics

- Route: `POST /nodes/:id/doctor` uses `middleware.RBAC("nodes:test")` and `OwnershipNodeCheck` at `backend/internal/api/router.go:154`.
- `RunDoctor` rejects request bodies/custom checks via `doctorRequestBodyAllowed` at `backend/internal/api/handlers/node_doctor_handler.go:64-90` and `:103-115`.
- The runner builds SSH auth using `sshutil.BuildSSHAuthWithKeyForPurpose(..., sshutil.PurposeNodeTest)` at `backend/internal/api/handlers/node_doctor_handler.go:125-133`.
- Diagnostic outputs are added through `r.add`, which sanitizes `Evidence` and `Suggestion` with `sanitizeDoctorEvidence` at `backend/internal/api/handlers/node_doctor_handler.go:182-189`.
- Doctor checks include:
  - Auth config at `backend/internal/api/handlers/node_doctor_handler.go:191-212`.
  - Known-hosts config at `:214-233`.
  - SSH dial classification at `:235-280`.
  - Sudo check using `sudo -n true 2>&1` at `:290-305`.
  - Tool checks using `command -v` and a fixed tool allowlist at `:307-366`, `:604-611`.
  - Backup directory checks using `test -d` / `test -w` at `:368-450`.
  - Disk check using `df -BG / | awk ...` at `:452-490`.
  - Probe-status summary from stored node fields at `:492-507`.
- Command allowlist enforcement is in `doctorCommandAllowed` at `backend/internal/api/handlers/node_doctor_handler.go:509-563`; path/tool validators are at `:565-624`.
- Sensitive evidence redaction checks marker strings including private key, password, token, secret, bearer, proxy URL, and `DATA_ENCRYPTION_KEY` at `backend/internal/api/handlers/node_doctor_handler.go:626-644`.
- Tests cover body rejection, auth-failure skips, probe status, setting-driven disk threshold, SSH error classification, command allowlist, and sensitive evidence sanitization in `backend/internal/api/handlers/node_doctor_handler_test.go:18-224`.
- Current credential-audit behavior: no `credentialaudit` import or write call was found in `backend/internal/api/handlers/node_doctor_handler.go`.

#### Node migration preflight diagnostics

- Route: `POST /nodes/:id/migrate/preflight` uses `middleware.RBAC("nodes:write")` and `OwnershipNodeCheck` at `backend/internal/api/router.go:350`.
- Request/response types are in `backend/internal/api/handlers/node_migrate_preflight_handler.go:21-61`; node summary includes `id`, `name`, `host`, `status`, and disk fields (`:34-41`).
- Handler loads source/target nodes with SSH keys at `backend/internal/api/handlers/node_migrate_preflight_handler.go:95-112` and checks operator ownership of target nodes at `:114-126`.
- Preflight uses `sshutil.ProbeNode(targetNode, h.db)` at `backend/internal/api/handlers/node_migrate_preflight_handler.go:198-216`; `ProbeNode` uses `PurposeProbe`, not `PurposeNodeMigration` (`backend/internal/sshutil/probe.go:22-31`).
- Tool/path checks then dial SSH with `executor.DialSSHForNodePurpose(ctx, targetNode, sshutil.PurposeNodeMigration)` at `backend/internal/api/handlers/node_migrate_preflight_handler.go:226-253`.
- Remote commands include `which/command -v <tool>` at `backend/internal/api/handlers/node_migrate_preflight_handler.go:240-252` and `test -d <sourcePath>` at `:255-279`.
- Local backup data size uses `os.Stat` and `du -sm` against task `RsyncTarget` at `backend/internal/api/handlers/node_migrate_preflight_handler.go:320-377`.
- Existing migration preflight test coverage found: DB policy lookup failure in `backend/internal/api/handlers/node_handler_test.go:219-256`.
- Current credential-audit behavior: no `credentialaudit` import or write call was found in `backend/internal/api/handlers/node_migrate_preflight_handler.go`.

#### Probes and background workers

- Shared SSH probe helper:
  - `sshutil.ProbeNode` uses `BuildSSHAuthForPurpose(..., PurposeProbe)` at `backend/internal/sshutil/probe.go:22-31`.
  - It dials SSH and runs a disk probe command at `backend/internal/sshutil/probe.go:34-67`.
- Node prober:
  - `probe.Prober` loads unarchived nodes with `Preload("SSHKey")` at `backend/internal/probe/prober.go:97-126`.
  - `probeNode` calls `sshutil.ProbeNode(node, p.db)` at `backend/internal/probe/prober.go:136-164`.
  - On success it updates node status/disk fields and asynchronously collects metrics at `backend/internal/probe/prober.go:166-198`.
  - `collectMetrics` dials SSH again with `PurposeProbe` and runs a CPU/mem/disk/load shell snippet at `backend/internal/probe/prober.go:249-289`.
  - Current credential-audit behavior: no `credentialaudit` import or write call was found in `backend/internal/probe/prober.go`.
- Uptime prober:
  - `backend/internal/uptime/prober.go` probes HTTP/TCP service monitors from the Xirang server; no SSH credential surface was found in this file.
- Node-log worker:
  - Scheduler enqueues nodes needing collection at `backend/internal/nodelogs/scheduler.go:65-94`.
  - Fetcher builds a fixed script using `journalctl` and `tail -c` for configured log paths at `backend/internal/nodelogs/fetcher.go:100-132`.
  - Production SSH runner uses `sshutil.BuildSSHAuthForPurpose(node, r.db, sshutil.PurposeNodeLogs)` at `backend/internal/nodelogs/ssh_runner.go:24-39`.
  - Current credential-audit behavior: no `credentialaudit` import or write call was found in `backend/internal/nodelogs/*`.
- Retention and integrity workers:
  - Restic/rclone retention uses `executor.DialSSHForNodePurpose(..., sshutil.PurposeRetention)` at `backend/internal/task/retention.go:121-207`.
  - Integrity checks use `executor.DialSSHForNodePurpose(..., sshutil.PurposeIntegrityCheck)` at `backend/internal/task/integrity_checker.go:52-130`.
  - These contexts are created with `context.Background()` in the worker methods (`retention.go:124`, `integrity_checker.go:55`, `:102`), so `executor.DialSSHForNodePurpose` only writes runtime audit if a `credentialaudit.RuntimeEvent` exists; the guard/no-op is at `backend/internal/task/executor/ssh_connect.go:101-104`.
- Snapshot/snapshot-diff surfaces:
  - Snapshot indexer uses `PurposeSnapshot` at `backend/internal/snapshot/indexer.go:171-197`.
  - Snapshot diff handler uses `PurposeSnapshotDiff` at `backend/internal/api/handlers/snapshot_diff_handler.go:108-132`.
  - Background anomaly snapshot diff uses `PurposeSnapshotDiff` at `backend/internal/anomaly/snapshot_diff.go:328-340`.
  - Restic executor snapshot list/file browsing uses `PurposeSnapshot` at `backend/internal/task/executor/restic_executor.go:282-333`.
- Restore verifier:
  - `backend/internal/task/verifier/verifier.go:240-259` dials SSH for `PurposeIntegrityCheck` through `BuildSSHAuthForPurpose`.

#### Current Settings risk aggregation

- Route: `GET /settings/security-risk-summary` is admin-only at `backend/internal/api/router.go:309`.
- Handler response shape is defined at `backend/internal/api/handlers/settings_handler.go:27-45`; generated timestamp/summary/items response is emitted at `:88-107`.
- Current risk categories are assembled in `securityRiskItems` at `backend/internal/api/handlers/settings_handler.go:203-247`:
  - `root_ssh_users` (`:250-266`)
  - `reused_ssh_keys` (`:286-323`)
  - `sudo_enabled_nodes` (`:268-284`)
  - `broad_scope_ssh_keys` (`:325-346`)
  - `disabled_ssh_keys_in_use` (`:348-378`)
  - `expired_ssh_keys_in_use` (`:380-410`)
  - `stale_ssh_keys` (`:412-435`)
  - `recent_credential_operations` (`:437-468`)
  - `weak_security_defaults` (`:512-564`)
- `recent_credential_operations` aggregates only action/count rows from `model.CredentialAuditEvent` newer than 7 days at `backend/internal/api/handlers/settings_handler.go:437-468`.
- High-risk credential-audit actions included in the Settings risk summary are currently:
  - `ssh_key.export`
  - `terminal.open`
  - `terminal.failure`
  - `task.manual_trigger`
  - `task.restore_trigger`
  - `task.batch_trigger`
  - `task.credential.use`
  - `batch_command.create`
  - `drill.trigger`
  - `drill.phase`
  from `backend/internal/api/handlers/settings_handler.go:470-482`.
- Existing P1 actions not currently included in `highRiskCredentialAuditActions()` are `ssh_key.test_connection`, `node.credential.test_connection`, and `terminal.close`.
- The frontend mapper enumerates current risk codes and falls unknown codes back to `weak_security_defaults` at `web/src/lib/api/settings-api.ts:26-37` and `:79-111`.
- The Settings UI renders risk items as advisory cards without remediation buttons at `web/src/pages/settings-page.system.tsx:157-212`.
- Backend test `TestSettingsSecurityRiskSummaryCountsAdvisorySignals` verifies counts and checks the response body does not include raw private-key fixture text or host substrings at `backend/internal/api/handlers/settings_handler_test.go:61-217`.
- Frontend mapper test covers snake_case mapping, numeric fallback, known risk codes, and unknown code/severity fallback at `web/src/lib/api/settings-api.test.ts:4-76`.

### Related Specs

- `.trellis/spec/backend/quality-guidelines.md:300-362` — Settings security risk summary route, response shape, contracts, validation matrix, and required tests.
- `.trellis/spec/backend/quality-guidelines.md:380-425` — Credential-use audit event scope, writer API, current P1 action strings, no-secret contracts, and required tests.

### Recommended implementation boundaries / tests

#### Implementation boundaries

- Keep all new credential-use records on the existing `credential_audit_events` table/model (`model.CredentialAuditEvent`) and writer APIs (`credentialaudit.Write`, `FromGin`, `WithRuntimeContext`, `WriteRuntime`) described in `.trellis/spec/backend/quality-guidelines.md:389-404`.
- Use existing SSH purpose constants from `backend/internal/sshutil/scope.go:13-55` for the P1b surfaces already named in code: `file_browser`, `docker_volumes`, `node_logs`, `probe`, `node_migration`, `snapshot`, `snapshot_diff`, `integrity_check`, `retention`.
- For handler-driven GET/POST surfaces, the current helper pattern is best-effort `writeCredentialAuditFromGin(c, db, event)` from `backend/internal/api/handlers/helpers.go:28-35`; audit write failures are logged, not returned to callers.
- For background/runtime surfaces that use `executor.DialSSHForNodePurpose`, audit writes only occur when `credentialaudit.WithRuntimeContext` has been attached to the context; absent runtime context is explicitly a no-op at `backend/internal/task/executor/ssh_connect.go:101-104`.
- Keep event fields to safe labels/IDs/counts/stages/booleans/latency. Existing sanitizer will drop metadata keys containing `private`, `password`, `token`, `secret`, `credential`, `config`, `output`, `stream`, or `command`; therefore new metadata should avoid those keys if it is intended to persist.
- Do not place file contents, terminal streams, remote command output, `executor_config`, raw task commands, Docker command output, node passwords, private keys, tokens, proxy URLs, or decrypted settings into `metadata`, `error_message`, `credential_source`, or Settings risk examples.
- If new action strings should affect Settings risk aggregation, update both `highRiskCredentialAuditActions()` and `credentialActionLabel()` in `backend/internal/api/handlers/settings_handler.go:470-509`; if new risk codes are added, update frontend `SecurityRiskCode`/mapper in `web/src/lib/api/settings-api.ts:26-111`.
- Keep Settings risk summary advisory-only and admin-only per `.trellis/spec/backend/quality-guidelines.md:327-347`.

#### Test boundaries

- Add handler tests for each newly audited surface asserting an event is written with safe `action`, `purpose`, `outcome`, `node_id`/`task_id`/`ssh_key_id` as applicable, and no raw secret/file-content/command-output strings in persisted `metadata` or `error_message`.
- Add no-event/raw-output assertions for file-preview surfaces: `GetNodeFileContent` should not persist file `content`, preview path contents, or SFTP read output in credential audit metadata.
- Add Docker volume audit tests around `listDockerVolumes`/`ListVolumes` behavior using safe volume names and unsafe-name cases; persisted events should not include full command output.
- Add config export tests for `include_secrets=false` and `include_secrets=true` paths: default export should omit node password/private key, SSH key private key, and task `executor_config`; sensitive export should be admin-only and should produce a credential-audit event if P1b scope includes config secret export.
- Add node doctor tests that audit entries preserve only check/stage/outcome/count labels and do not include sanitized diagnostic evidence containing private-key/password/token markers.
- Add migration preflight tests for success/failure/blocked outcomes and ensure returned diagnostic host/path information is not copied into Settings risk examples or credential audit metadata beyond safe IDs/counts/stages.
- Add background/runtime tests where context is intentionally present vs absent: with `WithRuntimeContext`, `DialSSHForNodePurpose` should write `task.credential.use`; without context, it should no-op as current code does.
- Extend `TestSettingsSecurityRiskSummaryCountsAdvisorySignals` when adding new high-risk action labels so the summary counts the new action and still excludes raw metadata/error fields.
- Extend `web/src/lib/api/settings-api.test.ts` if adding new risk codes; if only adding action labels under existing `recent_credential_operations`, frontend code likely only needs mapper coverage for unchanged response shape.

## Caveats / Not Found

- No external references were used; this was internal repository research only.
- No dedicated `docker_handler_test.go` was found under `backend/internal/api/handlers`.
- No test currently found that explicitly covers config export `include_secrets=true` or default export secret omission.
- Current file/SFTP, Docker volume, Node Doctor, migration preflight, node-log, probe, retention, integrity, and snapshot/snapshot-diff surfaces use purpose-scoped SSH auth where applicable, but no direct credential-audit event writes were found in those handlers/workers unless they run through runtime task contexts carrying `credentialaudit.WithRuntimeContext`.
- Research only; no lint/test command was run as part of this investigation.
