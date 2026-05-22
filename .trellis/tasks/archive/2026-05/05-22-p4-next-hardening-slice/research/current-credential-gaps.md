# Research: current credential gaps

- **Query**: Research current backend credential resolution paths after P4-1 local provider seam. Identify high-value next small slices, especially remaining direct SSH/private-key/password resolution outside `sshutil` provider, control ordering, metadata safety, and tests.
- **Scope**: internal
- **Date**: 2026-05-22

## Findings

### Files Found

| File Path | Description |
|---|---|
| `backend/internal/sshutil/credential_provider.go` | P4-1 local credential provider seam; central managed-key, inline-key, and password auth construction. |
| `backend/internal/sshutil/ssh_auth.go` | Public wrapper functions over the provider seam plus SSH dial/host-key helpers. |
| `backend/internal/sshutil/scope.go` | SSH key purpose constants and managed-key least-privilege scope validation. |
| `backend/internal/sshutil/credential_provider_test.go` | Provider tests for encrypted storage, scope denial, missing managed key fail-closed behavior, last-used update, and metadata safety. |
| `backend/internal/task/executor/ssh_connect.go` | Executor SSH connection helper; still builds SSH auth via executor-local resolver before dialing. |
| `backend/internal/task/executor/executor.go` | Rsync executor and executor-local private-key resolver; remaining direct private-key resolution outside the provider seam. |
| `backend/internal/task/executor/command_executor.go` | Command task executor uses `DialSSHForNodePurpose`, so it inherits executor-local credential resolution. |
| `backend/internal/task/executor/restic_executor.go` | Restic backup/restore/snapshot operations use `DialSSHForNodePurpose`; restic repository password is handled separately from SSH credentials. |
| `backend/internal/task/executor/rclone_executor.go` | Rclone backup/restore operations use `DialSSHForNodePurpose`. |
| `backend/internal/task/hook.go` | Task pre/post hooks use `executor.DialSSHForNodePurpose(..., PurposeTaskHook)`. |
| `backend/internal/task/integrity_checker.go` | Integrity checks preload `Node.SSHKey` and use executor dial path with `PurposeIntegrityCheck`. |
| `backend/internal/task/retention.go` | Retention cleanup preloads `Node.SSHKey` and uses executor dial path with `PurposeRetention`. |
| `backend/internal/api/handlers/node_handler.go` | Node test connection uses provider-backed auth but still performs raw `ssh.Dial`. |
| `backend/internal/sshutil/probe.go` | Probe uses provider-backed auth but still performs raw `ssh.Dial` inside `sshutil`. |
| `backend/internal/api/handlers/file_handler.go` | File browser uses provider-backed auth and `sshutil.DialSSH` with `PurposeFileBrowser`. |
| `backend/internal/api/handlers/docker_handler.go` | Docker volume discovery uses provider-backed auth and `sshutil.DialSSH` with `PurposeDockerVolumes`. |
| `backend/internal/api/handlers/node_doctor_handler.go` | Node Doctor uses provider-backed auth and `sshutil.DialSSH` with diagnostic-specific host-key classification. |
| `backend/internal/api/handlers/terminal_handler.go` | Terminal WebSocket path enforces realtime auth, step-up, grant, ownership/load, provider auth, host-key callback, then SSH dial. |
| `backend/internal/api/handlers/batch_handler.go` | Batch command creation enforces dangerous-command checks, ownership, step-up, and grants before creating command tasks. |
| `backend/internal/probe/prober.go` | Scheduled probe/metrics use provider-backed auth paths and safe credential-audit fallback metadata. |
| `backend/internal/nodelogs/ssh_runner.go` | Node log collection uses provider-backed auth and safe stage-based credential audit metadata. |
| `backend/internal/snapshot/indexer.go` | Snapshot indexing preloads node/key in `EnsureIndexed`, but `GetIndexStatus` loads only the task before listing snapshots. |
| `backend/internal/anomaly/snapshot_diff.go` | Snapshot diff defensively reloads task with `Preload("Node").Preload("Node.SSHKey")` and uses executor dial path. |
| `backend/internal/api/handlers/snapshot_diff_handler.go` | Snapshot diff handler preloads node/key and uses executor dial path. |
| `backend/internal/task/verifier/verifier.go` | Verifier uses provider-backed auth and `sshutil.DialSSH` with `PurposeIntegrityCheck`. |
| `backend/internal/api/handlers/node_migrate_preflight_handler.go` | Migration preflight combines provider-backed probe and executor dial path for tool checks. |
| `backend/internal/api/handlers/helpers.go` | Shared credential-audit fallback and safe-error helper functions. |
| `backend/internal/credentialaudit/audit.go` | Credential audit metadata/error sanitizer and domain audit write helpers. |
| `backend/internal/model/models.go` | Credential-bearing model fields, encryption hooks, sanitizers, and `CredentialAuditEvent` safety contract. |
| `backend/internal/database/migrations/sqlite/000059_ssh_key_scope_credential_audit.up.sql` | SQLite migration for SSH key scope columns and credential audit events. |
| `backend/internal/database/migrations/postgres/000059_ssh_key_scope_credential_audit.up.sql` | PostgreSQL migration for SSH key scope columns and credential audit events. |

