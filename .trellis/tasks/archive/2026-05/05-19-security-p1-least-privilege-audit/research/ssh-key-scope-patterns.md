# Research: SSH key least-privilege scope patterns

- **Query**: Research practical SSH key least-privilege metadata patterns for a self-hosted ops panel: disabled/expiry, allowed purposes, allowed node IDs/tags, permissive migration defaults, and enforcement at use boundaries. Compare with PAM/bastion patterns from JumpServer/Teleport/Boundary/Warpgate where useful, but map to Xirang's layered-hardening scope.
- **Scope**: mixed
- **Date**: 2026-05-19

## Findings

### Files Found

| File Path | Description |
|---|---|
| `.trellis/tasks/05-19-security-p1-least-privilege-audit/prd.md` | Active task requirements: add SSHKey disabled/expiry/purpose/node-or-tag scope, enforce at SSH use boundaries, preserve existing keys with permissive defaults, add credential-use audit and risk-summary signals. |
| `backend/internal/model/models.go` | Current `SSHKey` and `Node` models, sensitive-field sanitization, and GORM encryption hooks. |
| `backend/internal/database/migrations/sqlite/000001_baseline.up.sql` | Baseline SQLite schema for `ssh_keys` and `nodes.ssh_key_id`. |
| `backend/internal/database/migrations/postgres/000001_baseline.up.sql` | Baseline PostgreSQL schema for `ssh_keys` and `nodes.ssh_key_id`. |
| `backend/internal/api/handlers/ssh_key_handler.go` | SSH key REST API, response DTO, role/ownership-derived visibility query, create/update/delete/test/export handlers. |
| `backend/internal/api/handlers/node_handler.go` | Node create/update SSH key reference validation and node connection-test boundary that updates `ssh_keys.last_used_at`. |
| `backend/internal/sshutil/ssh_auth.go` | Shared SSH auth resolver for many HTTP/probe/log boundaries. |
| `backend/internal/task/executor/ssh_connect.go` | Task/executor SSH dial helper for backup, restore, command, hook, snapshot, retention, drill, and migration paths. |
| `backend/internal/task/executor/executor.go` | Executor-local `resolveNodePrivateKey` duplicate resolver used by rsync/local command paths. |
| `backend/internal/api/router.go` | Route-level RBAC and ownership middleware placement for nodes, tasks, SSH keys, file browser, Docker volumes, migrations, snapshots, and terminal WebSocket. |
| `backend/internal/middleware/rbac.go` | Role permission matrix: `admin` has `ssh_keys:write`; `operator`/`viewer` have read-only SSH key access. |
| `backend/internal/middleware/ownership.go` | Node/task object-level ownership checks and `OwnedNodeIDs` helper used by non-admin SSH key visibility filtering. |
| `backend/internal/api/handlers/settings_handler.go` | Current advisory security risk summary implementation and stable risk-code pattern. |
| `backend/internal/api/handlers/config_handler.go` | Config export/import paths for nodes and SSH keys, including optional sensitive export and SSH key field import. |
| `backend/internal/slo/compute.go` | Existing comma-separated node tag parsing and any-of tag matching pattern. |
| `backend/internal/alerting/silence.go` | Existing comma-separated node tag parsing and any-of tag matching pattern for silences. |
| `backend/internal/nodelogs/ssh_runner.go` | Node log SSH execution boundary using `sshutil.BuildSSHAuth`. |
| `backend/internal/api/handlers/docker_handler.go` | Docker volume discovery SSH boundary using `sshutil.BuildSSHAuth`. |
| `backend/internal/api/handlers/file_handler.go` | SFTP file browser boundaries using `dialSFTP` and `sshutil.BuildSSHAuth`. |
| `backend/internal/api/handlers/terminal_handler.go` | WebSocket terminal SSH boundary using in-protocol auth and `sshutil.BuildSSHAuth`. |
| `web/src/types/domain.ts` | Current frontend `SSHKeyRecord` and `NewSSHKeyInput` shape. |
| `web/src/lib/api/ssh-keys-api.ts` | Current frontend SSH key API mapper and request payload shape. |
| `.trellis/spec/backend/quality-guidelines.md` | Backend security-sensitive testing/RBAC/risk-summary contracts. |
| `.trellis/spec/backend/database-guidelines.md` | Migration, naming, sanitization, and sensitive-field handling contracts. |

