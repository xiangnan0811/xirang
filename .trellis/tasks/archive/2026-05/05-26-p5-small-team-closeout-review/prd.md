# P5 small-team closeout review

## Goal

Review the completed and released P5 small-team security roadmap end-to-end to decide whether it is ready to close, while preserving the adjusted product direction: personal/small-team users, low deployment burden, low blast radius, compatibility-first behavior, report-only/read-only posture cards, and lowest-friction confirmations for destructive operations.

## What I already know

* P5 has been adjusted away from enterprise security-platform work and toward personal/small-team value.
* P5 Settings posture slices shipped through `v0.43.35`: privileged users without TOTP, SSH host-key trust posture, audit-log integrity posture, deployment secret posture, backup/restore posture, admin recovery posture, risk-summary scanability, and weak security defaults/local-hardening.
* P5 misoperation/destructive-operation guardrails shipped in `v0.43.36`: batch command impact review/count acknowledgement/generic dangerous-command labels, and SSH key rotation affected-node count acknowledgement.
* The intended P5 posture style is advisory and bounded: generic count/examples, no raw secret or host-sensitive values, no remediation buttons, no backend policy or enforcement engine.
* The intended guardrail style is local and low-friction: frontend-only confirmation/acknowledgement before existing high-blast-radius actions, without changing backend API/auth/audit semantics.

## Requirements

* Perform a read-only closeout review of P5 implementation and Trellis history before recommending closure.
* Verify P5 still matches the small-team target: low deployment burden, low blast radius, compatibility-first, report-only/read-only posture or minimal-friction confirmation.
* Review backend Settings security-risk summary behavior for sensitive-data boundaries, bounded findings, severity/count semantics, duplicated/noisy signals, and omission of enterprise workflows.
* Review frontend Settings risk rendering for safe copy, accessibility, lack of remediation actions, and no accidental disclosure beyond backend-provided generic examples.
* Review misoperation guardrails for correct placement before high-risk operations, no browser-storage persistence of sensitive values, clear user feedback, and no backend enforcement scope creep.
* Identify coverage gaps that remain relevant to personal/small-team users, but classify them as residual risk or P6/backlog candidates rather than expanding P5 by default.
* Produce a concise pass/fail closeout result with evidence, unresolved findings if any, residual risks, and recommended next step.

## Acceptance Criteria

* [x] P5 shipped scope is enumerated with commits/releases/PRs where derivable from git and Trellis history.
* [x] Settings posture implementation is reviewed for sensitive output, bounded examples, report-only semantics, and target-user fit.
* [x] Misoperation guardrails are reviewed for confirmation timing, compatibility, sensitive-state persistence, and target-user fit.
* [x] Frontend i18n/UI rendering is reviewed for misleading copy, excessive noise, accessibility, and enterprise scope creep.
* [x] Tests/CI/release evidence is checked from local/remote state where available.
* [x] Residual risks are classified as accept, P6/backlog, or blocking P5 closeout.
* [x] If no blocker is found, P5 is explicitly recommended for closure.

## Definition of Done

* The review is documented in this task with enough evidence to support a close/continue decision.
* Any blocker has a concrete file/scope reference and a suggested minimal fix path.
* Any non-blocking issue is framed as residual risk or P6/backlog, not silently expanded into P5.
* No production code is modified unless the review finds a true blocker and the user explicitly asks for implementation.

## Closeout Review Result

**Decision**: Pass — recommend closing P5. No blocking issue was found that should expand the shipped P5 scope or delay closure.

**Reviewed shipped scope**:

* `d593771 fix(security): report privileged users without 2FA` shipped the privileged-users-without-TOTP Settings posture.
* `c6590eb fix(security): report SSH host-key trust posture` shipped SSH host-key trust posture.
* `ac720bc fix(security): report audit log integrity posture` shipped audit-log integrity posture.
* `46bfe0e fix(security): report deployment secret posture (#265)` shipped deployment secret posture.
* `6eb146c fix(security): report backup restore posture (#267)` shipped backup/restore posture.
* `3df4663 fix(security): report admin recovery posture` shipped admin recovery posture.
* `454b2e9 fix(security): improve risk summary scanability` shipped Settings risk-summary scanability.
* `cd71bdc fix(security): report weak local hardening defaults (#273)` shipped weak-default/local-hardening posture.
* `a60d6e4 fix(security): add misoperation guardrails (#275)` shipped batch-command and SSH-key-rotation guardrails.
* First local release tags containing the P5 slices are `v0.43.28` for privileged users without TOTP, `v0.43.29` for SSH host-key posture, `v0.43.30` through `v0.43.35` for the remaining Settings posture/usability slices, and `v0.43.36` for misoperation guardrails; `v0.43.36` points at `2ce006f chore(main): release 0.43.36 (#276)`.

