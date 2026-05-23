# Research: P4 provider precedent for restic credential resolver

- **Query**: 当前 Trellis 任务目录是 `/Users/weibo/Code/xirang/.trellis/tasks/05-23-p4-restic-credential-resolver`。只读研究已归档 P4-1/P4-2 任务和现有 sshutil credential provider seam，把可复用约束、验收标准、范围排除项写入该任务的 `research/p4-provider-precedent.md`。
- **Scope**: internal
- **Date**: 2026-05-23

## Findings

### Files Found

| File Path | Description |
|---|---|
| `.trellis/tasks/archive/2026-05/05-22-p4-credential-broker-foundation/prd.md` | P4-1 precedent: local-only provider foundation, SSH auth integration, safe metadata, tests, and out-of-scope architecture items. |
| `.trellis/tasks/archive/2026-05/05-22-p4-credential-broker-foundation/research/current-credential-resolution.md` | P4-1 baseline map of credential-bearing models, SSH/executor paths, restic repository-password handling, import/export, tests, and risks. |
| `.trellis/tasks/archive/2026-05/05-22-p4-credential-broker-foundation/research/broker-foundation-patterns.md` | P4-1 pattern research; recommends smallest hardcoded local-provider seam and explicitly defers restic repository-password seam unless a later task touches it. |
| `.trellis/tasks/archive/2026-05/05-22-p4-next-hardening-slice/prd.md` | P4-2 precedent: executor SSH provider adoption; preserves fail-closed preload semantics and excludes restic repository-password provider seam. |
| `.trellis/tasks/archive/2026-05/05-22-p4-next-hardening-slice/research/current-credential-gaps.md` | P4-2 gap research; identifies current provider seam, executor adoption, repeated restic repository-password extraction/env-prefix materialization, and metadata safety patterns. |
| `.trellis/tasks/archive/2026-05/05-22-p4-next-hardening-slice/research/remaining-p4-roadmap.md` | Roadmap ranking after P4-1; identifies restic repository-password local resolver as a later candidate and lists deferred architecture work. |
| `.trellis/tasks/archive/2026-05/05-21-plan-p3-p4-credential-security-hardening/prd.md` | P3/P4 planning source; classifies Vault/KMS/provider references, SSH CA, recording, command approval, and advanced step-up as architecture-level P4 items. |
| `backend/internal/sshutil/credential_provider.go` | Existing provider seam: `CredentialProviderLocal`, `CredentialProvider`, `LocalCredentialProvider`, managed-key/inline-key/password resolution. |
| `backend/internal/sshutil/ssh_auth.go` | Public provider-backed SSH auth wrappers plus `ResolvedCredential` last-used update helper. |
| `backend/internal/sshutil/scope.go` | Purpose constants, `ResolvedCredential` safe metadata shape, and managed-key scope validation. |
| `backend/internal/sshutil/credential_provider_test.go` | P4-1 tests proving encrypted local storage, safe metadata, scope denial, missing managed-key fail-closed, and `LastUsedAt` update behavior. |
| `backend/internal/task/executor/ssh_connect.go` | P4-2 executor SSH auth now delegates to provider-backed `sshutil` helpers and writes provider-safe runtime audit metadata. |
| `backend/internal/task/executor/executor.go` | P4-2 rsync path now delegates key resolution to `sshutil.ResolveKeyContentForPurpose`, while keeping rsync temp-key behavior and safe audit metadata. |
| `backend/internal/task/executor/restic_executor.go` | Main restic executor; parses `repository_password` from decrypted `Task.ExecutorConfig` and builds `RESTIC_PASSWORD=...` command prefixes. |
| `backend/internal/task/executor/restic_executor_test.go` | Existing restic tests around config parsing and command prefix behavior. |
| `backend/internal/snapshot/indexer.go` | Snapshot indexer restic password extraction/env-prefix path outside `ResticExecutor`. |
| `backend/internal/anomaly/snapshot_diff.go` | Background snapshot diff restic password extraction/env-prefix path outside `ResticExecutor`. |
| `backend/internal/api/handlers/snapshot_diff_handler.go` | API snapshot diff restic password extraction/env-prefix path outside `ResticExecutor`. |
| `backend/internal/task/integrity_checker.go` | Background restic integrity check password extraction/env-prefix path. |
| `backend/internal/task/retention.go` | Background restic retention password extraction/env-prefix path. |
| `backend/internal/task/verifier/verifier.go` | Restic verification password extraction/env-prefix path. |
| `backend/internal/model/models.go` | `Task.ExecutorConfig` encryption/decryption hooks and `CredentialAuditEvent` safety contract. |
| `.trellis/spec/backend/database-guidelines.md` | Storage contract: sensitive fields through model hooks, no raw secret response, no manual encrypt/decrypt in handlers. |
| `.trellis/spec/backend/quality-guidelines.md` | SSH scope, credential audit, grant ordering, safe metadata, and sensitive-data exclusion contracts. |
| `.trellis/spec/backend/logging-guidelines.md` | Logging exclusions for decrypted secrets, executor config, command output, endpoints, and unsafe credential metadata. |
| `.trellis/spec/backend/error-handling.md` | Error response exclusions for raw SQL, encryption details, private keys, tokens, command output, exported payloads, and stack-like details. |