### Code Patterns

#### Current Xirang SSH key data shape

- `model.SSHKey` currently stores identity and material fields only: `Name`, `Username`, `KeyType`, `PrivateKey`, `Fingerprint`, `LastUsedAt`, timestamps; no disabled, expiry, purpose, node-scope, or tag-scope metadata is present in the current model (`backend/internal/model/models.go:27-39`).
- `model.Node` stores `SSHKeyID`, relation `SSHKey`, comma-string `Tags`, `ExpiryDate`, `Archived`, and `UseSudo`; node expiry/archive/sudo already exist as node metadata rather than key metadata (`backend/internal/model/models.go:54-87`).
- `Node.Sanitized()` clears node `Password`, inline `PrivateKey`, and related `SSHKey.PrivateKey` before API responses (`backend/internal/model/models.go:41-52`).
- `SSHKey.BeforeSave` / `SSHKey.AfterFind` encrypt and decrypt `PrivateKey` through shared secure hooks, so new handlers should keep secret handling at model/service boundaries rather than manually processing private keys (`backend/internal/model/models.go:573-595`; `.trellis/spec/backend/database-guidelines.md:14-18`, `80-86`).
- Baseline migrations define `ssh_keys` with `id`, `name`, `username`, `key_type`, `private_key`, `fingerprint`, `last_used_at`, timestamps; `nodes` has `ssh_key_id` and an index (`backend/internal/database/migrations/sqlite/000001_baseline.up.sql:15-26`, `28-50`; `backend/internal/database/migrations/postgres/000001_baseline.up.sql:15-26`, `28-50`).

#### Current SSH key API and visibility scoping

- SSH key API responses use `sshKeyResponseItem`, which includes derived `public_key` and omits private key material (`backend/internal/api/handlers/ssh_key_handler.go:45-75`).
- Non-admin visibility is already scoped at list/get/export time, but it is visibility filtering, not credential-use enforcement: admin sees all keys; viewer sees keys bound to any node; operator sees keys bound to owned nodes (`backend/internal/api/handlers/ssh_key_handler.go:84-107`).
- `List`, `Get`, and `Export` route through `visibleSSHKeysQuery` (`backend/internal/api/handlers/ssh_key_handler.go:133-149`, `163-182`, `540-575`).
- Create/update/delete/test-connection handlers operate by raw key ID lookup, but the router protects write/test routes with `ssh_keys:write`; only admin currently has that permission (`backend/internal/api/router.go:221-229`; `backend/internal/middleware/rbac.go:9-21`, `44-52`, `65-70`).
- `Delete` and `BatchDelete` reject or skip keys currently referenced by nodes (`backend/internal/api/handlers/ssh_key_handler.go:319-339`, `640-699`).
- `TestConnection` builds a temporary node with the selected SSH key and dials requested nodes (`backend/internal/api/handlers/ssh_key_handler.go:356-455`). This is an explicit key-use boundary independent of node's stored `SSHKeyID`.

#### Current node binding and node-level enforcement

- Node create/update validates that a referenced `ssh_key_id` exists, but only for existence (`backend/internal/api/handlers/node_handler.go:135-155`).
- Node create/update clears inline private key when `SSHKeyID` is used, preserving a single credential source for referenced keys (`backend/internal/api/handlers/node_handler.go:212-224`, `365-377`).
- `NodeHandler.TestConnection` preloads `SSHKey`, builds auth with `BuildSSHAuthWithKey`, dials SSH, updates node probe fields, and updates `ssh_keys.last_used_at` on successful referenced-key use (`backend/internal/api/handlers/node_handler.go:667-784`).
- Node routes that access a specific node generally combine RBAC and `OwnershipNodeCheck`, e.g. node get/update/delete/test/doctor/files/docker/emergency-backup (`backend/internal/api/router.go:147-166`).

