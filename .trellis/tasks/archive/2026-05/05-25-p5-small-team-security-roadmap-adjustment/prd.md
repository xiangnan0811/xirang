# P5 small-team security roadmap adjustment

## Goal

Reframe the remaining P5 security roadmap around Xirang's actual target users: personal operators and small teams. Preserve already shipped report-only posture cards, remove enterprise security strategy as a default direction, and select the next implementation slice in a compatibility-first order that improves practical self-hosted safety without adding heavy infrastructure, governance workflows, or deployment burden.

## What I already know

* The product target is personal users and small teams, not enterprise security organizations.
* Already shipped P5 posture cards must stay: `privileged_users_without_totp` in v0.43.28, `ssh_host_key_trust_posture` in v0.43.29, and `audit_log_integrity_posture` in v0.43.30.
* Existing Settings security-risk summary is admin-only, read-only, report-only, bounded, and already renders advisory cards without remediation actions.
* Current P5 roadmap still lists enterprise-oriented follow-ups: enterprise policy UI, device trust governance, approval engine, session recording platform, full Vault/KMS rollout, and SSH CA.
* Current configuration already validates strong `JWT_SECRET` and `DATA_ENCRYPTION_KEY` outside development, but Settings risk summary does not expose a small-team deployment-secret posture card.
* Security constraints still apply: do not expose raw private keys, passwords, bearer tokens, step-up proofs, OTP/recovery values, executor config, terminal streams, command output/text, file contents, Docker output, diagnostic output, exported/imported secret material, raw SQL, endpoint/proxy values, hostnames, include-path contents, target-path contents, or host-sensitive strings.

## Requirements

* Re-rank P5 around personal/small-team value, compatibility, low deployment burden, and low blast radius.
* Preserve shipped P5 report-only posture cards and avoid deleting or weakening them.
* Downgrade or remove enterprise-only default directions from the active implementation queue.
* Select a first executable slice that can be implemented with existing Settings security-risk summary patterns.
* Keep the first slice report-only: no new enforcement, no schema migration, no route/auth/deployment behavior changes, and no mutation/remediation actions.
* Ensure the first slice returns only generic counts/examples and never returns secret values or host-sensitive strings.
* Curate implementation/check context so the next execution can proceed without expanding into enterprise architecture.

## Re-ranked P5 roadmap

| Rank | Direction | Target-user fit | Blast radius | Decision |
|---|---|---|---|---|
| 1 | Deployment secret posture card for local/small-team installs | High: catches weak/missing deployment secrets and dev-mode leakage risks | Low | **Selected first implementation slice** |
| 2 | Small-team backup/restore safety posture | High: validates practical recoverability and retention confidence | Low/Medium | Roadmap follow-up |
| 3 | Risk-summary usability/readability pass | Medium/High: makes existing security guidance easier to act on manually | Low | Roadmap follow-up |
| 4 | Dangerous-default cleanup and local hardening hints | Medium: improves self-hosted default safety without enterprise policy | Low | Roadmap follow-up |
| 5 | Lightweight misoperation guardrails for destructive operations | Medium: protects small teams from accidental damage | Medium | Later, only if flow-compatible |
| 6 | Secret-provider readiness metadata / Vault-KMS posture | Low/Medium after target adjustment | Medium | Deferred; not default P5 |
| 7 | Enterprise policy UI / device trust governance | Low for target users | High | Removed from default roadmap |
| 8 | Full Vault/KMS, SSH CA, command approval engine, session recording platform | Low for target users despite security value | High | Removed from default roadmap unless explicitly requested later |

## Selected first slice: deployment secret posture

Add a dedicated report-only Settings security-risk summary item for deployment secret posture. The item should detect generic weak/missing runtime-secret posture using only existing environment/config state, for example development mode/default secret usage, invalid/weak `JWT_SECRET`, invalid/weak `DATA_ENCRYPTION_KEY`, and placeholder-like `ADMIN_INITIAL_PASSWORD` if safely inferable without exposing values.

The card should be framed for personal/small-team self-hosting: help operators notice unsafe local deployment defaults before exposing the panel, without introducing Vault/KMS, external secret stores, enterprise policy, or hard enforcement.