### Code Patterns

#### 1. P4-1 reusable provider constraints

P4-1 established the first provider precedent as an intentionally conservative, internal-only local seam:

- Provider identity is fixed to `local`; no external provider dependency is introduced (`.trellis/tasks/archive/2026-05/05-22-p4-credential-broker-foundation/prd.md:13`, `:23`).
- Existing local encrypted DB storage remains the only source of truth; no migrations, provider tables, env vars, deployment changes, or public API fields are added (`.trellis/tasks/archive/2026-05/05-22-p4-credential-broker-foundation/prd.md:14`, `:28`).
- The first seam was placed around purpose-aware SSH auth helpers, preserving managed SSH key scope checks, inline credential behavior, password/key auth behavior, safe resolved metadata, and `LastUsedAt` updates (`.trellis/tasks/archive/2026-05/05-22-p4-credential-broker-foundation/prd.md:15`, `:25-27`).
- Operation-layer controls remain additive and fail-closed: primary auth, RBAC, ownership, step-up, grants, SSH key scope, and credential audit ordering are not replaced by the provider seam (`.trellis/tasks/archive/2026-05/05-22-p4-credential-broker-foundation/prd.md:16`).
- The seam must not log/audit/store/return/expose raw passwords, private keys, executor config, terminal streams, command text/output, file contents, Docker output, raw SQL, endpoint/proxy values, hostnames, paths, imported/exported payloads, or step-up proof material (`.trellis/tasks/archive/2026-05/05-22-p4-credential-broker-foundation/prd.md:17`).
- P4-1 tests were required to prove local-provider equivalence for managed SSH key, inline key, password auth, scope denial, and safe metadata (`.trellis/tasks/archive/2026-05/05-22-p4-credential-broker-foundation/prd.md:18`, `:29-30`, `:62`).
- P4-1 technical approach kept the seam below call sites and above storage hooks: delegate to existing helper/model-hook behavior, do not manually encrypt/decrypt in handlers, and do not bypass GORM hooks (`.trellis/tasks/archive/2026-05/05-22-p4-credential-broker-foundation/prd.md:44`).

P4-1 also deferred external architecture: Vault/KMS/Boundary/Teleport, provider UI/health/leases/renew/revoke/fallback, SSH CA, terminal/session recording, command approval, migrations, config import/export provider references, API/frontend/deployment/doc changes (`.trellis/tasks/archive/2026-05/05-22-p4-credential-broker-foundation/prd.md:33-40`).

#### 2. Existing `sshutil` credential provider seam

