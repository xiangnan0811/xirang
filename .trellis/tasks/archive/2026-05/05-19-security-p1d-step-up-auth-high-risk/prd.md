# P1d Step-up Authentication for High-risk Operations

## Goal

Require a recent stronger authentication check before allowing selected high-risk credential operations, reducing the blast radius of a stolen active session while preserving normal day-to-day usage. The enforcement must live at backend boundaries and produce bounded credential-audit evidence without storing secrets, OTP values, recovery codes, challenge tokens, command text/output, exported config payloads, file content, terminal streams, raw host-sensitive strings, or executor config.

## Requirements

- Add a short-lived step-up proof flow built on the existing TOTP capability and JWT infrastructure.
  - Issue a dedicated step-up JWT with a distinct `purpose` value, short TTL, current user identity, role, token version, JTI, issued-at, and expiry.
  - Validate step-up proof server-side against the same user, current token version, expiry, and purpose.
  - Do not store or log OTP values, recovery codes, step-up tokens, bearer tokens, or raw challenge inputs.
- Add a backend step-up verification endpoint for authenticated users with enabled TOTP.
  - Input: TOTP code only.
  - Output: short-lived step-up token/proof and expiry metadata.
  - Failure must return a sanitized error and must not disclose whether a code was close, replayed, or otherwise partially valid.
- Enforce step-up at backend high-risk boundaries; frontend state alone must never grant access.
  - `GET /ssh-keys/export`.
  - `GET /config/export?include_secrets=true` only; normal config export without secrets remains unchanged.
  - `GET /ws/terminal` first-message auth/open flow.
  - `POST /tasks/:id/trigger`.
  - `POST /tasks/batch-trigger`.
  - `POST /tasks/:id/restore`.
  - `POST /tasks/:id/snapshots/:sid/restore`.
  - `POST /batch-commands`.
- Preserve existing RBAC and ownership semantics.
  - Step-up is additive: requests still need the original auth, role/RBAC, and ownership checks.
  - A valid step-up proof for one user must not satisfy another user's request.
  - Password/role/2FA changes that increment token version must invalidate existing step-up proofs.
- Return a machine-readable step-up-required response that frontend code can distinguish from session expiration.
  - Do not use a response shape that causes the existing frontend API wrapper to treat step-up-required as login/session expiry.
  - Include only bounded, non-sensitive metadata such as an error code and proof TTL.
- Add frontend step-up prompt/retry handling for covered UI flows.
  - Prompt only when backend indicates step-up is required.
  - Store the step-up proof in session-scoped frontend state/storage only for its short TTL.
  - Attach the proof to retried high-risk requests, including direct-download and WebSocket auth flows.
  - Clear proof on logout and after expiry.
- Extend credential audit evidence for covered operations.
  - Log blocked/required, satisfied, and failed step-up outcomes using bounded safe metadata keys that pass existing credential-audit sanitizers.
  - Do not add metadata keys containing forbidden markers such as `token`, `credential`, `config`, `command`, `content`, `payload`, `output`, `stream`, `private`, `password`, or `secret`.
  - Snapshot restore currently lacks credential audit coverage; add safe bounded audit events for that covered restore surface.
- Keep the MVP focused on TOTP-based step-up.
  - Do not add WebAuthn/passkeys, device trust, per-operation policy editing UI, admin-configurable TTL, email/SMS OTP, or long-lived remember-this-device behavior.

## Acceptance Criteria

- A high-risk backend request without a valid recent step-up proof is blocked before executing the operation.
- Direct backend calls cannot bypass step-up, including direct CSV/file downloads and terminal WebSocket auth.
- A valid step-up proof allows covered high-risk operations only for the same authenticated user and only until expiry.
- Step-up proof is invalid after token-version changes such as password/role/2FA disablement.
- Config export only requires step-up when `include_secrets=true`; normal config export remains unchanged.
- Original RBAC and ownership denials continue to win for users who lack permission, regardless of step-up proof.
- Credential audit records show step-up required/satisfied/failed/blocked evidence without sensitive values or forbidden metadata keys.
- Frontend prompts for TOTP step-up on covered flows, retries the original action after success, and shows sanitized errors on failure/expiry.
- Backend tests cover success, missing proof, expired/invalid proof, wrong-user proof, token-version mismatch, RBAC/ownership interaction, config-export secret-only gating, terminal WebSocket auth gating, and snapshot restore audit evidence.
- Frontend tests cover API mapper/wrapper behavior, prompt/retry handling, proof expiry/clear behavior, direct download proof attachment, WebSocket auth proof attachment, and no session-expiry redirect for step-up-required responses.

