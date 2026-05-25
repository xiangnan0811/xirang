# P4 closeout residual security review

## Goal

Close out the current P4 local-only residual security hardening phase by auditing remaining surfaces for high-confidence, behavior-compatible residual leaks, then implementing exactly one final minimal slice if justified.

## Requirements

* Audit remaining P4-relevant residual surfaces across backend, frontend, credential/executor, and deployment-adjacent local evidence paths.
* Select only one implementation slice if it is high-confidence, local-only, behavior-compatible, and smaller than an architecture redesign.
* Preserve API, deployment, schema, and visible UI behavior.
* Implement the selected final slice: clear SSH key batch import parsed private-key state on close/success/unmount and ignore late file-reader completion after close.
* Record non-selected backend/frontend candidates as follow-up observations, not as scope expansion for this task.
* Keep architecture-level items out of this task: external Vault/KMS, SSH CA, session recording, command approval/inspection, WebAuthn/passkeys, device trust, configurable enterprise policy UI.

## Acceptance Criteria

* [ ] Research files identify reviewed surfaces, findings, ranked candidates, and excluded architecture-level items.
* [ ] SSH key batch import does not retain parsed private-key strings in mounted closed dialog state after cancel, success, unmount, or late file-reader completion.
* [ ] Reopening the dialog still starts from the existing clean idle state.
* [ ] Focused frontend tests use `FAKE_..._FOR_TEST_ONLY` key material and prove stale private-key entries cannot be submitted after close/reopen or late file-reader resolution.
* [ ] No API, deployment, schema, route, or product-visible behavior changes are introduced.
* [ ] Trellis finish-work, PR, CI, merge, release/Docker monitoring if triggered, and local `main` sync are completed.

## Definition of Done

* Trellis research, PRD, implement/check context, archive, and journal are complete.
* Frontend validation passes for focused tests and the relevant package check if practical.
* CI passes on the PR.
* The branch is merged and local `main` is synced.

## Technical Approach

Add a small cleanup boundary inside `SSHKeyBatchImportDialog`:

* Extract a state reset helper for `phase`, `entries`, `dragOver`, and `parseError`.
* Route `Dialog` open changes through a local handler that clears parsed entries before closing.
* Clear entries after successful import before closing.
* Track the active `FileReader` / request sequence or equivalent so late `onload` / `onerror` callbacks cannot repopulate entries after close/unmount.
* Keep the existing preview, validation, upload, and import behavior unchanged while the dialog is open.

## Decision (ADR-lite)

**Context**: Closeout research found several residual candidates. Backend candidates such as batch status DTO and alert read-boundary sanitization are valid, but they either alter response breadth for external callers or are legacy/current-write boundary tradeoffs. The frontend SSH key batch import issue stores raw private-key material in mounted closed component state and has a bounded behavior-compatible cleanup shape.

**Decision**: Implement only SSH key batch import private-key state cleanup in this closeout slice.

**Consequences**: This closes the highest-confidence raw-secret frontend residual without changing API/schema/deployment. Backend candidates remain documented follow-up observations for future review rather than expanding this final P4 closeout task.

## Out of Scope

* External Vault/KMS/secret broker integration.
* SSH CA, host trust rollout, certificate lifecycle, or revocation.
* Terminal/session recording, playback, retention, or object storage.
* Command-level approval, command parsing, allow/deny policy, or inspection.
* WebAuthn/passkeys/device trust or configurable step-up/grant policy UI.
* Broad executor command-construction redesign.
* AppCredential rendered hook/policy response redesign.
* Backend batch response DTO, alert read-boundary sanitizer, node probe/test alert sanitization, node migration result message sanitization, or persistent keyword filter policy changes in this PR.

## Research References

* [`research/backend-residual-closeout.md`](research/backend-residual-closeout.md) — backend audit found bounded candidates but no need to expand this slice beyond one final fix.
* [`research/frontend-residual-closeout.md`](research/frontend-residual-closeout.md) — frontend audit selected SSH key batch import parsed private-key state cleanup as highest-confidence compatible slice.
* [`research/credential-executor-closeout.md`](research/credential-executor-closeout.md) — credential/executor/deployment audit confirmed prior hardening and recommended the frontend secret-state cleanup as the final P4 closeout implementation.

## Technical Notes

* Current task: `.trellis/tasks/05-25-p4-closeout-residual-security-review`.
* Branch: `security/p4-closeout-residual-review`.
* Primary rule: no raw private keys, passwords, bearer tokens, step-up proofs, OTP/recovery values, executor config, terminal streams, command output/text, file contents, Docker output, diagnostic output, exported/imported secret material, raw SQL, endpoint/proxy values, hostnames, include-path contents, target-path contents, or host-sensitive strings in responses/logs/audit/docs/UI storage.