**Settings posture review**:

* Backend route remains admin-only and read-only: `backend/internal/api/router.go:324` registers `GET /settings/security-risk-summary` with `RequireRole("admin")`, and `backend/internal/api/handlers/settings_handler.go:90` only aggregates/returns risk items.
* P5 posture cards are bounded and advisory: `backend/internal/api/handlers/settings_handler.go:49` caps examples at `maxSecurityRiskExamples = 3`; `backend/internal/api/handlers/settings_handler.go:203` aggregates fixed risk items without mutation/remediation payloads.
* Newer high-sensitivity cards use generic labels instead of raw values: deployment secrets at `backend/internal/api/handlers/settings_handler.go:792`, backup/restore posture at `backend/internal/api/handlers/settings_handler.go:841`, and weak defaults at `backend/internal/api/handlers/settings_handler.go:971` report counts and generalized labels.
* Existing tests cover non-disclosure for raw audit/user/recovery/environment/backup evidence and weak-default values in `backend/internal/api/handlers/settings_handler_test.go`.

**Frontend Settings/i18n review**:

* Frontend mapping keeps raw snake_case response handling inside `web/src/lib/api/settings-api.ts:62` and normalizes severity/code safely at `web/src/lib/api/settings-api.ts:85` and `web/src/lib/api/settings-api.ts:92`.
* Settings UI renders read-only cards with count/examples only at `web/src/pages/settings-page.system.tsx:179`; no remediation buttons or links are present in the risk-summary section.
* Severity/count sorting at `web/src/pages/settings-page.system.tsx:27` improves scanability while preserving deterministic order.
* The card section has a labelled region and status text at `web/src/pages/settings-page.system.tsx:179` and `web/src/pages/settings-page.system.tsx:206`; decorative icons are hidden from assistive tech.

**Misoperation guardrail review**:

* Batch command review happens before step-up credential grant and command creation: `web/src/components/batch-command-dialog.tsx:110` opens review after local validation, while `web/src/components/batch-command-dialog.tsx:116` performs the grant/create path only after review confirmation.
* Multi-node batch commands require typed selected-node count: `web/src/components/batch-command-dialog.tsx:80` derives the requirement and `web/src/components/batch-command-dialog.tsx:118` blocks mismatches.
* Dangerous-command handling is generic and local: pattern labels at `web/src/components/batch-command-dialog.tsx:25` map to warning categories, while the review no longer duplicates raw command text outside the existing command input.
* SSH key rotation requires affected-node count acknowledgement before update: `web/src/components/ssh-key-rotation/ssh-key-rotation-wizard.tsx:182` only calls `executeRotation` from step 3 after the typed count matches.
* SSH rotation acknowledgement UI is labelled and associated with hint/error text at `web/src/components/ssh-key-rotation/rotation-progress.tsx:99`.
* Guardrail state is React component state only; no browser-storage persistence of command text, step-up proof, private key, token, or affected-node secret state was found.

**Tests, CI, and release evidence**:

* Local task evidence from the shipped implementation recorded `git diff --check`, backend tests/build, frontend `npm --prefix web run check`, and a Vite dev-server smoke check before PR/release.
* GitHub PR #275 (`fix(security): add misoperation guardrails`) merged to `main` at `a60d6e4` and recent `main` CI completed successfully for that merge.
* GitHub PR #276 (`chore(main): release 0.43.36`) merged to `main` at `2ce006f`; recent `main` CI and Release Please runs completed successfully.
* GitHub release `v0.43.36` is published and targets `2ce006f`.

**Trellis artifact/context review**:

* `implement.jsonl` and `check.jsonl` both point to this PRD plus the relevant backend quality, frontend type-safety, and frontend a11y specs; they do not request production implementation context beyond the review surfaces.
* Impact radius is L1 — Trellis review artifacts only. L2-L5 production code, API behavior, persisted data, deployment/runtime behavior, external services, release artifacts, and user infrastructure are out of scope for this closeout task.

