# Research: Remaining P4 security roadmap after credential broker foundation

- **Query**: Research the remaining P4 security roadmap and choose candidate executable hardening slices after the completed P4-1 credential broker foundation. Inspect Trellis archived tasks, PRDs, specs, workflow/journal as needed.
- **Scope**: internal
- **Date**: 2026-05-22

## Findings

### Files Found

| File Path | Description |
|---|---|
| `.trellis/tasks/archive/2026-05/05-21-plan-p3-p4-credential-security-hardening/prd.md` | Roadmap source separating P3 control-plane grant work from P4 architecture-level hardening. |
| `.trellis/tasks/archive/2026-05/05-22-p3-comprehensive-security-review/prd.md` | P3 completion/review task; classifies remaining architecture work as P4/out of scope for P3. |
| `.trellis/tasks/archive/2026-05/05-22-p3-comprehensive-security-review/research/p3-roadmap-completion.md` | Evidence that the main P3 grant sequence is complete and P4 remains the next security roadmap tier. |
| `.trellis/tasks/archive/2026-05/05-22-p4-credential-broker-foundation/prd.md` | Completed P4-1 scope: local-only credential provider seam for SSH auth resolution, preserving current behavior and avoiding external providers/migrations. |
| `.trellis/tasks/archive/2026-05/05-22-p4-credential-broker-foundation/research/current-credential-resolution.md` | Detailed map of credential-bearing models, SSH/executor/import/export paths, safe seam candidates, and risks. |
| `.trellis/tasks/archive/2026-05/05-22-p4-credential-broker-foundation/research/broker-foundation-patterns.md` | Pattern research for local provider, Vault/KMS, Boundary/Teleport concepts, and the smallest provider seam. |
| `.trellis/tasks/archive/2026-05/05-20-security-p2-credential-hardening/research/security-product-patterns.md` | Earlier product-pattern research for JIT grants, SSH certificates, Vault/KMS, and session recording. |
| `.trellis/tasks/archive/2026-05/05-18-security-baseline-hardening/research/secrets-management.md` | Secrets/key-management baseline, including OWASP-aligned guidance and current local encryption constraints. |
| `.trellis/workspace/xiangnan-mac/index.md` | Workspace session index showing P3 grant sequence and P4 credential broker foundation completion. |
| `.trellis/workspace/xiangnan-mac/journal-2.md` | Journal summary for Session 63, the completed P4 credential broker foundation. |
| `.trellis/workflow.md` | Trellis workflow; establishes research/task persistence, context curation, and branch-before-work principles. |
| `.trellis/spec/backend/quality-guidelines.md` | Backend security contracts for SSH key scope, credential audit, grants, and secret-safe metadata. |
| `.trellis/spec/backend/database-guidelines.md` | Backend persistence contracts for encrypted fields and GORM hook boundaries. |
| `.trellis/spec/backend/logging-guidelines.md` | Logging constraints forbidding secret, endpoint, command output, and decrypted credential leakage. |
| `.trellis/spec/backend/deployment-runtime.md` | Deployment contract keeping the official self-hosted runtime simple and avoiding new external service dependencies. |
| `backend/internal/sshutil/credential_provider.go` | P4-1 local credential provider seam and local SSH auth resolution implementation. |
| `backend/internal/sshutil/credential_provider_test.go` | P4-1 tests proving local encrypted storage, managed key scope denial, inline key/password behavior, safe metadata, and LastUsedAt updates. |
| `backend/internal/sshutil/ssh_auth.go` | Public SSH auth helpers now delegating to `DefaultCredentialProvider()`. |
| `backend/internal/task/executor/ssh_connect.go` | Task/runtime SSH dial path; still constructs auth and `ResolvedCredential` in executor-local code. |
| `backend/internal/task/executor/executor.go` | Rsync executor and executor-local private-key resolver; still has a direct resolver and temp-key materialization. |
| `backend/internal/task/executor/restic_executor.go` | Restic executor config and repository-password command-prefix materialization. |
| `backend/internal/api/handlers/policy_handler.go` | Policy create/update paths directly load decrypted `AppCredential.Config` and render app-profile hooks. |
| `backend/internal/api/handlers/file_handler.go` | File browser SSH path using shared `sshutil.BuildSSHAuthForPurpose`. |
| `backend/internal/api/handlers/docker_handler.go` | Docker volume SSH path using shared `sshutil.BuildSSHAuthForPurpose`. |
| `backend/internal/nodelogs/ssh_runner.go` | Node log SSH path using shared `sshutil.BuildSSHAuthForPurpose`. |
| `backend/internal/probe/prober.go` | Scheduled probe SSH path using shared `sshutil.BuildSSHAuthForPurpose`. |
| `backend/internal/api/handlers/node_migrate_preflight_handler.go` | Node migration preflight helper resolving credentials for audit through shared `sshutil.BuildSSHAuthForPurpose`. |

