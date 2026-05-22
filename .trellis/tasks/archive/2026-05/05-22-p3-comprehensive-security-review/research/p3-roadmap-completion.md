# Research: P3 roadmap completion

- **Query**: Research the P3/P4 credential hardening roadmap completion for the current repo. Read archived Trellis PRDs under `.trellis/tasks/archive/2026-05/` that mention P3/P4, credential grants, hardening, and recent commits. Determine which P3 slices are complete, which P3 requirements remain unimplemented or need review, and whether the best next task should be implementation or comprehensive P3 review.
- **Scope**: internal
- **Date**: 2026-05-22

## Findings

### Files Found

| File Path | Description |
|---|---|
| `.trellis/tasks/archive/2026-05/05-21-plan-p3-p4-credential-security-hardening/prd.md` | Roadmap source grouping P3 near-term control-plane hardening and P4 architecture-level hardening. |
| `.trellis/tasks/archive/2026-05/05-21-p3-config-import-grant-batch-telemetry/prd.md` | First P3 implementation slice: config import JIT grant plus all-blocked batch-trigger telemetry. |
| `.trellis/tasks/archive/2026-05/05-21-p3-control-plane-follow-up-hardening/prd.md` | P3 follow-up slice: sensitive config export JIT grant. |
| `.trellis/tasks/archive/2026-05/05-21-select-next-p3-p4-hardening-slice/prd.md` | P3 selection slice choosing snapshot restore task-scoped JIT grant. |
| `.trellis/tasks/archive/2026-05/05-22-select-next-p3-p4-hardening-slice-2/prd.md` | P3 selection slice choosing task restore task-scoped JIT grant after snapshot restore. |
| `.trellis/tasks/archive/2026-05/05-22-p3-minimal-grant-status-list/prd.md` | P3 visibility slice adding read-only admin grant status/list API/UI. |
| `.trellis/tasks/archive/2026-05/05-22-p3-grant-semantics/prd.md` | P3 semantics slice for operator-owned and multi-resource grant enforcement. |
| `.trellis/tasks/archive/2026-05/05-22-select-next-p3-p4-hardening-slice-2/research/remaining-p3-p4-roadmap.md` | Prior roadmap research after config import/export and snapshot restore, including remaining candidate ranking. |
| `.trellis/tasks/archive/2026-05/05-22-select-next-p3-p4-hardening-slice-2/research/code-candidate-feasibility.md` | Prior code feasibility research for remaining high-risk routes and frontend surfaces. |
| `.trellis/tasks/archive/2026-05/05-21-p3-control-plane-follow-up-hardening/research/remaining-control-plane-surfaces.md` | Candidate comparison that selected sensitive config export before later slices. |
| `.trellis/tasks/archive/2026-05/05-20-security-p2-credential-hardening/prd.md` | P2 baseline introducing row-backed terminal JIT grants and follow-up roadmap. |
| `backend/internal/api/router.go` | Current route registration showing grant request/list endpoints and protected high-risk operation routes. |
| `backend/internal/api/handlers/credential_access_grant.go` | Current backend grant constants, request handlers, list handler, role/resource semantics, matching/enforcement helpers, and audit writers. |
| `backend/internal/api/handlers/task_handler.go` | Current manual trigger, restore, batch trigger handlers and batch-trigger no-op telemetry. |
| `backend/internal/api/handlers/batch_handler.go` | Current batch command creation flow with ownership, step-up, and multi-node grant enforcement. |
| `web/src/lib/api/credential-access-grants-api.ts` | Current frontend grant list/request API mapping for all shipped grant action/purpose tuples. |
| `web/src/pages/tasks-page.tsx` | Current frontend batch trigger grant request flow; manual trigger flow remains notable for review. |

### Completed P3 Slices