The current seam is in `backend/internal/sshutil`:

```go
const CredentialProviderLocal = "local"

type CredentialProvider interface {
	ResolveKeyContentForPurpose(node model.Node, db *gorm.DB, purpose string) (string, string, ResolvedCredential, error)
	BuildSSHAuthWithKeyForPurpose(node model.Node, db *gorm.DB, purpose string) ([]ssh.AuthMethod, string, ResolvedCredential, error)
}
```

Relevant details:

- `CredentialProviderLocal = "local"`, `CredentialProvider`, `LocalCredentialProvider`, and `DefaultCredentialProvider()` are defined at `backend/internal/sshutil/credential_provider.go:13-24`.
- `LocalCredentialProvider.ResolveKeyContentForPurpose` resolves in this order: preloaded `node.SSHKey.PrivateKey` after `ValidateSSHKeyScope`, DB-loaded `model.SSHKey` by `node.SSHKeyID` when a DB handle exists, inline `node.PrivateKey`, then no key (`backend/internal/sshutil/credential_provider.go:26-60`).
- Inline private key metadata is safe and local-only: `ResolvedCredential{Kind: "node_private_key", Source: "node.private_key", Provider: CredentialProviderLocal}` (`backend/internal/sshutil/credential_provider.go:56-58`).
- `BuildSSHAuthWithKeyForPurpose` returns password metadata as `Kind: "password", Source: "node.password", Provider: "local"`, validates/parses key auth, marks managed key last-used, and returns auth methods plus safe metadata (`backend/internal/sshutil/credential_provider.go:62-94`).
- Public wrappers delegate through `DefaultCredentialProvider()`: `ResolveKeyContentForPurpose`, `BuildSSHAuthForPurpose`, `BuildSSHAuthWithKeyForPurpose`, and `BuildSSHAuthWithCredential` (`backend/internal/sshutil/ssh_auth.go:34-64`).
- Managed key last-used update is isolated in `markSSHKeyLastUsed` (`backend/internal/sshutil/ssh_auth.go:67-73`).
- SSH key metadata uses safe labels: `credentialFromSSHKey` returns `Kind: "ssh_key"`, `Source: "ssh_key_id=<id>"`, `Provider: "local"`, and optional `KeyID` (`backend/internal/sshutil/ssh_auth.go:75-80`).
- `ResolvedCredential` contains only `Kind`, `Source`, `Provider`, and optional `KeyID` (`backend/internal/sshutil/scope.go:57-64`).
- Purpose constants include restic-adjacent runtime purposes such as `task_backup`, `task_restore`, `snapshot`, `snapshot_diff`, `integrity_check`, and `retention` (`backend/internal/sshutil/scope.go:25-32`, `:47-54`).
- Managed-key scope validation enforces disabled, expiry, purpose, node ID, and node tag constraints (`backend/internal/sshutil/scope.go:129-153`).

Existing tests in `backend/internal/sshutil/credential_provider_test.go` cover the reusable pattern:

- Managed SSH key uses encrypted local storage and updates `LastUsedAt` (`:16-54`).
- Inline private key uses encrypted node storage (`:56-86`).
- Password auth uses encrypted node storage (`:88-118`).
- Disabled/expired/purpose-denied/node-denied/tag-denied managed keys fail before use and do not update `LastUsedAt` (`:120-220`).
- Missing managed SSH key fails closed (`:222-238`).
- Invalid private key errors and metadata exclude raw secret material (`:240-255`, `:302-312`).

#### 3. P4-2 reusable executor/provider adoption constraints

P4-2 extended P4-1 from shared `sshutil` helpers into executor-side SSH resolution:

- It adopted the `sshutil` local provider seam for executor SSH auth and rsync remote-backup key materialization (`.trellis/tasks/archive/2026-05/05-22-p4-next-hardening-slice/prd.md:9`).
- It preserved local encrypted DB/model storage as the only provider source and kept out external Vault/KMS/CA providers, migrations, deployment config, env vars, and public API fields (`.trellis/tasks/archive/2026-05/05-22-p4-next-hardening-slice/prd.md:10`).
- It preserved executor fail-closed semantics when `Node.SSHKeyID` is set but `Node.SSHKey` is unavailable; it must not fall back to stale inline `Node.PrivateKey` (`.trellis/tasks/archive/2026-05/05-22-p4-next-hardening-slice/prd.md:11`, `:35`, `:45`).
- It preserved purpose-aware managed-key checks for task-related purposes (`task_command`, `batch_command`, `task_backup`, `task_restore`, and adjacent executor purposes) (`.trellis/tasks/archive/2026-05/05-22-p4-next-hardening-slice/prd.md:12`).
- It preserved rsync remote-backup behavior: password auth unsupported, key temp file `0600`, best-effort cleanup, no raw key logging/persistence (`.trellis/tasks/archive/2026-05/05-22-p4-next-hardening-slice/prd.md:13`).
- Runtime credential audit evidence remains limited to provider/source/kind/key ID/status/stage metadata and excludes private keys, passwords, command text/output, terminal streams, sensitive paths, endpoints, hostnames, and decrypted executor config (`.trellis/tasks/archive/2026-05/05-22-p4-next-hardening-slice/prd.md:14`).
- P4-2 acceptance required provider-backed executor resolution, rsync key provider resolution, `Provider=local` metadata for managed/inline/password sources, compatibility for command/restore/remote-target/rsync paths, and tests for metadata, missing managed-key fail-closed behavior, stale inline-key rejection, password metadata, and safe errors (`.trellis/tasks/archive/2026-05/05-22-p4-next-hardening-slice/prd.md:17-24`).
- P4-2 explicitly excluded restic repository-password provider seam and profile-hook/app-credential provider seams (`.trellis/tasks/archive/2026-05/05-22-p4-next-hardening-slice/prd.md:47-54`).

Current executor code reflects that precedent:

- `resolveSSHAuthMethodsForPurpose` normalizes auth type, delegates to `sshutil.BuildSSHAuthForPurpose`, maps old error text for compatibility, and returns provider metadata (`backend/internal/task/executor/ssh_connect.go:66-93`).
- Runtime audit persists `provider` only if present and uses `stage`, `auth_type`, and optional `latency_ms` metadata (`backend/internal/task/executor/ssh_connect.go:106-147`).
- Rsync key resolution delegates to `sshutil.ResolveKeyContentForPurpose(node, nil, purpose)` (`backend/internal/task/executor/executor.go:522-524`).
- Rsync credential audit persists safe fields and optional `provider` metadata, with stage-based safe errors (`backend/internal/task/executor/executor.go:526-560`).

#### 4. Current restic repository-password materialization surfaces

Restic repository password is not currently provider-backed. It is stored in encrypted task executor config and materialized into remote command environment prefixes:

- `ResticConfig.RepositoryPassword` is the `repository_password` JSON field stored in `Task.ExecutorConfig` (`backend/internal/task/executor/restic_executor.go:17-22`, `:30`).
- Backup, restore, snapshot list, snapshot file list, and file restore parse decrypted `task.ExecutorConfig` with `parseResticConfig` (`backend/internal/task/executor/restic_executor.go:48-51`, `:133-136`, `:287-290`, `:315-318`, `:358-360`).
- These restic paths use provider-backed SSH dialing separately via purposes `task_backup`, `task_restore`, and `snapshot` (`backend/internal/task/executor/restic_executor.go:53-58`, `:138-142`, `:292-296`, `:320-324`, `:363-367`).
- Restic command materialization uses `buildCommandPrefix`, which calls `buildResticEnvPrefix(cfg.RepositoryPassword)` and optionally wraps with `sudo env` (`backend/internal/task/executor/restic_executor.go:393-402`).
- `buildResticEnvPrefix` preserves empty password as `RESTIC_PASSWORD=''` and otherwise returns `RESTIC_PASSWORD=` plus `ShellEscape(password)` (`backend/internal/task/executor/restic_executor.go:404-409`).
- Existing tests assert parse roundtrip and current prefix behavior, including `RESTIC_PASSWORD=` and empty password handling (`backend/internal/task/executor/restic_executor_test.go:46-69`, `:157-194`).

