# Research: Remaining P3/P4 hardening roadmap

- **Query**: Research the remaining P3/P4 hardening roadmap candidates for Xirang after completed/released slices: config import JIT grant, config export JIT grant, and snapshot restore task-scoped JIT grant. Use repo/Trellis files only. Include completed items, remaining candidates, risk/blast-radius ranking, and which candidates look executable as one PR.
- **Scope**: internal
- **Date**: 2026-05-22

## Findings

### Files Found

| File Path | Description |
|---|---|
| `.trellis/tasks/archive/2026-05/05-21-plan-p3-p4-credential-security-hardening/prd.md` | Roadmap source that groups unfinished work into P3 near-term control-plane hardening and P4 architecture-level hardening. |
| `.trellis/tasks/archive/2026-05/05-21-p3-config-import-grant-batch-telemetry/prd.md` | Implementation scope for the first P3 slice: `config.import` JIT grant plus all-blocked/no-op batch-trigger telemetry. |
| `.trellis/tasks/archive/2026-05/05-21-p3-control-plane-follow-up-hardening/prd.md` | Implementation scope for the next P3 slice: sensitive `config.export` JIT grant. |
| `.trellis/tasks/archive/2026-05/05-21-p3-control-plane-follow-up-hardening/research/remaining-control-plane-surfaces.md` | Prior candidate comparison across sensitive config export, SSH key export, task trigger/restore, snapshot restore, and batch command creation. |
| `.trellis/tasks/archive/2026-05/05-21-select-next-p3-p4-hardening-slice/prd.md` | Selection document for the snapshot restore task-scoped JIT grant slice. |
| `.trellis/tasks/archive/2026-05/05-20-security-p2-credential-hardening/prd.md` | P2 terminal grant baseline and follow-up list covering SSH key export, config import/export, task trigger/restore, snapshot restore, batch command creation, policy-driven grants, SSH CA, Vault/KMS, and session recording. |
| `.trellis/tasks/archive/2026-05/05-20-security-p2-credential-hardening/research/code-surfaces.md` | Original P2 code-surface research and open risks; records REST high-risk operation list and no-op batch-trigger telemetry gap. |
| `.trellis/spec/backend/quality-guidelines.md` | Backend contracts for SSH key scope, credential-use audit, and row-backed credential access grants. |
| `.trellis/spec/frontend/state-management.md` | Frontend contracts for grant prompt state and browser-storage safety. |
| `.trellis/spec/frontend/a11y-guidelines.md` | Frontend accessibility baseline for any new grant dialog/prompt. |
| `.trellis/workspace/xiangnan-mac/index.md` | Workspace session index showing completed sessions for config import, config export, and snapshot restore JIT grants. |
| `backend/internal/api/router.go` | Current route registration and middleware order for completed grants and remaining candidate operations. |
| `backend/internal/api/handlers/credential_access_grant.go` | Current row-backed grant constants, request handlers, matching helpers, system/task-scoped middleware, audit writers, and reason/TTL sanitizers. |
| `backend/internal/model/models.go` | `CredentialAccessGrant` model with optional `node_id`, `task_id`, and `policy_id`; no `ssh_key_id`, batch ID, snapshot ID, include-path, or multi-node set. |
| `backend/internal/api/handlers/config_handler.go` | Config export/import behavior and sensitive export fields. |
| `backend/internal/api/handlers/snapshot_handler.go` | Snapshot restore behavior and restore audit metadata. |
| `backend/internal/api/handlers/ssh_key_handler.go` | SSH key export behavior; exports public-key/fingerprint-oriented data after scope filtering. |
| `backend/internal/api/handlers/task_handler.go` | Task manual trigger, task restore, and batch-trigger behavior. |
| `backend/internal/api/handlers/batch_handler.go` | Batch command creation behavior, ownership filtering, task creation, and execution trigger. |
| `web/src/types/domain.ts` | Frontend grant action/purpose union now includes terminal, config import, config export, and snapshot restore. |
| `web/src/lib/api/credential-access-grants-api.ts` | Frontend grant API wrapper now includes terminal, config import, config export, and snapshot restore request methods. |
| `backend/README_backend.md` | Backend API docs list current grant endpoints and snapshot restore grant requirement. |
| `docs/admin/security.md` | Admin security docs describe high-risk temporary authorization surfaces now covered. |

