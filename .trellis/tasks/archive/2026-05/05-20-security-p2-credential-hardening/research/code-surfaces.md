# P2 Code Surface Research

- **Query**: Research current repo code surfaces for P2 credential/security hardening; identify the smallest coherent P2 slice that materially reduces credential blast radius without a full PAM/bastion rewrite.
- **Scope**: Mixed internal repo/code/spec/docs research plus existing product-pattern research in this task.
- **Date**: 2026-05-20

## Current P1 Baseline

P1/P1b/P1c/P1d are accepted as complete and P2 may start. The acceptance report states: "P1 is accepted. P2 may start" and found no blocker before P2 (`.trellis/tasks/archive/2026-05/05-20-p1-overall-security-review-before-p2/final-review-report.md:3-7`, `:82-93`). The accepted release slices are `v0.39.0` through `v0.42.0`, covering SSH key least privilege, extended credential audit coverage, credential audit UI/export, and step-up auth (`final-review-report.md:69-78`).

### Backend baseline

| Surface | Current behavior | Evidence |
|---|---|---|
| SSH key least-privilege metadata | `model.SSHKey` stores disabled/expiry/purpose/node/tag scope and `last_used_at`; private key is excluded from JSON. | `backend/internal/model/models.go:27-43` |
| Secret-safe node responses | `Node.Sanitized()` clears node password/private key and nested SSH key private key before API response. | `backend/internal/model/models.go:46-57` |
| Credential-use audit table | Dedicated model stores actor/action/purpose/kind/source/resource IDs/outcome/sanitized metadata and explicitly forbids raw secrets, terminal streams, command output, and executor config. | `backend/internal/model/models.go:421-443` |
| Managed SSH key scope enforcement | `ValidateSSHKeyScope` composes purpose, node ID, and tag checks; `ValidateSSHKeyPurpose` denies disabled/expired/disallowed-purpose keys. | `backend/internal/sshutil/scope.go:128-153` |
| Broad compatibility | Empty purpose/node/tag scope remains allowed for compatibility; broad keys are advisory risks. | `backend/internal/sshutil/scope.go:189-193`; `.trellis/spec/backend/quality-guidelines.md:244-250` |
| Purpose-aware SSH auth helpers | `BuildSSHAuthForPurpose` / `BuildSSHAuthWithKeyForPurpose` enforce managed-key scope before private key use and return safe `ResolvedCredential` labels. Inline node password/private-key credentials remain outside SSHKey scope. | `backend/internal/sshutil/ssh_auth.go:34-67`, `:76-123`; `.trellis/spec/backend/quality-guidelines.md:248-250` |
| Runtime task SSH audit | `executor.DialSSHForNodePurpose` resolves purpose-aware auth and writes runtime credential audit events for auth build, host key, dial success/failure. | `backend/internal/task/executor/ssh_connect.go:22-57`, `:101-140` |
| Auth token purpose isolation | Primary REST and realtime auth reject purpose-scoped JWTs, so `2fa_pending` or `step_up` proofs cannot replace normal bearer auth. | `backend/internal/middleware/auth.go:41-45`; `backend/internal/api/handlers/realtime_auth.go:24-30` |
| Step-up proof primitive | Step-up JWT uses purpose `step_up` with `StepUpProofTTL = 5 * time.Minute`. | `backend/internal/auth/jwt.go:19-23`, `:98-124` |
| REST step-up enforcement | `RequireStepUp`, `RequireStepUpIf`, and `EnforceStepUp` validate proof header `X-Xirang-Step-Up`; validation checks purpose, same user, role, token version, and TOTP enabled. | `backend/internal/api/handlers/step_up.go:17-23`, `:36-75`, `:77-112` |
| Secured route stack | Auth, generic audit, API rate limit, and body size limit wrap secured routes. | `backend/internal/api/router.go:126-130` |
| Covered high-risk REST routes | SSH key export, task trigger/restore, snapshot restore, sensitive config export, task batch trigger, and batch command creation are step-up-gated either by route middleware or handler helper. | `backend/internal/api/router.go:224-228`, `:281-287`, `:307-321`; `backend/internal/api/handlers/task_handler.go:717-719`; `backend/internal/api/handlers/batch_handler.go:113-115` |
| Terminal WebSocket gate | `/ws/terminal` is outside the secured group due to browser WS header limits; first auth message requires admin token plus step-up proof before node lookup and SSH auth/dial. | `backend/internal/api/router.go:362-365`; `backend/internal/api/handlers/terminal_handler.go:53-57`, `:202-226`, `:246-260` |
| Terminal audit without recording | Terminal writes generic and credential audit for open/failure/close; stdout/stdin are proxied and not persisted as recording. | `backend/internal/api/handlers/terminal_handler.go:258-337`, `:473-608` |
| Credential audit sanitization | Writer bounds strings, caps metadata at 16 entries, rejects secret-shaped keys/values, and redacts output markers in error messages. | `backend/internal/credentialaudit/audit.go:144-177`, `:208-259`, `:263-310` |
| Settings security risk summary | Admin-only summary includes broad/reused/stale keys, disabled/expired in-use keys, recent credential operations, root/sudo, and weak defaults. High-risk action list includes P1/P1b/P1d operations. | `backend/internal/api/router.go:313-315`; `backend/internal/api/handlers/settings_handler.go:435-491`; `.trellis/spec/backend/quality-guidelines.md:293-347` |
| Config export/import | Sensitive export (`include_secrets=true`) is admin-only and conditionally step-up-gated. Config import is admin-only and can ingest SSH private keys, node passwords/private keys, task executor config, and settings; route currently has no step-up gate. | `backend/internal/api/router.go:318-321`; `backend/internal/api/handlers/config_handler.go:59-86`, `:126-149`, `:205-207`, `:258-353`, `:415-419` |

