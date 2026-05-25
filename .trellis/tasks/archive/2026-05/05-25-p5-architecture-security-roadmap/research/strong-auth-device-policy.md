# Research: Strong auth / device trust / enterprise policy UI

- **Query**: Research the P5 WebAuthn/passkeys / device trust / enterprise policy UI direction for Xirang. Context: current auth includes JWT/RBAC/TOTP/recovery/login lockout/step-up-like sensitive flows. P5 should begin with a bounded, behavior-compatible enabling slice, not a full product redesign. Inspect auth middleware, frontend auth context, TOTP components, system settings, user model, policy surfaces, and docs/env constraints. Output should cover reviewed files/patterns, comparable approaches, feasible first slices, risks/blast radius, and recommendation.
- **Scope**: mixed internal/external
- **Date**: 2026-05-25

## Findings

### Executive Summary

Xirang already has a usable strong-auth foundation built around password login, optional TOTP, recovery codes, short-lived 2FA pending JWTs, short-lived step-up JWT proofs, RBAC, token-version invalidation, login rate limiting, persistent login-failure lockout, and high-risk temporary credential grants. The current architecture is compatible with a future passkey/WebAuthn direction, but a full passkey login/product redesign has broad blast radius across origin/RP configuration, DB schema, login and account UI, challenge lifecycle, recovery, and deployment documentation.

The lowest-risk P5 first slice is not full WebAuthn or trusted-device bypass. The best bounded enabling slice is a **report-only strong-auth posture / enterprise policy UI foundation** that uses existing data (`users.totp_enabled`, roles, current Settings risk summary patterns) to surface admin/operator strong-auth enrollment and policy readiness without changing login behavior or requiring new infrastructure. If P5 must include an actual WebAuthn ceremony in the first implementation, the narrower alternative is optional **passkey as step-up provider** only, disabled by default, not primary login and not trusted-device bypass.

### Files Found

