# P5 small-team emergency access posture

## Goal

Continue the adjusted P5 small-team security roadmap with a low-burden administrator recovery posture slice. Add a report-only Settings security-risk summary card that helps personal and small-team operators notice practical lockout risks, without adding break-glass login, bypasses, recovery workflows, enterprise access governance, or deployment requirements.

## What I already know

* Xirang targets personal users and small teams, so this slice must favor compatibility, low operational burden, and immediate self-hosted value.
* Already shipped Settings posture cards cover privileged users without TOTP, SSH host-key trust, audit-log integrity, deployment secrets, and backup/restore recoverability.
* Existing recovery primitives already exist: initial `admin` bootstrap via `ADMIN_INITIAL_PASSWORD`, admin-only user CRUD/password reset, self-service TOTP setup/disable, single-use recovery codes, token-version invalidation, and admin-only Settings security-risk summary.
* Existing code intentionally does not expose TOTP secrets, recovery codes, password hashes, token versions, or raw environment values through user APIs or Settings risk cards.
* `deployment_secret_posture` already reports placeholder initial admin passwords, so this slice should not duplicate raw deployment-secret checks.
* No existing break-glass, emergency admin, admin 2FA bypass, or CLI reset endpoint was found; adding one would increase blast radius and is out of scope for personal/small-team default hardening.

## Requirements

* Add a new Settings security-risk summary item for administrator recovery/emergency access posture.
* Keep the item report-only and read-only: no new route, no mutation, no password reset action, no TOTP bypass, no recovery-code reveal/regeneration, no schema migration, no background worker, and no deployment change.
* Use only existing local user/TOTP/recovery metadata to derive aggregate, generic findings.
* Count only generic posture findings and return bounded generic examples.
* Do not expose usernames, password hashes, TOTP secrets, recovery codes, token versions, login tokens, step-up proofs, environment values, audit metadata, IP addresses, user agents, or raw error strings.
* Preserve existing login, user-management, TOTP setup/disable/login, recovery-code consumption, step-up, and deployment-secret behavior.
* Update frontend type mapping, i18n, and Settings/System tests so the new card renders as advisory text with no remediation links or buttons.
* Preserve all shipped P5 Settings posture cards.

## MVP Scope

The MVP is a single new risk code, `admin_recovery_posture`, in `GET /api/v1/settings/security-risk-summary`.

The card should derive generic findings from local users:

* No admin account exists.
* Only one admin account exists.
* No admin account has TOTP enabled.
* All admin accounts have TOTP enabled but none has stored recovery-code evidence.
* At least one admin has TOTP enabled but lacks stored recovery-code evidence.
* No non-admin operator account exists as a lower-privilege fallback for routine operations.

Recommended severity:

* `critical` when there is no admin account, or when every admin depends on TOTP and no admin has recovery-code evidence.
* `warning` when there is only one admin, no admin has TOTP, at least one TOTP-enabled admin lacks recovery-code evidence, or there is no operator fallback.
* `info` when no findings are detected.

## Acceptance Criteria

* [x] PRD records the small-team target, MVP boundaries, route position, and out-of-scope enterprise/access-governance directions.
* [x] Implement/check context files reference only relevant PRD/research/spec files.
* [x] Trellis task is started before implementation.
* [x] Backend Settings security-risk summary includes `admin_recovery_posture` using existing read-only user/TOTP/recovery metadata.
* [x] Backend item returns generic count/examples only and caps examples with `maxSecurityRiskExamples`.
* [x] Backend tests cover critical/warning/info behavior or equivalent representative posture signals.
* [x] Backend tests prove no usernames, password hashes, TOTP secrets, recovery codes, token versions, login tokens, step-up proofs, raw env values, audit metadata, IPs, user agents, or raw errors leak through the Settings summary.
* [x] Frontend mapper/i18n/tests recognize and render the new risk item without remediation links or mutation actions.
* [x] `git diff --check`, backend tests/build, and frontend check pass before commit.
* [x] Trellis check review completes without unresolved findings.
* [ ] PR, CI, merge, release/Docker monitoring if triggered, and local main sync are completed.

## Definition of Done

* The next P5 small-team slice is implemented, tested, checked, committed, merged, and released end-to-end.
* The feature stays report-only and compatible with existing deployments.
* No enterprise-only direction or high-operation-cost access platform is introduced.
* Existing auth, TOTP, recovery-code, user-management, and deployment-secret behavior remains unchanged.

## Technical Approach

1. Add `admin_recovery_posture` to `SettingsHandler.securityRiskItems()` near the existing privileged auth posture card.
2. Query local `User` rows with minimal selected fields needed for aggregate role/TOTP/recovery evidence.
3. Convert admin/operator/TOTP/recovery-code evidence into generic summary labels, never user-identifying examples.
4. Count each generic finding once per posture category, not once per user, so the card is an availability posture summary rather than a user inventory.
5. Keep frontend Settings rendering unchanged apart from type/i18n/test updates.

## Decision (ADR-lite)

**Context**: Personal and small-team operators can lock themselves out if admin recovery paths are too brittle, but adding break-glass login or bypass mechanisms would create a higher-risk control surface.

**Decision**: Implement only a Settings security-risk summary posture card that reports aggregate administrator recovery risk from existing local account metadata.

**Consequences**: Operators get a high-level reminder to maintain redundant admin access and recovery-code evidence without adding any bypass, reset endpoint, enterprise policy engine, or secret-revealing UI. Detailed account management remains in the existing Users and Account settings surfaces.

## Out of Scope

* Break-glass login, emergency admin creation, TOTP bypass, account unlock, or password reset endpoints.
* Revealing, regenerating, downloading, rotating, or counting exact recovery codes in any user-facing response.
* Changing login, TOTP setup/disable/login, recovery-code consumption, step-up proof, token-version, or admin user-management behavior.
* New schema migrations, background workers, queue jobs, persisted posture tables, CLI reset commands, or deployment changes.
* Enterprise policy UI, exception management, device trust governance, command approval engines, session recording, full Vault/KMS, SSH CA, WebAuthn/passkeys, trusted-device flows, or compliance workflows.
* Returning usernames, password hashes, TOTP secrets, recovery codes, token versions, login tokens, step-up proofs, raw environment values, audit metadata, IP addresses, user agents, or raw error strings.

## Research References

* [`research/emergency-access-posture.md`](research/emergency-access-posture.md) — existing Xirang bootstrap admin, user-management, TOTP recovery, and Settings risk-summary primitives already support a low-burden advisory posture card.

## Technical Notes

* Branch: `security/p5-emergency-access-posture`.
* Task directory: `.trellis/tasks/05-25-p5-small-team-emergency-access-posture`.
* Primary backend extension point: `backend/internal/api/handlers/settings_handler.go`.
* Existing auth/user primitives: `backend/internal/model/models.go`, `backend/internal/auth/service.go`, `backend/internal/auth/totp.go`, `backend/internal/api/handlers/auth_handler.go`, `backend/internal/api/handlers/user_handler.go`.
* Existing frontend risk mapper: `web/src/lib/api/settings-api.ts`.
* Existing Settings card UI/test: `web/src/pages/settings-page.system.tsx`, `web/src/pages/settings-page.system.test.tsx`.
