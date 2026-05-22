# Research: Current credential resolution and use paths

- **Query**: Research current Xirang credential resolution and use paths for task `.trellis/tasks/05-22-p4-credential-broker-foundation`; identify where SSH keys, inline node passwords/private keys, executor credential config, import/export, terminal, task executors, batch command, snapshots/restores currently resolve or decrypt credentials. Include files/functions, current control order, GORM hook/decryption constraints, safe seam candidates, tests likely affected, and risks.
- **Scope**: internal
- **Date**: 2026-05-22

## Findings

### Files Found

| File Path | Description |
|---|---|
| `backend/internal/model/models.go` | Credential-bearing models and GORM encryption/decryption hooks for SSH keys, nodes, tasks, app credentials, integrations, users, and credential audit events. |
| `backend/internal/sshutil/ssh_auth.go` | Shared SSH credential resolution and auth-method construction used by handlers and utilities. |
| `backend/internal/sshutil/scope.go` | SSH key purpose/scope constants and validation for allowed purposes, node IDs, and node tags. |
| `backend/internal/sshutil/probe.go` | Node probe SSH path using shared `BuildSSHAuthForPurpose`. |
| `backend/internal/probe/prober.go` | Scheduled metrics collection over SSH using shared credential resolution and system credential audits. |
| `backend/internal/task/executor/ssh_connect.go` | Central executor SSH dial path and runtime credential audit stages. |
| `backend/internal/task/executor/executor.go` | Rsync backup/restore executor, private-key temp-file use, and executor-local private-key resolver. |
| `backend/internal/task/executor/command_executor.go` | Command task executor SSH credential use. |
| `backend/internal/task/executor/restic_executor.go` | Restic backup/restore/snapshot credential use and repository-password command environment prefix. |
| `backend/internal/task/executor/rclone_executor.go` | Rclone backup/restore credential use. |
| `backend/internal/task/runner.go` | Runtime task loading, `Node.SSHKey` preload, and credential audit runtime context attachment. |
| `backend/internal/task/manager.go` | Restore trigger loading and restore-run dispatch with `Node.SSHKey` preload. |
| `backend/internal/task/hook.go` | Policy pre/post hook SSH execution with `task_hook` purpose. |
| `backend/internal/task/drill.go` | Restore-drill SSH paths and explicit cross-node credential-spread block. |
| `backend/internal/task/integrity_checker.go` | Integrity-check SSH and restic-password use. |
| `backend/internal/task/retention.go` | Retention cleanup SSH and restic-password use. |
| `backend/internal/task/verifier/verifier.go` | Backup verification SSH and restic-password use via shared `sshutil`. |
| `backend/internal/api/handlers/node_handler.go` | Node password/private-key/managed-key CRUD and node test connection. |
| `backend/internal/api/handlers/ssh_key_handler.go` | Managed SSH key CRUD/test/export; public/sanitized export only. |
| `backend/internal/api/handlers/config_handler.go` | Config import/export, including sensitive plaintext export when `include_secrets=true`. |
| `backend/internal/api/handlers/task_handler.go` | Task executor config create/update/preservation, manual trigger, restore, and batch trigger grant/control flow. |
| `backend/internal/api/handlers/batch_handler.go` | Batch command creation, node ownership, step-up, grant enforcement, and command-task creation. |
| `backend/internal/api/handlers/terminal_handler.go` | WebSocket terminal credential path and terminal grant/step-up control order. |
| `backend/internal/api/handlers/file_handler.go` | Remote SFTP file browser credential path. |
| `backend/internal/api/handlers/snapshot_handler.go` | Restic snapshot list/browse/restore handler entry points and snapshot restore audit. |
| `backend/internal/api/handlers/snapshot_diff_handler.go` | Snapshot diff handler SSH path and restic-password command prefix. |
| `backend/internal/snapshot/indexer.go` | Snapshot indexing/status SSH and restic-password paths. |
| `backend/internal/anomaly/snapshot_diff.go` | Background snapshot diff anomaly analysis with full task reload and restic-password command prefix. |
| `backend/internal/api/handlers/docker_handler.go` | Docker volume discovery over SSH using `docker_volumes` purpose. |
| `backend/internal/nodelogs/ssh_runner.go` | Node log retrieval over SSH using `node_logs` purpose. |
| `backend/internal/api/handlers/node_migrate_preflight_handler.go` | Node migration preflight SSH probing/tool/path checks using `node_migration` purpose. |
| `.trellis/tasks/archive/2026-05/05-21-plan-p3-p4-credential-security-hardening/prd.md` | P3/P4 roadmap source identifying external secret broker/Vault/KMS/leases/fallback/import-export semantics as P4 architecture work. |
| `.trellis/tasks/archive/2026-05/05-22-select-next-p3-p4-hardening-slice-2/prd.md` | Prior P3 selection notes; task restore selected while operator/multi-resource grant semantics were deferred. |
| `.trellis/tasks/archive/2026-05/05-22-p3-grant-semantics/prd.md` | Current row-backed grant semantics for owned resources and multi-resource operations. |
| `.trellis/tasks/archive/2026-05/05-22-p3-comprehensive-security-review/research/p3-code-gap-audit.md` | P3 review baseline: current grant coverage and remaining review gaps around grant/list/batch/frontend safety. |

