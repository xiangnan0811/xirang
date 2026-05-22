# Research: minimal credential broker/provider foundation patterns

- **Query**: Research minimal credential broker/provider foundation patterns for task `.trellis/tasks/05-22-p4-credential-broker-foundation`; compare Vault/KMS provider abstraction, Teleport/Boundary credential broker ideas, and local encrypted fallback patterns; map to Xirang constraints: self-hosted, existing local encrypted DB storage, no external provider in this slice, no migration, no API/deployment behavior change, no secret exposure. Include 2-3 feasible approaches and recommend the smallest local-provider seam.
- **Scope**: mixed
- **Date**: 2026-05-22

## Findings

### Files Found

| File Path | Description |
|---|---|
| `backend/internal/secure/crypto.go` | Existing encryption primitive: loads `DATA_ENCRYPTION_KEY`, derives v2/v1 keys, and provides idempotent encrypt/decrypt helpers. |
| `backend/internal/model/models.go` | Central sensitive-field model hooks for `AppCredential`, `Integration`, `Task.ExecutorConfig`, `SSHKey`, `Node`, and `User`; also defines credential audit and grant models. |
| `backend/internal/sshutil/ssh_auth.go` | Current SSH credential resolution seam: purpose-aware helpers return auth material plus safe `ResolvedCredential` metadata. |
| `backend/internal/sshutil/scope.go` | Purpose constants and managed SSH key scope validation for disabled/expired/purpose/node/tag constraints. |
| `backend/internal/task/executor/ssh_connect.go` | Task/runtime SSH dial boundary; resolves auth, performs host-key handling, dials, and writes credential audit evidence. |
| `backend/internal/task/executor/executor.go` | Rsync executor resolves SSH key material, writes temporary key files for `rsync -e ssh`, and emits runtime credential audit. |
| `backend/internal/task/executor/restic_executor.go` | Restic executor parses encrypted `Task.ExecutorConfig` and injects repository password into remote command environment. |
| `backend/internal/task/executor/rclone_executor.go` | Rclone executor parses encrypted `Task.ExecutorConfig` and uses purpose-aware SSH dial helpers. |
| `backend/internal/task/executor/command_executor.go` | Command executor uses purpose-aware SSH dial helper for task commands. |
| `backend/internal/task/hook.go` | Pre/post hook execution uses purpose-aware SSH dial helper. |
| `backend/internal/api/handlers/app_credential_handler.go` | CRUD surface for saved app credentials; responses use sanitized config and `has_password`. |
| `backend/internal/api/handlers/policy_handler.go` | Policy create/update loads decrypted `AppCredential.Config` and renders app-profile hooks. |
| `backend/internal/profile/profile.go` | Built-in app profiles and hook templates; templates include password placeholders for some engines. |
| `backend/internal/credentialaudit/audit.go` | Credential audit writer that stores safe actor/resource metadata and drops sensitive keys/values. |
| `backend/internal/api/handlers/credential_access_grant.go` | Existing row-backed JIT grant pattern for high-risk credential-use boundaries. |
| `backend/internal/api/handlers/config_handler.go` | Config export/import behavior for secret-bearing fields, including `include_secrets` gating and sensitivity filtering. |
| `backend/internal/api/router.go` | Route wiring for app credentials, credential audit, grants, settings, and config import/export. |
| `backend/internal/middleware/rbac.go` | RBAC permissions: app credential read/write is currently admin-only via permissions map. |
| `.trellis/spec/backend/database-guidelines.md` | Backend database contracts for encrypted fields, migration parity, credential audit storage, and config export/import behavior. |
| `.trellis/spec/backend/quality-guidelines.md` | Backend security/quality contracts for sanitization, RBAC, SSH key least privilege, credential audit, and grants. |
| `.trellis/spec/backend/deployment-runtime.md` | Deployment contract that keeps official self-hosted runtime simple and fixed; no external secret service is part of the current deployment shape. |

### Code Patterns

#### 1. Existing local encrypted DB storage is already the source of truth