Other restic password extraction/env-prefix paths are separate from `ResticExecutor`:

- Snapshot index status currently loads only `db.First(&task, taskID)` before calling `ResticExecutor.ListSnapshots`; `EnsureIndexed` preloads `Node` and `Node.SSHKey` (`backend/internal/snapshot/indexer.go:33-45`, `:82-95`).
- Snapshot index file walking parses `Task.ExecutorConfig` through `parseResticIndexConfig` and builds `RESTIC_PASSWORD=...` via `buildIndexEnvPrefix` (`backend/internal/snapshot/indexer.go:178-185`, `:251-282`).
- API snapshot diff preloads `Node`/`Node.SSHKey`, parses `repository_password`, then builds `RESTIC_PASSWORD=...` via `buildDiffEnvPrefix` (`backend/internal/api/handlers/snapshot_diff_handler.go:87-118`, `:276-295`).
- Background anomaly snapshot diff reloads the full task with `Node`/`Node.SSHKey`, extracts `repository_password`, then builds `RESTIC_PASSWORD=...` (`backend/internal/anomaly/snapshot_diff.go:267-287`, `:313-321`).
- Restic integrity and retention parse `Task.ExecutorConfig` directly with `extractResticPassword` and local env-prefix construction (`backend/internal/task/integrity_checker.go:69-83`; `backend/internal/task/retention.go:138-162`, `:237-246`).
- Restic verifier parses `Task.ExecutorConfig` and builds `RESTIC_PASSWORD=` directly (`backend/internal/task/verifier/verifier.go:488-491`, `:531-543`).

#### 5. Storage, audit, logging, and error constraints for restic resolver work

- `Task.ExecutorConfig` is `json:"-"` and is encrypted/decrypted by GORM hooks (`backend/internal/model/models.go:308`, `:326-347`).
- `CredentialAuditEvent` must never contain raw secrets, terminal streams, command output, or executor config (`backend/internal/model/models.go:421-423`).
- Database guidelines require sensitive fields to stay behind model hooks (`.trellis/spec/backend/database-guidelines.md:13-17`), credential audit to store only safe identifiers/metadata and not raw credentials/decrypted executor config/streams/output/file contents (`:84-91`), no raw secret-bearing model values in responses (`:104-105`), and no manual encrypt/decrypt in handlers (`:109-110`).
- SSH least-privilege spec requires new managed-key SSH use sites to call purpose-aware helpers; legacy no-purpose helpers are compatibility shims and should not be copied (`.trellis/spec/backend/quality-guidelines.md:224-255`).
- Credential audit spec requires events to identify safe credential source/purpose/resource without storing raw passwords, private keys, decrypted executor config, terminal I/O, file contents, raw command output, Docker output, diagnostic evidence, exported payloads, or full command text (`.trellis/spec/backend/quality-guidelines.md:380-405`).
- Credential audit metadata keys/values containing `private`, `password`, `token`, `secret`, `credential`, `config`, `output`, `stream`, `command`, `content`, or `payload` must be dropped (`.trellis/spec/backend/quality-guidelines.md:398-401`).
- Logging spec forbids passwords, private keys, decrypted model-hook values, full command output when credential-sensitive, exported config payloads, Docker output, migration preflight output, executor config, and unsafe credential audit metadata (`.trellis/spec/backend/logging-guidelines.md:67-80`).
- Error-handling spec forbids exposing raw SQL, encryption details, SSH private keys, tokens, command output, SFTP/file content, Docker output, diagnostic evidence, exported config payloads, and stack-like details to clients (`.trellis/spec/backend/error-handling.md:68-74`).