### Completed Baseline

| Roadmap area | Current status | Evidence |
|---|---|---|
| P3 control-plane JIT grants | Main P3 implementation sequence is complete: config import/export, snapshot restore, task restore, grant list/status, manual trigger, batch trigger, and batch command grant semantics. | P3 review PRD lists those completed controls (`.trellis/tasks/archive/2026-05/05-22-p3-comprehensive-security-review/prd.md:7-16`). Roadmap completion research enumerates the same current grant action coverage and route coverage (`.trellis/tasks/archive/2026-05/05-22-p3-comprehensive-security-review/research/p3-roadmap-completion.md:31-41`, `:45-84`). |
| P4-1 broker foundation | Completed as a conservative internal seam: provider identity fixed to `local`, current encrypted local DB remains the source of truth, no migrations/API/deployment changes, and no external provider. | P4-1 PRD goal and requirements (`.trellis/tasks/archive/2026-05/05-22-p4-credential-broker-foundation/prd.md:3-19`), out-of-scope boundaries (`:33-40`), and decision (`:46-58`). Workspace session index records Session 63 / commit `c36a479` (`.trellis/workspace/xiangnan-mac/index.md:31-34`); journal summary says it added a local provider seam for SSH auth resolution (`.trellis/workspace/xiangnan-mac/journal-2.md:142-150`). |
| Local provider code | `sshutil` now exposes `CredentialProviderLocal = "local"`, `CredentialProvider`, `LocalCredentialProvider`, and `DefaultCredentialProvider()`. | `backend/internal/sshutil/credential_provider.go:13-24`. Public helpers delegate to the default provider (`backend/internal/sshutil/ssh_auth.go:34-60`). |
| Local provider behavior | Managed SSH keys, inline private keys, and passwords resolve through local provider metadata; managed keys still enforce disabled/expired/purpose/node/tag scope and update `LastUsedAt`. | `backend/internal/sshutil/credential_provider.go:26-60`, `:62-94`; tests at `backend/internal/sshutil/credential_provider_test.go:16-118`, `:120-255`. |

### Remaining P4 Roadmap Items

The canonical P4 list from planning remains architecture-level:

| P4 item | Roadmap source | Current one-PR fit after P4-1 |
|---|---|---|
| Vault/KMS/external secret broker or provider references | Planning PRD lists provider health, leases, fallback, and import/export semantics (`.trellis/tasks/archive/2026-05/05-21-plan-p3-p4-credential-security-hardening/prd.md:23-29`, `:69-77`). | Not ready as a direct external integration PR. P4-1 established only a local provider; archived research says external provider work needs provider references, health, leases, fallback, import/export semantics, and outage behavior (`.trellis/tasks/archive/2026-05/05-22-p4-credential-broker-foundation/research/current-credential-resolution.md:217-227`). |
| SSH certificates / external CA | Planning PRD lists CA, host trust rollout, principals, TTL, revocation, and signing (`.trellis/tasks/archive/2026-05/05-21-plan-p3-p4-credential-security-hardening/prd.md:23-29`, `:69-77`). | Not ready as a small implementation slice because it requires host `sshd` trust rollout/CA lifecycle, as also described in earlier product-pattern research (`.trellis/tasks/archive/2026-05/05-20-security-p2-credential-hardening/research/security-product-patterns.md:21-33`, `:62-64`). |
| Terminal/session recording | Planning PRD lists opt-in recording with retention/object storage/playback RBAC/privacy warnings (`.trellis/tasks/archive/2026-05/05-21-plan-p3-p4-credential-security-hardening/prd.md:23-29`, `:69-77`). | Not a next small hardening slice because current audit/grant contracts intentionally avoid terminal streams/output and recording would create a sensitive evidence store (`.trellis/spec/backend/quality-guidelines.md:397-405`, `.trellis/tasks/archive/2026-05/05-20-security-p2-credential-hardening/research/security-product-patterns.md:46-55`). |
| Command-level approval/inspection | Planning PRD lists shell command parsing and allow/deny policies (`.trellis/tasks/archive/2026-05/05-21-plan-p3-p4-credential-security-hardening/prd.md:23-29`, `:69-77`). | Not a next small slice because current credential audit/grant metadata forbids storing command text/output (`.trellis/spec/backend/quality-guidelines.md:397-400`, `:467-473`). |
| WebAuthn/passkeys/device trust/configurable step-up/grant policy UI | Planning PRD lists advanced step-up and configurable policy after the current model stabilizes (`.trellis/tasks/archive/2026-05/05-21-plan-p3-p4-credential-security-hardening/prd.md:23-29`, `:69-77`). | Larger auth/policy architecture work; not tied directly to the newly created local credential provider seam. |
| Provider seam expansion across remaining credential materialization paths | P4-1 research identified safe seam candidates beyond the first SSH helper path: executor SSH dial, rsync temp-key materialization, restic repository password helpers, config import/export, node/SSH key handlers, and app credential rendering (`.trellis/tasks/archive/2026-05/05-22-p4-credential-broker-foundation/research/current-credential-resolution.md:273-286`). | Strongest near-term P4 shape because it stays local-only/no-migration/no-deployment while incrementally reducing scattered direct credential materialization. |