### Frontend baseline

| Surface | Current behavior | Evidence |
|---|---|---|
| API step-up header | API core attaches `X-Xirang-Step-Up` when a proof is provided, clears auth and step-up proof on 401, and detects `STEP_UP_REQUIRED` 403 separately from login expiry. | `web/src/lib/api/core.ts:49-67`, `:132-145`, `:173-183` |
| Session-scoped step-up proof | Step-up proof is saved in session storage only, with expiry validation and clearing on expiry/logout. | `web/src/lib/step-up-storage.ts:1-67`; `web/src/context/auth-context-provider.tsx:157-201`, `:223-244`, `:257-286` |
| Shared retry flow | `useStepUpAction` retries a protected action once after backend returns `STEP_UP_REQUIRED`; it clears stale proof if retry still requires step-up. | `web/src/hooks/use-step-up-action.ts:5-25` |
| TOTP step-up API | Frontend calls `/auth/step-up` with current bearer token and TOTP code to obtain proof, expiry, and TTL metadata. | `web/src/lib/api/totp-api.ts:23-27`, `:61-67` |
| Terminal WS proof | Web terminal calls `ensureStepUpProof()` before opening the socket and sends `{ type: "auth", token, step_up_proof }` as the first message; it clears proof on policy-violation close. | `web/src/components/web-terminal.tsx:50-79`, `:98-108`, `:121-125` |
| Config import/export API | Export accepts optional step-up proof; import does not accept/pass step-up proof. | `web/src/lib/api/config-api.ts:34-49` |
| Credential audit frontend safety | Credential audit API mapper normalizes known action strings and re-sanitizes metadata/error text, endpoints, bearer tokens, and secret-shaped patterns before display/export handling. | `web/src/lib/api/credential-audit-api.ts:54-79`, `:81-134`, `:153-220` |

### P1 roadmap boundary

The P1 core PRD explicitly left step-up, approval/JIT workflows, SSH certificates/external CA, Vault/KMS, and terminal session recording out of scope (`.trellis/tasks/archive/2026-05/05-19-security-p1-least-privilege-audit/prd.md:55-62`). P1b left full terminal/file/session recording and approval/JIT/PAM/bastion workflows out of scope (`p1b.../prd.md:45-52`). P1c was review/export-only with no remediation or step-up changes (`p1c.../prd.md:69-77`). P1d delivered TOTP-backed step-up for selected high-risk operations while leaving policy UI, WebAuthn/passkeys, and broader write/admin enforcement out of scope (`p1d.../prd.md:7-43`, `:88-97`). The follow-up roadmap names P2 as approval/JIT workflows, SSH certificates/external CA, Vault/KMS integration, and terminal session recording where appropriate (`p1.../prd.md:109-115`).

## Candidate P2 Capabilities

### 1. Approval / JIT credential-use grants

**Fit with current code**: High. Existing P1 assets already provide stable action/purpose strings, actor identity, RBAC/ownership, TOTP step-up, purpose-aware SSH use, and credential audit. A JIT grant can be checked at the same server-side boundaries currently enforcing step-up.

**Likely enforcement points**:

- Terminal open: insert grant validation after realtime token + step-up proof validation and before node lookup / `BuildSSHAuthForPurpose` / `DialSSH` in `TerminalHandler.ServeTerminal` (`backend/internal/api/handlers/terminal_handler.go:202-260`).
- REST high-risk operations: route middleware can mirror `RequireStepUp` / `RequireStepUpIf` on `router.go` protected routes (`backend/internal/api/router.go:224-321`).
- Handler-level operations with pre-filtering: batch task trigger and batch command creation already have handler-level `EnforceStepUp` calls after validation/ownership (`task_handler.go:717-719`; `batch_handler.go:113-115`).
- Runtime task paths: if grants extend beyond explicit user-triggered calls, `credentialaudit.WithRuntimeContext` and `executor.DialSSHForNodePurpose` are current runtime evidence boundaries (`backend/internal/credentialaudit/audit.go:49-73`; `backend/internal/task/executor/ssh_connect.go:22-57`).

**Compatibility shape**:

- Grants can gate existing stored credentials without changing node auth mode or requiring remote host changes.
- Single-admin/self-hosted deployments need a non-deadlocking path: TOTP + reason can issue a short self-grant; multi-admin deployments can later require separate approval.
- First slice can be terminal-first or operation-bound for selected P1d high-risk actions; both preserve existing automation if only interactive/admin paths require grants initially.

**Security value**:

- A stolen active session plus generic 5-minute step-up proof is less useful if high-blast-radius credential use also requires a resource/action-bound grant with reason, expiry, and audit.
- Gate applies to both managed SSH keys and inline node password/private-key credentials, because the check is before credential resolution/use.

### 2. SSH certificates / external CA

**Fit with current code**: Medium-low for first P2 slice. Current SSH auth code assumes stored node passwords/private keys or managed SSH keys converted to `ssh.AuthMethod` (`backend/internal/sshutil/ssh_auth.go:87-117`). Adding certs requires a CA/key-signing model, SSH principal mapping, TTL/revocation semantics, and remote host trust rollout.

**Likely file impact if later implemented**:

- Backend model/migrations for CA configuration, cert roles, principals, TTLs, and node trust status.
- `backend/internal/sshutil/ssh_auth.go` for certificate signing and `ssh.CertSigner` auth methods.
- Node/SSH key APIs and UI for cert mode selection and rollout status.
- Docs for host `TrustedUserCAKeys` deployment.

**Why not smallest MVP**:

- Requires remote `sshd` trust changes or a proxy/agent path.
- Partial rollout leaves current stored key/password paths active, so blast-radius reduction is incomplete until migration is broad.
- More operational risk than a grant check layered over existing controls.

### 3. Vault/KMS / external secret broker

**Fit with current code**: Medium-low for first P2 slice. Sensitive fields are currently encrypted/decrypted by GORM hooks on models such as `SSHKey.PrivateKey`, `Node.Password`, `Node.PrivateKey`, `User.TOTPSecret`, `Task.ExecutorConfig`, `Integration` secrets, and `AppCredential.Config` (`backend/internal/model/models.go:152-185`, `:198-260`; further hooks in same file). Deployment docs center on `DATA_ENCRYPTION_KEY` and optional `DATA_ENCRYPTION_LEGACY_KEY` (`docs/env-vars.md:32-48`, `:252-260`; `docs/admin/security.md:75-78`).

**Likely file impact if later implemented**:

- New secret-provider abstraction around current encrypted-at-rest fields.
- Model changes to store references/versions/provider metadata instead of or alongside ciphertext.
- Config import/export semantics for secret references versus local material.
- Runtime handling for provider health, token storage, lease renewal, and fallback.
- Docs/env vars for provider configuration and outage behavior.

**Why not smallest MVP**:

- KMS alone protects encryption keys but does not remove app-side SSH private key use at runtime.
- Vault SSH OTP/cert modes may require host-side helpers or CA trust.
- External-service dependency and failure modes are disproportionate for a self-hosted default P2 slice.

### 4. Terminal session recording

**Fit with current code**: Medium technically, but mixed for blast-radius reduction. Terminal streams pass through one handler (`backend/internal/api/handlers/terminal_handler.go`), so capture is possible. Current P1 contract explicitly avoids storing terminal input/output, and credential audit forbids `stream`/`output`/`content` metadata (`backend/internal/model/models.go:421-423`; `backend/internal/credentialaudit/audit.go:263-280`; `.trellis/spec/backend/quality-guidelines.md:395-405`).

**Likely file impact if later implemented**:

- Terminal handler stream tap, recording metadata model, storage backend, retention worker, playback API/UI, access controls, and docs.
- Strong new sanitization/privacy policy because terminal output can contain tokens, passwords, file contents, customer data, and command output.

**Why not smallest MVP**:

- Recording improves accountability after access, but does not prevent or narrow initial credential use.
- Creates a new sensitive evidence store that conflicts with current P1 secret-safety invariants unless heavily scoped.
- Covers only web terminal, not scheduled tasks, batch commands, file browser, or direct SSH outside Xirang.

## Recommended P2 MVP

Recommended smallest coherent P2 slice: **operation-bound JIT credential-use grants, starting with terminal access as the first enforced high-blast-radius operation**.

### MVP definition

A minimal P2 MVP can introduce a short-lived `credential access grant` concept with these safe fields:

- actor: `user_id`, `username`, `role`;
- operation: stable `action` / `purpose`, initially `terminal.open` / `terminal`;
- resource scope: `node_id` for terminal-first MVP;
- intent: bounded `reason` text with length cap and sanitization;
- lifecycle: `status` (`requested`, `approved`, `denied`, `active`, `expired`, `revoked`), `requested_at`, `approved_at`, `expires_at`, optional `approver_user_id`;
- audit references: safe IDs only; no credential values, terminal streams, command text/output, hostnames, endpoints, or exported payloads.

### Why terminal-first is the smallest coherent slice

1. Terminal is the highest-value interactive SSH surface and already has a single backend choke point before credential resolution/use (`backend/internal/api/handlers/terminal_handler.go:202-260`).
2. Existing P1d terminal flow already requires admin role and TOTP step-up before SSH auth/dial; a grant check composes naturally after those checks and before private key/password use.
3. It covers both managed SSH keys and inline node credentials because enforcement is before `sshutil.BuildSSHAuthForPurpose` (`terminal_handler.go:258-260`).
4. It avoids remote host changes, Vault dependencies, SSH CA rollout, or terminal transcript storage.
5. It creates durable intent/approval evidence that complements credential audit without weakening the current no-stream/no-output audit contract.

### Suggested server-side order for terminal-first MVP

For `GET /ws/terminal` first-message auth:

1. Reserve session slot and upgrade WebSocket as today (`terminal_handler.go:160-183`).
2. Parse first auth message (`terminal_handler.go:184-200`).
3. Validate primary token and admin role (`terminal_handler.go:202-212`).
4. Validate step-up proof (`terminal_handler.go:213-226`).
5. Parse and validate `node_id` (`terminal_handler.go:228-244`).
6. Check active JIT grant for `(user_id, action=terminal.open, purpose=terminal, node_id, not expired)`.
7. If missing/expired, close with policy violation and write bounded credential audit / grant audit evidence.
8. Only then load node and call `BuildSSHAuthForPurpose` / SSH dial (`terminal_handler.go:246-337`).

This order prevents a grant miss from resolving or using SSH credentials. It also avoids leaking whether a node exists before both identity and step-up are validated; if product wants grant checks before node lookup, the grant can be keyed by the requested numeric node ID without loading node details.

### Incremental follow-up within P2

After terminal-first MVP lands and is verified, the same grant model can extend to the existing P1d operation set:

- `GET /ssh-keys/export` (`ssh_key.export`, `ssh_key_export`);
- `GET /config/export?include_secrets=true` (`config.export`, `config_export`);
- `POST /tasks/:id/trigger` (`task.manual_trigger`, purpose derived by task);
- `POST /tasks/batch-trigger` (`task.batch_trigger`, `task_command`);
- `POST /tasks/:id/restore` (`task.restore_trigger`, `task_restore`);
- `POST /tasks/:id/snapshots/:sid/restore` (`snapshot.restore`, `snapshot`);
- `POST /batch-commands` (`batch_command.create`, `batch_command`).

For each extension, grant checks should remain backend-enforced and should run before operation execution and before private key/password use.

## Likely Files

### Backend core files