### Reusable Constraints for `p4-restic-credential-resolver`

1. **Provider shape and source**
   - Keep provider identity fixed to `local` for this slice.
   - Keep existing encrypted local `Task.ExecutorConfig` JSON as the source of truth.
   - Do not add provider tables, provider reference columns, migrations, provider registry config, new env vars, deployment changes, or public API fields.
   - Keep behavior internal/backend-only unless the task scope explicitly says otherwise.

2. **Storage boundary**
   - Restic resolver work must use the decrypted value supplied by the `Task.ExecutorConfig` GORM `AfterFind` hook.
   - Do not manually encrypt/decrypt executor config in handlers or restic call sites.
   - Do not change config import/export semantics for `Task.ExecutorConfig` or introduce local-vs-external provider-reference export behavior in this slice.

3. **Runtime behavior preservation**
   - Preserve existing restic command behavior: `RESTIC_PASSWORD=''` for empty password, shell-escaped non-empty password, `sudo env` wrapping when `NeedsSudo(node)` is true, existing restic binary resolution, and current backup/restore/snapshot command shapes.
   - Preserve existing provider-backed SSH dialing and purpose constants for restic SSH access (`task_backup`, `task_restore`, `snapshot`, `snapshot_diff`, `integrity_check`, `retention`) rather than replacing them with restic password resolution.
   - A restic password resolver seam centralizes local materialization; it does not by itself remove the existing remote process/environment exposure of `RESTIC_PASSWORD`.

4. **Metadata and audit safety**
   - Any resolver result metadata must be safe-only and compatible with the existing `ResolvedCredential` style: provider/kind/source/status/stage labels only, never the repository password or decrypted executor config.
   - Do not use audit metadata keys that will be dropped by the sanitizer (`password`, `credential`, `config`, `command`, `output`, `content`, etc.).
   - Do not log raw restic password, decrypted `Task.ExecutorConfig`, command strings containing `RESTIC_PASSWORD`, restic output that may contain credentials, hostnames, endpoints, or path details prohibited by the relevant audit/log specs.

5. **Control ordering**
   - Preserve existing operation-layer gates and execution order around restic calls: auth/RBAC/ownership/step-up/grants where applicable, task/node loading, SSH scope checks, host-key resolution, SSH dial, then restic command execution.
   - Provider resolution must remain additive; it does not replace grants, step-up, RBAC, ownership, SSH key scope checks, or runtime credential audit.

6. **Scope clarity across repeated restic surfaces**
   - `ResticExecutor` is the central restic backup/restore/snapshot entry point, but snapshot indexer, snapshot diff handler, anomaly diff, retention, integrity checker, and verifier have separate password extraction/env-prefix code paths.
   - The implementation task should explicitly identify which of those surfaces are in scope and leave untouched surfaces behaviorally unchanged.

### Reusable Acceptance Criteria for `p4-restic-credential-resolver`

