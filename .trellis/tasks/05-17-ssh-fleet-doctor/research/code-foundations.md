# Research: SSH Fleet Doctor code foundations

- **Query**: Research the existing code foundations and implementation constraints for task `.trellis/tasks/05-17-ssh-fleet-doctor` (SSH Fleet Doctor MVP), focusing on Node/SSHKey models, `sshutil`, probes, task executors, sudo behavior, node APIs/UI, security boundaries, tests, and concrete gaps for allowlisted diagnostic checks.
- **Scope**: internal
- **Date**: 2026-05-17

## Findings

### Files Found

| File Path | Description |
|---|---|
| `backend/internal/model/models.go` | Core `Node`, `SSHKey`, `NodeMetricSample`, and `NodeOwner` models; includes sensitive-field sanitization/encryption hooks. |
| `backend/internal/sshutil/ssh_auth.go` | SSH auth construction, key resolution, known_hosts callback, auto-accept behavior, and context-aware SSH dial helper. |
| `backend/internal/sshutil/probe.go` | Existing SSH connection + root disk probe (`ProbeNode`, `ParseDiskProbe`). |
| `backend/internal/sshutil/private_key.go` | Private key normalization, type detection, and encrypted/passphrase key validation. |
| `backend/internal/probe/prober.go` | Periodic node prober that updates node status/latency/disk fields and writes metric samples. |
| `backend/internal/task/executor/ssh_connect.go` | Shared SSH dial and remote command helpers used by task executors. |
| `backend/internal/task/executor/sudo.go` | Central sudo behavior (`NeedsSudo`, sudo command wrappers). |
| `backend/internal/task/executor/executor.go` | Executor factory, rsync behavior, shell escaping, local/remote target readiness and disk checks. |
| `backend/internal/task/executor/command_executor.go` | Existing arbitrary remote command executor used for command tasks. |
| `backend/internal/task/executor/restic_executor.go` | Restic executor with remote tool detection and sudo-aware command prefixing. |
| `backend/internal/task/executor/rclone_executor.go` | Rclone executor with remote tool detection and sudo-aware sync command builder. |
| `backend/internal/api/handlers/node_handler.go` | Node CRUD, disabled node exec endpoint, current test-connection endpoint, host/backup-dir validation. |
| `backend/internal/api/handlers/node_migrate_preflight_handler.go` | Closest structured diagnostic/preflight flow; checks SSH, tools, paths, disk, running tasks, local backup data. |
| `backend/internal/api/handlers/ssh_key_handler.go` | SSH key sanitizing response type and key test-connection endpoint. |
| `backend/internal/api/handlers/batch_handler.go` | Batch command flow that accepts user commands with safety-net blacklist; useful security contrast for Doctor. |
| `backend/internal/api/handlers/file_handler.go` | Read-only SFTP file browser with path allowlist and symlink-escape defense. |
| `backend/internal/nodelogs/fetcher.go` | Server-generated allowlisted remote script pattern with shell quoting for log collection. |
| `backend/internal/api/router.go` | Existing `/api/v1` route registration and node/SSH key route middleware patterns. |
| `backend/internal/middleware/rbac.go` | Permission matrix; `nodes:test` exists for admin/operator but not viewer. |
| `backend/internal/middleware/ownership.go` | Node ownership middleware; admin/viewer bypass, operator requires `node_owners` row. |
| `backend/internal/api/handlers/response.go` | Unified API response envelope and response helper functions. |
| `web/src/types/domain.ts` | Frontend `NodeRecord`, `NewNodeInput`, SSH key and node auth domain types. |
| `web/src/lib/api/nodes-api.ts` | Frontend node API mapper; current `testNodeConnection` client. |
| `web/src/components/node-editor-dialog.tsx` | Node editor with auth method, SSH key/password, `backupDir`, `basePath`, and sudo toggle UI. |
| `web/src/pages/nodes-page.tsx` | Node list page composition and action wiring. |
| `web/src/pages/nodes-page.table.tsx` | Desktop node table action buttons including test-connection entry point. |
| `web/src/pages/nodes-page.grid.tsx` | Mobile/card node action surface including test, logs, terminal, file, edit, backup, migrate actions. |
| `web/src/pages/nodes-page.dialogs.tsx` | Node-context dialogs for editor, terminal, file browser, batch commands, migration wizard. |
| `web/src/pages/nodes-detail-page.tsx` | Node detail tab shell; current tabs do not include Doctor. |
| `web/src/features/nodes-detail/profile-tab.tsx` | Node profile details tab showing basic node and probe/backup timestamps. |
| `backend/internal/api/handlers/node_handler_test.go` | Existing node handler tests for disabled exec, batch delete, migrate/preflight errors, disk probe parsing. |
| `backend/internal/sshutil/ssh_auth_test.go` | Known_hosts behavior and disk probe parser tests. |
| `backend/internal/sshutil/private_key_test.go` | Private key normalization/decryption/invalid ciphertext tests. |
| `backend/internal/task/executor/sudo_test.go` | Sudo helper tests. |
| `backend/internal/task/executor/ssh_connect_test.go` | SSH user fallback and auth method validation tests. |
| `backend/internal/task/executor/executor_security_test.go` | Shell escaping tests against injection payloads. |
| `backend/internal/probe/prober_test.go` | Metrics parsing, maintenance-window, and metrics-retention tests. |
| `web/src/pages/nodes-page.test.tsx` | Nodes page UI tests including test-connection toast behavior. |
| `web/src/pages/nodes-detail-page.test.tsx` | Node detail tab state tests. |
| `web/src/pages/__tests__/nodes-page.a11y.test.tsx` | Nodes page axe smoke test. |
| `.trellis/spec/backend/error-handling.md` | Standard response/error handling and sensitive-error constraints. |
| `.trellis/spec/backend/quality-guidelines.md` | Backend quality, route middleware, sanitization, and test requirements. |
| `.trellis/spec/backend/logging-guidelines.md` | Logging rules and secret redaction constraints. |
| `.trellis/spec/frontend/component-guidelines.md` | Frontend component/API normalization/UI primitive conventions. |
| `.trellis/spec/frontend/a11y-guidelines.md` | Frontend accessibility baseline for icons, dialogs, forms, and tabs. |

