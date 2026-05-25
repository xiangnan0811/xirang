# P5 settings audit posture

## Goal

Deliver the next P5 architecture-security slice as a behavior-compatible, report-only Settings security-risk summary signal for audit/security-event posture. The slice should help operators see whether security-relevant administrative activity has enough audit coverage and retention visibility, without changing enforcement, deployment requirements, schema, or introducing SIEM/session-recording/approval architecture.

## What I already know

* P5 first slice shipped in v0.43.28 as a report-only Settings security-risk summary item for privileged users without TOTP.
* P5 SSH host-key trust posture shipped in v0.43.29 as a report-only Settings security-risk summary item based on global SSH host-key runtime configuration.
* Existing Settings security-risk summary is the preferred low-risk surface for advisory posture checks.
* Existing security constraints prohibit raw secrets and host-sensitive strings in responses/logs/audit/docs/UI storage.
* This task must preserve existing API/deployment/UI behavior except for adding advisory, sanitized posture information.

## Assumptions

* MVP should reuse `/settings/security-risk-summary` rather than adding a dedicated audit dashboard, mutation endpoint, or SIEM integration.
* MVP should rely on existing audit/security-event state and current configuration only. If no bounded, sanitized audit posture signal exists, the slice should report global coverage/config posture rather than adding schema.
* Raw actor identifiers, IPs, endpoints, resource identifiers, request paths, payloads, command text/output, terminal streams, file paths, or diagnostic output should not be surfaced unless code inspection proves an existing sanitized pattern.

## Research Scope

* Inspect audit log model, middleware, handlers, retention/cleanup, and Settings security-risk summary code.
* Determine whether audit posture can safely use existing aggregate counts or should stay global-config-only.
* Inspect frontend Settings security-risk summary mapping/rendering tests for adding another dedicated advisory item.
* Compare common audit posture patterns and map them to Xirang’s compatibility-first constraints.

## Research References

* [`research/audit-posture.md`](research/audit-posture.md) — Xirang already has hash-chained `audit_logs`; MVP should report aggregate hash-chain integrity posture only.

## Requirements

* Add a dedicated report-only Settings security-risk summary item for audit log integrity posture.
* Base the MVP on existing `audit_logs` aggregate state only: total rows, blank `entry_hash`, non-first blank `prev_hash`, and previous-hash link mismatches.
* Preserve existing behavior: no new enforcement, no new auth checks, no schema migration, no audit retention policy, and no audit read/export behavior changes.
* Reuse sanitized examples and bounded counts consistent with existing Settings security-risk summary cards.
* Avoid returning or logging raw usernames, emails, IPs, user agents, endpoints, paths, request bodies, command text/output, terminal streams, file contents, secret material, raw audit payloads, raw SQL, or host-sensitive strings.
* Keep the current broad `weak_security_defaults` and `recent_credential_operations` items but avoid duplicate audit-integrity examples there if a dedicated posture item covers them.

## Acceptance Criteria

* [x] PRD and research identify whether audit posture should be aggregate-state based, global-config-only, or both: aggregate `audit_logs` hash-chain state only for this MVP.
* [x] Existing audit logging and Settings risk-summary paths are inspected and referenced.
* [x] MVP scope explicitly excludes SIEM, session recording, command approval, tamper-evident logging, external log shipping, WebAuthn/passkeys, and device trust.
* [x] Backend risk summary returns a dedicated audit-log integrity posture item with no raw actor identifiers, IPs, paths, payloads, or host-sensitive strings.
* [x] Frontend mapper/i18n/tests recognize and render the dedicated risk item without remediation links or mutation actions.
* [x] Implementation is covered by backend and frontend tests where relevant.
* [x] `git diff --check`, backend tests/build, and frontend check pass before commit.
* [x] Browser smoke is attempted for any UI change and limitations are documented if auth/backend state blocks it: Vite dev server started; browser-level Settings/System smoke could not be completed in this session because no observable browser automation/authenticated backend was available.
* [x] Trellis check review completes without unresolved findings.
* [ ] Trellis task is archived and journaled before PR.

## Definition of Done

* Tests added/updated where behavior changes.
* Lint/typecheck/build/test commands pass.
* Browser smoke is attempted for UI change.
* PR is merged, CI is green, release flow completes if triggered, Docker publish is verified if release is created, and local `main` is synced.

## Out of Scope

* External SIEM/log shipping integration.
* Tamper-evident logging, append-only storage, WORM storage, or cryptographic log sealing.
* Full recomputation/repair of historical audit entry hashes.
* Audit retention policy or cleanup workflow changes.
* Session recording, command approval, command text/output capture, or terminal stream storage.
* Enforcing additional audit coverage or blocking operations when audit posture is weak.
* WebAuthn/passkeys, trusted-device bypass, enterprise policy enforcement UI.
* New required deployment configuration.

## Technical Approach

Add `audit_log_integrity_posture` to the existing Settings security-risk summary. The backend will scan audit rows ordered by ID and compute generic aggregate findings for missing entry hashes, missing non-first previous hashes, and previous-hash link mismatches. The response will only contain severity/count plus bounded generic examples, never raw audit row details. Frontend changes mirror the existing report-only risk-code mapping/i18n/card rendering pattern.

## Decision (ADR-lite)

**Context**: Audit logs already have hash-chain fields, and Settings risk summary is the established low-risk advisory surface.
**Decision**: Use aggregate existing audit-log integrity state for a report-only posture card.
**Consequences**: Operators get visibility into audit-chain gaps without deployment/schema/API behavior changes; deeper cryptographic recomputation, retention policy, and tamper-evident storage remain future work.

## Technical Notes

* Active branch: `security/p5-settings-audit-posture`.
* Task directory: `.trellis/tasks/05-25-p5-settings-audit-posture`.
* Prior P5 tasks: v0.43.28 privileged users without TOTP, v0.43.29 SSH host-key trust posture.
* Inspected paths: `backend/internal/middleware/audit.go`, `backend/internal/model/models.go`, `backend/internal/api/handlers/audit_handler.go`, `backend/internal/credentialaudit/audit.go`, `backend/internal/api/handlers/credential_audit_handler.go`, `backend/internal/api/handlers/settings_handler.go`, `web/src/lib/api/settings-api.ts`, `web/src/pages/settings-page.system.test.tsx`, `web/src/i18n/locales/{zh,en}.ts`.