### Code Patterns

#### Credential-bearing model fields and GORM hooks

`backend/internal/model/models.go` is the main persistence boundary for secret material.

- `model.SSHKey.PrivateKey` is `json:"-"` and is encrypted in `BeforeSave`, decrypted in `AfterFind`.
- `model.Node.Password` and `model.Node.PrivateKey` are encrypted in `BeforeSave`, decrypted in `AfterFind`; `Node.SSHKeyID` binds a node to a managed key.
- `model.Task.ExecutorConfig` is `json:"-"`, encrypted in `BeforeSave`, decrypted in `AfterFind`.
- `Node.Sanitized()` clears inline `Password`, inline `PrivateKey`, and nested `SSHKey.PrivateKey` before response use.
- `CredentialAuditEvent` explicitly documents that audit rows must never contain raw secrets, terminal streams, command output, or executor config.

GORM hook constraint: most consumers expect loaded model instances to already contain decrypted plaintext fields. Any broker seam that bypasses normal GORM model loading, uses `Select` without secret fields, uses raw SQL, or operates on already-mutated `BeforeSave` structs must preserve this invariant or explicitly move decryption into the broker.

#### Shared handler/helper SSH credential control order

`backend/internal/sshutil/ssh_auth.go` provides the shared path:

- `ResolveKeyContentForPurpose(node, db, purpose)` resolves key content in this order:
  1. Preloaded `node.SSHKey.PrivateKey` if non-empty; validate with `ValidateSSHKeyScope`.
  2. If `node.SSHKeyID != nil`, DB-load `model.SSHKey` by ID; validate with `ValidateSSHKeyScope`; use its decrypted `PrivateKey`.
  3. Inline `node.PrivateKey`.
  4. No key.
- `BuildSSHAuthWithKeyForPurpose(node, db, purpose)` chooses by `node.AuthType`:
  - `password`: require `node.Password`; return `ssh.Password(node.Password)` and credential source `node.password`.
  - `key`: call `ResolveKeyContentForPurpose`, parse signer, mark managed key last-used, and return `ssh.PublicKeys(signer)`.
- `BuildSSHAuthForPurpose(node, db, purpose)` is the common wrapper returning auth methods and resolved credential metadata.

Important asymmetry: shared `sshutil` can recover from a non-preloaded managed key because it can DB-load `SSHKeyID`.

#### SSH key purpose/scope enforcement

`backend/internal/sshutil/scope.go` defines purpose constants including `terminal`, `task_command`, `batch_command`, `task_backup`, `task_restore`, `task_hook`, `snapshot`, `snapshot_diff`, `integrity_check`, `retention`, `drill`, `file_browser`, `node_test`, `ssh_key_test`, `ssh_key_export`, `probe`, `docker_volumes`, `node_logs`, and `node_migration`.

`ValidateSSHKeyScope(key, node, purpose)` enforces:

1. `ValidateSSHKeyPurpose(key, purpose)`.
2. `AllowedNodeIDs` contains the node or is unrestricted.
3. `AllowedNodeTags` matches the node tags or is unrestricted.

This is currently enforced for managed SSH keys in both shared `sshutil` and executor-local resolver paths. Inline node private keys/passwords do not have per-key scope rows; they are governed by node/task ownership/RBAC/step-up/grants at the operation layer.

#### Executor SSH credential control order

`backend/internal/task/executor/ssh_connect.go` and `backend/internal/task/executor/executor.go` provide the runtime executor path:

- `DialSSHForNodePurpose(ctx, node, purpose)`:
  1. Normalizes port and username default.
  2. Calls `resolveSSHAuthMethodsForPurpose(node, purpose)`.
  3. Resolves host key callback.
  4. Dials SSH via `sshutil.DialSSH`.
  5. Writes runtime credential audit on auth build, host-key, dial, and success stages only when a `credentialaudit.RuntimeContext` is attached.
- `resolveSSHAuthMethodsForPurpose(node, purpose)`:
  - `key`: call executor-local `resolveNodePrivateKeyForPurpose`, parse signer, return public-key auth.
  - `password`: require `node.Password`, return password auth.
- Executor-local `resolveNodePrivateKeyForPurpose(node, purpose)` resolves in this order:
  1. Preloaded `node.SSHKey.PrivateKey`; validate scope.
  2. If `node.SSHKeyID != nil` but `node.SSHKey` is absent or empty, return blocked error `节点绑定的密钥不存在，请检查密钥配置`.
  3. Inline `node.PrivateKey`.
  4. No key.

Important asymmetry: executor code does not DB-load `SSHKeyID`. Managed keys in executor paths require callers to load tasks/nodes with `Preload("Node.SSHKey")` or equivalent.

#### Task runner and restore preloads

`backend/internal/task/runner.go` loads normal task runs with `Preload("Node").Preload("Node.SSHKey").Preload("Policy")`. It attaches a runtime credential audit context through `withTaskCredentialAuditContext`, with action `task.credential.use`, user/role `system`, node/task/run/policy IDs, trigger type, executor type, and source metadata.

`backend/internal/task/manager.go` uses `Preload("Node").Preload("Node.SSHKey").Preload("Policy")` before restore dispatch. Restore creates a new run and dispatches `runRestoreTask` with a restore-flavored task copy.

These preloads are the reason managed SSH keys work in normal executor paths.

#### Task executors and executor config secrets

- `backend/internal/task/executor/command_executor.go`: command tasks call `DialSSHForNodePurpose(ctx, task.Node, sshutil.PurposeTaskCommand)`.
- `backend/internal/task/executor/restic_executor.go`:
  - Backup uses `PurposeTaskBackup`.
  - Restore uses `PurposeTaskRestore`.
  - Snapshot list/file/restore paths use `PurposeSnapshot`.
  - `ResticConfig.RepositoryPassword` is read from decrypted `Task.ExecutorConfig`.
  - `buildResticEnvPrefix(password)` returns `RESTIC_PASSWORD=...` shell environment prefixes using `ShellEscape`.
- `backend/internal/task/executor/rclone_executor.go`:
  - Backup/restore use `PurposeTaskBackup`/`PurposeTaskRestore`.
  - The observed `RcloneConfig` has operational fields (`bandwidth_limit`, `transfers`) and no direct secret-bearing field in this slice.
- `backend/internal/task/executor/executor.go`:
  - Rsync remote backup/restore uses resolved private key material to create local temp key files (`xirang-key-*.pem`, chmod `0600`) for external `rsync` invocations.
  - Rsync remote execution rejects password auth for backup (`rsync 远程执行暂不支持密码认证，请为节点配置 SSH key`).

Executor config constraints:

- `Task.ExecutorConfig` is encrypted/decrypted as one JSON string by model hooks.
- Restic repository passwords are not brokered separately today; they become shell command environment prefixes.
- Task update logic preserves blank secret-like config values to avoid accidental secret erasure.

#### Node, SSH key, and config handlers