- `backend/internal/secure/crypto.go:43-70` loads encryption keys from `DATA_ENCRYPTION_KEY` / `DATA_ENCRYPTION_LEGACY_KEY`, with a development-only default. This matches the current self-hosted local storage model.
- `backend/internal/secure/crypto.go:128-146` exposes idempotent `EncryptIfNeeded` / `DecryptIfNeeded`, so callers can store or read values without duplicating prefix checks.
- `backend/internal/secure/crypto.go:148-174` writes v2 encrypted values and detects v1/v2 prefixes on decrypt; `backend/internal/secure/crypto.go:192-249` uses AES-GCM with a nonce and returns prefixed ciphertext.
- Sensitive model hooks are centralized rather than repeated in handlers:
  - `AppCredential.Config` encrypted/decrypted in `backend/internal/model/models.go:151-184`; `SanitizedConfig()` removes `password` at `backend/internal/model/models.go:187-194`.
  - `Integration.Endpoint`, `Integration.Secret`, and `Integration.ProxyURL` encrypted/decrypted in `backend/internal/model/models.go:212-260`.
  - `Task.ExecutorConfig` encrypted/decrypted in `backend/internal/model/models.go:326-347`.
  - `SSHKey.PrivateKey` encrypted/decrypted in `backend/internal/model/models.go:630-651`.
  - `Node.Password` and `Node.PrivateKey` encrypted/decrypted in `backend/internal/model/models.go:654-687`.
  - `User.TOTPSecret` and `User.RecoveryCodes` encrypted/decrypted in `backend/internal/model/models.go:692-725`.
- API response sanitization is already part of the data model: `Node.Sanitized()` strips node password/private key and embedded SSH key private key at `backend/internal/model/models.go:45-56`.
- Related spec contract: `.trellis/spec/backend/database-guidelines.md:14-17` states sensitive fields are encrypted/decrypted through model hooks and `backend/internal/secure/crypto.go`; `.trellis/spec/backend/database-guidelines.md:105-111` says not to expose raw model values containing secrets and not to manually encrypt/decrypt sensitive fields in handlers.

#### 2. Current SSH resolution is close to a local credential provider seam

- `sshutil.ResolvedCredential` is a safe metadata carrier, not secret material: `backend/internal/sshutil/scope.go:57-63` has `Kind`, `Source`, and optional `KeyID` only.
- `ResolveKeyContentForPurpose` resolves managed SSH key content, inline node private key content, and safe source metadata; managed keys are validated for scope before returning material (`backend/internal/sshutil/ssh_auth.go:34-67`).
- `BuildSSHAuthWithKeyForPurpose` converts resolved key/password material into `ssh.AuthMethod`, returns safe credential metadata, and marks managed keys as last used (`backend/internal/sshutil/ssh_auth.go:87-114`, `backend/internal/sshutil/ssh_auth.go:125-139`).
- Purpose normalization and allowed purpose list validation are in `backend/internal/sshutil/scope.go:13-55` and `backend/internal/sshutil/scope.go:65-100`.
- Managed SSH key least-privilege checks happen in `ValidateSSHKeyScope` / `ValidateSSHKeyPurpose` (`backend/internal/sshutil/scope.go:128-153`), including disabled/expired/purpose checks.
- Runtime SSH dial boundaries already pass purpose strings:
  - Command tasks: `backend/internal/task/executor/command_executor.go:28-33` uses `sshutil.PurposeTaskCommand`.
  - Restic backup/restore/snapshot operations: `backend/internal/task/executor/restic_executor.go:54-58`, `backend/internal/task/executor/restic_executor.go:139-143`, `backend/internal/task/executor/restic_executor.go:293-297`, and `backend/internal/task/executor/restic_executor.go:321-325`.
  - Rclone backup/restore: `backend/internal/task/executor/rclone_executor.go:59-63` and `backend/internal/task/executor/rclone_executor.go:101-105`.
  - Hooks: `backend/internal/task/hook.go:17-20` uses `sshutil.PurposeTaskHook`.
- `DialSSHForNodePurpose` in `backend/internal/task/executor/ssh_connect.go:22-57` is the current central SSH runtime boundary: resolve auth, resolve host key callback, dial, and write runtime credential audit with safe metadata.

Implication for a broker foundation: for SSH credentials, the smallest seam can wrap/delegate to these existing purpose-aware helpers instead of inventing a new storage contract.

#### 3. App credentials and executor config are direct storage reads today

