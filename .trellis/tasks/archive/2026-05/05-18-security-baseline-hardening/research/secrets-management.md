# Research: Secrets management and key management baseline for Xirang

- **Query**: Research secrets management and key management best practices relevant to Xirang storing SSH keys, node passwords, integration secrets, and task executor configs. Focus on OWASP Secrets Management, OWASP Cryptographic Storage, OWASP Key Management, and pragmatic self-hosted app constraints. Cover API redaction, never returning raw secrets, envelope/key rotation ideas, least privilege access, audit, and incident response.
- **Scope**: mixed internal / external
- **Date**: 2026-05-18

## Findings

### Files Found

| File Path | Description |
|---|---|
| `backend/internal/secure/crypto.go` | Central sensitive-field encryption helper: `enc:v1:` / `enc:v2:` prefixes, `DATA_ENCRYPTION_KEY`, `DATA_ENCRYPTION_LEGACY_KEY`, AES-GCM encryption/decryption, v1 re-encryption helper. |
| `backend/internal/model/models.go` | Domain model sensitive fields, JSON redaction tags, GORM `BeforeSave` / `AfterFind` encryption hooks, `Node.Sanitized()`, `AppCredential.SanitizedConfig()`. |
| `backend/internal/bootstrap/bootstrap.go` | v1-to-v2 encryption migration and V1 residual counting across encrypted columns. |
| `backend/internal/api/handlers/system_handler.go` | Admin-only encryption status endpoint returning `v1_remaining_count` / `healthy`. |
| `backend/internal/api/handlers/node_handler.go` | Node API list/get/create/update paths call `Node.Sanitized()` before returning node records. |
| `backend/internal/api/handlers/ssh_key_handler.go` | SSH key response DTO derives public key and excludes private key from API responses. |
| `backend/internal/api/handlers/app_credential_handler.go` | App credential API accepts password on writes, encrypts config via model hook, returns sanitized config plus `has_password`. |
| `backend/internal/api/handlers/integration_handler.go` | Integration API stores endpoint/secret/proxy URL through encrypted model fields; list/get mask Telegram bot token in endpoint. |
| `backend/internal/api/handlers/task_handler.go` | Task list/get sanitize nested node data; task model still has `executor_config` JSON response tag. |
| `backend/internal/task/executor/restic_executor.go` | Restic executor config can contain `repository_password`; builds `RESTIC_PASSWORD` command prefix for remote execution. |
| `backend/internal/profile/profile.go` | App-aware backup profile schemas mark password fields and keep hook templates out of JSON; templates render credentials into backup commands. |
| `backend/internal/util/sanitize.go` | Shared sanitizer for free-form messages, URLs, private-key blocks, token/key/password patterns, and output length bounding. |
| `backend/internal/api/router.go` | Authenticated API group, audit middleware, RBAC route registrations, admin-only system/settings/config endpoints. |
| `backend/internal/middleware/rbac.go` | Role-to-permission matrix for admin/operator/viewer and generic `RBAC` middleware. |
| `backend/internal/middleware/audit.go` | Non-GET/HEAD/OPTIONS audit logger and hash-chain audit log persistence. |
| `.trellis/spec/backend/quality-guidelines.md` | Project contracts for encrypted sensitive data, stripping secrets from responses, sanitizer use, encryption rotation docs, RBAC fail-closed expectations. |
| `.trellis/spec/backend/database-guidelines.md` | Project database contract: encrypted sensitive fields through hooks and response sanitizers; no manual handler encryption/decryption. |
| `.trellis/spec/backend/logging-guidelines.md` | Project logging contract: do not log passwords, private keys, TOTP secrets, JWTs, recovery codes, encryption keys, webhook secrets, bearer tokens, or raw endpoints. |
| `.trellis/spec/backend/error-handling.md` | Project error contract: do not expose raw SQL, encryption, SSH private key, token, stack-like errors, diagnostic secrets, proxy endpoints, or credential-bearing output. |
| `.trellis/spec/frontend/type-safety.md` | Frontend mapping contracts telling components to render sanitized backend evidence as-is and not enrich with raw credentials/secret-bearing data. |
| `docs/env-vars.md` | Public env documentation for `DATA_ENCRYPTION_KEY`, `DATA_ENCRYPTION_LEGACY_KEY`, key storage, rotation, and backup coupling. |
| `docs/admin/security.md` | Admin-facing security hardening docs for required production secrets and encrypted sensitive fields. |

### Internal Code Patterns

