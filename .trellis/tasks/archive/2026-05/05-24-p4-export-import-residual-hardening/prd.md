# P4 export/import residual security hardening

## Goal

Complete the next P4 residual security hardening slice by auditing export/import, AppCredential rendered hooks, rclone/restic residuals, and adjacent local evidence surfaces, then implementing only the smallest confirmed local-only, behavior-compatible fix. Research selected anomaly snapshot diff parse-failure logging because it currently copies raw combined restic stdout/stderr into a local process log even though the failure is intentionally skipped.

## Requirements

- Replace the anomaly snapshot diff restic snapshots JSON parse-failure log so it never records raw command output, command text, executor config, repository targets, endpoints, hostnames, paths, tokens, passwords, or host-sensitive diagnostic strings.
- Preserve existing anomaly snapshot diff behavior: non-restic skip behavior, empty target error behavior, SSH command execution, parse-failure `nil, nil` return, insufficient-snapshot skip behavior, successful diff parsing, history writes, finding generation, API/UI/deployment/schema behavior, and existing alert semantics.
- Keep the fix local to `backend/internal/anomaly/snapshot_diff.go` and focused tests unless implementation/check findings prove a narrower supporting helper is needed.
- Keep safe operational metadata such as task ID, a fixed stage string, parser error, and output presence/size indicators if useful; do not attempt partial redaction of arbitrary remote output.
- Treat config export/import payload changes, frontend browser-memory refinements, AppCredential rendered hook redesign, rclone config changes, and remote restic password execution changes as out of scope for this slice.
- Do not introduce external Vault/KMS, SSH CA, session recording, command approval, WebAuthn/passkeys, device trust, new product gates, API contract changes, or deployment changes.

## Acceptance Criteria

- [ ] `backend/internal/anomaly/snapshot_diff.go` no longer logs raw `snapOutput` or any equivalent raw snapshots command output on JSON parse failure.
- [ ] The parse-failure log uses only safe structured fields such as `task_id`, a fixed `stage`, `output_present`, and optionally output length/count metadata.
- [ ] The parse-failure branch still logs at debug level and returns `nil, nil`.
- [ ] Focused tests prove fake sensitive output fragments are absent from the structured log event/message and safe fields remain present.
- [ ] Existing snapshot diff parser/helper tests remain intact.
- [ ] Backend tests/build relevant to this backend-only security change pass with actual command output.

## Definition of Done

- Trellis task has research artifacts plus curated implement/check context.
- Implementation is reviewed by `trellis-check` and any findings are fixed.
- Focused backend tests and standard backend validation pass.
- Work is committed on the non-main branch, PR is created, CI is green, PR is merged, release automation completes, Docker publish is verified if triggered, and local `main` is synced.

## Technical Approach

Introduce or use a package-local logging helper for the anomaly snapshots JSON parse-failure branch. The helper should log the parser error and safe scalar metadata without including the raw snapshots output. Prefer a fixed `stage` value such as `snapshots_json_parse` and `output_present` derived from `strings.TrimSpace(output) != ""`. Keep the existing `AnalyzeSnapshotDiff` control flow unchanged.

## Decision (ADR-lite)

**Context**: The P4 residual audit found no minimal behavior-compatible export/import payload hardening and confirmed that AppCredential rendered hooks require a broader product/API/storage design. The smallest confirmed residual is a local anomaly process-log field that records raw restic snapshots output after JSON parse failure.

**Decision**: Limit this task to replacing that raw output log field with safe structured metadata and regression tests.

**Consequences**: Operators lose raw remote snapshots output in this debug event, but the existing behavior already skips this condition and project security rules prohibit raw command/diagnostic evidence in logs. Broader residuals remain explicitly deferred to future slices.

## Out of Scope

- Config export/import payload schema or authorized sensitive export semantics.
- Config import/export grant, step-up, audit, or frontend flow changes.
- AppCredential profile rendering, policy hook storage, or policy hook response redesign.
- rclone executor config or restic remote password command/environment behavior changes.
- Snapshot diff API/UI behavior, history schema, finding schema, or successful restic diff parsing changes.
- External secret managers, SSH CA, session recording, command approval, WebAuthn/passkeys, or device trust.

## Research References

- [`research/export-import-residual.md`](research/export-import-residual.md) — full residual audit; selected anomaly snapshot diff parse-failure process-log hardening as the smallest confirmed behavior-compatible slice.

## Technical Notes

- Primary target: `backend/internal/anomaly/snapshot_diff.go` parse-failure branch after `parseResticSnapshotsJSON(snapOutput)`.
- Test target: `backend/internal/anomaly/snapshot_diff_test.go` with fake `FAKE_..._FOR_TEST_ONLY` fragments.
- Relevant specs: backend logging, error-handling, and quality guidelines.
