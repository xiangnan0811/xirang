# Research: emergency access posture

- **Query**: Research the next P5 small-team emergency/admin recovery/access posture slice for Xirang. Target users are personal/small teams; default should be low-burden, low-blast-radius, report-only/read-only. Do not propose enterprise policy/device trust/approval/session recording/full Vault/KMS/SSH CA/WebAuthn/passkeys. Inspect repo code for existing user/admin/TOTP/recovery-code/setup/admin-initial-password signals, Settings security-risk-summary patterns, and tests. Map 2-4 comparable self-hosted/small-team admin recovery conventions if derivable from repo/common practice, then map them onto existing Xirang primitives.
- **Scope**: mixed internal repo research + common self-hosted/small-team convention mapping derived from repo/common practice
- **Date**: 2026-05-25

## Findings

### Files Found

| File Path | Description |
|---|---|
| `backend/internal/model/models.go` | User model fields (`role`, `totp_enabled`, encrypted `totp_secret` / `recovery_codes`, `token_version`, `onboarded`) and sensitive-field hooks. |
| `backend/internal/auth/service.go` | Login, user CRUD, password change, role normalization, and token-version invalidation service logic. |
| `backend/internal/auth/jwt.go` | JWT purposes for 2FA pending and step-up proofs; full auth middleware later rejects purpose-scoped tokens. |
| `backend/internal/auth/totp.go` | TOTP secret generation, code validation, recovery code generation, and single-use recovery code consumption. |
| `backend/internal/auth/password.go` | Password strength contract used by bootstrap, user creation, user update, and self password change. |
| `backend/internal/api/handlers/auth_handler.go` | Login, `/me`, onboarding completion, password change, TOTP setup/verify/disable/login, and step-up handler implementation. |
| `backend/internal/api/handlers/user_handler.go` | Admin user-management handlers; user response exposes `totp_enabled` but not secrets. |
| `backend/internal/bootstrap/bootstrap.go` | Initial `admin` seeding from `ADMIN_INITIAL_PASSWORD`; v1→v2 encrypted-field migration includes user TOTP/recovery columns. |
| `backend/internal/config/config.go` | Production strong-secret validation for JWT/data-encryption keys and config defaults. |
| `backend/internal/middleware/rbac.go` | Role/permission matrix for `admin`, `operator`, `viewer`; `RequireRole` and `RBAC` middleware. |
| `backend/internal/middleware/auth.go` | Bearer token parsing, purpose-token rejection, token-version validation. |
| `backend/internal/api/router.go` | Route registration for auth/TOTP/users/settings risk and high-risk step-up/grant boundaries. |
| `backend/internal/api/handlers/settings_handler.go` | Settings security-risk summary endpoint, risk item model, privileged-without-TOTP, deployment-secret, weak-default patterns. |
| `backend/internal/api/handlers/settings_handler_test.go` | Tests for advisory risk counts, sanitization, bounded examples, and secret-value exclusion. |
| `backend/internal/api/router_test.go` | Route presence and full-router RBAC tests for security-risk summary. |
| `backend/internal/api/handlers/auth_user_handler_test.go` | Auth/user CRUD tests and recovery-code consumption/save-failure tests. |
| `backend/internal/bootstrap/bootstrap_test.go` | Tests that initial admin password is required only before `admin` exists and seeding is idempotent. |
| `web/src/lib/api/totp-api.ts` | Frontend API wrappers for TOTP setup/verify/disable/login and step-up proof request. |
| `web/src/lib/api/settings-api.ts` | Frontend mapper/types for Settings security-risk summary. |
| `web/src/pages/settings-page.system.tsx` | Settings system tab renders risk summary as advisory cards. |
| `web/src/pages/settings-page.system.test.tsx` | Tests risk summary rendering and absence of remediation links/buttons. |
| `web/src/pages/settings-page.tsx` | Settings tab visibility: admin sees users/system/maintenance; non-admin sees personal/account only. |
| `web/src/pages/settings-page.account.tsx` | Self-service password change and TOTP enable/disable UI. |
| `web/src/pages/settings-page.users.tsx` | Admin user-management UI with role/password edits, create/delete, and 2FA badge. |
| `web/src/components/totp-setup-dialog.tsx` | TOTP setup dialog; displays QR/secret, verifies code, shows recovery codes once. |
| `web/src/components/totp-disable-dialog.tsx` | TOTP disable dialog requiring password + TOTP code. |
| `web/src/components/setup-wizard.tsx` | Onboarding wizard and best-effort `/me/onboarded` write. |
| `web/src/context/auth-context-provider.tsx` | Session auth storage, TOTP-enabled state, and step-up proof dialog/reuse behavior. |
| `web/src/pages/login-page.tsx` | Login flow that branches into 2FA using `login_token`; accepts TOTP/recovery code text. |
| `docs/deployment.md` | Deployment docs describe first login with `admin` and `.env` `ADMIN_INITIAL_PASSWORD`. |
| `docs/env-vars.md` | Env-var docs for auth/security keys and read locations. |
| `docs/admin/security.md` | Operator-facing security hardening doc for required secrets, TOTP, high-risk temporary authorization, and sensitive-field protection. |
| `.trellis/spec/backend/quality-guidelines.md` | Contracts for Settings security-risk summary and credential-access grant posture. |
| `.trellis/spec/frontend/type-safety.md` | Contracts for risk-summary mapping and advisory-only UI behavior. |
| `.trellis/spec/frontend/state-management.md` | Contracts for frontend step-up/grant prompt state. |
| `.trellis/spec/backend/deployment-runtime.md` | Deployment runtime contract for failing startup when production secrets/env are missing. |