### Code Patterns

#### Node / SSHKey models and sensitive data boundaries

- `SSHKey.PrivateKey` is explicitly hidden from JSON (`json:"-"`) and the comment states it should never be serialized directly; handlers use response structs instead (`backend/internal/model/models.go:27-35`).
- `Node.Sanitized()` strips `Password`, `PrivateKey`, and nested `SSHKey.PrivateKey` before API responses (`backend/internal/model/models.go:41-52`).
- `Node` already stores most Doctor inputs/status context: `Host`, `Port`, `Username`, `AuthType`, `Password`, `PrivateKey`, `SSHKeyID`, `BasePath`, `BackupDir`, `UseSudo`, `ConnectionLatency`, disk fields, probe timestamps, maintenance windows, and archive state (`backend/internal/model/models.go:54-87`).
- `NodeMetricSample` records `LatencyMs`, disk GB fields, and `ProbeOK`, which can support probe-status context (`backend/internal/model/models.go:399-413`).
- `NodeOwner` is the operator ownership join table for node-level authorization (`backend/internal/model/models.go:465-471`).

#### SSH auth, known_hosts, and dialing

- `ResolveKeyContent` checks preloaded `node.SSHKey`, `node.SSHKeyID` lookup, then inline `node.PrivateKey`; returned key-source labels include `ssh_key_id=<id>` or `node.private_key` (`backend/internal/sshutil/ssh_auth.go:26-54`).
- `BuildSSHAuth` supports only `password` and `key`, validates missing password/key, prepares private keys via `ValidateAndPreparePrivateKey`, and returns user-facing Chinese validation errors (`backend/internal/sshutil/ssh_auth.go:56-88`).
- `BuildSSHAuthWithKey` mirrors `BuildSSHAuth` and returns prepared key material for callers that update key usage (`backend/internal/sshutil/ssh_auth.go:90-122`).
- `ResolveSSHHostKeyCallback` defaults strict host key checking to true, ensures `SSH_KNOWN_HOSTS_PATH`, auto-accepts unknown hosts by default when `SSH_AUTO_ACCEPT_NEW_HOSTS` is true/default, and rejects changed keys via knownhosts errors (`backend/internal/sshutil/ssh_auth.go:124-174`).
- `DialSSH` uses context-aware TCP dialing and wraps TCP vs SSH handshake failures as `SSH 连接失败` and `SSH 握手失败` (`backend/internal/sshutil/ssh_auth.go:176-198`).
- Known-host append is serialized with a package mutex and skips duplicate host/key entries (`backend/internal/sshutil/ssh_auth.go:212-260`).