#### Current SSH auth/use boundaries

- Shared resolver path: `sshutil.ResolveKeyContent` checks preloaded `node.SSHKey.PrivateKey`, then falls back to DB lookup by `node.SSHKeyID`, then inline `node.PrivateKey` (`backend/internal/sshutil/ssh_auth.go:26-54`). `BuildSSHAuth` and `BuildSSHAuthWithKey` call this resolver before parsing private keys (`backend/internal/sshutil/ssh_auth.go:56-122`).
- Executor resolver path: `executor.resolveNodePrivateKey` checks preloaded `node.SSHKey.PrivateKey`, fails if `SSHKeyID` is set but relation is missing, then falls back to inline `node.PrivateKey` (`backend/internal/task/executor/executor.go:514-533`).
- `executor.DialSSHForNode` uses `resolveSSHAuthMethods`, which calls executor-local `resolveNodePrivateKey`; it is the main path for task executors and SSH command helpers (`backend/internal/task/executor/ssh_connect.go:15-40`, `42-74`).
- Task manager/runner preload `Node.SSHKey` before execution (`backend/internal/task/manager.go:242-244`; `backend/internal/task/runner.go:206-208`).
- Other SSH boundaries found by search:
  - probe collection uses `BuildSSHAuth` (`backend/internal/probe/prober.go:99`, `250`).
  - file browser/SFTP uses `dialSFTP` -> `BuildSSHAuth` (`backend/internal/api/handlers/file_handler.go:75-121`, `138-174`, `277-279`).
  - Docker volume discovery uses `BuildSSHAuth` (`backend/internal/api/handlers/docker_handler.go:50-65`, `83-90`).
  - terminal WebSocket preloads key and calls `BuildSSHAuth` inside the WebSocket handler (`backend/internal/api/handlers/terminal_handler.go:215-235`).
  - node logs use `BuildSSHAuth` (`backend/internal/nodelogs/ssh_runner.go:24-39`).
  - snapshot, snapshot diff, integrity checker, retention, hooks, drill, migration preflight, restic/rclone/command executors use `executor.DialSSHForNode` / `RunSSHCommandOutput` (see grep result paths under `backend/internal/task/`, `backend/internal/snapshot/`, `backend/internal/anomaly/`, `backend/internal/api/handlers/node_migrate_preflight_handler.go`).
- Because key material resolution currently has at least two resolver implementations (`sshutil.ResolveKeyContent` and `executor.resolveNodePrivateKey`), enforcement-at-use-boundary research should account for both paths and ad hoc `SSHKeyHandler.TestConnection`.

#### Current route/ownership scope boundaries

- `OwnershipNodeCheck` is object-level and applies to `operator`; admin/viewer bypass the operator ownership check, while unknown roles fail (`backend/internal/middleware/ownership.go:20-59`).
- `OwnershipTaskCheck` joins `tasks` to `node_owners` for task object access (`backend/internal/middleware/ownership.go:71-110`).
- SSH key list/get/export tests confirm non-admin visibility filters: operator sees only owned-node keys; viewer sees in-use keys; unbound or unowned key get returns 404 for operator (`backend/internal/api/handlers/ssh_key_handler_test.go:121-204`).
- `/ws/terminal` is registered outside the secured route group (`backend/internal/api/router.go:358`) and authenticates inside the WebSocket handler; key-scope enforcement for terminal cannot rely only on REST middleware.

#### Current tag representation