### Code Patterns

#### 1. User/admin account state and sensitive TOTP/recovery storage

- `model.User` includes `Role`, `TOTPSecret`, `TOTPEnabled`, `RecoveryCodes`, `TokenVersion`, and `Onboarded`; secret-like fields are excluded from JSON with `json:"-"` (`backend/internal/model/models.go:13-24`).
- User TOTP secret and recovery codes are encrypted/decrypted through GORM hooks rather than manually handled in handlers (`backend/internal/model/models.go:690-724`).
- User list responses intentionally expose only `id`, `username`, `role`, and `totp_enabled` (`backend/internal/api/handlers/user_handler.go:20-25`, `backend/internal/api/handlers/user_handler.go:55-64`).

#### 2. Initial admin bootstrap and password posture signals

- `SeedUsers` first checks whether username `admin` already exists. If it exists, seeding returns without reading `ADMIN_INITIAL_PASSWORD`; if absent, `ADMIN_INITIAL_PASSWORD` is required, validated for password strength, hashed, and used to create the sole `admin` user (`backend/internal/bootstrap/bootstrap.go:21-50`).
- Password strength requires at least 12 trimmed characters plus uppercase, lowercase, digit, and punctuation/symbol (`backend/internal/auth/password.go:23-47`).
- Bootstrap tests assert: missing `ADMIN_INITIAL_PASSWORD` fails before admin exists; seeding creates exactly one `admin` user; repeated seeding is idempotent; missing env is allowed once `admin` already exists (`backend/internal/bootstrap/bootstrap_test.go:24-87`).
- Deployment docs tell users to set `ADMIN_INITIAL_PASSWORD`, `JWT_SECRET`, and `DATA_ENCRYPTION_KEY`, then first-login as username `admin` with the env password (`docs/deployment.md:51-95`). Env docs state `ADMIN_INITIAL_PASSWORD` is “initial admin account password, bootstrap phase only” (`docs/env-vars.md:34-48`).
- Settings risk summary separately flags placeholder `ADMIN_INITIAL_PASSWORD` values via `deployment_secret_posture`; it checks the current environment value and does not inspect whether the seeded admin later changed password (`backend/internal/api/handlers/settings_handler.go:707-754`).

#### 3. Login, 2FA pending token, recovery codes, and step-up proof