### Completed Items

| Completed item | Repo/Trellis evidence | Notes |
|---|---|---|
| P1/P1b/P1c/P1d baseline | Planning PRD states P1/P1b/P1c/P1d delivered SSH key least-privilege metadata/enforcement, credential-use audit, credential audit UI/export, Settings risk signals, and TOTP step-up for selected high-risk operations (`.trellis/tasks/archive/2026-05/05-21-plan-p3-p4-credential-security-hardening/prd.md:7-11`). | Baseline for all later JIT grant work. |
| P2 terminal JIT grant | P2 PRD defines terminal-first row-backed grants and terminal enforcement before node load/SSH credential resolution (`.trellis/tasks/archive/2026-05/05-20-security-p2-credential-hardening/prd.md:18-43`, `:69-85`). Current grant constants include `terminal.open` and purpose `terminal` (`backend/internal/api/handlers/credential_access_grant.go:32-35`). | Completed before P3 roadmap selection. |
| P3 config import JIT grant | First P3 PRD requires `POST /config/import` to need admin auth, TOTP step-up, and active `config.import` / `config_import` grant before import mutation (`.trellis/tasks/archive/2026-05/05-21-p3-config-import-grant-batch-telemetry/prd.md:16-34`). Current router enforces `RequireStepUp` then `RequireConfigImportCredentialGrant` before `configHandler.Import` (`backend/internal/api/router.go:328`). | System-scoped grant; no resource ID. |
| P3 batch-trigger all-blocked/no-op telemetry | Same first P3 PRD requires sanitized attempted-action telemetry when batch trigger filters all requested tasks and executes none (`.trellis/tasks/archive/2026-05/05-21-p3-config-import-grant-batch-telemetry/prd.md:35-40`). Current handler writes a `task.batch_trigger` audit event with `stage=no_op`, requested/eligible/executed/failure/blocked counts, and `no_op=true` when `len(tasksToTrigger)==0` (`backend/internal/api/handlers/task_handler.go:732-746`). | Telemetry gap closed; no grant requirement added to batch trigger. |
| P3 sensitive config export JIT grant | Follow-up PRD selects sensitive config export because `include_secrets=true` can serialize node passwords/private keys, SSH private keys, executor config, and DB settings (`.trellis/tasks/archive/2026-05/05-21-p3-control-plane-follow-up-hardening/prd.md:3-20`). Current router gates only `include_secrets=true` with step-up and config-export grant before `configHandler.Export` (`backend/internal/api/router.go:323-327`). | Safe export remains unchanged by predicate. |
| P3 snapshot restore task-scoped JIT grant | Selection PRD chooses `snapshot.restore` / `snapshot` grant with `task_id=:id` for `POST /tasks/:id/snapshots/:sid/restore` (`.trellis/tasks/archive/2026-05/05-21-select-next-p3-p4-hardening-slice/prd.md:16-35`). Current router adds `RequireSnapshotRestoreCredentialGrant` after step-up and before `snapshotHandler.Restore` (`backend/internal/api/router.go:312-315`). | Task-scoped grant; no snapshot-ID/include-path/target-path binding. |
| Docs/API surface for current grants | Router registers grant endpoints for terminal, config import, config export, and snapshot restore (`backend/internal/api/router.go:263-268`). Backend README lists the same endpoints and notes snapshot restore grant requirement (`backend/README_backend.md:261-264`, `:282-285`). Admin security docs describe terminal, config import, sensitive export, and snapshot restore temporary authorization (`docs/admin/security.md:75-85`). | Current docs align with implemented surfaces. |

Workspace journal also lists the relevant completed sessions: config import grant and telemetry session #56, config export grant session #57, and snapshot restore JIT grant session #58 (`.trellis/workspace/xiangnan-mac/index.md:30-34`).

### Current Grant Model and Constraints