`backend/internal/api/handlers/node_handler.go` accepts node `password`, `private_key`, and `ssh_key_id` in request DTOs. Update paths preserve existing decrypted password/private key when omitted. Node test uses `sshutil.BuildSSHAuthWithKeyForPurpose(node, h.db, sshutil.PurposeNodeTest)`.

`backend/internal/api/handlers/ssh_key_handler.go` stores normalized private key material through GORM hooks. Response mapping derives public key from decrypted private key but does not return private key. Test connection uses `PurposeSSHKeyTest`. Export validates `PurposeSSHKeyExport` and exports public/sanitized formats, not private key material.

`backend/internal/api/handlers/config_handler.go` is the main intentional plaintext export/import surface:

- Export with `include_secrets=false` returns sanitized config.
- Export with `include_secrets=true` includes decrypted `Node.Password`, `Node.PrivateKey`, `SSHKey.PrivateKey`, and `Task.ExecutorConfig` in the response.
- Import writes imported `private_key`, `password`, and `executor_config` back through model hooks.

This is a broker-sensitive boundary because it intentionally crosses the plaintext secret line for admins under P3 grant/step-up controls.

#### Terminal control order

`backend/internal/api/handlers/terminal_handler.go` WebSocket terminal flow currently performs:

1. Reserve session slot.
2. Upgrade WebSocket.
3. Read auth message.
4. Validate primary realtime token with admin role.
5. Validate step-up proof.
6. Parse `node_id`.
7. Enforce terminal credential grant.
8. Load node with `Preload("SSHKey")`.
9. Build SSH auth with `sshutil.BuildSSHAuthForPurpose(node, h.db, sshutil.PurposeTerminal)`.
10. Resolve host-key callback and dial SSH.
11. Start PTY/shell.
12. Audit blocked/failure/open/close events with safe metadata.

Terminal streams are not to be bound into grants and must not be written into credential audit metadata.

#### File browser, docker volumes, node logs, probes, migration preflight

Additional SSH consumers use shared `sshutil` and purpose-scoped credential validation:

- `backend/internal/api/handlers/file_handler.go`: remote SFTP file browser preloads `SSHKey`, calls `BuildSSHAuthForPurpose(..., PurposeFileBrowser)`, then dials SSH/SFTP. Local backup file browsing is filesystem-based and not an SSH credential consumer.
- `backend/internal/api/handlers/docker_handler.go`: Docker volume discovery calls `BuildSSHAuthForPurpose(..., PurposeDockerVolumes)` before running Docker commands over SSH.
- `backend/internal/nodelogs/ssh_runner.go`: node log retrieval calls `BuildSSHAuthForPurpose(..., PurposeNodeLogs)`, then runs bounded log commands and writes credential audit failures by stage.
- `backend/internal/sshutil/probe.go`: ad hoc probe uses `BuildSSHAuthForPurpose(..., PurposeProbe)` and runs `df -BG /`.
- `backend/internal/probe/prober.go`: scheduled metrics collection uses `BuildSSHAuthForPurpose(..., PurposeProbe)` and writes rate-thresholded system credential audit failures.
- `backend/internal/api/handlers/node_migrate_preflight_handler.go`: migration preflight uses `sshutil.ProbeNodeForPurpose(..., PurposeNodeMigration)` and later `executor.DialSSHForNodePurpose(..., PurposeNodeMigration)` for tool/path checks; it writes sanitized credential audit metadata.

#### Snapshots, restores, integrity, retention, and anomaly paths

Snapshot/restic surfaces are split across handlers, executors, and background jobs:

