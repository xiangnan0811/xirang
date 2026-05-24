# P4 residual security review

## Goal

Run a focused residual P4 security review after the completed local-only hardening slices, then implement exactly one smallest behavior-compatible hardening slice if code inspection proves a remaining raw secret/evidence surface. The work must preserve existing API, deployment, and UI behavior unless inspection proves a broader change is necessary.

## What I already know

* The completed P4 local-only line covers SSH credential provider seams, executor SSH adoption, restic repository password resolver, AppCredential profile materialization resolver, Integration/notification credential surfaces, task runtime evidence sanitization, node log evidence sanitization, and diagnostic evidence sanitization.
* The original P4 architecture items — external Vault/KMS, SSH CA, session recording, command approval, WebAuthn/passkeys/device trust — are intentionally excluded from this local-only phase.
* The next useful step is not to assume a feature, but to audit remaining candidate surfaces and choose one minimal compatible slice.
* Candidate residual surfaces include rclone executor config/materialization, Docker/runtime output surfaces, file browser preview/content paths, config import/export boundaries, legacy evidence at-rest behavior, and AppCredential rendered hook persistence.

## Assumptions (temporary)

* If no remaining local-only behavior-compatible issue is found, the task should end as a review-only finding with no code hardening PR beyond Trellis/journal work.
* If multiple residual risks are found, choose the one that stores or returns raw sensitive evidence today with the smallest API-compatible fix.
* Existing response shapes should remain stable by masking/redacting fields rather than deleting fields.

## Open Questions

* None for the user; scope choice is delegated to code inspection and the existing P4 constraints.

## Requirements

* Inspect remaining residual P4 surfaces before choosing an implementation slice.
* Preserve local encrypted storage and existing deployment shape.
* Preserve public API field names, UI flows, authorization gates, ownership checks, step-up/grant behavior, and existing operation semantics unless code inspection proves a broader need.
* Do not add external Vault/KMS, SSH CA, session recording, command approval, WebAuthn/passkeys/device trust, provider health/lease/fallback semantics, new deployment dependencies, or provider config UI.
* Do not store, log, audit, return, document, or expose raw private keys, passwords, bearer tokens, step-up proofs, OTP/recovery values, executor config, terminal streams, command output/text, file contents, Docker output, diagnostic output, exported/imported secret material, raw SQL, endpoint/proxy values, hostnames, include-path contents, target-path contents, or host-sensitive strings.
* Select at most one executable hardening slice from the residual review.
* Implement the selected slice: add response-time read-boundary sanitization for legacy task-run detail and task/task-run logs.
* Sanitize `TaskRun.LastError` in task-run list/detail responses without mutating stored rows.
* Sanitize `TaskLog.Message` in both task-level and task-run log responses without mutating stored rows, preserving IDs, order, level, cursor, limit, timestamps, and authorization behavior.
* Sanitize `RestoreDrillEvidence` error fields in task-run detail responses without mutating stored rows, while preserving structured status/timing/ID fields and the current `drill_evidence` response shape.

## Acceptance Criteria

* [ ] Residual review covers rclone, Docker/runtime output, file browser/preview, config import/export, legacy evidence at-rest, and AppCredential rendered hook behavior.
* [ ] Review findings are persisted under this Trellis task's `research/` directory.
* [ ] The selected implementation slice is the smallest local-only behavior-compatible fix with the highest remaining security value.
* [ ] `GET /api/v1/tasks/:id/runs` masks legacy raw `last_error` values in returned task-run rows.
* [ ] `GET /api/v1/task-runs/:id` masks legacy raw `last_error` and drill error fields in the returned task-run detail.
* [ ] `GET /api/v1/tasks/:id/logs` masks legacy raw task-log messages in returned rows while preserving pagination/filter semantics.
* [ ] `GET /api/v1/task-runs/:id/logs` masks legacy raw task-log messages in returned rows while preserving pagination/filter semantics.
* [ ] Tests prove raw legacy task errors/logs/drill errors containing command output, paths, hosts, endpoints, and token-like values are not returned, and stored DB rows remain unchanged.
* [ ] Backend focused tests, full backend tests, backend build, backend lint, and `git diff --check` pass.
* [ ] Frontend verification is not required unless frontend code changes.
* [ ] Trellis task is started, implementation/check context is curated, work is committed, task archived, journal recorded, PR created, CI green, merged, release/Docker automation completed if triggered, and local `main` synced clean.

## Technical Approach

Add handler-level response DTO/sanitization in `backend/internal/api/handlers/task_run_handler.go` and reuse it from `backend/internal/api/handlers/task_handler.go`, backed by a small exported read-boundary helper in `backend/internal/task`. Apply it only to API response copies so legacy database rows are not rewritten and no migrations/backfill are needed. Keep route authorization, query filters, pagination, ordering, and field names unchanged.

## Decision (ADR-lite)

**Context**: The residual review found several possible surfaces. AppCredential rendered hooks are high value but require broader storage/runtime redesign; file-browser process logs and nginx query minimization are narrower process-log hardening; rclone executor config itself is not currently secret-bearing. Task-run detail/log endpoints still return stored legacy evidence directly, so prior write-time sanitizers do not fully protect historical rows.

**Decision**: Implement the task-run/detail/log read-boundary sanitizer first as the single executable slice for this task.

**Consequences**: This preserves schema and operation semantics, avoids DB backfill, and immediately protects user-visible legacy task evidence. AppCredential generated-hook persistence remains a confirmed residual candidate for a later, larger slice.

## Definition of Done

* Review evidence and selected-slice rationale are captured in task research/PRD files only.
* Implementation, if any, is minimal and local-only.
* Tests and verification commands pass with actual output.
* GitHub PR/release workflow completes end-to-end.
* Local main is clean and synchronized after merge/release.

## Out of Scope

* External Vault/KMS/secret broker integration.
* SSH CA or external CA rollout.
* Terminal/session recording or playback.
* Command parsing, approval, allow/deny policies, or inspection UI.
* WebAuthn/passkeys/device trust/configurable enterprise policy UI.
* DB migrations or historical data backfill unless review proves response-time masking is insufficient.
* Frontend redesign or API schema changes.
* User-facing docs for internal-only behavior.

## Technical Notes

* Prior P4 archive references:
  * `.trellis/tasks/archive/2026-05/05-22-p4-credential-broker-foundation/`
  * `.trellis/tasks/archive/2026-05/05-22-p4-next-hardening-slice/`
  * `.trellis/tasks/archive/2026-05/05-23-p4-restic-credential-resolver/`
  * `.trellis/tasks/archive/2026-05/05-23-p4-next-hardening/`
  * `.trellis/tasks/archive/2026-05/05-23-p4-integration-notification-access/`
  * `.trellis/tasks/archive/2026-05/05-23-p4-task-runtime-log-sanitization/`
  * `.trellis/tasks/archive/2026-05/05-24-p4-next-security-hardening/`
  * `.trellis/tasks/archive/2026-05/05-24-p4-restic-repo-password-resolver/`
* Branch: `security/p4-residual-review`.