### Code Patterns

#### 1. P4-1 covered shared `sshutil` helpers, not every SSH materialization path

- The provider seam is currently in `sshutil`: `CredentialProvider` and `LocalCredentialProvider` are defined in `backend/internal/sshutil/credential_provider.go:13-24`.
- `ResolveKeyContentForPurpose` in the local provider handles preloaded managed SSH key, DB-loaded managed SSH key by `SSHKeyID`, inline `node.PrivateKey`, and empty-key cases (`backend/internal/sshutil/credential_provider.go:26-60`).
- `BuildSSHAuthWithKeyForPurpose` handles password and key auth, uses provider resolution for key auth, validates/parses keys, marks managed keys as used, and returns safe `ResolvedCredential` metadata (`backend/internal/sshutil/credential_provider.go:62-94`).
- Public helper functions delegate to `DefaultCredentialProvider()` (`backend/internal/sshutil/ssh_auth.go:34-60`). This means handler/background paths that call `sshutil.BuildSSHAuthForPurpose` already use the P4-1 seam.
- Several non-task SSH paths already use the shared provider-backed helper:
  - file browser: `backend/internal/api/handlers/file_handler.go:355-380`;
  - Docker volumes: `backend/internal/api/handlers/docker_handler.go:118-135`;
  - node logs: `backend/internal/nodelogs/ssh_runner.go:27-45`;
  - probes: `backend/internal/probe/prober.go:259-277`;
  - migration-preflight audit helper: `backend/internal/api/handlers/node_migrate_preflight_handler.go:389-395`.

#### 2. Executor runtime SSH still has duplicated credential resolution

- `executor.DialSSHForNodePurpose` builds auth through executor-local `resolveSSHAuthMethodsForPurpose`, then resolves host key and dials SSH (`backend/internal/task/executor/ssh_connect.go:22-57`).
- The executor-local resolver branches on `node.AuthType` and builds `ResolvedCredential` directly (`backend/internal/task/executor/ssh_connect.go:66-99`). Password metadata currently lacks the `Provider` field (`backend/internal/task/executor/ssh_connect.go:89-95`).
- Rsync/private-key resolution is also direct: `resolveNodePrivateKeyForPurpose` checks preloaded `node.SSHKey`, returns an error when `SSHKeyID` exists but the key is not preloaded, then falls back to inline `node.PrivateKey` (`backend/internal/task/executor/executor.go:522-543`). The resulting metadata also lacks `Provider` (`backend/internal/task/executor/executor.go:535-540`, `:546-555`).
- Prior P4-1 research flags this as the executor/shared resolver asymmetry: shared `sshutil` can DB-load `SSHKeyID`, while executor code fails unless `Node.SSHKey` is preloaded (`.trellis/tasks/archive/2026-05/05-22-p4-credential-broker-foundation/research/current-credential-resolution.md:91-110`, `:306-310`).

#### 3. App credential and profile-hook paths still read decrypted config directly