- App credentials are stored as encrypted JSON in `AppCredential.Config`; `appCredentialRequest.Password` is accepted on create/update (`backend/internal/api/handlers/app_credential_handler.go:34-44`), config JSON is built in `backend/internal/api/handlers/app_credential_handler.go:80-99`, and responses call `sanitizeAppCredential()` / `SanitizedConfig()` (`backend/internal/api/handlers/app_credential_handler.go:46-78`).
- Policy create/update currently loads `model.AppCredential` directly, relies on GORM `AfterFind` to decrypt `Config`, unmarshals it to a map, then renders pre/post hooks (`backend/internal/api/handlers/policy_handler.go:257-284` and `backend/internal/api/handlers/policy_handler.go:583-608`).
- Built-in profile templates include password placeholders in shell command templates, for example MySQL/PostgreSQL/MongoDB/Redis templates at `backend/internal/profile/profile.go:55-85` and Docker variants at `backend/internal/profile/profile.go:95-125`. `RenderHooks()` renders these templates from decrypted config at `backend/internal/profile/profile.go:145-174`.
- Restic executor config contains `repository_password` (`backend/internal/task/executor/restic_executor.go:18-23`), is parsed from `Task.ExecutorConfig` (`backend/internal/task/executor/restic_executor.go:383-391`), and is inserted into a remote command environment prefix (`backend/internal/task/executor/restic_executor.go:394-410`).
- Config export only includes node password/private key, SSH private key, and task `executor_config` when `include_secrets=true` (`backend/internal/api/handlers/config_handler.go:125-148` and `backend/internal/api/handlers/config_handler.go:204-206`). Sensitive settings are omitted without `include_secrets` via `configExportSettingLooksSensitive()` (`backend/internal/api/handlers/config_handler.go:704-724`).

Implication for a broker foundation: app-credential and executor-config support can stay local and in-memory first. A no-migration slice should avoid adding credential reference columns or changing how existing policies/tasks serialize secrets.

#### 4. Existing grant and audit patterns map well to broker semantics

- `CredentialAuditEvent` is explicitly safe-only: comments at `backend/internal/model/models.go:421-423` say it must not contain raw secrets, terminal streams, command output, or executor config.
- `CredentialAccessGrant` is safe-only row-backed authorization state: comments at `backend/internal/model/models.go:446-448` say it stores actor/resource identifiers, reason text, lifecycle state, and timestamps.
- Grant request/validation code enforces step-up and role/resource checks before creating active grants (`backend/internal/api/handlers/credential_access_grant.go:476-503`, `backend/internal/api/handlers/credential_access_grant.go:513-553`).
- Grant enforcement for terminal WebSocket checks a matching active grant before continuing to node load / SSH credential resolution (`backend/internal/api/handlers/credential_access_grant.go:632-646`).
- Credential audit writer sanitizes metadata and drops forbidden keys/values containing markers such as `private`, `password`, `token`, `secret`, `credential`, `config`, `output`, `stream`, `command`, `content`, or `payload` (`backend/internal/credentialaudit/audit.go:208-280`) and redacts output-bearing error text (`backend/internal/credentialaudit/audit.go:291-310`).
- Route wiring keeps credential audit and most grant lifecycle routes admin-only (`backend/internal/api/router.go:263-270`), while task/batch grant routes reuse task permissions (`backend/internal/api/router.go:271-273`). App credential read/write permissions are granted only to admin in `backend/internal/middleware/rbac.go:8-42`.
- Related spec contract: `.trellis/spec/backend/quality-guidelines.md:380-447` defines credential-use audit event rules; `.trellis/spec/backend/quality-guidelines.md:451-520` defines credential access grant rules, including “row-backed authorization records, not bearer tokens” and “enforcement must happen before credential resolution.”

Implication for a broker foundation: the “broker” part can reuse existing grant/audit vocabulary: request has actor/purpose/resource, resolution returns safe credential source metadata, audit stores outcome and safe context only.

### Pattern / Tool Comparison Mapped to Xirang Constraints

| Pattern / Tool | Relevant idea | Fit for this slice | Carry-forward seam | Avoid in this slice |
|---|---|---|---|---|
| Vault secrets provider / dynamic secrets | Vault models secrets as provider-backed values that can carry leases; database secrets can issue dynamic credentials or rotate static-role passwords. | Conceptual only. Xirang’s current slice explicitly has no external provider, no deployment change, and local encrypted DB is already the source of truth. | A provider result can reserve safe metadata fields such as `provider="local"`, `source`, optional expiry/lease metadata, and a best-effort cleanup hook that is a no-op for local. | Do not add Vault client config, env vars, token handling, background renew/revoke workers, or migration columns now. |
| KMS / envelope-encryption provider abstraction | KMS separates key management from ciphertext storage; applications can use key IDs/data keys while encrypted data remains local. | Conceptual only. Existing `DATA_ENCRYPTION_KEY` + `secure.EncryptIfNeeded` is the current encryption contract. | Keep encryption at model hooks; a future provider could decrypt/encrypt at the same storage boundary without changing API response behavior. | Do not add cloud KMS SDK, key IDs, new ciphertext formats, or deployment settings in this foundation slice. |
| Boundary credential store/library/credential resources | Separates where credentials live, how targets request them, and how sessions receive them. | Good conceptual match for “broker as internal resolver,” but Xirang should not become a session proxy in this slice. | Model internal requests as `(purpose, resource, credential source)` and return only safe source metadata plus in-memory material to the caller. | Do not add proxy/session brokering, target resources, credential library tables, or user-visible provider selection. |
| Teleport JIT access / role and resource access requests | Temporary, role/resource-scoped access grants before privileged operations. | Already partly implemented through `CredentialAccessGrant`, step-up, RBAC, ownership, purpose-aware SSH key checks, and credential audit. | Keep broker resolution after grant checks where grants apply; carry existing action/purpose/resource tuple into provider requests. | Do not issue bearer grant tokens or broaden grant state beyond existing rows. |
| Local encrypted fallback provider | A local provider reads existing encrypted DB models through GORM hooks and returns in-memory material plus safe metadata. | Best fit. It satisfies self-hosted, no external provider, no migration, no API/deployment behavior change, and no new secret exposure. | Introduce an internal `local` provider that delegates to existing helpers and model hooks; keep it hardcoded/unconfigured for now. | Do not create credential URI refs or rewrite existing storage formats in this slice. |