- The grant table is row-backed and operation-bound. It stores requester/approver safe labels, action, purpose, optional `node_id`, optional `task_id`, optional `policy_id`, sanitized reason, status, TTL, and timestamps (`backend/internal/model/models.go:446-471`).
- The current stable grant constants cover `terminal.open`, `config.import`, `config.export`, and `snapshot.restore`; config import/export have dedicated purposes and snapshot restore reuses `sshutil.PurposeSnapshot` (`backend/internal/api/handlers/credential_access_grant.go:32-38`, `:199-238`).
- Matching already supports exact nullable node/task/policy resource scope. If a resource pointer is nil, the matcher requires the DB column to be `NULL`; if present, it requires equality (`backend/internal/api/handlers/credential_access_grant.go:400-406`, `:470-486`).
- Matching currently re-checks the current DB user role and requires both token role and DB role to be `admin` (`backend/internal/api/handlers/credential_access_grant.go:408-421`). This is the main constraint for operator-permitted candidate operations.
- Grant request validation enforces step-up, validates requester role state, sanitizes bounded reason text, and normalizes TTL (`backend/internal/api/handlers/credential_access_grant.go:251-274`, `:498-524`).
- Grant use/block audit metadata is intentionally bounded to fields such as `stage`, `operation`, `status`, `ttl_seconds`, `grant_id`, and optional resource IDs (`backend/internal/api/handlers/credential_access_grant.go:557-625`).
- Backend spec says grants are additive, must fail closed for missing/expired/revoked/denied/wrong-user/wrong-role/wrong-operation/wrong-purpose/wrong-resource cases, and must never store secrets, tokens, step-up proofs, commands, terminal streams, command output, file contents, exported payloads, raw SQL, endpoint/proxy values, or host-sensitive strings (`.trellis/spec/backend/quality-guidelines.md:451-499`).
- Frontend spec says grant rows live on the backend and the frontend must not store grant IDs/material/reason/status in `localStorage` or `sessionStorage`; prompt state should remain component-local (`.trellis/spec/frontend/state-management.md:71-115`).

### Remaining Candidates

#### P3 near-term control-plane candidates