| File Path | Description |
|---|---|
| `backend/internal/auth/jwt.go` | JWT claims, purpose-scoped tokens, 2FA pending token, step-up proof token, revocation logic. |
| `backend/internal/auth/service.go` | Password login flow, TOTP-required branching, user management, token version invalidation on password/role changes. |
| `backend/internal/auth/totp.go` | TOTP secret generation/validation and recovery-code generation/consumption. |
| `backend/internal/auth/login_lock.go` | Persistent username+IP login lockout with dynamic settings. |
| `backend/internal/middleware/auth.go` | Bearer-token auth middleware; rejects purpose-scoped tokens and checks token_version. |
| `backend/internal/middleware/rbac.go` | Static role-to-permission map for admin/operator/viewer and role/permission middleware. |
| `backend/internal/middleware/ownership.go` | Operator node/task ownership guard and current-user helpers. |
| `backend/internal/api/handlers/auth_handler.go` | Login, `/me`, password change, TOTP setup/verify/disable/login, and `/auth/step-up` handlers. |
| `backend/internal/api/handlers/step_up.go` | `X-Xirang-Step-Up` proof enforcement, proof validation, and credential-audit event writing. |
| `backend/internal/api/handlers/credential_access_grant.go` | High-risk operation grants bound to action/purpose/resource, using step-up plus short TTL. |
| `backend/internal/api/handlers/settings_handler.go` | Admin settings API and read-only security risk summary surface. |
| `backend/internal/api/handlers/user_handler.go` | Admin user list/create/update/delete DTOs, including `totp_enabled`. |
| `backend/internal/api/router.go` | Auth, users, settings, credential grants, step-up-protected high-risk routes, and CORS/security headers. |
| `backend/internal/model/models.go` | `User`, `LoginFailure`, `SystemSetting`, `CredentialAuditEvent`, and `CredentialAccessGrant` models plus encryption hooks. |
| `backend/internal/settings/service.go` | Dynamic settings registry, DB/env/default resolution, validation, cache, and update/reset behavior. |
| `backend/internal/config/config.go` | Auth/security env parsing and production weak-secret checks. |
| `backend/internal/database/migrations/sqlite/000011_user_totp.up.sql` | Adds `totp_secret`, `totp_enabled`, `recovery_codes`. |
| `backend/internal/database/migrations/sqlite/000019_user_token_version.up.sql` | Adds `users.token_version`. |
| `backend/internal/database/migrations/sqlite/000025_system_settings.up.sql` | Adds `system_settings`. |
| `backend/internal/database/migrations/sqlite/000059_ssh_key_scope_credential_audit.up.sql` | Adds credential audit event table and SSH key scope metadata. |
| `backend/internal/database/migrations/sqlite/000060_credential_access_grants.up.sql` | Adds credential access grants. |
| `web/src/context/auth-context-provider.tsx` | Browser auth state, sessionStorage token storage, TOTP flag, step-up dialog/proof flow. |
| `web/src/context/auth-context.shared.ts` | Auth context contract including `totpEnabled` and `ensureStepUpProof`. |
| `web/src/lib/step-up-storage.ts` | SessionStorage storage for short-lived step-up proof and expiry. |
| `web/src/lib/api/core.ts` | API request wrapper, Authorization header, `X-Xirang-Step-Up`, error-code helpers. |
| `web/src/lib/api/auth-api.ts` | Login/logout/password API wrapper. |
| `web/src/lib/api/totp-api.ts` | TOTP setup/verify/login/disable and step-up proof API wrapper. |
| `web/src/lib/api/users-api.ts` | User DTO mapper including `totp_enabled` -> `totpEnabled`. |
| `web/src/lib/api/settings-api.ts` | Settings and security-risk summary API mapper. |
| `web/src/pages/login-page.tsx` | Password login, captcha, and TOTP second step UI. |
| `web/src/pages/settings-page.account.tsx` | Account tab for password change and TOTP enable/disable. |
| `web/src/components/totp-setup-dialog.tsx` | TOTP enrollment dialog with QR code, verification, recovery codes. |
| `web/src/components/totp-disable-dialog.tsx` | TOTP disable dialog requiring password and TOTP code. |
| `web/src/pages/settings-page.tsx` | Settings tab visibility; admin-only user/system/security surfaces. |
| `web/src/pages/settings-page.system.tsx` | Generic system settings editor and read-only security risk summary cards. |
| `web/src/pages/settings-page.users.tsx` | Admin user management tab showing TOTP badges. |
| `docs/admin/security.md` | Production security documentation for HTTPS, login protections, TOTP, step-up grants, sensitive fields. |
| `docs/env-vars.md` | Auth/security env variables and settings key mapping. |
| `.trellis/spec/backend/database-guidelines.md` | Migration, model, and sensitive-field conventions. |
| `.trellis/spec/backend/error-handling.md` | API envelope and fail-closed auth/ownership error guidance. |
| `.trellis/spec/backend/logging-guidelines.md` | Sensitive logging exclusions including TOTP/JWT/recovery values. |
| `.trellis/spec/frontend/type-safety.md` | API mapper contracts, storage guards, and security-risk mapping conventions. |

### Code Patterns

#### Current auth token model is purpose-scoped and behavior-compatible with future assurance levels

- `backend/internal/auth/jwt.go:19-23` defines purpose constants: `Purpose2FAPending`, `PurposeStepUp`, and `StepUpProofTTL = 5 * time.Minute`.
- `backend/internal/auth/jwt.go:25-31` includes `Purpose` and `TokenVersion` in claims.
- `backend/internal/auth/jwt.go:74-96` generates a 5-minute 2FA pending token with purpose `2fa_pending` after password verification.
- `backend/internal/auth/jwt.go:98-124` generates a 5-minute step-up proof token with purpose `step_up`.
- `backend/internal/auth/jwt.go:126-146` generates normal JWTs without purpose for ordinary API access.
- `backend/internal/middleware/auth.go:21-65` parses Bearer tokens, rejects any non-empty `claims.Purpose`, checks DB `token_version`, and stores user id/username/role/token in Gin context.
- `backend/internal/api/handlers/realtime_auth.go:19-50` applies equivalent token-purpose and token-version checks for WebSocket protocol auth.

Implication: future passkey assurance can fit as another proof source if it preserves the existing separation between normal session tokens and purpose-scoped proof tokens. Primary passkey login would touch more surfaces than passkey-as-step-up because normal AuthMiddleware intentionally rejects purpose-scoped JWTs.

#### Current login and TOTP flow is optional and non-breaking