- `Node.Tags` is a comma-separated string (`backend/internal/model/models.go:65`).
- Existing backend tag matchers split by comma, trim whitespace, and use any-of matching (`backend/internal/slo/compute.go:34-60`, `63-88`; `backend/internal/alerting/silence.go:62-87`).
- Practical key tag scope can reuse the project’s existing node-tag meaning if stored as an explicit JSON list or another validated list form; substring SQL `LIKE` matching is already present elsewhere for reporting but is weaker than exact split-and-match (`backend/internal/reporting/generator.go:115` from search results).

#### Current security risk summary pattern

- Existing risk summary returns stable string codes and sanitized bounded examples (`.trellis/spec/backend/quality-guidelines.md:224-287`).
- Current codes are `root_ssh_users`, `reused_ssh_keys`, `sudo_enabled_nodes`, and `weak_security_defaults` (`backend/internal/api/handlers/settings_handler.go:198-212`, `215-288`; `.trellis/spec/backend/quality-guidelines.md:254-263`).
- Reused SSH key risk counts `nodes.ssh_key_id` groups with more than one node and sanitizes key-name examples (`backend/internal/api/handlers/settings_handler.go:251-288`).

#### Current config import/export implications

- Config export includes node `ssh_key_id`; when `include_secrets` is true it includes node `password`/`private_key` and SSH key `private_key` (`backend/internal/api/handlers/config_handler.go:99-131`).
- Config import reads SSH key `name`, `username`, `key_type`, `private_key`, `fingerprint`; it does not currently read any key-scope metadata because none exists (`backend/internal/api/handlers/config_handler.go:266-309`).
- Sensitive config export is admin-only and logs an audit warning (`backend/internal/api/handlers/config_handler.go:60-72`).

### Practical Metadata Pattern Synthesis

The following patterns map external PAM/bastion concepts to Xirang's stated layered-hardening scope without introducing full PAM/session recording/approval workflows.

#### Disabled and expiry

- External analogue:
  - Teleport role options use certificate/session TTL (`max_session_ttl`) and expired-certificate disconnection controls; Teleport also chooses the least permissive value when multiple role options conflict.
  - Boundary targets have session duration/connection limits.
  - JumpServer asset authorization includes start date and expiry date for authorization rules.
- Xirang-shaped metadata:
  - `disabled` boolean: hard stop for referenced-key use. Existing-key default can be `false`.
  - `expires_at` nullable timestamp: null means no expiry for existing keys; non-null past value rejects use.
  - Enforcement result should be a sanitized denial reason, not raw host/user/key material, matching existing security-risk summary sanitization contracts.

#### Allowed purposes

- External analogue:
  - JumpServer authorization includes `动作` (actions) such as connect, upload, download, clipboard, SSH session sharing.
  - Teleport has separate controls for SSH file copy, port forwarding, logins, labels, and session options.
  - Boundary targets are per network service/protocol with permissions and session authorization.
- Xirang-shaped metadata:
  - Store `allowed_purposes` as an explicit list/JSON list of stable enum values, with empty/null meaning all purposes for migration compatibility.
  - Candidate purpose buckets from current code boundaries:

| Purpose bucket | Current boundaries / files |
|---|---|
| `node_test` | `NodeHandler.TestConnection` (`backend/internal/api/handlers/node_handler.go:661-794`) |
| `ssh_key_test` | `SSHKeyHandler.TestConnection` (`backend/internal/api/handlers/ssh_key_handler.go:356-455`) |
| `probe` | node prober (`backend/internal/probe/prober.go:99`, `250`) |
| `terminal` | WebSocket terminal (`backend/internal/api/handlers/terminal_handler.go:215-235`) |
| `file_browser` | SFTP list/content (`backend/internal/api/handlers/file_handler.go:75-121`, `138-174`) |
| `docker_volumes` | Docker volume discovery (`backend/internal/api/handlers/docker_handler.go:50-65`, `83-90`) |
| `node_logs` | node log runner (`backend/internal/nodelogs/ssh_runner.go:24-39`) |
| `task_backup` / `task_restore` | task manager/runner and executor SSH paths (`backend/internal/task/manager.go:242-244`; `backend/internal/task/runner.go:206-208`; `backend/internal/task/executor/ssh_connect.go:15-74`) |
| `task_command` / `batch_command` | command executor and batch command task paths using executor SSH helpers (search result: `backend/internal/task/executor/command_executor.go`, `backend/internal/api/handlers/batch_handler.go` if present) |
| `task_hook` | pre/post hook SSH command path (`backend/internal/task/hook.go` from search results) |
| `snapshot` / `snapshot_diff` | snapshot/indexer/diff SSH command paths (search results under `backend/internal/snapshot/`, `backend/internal/api/handlers/snapshot_diff_handler.go`, `backend/internal/anomaly/snapshot_diff.go`) |
| `integrity_check` | integrity checker SSH command paths (`backend/internal/task/integrity_checker.go` from search results) |
| `retention` | retention worker SSH command paths (`backend/internal/task/retention.go` from search results) |
| `drill` | restore drill SSH paths (`backend/internal/task/drill.go` from search results) |
| `node_migration` | migration/preflight SSH paths (`backend/internal/api/handlers/node_migrate_preflight_handler.go` from search results) |

#### Allowed node IDs and node tags

- External analogue:
  - Teleport allows SSH server access by `node_labels`, including literal values, wildcards, lists, regex, and label expressions.
  - Boundary targets reference host sets within project scope.
  - JumpServer asset authorization references individual assets and nodes/asset groups.
  - Warpgate emphasizes precise 1:1 user/service assignment.
- Xirang-shaped metadata:
  - `allowed_node_ids`: explicit exact node IDs. Empty/null = all nodes for migration compatibility.
  - `allowed_node_tags`: explicit tag list. Empty/null = all tags/nodes for migration compatibility.
  - If both node IDs and tags exist, the implementation contract must define whether the effective scope is union or intersection. Existing Xirang tag matchers use any-of semantics for tag lists (`backend/internal/slo/compute.go:77-88`; `backend/internal/alerting/silence.go:76-87`).
  - Exact node ID matching maps cleanly to current ownership patterns (`OwnedNodeIDs`) and avoids the substring pitfalls of SQL `LIKE` tag matching.

#### Permissive migration defaults

- Active task assumptions explicitly require existing installations to keep working until admins tighten scopes (`.trellis/tasks/05-19-security-p1-least-privilege-audit/prd.md:15-20`, `27-32`, `34-41`).
- Project migration contracts require paired SQLite/PostgreSQL migrations, lockstep versions, safe existing-installation changes, and snake_case JSON/API fields (`.trellis/spec/backend/database-guidelines.md:42-53`, `60-70`).
- Compatible default pattern:
  - `disabled NOT NULL DEFAULT false`.
  - `expires_at NULL` means no expiry.
  - `allowed_purposes NULL` or empty list means all known purposes.
  - `allowed_node_ids NULL` or empty list means all nodes.
  - `allowed_node_tags NULL` or empty list means all tags/nodes.
  - These broad defaults preserve old behavior while allowing the security risk summary to classify global/unscoped keys as advisory risks.

#### Enforcement at use boundaries

- The enforcement check needs three inputs: key metadata, target node (or no node for export/list-only cases), and purpose.
- Central boundary candidates in current code:
  - `sshutil.ResolveKeyContent` / `BuildSSHAuth` / `BuildSSHAuthWithKey` for most HTTP/probe/log/terminal/SFTP boundaries (`backend/internal/sshutil/ssh_auth.go:26-122`).
  - `executor.resolveNodePrivateKey` / `DialSSHForNode` for task/executor paths (`backend/internal/task/executor/executor.go:514-533`; `backend/internal/task/executor/ssh_connect.go:15-74`).
  - `SSHKeyHandler.TestConnection` because it applies a selected key to arbitrary requested nodes rather than the node's bound key (`backend/internal/api/handlers/ssh_key_handler.go:356-455`).
  - Public-key-only export and config export/import are separate surfaces; config export with secrets already has admin-only/audit behavior (`backend/internal/api/handlers/config_handler.go:60-72`, `99-131`).