| Candidate | Current surface | Existing controls | Model fit | Notes from repo/Trellis |
|---|---|---|---|---|
| SSH key export JIT grant | `GET /ssh-keys/export` | `ssh_keys:read` RBAC, route-level step-up, SSH key purpose filtering, counts-only audit (`backend/internal/api/router.go:225-230`; `backend/internal/api/handlers/ssh_key_handler.go:705-727`). | Partial. A broad/system-scoped grant fits existing schema; per-key grant does not because `CredentialAccessGrant` has no `SSHKeyID` (`backend/internal/model/models.go:446-471`). | Current export emits public-key/fingerprint-oriented formats and JSON response items, not private keys (`backend/internal/api/handlers/ssh_key_handler.go:729-777`). Prior research ranked direct secret-exfiltration risk lower than sensitive config export (`.trellis/tasks/archive/2026-05/05-21-p3-control-plane-follow-up-hardening/research/remaining-control-plane-surfaces.md:78`). |
| Task restore JIT grant | `POST /tasks/:id/restore` | Admin-only route and route-level step-up (`backend/internal/api/router.go:292`). Handler triggers restore through task runner and audits `task.restore_trigger` with task/run IDs and `custom_target` boolean (`backend/internal/api/handlers/task_handler.go:609-652`). | Strong for task-scoped admin-only grant. Existing `task_id` in grants can represent the resource (`backend/internal/model/models.go:457`). | Similar shape to snapshot restore, but it creates a restore run through task manager rather than inline `ResticExecutor.RestoreFiles`. |
| Task manual trigger JIT grant | `POST /tasks/:id/trigger` | `tasks:trigger` RBAC, ownership check, route-level step-up, task credential audit (`backend/internal/api/router.go:287`; `backend/internal/api/handlers/task_handler.go:451-495`). | Mixed. Existing `task_id` fits a single task, but current grant matching is admin-only while this route supports RBAC/ownership for non-admin operators (`backend/internal/api/handlers/credential_access_grant.go:419-420`). | Prior roadmap grouped manual trigger/restore together as follow-ups, but operator-aware semantics are needed before adding the current admin-only grant model to this route (`.trellis/tasks/archive/2026-05/05-21-plan-p3-p4-credential-security-hardening/prd.md:51-67`). |
| Batch trigger JIT grant | `POST /tasks/batch-trigger` | `tasks:write` RBAC; ownership filtering happens before step-up; step-up only when at least one task remains eligible (`backend/internal/api/router.go:286`; `backend/internal/api/handlers/task_handler.go:681-719`). No-op telemetry is now present (`backend/internal/api/handlers/task_handler.go:732-746`). | Weak for current schema if multi-task grant is desired. Existing grant supports one `task_id`, not a task set. Current grant matching is admin-only while route can involve operators. | Telemetry is completed; grant enforcement remains a broader multi-resource/role design. |
| Batch command creation JIT grant | `POST /batch-commands` | `tasks:write` RBAC, dangerous-command block, node ownership authorization, handler-level step-up before task creation/trigger (`backend/internal/api/router.go:298`; `backend/internal/api/handlers/batch_handler.go:56-115`). | Weak for current schema. The operation is multi-node, and no batch ID exists before creation; current grant supports one `node_id` but not a node set. Admin-only grant matching conflicts with operator-permitted ownership flow. | High execution blast radius because it creates command tasks and triggers them across nodes (`backend/internal/api/handlers/batch_handler.go:117-154`), but a safe grant shape likely needs role/ownership/multi-resource semantics. |
| Policy/risk-driven grant requirements | Roadmap item based on broad-scope/reused SSH keys, root/sudo nodes, production tags, export/import with secrets, or recent risk signals. | Settings risk summary already reports root/sudo, broad/reused/stale keys, recent credential ops, and weak defaults (`.trellis/spec/backend/quality-guidelines.md:293-347`). | Broader policy layer. Existing grants can enforce operations, but policy-driven selection needs stable rules and likely UX/config semantics. | Planning PRD explicitly says to add policy/risk-driven requirements after broader operation coverage exists (`.trellis/tasks/archive/2026-05/05-21-plan-p3-p4-credential-security-hardening/prd.md:17-21`, `:61-67`). |
| Minimal admin grant status/list surface | No current list/status route is present in the grant endpoint cluster; only request endpoints are registered (`backend/internal/api/router.go:263-268`). | Credential audit list/export already exists for admin evidence (`backend/internal/api/router.go:261-264`). | Read-only admin list/status could fit existing grant rows without schema if kept narrow. | Planning PRD says consider a small admin grant status/list surface only after multiple operation types use grants, and not to build a heavy approval console first (`.trellis/tasks/archive/2026-05/05-21-plan-p3-p4-credential-security-hardening/prd.md:20-21`). Multiple operation types now exist. |

#### P4 architecture-level candidates

| Candidate | Current roadmap status | Main repo/Trellis constraints |
|---|---|---|
| SSH certificates / external CA | Listed as P4 architecture-level work in the P3/P4 planning PRD (`.trellis/tasks/archive/2026-05/05-21-plan-p3-p4-credential-security-hardening/prd.md:23-29`, `:69-77`). | Requires CA/key-signing model, principals, TTL/revocation, and remote host trust rollout; P2 research says current SSH auth assumes stored node passwords/private keys or managed SSH keys converted to SSH auth methods (`.trellis/tasks/archive/2026-05/05-20-security-p2-credential-hardening/research/code-surfaces.md:73-88`). |
| Vault/KMS/external secret broker | Listed as P4 architecture-level work (`.trellis/tasks/archive/2026-05/05-21-plan-p3-p4-credential-security-hardening/prd.md:23-29`, `:69-77`). | Sensitive fields are currently encrypted/decrypted through local model hooks and `DATA_ENCRYPTION_KEY`; external providers need provider references, health, leases, fallback, import/export semantics, and outage behavior (`.trellis/tasks/archive/2026-05/05-20-security-p2-credential-hardening/research/code-surfaces.md:90-107`). |
| Optional terminal/session recording | Listed as later/P4 evidence storage (`.trellis/tasks/archive/2026-05/05-21-plan-p3-p4-credential-security-hardening/prd.md:23-29`, `:69-77`). | Current contracts intentionally avoid storing terminal input/output; recording creates a sensitive evidence store requiring retention, object storage, playback RBAC, and privacy warnings (`.trellis/tasks/archive/2026-05/05-20-security-p2-credential-hardening/research/code-surfaces.md:108-121`; `.trellis/spec/backend/quality-guidelines.md:397-405`). |
| Command-level approval/inspection | Listed as P4 (`.trellis/tasks/archive/2026-05/05-21-plan-p3-p4-credential-security-hardening/prd.md:23-29`, `:69-77`). | Requires command/source boundaries, parsing/inspection rules, allow/deny policies, and privacy policy; current audit/grant contracts forbid storing command text/output in grant/audit metadata (`.trellis/spec/backend/quality-guidelines.md:397-401`, `:467-473`). |
| WebAuthn/passkeys/device trust/configurable policy UI | Listed as P4/future advanced step-up (`.trellis/tasks/archive/2026-05/05-21-plan-p3-p4-credential-security-hardening/prd.md:23-29`, `:69-77`). | Current high-risk auth model is TOTP-backed step-up plus row-backed grants. Configurable policy UI would expand policy surface beyond current fixed route/middleware semantics. |

