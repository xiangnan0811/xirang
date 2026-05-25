# P5 architecture security roadmap

## Goal

Move from completed P4 local-only residual hardening into a P5 architecture-level security roadmap without immediately taking on a risky platform rewrite. This task audits deferred architecture candidates, ranks them by security value and implementation blast radius, then implements one behavior-compatible first slice that prepares future enterprise security policy direction while preserving current API, deployment, and user workflows.

## What I already know

* P4 closeout completed in v0.43.27 with SSH key batch import private-key state cleanup.
* P4 intentionally deferred architecture-level work: external Vault/KMS/secret broker, SSH CA, session recording, command approval/inspection, WebAuthn/passkeys, device trust, and configurable enterprise policy UI.
* Xirang already encrypts sensitive fields via model hooks and uses JWT/RBAC/TOTP, WebSocket terminal/log streams, task executors, credential grants, and audit logging.
* The user approved continuing in the recommended order and does not want repeated scope confirmations when repo/Trellis evidence can decide the next step.
* Existing Settings security-risk summary is admin-only, report-only, and already renders advisory risk cards from backend-provided item codes.
* Existing users already expose role and `totp_enabled` status through sanitized admin user DTOs; TOTP secrets and recovery codes remain hidden/encrypted.

## Requirements

* Audit the deferred P5 architecture candidates against current backend/frontend architecture.
* Rank candidates by security impact, compatibility, operational cost, and implementation blast radius.
* Select exactly one first executable slice that is smaller than a full architecture rollout and can be validated locally.
* Implement only the selected first slice: add a report-only Settings security-risk summary item for privileged users without TOTP enabled.
* Treat `admin` and `operator` users as privileged for this first posture signal.
* Return only count and bounded sanitized examples for the new risk item; do not include passwords, TOTP/recovery values, tokens, raw secrets, command text/output, executor config, endpoints, hostnames, paths, or remote evidence.
* Preserve existing login behavior, TOTP enrollment behavior, RBAC behavior, API routes, deployment requirements, and Settings risk summary rendering pattern.
* Keep non-selected P5 directions documented as roadmap follow-ups, not scope expansion.
* Continue to enforce the project security rule: no raw private keys, passwords, bearer tokens, step-up proofs, OTP/recovery values, executor config, terminal streams, command output/text, file contents, Docker output, diagnostic output, exported/imported secret material, raw SQL, endpoint/proxy values, hostnames, include-path contents, target-path contents, or host-sensitive strings in responses/logs/audit/docs/UI storage.

## Acceptance Criteria

* [x] Research files cover secret broker/Vault/KMS, SSH trust/session-command controls, and strong auth/device trust/policy UI candidates.
* [x] PRD records ranked approaches and the selected first P5 slice.
* [x] Implement/check context jsonl files reference only PRD, research, and relevant specs.
* [x] Backend Settings security-risk summary includes a report-only risk item when one or more admin/operator users have TOTP disabled.
* [x] The new backend item returns a count and bounded non-secret examples only, with no TOTP secrets/recovery values, tokens, passwords, command text/output, executor config, endpoints, hostnames, paths, or remote evidence.
* [x] Frontend settings risk API mapper recognizes the new risk code without changing unknown-code fallback behavior.
* [x] Frontend Settings system tab renders the new advisory card through the existing read-only risk summary UI and does not add remediation/mutation actions.
* [x] Focused backend and frontend tests cover the new risk item and mapper/UI behavior.
* [x] Validation evidence is recorded before PR creation.
* [ ] Trellis finish-work, PR, CI, merge, release/Docker monitoring if triggered, and local main sync are completed.

## Definition of Done

* Trellis PRD, research, implement/check context, archive, and journal are complete.
* Selected slice has tests and relevant package checks passing.
* CI passes on the PR.
* The branch is merged and local `main` is synced.

## Technical Approach

1. Use the existing admin-only `GET /api/v1/settings/security-risk-summary` endpoint and risk item shape.
2. Add a backend advisory item with code `privileged_users_without_totp` that counts `admin`/`operator` users whose `totp_enabled` is false.
3. Keep examples bounded by the existing risk-summary example limit and include only safe account posture metadata already exposed by sanitized user DTOs.
4. Add the new risk item into the existing backend summary aggregation without changing route, auth, response envelope, or deployment behavior.
5. Add the new risk code to the frontend Settings API mapper and i18n labels; let the existing Settings system tab render it automatically.
6. Add focused backend/frontend regression tests and run package checks.

