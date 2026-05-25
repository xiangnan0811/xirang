# P5 host-key trust posture

## Goal

Deliver the next P5 architecture-security slice as a behavior-compatible, report-only SSH host-key trust posture signal. The slice should help operators see where SSH host-key verification is weakened or unknown, without changing connection behavior, deployment requirements, node schema, or introducing SSH CA / Vault / session-recording architecture.

## What I already know

* P5 first slice shipped in v0.43.28 as a report-only Settings security-risk summary item for privileged users without TOTP.
* The P5 roadmap ranked sanitized SSH host-key trust posture/inventory as the next likely low-blast-radius follow-up after strong-auth posture.
* Existing Settings security-risk summary is the preferred low-risk surface for advisory posture checks.
* Existing security constraints prohibit raw secrets and host-sensitive strings in responses/logs/audit/docs/UI storage.
* This task must preserve existing API/deployment/UI behavior except for adding advisory, sanitized posture information.

## Assumptions

* MVP should be report-only and reuse `/settings/security-risk-summary` rather than adding new enforcement or a dedicated host-key management workflow.
* MVP should rely on current configuration/state that already exists in the repo. If no persisted per-node host-key inventory exists, the slice should report global weak host-key defaults and avoid adding new schema.
* Raw hostnames, target paths, command output, terminal streams, private keys, passwords, endpoint/proxy values, or raw fingerprints should not be surfaced unless code inspection proves an existing sanitized pattern.

## Research Scope

* Inspect current SSH connection and probe code to determine how strict host-key checking and auto-accept behavior are configured.
* Inspect current node/SSH models and API handlers to determine whether host-key inventory already exists.
* Compare common host-key posture patterns and map them to Xirang’s compatibility-first constraints.

## Research References

* [`research/host-key-trust-posture.md`](research/host-key-trust-posture.md) — Xirang currently has env/file-backed `known_hosts` trust, not DB-backed remote host-key inventory; MVP should stay report-only and global-config based.
* [`research/ui-smoke.md`](research/ui-smoke.md) — Vite app loaded, but direct Settings/System smoke was blocked by missing auth state and unavailable backend API.

## Requirements

* Add a dedicated report-only Settings security-risk summary item for SSH host-key trust posture.
* Base the MVP on current global runtime configuration only: `SSH_STRICT_HOST_KEY_CHECKING`, `SSH_AUTO_ACCEPT_NEW_HOSTS`, and safe `SSH_KNOWN_HOSTS_PATH` readability/presence signals if practical.
* Preserve existing SSH connection behavior: no connection blocking, no host-key pinning enforcement, no changed defaults, no migration-required deployment changes.
* Reuse sanitized examples and bounded counts consistent with existing Settings security-risk summary cards.
* Avoid returning or logging raw hostnames, private keys, passwords, command text/output, file contents, endpoint/proxy values, raw known_hosts lines, raw fingerprints, raw known_hosts paths, or host-sensitive strings.
* Keep the current broad `weak_security_defaults` item but avoid duplicate SSH host-key examples there if a dedicated host-key posture item covers them.

## Acceptance Criteria

* [x] PRD and research identify whether host-key posture should be global-config-only or can safely include bounded sanitized node examples: global-config-only for this MVP because no DB-backed host-key inventory exists.
* [x] Existing code paths for SSH host-key checking are inspected and referenced.
* [x] MVP scope explicitly excludes SSH CA, host-key enforcement, session recording, command approval, Vault/KMS, passkeys, and device trust.
* [x] Backend risk summary returns a dedicated SSH host-key posture item with no raw hostnames, raw fingerprints, raw paths, or known_hosts contents.
* [x] Frontend mapper/i18n/tests recognize and render the dedicated risk item without remediation links or mutation actions.
* [x] Implementation is covered by backend and frontend tests where relevant.
* [x] `git diff --check`, backend tests/build, and frontend check pass before commit.
* [x] Trellis check review completes without unresolved findings.
* [ ] Trellis task is archived and journaled before PR.

## Definition of Done

* Tests added/updated where behavior changes.
* Lint/typecheck/build/test commands pass.
* Browser smoke is attempted for any UI change.
* PR is merged, CI is green, release flow completes if triggered, Docker publish is verified if release is created, and local `main` is synced.

## Out of Scope

* External Vault/KMS or secret broker integration.
* SSH CA issuance, rotation, or node-side trust migration.
* Enforcing strict host-key verification or blocking existing SSH flows.
* Storing terminal/session recordings, command text, command output, file contents, diagnostics, or raw fingerprints.
* WebAuthn/passkeys, trusted-device bypass, or enterprise policy enforcement UI.
* New required deployment configuration.

## Technical Notes

* Active branch: `security/p5-host-key-trust-posture`.
* Task directory: `.trellis/tasks/05-25-p5-host-key-trust-posture`.
* Prior P5 task archived at `.trellis/tasks/archive/2026-05/05-25-p5-architecture-security-roadmap`.
* Validation passed: `git diff --check`; `cd backend && TMPDIR=/tmp go test ./internal/api/handlers ./internal/sshutil -count=1`; `cd backend && TMPDIR=/tmp go test ./... -count=1`; `cd backend && TMPDIR=/tmp go build ./cmd/server`; `TMPDIR=/tmp npm --prefix web test -- --run src/lib/api/settings-api.test.ts src/pages/settings-page.system.test.tsx`; `TMPDIR=/tmp npm --prefix web run check`.
* Browser smoke limitation: Vite served the app, but the isolated browser had no auth state and backend API was unavailable, so Settings/System live card rendering was not observed.
