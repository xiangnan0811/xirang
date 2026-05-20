# P2 Credential Hardening

## Goal

Reduce credential blast radius after the accepted P1 baseline by adding backend-enforced, operation-bound JIT credential-use grants. The P2 MVP introduces a durable grant/request model and enforces it on the highest-risk interactive SSH boundary first: opening a web terminal. The design must preserve existing auth, RBAC, ownership, step-up, SSH key scope, and credential-audit protections while avoiding a full PAM/bastion rewrite.

## What I already know

- P1/P1b/P1c/P1d are accepted through `v0.42.0`; the P1 overall review found no blocker before P2.
- P1 delivered SSH key least-privilege metadata/enforcement, credential-use audit, admin audit UI/export, Settings risk signals, and TOTP-backed step-up for selected high-risk operations.
- The archived P1 roadmap names P2 candidates: approval/JIT workflows, SSH certificates/external CA, Vault/KMS integration, and terminal session recording where appropriate.
- P2 research found that JIT credential-use grants are the smallest coherent next slice because they build on existing JWT/RBAC/TOTP/audit/scope controls and do not require remote host changes.
- SSH certificates/external CA, Vault/KMS, and terminal recording remain valuable follow-ups but carry higher compatibility or sensitive-data storage risk.
- `POST /config/import` is admin-only and secret-bearing but not currently step-up-gated; record it as a hardening follow-up unless pulled into this MVP.

## Requirements

- Add a backend `credential access grant` model for short-lived operation-bound authorization.
  - Bind grants to requesting user, role, action, purpose, optional resource IDs, reason, status, expiry, and optional approver.
  - Store only bounded safe metadata and IDs; do not store secrets, tokens, commands, terminal streams, file contents, exported payloads, host-sensitive strings, or raw endpoint values.
  - Support SQLite and PostgreSQL migrations with UTC-safe timestamps.
- Add backend APIs for terminal JIT grant lifecycle.
  - Authenticated admins can request a terminal grant for a specific node with a bounded reason and requested TTL.
  - MVP supports self-hosted/single-admin self-grant after existing TOTP step-up; no deadlock when only one admin exists.
  - Grant responses must use safe DTOs and sanitized errors.
  - Expired, revoked, denied, wrong-user, wrong-resource, or wrong-operation grants must not authorize access.
- Enforce JIT grants server-side on terminal WebSocket open.
  - Preserve current first-message auth, admin check, and step-up proof validation.
  - Check for an active grant for `(user, action=terminal.open, purpose=terminal, node_id)` before loading SSH credentials or dialing SSH.
  - Missing/expired/invalid grant must reject the terminal open with a machine-readable denial that frontend can distinguish from session expiration and step-up-required.
  - Terminal enforcement must cover both managed SSH keys and inline node password/private-key credentials because the gate sits before credential resolution.
- Extend audit evidence safely.
  - Write credential/grant audit evidence for grant request, grant activation/self-approval, grant use, denied/blocked use, expiry/revocation where applicable.
  - Audit metadata must use sanitizer-compatible keys such as `stage`, `operation`, `status`, `ttl_seconds`, `node_id`, `grant_id`, and booleans/counts.
  - Never write raw reason text if it includes sensitive markers; sanitize and bound all user-provided reason text.
- Add frontend support for terminal JIT grants.
  - When terminal open is denied for missing grant, prompt the admin for a bounded reason and TOTP step-up if needed, request/activate a short grant, then retry terminal open.
  - Do not store grant material in long-lived browser storage.
  - Show sanitized messages for denied/expired/revoked grants.
- Keep the P2 MVP focused.
  - Implement reusable grant model/helper patterns that can later extend to SSH key export, sensitive config export, task trigger/restore, snapshot restore, and batch command creation.
  - Do not require all high-risk operations to use grants in this first P2 task unless the terminal-first model is already complete and tests remain manageable.
  - Record explicit follow-ups for broader operation coverage, policy-driven grants, external secret providers, SSH certificates, and terminal recording.

## Acceptance Criteria

- [ ] Credential access grant migrations exist for SQLite and PostgreSQL and pass Trellis/migration safety checks.
- [ ] Grant records and API DTOs contain only bounded safe fields and sanitized reason text.
- [ ] Admin terminal access without an active matching grant is blocked before node credential resolution or SSH dial.
- [ ] Active grants authorize only the same user, operation, purpose, resource, and unexpired time window.
- [ ] Existing primary auth, admin role check, purpose-scoped token rejection, and TOTP step-up semantics remain additive and cannot be bypassed by a grant.
- [ ] Missing/expired/invalid grant responses are machine-readable and do not trigger frontend login expiry handling.
- [ ] Terminal frontend can request/activate a grant with reason + step-up and retry opening the terminal.
- [ ] Credential/grant audit events are written for request/activation/use/block paths without raw private keys, passwords, bearer tokens, step-up proofs, OTP/recovery values, terminal streams, command text/output, file contents, exported payloads, raw SQL, endpoint/proxy values, or host-sensitive strings.
- [ ] Backend tests cover grant creation/activation, TTL expiry, wrong-user/wrong-node/wrong-operation denial, terminal WebSocket enforcement order, audit safety, and RBAC/step-up composition.
- [ ] Frontend tests cover machine-readable grant-required handling, terminal grant prompt/retry, sanitized errors, and no long-lived grant storage.
- [ ] Docs describe the P2 terminal JIT grant model, security expectations, and follow-up boundaries.