- `auth.Service.Login` registers login failures/success and, when `user.TOTPEnabled` is true, returns a short-lived 2FA pending token instead of a full JWT (`backend/internal/auth/service.go:80-115`).
- JWT purposes are stable strings: `Purpose2FAPending = "2fa_pending"`, `PurposeStepUp = "step_up"`, with `StepUpProofTTL = 5 * time.Minute` (`backend/internal/auth/jwt.go:19-23`). `Generate2FAPendingToken` expires in 5 minutes (`backend/internal/auth/jwt.go:74-96`).
- `AuthMiddleware` rejects any token whose `claims.Purpose` is non-empty, so 2FA-pending and step-up JWTs cannot be used as normal API bearer tokens (`backend/internal/middleware/auth.go:35-45`). It also checks `token_version` against DB state (`backend/internal/middleware/auth.go:46-58`).
- TOTP setup stores a pending secret while `TOTPEnabled` remains false, then verify validates the stored secret, generates 8 recovery codes of length 8, stores them, enables TOTP, and returns the recovery codes to the caller (`backend/internal/api/handlers/auth_handler.go:283-371`; generator constants/functions in `backend/internal/auth/totp.go:14-50`).
- Recovery-code login first tries the current TOTP code; if that fails, it validates and consumes one stored recovery code, saves the remaining list, then issues the full token (`backend/internal/api/handlers/auth_handler.go:492-552`; consume helper in `backend/internal/auth/totp.go:53-72`). Tests prove the used recovery code is removed before token issuance and token issuance fails if the remaining-code save fails (`backend/internal/api/handlers/auth_user_handler_test.go:240-340`).
- TOTP disable requires current account password and current TOTP code, clears secret/recovery codes, disables TOTP, and increments `token_version` (`backend/internal/api/handlers/auth_handler.go:440-469`).
- Step-up requires an already authenticated user with TOTP enabled and a valid current TOTP code, then issues a 5-minute proof and writes credential-audit evidence (`backend/internal/api/handlers/auth_handler.go:384-420`). Frontend `ensureStepUpProof()` refuses to proceed when `totpEnabled` is false (`web/src/context/auth-context-provider.tsx:224-241`).

#### 4. Admin/user recovery primitives that already exist

- Role normalization accepts only `admin`, `operator`, and `viewer` (`backend/internal/auth/service.go:243-251`).
- Admin user-management service can create users, update role and/or password, and delete users; role/password changes increment `token_version`, invalidating old tokens (`backend/internal/auth/service.go:126-203`). Self-deletion is rejected (`backend/internal/auth/service.go:206-218`).
- `/users` CRUD routes are protected with `middleware.RBAC("users:manage")`, which only `admin` has in the current permission map (`backend/internal/api/router.go:146-149`; `backend/internal/middleware/rbac.go:9-79`). Handler tests cover CRUD as admin and 403 for non-admin (`backend/internal/api/handlers/auth_user_handler_test.go:160-238`).
- Frontend Settings page shows admin-only tabs (`users`, `channels`, `silences`, `escalation`, `system`, `maintenance`) only when `role === "admin"`; non-admin users see only `personal` and `account` (`web/src/pages/settings-page.tsx:21-39`, `web/src/pages/settings-page.tsx:131-139`).
- Admin user UI supports create, role change, password reset, delete, disables self role/delete, and displays a 2FA badge when `totpEnabled` is true (`web/src/pages/settings-page.users.tsx:73-159`, `web/src/pages/settings-page.users.tsx:209-274`).
- Account UI supports self password change and self TOTP enable/disable via dialogs (`web/src/pages/settings-page.account.tsx:22-45`, `web/src/pages/settings-page.account.tsx:112-163`).

#### 5. Settings security-risk summary pattern