- `backend/internal/api/handlers/snapshot_handler.go` loads tasks with `Preload("Node").Preload("Node.SSHKey")` and delegates snapshot list/file/restore operations to `ResticExecutor`, which uses `PurposeSnapshot` SSH operations and `Task.ExecutorConfig` repository passwords.
- `backend/internal/api/handlers/snapshot_diff_handler.go` preloads `Node.SSHKey`, calls `executor.DialSSHForNodePurpose(..., PurposeSnapshotDiff)`, and builds `RESTIC_PASSWORD=...` prefixes.
- `backend/internal/snapshot/indexer.go` has both preloaded and non-preloaded paths: indexing paths preload `Node.SSHKey`, but `GetIndexStatus` was observed loading only `db.First(&task, taskID)` before calling `exec.ListSnapshots(ctx, task)`. That is credential/path-sensitive because `ResticExecutor.ListSnapshots` expects `task.Node`/credential material for remote restic operations.
- `backend/internal/anomaly/snapshot_diff.go` defensively reloads `fullTask` with `Preload("Node").Preload("Node.SSHKey")`, extracts restic password, dials with `PurposeSnapshotDiff`, and runs `restic snapshots`/`restic diff`.
- `backend/internal/task/integrity_checker.go` and `backend/internal/task/retention.go` preload `Node.SSHKey`, use `PurposeIntegrityCheck`/`PurposeRetention`, and extract restic passwords from decrypted executor config.
- `backend/internal/task/verifier/verifier.go` uses shared `sshutil.BuildSSHAuthForPurpose(..., PurposeIntegrityCheck)` and restic-password command prefixes.

Snapshot restore has two audit layers: grant/handler audit around the snapshot restore operation and actual SSH credential resolution inside restic executor paths.

#### Batch command and batch task trigger semantics

`backend/internal/api/handlers/batch_handler.go` batch command creation flow:

1. Bind and trim command; reject empty/over-length/dangerous commands.
2. Normalize name and generate `batch_id`.
3. Authorize node ownership set.
4. Check each requested node exists and is authorized.
5. Enforce step-up with action `batch_command`, purpose `batch_command`, and proof action `batch_run`.
6. Enforce node-scoped batch command credential grants for all target nodes.
7. Create one `command` task per node with `Source: "batch"`, `BatchID`, and command text.
8. Trigger each task manually via task manager.
9. Write credential audit for batch command creation with counts and safe metadata.

Runtime caveat: tasks created by batch command have `ExecutorType: "command"` and run through `CommandExecutor`, which dials SSH with `PurposeTaskCommand`; operation-level grant/audit for creation uses `PurposeBatchCommand`.

`backend/internal/api/handlers/task_handler.go` batch trigger flow enforces step-up and `EnforceTaskBatchTriggerCredentialGrants` only when `tasksToTrigger` is non-empty. A no-op/unsafe-target-only request audits but bypasses step-up/grant because no eligible task remains to trigger.

#### P3/P4 planning baseline

Archived P3/P4 planning establishes current constraints:

- P3 grants are row-backed authorization records, not bearer tokens.
- Grants are additive to auth, RBAC, ownership, step-up, and credential scope checks.
- Multi-resource operations use row-per-resource, all-or-nothing semantics.
- Grants should not bind to command text, target paths, include-path contents, terminal streams, executor config, hostnames, endpoint/proxy values, or file contents.
- Current grant coverage includes terminal open, config import/export, snapshot restore, task restore, manual task trigger, batch task trigger, and batch command.
- P4 is architecture-level work for credential broker/provider references, Vault/KMS/external secret broker, provider health, leases, fallback, and import/export semantics.

### Current Control Order Summary

#### Managed SSH key / inline key / password resolution

Shared `sshutil` control order:

1. Operation layer: primary auth, RBAC, ownership, step-up, grants as applicable.
2. Load node and optionally preloaded `SSHKey`.
3. `BuildSSHAuthWithKeyForPurpose` switches on `AuthType`.
4. For password auth: require decrypted `Node.Password`.
5. For key auth:
   - preloaded managed key,
   - DB-loaded managed key by `SSHKeyID`,
   - inline `Node.PrivateKey`.
6. Managed key scope/purpose validation.
7. Parse signer / build auth methods.
8. Resolve host-key callback.
9. Dial SSH/SFTP or run remote command.
10. Audit operation/credential event with sanitized metadata.

Executor control order:

1. Operation layer and task manager trigger controls.
2. Task manager loads `Task` with `Node`, `Node.SSHKey`, and `Policy` for normal task/restore paths.
3. Runtime context attaches system credential audit metadata.
4. Executor chooses purpose by operation/executor.
5. `DialSSHForNodePurpose` builds auth from preloaded managed key, inline private key, or password.
6. Managed key scope/purpose validation.
7. Host-key resolution, SSH dial, command execution.
8. Runtime credential audit for failures/success when context exists.

