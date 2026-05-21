# Plan P3/P4 Credential Security Hardening

## Goal

确认 P2 后仍未完成的安全加固项，合理拆分 P3/P4 范围，并把下一步可执行的 P3 切片收敛到不会破坏现有自托管体验、但能继续降低凭据集中和横向移动风险的任务。

## Current Completed Baseline

- P1/P1b/P1c/P1d 已完成并通过整体 review：SSH key least-privilege metadata/enforcement、credential-use audit、credential audit UI/export、Settings risk signals、TOTP step-up for selected high-risk operations。
- P2 已完成：terminal-first row-backed JIT credential access grants。Terminal open 的后端顺序为 primary admin auth → step-up proof → node_id parse → active grant check → node load / SSH credential resolution / SSH dial。
- P2 后续发现的 Trellis context path、journal placeholder、Docker rsync CVE release blocker、Docker CVE task context、follow-up journal placeholders 均已修复并合并。

## Unfinished Items

### P3 / near-term control-plane hardening

- Extend JIT grants beyond terminal open to selected REST high-risk operations.
- Harden `POST /config/import`, which is admin-only and secret-bearing but not currently step-up/grant-gated.
- Add sanitized attempted-action telemetry for all-blocked/no-op `POST /tasks/batch-trigger`; no task executes today, so this is audit completeness rather than execution bypass.
- Add policy/risk-driven grant requirements after broader operation coverage exists, using existing broad-scope key, root/sudo node, production tag, export-with-secrets, or recent risk signals.
- Consider a small admin grant status/list surface only after multiple operation types use grants; do not build a heavy approval console first.

### P4 / architecture-level hardening

- SSH certificates / external CA with host trust rollout, principals, TTL, revocation, and signing.
- Vault/KMS/external secret broker or provider references, including provider health, leases, fallback, and import/export semantics.
- Optional terminal/session recording with retention, object storage, playback RBAC, and privacy/sensitive-output warnings.
- Command-level approval/inspection, shell command parsing, allow/deny policies.
- WebAuthn/passkeys, device trust, remember-device, and configurable step-up/grant policy UI.

## Recommended P3 Scope

P3 should complete the control-plane model already introduced by P1/P2 before attempting credential-storage architecture changes. The first P3 slice should prove that the P2 grant model can safely protect REST high-risk operations without over-generalizing.

### P3 first executable slice: config import grant + batch-trigger no-op audit

Implement:

- Require step-up plus an active JIT grant before `POST /config/import` accepts secret-bearing import data.
- Add a grant request API for config import using the existing row-backed grant model, bounded reason text, short TTL, and safe audit fields.
- Update frontend config import flow to request reason + step-up + config-import grant before import, without storing grant material in long-lived browser storage.
- Emit sanitized credential audit attempted-action telemetry when `POST /tasks/batch-trigger` filters all requested tasks and executes none.

Why this slice first:

- `config/import` is explicitly known to ingest SSH private keys, node passwords/private keys, task executor config, and settings.
- It is high-impact but simpler than task restore/export flows because it can be treated as a system-scoped operation rather than per-node/per-task resource matching.
- It reuses the P2 grant model and validates a reusable REST grant pattern before broadening to every high-risk operation.
- Batch-trigger no-op telemetry is a small P1 review observation and closes an audit gap without changing execution semantics.

## P3 Follow-up Scope

After the first P3 slice lands and is reviewed:

1. Extend REST grant enforcement to selected existing P1d high-risk operations:
   - SSH key export.
   - Sensitive config export with `include_secrets=true`.
   - Task manual trigger / restore.
   - Snapshot restore.
   - Batch command creation.
2. Introduce policy-driven grant requirements:
   - broad-scope or reused SSH keys;
   - root login or sudo-enabled nodes;
   - production tags;
   - export/import with secrets;
   - recent risk summary signals.
3. Add a minimal admin grant list/status view only if useful for multiple operation types.

## P4 Scope

P4 should be treated as architecture-level credential modernization and evidence storage:

- SSH CA/external CA: opt-in per node/fleet, no forced migration from existing stored credentials.
- Vault/KMS/external secret broker: one provider shape first, local encrypted storage fallback retained, explicit outage/fallback behavior.
- Terminal/session recording: opt-in, retention-bounded, access-controlled, clear warning that recordings may contain secrets/customer data.
- Command-level approval/inspection: only after command/source boundaries and recording/privacy policies are explicit.
- Advanced step-up: WebAuthn/passkeys/device trust/configurable policy after current TOTP/step-up/grant model is stable.

