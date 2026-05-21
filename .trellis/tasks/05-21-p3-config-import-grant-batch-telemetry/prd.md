# P3 Config Import JIT Grant and Batch Trigger Telemetry

## Goal

Extend the P2 row-backed JIT credential access grant model from terminal-only enforcement to the first REST high-risk operation, while closing the P1 review observation for all-blocked batch-trigger attempted-action telemetry. The implementation should reduce secret-bearing config import blast radius without changing existing SSH credential storage, Docker release behavior, or unrelated operations.

## Background

- P1/P1b/P1c/P1d delivered SSH key scope, credential-use audit, audit UI/export, Settings risk summary, and TOTP step-up for selected high-risk operations.
- P2 delivered terminal-first JIT grants enforced before SSH credential resolution/dial.
- `POST /config/import` is admin-only and can ingest SSH private keys, node passwords/private keys, task executor config, settings, and other secret-bearing material, but it is not currently step-up/grant-gated.
- P1 final review observed that all-blocked/no-op `POST /tasks/batch-trigger` executes no tasks and therefore has no bypass, but it also writes no attempted-action audit event.

## Requirements

### Config import JIT grant

- Add or generalize backend grant lifecycle support for `config.import` / `config_import` using the existing row-backed `CredentialAccessGrant` model.
- Authenticated admins can request a short-lived config-import grant with bounded reason text after TOTP step-up.
- `POST /config/import` must require:
  - primary authenticated admin context;
  - TOTP step-up proof;
  - active matching grant for the same user, action, purpose, and unexpired time window.
- Missing/expired/revoked/wrong-user/wrong-action/wrong-purpose grants must deny before import mutation occurs.
- Grant checks are additive and must not replace auth, role/RBAC, step-up, request body limits, or existing validation.
- Grant records, responses, errors, logs, and audits must not include imported payloads, private keys, passwords, tokens, settings secret values, executor config, endpoints, raw SQL, host-sensitive strings, command output, file contents, or terminal streams.

### Frontend config import flow

- Update the config import UI/API flow to obtain reason + step-up + config-import grant before submitting import.
- Reuse existing accessible dialog/form primitives and existing step-up composition where practical.
- Do not persist grant ID, grant status, reason text, imported payload, or grant-required state in `localStorage` or `sessionStorage`.
- Show sanitized denial/expiry messages without treating grant-required as login/session expiry.

### Batch-trigger no-op telemetry

- When `POST /tasks/batch-trigger` filters all requested tasks and executes none, emit a sanitized attempted-action credential audit event.
- Preserve existing no-op execution semantics and response shape unless tests show a small machine-readable status addition is already compatible.
- Audit metadata should use bounded safe fields such as requested count, authorized count, executed count, blocked/no-op status, and stage.
- Do not log or audit raw task names, command text, executor config, paths, node endpoints, or other host-sensitive strings.

## Acceptance Criteria

- [ ] Config-import grant request endpoint is admin-only, step-up-gated, uses bounded reason text, and returns a safe DTO.
- [ ] `POST /config/import` denies without a valid active config-import grant before DB mutation/import execution.
- [ ] Grant matching rejects expired, revoked, wrong-user, wrong-action, wrong-purpose, and reused terminal grants.
- [ ] Config import grant request/use/denial writes safe audit evidence without imported secrets or payload-derived raw values.
- [ ] Frontend config import can request the grant, submit import, handle denial/expiry, and does not store grant material in browser storage.
- [ ] All-blocked/no-op batch trigger writes sanitized attempted-action credential audit evidence while continuing to execute no tasks.
- [ ] Backend tests cover config-import grant lifecycle/enforcement, denial order, audit safety, and batch-trigger no-op telemetry.
- [ ] Frontend tests cover config import grant prompt/retry/storage safety and sanitized errors.
- [ ] Trellis context validation, backend tests, frontend check, and `git diff --check` pass.

## Definition of Done

- Minimal implementation with no unrelated refactor.
- P1/P2 security contracts remain intact.
- No raw secret-bearing samples are added to tests, docs, logs, audit metadata, or UI state.
- Task is archived and session journal recorded before PR creation.
- PR checks pass, PR is merged, and post-merge automation status is checked before claiming completion.

## Technical Approach

- Prefer a small generic helper around the existing credential grant matcher rather than duplicating terminal-only logic.
- Treat config import as a system-scoped operation for the first REST grant slice; do not invent broad resource polymorphism until per-task/per-node operations are added.
- Keep route/handler enforcement close to existing step-up patterns so direct API calls cannot bypass frontend prompts.
- Frontend can duplicate the minimum grant prompt UI needed for config import; extract a shared grant prompt only if that is simpler than duplication and stays low-risk.
- Batch-trigger no-op telemetry should be implemented where the handler determines no eligible tasks remain, before returning the existing no-op response.

## Out of Scope

- SSH CA, Vault/KMS, external secret brokers, terminal recording, command-level approval, WebAuthn/passkeys, device trust, and configurable policy UI.
- Broad grant enforcement for SSH key export, config export, task restore, snapshot restore, or batch command creation in this first P3 task.
- Changing stored SSH credential model or requiring remote host changes.
- Recording imported config payloads or terminal/task output.

## References

- Parent planning task: `.trellis/tasks/05-21-plan-p3-p4-credential-security-hardening/prd.md`
- P2 PRD and roadmap: `.trellis/tasks/archive/2026-05/05-20-security-p2-credential-hardening/prd.md`
- P2 code surface research: `.trellis/tasks/archive/2026-05/05-20-security-p2-credential-hardening/research/code-surfaces.md`
- P1 final review: `.trellis/tasks/archive/2026-05/05-20-p1-overall-security-review-before-p2/final-review-report.md`