### Code Patterns

#### Provider-backed credential seam exists in `sshutil`

`backend/internal/sshutil/credential_provider.go:13-18` defines the local provider interface:

```go
const CredentialProviderLocal = "local"

type CredentialProvider interface {
	ResolveKeyContentForPurpose(node model.Node, db *gorm.DB, purpose string) (string, string, ResolvedCredential, error)
	BuildSSHAuthWithKeyForPurpose(node model.Node, db *gorm.DB, purpose string) ([]ssh.AuthMethod, string, ResolvedCredential, error)
}
```

`backend/internal/sshutil/ssh_auth.go:34-64` exposes wrapper functions such as `ResolveKeyContentForPurpose`, `BuildSSHAuthForPurpose`, `BuildSSHAuthWithKeyForPurpose`, and `BuildSSHAuthWithCredential` that delegate to `DefaultCredentialProvider()`.

Provider resolution order in `backend/internal/sshutil/credential_provider.go:26-60`:

1. Use preloaded `node.SSHKey.PrivateKey` when present, after `ValidateSSHKeyScope`.
2. If `node.SSHKeyID != nil`, DB-load `model.SSHKey`, validate scope, and use its private key.
3. Use inline `node.PrivateKey`.
4. Return no key material.

Password auth in `backend/internal/sshutil/credential_provider.go:62-94` returns `ResolvedCredential{Kind: "password", Source: "node.password", Provider: CredentialProviderLocal}` and builds `ssh.Password(node.Password)` only when `node.Password` is non-empty.

Managed key success calls `markSSHKeyLastUsed` after key parsing in `backend/internal/sshutil/credential_provider.go:85-90`; the helper updates `ssh_keys.last_used_at` in `backend/internal/sshutil/ssh_auth.go:67-73`.

#### Managed-key least-privilege scope

`backend/internal/sshutil/scope.go:13-33` defines purpose constants for `ssh_key_test`, `ssh_key_export`, `node_test`, `terminal`, `task_command`, `batch_command`, `drill`, `probe`, `file_browser`, `docker_volumes`, `node_logs`, `task_backup`, `task_restore`, `task_hook`, `snapshot`, `snapshot_diff`, `integrity_check`, `retention`, and `node_migration`.

`backend/internal/sshutil/scope.go:129-153` validates disabled/expired/purpose/node/tag constraints before managed-key use:

```go
func ValidateSSHKeyScope(key model.SSHKey, node model.Node, purpose string) error {
	if err := ValidateSSHKeyPurpose(key, purpose); err != nil {
		return err
	}
	if !nodeIDAllowed(key.AllowedNodeIDs, node.ID) {
		return fmt.Errorf("SSH Key 不允许用于该节点")
	}
	if !nodeTagsAllowed(key.AllowedNodeTags, node.Tags) {
		return fmt.Errorf("SSH Key 不允许用于该节点标签")
	}
	return nil
}
```

#### Remaining direct SSH/private-key/password resolution outside provider

The main remaining direct resolver is in executor code.