| P3 slice | Completion evidence | Current code evidence |
|---|---|---|
| `config.import` / `config_import` JIT grant | First P3 PRD required admin auth, TOTP step-up, and active grant before `POST /config/import` mutates secret-bearing config (`05-21-p3-config-import-grant-batch-telemetry/prd.md:16-34`). | Router applies `RequireStepUp` then `RequireConfigImportCredentialGrant` before `configHandler.Import` (`backend/internal/api/router.go:333`); request handler exists (`credential_access_grant.go:216-236`). Commit history includes `348c616 fix(security): gate config import with JIT grants`. |
| All-blocked/no-op batch trigger telemetry | First P3 PRD required sanitized attempted-action credential audit evidence when `POST /tasks/batch-trigger` filters all tasks (`05-21-p3-config-import-grant-batch-telemetry/prd.md:35-40`). | `BatchTrigger` writes `task.batch_trigger` audit with `stage=no_op`, requested/eligible/executed/failure/blocked counts, and `no_op=true` when no tasks remain (`backend/internal/api/handlers/task_handler.go:735-749`). |
| Sensitive `config.export` / `config_export` JIT grant | Follow-up PRD selected `GET /config/export?include_secrets=true` because it can serialize node secrets, SSH keys, executor config, and settings (`05-21-p3-control-plane-follow-up-hardening/prd.md:3-20`). | Router conditionally applies step-up and `RequireConfigExportCredentialGrantIf` only when `include_secrets=true` before `configHandler.Export` (`backend/internal/api/router.go:328-332`); request handler exists (`credential_access_grant.go:238-258`). Commit history includes `cea7d9b fix(security): gate sensitive config export with JIT grants`. |
| `snapshot.restore` / `snapshot` task-scoped JIT grant | Selection PRD chose `POST /tasks/:id/snapshots/:sid/restore` with task-bound `task_id=:id` (`05-21-select-next-p3-p4-hardening-slice/prd.md:16-35`). | Router applies admin role, step-up, `RequireSnapshotRestoreCredentialGrant`, then `snapshotHandler.Restore` (`backend/internal/api/router.go:317-319`); request handler validates restic task and creates task-scoped grant (`credential_access_grant.go:260-300`). Commit history includes `ef8a0a7 fix(security): gate snapshot restore with JIT grants (#217)`. |
| `task.restore_trigger` / `task_restore` task-scoped JIT grant | Second selection PRD chose admin-only `POST /tasks/:id/restore` as the cleanest uncovered one-PR candidate (`05-22-select-next-p3-p4-hardening-slice-2/prd.md:28-67`). | Router applies admin role, step-up, `RequireTaskRestoreCredentialGrant`, then `TaskHandler.Restore` (`backend/internal/api/router.go:297`); request handler validates task restore eligibility and creates task-scoped grant (`credential_access_grant.go:302-331`). Commit history includes `f832359 fix(security): gate task restore with JIT grants`. |
| Minimal read-only grant status/list API/UI | PRD positioned this as transitional P3 visibility before operator-owned/multi-resource enforcement (`05-22-p3-minimal-grant-status-list/prd.md:3-6`, `:118-124`). | Router registers admin-only `GET /credential-access-grants` (`backend/internal/api/router.go:265`); `CredentialAccessGrantHandler.List` returns paginated DTOs (`credential_access_grant.go:159-176`); frontend API has `listCredentialAccessGrants` and query mapper (`web/src/lib/api/credential-access-grants-api.ts:155-174`). Commit history includes `fd25d45 fix(security): add admin grant status list`. |
| Operator-owned / multi-resource grant semantics for manual trigger, batch trigger, and batch command | PRD required row-per-resource, all-or-nothing grants for manual task trigger, batch task trigger, and batch command creation while allowing operators only for owned resources (`05-22-p3-grant-semantics/prd.md:32-38`, `:64-78`). | Router now registers grant request endpoints for task manual trigger, task batch trigger, and batch command (`backend/internal/api/router.go:271-273`); manual trigger route has `RequireTaskManualTriggerCredentialGrant` (`router.go:292`); batch trigger enforces step-up and `EnforceTaskBatchTriggerCredentialGrants` before triggering (`task_handler.go:717-722`); batch command enforces step-up and `EnforceBatchCommandCredentialGrants` before task creation (`batch_handler.go:113-122`). Backend role matching allows `admin` and `operator` for these operations (`credential_access_grant.go:842-852`). Commit history includes `a7ed3ca fix(security): enforce owned resource grant semantics`. |

### Code Patterns

#### Current grant action/purpose coverage

`backend/internal/api/handlers/credential_access_grant.go` defines the active grant action set at lines 33-42:

```go
CredentialGrantActionTerminalOpen      = "terminal.open"
CredentialGrantActionConfigImport      = "config.import"
CredentialGrantActionConfigExport      = "config.export"
CredentialGrantActionSnapshotRestore   = "snapshot.restore"
CredentialGrantActionTaskRestore       = "task.restore_trigger"
CredentialGrantActionTaskManualTrigger = "task.manual_trigger"
CredentialGrantActionTaskBatchTrigger  = "task.batch_trigger"
CredentialGrantActionBatchCommand      = "batch_command.create"
CredentialGrantPurposeConfigImport     = "config_import"
CredentialGrantPurposeConfigExport     = "config_export"
```