### External References

- [HashiCorp Boundary: Credential store resource](https://developer.hashicorp.com/boundary/docs/concepts/domain-model/credential-stores) — Boundary separates credential storage as a domain resource; useful as a conceptual model for separating storage/provider from target operations.
- [HashiCorp Boundary: Credential library resource](https://developer.hashicorp.com/boundary/docs/concepts/domain-model/credential-libraries) — Boundary’s library concept maps to “which credentials can be supplied for a target,” but Xirang should keep this internal and local for this slice.
- [HashiCorp Boundary: Credential resource](https://developer.hashicorp.com/boundary/docs/concepts/domain-model/credentials) — Static credential resources are a conceptual analogue to Xirang’s existing `SSHKey`, `Node` inline credentials, `AppCredential`, and encrypted `Task.ExecutorConfig`.
- [HashiCorp Vault: Lease, Renew, and Revoke](https://developer.hashicorp.com/vault/docs/concepts/lease) — Relevant for future dynamic provider result shape; local provider can expose no lease/cleanup requirement.
- [HashiCorp Vault: Database secrets engine](https://developer.hashicorp.com/vault/docs/secrets/databases) — Relevant because Vault can generate dynamic database credentials and rotate static roles; this slice should not add those behaviors.
- [AWS KMS key concepts](https://docs.aws.amazon.com/kms/latest/developerguide/concepts.html) — Relevant as an envelope/key-management separation pattern; Xirang currently uses local app-level encryption via `DATA_ENCRYPTION_KEY`.
- [Teleport: Just-in-Time Access Requests](https://goteleport.com/docs/identity-governance/access-requests/) — Relevant to grant-before-use patterns; Teleport distinguishes role and resource access requests and temporary elevated access.
- [Teleport Role Reference](https://goteleport.com/docs/reference/access-controls/roles/) — Relevant to RBAC/resource scoping concepts; Xirang already has RBAC plus ownership and purpose-aware SSH key scopes.

## Feasible Approaches

### Approach 1 — Recommended: smallest local-provider seam around existing helpers

Shape:
- Add an internal credential-provider seam with only a local implementation registered/constructed in code.
- For SSH credentials, delegate to existing purpose-aware helpers (`sshutil.BuildSSHAuthWithKeyForPurpose`, `sshutil.BuildSSHAuthForPurpose`, or the existing lower-level `ResolveKeyContentForPurpose`) so disabled/expired/purpose/node/tag scope and `last_used_at` behavior stay unchanged.
- For app credentials, wrap the existing `db.First(&model.AppCredential{}, id)` + GORM-decrypted `Config` + JSON unmarshal pattern and return a config map plus safe metadata such as `provider=local`, `kind=app_credential`, `source=app_credential_id=<id>`.
- For executor config, defer large changes; keep `Task.ExecutorConfig` on the model hook and only expose it through the provider if a touched use site needs it.

Why this is smallest:
- No schema migration: existing IDs/fields remain the reference.
- No API behavior change: handlers can continue returning existing response DTOs.
- No deployment behavior change: provider is hardcoded local and adds no env vars or services.
- No external provider: only wraps existing local encrypted DB storage.
- No new secret exposure: material is returned only to existing in-memory call sites; logs/audit continue to use safe `ResolvedCredential`-style metadata.

Suggested seam boundary:
- Put the seam between current call sites and existing helpers, not below `secure.EncryptIfNeeded` and not above route/API DTOs.
- Preserve purpose as a required request field wherever SSH material is resolved.
- Preserve safe metadata shape similar to `sshutil.ResolvedCredential` for audit and future providers.

### Approach 2 — Broker facade over local provider with existing grants/audit as policies

Shape:
- Internal `Broker.Resolve(ctx, request)` validates any already-required grant/step-up context, calls the local provider, and writes credential audit outcome.
- Request shape carries actor/system context, action, purpose, resource IDs, and requested credential kind.
- Provider result carries in-memory secret material plus safe metadata; audit persists only metadata/outcome/error stage.

Fit:
- This aligns closely with Teleport/Boundary-style “authorize target/session before credential material is resolved.”
- It is larger than Approach 1 because more call sites must route through the facade, and grant enforcement order must remain unchanged.
- It can still be no-migration/no-API/no-deployment if the only provider is local and all checks delegate to existing code.

Best use if:
- The implementation slice plans to touch multiple credential-use boundaries at once and wants a single audit/grant gate.

### Approach 3 — Local provider registry only, with hardcoded `local`

Shape:
- Add a minimal provider registry or factory that always returns `local`.
- Existing callers can be migrated incrementally; initial change can focus on construction/tests and one low-risk call path.

Fit:
- No external provider, no API, no migration, no deployment change.
- Lower immediate behavioral risk, but less useful than Approach 1 unless at least one real call path is moved behind the seam.

Best use if:
- The foundation slice is intentionally plumbing-only and wants to establish naming/types without altering credential-use behavior.

## Recommended Smallest Local-Provider Seam

Use Approach 1.

Minimum recommended contract:
- Provider identity is internal and fixed: `local`.
- Requests include `purpose` and the resource/source already known today (`model.Node`, `ssh_key_id`, `app_credential_id`, or `task_id` as applicable).
- Results include:
  - in-memory material for the immediate caller only;
  - safe metadata compatible with `sshutil.ResolvedCredential` / `credentialaudit.Event` (`kind`, `source`, optional IDs, `provider=local`);
  - optional future fields like `expires_at` or `lease_id`, empty for local.
- The local provider delegates to existing model hooks and helper functions; it does not decrypt manually in handlers and does not bypass `ValidateSSHKeyScope`.
- The provider seam should not create new public API fields, new database columns, new env vars, new docs/deployment requirements, or external provider configuration in this slice.

Smallest first use-site candidates:
1. SSH credential resolution, because `sshutil.ResolvedCredential` and purpose-aware helpers already form the closest existing seam.
2. App credential config load for policy hook rendering, because the current direct `db.First` + JSON unmarshal flow is localized in policy/app credential code.
3. Restic `repository_password` later, if an implementation slice is already changing executor config handling; otherwise leave it unchanged to avoid broad task behavior changes.

## Related Specs

- `.trellis/spec/backend/database-guidelines.md:14-17` — sensitive fields are encrypted/decrypted through model hooks and `secure/crypto.go`.
- `.trellis/spec/backend/database-guidelines.md:74-96` — durable contracts for SSH key least-privilege metadata, credential audit events, and config export/import preservation of SSH key scope fields.
- `.trellis/spec/backend/database-guidelines.md:105-111` — do not expose raw secret-bearing model values; do not manually encrypt/decrypt sensitive fields in handlers.
- `.trellis/spec/backend/quality-guidelines.md:35-55` — required backend patterns include response helpers, encrypted sensitive data, sanitizers, docs sync, and reuse of existing helpers.
- `.trellis/spec/backend/quality-guidelines.md:224-290` — SSH key least-privilege scope contract and purpose-aware auth signatures.
- `.trellis/spec/backend/quality-guidelines.md:380-447` — credential-use audit event contract.
- `.trellis/spec/backend/quality-guidelines.md:451-520` — credential access grant contract, including grant-before-resolution behavior.
- `.trellis/spec/backend/deployment-runtime.md:22-32` — official deployment contract is a fixed self-hosted Docker Compose/All-in-One runtime with local bind mounts; no external credential service is part of this deployment shape.

## Caveats / Not Found

- No existing Xirang credential broker/provider abstraction was found. Searches for broker/provider terminology under `backend/internal` found dashboard metric providers only, not credential providers.
- External Vault/KMS/Boundary/Teleport references are pattern inputs only. Under this task’s constraints, they should not introduce clients, SDKs, network calls, env vars, migrations, or deployment changes.
- Policy hook rendering currently persists rendered hook command strings, and built-in templates can include app credential passwords in those command strings. A no-behavior-change provider foundation should document and wrap current behavior rather than silently changing hook storage/execution semantics in this slice.
- This research did not inspect frontend credential UI beyond backend route/API contracts because the requested foundation is backend/provider-pattern focused and explicitly has no API behavior change.