## Out of Scope for P3 First Slice

- Built-in SSH CA, CA key lifecycle, remote `sshd` trust rollout, or replacing stored SSH keys/passwords.
- Vault/KMS provider matrix, lease renewal engine, host-side Vault OTP helper, or external broker dependency.
- Terminal transcript recording/playback or storing terminal input/output.
- Command text parsing, shell-content inspection, command allow/deny policy, or command-level approval.
- Broad grant enforcement across every operation in one PR.
- Multi-admin reviewer routing, ChatOps/SIEM/ticketing integrations, or enterprise workflow builders.

## Acceptance Criteria for This Planning Task

- [ ] P2 completion baseline is documented.
- [ ] Remaining items are grouped into P3 near-term and P4 architecture-level work.
- [ ] First P3 executable slice has clear goal, scope, rationale, acceptance criteria, and out-of-scope boundaries.
- [ ] Trellis `implement.jsonl` and `check.jsonl` include the specs/research needed for the first P3 implementation task.
- [ ] Current planning task can be archived after creating or starting the first P3 executable task.

## Acceptance Criteria for First P3 Implementation Task

- [ ] `POST /config/import` requires primary admin auth, TOTP step-up, and active config-import grant before import mutation occurs.
- [ ] Config-import grant creation uses bounded safe DTOs and sanitized reason text; no imported payloads/secrets are stored in grant or audit metadata.
- [ ] Missing/expired/wrong-user/wrong-action config-import grant denies import with machine-readable sanitized error.
- [ ] Frontend config import flow obtains reason + step-up + grant, retries import, and does not persist grant ID/reason/status in `localStorage` or `sessionStorage`.
- [ ] All-blocked/no-op `POST /tasks/batch-trigger` emits a sanitized attempted-action credential audit event without changing execution behavior.
- [ ] Backend tests cover config-import grant success/denial/expiry/wrong-user/wrong-action, import audit safety, and batch-trigger no-op telemetry.
- [ ] Frontend tests cover config import grant prompt/retry/storage safety and sanitized errors.
- [ ] Backend `go test ./...`, frontend `npm run check`, Trellis context validation, and `git diff --check` pass before PR.

## Technical Approach for First P3 Task

- Reuse row-backed `CredentialAccessGrant` rather than introducing signed grant tokens.
- Generalize P2 helper logic enough to check action/purpose/resource for REST operations, but keep the first REST operation system-scoped (`config.import`) to avoid premature resource abstraction.
- Keep grants additive: they do not replace primary auth, admin/RBAC checks, ownership checks, or step-up.
- Place the config-import grant check before reading/applying secret-bearing import data where feasible, and before any DB mutation.
- Use existing credential audit sanitizer and safe metadata patterns for grant request/use/denial and batch-trigger no-op telemetry.
- Frontend should duplicate the minimum needed grant prompt pattern first; extract shared UI only after a second non-terminal flow confirms the shape.

## Research References

- `.trellis/tasks/archive/2026-05/05-20-security-p2-credential-hardening/prd.md` — P2 completed scope and follow-up roadmap.
- `.trellis/tasks/archive/2026-05/05-20-security-p2-credential-hardening/research/code-surfaces.md` — known code surfaces and open risks, including config import and batch-trigger no-op telemetry.
- `.trellis/tasks/archive/2026-05/05-20-security-p2-credential-hardening/research/security-product-patterns.md` — JIT, SSH CA, Vault/KMS, and session recording trade-offs.
- `.trellis/tasks/archive/2026-05/05-20-p1-overall-security-review-before-p2/final-review-report.md` — P1 acceptance and non-blocking observations.

## Technical Notes

- Current P2 grant implementation lives around `backend/internal/api/handlers/credential_access_grant.go`, terminal enforcement, frontend terminal grant prompt, and typed grant API wrappers.
- Relevant backend specs: `.trellis/spec/backend/quality-guidelines.md`, `.trellis/spec/backend/database-guidelines.md`, `.trellis/spec/backend/error-handling.md`, `.trellis/spec/backend/logging-guidelines.md`.
- Relevant frontend specs: `.trellis/spec/frontend/state-management.md`, `.trellis/spec/frontend/type-safety.md`, `.trellis/spec/frontend/component-guidelines.md`, `.trellis/spec/frontend/a11y-guidelines.md`.