#### Existing probe behavior

- `sshutil.ProbeNode` builds auth, resolves known_hosts callback, SSH dials with a 5-second timeout, measures latency, then runs a root filesystem disk command: `df -BG / | awk 'NR==2 {print $2" "$3}'` (`backend/internal/sshutil/probe.go:22-67`).
- `ParseDiskProbe` parses `df` output as `total used`, rejects invalid totals/negative/over-total values, and returns `(used, total, ok)` (`backend/internal/sshutil/probe.go:69-91`).
- The periodic `Prober` loads non-archived nodes with `Preload("SSHKey")`, runs a bounded worker pool, and skips maintenance windows (`backend/internal/probe/prober.go:97-140`).
- On probe failure, `Prober` sets node offline, zeroes latency, updates `last_probe_at`, increments `consecutive_failures`, and may raise node-probe alerts after threshold (`backend/internal/probe/prober.go:142-163`).
- On probe success, `Prober` sets node online, updates latency/disk/probe/seen fields, resets failures, resolves node alerts, and asynchronously collects metrics (`backend/internal/probe/prober.go:166-198`).
- `collectMetrics` runs a Linux-focused, server-generated command using `/proc/stat`, `free`, `df`, and `/proc/loadavg` (`backend/internal/probe/prober.go:249-289`).

#### Task executors and remote commands

- The executor factory supports `rsync`, `command`, `restic`, and `rclone`; unknown/local executors are disabled (`backend/internal/task/executor/executor.go:45-90`).
- `DialSSHForNode` centralizes executor SSH connections and defaults empty username to `root` with a warning (`backend/internal/task/executor/ssh_connect.go:15-40`).
- `RunSSHCommandOutput` executes a caller-provided command string over SSH and returns combined stdout/stderr with context cancellation support (`backend/internal/task/executor/ssh_connect.go:88-111`).
- `CommandExecutor` executes `task.Command` over SSH; when sudo is needed it wraps the user command with `sudo sh -c` and logs `执行命令: <command>` (`backend/internal/task/executor/command_executor.go:13-121`).
- `RsyncExecutor` rejects password auth for remote rsync, prepares a temporary key file, configures local OpenSSH known_hosts options, uses `--rsync-path sudo rsync` when `NeedsSudo`, and inserts `--` before source/target to terminate option parsing (`backend/internal/task/executor/executor.go:98-200`).
- `ShellEscape` single-quotes shell parameters and escapes embedded single quotes (`backend/internal/task/executor/executor.go:361-365`).
- `EnsureLocalTargetReady` creates/checks local target directories, probes writability with a temp file, and optionally enforces `RSYNC_MIN_FREE_GB` (`backend/internal/task/executor/executor.go:449-497`).
- `EnsureRemoteTargetReady` SSHes to the node, runs `test -d || mkdir -p`, uses sudo if configured, and optionally checks free GB (`backend/internal/task/executor/executor.go:544-599`). This helper can create remote directories, so it is not read-only as-is.
- Restic/rclone executors both perform remote tool checks via `which <bin> 2>/dev/null || command -v <bin> 2>/dev/null` before running (`backend/internal/task/executor/restic_executor.go:59-64`, `backend/internal/task/executor/rclone_executor.go:64-69`).
- Restic builds a sudo-aware command prefix (`sudo env ... restic` when needed) (`backend/internal/task/executor/restic_executor.go:66-70`).
- Rclone builds a sudo-aware sync command by prefixing `sudo ` to the binary when needed and shell-escaping source/destination (`backend/internal/task/executor/rclone_executor.go:202-218`).