**Residual risks / P6 backlog candidates**:

* P6 candidate: older Settings cards still render sanitized user-controlled display names such as node/key names. This is bounded/admin-only and passed through `util.SanitizeMessage`, but future P6 hardening could convert all examples to fully generic labels for stricter host-sensitive disclosure posture.
* P6 candidate: `recent_credential_operations` can be noisy because warning severity plus high count may sort above other warnings. This is a scanability tuning issue, not a P5 blocker.
* P6 candidate: unknown frontend risk-code fallback maps to `weak_security_defaults`, which is safe for the current closed set but can mislabel a future backend card until the mapper/i18n is updated.
* P6 candidate: batch command acknowledgement mismatch is reported through the dialog-level error banner instead of being associated with the acknowledgement input via `aria-invalid`/`aria-describedby`; the flow blocks correctly, but field-level accessibility can be improved.
* P6 candidate: SSH-key row action can open rotation for an unused key via the preselected-key path, producing a zero-affected-node flow. This is misleading but not a destructive-operation blocker because no node is affected.
* Accepted residual: guardrails are frontend/local by design and can be bypassed by direct API clients; adding backend enforcement, policy engines, approvals, or audit semantic changes is explicitly out of P5 scope.
* Accepted residual: SSH rotation close button remains visible while loading, but close attempts are ignored by `onOpenChange` during loading. This is a UX polish item rather than a security blocker.
* False positive: the batch review Back button is disabled while saving at `web/src/components/batch-command-dialog.tsx:177`; the generic FormDialog cancel control remains a broader dialog behavior, not a P5 guardrail regression.
* Trellis bookkeeping residual: several archived P5 task metadata files lack `commit`/`pr_url` values or have stale unchecked PRD rows, but git/PR/release evidence is sufficient for closeout.

**Blocking findings**: None.

**Recommended next step**: close/archive this P5 closeout review task, then start P6/backlog only if the user wants to address the non-blocking residuals above.

## Technical Approach

1. Inventory P5 commits, archived Trellis tasks, and release markers from git and task history.
2. Inspect the current Settings security-risk summary backend/frontend surfaces and tests.
3. Inspect the current batch command and SSH key rotation guardrails and tests.
4. Cross-check against the P5 constraints: small-team fit, bounded disclosure, no enterprise policy/approval/session-recording scope, and compatibility.
5. Summarize findings and recommended closeout decision.

## Decision (ADR-lite)

**Context**: P5 has already shipped multiple small-team security posture and guardrail slices. Before starting P6 or declaring completion, the project needs a closeout review that verifies the delivered implementation still matches the adjusted roadmap and did not accumulate security or scope regressions.

**Decision**: Treat this task as a read-only closeout review, not a feature slice. Only Trellis review artifacts are expected to change. Code changes are out of scope unless a blocker is found and separately authorized.

**Consequences**: The review can close P5 with explicit residual risks, or identify a small blocking fix. It will not add enterprise policy, approval workflows, or new posture cards by default.

## Out of Scope

* New P5 features, new risk cards, new backend APIs, schema changes, workers, or deployment changes.
* Enterprise policy UI, exception management, device trust governance, approval engines, session recording, full Vault/KMS, SSH CA, WebAuthn/passkeys, compliance workflows, or SIEM integrations.
* Changing existing authorization, audit semantics, credential-grant behavior, executor behavior, or release process.
* Publishing raw private keys, passwords, bearer tokens, step-up proofs, OTP/recovery values, executor config, terminal streams, command output/text, file contents, Docker output, diagnostics, SQL, endpoint/proxy values, hostnames, include paths, target paths, or host-sensitive strings in review output.

## Technical Notes

* Branch: `security/p5-closeout-review`.
* Task directory: `.trellis/tasks/05-26-p5-small-team-closeout-review`.
* Current release containing P5 guardrails: `v0.43.36`.
* Primary review surfaces: `backend/internal/api/handlers/settings_handler.go`, `backend/internal/api/handlers/settings_handler_test.go`, `web/src/lib/api/settings-api.ts`, `web/src/pages/settings-page.system.tsx`, `web/src/components/batch-command-dialog.tsx`, `web/src/components/ssh-key-rotation/rotation-progress.tsx`, and related tests/i18n.