#### 1. Sensitive data inventory and current storage boundary

| Data class | Current code path |
|---|---|
| User password hash / MFA secrets | `User.PasswordHash`, `TOTPSecret`, `RecoveryCodes`, and `TokenVersion` are `json:"-"` in `backend/internal/model/models.go:13-25`; TOTP secret and recovery codes are encrypted/decrypted in model hooks at `backend/internal/model/models.go:633-669`. |
| SSH private keys | `SSHKey.PrivateKey` is `json:"-"` and documented as never serialized at `backend/internal/model/models.go:27-39`; encrypted/decrypted in hooks at `backend/internal/model/models.go:573-595`; response DTO omits private key and derives `public_key` at `backend/internal/api/handlers/ssh_key_handler.go:43-73`. |
| Node SSH passwords/private keys | `Node.Password` and `Node.PrivateKey` are encrypted/decrypted in hooks at `backend/internal/model/models.go:597-630`; `Node.Sanitized()` clears both and nested `SSHKey.PrivateKey` at `backend/internal/model/models.go:41-52`. Node list/get/create/update call `Sanitized()` before returning at `backend/internal/api/handlers/node_handler.go:103-108`, `122-132`, `275-279`, and `415-423`. |
| App credentials | `AppCredential.Config` is `json:"-"`; comments state it stores full JSON including password and API responses must return `SanitizedConfig()` at `backend/internal/model/models.go:147-156`; hooks encrypt/decrypt at `backend/internal/model/models.go:161-181`; `SanitizedConfig()` deletes `password` at `backend/internal/model/models.go:183-190`; handler response includes sanitized `config` and `has_password` at `backend/internal/api/handlers/app_credential_handler.go:46-77`. |
| Integration secrets/endpoints/proxy URLs | `Integration.Endpoint`, `Secret`, and `ProxyURL` are encrypted/decrypted in hooks at `backend/internal/model/models.go:193-257`; `Secret` is `json:"-"` and `HasSecret` is computed at `backend/internal/model/models.go:198-203` and `253-255`; list/get call `maskIntegrationEndpoint` at `backend/internal/api/handlers/integration_handler.go:209-218` and `232-243`; that mask currently applies to Telegram bot tokens at `backend/internal/api/handlers/integration_handler.go:819-823`. |
| Task executor configs | `Task.ExecutorConfig` has `json:"executor_config,omitempty"` at `backend/internal/model/models.go:291-304`; encrypted/decrypted in hooks at `backend/internal/model/models.go:321-343`; Restic config explicitly includes `repository_password` at `backend/internal/task/executor/restic_executor.go:17-29`; task list/get sanitize nested node but return task entities at `backend/internal/api/handlers/task_handler.go:123-160` and `174-193`. |
| Task/app profile generated command secrets | App profile schemas mark password fields as `Type: "password"` at `backend/internal/profile/profile.go:31-43`; hook templates are `json:"-"` at `backend/internal/profile/profile.go:17-19`; several built-in templates render passwords into command lines/env vars, e.g. MySQL/Postgres/Mongo/Redis at `backend/internal/profile/profile.go:55`, `65`, `75`, and `85`. |

#### 2. Encryption at rest

- `backend/internal/secure/crypto.go` defines versioned ciphertext prefixes `enc:v1:` and `enc:v2:` at lines `20-23`.
- v2 uses an Argon2id-derived or raw base64 32-byte primary key; Argon2id parameters are declared at `backend/internal/secure/crypto.go:26-33`, and key loading/legacy override logic is at `backend/internal/secure/crypto.go:43-70`.
- Raw base64 keys of at least 32 bytes are accepted directly; other strings are derived through Argon2id for the primary key and SHA-256 for legacy compatibility at `backend/internal/secure/crypto.go:72-94`.
- `EncryptIfNeeded` and `DecryptIfNeeded` are idempotent wrappers at `backend/internal/secure/crypto.go:128-146`; `EncryptString` writes v2 and `DecryptString` dispatches by prefix at `backend/internal/secure/crypto.go:148-174`.
- Encryption uses AES-GCM with a random nonce from `crypto/rand`; nonce+ciphertext is base64 encoded at `backend/internal/secure/crypto.go:192-210`; decryption validates length and returns generic format/decryption failures at `backend/internal/secure/crypto.go:221-249`.
- `ReEncryptV1Value` converts a v1 ciphertext to v2 at `backend/internal/secure/crypto.go:176-190`.
- Bootstrap migration covers nodes, ssh_keys, integrations, tasks, and users at `backend/internal/bootstrap/bootstrap.go:53-110`; V1 residual counting covers the same table/column list at `backend/internal/bootstrap/bootstrap.go:175-201`.
- Admin encryption status is exposed through `GET /system/encryption-status`, returning `v1_remaining_count` and `healthy`, at `backend/internal/api/handlers/system_handler.go:195-213`; route registration is admin-only at `backend/internal/api/router.go:339-345`.