`backend/internal/task/executor/ssh_connect.go:20-61` calls `resolveSSHAuthMethodsForPurpose(node, purpose)` before resolving host-key callback and dialing via `sshutil.DialSSH`.

`backend/internal/task/executor/ssh_connect.go` contains duplicated auth construction behavior:

```go
func resolveSSHAuthMethodsForPurpose(node model.Node, purpose string) ([]ssh.AuthMethod, sshutil.ResolvedCredential, error) {
	authType := strings.ToLower(strings.TrimSpace(node.AuthType))
	var authMethods []ssh.AuthMethod

	switch authType {
	case "key":
		keyContent, _, credential, err := resolveNodePrivateKeyForPurpose(node, purpose)
		...
		normalizedKey, _, err := sshutil.ValidateAndPreparePrivateKey(keyContent, sshutil.SSHKeyTypeAuto)
		...
		signer, err := ssh.ParsePrivateKey([]byte(normalizedKey))
		...
		return authMethods, credential, nil
	case "password":
		credential := sshutil.ResolvedCredential{Kind: "password", Source: "node.password"}
		if node.Password == "" {
			return nil, credential, fmt.Errorf("密码认证未配置密码")
		}
		authMethods = append(authMethods, ssh.Password(node.Password))
		return authMethods, credential, nil
	default:
		return nil, sshutil.ResolvedCredential{}, fmt.Errorf("不支持的认证方式: %s", authType)
	}
}
```

`backend/internal/task/executor/executor.go` contains the executor-local key resolver:

```go
func resolveNodePrivateKeyForPurpose(node model.Node, purpose string) (string, string, sshutil.ResolvedCredential, error) {
	if node.SSHKey != nil {
		if key := strings.TrimSpace(node.SSHKey.PrivateKey); key != "" {
			credential := resolvedCredentialFromSSHKey(node, node.SSHKey.ID)
			if err := sshutil.ValidateSSHKeyScope(*node.SSHKey, node, purpose); err != nil {
				return "", credential.Source, credential, err
			}
			return key, credential.Source, credential, nil
		}
	}

	if node.SSHKeyID != nil {
		keyID := *node.SSHKeyID
		credential := sshutil.ResolvedCredential{Kind: "ssh_key", Source: fmt.Sprintf("ssh_key_id=%d", keyID), KeyID: &keyID}
		return "", credential.Source, credential, fmt.Errorf("节点绑定的密钥不存在，请检查密钥配置")
	}

	if key := strings.TrimSpace(node.PrivateKey); key != "" {
		credential := sshutil.ResolvedCredential{Kind: "node_private_key", Source: "node.private_key"}
		return key, credential.Source, credential, nil
	}
	return "", "", sshutil.ResolvedCredential{}, nil
}
```

Observed asymmetry:

- `sshutil.LocalCredentialProvider` can DB-load a managed key by `SSHKeyID` when `node.SSHKey` is absent.
- The executor-local resolver fails closed when `SSHKeyID` is set but `node.SSHKey` is absent or empty.

The existing executor regression `backend/internal/task/executor/executor_test.go` includes `TestRsyncExecutorRejectsStaleNodePrivateKeyWhenSSHKeyIDPresent`, which verifies that a node with `SSHKeyID` does not fall back to stale inline `Node.PrivateKey`.

#### Runtime paths inheriting executor-local resolution

The following paths use `executor.DialSSHForNodePurpose`, so they currently inherit the executor-local resolver rather than `sshutil.CredentialProvider`:

- `backend/internal/task/executor/command_executor.go` — command tasks use `PurposeTaskCommand`.
- `backend/internal/task/executor/restic_executor.go` — restic backup/restore/snapshot operations.
- `backend/internal/task/executor/rclone_executor.go` — rclone backup/restore operations.
- `backend/internal/task/hook.go` — task hooks use `PurposeTaskHook`.
- `backend/internal/task/integrity_checker.go` — integrity checks use `PurposeIntegrityCheck`.
- `backend/internal/task/retention.go` — retention cleanup uses `PurposeRetention`.
- `backend/internal/anomaly/snapshot_diff.go` and `backend/internal/api/handlers/snapshot_diff_handler.go` — snapshot diff uses `PurposeSnapshotDiff`.
- `backend/internal/api/handlers/node_migrate_preflight_handler.go` — migration preflight tool checks use `PurposeNodeMigration`.

