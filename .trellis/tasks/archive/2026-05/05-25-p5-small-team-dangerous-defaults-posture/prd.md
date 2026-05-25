# P5 small-team dangerous defaults posture

## Goal

Continue the adjusted P5 small-team security roadmap with a low-burden local-hardening slice. Strengthen the existing report-only Settings security-risk summary card for weak security defaults so personal and small-team operators can notice unsafe control-plane exposure defaults without adding enforcement, remediation actions, enterprise policy, device trust, approval workflows, session recording, Vault/KMS, SSH CA, or deployment requirements.

## What I already know

* Xirang targets personal users and small teams, so this slice must favor compatibility, low operational burden, and immediate self-hosted value.
* Already shipped Settings posture cards cover privileged users without TOTP, SSH host-key trust, audit-log integrity, deployment secrets, backup/restore recoverability, administrator recovery posture, and risk-summary scanability.
* The P5 small-team roadmap ranked dangerous-default cleanup and local hardening hints after deployment secrets, backup/restore posture, and risk-summary usability.
* The backend already exposes a `weak_security_defaults` Settings risk item with report-only examples for `WS_ALLOW_EMPTY_ORIGIN`, login CAPTCHA, and second CAPTCHA posture.
* Deployment secret and SSH host-key posture are already separate cards, so this slice must not duplicate APP_ENV/JWT/DATA_ENCRYPTION_KEY/admin-password or SSH host-key findings.
* Existing config/router state includes low-burden local hardening signals that can be reported generically: missing metrics token for `/metrics`, wildcard/empty CORS posture, long JWT TTL, permissive WebSocket Origin, login CAPTCHA posture, and security response headers.
* Existing Settings risk-summary cards intentionally do not expose raw environment values, endpoints, paths, hostnames, tokens, diagnostic output, or secret material.

## Requirements

* Improve the existing `weak_security_defaults` Settings security-risk summary item rather than adding a duplicate new risk code.
* Keep the item report-only and read-only: no new route, no mutation, no enforcement, no remediation links/buttons, no schema migration, no deployment change, and no background worker.
* Use only existing local configuration/settings/runtime metadata to derive aggregate generic findings.
* Include small-team-relevant hardening signals that are not already covered by dedicated cards, such as:
  * WebSocket empty Origin allowed.
  * Login CAPTCHA disabled.
  * Login second CAPTCHA disabled.
  * `/metrics` token not configured.
  * CORS origin allowlist not explicitly configured or contains wildcard-like broad entries outside production startup validation.
  * JWT session TTL is unusually long for a control-plane UI.
* Count each generic posture finding once and return bounded generic examples via `maxSecurityRiskExamples`.
* Do not expose raw environment values, origin values, tokens, hostnames, paths, URLs, diagnostics, secrets, passwords, JWTs, TOTP/recovery values, SQL, command text/output, executor config, or raw errors.
* Preserve existing login, CAPTCHA, WebSocket, CORS, metrics, Settings, deployment-secret, host-key, backup/restore, auth, and route behavior.
* Preserve all shipped P5 Settings posture cards.
* Update backend tests and frontend mapper/i18n/UI tests only as needed for the refined existing card.

## MVP Scope

The MVP is an enhancement of the existing `weak_security_defaults` item in `GET /api/v1/settings/security-risk-summary`.

The card should derive generic findings from existing local state:

* `WS_ALLOW_EMPTY_ORIGIN=true` or invalid boolean value.
* `login.captcha_enabled=false` or invalid boolean value.
* `login.second_captcha_enabled=false` or invalid boolean value.
* `METRICS_TOKEN` missing, because `/metrics` remains open for compatibility when no token is configured.
* `CORS_ALLOWED_ORIGINS` missing/empty or containing `*` as a broad-origin posture signal.
* `JWT_TTL` parsing failure or a configured duration longer than a small-team control-plane threshold.

Recommended severity:

* `warning` when one or more findings are detected.
* `info` when no findings are detected.

## Acceptance Criteria