### Risk / Blast-Radius Ranking After Completed Slices

#### Remaining P3 candidate ranking

| Rank | Candidate | Blast-radius rationale | Execution/locality rationale |
|---:|---|---|---|
| 1 | Batch command creation JIT grant | Very high. It can create command tasks across multiple nodes and trigger execution (`backend/internal/api/handlers/batch_handler.go:117-154`). | Not the most local one-PR slice because it is multi-node, operator-permitted, and current grants are admin-only and single-resource. |
| 2 | Task manual trigger / batch trigger JIT grants | High. These can start task execution that later uses stored credentials; batch trigger fans out across tasks (`backend/internal/api/handlers/task_handler.go:472-495`, `:721-773`). | Manual/batch trigger are RBAC/ownership flows, so the current admin-only grant matcher is not directly compatible. Batch trigger also lacks multi-task grant shape. |
| 3 | Task restore JIT grant | High. Restore mutates target data through task runner (`backend/internal/api/handlers/task_handler.go:621-652`). | More executable than manual/batch trigger because the route is admin-only and `task_id` scope already exists (`backend/internal/api/router.go:292`; `backend/internal/model/models.go:457`). |
| 4 | Policy/risk-driven grant requirements | Potentially high because it can focus grants on broad-scope keys, root/sudo nodes, production tags, and recent risk signals. | Roadmap says this follows broader operation coverage; it is a policy/matrix design rather than one operation surface (`.trellis/tasks/archive/2026-05/05-21-plan-p3-p4-credential-security-hardening/prd.md:61-67`). |
| 5 | SSH key export JIT grant | Medium. Current export discloses key inventory/public keys/fingerprints and derives public keys from stored private key material server-side, but does not export private keys (`backend/internal/api/handlers/ssh_key_handler.go:729-777`). | A system-scoped grant is localized; a per-key grant is not supported by current grant resource columns. |
| 6 | Minimal admin grant status/list surface | Low direct blast-radius reduction; improves visibility/evidence rather than gating a new operation. | Likely local if read-only and admin-only, but roadmap says avoid a heavy approval console first (`.trellis/tasks/archive/2026-05/05-21-plan-p3-p4-credential-security-hardening/prd.md:20-21`). |

#### P4 ranking by architectural security impact

| Rank | Candidate | Blast-radius / security impact | Why it is not a near-term one-PR P3 slice |
|---:|---|---|---|
| 1 | Vault/KMS/external secret broker | Highest stored-secret blast-radius reduction if secret material moves out of local encrypted DB paths. | Requires provider abstraction, references/versions, health, leases, fallback, import/export semantics, and outage behavior. |
| 2 | SSH certificates / external CA | High reduction of long-lived SSH private-key blast radius where hosts trust CA-issued short-lived certs. | Requires remote host trust rollout, CA lifecycle, principals, TTL/revocation, and partial-migration behavior. |
| 3 | Command-level approval/inspection | High for command execution risk, especially batch command and manual command task flows. | Requires shell/command policy semantics and must avoid storing command text/output in grants/audit. |
| 4 | WebAuthn/passkeys/device trust/configurable policy UI | High for account/session abuse reduction and stronger step-up. | Requires auth UX and policy configuration model beyond current TOTP + fixed route grants. |
| 5 | Terminal/session recording | Medium-to-high accountability value but mostly detective, not preventative. | Creates sensitive recording store with retention, access control, playback, and privacy requirements. |