#### 3. API redaction and never-return-raw-secret patterns

- Project backend quality guidelines require sensitive data to be encrypted through model hooks and stripped from response structs, citing `model.Node.Sanitized()` at `.trellis/spec/backend/quality-guidelines.md:37-48`.
- Database guidelines state sensitive fields are encrypted/decrypted through hooks and `backend/internal/secure/crypto.go` at `.trellis/spec/backend/database-guidelines.md:14-17`, and warn not to expose raw model values containing secrets or manually encrypt/decrypt in handlers at `.trellis/spec/backend/database-guidelines.md:80-86`.
- The strongest current response patterns are dedicated DTOs/metadata: SSH keys return `public_key`/fingerprint but no private key (`backend/internal/api/handlers/ssh_key_handler.go:43-73`); app credentials return `config` with `password` removed plus `has_password` (`backend/internal/api/handlers/app_credential_handler.go:58-77`, `117-124`); nodes return sanitized copies (`backend/internal/model/models.go:41-52`).
- `json:"-"` is used as defense-in-depth for `User.PasswordHash`, TOTP secret/recovery codes, `SSHKey.PrivateKey`, `AppCredential.Config`, `Integration.Secret`, and several internal fields at `backend/internal/model/models.go:16-21`, `34`, `155`, and `198`.
- Task responses are a distinct path to inspect during baseline work: `Task.ExecutorConfig` is encrypted at rest but has a JSON response tag at `backend/internal/model/models.go:303`; task list/get responses sanitize nested `Node` but no task-specific `ExecutorConfig` response sanitizer was found in the searched handler ranges (`backend/internal/api/handlers/task_handler.go:123-160`, `174-193`).

#### 4. Error, diagnostic, and log redaction

- Shared sanitizer redacts PEM private-key blocks, URL credentials/query/path secrets, Telegram-style bot tokens, and `authorization|bearer|token|api_key|secret|password` key-value patterns; it truncates output to 500 runes at `backend/internal/util/sanitize.go:9-43`, with URL/path handling at `backend/internal/util/sanitize.go:55-91`.
- Logging spec says not to log passwords, private keys, TOTP secrets, JWTs, recovery codes, `DATA_ENCRYPTION_KEY`, SMTP passwords, webhook secrets, bearer tokens, raw notification endpoints, decrypted hook-returned values, or full command output that may contain credentials at `.trellis/spec/backend/logging-guidelines.md:68-77`.
- Error-handling spec says not to expose raw SQL, encryption, SSH private key, token, or stack-like details to clients at `.trellis/spec/backend/error-handling.md:68-73`.
- Doctor diagnostics contract requires evidence to be sanitized/concise and not return passwords, private keys, tokens, proxy endpoints, raw SQL/encryption details, or credential-bearing full command output at `.trellis/spec/backend/error-handling.md:96-107`.
- Frontend type-safety contracts instruct components to render backend-provided sanitized evidence/suggestions as-is and not enrich them with raw node credentials or secret-bearing data at `.trellis/spec/frontend/type-safety.md:91-100` and `.trellis/spec/frontend/type-safety.md:162-171`.

#### 5. Least privilege, RBAC, ownership, and audit

- `/api/v1` secured routes apply `AuthMiddleware`, `AuditLogger`, API rate limiting, and request body size limits at `backend/internal/api/router.go:124-128`.
- App credential routes use `app_credentials:read` / `app_credentials:write` at `backend/internal/api/router.go:239-244`; only `admin` has these permissions in `backend/internal/middleware/rbac.go:9-43`, while operator/viewer maps do not include them at `backend/internal/middleware/rbac.go:44-79`.
- SSH key routes use `ssh_keys:*` at `backend/internal/api/router.go:221-229`; integrations use `integrations:*` at `backend/internal/api/router.go:231-237`; node/task routes combine RBAC with ownership checks for node/task-specific access at `backend/internal/api/router.go:147-166` and `269-287`.
- The RBAC spec says sensitive management surfaces such as saved credentials, system settings, recovery, and secret-bearing config should fail closed and not grant operator/viewer access unless the product contract explicitly says so at `.trellis/spec/backend/quality-guidelines.md:151-174`.
- `AuditLogger` skips GET/HEAD/OPTIONS and records non-read operations after handler execution at `backend/internal/middleware/audit.go:21-56`; audit records include user, role, method, path, status, client IP, user agent, and timestamp at `backend/internal/middleware/audit.go:42-52`.
- Audit logs are written with a hash chain (`PrevHash` / `EntryHash`) inside a transaction at `backend/internal/middleware/audit.go:59-92`.