- Policy create renders profile hooks by loading `model.AppCredential` directly, relying on `AfterFind` decryption, unmarshalling `cred.Config`, then passing the map to `profile.RenderHooks` (`backend/internal/api/handlers/policy_handler.go:257-274`).
- Policy update repeats the same direct path (`backend/internal/api/handlers/policy_handler.go:583-598`).
- P4-1 pattern research identified app credential config loading for policy hook rendering as the second-smallest seam candidate after SSH credential resolution (`.trellis/tasks/archive/2026-05/05-22-p4-credential-broker-foundation/research/broker-foundation-patterns.md:175-178`).
- Existing research caveat: built-in app profile templates can include passwords in generated hook commands, so a no-behavior-change provider seam should wrap current materialization rather than silently changing hook storage/execution semantics (`.trellis/tasks/archive/2026-05/05-22-p4-credential-broker-foundation/research/broker-foundation-patterns.md:191-195`).

#### 4. Restic repository passwords are still materialized from task executor config

- `ResticConfig` stores `RepositoryPassword` in `Task.ExecutorConfig` JSON (`backend/internal/task/executor/restic_executor.go:18-23`).
- Restic execution parses decrypted `Task.ExecutorConfig` (`backend/internal/task/executor/restic_executor.go:49-52`, `:383-391`).
- `buildCommandPrefix` converts the repository password into `RESTIC_PASSWORD=...` command prefix, with optional sudo env wrapping (`backend/internal/task/executor/restic_executor.go:394-410`).
- Current credential-resolution research lists repeated restic password env-prefix helpers as a safe seam candidate, with the constraint that remote process/environment exposure remains unless a larger architecture changes (`.trellis/tasks/archive/2026-05/05-22-p4-credential-broker-foundation/research/current-credential-resolution.md:259-264`, `:281-283`, `:311-316`).

#### 5. External provider integrations require import/export/fallback semantics not yet represented in code

- P4-1 intentionally avoided provider tables, migrations, env vars, provider health endpoints, leases, renew/revoke workers, fallback policy, and config import/export provider-reference semantics (`.trellis/tasks/archive/2026-05/05-22-p4-credential-broker-foundation/prd.md:11-19`, `:33-40`).
- Pattern research explicitly maps Vault/KMS/Boundary/Teleport concepts to future seams but says the local provider is the best fit for the foundation slice and should not introduce clients, SDKs, env vars, migrations, or deployment changes (`.trellis/tasks/archive/2026-05/05-22-p4-credential-broker-foundation/research/broker-foundation-patterns.md:90-99`, `:191-195`).

### Candidate Executable Hardening Slices

Ranked by fit with the completed P4-1 foundation, implementation locality, and ability to preserve existing deployment/API behavior:

| Rank | Candidate slice | Candidate boundary | Why it fits now | Main files |
|---:|---|---|---|---|
| 1 | **Executor SSH local-provider adoption** | Route task/runtime SSH credential resolution and rsync private-key materialization through the existing local provider seam or provider-compatible helper path, preserving current encrypted local storage and existing preloaded-key behavior. | It directly extends P4-1 to the highest-volume credential-use path (task executors) without external services, migrations, public API changes, or deployment changes. It also aligns safe metadata with `Provider=local` across runtime audit paths. | `backend/internal/task/executor/ssh_connect.go`, `backend/internal/task/executor/executor.go`, `backend/internal/sshutil/credential_provider.go`, executor/manager tests. |
| 2 | **AppCredential local-provider seam for profile hook rendering** | Introduce a local provider/resolver wrapper for app credential config materialization used by policy create/update and app-credential cascade rendering; return decrypted config only in-memory plus safe metadata. | It is localized and was explicitly identified as the next-smallest seam after SSH. It preserves current GORM hook behavior while reducing direct handler-level credential reads. | `backend/internal/api/handlers/policy_handler.go`, `backend/internal/api/handlers/app_credential_handler.go`, `backend/internal/profile/profile.go`, app credential/policy tests. |
| 3 | **Restic repository-password local resolver seam** | Wrap restic repository password extraction/materialization behind a local resolver used by `ResticExecutor` first; keep command-prefix behavior unchanged and metadata safe. | It targets a major non-SSH secret in unattended backup/restore flows. It is more spread out than rank 1 because snapshot/indexer/anomaly/retention/verifier paths also parse restic config, so a first slice should be explicit about which call sites it covers. | `backend/internal/task/executor/restic_executor.go`, plus later `backend/internal/snapshot/indexer.go`, `backend/internal/anomaly/snapshot_diff.go`, `backend/internal/task/integrity_checker.go`, `backend/internal/task/retention.go`, `backend/internal/task/verifier/verifier.go`. |
| 4 | **Provider-reference semantics design task before external providers** | A planning/research/ADR slice to define local-vs-external provider references, import/export behavior, lease/health/fallback semantics, and outage behavior before any Vault/KMS code. | External Vault/KMS is on the P4 roadmap but not yet executable safely as code. A design slice is executable as planning and can prevent incompatible schemas/API shapes. | Archived P4 PRDs/research, `.trellis/spec/backend/*`, config import/export docs; no code by default. |

