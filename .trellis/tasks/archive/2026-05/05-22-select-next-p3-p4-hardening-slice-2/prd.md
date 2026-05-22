# Select next P3/P4 hardening slice

## Goal

Continue the credential/control-plane blast-radius reduction roadmap by choosing the next executable P3/P4 hardening slice after the completed and released JIT grants for config import, sensitive config export, and snapshot restore.

## What I already know

- Current release baseline includes:
  - `config.import` / `config_import` JIT grant for `POST /config/import`.
  - `config.export` / `config_export` JIT grant for sensitive `GET /config/export?include_secrets=true`.
  - `snapshot.restore` / `snapshot` task-scoped JIT grant for `POST /tasks/:id/snapshots/:sid/restore`.
- Current row-backed `CredentialAccessGrant` supports nullable `node_id`, `task_id`, and `policy_id` resource binding.
- Current grant matching is admin-oriented; several remaining high-risk operations support operator + ownership semantics, making them larger than a mechanical copy of existing admin-only grant flows.
- Selection criteria for this slice:
  - high credential/control-plane blast radius;
  - executable in one PR without schema/model migration;
  - can reuse existing step-up, grant, audit, frontend prompt, and storage-safety patterns;
  - does not require deciding multi-resource/operator grant semantics.

## Research References

- [`research/remaining-p3-p4-roadmap.md`](research/remaining-p3-p4-roadmap.md) — remaining one-PR candidates are strongest for task restore JIT grant, SSH key export system-scoped grant, or minimal grant status/list surface.
- [`research/code-candidate-feasibility.md`](research/code-candidate-feasibility.md) — `POST /tasks/:id/restore` is the cleanest uncovered one-PR JIT-grant candidate because it is admin-only, step-up gated, and task-bound.

## Candidate Summary

| Candidate | Decision | Reason |
|---|---|---|
| Task restore trigger JIT grant | **Selected** | High-risk restore mutation, admin-only, already step-up gated, exact `task_id` grant fit, close to existing snapshot restore pattern. |
| SSH key export JIT grant | Defer | Current export does not expose private keys; exact per-key grants require new `ssh_key_id` resource binding or a broader system-scoped product decision. |
| Manual task trigger JIT grant | Defer | Route allows operator + ownership; current grant matcher/request routes are admin-only. |
| Batch trigger JIT grant | Defer | Multi-task request semantics do not fit current single-resource grant model. |
| Batch command creation JIT grant | Defer | High-risk but multi-node/operator-permitted and command-sensitive; needs multi-resource grant semantics. |
| Minimal grant list/status UI/API | Defer | Evidence/visibility improvement, not direct blast-radius reduction; better after next operation coverage. |
| P4 SSH CA / Vault-KMS / WebAuthn / recording / command approval | Out of scope | Architecture-level work requiring provider/policy/privacy/migration design. |

## Decision (ADR-lite)

**Context**: P3 work has already covered terminal, config import, sensitive config export, and snapshot file restore with row-backed JIT grants. The next slice should keep reducing high-risk execution/restore surfaces without introducing broad operator/multi-resource policy design.

**Decision**: Implement a task-scoped JIT grant for `POST /tasks/:id/restore` using:

- `action`: `task.restore_trigger`
- `purpose`: `task_restore`
- `task_id`: route `:id`

The route should continue to require admin auth and TOTP step-up, then require an active matching grant before `TaskHandler.Restore` triggers the restore run.

**Consequences**:

- This directly reduces restore blast radius for task-runner restore operations.
- It reuses existing grant schema and task-scoped matching; no migration is needed.
- It intentionally does not solve operator-grant semantics for manual triggers, batch triggers, or batch commands.
- It should mirror storage-safety from snapshot restore: no proof/reason/grant ID/target path persistence in browser storage.

## Requirements

- Add a backend grant request endpoint for task restore grants, admin-only and step-up protected.
- Add a backend route middleware requiring an active `task.restore_trigger` / `task_restore` grant with matching `task_id` before `POST /tasks/:id/restore` reaches `TaskHandler.Restore`.
- Keep existing admin role and TOTP step-up requirements; the grant is additive and fail-closed.
- Validate the target task exists and is eligible for restore before issuing the grant; do not broaden restore support beyond existing behavior.
- Add frontend restore flow support to request a local-only reason, obtain a one-time non-persistent step-up proof, request the task restore grant, then call restore with the same proof.
- Keep grant prompt state component-local; do not persist reason, grant metadata, target path, task ID, run ID, proof, or backend denial payloads in `localStorage` or `sessionStorage`.
- Render backend denial/grant errors as bounded text, not HTML.
- Update backend/frontend domain types, API wrappers, i18n, docs, and tests for the new grant tuple.

## Acceptance Criteria

- [ ] `POST /tasks/:id/restore` fails closed without a valid matching active grant even when admin auth and step-up are present.
- [ ] A valid active grant for a different user, role, action, purpose, node, policy, or task does not authorize restore.
- [ ] A valid task-scoped `task.restore_trigger` / `task_restore` grant for the route task allows restore to proceed to `TaskHandler.Restore`.
- [ ] Grant creation validates reason, TTL, step-up proof, and task eligibility without storing target paths or other restore payload details in the grant row/audit metadata.
- [ ] Frontend restore prompt blocks empty/too-long reasons locally, uses `ensureStepUpProof({ persist: false, reuseCached: false })`, and sends the same proof to grant request and restore call.
- [ ] Browser storage tests assert no proof/reason/grant status/grant ID/task target path/run ID is persisted by the restore grant flow.
- [ ] Backend tests cover grant request, fail-closed middleware, wrong tuple/resource matching, and safe audit metadata.
- [ ] Frontend tests cover unchanged opening behavior, local reason validation, success flow ordering, storage safety, and safe error rendering.
- [ ] `go -C backend test ./... -count=1`, `npm --prefix web run check`, doc freshness, diff check, and Trellis validation pass before commit.

## Definition of Done

- Tests added/updated for backend and frontend behavior.
- Backend docs and admin security docs updated if API/behavior changes.
- Trellis `implement.jsonl` and `check.jsonl` curated and validated before implementation.
- Code committed on a non-main branch, Trellis task archived, journal recorded, PR opened, CI monitored, merged, release automation monitored.

## Out of Scope

- Operator-owned manual trigger grant semantics.
- Batch trigger or batch command multi-resource grant semantics.
- Per-target-path, per-run, per-include-path, or task payload binding.
- SSH key export per-key grant resource modeling.
- Grant approval workflow, reviewer routing, policy UI, or grant management console.
- P4 architecture work: SSH CA, Vault/KMS, terminal/session recording, command-level approval/inspection, WebAuthn/passkeys, device trust.

## Technical Notes

- Backend route now observed at `backend/internal/api/router.go`: `POST /tasks/:id/restore` is admin-only and already uses `RequireStepUp(..., "task.restore_trigger", sshutil.PurposeTaskRestore, "task_restore")` before `taskHandler.Restore`.
- Backend restore handler at `backend/internal/api/handlers/task_handler.go` calls `h.runner.TriggerRestore(id, req.TargetPath)` and writes credential audit action `task.restore_trigger` with `custom_target` boolean only.
- Frontend restore dialog at `web/src/components/restore-confirm-dialog.tsx` currently wraps `apiClient.restoreTask(...)` with `useStepUpAction()` but has no grant reason prompt.
- Frontend task API at `web/src/lib/api/tasks-api.ts` already accepts optional step-up proof for restore calls.
- Existing snapshot restore grant implementation is the nearest backend/frontend reference for a task-scoped one-time grant flow.
