# P5 small-team backup restore posture

## Goal

Continue the adjusted P5 roadmap with a personal/small-team backup and restore safety posture slice. Add a low-burden, report-only Settings security-risk summary card that surfaces whether existing backup/restore evidence suggests practical recoverability risk, without adding new backup execution, restore execution, governance workflow, enterprise policy, or deployment requirements.

## What I already know

* Xirang targets personal users and small teams, so this slice must favor compatibility, low operational burden, and immediate self-hosted value.
* The previous P5 adjustment preserved shipped report-only posture cards and ranked backup/restore safety posture as the next follow-up after deployment secret posture.
* Existing backup posture primitives already exist: `GET /overview/backup-health`, `GET /overview/backup-confidence`, restore drill evidence, task run evidence, verification/integrity alerts, and generated reports.
* `backup_confidence_handler.go` already models the relevant signals: no task, no successful backup, recent backup failure, incomplete backup, verify failure/warning/missing, RPO unknown/exceeded, missing/failed/non-confident restore drill, and integrity/verification/drill alerts.
* Settings security-risk summary already renders bounded, read-only advisory cards and the latest shipped cards intentionally avoid remediation links or mutation actions.
* Cross-node restore drill transfer is intentionally blocked for credential-safety reasons; this task must not weaken or bypass that safety boundary.

## Requirements

* Add a new Settings security-risk summary item for backup/restore safety posture.
* Keep the item report-only and read-only: no new route, no mutation, no restore/drill trigger, no schema migration, no background worker, and no deployment change.
* Reuse or mirror existing backup confidence semantics rather than inventing enterprise compliance rules.
* Count only aggregate generic posture findings and return only bounded generic examples.
* Do not expose raw node names, policy names, hosts, IPs, paths, task names, task output, executor config, restore paths, snapshot refs, alert messages, error strings, credentials, secrets, or environment values in the new Settings response.
* Keep existing backup confidence/health UI behavior unchanged; Settings only receives a summary-level advisory signal.
* Update frontend type mapping, i18n, and Settings/System tests so the new card renders as advisory text with no remediation links or buttons.
* Preserve shipped P5 cards: privileged users without TOTP, SSH host-key trust posture, audit log integrity posture, and deployment secret posture.

## MVP Scope

The MVP is a single new risk code, `backup_restore_posture`, in `GET /api/v1/settings/security-risk-summary`.

The card should derive generic findings from enabled, non-template backup policies and their existing backup confidence evidence:

* No enabled backup policies exist.
* Enabled policies have no executable tasks.
* Enabled policies lack successful backup evidence.
* Recent backup or latest task run evidence failed or did not complete.
* Backup verification is failing, warning, or missing when enabled.
* RPO evidence is unknown or exceeded.
* Restore drill evidence is missing, failed, or not confidence-eligible.
* Integrity, verification, or restore drill alerts remain unresolved.

Recommended severity:

* `critical` when any critical recoverability finding exists, such as no successful backup evidence, recent failed backup, failed restore drill, RPO exceeded, or integrity/drill alert.
* `warning` when only lower-confidence findings exist, such as missing restore drill, incomplete evidence, verification warning/missing, or no enabled policies.
* `info` when no findings are detected.

## Acceptance Criteria

* [x] PRD records the small-team target, MVP boundaries, route position, and out-of-scope enterprise directions.
* [x] Implement/check context files reference only relevant PRD/research/spec files.
* [x] Trellis task is started before implementation.
* [x] Backend Settings security-risk summary includes `backup_restore_posture` using existing read-only backup/restore evidence semantics.
* [x] Backend item returns generic count/examples only and caps examples with `maxSecurityRiskExamples`.
* [x] Backend tests cover critical/warning/info behavior or equivalent representative posture signals.
* [x] Backend tests prove no policy names, node names, hostnames/IPs, paths, snapshot refs, alert messages, task output, executor config, credentials, or raw errors leak through the Settings summary.
* [x] Frontend mapper/i18n/tests recognize and render the new risk item without remediation links or mutation actions.
* [x] `git diff --check`, backend tests/build, and frontend check pass before commit.
* [x] Trellis check review completes without unresolved findings.
* [ ] PR, CI, merge, release/Docker monitoring if triggered, and local main sync are completed.

## Definition of Done

* The next P5 small-team slice is implemented, tested, checked, committed, merged, and released end-to-end.
* The feature stays report-only and compatible with existing deployments.
* No enterprise-only direction or high-operation-cost platform is introduced.
* Existing backup confidence/health/restore behavior remains unchanged.

## Technical Approach

1. Add `backup_restore_posture` to `SettingsHandler.securityRiskItems()` after deployment secret posture and before weak defaults.
2. Build the item from enabled, non-template `Policy` rows and existing backup confidence contexts where practical.
3. Convert detailed confidence reason codes into a small set of generic summary labels, deduplicated and bounded.
4. Count each generic finding occurrence across policies, but only return generic example labels.
5. Keep frontend Settings rendering unchanged apart from type/i18n/test updates.

## Decision (ADR-lite)

**Context**: Backup recoverability matters directly to personal and small-team operators, but Xirang already has detailed backup confidence panels and restore drill evidence. Adding another workflow would increase blast radius and deployment burden.

**Decision**: Implement only a Settings security-risk summary posture card that aggregates existing backup/restore confidence semantics into generic report-only findings.

**Consequences**: Operators get a high-level security posture signal in Settings without triggering restores, changing backup execution, or adding enterprise compliance flows. Detailed investigation remains in the existing Backups, task-run, alerts, and reports surfaces.

## Out of Scope

* Creating, editing, triggering, approving, or scheduling backups, restores, or restore drills.
* Changing restore drill credential-safety behavior, especially the cross-node transfer block.
* New schema migrations, background workers, queue jobs, or persisted posture tables.
* Enterprise policy UI, exception management, device trust governance, command approval engines, session recording, full Vault/KMS, SSH CA, WebAuthn/passkeys, or trusted-device flows.
* Returning raw policy/node/task names, hosts, IPs, file paths, restore paths, snapshot refs, executor config, command output, alert messages, error strings, credentials, secrets, or environment values.
* Redesigning Backup Confidence, Backup Health, Reports, Alerts, or Restore Drill UI.

## Research References

* [`research/backup-restore-posture.md`](research/backup-restore-posture.md) — existing Xirang backup confidence/health/drill/reporting primitives already cover the low-burden posture signals needed for this slice.

## Technical Notes

* Branch: `security/p5-backup-restore-posture`.
* Task directory: `.trellis/tasks/05-25-p5-small-team-backup-restore-posture`.
* Primary backend extension point: `backend/internal/api/handlers/settings_handler.go`.
* Existing confidence semantics: `backend/internal/api/handlers/backup_confidence_handler.go`.
* Existing frontend risk mapper: `web/src/lib/api/settings-api.ts`.
* Existing Settings card UI/test: `web/src/pages/settings-page.system.tsx`, `web/src/pages/settings-page.system.test.tsx`.