- [ ] A backend-internal restic credential resolver/provider seam exists with provider identity fixed to `local` and no external provider dependencies.
- [ ] The seam resolves `repository_password` from existing decrypted `Task.ExecutorConfig` JSON and does not change storage format, import/export behavior, public API responses, migrations, env vars, deployment config, or frontend behavior.
- [ ] The touched restic call sites use the resolver for repository-password materialization while preserving current command-prefix behavior: empty password emits `RESTIC_PASSWORD=''`, non-empty password is shell-escaped, `sudo env` wrapping remains equivalent, and restic binary selection remains equivalent.
- [ ] Provider/result metadata, audit metadata, errors, logs, and test assertions never include raw repository passwords, decrypted executor config, full command strings containing secrets, command output, hostnames/endpoints, or forbidden path/payload details.
- [ ] Existing SSH credential behavior for restic operations remains provider-backed and purpose-aware; managed SSH key disabled/expired/purpose/node/tag checks are not bypassed.
- [ ] Operation-layer controls remain additive and fail-closed; restic credential resolution does not run before required auth/RBAC/ownership/step-up/grant gates at protected boundaries.
- [ ] If only `ResticExecutor` is in scope, direct password helper paths in snapshot indexer, snapshot diff, anomaly diff, retention, integrity checker, and verifier are explicitly left unchanged; if included, each adopted path has equivalent behavior and safe metadata tests.
- [ ] Tests cover non-empty repository password, empty repository password, shell escaping, sudo prefix behavior where applicable, invalid JSON/error handling, metadata safety, and absence of raw password/executor config in loggable/auditable fields.
- [ ] Targeted backend tests for restic resolver/executor paths pass; full backend verification passes before commit. Frontend full check is only required if frontend files change.

### Reusable Out-of-Scope Items

- External credential providers: Vault, KMS, Boundary, Teleport, SSH CA, cloud secret managers, dynamic secrets, leases, renewal/revoke workers, provider health endpoints, fallback policy, outage semantics, provider configuration UI, or provider registry config.
- Database/storage architecture changes: provider tables, credential reference columns, migration from `Task.ExecutorConfig.repository_password` to provider refs, new ciphertext formats, or config import/export provider-reference semantics.
- Public surface changes: REST/WS API response changes, frontend UI/API client changes, deployment/Docker/Compose changes, env var docs, user-facing deployment docs, or tracked process docs.
- Other security roadmap items: terminal/session recording, replay/transcript storage, command approval/inspection/parsing, task/batch reviewer approval workflow, WebAuthn/passkeys/device trust/configurable policy UI.
- SSH credential changes already covered by P4-1/P4-2: replacing stored SSH keys/passwords, SSH CA behavior, changing managed-key scope semantics, or changing executor missing-managed-key fail-closed behavior.
- Restic behavior changes beyond the resolver seam: replacing `RESTIC_PASSWORD` remote environment-prefix execution, changing backup/restore/snapshot command semantics, changing restic output parsing, changing retention/integrity/verifier algorithms, or broadening path/host data exposure.

### External References

None. This was an internal codebase and archived Trellis research pass.

### Related Specs

- `.trellis/spec/backend/database-guidelines.md:13-17` — sensitive fields are encrypted/decrypted through model hooks.
- `.trellis/spec/backend/database-guidelines.md:84-91` — credential audit storage must not include raw credentials, decrypted executor config, terminal streams, command output, or file contents.
- `.trellis/spec/backend/database-guidelines.md:104-110` — do not expose raw secret-bearing model values and do not manually encrypt/decrypt sensitive fields in handlers.
- `.trellis/spec/backend/quality-guidelines.md:224-255` — SSH key least-privilege scope and purpose-aware helper contract.
- `.trellis/spec/backend/quality-guidelines.md:380-405` — credential-use audit event safety contract.
- `.trellis/spec/backend/quality-guidelines.md:451-520` — credential access grant ordering and sensitive-data exclusion contract.
- `.trellis/spec/backend/logging-guidelines.md:67-80` — logging exclusions for decrypted secrets, executor config, command output, endpoints, and unsafe audit metadata.
- `.trellis/spec/backend/error-handling.md:68-74` — client-facing errors must not expose secrets, command output, exported payloads, or stack-like details.

## Caveats / Not Found

- No external documentation search was performed; the request was explicitly about archived P4 tasks and existing internal seams.
- No tests were run; findings are based on static inspection only.
- No existing restic repository-password provider seam was found. Current code parses `repository_password` from decrypted `Task.ExecutorConfig` and builds `RESTIC_PASSWORD=...` command prefixes directly in several places.
- P4-2 explicitly excluded restic repository-password provider work, so this task is the first P4 slice that would cover that credential class if implemented.