#### Sudo behavior

- `NeedsSudo` returns true only when `node.UseSudo` is true and the trimmed username is neither empty nor `root` (`backend/internal/task/executor/sudo.go:9-17`).
- `WrapWithSudo` is intended for system-generated single commands; `WrapWithSudoShell` is intended for user-authored compound commands and uses `sh -c` with shell escaping (`backend/internal/task/executor/sudo.go:19-28`).
- The frontend node editor only shows the sudo toggle for non-root, non-empty usernames and clears sudo when username becomes root/empty (`web/src/components/node-editor-dialog.tsx:252-276`).

#### Node APIs, route middleware, and response envelope

- Existing node routes are registered under authenticated `/api/v1` with RBAC and ownership middleware for node-specific routes; `POST /nodes/:id/test-connection` uses `nodes:test` and `OwnershipNodeCheck` (`backend/internal/api/router.go:118-150`).
- Current node list/get responses preload `SSHKey` and return sanitized nodes (`backend/internal/api/handlers/node_handler.go:78-126`).
- `NodeHandler.Exec` is disabled and returns a 403 response with `XR-SEC-EXEC-DISABLED` (`backend/internal/api/handlers/node_handler.go:635-641`).
- Current `TestConnection` builds SSH auth, resolves known_hosts callback, dials via `ssh.Dial`, runs the same disk probe, updates node status/latency/disk/last-seen fields, updates `SSHKey.LastUsedAt`, and returns `{ ok, message, latency_ms, disk_used_gb, disk_total_gb, probe_at }` (`backend/internal/api/handlers/node_handler.go:654-786`). Failures are returned as a generic message: `SSH 连接失败，请检查主机地址、端口、认证配置` (`backend/internal/api/handlers/node_handler.go:666-730`).
- Node host validation rejects blank host, localhost/localhost.localdomain, loopback IPs, invalid/too-long hostnames, and invalid ports (`backend/internal/api/handlers/node_handler.go:843-870`).
- `BasePath` intentionally has no backend default `/` on create to avoid opening the whole machine in file browser allowlists (`backend/internal/api/handlers/node_handler.go:178-179`).
- `response.go` defines the standard envelope and helpers (`Response{Code, Message, Data}`, `respondOK`, `respondBadRequest`, `respondForbidden`, `respondNotFound`, `respondBadGateway`, `respondInternalError`) (`backend/internal/api/handlers/response.go:11-96`).
- RBAC grants `nodes:test` to admin and operator, not viewer (`backend/internal/middleware/rbac.go:9-78`).
- `OwnershipNodeCheck` lets admin/viewer pass, requires operator ownership, and fail-closes unknown roles with 403 (`backend/internal/middleware/ownership.go:20-60`).

#### Existing structured preflight / diagnostic-adjacent flows

- `MigratePreflight` defines `PreflightCheckItem{Name, Status, Message}` with statuses `pass / fail / warn / skip` (`backend/internal/api/handlers/node_migrate_preflight_handler.go:21-31`).
- Migration preflight checks SSH connectivity via `sshutil.ProbeNode`; on failure it appends a `ssh` fail check and sets `CanProceed=false` (`backend/internal/api/handlers/node_migrate_preflight_handler.go:198-216`).
- It collects executor types from affected tasks and checks remote tools using `which`/`command -v`; if SSH failed it marks tool checks as skip (`backend/internal/api/handlers/node_migrate_preflight_handler.go:151-157`, `backend/internal/api/handlers/node_migrate_preflight_handler.go:218-253`).
- It checks source paths via `test -d <path> && echo EXISTS || echo MISSING` with `executor.ShellEscape` (`backend/internal/api/handlers/node_migrate_preflight_handler.go:255-279`).
- It performs a disk-space comparison using probe disk totals and source node used GB (`backend/internal/api/handlers/node_migrate_preflight_handler.go:283-301`).
- It also checks running tasks and local backup data migratability (`backend/internal/api/handlers/node_migrate_preflight_handler.go:303-356`).

#### Security boundary examples relevant to Doctor