## Acceptance Criteria

* [x] PRD records the target-user adjustment and explicitly preserves already shipped P5 posture cards.
* [x] PRD re-ranks the remaining P5 roadmap for personal/small-team users.
* [x] Enterprise policy UI, device trust governance, approval engines, session recording platforms, full Vault/KMS, and SSH CA are removed from the default implementation queue or clearly deferred.
* [x] First implementation slice is selected as a report-only deployment secret posture card.
* [x] Implement/check context files reference only relevant PRD/research/spec/code paths.
* [x] Trellis task is started before implementation.
* [x] Backend Settings security-risk summary includes a dedicated small-team deployment-secret posture item using only generic signals and bounded generic examples.
* [x] Frontend mapper/i18n/tests recognize and render the new risk item without remediation links or mutation actions.
* [x] Focused backend/frontend tests prove no raw secret values, env values, endpoints, paths, hostnames, or sensitive strings are returned.
* [x] `git diff --check`, backend tests/build, and frontend check pass before commit.
* [x] Trellis check review completes without unresolved findings.
* [ ] PR, CI, merge, release/Docker monitoring if triggered, and local main sync are completed.

## Definition of Done

* Roadmap adjustment is committed as part of this task and remains consistent with the personal/small-team product scope.
* The first selected slice is implemented, tested, checked, and shipped end-to-end.
* No enterprise-only direction is introduced as default scope.
* Already shipped P5 behavior remains intact.

## Technical Approach

1. Use the existing `GET /api/v1/settings/security-risk-summary` item model and Settings/System rendering pattern.
2. Add a new backend item code for deployment secret posture, with severity derived from generic weak/missing runtime secret signals.
3. Reuse existing config strength logic where practical rather than duplicating divergent rules.
4. Keep examples generic and bounded, such as “JWT signing secret is weak or missing” rather than returning actual environment values.
5. Add frontend type/i18n/test coverage so the existing read-only risk-summary card renders the item automatically.

## Decision (ADR-lite)

**Context**: The earlier P5 plan leaned toward enterprise security architecture candidates, but the product is intended for personal users and small teams. Those users benefit more from simple, local, understandable safety checks than from heavy governance and infrastructure platforms.

**Decision**: Preserve shipped report-only posture cards, re-rank P5 toward low-burden self-hosted safety, and implement deployment secret posture as the next slice.

**Consequences**: Xirang continues improving security posture without becoming an enterprise security platform. Full Vault/KMS, SSH CA, approval engines, session recording, device trust, and enterprise policy UI remain out of default scope unless explicitly reintroduced later.

## Out of Scope

* Removing or hiding shipped P5 posture cards.
* Enterprise security policy UI, exception management, device trust governance, or organization-wide compliance workflows.
* Full Vault/KMS/secret broker rollout or required external secret infrastructure.
* SSH CA issuance, certificate rotation, revocation, or node-side trust migration.
* Command approval/inspection engine, command parser, or execution workflow redesign.
* Terminal/session recording, playback, storage, retention, or privacy controls.
* WebAuthn/passkeys, trusted-device bypass, passwordless login, or enterprise step-up policy redesign.
* Any API, auth, deployment, schema, or UI behavior break.

## Technical Notes

* Branch: `security/p5-small-team-roadmap-adjustment`.
* Task directory: `.trellis/tasks/05-25-p5-small-team-security-roadmap-adjustment`.
* Relevant previous P5 tasks: `.trellis/tasks/archive/2026-05/05-25-p5-architecture-security-roadmap`, `.trellis/tasks/archive/2026-05/05-25-p5-host-key-trust-posture`, `.trellis/tasks/archive/2026-05/05-25-p5-settings-audit-posture`.
* Inspected paths: `backend/internal/api/handlers/settings_handler.go`, `backend/internal/config/config.go`, `backend/internal/secure/crypto.go`, `backend/internal/api/handlers/settings_handler_test.go`, `web/src/lib/api/settings-api.ts`, `web/src/pages/settings-page.system.tsx`.