Frontend mapping recognizes the same current grant operations and non-authorizing fallbacks: `credential-access-grants-api.ts:61-84` maps known actions (`terminal.open`, `config.import`, `config.export`, `snapshot.restore`, `task.restore_trigger`, `task.manual_trigger`, `task.batch_trigger`, `batch_command.create`), known purposes (`terminal`, `config_import`, `config_export`, `snapshot`, `task_restore`, `task_command`, `batch_command`), and unknown statuses to `expired`.

#### Current backend enforcement coverage

- System-scoped grants: `RequireConfigImportCredentialGrant` and `RequireConfigExportCredentialGrantIf` use null resource IDs (`credential_access_grant.go:648-654`).
- Task-scoped grants: snapshot restore, task restore, and manual trigger parse route `:id` and require an exact `task_id` match (`credential_access_grant.go:656-686`).
- Multi-resource task grants: batch trigger normalizes task IDs and requires one matching row per task (`credential_access_grant.go:689-695`).
- Multi-resource node grants: batch command normalizes node IDs and requires one matching row per node (`credential_access_grant.go:697-703`).
- Grant matching reloads the user role from DB and checks operation-specific allowed roles; only manual trigger, batch trigger, and batch command allow `operator`, all other grant operations default to `admin` (`credential_access_grant.go:795-852`).

#### Current routes for P3 grant coverage

`backend/internal/api/router.go` now shows a mature grant endpoint cluster:

- `GET /credential-access-grants` admin-only list (`router.go:265`).
- Grant request endpoints for terminal, config import, config export, snapshot restore, task restore, task manual trigger, task batch trigger, and batch command (`router.go:266-273`).
- Enforced operation routes:
  - manual task trigger: `RBAC("tasks:trigger")`, ownership check, step-up, manual-trigger grant, handler (`router.go:292`).
  - task restore: admin, step-up, task-restore grant, handler (`router.go:297`).
  - snapshot restore: admin, step-up, snapshot-restore grant, handler (`router.go:319`).
  - sensitive config export: admin, conditional step-up, conditional grant, handler (`router.go:328-332`).
  - config import: admin, step-up, grant, handler (`router.go:333`).
- Batch trigger and batch command use handler-level grant enforcement because they must parse request bodies and compute authorized target sets before all-or-nothing enforcement (`task_handler.go:717-722`; `batch_handler.go:113-122`).

### Remaining P3 Requirements / Items Needing Review

| Requirement / candidate | Current status from repo | Why it remains unimplemented or needs review |
|---|---|---|
| SSH key export JIT grant | Not observed in current grant constants/routes. Existing P3/P2 roadmap listed SSH key export as a follow-up (`05-21-plan-p3-p4-credential-security-hardening/prd.md:55-58`; `05-20-security-p2-credential-hardening/prd.md:111-114`). | Prior research noted current export emits public-key/fingerprint-oriented data rather than private keys and exact per-key grants would need `ssh_key_id` or a system-scoped decision. It remains the clearest named P3 candidate not yet grant-covered. |
| Node test connection / SSH key test connection / emergency backup / policy drill trigger / system backup DB | These appeared as feasibility candidates in `code-candidate-feasibility.md:57-62`, but were not in the core P3 grant implementation sequence. | They need classification in a comprehensive review: some use stored credentials or fan out to tasks, but archived PRDs did not select them as required P3 completion criteria. |
| Policy/risk-driven grant requirements | P3 planning listed policy/risk-driven grant requirements after broader operation coverage (`05-21-plan-p3-p4-credential-security-hardening/prd.md:20-21`, `:61-67`). | Broader operation coverage now exists, but no current code evidence of a policy/risk matrix or configurable grant requirement layer was found. This is probably a later P3/P4 boundary decision rather than an immediate one-route implementation. |
| Grant semantics correctness across the newly shipped operator/multi-resource slice | Implemented in code, but this is the newest and broadest P3 change (`a7ed3ca`). | Because it changed role semantics, ownership checks, multi-row grant issuance, all-or-nothing enforcement, and batch command/task trigger execution gates, it is the main area that needs comprehensive review before continuing to more implementation. |
| Frontend prompt coverage for all grant-required routes | Frontend API supports grant request endpoints. `tasks-page.tsx` batch trigger requests a batch grant with a generated localized reason (`tasks-page.tsx:365-384`). Manual trigger path at `tasks-page.tsx:248-258` calls `triggerTask(taskId)` through the hook path and should be included in review to confirm the grant prompt/retry path exists at the hook/API layer and not only backend-denies. | This is not enough evidence to mark manual trigger UX complete without reading `use-console-data`/task hook implementations and tests. |
| Alert-center task trigger path | Prior feasibility research observed `web/src/pages/notifications/alert-center.tsx` calls `apiClient.triggerTask(token, alert.taskId)` without the standard step-up wrapper (`code-candidate-feasibility.md:42`, `:254-260`). | Current review should verify whether this path now handles both `STEP_UP_REQUIRED` and `CREDENTIAL_GRANT_REQUIRED` or intentionally surfaces a bounded denial. |
| P4 architecture items | P3/P4 planning listed SSH CA/external CA, Vault/KMS/external broker, terminal/session recording, command-level approval/inspection, WebAuthn/passkeys/device trust/configurable policy UI (`05-21-plan-p3-p4-credential-security-hardening/prd.md:23-29`, `:69-77`). | No evidence these were intended to be part of P3 completion. They remain P4 architecture-level work. |