- Batch commands accept arbitrary command text but validate only required/length/dangerous blacklist before creating `command` tasks (`backend/internal/api/handlers/batch_handler.go:45-70`, `backend/internal/api/handlers/batch_handler.go:102-147`). This is an existing arbitrary-command path, not an allowlisted diagnostic contract.
- File browser uses SFTP and `RealPath` to verify requested paths stay under `Node.BasePath` or task `RsyncSource`; it avoids leaking resolved real paths on rejection (`backend/internal/api/handlers/file_handler.go:311-377`).
- Node logs use a server-generated bash script (`buildScript`) based on configured log sources/paths, not an arbitrary user command string; it quotes path/cursor values with `shellQuote` (`backend/internal/nodelogs/fetcher.go:100-140`).
- SSH key API responses derive public key/fingerprint metadata and never return private key material (`backend/internal/api/handlers/ssh_key_handler.go:43-74`).
- SSH key test-connection returns per-node `Error` strings from auth/dial errors (`backend/internal/api/handlers/ssh_key_handler.go:351-413`), so any Doctor endpoint needs its own sanitization boundary if it exposes diagnostic evidence.

#### Frontend node context surfaces

- `NodeRecord` contains node host/port/username/auth type/key ID/status/disk/probe/backup/sudo fields needed to place Doctor results in node context (`web/src/types/domain.ts:67-94`).
- `NewNodeInput` includes password/inline key, `backupDir`, and `useSudo`, but frontend node responses never include password/private key (`web/src/types/domain.ts:378-396`).
- `nodes-api.ts` maps backend snake_case node fields to frontend `NodeRecord`, including `backup_dir`, `use_sudo`, disk, and probe fields; current `testNodeConnection` calls `POST /nodes/:id/test-connection` (`web/src/lib/api/nodes-api.ts:4-40`, `web/src/lib/api/nodes-api.ts:90-176`).
- Nodes page state already wires `onTestNode` to call `testNodeConnection` and show success/error toasts (`web/src/pages/nodes-page.state.ts:360-374`).
- Desktop table and card/mobile grid already expose node action buttons for test connection, logs, terminal, file browser, edit, migration, delete, manual/emergency backup (`web/src/pages/nodes-page.table.tsx:169-249`, `web/src/pages/nodes-page.grid.tsx:137-207`, `web/src/pages/nodes-page.grid.tsx:302-395`).
- `NodesPageDialogs` is the existing place for node-context dialogs (editor, terminal, file browser/docker volumes, batch commands/results, migration wizard) (`web/src/pages/nodes-page.dialogs.tsx:90-235`).
- Node detail page has tabs `overview`, `metrics`, `tasks`, `alerts`, `profile`, `log-config`, `anomaly`; no Doctor tab is registered (`web/src/pages/nodes-detail-page.tsx:12-43`).
- Manual tab UI uses `role="tablist"`, `role="tab"`, and `role="tabpanel"` (`web/src/pages/nodes-detail-page.tsx:63-94`). The a11y spec requires full tab semantics including `aria-controls`, `aria-selected`, and `tabIndex` when wiring roles manually.

#### Existing tests

- `node_handler_test.go` pins the disabled node exec response envelope and code (`backend/internal/api/handlers/node_handler_test.go:31-73`).
- `node_handler_test.go` includes migrate/preflight DB-error tests and disk probe parser tests (`backend/internal/api/handlers/node_handler_test.go:180-260`).
- `ssh_auth_test.go` covers unknown known_hosts auto-accept default, explicit rejection, mismatch rejection, serialized concurrent writes, duplicate skipping, and disk parsing (`backend/internal/sshutil/ssh_auth_test.go:30-168`).
- `private_key_test.go` covers private key extraction from mixed text, decrypting encrypted ciphertext, rejecting invalid ciphertext, and escaped key input (`backend/internal/sshutil/private_key_test.go:30-95`).
- `sudo_test.go` covers `NeedsSudo`, `WrapWithSudo`, and `WrapWithSudoShell` including root/empty username and quoting cases (`backend/internal/task/executor/sudo_test.go:9-121`).
- `ssh_connect_test.go` covers SSH username default/trim and auth-method rejection for empty/missing auth settings (`backend/internal/task/executor/ssh_connect_test.go:8-54`).
- `executor_security_test.go` validates `ShellEscape` against command-substitution, semicolon, pipe, `&&`, newline, variable, glob, Unicode, and empty inputs using a real shell (`backend/internal/task/executor/executor_security_test.go:9-75`).
- `prober_test.go` covers metric output parsing, maintenance windows, and metric retention cleanup (`backend/internal/probe/prober_test.go:33-148`).
- `nodes-page.test.tsx` covers view switching, logs link semantics, card semantics, and test-connection failure toast wiring (`web/src/pages/nodes-page.test.tsx:174-260` and later test cases in the same file).
- `nodes-detail-page.test.tsx` covers default/query/clicked tab selection (`web/src/pages/nodes-detail-page.test.tsx:38-56`).
- `nodes-page.a11y.test.tsx` runs an axe smoke test for initial Nodes page render (`web/src/pages/__tests__/nodes-page.a11y.test.tsx:132-153`).