### Candidates That Look Executable as One PR

| Candidate | One-PR fit | Suggested one-PR boundary visible from repo/Trellis |
|---|---|---|
| Task restore JIT grant | Strong | Add `task.restore_trigger` / `task_restore` task-scoped grant request and middleware for `POST /tasks/:id/restore`, mirroring snapshot restore task-scoped matching. Keep it admin-only, additive to existing step-up, and enforce before `h.runner.TriggerRestore` (`backend/internal/api/router.go:292`; `backend/internal/api/handlers/task_handler.go:609-652`). |
| SSH key export JIT grant | Medium-strong if system-scoped | Add a system-scoped `ssh_key.export` / `ssh_key_export` grant for the existing export route. Keep current step-up and purpose filtering. Do not attempt per-key grants unless adding a new `SSHKeyID` grant resource is explicitly scoped (`backend/internal/api/router.go:229`; `backend/internal/api/handlers/ssh_key_handler.go:705-777`). |
| Minimal admin grant list/status API/UI | Medium | Add a read-only admin list/status endpoint over `credential_access_grants` plus optional small UI if needed. Keep it list/status only; do not add approval workflow, policy editor, or reviewer routing. Current roadmap says this is only appropriate after multiple operation types use grants, which is now true (`backend/internal/api/router.go:263-268`; `.trellis/tasks/archive/2026-05/05-21-plan-p3-p4-credential-security-hardening/prd.md:20-21`). |
| Task manual trigger JIT grant | Weak as currently shaped | A single-task `task_id` grant is technically possible, but current grants require admin and the route is RBAC/ownership-based for operators. One PR would need a clear product decision either to support operator grants or restrict the slice to admin-only semantics. |
| Batch trigger JIT grant | Weak | Needs multi-task or request-scoped semantics and operator ownership behavior. The completed no-op telemetry should remain separate from grant enforcement. |
| Batch command creation JIT grant | Weak | High-value but multi-node/operator-permitted and command-sensitive. Current schema has no multi-node set or pre-created batch resource, and grant/audit contracts forbid command text/output storage. |
| Policy/risk-driven grant requirements | Weak | Better treated after more operation coverage; needs rule definitions for broad keys, root/sudo nodes, production tags, and risk signals. |
| P4 SSH CA / Vault-KMS / recording / command approval / WebAuthn-device trust | Not one PR | These are architecture-level changes per the planning PRD and P2 research, with migration/compatibility/privacy/provider implications. |

### Related Specs

- `.trellis/spec/backend/quality-guidelines.md:224-255` — SSH key least-privilege scope and config export/import scope contracts.
- `.trellis/spec/backend/quality-guidelines.md:380-447` — credential-use audit event contracts and forbidden metadata categories.
- `.trellis/spec/backend/quality-guidelines.md:451-499` — credential access grant contracts, fail-closed matrix, and audit requirements.
- `.trellis/spec/frontend/state-management.md:71-115` — frontend grant prompt storage-safety and sanitized denial rendering contracts.
- `.trellis/spec/frontend/a11y-guidelines.md:20-49` — accessible dialog/form requirements for any new grant prompt.

### External References

None. This research used repo/Trellis files only.

## Caveats / Not Found

- Release status was not independently verified against tags or remotes; repo/Trellis evidence shows the completed sessions and current code/docs for config import, config export, and snapshot restore grants.
- The current `CredentialAccessGrant` model has `node_id`, `task_id`, and `policy_id` resource fields only. It does not have `ssh_key_id`, snapshot ID, include path, target path, batch ID, or multi-resource set columns (`backend/internal/model/models.go:446-471`).
- Current grant matching requires `admin`, so operator-permitted flows such as task manual trigger, batch trigger, and batch command creation need role/ownership grant semantics before direct reuse (`backend/internal/api/handlers/credential_access_grant.go:408-421`).
- Existing snapshot restore grant is intentionally task-scoped only; the archived selection PRD explicitly left per-snapshot-ID, include-path, and target-path grant binding out of scope (`.trellis/tasks/archive/2026-05/05-21-select-next-p3-p4-hardening-slice/prd.md:65-72`).