## Definition of Done

- Backend `go test ./... -count=1` passes.
- Frontend `npm run check` passes.
- Targeted backend and frontend tests are added for all new step-up behavior.
- Trellis task validates successfully.
- No raw private keys, passwords, tokens, executor config, terminal streams, command output, file contents, Docker command output, diagnostic output, exported secret material, raw SQL, or host-sensitive strings are added to API responses, logs, audit records, docs, tests, or UI state.

## Technical Approach

- Reuse existing JWT claims and TOTP validation patterns rather than introducing a new dependency.
- Add a dedicated step-up purpose token to `auth.JWTManager` and a validation helper that checks:
  - signature and expiry;
  - purpose matches the step-up purpose;
  - proof user ID equals authenticated user ID;
  - proof token version equals current user token version.
- Add an authenticated endpoint under `/api/v1/auth/step-up` (exact path may be adjusted to existing route naming) that verifies the current user's TOTP code and returns a short-lived proof.
- Add backend middleware/helper for REST high-risk routes that accepts the proof through a dedicated request header and returns a 403 step-up-required envelope when missing/invalid/expired.
- Extend terminal WebSocket auth message to accept a step-up proof alongside the normal auth token and enforce it before opening SSH.
- Add frontend API support for requesting a step-up proof and a shared prompt/retry utility/component that high-risk flows can reuse.
- For direct downloads and WebSocket flows that do not use the central `request<T>()`, explicitly attach the step-up proof and handle step-up-required responses.

## Decision (ADR-lite)

**Context**: P1c exposed credential audit review/export, and P1/P1b broadened credential-use evidence. The next blast-radius reducer is preventing a stolen active session from immediately opening terminals, exporting keys/config secrets, or triggering destructive/credential-sensitive operations.

**Decision**: Implement TOTP-backed short-lived step-up proof as an additive backend-enforced gate for a bounded initial high-risk operation set. Use existing JWT/TOTP/token-version primitives, not a new database table or external MFA provider.

**Consequences**: This keeps scope small and compatible with current 2FA, but users without TOTP enabled cannot complete step-up until they enable 2FA. More advanced policies such as device trust, configurable TTL, WebAuthn, or per-operation admin tuning remain future work.

## Out of Scope

- WebAuthn/passkey step-up.
- Email/SMS OTP.
- Remember-this-device or long-lived trust grants.
- Admin policy UI for choosing operations or TTL.
- Replacing existing login 2FA.
- Broadly requiring step-up for all writes or all admin routes.
- Changing credential/key scoping compatibility from P1/P1b.

## Research References

- [`research/code-surfaces.md`](research/code-surfaces.md) — Existing JWT/TOTP, route, credential-audit, and frontend high-risk surfaces mapped; no existing step-up proof implementation found.

## Technical Notes

- Existing `2fa_pending` JWT pattern in `backend/internal/auth/jwt.go` is the closest backend primitive for a short-lived proof.
- Existing `AuthMiddleware` rejects purpose-scoped pending tokens for normal API access; step-up proof should be a secondary proof, not a replacement bearer auth token.
- Terminal WebSocket is outside the secured Gin group and must be handled through first-message protocol auth.
- Frontend `request<T>()` currently treats most 401s as session expiry, so step-up-required should use a distinguishable non-401 response/code to avoid login redirects.
- Existing credential-audit metadata sanitizers reject keys containing `token`, `credential`, `config`, `command`, `content`, `payload`, `output`, `stream`, `private`, `password`, and `secret`; use safe keys such as `stage`, `proof`, `ttl_seconds`, `operation`, `required`, and counts only.