- `backend/internal/auth/service.go:80-116` checks username/password, clears login failure state on success, returns `Requires2FA` plus a 2FA pending token when `user.TOTPEnabled` is true, otherwise returns a normal JWT.
- `backend/internal/api/handlers/auth_handler.go:94-152` exposes this as `/auth/login`; the response is either `{requires_2fa, login_token}` or `{token, user}`.
- `backend/internal/api/handlers/auth_handler.go:492-552` completes `/auth/2fa/login` by requiring a `login_token` whose purpose is `2fa_pending`, then validating either TOTP or a recovery code before issuing a normal JWT.
- `web/src/pages/login-page.tsx:73-95` handles the password login response and switches to the TOTP step if `requires_2fa` is present.
- `web/src/pages/login-page.tsx:127-149` submits the 2FA login token plus one-time code to finish login.

Implication: a first P5 slice should not change the default login response contract unless it is explicitly a product decision. WebAuthn as a primary login method would need a new ceremony state machine before issuing the current normal JWT.

#### TOTP secret/recovery storage uses model hooks and token invalidation

- `backend/internal/model/models.go:13-24` defines `User` fields: `TOTPSecret`, `TOTPEnabled`, `RecoveryCodes`, `TokenVersion`, and `Onboarded`.
- `backend/internal/model/models.go:690-725` encrypts/decrypts `TOTPSecret` and `RecoveryCodes` with GORM hooks.
- `backend/internal/api/handlers/auth_handler.go:274-371` implements TOTP setup/verify; setup stores a pending secret, verify activates TOTP and returns recovery codes.
- `backend/internal/api/handlers/auth_handler.go:440-474` disables TOTP only after password and TOTP validation, clears secret/recovery codes, and increments `token_version`.
- `backend/internal/auth/service.go:192-195` increments `token_version` when password or role is changed; `backend/internal/auth/service.go:220-240` increments it on self-service password change.

Implication: WebAuthn credential storage should follow the same “do not expose secrets, invalidate stale sessions on auth-factor removal/policy weakening” principle. Public keys are not secrets, but credential IDs, labels, transports, sign counters, attestation metadata, and device names still need privacy-aware handling and sanitized API responses.

#### Step-up is already the high-risk auth boundary

- `backend/internal/api/handlers/step_up.go:17-23` defines `X-Xirang-Step-Up`, `STEP_UP_REQUIRED`, 300-second TTL, and audit credential kind/source.
- `backend/internal/api/handlers/step_up.go:58-75` requires a proof header, audits missing/failed/satisfied proof states, and returns a machine-readable error.
- `backend/internal/api/handlers/step_up.go:77-112` validates proof purpose, user id, role, DB role, token version, and `totp_enabled`.
- `backend/internal/api/handlers/auth_handler.go:384-420` issues step-up proofs only after validating TOTP.
- `web/src/context/auth-context-provider.tsx:224-252` exposes `ensureStepUpProof`, checks cached proofs, rejects unauthenticated/non-TOTP users, and opens the step-up dialog.
- `web/src/context/auth-context-provider.tsx:265-297` posts the TOTP code to `/auth/step-up`, stores the proof only when requested, and resolves the pending request.
- `web/src/lib/step-up-storage.ts:40-67` stores proof and expiry in sessionStorage and clears expired entries.
- `web/src/lib/api/core.ts:49-67` sends `Authorization` and optional `X-Xirang-Step-Up` headers.
- `web/src/lib/api/core.ts:173-191` detects `STEP_UP_REQUIRED` and `CREDENTIAL_GRANT_REQUIRED` by machine-readable error codes rather than localized message text.

Implication: passkey support is most bounded if introduced as another way to obtain the existing step-up proof, rather than as passwordless primary login or a trusted-device bypass.

#### Temporary credential grants layer step-up with action/purpose/resource binding

- `backend/internal/api/handlers/credential_access_grant.go:25-52` defines grant statuses, high-risk actions, purposes, min/default/max TTL, and `CREDENTIAL_GRANT_REQUIRED`.
- `backend/internal/api/handlers/credential_access_grant.go:480-503` validates grant requests by first enforcing step-up and then checking current role context, reason, and TTL.
- `backend/internal/api/handlers/credential_access_grant.go:632-754` enforces active grants for terminal, config import/export, snapshot restore, task restore, task manual trigger, batch trigger, and batch command.
- `backend/internal/api/handlers/credential_access_grant.go:795-840` matches active grants by requester user/role, action, purpose, and resource IDs, marks expired grants, and fails closed for invalid/expired/revoked/denied states.
- `backend/internal/model/models.go:446-471` stores safe grant lifecycle metadata only.