#### Restic executor config resolution

1. Task is loaded via GORM; `Task.ExecutorConfig` is decrypted by `AfterFind`.
2. Restic code unmarshals JSON into `ResticConfig` or extracts `repository_password`.
3. Password is converted into a shell-escaped `RESTIC_PASSWORD=...` prefix.
4. Remote restic command is executed over SSH.

#### Import/export control order

1. Config import/export routes rely on secured API group and P3 grant/step-up controls.
2. Export without secrets sanitizes fields.
3. Export with `include_secrets=true` intentionally reads decrypted model fields and emits plaintext secrets.
4. Import accepts secret-bearing fields and writes them through GORM `BeforeSave` encryption hooks.

### Safe Seam Candidates

| Seam | Why it is a candidate | Constraints |
|---|---|---|
| `sshutil.ResolveKeyContentForPurpose` | Central shared key lookup for handlers/utilities. | Must preserve managed-key scope validation, DB-load fallback, `LastUsedAt`, and existing error shapes. |
| `sshutil.BuildSSHAuthWithKeyForPurpose` / `BuildSSHAuthForPurpose` | Central auth-method construction for password/private-key paths outside executors. | Broker API must not leak raw key/password; host-key and purpose behavior must remain unchanged. |
| `executor.DialSSHForNodePurpose` | Central runtime executor SSH dial with audit stages. | Must address executor/shared asymmetry for `SSHKeyID` non-preload cases or keep strict preload invariant. |
| `executor.resolveNodePrivateKeyForPurpose` | Runtime private-key resolution branch. | Must preserve fail-closed managed-key errors and purpose/scope checks. |
| `RsyncExecutor.Run` temp key file creation | Explicit local private-key materialization for external `rsync`. | Broker foundation could isolate ephemeral materialization/cleanup; must keep chmod `0600` and deletion guarantees. |
| Restic password helpers (`buildResticEnvPrefix`, `buildDiffEnvPrefix`, retention/index/verifier env builders) | Repeated conversion of repository password into command environment prefixes. | Must avoid logging/output leakage; remote process environment exposure remains unless architecture changes. |
| `config_handler` sensitive import/export branches | Clear plaintext secret ingress/egress boundary. | P3 grant semantics intentionally allow sensitive export; P4 broker must define export/fallback/provider-reference semantics. |
| `node_handler` and `ssh_key_handler` create/update paths | Main secret write ingress for node inline credentials and managed SSH keys. | Must preserve GORM encryption hooks, validation, sanitized responses, and public-key derivation. |
| Terminal/file/docker/log/probe/migration direct `sshutil` callers | Operation-local high-risk credential consumers with purpose-scoped auth. | Controls differ by route; avoid broadening access or skipping operation-specific RBAC/ownership/step-up/grants. |
| `credentialaudit` runtime context and handler audit writers | Existing evidence channel for credential use/attempts. | Audit metadata must remain sanitizer-compatible and must not include secrets, command output, executor config, paths where forbidden, or terminal streams. |

### Tests Likely Affected

Backend tests most likely to need updates when introducing a broker seam:

- `backend/internal/sshutil` tests for auth building, managed key scope/purpose validation, disabled/expired/wrong-purpose/wrong-node/wrong-tag key rejection, DB-load fallback, and `LastUsedAt` behavior.
- `backend/internal/task/executor` tests for `DialSSHForNodePurpose`, private-key resolver behavior, managed-key preload requirements, password handling, rsync temp-key cleanup, and runtime credential audit stages.
- Task manager/runner tests around `Preload("Node.SSHKey")`, manual trigger, restore trigger, batch trigger, runtime audit context, and command/restic/rclone executor dispatch.
- Handler tests for node create/update/test, SSH key CRUD/test/export, config import/export with and without secrets, terminal open, file browser, snapshot restore/diff, batch command creation, docker volume discovery, node logs, probe, and node migration preflight.
- Grant-related tests from P3 baseline for terminal, config import/export, snapshot restore, task restore, manual trigger, batch trigger, and batch command, because broker integration must remain additive rather than replacing grants.
- Snapshot/indexer/anomaly tests for `Node.SSHKey` preloading and restic repository password handling.
- Audit tests ensuring no raw private keys, node passwords, repository passwords, executor config, command output, terminal streams, endpoint/proxy values, or imported/exported payloads are written into credential audit metadata.