## Ranked P5 candidate families

| Rank | Candidate family | Security impact | Compatibility | Operational cost | Blast radius | Decision |
|---|---|---|---|---|---|---|
| 1 | Report-only strong-auth posture / enterprise policy UI foundation | Medium immediate value; prepares future enforcement | High | Low | Low | **Selected first slice** |
| 2 | Sanitized SSH host-key trust posture/inventory | Medium/High | Medium/High | Low/Medium | Medium | Roadmap follow-up |
| 3 | Secret provider abstraction / Vault/KMS readiness metadata | Medium | High if metadata-only | Low/Medium | Medium | Roadmap follow-up |
| 4 | Optional passkey as step-up provider | High for phishing-resistant high-risk proof | Medium | Medium | Medium/High | Later after policy language/RP config |
| 5 | Full Vault/KMS/secret broker rollout | High | Low as required default | High | High | Later optional infrastructure provider |
| 6 | SSH CA rollout | High | Low without node-side migration | High | High | Later platform architecture |
| 7 | Command approval/inspection engine | High | Medium/Low | Medium/High | High | Later policy/workflow redesign |
| 8 | Terminal/session recording | High for forensics | Low for privacy/storage compatibility | High | High | Later with retention/privacy/storage design |
| 9 | Trusted-device bypass / full passkey login | Mixed; can weaken current posture if bypass-oriented | Low/Medium | Medium/High | High | Not first; avoid bypass semantics |

## Decision (ADR-lite)

**Context**: P5 needs to start architecture-level security work, but full Vault/KMS, SSH CA, session recording, command approval, WebAuthn/passkeys, and device trust all introduce new deployment assumptions, schema/config complexity, sensitive storage surfaces, or workflow changes.

**Decision**: Implement the first P5 slice as a report-only strong-auth posture signal in the existing Settings security-risk summary. The item reports privileged users without TOTP enabled and remains advisory only.

**Consequences**: This creates enterprise policy language and an admin-facing posture surface without changing authentication, enforcement, deployment, or API workflows. It does not deliver phishing-resistant WebAuthn, external secret custody, SSH CA, command approval, session recording, or device trust; those remain roadmap follow-ups after the posture/report-only foundation is validated.

## Research References

* [`research/secret-broker-vault-kms.md`](research/secret-broker-vault-kms.md) — full Vault/KMS/broker rollout is high-value but too infrastructure-dependent for the first slice.
* [`research/ssh-session-command-controls.md`](research/ssh-session-command-controls.md) — SSH CA, session recording, and command approval are high-blast-radius; sanitized posture/inventory belongs later.
* [`research/strong-auth-device-policy.md`](research/strong-auth-device-policy.md) — recommended first slice is report-only strong-auth posture using existing roles and TOTP state.

## Out of Scope

* Full external Vault/KMS/secret broker rollout requiring new production infrastructure.
* Full SSH CA issuance, host trust rollout, certificate lifecycle automation, or revocation system.
* Terminal/session recording playback, retention, object storage, or privacy controls.
* Command parser, command approval engine, command allow/deny policy UI, or executor redesign.
* WebAuthn/passkeys, device trust, trusted-device bypass, passwordless login, or step-up policy redesign as a full product feature in this first slice.
* Login enforcement that requires TOTP for any role.
* New database schema or deployment configuration.
* Breaking API/deployment/schema changes.

## Validation Evidence

* `git diff --check` — passed.
* `cd backend && TMPDIR=/tmp go test ./... -count=1` — passed.
* `cd backend && TMPDIR=/tmp go build ./cmd/server` — passed.
* `TMPDIR=/tmp npm --prefix web run check` — passed: typecheck, lint, 110 test files / 470 tests, production build.
* Browser smoke: `TMPDIR=/tmp npm --prefix web run dev -- --host 127.0.0.1` loaded Settings/System risk-summary area; backend was not running so live API data returned 404, while mapper/UI tests validate rendered data behavior.
* Trellis check agent reviewed scope/security and fixed backend example query to use `COUNT(*)` plus `Limit(maxSecurityRiskExamples)`.

## Technical Notes

* Current task: `.trellis/tasks/05-25-p5-architecture-security-roadmap`.
* Branch: `security/p5-architecture-security-roadmap`.
* Baseline release: `v0.43.27`.
* Relevant specs: backend error handling/logging, frontend type safety and a11y.