Rsync remote backup still materializes normalized private key material to a temporary PEM file in `backend/internal/task/executor/executor.go` using `os.CreateTemp("", "xirang-key-*.pem")`, chmod `0600`, `-i <tempfile>` in the ssh command, and cleanup removal.

Restic repository passwords in `backend/internal/task/executor/restic_executor.go` are credential-adjacent but separate from SSH provider resolution. `ResticConfig.RepositoryPassword` is converted into a remote shell env prefix by `buildResticEnvPrefix`.

#### Raw SSH dial call sites

Only two raw `ssh.Dial` call sites were identified in the inspected backend SSH paths:

- `backend/internal/api/handlers/node_handler.go` — `TestConnection` uses provider-backed `sshutil.BuildSSHAuthWithKeyForPurpose(..., PurposeNodeTest)` but manually dials with `ssh.Dial` and an inline `ssh.ClientConfig`.
- `backend/internal/sshutil/probe.go` — probe uses provider-backed `BuildSSHAuthForPurpose` but manually dials with `ssh.Dial` inside `sshutil`.

Other inspected paths generally use `sshutil.DialSSH` after auth construction.

#### Control ordering patterns

Terminal (`backend/internal/api/handlers/terminal_handler.go`) follows this order:

1. Reserve terminal session slot.
2. Upgrade WebSocket.
3. Read auth message.
4. Validate realtime token/admin role.
5. Validate step-up proof.
6. Parse `node_id`.
7. Enforce terminal credential grant.
8. Load node with `Preload("SSHKey")`.
9. Build auth with `sshutil.BuildSSHAuthForPurpose(..., PurposeTerminal)`.
10. Resolve host-key callback.
11. Dial SSH with `sshutil.DialSSH`.
12. Request PTY/shell.
13. Write credential audit events.

Batch command creation (`backend/internal/api/handlers/batch_handler.go`) follows this order:

1. Bind and trim command.
2. Reject empty/overlength command.
3. Dangerous command check.
4. Authorize node ownership set.
5. Enforce step-up.
6. Enforce credential grants.
7. Create command tasks.
8. Trigger each task.
9. Write audit counts.

Runtime caveat: batch command creation/grant purpose is `batch_command`, while the created command tasks later run through `CommandExecutor` as `task_command`.

Node test connection (`backend/internal/api/handlers/node_handler.go`) builds provider-backed auth and writes credential audit events around blocked/failure/success outcomes, but uses raw `ssh.Dial` and also manually updates `SSHKey.LastUsedAt` after success even though the provider path updates managed-key last-used metadata during auth construction.

Probe/metrics paths (`backend/internal/probe/prober.go`, `backend/internal/sshutil/probe.go`) use provider-backed auth with purpose `probe`; scheduled metric collection uses `sshutil.DialSSH`, while `ProbeNode` uses raw `ssh.Dial` inside `sshutil`.

File browser, Docker volumes, Node Doctor, node logs, verifier, and scheduled prober paths use purpose-specific provider-backed auth and generally resolve host-key callback before `sshutil.DialSSH`.

Snapshot indexing edge: `backend/internal/snapshot/indexer.go` preloads `Node` and `Node.SSHKey` in `EnsureIndexed`, but `GetIndexStatus` loads only `db.First(&task, taskID)` before `exec.ListSnapshots(ctx, task)`. Restic snapshot listing expects task/node credential context.

#### Metadata safety patterns

`backend/internal/model/models.go` defines `CredentialAuditEvent` with an explicit safety contract: it must never contain raw secrets, terminal streams, command output, or executor config.

`backend/internal/credentialaudit/audit.go` filters metadata keys and values. Denied metadata key substrings include `private`, `password`, `token`, `secret`, `credential`, `config`, `output`, `stream`, `command`, `content`, and `payload`. Denied metadata value substrings include those plus `bearer` and `authorization:`.

`backend/internal/credentialaudit/audit.go` also redacts error message tails after markers such as `输出:`, `output:`, `stdout:`, and `stderr:`.