Frontend tests potentially affected only if broker semantics surface through APIs/UI:

- Config import/export flows and storage-safety tests.
- Terminal/restore/grant prompt tests where step-up proof and grant state must remain non-persistent.
- API client type mapping tests if secret/provider-reference DTOs are introduced.

### Risks

1. Executor/shared resolver asymmetry: shared `sshutil` DB-loads `SSHKeyID`, but executor resolver fails unless `Node.SSHKey` is preloaded. New broker code could accidentally mask or worsen this invariant.
2. `snapshot.GetIndexStatus` appears to call `ResticExecutor.ListSnapshots` after `db.First(&task, taskID)` without preloading `Node`/`Node.SSHKey`; this is a likely credential/path-sensitive edge.
3. Rsync remote backup currently writes private key material to a local temp file for external `rsync`; cleanup/chmod failures can increase credential exposure.
4. Restic repository password is placed into remote shell command environment prefixes. Shell escaping helps command safety but does not eliminate process/environment exposure on the remote host.
5. Sensitive config export intentionally emits plaintext secrets when `include_secrets=true`; a broker foundation must explicitly decide export semantics for provider-backed or leased credentials.
6. Config import accepts secret-bearing payloads and stores through hooks; provider-reference imports/fallbacks could create partial or ambiguous credential state unless modeled carefully.
7. Batch command operation uses `batch_command` purpose/grant at creation, while runtime command executor uses `task_command`; broker/audit designs must account for this two-stage purpose split.
8. Snapshot restore/diff/indexing surfaces combine task-scoped grants/audits with separate SSH/restic credential use; broker integration must not assume one audit event covers all credential materialization.
9. Audit metadata safety is critical: several paths handle command text/output, terminal streams, file paths, restic output, Docker output, and diagnostic output near credential events.
10. GORM `BeforeSave` mutates in-memory secret fields to encrypted values. Code that reuses the same struct after save can observe encrypted text unless it reloads or preserves plaintext separately.
11. Inline node credentials have no independent scope row; moving to broker/provider references must preserve current operation-layer controls and avoid granting broader cross-node access.
12. Background/system users (`system` runtime audit, scheduled probes, retention, integrity, anomaly checks) need non-interactive credential access semantics distinct from user-initiated JIT grants.

## External References

None. This was an internal codebase and archived-planning research request.

## Related Specs

- `.trellis/spec/backend/quality-guidelines.md` — referenced by archived P3 grant semantics as the credential grant safety contract: row-backed, additive controls, exact fail-closed semantics, safe audit metadata.
- `.trellis/tasks/archive/2026-05/05-21-plan-p3-p4-credential-security-hardening/prd.md` — P3/P4 roadmap and P4 credential broker/external secret provider direction.
- `.trellis/tasks/archive/2026-05/05-22-select-next-p3-p4-hardening-slice-2/prd.md` — prior P3 slice selection and deferred P4 architecture work.
- `.trellis/tasks/archive/2026-05/05-22-p3-grant-semantics/prd.md` — owned-resource and multi-resource JIT grant semantics currently forming the P3 baseline.
- `.trellis/tasks/archive/2026-05/05-22-p3-comprehensive-security-review/research/p3-code-gap-audit.md` — review baseline for current grant coverage and residual gaps.

## Caveats / Not Found

- No external documentation search was performed because the request was codebase/planning research.
- This report focuses on credential resolution and materialization seams; it does not propose or implement broker design changes.
- Some large archived review content was summarized from prior reads rather than exhaustively reproduced here.
- Additional credential-adjacent encrypted fields exist outside the requested core (for example integration endpoints/secrets/proxy URLs and app credential config); they are included as model-hook constraints but were not traced through every notification/app-credential consumer.
