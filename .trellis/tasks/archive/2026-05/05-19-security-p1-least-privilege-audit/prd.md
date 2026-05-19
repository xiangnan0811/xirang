# Security P1 Least Privilege Audit

## Goal

Reduce credential blast radius beyond the v0.38.2 baseline by adding enforceable SSH key least-privilege metadata and credential-use audit trails, while expanding security-health visibility without introducing a full PAM/bastion workflow in this task.

## What I already know

* v0.38.2 shipped P0 security hardening: task executor config redaction, unsafe drill key propagation blocking, non-admin SSHKey visibility scoping, command validation parity, and Settings advisory risk summary.
* P1 roadmap from the prior task includes SSHKey scope/purpose/expiry/disabled metadata, credential-use audit events, richer security-health signals, and optional step-up auth after core P0 paths are closed.
* Existing RBAC has admin/operator/viewer roles and node ownership for operators.
* Existing audit logs are hash-chained request-level logs for non-read HTTP operations; high-value credential-use events need more domain-specific evidence than generic route/path/status.
* This task should stay layered-hardening, not a full PAM/bastion rewrite.

## Assumptions

* The user expects the approved security roadmap to be completed; budget is sufficient, and the AI should choose the engineering sequence rather than asking for every intermediate scope decision.
* This task will implement Approach B as the P1 core: SSHKey scope plus minimal credential-use audit. Approach C coverage will be split into follow-up tasks after this core lands.
* Step-up authentication remains important but should be treated as a later independent task after least-privilege metadata and audit evidence exist.
* Existing installations need a safe migration path: existing SSH keys should continue to work until admins intentionally tighten scopes.

## Open Questions

* None for this task; proceed with the recommended phased plan autonomously.

## Requirements

* Add SSHKey least-privilege metadata that can express disabled state, expiration, allowed purposes, and optional node/tag scope.
* Enforce SSHKey metadata at use boundaries where the backend opens SSH connections or tests/exports/uses keys.
* Preserve compatibility for existing keys via permissive defaults, while surfacing risky/global keys in the security-health model.
* Add domain-specific credential-use audit events for the P1 core event set: SSH key test/export, node test, terminal open/failure/close, command/batch task trigger, and drill block/phase where practical.
* Expand Settings security risk summary with stale/expired/disabled/global/reused key signals and available high-risk operation history from the new audit events.
* Keep the implementation enforcement- and evidence-focused; do not add approval workflows, SSH certificates, Vault/KMS, step-up auth, or full session recording in this task.
* Record Approach C coverage gaps as follow-up Trellis tasks after this core task is delivered.

## Acceptance Criteria

* [ ] SSHKey records have explicit least-privilege metadata with safe migration defaults.
* [ ] Backend rejects usage of disabled/expired/out-of-scope keys at relevant SSH connection/use boundaries with sanitized errors.
* [ ] Existing unscoped keys continue to work but are visible as broad-scope risks.
* [ ] Credential-use audit records capture who/what used which key or credential for which node/purpose without storing raw secrets.
* [ ] Settings security risk summary reports key least-privilege and credential-use risks without exposing raw credentials or host-sensitive details beyond the chosen contract.
* [ ] Backend and frontend tests cover migrations, enforcement, audit events, and risk summary mapping/UI.
* [ ] Follow-up tasks are created or recorded for Approach C coverage: SFTP/file reads, Docker volumes, config export, probes/background workers, richer audit UI/export, and step-up auth.

## Definition of Done (team quality bar)

* Tests added/updated for security-sensitive behavior.
* `cd backend && go test ./... -count=1` passes.
* `cd web && npm run check` passes if frontend changes are included.
* Migration safety is verified for SQLite and PostgreSQL if schema changes are included.
* Code-spec updates capture any new API/DB/audit contracts.
* End-to-end PR workflow is completed by default after implementation.

## Out of Scope (explicit)