### External References

- None. This was an internal codebase/spec research request only.

### Related Specs

- `.trellis/tasks/05-17-ssh-fleet-doctor/prd.md` — Doctor MVP goal, allowlisted/no arbitrary shell requirement, structured result fields (`check`, `status`, `evidence`, `suggestion`), node-context frontend display, diagnose-only scope, and required backend/frontend tests.
- `.trellis/spec/backend/error-handling.md` — API handlers must use the unified response envelope, map validation/auth/not-found/internal errors consistently, and avoid leaking raw SQL/encryption/SSH private key/token/stack-like details (`.trellis/spec/backend/error-handling.md:9-74`).
- `.trellis/spec/backend/quality-guidelines.md` — forbids ad hoc JSON response shapes, unsanitized node/SSH key responses, and routes without Auth/RBAC/ownership; requires explicit tests for SSH auth/path validation/RBAC/ownership-sensitive code (`.trellis/spec/backend/quality-guidelines.md:17-70`).
- `.trellis/spec/backend/logging-guidelines.md` — new backend logs should use `logger.Module`, include stable IDs, and must not log passwords/private keys/tokens/secrets/raw endpoints or full command output that may contain credentials (`.trellis/spec/backend/logging-guidelines.md:37-79`).
- `.trellis/spec/frontend/component-guidelines.md` — API normalization belongs in API mappers; reuse UI primitives; keep component exports focused (`.trellis/spec/frontend/component-guidelines.md:20-34`).
- `.trellis/spec/frontend/a11y-guidelines.md` — icon buttons need accessible names; decorative icons need `aria-hidden`; dialogs need titles; manual tabs need complete tab semantics (`.trellis/spec/frontend/a11y-guidelines.md:20-49`).

## Caveats / Not Found

- The current Trellis task command reported `.trellis/tasks/05-17-trust-demo-feedback`; this research file is written under the user-specified task `.trellis/tasks/05-17-ssh-fleet-doctor`.
- No existing SSH Fleet Doctor package, route, frontend API method, result type, UI component, dialog, or node-detail tab was found.
- No existing backend response shape matches the requested Doctor contract `check/status/evidence/suggestion`; migration preflight has `name/status/message` only.
- Current `NodeHandler.TestConnection` updates node state and returns a generic success/failure result; it does not classify connection failure vs auth failure vs known_hosts conflict vs sudo/tool/path/disk/probe categories.
- No explicit allowlisted diagnostic runner was found. Existing `RunSSHCommandOutput` and `CommandExecutor` execute caller-provided command strings and should be treated as low-level implementation utilities, not as a user-facing Doctor input contract.
- No explicit non-interactive sudo diagnostic such as `sudo -n true` was found.
- `EnsureRemoteTargetReady` can create remote directories with `mkdir -p`; it is not diagnose-only/read-only as-is.
- Existing restic/rclone tool checks are hard-coded in their executors and migration preflight; no shared typed tool-diagnostic contract was found.
- No tests were found for Doctor allowlist enforcement, rejected arbitrary diagnostic input, sanitized diagnostic evidence, or categorized diagnostic outcomes.