- Backend response shape is `generated_at`, `summary.total_risks`, `summary.categories`, and `items[]` with stable `code`, `severity`, `title`, `description`, `count`, and bounded `examples` (`backend/internal/api/handlers/settings_handler.go:28-52`).
- Route is admin-only: `secured.GET("/settings/security-risk-summary", middleware.RequireRole("admin"), settingsHandler.SecurityRiskSummary)` (`backend/internal/api/router.go:323-324`). Router tests prove viewer receives 403 and admin receives 200 through the real stack (`backend/internal/api/router_test.go:144-190`).
- Current risk list includes SSH posture, credential-operation posture, `privileged_users_without_totp`, audit-log integrity, host-key trust, deployment secret posture, backup/restore posture, and weak security defaults (`backend/internal/api/handlers/settings_handler.go:202-264`).
- `privileged_users_without_totp` counts only `role IN (admin, operator)` with `totp_enabled=false`; examples include sanitized `username（role）`; severity falls to `info` when count is zero (`backend/internal/api/handlers/settings_handler.go:454-488`).
- `deployment_secret_posture` checks development mode, weak JWT secret, weak data-encryption key, placeholder `ADMIN_INITIAL_PASSWORD`, and missing `APP_ENV`; it returns labels, not raw env values (`backend/internal/api/handlers/settings_handler.go:707-740`). Placeholder admin values are `change-me`, `change-me-admin-password`, `admin`, `password`, and `please-change-me` (`backend/internal/api/handlers/settings_handler.go:747-754`).
- `weak_security_defaults` covers `WS_ALLOW_EMPTY_ORIGIN`, login captcha disabled, and second captcha disabled, with examples capped at `maxSecurityRiskExamples` (`backend/internal/api/handlers/settings_handler.go:886-936`).
- Backend tests seed representative risk data and assert counts, bounded examples, and absence of secret-shaped fields such as private keys, TOTP secrets, recovery codes, raw env values, host/IP/path/output fields (`backend/internal/api/handlers/settings_handler_test.go:61-345`).
- Frontend mapper keeps raw snake_case private, normalizes counts, severities, and known codes (`web/src/lib/api/settings-api.ts:26-145`). System tab loads settings/log-settings/risk summary together and renders risk cards (`web/src/pages/settings-page.system.tsx:35-63`, `web/src/pages/settings-page.system.tsx:157-212`). UI test asserts cards render without links or buttons in the risk-summary section (`web/src/pages/settings-page.system.test.tsx:117-146`).

#### 6. Existing high-risk access posture primitives

- Docs define high-risk temporary authorization for Web SSH terminal, config import, sensitive config export, snapshot restore, and task restore as combinations of valid admin auth, TOTP step-up proof, and short-lived matching grants (`docs/admin/security.md:75-87`).
- Router currently gates examples of sensitive operations with `RequireRole("admin")`, step-up proof, and grant middleware (e.g. credential-audit/grant admin routes, config export/import, task restore, snapshot restore) (`backend/internal/api/router.go:263-333`).
- Backend spec says grants are row-backed authorization records, additive to existing controls, fail closed on wrong user/role/action/purpose/resource/expiry, and must not store secrets, step-up proofs, OTP/recovery values, commands, terminal streams, exported payloads, endpoints, or host-sensitive strings (`.trellis/spec/backend/quality-guidelines.md:456-499`).
- Frontend spec says grant prompt state must stay component-local, reuse `ensureStepUpProof()`, not ask operation components to store TOTP codes, and not write grant IDs/reasons/status to browser storage (`.trellis/spec/frontend/state-management.md:71-116`).

### Comparable self-hosted / small-team convention mapping

These mappings are derived from existing repo behavior/docs and common self-hosted/small-team admin console patterns. No live external documentation lookup tool was available in this run, so the table avoids product-specific claims.

| Comparable convention | Common small-team shape | Existing Xirang primitive(s) | Notes / current fit |
|---|---|---|---|
| One-time bootstrap admin credential | First container/server start creates a local `admin` from an environment/bootstrap secret; after the DB user exists, the env value is not a rotating reset mechanism. | `ADMIN_INITIAL_PASSWORD` seeding in `SeedUsers`; password strength validation; deployment docs; `deployment_secret_posture` placeholder signal. | Fits current low-burden setup. Current code returns early when `admin` exists, so changing `ADMIN_INITIAL_PASSWORD` after first seed does not reset admin credentials. |
| Owner/admin password reset for local users | A signed-in admin can reset another local user’s password or create a second admin/operator account; this is the first-line small-team recovery path when at least one admin still has access. | `/users` CRUD behind `users:manage`; `auth.Service.UpdateUser` password update and `token_version` invalidation; frontend Users tab. | Fits existing primitives. It is not a recovery path if every admin is locked out or has lost 2FA/recovery codes. |
| TOTP recovery codes as low-friction 2FA fallback | When enabling authenticator-app 2FA, show single-use recovery codes once; login accepts either current TOTP or one unused recovery code. | `GenerateRecoveryCodes`; `TOTPVerify` returns codes; `TOTPLogin` consumes and saves remaining recovery codes before issuing full JWT; encrypted user fields. | Fits current implementation. No current user-facing inventory of remaining recovery-code count was found. |
| Read-only security posture checklist | Admin console shows advisory posture cards for high-risk defaults and access gaps; cards report counts/examples but do not mutate settings, users, keys, or hosts. | `GET /settings/security-risk-summary`; `privileged_users_without_totp`; `deployment_secret_posture`; `weak_security_defaults`; frontend System tab; risk-summary tests. | Strong fit for the requested “report-only/read-only” default. Existing specs explicitly forbid remediation links/actions from risk cards. |