| File | Why it matters for P2 JIT |
|---|---|
| `backend/internal/model/models.go` | Add grant/request model if implementation proceeds; current SSHKey and CredentialAuditEvent models are reference patterns (`:27-43`, `:421-443`). |
| `backend/internal/database/migrations/sqlite/*` and `backend/internal/database/migrations/postgres/*` | Add paired SQLite/PostgreSQL migration(s) for grant state; current project requires dual-track migrations. |
| `backend/internal/api/router.go` | Register grant request/list/approve routes and add grant middleware/checks near existing step-up high-risk routes (`:224-321`, `:362-365`). |
| `backend/internal/api/handlers/terminal_handler.go` | First terminal enforcement point; check grant before node load/SSH auth/dial (`:202-260`). |
| `backend/internal/api/handlers/step_up.go` | Pattern for small reusable gate helpers and machine-readable denial shape (`:36-75`, `:114-123`). |
| `backend/internal/api/handlers/helpers.go` | Existing helper style for safe audit and ownership utilities. |
| `backend/internal/credentialaudit/audit.go` | Existing event sanitizer and writer; any grant-use audit must preserve the same forbidden-key/value constraints (`:144-177`, `:263-310`). |
| `backend/internal/middleware/rbac.go` | If new permissions are added, role matrix and full-router tests must cover intended roles. |
| `backend/internal/middleware/ownership.go` | Needed if grants later become operator/node-owner scoped rather than terminal admin-only. |
| `backend/internal/api/handlers/task_handler.go` | Later extension point for manual/batch trigger and restore (`:462-495`, `:609-652`, `:666-759`). |
| `backend/internal/api/handlers/batch_handler.go` | Later extension point for batch command creation after validation/ownership and before task creation (`:56-115`). |
| `backend/internal/api/handlers/config_handler.go` | Sensitive export is already step-up-gated; import is admin-only secret-bearing and may need separate P2 hardening consideration (`:59-86`, `:258-353`, `:415-419`). |
| `backend/internal/api/handlers/settings_handler.go` | Existing security summary can later surface pending/active/blocked grant signals without exposing raw metadata (`:435-491`). |
| `backend/internal/api/handlers/credential_audit_handler.go` | Existing safe list/export DTO pattern for security-sensitive review surfaces. |

### Backend tests likely needed

| Area | Test intent |
|---|---|
| Full-router grant route tests | Missing/expired token -> 401; non-admin denial or intended role policy -> 403; admin path works through real middleware. |
| Grant lifecycle handler tests | Request, self-grant/approval, denial, expiry, revocation, bounded reason sanitization, no secret-shaped fields. |
| Terminal WS tests | Missing grant blocks before SSH auth/dial; active grant allows existing step-up-authenticated flow; expired/wrong-user/wrong-node grant blocks. |
| Credential audit / grant audit tests | Denials and use events contain only safe IDs, counts, booleans, status, operation, reason category/hash if used; no terminal stream/command/output/host/secret fields. |
| Migration tests | SQLite and PostgreSQL migration names/UTC safety per existing project hooks. |

### Frontend files

| File | Why it matters for P2 JIT |
|---|---|
| `web/src/components/web-terminal.tsx` | First UI flow needing grant request/consume behavior before opening WebSocket (`:50-108`). |
| `web/src/context/auth-context-provider.tsx` | Existing step-up prompt can compose with a grant prompt/request flow; session-scoped proof handling is here (`:223-286`). |
| `web/src/hooks/use-step-up-action.ts` | Pattern for retrying backend-denied high-risk actions (`:5-25`); a similar `useJITGrantAction` may be appropriate later. |
| `web/src/lib/api/core.ts` | Machine-readable errors and auth/step-up clearing live here; JIT-required error handling should not trigger login expiry (`:132-183`). |
| `web/src/lib/api/totp-api.ts` | Current step-up request API; grant request API can follow similar module patterns (`:61-67`). |
| `web/src/lib/api/config-api.ts` | Later extension: export already passes step-up; import currently does not pass proof and is not step-up-gated (`:34-49`). |
| `web/src/lib/api/credential-audit-api.ts` | Safe mapper pattern for security-sensitive events and metadata (`:54-134`, `:153-220`). |
| `web/src/pages/settings` / security UI files | Likely place to surface active/pending grants or link to a grant review page if MVP includes admin review UI. |
| `web/src/router.tsx`, navigation/layout files | Needed only if adding a dedicated grant review page. |

### Docs/specs likely touched if implementation proceeds

| File | Why it matters |
|---|---|
| `.trellis/spec/backend/quality-guidelines.md` | Add durable contracts for JIT grant checks, grant metadata, and denial/audit behavior. |
| `backend/README_backend.md` | Document new grant endpoints and terminal/high-risk route requirements. |
| `docs/admin/security.md` | Explain JIT grant security model and deployment expectations without process notes. |
| `docs/env-vars.md` | Only if adding configurable TTL/policy/env; first MVP can avoid new env vars. |

## Security Constraints