Implication: the current high-risk operation boundary already separates “prove the actor is present” from “authorize this operation/resource for a short time.” A device-trust feature that bypasses this would weaken an intentional layered model unless treated as report-only inventory or an additional signal, not as a bypass.

#### RBAC and enterprise policy surfaces are simple/static today

- `backend/internal/middleware/rbac.go:9-80` statically maps admin/operator/viewer to permissions.
- `backend/internal/middleware/rbac.go:82-110` exposes `HasPermission`, `RequireRole`, and `RBAC`; there is no dynamic policy engine.
- `backend/internal/middleware/ownership.go:20-60` applies object ownership only to operators for node routes; admin/viewer bypass the ownership check by design.
- `backend/internal/api/router.go:323-326` restricts Settings API to admin; `backend/internal/api/router.go:146-149` restricts user management via `users:manage`.
- `web/src/pages/settings-page.tsx:28-29` shows only `personal` and `account` tabs to non-admins, while admins see all settings tabs.

Implication: “enterprise policy UI” should start as advisory/report-only or very narrow settings-driven gates. A full dynamic auth policy engine is a product redesign.

#### Dynamic settings are good for simple flags, not rich policy matrices

- `backend/internal/settings/service.go:72-107` registers simple settings definitions. Existing security settings are login rate/lock/captcha booleans and durations.
- `backend/internal/settings/service.go:143-168` resolves settings as DB override → env var → code default.
- `backend/internal/settings/service.go:210-324` validates only int/bool/duration/string values, with a max value length of 256.
- `backend/internal/api/handlers/settings_handler.go:122-170` batch-updates settings atomically and logs key/change facts without raw values.
- `web/src/pages/settings-page.system.tsx:214-278` renders settings generically by type with simple controls.

Implication: simple policy toggles like report-only flags can reuse settings. Rich enterprise rules such as per-role/per-factor/per-operation enforcement matrices do not fit the current settings registry without a new model/API/UI.

#### Existing security-risk summary is a ready-made report-only policy UI pattern

- `backend/internal/api/handlers/settings_handler.go:79-108` serves an admin-only read-only security risk summary.
- `backend/internal/api/handlers/settings_handler.go:201-246` aggregates risk items.
- `backend/internal/api/handlers/settings_handler.go:546-598` includes weak security defaults such as login captcha not enabled.
- `web/src/lib/api/settings-api.ts:26-45` defines constrained risk code/severity types.
- `web/src/lib/api/settings-api.ts:114-124` maps raw snake_case summary data to safe camelCase data with fallback defaults.
- `web/src/pages/settings-page.system.tsx:157-212` renders risk cards without remediation actions.
- `.trellis/spec/frontend/type-safety.md:327-377` explicitly documents Settings security-risk summary mapping and that risk cards are advisory only.

Implication: adding a report-only “admins/operators without strong auth” posture is the smallest behavior-compatible enterprise-policy UI slice because it reuses an existing admin-only advisory surface and existing user/TOTP data.

#### Frontend auth storage is session-scoped; no trusted-device storage exists

- `web/src/context/auth-context-provider.tsx:84-140` reads current auth state from sessionStorage and migrates legacy localStorage token state into sessionStorage, then removes legacy localStorage keys.
- `web/src/context/auth-context-provider.tsx:151-190` writes new login state to sessionStorage and clears localStorage auth keys.
- `web/src/context/auth-context-provider.tsx:193-209` clears session/local auth keys and step-up proof on logout.
- `web/src/lib/step-up-storage.ts:1-67` stores only short-lived step-up proof and expiry in sessionStorage.

Implication: device trust would require a new persistent client token/cookie strategy plus revocation, expiry, privacy copy, and abuse controls. That is not behavior-compatible by default and should not be the first P5 slice.

#### Docs/deployment constraints matter for passkeys