### Recent Commit Evidence

Recent security-related commits in the current repo show the P3 sequence has continued beyond the archived selection research:

| Commit | Meaning for roadmap completion |
|---|---|
| `348c616 fix(security): gate config import with JIT grants` | First P3 implementation slice landed. |
| `cea7d9b fix(security): gate sensitive config export with JIT grants` | Sensitive export follow-up landed. |
| `ef8a0a7 fix(security): gate snapshot restore with JIT grants (#217)` | Snapshot restore task-scoped grant landed. |
| `f832359 fix(security): gate task restore with JIT grants` | Task restore task-scoped grant landed. |
| `fd25d45 fix(security): add admin grant status list` | Minimal grant list/status visibility slice landed. |
| `a7ed3ca fix(security): enforce owned resource grant semantics` | Operator-owned and multi-resource grant semantics landed for manual trigger, batch trigger, and batch command. |
| `0a4385b chore(main): release 0.43.10` | Current branch baseline includes the latest release after the owned-resource grant semantics commit. |

### Related Specs

- `.trellis/spec/backend/quality-guidelines.md` — Defines credential access grant contract: row-backed records, additive controls, exact fail-closed semantics, safe metadata and forbidden secret/control-plane payload categories.
- `.trellis/spec/frontend/state-management.md` — Defines grant prompt state and browser-storage safety rules.
- `.trellis/spec/frontend/type-safety.md` — Defines raw grant DTO/domain mapping, unknown fallback behavior, and safe error/status handling.
- `.trellis/spec/frontend/a11y-guidelines.md` — Applies to grant prompts/dialogs and grant status/list UI.

### Assessment: Implementation vs Comprehensive Review

The current repository appears to have completed the main P3 implementation sequence described by the archived PRDs:

1. system-scoped config import grant,
2. all-blocked batch-trigger telemetry,
3. system-scoped sensitive config export grant,
4. task-scoped snapshot restore grant,
5. task-scoped task restore grant,
6. read-only admin grant status/list,
7. operator-owned and row-per-resource grant semantics for manual trigger, batch trigger, and batch command.

The best next task should be **comprehensive P3 review**, not another immediate implementation slice. The reason is that the latest completed slice (`a7ed3ca`) is broad and touches the highest-risk remaining P3 semantics: operator access, ownership, role-change invalidation, multi-resource all-or-nothing enforcement, frontend prompt composition, and no-secret audit/storage boundaries. A review can confirm whether P3 is actually complete, identify any regressions or missed frontend paths, and classify the few remaining candidates (especially SSH key export and policy/risk-driven grants) as either required P3 work or deferred/P4 scope.

### Caveats / Not Found

- No external references were used; this was a repo/Trellis-only roadmap and code inspection.
- I did not run backend/frontend tests for this research; completion status is based on archived PRDs, code presence, and commit history.
- SSH key export JIT grant was not observed as implemented in current grant constants/routes. Prior research downgraded it because current export is public-key/fingerprint-oriented and exact per-key scoping lacks a grant resource column, but the roadmap still names it as a P3 follow-up candidate.
- Manual trigger frontend coverage and alert-center trigger behavior need direct review in the comprehensive task; backend route enforcement is present, but UI prompt/retry evidence was only partially inspected here.
- P4 architecture work remains explicitly out of scope for P3 completion.
