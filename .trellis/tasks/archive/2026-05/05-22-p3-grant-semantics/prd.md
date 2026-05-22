# P3 grant semantics for owned resources

## Goal

Extend credential access grant semantics from admin-only, single-resource operations to the remaining P3 high-risk resource operations that can involve operator-owned resources and multi-resource requests, while preserving the existing JIT grant model as short-lived row-backed authorization records.

This slice should unlock safer grant enforcement for manual task trigger, batch task trigger, and batch command creation without introducing P4 credential architecture, approval workflows, or command-inspection systems.

## Requirements

### Grant semantics

- Credential access grants remain row-backed authorization records, not bearer tokens or client-stored grant material.
- A grant remains additive to existing controls: primary authentication, purpose-scoped token rejection, RBAC, ownership checks, step-up, and credential scope checks must still run.
- Grant issuance and use must fail closed for missing, expired, revoked, denied, wrong-user, wrong-role, wrong-action, wrong-purpose, or wrong-resource grants.
- Admins may request and use grants for allowed high-risk operations as they do today, subject to the operation's existing RBAC/step-up/resource checks.
- Operators may request and use grants only for operations and resources already allowed by RBAC and ownership checks.
- Viewers and unknown roles must not be able to request or use credential access grants.
- Existing admin-only grant flows for terminal open, config import/export, task restore, and snapshot restore must keep their current authorization semantics unless explicitly touched by this task.
- Existing grant DTO/list safe-field boundary must not expand to include secrets or operational payloads.

### Resource binding

- Reuse the existing `CredentialAccessGrant` resource columns (`node_id`, `task_id`, `policy_id`) without adding a schema migration.
- Single-resource operations must bind the grant to the exact relevant resource ID.
- Multi-resource operations must use row-per-resource semantics: each protected resource requires an active matching grant row for the requesting user, role, action, purpose, and resource ID.
- Multi-resource enforcement must be all-or-nothing. If any protected target lacks a matching active grant, the operation must not partially execute.
- Grant request APIs for multi-resource operations may create multiple active rows in one request, one per normalized unique resource ID, but responses must expose only safe scalar metadata.
- Resource IDs must be normalized and deduplicated before grant creation and before grant enforcement.
- Do not bind grants to command text, target paths, include-path contents, terminal streams, executor config, hostnames, endpoint/proxy values, or file contents.

### Target operations

- Add JIT credential grant coverage for manual task trigger (`task.manual_trigger` / task-command purpose) while preserving existing `tasks:trigger`, task ownership, and step-up checks.
- Add JIT credential grant coverage for batch task trigger while preserving existing `tasks:write`, target task authorization, and any existing no-op/unsafe-target handling.
- Add JIT credential grant coverage for batch command creation while preserving existing `tasks:write`, target node authorization, and credential resolution safeguards.
- Keep task restore and snapshot restore as admin-only already-grant-gated flows unless shared helper refactoring is required.
- Keep config import/export and terminal open as existing admin-only flows for this slice.

### Backend API

- Add safe grant request endpoints only where needed for the new protected operations.
- Reuse existing authentication/RBAC style from the secured API group and operation-specific role/RBAC middleware.
- Require step-up before issuing grants for the new operations.
- Verify requester context against the current database role at grant issue and grant use time.
- For operator grant issuance, verify ownership of every requested resource before creating any grant row.
- For multi-resource issuance, use a transaction so rows are created all-or-nothing.
- Use bounded, sanitized reason text and existing TTL normalization.
- Audit grant request, grant use, and grant denial paths with sanitizer-compatible metadata only.
- Return existing sanitized denial shape for missing/invalid grants; do not leak which hidden resource failed if the caller is not authorized to know it.

### Frontend/API client

- Add typed frontend API helpers only if new grant request endpoints are surfaced through the UI.
- Keep raw snake_case API response types private to API modules and expose camelCase domain objects.
- Do not persist grant DTOs, reason text, step-up proofs, target lists, command text, command output, or grant request payloads to `localStorage` or `sessionStorage`.
- If UI affordances are added for requesting these grants, they must be minimal and operation-local; do not add approval, denial, revoke, refresh, reviewer routing, or policy-edit controls.

### Safety exclusions

- Do not expose or persist raw private keys, passwords, bearer tokens, step-up proofs, OTP/recovery values, executor config, imported/exported config payloads, terminal streams, command text/output, Docker output, diagnostic output, exported secret material, raw SQL, endpoint/proxy values, hostnames, target paths, include-path contents, file contents, or decrypted credential material.
- Do not add command parsing, command policy language, command approval, reviewer routing, grant approval workflow, grant revocation UI, SIEM/ChatOps integration, SSH CA, Vault/KMS/external brokers, terminal/session recording, WebAuthn/passkeys, device trust, or configurable grant policy UI.

## Acceptance Criteria

- [ ] Operators can request a manual task-trigger grant only for tasks they are authorized to trigger and whose target resources they own.
- [ ] Operators cannot request or use manual task-trigger grants for unowned tasks/resources.
- [ ] Manual task trigger requires a matching active grant in addition to current auth/RBAC/ownership/step-up checks.
- [ ] Batch task trigger requires matching active grant rows for every protected target and fails all-or-nothing when any target is missing/invalid.
- [ ] Batch command creation requires matching active grant rows for every protected target node and fails all-or-nothing when any target is missing/invalid.
- [ ] Admin grant behavior remains compatible with existing admin-only grant-gated operations.
- [ ] Viewers and unknown roles cannot request or use new credential access grants.
- [ ] Expired, revoked, denied, wrong-user, wrong-role, wrong-action, wrong-purpose, and wrong-resource grants fail closed with sanitized errors.
- [ ] Grant request/use/denial tests cover operator-owned resources, unowned resources, multi-resource success, multi-resource partial-missing failure, and role-change invalidation.
- [ ] Tests prove no unsafe credential/control-plane material is exposed in responses, audit metadata, or frontend storage.
- [ ] Frontend tests cover any new API helpers/UI affordances and prove no grant DTOs, reason text, step-up proofs, target lists, or command payloads are persisted to browser storage.
- [ ] No approve/revoke/deny/workflow/policy-edit controls are introduced.
- [ ] Backend and frontend verification commands pass for touched areas, with full project checks run before commit.