- `docs/admin/security.md:17-27` says the All-in-One container exposes HTTP and public HTTPS should be terminated by an external reverse proxy; CORS allowed origins should be configured for external domain access.
- `docs/env-vars.md:36-47` lists current auth/security env vars; there are no WebAuthn RP ID/origin/display-name variables.
- `backend/internal/api/router.go:54-83` sets CORS and security headers; `Access-Control-Allow-Headers` currently includes `Authorization`, `Content-Type`, and `X-Xirang-Step-Up`.
- `backend/internal/config/config.go:195-210` rejects weak JWT/data-encryption settings and `CORS_ALLOWED_ORIGINS=*` in production.

Implication: WebAuthn verification must know the browser-visible RP ID and origin, which are not necessarily the backend listen address in Xirang’s deployment model. RP/origin config is an unavoidable passkey prerequisite.

### Comparable Approaches

| Approach | How it maps to Xirang | Compatibility / blast radius |
|---|---|---|
| Current TOTP-only strong auth | Already implemented as optional login second factor and required step-up proof for selected high-risk operations. | Lowest risk; no new browser/deployment constraints. Does not provide phishing-resistant auth. |
| Report-only enterprise strong-auth posture | Use existing roles and `totp_enabled` to show admin/operator enrollment/policy readiness in Settings security risk summary or a small admin policy section. | Behavior-compatible; no login/API flow change. Prepares policy language and UI without lockouts. |
| WebAuthn/passkey as step-up provider | Keep password+JWT login, add optional passkey ceremony to mint the existing `PurposeStepUp` proof. | Medium blast radius: new schema/challenge storage/RP config/frontend ceremony, but high-risk route behavior can stay unchanged. |
| WebAuthn/passkey enrollment inventory only | Let users register/delete passkeys but do not use them for login or step-up yet. | Behavior-compatible but lower immediate security value; still requires RP config and ceremony correctness. |
| WebAuthn/passkey as primary second factor for login | Replace/augment TOTP login second step after password. | Higher blast radius: login page state machine, recovery, account lockout, rollout policy, and support burden. |
| Passwordless/passkey-primary login | Start login with discoverable credential and issue normal JWT on success. | Highest blast radius: new identity flow, username-less UX, recovery/admin bootstrap, audit, and docs redesign. |
| Trusted device / remember this device | Persist a device token to reduce repeated 2FA/step-up prompts. | Security-sensitive and privacy-sensitive; conflicts with current sessionStorage-only posture and could bypass high-risk step-up/grants. Not first slice. |
| Dynamic enterprise auth policy engine | Per-role/per-operation factor requirements, enforcement modes, exceptions, rollout/audit. | Product redesign; current settings registry supports simple values only, not a policy matrix. |

### External References

External URLs were availability-checked on 2026-05-25 and returned HTTP 200. Dependency versions were not pinned by this research; verify releases before implementation.

