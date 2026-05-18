# Security Baseline Hardening

## Goal

Reduce the blast radius of a compromised Xirang control panel by hardening credential exposure, SSH key usage, high-risk operations, and recovery-drill transfer behavior while preserving the existing operations workflow as much as possible.

## What I already know

* Xirang is a high-privilege server operations platform; a compromised panel can become a lateral-movement point across user VPS assets.
* Users may import root SSH keys or reuse one SSH key across multiple VPS nodes, which increases blast radius.
* Product boundary is layered hardening, not a full bastion/PAM rewrite in this task.
* Current code already encrypts many sensitive fields at rest through GORM hooks and avoids returning Node passwords/private keys through `Node.Sanitized()`.
* Current RBAC has coarse `ssh_keys:read` for admin/operator/viewer, even though SSH keys are high-value global resources.
* Current `Task.AfterFind()` decrypts `ExecutorConfig`; task handlers return task entities directly, so sensitive executor config can leak through API responses.
* Recovery drill currently copies the source node private key into a sandbox node temporary file and uses rsync with `StrictHostKeyChecking=no`.
* Batch commands and terminal access are intentionally powerful lateral-movement surfaces; existing batch command blacklist is only a safety net.

## Assumptions

* MVP should prioritize high-risk leakage and credential-spread fixes over advanced features such as SSH certificates, Vault integration, approvals, or full session recording.
* Existing admin workflows should continue to work by default unless a behavior is clearly unsafe.
* Security defaults can become stricter if there is a low-friction escape hatch or clear migration path.

## Open Questions

* None for MVP scope at this point.

## Requirements

* Redact sensitive task executor configuration from API responses.
* Prevent recovery drill from spreading source node private keys to sandbox nodes, or disable that unsafe path until a safer transfer design is implemented.
* Reuse strict host key policy for recovery drill SSH/rsync paths instead of disabling host key checking.
* Reduce unnecessary SSHKey metadata visibility for non-admin users.
* Align direct command task validation with batch command safety checks where feasible.
* Add advisory-only lightweight risk visibility on the Settings system tab for root SSH users, reused SSH keys, sudo-enabled nodes, and weak security defaults where data is already available; no one-click remediation or cross-page filter links in this MVP.
* Implement high-risk guardrails in this MVP as hard server-side blocks/validation for known unsafe cases; do not add step-up authentication yet.
* Preserve deferred security work as a phased roadmap rather than dropping it from the overall plan.
* Add focused backend tests for high-risk redaction, unsafe-drill prevention, SSHKey visibility, command validation, and security risk summary behavior.

## Acceptance Criteria

* [ ] Task list/detail/batch responses do not expose raw decrypted `executor_config` secrets.
* [ ] Recovery drill no longer writes a source node private key to a sandbox node temporary file, or the unsafe flow is blocked by default with a clear error.
* [ ] Recovery drill transfer paths do not force `StrictHostKeyChecking=no`.
* [ ] Non-admin users cannot enumerate all global SSH keys unrelated to nodes they can access.
* [ ] Direct command task validation is at least as strict as batch command validation for command length and known dangerous patterns.
* [ ] The Settings system tab shows an advisory-only lightweight security risk summary with counts/examples for root SSH users, reused SSH keys, sudo-enabled nodes, and weak defaults without exposing raw secrets.
* [ ] Backend tests cover redaction, unsafe recovery-drill prevention, SSHKey visibility, command validation, and security risk summary behavior.
* [ ] PRD roadmap records deferred step-up auth, SSHKey scope, credential-use audit, approvals, SSH certificates, Vault/KMS, and session recording as future phases.

## Definition of Done (team quality bar)

* Tests added/updated for security-sensitive behavior.
* `cd backend && go test ./... -count=1` passes.
* Frontend checks are run if UI changes are included.
* Security-relevant behavior changes are documented in existing user-facing docs only if needed.
* Rollout/rollback implications are considered for existing installations.

## Decision (ADR-lite)

**Context**: The first security hardening task must reduce the largest blast-radius risks quickly without turning Xirang into a full bastion/PAM product.

**Decision**: Use Option B: implement P0 blockers for secret exposure and unsafe key transfer, plus lightweight risk visibility for dangerous existing configurations on the Settings system tab.

**Consequences**: This gives users immediate protection and awareness while deferring step-up authentication, schema-heavy SSHKey scope, credential-use audit, approval workflows, SSH certificates, Vault/KMS, and session recording to future phases.

## Phased Security Roadmap

### This task: P0 + advisory visibility