### Candidate Details

#### Candidate 1 — Executor SSH local-provider adoption

Observed current boundary:

- `DialSSHForNodePurpose` is the central executor SSH dial path (`backend/internal/task/executor/ssh_connect.go:22-57`).
- It uses `resolveSSHAuthMethodsForPurpose` rather than `sshutil.BuildSSHAuthForPurpose` (`backend/internal/task/executor/ssh_connect.go:35-36`, `:66-99`).
- Rsync private-key materialization uses `resolveNodePrivateKeyForPurpose` directly (`backend/internal/task/executor/executor.go:522-543`).

Executable slice shape from existing facts:

- Keep provider identity fixed to `local` and keep no migration/no env/no API/deployment changes, matching P4-1 PRD requirements (`.trellis/tasks/archive/2026-05/05-22-p4-credential-broker-foundation/prd.md:11-19`).
- Preserve executor preload behavior or explicitly keep fail-closed behavior for non-preloaded `SSHKeyID`; prior research identified this as a risk area (`.trellis/tasks/archive/2026-05/05-22-p4-credential-broker-foundation/research/current-credential-resolution.md:306-310`).
- Preserve current runtime audit safe fields: action/purpose/kind/source/SSH key ID/node ID/outcome/stage, with no secrets or command output (`.trellis/spec/backend/quality-guidelines.md:380-447`).

Why it is the strongest next slice:

- It extends the exact P4-1 seam rather than starting a different architecture track.
- It touches internal backend code only and can be regression-tested with focused executor/sshutil tests.
- It increases consistency of safe provider metadata on task/runtime credential use.

#### Candidate 2 — AppCredential local-provider seam for profile hook rendering

Observed current boundary:

- Policy create directly loads `model.AppCredential`, unmarshals decrypted `Config`, and renders hooks (`backend/internal/api/handlers/policy_handler.go:257-274`).
- Policy update repeats this flow (`backend/internal/api/handlers/policy_handler.go:583-598`).
- AppCredential config itself is encrypted/decrypted through model hooks and sanitized for API responses; P4-1 research maps it as a broker-sensitive storage boundary (`.trellis/tasks/archive/2026-05/05-22-p4-credential-broker-foundation/research/broker-foundation-patterns.md:68-76`).

Executable slice shape from existing facts:

- Add a local-only app credential resolver that delegates to the existing GORM model hook path and returns an in-memory config map plus safe metadata such as provider/kind/source.
- Keep public API responses and hook-rendered behavior unchanged.
- Keep app profile password rendering caveat explicit because existing templates can render app passwords into hook commands (`.trellis/tasks/archive/2026-05/05-22-p4-credential-broker-foundation/research/broker-foundation-patterns.md:191-195`).

Why it is a viable follow-up:

- It is localized to policy/app credential code.
- It advances the credential broker shape beyond SSH without external dependencies.
- It prepares future provider-reference semantics for app-aware backup credentials.

#### Candidate 3 — Restic repository-password local resolver seam

Observed current boundary:

- Restic repository password is a field in encrypted `Task.ExecutorConfig` (`backend/internal/task/executor/restic_executor.go:18-23`).
- It is parsed from decrypted config and turned into a shell env prefix (`backend/internal/task/executor/restic_executor.go:383-410`).
- Current research identifies remote env-prefix exposure as an existing limitation (`.trellis/tasks/archive/2026-05/05-22-p4-credential-broker-foundation/research/current-credential-resolution.md:259-264`, `:311-316`).

Executable slice shape from existing facts:

- Bound the first PR to `ResticExecutor` if chosen; broader snapshot/indexer/anomaly/retention/verifier unification is likely a later slice.
- Preserve `RESTIC_PASSWORD` command behavior and shell escaping; a provider seam alone does not eliminate remote process/environment exposure.
- Return or record only safe metadata, never repository passwords or executor config.