- [W3C Web Authentication: An API for accessing Public Key Credentials Level 3](https://www.w3.org/TR/webauthn-3/) — canonical WebAuthn ceremony model: relying party, origin/RP ID, challenge/response, authenticator selection, user verification, and public-key credential assertions.
- [MDN Web Authentication API](https://developer.mozilla.org/en-US/docs/Web/API/Web_Authentication_API) — browser-facing constraints and API shape via `navigator.credentials.create()` / `navigator.credentials.get()` and secure-context expectations.
- [go-webauthn/webauthn](https://github.com/go-webauthn/webauthn) — Go library option matching Xirang’s backend stack for registration/login ceremonies and credential storage integration.
- [SimpleWebAuthn docs](https://simplewebauthn.dev/docs/) — TypeScript/browser helper ecosystem; relevant for frontend ceremony helpers, though Xirang’s backend is Go.
- [NIST SP 800-63B](https://pages.nist.gov/800-63-4/sp800-63b.html) — digital identity authenticator guidance; relevant for phishing-resistant authenticators, verifier name binding, reauthentication, and authenticator lifecycle framing.

### Related Specs

- `.trellis/spec/backend/database-guidelines.md` — migrations must be paired for SQLite/PostgreSQL; sensitive-field handling belongs in model/service boundaries; no raw secrets in responses.
- `.trellis/spec/backend/error-handling.md` — auth uncertainty should fail closed; use standard response helpers and avoid leaking raw internals.
- `.trellis/spec/backend/logging-guidelines.md` — do not log TOTP secrets, JWTs, recovery codes, decrypted values, endpoints, command output, or credential evidence.
- `.trellis/spec/frontend/type-safety.md` — keep raw snake_case API types private, map to camelCase at API boundary, use finite-number fallbacks, and keep risk summary cards advisory-only.
- `.trellis/spec/frontend/a11y-guidelines.md` — any new auth dialogs/policy UI need accessible labels, focus management, and keyboard behavior consistent with existing dialogs.

## Feasible First Slices

### Slice 1 — Report-only strong-auth posture / enterprise policy UI foundation (recommended)

Shape:

- Use existing `users.role` and `users.totp_enabled` as the current strong-auth signal.
- Surface admin/operator accounts without TOTP in a read-only admin-facing posture/risk UI, preferably by extending the existing Settings security-risk summary pattern.
- Keep this in report-only/advisory mode: no login enforcement, no TOTP auto-enrollment requirement, no WebAuthn dependency, no trusted-device bypass.
- If a setting is needed, keep it simple and default-compatible, for example an advisory/report-only policy flag in the existing `security` settings category. Avoid a rich policy matrix in the first slice.

Why it is feasible:

- Existing backend already exposes user TOTP status through user DTOs (`backend/internal/api/handlers/user_handler.go:20-25`, `backend/internal/auth/service.go:118-124`).
- Existing frontend already maps and displays `totpEnabled` (`web/src/lib/api/users-api.ts:11-17`, `web/src/pages/settings-page.users.tsx:218-223`).
- Existing Settings security-risk summary is admin-only and advisory (`backend/internal/api/handlers/settings_handler.go:79-108`, `web/src/pages/settings-page.system.tsx:157-212`).
- No new infrastructure, RP ID, browser ceremony, DB secret type, or login behavior change is required.

Validation shape if implemented:

- Backend handler tests for counts/examples of admin/operator users without TOTP.
- Frontend API mapper test for any new risk code and invalid fallback behavior.
- Settings UI test that risk cards remain advisory and have no remediation/mutation action.
- Existing backend/frontend package checks.

### Slice 2 — Optional passkey enrollment inventory only

Shape:

- Add passkey credential registration/delete UI and backend storage, but do not use credentials for login or step-up yet.
- Requires WebAuthn RP config, challenge storage, credential table, and account UI.

Pros:

- Behavior-compatible for login and high-risk operations.
- Establishes DB/API ceremony groundwork.

Cons:

- User-visible enrollment that does not affect auth may confuse users.
- Still has WebAuthn ceremony and deployment complexity.
- Less immediate security value than step-up provider.

### Slice 3 — Optional passkey step-up provider

Shape:

- Add passkey credentials and allow users to satisfy `/auth/step-up` through a passkey assertion, producing the existing `PurposeStepUp` proof.
- Keep TOTP path as-is; default behavior remains unchanged unless a user registers a passkey and chooses it.
- Do not use passkeys for primary login in this slice.

Pros:

- Reuses the current high-risk proof boundary and `X-Xirang-Step-Up` header.
- Can add phishing-resistant presence verification where it matters most: terminal, config secret export/import, snapshot/task restore, manual/batch command operations.

Cons:

- Medium blast radius: WebAuthn library/dependency, RP config, challenges, new table, account UI, step-up dialog changes, tests.
- Needs clear recovery behavior when passkeys are unavailable.

### Slice 4 — Trusted-device inventory or bypass

Shape:

- Persistent device token and optional “remember this device” behavior.

Assessment:

- Not recommended as a first slice. It needs durable client-side state/cookies, token hashing, rotation, revocation UI, suspicious-use audit, privacy copy, and careful interaction with step-up/grants.
- It risks weakening the current high-risk presence checks if used as a bypass.

### Slice 5 — Full passkey login or passwordless login

Assessment:

- Not recommended as a first slice. It changes the login state machine, deployment config, account recovery/admin bootstrap story, onboarding, and support posture.
- Better after report-only policy posture and/or optional step-up provider proves the WebAuthn stack.

## Risks / Blast Radius

### Origin/RP ID and HTTPS deployment

WebAuthn binds credentials to a relying party ID and verifies browser-visible origins. Xirang’s All-in-One image serves HTTP internally and expects public HTTPS termination at an external proxy (`docs/admin/security.md:17-27`). That means a passkey implementation needs explicit RP ID/origin configuration aligned to the public URL, not `SERVER_ADDR` or container host. Local development can use localhost-style secure-context exceptions, but production cannot assume plain HTTP.

Blast radius: config/env docs, Docker examples, backend verifier config, tests for origin mismatch, operator docs for reverse proxy/CORS.

### Challenge/session lifecycle

Current login is mostly stateless after password verification, using signed JWTs for 2FA pending and step-up. WebAuthn requires fresh challenges and replay prevention for registration and assertion ceremonies. Existing login lockout and rate limits do not provide WebAuthn challenge storage.

Blast radius: new short-lived DB/cache records or signed challenge state with replay tracking, cleanup, tests, and failure handling.

### Account recovery and rollout safety

TOTP has recovery codes (`backend/internal/auth/totp.go:35-73`). Passkeys can be lost, synced unpredictably depending on platform, or be unavailable on some browsers/devices. Enforcing passkeys too early risks admin lockout.

Blast radius: recovery UX, admin reset capability, audit events, docs, bootstrap/admin emergency path.

### Token and proof semantics

AuthMiddleware rejects purpose-scoped tokens for normal API access (`backend/internal/middleware/auth.go:41-45`). Step-up proof validation also checks role, token version, DB role, and TOTP enabled (`backend/internal/api/handlers/step_up.go:77-112`). A passkey proof path must preserve those invariants or introduce an explicit replacement invariant such as “has active WebAuthn credential at proof time.”

Blast radius: auth handler tests, router tests for high-risk routes, credential audit, frontend `ensureStepUpProof` behavior.

### Device trust could weaken existing hardening

Current browser auth/proof storage is session-scoped and cleared on logout (`web/src/context/auth-context-provider.tsx:151-209`, `web/src/lib/step-up-storage.ts:1-67`). A trusted-device feature usually needs persistent storage and server-side revocation. If it bypasses TOTP/step-up/grants, it reduces protection for terminal, restore, config import/export, and command operations.

Blast radius: persistent token model, browser storage/cookie choices, revocation UI, audit logs, privacy policy/copy, abuse handling.

### Enterprise policy complexity

The settings registry supports simple values only and caps values at 256 chars (`backend/internal/settings/service.go:26-28`, `backend/internal/settings/service.go:210-324`). A real policy matrix needs richer storage, lifecycle, defaults, dry-run/enforce modes, exemptions, and audit.

Blast radius: new model/API/UI, docs, migration, tests, and compatibility behavior for existing deployments.

### Dependency and version risk

No WebAuthn/passkey dependencies are currently present in `backend/go.mod` or `web/package.json` (`backend/go.mod:5-25`, `web/package.json:16-45`). Adding one requires version/license/security review and release verification before commit.

## Recommendation

Start P5 strong-auth/device-policy work with **Slice 1: report-only strong-auth posture / enterprise policy UI foundation**.

This is the best first slice because it is behavior-compatible, local-only, deployable in the current Docker/reverse-proxy model, and uses existing data and UI contracts. It also creates the right product language for future enforcement: “which roles/operations require a strong factor, which users are ready, and what is advisory vs enforced” without immediately changing authentication behavior or introducing RP/origin/challenge complexity.

Do not start with trusted-device bypass. It works against the current direction of short-lived sessionStorage tokens, explicit step-up, and operation-bound grants. Treat device trust later as an audit/inventory signal or additional risk factor, not as a first bypass mechanism.

If the implementation decision requires actual passkey functionality in the first P5 code slice, choose **optional passkey step-up provider** rather than primary login. Keep the existing password+TOTP flow intact, mint the existing `PurposeStepUp` proof after a WebAuthn assertion, and make it opt-in. That confines passkeys to the current high-risk proof boundary and avoids a full login redesign.

## Caveats / Not Found

- No existing WebAuthn/passkey code, `navigator.credentials` usage, `go-webauthn`, `@simplewebauthn`, or equivalent dependency was found in the repository.
- No trusted-device model, persistent device token, remembered-device setting, or revocation UI was found.
- No dynamic enterprise auth policy model was found; current RBAC is static and current settings are simple typed key/value definitions.
- External references were checked for availability only; this research did not pin dependency versions. Per project memory, verify dependency/release versions before driving any commit that adds WebAuthn libraries.
- This file is research only. No code outside this research file was modified.