## Definition of Done

- Trellis context files validate successfully.
- Backend `go test ./... -count=1` passes.
- Backend lint/security checks pass through local CI parity or CI.
- Frontend `npm run check` passes.
- Migration safety checks pass.
- Docs touched by API/security behavior changes are updated.
- PR is created, CI is green, branch is merged, and release automation status is checked before claiming completion.

## Technical Approach

- Implement a row-backed grant model rather than a signed grant token.
  - Row-backed grants are easy to expire, revoke, query, and audit.
  - They avoid introducing another bearer-like artifact that would need purpose isolation and replay prevention.
- Add a small grant service/helper similar in spirit to step-up helpers.
  - Normalize action/purpose/resource matching.
  - Centralize TTL validation, status checks, and sanitized denial responses.
  - Write safe audit events for grant lifecycle and blocked use.
- Start enforcement at terminal WebSocket open.
  - Current order remains: WebSocket first-message auth → primary token/admin validation → step-up proof → parse node ID → JIT grant check → node load → SSH auth resolution → SSH dial.
  - This prevents credential resolution/use when the grant is missing.
- Frontend flow composes with existing `ensureStepUpProof` rather than replacing it.
  - Terminal open first attempts normal flow.
  - Backend grant-required denial opens a grant reason prompt.
  - The grant request uses existing step-up proof and then retries terminal open.
- Keep Settings/security risk summary read-only in this MVP; grant status/list UI can be minimal and scoped to terminal flow unless code patterns already make a small admin list low-risk.

## Decision (ADR-lite)

**Context**: P1 created enforceable least-privilege metadata, audit visibility, and TOTP step-up. The next risk is that a stolen active admin session with recent step-up can still immediately open interactive SSH or use high-risk credential paths. The broader P2 roadmap includes JIT approval, SSH CA, Vault/KMS, and terminal recording, but several options require remote host changes or create new sensitive stores.

**Decision**: Implement operation-bound JIT credential-use grants as P2 MVP, with terminal open as the first enforced surface. Use a row-backed grant model with short TTL, reason, safe lifecycle audit, and backend enforcement before SSH credential resolution.

**Consequences**: This reduces standing interactive credential access without breaking existing SSH key/password compatibility. It does not remove stored secrets or record terminal streams. Broader high-risk operation coverage, policy-driven grant requirements, SSH certificates, Vault/KMS, and terminal recording remain follow-up tasks.

## Out of Scope

- Built-in SSH certificate authority or requiring nodes to trust a new CA.
- Replacing existing stored SSH keys/passwords or forcing node auth migration.
- Vault/KMS provider matrix, lease renewal engine, host-side Vault OTP helper, or external secret brokerage.
- Terminal transcript recording/playback or storing terminal input/output.
- Command-level approval, command parsing, shell-content inspection, or allow/deny command policy.
- ChatOps/SIEM/ticketing reviewer integrations.
- Automatic remote key rotation or account provisioning.
- Requiring JIT grants for every high-risk operation in the first P2 task.

## Research References

- [`research/code-surfaces.md`](research/code-surfaces.md) — Current P1 baseline, candidate P2 capabilities, recommended terminal-first JIT MVP, likely files, and security constraints.
- [`research/security-product-patterns.md`](research/security-product-patterns.md) — Comparable approval/JIT, SSH certificate, Vault/KMS, and session-recording product patterns and trade-offs.

## Follow-up Roadmap

- P2b: Extend the same grant model to SSH key export, sensitive config export/import, task trigger/restore, snapshot restore, and batch command creation.
- P2c: Add policy-driven grant requirements based on broad-scope keys, root/sudo nodes, production tags, export-with-secrets, or recent risk summary signals.
- P2d: Prototype external secret references for one provider shape while preserving local encrypted storage fallback.
- P3/P4: SSH certificates/external CA after operators can safely opt nodes into CA trust.
- Later: Optional terminal session recording with explicit retention, storage, access control, and sensitive-output warnings.

## Technical Notes

- Current terminal backend choke point: `backend/internal/api/handlers/terminal_handler.go` first-message auth before SSH credential use.
- Existing step-up helper and denial shape: `backend/internal/api/handlers/step_up.go`.
- Existing purpose-aware SSH credential boundary: `backend/internal/sshutil/ssh_auth.go` and `backend/internal/sshutil/scope.go`.
- Existing credential audit sanitizer: `backend/internal/credentialaudit/audit.go`.
- Existing frontend step-up composition: `web/src/context/auth-context-provider.tsx`, `web/src/hooks/use-step-up-action.ts`, `web/src/lib/step-up-storage.ts`, and `web/src/components/web-terminal.tsx`.
- Current P2 hardening observations to preserve: `POST /config/import` is secret-bearing and admin-only but not step-up-gated; batch-trigger all-blocked/no-op path lacks attempted-action telemetry because no task executes.