- The most practical Xirang pattern is not a new PAM session broker; it is a metadata gate before private key material is used for a dial/session and before handlers intentionally test or export key-derived material.

### External References

- [Teleport Role Reference](https://goteleport.com/docs/reference/access-controls/roles/) — Relevant patterns: default-deny allow/deny rules; deny evaluated first; `allow.logins`; `node_labels`; label expressions; `ssh_file_copy`; `max_session_ttl`; `disconnect_expired_cert`; least-permissive option combination. Useful for purpose flags, label/tag scope, and expiry/session semantics, but Teleport's certificate-based PAM model is broader than this Xirang task.
- [Boundary Scopes](https://developer.hashicorp.com/boundary/docs/domain-model/scopes) — Relevant patterns: a scope is a permission boundary/container; resources are partitioned and ownership assigned to users/groups. Useful as conceptual background for Xirang key scope, not as a full hierarchy model.
- [Boundary Targets](https://developer.hashicorp.com/boundary/docs/domain-model/targets) — Relevant patterns: a target represents a networked service with permissions, belongs to a project, references host sets and credential libraries, and has session limits. Useful for mapping node/tag scope to a lightweight target boundary.
- [Boundary Credentials](https://developer.hashicorp.com/boundary/docs/domain-model/credentials) — Relevant patterns: a credential binds identity to permissions/capabilities on a host for a session; SSH private key credentials include username/private key fields. Xirang `SSHKey` already acts as a stored credential object.
- [JumpServer Asset Authorization](https://docs.jumpserver.org/zh/v4/manual/admin/console/authorization_manage/assets_authorization/) — Relevant patterns: authorization rules limit users to assets; rule parameters include users/user groups, assets/nodes, accounts, protocols, actions, start date, and expiry date; empty required options make a rule ineffective; no wildcard full-match. Useful for Xirang purpose/time/node-or-tag metadata, but JumpServer is a full bastion/PAM product.
- [Warpgate README](https://github.com/warp-tech/warpgate) — Relevant patterns: precise 1:1 assignment between users and services; target and user lists are assigned through admin UI; sessions may be recorded. Useful as a target-assignment comparison only; session recording and bastion proxying are outside Xirang's stated scope.

### Related Specs

- `.trellis/spec/backend/quality-guidelines.md` — Security-sensitive code such as SSH auth/RBAC/ownership needs explicit denial tests; RBAC route permissions must be covered by role matrix; security risk summary must be advisory-only, sanitized, stable-coded, and admin-only.
- `.trellis/spec/backend/database-guidelines.md` — Add paired SQLite/PostgreSQL migrations, keep versions lockstep, make migrations safe for existing installations, keep snake_case JSON/API names, and do not expose raw model values containing secrets.
- `.trellis/spec/guides/cross-layer-thinking-guide.md` — Relevant for keeping backend model/API/frontend mappings aligned when new SSH key metadata is added.

## Caveats / Not Found

- Not found in current Xirang code: SSHKey `disabled`, `expires_at`, `allowed_purposes`, `allowed_node_ids`, or `allowed_node_tags` fields; frontend `SSHKeyRecord` and API mapper likewise lack those fields (`web/src/types/domain.ts:491-508`; `web/src/lib/api/ssh-keys-api.ts:4-25`, `67-95`).
- Not found in current Xirang code: a single centralized purpose-aware SSH credential-use gate; there are shared and executor-local key resolution paths.
- Not found in current Xirang code: domain-specific credential-use audit records beyond existing route/audit logging and `last_used_at` update on node test success.
- Warpgate docs site fetch returned SSL EOF from this environment, so Warpgate comparison uses the repository README rather than the docs page.
- External PAM/bastion systems separate human authorization, target authorization, credential brokering, session recording, and approvals. Xirang's active task is explicitly layered hardening, so only the metadata/enforcement/audit portions map directly.