* Step-up authentication unless explicitly pulled into this MVP.
* Approval/JIT workflows.
* SSH certificate authority or external CA support.
* Vault/KMS/secret-manager integration.
* Terminal session recording/playback.
* One-click remediation that mutates SSH keys/nodes from the Settings risk summary.

## Technical Approach Options

**Approach A: SSHKey scope first**

* Add SSHKey disabled/expiry/allowed_purposes/allowed_node_ids/allowed_node_tags fields and enforce them at key-use boundaries.
* Expand Settings risk summary for global/expired/disabled/stale keys.
* Defer credential-use audit table to a separate task.
* Pros: directly reduces blast radius and keeps this task smaller.
* Cons: less forensic visibility until the audit task lands.

**Approach B: Scope plus minimal credential-use audit (Recommended)**

* Include Approach A.
* Add a dedicated credential-use audit event table with a small initial event set: SSH key test/export, node test, terminal open/failure/close, command/batch task trigger, and drill block/phase where practical.
* Keep full audit UI/search/export minimal or backend-only if needed; use Settings risk summary for high-level visibility.
* Pros: balances enforcement with evidence; avoids overloading the existing HTTP `audit_logs` hash-chain model.
* Cons: adds a migration and new writer/test surface.

**Approach C: Full P1 sweep**

* Include Approach B.
* Add broad coverage for SFTP/file reads, Docker volumes, config export, probes, retention/integrity/background flows, and richer audit UI filters/export.
* Pros: strongest coverage.
* Cons: high cross-layer complexity and larger risk of churn; likely too much for one PR.

## Decision (ADR-lite)

**Context**: P1 must keep moving toward the full security roadmap, but the first core slice should reduce blast radius and add forensic evidence without turning into a full PAM/bastion rewrite.

**Decision**: Implement Approach B in this task: SSHKey least-privilege metadata plus a dedicated minimal credential-use audit table. Sequence the broader Approach C surfaces as follow-up tasks after this core lands.

**Consequences**: This adds a migration and new enforcement/audit writer surface now, while keeping step-up auth, broad background-flow auditing, richer audit UI/export, and PAM-grade controls out of this PR so they can be delivered cleanly in later tasks.

## Decision Notes

* Research recommends a dedicated credential-use event table over extending `audit_logs` because existing `AuditLog` is HTTP/WS envelope-shaped and its hash payload does not cover domain metadata.
* Existing keys should migrate with permissive defaults: `disabled=false`, `expires_at=NULL`, empty/null allowed purposes/node IDs/tags meaning all, then the risk summary classifies broad keys as advisory risks.
* Enforcement needs both shared `sshutil` and task executor credential paths because current code has multiple SSH auth resolution paths.
* Purpose scope should use stable enum strings; node scope should prefer exact node IDs plus exact tag matching rather than SQL substring matching.

## Research References

* [`research/ssh-key-scope-patterns.md`](research/ssh-key-scope-patterns.md) — SSH key disabled/expiry/purpose/node/tag scope patterns and enforcement boundaries.
* [`research/credential-use-audit.md`](research/credential-use-audit.md) — Credential-use event field design and audit table tradeoffs.

## Follow-up Roadmap

* P1b: Extend credential-use audit to SFTP/file reads, Docker volumes, config export, node doctor, and migration preflight.
* P1c: Add richer audit listing/export UI and filters for credential-use events.
* P1d: Add step-up authentication for high-risk operations using the credential-use audit model as evidence.
* P2: Add approval/JIT workflows, SSH certificates/external CA, Vault/KMS integration, and terminal session recording where appropriate.

## Technical Notes

* Start from the v0.38.2 security baseline implementation and specs.
* Likely backend areas: `backend/internal/model/models.go`, SSHKey handlers, node/task/drill/terminal SSH connection paths, audit middleware/model, settings risk summary, migrations.
* Likely frontend areas: `web/src/lib/api/settings-api.ts`, Settings system tab, i18n, tests.
* Existing P0 research and code-specs should be reused; current-code research agent failed to persist its file, so implementation planning must inspect current code directly before coding.