## Definition of Done

- Backend tests for grant issuance and enforcement semantics pass.
- Frontend tests for any touched API/UI grant flows pass.
- Full backend test suite and frontend check suite pass before commit.
- Trellis task is validated, started, implemented, checked, committed, archived, and recorded in the session journal.
- PR is created, CI is monitored to green, merged, and release automation is monitored through the published release and Docker image workflow.

## Technical Approach

### Backend

1. Introduce shared grant role/resource authorization helpers that keep the existing admin path valid while allowing operator paths only for explicitly supported owned-resource operations.
2. Generalize requester validation so it verifies DB role equality and allowed roles per operation, instead of globally requiring `admin` for every grant.
3. Generalize active grant lookup so it validates current DB role equality and allows `operator` for explicitly supported operations, while preserving exact user/role/action/purpose/resource matching.
4. Add grant request endpoints for task manual trigger, batch task trigger, and batch command creation. For multi-resource requests, normalize/deduplicate IDs, authorize all targets, and create one grant row per resource in a transaction.
5. Add middleware/helpers for requiring one grant for a single task and matching grant rows for all targets in a batch request before credential resolution or operation execution.
6. Wire the new grant requirements into task trigger, batch trigger, and batch command creation routes/handlers without weakening existing RBAC, ownership, or step-up middleware.
7. Add focused tests around ownership, role changes, all-or-nothing multi-resource matching, inactive grants, and sanitized audit/error metadata.

### Frontend

1. Inspect existing manual trigger, batch trigger, and batch command UI flows to decide the smallest operation-local grant-request affordance needed.
2. Add API helpers for new grant request endpoints if the UI needs to request grants directly.
3. Reuse existing step-up/grant-required UI patterns where available; otherwise add minimal dialogs/forms with in-memory state only.
4. Add tests for grant request mapping, non-persistence to browser storage, and absence of workflow controls.

## Decision (ADR-lite)

**Context**: Current credential access grants cover several admin-only high-risk operations, but remaining P3 operations include operator-owned and multi-resource flows. The current schema supports optional single resource IDs and current enforcement hard-codes `admin`, so semantics must be clarified before broadening use.

**Decision**: Use row-per-resource JIT grants for multi-resource operations and allow operators only for explicitly supported owned-resource actions. Keep grants short-lived, self-issued after step-up, exact-match, and additive to RBAC/ownership/credential checks. Use all-or-nothing multi-resource enforcement.

**Consequences**: This avoids schema changes and prevents partial execution gaps, but may create several grant rows for batch operations. It intentionally does not solve approval workflows, revocation UX, reviewer routing, command inspection, configurable policy UI, or P4 credential broker architecture.

## Out of Scope

- New database schema or migration changes.
- Grant approval, denial, revocation, refresh, retry, reviewer routing, or workflow state transitions.
- Operator/admin grant management console beyond the already shipped admin read-only list.
- Terminal open, config import/export, task restore, or snapshot restore semantic expansion unless required by shared helper safety.
- SSH key export per-key grant modeling.
- Command parsing, command policy language, command approval, or storing command text.
- Per-target-path, per-include-path, per-run-payload, or file-content grant binding.
- SSH CA, Vault/KMS/external brokers, terminal/session recording, WebAuthn/passkeys, device trust, SIEM/ChatOps integration, or configurable grant policy UI.
- Persisting grant list/filter/detail/request state to browser storage.

## Research References

- `.trellis/spec/backend/quality-guidelines.md` — credential grant safety contract: row-backed, additive controls, exact fail-closed semantics, safe audit metadata.
- `.trellis/tasks/archive/2026-05/05-21-plan-p3-p4-credential-security-hardening/prd.md` — roadmap source for extending P3 grant enforcement to selected REST high-risk operations.
- `.trellis/tasks/archive/2026-05/05-22-select-next-p3-p4-hardening-slice-2/prd.md` — deferred manual trigger and batch operation semantics because existing grants were admin-only/single-resource.
- `.trellis/tasks/archive/2026-05/05-22-p3-minimal-grant-status-list/prd.md` — predecessor slice explicitly positioned before operator-owned and multi-resource enforcement.

## Technical Notes

- Current task directory: `.trellis/tasks/05-22-p3-grant-semantics`.
- Current backend grant implementation is centered in `backend/internal/api/handlers/credential_access_grant.go`.
- Current grant model is `model.CredentialAccessGrant` with nullable `node_id`, `task_id`, and `policy_id`.
- Current grant request routes are admin-only in `backend/internal/api/router.go`.
- Current manual task trigger route already has RBAC, task ownership, and step-up but no credential grant requirement.
- Current batch trigger and batch command routes are multi-resource candidates and require careful all-or-nothing grant enforcement before credential use.
- Existing ownership helpers in `backend/internal/api/handlers/helpers.go` and middleware ownership checks should be reused where possible.