1. **Backend enforcement only**: UI prompts and frontend state must not grant access by themselves. Grant checks must live before operation execution and before private key/password use, following the P1d backend step-up model (`backend/internal/api/handlers/step_up.go:36-75`).
2. **Preserve auth/RBAC/ownership composition**: A grant is additive. It must not replace primary bearer token auth, admin/RBAC checks, ownership checks, or step-up proof validation (`backend/internal/api/router.go:126-130`, `:224-321`; `backend/internal/middleware/auth.go:41-45`).
3. **Purpose-scoped tokens remain secondary only**: `step_up` and any future grant proof must not be accepted as normal bearer tokens; current REST/realtime auth rejects purpose-scoped tokens (`middleware/auth.go:41-45`; `realtime_auth.go:24-30`).
4. **No raw sensitive evidence**: Do not store or return private keys, passwords, TOTP/JWT/recovery codes, decrypted executor config, terminal input/output, SFTP payloads, file contents, command output/text, Docker output/volume names, diagnostic evidence, exported config payloads, raw endpoints, proxy URLs, hostnames, or username+host strings in grant records, audit records, docs, tests, or UI.
5. **Bounded metadata only**: Reuse safe identifiers and bounded metadata (`stage`, `operation`, `status`, `ttl_seconds`, `node_id`, counts, booleans). Current credential audit sanitizer drops keys containing `private`, `password`, `token`, `secret`, `credential`, `config`, `output`, `stream`, `command`, `content`, or `payload` (`backend/internal/credentialaudit/audit.go:263-280`).
6. **Terminal recording is not part of the MVP**: Current accepted P1 contract intentionally avoids terminal stream persistence; recording would create a new sensitive data store and needs a separate privacy/storage/access-control design.
7. **Compatibility with broad scopes**: Existing unscoped SSH keys remain allowed for compatibility and are surfaced as risks; JIT should not silently reinterpret empty SSHKey scope as deny (`backend/internal/sshutil/scope.go:155-158`, `:189-193`; `.trellis/spec/backend/quality-guidelines.md:244-250`).
8. **Inline credential coverage**: Because inline node password/private-key credentials are not governed by SSHKey scope metadata, the grant must sit at operation/use boundaries, not only on `SSHKey` records (`.trellis/spec/backend/quality-guidelines.md:248-250`).
9. **SQLite/PostgreSQL parity**: Any grant model requires paired migrations and migration-safety checks; Xirang supports both SQLite and PostgreSQL.
10. **Self-hosted admin safety**: Single-admin deployments need a non-deadlocking self-grant/break-glass mode, still bounded by TOTP, reason, short TTL, and audit.

## Open Risks

1. **Config import is secret-bearing but not step-up-gated**: `POST /config/import` is admin-only (`backend/internal/api/router.go:321`) and can ingest SSH private keys, node passwords/private keys, task executor config, and settings (`backend/internal/api/handlers/config_handler.go:308-353`, `:415-419`, `:515+`). This is not a P1 blocker, but it is a relevant P2 hardening surface.
2. **Batch-trigger no-op observation remains**: The P1 final review notes that `POST /tasks/batch-trigger` returns without step-up and without `task.batch_trigger` credential audit when every requested task is missing/filtered before execution; no task runs, so this is telemetry-only, not an execution bypass (`final-review-report.md:90-93`; `backend/internal/api/handlers/task_handler.go:717-739`).
3. **Grant policy scope must avoid lockout**: Requiring grants broadly for all high-risk operations without self-hosted fallback could lock out single-admin installs. Start with terminal-first or opt-in operation policy.
4. **Grant proof vs grant row design**: A row-backed active grant is easiest to revoke/expire/audit. A signed grant token would need the same purpose isolation as step-up and careful replay/resource binding. The MVP should choose one model explicitly during implementation planning.
5. **Reason text can become a leak path**: Free-text reasons must be length-capped and sanitized; consider storing only bounded reason plus optional redacted display text, not paths/hosts/commands.
6. **Terminal-first MVP does not cover all credential use**: It reduces interactive blast radius first, but task triggers, restores, exports, file browser, Docker, probes, and background workers still rely on existing P1 controls until grant checks are extended.
7. **SSH certs/Vault remain larger future work**: They can reduce stored-secret risk more deeply, but require remote trust/provider availability changes. Treat them as follow-ups after JIT grant semantics are stable.
8. **Session recording has privacy/storage risk**: Recording can improve accountability, but may persist secrets/customer data and does not prevent initial credential use. It should remain a separate opt-in design, not part of the first P2 blast-radius reducer.