* Redact task executor config from task API responses.
* Stop or block recovery drill paths that spread source private keys to sandbox nodes.
* Remove forced host-key-check disabling from drill transfer paths.
* Tighten non-admin SSHKey list/export visibility without introducing the full SSHKey scope model.
* Align direct command task validation with batch command validation for length and known dangerous patterns.
* Add Settings system tab advisory-only risk summary for root SSH users, reused SSH keys, sudo-enabled nodes, and weak defaults.
* Use hard server-side validation/blocking for known unsafe cases; no step-up authentication in this task.

### Next phase: P1 least-privilege and audit

* Add SSHKey scope/purpose/expiry/disabled metadata and enforce it at node/task/drill usage boundaries.
* Add credential-use audit events for SSHKey/node credential usage, key tests, exports, command tasks, batch commands, terminal sessions, and drills.
* Expand risk visibility into a richer security-health model, including last-used, stale keys, broad reuse, and high-risk operation history.
* Add optional step-up authentication for high-risk operations after the core P0 leaks and unsafe paths are closed.

### Later phase: P2 PAM/bastion-grade controls

* Add approval/JIT workflows for high-risk access.
* Add SSH certificate or external CA support for short-lived credentials.
* Add Vault/KMS/secret-manager integration for deployments that do not want Xirang to store long-lived private keys directly.
* Add terminal command/session recording or replay where appropriate.
* Add remediation workflows after the advisory risk model is proven.

## Out of Scope (explicit)

* Full bastion/PAM product rewrite.
* SSH certificate authority implementation.
* Vault/KMS integration.
* Step-up authentication for high-risk operations in this task.
* Multi-admin approval workflow.
* Full terminal session recording/playback.
* Replacing all SSH key storage with dynamic credentials.
* One-click remediation actions or automatic mutation of nodes/SSH keys from the Settings risk summary.
* Cross-page filter/link integration from risk cards to Nodes/SSHKeys pages.

## Technical Approach Options

**Option A: P0 leak and key-spread blockers only**

* Fix raw `executor_config` exposure in task responses.
* Block or redesign recovery drill transfer so source private keys are never written to sandbox filesystems.
* Remove forced `StrictHostKeyChecking=no` from drill transfer paths.
* Pros: fastest risk reduction, minimal product churn.
* Cons: does not give users visibility into root/key-reuse/sudo risk yet.

**Option B: P0 blockers plus lightweight risk visibility (Chosen)**

* Include Option A.
* Add a small backend risk summary or security-health signal for root users, reused SSH keys, sudo-enabled nodes, and broad SSHKey visibility where feasible.
* Tighten non-admin SSHKey read/export behavior without designing the full future SSHKey scope model.
* Pros: addresses urgent leaks and helps users find dangerous configurations; still avoids a full PAM rewrite.
* Cons: touches more API/UI surface if shown in the frontend.

**Option C: Broader P0+P1 slice**

* Include Option B.
* Add SSHKey scope/purpose/expiry and credential-use audit events.
* Pros: stronger blast-radius reduction.
* Cons: larger schema/API/frontend migration surface; more likely to delay the urgent fixes.

## Research References

* [`research/current-code-security-surfaces.md`](research/current-code-security-surfaces.md) — Current backend security surfaces, gaps, and P0 implementation points.
* [`research/pam-bastion-patterns.md`](research/pam-bastion-patterns.md) — Bastion/PAM/zero-trust SSH conventions mapped to Xirang's layered boundary.
* [`research/secrets-management.md`](research/secrets-management.md) — OWASP-aligned secret storage, redaction, rotation, least-privilege, and incident-response patterns.

## Technical Notes

* `backend/internal/model/models.go` — `SSHKey`, `Node.Sanitized()`, `Task.BeforeSave`, `Task.AfterFind`.
* `backend/internal/api/handlers/ssh_key_handler.go` — SSHKey list/detail/create/update/export/test behavior.
* `backend/internal/api/handlers/task_handler.go` — task list/detail directly return task entities after `AfterFind` decryption.
* `backend/internal/api/handlers/batch_handler.go` — batch command creation, ownership checks, and command blacklist.
* `backend/internal/task/drill.go` — recovery drill path writes source key to sandbox `/tmp` and disables host key checking.
* `backend/internal/middleware/rbac.go` — current role-to-permission map grants `ssh_keys:read` broadly.
* `web/src/pages/settings-page.system.tsx` — Settings system tab already loads settings/log-retention data and renders card-style sections; MVP risk summary can fit here without adding a new route.
* `Task.ExecutorConfig` is encrypted at rest but has `json:"executor_config,omitempty"`; task list/get/create/update return task entities directly.
* Direct command tasks lack the command length and dangerous-pattern checks currently applied to batch commands.
* Recovery/drill path validators are inconsistent across task restore, snapshot restore, policy drill config, and drill runtime.
