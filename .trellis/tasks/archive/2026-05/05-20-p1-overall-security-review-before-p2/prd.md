# P1 Overall Security Review Before P2

## Goal

Perform a phase-level P1 security acceptance review before starting P2, confirming that P1/P1b/P1c/P1d work released through `v0.42.0` is coherent, complete against the approved P1 scope, and free of known blocking issues. The review should produce a clear go/no-go conclusion for P2 without adding new security features unless a blocking defect is found.

## What I already know

- The user wants P1 reviewed as a whole before advancing to P2.
- P1 shipped least-privilege SSH key metadata/enforcement, core credential-use audit events, and risk summary signals.
- P1b extended credential audit coverage to additional SSH/read/export/diagnostic/background surfaces with bounded metadata.
- P1c added admin-only credential audit list/export UI/API with output-time re-sanitization.
- P1d added TOTP-backed step-up authentication for selected high-risk operations and shipped as `v0.42.0`.
- Recent releases in git: `v0.39.0` P1, `v0.40.0` P1b, `v0.41.0` P1c, `v0.42.0` P1d.
- Prior per-task checks passed, but there has not yet been a dedicated cross-task P1 acceptance review.

## Requirements

- Review the P1 scope end-to-end across P1/P1b/P1c/P1d archived Trellis tasks, release commits, backend surfaces, frontend surfaces, tests, docs, and CI/release evidence.
- Build a coverage matrix for the P1 security goals:
  - SSH key least-privilege metadata and enforcement.
  - Compatibility for existing unscoped keys plus visibility as broad-scope risk.
  - Credential-use audit coverage for high-risk user-triggered, background, export, diagnostic, terminal, task, snapshot, and config surfaces.
  - Credential audit list/export UI/API safety and admin-only access.
  - Step-up enforcement for selected high-risk operations.
  - Settings/security risk summary alignment with P1 audit actions.
- Verify cross-layer consistency:
  - Backend route/middleware/handler enforcement matches frontend API/UI flows.
  - RBAC, ownership, and step-up gates compose in the intended order and do not weaken each other.
  - WebSocket flows reject purpose-scoped primary auth and enforce protocol-level proof where required.
  - Direct-download/export flows cannot bypass backend enforcement.
- Verify secret-safety invariants across code, tests, docs, audit metadata, and UI state.
  - No raw private keys, passwords, bearer tokens, step-up proofs, OTP/recovery values, executor config, terminal streams, raw command output, Docker output, diagnostic output, exported secret material, raw SQL, file contents, or host-sensitive strings should be exposed or persisted by P1 features.
  - Credential audit metadata remains bounded and sanitizer-compatible.
- Review test and CI evidence for the P1 surfaces, including targeted tests where available and full backend/frontend checks from PR/release workflows.
- Review documentation freshness for P1 behavior that changed API endpoints, security expectations, or user-visible flows.
- Classify findings into:
  - **Blocker before P2**: must be fixed before starting P2.
  - **Non-blocking follow-up**: should be tracked but does not prevent P2.
  - **Observation**: useful context, no action required.
- If blockers are found, create or update Trellis tasks for fixes and do not mark P1 accepted until they are resolved.
- If no blockers are found, record an explicit P1 accepted/go-for-P2 conclusion.

## Acceptance Criteria

- [ ] A P1 coverage matrix exists and maps every P1/P1b/P1c/P1d requirement to implemented code, tests, docs, or a justified non-blocking follow-up.
- [ ] High-risk operation coverage is checked for backend enforcement, direct API bypass resistance, frontend retry/prompt handling where applicable, and audit evidence.
- [ ] RBAC/ownership/step-up interactions are reviewed for representative admin/operator/viewer and owner/non-owner cases.
- [ ] Credential audit metadata and response/export/UI rendering paths are reviewed for forbidden sensitive fields and unbounded raw evidence.
- [ ] Release and CI evidence for `v0.39.0` through `v0.42.0` is recorded.
- [ ] Any issue found is classified as blocker, non-blocking follow-up, or observation with concrete file/surface references.
- [ ] Final conclusion clearly states whether P1 is accepted and whether P2 may start.

## Definition of Done

- Trellis context files validate successfully.
- Review research/evidence is persisted under this task's `research/` directory.
- A final review report is persisted in the task directory and summarized to the user.
- If code changes are needed for blockers, they are implemented in a separate fix task/commit with tests before P2 proceeds.
- If no blockers are found, this task is archived and included in the normal PR/CI workflow.

## Technical Approach

- Treat this as an audit/review task, not a feature implementation task.
- Use archived Trellis PRDs, git release history, tests, and current code as authoritative inputs.
- Delegate broad evidence gathering to read-only/research agents that persist findings under `research/`.
- Main session synthesizes findings into a coverage matrix and go/no-go conclusion.
- Avoid exposing sensitive examples in the review output; use file paths, route names, action names, and safe identifiers only.

## Decision (ADR-lite)

**Context**: P1 was intentionally delivered in layered slices, so each slice passed its own implementation/check/release workflow. Before P2 adds higher-complexity controls, the project needs a phase-level acceptance gate to catch gaps between slices.

**Decision**: Perform a dedicated P1 overall security review before starting P2. The review gates P2 only on blockers, while non-blocking hardening opportunities become follow-up tasks.

**Consequences**: This creates a defensible P1 acceptance record and reduces the risk of compounding P1 gaps during P2. It may delay P2 if a real blocker is found, which is intentional.

## Out of Scope

- Implementing P2 features.
- Adding new P1 hardening features not already in scope.
- External penetration testing, live production scanning, destructive testing, or DoS testing.
- Rewriting the P1 architecture unless a blocker proves it necessary.
- Publishing raw secret samples or host-sensitive examples in the review report.

## Technical Notes

- P1 task: `.trellis/tasks/archive/2026-05/05-19-security-p1-least-privilege-audit/`.
- P1b task: `.trellis/tasks/archive/2026-05/05-19-security-p1b-credential-audit-extended-surfaces/`.
- P1c task: `.trellis/tasks/archive/2026-05/05-19-security-p1c-credential-audit-ui-export/`.
- P1d task: `.trellis/tasks/archive/2026-05/05-19-security-p1d-step-up-auth-high-risk/`.
- Release commits: `v0.39.0`, `v0.40.0`, `v0.41.0`, `v0.42.0`.
- Review should pay special attention to security boundaries crossing `backend/internal/api/router.go`, handler-level `EnforceStepUp`, WebSocket protocol auth, `credentialaudit` sanitization, Settings risk summary aggregation, frontend API wrappers, and direct download/WebSocket flows.
