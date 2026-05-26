# P5 small-team misoperation guardrails

## Goal

Continue the adjusted P5 small-team security roadmap with a low-burden guardrail slice that reduces accidental damage in the highest-blast-radius UI operations without changing backend authorization, API behavior, audit semantics, deployment requirements, or introducing enterprise approval workflows.

## What I already know

* Xirang targets personal users and small teams, so this slice must favor compatibility, low operational burden, and immediate self-hosted value.
* The P5 Settings posture line has shipped report-only cards for privileged users without TOTP, SSH host-key trust, audit-log integrity, deployment secrets, backup/restore recoverability, administrator recovery posture, risk-summary scanability, and weak dangerous defaults/local-hardening hints.
* The adjusted P5 roadmap kept lightweight misoperation guardrails as the remaining small-team-relevant follow-up after Settings posture work.
* Enterprise policy UI, exception management, device trust governance, approval engines, session recording, full Vault/KMS, SSH CA, WebAuthn/passkeys, and compliance workflows are not default P5 scope.
* Existing high-risk flows already include some authorization controls: batch command execution uses step-up plus credential grant, and restore flow uses reason + step-up + grant.
* Code inspection found the highest-blast-radius frontend-only guardrail target is batch command execution because it can run arbitrary commands across selected nodes.
* Code inspection found SSH key rotation as a second high-impact candidate because replacing a shared key can lock operators out of affected nodes.

## Requirements

* Keep this slice frontend-only unless code inspection proves a backend bug must be fixed.
* Preserve existing backend APIs, auth/RBAC, step-up, credential grant, audit logging, route behavior, and deployment behavior.
* Add a final impact review guardrail before batch command execution requests step-up/grant or creates the command.
* In the batch command guardrail, show bounded context that helps operators understand blast radius: selected node count, a small sample of selected node names, retain/log retention state, and whether the command matches locally detected dangerous patterns.
* Require a lightweight typed acknowledgement for multi-node batch command execution by entering the selected node count before proceeding.
* Detect dangerous command patterns locally using generic labels only; do not log, persist, or send detection metadata separately.
* Add a final acknowledgement guardrail before SSH key rotation can update the key.
* In the SSH rotation guardrail, show affected node count and online/offline split, and require typing the affected node count before rotation.
* Keep all guardrail state local to component state; do not store command text, step-up proofs, private keys, tokens, node hostnames, paths, terminal output, command output, or sensitive strings in browser storage.
* Preserve existing validation for empty commands, command length, missing selected nodes, private-key validation, and existing success/error flows.
* Keep guardrails accessible: labeled inputs, keyboard-operable dialogs/forms, decorative icons hidden from screen readers, and clear validation errors.
* Update focused frontend tests for the new guardrails and existing security invariants.

## MVP Scope

The MVP covers two existing high-impact frontend flows:

1. `BatchCommandDialog`: add a review/acknowledgement step before step-up grant and command creation. Multi-node executions require typing the selected node count. Dangerous command patterns display a warning but remain compatible and do not block authorized use.
2. `SSHKeyRotationWizard`: add final affected-node count acknowledgement before `updateSSHKey` runs.

## Acceptance Criteria

* [x] PRD records the small-team target, MVP boundaries, route position, and out-of-scope enterprise/security-platform directions.
* [x] Implement/check context files reference only relevant PRD/spec files.
* [x] Trellis task is started before implementation.
* [x] Batch command execution requires a final impact review before any step-up grant or command creation call.
* [x] Multi-node batch command execution requires typing the selected node count before proceeding.
* [x] Dangerous-looking batch command text produces a generic local warning without blocking compatibility or storing/sending additional sensitive metadata.
* [x] SSH key rotation requires a final affected-node-count acknowledgement before `updateSSHKey` is called.
* [x] Guardrail UI remains accessible and keyboard-operable.
* [x] Tests prove cancellation or missing/wrong acknowledgement prevents grant/create/update calls.
* [x] Tests preserve the no browser-storage persistence invariant for command text, step-up proof, private keys, and other sensitive values.
* [x] `git diff --check`, focused frontend tests, and frontend check pass before commit.
* [x] Trellis check review completes without unresolved findings.
* [ ] Trellis finish-work, PR, CI, merge, release/Docker monitoring if triggered, and local main sync are completed.

## Definition of Done

* The next P5 small-team slice is implemented, tested, checked, committed, merged, and released end-to-end.
* The feature reduces accidental destructive operations without changing server-side authorization or deployment requirements.
* No enterprise-only direction or high-operation-cost access platform is introduced.
* Existing batch command, SSH key rotation, step-up, credential grant, audit, auth, route, and Settings behavior remains compatible.

## Technical Approach

1. Update `BatchCommandDialog` to separate validation from execution: first validate local inputs, then open an impact review state; only the confirmed review path calls `withStepUp`, `requestBatchCommandCredentialGrant`, and `createBatchCommand`.
2. Keep dangerous command detection local and simple with generic pattern labels such as destructive file removal, disk formatting/writes, shutdown/reboot, and broad container cleanup.
3. Reuse existing dialog/UI primitives and i18n conventions rather than adding a new dependency.
4. Update `SSHKeyRotationWizard` / rotation progress step so final rotation requires an acknowledgement value matching the affected node count.
5. Add focused tests in existing component test files to verify blocking/cancel paths and successful paths.

## Decision (ADR-lite)

**Context**: After the Settings posture cards, the remaining small-team P5 value is preventing obvious operator mistakes in high-impact flows. Batch command and SSH key rotation have high blast radius but can be improved with local UI guardrails without new backend workflows.

**Decision**: Implement lightweight frontend impact-review guardrails for batch command execution and SSH key rotation. Keep the guardrails advisory/acknowledgement-based rather than policy-based or approval-based.

**Consequences**: Operators get an extra chance to catch broad or dangerous actions while existing authorized workflows remain available. This does not create enterprise approval, command inspection, session recording, policy exceptions, or backend enforcement.

## Out of Scope

* Backend command parser, allow/deny rules, policy engine, approval workflow, or executor redesign.
* Session recording, terminal transcript retention, command output inspection, or SIEM/compliance workflows.
* New database schema, new API routes, new credential-grant types, deployment configuration changes, or background workers.
* Blocking dangerous commands server-side or changing who can run batch commands.
* SSH CA, Vault/KMS, device trust, WebAuthn/passkeys, passwordless login, or enterprise step-up policy redesign.
* Returning, logging, storing, or exposing raw private keys, passwords, bearer tokens, step-up proofs, OTP/recovery values, executor config, terminal streams, command output/text beyond the existing command submission field, file contents, Docker output, diagnostics, SQL, endpoint/proxy values, hostnames, include paths, target paths, or host-sensitive strings.

## Technical Notes

* Branch: `security/p5-misoperation-guardrails`.
* Task directory: `.trellis/tasks/05-26-p5-small-team-misoperation-guardrails`.
* Primary frontend files: `web/src/components/batch-command-dialog.tsx`, `web/src/components/batch-command-dialog.test.tsx`, `web/src/components/ssh-key-rotation/ssh-key-rotation-wizard.tsx`, `web/src/components/ssh-key-rotation/rotation-progress.tsx`.
* Relevant specs: `.trellis/spec/frontend/type-safety.md`, `.trellis/spec/frontend/a11y-guidelines.md`.