#### 6. Rotation and incident-response support already represented in code/docs

- `docs/env-vars.md` states that Xirang encrypts passwords, private keys, TOTP keys, notification endpoint/secrets, and HTTP proxy URLs; production must set `DATA_ENCRYPTION_KEY`; `DATA_ENCRYPTION_LEGACY_KEY` can temporarily decrypt old data during rotation at `docs/env-vars.md:252-260`.
- `docs/admin/security.md` lists production-required `ADMIN_INITIAL_PASSWORD`, `JWT_SECRET`, and `DATA_ENCRYPTION_KEY`, and says weak/missing production secrets refuse startup at `docs/admin/security.md:5-15`.
- `docs/admin/security.md` notes that database backups are not sufficient without the corresponding `DATA_ENCRYPTION_KEY` to recover encrypted sensitive fields at `docs/admin/security.md:75-78`.
- Quality guidelines require encryption-key rotation docs and implementation to stay in lockstep, with `DATA_ENCRYPTION_KEY` as primary v2 write key and `DATA_ENCRYPTION_LEGACY_KEY` honored for v1 decrypt/migration at `.trellis/spec/backend/quality-guidelines.md:45-48`.

### External References

- [OWASP Secrets Management Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Secrets_Management_Cheat_Sheet.html) / [raw source](https://raw.githubusercontent.com/OWASP/CheatSheetSeries/master/cheatsheets/Secrets_Management_Cheat_Sheet.md) — Relevant themes: centralize/standardize secrets lifecycle; enforce fine-grained access control/least privilege because any user who can read or update a secret can leak it; reduce manual secret handling; automate rotation; rotation strategies include gradual rotation, new keys for writes, old keys for reads, rapid rotation, and scheduled rotation.
- [OWASP Cryptographic Storage Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Cryptographic_Storage_Cheat_Sheet.html) / [raw source](https://raw.githubusercontent.com/OWASP/CheatSheetSeries/master/cheatsheets/Cryptographic_Storage_Cheat_Sheet.md) — Relevant themes: do not store sensitive information unless needed; dedicated secret/key management systems can add protection but add operational cost; symmetric encryption should use AES with at least 128-bit keys, ideally 256-bit, and a secure mode; security-sensitive random values must come from CSPRNGs.
- [OWASP Key Management Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Key_Management_Cheat_Sheet.html) / [raw source](https://raw.githubusercontent.com/OWASP/CheatSheetSeries/master/cheatsheets/Key_Management_Cheat_Sheet.md) — Relevant themes: document key lifecycle management (generation, distribution, destruction), compromise/recovery/zeroization, key storage, and key agreement; map every component that processes or stores cryptographic key material; distinguish data-encryption keys and key-encryption keys.
- [OWASP Logging Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Logging_Cheat_Sheet.html) / [raw source](https://raw.githubusercontent.com/OWASP/CheatSheetSeries/master/cheatsheets/Logging_Cheat_Sheet.md) — Relevant themes: application logging should include security events and audit trails; logs should not directly record access tokens, authentication passwords, database connection strings, encryption keys, or other primary secrets; such data should be removed, masked, sanitized, hashed, or encrypted.

### Best-Practice Mapping for Xirang Baseline Work

| Topic | OWASP-aligned pattern relevant to Xirang |
|---|---|
| API redaction / never returning raw secrets | Treat API read paths as metadata-only for secrets: expose identifiers, names, fingerprints/public derivations, `has_secret` / `has_password`, last-used timestamps, and validation/test status; accept raw secrets only on create/update/test paths; avoid read-back of private keys, passwords, tokens, bearer URLs, proxy URLs, and executor config passwords. |
| Storage minimization | Do not store secrets that can be avoided. Where Xirang must store SSH credentials, notification tokens, app passwords, and restic repository passwords for unattended operation, centralize the inventory and storage boundary in model hooks/services rather than individual handlers. |
| Encryption algorithm and randomness | AES-GCM with 256-bit keys and random nonces maps to OWASP Cryptographic Storage guidance; `crypto/rand` nonce generation in `secure/crypto.go` is the relevant internal code path. |
| Self-hosted master key handling | A pragmatic self-hosted baseline is one deployment master key supplied by env var/secret manager plus operational docs requiring the key to be backed up separately from the DB and never written to logs/issues/docs. Existing docs already bind DB backups to key backup. |
| Envelope/key hierarchy idea | OWASP Key Management distinguishes data-encryption keys and key-encryption keys. A self-hosted envelope pattern would use `DATA_ENCRYPTION_KEY` or external KMS/Vault as a KEK, generate per-record/per-domain DEKs, store encrypted DEK + key version/key ID beside ciphertext, rotate KEKs by rewrapping DEKs, and rotate DEKs by re-encrypting affected secrets. This is an architecture idea; no envelope/per-record DEK implementation was found in the searched code. |
| Rotation model | OWASP Secrets Management lists “new keys for write, old keys for read” as a rotation strategy. Xirang’s current v1/v2 prefix and `DATA_ENCRYPTION_LEGACY_KEY` support that style for legacy decrypt/migration; `MigrateEncryptionV1ToV2` and `/system/encryption-status` are the current observable operational hooks. |
| Least privilege | Fine-grained RBAC should align with whether a surface can reveal, test, mutate, export, or indirectly use secrets. Project spec already calls for fail-closed sensitive management surfaces; app credential read/write is admin-only in current RBAC. |
| Audit | Secret create/update/delete/test/export and key-rotation actions are high-value audit events. Existing audit middleware records non-GET secured API operations with a hash chain; GET/read events are intentionally skipped in current middleware. |
| Logs/errors/diagnostics | Apply the shared sanitizer to command output, delivery failures, diagnostic evidence, and user-visible incident messages; keep redaction broad enough for PEM blocks, token-like URL paths/query strings, passwords, bearer tokens, and webhook endpoints. |
| Incident response | For a suspected secret leak, the relevant inventory includes DB encrypted fields, `DATA_ENCRYPTION_KEY`, `DATA_ENCRYPTION_LEGACY_KEY`, `JWT_SECRET`, SSH private keys/passwords, app credential passwords, integration endpoints/secrets, proxy URLs, restic repository passwords, rendered hooks/commands, logs, alerts, reports, and backups. Evidence sources include audit logs, alert deliveries, task logs, application logs, DB backups, and config/import/export artifacts. |

### Related Specs

- `.trellis/spec/backend/quality-guidelines.md` — Sensitive data encryption/response stripping, sanitizer use for user-visible evidence, key-rotation doc/implementation sync, RBAC fail-closed expectations, and test fixture credential naming.
- `.trellis/spec/backend/database-guidelines.md` — Model hook boundary for sensitive fields and prohibition on raw secret model exposure.
- `.trellis/spec/backend/logging-guidelines.md` — Explicit “what not to log” list for secrets and decrypted values.
- `.trellis/spec/backend/error-handling.md` — Generic client error handling and sanitized diagnostic evidence contracts.
- `.trellis/spec/frontend/type-safety.md` — Frontend must not enrich sanitized backend evidence with connection secrets or raw credentials.

## Caveats / Not Found

- Direct requests to OWASP public cheat-sheet pages returned HTTP 403 from this environment, so the research used OWASP Cheat Sheet Series raw GitHub markdown mirrors and kept public page links as canonical references.
- No generic API-wide outbound response scrubber was found. Redaction appears to be implemented through model JSON tags, model-specific `Sanitized*` helpers, and handler-specific response DTOs.
- No envelope encryption, per-record DEK, external KMS/Vault integration, or key ID metadata beyond ciphertext prefix (`enc:v1:` / `enc:v2:`) was found in the searched code.
- `MigrateEncryptionV1ToV2` / `CountV1EncryptedData` searched ranges cover nodes, ssh_keys, integrations, tasks, and users; `app_credentials.config` was not present in that table list, while `AppCredential` itself does have encryption hooks. This research did not determine whether historical v1 `app_credentials.config` rows can exist.
- Task `executor_config` is encrypted at rest, but no task response sanitizer for that field was found in the searched `TaskHandler.List` / `TaskHandler.Get` paths; those paths sanitize nested node data and return task entities.
- This was source research, not an endpoint-level dynamic test. No claims here are based on running API calls.