### Related Specs

- `.trellis/spec/backend/quality-guidelines.md` — Settings Security Risk Summary contract: admin-only, read-only, advisory-only, stable codes, bounded/sanitized examples, and no mutation/remediation behavior (`.trellis/spec/backend/quality-guidelines.md:293-359`).
- `.trellis/spec/backend/quality-guidelines.md` — Credential Access Grant contract: row-backed, additive, fail-closed short-lived grants for high-risk credential operations; no secrets/proofs/commands/streams in grants/audit/logs (`.trellis/spec/backend/quality-guidelines.md:456-499`).
- `.trellis/spec/frontend/type-safety.md` — Settings risk-summary mapping contract: private raw snake_case types, safe code/severity fallbacks, advisory-only rendering, no client-side enrichment with host/credential details or remediation links/buttons (`.trellis/spec/frontend/type-safety.md:327-377`).
- `.trellis/spec/frontend/state-management.md` — Step-up/grant frontend state contract: use `ensureStepUpProof()`, keep prompt state component-local, avoid storing grant material/reason/status in local/session storage (`.trellis/spec/frontend/state-management.md:71-116`).
- `.trellis/spec/backend/deployment-runtime.md` — Deployment runtime contract: missing `.env` or required production secrets should fail startup through existing production config/bootstrap validation (`.trellis/spec/backend/deployment-runtime.md:34-42`).

### External References

- No external web references were fetched in this run because no web-search tool was available in the provided tool namespace. Comparable conventions above are explicitly marked as derived from repo behavior/docs plus common self-hosted practice rather than independently verified external product documentation.

## Caveats / Not Found

- No `break-glass`, `emergency admin`, admin recovery endpoint, admin 2FA bypass endpoint, or CLI/admin reset command was found in the inspected backend/frontend/docs/spec paths. Existing recovery surfaces are bootstrap seed-before-admin-exists, signed-in admin user management, self-service recovery codes, and advisory posture reporting.
- No current risk item was found for “zero admin users,” “only one admin,” “all admins lack TOTP,” “all admins have TOTP but no available recovery-code count,” or “stale/unrotated seeded admin password after first login.” Current Settings posture covers `admin`/`operator` users without TOTP and deployment env placeholder/weak secrets, but not those aggregate availability states.
- `ADMIN_INITIAL_PASSWORD` is bootstrap-only in current code and docs; after the `admin` row exists, `SeedUsers` returns without validating or applying the env value (`backend/internal/bootstrap/bootstrap.go:21-33`).
- Recovery code count/health is not exposed in `userResponse`; the API intentionally returns only `totp_enabled` and not the encrypted code list (`backend/internal/api/handlers/user_handler.go:20-25`, `backend/internal/api/handlers/user_handler.go:55-64`).
- Settings risk-summary specs in `.trellis/spec/backend/quality-guidelines.md` list an older subset of risk codes at `.trellis/spec/backend/quality-guidelines.md:323`; code now also includes `privileged_users_without_totp`, `audit_log_integrity_posture`, `ssh_host_key_trust_posture`, `deployment_secret_posture`, and `backup_restore_posture` (`backend/internal/api/handlers/settings_handler.go:202-264`).
- Per query boundary, enterprise policy/device trust/approval/session recording/full Vault/KMS/SSH CA/WebAuthn/passkeys were not researched or mapped.