Why it is lower than rank 1/2:

- It affects more runtime surfaces and repeated helper paths.
- It is easier to over-scope into provider-reference/import-export semantics, which P4-1 explicitly excluded.

### Deferred / Not a One-PR Implementation Slice Right Now

| Deferred item | Why deferred by current evidence |
|---|---|
| Direct Vault/KMS/external secret provider integration | Requires provider config, references, health, leases, fallback, import/export behavior, outage semantics, and docs/deployment changes; P4-1 explicitly excluded all of these (`.trellis/tasks/archive/2026-05/05-22-p4-credential-broker-foundation/prd.md:33-40`). |
| SSH CA / external CA | Requires remote host trust rollout, principal mapping, TTL/revocation, signing/CA lifecycle, and migration/fallback behavior (`.trellis/tasks/archive/2026-05/05-21-plan-p3-p4-credential-security-hardening/prd.md:69-77`). |
| Terminal/session recording | Adds a sensitive evidence store containing terminal output; current credential audit/grant specs forbid terminal streams/output in audit/grant metadata (`.trellis/spec/backend/quality-guidelines.md:397-405`, `:467-473`). |
| Command approval/inspection | Requires command parsing/allow-deny semantics while current grant/audit contracts forbid command text/output storage (`.trellis/spec/backend/quality-guidelines.md:397-400`, `:467-473`). |
| WebAuthn/passkeys/device trust/configurable policy UI | Larger auth/policy work; not directly enabled by the local credential provider seam and requires frontend/auth/migration/product decisions. |
| Policy/risk-driven grant requirements | P3 research classifies it as a later P3/P4 boundary decision after broader operation coverage (`.trellis/tasks/archive/2026-05/05-22-p3-comprehensive-security-review/research/p3-roadmap-completion.md:86-97`). It is not a provider/broker continuation slice. |

### Related Specs

- `.trellis/spec/backend/quality-guidelines.md:224-255` — SSH key least-privilege scope and purpose-aware auth-helper contract.
- `.trellis/spec/backend/quality-guidelines.md:380-447` — credential-use audit event contract and forbidden metadata categories.
- `.trellis/spec/backend/quality-guidelines.md:451-520` — credential access grant contract; still relevant because provider resolution must remain after auth/RBAC/ownership/step-up/grants where applicable.
- `.trellis/spec/backend/database-guidelines.md` — sensitive fields must remain behind GORM model hooks; handlers should not manually encrypt/decrypt or expose raw model values.
- `.trellis/spec/backend/logging-guidelines.md` — no logging of decrypted secrets, executor config, command output, endpoints, proxies, or host-sensitive details.
- `.trellis/spec/backend/deployment-runtime.md` — self-hosted runtime should not gain external Vault/KMS/CA dependencies in small local-provider slices.
- `.trellis/workflow.md:41-73` — Trellis task structure/context curation and validation commands for implementation/check phases.

### External References

No new external references were used for this research. Existing archived research already contains external pattern references for OWASP, Vault/KMS, Boundary, Teleport, SSH certificates, and session recording:

- `.trellis/tasks/archive/2026-05/05-18-security-baseline-hardening/research/secrets-management.md:93-113`
- `.trellis/tasks/archive/2026-05/05-20-security-p2-credential-hardening/research/security-product-patterns.md:16-55`
- `.trellis/tasks/archive/2026-05/05-22-p4-credential-broker-foundation/research/broker-foundation-patterns.md:100-109`

## Caveats / Not Found

- This research did not run tests and did not modify code.
- No P4 task after `p4-credential-broker-foundation` was found in the archive; the current active task is the selection/planning task.
- No external provider configuration, provider tables, provider reference columns, lease/renew/revoke workers, provider health endpoints, or CA/session-recording storage were found in current code; that matches P4-1 out-of-scope boundaries.
- `ResolvedCredential.Provider` exists in `sshutil` metadata but current credential audit events store only kind/source/IDs; provider metadata persistence would need to remain sanitizer-compatible if any future slice exposes it.
- Executor/shared resolver behavior is not identical today: shared `sshutil` can DB-load a managed key by `SSHKeyID`, while executor-local code expects `Node.SSHKey` to be preloaded and fails closed otherwise. Any executor-provider slice must preserve or explicitly account for that existing behavior.
- Restic repository-password handling remains command-environment based. A local resolver seam can centralize materialization but does not itself remove remote process/environment exposure.
