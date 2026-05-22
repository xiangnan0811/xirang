# P3 comprehensive security review

## Goal

Confirm whether the planned P3 credential/control-plane hardening sequence is complete, then close the highest-confidence review gaps that can be fixed and verified in a small security review slice.

## Current Assessment

The main P3 implementation sequence is complete enough to move from feature delivery to comprehensive review:

- `config.import` is gated by admin auth, step-up, and a system-scoped credential access grant.
- Sensitive `config.export?include_secrets=true` is gated by admin auth, step-up, and a system-scoped credential access grant.
- `snapshot.restore` and `task.restore_trigger` are gated by admin auth, step-up, and task-scoped credential access grants.
- Grant status/list visibility exists as an admin-only read surface.
- Manual trigger, batch trigger, and batch command now support operator-owned resources with row-per-resource grants and all-or-nothing enforcement.
- P4 architecture items remain out of scope for this task.

## Requirements

- Record a P3 completion judgement based on archived P3/P4 planning, current route coverage, current grant semantics, frontend flows, and tests.
- Treat this task as a comprehensive P3 review plus small gap closure, not a new broad P3 feature.
- Add missing full-router registration coverage for all current credential access grant request endpoints.
- Keep grant list/status UI filters aligned with the full current grant action/purpose domain.
- Ensure one-shot frontend grant/action pairs request step-up proof without persisting or reusing cached proof material.
- Confirm the batch-trigger no-eligible-task path remains a bounded no-op/audit path and does not need a grant because no eligible task is executed.
- Classify remaining named candidates:
  - SSH key export is not a required P3 gap for this task unless current code exposes private key export outside sensitive config export.
  - Policy/risk-driven grant requirements and broker/CA/session-recording architecture remain P4 boundary work.
- Preserve all existing security constraints: grant rows are additive authorization records, enforcement is fail-closed, and responses/logs/audit/tests/UI storage must not expose secrets, proof material, commands, output, raw config payloads, raw SQL, endpoints, hostnames, paths, or other host-sensitive strings.

## Acceptance Criteria

- [ ] PRD states the P3 completion decision and remaining P4/out-of-scope classification.
- [ ] Full-router route registration tests cover the complete current credential access grant endpoint set.
- [ ] Grant list/status UI filter options include all current action and purpose values.
- [ ] One-shot config import/export, manual trigger, batch trigger, and batch command flows use non-persistent, non-reused step-up proof behavior.
- [ ] Tests verify the affected frontend grant flows and storage-safety expectations.
- [ ] Backend targeted tests pass.
- [ ] Frontend targeted tests pass.
- [ ] Full backend and frontend verification commands pass before commit.

## Out of Scope

- Adding a new SSH key export grant unless review discovers private-key export not already covered by sensitive config export gating.
- Configurable policy/risk matrix, WebAuthn/device trust, external broker/Vault/KMS/CA integration, terminal/session recording, and command inspection/approval architecture.
- Expanding grant list visibility beyond the current admin-only oversight surface.
- Reworking grant persistence schema or adding new resource columns.

## Technical Approach

- Use the persisted roadmap and code-gap research as the review basis.
- Close deterministic code/test gaps first: route registration assertions and grant-list filters.
- Update frontend step-up wrappers/call sites minimally so one-shot grant/action pairs request one-time proof material.
- Prefer targeted regression tests around changed behavior, then run the full project verification commands.

## Research References

- [`research/p3-roadmap-completion.md`](research/p3-roadmap-completion.md) — concludes the main P3 implementation sequence is complete and the next step should be comprehensive P3 review.
- [`research/p3-code-gap-audit.md`](research/p3-code-gap-audit.md) — identifies route-registration, grant-list filter, and one-shot step-up proof consistency gaps.

## Definition of Done

- Code changes are minimal and limited to review-backed gaps.
- Security-sensitive strings/proofs/secrets are not exposed or persisted by new tests or UI state.
- Backend and frontend checks pass with actual command output.
- Changes are committed on the work branch, Trellis task is archived/journaled, PR is created, CI is green, branch is merged, and the release/Docker publish path is monitored if triggered.