* [x] PRD records the small-team target, MVP boundaries, route position, and out-of-scope enterprise/security-platform directions.
* [x] Implement/check context files reference only relevant PRD/spec files.
* [x] Trellis task is started before implementation.
* [x] Backend Settings security-risk summary keeps `weak_security_defaults` and expands it with generic local-hardening findings using existing configuration/settings metadata.
* [x] Backend item returns generic count/examples only and caps examples with `maxSecurityRiskExamples`.
* [x] Backend tests cover warning/info behavior or equivalent representative weak-default signals.
* [x] Backend tests prove no raw environment values, origins, URLs, hostnames, paths, tokens, passwords, TOTP/recovery values, command output, diagnostics, SQL, or raw errors leak through the Settings summary.
* [x] Frontend mapper/i18n/tests continue recognizing and rendering `weak_security_defaults` without remediation links or mutation actions.
* [x] `git diff --check`, backend tests/build, and frontend check pass before commit.
* [x] Trellis check review completes without unresolved findings.
* [ ] Trellis finish-work, PR, CI, merge, release/Docker monitoring if triggered, and local main sync are completed.

## Definition of Done

* The next P5 small-team slice is implemented, tested, checked, committed, merged, and released end-to-end.
* The feature stays report-only and compatible with existing deployments.
* No enterprise-only direction or high-operation-cost access platform is introduced.
* Existing auth, CAPTCHA, WebSocket, CORS, metrics, deployment-secret, host-key, backup/restore, and Settings behavior remains unchanged.

## Technical Approach

1. Extend `SettingsHandler.weakSecurityDefaultsRiskItem()` rather than adding a new response code.
2. Keep existing login CAPTCHA and WebSocket empty-Origin checks, but make helper logic count all findings while still capping returned examples.
3. Add generic local-hardening checks for missing metrics token, broad/missing CORS allowlist declaration, and long/invalid JWT TTL using existing environment/settings state.
4. Keep labels generic, e.g. “metrics endpoint lacks token protection” rather than returning actual token/origin/URL values.
5. Keep frontend Settings rendering unchanged apart from i18n copy/test fixture updates for the refined card.
6. Add backend regression tests for representative risk and healthy postures plus leak-prevention assertions.

## Decision (ADR-lite)

**Context**: Personal and small-team operators benefit from simple local hardening hints, but adding enforcement or enterprise policy would break compatibility and raise operational burden. A `weak_security_defaults` risk code already exists, so a duplicate card would reduce scanability.

**Decision**: Refine the existing `weak_security_defaults` card into a broader local-hardening posture summary using generic, bounded findings from existing runtime/settings metadata.

**Consequences**: Operators get a single advisory card for small-team dangerous defaults without new controls or behavior changes. Dedicated posture cards continue owning deployment secrets, SSH host-key trust, audit integrity, backup/restore, and admin recovery so findings do not duplicate across cards.

## Out of Scope

* Adding enforcement, blocking login, requiring CAPTCHA/TOTP, or changing defaults.
* New remediation buttons, mutation actions, links, routes, workflows, schema migrations, workers, or deployment requirements.
* Enterprise policy UI, exception management, device trust governance, command approval, session recording, full Vault/KMS, SSH CA, WebAuthn/passkeys, or compliance workflows.
* Duplicating deployment-secret posture checks for JWT secret strength, data encryption key strength, APP_ENV, or admin initial password placeholders.
* Duplicating SSH host-key trust posture checks.
* Returning raw environment values, origin allowlists, URLs, endpoints, hostnames, paths, tokens, passwords, secret material, diagnostics, SQL, or raw error strings.

## Technical Notes

* Branch: `security/p5-dangerous-defaults-posture`.
* Task directory: `.trellis/tasks/05-25-p5-small-team-dangerous-defaults-posture`.
* Primary backend extension point: `backend/internal/api/handlers/settings_handler.go`.
* Backend tests: `backend/internal/api/handlers/settings_handler_test.go`.
* Frontend risk mapper: `web/src/lib/api/settings-api.ts` and `web/src/lib/api/settings-api.test.ts`.
* Existing Settings card UI/test: `web/src/pages/settings-page.system.tsx`, `web/src/pages/settings-page.system.test.tsx`.
* Relevant specs: `.trellis/spec/frontend/type-safety.md`, `.trellis/spec/frontend/a11y-guidelines.md`.