`backend/internal/api/handlers/helpers.go` centralizes fallback credential metadata and safe error messages:

- `eventCredentialFields` uses `ResolvedCredential` first, then fallback kind/source.
- `nodeCredentialFallback` derives safe source metadata such as `node.password`, `node.private_key`, or `ssh_key_id=<id>` without exposing material.
- `credentialAuditSafeError(stage, err)` returns stage-based messages such as `<stage> failed`.

Inspected handler-specific metadata patterns:

- File browser metadata includes stage, path hash/count/truncation/preview bytes, not file content.
- Docker metadata includes stage, count, warning flag, not raw Docker output or volume names.
- Node Doctor audit tests assert no host, private-key text, password markers, or diagnostic evidence is persisted.
- Terminal metadata includes stage, session ID, latency/duration; terminal stream content is not stored.
- Node logs metadata includes stage and max bytes; error messages are stage-based.
- Migration preflight audit metadata is sanitized and does not store raw command output.

#### Tests already present

`backend/internal/sshutil/credential_provider_test.go` covers:

- Managed SSH key uses encrypted local storage.
- Inline node private key uses encrypted node storage.
- Password auth uses encrypted node storage.
- Managed SSH key scope denial before use for disabled, expired, purpose-denied, node-denied, and tag-denied cases.
- Missing managed key fails closed.
- Invalid private key errors do not include raw private key material.
- Credential metadata does not contain raw secrets.
- Successful managed-key use updates `LastUsedAt`.

Executor tests include the stale inline-key regression when `SSHKeyID` is present.

Handler tests observed around metadata safety include file handler validation/audit tests, Docker handler audit tests, and Node Doctor audit tests.

### External References

No external references were used. This was an internal codebase research task.

### Related Specs

- `.trellis/spec/backend/error-handling.md` — Security-sensitive errors must not expose raw SQL, encryption details, SSH private keys, tokens, command output, SFTP/file content, Docker output, diagnostic evidence, exported config payloads, or stack-like details.
- `.trellis/spec/backend/logging-guidelines.md` — Logs must not contain passwords, private keys, TOTP secrets, JWTs, recovery codes, encryption keys, SMTP passwords, webhook secrets, bearer tokens, raw endpoints, decrypted values, unsafe command output, SFTP contents, Docker output/volume names, Node Doctor evidence, migration preflight output, executor config, or risky credential audit metadata.
- `.trellis/spec/backend/quality-guidelines.md` — Sensitive fields must be sanitized in API responses, and SSH auth/RBAC/ownership/security-sensitive paths require explicit denial tests.
- `.trellis/spec/frontend/component-guidelines.md` — SSH Key least-privilege scope UI convention; backend enforces disabled/expiry/purpose/node/tag restrictions and UI must not expose credential details or unsafe metadata.
- `.trellis/tasks/archive/2026-05/05-22-p4-credential-broker-foundation/research/current-credential-resolution.md` — Prior baseline research for current credential resolution and seams; noted executor/shared resolver asymmetry and snapshot index preload edge.
- `.trellis/tasks/archive/2026-05/05-19-security-p1-least-privilege-audit/research/credential-use-audit.md` — Prior credential audit design and field research.
- `.trellis/tasks/archive/2026-05/05-18-security-baseline-hardening/research/current-code-security-surfaces.md` — Earlier security surface research covering task command, audit, route/ownership, restore/drill, and SSH key surfaces.

## Caveats / Not Found

- No code files were modified during this research.
- No build or test commands were run, so findings are based on static inspection only.
- The highest-value remaining direct credential-resolution surface found is executor-side auth/key/password construction in `backend/internal/task/executor/ssh_connect.go` and `backend/internal/task/executor/executor.go`.
- Two raw `ssh.Dial` call sites were observed: node test connection and `sshutil.ProbeNode`; both use provider-backed auth before dialing.
- No additional direct SSH private-key/password auth builders outside `sshutil` and the executor-local resolver were identified in the inspected paths.
- Repository credentials in restic executor config are separate from SSH credential provider scope, but they remain sensitive credential-adjacent data handled in executor config paths.
