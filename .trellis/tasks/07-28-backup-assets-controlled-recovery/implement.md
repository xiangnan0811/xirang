# Backup Asset Controlled Recovery Implementation Plan

> **For future implementation sessions:** load `trellis-before-dev` and
> `superpowers:test-driven-development` before the first product edit, run
> `trellis-check` after edits, and use
> `superpowers:verification-before-completion` before any completion claim.
> The R0 rebaseline and F3 adjudication ran inline in the main session.
> Later bounded workers, review tasks, goals, or subagents are optional tools,
> not a required execution topology; use them only under the controlling R0
> governance in §1.3.

**Goal:** deliver exact, preflighted and fenced backup-asset recovery plans and
jobs, isolated result delivery and bounded plaintext cleanup, with a separately
authorized high-risk in-place phase.

**Architecture:** a managed Recovery graph freezes one Repository, one exact
RecoveryPoint and explicit AssetRefs into a versioned plan; performs read-only
source/target/capability preflight; consumes a plan-bound Admin grant; and runs
closed Provider restore contracts through fenced target ports. Repository owns
the concrete managed-Rsync resolver/port, while Provider remains free of
Repository and Recovery dependencies. RecoveryJob outcome and RecoveryResultSet
cleanup remain independent. Existing
lease, keyring, Content delivery, audit, settings and runtime seams are extended
through closed typed arms rather than duplicated.

**Tech stack:** Go 1.26, Gin, GORM, SQLite/PostgreSQL, SSH/SFTP and typed Provider
adapters; React 18, TypeScript 5.8 strict, Vite 7, Vitest, Testing Library,
i18next; Trellis task/PR/CI workflow.

---

## 1. Execution And Approval Gate

### 1.1 Current Planning State

```text
task:                     .trellis/tasks/07-28-backup-assets-controlled-recovery
status:                   in_progress
parent:                   07-12-backup-data-explorer-design (planning)
branch:                   codex/task8-managed-runtime
planning baseline:        51771654a85967656fe1ca69686590b734ff9214
delivered program state:  12/15
original planning artifacts: complete_approved (two independent rereviews, 2026-07-28)
current planning scope:   9 paths
future manifest:          55 create + 81 modify
current exact union:      145 unique, disjoint paths
migration ownership:      paired 000069; 000070+ reserved
task.py start:            executed_once (2026-07-28)
Task 1 execution:         complete_approved (000069/model/contracts)
Task 1 focused correction: four latest Important gaps closed with observed RED-to-GREEN
Task 1 review gates:      independent spec and live-worktree quality reviews APPROVED (2026-07-29)
Task 2 execution:         complete_approved (selection/source/plan; 2026-07-29)
Task 2 review gates:      controller-inline spec and quality passes APPROVED (2026-07-29)
Task 3 execution:         complete_approved (task scope, 2026-07-30)
Task 3 review gates:      independent specification APPROVED; controller-inline quality recheck passed
Task 4 B1 execution:      complete_approved (focused Catalog closure; SPEC APPROVED; QUALITY APPROVED)
Task 5 execution:         complete_approved (focused receipt scope; SPEC APPROVED; QUALITY APPROVED: READY)
Task 6 B1:                complete_checked (focused batches preserved; fresh combined/whole evidence passed)
Task 6 B2-E1/E2:          complete_checked (Corrections 4 and 6 + delete row; focused scopes preserved)
Task 6 B2:                complete_checked (fresh combined/whole evidence passed)
Task 6 B3:                PROVED_COMPLETE_FOCUSED_ONLY (Correction 14; integrated by current whole review)
Task 6 F6:                complete_approved (focused live-mutation-permit scope only; SPEC/QUALITY APPROVED)
Task 6 F3:                complete_checked (focused persistent scheduler/pre-write-drift/replay scope only)
Task 6 F4:                complete_checked (focused workspace/deadline/cleanup-only scope only)
Task 6 execution:         complete_checked (20 corrections; whole spec/quality review and final gates passed)
Task 7 execution:         complete_checked_whole_scope; delivered through PR #410
Task 8 execution:         in_progress_b7_complete_checkpoint
Tasks 9-10 execution:     not_executed
Task 5 receipt evidence:  focused SQLite/real-PostgreSQL normal and race gates passed; both independent reviews approved
Task 8 dirty union:       34 paths; approved product/task-local evidence scope; staged zero
stage/commit/push/PR/CI/merge: not_executed
parent completion:        forbidden in Child 13
```

The former standing direction authorized the old controller to make routine
technical and process decisions. It is retained as historical authority for the
work already performed, but the R0 rebaseline in §1.3 supersedes it for future
execution: it is not an open-ended authorization to continue unattended. The
old controller task and its goal are paused and are evidence, not the current
execution owner. Manifest drift, genuinely new product scope, destructive
external actions outside this plan, direct commits to `main`, and premature
completion claims remain unauthorized.

### 1.2 Required Pre-Start Preflight

- [x] **Step 1: Reconfirm branch, baseline and task state.**

```bash
cd /home/murray/code/xirang
git fetch origin --prune
test "$(git branch --show-current)" = codex/backup-assets-controlled-recovery
test "$(git rev-parse HEAD)" = 51771654a85967656fe1ca69686590b734ff9214
test "$(git rev-parse main)" = 51771654a85967656fe1ca69686590b734ff9214
test "$(git rev-parse origin/main)" = 51771654a85967656fe1ca69686590b734ff9214
test "$(git merge-base HEAD origin/main)" = 51771654a85967656fe1ca69686590b734ff9214
jq -e '.status == "planning"' \
  .trellis/tasks/07-28-backup-assets-controlled-recovery/task.json
jq -e '.status == "planning"' \
  .trellis/tasks/07-12-backup-data-explorer-design/task.json
test -z "$(git diff --cached --name-only)"
```

Expected: every command exits zero. If `origin/main` moved, refresh the planning
baseline and re-run both planning reviews before starting; never silently build
on an unreviewed base.

- [x] **Step 2: Reconfirm migration reservation and exact planning scope.**

```bash
for engine in sqlite postgres; do
  test -e "backend/internal/database/migrations/$engine/000068_backup_asset_export.up.sql"
  test -e "backend/internal/database/migrations/$engine/000068_backup_asset_export.down.sql"
  for version in 000069 000070 000071; do
    test -z "$(find "backend/internal/database/migrations/$engine" -maxdepth 1 \
      -name "${version}_*" -print -quit)"
  done
done
python3 ./.trellis/scripts/task.py validate \
  .trellis/tasks/07-28-backup-assets-controlled-recovery
python3 ./.trellis/scripts/task.py validate \
  .trellis/tasks/07-12-backup-data-explorer-design
git diff --check
```

Expected: paired 000068 exists, 000069-000071 are absent, both tasks validate,
and the only dirty paths are the nine Phase 1 paths in §2.1.

- [x] **Step 3: Complete independent planning reviews.** One reviewer covers
  PRD/design security, data and state contracts. A second reviewer covers the
  exact manifest, ordered TDD plan, commands and delivery truth. Resolve every
  Critical/Important finding, then re-run this section.

- [x] **Step 4: Record planning/start authorization and start exactly once.**

```bash
python3 ./.trellis/scripts/task.py start \
  .trellis/tasks/07-28-backup-assets-controlled-recovery
jq -e '.status == "in_progress"' \
  .trellis/tasks/07-28-backup-assets-controlled-recovery/task.json
jq -e '.status == "planning"' \
  .trellis/tasks/07-12-backup-data-explorer-design/task.json
```

Expected: Child 13 becomes `in_progress`; the parent remains `planning`. Do not
run this command before the two planning reviews close.

### 1.3 R0 Bounded-Execution Rebaseline (2026-08-02)

This section is later controlling process guidance for the unfinished Child 13
work. It changes no product requirement, migration reservation, manifest path,
task state, test credit, or delivery status. It is a recovery plan for this
specific task, not a universal repository workflow or a claim that one process
shape is a silver bullet.

- **Hard heartbeat rule:** do not create, resume, or replace a 15-minute
  heartbeat for this task. The existing `child-13-controller-heartbeat` remains
  paused as evidence. A different future automation requires an explicit,
  bounded purpose and must not recreate continuous unattended execution by
  another interval.
- **Goals and orchestration remain available:** a goal, controller task, child
  task, user-visible task, or subagent may be used when it has one concrete
  milestone, an exact scope/owner, an observable handoff, and a clear stop
  condition. Do not use one goal to span the rest of Child 13 plus Children
  14--15, and do not keep an open-ended goal running merely because work remains.
- **Inline is the current default, not a permanent ban on delegation:** R0 and
  the F3 decision were handled in ordinary sessions. Later implementation or
  independent review may use bounded workers when isolation or review
  independence materially helps. No worker may infer authority beyond its
  assigned milestone or silently create an unbounded descendant tree.
- **Concurrency is a default, not an absolute:** normally keep one active
  feature branch, one working implementation worktree, and one product PR for
  this Child. More may be used only when the work is genuinely independent,
  shared-state collisions are ruled out, and the additional coordination cost
  is justified. The nine detached historical worktrees are not active Child 13
  execution worktrees and are not cleaned during R0.
- **Progress is evidence-based and multidimensional:** do not infer progress
  from elapsed time, token use, line count, dirty-path count, or checkbox count
  alone. Report (a) parent deliverables merged/archived, (b) exact Child task and
  batch status, (c) focused versus whole-gate evidence, (d) delivery state, and
  (e) unresolved scope/risk.

Current R0 progress is therefore:

| Axis | Current evidence-backed status |
|---|---|
| Parent program | 12 of 15 deliverables merged and archived = 80%; Trellis `12/13` counts only instantiated children |
| Child 13 product | Tasks 1--5 `complete_approved`; Task 6 partial; Tasks 7--10 not executed |
| Task 6 | B1-E1/E2/E3 `complete_checked` but B1 aggregate partial; B2-E1/E2 `complete_checked` but B2 aggregate partial; B3 and F6 focused complete; F3 and F4 `complete_checked`; unchecked rows, whole reviews and whole gates open |
| Child verification | focused SQLite/race/real-PostgreSQL receipts exist; frontend, Child-wide, Docker, hosted CI and delivery gates remain open |
| Git delivery | staged/commit/push/Child-13-PR/CI/merge all not executed |
| Scope risk | 80 dirty paths are in the 145-path allow-list; `go.mod` and root-level `recovery/testdata/rsync_local_to_remote.json` remain protected pending disposition |

The bounded milestone sequence is:

1. **R0 rebaseline (`complete`):** reconcile evidence, governance,
   Git/worktree/remote state, and the mechanical ledger only. No product
   implementation or Git delivery.
2. **F3 adjudication (`complete_approved`, decision only):** persistent Plan A+
   is recorded with exact files, selectors, RED seam, paired-migration
   disposition and stop condition. It grants no product completion credit.
3. **F3 implementation (`complete_checked`):** the approved F3 batch, focused
   check, evidence and stop/report checkpoint completed on 2026-08-02; no B1/B2
   work started.
4. **B1/B2 evidence closure (`complete_checked` at focused batches):**
   B1-E1, B1-E2, B1-E3, B2-E1 and B2-E2 are complete at their exact focused
   scopes. Both aggregates remain partial until their combined/whole evidence;
   reassess before starting F4.
5. **F4 and whole Task 6:** the Task-6-owned F4 slice is `complete_checked` at
   focused scope. Next run whole Task 6 specification review, quality review and
   final Task 6 gates.
6. **Tasks 7--10 and delivery:** proceed one task at a time with a fresh entry
   check and explicit exit evidence, then run Task 11 and Task 12. Children 14
   and 15 remain later parent-program deliverables and are not covered by this
   Child's execution authority.

At each milestone, stop on scope change, conflicting controlling contracts,
missing required evidence, environment failure that prevents the exact gate, or
need for new user authority. Ordinary implementation discoveries inside the
approved milestone do not require a new controller topology by default.

## 2. Exact File Manifest

All paths are repo-root relative. Any product, test, migration, specification
or Trellis evidence path outside these lists requires a focused written
amendment before edit. Create paths must be absent at the reviewed HEAD; modify
paths must be tracked at that HEAD. The create and modify sets are unique and
disjoint.

### 2.1 Phase 1 Planning Manifest (9 Paths)

```text
.trellis/tasks/07-12-backup-data-explorer-design/task.json
.trellis/tasks/07-28-backup-assets-controlled-recovery/check.jsonl
.trellis/tasks/07-28-backup-assets-controlled-recovery/design.md
.trellis/tasks/07-28-backup-assets-controlled-recovery/implement.jsonl
.trellis/tasks/07-28-backup-assets-controlled-recovery/implement.md
.trellis/tasks/07-28-backup-assets-controlled-recovery/prd.md
.trellis/tasks/07-28-backup-assets-controlled-recovery/research/child13-security-state-review.md
.trellis/tasks/07-28-backup-assets-controlled-recovery/research/current-main-evidence.md
.trellis/tasks/07-28-backup-assets-controlled-recovery/task.json
```

### 2.2 Future Create Manifest (55 Paths)

```text
.trellis/tasks/07-28-backup-assets-controlled-recovery/research/implementation-evidence.md

backend/internal/model/backup_asset_recovery.go
backend/internal/model/backup_asset_recovery_test.go

backend/internal/database/migrations/sqlite/000069_backup_asset_recovery.up.sql
backend/internal/database/migrations/sqlite/000069_backup_asset_recovery.down.sql
backend/internal/database/migrations/postgres/000069_backup_asset_recovery.up.sql
backend/internal/database/migrations/postgres/000069_backup_asset_recovery.down.sql

backend/internal/backupasset/recovery/contracts.go
backend/internal/backupasset/recovery/contracts_test.go
backend/internal/backupasset/recovery/state.go
backend/internal/backupasset/recovery/state_test.go
backend/internal/backupasset/recovery/service.go
backend/internal/backupasset/recovery/service_test.go
backend/internal/backupasset/recovery/preflight.go
backend/internal/backupasset/recovery/preflight_test.go
backend/internal/backupasset/recovery/target.go
backend/internal/backupasset/recovery/worker.go
backend/internal/backupasset/recovery/target_test.go
backend/internal/backupasset/recovery/worker_test.go
backend/internal/backupasset/recovery/executor_test.go
backend/internal/backupasset/recovery/executor.go
backend/internal/backupasset/recovery/result_lifecycle.go
backend/internal/backupasset/recovery/result_lifecycle_test.go
backend/internal/backupasset/recovery/delivery.go
backend/internal/backupasset/recovery/delivery_test.go
backend/internal/backupasset/recovery/metrics.go
backend/internal/backupasset/recovery/metrics_test.go
backend/internal/backupasset/recovery/behavior_integration_test.go
backend/internal/backupasset/recovery/testutil_test.go
backend/internal/backupasset/recovery/testdata/rsync_local_to_remote.json
backend/internal/backupasset/recovery/testdata/restic_exact_snapshot.json
backend/internal/backupasset/recovery/testdata/rclone_committed_prefix.json
backend/internal/backupasset/recovery/testdata/target_preflight.json

backend/internal/backupasset/provider/restore.go
backend/internal/backupasset/provider/restore_test.go
backend/internal/backupasset/provider/rsync_restore.go
backend/internal/backupasset/provider/rsync_restore_test.go
backend/internal/backupasset/provider/restic_restore.go
backend/internal/backupasset/provider/restic_restore_test.go
backend/internal/backupasset/provider/rclone_restore.go
backend/internal/backupasset/provider/rclone_restore_test.go

backend/internal/backupasset/runtime/recovery_runtime.go
backend/internal/backupasset/runtime/recovery_runtime_test.go

backend/internal/api/handlers/backup_recovery_handler.go
backend/internal/api/handlers/backup_recovery_handler_test.go

web/src/lib/api/backup-recovery-api.ts
web/src/lib/api/backup-recovery-api.test.ts
web/src/features/backup-assets/use-backup-recovery.ts
web/src/features/backup-assets/use-backup-recovery.test.tsx
web/src/features/backup-assets/recovery-plan-wizard.tsx
web/src/features/backup-assets/recovery-plan-wizard.test.tsx
web/src/features/backup-assets/recovery-impact-panel.tsx
web/src/features/backup-assets/recovery-impact-panel.test.tsx
web/src/features/backup-assets/recovery-job-panel.tsx
web/src/features/backup-assets/recovery-job-panel.test.tsx
```

### 2.3 Future Modify Manifest (81 Paths)

```text
.github/workflows/ci.yml
.trellis/spec/backend/database-guidelines.md

backend/cmd/server/main.go
backend/cmd/server/main_test.go

backend/internal/api/router.go
backend/internal/api/router_test.go
backend/internal/api/backup_asset_rbac_test.go
backend/internal/api/docs/docs.go
backend/internal/api/handlers/backup_content_handler.go
backend/internal/api/handlers/backup_content_handler_test.go
backend/internal/api/handlers/config_handler_test.go
backend/internal/api/handlers/settings_handler.go
backend/internal/api/handlers/settings_transition_test.go

backend/internal/backupasset/publication/contracts.go
backend/internal/backupasset/publication/contracts_test.go
backend/internal/backupasset/provider/catalog.go
backend/internal/backupasset/provider/contracts.go
backend/internal/backupasset/provider/contracts_test.go
backend/internal/backupasset/provider/registry.go
backend/internal/backupasset/provider/registry_test.go
backend/internal/backupasset/provider/rsync.go
backend/internal/backupasset/provider/rsync_test.go

backend/internal/backupasset/content/contracts.go
backend/internal/backupasset/content/contracts_test.go
backend/internal/backupasset/content/broker.go
backend/internal/backupasset/content/broker_test.go
backend/internal/backupasset/content/ticket.go
backend/internal/backupasset/content/ticket_test.go
backend/internal/backupasset/content/audit.go
backend/internal/backupasset/content/audit_test.go
backend/internal/backupasset/content/reconciler.go
backend/internal/backupasset/content/reconciler_test.go
backend/internal/backupasset/content/behavior_integration_test.go

backend/internal/backupasset/runtime/admission.go
backend/internal/backupasset/runtime/admission_test.go
backend/internal/backupasset/runtime/runtime.go
backend/internal/backupasset/runtime/runtime_test.go

backend/internal/backupasset/service.go
backend/internal/backupasset/service_test.go
backend/internal/backupasset/repository/query.go
backend/internal/backupasset/repository/query_test.go
backend/internal/backupasset/repository/testutil_test.go
backend/internal/fileaccess/contracts.go
backend/internal/fileaccess/local_linux.go
backend/internal/fileaccess/local_other.go
backend/internal/fileaccess/local_test.go
backend/internal/settings/service.go
backend/internal/settings/service_test.go
backend/internal/model/task.go
backend/internal/database/backup_asset_migrations_integration_test.go
backend/internal/sshutil/node_dialer.go
backend/internal/sshutil/node_dialer_test.go
backend/internal/sshutil/scope.go
backend/internal/sshutil/scope_test.go
backend/internal/task/manager.go
backend/internal/task/manager_test.go
backend/internal/task/runner.go

web/src/lib/api/core.ts
web/src/lib/api/client.test.ts
web/src/pages/__tests__/backups-page.a11y.test.tsx
web/src/pages/backups-page.data.tsx
web/src/pages/backups-page.recovery.tsx
web/src/pages/backups-page.test.tsx
web/src/features/backup-assets/asset-bulk-bar.tsx
web/src/features/backup-assets/asset-bulk-bar.test.tsx
web/src/features/backup-assets/asset-browser.tsx
web/src/features/backup-assets/asset-browser.test.tsx
web/src/features/backup-assets/asset-inspector.tsx
web/src/features/backup-assets/asset-inspector.test.tsx
web/src/features/backup-assets/backup-assets-workspace.tsx
web/src/features/backup-assets/backup-assets-workspace.test.tsx
web/src/features/backup-assets/backup-assets-route-state.ts
web/src/features/backup-assets/backup-assets-route-state.test.ts
web/src/features/backup-assets/backup-assets-state.ts
web/src/features/backup-assets/backup-assets-state.test.ts
web/src/features/backup-assets/__tests__/handlers.ts
web/src/features/backup-assets/__tests__/test-utils.tsx
web/src/lib/api/__fixtures__/backup-assets.fixture.json
web/src/types/domain.ts
web/src/i18n/locales/zh.ts
web/src/i18n/locales/en.ts
```

### 2.4 Task 4 B1 Boundary Amendment (2026-07-30)

This authorized controller scope correction replaces the unsafe Provider-local
Rsync issuer. It is needed because an arbitrary caller could forge a raw source
locator and because validation followed by reconstructing a path leaves a
root/source time-of-check-to-time-of-use window before each restore phase.

No create or current-planning path changes. No path is removed. The eight added
tracked modify paths are:

```text
backend/internal/backupasset/provider/rsync.go
backend/internal/backupasset/provider/rsync_test.go
backend/internal/backupasset/repository/query.go
backend/internal/backupasset/repository/query_test.go
backend/internal/fileaccess/contracts.go
backend/internal/fileaccess/local_linux.go
backend/internal/fileaccess/local_other.go
backend/internal/fileaccess/local_test.go
```

The prior amended total was exactly 9 current + 55 create + 80 modify = 144 unique,
disjoint paths. `provider/rsync.go` and its test retain the portable
Provider-side source/stream contract and prove no raw source reaches a runner.
`repository/query.go` and its test own the concrete managed-Rsync descriptor
resolver and `RestorePort` implementation. The four `fileaccess` paths own the
cross-platform pinned strict no-follow tree capability and its Linux descriptor
implementation/tests. No migration, model/table, `000070`, `000070+`,
`repository/binding.go`, or `repository/rsync_publication_execution.go` is
added to this correction.

### 2.5 Task 4 B1 Catalog Completion Blocker Amendment (2026-07-30)

Read-only inspection proved one deterministic precondition blocker after the
corrected B1 boundary work. `provider/catalog.go` constructs generic
`CatalogRecord` values in `catalogReadSession.acceptEntry` without assigning
`FingerprintStrength`. The committed managed-Rsync adapter uses that generic
session. Repository's `sealedCatalogReadSession` only encrypts and clears the
Provider locator, while `catalog.Indexer` calls `ParseFingerprintStrength` and
rejects the resulting empty value. A real immutable managed-Rsync Catalog
therefore cannot become complete.

The minimal tracked-existing manifest delta is exactly one production path:

```text
backend/internal/backupasset/provider/catalog.go
```

The genuine RED uses the real committed managed-Rsync fixture already present
in `backend/internal/backupasset/provider/rsync_test.go`; the cross-layer
Repository→Catalog completion regression uses the real managed-tree fixture in
the already manifested
`backend/internal/backupasset/repository/query_test.go`. Both test paths were
already in §2.3, so they do not change the count. Read-only inspection also
covered `provider/catalog_contract_test.go`, `repository/catalog_read_test.go`,
and `catalog/indexer_test.go`; none needs modification for the minimal fix, and
their existing closed-contract packages remain required verification gates.

The minimal GREEN belongs only in Provider: when the generic `Entry` contract
has no proved fingerprint, emit an empty fingerprint with closed
`FingerprintStrength="none"`. Do not teach Repository or Catalog to accept or
default an empty strength, do not add fingerprint evidence, and do not change
projection digest ordering. The current exact union is 9 current + 55 create +
81 modify = 145 unique, disjoint paths. No create path, migration, model/table,
`000070+`, or Task 5--10 path is added. At this dated amendment, it had not
executed the RED, GREEN, product tests, implementation, or independent reviews;
Task 4 was still open.

At amendment validation, the actual 61-path dirty union contains 59 manifest
paths plus exactly two unrelated paths: `go.mod` and the root-level
`recovery/testdata/rsync_local_to_remote.json`. The supplied similarly named
path under `backend/internal/backupasset/recovery/testdata` is absent and remains
an unexecuted future-create manifest path. Preserve both actual unrelated paths;
do not move, delete, stage or admit them to this Child's scope.

### 2.5a Task 5 Authorization-Receipt Amendment (2026-07-31)

Two independent read-only reviews found Task 5 was not implementation-ready:
generic step-up only validates a JTI, the four authorization mutations have no
durable idempotency ledger, ordinary audit projection is post-commit/purgeable,
and encrypted reasons are absent from a unique full-intent product. The
controller approves the stronger evidence-row receipt design under the user's
standing authorization.

This is a planning-only amendment. It does not execute a RED/GREEN, migrate,
edit a product/test path, change a task state, or create implementation evidence.
It adds no path to §§2.1--2.3: the paired `000069` files, model, recovery
contracts/service/tests, database integration test and recovery handler/test are
already in the exact manifest. It adds no Recovery table, `000070+`, generic
step-up/credential-grant/audit handler change, or route. The invariant remains
exactly 9 current + 55 create + 81 modify = 145 unique paths; Tasks 1--4 remain
`complete_approved` and Tasks 5--10 remain `not_executed`.

### 2.5b Task 6 Restart-Adoption Persistence Amendment (2026-07-31)

The controller approves the later controlling Task 6 persistence design in
`design.md` §19, and the final independent result is `SPEC APPROVED`. This is a
planning-only amendment. It adds no path to §§2.1--2.3, changes no create/modify
classification, allocates no table or migration, and does not edit product,
test, model or migration files. The exact union remains 9 current + 55 create +
81 modify = 145 unique, disjoint paths. Only the existing unshipped paired
`000069` may later change; `000070+`, backfill, a new table and a new path are
forbidden.

Review chronology is immutable: initial independent review 3 Critical + 2
Important; first revision rejected for inventing a delete absence digest;
corrected controlling revision reviewed with 2 Important for skip/source
identity conflation and insert-before-grant ordering; both corrections adopted;
final result `SPEC APPROVED`. The nonblocking clarification freezes skip's
separate prior-target bytes as immutable and holds the exact key/version lock
transactionally through grant CAS plus complete aggregate insert.

The amendment freezes the first thirteen product corrections covering expected-post
identity, create/overwrite source equality, skip target facts, explicit delete
absence, closed Verify unions, absence chain binding, canonical item locators,
separate semantic/final-object digests, preallocated workspace identity,
recovery-local row-bound locator encryption, the encrypted preflight snapshot,
internal-only adoption, key-loss/grant-first prepared-aggregate creation and
paired immutable enforcement. Their count remained thirteen at this review point. The later
2026-08-01 evidence-backed
clarification returned by the independent fidelity researcher is approved by
controller direction as a focused planning amendment: Task 6 fidelity is exact
lowercase SHA-256 content identity plus byte count only, with no metadata,
independent fidelity digest or synthetic absence digest. That dated planning
amendment itself supplied no RED/GREEN. Current bounded status is B1-E1/E2/E3,
B2-E1/E2 and original-review Task-6-owned F4 `complete_checked`; B1 and B2
aggregates remain partial, while unchecked items and whole gates/reviews remain
open. F3/F6 are closed only at their focused scopes. The
original review contains F1--F8 only. Tasks 7--10 remain `not_executed`.

### 2.5c Task 6 Locator Contract Repair (2026-08-01)

Independent read-only design research returned `DESIGN READY`. Controller
direction approves this coherent focused clarification inside the then-existing
first thirteen corrections and already approved exact manifest. It refines corrections 7--10 and 12--13 in
`design.md` §19; correction 11's three-boundary adoption flow is unchanged
integration context, not part of this locator-contract repair. It does not itself add
the fourteenth correction later authorized in §2.5d, product scope, path, table,
route, migration, backfill or `000070+`. Task 6 stays `in_progress`; this artifact-only repair is neither
RED/GREEN evidence nor implementation completion.

The controlling implementation order is now:

1. outside the execute transaction, look up replay, decode and validate the
   canonical snapshot/whole operation product, resolve associations, preallocate
   job/item IDs and isolated `jobs/<opaque>` identity, select one explicit Active
   cleanup key, calculate separate semantic/final/workspace digests and complete
   every generic/recovery-local AEAD encryption into an immutable prepared
   aggregate;
2. inside one transaction, recheck replay/proof; lock plan, preflight/items,
   grant, exact cleanup-key row, source/Catalog and existing source/node/attempt
   resources; recompute and require byte-for-byte prepared-aggregate equality;
   require `LockActiveTx` to match the selected key; make grant CAS the first
   effect mutation; insert the complete job/items/source lease/node lease/
   attempt/plan transition/receipt; and commit once, with no encryption,
   Provider/SSH/target I/O, audit or remote reservation inside;
3. insert isolated jobs at `workspace_phase=none` with preallocated generic-
   encrypted workspace locator plus immutable `WorkspaceBindingDigest`, but no
   marker/owner/fence/deadline. `PrepareFirstWrite` must reuse that exact identity
   for `none -> reserved`; in-place none has no workspace fields, and an
   unexpected remote directory fails closed without rename/reallocation;
4. persist canonical `target_relative_locator` for every schema-v2 row,
   including delete, plus `SemanticTargetDigest`. Persist on every item a
   distinct `TargetObjectDigest`, recovery-local locator AEAD, positive explicit
   key/cipher versions and the complete operation facts. The item column bypasses
   generic model encryption hooks; generic encryption remains for
   `EncryptedOperationRows` and the job workspace locator. Existing generic
   `enc:v2` names only the encrypted preflight snapshot, not item AEAD; and
5. make adoption exactly `AdoptInterruptedOperation(ctx, claim, jobItemID)` with
   a short DB load/lock+decrypt+validation phase, target I/O with no DB
   transaction held, and a final re-lock/fenced-CAS phase. A worker constructs
   `TargetObjectRef`/permit only after locking the exact persisted workspace/item,
   decrypting, strict-joining and recomputing the final digest.

`TargetLocatorEnvelopeBinding` length-frames codec/cipher versions; job/item/
plan/nullable plan-item/source-row identities; mode/node/root/root digest;
workspace identity and binding digest; separate semantic/final-object digests;
operation presence/digest/byte facts; and explicit key/cipher versions. Its
plaintext carries the exact canonical item and workspace locators. Cross-row,
job, root, item, workspace, key, cipher or operation-product substitution fails
before target I/O. `TargetObjectRef.TargetPathDigest` carries only the final
`TargetObjectDigest`.

The paired `000069` contract must enforce isolated-none versus reserved+
workspace products, in-place-empty workspace, frozen job/item identities, both
digests, ciphertext/versions, the operation matrix, per-job semantic/final
uniqueness, insert/delete-mode facts and terminal immutability. SQL cannot
authenticate ciphertext, so every service/worker load reconstructs the complete
item set before I/O. Preparation failure leaves no state; transaction failure
rolls back grant plus aggregate; a crash after commit leaves one complete
unreserved aggregate. Cleanup-key reconciliation remains the existing pre-arm-
unchanged/current-post-arm-`needs_attention|cleanup_due` DB-only contract.

### 2.5d Task 6 Unresolved Remote Outcome Correction (2026-08-01)

The focused fourteenth Task 6 product correction is now B3
`PROVED_COMPLETE_FOCUSED_ONLY`. Its independent specification receipt
`019fc0c2-cfda-74e3-b218-246f3a425545` returned `APPROVED` and closed both prior
Important test-evidence findings. This does not give B1/B2, whole Task 6, or
Tasks 7--10 any implementation/review credit; Task 6 remains `in_progress`,
Tasks 7--10 remain `not_executed`, and staged paths remain zero.

After mutation arm, invalid or contradictory write/verification outcomes must
append terminal phase `operation_unresolved` and project the stable failure
category `remote_outcome_unresolved`. The unresolved category is closed to
`revision_disagreement|verification_mismatch|write_result_invalid|observation_invalid`.
The checkpoint facts are exactly `job_item_id`, `unresolved_category`,
`write_result_digest`, `write_target_revision`, `observation_digest`,
`observed_target_revision`, `observed_presence` (`''|present|absent`) and
`source_revalidation_outcome` (`matched|drifted|failed`). Every older phase
requires neutral empty values for these facts.

Category field legality is closed: `revision_disagreement` requires valid write
and observation digests/revisions with unequal revisions;
`verification_mismatch` requires a valid observation, neutral write fields for
`skip`, and a valid write digest/revision for `create|overwrite`, without
changing the existing B2 delete contract; `write_result_invalid` persists only
the sanitized length-framed invalid-result digest with no structured write
revision or observation facts; and `observation_invalid` retains valid write
facts when applicable but persists only the sanitized length-framed invalid-
observation digest with structured observation facts neutral. Source drift or
failure may coexist through `source_revalidation_outcome` without replacing the
remote-outcome category.

The unresolved checkpoint binds the current job/item/canonical operation/prior
target revision, attempt/node/source/preflight/applicable-authority fences and
sanitized length-framed remote facts. It has empty `next_target_revision`, may
be sequence 1 or follow applicable `workspace_reserved`, earlier-operation, or
`delete_authority_consumed` history, is terminal and never advances the target
chain. No checkpoint for the job may follow, and the current item receives no
success/skipped/adoption checkpoint.
One short fenced transaction must append exactly one such checkpoint, mark the
current item failed, mark the job
`needs_attention/remote_outcome_unresolved`, write sanitized failure evidence,
close the attempt, release source/node leases, and preserve every existing
success/skipped/job-success/chain field. Failure evidence cross-binds the new
checkpoint, job, item, attempt and node fences. Any guard or write failure rolls
back the whole disposition, and no continued remote write is allowed.

`WriteResultDigest` and `ObservationDigest` are private, domain-separated,
length-framed sanitized evidence bindings. They are not fidelity digests,
absence digests, trusted revisions, plaintext/raw result serialization, or raw
error storage. This correction amends only the two existing SQLite/PostgreSQL
`000069_backup_asset_recovery.up.sql` files; both down migrations remain
unchanged. Other work stays inside the already manifested model/state/executor/
worker and test paths. It adds no path, table, migration, backfill, `000070`,
target/contracts interface, keyring domain, or crypto primitive. The exact manifest
remains 9 current + 55 create + 81 modify = 145.

The exact RED contract consists of:

```text
TestStateOperationUnresolvedProductsAreClosedAndTerminal
TestBackupAssetRecoveryCheckpointCarriesPrivateUnresolvedOutcomeProduct
TestRecoveryExecuteClaimProjectsUnresolvedRemoteOutcomeMatrix
TestBackupAssetMigration069UnresolvedOperationOutcomeSQLite
TestBackupAssetMigration069UnresolvedOperationOutcomePostgres
```

The SQLite migration command must also name the existing
`TestBackupAssetMigration069SQLite` legality helper and
`TestBackupAssetMigration069PairedFiles` parity helper. The required real-
PostgreSQL command must also name the existing
`TestBackupAssetMigration069Postgres` helper. These are gate companions, not
additional counted correction selectors.

The completed B3 chronology froze those selectors after independent approval,
removed only the uncredited fourteenth behavior to the authorized pre-feature
baseline, observed genuine RED locally/SQLite and on required real PostgreSQL,
then restored/fixed the product and reran the unchanged selectors for GREEN.
Current inherited GREEN was not relabeled as RED.

The sole bounded final writer changed only
`backend/internal/backupasset/recovery/executor_test.go` and
`backend/internal/database/backup_asset_migrations_integration_test.go`.
Required real PostgreSQL `000069` plus its six-case behavior matrix passed with
no skip; the bounded cancellation set, focused race, affected exact-mirror
regressions, vet, owned gofmt, diff, manifest and staged-zero gates passed, and
resources were cleaned. Controller-inline code-quality review found no issue.
A local reviewer rerun could not link because of host disk quota and is not
claimed as pass or fail.

### 2.5e Post-B3 Task 6 Batch And Ownership Reconciliation (2026-08-02)

| Batch | Exact correction scope | Status |
|---|---|---|
| B1 | ordinary/foundation Corrections 1--3, 5 and 7--13 | B1-E1/E2/E3 `complete_checked`; aggregate remains partial |
| B2 | exact-mirror/multi-delete Corrections 4 and 6 plus its delete row | B2-E1/E2 `complete_checked`; aggregate remains partial pending combined/whole evidence |
| B3 | Correction 14 unresolved remote outcome | `PROVED_COMPLETE` at focused scope only |

Task 6 owns preallocated workspace/reservation, deadline and cleanup-only
classification plus bounded restart adoption/reconciliation semantics. Task 7
owns publication, Content revalidation, `revoking` takeover, cleanup node-lease
behavior and `RecoveryResultRef` denial. Task 8 owns startup/listener ordering
and managed lifecycle. Tasks 7--10 remain `not_executed`.

F6 is `complete_approved` and F3, B2-E1/E2 and Task-6-owned F4 are
`complete_checked` at their focused scopes only. The remaining sequence is
exact: obtain whole Task 6 specification review; obtain whole Task 6 quality
review; run every final gate; then begin Task 7. The remaining review ledger is
combined B1/B2 and unchecked execution items, plus whole gates/reviews. Design Corrections
5--9 are not review findings, and no Finding 9 is created.

### 2.5f R65 Foundation Audit Contract Exception (2026-08-09)

R65 exposed a genuine cross-boundary contract conflict: the frozen Recovery
reconciliation product must write the existing
`AuditActionRecoveryCleanup` with `AuditFieldOperation="recovery_reconcile"`,
while the Foundation validator previously rejected every operation field for
that action. The user explicitly approved the smallest correction after the
RED. This is a task-local execution exception, not a new Recovery action,
field, route, model, migration or product-manifest row.

The independently allow-listed exception paths are exactly:

```text
backend/internal/backupasset/audit_action.go
backend/internal/backupasset/audit_action_test.go
```

The production change admits only the exact cleanup/reconciliation pair. The
test keeps the negative matrix for another cleanup operation, a non-string
value and reuse by another action. The base product manifest remains exactly
9 current + 55 create + 81 modify = 145 unique/disjoint paths; these two
Foundation-owned paths are reported separately as `approved_exception` by the
structural gate. No other path outside the base manifest or this two-path
exception is authorized.

### 2.5g Task 8 Current-Baseline Reconciliation (2026-08-12)

Tasks 1--7 were delivered through PR #410 before Task 8 began. The dated
Phase-1 create/modify classification remains immutable planning history, but
it is not a current-HEAD assertion. At `987ebc9f94c6a5230e1ab27bf89eaa9d5c150c4c`,
36 of the 55 historical create paths are already tracked and 19 remain absent.
This is expected delivery progress, not duplicate creation or scope growth.

For Task 8, the already tracked Recovery service, runtime owner and handler
paths are modifications. The only Task-8 product paths still genuinely created
are `backend/internal/backupasset/recovery/metrics.go` and
`backend/internal/backupasset/recovery/metrics_test.go`. The other absent
historical create paths remain reserved for later approved Tasks 9--10 or are
unused fixtures/adapters and receive no Task 8 credit.

The Task 8 structural gate compares changed product paths with the same
`9 + 55 + 81 = 145` historical union by membership; it must not require old
create paths to remain absent. It still reports changed paths outside that
union and the existing R65 exception separately. These research paths are
task-local, non-product evidence only:

```text
.trellis/tasks/07-28-backup-assets-controlled-recovery/research/task8-runtime-settings-metrics.md
.trellis/tasks/07-28-backup-assets-controlled-recovery/research/task8-production-authorities.md
.trellis/tasks/07-28-backup-assets-controlled-recovery/research/task8-production-authorities-audit.md
```

They do not alter the product manifest or authorize Tasks 9--10. The authority
audit is dated discovery evidence; later Task 8 fixes supersede its then-open
disabled-reconciliation and publication-shell findings, while its production
authority ownership gaps remain controlling at the checkpoint below.

### 2.6 Explicitly Unchanged Without An Amendment

- `backend/internal/backupasset/lease.go` and `lease_test.go`: reuse the existing
  `recovery_job` holder and Tx/fence contract.
- `backend/internal/backupasset/authorization.go`,
  `backend/internal/auth/step_up_action.go`, `backend/internal/api/handlers/step_up.go`,
  `backend/internal/api/handlers/credential_access_grant.go`, generic audit
  handlers/writer, and recovery audit-action registry: consume the already
  registered permission/actions and sanitized projection seam; except for the
  exact Foundation exception recorded in §2.5f, do not add free-form
  alternatives or alter their generic semantics.
- existing 000062-000068 migration files and every 000070+ path.
- existing Provider read/publication implementations except the manifest-listed
  Catalog projection, contracts/registry/managed-Rsync modify paths and eight
  restore create paths.
- Auth logout handler semantics, public deployment files, release workflows,
  package manifests, README/public docs, Child 14 retention/reconnect paths and
  Child 15 GA/legacy-removal paths.
- parent PRD/design/implement artifacts: Child 13 may update only the already
  registered parent `task.json`; final parent acceptance remains later.

### 2.7 Workflow-Owned Completion Paths

After the feature PR merges and post-merge automation is understood, the
controller uses a dedicated bookkeeping branch. `task.py archive` and the
journal helper may generate only the deterministic Child archive plus the
actual changed workspace index/journal paths:

```text
.trellis/tasks/archive/2026-07/07-28-backup-assets-controlled-recovery/
.trellis/workspace/weibo/index.md
.trellis/workspace/weibo/journal-1.md
```

These are not product-manifest paths and must never be generated before merge.
Inspect actual output, stage only actual generated paths, deliver them through a
second PR, and leave the parent `planning`.

## 3. Ordered TDD And Implementation Tasks

Every task follows RED → observed expected failure → minimal GREEN →
focused normal/race verification → controller review. Record commands and
results in `research/implementation-evidence.md`. Test times derive from an
injected clock or one test-start instant; fixed calendar expiries are forbidden.
Subagents never stage or commit. The controller may create coherent work commits
only after the relevant review checkpoint is green.

### 3.1 Immutable Security/State Review Closure Ownership

The note `research/child13-security-state-review.md` remains immutable evidence,
not approval. Each finding below owns named tests that must be added and observed
failing before the listed GREEN implementation. The same exact selector must
then pass. Renaming or deleting these tests requires a written plan amendment;
no later broad aggregate can substitute for the focused RED observation.

| Finding | Owning RED batch/files | Owning GREEN batch |
|---|---|---|
| F1 target mode/destructive authority | Task 1 contracts/state/model/migration tests + Task 5 service/handler/API tests | Tasks 1, 5 and 6 closed products, issue/consume transactions and worker barrier; Task 9 routes |
| F2 malware/security decision | Task 3 preflight tests + Task 5 service/handler/audit tests + Task 10 mapper/wizard tests | Tasks 3, 5, 9 and 10 closed decision/override flow |
| F3 pre-first-write drift | Task 1 state/unique-job constraints + Task 5 replay tests + Task 6 two-worker/crash behavior tests | Task 6 guarded cross-state transaction and lease release on both engines |
| F4 unpublished plaintext/revoking takeover | Task 1 model/state constraints + Task 6 workspace/deadline/cleanup-only tests + Task 7 lifecycle/Content crash tests | Task 6 preallocation/reservation/deadline/cleanup-only classification; Task 7 publication, Content revalidation, revoking takeover and cleanup node lease |
| F5 cleanup node writer lease | Task 3 admission/Task writer tests + Task 7 cleanup race/fairness tests | Tasks 3 and 7 shared node lease claim/renew/release sequence |
| F6 permanent 000069 use latch | Task 1 paired migration/model/down tests + Task 6 live mutation-permit crash/revocation tests | Tasks 1 and 6 distinguished immutable evidence row and live permit checks before CreateOwnedJobDir/CreateDirectory/WriteAtomic/Delete; RemoveOwnedJobDir remains Task 7 |
| F7 downgrade/mixed versions | Task 8 runtime/settings/config transition tests + Task 9 route tests + Task 10 compatibility mapping | Tasks 8–10 disable/readiness gate and closed mixed-version UX |
| F8 locator encryption/no leak | Task 1 model/migration tests + Tasks 2–5 substitution/boundary tests + Tasks 9–10 API/Swagger/frontend tests | Tasks 1–5 and 9–10 encrypted `json:"-"` fields, domain digests and boundary-only decrypt |

Required focused backend selectors, first RED and later GREEN:

```bash
cd backend

go test ./internal/model ./internal/backupasset/recovery ./internal/api ./internal/api/handlers \
  -run '^(TestRecoveryReviewF1TargetModeAndOperationDigests|TestRecoveryReviewF1WriteAuthorityOneUse|TestRecoveryReviewF1ExactMirrorDeleteAuthority|TestRecoveryReviewF1InPlaceResultRefDenied|TestRecoveryReviewF1AuthorityRoutes)$' \
  -count=1

go test ./internal/backupasset/recovery ./internal/api/handlers \
  -run '^(TestRecoveryReviewF2SecurityDecisionMatrix|TestRecoveryReviewF2AdminOverrideBinding|TestRecoveryReviewF2OverrideAuditNoLeak)$' \
  -count=1

go test ./internal/backupasset/recovery \
  -run '^(TestRecoveryReviewF3PreWriteDriftTransactionSQLite|TestRecoveryReviewF3ExecuteReplayAfterDrift|TestRecoveryReviewF3TwoWorkerAndCrashBarriers)$' \
  -count=1

go test ./internal/backupasset/recovery ./internal/backupasset/content \
  -run '^(TestRecoveryReviewF4WorkspaceDeadlineAndPublication|TestRecoveryReviewF4PartialWorkspaceCleanupOnly|TestRecoveryReviewF4RevokingCrashTakeover|TestRecoveryReviewF4ContentRevalidatesPublishBarrier)$' \
  -count=1

go test ./internal/backupasset/recovery ./internal/backupasset/runtime ./internal/task \
  -run '^(TestRecoveryReviewF5CleanupNodeLeaseRaces|TestRecoveryReviewF5CleanupLeaseLossDuringDelete|TestRecoveryReviewF5BusyNodeFairness|TestRecoveryReviewF5OrdinaryWriterExclusion)$' \
  -count=1

go test ./internal/database ./internal/model ./internal/backupasset/recovery \
  -run '^(TestRecoveryReviewF6UseLatchSQLite|TestRecoveryReviewF6LatchBeforeTargetMutation|TestRecoveryReviewF6UsedDownAtomicRefusal)$' \
  -count=1

go test ./internal/backupasset/runtime ./internal/api ./internal/api/handlers \
  -run '^(TestRecoveryReviewF7FeatureDisableKeepsCleanup|TestRecoveryReviewF7DowngradeReadiness|TestRecoveryReviewF7ForwardFixOnlyAfterUse|TestRecoveryReviewF7MixedVersionRoutes)$' \
  -count=1

go test ./internal/model ./internal/backupasset/recovery ./internal/backupasset/provider ./internal/api ./internal/api/handlers \
  -run '^(TestRecoveryReviewF8LocatorCiphertextAtRest|TestRecoveryReviewF8SourceLocatorSubstitution|TestRecoveryReviewF8TargetRootSubstitution|TestRecoveryReviewF8LocatorNoLeak)$' \
  -count=1
```

The real-PostgreSQL companions are required, not optional:

```bash
cd backend
REQUIRE_POSTGRES_RECOVERY_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" \
  go test ./internal/backupasset/recovery \
  -run '^(TestRecoveryReviewF3PreWriteDriftTransactionPostgres|TestRecoveryReviewF4RevokingCrashTakeoverPostgres|TestRecoveryReviewF5CleanupNodeLeaseRacesPostgres)$' \
  -count=1
REQUIRE_POSTGRES_MIGRATION_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" \
  go test ./internal/database \
  -run '^TestRecoveryReviewF6UseLatchPostgres$' -count=1
```

Frontend review selectors are likewise named and focused:

```bash
cd web
env -u NODE_ENV npm run test -- \
  src/lib/api/backup-recovery-api.test.ts \
  src/features/backup-assets/recovery-plan-wizard.test.tsx \
  src/features/backup-assets/recovery-job-panel.test.tsx \
  src/pages/__tests__/backups-page.a11y.test.tsx \
  -t 'RecoveryReviewF(1|2|4|7|8)'
```

### Task 0: Authorized Start And Spec Reload

**Files:** Phase 1 paths only.

**R0 ledger disposition:** `complete_approved` as the historical activation
gate. Checking these rows reconciles already executed setup and reload evidence;
it is not a second `task.py start` or a claim that later package reloads are
unnecessary.

- [x] Run §1.2 and capture immutable output hashes.
- [x] Load every file in `implement.jsonl`, plus package-local `AGENTS.md` files
  before touching handlers or pages.
- [x] Load `trellis-before-dev`, `superpowers:test-driven-development` and the
  Phase 2.1 workflow detail.
- [x] Run `task.py start` once and record Child=`in_progress`, parent=`planning`.

### Task 1: Paired 000069 Aggregate And Closed States

**Controller execution status (2026-07-29):** `complete_approved`.
The paired `000069`/model/contracts implementation exists and has undergone
multiple focused remediation cycles. The latest independent Task 1
specification review found four technical Important gaps: Content CHECK
NULL/proof/down parity, consumed-grant binding immutability, the absent exact
PostgreSQL F6 selector, and the rejected-down mutation-arm trigger/function
snapshot. Focused corrective work observed RED before GREEN for those findings;
recorded focused evidence covers full SQLite 000069, paired files, the database
package, model/recovery regressions, `go vet`, and real PostgreSQL 18
`TestBackupAssetMigration069Postgres` plus
`TestRecoveryReviewF6UseLatchPostgres`, with the disposable service removed.

The original Task 1 model/state/contracts implementation did not observe RED
before GREEN. The controller accepts this irreversible historical process
deviation under standing user authorization; it is not a passed TDD gate and no
RED is retroactively claimed. Every new fix and Tasks 2--10 require strict
observed RED-to-GREEN chronology. The final independent specification re-review
and a fresh live-worktree quality re-review both returned `APPROVED`. The quality
re-review confirmed that the older armed-attempt finding was stale against the
current paired migrations and cross-engine regression matrix. Task 1 is
complete; this focused evidence does not close the Child or any full gate.

**Files:** recovery model/test, four 000069 files, database integration test,
database guideline and recovery contracts/state files/tests.

- [x] **Write migration/model regression tests.** Assert twelve exact tables, required
  columns/FKs/indexes, closed target/security/authority/state/phase products,
  operation/delete digest lengths, exactly one safe source-revision arm,
  encrypted `json:"-"` locator/root/path/reason fields, one job per plan,
  cross-table plan/job/item parity, workspace/publication/deadline invariants,
  active-node-lease uniqueness and Content RecoveryResult FK/check activation.
- [x] **Write the exact use-latch regression matrix.** Require the fixed-ID
  `schema_use_latch` arm in `backup_asset_recovery_evidence`, ordinary-evidence
  job FK/latch-null exclusivity, paired immutable update/delete triggers and
  down's all-or-nothing guard. Cover pristine down, latch-only used down,
  purge-to-empty, crash immediately after latch commit and every used family on
  SQLite and real PostgreSQL; rejected down must preserve every table,
  constraint, trigger and schema version.
- [x] **Record the RED chronology truth.** The original model/state/contracts
  implementation did not observe RED before GREEN and is not retroactively
  claimed as TDD. Every focused review correction recorded its genuine RED
  before the corresponding GREEN.

```bash
cd backend
go test ./internal/database -run 'BackupAssetMigration069' -count=1
go test ./internal/model -run 'BackupAssetRecovery' -count=1
go test ./internal/backupasset/recovery -run 'State|Contract' -count=1
```

Historical expected result: fail because 000069/model/recovery package is
absent. That original RED was not observed; the accepted deviation is recorded
above and in `research/implementation-evidence.md`.

- [x] **Implement minimal schema GREEN.** Define exactly the twelve tables in
  `design.md` §4.5. Keep the distinguished latch row as the only latch mechanism;
  its service insert belongs to Task 6, but SQL/model contracts make it
  immutable and normal cleanup cannot target it. Do not create a thirteenth
  latch or delivery table.
- [x] **Prove paired parity.** Cover pristine down, every used family, atomic
  rejection without partial drops, model/UTC/CHECK/FK/index parity and existing
  000068 preservation on SQLite and real PostgreSQL.
- [x] **Update executable database knowledge.** Change the spec's migration head
  and parity language from 000068 to 000069 only after the implementation/tests
  prove it.

### Task 2: Exact Selection, Source Revision And Plan Idempotency

**Execution status (2026-07-29):** `complete_approved`. Task 1 independent
specification and live-worktree quality reviews closed the start gate. Findings
1--7 retain genuine RED-to-GREEN evidence. Finding 8 was an immediate-GREEN
coverage gap, so no fake RED or unnecessary production change was introduced.
Final controller-inline specification and quality passes found no open finding.

**Files:** recovery contracts/service/tests and publication contracts/tests.

- [x] **Write RED table-driven tests** for one Repository/RecoveryPoint,
  canonical explicit AssetRefs, directory expansion bounds, same generation,
  immutable RecoveryPoint + domain-separated locator digest + manifest,
  mutable fingerprint+generation+observed-at, selection digest, unknown/dual
  revision rejection and no `latest` arm. Seed a recognizable fake locator and
  prove authority structs/JSON never contain it.
- [x] **Write RED idempotency/concurrency tests.** Same requester/endpoint/key
  and intent returns one plan; a one-field intent change conflicts; concurrent
  callers elect one durable winner; failure at every transaction boundary leaves
  no partial items, preflight, authority or lease. Intent changes include target
  mode, operation/delete digest, security decision and authority category.
- [x] **Add the narrow source validator.** Ordinary and caller-Tx methods
  revalidate the frozen Repository/RecoveryPoint/Catalog tuple, decrypt only at
  the Repository boundary, recompute the domain digest and pass the exact value
  directly to Provider. Ciphertext/row/RecoveryPoint substitution fails before
  I/O; handler/frontend code sees no raw locator or locator-derived digest.
- [x] **Run GREEN and race repetition.**

```bash
cd backend
go test ./internal/backupasset/recovery ./internal/backupasset/publication \
  -run 'Selection|SourceRevision|Plan|Idempotency' -count=1
go test -race ./internal/backupasset/recovery \
  -run 'Plan.*Concurrent|Idempotency' -count=10
```

### Task 3: Read-Only Target Preflight And Node-Write Coordination

**Execution status (2026-07-30):**
`complete_approved` at Task 3 scope; it does not close Child/full gates.
All earlier Task 3 implementation rows and focused gates below were green. The
first independent specification review returned one Critical and three Important
findings; the two product defects have genuine RED-to-GREEN fixes, and the real
PostgreSQL 18.4 matrix is permanently covered by
`TestRecoveryBehaviorPostgres`. The second independent specification review
then found canceled-run resurrection and mutable task-node migration hiding a
live writer. Their atomic no-executor compensation and immutable writer-node
remediation are complete with observed RED-to-GREEN evidence. A follow-up
review found the PostgreSQL `/deadline` harness used a one-second wall-clock
before `commitEntered`; its test-first deterministic run-context seam is also
complete. The earlier independent specification and live-worktree quality
rereviews were superseded when a fresh bounded verifier found one Critical and
two Important findings. Their focused corrections, including the final legacy
early-cancel terminal-overwrite CAS, are now complete. A fresh independent
specification rereview returned `APPROVED`; the controller-inline quality
recheck passed the affected normal/race, SQLite, real PostgreSQL, static,
format, manifest, and staged-zero gates. The exact chronology is recorded once
in `research/implementation-evidence.md`. Task 4 B1 is `complete_approved`
after the focused Catalog correction and `SPEC APPROVED` / `QUALITY APPROVED`
receipts; the inherited-GREEN follow-up is not a new RED. Tasks 5--10 remain
`not_executed`.

Chronology correction: the original Task 3A/3B implementation did not preserve
an executed pre-GREEN RED. The following first three completed rows therefore
record regression coverage and delivered behavior, not observed original RED.
That irreversible `not_observed_before_green` deviation is not retroactively
relabeled as TDD. Task 3C, both node-write remediations, and the deterministic
deadline-seam remediation retain genuine RED-to-GREEN evidence; the permanent
PostgreSQL test was immediate-GREEN coverage and caused no production change.

**Files:** recovery preflight/target files/tests, runtime admission files/tests,
shared node dialer/tests, SSH scope files/tests, Task manager/runner and manager
tests, TaskRun model, paired 000069/database integration tests, Recovery behavior
integration test, and the target fixture.

- [x] **Add target matrix regression coverage.** Cover archived/offline/unauthorized nodes,
  wrong credential purpose, missing tool, source access, root realpath/device/
  mount/owner/mode, symlink components, overlap with Xirang/source roots,
  insufficient bytes/inodes, active node writers, existing targets, policy/
  malware findings and bounded create/overwrite/delete/skip impact.
- [x] **Add closed-plan product regression coverage.** Require `isolated|in_place`, canonical
  length-framed operation rows, independent delete-set digest, canonical empty
  delete digest outside `in_place+exact_mirror`, duplicate/unbounded rejection
  and safe opaque impact summaries.
- [x] **Add security-decision matrix regression coverage.** Clean is the only direct allow;
  every finding defaults to block; only known policy-marked categories accept an
  Admin override input; unknown or non-overridable categories have no override
  path. Finding/policy revision drift invalidates the decision.
- [x] **Prove eligibility policy A.** Any non-archived registered node that
  passes the complete matrix is eligible; the producing node is only the
  preferred UI default when it also passes, never a hard recovery restriction.
- [x] **Prove preflight is read-only.** Every target mutation port panics/fails
  the test if invoked; old/expired evidence and a revision drift before grant or
  write return a typed conflict without automatic refresh.
- [x] **Implement purpose-exact target sessions.** Add separate preflight,
  write, verify, result-read and cleanup SSH purposes. `TargetPort` exposes only
  the typed methods in `design.md`; no generic command or arbitrary path method.
  Reuse `sshutil.NodeDialer` for shared auth/host-key/session mechanics.
- [x] **Implement durable node-write exclusion.** Recovery and ordinary writes
  share admission, lock order and current fence; update Task manager/runner
  seams and races so legacy restore/ordinary writes cannot bypass the durable
  node lease. Preview/read activity remains bounded but compatible.
- [x] **Remediate canceled-run resurrection with a deterministic RED.** Pause
  each ordinary/legacy restore runner after its prior one-time observation,
  commit cancellation, then let Recovery acquire the same node boundary. The
  atomic compensation must preserve terminal outcomes, restore only pending or
  retrying states, and make zero executor/precheck calls. Minimal GREEN moves
  the exact `pending -> running` CAS into the coordinator's caller-owned
  node-boundary transaction and treats CAS 0/lease conflict as terminal
  no-executor.
- [x] **Freeze writer node identity in paired 000069.** Add immutable,
  migration-backfilled `task_runs.node_id_snapshot` in both engines and the
  tracked model field. Reservation writes it under the shared node lock;
  Recovery queries the snapshot instead of joining mutable `tasks.node_id`.
  Deterministically migrate the Task to another node while its run remains
  active and prove old-node Recovery admission still conflicts on SQLite and
  isolated-schema real PostgreSQL. Missing/changed snapshots fail closed, and
  `000070+` remains absent.
- [x] **Make the PostgreSQL deadline harness deterministic.** The external
  behavior harness was written first and initially failed to compile until the
  constructor-time `WithRunContextFactory` seam existed. The harness now closes
  `Done` and returns `context.DeadlineExceeded` only after `commitEntered`; a
  watchdog is diagnostic only, and neither `Cancel` nor `Shutdown` proxies the
  deadline outcome.
- [x] Run focused normal/race tests for preflight and admission.

### Task 4: Closed Restore Contracts And Repository-Owned Rsync Port

**Controller disposition (2026-07-31):** Task 4 is `complete_approved` at
focused task scope. The Provider missing-strength RED and the real Repository
immutable Rsync Catalog point changed RED were observed; the detached synthetic
factory/fingerprint rewrite was rejected; production uses authenticated
`request.SourceFingerprint`; and the real `Service.OpenCatalogRead` through
`catalog.Indexer` passed focused normal and race selectors. The independent
receipts are `SPEC APPROVED` and `QUALITY APPROVED`. The inherited-GREEN
follow-up is not a new RED. Broad Provider remains blocked by host `IFree=0`
and is not claimed as a package pass; this closes neither Child/full,
PostgreSQL, frontend, CI, PR, nor merge gates. The existing B1 rows remain the
governing contract and their dated pre-closure statements remain historical.

**R0 ledger reconciliation (2026-08-02):** all ten Task 4 rows are checked at
the already approved focused scope. This is bookkeeping against the existing
Task 4 implementation/evidence and independent receipts, not a rerun or new
completion claim. Broad Provider remains environment-blocked and the explicitly
deferred runtime-injection selector remains Task 8 ownership.

**Files:** existing Provider restore/contracts/registry files and tests;
`provider/catalog.go`, `provider/rsync.go`, `provider/rsync_test.go`, `repository/query.go`,
`repository/query_test.go`, and the four `fileaccess` paths added by §2.4;
already-manifested Recovery contracts/service tests, publication contracts/tests,
Repository test utility, and Task 8 recovery-runtime files/tests. Do not modify
`repository/binding.go` or `repository/rsync_publication_execution.go`.

- [x] **Observe the real managed-Rsync Catalog RED before editing
  `provider/catalog.go`.** In the existing
  `TestRsyncCommittedCatalogRevalidatesExactManifestAtFinalize` fixture, require
  every generic committed-tree record without a proved fingerprint to carry an
  empty fingerprint plus `FingerprintStrength="none"`. Add
  `TestManagedRsyncCatalogBuildCompletesWithFingerprintNone` to the already
  manifested Repository query test: use its real managed publication/tree
  fixture through Repository's sealing session and `catalog.Indexer`, require an
  active complete generation, and load the persisted entry with strength
  `none`. Run the exact two-test selector and preserve the pre-GREEN failure;
  do not substitute a synthetic `CatalogRecord` or a direct DB insert.
- [x] **Implement only the minimal Catalog GREEN.** Set the closed `none`
  strength in `catalogReadSession.acceptEntry` when the generic `Entry` carries
  no proved fingerprint. Keep `Fingerprint` empty, leave Repository's
  locator-only wrapper and Catalog's closed parser unchanged, and do not alter
  materialized Rclone strength mapping or Catalog projection field ordering.
- [x] **Close the focused Catalog correction and Task 4 review.** The unchanged
  selector, focused Provider/Catalog/Repository normal and race selectors,
  `go vet`, format/dependency/privacy/static scans, exact-manifest accounting,
  and staged-zero checks are recorded in the evidence ledger. Fresh independent
  receipts are `SPEC APPROVED` and `QUALITY APPROVED`. None of this focused
  evidence belongs to Tasks 5--10.

- [x] **Write RED portable-contract and registry tests.** Define a closed
  `provider.RsyncRestoreSourceRef` with exactly `PlanID`, plan-binding digest,
  repository ID, RecoveryPoint ID, catalog-generation ID, selection digest,
  source-revision digest, and manifest digest. It has no locator, root, task ID,
  marker, ciphertext, identity fact, target transport, or `json:"-"` private
  input. Empty/dual/unknown arms, an executor string, a forged ref, or a
  mismatched `RestorePort.ProviderKind()` must fail before a runner call. The
  registry must reject a registered port whose kind differs from its registration
  kind; it must not fall back to another provider. Provider also owns the
  `RsyncRestoreSourceResolver` interface, whose only source input is that ref
  and whose only output is an opaque bounded declared-entry capability.
- [x] **Write RED pinned-tree tests before adding an adapter.** Add the
  `fileaccess` strict pinned no-follow tree capability and test its Linux
  descriptor behavior for managed-root replacement, final-tree replacement and
  link replacement between validation and use. The capability holds the opened
  descriptor through the caller's bounded regular-file opens, uses
  `openat2`/`RESOLVE_BENEATH|RESOLVE_NO_MAGICLINKS|RESOLVE_NO_SYMLINKS|RESOLVE_NO_XDEV`,
  exposes no reconstructed root/path, and returns `ErrStrictUnavailable` on
  unsupported platforms. `provider/rsync_test.go` must prove the managed point
  passes the pinned handle, rather than a string path, to its consumer.
- [x] **Write RED Repository resolver tests.** `repository.Service` must derive
  the producing task only from the RecoveryPoint and use the existing
  `managedRsyncCommittedPointReadRequest` path. Immediately before every
  preflight, execute, verify and reconcile runner call, it locks and revalidates
  the durable plan/plan items, active complete catalog generation, exact selected
  entries, source revision, current point manifest, `ImmutableLocatorDigest`,
  committed managed-point semantics, root marker and pinned descriptor identity.
  `mutable_head`, legacy Rsync bindings, caller task IDs, caller roots and
  ciphertext/point/catalog/selection substitution reject with zero runner calls.
  Move the shared `ImmutableLocatorDigest` implementation to the already
  manifested `backupasset/publication/contracts.go`; Recovery and Repository use
  that one helper.
- [x] **Implement the corrected ownership boundary.** Provider retains only
  portable closed request/result/ref contracts, the kind-checked registry,
  typed sanitized errors, `RsyncRestoreSourceResolver`, and a narrow
  `RsyncTargetWriter` stream/writer contract. Repository implements the concrete
  Rsync `RestorePort` and Provider resolver interface;
  it opens the `fileaccess` capability, supplies only declared regular-entry
  streams to the writer, and retains the handle until the runner returns. Recovery
  creates only the scalar ref from its durable plan and removes
  `RsyncManagedSourceRoot`, `NewRsyncManagedSourceRoot`, and
  `RevalidateRsyncRestoreSource`; it never imports Repository or supplies a root
  or raw locator. The dependency direction is exactly
  `recovery -> provider`, `repository -> provider`, and
  `runtime -> repository + recovery + provider`; Provider imports neither
  Repository nor Recovery.
- [x] **Close each phase without path or error leakage.** The concrete port
  resolves/revalidates the ref again immediately before its runner call and
  validates root, marker, point identity and manifest again after it. A malformed
  ref/union or source identity drift returns a safe Provider source-drift
  sentinel that still satisfies `errors.Is(err, ErrInvalidRestoreRequest)`.
  Arbitrary resolver or runner failures become a separate typed unavailable
  sentinel; only context cancellation/deadline preserve their original identity.
  No runner error, stderr, marker, locator, source/root path, or raw remote path
  may reach Recovery, audit, logs or an API. If post-write source drift is found,
  Task 6 maps the typed source-drift result to `ErrRecoverySourceChanged` and the
  partial-write disposition; Task 4 does not relabel it as success.
- [x] **Keep target authority out of the test seam.** The current
  `RsyncBoundRemoteTarget` is not a usable transport and must not grow a raw
  remote path. Recovery's fenced `TargetPort` implements the narrow
  Provider-owned `RsyncTargetWriter`; Task 6/8 runtime composition injects it.
  A fake runner is a data-flow test seam only and cannot mint source or target
  authority.
- [x] **Run RED then GREEN evidence in this order.** First run the named tests
  below before production changes and record the observed failure. Implement only
  the boundary or Catalog correction needed to make each unchanged selector
  green, then run the same selector, the repeated race selector, and the wider
  Task 4 Provider/Catalog/Repository suites. The Task 8 runtime-injection
  selector remains pending until Task 8; it is not credited as a Task 4 pass.

```bash
cd backend
go test ./internal/backupasset/provider ./internal/backupasset/repository \
  -run '^(TestRsyncCommittedCatalogRevalidatesExactManifestAtFinalize|TestManagedRsyncCatalogBuildCompletesWithFingerprintNone)$' \
  -count=1
go test ./internal/fileaccess \
  -run '^(TestPinnedStrictTreeRejects(Root|FinalTree|Link)Swap|TestPinnedStrictTreeNeverReturnsPath)$' \
  -count=1
go test ./internal/backupasset/provider ./internal/backupasset/repository \
  ./internal/backupasset/recovery \
  -run '^(TestRestorePortKindMatchesRegistration|TestRsyncRestoreSourceRefHasNoPrivateSourceFields|TestRsyncRestorePortRevalidatesAllFourPhases|TestRsyncRestorePortRejectsSourceOrRootSwapBeforeRunner|TestRsyncRestorePortSanitizesResolverAndRunnerErrors|TestRsyncResolverDerivesProducingTaskOnly|TestRsyncResolverRejectsPlanSelectionCatalogRevisionAndCiphertextDrift|TestRecoveryCreatesRsyncScalarRefWithoutCallerRoot)$' \
  -count=1
go test ./internal/backupasset/catalog \
  -run '^(TestCatalogContractsClosedEnumsFailClosed|TestCatalogIndexerCompletesZeroEntryAndAtomicallySupersedes|TestCatalogIndexerProofMismatchPreservesOldActiveGeneration)$' \
  -count=1
go test -race ./internal/fileaccess ./internal/backupasset/provider \
  ./internal/backupasset/catalog \
  ./internal/backupasset/repository ./internal/backupasset/recovery \
  -run '^(TestPinnedStrictTree|TestRsyncCommittedCatalogRevalidatesExactManifestAtFinalize|TestManagedRsyncCatalogBuildCompletesWithFingerprintNone|TestRsyncRestorePort|TestRsyncResolver|TestRecoveryCreatesRsyncScalarRef)' \
  -count=10
go test ./internal/backupasset/provider ./internal/backupasset/catalog \
  ./internal/backupasset/repository \
  ./internal/backupasset/recovery \
  -run 'Restore|Rsync.*Recovery|Rsync.*Resolver|Restic.*Recovery|Rclone.*Recovery' -count=1
go vet ./internal/fileaccess ./internal/backupasset/provider \
  ./internal/backupasset/catalog ./internal/backupasset/repository \
  ./internal/backupasset/recovery

cd /home/murray/code/xirang
gofmt -d backend/internal/backupasset/provider/catalog.go \
  backend/internal/backupasset/provider/rsync_test.go \
  backend/internal/backupasset/repository/query_test.go
test -z "$(rg -n 'backupasset/(repository|recovery)|internal/(model|fileaccess)|gorm\\.io' \
  backend/internal/backupasset/provider/catalog.go)"
rg -n 'FingerprintStrength|ParseFingerprintStrength|sealedCatalogReadSession' \
  backend/internal/backupasset/provider/catalog.go \
  backend/internal/backupasset/provider/rsync_test.go \
  backend/internal/backupasset/repository/catalog_read.go \
  backend/internal/backupasset/repository/query_test.go \
  backend/internal/backupasset/catalog/indexer.go
git diff -- backend/internal/backupasset/repository/catalog_read.go \
  backend/internal/backupasset/catalog/indexer.go
git diff --check
test -z "$(git diff --cached --name-only)"
```

Observed static disposition: Provider owns the Catalog production change and
the authenticated `request.SourceFingerprint` correction; Repository's
locator-only wrapper and Catalog's strict parser/indexer have no correction
diff. The `rg` output is inspected match-by-match and is not itself a pass/fail
count. The focused evidence is recorded in
`research/implementation-evidence.md`; broad Provider remains blocked by host
`IFree=0` and is not claimed as a package pass.

Task 8 must later run
`TestRecoveryRuntimeRequiresBoundRsyncResolverAndTargetWriter`; it proves runtime
injects the Repository resolver and rejects nil/unbound Rsync ports before graph
publication.

### Task 5: Plan Authorization And Atomic Job Creation

**Status:** `complete_approved` at focused Task 5 authorization-receipt scope.
The 2026-07-31 receipt amendment and follow-up remain the historical planning
authority. Product, test, paired-migration, settings, operation-snapshot and
standalone receipt-owner work exists inside the same 145-path manifest; fresh
focused SQLite and required real PostgreSQL gates pass. Independent receipts
`019fb71a-75df-7770-a17d-9b3d8647d99d` and
`019fb73d-03b6-7111-baf3-83e1ae2e3f8b` returned `SPEC APPROVED` and `QUALITY
APPROVED: READY`. Tasks 6--10 remain separate, and the exact
RED/immediate-GREEN chronology is qualified in
`research/implementation-evidence.md` without reconstructed output.

**Files already in the exact 145-path manifest:** paired SQLite/PostgreSQL
`000069` files; `model/backup_asset_recovery.{go,test.go}`;
`database/backup_asset_migrations_integration_test.go`; Recovery
`contracts.{go,test.go}`, `state.{go,test.go}`, `service.{go,test.go}` and
`behavior_integration_test.go`; `api/handlers/backup_recovery_handler.{go,test.go}`;
and the already listed router/RBAC/settings/config/frozen-fixture paths where
their later task owns wiring. Do not create a receipt table, a `000070`, a path
outside this manifest, or an edit to generic `step_up.go`, credential-grant or
generic audit code.

- [x] **Step 1 — write the receipt schema/model RED before changing 000069.**
  In the existing model/migration/database tests, add
  `TestBackupAssetMigration069AuthorizationReceiptSQLite` and
  `TestRecoveryAuthorizationReceiptModelContract`. They must demand exactly one
  new `backup_asset_recovery_evidence.kind=authorization_receipt` arm with
  private plan/job/checkpoint/grant/attempt/single-source-lease/node-lease effect
  linkage; requester; closed operation/category/endpoint; key/intent/proof/
  presenting-session digests; proof/session/replay expiries; and exact effect
  references. Assert both partial unique indexes, receipt-vs-normal-vs-latch
  CHECK exclusivity, immutable UPDATE, pre-expiry direct DELETE and parent-cascade
  rejection, indexed bounded replay/proof/reaper reads, and first-statement down
  refusal while a receipt remains. Execute must require exactly one
  `recovery_job` lease matching the plan's sole RecoveryPoint/job/initial
  attempt, with no list/blob substitute. Paired deadline checks must require
  `proof_expires_at <= replay_expires_at <= presenting_session_expires_at` and
  write/delete grant expiry no later than replay expiry; add the owner-first
  `recovery_job` linkage index over holder/owner/attempt/RecoveryPoint/id. The first command must fail because the
  existing evidence shape has no receipt arm:

```bash
cd backend
go test ./internal/database ./internal/model \
  -run '^(TestBackupAssetMigration069AuthorizationReceiptSQLite|TestRecoveryAuthorizationReceiptModelContract)$' \
  -count=1
```

- [x] **Step 2 — write direct-SQL parity REDs.** Add
  `TestRecoveryAuthorizationReceiptDirectSQLSQLite` and the required real
  `TestRecoveryAuthorizationReceiptDirectSQLPostgres`. Each inserts a valid
  security-override, write-authorize, delete-authorize and execute receipt, then
  independently attempts duplicate requester+endpoint+key, duplicate nonempty
  proof digest, wrong operation/category/effect linkage, mutation, pre-expiry
  deletion, parent cascade, invalid proof/replay/session/grant deadline order,
  missing/extra/substituted execute source lease, expired-reaper deletion, used
  down and post-reap pristine down. Assert the same typed SQL failure/product on
  both engines and that no DELETE can remove `schema_use_latch`. Also seed old
  protected receipt/normal/latch rows before later eligible receipts and prove
  the indexed eligibility query excludes them before LIMIT. The RED must be recorded before
  paired SQL/model implementation:

```bash
cd backend
go test ./internal/database ./internal/backupasset/recovery \
  -run '^(TestRecoveryAuthorizationReceiptDirectSQLSQLite|TestRecoveryAuthorizationReceiptModelContract)$' \
  -count=1
REQUIRE_POSTGRES_RECOVERY_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" \
  go test ./internal/backupasset/recovery ./internal/database \
  -run '^TestRecoveryAuthorizationReceiptDirectSQLPostgres$' -count=1
```

- [x] **Step 3 — write four service/API receipt REDs, one table per endpoint.**
  In `service_test.go` and `backup_recovery_handler_test.go`, add
  `TestRecoveryAuthorizationReceiptSecurityOverrideReplayAndConflict`,
  `TestRecoveryAuthorizationReceiptWriteAuthorizeReplayAndConflict`,
  `TestRecoveryAuthorizationReceiptDeleteAuthorizeReplayAndConflict`, and
  `TestRecoveryAuthorizationReceiptExecuteReplayAndConflict`. Each test must
  prove: same requester+canonical endpoint+key+full intent returns the same
  stored effect; one changed intent input (including reason digest, expected
  revision, binding, category or secret hash where applicable) is 409 and makes
  no write; receipt replay is allowed only in the stored presenting session;
  initial operation requires fresh exact `asset.recover`; and security override,
  write, delete and execute record the exact effect references specified in
  `design.md` §4.3.1. Execute additionally asserts the same durable job after
  terminal state and no second consumed grant/job item/attempt/source lease/node
  lease. These are genuine REDs; no implementation edit precedes their first
  observed failure.

- [x] **Step 4 — write proof/session composition REDs without editing generic
  handlers.** Add `TestRecoveryAuthorizationReceiptProofReuseAcrossPlanAndCategory`,
  `TestRecoveryAuthorizationReceiptReplayAfterProofExpiryInSameLoginSession`,
  `TestRecoveryAuthorizationReceiptRejectsDifferentPresentingSession`, and
  `TestRecoveryAuthorizationReceiptDoesNotAssertProofJTIEqualsSessionJTI`, plus
  `TestRecoveryAuthorizationReceiptRejectsUncoverableProofLifetime` and
  `TestRecoveryAuthorizationReceiptReaperNeverReopensLiveProof`.
  Use the unchanged generic `validateStepUpProof` contract only for a no-receipt
  request. Prove a proof consumed by any of the four operations cannot authorize
  another plan/category/endpoint, proof claims and current login session are
  independently validated, and an original same-session lost-response replay
  does not need a second valid proof. Operator/Viewer, wrong owner, wrong action,
  expired initial proof and result-purpose proof must fail before an effect;
  none may make an existence or reason leak. A near-expiry presenting session
  that cannot cover the actual proof/grant deadline makes zero effect, and a
  reaper race cannot delete the receipt while its proof remains valid or admit
  that proof under another session.

- [x] **Step 5 — write client-secret and lost-response REDs.** Add
  `TestRecoveryGrantSecretCanonicalShape`,
  `TestRecoveryWriteGrantSecretLostResponseReplay`,
  `TestRecoveryDeleteGrantSecretLostResponseReplay`, and
  `TestRecoveryExecuteRejectsMismatchedGrantSecret`. The fixtures generate
  exactly 32 bytes with `crypto/rand`, encode base64url without padding, retain
  that value only in test-local client state, and assert all malformed, padded,
  whitespace, noncanonical or wrong-length forms fail before transaction. A
  valid write/delete issue retry with the same key/intent/secret returns only
  the existing metadata; changing the secret with the same key conflicts; DB
  stores only the domain-separated hash; execute requires a hash match. Both
  write and delete grant expiry must be no later than receipt replay expiry. Scan
  handler response, model rows, audit input, structured-log capture and error
  text for the fake raw secret, JWT, proof JTI, session JTI, reason and reason
  ciphertext.

- [x] **Step 6 — write transaction/race/fault REDs.** Add
  `TestRecoveryAuthorizationReceiptRollbackBeforeCommit`,
  required real `TestRecoveryAuthorizationReceiptRollbackBeforeCommitPostgres`,
  `TestRecoveryAuthorizationReceiptAuditFailureAfterCommit`,
  `TestRecoveryAuthorizationReceiptConcurrentSQLiteWinner`, and required real
  `TestRecoveryAuthorizationReceiptConcurrentPostgresWinner`. Inject a failure
  after every security override/write grant/delete grant/execute effect stage
  but before receipt commit and assert zero receipt/effect/lease residue. Then
  inject failure only in the existing post-commit audit projection and assert
  exactly one committed receipt/effect, a sanitized successful response, and a
  read-only retry. Race same intent and cross-plan/cross-category proof reuse:
  exactly one transaction wins; the key loser replays and the proof loser gets
  safe `proof_used`; no raw details, duplicate job, grant consumption, item,
  attempt, source lease or node lease appears.

  Add `TestRecoveryAuthorizationReceiptReaperProgressAndRestart` for the
  runtime-owned bounded maintenance service. It must prove restart, disabled
  runtime continuation, stateless `(replay_expires_at,id)` progress past old
  protected/normal/latch rows, and shutdown/schema-drain join without touching
  any non-receipt row.

- [x] **Step 7 — run the named RED selectors and preserve their output.** Run
  these exact commands before the first Task 5 production edit. Their expected
  result at this point is FAIL for missing receipt schema/service behavior; do
  not substitute a package aggregate or an inherited Task 1--4 GREEN:

```bash
cd backend
go test ./internal/model ./internal/database ./internal/backupasset/recovery \
  ./internal/api/handlers \
  -run '^(TestBackupAssetMigration069AuthorizationReceiptSQLite|TestRecoveryAuthorizationReceipt(ModelContract|DirectSQLSQLite|SecurityOverrideReplayAndConflict|WriteAuthorizeReplayAndConflict|DeleteAuthorizeReplayAndConflict|ExecuteReplayAndConflict|ProofReuseAcrossPlanAndCategory|ReplayAfterProofExpiryInSameLoginSession|RejectsDifferentPresentingSession|DoesNotAssertProofJTIEqualsSessionJTI|RejectsUncoverableProofLifetime|ReaperNeverReopensLiveProof|RollbackBeforeCommit|AuditFailureAfterCommit|ConcurrentSQLiteWinner|ReaperProgressAndRestart)|TestRecoveryGrantSecretCanonicalShape|TestRecoveryWriteGrantSecretLostResponseReplay|TestRecoveryDeleteGrantSecretLostResponseReplay|TestRecoveryExecuteRejectsMismatchedGrantSecret)$' \
  -count=1
REQUIRE_POSTGRES_RECOVERY_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" \
  go test ./internal/backupasset/recovery ./internal/database \
  -run '^(TestRecoveryAuthorizationReceiptDirectSQLPostgres|TestRecoveryAuthorizationReceiptConcurrentPostgresWinner|TestRecoveryAuthorizationReceiptRollbackBeforeCommitPostgres)$' \
  -count=1
```

- [x] **Step 8 — implement the smallest paired receipt GREEN.** Extend only
  the existing evidence model and paired `000069` SQL with the closed receipt
  arm, partial unique indexes, private FK/trigger linkage, immutable/pre-expiry
  retention guards, proof/replay/session plus grant/replay deadline checks,
  exact singleton execute-source-lease linkage, stateless eligible-reaper index,
  and down parity. Add receipt request/result contracts and
  domain-separated digest helpers in Recovery; keep raw grant secret in a
  short-lived request value, replace raw recovery session persistence with the
  one-way presenting-session binding, and include one-way reason/secret inputs
  in full intent. Do not change generic step-up, credential-grant or audit
  handlers/writer, add a table, add a migration, or add an API path.

- [x] **Step 9 — implement four receipt+effect transactions and thin handler
  composition.** The no-receipt handler branch uses the frozen exact proof
  validator plus `CurrentSessionBinding`; replay occurs before that validation.
  Service transactions perform security-override plan CAS, write/delete grant
  creation, and execute grant consumption+job/items/attempt/exactly one source lease/node
  lease+plan transition, each with receipt insertion before the single commit.
  On unique race, rollback and classify same-intent replay versus idempotency
  conflict versus proof-used conflict. Call the existing sanitized audit
  projection only after commit and make its failure non-duplicating/non-leaking.
  Return grant/job metadata only; never return a bearer secret or proof/JTI.

- [x] **Step 10 — prove GREEN, races and no-leak before Task 6.** Re-run the
  unchanged Step 7 selectors until all are PASS, then run repeated SQLite race,
  required real PostgreSQL, direct SQL/down, handler/RBAC and static privacy
  selectors below. Record each original RED and corresponding unchanged GREEN
  in `research/implementation-evidence.md` only after observation; this planning
  amendment itself is not evidence.

```bash
cd backend
go test ./internal/model ./internal/database ./internal/backupasset/recovery \
  ./internal/api/handlers -run 'AuthorizationReceipt|Recovery.*(Authorize|Execute)' -count=1
go test -race ./internal/backupasset/recovery \
  -run 'TestRecoveryAuthorizationReceipt(ConcurrentSQLiteWinner|.*ReplayAndConflict|.*RollbackBeforeCommit|RejectsUncoverableProofLifetime|ReaperNeverReopensLiveProof|ReaperProgressAndRestart)' \
  -count=10
REQUIRE_POSTGRES_RECOVERY_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" \
  go test ./internal/backupasset/recovery ./internal/database \
  -run '^(TestRecoveryAuthorizationReceiptDirectSQLPostgres|TestRecoveryAuthorizationReceiptConcurrentPostgresWinner|TestRecoveryAuthorizationReceiptRollbackBeforeCommitPostgres)$' \
  -count=1
rg -n 'FAKE_RECOVERY_(RAW_PROOF|PROOF_JTI|SESSION_JTI|REASON|SECRET)' \
  ./internal/backupasset/recovery ./internal/api/handlers ./internal/model
go vet ./internal/backupasset/recovery ./internal/api/handlers ./internal/model ./internal/database
```

- [x] **Step 11 — observe settings/runtime-owner RED before adding receipt
  settings.** Add
  `TestRecoveryAuthorizationReceiptSettingsRegistryComplete`,
  `TestRecoveryAuthorizationReceiptSettingsAtomicSnapshot`,
  `TestRecoveryAuthorizationReceiptSettingsDeadlineOrdering`, and
  `TestRecoveryAuthorizationReceiptSettingsTransitions` in the manifested
  settings/Foundation/Repository fixture and transition test paths. Before any
  settings production edit, run and preserve this failing selector:

```bash
cd backend
go test ./internal/settings ./internal/backupasset ./internal/backupasset/repository \
  ./internal/api/handlers \
  -run '^TestRecoveryAuthorizationReceiptSettings(RegistryComplete|AtomicSnapshot|DeadlineOrdering|Transitions)$' \
  -count=1
```

  Then add the bounded authorization-receipt replay/retention TTL and bounded
  reaper cadence/batch to the existing Recovery settings registry, atomic
  snapshot, Foundation parser/tests and Repository frozen defaults fixture.
  Registry/default completeness, DB→env→default atomic snapshots, write/delete
  grant TTL `<=` receipt replay TTL, positive bounded maintenance values, and
  per-request `proof <= replay <= presenting session` validation must all close.
  Batch update, delete/reset and config import use the existing
  validate→drain→persist transition and preserve the old snapshot on failure.
  Re-run the unchanged selector plus all four endpoint replay/lifetime tests for
  GREEN. No raw reason/proof/JTI/secret becomes a setting or audit field.

- [x] **Step 12 — close the frozen operation-row persistence gap before Task
  6.** First add
  `TestRecoveryAuthorizationReceiptExecutePersistsExactOperationRows`,
  `TestRecoveryAuthorizationReceiptExecuteRejectsMissingOrTamperedOperationSnapshot`,
  `TestRecoveryAuthorizationReceiptExecuteDeleteRowHasNoPlanItem`,
  `TestRecoveryAuthorizationReceiptOperationSnapshotModelContract` and paired
  SQLite/PostgreSQL `000069` migration assertions. Observe RED against the
  current hard-coded `OperationKind: "create"` projection. The tests must use
  create/overwrite/skip plus an `in_place+exact_mirror` delete row and prove:
  the preflight owns a nonempty versioned encrypted snapshot; decrypt/rebuild
  exactly reproduces `operation_set_digest`, `delete_set_digest`, counts and
  bytes; non-delete rows bind the selected plan item; delete rows have no plan
  item; target-path rows are unique; missing/malformed/tampered ciphertext or a
  substituted source fails before grant consumption, leases, job/items or plan
  transition.

  Minimal GREEN extends only the existing preflight and job-item models plus
  paired `000069`: preflight gains encrypted operation-snapshot ciphertext;
  job items gain target-path/expected-prior/display/estimated-byte columns and
  nullable `plan_item_id` only for delete. Add one closed versioned codec in
  Recovery, rebuild products on load, and insert the exact canonical rows in
  `persistExecuteAuthorizationTx`; never expose the private snapshot through an
  API DTO and never fall back to guessed `create` rows. Re-run:

```bash
cd backend
go test ./internal/model ./internal/database ./internal/backupasset/recovery \
  -run '^(TestRecoveryAuthorizationReceipt(OperationSnapshotModelContract|ExecutePersistsExactOperationRows|ExecuteRejectsMissingOrTamperedOperationSnapshot|ExecuteDeleteRowHasNoPlanItem)|TestBackupAssetMigration069.*OperationSnapshot)' \
  -count=1
REQUIRE_POSTGRES_RECOVERY_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" \
  go test ./internal/database ./internal/backupasset/recovery \
  -run '^TestBackupAssetMigration069PostgresOperationSnapshot$' -count=1
```

### Task 6: Fenced Worker, Checkpoints And In-Place Policies

**Files:** recovery state/executor/worker files/tests and behavior integration
test; recovery checkpoint model/test; the existing paired SQLite/PostgreSQL
`000069` files; and the existing database migration integration test. Every path
is already in the exact 145-path manifest.

**Current bounded Task 6 status (2026-08-02):** B1-E1, B1-E2 and B1-E3 are
`complete_checked` for Corrections 1--3, 5 and 7--13 at their exact focused
scopes, but B1 aggregate remains partial. B2-E1/E2 are `complete_checked` for
Corrections 4 and 6 plus the delete row; B2 aggregate remains partial pending
combined/whole evidence. B3 is `PROVED_COMPLETE_FOCUSED_ONLY` for Correction 14 after independent
specification approval and focused evidence. F6 and F3 are also closed only at
their recorded focused scopes. Task-6-owned F4 is `complete_checked` at its
focused workspace/deadline/cleanup-only scope. Task 6 remains `in_progress`.
The focused B3, F6, F3, F4, B1-E1/E2/E3 and B2-E1/E2 rows are closed only as
recorded; all remaining generic Task 6 rows stay unchecked. The open ledger is
unchecked execution items and whole gates/reviews. The original review is
F1--F8 and has no Finding 9. Tasks 7--10 remain `not_executed`.

#### Frozen Task 6 matrix — split evidence status

The following test names remain immutable. The first twelve belong to the
first-thirteen B1/B2 evidence closure: their B1-E1/E2/E3 and B2-E1/E2 arms are
`complete_checked`, while the combined whole matrix remains unproven. The final five belong to B3 and are proved only at
focused Correction 14 scope:

| Stable selector | Frozen responsibility |
|---|---|
| `TestRecoveryOperationSnapshotV2CanonicalLocatorMatrix` | every schema-v2 row, including delete, has an exact canonical locator plus semantic digest; no fallback |
| `TestRecoveryOperationSnapshotV2WholeProductTamperMatrix` | whole operation/source/policy/locator product rejects canonical self-consistent substitutions; B2-E1 delete facts/policy `complete_checked` on 2026-08-02 |
| `TestRecoveryExecutePreparedAggregateGrantFirstMatrix` | B1-E3 frozen selector: outside-tx prepared aggregate, locked byte equality, exact key match and grant-CAS-first atomic insert; `complete_checked` on 2026-08-02 |
| `TestRecoveryTargetLocatorCiphertextBindingMatrix` | full length-framed row/job/root/workspace/digest/key/cipher/operation AEAD binding and hook exclusion |
| `TestRecoveryPrepareFirstWriteUsesPreallocatedWorkspaceMatrix` | isolated none-state identity is preallocated and reused for `none -> reserved`; unexpected directory fails closed |
| `TestRecoveryAdoptInterruptedOperationDurableDerivationMatrix` | B1-E3 frozen selector: exact three-boundary adoption API derives every fact from durable state; `complete_checked` on 2026-08-02 |
| `TestRecoveryVerifyOperationProductMatrix` | B1-E1 ordinary, B2-E1 exact-absence/durable-delete-authority, and B2-E2 absence-chain/ordered multi-delete/restart arms `complete_checked` at their separate focused scopes on 2026-08-02; combined whole-matrix credit remains open |
| `TestRecoveryPermanentCleanupKeyLossMatrix` | B1-E3 frozen selector: pre-arm unchanged; current post-arm DB-only `needs_attention|cleanup_due`; no decrypt/I/O/success; `complete_checked` on 2026-08-02 |
| `TestBackupAssetMigration069RecoveryLocatorProductSQLite` | B1-E3 immutable arms and B2-E1 SQLite delete-row legality arm `complete_checked` on 2026-08-02 |
| `TestBackupAssetMigration069RecoveryLocatorProductPostgres` | required real PostgreSQL parity for the B1-E3 immutable and B2-E1 delete-row legality arms; `complete_checked` on 2026-08-02 |
| `TestRecoveryLocatorRaceTakeoverOneWinner` | B1-E3 frozen selector: deterministic execute/adoption/takeover race has one fenced winner; `complete_checked` on 2026-08-02 |
| `TestRecoveryLocatorProductNoPlaintextLeak` | raw item/workspace locator is absent from DB-external products and failures |
| `TestStateOperationUnresolvedProductsAreClosedAndTerminal` | closed terminal phase/category/source-revalidation state product and no successor |
| `TestBackupAssetRecoveryCheckpointCarriesPrivateUnresolvedOutcomeProduct` | exact private checkpoint facts and neutral defaults |
| `TestRecoveryExecuteClaimProjectsUnresolvedRemoteOutcomeMatrix` | exact production four-category field-legality/predecessor matrix and one fenced atomic needs-attention disposition |
| `TestBackupAssetMigration069UnresolvedOperationOutcomeSQLite` | SQLite closed fields/checks/triggers/legal-predecessor/terminal/no-chain contract, alongside the existing 000069 legality/parity helpers |
| `TestBackupAssetMigration069UnresolvedOperationOutcomePostgres` | required real PostgreSQL migration/behavior parity for the same unresolved outcome product, alongside the existing 000069 PostgreSQL helper |

Every semantically pre-arm or zero-mutation negative case asserts all six zero-
effects: no sequence-1 checkpoint, no item `success` or `skipped`, no job
success, no target-chain advance, no target I/O where forbidden, and no raw
locator leak. An unresolved-outcome case instead prohibits a success/adoption
checkpoint for the current item and requires exactly one terminal
`operation_unresolved` checkpoint for that item. It preserves earlier valid
checkpoints and prior-item success/skipped facts, keeps the current item's
`next_target_revision` empty, and forbids current-item target-chain advancement.
For an unresolved first operation, that unique terminal checkpoint may itself
be sequence 1.

The B3 writer froze the five fourteenth-correction selectors, removed only the
fourteenth behavior to the authorized pre-feature baseline, observed their
genuine failure for the absent product while preserving first-thirteen behavior,
then restored/fixed the final product. The exact local/SQLite and required real
PostgreSQL commands were:

```bash
cd backend
go test ./internal/backupasset/recovery ./internal/model ./internal/database \
  -run '^(TestStateOperationUnresolvedProductsAreClosedAndTerminal|TestBackupAssetRecoveryCheckpointCarriesPrivateUnresolvedOutcomeProduct|TestRecoveryExecuteClaimProjectsUnresolvedRemoteOutcomeMatrix|TestBackupAssetMigration069SQLite|TestBackupAssetMigration069PairedFiles|TestBackupAssetMigration069UnresolvedOperationOutcomeSQLite)$' \
  -count=1

REQUIRE_POSTGRES_RECOVERY_TEST=1 REQUIRE_POSTGRES_MIGRATION_TEST=1 \
TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" \
  go test ./internal/database \
  -run '^(TestBackupAssetMigration069Postgres|TestBackupAssetMigration069UnresolvedOperationOutcomePostgres)$' \
  -count=1
```

The authorized baseline produced genuine FAIL caused by missing
`operation_unresolved` / `remote_outcome_unresolved` state, checkpoint,
projection or paired-`000069` behavior. Fixture, cache, missing-DSN or unrelated
first-thirteen failures were not accepted as RED. The correction was then
restored/fixed and both unchanged commands reached GREEN.

The same combined first-thirteen commands remain the B2/whole-matrix gate. B1
closed them only through separately bounded B1-E1/E2/E3 selector subsets with
their own controlled RED and unchanged-selector GREEN; that focused evidence
does not close the pending delete/absence/chain arms:

```bash
cd backend
go test ./internal/model ./internal/database ./internal/backupasset/recovery \
  -run '^(TestRecoveryOperationSnapshotV2CanonicalLocatorMatrix|TestRecoveryOperationSnapshotV2WholeProductTamperMatrix|TestRecoveryExecutePreparedAggregateGrantFirstMatrix|TestRecoveryTargetLocatorCiphertextBindingMatrix|TestRecoveryPrepareFirstWriteUsesPreallocatedWorkspaceMatrix|TestRecoveryAdoptInterruptedOperationDurableDerivationMatrix|TestRecoveryVerifyOperationProductMatrix|TestRecoveryPermanentCleanupKeyLossMatrix|TestBackupAssetMigration069RecoveryLocatorProductSQLite|TestRecoveryLocatorRaceTakeoverOneWinner|TestRecoveryLocatorProductNoPlaintextLeak)$' \
  -count=1

REQUIRE_POSTGRES_RECOVERY_TEST=1 REQUIRE_POSTGRES_MIGRATION_TEST=1 \
TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" \
  go test ./internal/model ./internal/database ./internal/backupasset/recovery \
  -run '^(TestRecoveryOperationSnapshotV2CanonicalLocatorMatrix|TestRecoveryOperationSnapshotV2WholeProductTamperMatrix|TestRecoveryExecutePreparedAggregateGrantFirstMatrix|TestRecoveryTargetLocatorCiphertextBindingMatrix|TestRecoveryPrepareFirstWriteUsesPreallocatedWorkspaceMatrix|TestRecoveryAdoptInterruptedOperationDurableDerivationMatrix|TestRecoveryVerifyOperationProductMatrix|TestRecoveryPermanentCleanupKeyLossMatrix|TestBackupAssetMigration069RecoveryLocatorProductPostgres|TestRecoveryLocatorRaceTakeoverOneWinner|TestRecoveryLocatorProductNoPlaintextLeak)$' \
  -count=1
```

Both combined first-thirteen commands remain open for B2 and whole Task 6; B3
provides no credit to them.

- [x] **Obtain fresh independent specification approval before product edits.**
  Review the amended PRD/design/implementation contracts against all fourteen
  corrections, the unchanged 145-path manifest and current repository selector
  conventions. Resolve every Critical/Important finding in these four planning
  artifacts; do not remove product behavior or run/credit RED until the review
  returns approval.
- [x] **Freeze the fourteenth selectors before touching its product.** Preserve
  the exact five named test bodies and record that their current state is
  unreviewed inherited GREEN. Do not execute that GREEN as a substitute for RED.
- [x] **Restore only the authorized pre-feature baseline.** Remove only the
  `operation_unresolved` phase/category/facts/runtime projection and the two
  `000069` up-migration enforcement arms belonging to the fourteenth correction.
  Preserve both down migrations, all first-thirteen Task 6 product/tests/
  migration work, every other dirty path and all five frozen selectors; do not
  stage the temporary baseline.
- [x] **Observe the fourteenth correction's genuine RED.** Run the exact focused
  local/SQLite command above, then its required real-PostgreSQL command. The
  failure must be caused by the missing fourteenth behavior; preserve exact
  output and reject fixture, cache, environment or unrelated failures.
- [x] **Reapply the closed unresolved product after RED.** Add terminal
  `operation_unresolved`, `remote_outcome_unresolved`, the four closed unresolved
  categories and exact private checkpoint facts. All older phases require
  neutral values. Enforce the closed field matrix: disagreement has two valid
  unequal revisions; mismatch has a valid observation with neutral skip write
  fields or valid create/overwrite write facts; invalid-write stores only its
  sanitized framed digest and no observation facts; invalid-observation stores
  only its sanitized framed digest plus valid write facts when applicable and
  neutral structured observation facts. Source drift/failure may coexist without
  replacing the remote category. Unresolved rows bind current job/item/canonical
  operation/prior target revision plus attempt/node/source/preflight/authority
  fences and sanitized framed remote facts, keep `next_target_revision` empty,
  allow sequence 1 or applicable workspace/operation/delete-authority predecessors,
  admit no successor and never advance the target chain. Existing B2 delete
  approval/evidence remains unchanged.
- [x] **Reapply the atomic disposition and sanitized bindings after RED.** In
  one short fenced transaction append exactly one checkpoint, fail the current item,
  mark the job needs-attention, insert sanitized failure evidence, close the
  attempt and release source/node leases while success/skipped/job-success/chain
  fields remain unchanged. Evidence cross-binds checkpoint/job/item/attempt/node
  fences; no current-item success/skipped/adoption checkpoint or continued write
  is allowed. `WriteResultDigest` and `ObservationDigest` are
  domain-separated length-framed evidence bindings only, never fidelity,
  absence, plaintext/raw result or raw-error storage.
- [x] **Reapply the two 000069 up migrations only and prove GREEN.** Enforce identical closed
  values, neutral arms, current binding, terminality and no-chain/failure-evidence
  relations in the existing SQLite/PostgreSQL `000069` up files; both down files
  remain unchanged. Add no path, table, migration, backfill, `000070`, contract
  surface, keyring or crypto. Rerun the exact focused commands unchanged,
  including the named base legality/parity helpers; only their post-RED passes
  may be credited as GREEN.

#### F6 live-mutation permit TDD (focused closure 2026-08-02)

Status: `complete_approved` at focused F6 scope only. This gives no credit to F3,
B1/B2, F4, whole Task 6, Child, delivery or full gates. Permanent ownership is
limited to `backend/internal/backupasset/recovery/worker_test.go`. The only
temporary controlled-baseline path was
`backend/internal/backupasset/recovery/target.go`; it must finish at its intended
live-validation behavior and must never be staged while RED is exposed.

- [x] **F6 obtain independent focused planning/spec approval.** Review this
  subsection against original-review F6, current `TargetMutationPermit`/
  `validateFirstWritePermitAt`, the two-path boundary and the exact gates below.
  Resolve every Critical/Important issue before assigning the writer.
- [x] **F6 add and freeze the permanent selector before changing the baseline.**
  Add exactly `TestRecoveryReviewF6LatchBeforeTargetMutation` to
  `worker_test.go`. It must exercise latch loss and job/attempt/node/source-fence
  loss after a structurally valid permit is issued. For each revoked case, the
  fake for `CreateOwnedJobDir`, `CreateDirectory`, `WriteAtomic` or `Delete` must
  record a call if and only if permit admission succeeds; the final expectation
  is zero calls. Current latch plus current authority must admit each applicable
  mutator. `RemoveOwnedJobDir` is excluded for Task 7. Freeze the selector before
  touching `target.go`; an inherited pre-baseline GREEN is not evidence.

  ```bash
  cd /home/murray/code/xirang
  F6_TARGET_START_HASH="$(sha256sum backend/internal/backupasset/recovery/target.go | cut -d' ' -f1)"
  F6_SELECTOR_FROZEN_HASH="$(sha256sum backend/internal/backupasset/recovery/worker_test.go | cut -d' ' -f1)"
  test "$(rg -n '^func TestRecoveryReviewF6LatchBeforeTargetMutation\(' \
    backend/internal/backupasset/recovery/worker_test.go | wc -l)" -eq 1
  test -z "$(git diff --cached --name-only)"
  ```

- [x] **F6 expose only the authorized temporary RED baseline.** In `target.go`,
  temporarily bypass only the private live proof callback in
  `TargetMutationPermit.ValidateAt`; retain all structural, expiry, purpose,
  object-binding, type and `TargetPort` interface behavior. The complete
  temporary function is:

  ```go
  func (permit TargetMutationPermit) ValidateAt(now time.Time) error {
      if permit.validateShapeAt(now) != nil {
          return ErrInvalidTargetPermit
      }
      return nil
  }
  ```

  Do not change `issueTargetMutationPermit`, permit fields, public interfaces or
  unrelated behavior. Do not stage this state.
- [x] **F6 observe genuine RED through a mutating fake call.** Run only the
  frozen selector. RED is valid only when a structurally valid permit survives
  latch or job/attempt/node/source-fence loss and at least one applicable
  mutating fake call occurs. Compile/fixture/cache/environment failure or a test
  that stops before the fake call is not RED.

  ```bash
  cd /home/murray/code/xirang/backend
  go test ./internal/backupasset/recovery \
    -run '^TestRecoveryReviewF6LatchBeforeTargetMutation$' -count=1
  ```

  Expected: `FAIL` from a nonzero mutating fake call after authority revocation.
- [x] **F6 restore/fix live validation for GREEN.** Restore
  `TargetMutationPermit.ValidateAt` so a permit requires its private proof and
  re-runs `proof.validateAt(now)` on every admission. That callback must recheck
  the committed latch and current job, attempt, node lease and source fence
  before each of `CreateOwnedJobDir`, `CreateDirectory`, `WriteAtomic` and
  `Delete`. Revoked authority reaches no target mutator; current authority does.
  `target.go` must equal its recorded final-intended start hash before handoff.

  ```go
  func (permit TargetMutationPermit) ValidateAt(now time.Time) error {
      if permit.validateShapeAt(now) != nil || permit.proof == nil ||
          permit.proof.validateAt == nil || permit.proof.validateAt(now) != nil {
          return ErrInvalidTargetPermit
      }
      return nil
  }
  ```

- [x] **F6 run the exact normal, race and required PostgreSQL gates.** A required
  PostgreSQL test must use a usable `TEST_POSTGRES_DSN`, must not skip, and must
  fail the batch if required mode cannot connect.

  ```bash
  cd /home/murray/code/xirang/backend
  go test ./internal/database ./internal/model ./internal/backupasset/recovery \
    -run '^(TestRecoveryReviewF6UseLatchSQLite|TestRecoveryReviewF6LatchBeforeTargetMutation|TestRecoveryReviewF6UsedDownAtomicRefusal)$' \
    -count=1
  go test -race ./internal/backupasset/recovery \
    -run '^TestRecoveryReviewF6LatchBeforeTargetMutation$' -count=10
  REQUIRE_POSTGRES_MIGRATION_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" \
    go test ./internal/database \
    -run '^TestRecoveryReviewF6UseLatchPostgres$' -count=1
  ```

  Expected: every named selector passes; PostgreSQL reports a real pass and no
  skip.
- [x] **F6 run the exact affected regressions.** B3 and workspace/ordinary/
  exact-mirror behavior receive no new credit from this pass; these selectors
  only prove F6 did not regress them.

  ```bash
  cd /home/murray/code/xirang/backend
  go test ./internal/backupasset/recovery \
    -run '^(TestRecoveryPrepareFirstWriteUsesPreallocatedWorkspaceMatrix|TestWorkerPrepareFirstWriteCommitsLatchReservationAndFenceBoundPermit|TestRecoveryOrdinaryExecutionMutationMatrix|TestRecoveryExactMirrorSuccessfulDeleteProjectsAbsenceCheckpointAndChain)$' \
    -count=1
  ```

- [x] **F6 enforce frozen, static, manifest and staged-zero handoff.** The
  selector body remains byte-for-byte frozen after baseline removal; `target.go`
  returns to the recorded start hash; only the permanent test path remains as the
  F6 final delta; both format and vet are clean; the exact manifest remains
  9 Phase 1 + 55 create + 81 modify = 145 unique/disjoint paths; staged paths
  remain zero.

  ```bash
  cd /home/murray/code/xirang
  test "$(sha256sum backend/internal/backupasset/recovery/worker_test.go | cut -d' ' -f1)" = "$F6_SELECTOR_FROZEN_HASH"
  test "$(sha256sum backend/internal/backupasset/recovery/target.go | cut -d' ' -f1)" = "$F6_TARGET_START_HASH"
  test -z "$(gofmt -d backend/internal/backupasset/recovery/worker_test.go backend/internal/backupasset/recovery/target.go)"
  cd backend
  go vet ./internal/backupasset/recovery ./internal/database
  cd ..
  git diff --check
  test -z "$(git diff --cached --name-only)"
  ```

  Re-run the §2.1--§2.3 manifest parser and require exactly
  `phase1=9 create=55 modify=81 total=145 unique=145 duplicates=0`. Record the
  genuine RED and all GREEN outputs before requesting the focused review.

##### F6 focused completion evidence (2026-08-02)

The sole permanent F6 delta is the recording fake near `worker_test.go:34` and
`TestRecoveryReviewF6LatchBeforeTargetMutation` near line 669. `worker_test.go`
started at SHA-256
`a2452e6d5f01c4afb9fb5255ecc188b8790b695f0121430ac078a58cce373534` and
finished at
`352c31b6e5ced3f9f4a033a096ee90c5cd196be3bc4da65ab426bca18254ab3d`.
`target.go` was changed only for the controlled RED and restored byte-for-byte
to SHA-256
`8a0efaafc5bb08d3981790cc0fa27760936b80a58862f1910fd3e96dd5c64822`.

The genuine RED bypassed only the `TargetMutationPermit` live-proof callback.
Every revoked permanent latch, current job, attempt fence, node-lease fence and
source fence reached `CreateOwnedJobDir` and produced
`revoked authority CreateOwnedJobDir error=<nil>, want ErrInvalidTargetPermit`.
Compilation and quota failures are not RED evidence. Final GREEN admits
`CreateOwnedJobDir`, `CreateDirectory`, `WriteAtomic` and `Delete` for current
authority and rejects every listed authority loss before fake mutation.
`RemoveOwnedJobDir` remains deferred to Task 7.

The writer recorded PASS for the combined SQLite/model/recovery selector, the
F6 selector with `-race -count=10`, the four frozen recovery regressions,
`gofmt`, `go vet`, `git diff --check`, the exact-manifest guard and staged-zero
guard. Independent specification thread
`019fc136-feca-7fb0-82bc-3c33739aef12` returned `SPEC APPROVED`, confirming the
sole permanent `worker_test.go` delta, restored `target.go`, frozen hashes,
145-path manifest and staged paths zero. Independent quality thread
`019fc13c-0710-7343-b261-dd866382a8c0` returned `QUALITY APPROVED`, confirming
deterministic isolated fixtures, reliable admission recording, frozen hashes,
the manifest and staged paths zero.

Required PostgreSQL gate thread `019fc13d-ea0e-7f93-b1c6-32aebcb7368e`
returned `POSTGRES GATE PASSED` for:

```bash
REQUIRE_POSTGRES_MIGRATION_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" go test ./internal/database -run '^TestRecoveryReviewF6UseLatchPostgres$' -count=1
```

It exited 0 with `ok xirang/backend/internal/database 1.709s`, wall 31.032s,
against PostgreSQL 18.4 from isolated `postgres:18-alpine` at loopback database
`xirang_f6_gate`. The first two compile attempts exhausted `/tmp` quota before
tests and are not RED/test evidence; the passing run moved Go and cgo temporary
work to `/dev/shm`. Created container and scratch resources were removed, and
pre-existing resources were untouched.

The exact manifest remains 9 Phase-1 + 55 create + 81 modify = 145 unique/
disjoint paths and staged paths remain zero. Task 6 and the Child remain
`in_progress`; the parent remains `planning`. At that F6 checkpoint the fixed next order was F3,
B1-E1/E2/E3, B2-E1/E2, Task-6-owned F4, whole Task 6 specification review,
whole quality review, all final gates, then Task 7.

#### F3 pre-first-write drift batch (after F6)

**F3 adjudication: `complete_approved` at design-decision scope only
(2026-08-02).** The user approved persistent Plan A+. No product file or test was
changed and no F3 implementation/review credit is granted by this decision.

**F3 implementation: `complete_checked` at focused scope (2026-08-02).** The
four frozen selectors, focused race/regression/static gates, all SQLite 000069
selectors and required real PostgreSQL 000069/F3 selectors pass. The exact
implementation and gate record is in `research/implementation-evidence.md`.
This status grants no B1/B2, F4, whole Task 6, Child or delivery credit.

The stateless Plan B is rejected: current `updated_at,id` and
`lease_expires_at,id` scans restart from the oldest key, and an SQL-eligible
candidate can repeatedly fail after selection without any durable eligibility
or ordering change. The current restart tests cover pre-`LIMIT` ineligibility,
not this persistent class. Per-domain-row backoff is also rejected because a
stale fence loser has no authority to mutate Job/Attempt state for scheduling.

The approved writer adds exactly two fixed `scheduler_state` rows and closed
cursor/high-water/revision fields inside `backup_asset_recovery_evidence`, using
claim `(recovery_job.updated_at,recovery_job.id)` and takeover
`(attempt_row.lease_expires_at,attempt_row.id)` keys. A short scheduler
transaction durably pre-advances one candidate before its separate claim/
takeover transaction. Candidate conflict/fence loss advances only scheduler
metadata; a crash delays one candidate until wrap; a database-wide failure fails
the pass. The paired down guards ignore only the two exact scheduler rows and
remain fail-closed for every real Recovery/latch/content/lease fact. This amends
only the existing unshipped paired `000069`; no table, `000070`, route or API is
added.

The same batch owns authority-only pre-write drift terminalization and frozen-
intent execute replay. The current fence owner may atomically apply the sole
guarded `executed -> superseded` plan transition and fail the job as
`pre_write_drift` only before mutation arm, checkpoint or ambiguous target
observation; it closes the attempt, revokes unused authority and releases both
leases. Same-key replay returns that original receipt-linked failed job without
recomputing intent from mutable source facts.

The later bounded F3 TDD writer owns exactly:

```text
backend/internal/backupasset/recovery/worker.go
backend/internal/backupasset/recovery/worker_test.go
backend/internal/backupasset/recovery/service.go
backend/internal/backupasset/recovery/service_test.go
backend/internal/model/backup_asset_recovery.go
backend/internal/database/migrations/sqlite/000069_backup_asset_recovery.up.sql
backend/internal/database/migrations/sqlite/000069_backup_asset_recovery.down.sql
backend/internal/database/migrations/postgres/000069_backup_asset_recovery.up.sql
backend/internal/database/migrations/postgres/000069_backup_asset_recovery.down.sql
backend/internal/database/backup_asset_migrations_integration_test.go
```

The four frozen selectors/RED seams are:

```text
TestRecoveryReviewF3PreWriteDriftTransactionSQLite
TestRecoveryReviewF3ExecuteReplayAfterDrift
TestRecoveryReviewF3TwoWorkerAndCrashBarriers
TestRecoveryReviewF3PreWriteDriftTransactionPostgres
```

The implementation milestone must observe a genuine controlled-baseline RED,
make the same selectors GREEN, run the required non-skipped real PostgreSQL
companion plus focused race/regression/static/paired-migration/manifest/
staged-zero gates, record evidence, then stop and report. It cannot start B1/B2
or claim whole Task 6/Child/delivery credit.

- [x] **F3 choose and approve one fairness/data-model plan.** Plan A+ is the sole
  implementation authority; Plan B and per-domain-row scheduling backoff are
  rejected for the reasons above. Product work was still unstarted at the
  decision checkpoint; the later implementation status is recorded above.

- [x] **F3 write RED state/fence tests.** Cover every legal/illegal Plan, Job,
  attempt and checkpoint transition; monotonic revisions; claim/heartbeat/
  deadline/takeover; source and node lease loss; stale worker zero mutation; and
  bounded keyset scheduling where persistent failures cannot starve later work.
  Include the sole guarded `executed -> superseded` edge and reject it after
  mutation arm, any checkpoint or ambiguous target observation.
- [x] **F3 write RED legal pre-write drift transaction.** Put barriers after job
  commit, after claim and immediately before mutation arm; race two workers and
  crash every transaction boundary on SQLite and real PostgreSQL. The current
  fence owner atomically records `failed/pre_write_drift`, supersedes the plan,
  revokes unused authorities, closes the attempt and releases source/node
  leases. Same-key execute replay returns the same failed job; the loser performs
  zero DB/remote mutation. Use the frozen F3 selectors and required PostgreSQL
  companion in §3.1; no B1/B2 evidence batch starts until F3 closes.

#### B1/B2 bounded first-thirteen evidence closure

The inherited implementation is not a broad pass. Close it in the following
bounded units, each with a separately frozen focused selector set, genuine
controlled-baseline RED, unchanged-selector GREEN, focused review and evidence
entry. No unit grants retroactive credit to another:

- [x] **B1-E1 close Corrections 1--3 and 5.** Bound exact ordinary operation
  identity/bytes, create/overwrite revalidation, skip source/target separation
  and closed Verify evidence.
- [x] **B1-E2 close Corrections 7--10.** Bound canonical locator mapping,
  preallocated workspace/final-object digest, row-bound item AEAD and versioned
  aggregate-envelope evidence.
- [x] **B1-E3 close Corrections 11--13.** Bound three-boundary adoption, fatal
  key-loss/grant-first prepared aggregate and paired immutable enforcement.
- [x] **B2-E1 close Correction 4 plus its delete row.** Bound exact absence,
  delete-row legality and no synthetic absence digest.
- [x] **B2-E2 close Correction 6 and multi-delete evidence.** Bound absence-chain
  encoding, exact-mirror delete authority, multi-delete ordering and terminal
  chain projection without widening B3.

**Current bounded status:** B1-E1, B1-E2, B1-E3, B2-E1 and B2-E2 are `complete_checked` at
their focused scopes. Each frozen subset observed controlled RED and final
GREEN; focused race, affected-package regression, paired SQLite/required-
PostgreSQL companions, vet, format and scope gates are recorded in
`research/implementation-evidence.md`. B1 and B2 aggregates remain partial;
F4 and every whole-task item remain open, and no focused unit grants broader credit.

The legacy requirement rows below supply the product matrix for these units;
they do not authorize combining them into one unbounded evidence claim.

- [ ] **Implement the complete frozen test inventory before other production edits.** Preserve
  exactly the seventeen stable selectors above and capture an immutable baseline. The
  matrix must cover exact lowercase SHA-256/byte matching; closed present/absent
  union arms; bounded opaque revision including empty, oversize and SHA-256-shape
  assumptions; every create/overwrite/skip/delete presence, digest and byte-
  sentinel mutation; schema-v2 duplicate-source, policy-invalid and canonical
  self-consistent-but-operation-invalid products; snapshot/envelope/ciphertext/
  cross-row tamper including workspace identity/binding, separate semantic/
  final-object digests and operation facts;
  non-echo fakes; wrong root/item and cross-adoption; locator aliases,
  normalization, duplicates, collisions, cross-item mapping and rename; removal
  of caller-forged adoption inputs; key loss/version/auth; source drift;
  create/overwrite post mismatch; skip unchanged-target semantics; explicit
  delete absence versus permission, timeout and ambiguous missing; paired direct
  SQL/pristine/down/reapply; and deterministic race/takeover one-winner behavior.
- [ ] **Observe strict RED before production changes.** Run the unchanged focused
  selector first on SQLite, then the required real PostgreSQL selector. The RED
  must arise from missing/incorrect amendment behavior rather than fixture,
  compile-cache or environment noise. Preserve exact output; do not weaken a
  selector, reconstruct output or credit an inherited GREEN.
- [ ] **Extend the immutable snapshot and job-item product.** Freeze
  `ExpectedPostIdentityDigest` and operation-appropriate byte facts exactly as
  `design.md` §19.2/§19.2a require. `create` is prior absent, exact lowercase
  SHA-256 post digest + bytes `>= 0`, prior bytes `-1`; `overwrite` is prior
  present with exact post digest + bytes `>= 0`; `skip` is prior present, post
  digest equal to frozen prior digest, post bytes `-1`, separate frozen prior-
  target bytes `>= 0`, and skipped-only outcome; `delete` is prior present,
  empty post digest, both byte fields `-1`, and exact absence verification.
  Canonical schema-v2/envelope bindings retain those operation facts and delete-
  empty framing without an independent fidelity field or absence digest.
  Every row, including delete, also freezes its exact canonical
  `target_relative_locator` and `SemanticTargetDigest`: target-root-relative for
  in-place, deterministic workspace-relative suffix for isolated. Snapshot
  decode validates the full approved operation/source/policy/locator context and
  rejects duplicate-source, policy-invalid, missing-locator and self-consistent
  invalid payloads on every load; no plan or plan-item locator fallback exists.
- [ ] **Persist the dual-digest encrypted item locator product.** Outside the
  execute transaction preallocate job/item IDs and isolated `jobs/<opaque>`
  identity, then calculate a distinct final root-relative `TargetObjectDigest`
  (`jobs/<opaque>/<suffix>` for isolated). Persist on every item both semantic
  and final digests, recovery-local locator ciphertext, positive
  `TargetLocatorKeyVersion`, positive local cipher version and complete operation
  facts. The item locator column must not use generic model
  `BeforeSave`/`AfterFind` encryption hooks; only Recovery opens it by persisted
  version using HKDF-SHA256/AES-256-GCM from
  `KeyDomainRecoveryCleanupOwnership` and
  `xirang/recovery/job-item-target-locator/aead/v1`.
  `TargetLocatorEnvelopeBinding` length-frames codec/cipher versions; job/item/
  plan/nullable plan-item/source-row IDs; mode/node/root/root digest; workspace
  identity/`WorkspaceBindingDigest`; both target digests; every operation fact;
  and explicit key/cipher versions. Plaintext includes exact item and workspace
  locators. Keep generic model encryption only for `EncryptedOperationRows` and
  the job workspace locator; existing generic `enc:v2` means the encrypted
  preflight snapshot, not item AEAD. Do not change `keyring.go` or
  `secure/crypto.go`, use an implicit current version, rekey immutable item
  ciphertext or fall back to another locator.
- [ ] **Make Verify a closed present/absent product.** Present expectation and
  observation carry exact matching lowercase SHA-256 content identity and bytes
  `>= 0`; there is no separate fidelity digest. Absent expectation accepts only
  explicit exact `AbsentObservation`. Permission, timeout and ambiguous missing
  are not absence. `ObservedRevision` is bounded, nonempty, opaque, strong and
  target-derived, not SHA-256-shaped. Target-chain encoding separately domain-
  binds the absence observation while expected-post remains empty.
- [ ] **Implement only the exact adoption API.** The public service boundary is
  `AdoptInterruptedOperation(ctx, claim, jobItemID)` with no locator, identity,
  revision, operation or product parameters. Split it into: a short DB
  load/lock+decrypt+whole-product-validation transaction; source/target Verify
  I/O with no DB transaction held; and a final DB re-lock/revalidation/fenced
  CAS. Only after locking the exact persisted workspace/item, decrypting the
  locator, strict-joining, and recomputing/equality-checking
  `TargetObjectDigest` may the worker build `TargetObjectRef`/permit;
  `TargetPathDigest` carries only that final digest. The final CAS alone may
  atomically project success or skipped, append sequence 1 where applicable,
  advance the chain and close the attempt.
- [ ] **Implement fatal cleanup-key DB-only reconciliation.** Before runtime
  returns permanent `ErrKeyLost|ErrKeyUnavailable`, a bounded idempotent pass
  marks only current post-arm work sanitized `needs_attention|cleanup_due` and
  closes its attempts. Pre-arm, terminal and stale/taken-over work is unchanged.
  This path performs no target I/O, decrypt, checkpoint, success, skip or chain
  advance, and startup returns the original fatal error.
- [ ] **Enforce prepared grant-first aggregate creation.** Outside the effect
  transaction perform replay lookup, canonical snapshot decode, whole-product
  validation, association resolution, all ID/workspace allocation, explicit
  Active cleanup-key selection, all semantic/final/workspace digests and all
  AEAD/generic encryption. Inside the transaction recheck replay/proof; lock
  plan, preflight/plan items, grant, exact cleanup-key row, source/Catalog and
  existing source/node/attempt resources; recompute the prepared aggregate from
  locked facts and require byte-for-byte equality; and require `LockActiveTx` to
  match the selected key. CAS-consume the grant as the first effect mutation,
  then insert complete job/items/source lease/node lease/attempt/plan transition/
  receipt and commit once. No encryption, Provider/SSH/target I/O, audit or
  remote reservation runs inside. Preparation leaves no state on failure; a
  transaction failure rolls back grant and aggregate; post-commit crash leaves
  the complete unreserved aggregate.
- [ ] **Amend paired 000069 only after RED.** Add paired checks and insert,
  immutable, one-way projection and checkpoint triggers. Isolated none requires
  workspace ciphertext/`WorkspaceBindingDigest` and empty marker/owner/fence/
  deadline; reserved+ retains that exact identity and requires its phase product;
  in-place none has all workspace fields empty. Freeze job/item identities,
  both item digests, ciphertext/versions and every operation presence/digest/
  byte binding, including skip prior-target bytes and delete empty expected-post;
  require unique semantic/final digests per job; bind insert facts; allow delete
  only for in-place exact-mirror; permit only documented pending-to-terminal
  projection plus `updated_at`; and reject terminal rewrites. Because SQL cannot
  authenticate ciphertext, every service/worker load reconstructs the full item
  set before I/O. Down runs the existing data guard before dropping every new
  trigger/function. Prove identical SQLite and real PostgreSQL direct-SQL,
  pristine/down/reapply and used-down behavior. Do not add `000070`, a backfill,
  table or path.
- [ ] **Prove unchanged GREEN and case-appropriate negative effects.** Re-run the exact RED
  selectors normally, under deterministic race/takeover repetition and on
  required real PostgreSQL. Semantically pre-arm or zero-mutation negatives
  assert no sequence-1 checkpoint, item success/skipped projection, job success,
  target-chain advance, forbidden target I/O or plaintext locator leak.
  Unresolved-outcome cases instead assert no current-item success/adoption
  checkpoint, exactly one terminal `operation_unresolved` checkpoint for the
  current item, preservation of earlier valid checkpoints and prior-item
  success/skipped facts, an empty current-item `next_target_revision`, and no
  current-item target-chain advancement; the unresolved checkpoint may itself
  be sequence 1 for the first operation. Reconcile the exact 9 + 55 + 81 = 145
  manifest before requesting reviews; no narrower pass can close Task 6.

- [ ] **Write RED latch/mutation barriers.** Every mutating TargetPort fake must
  observe the fixed use-latch row committed plus current job/attempt/node permit
  before its first call, including directory/marker creation and cleanup. Crash
  after latch/before first byte leaves the permanent latch and a reconcilable
  workspace; no test path may observe remote mutation without the latch.
- [ ] **Write RED mutation-aware revision chain.** Revalidate source/target/
  fences before and after every operation. Authorized changes advance only from
  expected prior revision + operation digest + current fences + verified target
  identity. External drift before arm takes the supersede transaction; drift or
  ambiguity after arm preserves evidence and enters `needs_attention`. Crash
  after remote write/before checkpoint may exact-verify/adopt once or fail
  closed, never blind replay or false drift.
- [ ] **Write RED item/evidence projections.** Persist per-item success/failed/
  skipped, bytes, exact lowercase SHA-256 content identity + byte-count fidelity
  verification and stable failure category. Task 6 does not claim mtime/mode/
  owner/MIME/metadata fidelity; that requires a future source-freeze and target-
  observation contract amendment. Before mutation arm, an invalid verification
  product follows the existing pre-arm/zero-mutation exact zero-effect contract.
  After mutation arm, any `verification_mismatch` appends exactly one terminal
  `operation_unresolved` (which may be sequence 1) and atomically projects current item
  `failed/remote_outcome_unresolved`, job
  `needs_attention/remote_outcome_unresolved`, failure evidence, attempt closure
  and lease release; it never degrades, publishes, advances the target chain or
  projects success.
- [ ] **Write RED cancellation boundaries.** Draft/preflight/queued cancellation
  has no target side effect; cancellation after writes is best effort, stops new
  mutations and preserves a truthful partial-state report.
- [ ] **Write RED conflict matrix.** `fail_on_conflict` writes nothing;
  `skip_existing` and `overwrite_selected` affect only frozen selections;
  `exact_mirror` has the frozen delete-set digest, pauses at
  `delete_authority_required`, performs fresh node/root/fence/target validation,
  accepts only the one-use delete grant from Task 5, consumes it in the second
  checkpoint after a transient client-retained-secret hash match, then deletes.
  Crash/lost handoff requires receipt replay to re-present the secret; no raw
  bearer is read from DB. Missing/stale/reused/cross-category/mismatched-secret
  grant deletes nothing and reaches the defined failed/needs-attention terminal.
- [ ] **Keep in-place authority separate.** An isolated job cannot be toggled
  into in-place mode. In-place work creates a new plan, preflight and write
  authority with its own target/operation/delete/security revisions and
  idempotency intent. Task 7 owns the implementation and proof that it can never
  create a `RecoveryResultRef`.
- [x] **F4 prove the Task-6-owned preallocated workspace, reservation, deadline
  and cleanup-only boundary.** Execute
  inserts an isolated job at `workspace_phase=none` with the preallocated
  generic-encrypted `jobs/<opaque>` locator and immutable
  `WorkspaceBindingDigest`, while marker/owner/fence/deadline stay empty.
  `PrepareFirstWrite` locks that exact identity and reuses it for
  `none -> reserved`, adding HMAC marker binding, owner/fence and immutable
  deadline without rewrite or reallocation. Only then may `CreateOwnedJobDir`
  create mode 0700 and marker under latch/current fences; an unexpected remote
  directory fails closed and temp names are never persisted.
  Task 6 proves only that `succeeded|degraded` may seal for later Task 7
  publication and that `failed|canceled|needs_attention` becomes cleanup-only;
  publication and Content revalidation remain Task 7.
- [ ] **Implement restart reconciliation.** New fences resume only validated
  idempotent/checkpointed work; ambiguous non-idempotent writes verify or become
  `needs_attention`, never blind replay. This Task 6 row owns bounded adoption/
  reconciliation semantics only. Task 8 owns startup/listener ordering and the
  managed lifecycle, including when long restores begin.
- [x] Run recovery package normal/race suites with repeated two-worker barriers.
- [x] **Obtain whole Task 6 specification review.** Review F6, F3, every bounded
  B1/B2 evidence closure, the Task-6-owned F4 boundary, all twenty corrections
  and every ownership split as one Task 6 product. The B3 receipt is focused
  Correction 14 evidence only and cannot satisfy this whole-task review.
- [x] **Obtain whole Task 6 quality review.** Require no open Critical/Important
  finding across the complete Task 6 delta and evidence chronology. The
  controller-inline B3 review has no findings at its focused scope; the local B3
  reviewer rerun that could not link because of host disk quota is neither a pass
  nor a failure and cannot satisfy this whole-task review.
- [x] **Run and record every final Task 6 gate before Task 7.** Run all frozen
  selectors and affected regressions, the declared deterministic race/repetition
  sets, and every required real-PostgreSQL selector with a usable DSN and no skip.
  Require clean `go vet`, owned `gofmt -d`, `git diff --check`, frozen-selector
  and frozen-path checks, exact manifest parsing at
  `9 + 55 + 81 = 145` unique/disjoint paths, and staged paths zero. No focused
  B3 or bounded B1/B2/F3/F4/F6 pass substitutes for this complete gate.

### Task 7: ResultSet Lifecycle And RecoveryResult Content Arm

**Files:** recovery result lifecycle/delivery files/tests; Content contracts,
broker, ticket, audit, reconciler and behavior tests; backup content handler
tests.

- [ ] **Write RED resolver matrix.** Only an owned regular file or verification
  report under an isolated, terminal `succeeded|degraded`, atomically published
  job tree resolves from
  `RecoveryResultRef{RecoveryJobID,ResultID}`. Directory, path-like ID,
  in-place source, unpublished/partial workspace, symlink/hardlink/special,
  missing, dual resource, stale fence, marker mismatch and wrong job collapse to
  safe closed products.
- [ ] **Write RED issuance/read matrix.** Require Admin recover permission,
  exact job ownership and fresh `recovery.result_download`; `asset.download` is
  neither sufficient nor additionally required. Reuse Content Range, request/
  byte/in-flight budgets, trusted-proxy policy, cookies, logout revoke, drain
  and redacted audit without weakening the backup-asset arm. Ticket issue,
  reauthorize and open each revalidate terminal job + publication revision +
  ready ResultSet + current cleanup fence.
- [ ] **Write RED publication/partial-disposition tests.** The public ResultSet
  union remains four states; no WIP row exists. Persist workspace deadline before
  first byte, never register temp names, and atomically publish ready + verified
  regular-file/report rows only for isolated succeeded/degraded terminal jobs.
  Failed/canceled/needs-attention partials are cleanup-only and Content cannot
  observe them. Crash before/after the publish transaction has no partial rows.
- [ ] **Write RED lifecycle tests.** `ready -> revoking -> cleaned`, current-owner
  `revoking -> cleanup_failed`, `cleanup_failed -> revoking`, plus expired-owner
  `revoking -> revoking` takeover with a new attempt/fence; retain requires exact
  `recovery.result_retain`, only extends within hard cap and cannot revive.
  Cleanup persists owner/lease/node-fence and every phase.
- [ ] **Write RED cleanup node-lease/order/race matrix.** Candidate reads do not
  lock ResultSet. Claim transaction locks job, acquires fresh node-wide cleanup
  lease/fence, then locks/CASes ResultSet or unpublished workspace; a lost CAS
  releases the lease. Renew through revoke/drain/validate/delete/tombstone and
  release on every current-owner outcome. Race active/new Recovery and ordinary
  Task writers, lose the lease before/during delete, retry a busy node without
  starving later nodes, and prove old cleanup/node fences perform zero new
  remote/DB mutation.
- [ ] **Write crash/restart/orphan tests.** Crash before/after claim, revoke,
  drain, validate, delete-start, delete and tombstone—including before
  `cleanup_failed` can be written—then take over after expiry with a fresh node
  lease/fence and resume from durable phase. Old streams fail; invalid/unknown/
  unmatched markers quarantine without deletion; job outcome never changes.
- [ ] **Implement and run Content + Recovery behavior on both engines.** Logout
  fan-out attempts all products and returns one sanitized aggregate.
- [ ] **Keep directory delivery source-bound.** A directory result delegates to
  the existing persistent ExportJob using the original frozen source selection;
  it is never dynamically archived from the remote plaintext tree.

### Task 8: Managed Runtime, Settings And Metrics

**Files:** recovery runtime files/tests, runtime/admission files, main/tests,
settings/config handler transition files/tests, settings service/tests, recovery
metrics/tests and CI workflow.

- [x] **Write RED graph lifecycle tests.** Optional graph installs once only
  after metadata reconciliation; nil/duplicate publication fails; startup does
  not execute queued restores; `StopAccepting` is sticky; shutdown unpublishes,
  stops claims, cancels/joins, fences attempts, revokes/drains delivery and joins
  cleanup within deadline.
- [x] **Write RED wake/retry tests.** Job creation wakes the worker; retries use
  their own bounded deadline scheduler; cleanup uses durable keyset/high-water
  scheduling; none depend solely on a long GC cadence.
- [x] **Write RED receipt-reaper ownership tests.** Runtime starts one bounded
  receipt owner after metadata reconciliation, keeps it running while new
  Recovery admission is disabled, and cancels/joins it before schema drain.
  Restart plus old protected/normal/latch rows must still reach later eligible
  receipts through the stateless indexed predicate; no mutable timestamp cursor,
  ordinary evidence mutation or latch mutation is allowed.
- [x] **Write RED settings transitions.** validate -> drain -> persist -> install
  and rollback preserve the old graph on failure. Global disabled prevents new
  plans/security overrides/authorities/jobs/writes while result revoke/cleanup
  and unpublished/orphan reconciliation continue. Retained plaintext is allowed
  while disabled but remains downgrade-unready. Cover BatchUpdate, delete/reset
  and config import; target-root locators remain encrypted/redacted.
- [ ] **Write RED downgrade-readiness/mixed-version matrix.** The current-binary
  Admin operation requires disabled feature, fresh proof, reason and idempotency;
  installs a sticky transition fence; joins mutation workers; and checks zero
  queued/running jobs, unconsumed authorities, leases/attempts, non-cleaned
  ResultSets, Recovery Content grants/tickets/streams and reconciliation backlog.
  It returns pristine-ready only when the use latch is absent; after latch it is
  always `forward_fix_only`. Cover default-disabled new backend, old frontend,
  new frontend/old backend, disable with retained plaintext, restart and
  forbidden pre-Child downgrade without relying on old code to read 000069.
- [ ] **Implement the two runtime transitions.** Same-binary disable leaves the
  lifecycle owner published and running. Downgrade-readiness runs only while
  disabled under the sticky transition generation, returns a bounded blocker
  product/receipt, and never performs schema down itself; latch-present always
  returns forward-fix-only.
- [x] **Add closed-label metrics.** Provider/state/outcome/category only; no
  repository/job/node/user/path/reason/error-string labels.
- [x] **Wire runtime through main.** Router sees narrow recovery facades; no
  handler builds its own graph or reads raw models.

#### Task 8 fail-closed checkpoint (2026-08-14)

**Status:** `in_progress_fail_closed_checkpoint`. Graph lifecycle, worker
wake/retry, receipt reaper ownership, settings transition mechanics,
closed-label metrics, and main/router narrow-facade composition are each
`complete_checked`. Disabled runtime continues bounded cleanup and logical
reconciliation; enabled production publication rejects known-unavailable
authorities instead of synthesizing authority.

The downgrade runtime substrate is implemented: disabled-only execution,
sticky transition fencing, latch-dominant `forward_fix_only`, durable blocker
inspection, and fresh-reconciliation failure as a blocker. The Admin endpoint's
proof, reason, idempotency and receipt contract belongs to Task 9 and has not
executed. Therefore the downgrade-readiness/mixed-version checklist row remains
open, and the two-runtime-transition row receives only partial credit.

Production enablement remains blocked on real owning products for all three
authorities below:

1. `RecoveryPreflightExternalEvidenceAuthority`: current policy/finding,
   overlap and reserve evidence is absent.
2. `RecoveryAuthorityRevalidator`: no complete current policy/finding,
   preflight-freshness and target-root-revision revalidation exists.
3. `RecoveryReconciliationRevisionSource`: no independent durable current
   target-root revision exists.

Frozen plan values, timestamps, locator digests, or clean/false/zero defaults
must not stand in for those authorities. Separately, these parsed and
transition-validated settings still lack a production Recovery domain seam:
`DefaultRootID`, `PreflightTTL`, `MaxSelectionItems`, `MaxLogicalBytes`,
`LeaseRenewMargin`, `ExecutionTimeout`, `VerificationTimeout`, and
`OrphanQuarantineLimit`. Wiring them requires a focused amendment to the owning
plan/preflight/worker/executor/reconciliation contracts; silently ignoring them
or copying private constants is forbidden.

Four existing Recovery authorization routes were registered only so the Task 8
facade has real consumers and its `401`/`403`/`503` boundary is testable. This
does not execute Task 9's complete route, response, audit/privacy, or Swagger
matrix. Tasks 9--10 remain `not_executed`. Do not stage, commit, push, create a
PR, mark Task 8 complete, or claim Task 9/10 completion at this checkpoint.

#### Task 8 production-authority completion implementation plan (2026-08-14)

> **For agentic workers:** execute one slice at a time with
> `superpowers:test-driven-development`; after each slice, stop for controller
> review. Do not stage or commit a partial slice. The final whole-scope check is
> owned by a fresh `trellis-check` agent.

**Goal:** replace the three known-unavailable production authorities with one
current Recovery eligibility owner, consume every approved Task 8 setting in
its owning domain, and enable managed Rsync without weakening disabled-runtime
maintenance or downgrade safety.

**Architecture:** the existing encrypted per-node/root registry advances to a
strict document v2. `TargetRootAuthorityService` owns fresh registration probes
and root mutations; one `RecoveryEligibilityAuthority` performs short durable
snapshot transaction -> external observation -> short locked revalidation and
adapts to preflight, live-effect and reconciliation contracts. Runtime injects
immutable plan, preflight, worker and reconciliation policies.

**Tech stack:** Go 1.26, GORM, SQLite and PostgreSQL, existing Repository pinned
Rsync source, Processing canonical malware evidence, SSH/SFTP Recovery target
ports, zerolog/Prometheus closed boundaries.

##### Focused file-manifest amendment

The historical `9 + 55 + 81 = 145` union remains the baseline. Complete
security authority needs the Processing runtime owner that already validates
canonical malware artifacts. Add exactly these two tracked modify paths:

```text
backend/internal/backupasset/runtime/processing_runtime.go
backend/internal/backupasset/runtime/processing_runtime_test.go
```

The focused union was initially `9 + 55 + 83 = 147`. Source-namespace research
proved that a purpose-exact external observer cannot truthfully live inside the
existing Repository query files. Add exactly these two focused create paths:

```text
backend/internal/backupasset/recovery/source_namespace.go
backend/internal/backupasset/recovery/source_namespace_test.go
```

The corrected union is `9 + 55 + 85 = 149`. No model, migration, workflow or
frontend path is added. The task-local authority research files remain
evidence, not product-manifest members. Any further path requires another
written amendment before edit.

Final V1 reconciliation found that B4--B6's approved closed-owner split was
described by responsibility but its eight concrete product/test paths were not
copied into this ledger. Add these seven create paths and one tracked modify
path; they are the Recovery owner/target ports, Repository constructor/tx seam
and runtime adapters already required by the completed B4--B6 design:

```text
backend/internal/backupasset/recovery/eligibility.go
backend/internal/backupasset/recovery/eligibility_test.go
backend/internal/backupasset/recovery/eligibility_target.go
backend/internal/backupasset/recovery/eligibility_target_test.go
backend/internal/backupasset/repository/service_test.go
backend/internal/backupasset/runtime/recovery_eligibility.go
backend/internal/backupasset/runtime/recovery_eligibility_test.go
backend/internal/backupasset/repository/service.go
```

The final exact union is therefore `9 current + 64 create + 84 modify = 157`
unique, disjoint paths. This is a ledger correction for already reviewed Task 8
authority code, not new product scope; it adds no model, migration, route,
workflow, dependency or frontend path. Further product paths still require a
written amendment before edit.

##### T8-A: encrypted root v2 and target-root authority

**Files:**

- Modify: `backend/internal/settings/service.go`
- Modify: `backend/internal/settings/service_test.go`
- Modify: `backend/internal/backupasset/recovery/target.go`
- Modify: `backend/internal/backupasset/recovery/target_test.go`
- Modify: `backend/internal/backupasset/recovery/service.go`
- Modify: `backend/internal/backupasset/recovery/service_test.go`

- [x] **Step A1: write genuine RED registry-v2 tests.** Add table-driven tests
  named `TestRecoveryTargetRootV2RejectsIncompleteAuthority`,
  `TestRecoveryTargetRootV2RotationSemantics`, and
  `TestRecoveryTargetRootV2ConcurrentMutation`. Freeze this contract before
  implementation:

  ```go
  type RecoveryTargetRootPolicy struct {
      ReserveBytes         int64 `json:"-"`
      ReserveInodes        int64 `json:"-"`
      OverlapPolicyBinding string `json:"-"`
  }

  type RecoveryTargetRootDefinition struct {
      NodeID                  uint
      RootID                  string
      SafeLabel               string
      Locator                 string `json:"-"`
      AuthorityRevision       string `json:"-"`
      RootObservationRevision string `json:"-"`
      Policy                  RecoveryTargetRootPolicy `json:"-"`
  }
  ```

  Assert document `schema_version: 1`, missing/duplicate/unknown fields,
  `enc:v1:`, tamper and substituted key/payload return only
  `ErrRecoveryTargetRootUnavailable`. Exact replay and safe-label-only update
  preserve `AuthorityRevision`; locator, observation, reserve or policy change
  requires a different revision.

- [x] **Step A2: run RED and record the failure.** From `backend/` run:

  ```bash
  go test ./internal/settings \
    -run '^TestRecoveryTargetRootV2(RejectsIncompleteAuthority|RotationSemantics|ConcurrentMutation)$' \
    -count=1
  ```

  Expected: FAIL because the current document is schema 1 and has no authority,
  observation or policy fields.

- [x] **Step A3: implement strict registry v2.** Set the document schema to 2,
  keep the ciphertext envelope at `enc:v2:`, strictly encode/decode every field,
  and extend `RecoveryTargetRootResolution` with the private revisions/policy.
  `RegisterRecoveryTargetRootTx` accepts only a complete Recovery-issued
  definition; it never generates or infers missing authority. Keep safe-list
  DTOs unchanged and retain the exact internal-key/config-export exclusions.

- [x] **Step A4: write RED target-authority service tests.** Add
  `TestTargetRootAuthorityServiceRequiresFreshReadOnlyProbe` and
  `TestTargetRootAuthorityServiceRegisterRotateDelete` using this narrow seam:

  ```go
  type TargetRootRegistrationProbe interface {
      ObserveRecoveryTargetRoot(
          context.Context,
          TargetRootRegistrationRequest,
      ) (TargetRootRegistrationObservation, error)
  }

  type TargetRootAuthorityServiceDependencies struct {
      DB          *gorm.DB
      Registry    RecoveryTargetRootRegistry
      Probe       TargetRootRegistrationProbe
      NewRevision func() (string, error)
      Now         func() time.Time
  }
  ```

  Test read-only purpose, stale observation, node/credential drift, exact replay,
  safe-label update, security rotation, delete with zero remote mutation,
  persistence failure and sanitized dependency errors.

- [x] **Step A5: implement the minimal owner and rerun normal/race tests.** The
  owner performs probe outside the transaction, then locks node, credential and
  exact root row before persistence. Use `backupasset.NewOpaqueID` for a new
  security revision and preserve the old revision only for exact/security-
  equivalent updates.

  ```bash
  go test ./internal/settings ./internal/backupasset/recovery \
    -run 'RecoveryTargetRootV2|TargetRootAuthorityService' -count=1
  go test -race ./internal/settings ./internal/backupasset/recovery \
    -run 'RecoveryTargetRootV2|TargetRootAuthorityService' -count=1
  ```

  Expected: PASS; recognizable locator, revision, policy and raw-error canaries
  are absent from every error/format/JSON boundary.

- [x] **Step A6: run required PostgreSQL parity and stop.** Use the existing
  command-scoped secret-preserving DSN harness:

  ```bash
  REQUIRE_POSTGRES_RECOVERY_TEST=1 TEST_POSTGRES_DSN="$C13_PG_DSN" \
    go test ./internal/backupasset/recovery \
      -run '^TestRecoveryTargetRootAuthorityPostgres$' -count=1
  REQUIRE_POSTGRES_RECOVERY_TEST=1 TEST_POSTGRES_DSN="$C13_PG_DSN" \
    go test -race ./internal/backupasset/recovery \
      -run '^TestRecoveryTargetRootAuthorityPostgres$' -count=1
  ```

  Expected: PASS with no skip and no schema/role residue. Append RED/GREEN and
  command evidence, then stop before T8-B.

##### T8-B: one eligibility owner and three production adapters

**Files:**

- Modify: `backend/internal/backupasset/runtime/processing_runtime.go`
- Modify: `backend/internal/backupasset/runtime/processing_runtime_test.go`
- Modify: `backend/internal/backupasset/provider/contracts.go`
- Modify: `backend/internal/backupasset/provider/contracts_test.go`
- Modify: `backend/internal/backupasset/repository/query.go`
- Modify: `backend/internal/backupasset/repository/query_test.go`
- Modify: `backend/internal/backupasset/recovery/preflight.go`
- Modify: `backend/internal/backupasset/recovery/preflight_test.go`
- Modify: `backend/internal/backupasset/recovery/service.go`
- Modify: `backend/internal/backupasset/recovery/service_test.go`
- Modify: `backend/internal/backupasset/recovery/worker.go`
- Modify: `backend/internal/backupasset/recovery/worker_test.go`
- Modify: `backend/internal/backupasset/recovery/executor.go`
- Modify: `backend/internal/backupasset/recovery/executor_test.go`
- Modify: `backend/internal/backupasset/runtime/recovery_runtime.go`
- Modify: `backend/internal/backupasset/runtime/recovery_runtime_test.go`

- [x] **Step B1: expose one private current Processing observation.** Add RED
  tests `TestProcessingRuntimeRecoverySecurityObservationBindsCanonicalEvidence`
  and `TestProcessingRuntimeRecoverySecurityObservationRejectsIncompleteEvidence`.
  The runtime-private product is:

  ```go
  type managedProcessingRecoverySecurityObservation struct {
      PolicyRevision   string
      FindingSetDigest string
      ScanState        capabilityspec.ScanState
      Complete         bool
  }
  ```

  Derive the finding-set digest from the exact asset/source binding, canonical
  decoded malware result, signature-bundle fingerprint and policy revision with
  a domain-separated length-framed digest. Do not change public preview DTOs.

- [x] **Step B2: expose a Repository-owned Rsync source authority.** Add RED
  tests proving it resolves the existing scalar `RsyncRestoreSourceRef`, opens
  the pinned declared tree, calls `Revalidate`, derives exact capability/source
  namespace evidence, compares overlap only when node identities match, closes
  the source on every path, and never returns a locator. Restic/Rclone return
  `backupasset.ErrCapabilityUnavailable` through this authority.

- [x] **Step B3: run the two owner RED selectors.** From `backend/` run:

  ```bash
  go test ./internal/backupasset/runtime ./internal/backupasset/repository \
    -run 'RecoverySecurityObservation|RecoveryRsyncSourceAuthority' -count=1
  ```

  Expected: FAIL because neither detailed current evidence seam exists.

- [x] **Step B3a: add the purpose-exact source-namespace observer.** Create
  `recovery/source_namespace.go` and its colocated test. First write RED tests
  proving short durable capture -> external SSH/SFTP observation -> locked
  revalidation; exact `recovery_preflight` read-only purpose; strict known-host
  identity with accept-new/insecure posture unavailable; per-component
  `Lstat`/`RealPath` canonicalization; symlink, non-directory, node, credential,
  Task/source and binding drift; cancellation; every close path; and fully
  redacted private products. Repository contributes only pinned-source and
  producing-task provenance. Do not hash `Task.RsyncSource`, return a locator or
  label unequal digests disjoint.

  The Recovery eligibility owner compares source and fresh target private
  canonical namespaces only after exact authenticated node identity equality,
  using bidirectional component-boundary containment. Missing source/target
  observation, unproved host identity, or any drift remains capability
  unavailable/conflict; Restic/Rclone stop before either observer.

  Run the observer RED/GREEN selector before B4:

  ```bash
  go test ./internal/backupasset/recovery ./internal/backupasset/repository \
    -run 'Recovery(SourceNamespaceAuthority|RsyncSourceAuthority)' -count=1
  go test -race ./internal/backupasset/recovery ./internal/backupasset/repository \
    -run 'Recovery(SourceNamespaceAuthority|RsyncSourceAuthority)' -count=1
  ```

  Expected GREEN: managed Rsync exposes a complete source product only when
  both exact pinned access and the authenticated namespace observation exist;
  every unavailable arm returns a closed product and zero target access.

- [x] **Step B4: define the sealed eligibility contract.** In Recovery, add
  one owner with source, security, target-root and target-observation ports.
  Keep proof fields private and redacted:

  ```go
  type RecoveryAuthorityObservation struct {
      observedAt time.Time
      expiresAt  time.Time
      binding    recoveryEligibilityBinding
      proof      *recoveryEligibilityProof
  }

  type RecoveryAuthorityRevalidator interface {
      ObserveRecoveryAuthority(
          context.Context,
          RecoveryAuthorityBinding,
      ) (RecoveryAuthorityObservation, error)
      RevalidateRecoveryAuthorityTx(
          context.Context,
          *gorm.DB,
          RecoveryAuthorityBinding,
          RecoveryAuthorityObservation,
      ) error
  }
  ```

  `RecoveryEligibilityAuthority` also implements the existing preflight and
  reconciliation interfaces. Preflight returns only the existing closed scalar
  observation. Reconciliation returns node/credential/authority revisions;
  marker root observation stays a separate scan binding.

- [x] **Step B5: write the complete RED matrix.** Add tests for source,
  capability, policy, finding, root authority, root observation, node,
  credential, overlap and reserve substitution/unavailable/drift. Each test
  mutates its durable owner after observation and before locked revalidation.
  Assert Restic/Rclone, partial success, request echo and zero defaults all
  produce unavailable/conflict with zero target mutation.

- [x] **Step B6: implement two-phase call sites.** Authorization, worker claim,
  execute and exact-delete obtain `RecoveryAuthorityObservation` before opening
  their mutation transaction, then pass it to the locked revalidator. Never
  run Repository, Processing, SSH or SFTP work inside that transaction. Keep
  existing per-operation source/target revalidation after commit.

- [x] **Step B7: run focused normal/race and PostgreSQL gates.** Run:

  ```bash
  go test ./internal/backupasset/processing ./internal/backupasset/repository \
    ./internal/backupasset/recovery ./internal/backupasset/runtime \
    -run 'Recovery(SecurityObservation|RsyncSourceAuthority|EligibilityAuthority|AuthorityRevalidation|ReconciliationRevision)' \
    -count=1
  go test -race ./internal/backupasset/repository ./internal/backupasset/recovery \
    ./internal/backupasset/runtime \
    -run 'Recovery(RsyncSourceAuthority|EligibilityAuthority|AuthorityRevalidation|ReconciliationRevision)' \
    -count=1
  REQUIRE_POSTGRES_RECOVERY_TEST=1 TEST_POSTGRES_DSN="$C13_PG_DSN" \
    go test ./internal/backupasset/recovery \
      -run '^TestRecoveryEligibilityAuthorityPostgres$' -count=1
  REQUIRE_POSTGRES_RECOVERY_TEST=1 TEST_POSTGRES_DSN="$C13_PG_DSN" \
    go test -race ./internal/backupasset/recovery \
      -run '^TestRecoveryEligibilityAuthorityPostgres$' -count=1
  ```

  Expected: PASS/no-skip with raw source, malware, locator, credential and error
  canaries absent. Record evidence and stop before T8-C.

##### T8-C: owning policies, heartbeat and absolute deadline

**Files:**

- Modify: `backend/internal/backupasset/service.go`
- Modify: `backend/internal/backupasset/service_test.go`
- Modify: `backend/internal/settings/service.go`
- Modify: `backend/internal/settings/service_test.go`
- Modify: `backend/internal/backupasset/recovery/service.go`
- Modify: `backend/internal/backupasset/recovery/service_test.go`
- Modify: `backend/internal/backupasset/recovery/preflight.go`
- Modify: `backend/internal/backupasset/recovery/preflight_test.go`
- Modify: `backend/internal/backupasset/recovery/worker.go`
- Modify: `backend/internal/backupasset/recovery/worker_test.go`

- [x] **Step C1: write RED config and constructor tests.** Freeze these domain
  policies and require positive bounded values:

  ```go
  type PlanPolicy struct {
      MaxSelectionItems int64
      MaxLogicalBytes   int64
  }
  type PreflightPolicy struct { TTL time.Duration }
  type WorkerPolicy struct {
      LeaseRenewMargin time.Duration
      ExecutionTimeout time.Duration
  }
  type ReconciliationPolicy struct { FindingLimit int }
  ```

  Remove `DefaultRootID` and `VerificationTimeout`; rename
  `OrphanQuarantineLimit` to `ReconciliationFindingLimit` in `RecoveryConfig`,
  registry definitions, fixtures and transition snapshots. Config import must
  reject all three old names.

- [x] **Step C2: run RED config tests.** Run:

  ```bash
  go test ./internal/settings ./internal/backupasset \
    -run 'Recovery(Config|Policy|RemovedSetting|ReconciliationFindingLimit)' \
    -count=1
  ```

  Expected: FAIL on current fields and inert parsed values.

- [x] **Step C3: inject plan and preflight policies.** `PlanService` rejects
  selection/estimated impact above the lower dynamic and immutable hard cap
  before persistence and repeats the check against materialized rows.
  `PreflightService` replaces caller TTL with `now + policy.TTL` and validates
  the same value before commit; no request field can lengthen it.

- [x] **Step C4: inject worker and reconciliation policies.** Source lease
  creation freezes `AbsoluteDeadline` as the earliest of
  `now + ExecutionTimeout`, grant expiry and any existing hard authority bound.
  The managed worker starts one claim-scoped heartbeat scheduled from
  `LeaseExpiresAt - LeaseRenewMargin`; success atomically propagates the renewed
  source/node/attempt deadline, while failure cancels the claim context and
  makes the old fence unusable before another target call. Pass
  `ReconciliationFindingLimit` into every reconciliation permit without
  changing immutable chain/cursor caps.

- [x] **Step C5: run focused normal/race tests.** Run:

  ```bash
  go test ./internal/settings ./internal/backupasset \
    ./internal/backupasset/recovery ./internal/backupasset/runtime \
    -run 'Recovery(PlanPolicy|PreflightPolicy|WorkerPolicy|Heartbeat|ExecutionDeadline|ReconciliationFindingLimit|RemovedSetting)' \
    -count=1
  go test -race ./internal/backupasset/recovery ./internal/backupasset/runtime \
    -run 'Recovery(Heartbeat|ExecutionDeadline|ReconciliationFindingLimit)' \
    -count=1
  ```

  Expected: PASS; a failed heartbeat causes cancellation and zero subsequent
  target calls, and timeout preserves post-arm/unresolved semantics.

- [x] **Step C6: run static removal checks and stop.** From repo root run:

  ```bash
  ! rg -n 'DefaultRootID|VerificationTimeout|OrphanQuarantineLimit|recovery\.default_root_id|recovery\.verification_timeout|recovery\.orphan_quarantine_limit' \
    backend/internal/backupasset backend/internal/settings
  rg -n 'PreflightTTL|MaxSelectionItems|MaxLogicalBytes|LeaseRenewMargin|ExecutionTimeout|ReconciliationFindingLimit' \
    backend/internal/backupasset backend/internal/settings
  git diff --check
  ```

  Expected: the first scan is empty, every retained setting has an owning
  product reference, and diff check passes. Record evidence and stop before
  T8-D.

##### T8-D: production composition, transitions and maintenance continuity

**Files:**

- Modify: `backend/internal/backupasset/runtime/recovery_runtime.go`
- Modify: `backend/internal/backupasset/runtime/recovery_runtime_test.go`
- Modify: `backend/internal/backupasset/runtime/runtime.go`
- Modify: `backend/internal/backupasset/runtime/runtime_test.go`
- Modify: `backend/internal/backupasset/recovery/metrics.go`
- Modify: `backend/internal/backupasset/recovery/metrics_test.go`
- Modify: `backend/cmd/server/main.go`
- Modify: `backend/cmd/server/main_test.go`
- Modify: `backend/internal/api/router.go`
- Modify: `backend/internal/api/router_test.go`
- Modify: `backend/internal/api/backup_asset_rbac_test.go`

- [x] **Step D1: write RED production-composition tests.** Add tests proving a
  complete Repository/Processing/target-root authority publishes exactly once
  after metadata reconciliation; known-unavailable or nil authorities publish
  nothing. A dependency that becomes unavailable after publication must close
  preflight and every effect without unpublishing maintenance facades.

- [x] **Step D2: replace production unavailable adapters.** Construct one
  `RecoveryEligibilityAuthority`, inject it through all three narrow adapters,
  and keep unavailable fakes only in explicit negative tests. Compose
  `TargetRootAuthorityService` behind a narrow runtime facade for Task 9; do not
  register the Task 9 root routes in this slice.

- [x] **Step D3: complete transition fault tests.** Inject failure at validate,
  drain, persist, construct and install. Prove the prior persisted config and
  graph are restored before admission reopens; if restoration is injected to
  fail, the sticky fence stays closed and downgrade readiness is blocked.
  Cover fresh-disabled startup, enabled-to-disabled transition, restart,
  re-enable, no-latch pristine clear and latch-dominant `forward_fix_only`.

- [x] **Step D4: preserve disabled owners and closed metrics.** Disabled graphs
  run cleanup, logical reconciliation and receipt reaping immediately and join
  them before schema drain. Metrics use only fixed provider/state/outcome/
  category labels; root/node/plan/job IDs, revisions, locators and raw errors
  are forbidden.

- [x] **Step D5: run composition normal/race tests.** Run:

  ```bash
  go test ./cmd/server ./internal/api ./internal/backupasset/runtime \
    ./internal/backupasset/recovery \
    -run 'Recovery(ProductionAuthority|Runtime|Transition|Disabled|Downgrade|Metrics|RBAC)' \
    -count=10
  go test -race ./cmd/server ./internal/api ./internal/backupasset/runtime \
    ./internal/backupasset/recovery \
    -run 'Recovery(ProductionAuthority|Runtime|Transition|Disabled|Downgrade)' \
    -count=10
  ```

  Expected: PASS with stable lifecycle ordering and zero leaked canaries.
  Record evidence and stop before T8-V1.

##### T8-V1: fresh whole-scope quality and completion decision

- [x] **Step V1: run whole backend normal/race gates.** From `backend/` run:

  ```bash
  go test ./... -count=1
  go test -race ./internal/backupasset/processing \
    ./internal/backupasset/repository \
    ./internal/backupasset/recovery \
    ./internal/backupasset/runtime \
    ./internal/api ./cmd/server -count=1
  go vet ./...
  ```

  Expected: PASS.

- [x] **Step V2: run required PostgreSQL normal/race with no skip.** Run the
  complete Recovery selector under `REQUIRE_POSTGRES_RECOVERY_TEST=1` using the
  existing disposable PostgreSQL harness; do not print its password or DSN:

  ```bash
  REQUIRE_POSTGRES_RECOVERY_TEST=1 TEST_POSTGRES_DSN="$C13_PG_DSN" \
    go test ./internal/backupasset/recovery -run 'Postgres' -count=1
  REQUIRE_POSTGRES_RECOVERY_TEST=1 TEST_POSTGRES_DSN="$C13_PG_DSN" \
    go test -race ./internal/backupasset/recovery -run 'Postgres' -count=1
  ```

  Expected: PASS, no skip and zero schema/role/container/volume residue.

- [x] **Step V3: run project, privacy and structural gates.** From repo root:

  ```bash
  make lint-backend
  make backend-build
  make check
  git diff --check
  test -z "$(find backend/internal/database/migrations -type f -name '000070_*' -print -quit)"
  python3 ./.trellis/scripts/task.py validate \
    .trellis/tasks/07-28-backup-assets-controlled-recovery
  python3 ./.trellis/scripts/task.py validate \
    .trellis/tasks/07-12-backup-data-explorer-design
  jq -s empty \
    .trellis/tasks/07-28-backup-assets-controlled-recovery/implement.jsonl \
    .trellis/tasks/07-28-backup-assets-controlled-recovery/check.jsonl
  test -z "$(git diff --cached --name-only)"
  ```

  Also run the established forbidden direct-log, private-canary, migration,
  manifest-disjointness and protected-user-file scans. Expected: every command
  passes; the manifest union is exactly 157 and the user's main-worktree
  `.codex/agents/trellis-research.toml` remains outside this worktree delta.

- [x] **Step V4: dispatch one fresh full-scope `trellis-check`.** Require zero
  open Critical or Important defect across sections 48.1--48.7, runtime
  lifecycle, settings transitions, SQLite/PostgreSQL parity, privacy and Task 9
  scope separation. Fix findings in their owning slice and rerun affected plus
  whole gates.

- [x] **Step V5: record the completion truth and stop before delivery.** Append
  exact commands, durations, results and reviewer receipt to
  `research/implementation-evidence.md`. Mark Task 8 complete only if every
  checkbox above and the full review pass. Tasks 9--10 remain `not_executed`;
  do not stage, commit, push or create a PR until the controller separately
  enters Phase 3.3/3.4.

##### Focused planning convergence

| Requirement | Technical design | Execution owner |
|---|---|---|
| encrypted root v2, independent authority/observation revisions, no `000070` | sections 48.1--48.2, 48.5--48.7 | T8-A, T8-V1 |
| one complete eligibility owner and three narrow adapters | sections 48.3, 48.5--48.6 | T8-B, T8-D |
| managed Rsync exact source; Restic/Rclone unavailable | sections 48.3--48.4, 48.6 | T8-B, T8-D |
| five retained/renamed settings and two removed settings | sections 48.4--48.7 | T8-C, T8-D |
| disabled maintenance and latch-dominant downgrade readiness | sections 48.4--48.7 | T8-D, T8-V1 |
| Task 9 target-root and downgrade Admin route ownership | sections 10, 48.1 | Task 9 route/response/audit/Swagger matrix |

The focused PRD has no unresolved product, scope, compatibility, risk or
acceptance decision. The design contains no placeholder, the 157-path manifest
is exact and disjoint by amendment, and every acceptance row has a TDD slice
plus a whole-scope gate. The approved plan is implemented and whole-scope
checked through T8-V1; stop before delivery or Task 9 until the controller
separately authorizes the next phase.

### Task 9: API, RBAC, Audit And Swagger

**Files:** recovery handler/tests, router/tests/docs, backup-asset RBAC test,
settings/config handler transition files/tests and backup content handler/tests.

- [ ] **Write RED route matrix** for every route in `design.md` §10: Auth,
  Admin `backup_assets:recover`, ownership, expected revision, idempotency,
  correct proof purpose, registered audit and safe disabled behavior. Assert the
  explicit security-override, write-authority and exact-mirror-delete-authority
  routes exist and no undifferentiated authority route exists; cover the Admin downgrade-
  readiness settings route under the same permission and transition owner.
  Cover the exact Admin target-root register, rotate, delete and node-scoped
  safe-list routes; all mutations require step-up/idempotency and delegate to
  the Task 8 transition facade rather than generic settings mutation.
- [ ] **Write RED response matrix.** Closed 400/403/404/409/413/429/500/503
  mapping uses response helpers; unexpected errors expose no DB/SSH/Provider/
  path/reason detail. Hidden objects do not leak existence.
- [ ] **Write RED audit/privacy tests.** Only opaque stable IDs, action, stage,
  authority/security category, sanitized outcome and counts/bytes. Seed raw
  source locator, configured root, target path and separate reasons; prove none,
  nor credentials, proofs, grants, tickets, marker material or command output,
  enters API, Swagger, audit, logs, metrics or failure evidence.
- [ ] **Implement thin handlers.** Parse/map/respond only; all ownership,
  transitions and transactions stay in Recovery service.
- [ ] **Regenerate Swagger and verify truth.** Required target-mode, security-
  decision and authority enums, bindings, security and all error products match
  live routes. Locator/root/path ciphertext and internal digests/phases are
  absent; generated `docs.go` is the only generated API-doc path.

```bash
make swag-init
cd backend
go test ./internal/api ./internal/api/handlers ./internal/middleware \
  -run 'Recovery|BackupContent|BackupAssetRBAC|Settings.*Recovery' -count=1
go test -race ./internal/api ./internal/api/handlers \
  -run 'Recovery|BackupContent' -count=1
```

### Task 10: Typed Frontend Recovery Wizard

**Files:** all frontend paths in §2.2/§2.3.

- [ ] **Write RED API boundary tests.** Raw snake_case DTOs remain private;
  whole-product mapping validates schema version, exact opaque IDs/revisions,
  target mode, operation/delete summary, security decision, authority/checkpoint
  category, times, counts, publication state, URLs and closed errors. Unknown/
  dual/contradictory products reject atomically. Raw source/target/root locator,
  their internal digests/ciphertext and workspace phases are not DTO fields. No
  component calls `fetch`.
- [ ] **Write RED controller tests.** Explicit selection handoff; create,
  preflight, security override, write authority, execute, delete-checkpoint
  authority, poll, cancel, retain and cleanup; endpoint-specific same-key replay
  after network/5xx ambiguity; AbortSignal/timer cleanup; hidden-page pause;
  reload reconciliation; stale-response suppression; separate override/write/
  delete proof/reason/grant and ticket clearing on context/session change with no
  URL/storage persistence.
- [ ] **Write RED client-secret crypto/lifecycle tests.** The API boundary uses
  Web Crypto `getRandomValues` for exactly 32 bytes and canonical 43-character
  unpadded base64url, and fails closed when CSPRNG is absent; `Math.random` and
  server/replay regeneration are forbidden. Network/5xx ambiguity must reuse the
  same endpoint key+secret. The write secret survives only until execute handoff,
  the delete secret only until fenced delete handoff, and each clears after
  definitive consumption or any plan/job/session/context replacement. Assert no
  URL, browser storage, serialized reducer state, log, DOM snapshot or API
  response contains the raw secret, and require both grant expiries no later than
  their receipt replay expiry.
- [ ] **Write RED wizard/panel tests.** Selection -> target -> preflight ->
  security decision/override -> impact -> write reason/step-up -> progress ->
  exact-mirror delete checkpoint/reason/step-up when required -> verification ->
  result actions. Unknown/non-overridable finding never exposes override;
  in-place/partial work never exposes result download. Separate Job outcome from
  ResultSet lifecycle; show drift, partial writes, destructive impact, TTL and
  cleanup failure; bound item/impact DOM while retaining real paging.
- [ ] **Write RED integration tests.** Bulk bar/browser/inspector/workspace and
  route/state carry only opaque IDs and explicit selection. Legacy `latest`/
  default-source restore remains gated and no accidental Start Recovery control
  bypasses plan creation. A new frontend against missing old-backend endpoints
  maps to disabled/unavailable and never falls back; the old page route against
  a new default-disabled backend still exposes no recovery mutation.
- [ ] **Write a11y/i18n/responsive tests.** Keyboard step order, focus transfer/
  restoration, DialogTitle/labels, quiet live regions, distinct security-
  override and delete confirmations, reduced motion, axe, zh/en completeness,
  200% zoom and 1440/1200/390 widths. Update the existing page-level Backups
  axe test and data-page integration rather than creating parallel page shells.
- [ ] Implement minimal GREEN using existing UI primitives and run focused
  tests, then the full frontend gate.

```bash
cd web
env -u NODE_ENV npm run test -- \
  src/lib/api/backup-recovery-api.test.ts \
  src/features/backup-assets/use-backup-recovery.test.tsx \
  src/features/backup-assets/recovery-plan-wizard.test.tsx \
  src/features/backup-assets/recovery-impact-panel.test.tsx \
  src/features/backup-assets/recovery-job-panel.test.tsx \
  src/pages/__tests__/backups-page.a11y.test.tsx
env -u NODE_ENV npm run check
env -u NODE_ENV node scripts/check-bundle-budget.mjs
```

### Task 11: Cross-Engine And Full Verification

- [ ] **Run focused package gates.**

```bash
cd backend
go test ./internal/backupasset/recovery ./internal/backupasset/provider \
  ./internal/backupasset/content ./internal/backupasset/runtime \
  ./internal/api ./internal/api/handlers ./internal/middleware -count=1
go test -race ./internal/backupasset/recovery ./internal/backupasset/provider \
  ./internal/backupasset/content ./internal/backupasset/runtime \
  ./internal/api ./internal/api/handlers -count=1
go test ./internal/database -run 'BackupAssetMigration069' -count=1
```

- [ ] **Rerun every §3.1 finding selector exactly.** Preserve the first RED
  output in implementation evidence, record the final GREEN output separately,
  and run the F1–F8 backend, required PostgreSQL and frontend commands without
  weakening their regex or substituting an aggregate package pass.

- [ ] **Run required real PostgreSQL gates.** Required mode must fail rather
  than skip when `TEST_POSTGRES_DSN` is absent or unreachable.

```bash
cd backend
REQUIRE_POSTGRES_MIGRATION_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" \
  go test ./internal/database \
  -run '^(TestBackupAssetMigration062PostgresApplyDown|TestBackupAssetMigration0(63|64|65|66|67|68|69)Postgres|TestPostgresTimestamptzScanUsesConfiguredUTC|TestRunMigrationsPostgresDirtyCheckUsesSearchPath)$' \
  -count=1
REQUIRE_POSTGRES_RECOVERY_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" \
  go test ./internal/backupasset/recovery \
  -run '^TestRecoveryBehaviorPostgres$' -count=1
```

- [ ] **Run frontend, aggregate, static and build gates.**

```bash
cd /home/murray/code/xirang/web
env -u NODE_ENV npm run check
env -u NODE_ENV node scripts/check-bundle-budget.mjs
env -u NODE_ENV npm audit --audit-level=moderate

cd /home/murray/code/xirang
make backend-test
make backend-build
rm -f backend/xirang-server
test ! -e backend/xirang-server
env -u NODE_ENV make check
rm -f backend/xirang-server
test ! -e backend/xirang-server
bash scripts/check-migration-utc-safety.sh
bash scripts/check-doc-freshness.sh
bash scripts/check-compose-config.sh
bash scripts/check-compose-config.test.sh
bash scripts/test-core-compose.sh
bash scripts/test-core-compose.test.sh
bash scripts/test-asset-worker.test.sh
make docker-build
```

The audit command is an observed-risk comparison, not a false clean-audit
claim. Compare it with the explicitly reviewed residual baseline delivered by
the archived frontend dependency-remediation task; no package manifest is in
this Child's scope. Any new, changed or newly applicable moderate/high finding
must be assessed. Never use `--force`, overrides, peer bypasses or audit
suppression to manufacture a pass.

If local Docker/browser/PostgreSQL execution is externally unavailable, record
the exact fail-closed command and environment error, keep the gate open and
require hosted CI evidence. Environment noise is never relabeled as a pass.
Hosted CI must additionally prove both native amd64/arm64 Worker runtime-closure
artifacts, Worker image build/smoke/scan on each architecture and the amd64
complete Worker Compose profile. A local amd64 image alone cannot substitute
for those matrix rows.

- [ ] **Run two final independent reviews.** One specification/security/data
  review traces every PRD requirement and state/authority boundary. One quality
  review inspects code, tests, races, reuse, privacy, generated truth and exact
  scope. Resolve all Critical/Important findings with new RED→GREEN cycles and
  rerun affected/full gates.

### Task 12: Exact Delivery, Merge And Child-Only Bookkeeping

**Status:** `not_executed`. Task 5's recorded independent-review receipt below
is an approval-ledger fact, not a Task 12 checklist row.

- [ ] **Prove exact dirty union.** Union tracked diff, cached diff and untracked
  files; require exactly 9 Phase 1 + 55 create + 81 modify = 145 paths, no
  duplicates, no overlap and no extra/missing path. Confirm 000069 is exactly
  four paired files, 000070+ absent and `backend/xirang-server` absent.
- [ ] **Stage exact paths only.** Pipe the structurally extracted §2.1-§2.3
  path lists to `git add --pathspec-from-file=-`; never stage a directory or
  wildcard. Compare cached names to the same 145-path set and run
  `git diff --cached --check`.
- [ ] **Create a coherent conventional work commit.** Suggested subject:
  `feat: add controlled backup asset recovery`. Subagent commits are forbidden;
  the controller owns this action.
- [ ] **Push and open one draft PR to `main`.** Include migration/rollback,
  security, PostgreSQL, frontend/a11y, scope and release-disposition evidence.
  Monitor every required check, fix failures on this branch with TDD, push and
  keep monitoring. Mark ready and squash merge only when all required checks
  are green and reviews are closed.
- [ ] **Monitor post-merge automation.** Observe main CI, Release Please and any
  actually triggered release/image/description workflow. This feature PR is not
  expected to publish a formal release or image unless automation proves
  otherwise; record actual truth.
- [ ] **Create a post-merge bookkeeping branch from synchronized `main`.** Update
  the Child delivery ledger with feature head, squash commit, PR, CI and
  post-merge facts; run `task.py archive`; record the journal; validate the
  archived Child while the parent remains `planning`; deliver the bookkeeping
  through its own PR and monitor its CI/post-merge automation.
- [ ] **Resync local main.** Require `main...origin/main = 0 0`, clean status,
  Child 13 archived, parent still planning with mechanical 13/13 instantiated
  children but only 13/15 program deliverables complete. Only then create Child
  14 from the updated main.

## 4. Validation Matrix

| Boundary | Required evidence |
|---|---|
| exact identity | one Repository/RP, canonical AssetRefs, immutable locator digest/observation union, encrypted boundary-only locator, no latest/substitution |
| plan authority | target mode, bounded operation/delete digests, closed security decision, Task 2 plan-create idempotency, Tx revalidation, preflight expiry |
| authorization | immutable evidence-table receipt for all four authorization mutations: requester/endpoint key+full intent, globally consumed proof digest, `proof <= replay <= presenting session`, grant `<= replay`, separate presenting-session binding, exact singleton execute source-lease/effect refs and bounded runtime-owned retention; separate Admin security override/write/delete proof+reason; Web-Crypto-generated hash-only client-secret write/delete one-use category-exact consumption |
| source protection | zero-deadline `recovery_job` leases, persisted returned caps, renew/takeover fence, no source mutation |
| target safety | encrypted root locator, canonical root/device/mount/owner/mode, no symlink/overlap, capacity/inodes, mutation-aware chain, durable node-write exclusion |
| restart item identity | fidelity is exact lowercase SHA-256 content identity + byte count only; create: prior absent, post digest+bytes, prior bytes -1; overwrite: prior present, post digest+bytes; skip: prior present, post digest equals frozen prior digest, post bytes -1, separate prior-target bytes >=0, skipped only; delete: prior present, empty post digest, both byte fields -1, exact absence; no metadata, parallel fidelity field or absence digest |
| target object locator | every schema-v2 row including delete has canonical item locator + `SemanticTargetDigest`; execute preallocates isolated `jobs/<opaque>` and distinct final `TargetObjectDigest`; locked strict join must recompute it before `TargetObjectRef`, whose `TargetPathDigest` carries only the final digest; no fallback/alias/normalization/duplicate/collision/cross-item/rename |
| locator persistence | each item has both digests, recovery-local AEAD, explicit positive key/cipher versions and full length-framed row/job/root/workspace/operation binding; generic hooks are excluded from the item column while generic encryption remains for operation snapshot/workspace; existing `enc:v2` is not item AEAD; full-set reload validation precedes I/O |
| execute prepared aggregate | outside tx: replay/decode/validate/associate/preallocate/select-key/digest/encrypt; inside tx: ordered locks, byte-for-byte recomputation, exact `LockActiveTx`, grant CAS first, complete aggregate+receipt and one commit; no encryption/Provider/SSH/target/audit/reservation inside |
| Provider restore | closed Rsync/Restic/Rclone requests, exact locators/includes/prefixes, no free shell/latest/destructive sync |
| job/checkpoint | guarded executed→superseded pre-write transaction, mutation arm/latch, target-chain CAS, partial evidence, exact `AdoptInterruptedOperation(ctx, claim, jobItemID)` with DB-load/decrypt then transaction-free I/O then final re-lock CAS, closed present/absent Verify union with exact digest/bytes and bounded opaque strong target-derived revision, no blind replay or caller-forged facts |
| unresolved remote outcome | post-arm invalid/contradictory write or observation uses exactly one terminal `operation_unresolved`, failure `remote_outcome_unresolved`, four closed category-specific field arms and exact private facts; source drift/failure may coexist; old phases are neutral; source/preflight/authority and ordinary-evidence fences cross-bind; sequence 1 or applicable workspace/operation/delete-authority predecessors are legal; next revision is empty; one fenced transaction fails item/job, writes sanitized evidence, closes attempt, releases both leases, forbids current-item success/skipped/adoption or continued writes, and preserves prior success/skipped/chain fields |
| conflict policy | fail/skip/overwrite exactness; exact-mirror frozen delete digest, paused checkpoint, separate delete grant/consume |
| result lifecycle | isolated execute commit has preallocated none-state locator/binding but empty reservation facts; `PrepareFirstWrite` reuses identity for pre-byte reservation/deadline; terminal publish barrier; partial cleanup-only; four public states; phased revoking takeover |
| cleanup exclusion | fresh node-wide writer lease per remote attempt, declared lock order, renew/release, lease-loss and busy-node fairness |
| marker/root | installation/job/root/nonce HMAC, no client absolute path, no-follow revalidation, orphan quarantine |
| Content delivery | exact RecoveryResult arm, recover + result proof, Range/budgets/revoke/drain/audit, asset arm unchanged |
| schema | paired twelve-table 000069, immutable distinguished evidence latch, isolated-none/reserved+/in-place workspace matrix, frozen job/item identities + semantic/final digests + cipher versions + operation product + terminality, immutable receipt arm, Content FK and SQL/model/index/trigger/down parity |
| runtime/settings | atomic sensitive roots, default-disabled graph, sticky disable, cleanup continuity, downgrade-readiness/forward-fix gate |
| fatal cleanup key | bounded idempotent DB-only current-post-arm reconciliation before original fatal startup error; sanitized needs_attention/cleanup_due and attempt close; no decrypt/target I/O/checkpoint/success/skip/chain |
| API/RBAC/audit | explicit override/write/delete/readiness routes, ownership, receipt-first same-session replay, typed errors, response helpers, audit-projection-after-commit non-duplication, full locator/proof/JTI/reason/secret no-leak, Swagger truth |
| frontend | closed target/security/authority mapper, separate drafts/replay, polling/reload, publication-aware actions, mixed-version/legacy gate, a11y/i18n |
| delivery | exact 145 paths, full/race/PostgreSQL/frontend/Docker/CI evidence, PR/post-merge/archive truth |

## 5. Security And Fault-Injection Matrix

| Injection or race | Required closed outcome |
|---|---|
| source changes at preflight/write-authority/job-commit/claim/first-write/resume | guarded cross-state supersede+failed+lease release before arm; after arm/partial stop and needs_attention |
| target root/component swapped or becomes symlink/mount mismatch | current operation refuses; no traversal or unrelated deletion |
| two plan/job creators share an idempotency key | one durable winner or stable intent conflict; no orphan rows/leases |
| proof/grant reused across security/write/delete action/session/plan | reject before mutation; no existence or reason leak |
| four authorization callers share requester+endpoint+key+intent | one immutable receipt/effect winner; same-session losers replay only its durable result, never re-run effect |
| same authorization key changes reason/binding/revision/category/grant-secret hash | stable idempotency conflict; no proof consumption or partial effect |
| one fresh step-up proof races across plan/category/endpoint | global proof-digest unique elects one committed receipt; every other caller gets safe `proof_used` without subject disclosure |
| transaction faults before receipt commit | receipt, CAS/grant/job/items/attempt/source leases/node lease all rollback together |
| ordinary audit projection fails after receipt commit | receipt/effect remain unique; response/retry is committed success; no duplicate audit-authority effect or raw failure leak |
| client loses write/delete issue response | same key+intent+canonical retained secret returns only existing grant metadata; changed secret conflicts; raw bearer is never persisted/returned |
| execute replay after job creation or terminal transition | receipt returns the unique job and consumes neither grant nor another item/attempt/source lease/node lease |
| direct SQL receipt update/delete/cascade/down before replay expiry | immutable/retention trigger and first down guard reject on SQLite and PostgreSQL; latch remains permanent |
| near-expiry session or receipt reaper races a still-valid proof/grant | initial effect is rejected or receipt remains; proof cannot be globally reused and later eligible receipts still reap |
| protected old receipt/normal evidence/latch precedes eligible retention rows | stateless indexed eligibility excludes protected rows before LIMIT; restart/disabled runtime still advances and shutdown joins the owner |
| malware finding is unknown/non-overridable or override revision drifts | default blocked; no override/authority/job mutation or raw finding leak |
| source or node lease/fence expires/takes over | old worker performs zero mutation/checkpoint/status update |
| crash before/after target write/checkpoint | validate checkpoint and verify/resume, or needs_attention; never blind replay |
| post-arm write result is invalid, observation is invalid, trusted revisions disagree, or valid observation mismatches expectation | append exactly one terminal `operation_unresolved` with the exact closed category-specific field arm and sanitized length-framed bindings; allow it as sequence 1 or after applicable earlier durable history; atomically fail the item, move the job to `needs_attention/remote_outcome_unresolved`, cross-bind failure evidence, close attempt/release leases, forbid a current-item success/skipped/adoption checkpoint or continued write, and keep next/chain/prior-success/skipped fields unchanged |
| any unresolved-disposition insert/CAS/evidence/attempt/lease step fails or a stale fence races it | roll back the entire short transaction; no partial checkpoint, false terminal projection, lease release or target-chain change |
| exact-mirror target changes before delete or write grant is presented | delete authority/second checkpoint invalid; no deletion |
| crash before publication or unsafe terminal partial exists | no ready/result row; immutable-deadline workspace is cleanup-only |
| forged/missing/tampered marker or lost cleanup key | fail closed, quarantine/alert, never automatic delete |
| cleanup races ordinary writer or loses node lease during delete | unique writer wins; old cleanup fence makes no further remote/DB mutation; takeover verifies |
| cleanup crashes at claim/revoke/drain/validate/delete/tombstone | expired revoking takeover from durable phase with fresh node/cleanup fences; job outcome unchanged |
| result ref is unpublished/in-place/path/link/special/cross-job/dual arm | generic not-found/forbidden; no target open or existence leak |
| Range/budget/replay/logout race | conservative counters, all revokers attempted, stale session cannot read after reconciliation |
| Provider output contains path/credential/command detail | sanitize before persistence/log/audit/API; stdout is not authority |
| persistent reconciliation failures fill one batch | durable keyset/high-water selection still reaches later actionable rows |
| crash after latch commit or schema used then rows cleaned | permanent latch remains; down refuses on both engines before any schema change |
| encrypted source/root locator is substituted | domain digest mismatch before Provider/target I/O; raw value absent from every external surface |
| encrypted job-item locator/envelope is moved across row/job/root/item/workspace/key/cipher version or operation product | recovery-local AEAD authentication or complete length-framed binding fails before target I/O; no adoption/checkpoint/projection/chain advance/plaintext leak |
| present Verify digest or byte differs, union has zero/multiple arms, or revision is empty/oversize/treated as SHA-256-shaped | before mutation arm, reject without a new operation success/adoption checkpoint or chain advance; after mutation arm, a valid mismatch is `verification_mismatch` and an invalid union/revision is `observation_invalid`, each using the exactly-one terminal unresolved disposition |
| create/overwrite/skip/delete presence, digest or byte sentinel is mutated | reject the operation product, including create prior bytes != -1, skip post digest drift/post bytes != -1, and delete nonempty post digest/either byte field != -1 |
| schema-v2 payload is duplicate-source, policy-invalid or only self-consistent outside the approved operation context | snapshot decode rejects the whole product before target I/O; canonical encoding alone is not authority |
| canonical row locator, `SemanticTargetDigest` or final `TargetObjectDigest` is substituted, omitted or conflated | whole-product validation rejects before target I/O; `TargetObjectRef.TargetPathDigest` is never constructed from the semantic digest |
| isolated workspace identity/binding or item key/cipher/product fact is substituted | locked reload, decrypt, strict join and digest recomputation reject before ref/permit construction or target I/O |
| a remote directory already exists for a preallocated isolated identity, or retry/adoption proposes a different identity | fail closed; never rename/reallocate, and `PrepareFirstWrite` reuses only the persisted none-state identity |
| skip source changes or target differs from frozen prior digest/bytes | source drift alone keeps its existing fail-closed handling; after mutation arm, source drift/failure may coexist while a valid target mismatch projects `verification_mismatch`; never reinterpret source identity as target identity or project succeeded |
| delete verify reports permission/timeout/ambiguous missing | not absence; before mutation arm, reject without a new delete success/adoption checkpoint or chain advance, while a post-arm invalid observation projects exactly one terminal `observation_invalid` after applicable `delete_authority_consumed` history; never project item/job success |
| adoption attempts to hold a DB transaction across target I/O, accepts caller locator facts, or races a stale/taken-over claim | reject the invalid shape; valid adoption is DB load/decrypt validation, transaction-free target I/O, then final re-lock/fenced CAS with exactly one winner |
| prepared aggregate differs byte-for-byte after ordered locks, selected key/version no longer matches exact `LockActiveTx`, or any aggregate insert fails after grant CAS | one transaction rejects or rolls back grant consumption and every aggregate/receipt row; no insert-first/deferred grant consumption or partial identity |
| cleanup locator key is permanently lost at startup | bounded DB-only reconciliation closes current post-arm attempts as needs_attention/cleanup_due, leaves pre-arm/terminal/stale rows unchanged, performs no target/decrypt/success work, then returns the original fatal error |
| same-binary disable during active recovery | stop new authority/writes, fence attempts, preserve revoke/cleanup until bounded plaintext is gone |
| pre-Child downgrade requested after first use/with retained result | current readiness gate returns forward-fix-only/unready; old binary never starts by contract |

## 6. Exact Scope, Privacy And Static Scans

Run before each review checkpoint and again immediately before staging:

```bash
git diff --name-only --diff-filter=ACMR
git diff --cached --name-only --diff-filter=ACMR
git ls-files --others --exclude-standard
git diff --check
test -z "$(git diff --cached --name-only)"  # before delivery only
test ! -e backend/xirang-server

find backend/internal/database/migrations/sqlite \
     backend/internal/database/migrations/postgres -maxdepth 1 -type f | \
  sort | rg '00006(8|9)|00007(0|1)'

rg -n '[T]ODO|[F]IXME|[T]BD|[P]LACEHOLDER|unknown as|\\bany\\b|fetch\\(' \
  backend/internal/backupasset/recovery \
  backend/internal/api/handlers/backup_recovery_handler.go \
  web/src/lib/api/backup-recovery-api.ts \
  web/src/features/backup-assets/recovery-*.tsx \
  web/src/features/backup-assets/use-backup-recovery.ts

rg -n 'locator|root|path|reason|credential|proof|grant|ticket|marker|nonce|command|stdout|stderr' \
  backend/internal/backupasset/recovery \
  backend/internal/api/handlers/backup_recovery_handler.go

rg -n 'FAKE_RECOVERY_(RAW_PROOF|PROOF_JTI|SESSION_JTI|REASON|SECRET)' \
  backend/internal/backupasset/recovery \
  backend/internal/api/handlers/backup_recovery_handler.go \
  backend/internal/model/backup_asset_recovery.go \
  backend/internal/backupasset/audit.go

git diff --name-only | rg '^(deploy/|docker-compose|README\\.md$|CHANGELOG\\.md$|docs/|scripts/)'
git diff --name-only | rg '00007(0|1)|backend/internal/backupasset/(retention|lifecycle)/'
```

Expected: only the exact four 000069 files are new; 000070-000071 and Child
14/15/deploy/release paths are absent. Placeholder and privacy scans are
interpreted match-by-match; legitimate typed field names are not failures, but
every occurrence must be encrypted, opaque, sanitized or test-only. Task 5's
named fake proof/JTI/reason/secret literals must appear only in scoped tests;
their absence from persisted rows, response/audit/log capture and generated
Swagger is a required assertion, not a grep-only claim.

## 7. Rollback And Mixed-Version Behavior

### 7.1 Runtime Rollback

1. Disable new plan/preflight/security-override/write/delete-authority/execute admission.
2. Unpublish mutation/ticket facades, stop new claims, cancel/join current
   attempts and advance source/node fences.
3. Revoke and drain RecoveryResult deliveries.
4. Keep published and unpublished lifecycle/orphan reconciliation running;
   retained plaintext may remain within its immutable hard cap but makes binary
   downgrade unready.
5. Preserve job/checkpoint/evidence truth. Never claim generic rollback of
   in-place writes; `needs_attention` requires operator repair.

### 7.2 Schema And Binary Rollback

Feature disable is not binary downgrade. The current-binary readiness operation
must first install a sticky transition fence and prove zero queued/running job,
unconsumed authority, source/node lease, live attempt, non-cleaned ResultSet,
Recovery Content grant/ticket/stream and reconciliation backlog. Down is allowed
only when that proof also finds no fixed use-latch row; then current 000069 down
must atomically return the schema to 000068 before a pre-Child binary starts.

Once the latch exists, 000069 remains installed permanently and the only
supported rollback is forward-fix, even after ordinary rows are cleaned. A
retained ResultSet is explicitly downgrade-unready. Never rely on the old binary
to recognize 000069, preserve cleanup, or reject its own startup.

New frontend against an old backend maps unavailable/404 to a closed disabled
state with no legacy fallback. Old frontend exposes no plan UI while a new
default-disabled backend still runs reconciliation. Legacy restore remains gated
through Child 15; this Child does not enable GA, change deploy defaults or claim
release readiness.

## 8. Approval And Delivery Ledger

| Gate/action | Current status | Required transition |
|---|---|---|
| branch/task creation and parent registration | executed | preserve exact Phase 1 scope |
| current-main research and product decisions | complete | preserve evidence |
| immutable security/state review | complete, original disposition NOT APPROVED | retained verbatim; all eight corrections independently rereviewed |
| PRD/design | `complete_approved` on 2026-07-28 | independent spec/security/data rereview found no Critical/Important issue |
| implementation plan and original 55/71 manifest | `complete_approved` on 2026-07-28 | independent plan/manifest rereview found no Critical/Important issue; the TaskRun snapshot amendment made the prior scope 55/72 and 136 total, the Task 4 B1 source-boundary amendment made it 55/80 and 144 total, and the focused Catalog blocker amendment adds only tracked `provider/catalog.go` for the current 55/81 and 145 total without using 000070 |
| planning/start authorization | `complete_approved` under standing user authorization | run final immutable preflight |
| `task.py start` | `executed_once` on 2026-07-28 | Child `in_progress`; parent remains `planning` |
| Task 1 product/tests/migration | `complete_approved`; focused remediation and final review evidence recorded | original model/state/contracts chronology is an accepted historical deviation, not a passed TDD gate; every new fix uses observed RED→GREEN |
| Task 2 product/tests | `complete_approved` on 2026-07-29 | Findings 1--7 observed RED→GREEN; Finding 8 immediate-GREEN coverage gap; controller-inline spec/quality passes and focused/full/race/vet/lint/static gates passed |
| Task 3 product/tests | `complete_approved` on 2026-07-30 | fresh Critical/Important findings and final legacy terminal-overwrite race closed by observed RED→GREEN; independent specification APPROVED and controller-inline quality recheck passed; detailed chronology is in `research/implementation-evidence.md` |
| Task 4 product/tests | `complete_approved` at focused task scope | Provider missing-strength RED and real Repository immutable Rsync Catalog point changed RED observed; detached factory/fingerprint rewrite rejected; authenticated `request.SourceFingerprint` plus real `Service.OpenCatalogRead`/`catalog.Indexer` normal/race GREEN; `SPEC APPROVED`; `QUALITY APPROVED`; broad Provider blocked by host `IFree=0` and not claimed |
| Task 5 authorization receipt amendment | `complete_approved` at focused authorization-receipt scope | evidence-table receipts, four atomic effects, proof/session/grant lifetime ordering, singleton execute lease, stateless reaper, standalone owner, settings and exact operation rows are implemented; independent receipt `019fb71a-75df-7770-a17d-9b3d8647d99d` returned `SPEC APPROVED` |
| Task 5 product/tests/migration | `complete_approved` at focused authorization-receipt scope | unchanged SQLite selectors, repeated races, required real PostgreSQL direct-SQL/concurrency/rollback and full 000069 gates pass; independent receipt `019fb73d-03b6-7111-baf3-83e1ae2e3f8b` returned `QUALITY APPROVED: READY` with no finding |
| Task 6 batch/evidence ledger | B1-E1 `complete_checked` for Corrections 1--3 and 5; B1-E2 `complete_checked` for Corrections 7--10; B1-E3 `complete_checked` for Corrections 11--13; B2-E1 `complete_checked` for Correction 4 plus its delete row; B2-E2 `complete_checked` for Correction 6; B3 `PROVED_COMPLETE_FOCUSED_ONLY`; F3 and F4 `complete_checked` | B1 and B2 aggregates remain partial. Every focused batch grants no broader whole-task credit; open combined/whole work receives no retroactive RED/review credit |
| Task 6 product/tests/migration | `in_progress`, not complete; B3, F6, focused F3/F4, B1-E1/E2/E3 and B2-E1/E2 are closed at their exact scopes | complete whole specification review, whole quality review and all final gates before Task 7; no focused batch gives broader credit |
| Task 6 focused B3 quality/evidence | controller-inline review found no issue; required non-skipped PostgreSQL 000069/six-case, cancellation, focused race, affected regression, vet, owned gofmt, diff, manifest and staged-zero gates passed; resources cleaned | focused Correction 14 scope only. The local reviewer rerun could not link because of host disk quota and is not classified as pass or fail; whole Task 6 reviews and gates remain unchecked |
| Task 6 focused F3 quality/evidence | `complete_checked`; all SQLite 000069, required real PostgreSQL 000069/F3, frozen selectors, race x10, affected package regressions, vet, owned gofmt, diff, manifest and staged-zero gates passed; resources cleaned | focused persistent scheduler, legal pre-write drift and frozen execute replay only; it grants no B1/B2 or F4 credit, and B1-E1 is recorded separately |
| Task 6 focused B1-E1 quality/evidence | `complete_checked`; frozen selector normal/race x10, full recovery and five affected packages, paired SQLite/required-real-PostgreSQL ordinary-row matrix, vet, owned gofmt, diff, manifest and staged-zero gates passed | Corrections 1--3 and 5 only: exact ordinary identity/bytes, source revalidation, skip source/target separation and closed Verify; B1-E2/E3 are recorded separately, while B2, F4 and whole gates remain open |
| Task 6 focused B1-E2 quality/evidence | `complete_checked`; five frozen selectors observed controlled RED/final GREEN, race x10, full recovery and five affected packages, SQLite/required-real-PostgreSQL operation-snapshot/job-item/locator companions, vet, owned gofmt, diff, Trellis, manifest and staged-zero gates passed | Corrections 7--10 only: canonical semantic locator mapping, preallocated workspace/distinct final-object digest, row-bound item AEAD and versioned aggregate envelope; migration companions grant no B1-E3 credit |
| Task 6 focused B1-E3 quality/evidence | `complete_checked`; six frozen selectors observed controlled RED/final GREEN, race x10, full recovery, runtime startup and six affected packages, SQLite full/paired and required-real-PostgreSQL locator/adoption/rollback/full-000069 companions, vet, owned gofmt, diff, Trellis, manifest and staged-zero gates passed | Corrections 11--13 only: three-boundary durable adoption, bounded DB-only permanent key-loss reconciliation, grant-first/exact-key prepared aggregate and paired immutable enforcement; B1 aggregate stays partial and B2, F4 and whole gates remain open. Three focused lint findings were fixed; seven earlier recovery-package findings remain, so no whole-package lint pass is claimed |
| Task 6 focused B2-E1 quality/evidence | `complete_checked`; four frozen selectors and three support regressions observed inherited GREEN plus four controlled behavior-removal REDs/final GREEN, race x10, full recovery, six affected packages, SQLite and required-real-PostgreSQL locator-product companions, vet, owned gofmt, diff, Trellis, manifest and staged-zero gates passed | Correction 4 plus its delete row only: durable `in_place+exact_mirror` authority, exact absence, closed delete sentinels/null plan item and no synthetic absence digest. B2 aggregate stays partial; B2-E2 chain/multi-delete assertions receive support-only, not completion, credit. Seven earlier recovery-package lint findings remain, so no whole-package lint pass is claimed |
| Task 6 focused B2-E2 quality/evidence | `complete_checked`; existing frozen Verify selector plus four support regressions observed two controlled behavior-removal REDs/final GREEN, race x10, full recovery, six affected packages, production SQLite and required-real-PostgreSQL `000069` multi-delete evidence, vet, owned gofmt, Trellis, manifest and staged-zero gates passed | Correction 6 only: separate absence-chain domain, ordered same-execution and restart multi-delete chain, exactly-once delete-set authority consumption/reuse and terminal chain projection with B3 fields neutral. B2 aggregate stays partial and F4/whole gates remain open. Seven earlier recovery-package lint findings remain, so no whole-package lint pass is claimed |
| Task 6 focused F4 quality/evidence | `complete_checked`; two frozen selectors observed two controlled behavior-removal REDs/final GREEN, normal/race x10, full recovery and five related packages, three SQLite and three required-real-PostgreSQL companions, vet, owned gofmt, diff, Trellis, manifest and staged-zero gates passed | Task-6-owned preallocated identity, `none -> reserved`, immutable 24-hour deadline and partial cleanup-only classification only. Publication, Content, revoking takeover, cleanup node lease and result-ref denial remain Task 7. Seven earlier Recovery lint findings remain, so no whole-package lint pass is claimed |
| Tasks 7--10 product/tests/migration | not_executed | remain separate and receive no Task 5 or Task 6 planning-amendment completion credit |
| real PostgreSQL/frontend/full/Docker gates | Task 1 and Task 3 PostgreSQL evidence, Task 5 required receipt/full 000069 selectors, focused F6, focused F3 full-000069/authority-drift, focused B1-E1 ordinary-row, focused B1-E2 locator-product, focused B1-E3 locator/adoption/rollback/full-000069 and focused F4 workspace/locator/deadline companion selectors passed; frontend/Child/full/Docker and whole Task 6 gates remain not_executed or open | fresh non-skipped evidence for each remaining gate |
| Task 1 independent specification re-review and quality review | `complete_approved` on 2026-07-29 | specification and fresh live-worktree quality reviews returned `APPROVED` |
| final Child specification and quality reviews | not_executed | no open Critical/Important findings |
| exact staging/work commit | not_executed | controller only after Step 11 |
| push/PR/required CI/squash merge | not_executed | monitor until required checks pass |
| post-merge CI/Release Please/publication disposition | not_executed | observe actual automation truth |
| Child archive/journal/bookkeeping PR | not_executed | only after feature merge/post-merge truth |
| parent archive or GA enablement | forbidden | Child 15/final parent acceptance only |

No row may be relabeled `passed`, `executed`, `approved` or `complete` without
fresh evidence for that exact action. Historical Child 1-12 gates are reusable
contracts, not Child 13 completion evidence.

### Task 6 B1-E3 focused closure (2026-08-02)

B1-E3 is `complete_checked` for Corrections 11--13 only. The permanent owner
set is `recovery/service_test.go`, `recovery/worker_test.go` and
`database/backup_asset_migrations_integration_test.go`; all controlled changes
to `recovery/service.go`, `recovery/worker.go` and the paired `000069` up files
were restored byte-for-byte. The frozen subset is exactly:

```text
TestRecoveryExecutePreparedAggregateGrantFirstMatrix
TestRecoveryAdoptInterruptedOperationDurableDerivationMatrix
TestRecoveryPermanentCleanupKeyLossMatrix
TestRecoveryLocatorRaceTakeoverOneWinner
TestBackupAssetMigration069RecoveryLocatorProductSQLite
TestBackupAssetMigration069RecoveryLocatorProductPostgres
```

The inherited implementation first passed those selectors and received only
coverage credit. Five narrow production/migration baselines then produced
genuine unchanged-selector RED for adoption durable-digest drift, post-arm
cleanup reconciliation, grant-first persistence, exact transaction-scoped key
matching and paired immutable semantic-digest enforcement. The protected files
were restored to their exact hashes before final GREEN.

Fresh normal, race x10, full recovery, runtime startup, six affected packages,
same-scope vet, SQLite full/paired and required real PostgreSQL locator,
adoption, rollback and full-000069 companions passed. The three focused lint
findings were fixed. The complete recovery package still has seven earlier
lint findings, so `golangci-lint` is recorded as a visible pre-existing
limitation rather than a pass. No shared `.trellis/spec/` change is required:
the behavior is an unshipped task-local Recovery contract already captured in
`design.md` §19 and this frozen matrix.

At the B1-E3 checkpoint, B1 aggregate remained partial and B2-E1/E2, F4, whole
Task 6 reviews/gates, Child completion and every Git delivery action remained
open. Its required stop was before B2-E1.

### Task 6 B2-E1 focused closure (2026-08-02)

B2-E1 is `complete_checked` for Correction 4 plus its delete row only. The
permanent product delta is limited to
`backend/internal/backupasset/recovery/contracts_test.go`; no top-level selector,
interface, model, table, path, migration number or crypto domain was added. The
four frozen selectors are:

```text
TestRecoveryOperationSnapshotV2WholeProductTamperMatrix
TestRecoveryVerifyOperationProductMatrix
TestBackupAssetMigration069RecoveryLocatorProductSQLite
TestBackupAssetMigration069RecoveryLocatorProductPostgres
```

The inherited implementation first passed those selectors and received only
coverage credit. The existing frozen selectors were then strengthened in place
to bind delete policy/sentinels, exact-only absence evidence, the permission,
timeout, unsupported-stat, transport-failure and ambiguous-missing matrix, and
the durable delete-authority pause. Four controlled behavior removals produced
genuine unchanged-selector RED: accepting arbitrary nonempty absence evidence,
bypassing the empty-grant pause gate, accepting a synthetic delete post digest,
and allowing a delete row outside `in_place + exact_mirror` in both paired
migrations. `contracts.go`, `executor.go` and both `000069` up migrations were
restored to their exact frozen hashes before final GREEN.

Fresh focused normal and race x10, full recovery, six affected packages,
same-scope `go vet`, SQLite and required real PostgreSQL locator-product
companions, owned format/diff, Trellis, manifest and staged-zero checks passed.
The healthy `xirang-c13-pg` container was reused in required mode without skip,
restart, reconfiguration or removal. The complete recovery-package lint still
reports the same seven earlier findings outside the B2-E1-owned delta, so no
whole-package lint pass is claimed. No shared code-spec update is required:
the unshipped contract is already explicit in `design.md` section 19.

`TestRecoveryExactMirrorSuccessfulDeleteProjectsAbsenceCheckpointAndChain` is a
supporting regression only. Its inherited chain assertions do not close
Correction 6. B2 aggregate remains partial; B2-E2, F4, whole Task 6 reviews and
gates, Child completion and every Git delivery action remain open. The required
stop is before B2-E2.

### Task 6 B2-E2 focused closure (2026-08-02)

B2-E2 is `complete_checked` for Correction 6 only. The existing top-level
frozen selector set remains exactly seventeen; the owned frozen selector is
`TestRecoveryVerifyOperationProductMatrix`, strengthened in place with the
existing successful-delete, same-execution multi-delete, restart multi-delete
and consumed-authority reconciliation regressions. Permanent changes are
limited to `recovery/contracts_test.go`, `recovery/executor_test.go` and
`recovery/testutil_test.go`; no product interface, model, table, migration,
crypto domain, manifest path or top-level selector was added.

The strengthened same-execution contract binds exact delete call order, one
`delete_authority_consumed` checkpoint, one operation checkpoint per delete,
the literal `xirang/recovery/target-absence-chain/v1` length-framed revision for
each exact absence, each next revision as the following prior revision, the
terminal job chain as the final delete revision, and neutral B3 unresolved
fields. The restart and consumed-authority arms prove that the durable required/
consumed pair authorizes the remaining delete without a bearer secret, without
reconsumption or duplicate deletion.

Two controlled production behavior removals made the unchanged frozen selector
fail. Conflating the absence-chain domain with the ordinary present-target
domain broke the exact single- and multi-delete chain revisions. Ignoring the
durable consumed-delete checkpoint pair broke same-execution second-delete,
restart continuation and consumed-absence reconciliation. `executor.go` and
`worker.go` were restored byte-for-byte before final GREEN.

The required production-`000069` PostgreSQL fixture exposed one test-only clock
fault: PostgreSQL preserves the migration scheduler rows' subsecond
`CURRENT_TIMESTAMP`, while the shared deterministic fixture clock is aligned to
whole seconds. The fixture now rebases itself just after that durable scheduler
floor. The original trigger rejection is fixture evidence, not a product RED;
the unchanged PostgreSQL subtest then passed five consecutive runs and the full
frozen selector passed in required mode with no skip.

Focused normal and race x10, full Recovery, six affected packages, same-scope
`go vet`, production SQLite and required real PostgreSQL multi-delete evidence,
owned gofmt, manifest and staged-zero checks passed. Complete Recovery lint
still reports the same seven earlier findings outside the B2-E2-owned lines, so
no whole-package lint pass is claimed. No shared `.trellis/spec/` update is
required because this remains an unshipped task-local contract already captured
in the design and frozen matrix.

B2 aggregate remains partial pending combined/whole evidence. F4, unchecked
Task 6 rows, whole reviews/gates, Child completion and every Git delivery action
remain open. The required stop is before Task-6-owned F4.

### Task 5 current independent-review closure (2026-07-31)

This later approval ledger preserves the dated pending implementation records
above. Task 5 is `complete_approved` at focused authorization-receipt scope
only. The independent specification receipt
`019fb71a-75df-7770-a17d-9b3d8647d99d` returned `SPEC APPROVED` after exact
Steps 7/11/12, SQLite/PostgreSQL races, the full PostgreSQL `000069` matrix, and
manifest/static/Trellis/index checks; it confirmed full Task 8 graph wiring is
intentionally deferred. The independent quality receipt
`019fb73d-03b6-7111-baf3-83e1ae2e3f8b` returned `QUALITY APPROVED: READY` with
no Critical, Important, or Minor finding after focused/eight-package tests,
SQLite race x10, runtime owner x50, PostgreSQL winner x10/direct-SQL/rollback,
vet/format/diff/Trellis/manifest/index checks.

- **Recorded Task 5 receipts:** their approval
  changes only Task 5's focused status. At that closure snapshot Tasks 6--10 and
  the full Task 8 graph were `not_executed`; Task 6 is now `in_progress`, while
  Tasks 7--10 and frontend/Child/full/CI/delivery gates remain `not_executed` or
  open. Child remains `in_progress` and parent remains `planning`. The manifest remains
  exactly 9 + 55 + 81 = 145, with only `go.mod` and
  `recovery/testdata/rsync_local_to_remote.json` excluded from the dirty union.

### Task 6 F4 focused closure (2026-08-02)

F4 is `complete_checked` for the Task-6-owned workspace/deadline/cleanup-only
boundary only. The permanent delta is limited to
`backend/internal/backupasset/recovery/executor_test.go`, whose final SHA-256 is
`9964b2d1b02c2f6071c4b4d58614ff9af5975f5c046aaa29a246f4e3d5a900a2`.
It adds exactly these two stable selectors outside the unchanged seventeen
first-thirteen Task 6 selectors:

```text
TestRecoveryReviewF4WorkspaceDeadlineAndPublication
TestRecoveryReviewF4PartialWorkspaceCleanupOnly
```

The first selector proves encrypted-at-rest preallocation of the exact
`jobs/<opaque>` identity, durable `none -> reserved` reuse, immutable binding,
marker/owner/fence, 24-hour plaintext deadline, reservation checkpoint and use
latch before `CreateOwnedJobDir` or content bytes, sealed-but-unpublished
success, and exact-identity retry after an unexpected remote directory. The
second proves that pre-arm failure and queued cancellation remain `none`, while
armed cancellation and post-arm unresolved outcome become
`needs_attention|cleanup_due` without a public ResultSet or result row.

Two controlled behavior removals produced genuine unchanged-selector RED:

1. The reservation deadline was temporarily changed from 24 hours to
   `24h - 1s`; the unchanged workspace/deadline selector failed.
2. The armed-cancellation `cleanup_due` projection was temporarily removed;
   the unchanged partial-workspace selector observed illegal `reserved` state.

The product files were restored before final GREEN. Their protected hashes are:

```text
worker.go    75e4c4a2beb421a6f76d6d9b752f7afb47cc034a6609d00dd0760b66e2798972
executor.go  23762c8e435a553d1e0da1dc346b8f03bef0e100003cd8530036e37bb6d913a9
```

Fresh verification after final GREEN passed:

```text
F4 normal Recovery selectors                         PASS (0.180s)
F4 SQLite workspace/locator/deadline companions      PASS (0.945s)
F4 Recovery selectors under -race -count=10          PASS (6.020s)
full Recovery package                                PASS (12.227s)
model/database/runtime/backupasset/server packages   PASS
three required real PostgreSQL companions, no skip   PASS (17.749s)
same-scope go vet                                    PASS
owned gofmt -d                                       PASS (no output)
git diff --check                                     PASS (no output)
```

The first PostgreSQL invocation supplied a libpq keyword DSN where the fixture
requires a URL and failed before connection or migration setup; it is a harness
diagnostic, not product failure or RED. The unchanged selectors then passed
with a URL DSN derived inside the shell from the healthy pre-existing
`xirang-c13-pg` PostgreSQL 18 container on loopback port 55470. Credentials were
not printed; the container was not restarted, reconfigured or removed.

Complete Recovery `golangci-lint` still reports exactly the seven earlier Task
6 findings: one `errcheck`, four `staticcheck` and two unused helpers. None is
in the new F4 selector range, so no whole-package lint pass is claimed. Both
Trellis task validations and JSON/JSONL parsing passed with 17 implement and 18
check entries. The exact manifest remains
`phase1=9 create=55 modify=81 total=145 unique=145 duplicates=0`; the dirty
union remains 82 paths, comprising 80 manifest members plus only protected
`go.mod` and `recovery/testdata/rsync_local_to_remote.json`, with zero outside
the allow-list. Staged paths are zero, `.git/index.lock` is absent, exactly four
paired `000069` files exist, and `000070+` is absent.

The protected unrelated hashes remain:

```text
go.mod                                        b767fc9ef9376c651d0493329b710ac8dcf8d77e52686b847d244aff9f6d48fd
recovery/testdata/rsync_local_to_remote.json  2570bd4472541322d902c4cdf2fe43f247b69f19d335558830e749567f763892
```

This F4 closure gives no publication, Content, `revoking` takeover, cleanup
node-lease, `RecoveryResultRef`, B1/B2 aggregate, whole Task 6, Child or delivery
credit. Task 6 and Child 13 remain `in_progress`; the parent remains
`planning`. No stage, commit, push, PR, CI, merge, branch, worktree, goal,
subagent or heartbeat action was performed. The required stop is before whole
Task 6 specification review.

## 9. Whole Task 6 Corrections 15--17 Recovery Plan (2026-08-03)

> **Execution mode:** bounded inline TDD in the existing branch/worktree. Do not
> use a goal, heartbeat or subagent for this batch. Do not stage or commit until
> the existing Task 11/12 delivery gate authorizes the exact aggregate.

**Goal:** Close the whole Task 6 specification blockers without adding a table,
migration number, unresolved category, public interface or manifest path.

**Architecture:** Extend Correction 14 to represent target-call errors with no
returned product; inject the existing Repository-owned Rsync source resolver
into restart adoption; atomically retain normal operation evidence when source
trust fails; and persist `marker_created` with paired one-way SQL guards.

### 9.1 Exact file map

Task artifacts are the existing `prd.md`, `design.md`, `implement.md` and
`task.json`. Product ownership is limited to these already-manifested paths:

```text
backend/internal/backupasset/recovery/executor.go
backend/internal/backupasset/recovery/executor_test.go
backend/internal/backupasset/recovery/worker.go
backend/internal/backupasset/recovery/worker_test.go
backend/internal/backupasset/recovery/state.go
backend/internal/backupasset/recovery/state_test.go
backend/internal/model/backup_asset_recovery.go
backend/internal/model/backup_asset_recovery_test.go
backend/internal/database/migrations/sqlite/000069_backup_asset_recovery.up.sql
backend/internal/database/migrations/postgres/000069_backup_asset_recovery.up.sql
backend/internal/database/backup_asset_migrations_integration_test.go
```

The model is expected to need assertions only, not a new field. Down migrations,
Provider interfaces, Repository implementation, Runtime, API and frontend are
read-only in this batch.

### Task 6-R1: Freeze post-arm no-product RED

**Files:** `recovery/executor_test.go`, database migration integration test.

- [ ] Add `TestRecoveryExecuteClaimPostArmCallErrorsBecomeUnresolved` with
  create/overwrite write-error, exact-mirror delete-error and
  create/overwrite/skip/delete verify-error arms. Record the call before the
  private sentinel is returned. Require exactly one unresolved checkpoint,
  current item failure, job needs-attention, failed attempt, released leases,
  no later target call and no raw sentinel/locator leak. No-product arms require
  empty digest/revision/presence; returned-invalid arms retain a sanitized
  digest.
- [ ] Add no-product and adoption-no-write legality/negative subcases to
  `TestBackupAssetMigration069WholeTask6ClosureSQLite` and
  `TestBackupAssetMigration069WholeTask6ClosurePostgres`.
- [ ] Run the unchanged tests and observe genuine RED:

```bash
cd backend
go test ./internal/backupasset/recovery \
  -run '^TestRecoveryExecuteClaimPostArmCallErrorsBecomeUnresolved$' -count=1
go test ./internal/database \
  -run '^TestBackupAssetMigration069WholeTask6ClosureSQLite$' -count=1
```

Expected RED: current code returns target errors while the attempt/job remain
running, and current SQL rejects the newly legal empty-product rows. Compile,
fixture or unrelated failures do not count.

### Task 6-R2: Freeze Provider-source and completed-evidence RED

**Files:** `recovery/executor_test.go`, `recovery/worker_test.go`, migration
integration test.

- [ ] Strengthen `TestRecoveryExecuteClaimRevalidatesPinnedSourcePerOperation`.
  The post-result arm must retain the item, exact item-bound operation
  checkpoint and chain, then write sanitized source-failure evidence, close the
  attempt/leases and set `needs_attention/source_revalidation_failed`. The
  between-operation arm must preserve prior evidence and issue no later write.
- [ ] Add
  `TestRecoveryAdoptInterruptedOperationUsesProviderSourceAndTerminalDisposition`.
  Inject a recording `provider.RsyncRestoreSourceResolver`; require the exact
  `NewRsyncRestoreSourceRef(plan)` tuple, one resolve/revalidate/close, no
  locator-bearing input, unresolved disposition for Verify error/invalid/
  mismatch, and normal operation plus needs-attention for exact target with an
  untrusted source.
- [ ] In both migration selectors require normal operation checkpoint
  `job_item_id`, mutating next != prior, skip next == prior, and exact current
  item/checkpoint/fence/lease binding for source-failure evidence. Reject cross-
  item, stale-fence, wrong-chain and unresolved-field contamination.
- [ ] Run genuine RED:

```bash
cd backend
go test ./internal/backupasset/recovery \
  -run '^(TestRecoveryExecuteClaimRevalidatesPinnedSourcePerOperation|TestRecoveryAdoptInterruptedOperationUsesProviderSourceAndTerminalDisposition)$' \
  -count=1
go test ./internal/database \
  -run '^TestBackupAssetMigration069WholeTask6ClosureSQLite$' -count=1
```

### Task 6-R3: Freeze durable marker-phase RED

**Files:** `recovery/worker_test.go`, `recovery/state_test.go`, migration
integration test.

- [ ] Add `TestRecoveryPrepareFirstWritePersistsMarkerCreatedBeforeContent`.
  The target fake reads the durable phase at `CreateOwnedJobDir`, first item
  mutation and retry. Require `reserved` at owned-directory call,
  `marker_created` before item I/O, immutable locator/binding/deadline reuse,
  `writing` only after the first operation checkpoint, and zero item calls after
  a forced marker-phase CAS failure.
- [ ] Retain the Go phase table and add every exact legal/same-value transition
  plus reserved-to-writing, reverse, skip and terminal-rewrite negatives to the
  paired whole-closure migration selectors.
- [ ] Run genuine RED:

```bash
cd backend
go test ./internal/backupasset/recovery \
  -run '^TestRecoveryPrepareFirstWritePersistsMarkerCreatedBeforeContent$' -count=1
go test ./internal/database \
  -run '^TestBackupAssetMigration069WholeTask6ClosureSQLite$' -count=1
```

Expected RED: current code stays `reserved` after the marker call and current
migrations admit illegal phase skips/reversals.

### Task 6-G1: Implement Correction 15 minimally

**Files:** `recovery/executor.go`, `recovery/worker.go`, paired `000069` up
migrations.

- [ ] Add private `writeCallFailed` and `observationCallFailed` booleans to
  `ordinaryOperationResult`. Set them only on the matching target call error;
  set the existing category and return `errOrdinaryRemoteOutcomeUnresolved` so
  the outer boundary performs the bounded terminal projection.
- [ ] Close `validOrdinaryUnresolvedResult`: a no-product arm requires exactly
  its call-failed flag; returned-invalid requires its returned flag; a call-
  failed flag and returned product are mutually exclusive. Do not hash errors or
  zero structs. `populateUnresolvedCheckpointTargetFacts` naturally leaves
  absent products empty.
- [ ] Widen only the exact paired category arms for no-product errors and
  adoption no-write mismatch. Preserve every predecessor, terminal, chain and
  evidence guard.
- [ ] Re-run the unchanged R1 commands to GREEN before continuing.

### Task 6-G2: Implement Correction 16 atomically

**Files:** `recovery/executor.go`, `recovery/worker.go`, paired up migrations.

- [ ] Add `SourceResolver provider.RsyncRestoreSourceResolver` to
  `WorkerCoordinatorDependencies`, retain it as `sourceResolver`, require it at
  construction, and inject a closed fake through the one shared Recovery test
  coordinator fixture.
- [ ] Implement a transaction-free helper with this exact closed result:

```go
func (coordinator *WorkerCoordinator) revalidateAdoptionSource(
    ctx context.Context,
    plan model.BackupAssetRecoveryPlan,
) SourceRevalidationOutcome
```

It derives the durable scalar ref, resolves, revalidates and closes on every
path. It returns only matched/drifted/failed; private errors never escape.
- [ ] Adoption always performs read-only Verify after source classification.
  Route call error/invalid/mismatch through `projectPendingOperationUnresolved`.
  Route exact observation through normal projection with the source outcome.
- [ ] Set exact `JobItemID` on every normal ordinary/adoption operation
  checkpoint. Emit a skip operation checkpoint with next == prior. Update
  in-place history validation to require item identity and permit equality only
  for skip.
- [ ] Refactor normal projection to accept `SourceRevalidationOutcome`. After
  item/checkpoint/chain persistence, non-matched source creates exact checkpoint-
  bound failure evidence, closes the attempt, releases both leases and CASes the
  job to `needs_attention/source_revalidation_failed` plus isolated
  `cleanup_due`. A later pre-operation failure performs only that terminal tail
  from the last item-bound checkpoint.
- [ ] Add the exact paired normal-operation/item/evidence guards without
  weakening the unresolved branch. Re-run R2 plus existing adoption derivation
  and Correction 14 selectors to GREEN.

### Task 6-G3: Implement Correction 17 and one-way phases

**Files:** `recovery/worker.go`, paired up migrations.

- [ ] Add this private fenced CAS helper and call it only after the owned result
  exactly matches object, marker binding and a valid target revision:

```go
func (coordinator *WorkerCoordinator) markWorkspaceMarkerCreated(
    ctx context.Context,
    claim RecoveryWorkerClaim,
    request CreateOwnedJobDirRequest,
) error
```

It re-locks latch/job/attempt/source/node and CASes reserved to marker-created;
an already matching marker-created retry is idempotent, every other state or
fence is lost.
- [ ] Workspace-object permit validation accepts reserved/marker-created for
  idempotent owned-directory handling. Item-object validation accepts only
  marker-created/writing. Durable item handoff rejects reserved. First item
  projection performs marker-created to writing.
- [ ] Extend the existing SQLite
  `trg_backup_asset_recovery_jobs_publication_integrity` trigger and the existing
  PostgreSQL `backup_asset_recovery_job_publication_integrity_guard()` function/
  trigger with design §29.4's edge table. Same-value is legal; in-place stays
  none; all other changing edges fail. The unchanged down migrations already
  drop those existing objects, so this step creates no new down-owned schema
  object.
- [ ] Re-run R3, F4 and F6 unchanged selectors to GREEN.

### Task 6-V1: Whole correction verification and review restart

- [ ] Run the complete focused/local matrix:

```bash
cd backend
go test ./internal/backupasset/recovery \
  -run '^(TestRecoveryExecuteClaimPostArmCallErrorsBecomeUnresolved|TestRecoveryExecuteClaimRevalidatesPinnedSourcePerOperation|TestRecoveryAdoptInterruptedOperationUsesProviderSourceAndTerminalDisposition|TestRecoveryPrepareFirstWritePersistsMarkerCreatedBeforeContent|TestRecoveryExecuteClaimProjectsUnresolvedRemoteOutcomeMatrix|TestRecoveryAdoptInterruptedOperationDurableDerivationMatrix|TestRecoveryReviewF4WorkspaceDeadlineAndPublication|TestRecoveryReviewF6LatchBeforeTargetMutation)$' \
  -count=1
go test ./internal/database \
  -run '^(TestBackupAssetMigration069WholeTask6ClosureSQLite|TestBackupAssetMigration069SQLite|TestBackupAssetMigration069PairedFiles)$' \
  -count=1
```

- [ ] Run focused race x10, full Recovery and affected packages, and vet:

```bash
cd backend
go test -race ./internal/backupasset/recovery \
  -run '^(TestRecoveryExecuteClaimPostArmCallErrorsBecomeUnresolved|TestRecoveryExecuteClaimRevalidatesPinnedSourcePerOperation|TestRecoveryAdoptInterruptedOperationUsesProviderSourceAndTerminalDisposition|TestRecoveryPrepareFirstWritePersistsMarkerCreatedBeforeContent)$' \
  -count=10
go test ./internal/backupasset/recovery ./internal/backupasset/provider \
  ./internal/backupasset/repository ./internal/backupasset/runtime \
  ./internal/model ./internal/database -count=1
go vet ./internal/backupasset/recovery ./internal/model ./internal/database
```

- [ ] Run required real PostgreSQL with no skip:

```bash
cd backend
REQUIRE_POSTGRES_MIGRATION_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" \
  go test ./internal/database \
  -run '^(TestBackupAssetMigration069WholeTask6ClosurePostgres|TestBackupAssetMigration069Postgres)$' \
  -count=1
```

- [ ] Fix the seven recorded Recovery lint findings only if still current; run
  the configured linter, owned `gofmt -d`, `git diff --check`, Trellis validate,
  JSON/JSONL parse, exact-manifest, protected-path hash and staged-zero gates.
  Do not touch `go.mod` or
  `recovery/testdata/rsync_local_to_remote.json`.
- [ ] Record exact RED/GREEN evidence and update Corrections 15--17 status.
  Reopen whole Task 6 specification review, then whole quality review. Do not
  begin Task 7 in the same unchecked batch.

## 10. Task 6 Correction 18 Isolated Adoption Continuation Plan (2026-08-03)

> **Execution mode:** bounded inline TDD in the existing branch/worktree. Do not
> use a goal, heartbeat or subagent. Do not stage, commit, push or start Task 7.

**Goal:** Preserve exact isolated checkpoint history and safely continue only
the pending work after the current takeover has adopted its ambiguous operation.

**Architecture:** Keep marker creator provenance immutable while current
attempt/source/node fences authorize item writes. A full history validator
admits either a fresh one-reservation execution or a current-checkpoint-bound
continuation; adoption keeps the attempt active while pending work remains and
uses ordinary terminalization when none remains.

**Tech stack:** Go, GORM transactions/CAS, SQLite and PostgreSQL paired
regressions, repository-owned Rsync source contracts.

### Task 6-R4: Freeze multi-item continuation RED

**Files:**

- Modify: `backend/internal/backupasset/recovery/executor_test.go`
- Modify: `backend/internal/backupasset/recovery/worker_test.go`

- [ ] Extend `TestRecoveryAdoptsLaterIsolatedOperationAfterPriorCheckpoint` so
  `ExecuteClaim` on the fresh takeover before adoption returns
  `ErrRecoveryWorkerFenceLost` with no additional target write.
- [ ] After adoption, assert the earlier checkpoint is byte-for-byte unchanged,
  workspace owner/fence/marker binding equal their pre-takeover values, the
  selected item is completed, one item remains pending, and current attempt,
  source lease and node lease remain active.
- [ ] Invoke `ExecuteClaim` with the same takeover claim and a fresh closed
  Repository source. Require only the pending item to execute, no duplicate
  write of completed items, a complete target chain, released leases, completed
  attempt and `succeeded|sealed` job.
- [ ] Update the stable sequence-zero create/skip/adoption projection assertions
  to expect a running attempt whenever their default three-item fixture still
  has pending work. Preserve the existing post-terminal rewrite rejection.
- [x] Run:

```bash
cd backend
go test ./internal/backupasset/recovery \
  -run '^(TestRecoveryAdoptsLaterIsolatedOperationAfterPriorCheckpoint|TestRecoveryAdoptInterruptedOperationDurableDerivationMatrix|TestWorkerAdoptionAtomicallyProjectsVerificationAndRejectsPostTerminalRewrite)$' \
  -count=1
```

Expected genuine RED: pre-adoption replay remains rejected, but adoption closes
the takeover attempt and/or continuation cannot obtain a valid item permit.

### Task 6-G4: Implement closed continuation admission

**Files:**

- Modify: `backend/internal/backupasset/recovery/worker.go`
- Modify: `backend/internal/backupasset/recovery/executor.go`

- [ ] Keep `ordinaryCheckpointOperation` shared by isolated/in-place history and
  validate isolated reservation plus every completed-item checkpoint. Bind the
  complete validated history into `interruptedOperationDurableDigest`.
- [ ] Admit ordinary isolated handoff history only through these predicates:

```go
fresh := len(checkpoints) == 1 && checkpoints[0].AttemptID == claim.AttemptID
continuation := len(checkpoints) > 1 &&
    job.WorkspacePhase == string(WorkspacePhaseWriting) &&
    checkpoints[len(checkpoints)-1].AttemptID == claim.AttemptID &&
    checkpoints[len(checkpoints)-1].AttemptFence == claim.AttemptFence &&
    checkpoints[len(checkpoints)-1].NodeFence == claim.NodeFence
```

  Both paths still require exact reservation/history/item/chain validation;
  every other shape returns `ErrRecoveryWorkerFenceLost` before target I/O.
- [ ] Let the already-armed `prepareFirstWrite` path reuse the workspace permit
  for an authenticated continuation. At `writing`, do not construct a
  `CreateOwnedJobDirRequest` and do not call marker creation/CAS again.
- [ ] In `validateFirstWritePermitAt`, require marker owner/fence to match the
  current claim only for workspace creation or pre-writing item permits. For a
  `writing` item permit, rely on the immutable marker product plus current
  attempt/source/node/latch validation.
- [ ] Remove workspace owner/fence updates from adoption and ordinary operation
  projections. They are immutable marker provenance, not current attempt state.
- [ ] In `projectInterruptedOperationTx`, count pending items after the item and
  checkpoint projection. Return with attempt/leases active when pending is
  nonzero; when zero, reuse the ordinary close/release/job-success transaction
  shape rather than leaving a running job with a completed attempt.
- [ ] Re-run the unchanged R4 selector and require GREEN.

### Task 6-V2: Correction 18 verification and whole-review restart

- [ ] Run the adoption/source/F3 companions:

```bash
cd backend
go test ./internal/backupasset/recovery \
  -run '^(TestRecoveryAdoptsLaterIsolatedOperationAfterPriorCheckpoint|TestRecoveryAdoptInterruptedOperationUsesProviderSourceAndTerminalDisposition|TestRecoveryAdoptInterruptedOperationDurableDerivationMatrix|TestWorkerConcurrentAdoptersProduceOneCheckpoint|TestRecoveryExecuteClaimSupersedesProviderDriftBeforeFirstWrite)$' \
  -count=1
go test -race ./internal/backupasset/recovery \
  -run '^(TestRecoveryAdoptsLaterIsolatedOperationAfterPriorCheckpoint|TestRecoveryAdoptInterruptedOperationUsesProviderSourceAndTerminalDisposition|TestWorkerConcurrentAdoptersProduceOneCheckpoint)$' \
  -count=10
```

- [ ] Run the complete Corrections 15--18, F3/F4/F6, whole Recovery, affected
  packages, paired SQLite and required-real-PostgreSQL matrices already listed
  in Task 6-V1; no skipped PostgreSQL selector is completion evidence.
- [ ] Run `go vet`, configured Recovery lint, owned `gofmt -d`,
  `git diff --check`, both Trellis validations, JSON/JSONL parsing, exact
  `9 + 55 + 81 = 145` manifest reconciliation, protected-file hashes and
  staged-zero checks.
- [ ] Record RED/GREEN and gate evidence in the existing task ledgers, then
  reopen whole Task 6 specification review and whole quality review. Stop before
  Task 7 and before all Git delivery actions.

## 11. Task 6 Correction 19 Durable Marker-Validation Takeover Plan (2026-08-03)

> **Execution mode:** controller-approved bounded inline TDD in the existing
> branch/worktree. Do not use a goal, heartbeat or subagent. Do not stage,
> commit, push, create a PR or start Task 7.

**Goal:** Let a fresh takeover safely finish marker validation after an old
claim's remote marker success/CAS crash, without rewriting marker creator
provenance or permitting blind first-item replay.

**Architecture:** Add one closed job-level validation tuple for the attempt and
node fences that atomically committed `marker_created`. Keep creator provenance,
marker validation provenance, current live fences and operation-checkpoint
adoption as four distinct products.

### Task 6-R5: Freeze marker-takeover RED

**Files:**

- Modify: `backend/internal/backupasset/recovery/worker_test.go`
- Modify: `backend/internal/backupasset/recovery/executor_test.go`
- Modify: `backend/internal/model/backup_asset_recovery_test.go`
- Modify: `backend/internal/database/backup_asset_migrations_integration_test.go`

- [x] Add `TestRecoveryReservedMarkerTakeoverPersistsValidationBeforeFirstItem`.
  Inject the existing marker-created job CAS failure after a successful
  `CreateOwnedJobDir`; assert `reserved`, one immutable reservation, empty tuple
  and no item call. Expire the old claim, take over, and require the exact same
  workspace request, unchanged creator provenance, current tuple committed
  before first item I/O and normal terminal completion.
- [x] Add `TestRecoveryMarkerCreatedTakeoverRequiresAdoptionBeforeReplay`.
  Commit marker creation without starting an item, expire/take over, invoke
  ordinary execution, and require fence loss before workspace or item mutation;
  the old validation tuple must remain unchanged.
- [x] Extend the model closed-product selector for the three private fields and
  both whole-Task-6 migration selectors for paired columns, all-or-none phase
  shape, atomic assignment and immutable rewrite rejection.
- [x] Run the exact new selectors before product changes and record genuine RED
  caused by missing fields/behavior/DDL rather than compilation or harness
  diagnostics.

### Task 6-G5: Implement the minimal closed validation product

**Files:**

- Modify: `backend/internal/backupasset/recovery/worker.go`
- Modify: `backend/internal/model/backup_asset_recovery.go`
- Modify: `backend/internal/database/migrations/sqlite/000069_backup_asset_recovery.up.sql`
- Modify: `backend/internal/database/migrations/sqlite/000069_backup_asset_recovery.down.sql`
- Modify: `backend/internal/database/migrations/postgres/000069_backup_asset_recovery.up.sql`
- Modify: `backend/internal/database/migrations/postgres/000069_backup_asset_recovery.down.sql`

- [x] Add private non-null/default model fields matching the paired snake-case
  columns. Keep every in-place and pre-reservation producer at empty/zero.
- [x] Permit a current fenced claim to use a workspace-only permit for an old
  sequence-zero reservation only while phase is `reserved` and the tuple is
  empty. Do not admit an old-tuple `marker_created` job to ordinary execution.
- [x] In `markWorkspaceMarkerCreated`, validate immutable marker provenance and
  all current attempt/source/node/latch products, then CAS
  `reserved + empty tuple -> marker_created + current tuple`. A same-claim
  `marker_created` retry is idempotent only when the tuple matches exactly.
- [x] At `marker_created`, require item permits and ordinary handoff to match all
  tuple fields to the current claim. At `writing`, retain Correction 18's latest
  current operation-checkpoint gate. Include the tuple in the durable handoff
  digest and never rewrite it during adoption/projection.
- [x] Extend paired CHECK/trigger enforcement for phase shape, atomic initial
  assignment and immutability. Preserve existing one-way edges, creator
  provenance immutability, down behavior and paired text parity.
- [x] Run the unchanged R5 selectors and require GREEN.

### Task 6-V3: Focused and whole-review gates

- [x] Run R5 normal and race repetition with the existing marker/adoption/F4/F6
  companions and full Recovery package.
- [x] Run the paired SQLite whole-Task-6 selector, paired-file selector and
  required-real-PostgreSQL whole-Task-6 selector with no skip.
- [x] Run model/database packages, same-scope `go vet`, configured lint, owned
  `gofmt -d`, `git diff --check`, both Trellis validations, JSON/JSONL parsing,
  exact 145-path manifest, protected hashes and staged-zero checks.
- [x] Record RED/GREEN and exact gate evidence. Keep the consumed exact-mirror
  fresh-takeover finding open for a separate controlling amendment; do not call
  whole Task 6 complete or start Task 7.

## 12. Task 6 Correction 20 Consumed Exact-Mirror Takeover Plan (2026-08-03)

> **Execution mode:** user-approved bounded inline TDD in the existing
> branch/worktree. Do not use a goal, heartbeat or subagent. Do not stage,
> commit, push, create a PR or start Task 7.

**Goal:** Let a fresh in-place takeover reconcile an ambiguous delete under the
already-consumed immutable delete-set authority, while keeping ordinary replay
closed until the current claim has appended an adoption checkpoint.

**Architecture:** Validate the historical required/consumed checkpoint pair and
grant against each other, not against the later takeover claim. Preserve the
existing current live-fence checks and latest-current-operation-checkpoint gate
as the separate continuation authority.

### Task 6-R6: Freeze fresh-takeover RED

**Files:**

- Modify: `backend/internal/backupasset/recovery/executor_test.go`

- [x] Add
  `TestRecoveryExactMirrorConsumedAuthorityFreshTakeoverRequiresAdoption`.
  Consume one delete-set grant, crash after the first remote Delete but before
  its operation checkpoint, expire the original attempt and take over.
- [x] Prove `ExecuteClaim` on the fresh claim fails before another target
  mutation, without rewriting the historical pair or grant.
- [x] Prove exact-absence adoption appends a current-claim operation checkpoint,
  leaves the takeover attempt active while another delete is pending, and then
  permits bearer-free continuation to execute the remaining delete exactly
  once and finish normally.
- [x] Run the exact selector against the current implementation and record a
  genuine failure caused by the historical/current-claim conflation.

### Task 6-G6: Separate historical and current authority

**Files:**

- Modify: `backend/internal/backupasset/recovery/worker.go`
- Modify: `backend/internal/backupasset/recovery/executor.go`

- [x] Change consumed-delete grant validation so the required and consumed
  checkpoints have the same historical attempt id/fences and the grant's
  delete-attempt tuple matches those checkpoints.
- [x] Remove only the invalid comparison between that historical tuple and the
  current claim. Keep all existing grant/job/plan/binding/expiry/consumption
  checks and all current attempt/source/node/latch checks.
- [x] Keep the latest-current-operation-checkpoint condition unchanged so
  ordinary execution before adoption still fails closed.
- [x] Run the unchanged R6 selector and require GREEN.

### Task 6-V4: Focused and whole-review gates

- [x] Run R6 normally and under `-race -count=10`, plus the existing consumed
  reload, same-execution multi-delete, same-claim restart and adoption
  regressions.
- [x] Run the full Recovery package normally and under race, then model,
  database, backend lint, same-scope vet, owned `gofmt -d` and
  `git diff --check` gates.
- [x] Re-run Child/parent Trellis validation, JSON/JSONL parsing, exact
  145-path manifest, protected hashes, branch/HEAD and staged-zero checks.
- [x] Record RED/GREEN and exact gate evidence. Do not call whole Task 6
  complete, start Task 7, stage, commit, push or create a PR.

## 13. Task 7 Unpublished Workspace Cleanup Claim Plan (2026-08-03)

> **Execution mode:** user-approved bounded inline TDD in the existing
> branch/worktree. Do not use a goal, heartbeat or subagent. Do not create or
> switch a branch/worktree, and do not stage, commit, push or create a PR.

**Goal:** Add a cleanup-specific durable owner/fence tuple to unpublished
`cleanup_due` jobs and admit one fenced cleanup owner under the shared target
node writer boundary without rewriting Task 6 marker provenance.

**Architecture:** Keep the workspace cleanup row on the existing job aggregate,
with a separate private seven-field tuple and paired SQL state machine. Claim
from an unlocked snapshot, lock the job, acquire a fresh `recovery_cleanup`
node lease, then CAS the cleanup tuple; release the new lease in the committed
transaction when the CAS loses.

### Task 7-R1: Freeze model and paired-schema RED

**Files:**

- Modify: `backend/internal/model/backup_asset_recovery_test.go`
- Modify: `backend/internal/database/backup_asset_migrations_integration_test.go`

- [x] Add `TestBackupAssetRecoveryJobCarriesPrivateWorkspaceCleanupTuple` with
  the exact seven Go field names and require `json:"-"`, non-null/default GORM
  tags, plus nullable time/node-lease types.
- [x] Add paired
  `TestBackupAssetMigration069WorkspaceCleanupOwnershipSQLite/Postgres` coverage
  for the exact columns, neutral/active/retryable/tombstoned shapes, composite
  node-lease linkage, permitted phase transitions and rejection of provenance
  rewrites, active-owner replacement and stale/non-monotonic fences.
- [x] Run:

  ```text
  cd backend && go test ./internal/model -run '^TestBackupAssetRecoveryJobCarriesPrivateWorkspaceCleanupTuple$' -count=1
  cd backend && go test ./internal/database -run '^TestBackupAssetMigration069WorkspaceCleanupOwnershipSQLite$' -count=1
  ```

  Require genuine RED from missing fields/columns, not a harness or syntax
  error. Do not edit production/model/migration code before both RED products
  are observed.

### Task 7-G1: Add the minimal paired durable product

**Files:**

- Modify: `backend/internal/model/backup_asset_recovery.go`
- Modify: `backend/internal/database/migrations/sqlite/000069_backup_asset_recovery.up.sql`
- Modify: `backend/internal/database/migrations/postgres/000069_backup_asset_recovery.up.sql`

- [x] Add the seven private model fields and exact paired columns inside
  existing `000069`; keep `workspace_cleanup_phase='claimed'` and all other
  cleanup fields neutral by default.
- [x] Add the four closed row shapes, allowed claim/takeover/progress/failure/
  tombstone transitions, published-job immutability and the job-bound cleanup
  node-lease FK. Keep execution owner/fence and marker tuple immutable, and
  explicitly tear down only the added guards in pristine paired down.
- [x] Run the unchanged model and SQLite selectors and require GREEN. Then run
  the required real PostgreSQL selector with no skip when the configured DSN is
  available.

### Task 7-R2: Freeze unpublished claim RED

**Files:**

- Modify: `backend/internal/backupasset/recovery/result_lifecycle_test.go`

- [x] Add `TestRecoveryWorkspaceCleanupClaimLocksJobBeforeNodeLeaseAndCAS`.
  Require unlocked candidate read, locked job/workspace row, shared node
  admission, fresh cleanup lease, job CAS, and unchanged execution/marker
  provenance.
- [x] Add `TestRecoveryWorkspaceCleanupClaimRetriesFailureAndTakesOverExpiredOwner`.
  Require retryable failure to claim at `claimed`, expired active takeover to
  preserve its durable phase, and both paths to increment cleanup and node
  fences/attempts.
- [x] Add `TestRecoveryWorkspaceCleanupClaimRejectsActiveInvalidOrBusyCandidate`
  for an active owner, non-cleanup phase, in-place/nonterminal/published jobs and
  shared-node conflict, with no cleanup lease leak.
- [x] Add `TestRecoveryWorkspaceCleanupLostCASReleasesFreshNodeLeaseInTransaction`
  and force the job CAS to affect zero rows; require one durable `released`
  lease and an unchanged cleanup tuple.
- [x] Run:

  ```text
  cd backend && go test ./internal/backupasset/recovery -run '^TestRecoveryWorkspaceCleanup' -count=1
  ```

  Require compile/behavior RED caused by the absent request/method/product.

### Task 7-G2: Implement only claim admission

**Files:**

- Modify: `backend/internal/backupasset/recovery/result_lifecycle.go`

- [x] Add closed `ClaimRecoveryWorkspaceCleanupRequest` and
  `RecoveryWorkspaceCleanupClaim` products plus `ClaimWorkspaceCleanup`.
- [x] Reuse the current cleanup node-admission/fence/lease and lost-CAS release
  mechanics. Validate the complete unlocked job snapshot again under the job
  lock; do not add a second workspace-row lock or reuse execution provenance.
- [x] Initial/retryable claims enter `claimed`; expired active takeover preserves
  phase. Map shared node contention to `ErrRecoveryResultCleanupBusy` and every
  invalid/lost candidate to the closed cleanup conflict.
- [x] Run the unchanged R2 selector and existing published cleanup selectors;
  require GREEN before any refactor.

### Task 7-V1: Focused verification and stop point

- [x] Run focused workspace/published cleanup and migration/model tests normally
  and under the declared race repetition, then full Recovery and Content
  packages, affected `go vet`, `make lint-backend`, owned `gofmt -d` and
  `git diff --check`.
- [x] Run paired SQLite and required real-PostgreSQL `000069` selectors with no
  skip, both Trellis validators, JSON/JSONL parsing, exact manifest/protected
  hash/staged-zero checks, and record fresh evidence.
- [x] Stop with Task 7 still `in_progress`. Do not implement revoke, drain,
  renewal, target validation, remote delete, failure/tombstone execution,
  orphan/runtime scheduling or whole Task 7 closure; do not stage, commit, push
  or create a PR.

## 14. Task 7 Resource-Scoped Revoke And Drain Plan (2026-08-03)

> **Execution mode:** bounded inline TDD in the existing branch/worktree. Do
> not use a goal, heartbeat or subagent. Do not create or switch a branch or
> worktree, and do not stage, commit, push or create a PR.

**Goal:** Revoke and drain only one Recovery job's Content authority under its
current cleanup/node fences, persist both published and unpublished cleanup
through `drained`, and keep target deletion impossible in this batch.

**Architecture:** Content owns grant mutation, in-memory read cancellation and
Content lease release. Result lifecycle owns the caller transaction, cleanup
and node fences, lease renewal and durable phase CAS. Published cleanup crosses
that narrow transaction boundary; unpublished cleanup advances the same phases
without touching Content.

### Task 7-R3: Freeze resource-scoped Content RED

**Files:**

- Modify: `backend/internal/backupasset/content/broker_test.go`

- [x] Add a RecoveryResult cleanup selector that starts a blocking Recovery
  read, keeps a different Recovery job and a backup-asset grant live, and
  requires transaction-scoped revocation to affect only the selected job.
- [x] Require post-commit cancellation to reject a late read registration,
  bounded drain to join the selected read and release its exact Content lease,
  and unrelated grants/bindings/leases to remain active.
- [x] Add invalid-job/reason/transaction and canceled-drain coverage. Run the
  exact selector and require compile/behavior RED caused by the absent scoped
  Broker product.

### Task 7-G3: Implement only the Content boundary

**Files:**

- Modify: `backend/internal/backupasset/content/broker.go`

- [x] Add `RevokeRecoveryResultGrantsTx`,
  `CancelRecoveryResultReads` and `DrainRecoveryResult` with the exact §34.2
  scope. Reuse existing grant transition, read registration, wait and lease
  release mechanics; do not pause global admission or weaken the asset arm.
- [x] Add the mutex-protected revoked-grant registration barrier and require
  terminal/zero-in-flight durable state before drain success.
- [x] Run the unchanged R3 selector and existing Recovery issuance/logout/global
  drain regressions; require GREEN before lifecycle production edits.

### Task 7-R4: Freeze durable phase and renewal RED

**Files:**

- Modify: `backend/internal/backupasset/recovery/result_lifecycle_test.go`

- [x] Add published cleanup tests for atomic grant revoke + lease renewal +
  `claimed -> revoked`, post-commit cancel, bounded drain and a second fenced
  renewal + `revoked -> drained`.
- [x] Prove revoke failure rolls back phase/grants, drain failure remains
  `revoked`, and an expired/replaced cleanup or node fence causes zero Content
  calls and zero lifecycle mutation.
- [x] Add unpublished workspace coverage proving the same renewal and phase
  sequence with no Content call and unchanged execution/marker provenance.
- [x] Run the exact selector and require compile/behavior RED caused by the
  absent lifecycle dependency and methods.

### Task 7-G4: Implement the durable revoke/drain boundary

**Files:**

- Modify: `backend/internal/backupasset/recovery/result_lifecycle.go`

- [x] Add the narrow Content lifecycle dependency and explicit published and
  workspace revoke/drain methods. Lock `job -> cleanup node lease ->
  ResultSet/workspace`, validate every claim field, renew both expiries and use
  sequential phase CAS only.
- [x] Keep Content drain outside database transactions. Relock and revalidate
  after it returns; a lost fence must not mutate the phase, and a retry resumes
  from durable `revoked`.
- [x] Run the unchanged R4 and R3 selectors plus existing claim/retain/delivery
  regressions and require GREEN.

### Task 7-V2: Focused verification and stop point

- [x] Run the scoped Content and Recovery selectors normally and under
  `-race -count=5`, then full Content and Recovery packages, affected
  `go vet`, `make lint-backend`, owned `gofmt -d` and `git diff --check`.
- [x] Re-run both Trellis validators, JSON/JSONL parsing, exact manifest,
  protected hashes and staged-zero checks; record fresh RED/GREEN and gate
  evidence in the existing implementation ledger and task metadata.
- [x] Stop with Task 7 still `in_progress`. Do not implement target validation,
  remote deletion, failure/tombstone execution, orphan/runtime scheduling or
  whole Task 7 closure; do not stage, commit, push or create a PR.

## 15. Task 7 Cleanup Target Validation Plan (2026-08-03)

> **Execution mode:** bounded inline TDD in the existing branch/worktree. Do
> not use a goal, heartbeat or subagent. Do not create or switch a branch or
> worktree, and do not stage, commit, push or create a PR. This section is a
> reviewed implementation plan only; product edits require a separate user
> approval after the planning gates below pass.

**Execution status (2026-08-03):** `complete_checked` at the focused cleanup
target-validation boundary. R5--R7 retain genuine RED evidence, G5--G7 are
GREEN, and V3 passed including required real PostgreSQL. Task 7 remains
`in_progress`; execution stopped at durable `validated` with every destructive
target operation uncalled.

**Goal:** Advance current published and unpublished cleanup owners from durable
`drained` to durable `validated` through one exact, read-only target
observation, while projecting validation failure into a retryable ownerless
`drained` state and keeping every destructive target operation unreachable.

**Architecture:** Replace cleanup's execution-permit wrapper with an immutable,
cleanup-specific local proof. `ResultLifecycleService` performs one short
renewal transaction, calls `ValidateOwnedJobDir` outside all transactions, then
performs one short closing transaction. Success keeps the current cleanup and
node authority at `validated`; a closed validation failure atomically releases
that exact node lease and preserves `drained` for a fresh-fence retry.

### 15.1 Exact file and responsibility map

**Product and focused tests:**

- Modify: `backend/internal/backupasset/recovery/target.go`: cleanup-specific
  permit, immutable proof, closed validation request/result and `TargetPort`
  observation method; restore `TargetMutationPermit` to write-only.
- Modify: `backend/internal/backupasset/recovery/target_test.go`: exact permit,
  proof, operation/resource/object and closed-interface tests.
- Modify: `backend/internal/backupasset/recovery/preflight_test.go`: add the
  fail-fast validation method to the read-only full-port fake.
- Modify: `backend/internal/backupasset/recovery/result_lifecycle.go`: inject
  `TargetPort`; add published/workspace validation, two-transaction renewal,
  retryable failure projection and `drained` retry admission.
- Modify: `backend/internal/backupasset/recovery/result_lifecycle_test.go`:
  recording target, success/failure/takeover/latch/root/marker/retry matrices.
- Modify:
  `backend/internal/database/migrations/sqlite/000069_backup_asset_recovery.up.sql`
  Responsibility: let an ownerless positive-history retry preserve, but never
  change, its non-tombstoned workspace cleanup phase.
- Modify:
  `backend/internal/database/migrations/postgres/000069_backup_asset_recovery.up.sql`
  Responsibility: apply the exact PostgreSQL equivalent of the SQLite guard
  correction.
- Modify: `backend/internal/database/backup_asset_migrations_integration_test.go`:
  prove the paired retry guard with the existing workspace-cleanup selector.

**Evidence after GREEN:**

- Modify:
  `.trellis/tasks/07-28-backup-assets-controlled-recovery/implement.md`: mark
  only R5/G5/R6/G6/R7/G7/V3 evidence actually observed.
- Modify:
  `.trellis/tasks/07-28-backup-assets-controlled-recovery/research/implementation-evidence.md`
  Responsibility: append exact RED/GREEN commands, outputs and focused gate
  results.
- Modify: `.trellis/tasks/07-28-backup-assets-controlled-recovery/task.json`:
  retain Child/Task 7 `in_progress`, add only the validated-boundary focused
  credit, and leave delete/tombstone/orphan/whole-review work open.

No down migration changes are required because this batch adds no schema
object. No model, API, runtime, settings, Provider, Repository, SSH/SFTP,
frontend, `go.mod`, root-level `recovery/`, deletion or tombstone path is in
scope. Production target-root registration, root-locator plan population,
marker document/codec interoperability and a concrete target adapter remain
unclaimed.

### Task 7-R5: Freeze cleanup permit and closed target observation RED

**Files:**

- Modify: `backend/internal/backupasset/recovery/target_test.go`
- Modify: `backend/internal/backupasset/recovery/preflight_test.go`

- [x] Add `TestTargetCleanupPermitBindsExactValidationAuthority`. Build the
  approved cleanup product with every field below, first require the raw
  literal to fail without its private proof, then issue it through the private
  test-visible issuer and require success:

  ```go
  permit := TargetCleanupPermit{
      SchemaVersion: 1,
      Purpose: TargetPurposeCleanup,
      Operation: TargetCleanupValidateOwnedJobDir,
      ResourceKind: CleanupResourceResultSet,
      ResourceID: strings.Repeat("1", 32),
      JobID: strings.Repeat("2", 32),
      CleanupOwner: "cleanup-validation-owner",
      CleanupFence: 3,
      CleanupAttempt: 4,
      NodeID: 7,
      NodeLeaseID: strings.Repeat("5", 32),
      NodeFence: 6,
      RootID: "root-a",
      RootLocatorDigest: rootLocatorDigest,
      TargetPathDigest: object.TargetPathDigest,
      RootRevision: "root-revision-1",
      MarkerBindingDigest: strings.Repeat("b", sha256DigestLength),
      UseLatchID: RecoverySchemaUseLatchID,
      ExpiresAt: now.Add(time.Minute),
  }
  ```

  Table-mutate schema, purpose, operation, resource kind/ID, job, cleanup
  owner/fence/attempt, node/lease/fence, root/locator/path/revision, marker,
  latch and expiry after issuance; every mutation must invalidate the proof.
  Require workspace authority to bind `ResourceID == JobID`, validation
  authority to reject `remove_owned_job_dir`, a different object or marker to
  fail, and an expired permit to fail. Assert the type has no execution
  `AttemptID`, `AttemptFence`, source fence or `ExpectedTargetRevision` field.
- [x] Add
  `TestTargetPortValidateOwnedJobDirUsesClosedCleanupObservationBoundary` and
  reflect the exact method contract:

  ```go
  ValidateOwnedJobDir(
      context.Context,
      TargetCleanupPermit,
      ValidateOwnedJobDirRequest,
  ) (OwnedJobDirValidation, error)
  ```

  Update the exact `TargetPort` method-set and purpose-exact maps to include
  `ValidateOwnedJobDir` once and only once. Add it to `closedTargetPortFake`
  with an exact request-derived observation; add a panic implementation to
  `readOnlyPreflightTargetFake`. Keep `RemoveOwnedJobDir` present and separate.
- [x] Add `TestTargetMutationPermitIsWriteOnly`. A proof-bearing
  `TargetMutationPermit` with `Purpose=TargetPurposeCleanup` must fail, while
  the existing write permit remains valid. Remove the old cleanup constructor
  arm from `TestTargetPurposeSpecificPermitConstructionRejectsCrossPurpose`;
  cleanup is covered only by the new cleanup-specific product.
- [x] Run the unchanged package with the exact focused selector before any
  production edit:

  ```bash
  cd backend
  go test ./internal/backupasset/recovery \
    -run '^(TestTargetCleanupPermitBindsExactValidationAuthority|TestTargetPortValidateOwnedJobDirUsesClosedCleanupObservationBoundary|TestTargetMutationPermitIsWriteOnly|TestTargetPortExposesOnlyClosedMethods|TestTargetPortOperationPermitsArePurposeExact|TestTargetPurposeSpecificPermitConstructionRejectsCrossPurpose)$' \
    -count=1
  ```

  Expected RED: missing cleanup resource/operation/request/observation types or
  missing `ValidateOwnedJobDir`; a syntax, fixture or unrelated package failure
  is not RED evidence.

### Task 7-G5: Implement the cleanup-only target contract

**Files:**

- Modify: `backend/internal/backupasset/recovery/target.go`

- [x] Add the exact closed enums and request/result shapes from design §35:

  ```go
  type CleanupResourceKind string

  const (
      CleanupResourceResultSet CleanupResourceKind = "result_set"
      CleanupResourceWorkspace CleanupResourceKind = "workspace"
  )

  type TargetCleanupOperation string

  const (
      TargetCleanupValidateOwnedJobDir TargetCleanupOperation = "validate_owned_job_dir"
      TargetCleanupRemoveOwnedJobDir   TargetCleanupOperation = "remove_owned_job_dir"
  )

  type TargetCleanupPermit struct {
      SchemaVersion       int
      Purpose             TargetPurpose
      Operation           TargetCleanupOperation
      ResourceKind        CleanupResourceKind
      ResourceID          string
      JobID               string
      CleanupOwner        string
      CleanupFence        uint64
      CleanupAttempt      uint64
      NodeID              uint
      NodeLeaseID         string
      NodeFence           uint64
      RootID              string
      RootLocatorDigest   string `json:"-"`
      TargetPathDigest    string `json:"-"`
      RootRevision        string
      MarkerBindingDigest string `json:"-"`
      UseLatchID          string
      ExpiresAt           time.Time
      proof               *targetCleanupPermitProof
  }

  type ValidateOwnedJobDirRequest struct {
      Object              TargetObjectRef
      MarkerBindingDigest string
  }

  type OwnedJobDirValidation struct {
      Object              TargetObjectRef
      MarkerBindingDigest string
      RootRevision        string
      TargetRevision      string
  }
  ```

- [x] Replace the old cleanup wrapper with the exact standalone
  `TargetCleanupPermit` in design §35.2. Its package-private proof stores a
  digest over every public field using the domain
  `xirang/recovery/target-cleanup-permit/v1`, length-framed values,
  `strconv.FormatUint`, and `ExpiresAt.UTC().Format(time.RFC3339Nano)`:

  ```go
  type targetCleanupPermitProof struct {
      bindingDigest string
  }

  func targetCleanupPermitBindingDigest(permit TargetCleanupPermit) string {
      return framedDigest(
          "xirang/recovery/target-cleanup-permit/v1",
          strconv.Itoa(permit.SchemaVersion), string(permit.Purpose),
          string(permit.Operation), string(permit.ResourceKind),
          permit.ResourceID, permit.JobID, permit.CleanupOwner,
          strconv.FormatUint(permit.CleanupFence, 10),
          strconv.FormatUint(permit.CleanupAttempt, 10),
          strconv.FormatUint(uint64(permit.NodeID), 10), permit.NodeLeaseID,
          strconv.FormatUint(permit.NodeFence, 10), permit.RootID,
          permit.RootLocatorDigest, permit.TargetPathDigest,
          permit.RootRevision, permit.MarkerBindingDigest, permit.UseLatchID,
          permit.ExpiresAt.UTC().Format(time.RFC3339Nano),
      )
  }

  func issueTargetCleanupPermit(permit TargetCleanupPermit) TargetCleanupPermit {
      permit.proof = &targetCleanupPermitProof{
          bindingDigest: targetCleanupPermitBindingDigest(permit),
      }
      return permit
  }
  ```

  `ValidateAt` must validate the complete shape and compare that immutable
  digest. `CleanupResourceKind.valid` accepts only the two declared kinds and
  `TargetCleanupOperation.valid` accepts only the two declared operations. Add
  `ValidateOperationObjectAt` and
  `ValidateOwnedJobDirRequestAt`; the latter requires the exact validation
  operation, exact recomputed object and exact marker digest. It performs no DB
  read. A workspace resource additionally requires `ResourceID == JobID`.
- [x] Change `TargetMutationPermit.validateShapeAt` to accept only
  `TargetPurposeWrite`; delete the old `NewTargetCleanupPermit` wrapper and its
  `ValidateFrozenJobAt` execution binding. Add the new observation method to
  `TargetPort` without changing `TargetObservationPort`.
- [x] Re-run R5 unchanged and require GREEN. Then run all target/preflight
  tests to catch interface-fake drift:

  ```bash
  cd backend
  go test ./internal/backupasset/recovery \
    -run '^(TestTarget|TestPreflight)' -count=1
  ```

### Task 7-R6: Freeze two-transaction success and lost-fence behavior RED

**Files:**

- Modify: `backend/internal/backupasset/recovery/result_lifecycle_test.go`

- [x] Add a `recoveryCleanupValidationTargetFake` that embeds
  `closedTargetPortFake`, validates the request locally through
  `permit.ValidateOwnedJobDirRequestAt`, records permit/request/call order,
  queries the durable row during the target call, supports before/after-observation
  takeover hooks, returns an exact observation by default, and counts
  `RemoveOwnedJobDir` separately. Inject it through the lifecycle fixture and
  insert one exact permanent use-latch evidence row because the fixture
  simulates a post-write terminal job.
- [x] Add
  `TestRecoveryCleanupTargetValidationRenewsAndAdvancesPublishedAndWorkspace`.
  For a published ResultSet and an unpublished workspace independently:

  ```text
  claim -> revoke -> drain -> validate
  phase: drained -> validated
  first renewal commits before one target observation
  second renewal and phase CAS commit after that observation
  cleanup fence/attempt and node lease/fence stay equal
  cleanup and node expiries are equal and strictly monotonic
  owner and active node lease remain for the delete batch
  execution marker provenance is byte-for-byte unchanged
  RemoveOwnedJobDir calls = 0
  ```

  Record GORM query events and require the ordered subsequence
  `job -> cleanup node lease -> ResultSet/workspace -> target -> job -> cleanup
  node lease -> ResultSet/workspace`. The target fake's DB query must observe
  durable `drained` plus the first renewed expiry, proving no transaction spans
  target I/O.
- [x] Add `TestRecoveryCleanupTargetValidationRejectsDriftAndLostFence` with
  these stable cases: tampered local claim, cross-job/resource substitution,
  takeover before observation, takeover after observation and final-CAS lost
  race. A local mismatch makes zero target calls. After takeover, the old owner
  changes no phase/failure field and does not release or alter the fresh
  owner's node lease. Every arm keeps `RemoveOwnedJobDir` at zero.
- [x] Run before lifecycle production edits:

  ```bash
  cd backend
  go test ./internal/backupasset/recovery \
    -run '^(TestRecoveryCleanupTargetValidationRenewsAndAdvancesPublishedAndWorkspace|TestRecoveryCleanupTargetValidationRejectsDriftAndLostFence)$' \
    -count=1
  ```

  Expected RED: missing `Target` dependency and missing published/workspace
  validation methods. An unrelated constructor or fixture failure is not RED
  evidence.

### Task 7-G6: Implement renewal, observation and durable validated CAS

**Files:**

- Modify: `backend/internal/backupasset/recovery/result_lifecycle.go`

- [x] Add `Target TargetPort` to `ResultLifecycleDependencies`, retain it on
  `ResultLifecycleService`, and reject a nil target in the constructor. There is
  no runtime constructor to update in this batch; the only current constructor
  is the focused test fixture.
- [x] Add the sanitized sentinel and public lifecycle methods:

  ```go
  var ErrRecoveryResultCleanupValidationFailed =
      errors.New("recovery result cleanup target validation failed")

  func (service *ResultLifecycleService) ValidateRecoveryResultCleanup(
      ctx context.Context,
      claim RecoveryResultCleanupClaim,
  ) (RecoveryResultCleanupClaim, error)

  func (service *ResultLifecycleService) ValidateRecoveryWorkspaceCleanup(
      ctx context.Context,
      claim RecoveryWorkspaceCleanupClaim,
  ) (RecoveryWorkspaceCleanupClaim, error)
  ```

- [x] In each first transaction lock the job, exact active cleanup node lease,
  then ResultSet for published cleanup; the workspace tuple remains on the
  already locked job. Require current `drained` claim equality, isolated
  terminal job and published/no-ResultSet parity. Load the immutable plan and
  permanent latch and require plan/job node/root equality, nonempty encrypted
  root locator, valid root revision, exact workspace binding, recomputed
  workspace path digest and exact marker binding. Renew the node and resource
  expiry to one equal later value, then issue:

  ```go
  TargetCleanupPermit{
      SchemaVersion: 1,
      Purpose: TargetPurposeCleanup,
      Operation: TargetCleanupValidateOwnedJobDir,
      ResourceKind: CleanupResourceResultSet, // or CleanupResourceWorkspace
      ResourceID: resultSet.ID,                // or job.ID
      JobID: job.ID,
      CleanupOwner: claim.WorkerID,
      CleanupFence: claim.CleanupFence,
      CleanupAttempt: claim.CleanupAttempt,
      NodeID: job.TargetNodeID,
      NodeLeaseID: claim.NodeLeaseID,
      NodeFence: claim.NodeFence,
      RootID: job.TargetRootID,
      RootLocatorDigest: job.RootLocatorDigest,
      TargetPathDigest: workspacePathDigest,
      RootRevision: plan.RootRevision,
      MarkerBindingDigest: job.WorkspaceMarkerBindingDigest,
      UseLatchID: RecoverySchemaUseLatchID,
      ExpiresAt: renewedExpiry,
  }
  ```

- [x] Commit the first transaction, derive a target-call deadline at the
  midpoint between current time and renewed expiry, and call
  `ValidateOwnedJobDir` exactly once. The midpoint must be after now and before
  expiry; the caller's earlier deadline still wins. Do not call the target when
  first-transaction authority validation fails.
- [x] In the closing transaction repeat `job -> node lease -> resource`, reload
  plan and latch, validate the complete renewed claim and permit proof, and
  require returned object/marker/root revision equality plus one bounded
  nonempty target revision. Renew both expiries again and CAS only the same
  owner/fences/attempt from `drained` to `validated`. Keep state `revoking`, the
  owner and node lease active. Return conflict without phase/failure/release
  mutation when ownership or either fence changed.
- [x] Factor the node-lease row lock so renewal and failure projection use one
  exact predicate: job/node, `holder_kind=recovery_cleanup`, null attempt,
  owner/fence, active state, nil `released_at`, equal expiry and expiry after
  now. Do not add a DB callback to the permit proof and do not issue a remove
  operation permit.
- [x] Re-run R6, R5 and the existing revoke/drain selectors unchanged; require
  GREEN before adding failure projection:

  ```bash
  cd backend
  go test ./internal/backupasset/recovery \
    -run '^(TestRecoveryCleanupTargetValidation|TestRecoveryResultCleanupRevokeAndDrainRenewLeasesAndAdvanceDurablePhases|TestRecoveryResultCleanupLostFenceAfterExternalDrainDoesNotAdvancePhase|TestRecoveryWorkspaceCleanupRevokeAndDrainRenewsWithoutContent|TestTargetCleanupPermit|TestTargetPort)' \
    -count=1
  ```

### Task 7-R7: Freeze retryable failure projection and paired retry guard RED

**Files:**

- Modify: `backend/internal/backupasset/recovery/result_lifecycle_test.go`
- Modify: `backend/internal/database/backup_asset_migrations_integration_test.go`

- [x] Add
  `TestRecoveryCleanupTargetValidationFailureReleasesLeaseAndResumesDrained`.
  Run both published and unpublished arms across target error, missing
  directory, symlink/special observation, marker mismatch, root-revision drift,
  partial/empty target revision, timeout, cancellation, transport ambiguity,
  missing latch before issuance and latch loss before closing CAS. Require only
  `ErrRecoveryResultCleanupValidationFailed`, never raw target text.
- [x] For every still-current failed arm require one atomic projection:

  ```text
  published:   state=cleanup_failed, cleanup_phase=drained
  unpublished: workspace_phase=cleanup_due, workspace_cleanup_phase=drained
  both:        owner='', lease_expiry=NULL, node_lease_id=NULL, node_fence=0
  both:        cleanup_fence and cleanup_attempt retained
  old node:    state=released with released_at set
  target:      RemoveOwnedJobDir calls=0
  ```

  Cancellation must still use one bounded `context.WithoutCancel` DB cleanup
  context. If a failure-projection CAS is forced to zero rows, return the DB/
  conflict error, leave the active lease to expiry/reconciliation, and do not
  claim a durable failure product.
- [x] In the same selector claim the projected row with a fresh worker. Require
  both cleanup fence/attempt and node fence to increase, phase to remain
  `drained`, and a subsequent successful validation to reach `validated`
  without another Content revoke, cancel or drain call.
- [x] Extend the shared paired migration test's ownerless retry segment. Advance
  the first owner to `drained`, release/clear it, reject fresh-owner retries
  that request `claimed`, `revoked`, `validated`, `delete_started` or `deleted`,
  and accept only a new owner/fence/attempt/node lease that preserves
  `drained`. Keep the neutral zero-history initial claim at `claimed` and the
  expired active-owner takeover behavior unchanged.
- [x] Run both RED selectors before migration/lifecycle production edits:

  ```bash
  cd backend
  go test ./internal/backupasset/recovery \
    -run '^TestRecoveryCleanupTargetValidationFailureReleasesLeaseAndResumesDrained$' \
    -count=1
  go test ./internal/database \
    -run '^TestBackupAssetMigration069WorkspaceCleanupOwnershipSQLite$' \
    -count=1
  ```

  Observed RED: lifecycle rows retained active authority and a fresh retry could
  not resume the durable phase. The SQLite guard accepted a prohibited
  positive-history rewind to `claimed`; the selector stopped at that first
  invalid arm before the valid `drained` re-claim. No fixture failure was
  counted as product RED.

### Task 7-G7: Implement closed failure projection and exact drained retry

**Files:**

- Modify: `backend/internal/backupasset/recovery/result_lifecycle.go`
- Modify:
  `backend/internal/database/migrations/sqlite/000069_backup_asset_recovery.up.sql`
- Modify:
  `backend/internal/database/migrations/postgres/000069_backup_asset_recovery.up.sql`

- [x] Add separate published/workspace failure helpers. Under one bounded
  detached context created with `context.WithTimeout` and
  `context.WithoutCancel(nonNilRecoveryContext(ctx))`, using the exact private
  `recoveryCleanupFailureProjectionTimeout = 5 * time.Second` constant,
  lock `job -> exact cleanup node lease -> resource`, prove the still-current
  `drained` tuple, release that node lease, then project the exact ownerless
  state above in the same transaction. Target errors and invalid observations
  collapse to the sanitized sentinel; never wrap or store raw locators, marker
  bytes, SSH output or transport text. Lost ownership/fence changes nothing.
- [x] Change `ClaimCleanup` so `cleanup_failed` with positive historical fence/
  attempt preserves its existing non-tombstoned phase; only a zero-history
  `ready` ResultSet starts at `claimed`. Change `ClaimWorkspaceCleanup` so an
  ownerless positive-history retry also preserves its phase; neutral zero
  history still starts at `claimed`; expired active takeover remains unchanged.
- [x] Narrow the paired workspace cleanup guard's ownerless-claim arm to this
  exact disjunction:

  ```text
  old fence=0 and attempt=0 and old/new phase=claimed
  OR
  old fence>0 and attempt>0 and new phase=old phase and phase is non-tombstoned
  ```

  Keep fresh owner, later expiry, `fence+1`, `attempt+1`, active exact
  `recovery_cleanup` node lease and positive node fence requirements. Do not
  weaken active takeover, phase progression, release or tombstone arms, and do
  not add a column, table, trigger name, migration number or down-script edit.
- [x] Re-run R7 and all claim/revoke/drain/validation selectors. Then require
  paired PostgreSQL GREEN with a usable DSN and no skip:

  ```bash
  cd backend
  REQUIRE_POSTGRES_MIGRATION_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" \
    go test ./internal/database \
    -run '^TestBackupAssetMigration069WorkspaceCleanupOwnershipPostgres$' \
    -count=1
  ```

### Task 7-V3: Focused verification and durable validated stop point

- [x] Run the complete focused matrix normally:

  ```bash
  cd backend
  go test ./internal/backupasset/recovery \
    -run '^(TestTargetCleanupPermitBindsExactValidationAuthority|TestTargetPortValidateOwnedJobDirUsesClosedCleanupObservationBoundary|TestTargetMutationPermitIsWriteOnly|TestRecoveryCleanupTargetValidationRenewsAndAdvancesPublishedAndWorkspace|TestRecoveryCleanupTargetValidationRejectsDriftAndLostFence|TestRecoveryCleanupTargetValidationFailureReleasesLeaseAndResumesDrained|TestRecoveryResultCleanupClaimRetriesFailureAndTakesOverExpiredOwner|TestRecoveryWorkspaceCleanupClaimRetriesFailureAndTakesOverExpiredOwner|TestRecoveryResultCleanupRevokeAndDrainRenewLeasesAndAdvanceDurablePhases|TestRecoveryWorkspaceCleanupRevokeAndDrainRenewsWithoutContent)$' \
    -count=1
  go test ./internal/database \
    -run '^(TestBackupAssetMigration069WorkspaceCleanupOwnershipSQLite|TestBackupAssetMigration069PairedFiles)$' \
    -count=1
  ```

- [x] Run the stateful lifecycle selectors under the race detector five times,
  then the full Recovery and Content packages:

  ```bash
  cd backend
  go test -race ./internal/backupasset/recovery \
    -run '^(TestRecoveryCleanupTargetValidationRenewsAndAdvancesPublishedAndWorkspace|TestRecoveryCleanupTargetValidationRejectsDriftAndLostFence|TestRecoveryCleanupTargetValidationFailureReleasesLeaseAndResumesDrained)$' \
    -count=5
  go test ./internal/backupasset/recovery ./internal/backupasset/content -count=1
  go test -race ./internal/backupasset/recovery -count=1
  ```

- [x] Run required real PostgreSQL parity and the affected static gates:

  ```bash
  cd backend
  REQUIRE_POSTGRES_MIGRATION_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" \
    go test ./internal/database \
    -run '^(TestBackupAssetMigration069WorkspaceCleanupOwnershipPostgres|TestBackupAssetMigration069Postgres)$' \
    -count=1
  go vet ./internal/backupasset/recovery ./internal/database
  gofmt -d internal/backupasset/recovery/target.go \
    internal/backupasset/recovery/target_test.go \
    internal/backupasset/recovery/preflight_test.go \
    internal/backupasset/recovery/result_lifecycle.go \
    internal/backupasset/recovery/result_lifecycle_test.go \
    internal/database/backup_asset_migrations_integration_test.go
  ! rg -n '[[:blank:]]+$|^(<<<<<<<|=======|>>>>>>>)' \
    internal/backupasset/recovery/target.go \
    internal/backupasset/recovery/target_test.go \
    internal/backupasset/recovery/preflight_test.go \
    internal/backupasset/recovery/result_lifecycle.go \
    internal/backupasset/recovery/result_lifecycle_test.go \
    internal/database/backup_asset_migrations_integration_test.go \
    internal/database/migrations/sqlite/000069_backup_asset_recovery.up.sql \
    internal/database/migrations/postgres/000069_backup_asset_recovery.up.sql
  cd ..
  make lint-backend
  git diff --check
  ```

  Expected: PostgreSQL does not skip, vet/lint pass, `gofmt -d` emits no diff,
  and `git diff --check` exits zero.
- [x] Re-run Child and parent Trellis validation, JSON/JSONL parsing and the
  immutable branch/protected/stage checks:

  ```bash
  python3 ./.trellis/scripts/task.py validate \
    .trellis/tasks/07-28-backup-assets-controlled-recovery
  python3 ./.trellis/scripts/task.py validate \
    .trellis/tasks/07-12-backup-data-explorer-design
  jq -e . .trellis/tasks/07-28-backup-assets-controlled-recovery/task.json \
    .trellis/tasks/07-28-backup-assets-controlled-recovery/implement.jsonl \
    .trellis/tasks/07-28-backup-assets-controlled-recovery/check.jsonl \
    .trellis/tasks/07-12-backup-data-explorer-design/task.json >/dev/null
  test "$(git branch --show-current)" = codex/backup-assets-controlled-recovery
  test "$(git rev-parse HEAD)" = 51771654a85967656fe1ca69686590b734ff9214
  test "$(git rev-parse main)" = 51771654a85967656fe1ca69686590b734ff9214
  test "$(git rev-parse origin/main)" = 51771654a85967656fe1ca69686590b734ff9214
  test -z "$(git diff --cached --name-only)"
  test "$(sha256sum go.mod | cut -d' ' -f1)" = \
    b767fc9ef9376c651d0493329b710ac8dcf8d77e52686b847d244aff9f6d48fd
  test "$(sha256sum recovery/testdata/rsync_local_to_remote.json | cut -d' ' -f1)" = \
    2570bd4472541322d902c4cdf2fe43f247b69f19d335558830e749567f763892
  git diff --quiet HEAD -- backend/internal/backupasset/content/lease.go \
    backend/internal/backupasset/content/source_contracts.go
  ```

  Re-run the existing exact scope parser and require
  `phase1=9 create=55 modify=81 total=145 unique=145 duplicates=0`, zero paths
  outside the manifest, exactly the two protected dirty paths, zero staged
  paths, zero create paths present at HEAD, zero missing modify paths at HEAD
  and zero `000070|000071` migration files. Record exact command evidence; do
  not infer any whole Task 7, frontend, Docker, hosted CI or delivery credit.
- [x] Stop at durable `validated` with Task 7 and Child 13 still `in_progress`.
  The recording target must show zero `RemoveOwnedJobDir` calls. Do not enter
  `delete_started`, delete a directory, write `deleted|tombstoned|cleaned`,
  release successful validation authority, wire a production target, implement
  orphan/quarantine scheduling, stage, commit, push or create a PR. Present the
  focused evidence and request approval for the next bounded Task 7 batch.

## 16. Task 7 Production Target-Root Registry And Plan Snapshot Plan (2026-08-03)

R1 and the bounded batch boundary were approved by the user on 2026-08-03, and
the user subsequently approved product-code execution. Design section 36 is
controlling. Task 7 and Child 13 stay
`in_progress`; this section does not reopen completed Task 7-V3 evidence and
does not authorize Task 8/9, marker, adapter or deletion work.

### 16.1 Exact file and responsibility map

- Modify `backend/internal/settings/service_test.go`: freeze strict private-row,
  canonical path, encryption, isolation and generic-settings exclusion RED.
- Modify `backend/internal/api/handlers/config_handler_test.go`: freeze omission
  from ordinary and `include_secrets=true` config export plus import rejection.
- Modify `backend/internal/settings/service.go`: implement the private typed
  root definitions, key/document codec, current-v2 encryption, exact Tx
  registration/deletion, bounded safe listing and exact Tx resolution.
- Modify `backend/internal/backupasset/recovery/service_test.go`: freeze required
  resolver, encrypted snapshot, zero-write failure, replay and rotation RED.
- Modify `backend/internal/backupasset/recovery/service.go`: require the resolver,
  resolve only new plans in the existing transaction, persist the private
  locator and validate persisted snapshots on replay.
- Update only the already-manifested Child/parent Trellis paths needed to record
  approved design, implementation evidence and focused status.

No new path or manifest amendment is required. In particular, do not touch
`bootstrap/bootstrap.go`: private root rows are new and current-v2-only, so
there is no valid v1 population to migrate. Do not touch model/migration,
settings/config handlers, runtime, target, SSH, Provider, Repository, frontend,
`go.mod` or root-level protected files.

### Task 7-R8: Freeze private target-root registry RED

**Tests first:**

- [x] Add `TestRecoveryTargetRootRegistryPersistsPrivateV2Records`. Seed two
  active nodes; register two roots on node A and the same root ID on node B
  through caller-owned transactions. Require exact resolve, root-ID-sorted safe
  list, hook-free raw values with `enc:v2:` and without recognizable locator,
  idempotent identical registration, isolated rotation, exact deletion and no
  sibling/cross-node mutation.
- [x] Add `TestRecoveryTargetRootRegistryRejectsInvalidDefinitions`. Table-drive
  missing/archived node, noncanonical node/root key forms, empty/control/
  overlong label, root `/`, relative, cleaned-but-not-equal, double/trailing
  slash, dot/dot-dot component, backslash, NUL/control and overlong locator.
  Every case must leave the private row count and prior valid rows unchanged.
- [x] Add `TestRecoveryTargetRootRegistryRejectsInvalidStoredRecords`. Starting
  from a valid row, independently inject empty/plaintext, valid legacy v1,
  corrupt v2, unsupported schema, unknown/duplicate/missing field, trailing
  document, oversized plaintext, wrong node/root, swapped ciphertext and wrong
  digest. Exact resolve must return only the safe private-state category and a
  list must fail wholly rather than return a partial allowlist.
- [x] Extend the internal-key matrix so the two existing fixed processing keys
  and every target-root prefix row are internal, while lookalike prefixes are
  not. Prove `Registry`, `GetAll`, `GetEffective`, `Validate`, `Update`,
  `UpdateMany` and `Delete` never enumerate/decrypt/accept the dynamic key.
- [x] In `config_handler_test.go`, add
  `TestConfigExportAndImportExcludeRecoveryTargetRootRegistry`. Both export
  modes must omit key, ciphertext, locator and label. Importing an injected
  private row must fail validation and preserve the registered row.
- [x] Run the exact selectors before any product edit and record genuine RED:

  ```bash
  cd backend
  go test ./internal/settings ./internal/api/handlers \
    -run '^(TestRecoveryTargetRootRegistry|TestConfigExportAndImportExcludeRecoveryTargetRootRegistry)' \
    -count=1
  ```

  Expected RED: the typed private registry and dynamic internal-key recognition
  do not exist. Compilation failures caused only by those missing products are
  valid RED; unrelated fixture/build failures are not.

### Task 7-G8: Implement the private settings sub-registry

- [x] Add the reserved v1 key prefix, bounded types and safe sentinels in
  `settings/service.go`. Keep locator/digest fields `json:"-"`; safe summaries
  contain only node ID, root ID and label. Expand `IsInternalSettingKey` by the
  exact full prefix without adding a public `SettingDef` or cache entry.
- [x] Implement one strict canonical root-ID/label/POSIX-locator validator and
  the length-framed `xirang/recovery/target-root/v1` digest. Do not trim or
  silently clean accepted input. Reject unknown/duplicate JSON fields, trailing
  documents and documents above the frozen bound.
- [x] Implement `RegisterRecoveryTargetRootTx` with a narrow active-node query,
  exact key construction, current `secure.EncryptString`, identical no-op and
  single-row rotation. Implement exact-key delete, bounded sorted safe list and
  exact Tx resolution. Every load must require current v2, decrypt, strictly
  decode, validate key/payload identity and recompute the digest. No method logs
  record material or raw underlying errors.
- [x] Keep generic setting paths unchanged except internal-key classification.
  Config export should become safe through its existing early
  `IsInternalSettingKey` check; production handler code must not change.
- [x] Re-run R8 unchanged and require GREEN. Then run full settings and focused
  config-handler regressions:

  ```bash
  cd backend
  go test ./internal/settings -count=1
  go test ./internal/api/handlers \
    -run '^(TestConfigExport|TestConfigImport|TestConfigExportAndImportExcludeRecoveryTargetRootRegistry)' \
    -count=1
  ```

### Task 7-R9: Freeze transaction-bound plan snapshot RED

**Tests first:**

- [x] Update all `NewPlanService` fixtures with an explicit resolver fake;
  changing fixtures alone is not RED evidence. Add
  `TestRecoveryPlanSnapshotsResolvedTargetRootLocator`: require one new-plan
  resolution with the service transaction, exact node/root/digest equality,
  hook-free encrypted DB storage, normal plaintext reload and no locator in the
  result/error JSON.
- [x] Add `TestRecoveryPlanTargetRootResolutionFailsClosedBeforeWrites` for nil
  resolver, missing/archived root, returned node/root mismatch, canonical
  locator failure, digest mismatch, private-state error, context cancellation,
  plan hook encryption failure and resolver-side transaction error. Require
  zero plan/item writes and no recognizable locator/ciphertext/raw error text.
- [x] Add `TestRecoveryPlanIdempotentReplayUsesFrozenTargetRootSnapshot`.
  After the first plan, rotate then delete the registry row and make the fake
  resolver fail if called. Same intent must validate/replay the old snapshot;
  a new idempotency key with old digest must fail, while a new key with the new
  digest snapshots the new locator. Corrupting the old plan snapshot must fail
  without consulting current registry.
- [x] Add `TestRecoveryPlanTargetRootRotationCannotCrossBind`. Use controlled
  transaction barriers around registry rotation and plan create. Each accepted
  outcome must be one complete old or new tuple; no plan may contain one
  locator with the other digest. Repeat the stateful selector under `-race`.
- [x] Run the exact selectors before the PlanService product edit and record
  genuine RED:

  ```bash
  cd backend
  go test ./internal/backupasset/recovery \
    -run '^(TestRecoveryPlanSnapshotsResolvedTargetRootLocator|TestRecoveryPlanTargetRootResolutionFailsClosedBeforeWrites|TestRecoveryPlanIdempotentReplayUsesFrozenTargetRootSnapshot|TestRecoveryPlanTargetRootRotationCannotCrossBind)$' \
    -count=1
  ```

  Expected RED: missing required resolver/snapshot resolution and the currently
  empty `EncryptedTargetRootLocator`. Preexisting tests that manually seed that
  field are not evidence for this production path.

### Task 7-G9: Implement exact resolution and immutable plan snapshot

- [x] Add the required narrow resolver to `PlanServiceDependencies` and retain
  it on `PlanService`; reject nil in `NewPlanService`. Preserve injection in
  tests and leave production composition to Task 8.
- [x] For a new plan only, after source/selection revalidation and before ID or
  row creation, resolve through the existing `tx`. Require returned node/root,
  canonical locator and recomputed locator digest to equal the request target.
  Preserve context cancellation; map missing/digest drift to the stable target-
  changed conflict and private-state/DB/crypto failure to plan unavailable.
- [x] Pass the private resolution separately into `recoveryPlanRow` and assign
  `EncryptedTargetRootLocator`. Do not add locator to request, response,
  `planIntentDigest`, audit, log or another model. The existing model hook owns
  final plan-field encryption.
- [x] Tighten replay validation to recompute the persisted snapshot digest and
  reject empty/noncanonical/mismatched snapshots. Replay must return before
  resolver use and must never repair from the registry.
- [x] Re-run R9 unchanged and require GREEN, then run every existing plan/
  selection/idempotency/rollback selector and the whole Recovery package:

  ```bash
  cd backend
  go test ./internal/backupasset/recovery \
    -run '^(TestRecoveryPlan|TestCreatePlan|TestPlan|TestExactSelection)' \
    -count=1
  go test ./internal/backupasset/recovery -count=1
  ```

### Task 7-V4: Focused verification and encrypted-snapshot stop point

- [x] Run the combined focused matrix normally and the stateful rotation/replay
  subset under race five times:

  ```bash
  cd backend
  go test ./internal/settings ./internal/api/handlers ./internal/backupasset/recovery \
    -run '^(TestRecoveryTargetRootRegistry|TestConfigExportAndImportExcludeRecoveryTargetRootRegistry|TestRecoveryPlanSnapshotsResolvedTargetRootLocator|TestRecoveryPlanTargetRootResolutionFailsClosedBeforeWrites|TestRecoveryPlanIdempotentReplayUsesFrozenTargetRootSnapshot|TestRecoveryPlanTargetRootRotationCannotCrossBind)' \
    -count=1
  go test -race ./internal/settings ./internal/backupasset/recovery \
    -run '^(TestRecoveryTargetRootRegistryPersistsPrivateV2Records|TestRecoveryPlanIdempotentReplayUsesFrozenTargetRootSnapshot|TestRecoveryPlanTargetRootRotationCannotCrossBind)$' \
    -count=5
  ```
- [x] Run full affected packages and existing Recovery race coverage. No
  settings or Recovery test may be skipped:

  ```bash
  cd backend
  go test ./internal/settings ./internal/api/handlers ./internal/backupasset/recovery -count=1
  go test -race ./internal/backupasset/recovery -count=1
  ```
- [x] Run static/privacy gates on exactly the product files, then full backend
  lint:

  ```bash
  cd backend
  go vet ./internal/settings ./internal/api/handlers ./internal/backupasset/recovery
  gofmt -d internal/settings/service.go internal/settings/service_test.go \
    internal/api/handlers/config_handler_test.go \
    internal/backupasset/recovery/service.go \
    internal/backupasset/recovery/service_test.go
  ! rg -n '[[:blank:]]+$|^(<<<<<<<|=======|>>>>>>>)' \
    internal/settings/service.go internal/settings/service_test.go \
    internal/api/handlers/config_handler_test.go \
    internal/backupasset/recovery/service.go \
    internal/backupasset/recovery/service_test.go
  cd ..
  make lint-backend
  git diff --check
  ```
- [x] Use recognizable `FAKE_*_FOR_TEST_ONLY` locators/labels and scan captured
  JSON, errors and logs plus the source diff. Require no raw locator in a public
  struct/JSON tag/log/audit/metric and no dynamic root key in public registry or
  bootstrap v1 allowlist. A raw DB scalar may contain only `enc:v2:` ciphertext.
- [x] Re-run Child and parent Trellis validation, JSON/JSONL parsing, exact
  manifest accounting, branch/HEAD/main/origin-main, protected hashes and
  staged-zero checks. Require the existing `9 + 55 + 81 = 145` unique/disjoint
  manifest, zero outside paths, exactly the two protected dirty paths, no new
  migration and no staging.
- [x] Record exact RED/GREEN/verification evidence, update Task 7 focused status
  only, and stop. Task 7 remains `in_progress`; do not claim Task 8/9, production
  runtime reachability, remote-root safety, marker interoperability or target
  adapter completion. `RemoveOwnedJobDir` calls remain zero and
  `delete_started|deleted|tombstoned|cleaned` remain untouched. Present the
  focused result and request approval for the separately designed marker-codec
  batch.

#### Task 7-V4a: Full-race test-fixture remediation (2026-08-04)

The first V4 full Recovery race gate produced a genuine test-harness RED in
`TestRecoveryLocatorRaceTakeoverOneWinner`: two concurrent adopters shared the
same unsynchronized `recoveryAdoptionSourceResolverFake.refs` slice and the
same unsynchronized `recoveryExecutionSourceFake` lifecycle counters. The race
detector reported only test-file accesses; no production frame owned the
contended state.

This verification remediation adds exactly two already-manifested test paths to
the section 16 batch:

```text
backend/internal/backupasset/recovery/executor_test.go
backend/internal/backupasset/recovery/worker_test.go
```

The minimal GREEN is limited to synchronizing mutable fake state. It must not
change Worker, resolver, source, target, database, marker, adapter or deletion
product behavior. Re-run the exact race selector and the complete Recovery race
gate after the edit; a lucky unreported race is not sufficient. The section 16
encrypted-snapshot stop point and the 145-path global manifest remain unchanged.

The subsequent required PostgreSQL Recovery gate exposed a second deterministic
test-only mismatch. `newAuthorizationReceiptPostgresServiceFixture` creates one
job item, unlike the default three-item SQLite fixture named by Correction 18,
so adopting that sole item must use ordinary terminalization rather than retain
an active continuation attempt. The PostgreSQL regression now freezes its
single-item cardinality and asserts zero pending items, a completed attempt and
the terminal `succeeded|sealed` job while preserving post-terminal rewrite
rejection. Production behavior remains unchanged.

## 17. Task 7 Recovery Workspace Marker Codec Plan (2026-08-04)

> **Execution mode:** bounded inline TDD in the existing branch/worktree. Do
> not use a goal, heartbeat or subagent. Do not create or switch a branch or
> worktree, and do not stage, commit, push or create a PR. The user approved
> design section 37 before this plan was persisted.

**Goal:** Implement a strict authenticated private marker document and prove
that current workspace creation and cleanup validation bind the same immutable
creator provenance, without performing remote filesystem I/O.

**Architecture:** A package-private codec owns strict JSON, CSPRNG nonce,
installation-ID derivation and HMAC authentication. Worker and lifecycle
contracts carry creator/fence facts; recording target tests call the real codec
across existing write and cleanup permits. Concrete SSH/SFTP remains separate.

### 17.1 Exact file and responsibility map

**Product:**

- Modify `backend/internal/backupasset/recovery/target.go`: marker document and
  codec, closed errors, creator-bound target requests and cleanup proof.
- Modify `backend/internal/backupasset/recovery/worker.go`: populate and
  revalidate immutable marker creator facts on `CreateOwnedJobDir`.
- Modify `backend/internal/backupasset/recovery/result_lifecycle.go`: carry the
  locked creator tuple through cleanup permit issuance and closing comparison.

**Focused tests:**

- Modify `backend/internal/backupasset/recovery/target_test.go`: codec,
  strict/tamper/privacy/error and exact authority tests.
- Modify `backend/internal/backupasset/recovery/worker_test.go`: initial and
  reserved-takeover creator provenance assertions.
- Modify `backend/internal/backupasset/recovery/result_lifecycle_test.go`:
  cleanup permit/request creator parity and drift rejection.

**Evidence after GREEN:** existing `prd.md`, `design.md`, `implement.md`,
`task.json` and `research/implementation-evidence.md` only.

No model, migration, settings, keyring/secure, runtime, API, Provider,
Repository, frontend, `go.mod`, root-level `recovery/` or new path is in scope.

### Task 7-R10: Freeze strict marker codec RED

- [x] Add `TestRecoveryWorkspaceMarkerCodecCreatesAndValidatesClosedDocument`.
  Construct a real proof-bearing write permit, creator-bound create request,
  deterministic 32-byte entropy source and exact ownership key fake. Require
  the wished-for codec to encode one bounded document and validate the same
  bytes through an independently issued cleanup permit/request. Strictly inspect
  the document fields in test code; require canonical 43-character nonce,
  correct key version and no raw locator/creator/key material.
- [x] In the same selector prove two creates consume entropy and produce
  distinct nonces, while validating an existing marker does not consume
  entropy and returns no nonce/document product.
- [x] Add `TestRecoveryWorkspaceMarkerCodecRejectsTamperAndAmbiguity` for empty,
  oversized, unknown/duplicate/missing/trailing fields; schema/key/install/job/
  root/revision/nonce/binding/tag mutations; wrong key material/version and
  cross-object/creator/fence/permit substitutions. Every marker case returns
  only `ErrInvalidRecoveryWorkspaceMarker`; permit mismatches return only
  `ErrInvalidTargetPermit`.
- [x] Add `TestRecoveryWorkspaceMarkerCodecErrorsAreSanitized` for canceled and
  deadline contexts, missing/lost key material and an entropy reader returning
  a recognizable private error. Context identity is preserved; other errors
  are exact safe sentinels and contain none of the recognizable values.
- [x] Run before any product edit:

  ```bash
  cd backend
  go test ./internal/backupasset/recovery \
    -run '^TestRecoveryWorkspaceMarkerCodec' -count=1
  ```

  Expected RED is missing codec/error/request products. Fixture or unrelated
  package failure is not valid RED.

### Task 7-G10: Implement the minimal codec

- [x] Add the exact nine-field body/wire structs, 2048-byte bound, two HMAC
  domains, 32-byte raw-url nonce validator and package-private codec with
  injected `RecoveryWorkspaceKeySource` and `io.Reader`.
- [x] Encode only after validating write permit/request, loading the active
  ownership key and recomputing exact DB marker binding from private object and
  creator provenance. Consume entropy only after all authority checks pass.
- [x] Decode one strict object with exactly-once fields, load exact `ByVersion`
  material, validate cleanup permit/request, recompute installation ID and DB
  binding, rebuild the canonical body and authenticate with `hmac.Equal`.
- [x] Return no decoded document or nonce. Collapse errors exactly as design
  37.5 requires and emit no logs.
- [x] Re-run R10 unchanged and require GREEN. Then run all target tests:

  ```bash
  cd backend
  go test ./internal/backupasset/recovery -run '^TestTarget|^TestRecoveryWorkspaceMarkerCodec' -count=1
  ```

### Task 7-R11: Freeze creator-bound production contracts RED

- [x] Extend existing worker initial/reserved-takeover selectors to require
  `CreateOwnedJobDirRequest.MarkerCreatorID/Fence` exactly equal the immutable
  job `workspace_owner/workspace_fence`, including when the current takeover
  attempt differs. `markWorkspaceMarkerCreated` must reject a substituted tuple
  without phase or validation-tuple mutation.
- [x] Extend cleanup validation success selectors to require the cleanup permit,
  request and `recoveryCleanupTargetBinding` carry the same creator tuple.
  Add creator drift before closing CAS and require sanitized validation failure,
  no `validated` advance and zero remove calls.
- [x] Extend target JSON privacy/proof tests: creator and marker fields are
  `json:"-"`; changing creator ID/fence after cleanup permit issuance
  invalidates its proof and cross-request validation.
- [x] Run the unchanged selectors and observe genuine RED before product edits:

  ```bash
  cd backend
  go test ./internal/backupasset/recovery \
    -run '^(TestRecoveryPrepareFirstWriteUsesPreallocatedWorkspaceMatrix|TestRecoveryReservedMarkerTakeoverPersistsValidationBeforeFirstItem|TestRecoveryCleanupTargetValidationRenewsAndAdvancesPublishedAndWorkspace|TestRecoveryCleanupTargetValidationRejectsDriftAndLostFence|TestTargetCleanupPermitBindsExactValidationAuthority)$' \
    -count=1
  ```

### Task 7-G11: Bind worker and cleanup lifecycle minimally

- [x] Add private creator fields to create/validation requests and cleanup
  permit/proof. Validate shape and exact request parity in `target.go`.
- [x] Populate creation fields from the locked job in `worker.go`; revalidate
  them in `markWorkspaceMarkerCreated` without changing creator provenance or
  marker-validation tuple semantics.
- [x] Add creator fields to `recoveryCleanupTargetBinding`; issue them from the
  locked job and include them in both permit/request and final binding equality.
  Do not add a DB column, query, transaction or target call.
- [x] Re-run R11 and R10 unchanged, then all existing marker/takeover/cleanup
  validation selectors. Keep `RemoveOwnedJobDir` calls at zero.

### Task 7-V5: Focused verification and codec stop point

- [x] Run focused normal and race repetition:

  ```bash
  cd backend
  go test ./internal/backupasset/recovery \
    -run '^(TestRecoveryWorkspaceMarkerCodec|TestTargetCleanupPermit|TestRecoveryPrepareFirstWrite|TestRecoveryReservedMarkerTakeover|TestRecoveryCleanupTargetValidation)' \
    -count=1
  go test -race ./internal/backupasset/recovery \
    -run '^(TestRecoveryWorkspaceMarkerCodec|TestRecoveryReservedMarkerTakeoverPersistsValidationBeforeFirstItem|TestRecoveryCleanupTargetValidation)' \
    -count=5
  ```
- [x] Run whole Recovery normally and under race, then required real PostgreSQL
  Recovery behavior normally and under race with no skip:

  ```bash
  cd backend
  go test ./internal/backupasset/recovery -count=1
  go test -race ./internal/backupasset/recovery -count=1
  REQUIRE_POSTGRES_RECOVERY_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" \
    go test ./internal/backupasset/recovery -count=1
  REQUIRE_POSTGRES_RECOVERY_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" \
    go test -race ./internal/backupasset/recovery -count=1
  ```
- [x] Run `go vet ./internal/backupasset/recovery`, owned `gofmt -d`, whitespace/
  conflict scans, `make lint-backend` and `git diff --check`.
- [x] Review the complete bounded delta against design 37 and the existing
  Task 6/7 fence/state contracts. No open Critical/Important finding may remain.
- [x] Validate Child/parent Trellis and JSON/JSONL; rerun exact manifest,
  branch/HEAD/main/origin-main, protected hash and staged-zero checks. Require
  145 unique manifest paths, only the two protected unrelated dirty paths, no
  `000070|000071` and no staged path.
- [x] Record exact RED/GREEN/gate evidence and mark only this marker codec
  boundary `focused_complete_checked`. Task 7/Child remain `in_progress`, parent
  remains `planning`, program remains 12/15. Stop before SSH/SFTP, remote marker
  filename/write, runtime composition, deletion/tombstone/orphan work and every
  Git delivery action.

## 18. Task 7 A1 Exact-Plan SFTP Workspace-Marker Plan (2026-08-04)

> **Execution mode:** bounded inline TDD in the existing branch and worktree.
> Do not use a goal, heartbeat or subagent. Do not create or switch a branch or
> worktree, and do not stage, commit, push or create a PR. The user approved
> Scheme A and design section 38 before this plan was persisted. Each RED below
> must be observed and recorded before its matching product edit.

**Goal:** Open purpose-exact SSH/SFTP sessions from the immutable executed-plan
snapshot and implement only authenticated no-overwrite workspace-marker create
and observational validation on the concrete target.

**Architecture:** A package-private session binding is built from the same
hook-decrypted, locked plan used to issue write or cleanup authority. A narrow
node resolver and real `sshutil.NodeDialer` factory own SSH/SFTP lifetime and
purpose selection. The concrete target exposes only the fixed marker protocol;
all A2 methods and destructive removal remain closed before session creation.

**Tech stack:** Go 1.26, GORM locked rows, `golang.org/x/crypto/ssh`, existing
`github.com/pkg/sftp v1.13.10`, the Section 17 marker codec, table-driven unit
tests and an in-process `net.Pipe` SFTP protocol test.

### 18.1 Exact file and responsibility map

**Product:**

- Modify `backend/internal/backupasset/recovery/target.go`: private plan-session
  binding and proof seals; exact node/SSH/SFTP factory; fixed path/mode/read
  protocol; `ValidateForCreate`; concrete target; stable closed errors.
- Modify `backend/internal/backupasset/recovery/worker.go`: build the immutable
  session binding while the exact executed plan is locked and carry it only in
  the first-write proof used for workspace marker creation.
- Modify `backend/internal/backupasset/recovery/result_lifecycle.go`: build the
  same binding in each locked cleanup transaction, carry it in the private
  cleanup binding and permit proof, and exact-compare it on close.

**Focused tests:**

- Modify `backend/internal/backupasset/recovery/target_test.go`: hook-decrypted
  binding, proof substitution, factory purpose/lifetime, concrete create/read,
  path/mode/race/fault/cancellation/privacy and closed-method tests.
- Modify `backend/internal/backupasset/recovery/worker_test.go`: first-write
  snapshot propagation and corrupt-snapshot rejection before the target call.
- Modify `backend/internal/backupasset/recovery/result_lifecycle_test.go`:
  published and unpublished cleanup session-binding propagation, closing drift
  rejection and zero-remove preservation.

**Evidence after GREEN:** update only existing `prd.md`, `design.md`,
`implement.md`, `task.json` and `research/implementation-evidence.md` as needed
to record the bounded result. No new file is created.

No model, DDL, migration, settings implementation, sshutil implementation,
runtime/main, API, Provider, Repository, frontend, `executor.go`, `go.mod` or
root-level `recovery/` path is in A1. The global manifest remains exactly
`9 + 55 + 81 = 145`. In particular, ordinary item permits derived in
`executor.go` do not acquire a usable session binding in A1: every A2 method
must reject them before resolution/session creation until A2 receives its own
approved amendment.

### Task 7-R12: Freeze exact locked-plan session binding RED

- [x] Add
  `TestRecoveryTargetSessionBindingRequiresExactHookDecryptedPlanSnapshot` in
  `target_test.go`. Persist an executed plan through its model hook, reload it
  normally under a transaction lock and require one binding with exact plan ID,
  plan binding digest, node ID, target base revision, credential-scope
  revision, root ID, plaintext locator, locator digest and root revision.
  Require a 64-lowercase-hex digest under
  `xirang/recovery/target-session-binding/v1` in the displayed Section 38 field
  order. Independently reject `enc:v2:` input, empty/noncanonical locator,
  invalid plan/root/node/revision identity, digest mismatch, locator mutation
  and copied binding/digest substitution with only `ErrInvalidTargetPermit`.
- [x] Extend target permit privacy/proof coverage so the write and cleanup
  proofs carry the private binding, changing any binding field or only its
  digest invalidates concrete session authority, and JSON/reflection output for
  permits, requests and results contains no plan binding, node/credential
  revision or root locator. Existing proof-only tests that do not call the
  concrete target remain compatible.
- [x] Add
  `TestRecoveryPrepareFirstWriteCarriesExactTargetSessionBinding` in
  `worker_test.go`. Capture the proof passed to the recording target and require
  it to come from the same locked executed plan as the mutation permit. Rotate
  and delete the current settings root after the plan exists and require the
  old locator bytes to remain unchanged; seed a separately created plan with a
  new snapshot and require only that plan to use the new bytes. A registry fake
  that panics on `ResolveRecoveryTargetRootTx`, `ListRecoveryTargetRoots` or a
  generic settings read must receive zero calls.
- [x] Add
  `TestRecoveryPrepareFirstWriteRejectsInvalidTargetSessionSnapshotBeforeRemote`
  for ciphertext left in memory, locator/node/root/revision substitution and
  recomputed settings-digest mismatch. Require the existing sanitized worker
  error category, no target call and no durable workspace/attempt advance.
- [x] Add
  `TestRecoveryCleanupValidationCarriesExactTargetSessionBinding` and
  `TestRecoveryCleanupValidationRejectsTargetSessionSnapshotDrift` in
  `result_lifecycle_test.go`. Cover both published ResultSet and unpublished
  workspace validation. Require the first transaction's binding in the cleanup
  proof, exact reconstruction in the closing transaction, registry
  rotation/deletion independence, drift failure without `validated`, and zero
  `RemoveOwnedJobDir` calls.
- [x] Run the exact new selectors before changing product code:

  ```bash
  cd backend
  go test ./internal/backupasset/recovery \
    -run '^(TestRecoveryTargetSessionBinding|TestRecoveryPrepareFirstWrite.*TargetSession|TestRecoveryCleanupValidation.*TargetSession)' \
    -count=1
  ```

  Expected RED: the private session-binding type/proof fields and constructor do
  not exist, and Worker/lifecycle proofs cannot expose an exact plan snapshot.
  An unrelated fixture, database or encryption failure is not valid RED.

### Task 7-G12: Implement immutable session authority minimally

- [x] In `target.go`, add exactly this package-private product shape, keeping
  every field outside JSON and public DTOs:

  ```go
  type recoveryTargetSessionBinding struct {
      PlanID               string
      PlanBindingDigest    string
      NodeID               uint
      NodeRevision         string
      CredentialRevision   string
      RootID               string
      RootLocator          string
      RootLocatorDigest    string
      RootRevision         string
      bindingDigest        string
  }
  ```

  Implement `newRecoveryTargetSessionBinding(model.BackupAssetRecoveryPlan)`
  without another query or decryption call. Require executed-plan identity,
  reject an `enc:v2:` locator, recompute
  `settings.RecoveryTargetRootLocatorDigest(plan.TargetNodeID,
  plan.TargetRootID, plan.EncryptedTargetRootLocator)` without trimming, and
  use existing `framedDigest` with the exact Section 38 domain/order.
- [x] Extend `targetMutationPermitProof` with the binding and a seal over its
  digest. Extend `targetCleanupPermitProof` likewise and include the private
  binding digest in the existing cleanup proof seal. Preserve existing callers
  with a variadic private binding argument, but require exactly one valid
  binding in the concrete target's pre-session validation. A zero-binding proof
  may continue to validate legacy/fake contract tests but can never open A1 or
  later concrete sessions.
- [x] In `worker.go`, derive the binding immediately after the executed plan is
  hook-loaded under `FOR UPDATE`, retain it only in the local transaction
  result, and pass it when sealing the first-write permit. Identity/digest/
  ciphertext drift returns the existing closed worker fence category before
  `CreateOwnedJobDir`; DB/hook failure retains the existing sanitized
  unavailable mapping. Do not add a settings service dependency.
- [x] In `result_lifecycle.go`, add the session binding to
  `recoveryCleanupTargetBinding`. Construct it from the already locked plan in
  `loadRecoveryCleanupTargetBindingTx`, pass it to both cleanup permit issuers,
  and include every field plus its digest in
  `sameRecoveryCleanupTargetBinding` and the published/workspace permit-match
  helpers. Do not extend a public claim or persist another field.
- [x] Re-run R12 unchanged and require GREEN. Then run the existing plan
  snapshot, first-write, marker, cleanup validation and privacy regressions:

  ```bash
  cd backend
  go test ./internal/backupasset/recovery \
    -run '^(TestRecoveryPlan(IdempotentReplayUsesFrozenTargetRootSnapshot|TargetRootRotationCannotCrossBind)|TestRecoveryPrepareFirstWrite|TestRecoveryWorkspaceMarkerCodec|TestTargetCleanupPermit|TestRecoveryCleanupTargetValidation)' \
    -count=1
  ```

  **Stop/rollback point:** if a valid old plan needs current registry state, if
  a constructor sees ciphertext after normal GORM load, or if a binding cannot
  be carried without a public/persisted field or an `executor.go` edit, revert
  only the current R12/G12 delta and amend Section 38 before continuing.

### Task 7-R13: Freeze purpose-exact session factory and closed adapter RED

- [x] Add `TestRecoverySFTPTargetFactoryUsesExactPurposeAndRevisions`. Table
  create versus validate and require resolver calls `(nodeID,
  TargetPurposeWrite)` versus `(nodeID, TargetPurposeCleanup)`, NodeDialer calls
  `sshutil.PurposeRecoveryWrite` versus
  `sshutil.PurposeRecoveryCleanup`, and safe correlation contains only job ID.
  Reject archived/wrong node, wrong node revision, wrong credential revision,
  wrong selected purpose and copied session binding before dial/SFTP open.
- [x] In the same selector inject resolver, dial and SFTP-open failure. Require
  caller context identity when applicable; otherwise require exact
  `ErrRecoveryTargetUnavailable`, no dependency text, and close order
  `sftp -> ssh` at most once. SFTP-open failure must close SSH; ordinary close
  failure after a successful operation must turn the result into unavailable.
- [x] Add `TestRecoverySFTPTargetClosedMethodsOpenNoSession`. Call `ProbeRoot`,
  `Lstat`, `CreateDirectory`, `WriteAtomic`, `Delete`, `Verify`,
  `OpenOwnedResult` and `RemoveOwnedJobDir` on the concrete target. Every call
  must return `ErrRecoveryTargetUnavailable`, resolve zero nodes, dial zero SSH
  sessions and perform zero SFTP operations.
- [x] Add `TestRecoverySFTPTargetSessionCancellationClosesAndJoins`. Cancel
  during resolver, dial and SFTP construction; require exact
  `context.Canceled` or `context.DeadlineExceeded`, no goroutine left blocked,
  and each acquired resource closed at most once.
- [x] Run before adding the concrete factory/adapter:

  ```bash
  cd backend
  go test ./internal/backupasset/recovery \
    -run '^(TestRecoverySFTPTargetFactory|TestRecoverySFTPTargetClosedMethods|TestRecoverySFTPTargetSessionCancellation)' \
    -count=1
  ```

  Expected RED: no concrete SFTP target, node-session resolver, session factory
  or stable target-unavailable error exists.

### Task 7-G13: Implement the narrow SSH/SFTP lifetime boundary

- [x] Add `ErrRecoveryTargetUnavailable` and the exact private resolver product
  from design 38.2:

  ```go
  type recoveryTargetNodeSession struct {
      Node                 model.Node
      NodeRevision         string
      CredentialRevision   string
  }

  type recoveryTargetNodeSessionResolver interface {
      ResolveRecoveryTargetNodeSession(
          context.Context, uint, TargetPurpose,
      ) (recoveryTargetNodeSession, error)
  }
  ```

  Add the one-method `recoveryTargetNodeDialer` matching
  `(*sshutil.NodeDialer).Dial`. The target, not a request, chooses write or
  cleanup purpose. Require non-archived node ID and both revisions to equal the
  session binding before dialing.
- [x] Add private SFTP client/file facades limited to `RealPath`, `Lstat`,
  `Stat`, `Mkdir`, `Chmod`, `Open`, `OpenFile`, standard `Rename`, exact-file
  `Remove`, `Read`, `Write`, `Stat`, mandatory `Sync` and `Close`. Wrap
  `*sftp.Client`/`*sftp.File`; expose no command runner, glob, recursive remove,
  directory remove, generic upload or `PosixRename`.
- [x] Implement a production factory that accepts the private resolver,
  `*sshutil.NodeDialer` and `sftp.NewClient`. An internal test constructor may
  inject the resolver, one-method dialer, SFTP opener and SSH close hook. Start
  cancellation ownership after SSH acquisition so cancellation can unblock
  SFTP construction; atomically attach the SFTP client when opened. One
  idempotent session `Close` closes SFTP then SSH, preserves the first close
  error, stops/joins its watcher on every return and lets context identity win
  over transport errors.
- [x] Add the concrete `recoverySFTPTarget` with the session factory and marker
  codec. Implement only the two A1 methods in later GREEN steps. For all other
  `TargetPort` methods, return `ErrRecoveryTargetUnavailable` directly before
  proof validation, resolver use or session creation.
- [x] Re-run R13 unchanged and require GREEN. Run target contract and sshutil
  purpose regressions:

  ```bash
  cd backend
  go test ./internal/backupasset/recovery -run '^TestTarget|^TestRecoverySFTPTarget' -count=1
  go test ./internal/sshutil \
    -run '^(TestNodeDialerAuditsPurposeExactRecoverySessions|TestNodeDialerRejectsRecoveryPurposeAuditActionMismatchBeforeNetwork)$' \
    -count=1
  ```

  **Stop/rollback point:** if cancellation cannot unblock and join SFTP open, if
  raw node/root/remote errors escape, or if any A2/A3 method opens a session,
  revert only R13/G13 and redesign the lifetime seam before marker mutation.

### Task 7-R14: Freeze no-overwrite marker creation RED

- [x] Add `TestRecoverySFTPTargetCreateOwnedJobDirWritesExactProtocol`. With an
  operation-recording SFTP fake and real marker codec, require this exact order:
  prevalidate permit/request/binding; open write-purpose session; walk every
  absolute root prefix using `Lstat` then `RealPath`; inspect `jobs` and exact
  job directory; encode before first mutation; create missing components one at
  a time; chmod and verify each created directory `0700`; exclusive-open the
  SHA-256-derived temp; chmod/verify `0600`; complete-write; `Sync`; close;
  reopen and bounded byte-compare; revalidate components; standard `Rename` to
  `.xirang-recovery-owner-v1`; authenticate/read final; repeat canonical checks;
  close session; return the exact object, marker binding and observation digest.
- [x] In that selector require the temp basename exactly
  `.xirang-recovery-owner-v1.tmp-<lowercase SHA-256(marker bytes)>`, flags
  `O_WRONLY|O_CREATE|O_EXCL`, no `Create`/`WriteFile`/truncating open, no
  `PosixRename`, directory mode `0700`, marker mode `0600` and one mandatory
  `File.Sync` before rename.
- [x] Add `TestRecoverySFTPTargetCreateOwnedJobDirIsAuthenticatedIdempotent`.
  Replay an existing exact marker and require the same observation revision,
  zero entropy and zero mutation. Reject markerless, wrong-marker, alias,
  extra-component and mismatched existing job directories without adoption or
  chmod. Simulate a lost job-directory create race: accept only a winner whose
  final marker authenticates against this exact request.
- [x] Add `TestRecoverySFTPTargetCreateOwnedJobDirRejectsPathAndModeDrift`.
  Table every root prefix, root, `jobs`, job, temp and final component across
  missing where required, canonical alias, symlink, regular-file/special
  replacement and wrong mode. Require `ErrRecoveryTargetChanged` and no
  mutation before the first fully validated mutation point.
- [x] Add `TestRecoverySFTPTargetCreateOwnedJobDirFailureMatrix`. Inject key,
  entropy, mkdir, chmod, exclusive-open, short/zero write, sync, close, reopen,
  short/oversized/mismatched read, pre-rename revalidation, rename conflict,
  ambiguous rename, final read/authentication and final revalidation failures.
  Require exact sanitized classification, final marker never overwritten,
  shared/job directory never removed and at most the exact temp exclusively
  created by this invocation best-effort removed while preserving the primary
  error.
- [x] Add `TestRecoverySFTPTargetObservationRevisionIsExact`. Recompute the
  length-framed digest under
  `xirang/recovery/sftp-owned-workspace-observation/v1` from exact Section 38.5
  field order, including canonical decimal node ID/marker byte count, fixed
  modes and marker SHA-256. Any field substitution must change the digest.
- [x] Run before implementing create or `ValidateForCreate`:

  ```bash
  cd backend
  go test ./internal/backupasset/recovery \
    -run '^(TestRecoverySFTPTargetCreateOwnedJobDir|TestRecoverySFTPTargetObservationRevision)' \
    -count=1
  ```

  Expected RED: create remains unavailable and the codec has no
  `ValidateForCreate` idempotent-read path.

### Task 7-G14: Implement only authenticated marker create

- [x] Add `ValidateForCreate(ctx, TargetWritePermit,
  CreateOwnedJobDirRequest, []byte, time.Time) error` to the existing codec.
  Reuse one strict private document/authentication core with cleanup validation,
  but validate write authority and creator provenance. Consume no entropy and
  return no decoded document, nonce or marker product.
- [x] Add private constants for the fixed final name and complete temp prefix,
  root-relative locator validation requiring exactly
  `jobs/<permit.JobID>`, POSIX absolute-prefix expansion, canonical
  `Lstat`/`RealPath` checks and exact directory/file mode checks. Use `path`,
  never `filepath`; never trim or clean accepted locator bytes. Derive the
  absolute `jobs`, job and marker paths only from the proof's exact
  `RootLocator` bytes plus the already validated relative components; never
  consult or reconstruct them from the current target-root registry.
- [x] Implement preflight of the entire snapshot root before mutation. The root
  is observed, never chmodded. Existing `jobs` must be canonical `0700`; a newly
  created `jobs` is immediately chmodded and revalidated. Encode before the
  first `Mkdir`. Exclusively create the exact job directory and accept a lost
  race only through full existing-marker authentication.
- [x] Implement the exact temp protocol with explicit complete-write handling,
  required `Sync`, close/reopen, a 2049-byte bounded exact comparison, repeated
  component checks and standard `Rename`. Track exclusive-temp ownership in a
  local boolean; before successful rename, a deferred cleanup may call only
  exact-temp `Remove` and must preserve the primary error. Never remove or repair
  `jobs`, the job directory or an existing final marker.
- [x] After rename, bounded-read and `ValidateForCreate`, verify final stat/mode/
  size, rewalk all components and derive the observation digest from the exact
  frozen fields. Close failure changes an otherwise successful result to
  `ErrRecoveryTargetUnavailable`; context cancellation retains context identity.
- [x] Re-run R14 unchanged and require GREEN, then rerun every codec and target
  proof selector:

  ```bash
  cd backend
  go test ./internal/backupasset/recovery \
    -run '^(TestRecoverySFTPTargetCreateOwnedJobDir|TestRecoverySFTPTargetObservationRevision|TestRecoveryWorkspaceMarkerCodec|TestTarget)' \
    -count=1
  ```

  **Stop/rollback point:** if the server requires overwrite rename, lacks
  `fsync@openssh.com`, exposes a noncanonical root, or cannot prove exclusive
  temp ownership, return the frozen unavailable/changed category and stop. Do
  not add a fallback, `PosixRename`, overwrite, chmod repair or directory
  cleanup.

### Task 7-R15: Freeze bounded observational validation and cancellation RED

- [x] Add `TestRecoverySFTPTargetValidateOwnedJobDirReadsWithoutMutation`.
  Require cleanup-purpose session, exact canonical root/`jobs`/job/final marker,
  directory `0700`, regular marker `0600`, declared size `1..2048`, pre-open /
  opened / post-read size-mode-modtime parity, at most 2049 bytes read, repeated
  `Lstat`/`RealPath`, strict `ValidateForCleanup`, and zero `Mkdir`, `Chmod`,
  `OpenFile`, `Rename` or `Remove`. The result contains only exact request
  object/binding, immutable root revision and observation revision.
- [x] Add `TestRecoverySFTPTargetValidateOwnedJobDirRejectsObservationDrift`.
  Table missing/alias/symlink/file/special/wrong-mode root and object components;
  zero/negative/oversized declared marker size; short/2049-byte/replaced marker;
  pre/open/post stat drift; canonical drift and every codec substitution.
  Require changed versus invalid-marker categories exactly and never successful
  absence.
- [x] Add `TestRecoverySFTPTargetCreateAndValidateReturnSameObservation` on an
  OS-backed implementation of the exact private SFTP facade whose file uses a
  real successful `os.File.Sync`. Create once, replay once and validate through
  an independently issued cleanup permit; require identical observation
  revision and exact disk names/modes/content. Separately run
  `TestRecoverySFTPTargetCreateOwnedJobDirRequiresServerFsync` through
  `net.Pipe`, `sftp.NewServer` and `sftp.NewClientPipe`: pkg/sftp's built-in
  server does not implement `fsync@openssh.com`, so require closed-unavailable,
  no final marker and exact-temp cleanup rather than a fallback or a false
  success claim.
- [x] Add `TestRecoverySFTPTargetCancellationAndErrorsAreClosed`. Cancel during
  write, sync, rename, bounded read and session close. Require original context
  identity, at-most-once resource closes, no inferred remote outcome and no
  raw host, username, credential, plan locator, remote path, fixed/temp name,
  marker bytes, nonce, SFTP status or injected dependency text in returned
  errors, JSON or captured audit/log output. The adapter itself emits zero logs.
- [x] Run before implementing observational validation:

  ```bash
  cd backend
  go test ./internal/backupasset/recovery \
    -run '^(TestRecoverySFTPTargetValidateOwnedJobDir|TestRecoverySFTPTargetCreateAndValidate|TestRecoverySFTPTargetCancellationAndErrors)' \
    -count=1
  ```

  Expected RED: concrete validation remains unavailable and cannot produce the
  shared observation revision.

### Task 7-G15: Implement only bounded marker observation

- [x] Implement `ValidateOwnedJobDir` by validating cleanup permit/request and
  its private session proof before resolving the node. Open only
  `TargetPurposeCleanup`; never call a mutation method.
- [x] Reuse the same POSIX component/mode helpers and bounded marker reader as
  create. Record immutable pre-open, opened-file and post-read
  size/mode/modtime facts, require exact parity and exact byte count, then rewalk
  every component before authentication. The bounded reader must allocate/read
  no more than 2049 bytes and reject any result above 2048.
- [x] Call `ValidateForCleanup` only after all filesystem observations pass.
  Return exact request object/binding, binding root revision and the same
  observation formula as create. Return no marker bytes, nonce, root locator,
  node/session capability or remote error.
- [x] Map invalid authority to `ErrInvalidTargetPermit`; canonical/path/type/
  mode/replacement drift to `ErrRecoveryTargetChanged`; authenticated document
  failure to `ErrInvalidRecoveryWorkspaceMarker`; key dependency failure to
  `ErrRecoveryWorkspaceMarkerUnavailable`; resolver/SSH/SFTP/sync/I/O/close or
  ambiguous transport failure to `ErrRecoveryTargetUnavailable`; and preserve
  caller cancellation/deadline identity exactly.
- [x] Re-run R15 unchanged and require GREEN. Then run the combined A1 selectors
  normally and the stateful create/validate/cancellation subset under race five
  times:

  ```bash
  cd backend
  go test ./internal/backupasset/recovery \
    -run '^(TestRecoveryTargetSessionBinding|TestRecoveryPrepareFirstWrite.*TargetSession|TestRecoveryCleanupValidation.*TargetSession|TestRecoverySFTPTarget)' \
    -count=1
  go test -race ./internal/backupasset/recovery \
    -run '^(TestRecoverySFTPTarget(CreateOwnedJobDir|ValidateOwnedJobDir|CreateAndValidate|SessionCancellation|CancellationAndErrors))' \
    -count=5
  ```

  **Stop/rollback point:** any validation mutation, unbounded read, mismatched
  observation token, context identity loss or resource/goroutine leak blocks A1
  closure. Revert only R15/G15 if needed; do not proceed into A2 or A3.

### Task 7-V6: A1 focused verification and exact stop point

- [x] Run the complete existing marker/first-write/takeover/cleanup regression
  matrix plus all A1 selectors:

  ```bash
  cd backend
  go test ./internal/backupasset/recovery \
    -run '^(TestRecoveryWorkspaceMarkerCodec|TestTargetCleanupPermit|TestRecoveryPrepareFirstWrite|TestRecoveryReservedMarkerTakeover|TestRecoveryCleanupTargetValidation|TestRecoveryTargetSessionBinding|TestRecoveryCleanupValidation.*TargetSession|TestRecoverySFTPTarget)' \
    -count=1
  go test -race ./internal/backupasset/recovery \
    -run '^(TestRecoveryPrepareFirstWrite|TestRecoveryReservedMarkerTakeoverPersistsValidationBeforeFirstItem|TestRecoveryCleanupTargetValidation|TestRecoverySFTPTarget)' \
    -count=5
  ```
- [x] Run the whole Recovery package normally and under race, then required real
  PostgreSQL Recovery behavior normally and under race with no skip:

  ```bash
  cd backend
  go test ./internal/backupasset/recovery -count=1
  go test -race ./internal/backupasset/recovery -count=1
  REQUIRE_POSTGRES_RECOVERY_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" \
    go test ./internal/backupasset/recovery -count=1
  REQUIRE_POSTGRES_RECOVERY_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" \
    go test -race ./internal/backupasset/recovery -count=1
  ```
- [x] Run exact static/privacy/scope gates:

  ```bash
  cd backend
  go vet ./internal/backupasset/recovery
  gofmt -d internal/backupasset/recovery/target.go \
    internal/backupasset/recovery/target_test.go \
    internal/backupasset/recovery/worker.go \
    internal/backupasset/recovery/worker_test.go \
    internal/backupasset/recovery/result_lifecycle.go \
    internal/backupasset/recovery/result_lifecycle_test.go
  ! rg -n '[[:blank:]]+$|^(<<<<<<<|=======|>>>>>>>)' \
    internal/backupasset/recovery/target.go \
    internal/backupasset/recovery/target_test.go \
    internal/backupasset/recovery/worker.go \
    internal/backupasset/recovery/worker_test.go \
    internal/backupasset/recovery/result_lifecycle.go \
    internal/backupasset/recovery/result_lifecycle_test.go
  ! rg -n 'ResolveRecoveryTargetRootTx|ListRecoveryTargetRoots|PosixRename|filepath\.' \
    internal/backupasset/recovery/target.go
  cd ..
  make lint-backend
  git diff --check
  ```
- [x] Prove every non-A1 concrete method opens zero sessions and
  `RemoveOwnedJobDir` is never called. Scan recognizable
  `FAKE_*_FOR_TEST_ONLY` node/credential/locator/marker/dependency values across
  JSON, errors and captured logs/audits; only NodeDialer's existing safe
  purpose/stage/outcome and IDs may appear.
- [x] Re-run Child and parent Trellis validation, JSON/JSONL parsing, exact
  145-path manifest accounting, branch/HEAD/main/origin-main, protected hashes,
  no `000070|000071`, staged-zero and whitespace checks. Do not fetch, stage or
  alter the two protected unrelated paths during this gate.
- [x] Review the complete A1 delta against every design 38.1--38.8 row. Scan the
  implementation plan with the writing-plan skill's complete placeholder
  pattern list, then reconcile every referenced function/type signature with
  the product. No placeholder or open Critical/Important issue may remain.
- [x] Record exact RED/GREEN/verification evidence and mark only A1
  `focused_complete_checked`. Task 7 and Child 13 remain `in_progress`; parent
  remains `planning`; program remains 12/15. Stop before A2 payload operations,
  A3 removal or terminal cleanup transitions, runtime/main composition,
  orphan/quarantine work and every Git delivery action. Present the focused
  result and obtain separate approval before designing or executing A2.

# Task 7 A2a Exact-Plan Regular-File Verify Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> `superpowers:executing-plans` to implement this plan task-by-task. The current
> Trellis task is `codex-inline`; implement/check subagents are prohibited.
> Preserve every unrelated dirty path and do not stage or commit until the
> Phase 3.4 whole-task gate explicitly opens.

**Goal:** Seal exact executed-plan verification authority and implement only
the concrete present regular-file `TargetPort.Verify` arm.

**Architecture:** A locked durable handoff carries A1's private immutable plan
session binding. One package-private issuer seals a purpose/job/mode-specific
verify permit; the concrete adapter opens only `recovery_verify`, revalidates
the exact canonical object, streams its bytes through SHA-256, and returns the
existing closed observation union with one stable opaque target token. Every
other A2/A3 concrete method and absent Verify remain closed before session open.

**Tech Stack:** Go 1.26, GORM locked aggregate loads, `golang.org/x/crypto/ssh`,
`github.com/pkg/sftp`, standard-library SHA-256/base64/path/io, Go testing and
race detector.

---

## A2a file map and immutable scope

- Modify `backend/internal/backupasset/recovery/target.go`: sealed verify proof,
  verify-purpose session admission, canonical bounded regular-file read and
  opaque observation revision.
- Modify `backend/internal/backupasset/recovery/target_test.go`: direct proof,
  session, path/content, cancellation/privacy and closed-method tests.
- Modify `backend/internal/backupasset/recovery/worker.go`: construct and retain
  the exact session binding inside the existing locked durable handoff load;
  issue adoption Verify through one helper.
- Modify `backend/internal/backupasset/recovery/worker_test.go`: locked handoff,
  adoption issuance and substitution denials.
- Modify `backend/internal/backupasset/recovery/executor.go`: replace three
  structural observation-permit constructions with the shared sealed issuer.
- Modify `backend/internal/backupasset/recovery/executor_test.go`: ordinary
  post-operation and both delete-oriented issuance regressions.
- Append evidence only to
  `.trellis/tasks/07-28-backup-assets-controlled-recovery/research/implementation-evidence.md`
  after the corresponding command has actually run.
- Do not modify `contracts.go`, models, migrations, settings, sshutil, Provider,
  Repository, runtime, main, API, frontend, `go.mod`, either protected path or
  any path outside the existing 145-path manifest.

### Task 7-R16/G16: Seal `TargetVerifyPermit`

**Files:**

- Modify: `backend/internal/backupasset/recovery/target_test.go`
- Modify after RED: `backend/internal/backupasset/recovery/target.go`

- [ ] **Step 1: Add the direct failing proof test**

  Add `TestTargetVerifyPermitRequiresExactPrivateSessionProof`. Its accepted
  fixture uses an executed session binding, exact object, 32-byte job ID and
  isolated mode; the raw structural permit must fail while the issued permit
  succeeds:

  ```go
  now := time.Now().UTC().Truncate(time.Second)
  binding := recoveryTargetSessionBindingForTest(t)
  jobID := strings.Repeat("1", 32)
  object := TargetObjectRef{
      RootID: binding.RootID, RootLocatorDigest: binding.RootLocatorDigest,
      PrivateRelativeLocator: recoveryWorkspaceLocatorDirectory + "/" + jobID + "/item.bin",
  }
  object.TargetPathDigest = mustTargetPathDigest(
      t, object.RootID, object.RootLocatorDigest, object.PrivateRelativeLocator,
  )
  raw := TargetObservationPermit{
      SchemaVersion: 1, NodeID: binding.NodeID, Purpose: TargetPurposeVerify,
      RootID: object.RootID, RootLocatorDigest: object.RootLocatorDigest,
      TargetPathDigest: object.TargetPathDigest, RootRevision: binding.RootRevision,
      ExpiresAt: now.Add(time.Minute),
  }
  if _, err := NewTargetVerifyPermit(raw, now); !errors.Is(err, ErrInvalidTargetPermit) {
      t.Fatalf("structural verify permit error = %v, want ErrInvalidTargetPermit", err)
  }
  sealed := issueTargetVerifyPermit(raw, binding, jobID, TargetModeIsolated)
  permit, err := NewTargetVerifyPermit(sealed, now)
  if err != nil || permit.ValidateObjectAt(now, object) != nil {
      t.Fatalf("exact sealed verify authority rejected: permit=%+v error=%v", permit, err)
  }
  ```

  Deep-copy the proof, mutate each public field plus private `jobID`,
  `targetMode`, locator, credential revision and proof digest one at a time, and
  require `ErrInvalidTargetPermit`. Marshal raw/sealed/wrapped permits and assert
  that plan ID/binding, node/credential revisions, root locator and private proof
  digest never appear.

- [ ] **Step 2: Run R16 and capture genuine RED**

  Run:

  ```bash
  cd backend
  go test ./internal/backupasset/recovery \
    -run '^TestTargetVerifyPermitRequiresExactPrivateSessionProof$' -count=1
  ```

  Expected: FAIL to compile because `issueTargetVerifyPermit` and the private
  proof do not exist, or FAIL because structural Verify authority is accepted.
  Record the exact command, failure identity and timestamp before product edits.

- [ ] **Step 3: Implement the minimal sealed proof**

  In `target.go`, add the exact domain and private shape:

  ```go
  const targetVerifyPermitProofDomain = "xirang/recovery/target-verify-permit-proof/v1"

  type targetVerifyPermitProof struct {
      sessionBinding recoveryTargetSessionBinding
      jobID           string
      targetMode      TargetMode
      bindingDigest   string
  }
  ```

  Add `proof *targetVerifyPermitProof` to `TargetObservationPermit`. Implement
  `targetVerifyPermitProofDigest` with `framedDigest` over, in order, schema,
  decimal node, purpose, root ID, root locator digest, target path digest, root
  revision, `ExpiresAt.UTC().Format(time.RFC3339Nano)`, job ID, target mode and
  session-binding digest. Implement this exact issuer:

  ```go
  func issueTargetVerifyPermit(
      permit TargetObservationPermit,
      binding recoveryTargetSessionBinding,
      jobID string,
      mode TargetMode,
  ) TargetObservationPermit {
      permit.proof = nil
      if permit.SchemaVersion != 1 || permit.NodeID == 0 ||
          permit.Purpose != TargetPurposeVerify ||
          !validBoundedOpaque(permit.RootID, targetRootIDMax) ||
          !validDigest(permit.RootLocatorDigest) ||
          !validDigest(permit.TargetPathDigest) ||
          !validOpaqueRevision(permit.RootRevision) || permit.ExpiresAt.IsZero() ||
          !binding.valid() || !validOpaqueID(jobID) || mode.Validate() != nil ||
          binding.NodeID != permit.NodeID || binding.RootID != permit.RootID ||
          binding.RootLocatorDigest != permit.RootLocatorDigest ||
          binding.RootRevision != permit.RootRevision {
          return permit
      }
      permit.proof = &targetVerifyPermitProof{
          sessionBinding: binding, jobID: jobID, targetMode: mode,
      }
      permit.proof.bindingDigest = targetVerifyPermitProofDigest(permit, permit.proof)
      return permit
  }
  ```

  This issuer is deliberately shape-only. Expiry is enforced only by
  `NewTargetVerifyPermit`/`ValidateAt` with the caller's exact `now`, preventing
  clock disagreement in a pure sealer.

  Add `validateVerifyPurposeAt(now)` which first runs existing shape/purpose
  validation, then requires a valid proof, exact proof digest and exact
  session/permit parity. Route only `NewTargetVerifyPermit`,
  `TargetVerifyPermit.ValidateAt` and `ValidateObjectAt` through it. Leave
  preflight/result-read structural constructors unchanged in A2a.

- [ ] **Step 4: Run R16 to GREEN and regress permit contracts**

  Run:

  ```bash
  cd backend
  go test ./internal/backupasset/recovery \
    -run '^(TestTargetVerifyPermitRequiresExactPrivateSessionProof|TestTargetPortOperationPermitsArePurposeExact|TestTargetPurposeSpecificPermitConstructionRejectsCrossPurpose|TestTargetPermitsRequireExactObjectAndFrozenJobBinding|TestTargetPortVerifyUsesClosedExpectationObservationBoundary)$' \
    -count=1
  ```

  Expected: PASS. Any existing direct Verify-constructor fixture must call the
  private test issuer with an exact test binding; preflight/result-read tests
  must not gain a fake executed-plan proof.

  **Stop/rollback point:** structural Verify remains accepted, another purpose
  starts requiring an executed-plan proof, or JSON exposes any private field.

### Task 7-R17/G17: Bind every durable issuance path

**Files:**

- Modify: `backend/internal/backupasset/recovery/worker_test.go`
- Modify: `backend/internal/backupasset/recovery/executor_test.go`
- Modify after RED: `backend/internal/backupasset/recovery/worker.go`
- Modify after RED: `backend/internal/backupasset/recovery/executor.go`

- [ ] **Step 1: Add failing locked-handoff and call-site tests**

  Extend the existing ordinary execution/adoption fakes so `Verify` and `Lstat`
  call `permit.ValidateObjectAt(fake.now, object)` and capture the private proof
  tuple. Add selectors:

  ```go
  TestRecoveryInterruptedOperationHandoffCarriesLockedTargetSessionBinding
  TestRecoveryOrdinaryVerifyIssuanceUsesExactLockedTargetSessionBinding
  TestRecoveryDeleteObservationIssuanceUsesExactLockedTargetSessionBinding
  TestRecoveryAdoptionVerifyIssuanceUsesExactLockedTargetSessionBinding
  ```

  The first selector loads the existing durable handoff and compares its private
  binding with `newRecoveryTargetSessionBinding(handoff.plan)`. The other three
  require exact job ID/mode/node/root/revisions/object and mutate one copied
  handoff binding before target I/O to prove zero fake calls and fence loss.

- [ ] **Step 2: Run R17 and capture genuine RED**

  Run:

  ```bash
  cd backend
  go test ./internal/backupasset/recovery \
    -run '^(TestRecoveryInterruptedOperationHandoffCarriesLockedTargetSessionBinding|TestRecoveryOrdinaryVerifyIssuanceUsesExactLockedTargetSessionBinding|TestRecoveryDeleteObservationIssuanceUsesExactLockedTargetSessionBinding|TestRecoveryAdoptionVerifyIssuanceUsesExactLockedTargetSessionBinding)$' \
    -count=1
  ```

  Expected: FAIL because the handoff has no private binding and current call
  sites construct structural permits rejected by G16. Record RED before edits.

- [ ] **Step 3: Add the binding to the locked handoff**

  In `worker.go`, add:

  ```go
  type interruptedOperationHandoff struct {
      plan                     model.BackupAssetRecoveryPlan
      preflight                model.BackupAssetRecoveryPreflight
      job                      model.BackupAssetRecoveryJob
      item                     model.BackupAssetRecoveryJobItem
      operation                RecoveryOperation
      checkpointOperations     []ordinaryCheckpointOperation
      object                   TargetObjectRef
      expectation              TargetVerifyExpectation
      targetSessionBinding     recoveryTargetSessionBinding
      operationDigest          string
      durableDigest            string
      deleteAuthorityConsumed  bool
      reconcileConsumedDelete  bool
  }
  ```

  Inside `loadInterruptedOperationHandoffTx`, after the complete locked plan/job/
  item/object/expectation validation and before return, call
  `newRecoveryTargetSessionBinding(plan)`. Reject any failure as
  `ErrRecoveryWorkerFenceLost`, then pass `binding.bindingDigest` immediately
  before `object` in the sole `interruptedOperationDurableDigest` call. Add the
  matching `targetSessionBindingDigest string` parameter at the same position
  in that function and append it immediately before the object fields in the
  framed digest. Return `targetSessionBinding: binding` on the handoff. Do not
  persist it or return it from a public API:

  ```go
  binding, err := newRecoveryTargetSessionBinding(plan)
  if err != nil {
      return interruptedOperationHandoff{}, ErrRecoveryWorkerFenceLost
  }
  durableDigest := interruptedOperationDurableDigest(
      claim, plan, preflight, grant, job, selected, source, node, attempt,
      itemSetDigest, checkpointDigest, operationDigest,
      binding.bindingDigest, object, expectation,
  )
  ```

- [ ] **Step 4: Add one exact issuer helper and replace four call sites**

  Add this package-private helper in `worker.go`:

  ```go
  func newRecoveryTargetVerifyPermit(
      handoff interruptedOperationHandoff,
      expiresAt time.Time,
      now time.Time,
  ) (TargetVerifyPermit, error) {
      binding := handoff.targetSessionBinding
      mode := TargetMode(handoff.job.TargetMode)
      if !binding.valid() || mode.Validate() != nil ||
          binding.PlanID != handoff.plan.ID ||
          binding.PlanBindingDigest != handoff.plan.BindingDigest ||
          handoff.job.PlanID != handoff.plan.ID ||
          handoff.job.PlanBindingDigest != handoff.plan.BindingDigest ||
          binding.NodeID != handoff.plan.TargetNodeID ||
          binding.NodeID != handoff.job.TargetNodeID ||
          binding.NodeRevision != handoff.plan.TargetBaseRevision ||
          binding.CredentialRevision != handoff.plan.CredentialScopeRevision ||
          binding.RootID != handoff.plan.TargetRootID ||
          binding.RootID != handoff.job.TargetRootID ||
          binding.RootID != handoff.object.RootID ||
          binding.RootLocator != handoff.plan.EncryptedTargetRootLocator ||
          binding.RootLocatorDigest != handoff.plan.RootLocatorDigest ||
          binding.RootLocatorDigest != handoff.job.RootLocatorDigest ||
          binding.RootLocatorDigest != handoff.object.RootLocatorDigest ||
          binding.RootRevision != handoff.plan.RootRevision ||
          handoff.plan.TargetMode != handoff.job.TargetMode ||
          handoff.object.TargetPathDigest != handoff.item.TargetObjectDigest {
          return TargetVerifyPermit{}, ErrRecoveryWorkerFenceLost
      }
      raw := TargetObservationPermit{
          SchemaVersion: 1, NodeID: binding.NodeID, Purpose: TargetPurposeVerify,
          RootID: handoff.object.RootID,
          RootLocatorDigest: handoff.object.RootLocatorDigest,
          TargetPathDigest: handoff.object.TargetPathDigest,
          RootRevision: binding.RootRevision, ExpiresAt: expiresAt,
      }
      return NewTargetVerifyPermit(
          issueTargetVerifyPermit(raw, binding, handoff.job.ID, mode), now,
      )
  }
  ```

  Replace the structural constructors at ordinary post-operation Verify,
  `pauseOrdinaryDeleteAuthority`, `observeOrdinaryDeleteTarget`, and
  `AdoptInterruptedOperation`. Preserve each existing exact expiry source and
  public error mapping. No target I/O moves inside a transaction.

- [ ] **Step 5: Run R17 to GREEN and focused worker regression**

  Run:

  ```bash
  cd backend
  go test ./internal/backupasset/recovery \
    -run '^(TestRecoveryInterruptedOperationHandoffCarriesLockedTargetSessionBinding|TestRecoveryOrdinaryVerifyIssuanceUsesExactLockedTargetSessionBinding|TestRecoveryDeleteObservationIssuanceUsesExactLockedTargetSessionBinding|TestRecoveryAdoptionVerifyIssuanceUsesExactLockedTargetSessionBinding|TestRecoveryExecuteClaim|TestRecoveryAdoptInterruptedOperation)' \
    -count=1
  ```

  Expected: PASS with unchanged durable rows/checkpoints and no transaction held
  by any fake target call.

  **Stop/rollback point:** a locator is loaded from the registry, a public type
  gains the binding, a durable digest changes without exact reload parity, or a
  target call occurs inside the load transaction.

### Task 7-R18/G18: Open only purpose-exact verify sessions

**Files:**

- Modify: `backend/internal/backupasset/recovery/target_test.go`
- Modify after RED: `backend/internal/backupasset/recovery/target.go`

- [ ] **Step 1: Add the failing verify-purpose factory matrix**

  Add `TestRecoveryTargetSessionFactoryOpensPurposeExactVerify`. Construct an
  exact binding and assert:

  ```go
  session, err := factory.Open(ctx, binding, TargetPurposeVerify, jobID)
  if err != nil {
      t.Fatalf("open exact verify session: %v", err)
  }
  if resolver.purpose != TargetPurposeVerify ||
      dialer.purpose != string(TargetPurposeVerify) ||
      dialer.audit.CorrelationID != jobID {
      t.Fatalf("verify session purpose/audit mismatch: resolver=%q dial=%q audit=%+v",
          resolver.purpose, dialer.purpose, dialer.audit)
  }
  if err := session.Close(); err != nil {
      t.Fatalf("close verify session: %v", err)
  }
  ```

  Reuse the existing substitution/failure matrix for wrong node revision,
  credential revision, resolver error, dial error, SFTP-open error and close
  ordering; require sanitized errors and no recognizable fake secret.

- [ ] **Step 2: Run R18 and capture genuine RED**

  Run:

  ```bash
  cd backend
  go test ./internal/backupasset/recovery \
    -run '^TestRecoveryTargetSessionFactoryOpensPurposeExactVerify$' -count=1
  ```

  Expected: FAIL with `ErrInvalidTargetPermit` because the factory allowlist
  currently admits only write and cleanup.

- [ ] **Step 3: Expand the factory allowlist by one literal purpose**

  Change only the factory admission expression:

  ```go
  (purpose != TargetPurposeWrite &&
      purpose != TargetPurposeVerify &&
      purpose != TargetPurposeCleanup)
  ```

  Keep exact node/credential comparison, dial audit, cancellation watcher,
  SFTP/SSH close order and all error mapping unchanged.

- [ ] **Step 4: Run R18 to GREEN and all A1 session regressions**

  Run:

  ```bash
  cd backend
  go test ./internal/backupasset/recovery \
    -run '^(TestRecoveryTargetSessionFactoryOpensPurposeExactVerify|TestRecoverySFTPTargetFactory|TestRecoverySFTPTargetSessionCancellationClosesAndJoins|TestRecoverySFTPTargetCreateOwnedJobDir|TestRecoverySFTPTargetValidateOwnedJobDir)' \
    -count=1
  ```

  Expected: PASS; no preflight/result-read purpose is admitted.

### Task 7-R19/G19: Implement exact present regular-file Verify

**Files:**

- Modify: `backend/internal/backupasset/recovery/target_test.go`
- Modify after RED: `backend/internal/backupasset/recovery/target.go`

- [ ] **Step 1: Add the failing success, namespace and revision tests**

  Add `recoveryVerifyAuthorityForTest` to derive an exact object/permit from a
  binding, job ID, mode, locator, digest and bytes. Add:

  ```go
  TestRecoverySFTPTargetVerifyPresentRegularFile
  TestRecoverySFTPTargetVerifyNamespaceAndObservationRevision
  ```

  For isolated success, create the A1 workspace, write `item.bin` beneath it,
  then require:

  ```go
  wantDigest := sha256.Sum256(payload)
  expectation := TargetVerifyExpectation{
      Kind: TargetPresencePresent,
      Present: &PresentExpectation{
          IdentityDigest: hex.EncodeToString(wantDigest[:]), Bytes: int64(len(payload)),
      },
  }
  got, err := target.Verify(context.Background(), permit, object, expectation)
  if err != nil || got.ValidateAgainst(expectation) != nil {
      t.Fatalf("verify exact regular file: observation=%+v error=%v", got, err)
  }
  if !strings.HasPrefix(got.ObservedRevision, "sftp1:") ||
      len(got.ObservedRevision) != 49 || sha256Shaped(got.ObservedRevision) {
      t.Fatalf("observation revision = %q, want bounded opaque sftp1 token", got.ObservedRevision)
  }
  ```

  Repeat unchanged content for token stability; vary root, path, content and
  bytes for separation. Cover zero bytes and in-place exact object success.
  Assert isolated top-level marker/temp collisions and wrong `jobs`/job `0700`
  modes fail, while a deeper ordinary filename equal to the marker literal is
  not rejected solely by namespace parsing.

- [ ] **Step 2: Run R19 and capture genuine RED**

  Run:

  ```bash
  cd backend
  go test ./internal/backupasset/recovery \
    -run '^(TestRecoverySFTPTargetVerifyPresentRegularFile|TestRecoverySFTPTargetVerifyNamespaceAndObservationRevision)$' \
    -count=1
  ```

  Expected: FAIL because concrete Verify returns unavailable. Record RED before
  product edits.

- [ ] **Step 3: Implement pre-session authority and namespace validation**

  Replace concrete Verify's closed body with the same context/dependency/open/
  close skeleton as A1. Add `recoveryTargetVerifySessionAuthority` which requires
  valid sealed permit/object/expectation, exact session-to-permit parity and a
  present arm. It returns binding/job/mode privately. A valid absent arm returns
  `ErrRecoveryTargetUnavailable` before calling the factory.

  Add `validateRecoveryVerifyNamespace`. Isolated mode requires exact
  `jobs/<jobID>/<first-item-component>` with optional descendant components and
  rejects only a first item component equal to the marker name or beginning
  with its temp prefix. After the session opens, isolated validation requires
  `jobs` plus the exact job directory at `0700`. In-place mode uses the exact
  mode-bound handoff object without interpreting a literal `jobs/` prefix.
  Both paths use POSIX `path`, never `filepath`.

- [ ] **Step 4: Factor canonical regular-file observation without changing A1**

  Extract A1's final-file type/`RealPath` check into:

  ```go
  func observeRecoveryCanonicalRegularFile(
      client recoveryTargetSFTPClient,
      value string,
  ) (os.FileInfo, error)
  ```

  Keep `validateRecoveryCanonicalRegularFile(client, value, mode)` as a wrapper
  that additionally compares permissions, so every marker test remains
  byte-for-byte equivalent. Rename `recoveryMarkerFileSnapshot` to
  `recoverySFTPFileSnapshot` and `recoveryMarkerSnapshot` to
  `recoverySFTPFileSnapshotOf`; update `readRecoveryMarkerFile` to use those
  exact names and reuse them for payload reads.

- [ ] **Step 5: Implement the minimal successful bounded read**

  Add `readRecoveryPresentRegularFile` with the exact signature below. R19
  establishes the bounded success path and maps every non-successful read/stat
  outcome to unavailable. R20's already-planned adversarial RED then refines
  the target-changed versus unavailable matrix without retroactive test credit:

  ```go
  func readRecoveryPresentRegularFile(
      client recoveryTargetSFTPClient,
      finalPath string,
      expectation PresentExpectation,
  ) (string, int64, recoverySFTPFileSnapshot, error) {
      before, err := observeRecoveryCanonicalRegularFile(client, finalPath)
      if err != nil || before.Size() != expectation.Bytes {
          return "", 0, recoverySFTPFileSnapshot{}, ErrRecoveryTargetUnavailable
      }
      file, err := client.Open(finalPath)
      if err != nil {
          return "", 0, recoverySFTPFileSnapshot{}, ErrRecoveryTargetUnavailable
      }
      opened, err := file.Stat()
      beforeSnapshot := recoverySFTPFileSnapshotOf(before)
      if err != nil || recoverySFTPFileSnapshotOf(opened) != beforeSnapshot {
          _ = file.Close()
          return "", 0, recoverySFTPFileSnapshot{}, ErrRecoveryTargetUnavailable
      }
      hasher := sha256.New()
      copied, err := io.CopyN(hasher, file, expectation.Bytes)
      if err != nil || copied != expectation.Bytes {
          _ = file.Close()
          return "", 0, recoverySFTPFileSnapshot{}, ErrRecoveryTargetUnavailable
      }
      var extra [1]byte
      count, extraErr := file.Read(extra[:])
      if count != 0 || !errors.Is(extraErr, io.EOF) {
          _ = file.Close()
          return "", 0, recoverySFTPFileSnapshot{}, ErrRecoveryTargetUnavailable
      }
      if err := file.Close(); err != nil {
          return "", 0, recoverySFTPFileSnapshot{}, ErrRecoveryTargetUnavailable
      }
      return hex.EncodeToString(hasher.Sum(nil)), copied, beforeSnapshot, nil
  }
  ```

  Then repeat final `Lstat`, require the identical `(size, mode, modtime)`
  snapshot, and rewalk root/parents/final canonical facts. Encode the content
  digest as lowercase hex and require exact expectation equality. No read buffer
  may scale with file size.

- [ ] **Step 6: Implement the frozen observation token and closed product**

  Add `recoverySFTPRegularFileObservationRevision` with the exact signature
  below. Call `framedDigest` with the design 39.4 domain/field order, decode its
  guaranteed 64-hex result with `hex.DecodeString`, and raw-url-base64 encode the
  32 bytes:

  ```go
  func recoverySFTPRegularFileObservationRevision(
      binding recoveryTargetSessionBinding,
      object TargetObjectRef,
      identityDigest string,
      bytesRead int64,
  ) (string, error) {
      encoded := framedDigest(
          recoverySFTPRegularFileObservationDomain,
          strconv.FormatUint(uint64(binding.NodeID), 10), binding.RootID,
          binding.RootLocatorDigest, binding.RootRevision,
          object.PrivateRelativeLocator, "regular", identityDigest,
          strconv.FormatInt(bytesRead, 10),
      )
      raw, err := hex.DecodeString(encoded)
      if err != nil {
          return "", ErrRecoveryTargetUnavailable
      }
      revision := "sftp1:" + base64.RawURLEncoding.EncodeToString(raw)
      if len(revision) != 49 || !validOpaqueRevision(revision) || sha256Shaped(revision) {
          return "", ErrRecoveryTargetUnavailable
      }
      return revision, nil
  }
  ```

  Return exactly one present arm, exact digest/bytes and this revision. Map all
  operation errors through `recoveryTargetOperationError`; context wins, an
  operation error wins over close error, and close failure blocks success.

- [ ] **Step 7: Run R19 to GREEN**

  Run:

  ```bash
  cd backend
  go test ./internal/backupasset/recovery \
    -run '^(TestRecoverySFTPTargetVerifyPresentRegularFile|TestRecoverySFTPTargetVerifyNamespaceAndObservationRevision|TestRecoverySFTPTargetCreateOwnedJobDir|TestRecoverySFTPTargetValidateOwnedJobDir|TestRecoverySFTPTargetCreateAndValidateReturnSameObservation)$' \
    -count=1
  ```

  Expected: PASS. A1 marker permissions, bounded 2049-byte reads and observation
  revisions remain unchanged.

  **Stop/rollback point:** absent succeeds, a symlink is followed, namespace
  parsing broadens authority, memory scales with expected bytes, mode/mtime is
  returned as fidelity, or the token is 64-hex/over 64 bytes.

### Task 7-R20/G20: Close drift, cancellation, privacy and deferred methods

**Files:**

- Modify: `backend/internal/backupasset/recovery/target_test.go`
- Modify after RED: `backend/internal/backupasset/recovery/target.go`

- [ ] **Step 1: Add the failing adversarial matrix**

  Add:

  ```go
  TestRecoverySFTPTargetVerifyRejectsPathContentAndStatDrift
  TestRecoverySFTPTargetVerifyCancellationAndErrors
  TestRecoverySFTPTargetA2aDeferredMethodsOpenNoSession
  ```

  Reuse `recoveryScriptedSFTPClient`/`recoveryScriptedSFTPFile` to inject missing
  paths, parent/final symlinks, directory/special final entries, wrong RealPath,
  pre/open/post size/mode/modtime changes, short read, extra byte, `(0,nil)` after
  expected bytes, digest mismatch, stat/open/read/file-close/client-close errors
  and cancellation during read. For every case assert the exact design 39.5
  sentinel, at-most-once resource close and absence of raw fake error/locator/
  credential text.

  Update `TestRecoverySFTPTargetClosedMethodsOpenNoSession` into the A2a boundary:
  issue a valid sealed absent Verify and assert unavailable, then assert the
  seven deferred methods are unavailable and resolver/dialer counts remain zero.
  Keep separate existing A1 create/validate regressions; do not call them with
  zero permits in this closed-method test.

- [ ] **Step 2: Run R20 and capture genuine RED**

  Run:

  ```bash
  cd backend
  go test ./internal/backupasset/recovery \
    -run '^(TestRecoverySFTPTargetVerifyRejectsPathContentAndStatDrift|TestRecoverySFTPTargetVerifyCancellationAndErrors|TestRecoverySFTPTargetA2aDeferredMethodsOpenNoSession)$' \
    -count=1
  ```

  Expected: at least one injected drift/error/cancellation classification or
  deferred-boundary assertion fails. Record exact RED before the matching fix.

- [ ] **Step 3: Implement the exact adversarial classifications**

  Refine `readRecoveryPresentRegularFile` and the post-read canonical rewalk so
  missing path, symlink/non-regular shape, canonical alias, size/mode/modtime
  mismatch, short `io.EOF`/`io.ErrUnexpectedEOF`, extra byte, `(0, nil)` after
  the expected byte count, digest mismatch and visible replacement return
  `ErrRecoveryTargetChanged`. Resolver, dial, SFTP construction, non-missing
  stat/open errors, non-EOF read errors, file close and session close return
  `ErrRecoveryTargetUnavailable`. At each opened-file failure branch, call
  `file.Close()` exactly once and preserve the classified operation error over
  that close result. After `session.Close()`, return `ctx.Err()` when non-nil;
  otherwise preserve the operation error, and allow a close error to block only
  an otherwise successful observation. Do not add a sentinel or log statement.

- [ ] **Step 4: Run R20 to GREEN and the combined A2a selector under race**

  Run:

  ```bash
  cd backend
  go test ./internal/backupasset/recovery \
    -run '^(TestTargetVerifyPermitRequiresExactPrivateSessionProof|TestRecoveryInterruptedOperationHandoffCarriesLockedTargetSessionBinding|TestRecoveryOrdinaryVerifyIssuanceUsesExactLockedTargetSessionBinding|TestRecoveryDeleteObservationIssuanceUsesExactLockedTargetSessionBinding|TestRecoveryAdoptionVerifyIssuanceUsesExactLockedTargetSessionBinding|TestRecoveryTargetSessionFactoryOpensPurposeExactVerify|TestRecoverySFTPTargetVerify|TestRecoverySFTPTargetA2aDeferredMethodsOpenNoSession)$' \
    -count=1
  go test -race ./internal/backupasset/recovery \
    -run '^(TestRecoveryTargetSessionFactoryOpensPurposeExactVerify|TestRecoverySFTPTargetVerify)' \
    -count=5
  ```

  Expected: PASS with no race, resource leak or raw sensitive output.

### Task 7-V7: A2a focused verification and exact stop point

- [ ] **Step 1: Run existing A1 plus complete A2a focused regression**

  ```bash
  cd backend
  go test ./internal/backupasset/recovery \
    -run '^(TestRecoveryWorkspaceMarkerCodec|TestTargetCleanupPermit|TestRecoveryPrepareFirstWrite|TestRecoveryReservedMarkerTakeover|TestRecoveryCleanupTargetValidation|TestRecoveryTargetSessionBinding|TestTargetVerifyPermit|TestRecoveryOrdinaryVerifyIssuance|TestRecoveryDeleteObservationIssuance|TestRecoveryAdoptionVerifyIssuance|TestRecoveryTargetSessionFactory|TestRecoverySFTPTargetFactory|TestRecoverySFTPTarget)' \
    -count=1
  go test -race ./internal/backupasset/recovery \
    -run '^(TestRecoveryPrepareFirstWrite|TestRecoveryReservedMarkerTakeoverPersistsValidationBeforeFirstItem|TestRecoveryCleanupTargetValidation|TestRecoverySFTPTarget)' \
    -count=5
  ```

  Expected: PASS.

- [ ] **Step 2: Run whole Recovery and required real PostgreSQL behavior**

  ```bash
  cd backend
  go test ./internal/backupasset/recovery -count=1
  go test -race ./internal/backupasset/recovery -count=1
  REQUIRE_POSTGRES_RECOVERY_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" \
    go test ./internal/backupasset/recovery -count=1
  REQUIRE_POSTGRES_RECOVERY_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" \
    go test -race ./internal/backupasset/recovery -count=1
  ```

  Expected: all pass without PostgreSQL skip. If the DSN is unavailable, A2a
  cannot be marked focused complete; record the external blocker without
  weakening the gate.

- [ ] **Step 3: Run static, format, privacy and closed-scope gates**

  ```bash
  cd backend
  go vet ./internal/backupasset/recovery
  gofmt -d internal/backupasset/recovery/target.go \
    internal/backupasset/recovery/target_test.go \
    internal/backupasset/recovery/executor.go \
    internal/backupasset/recovery/executor_test.go \
    internal/backupasset/recovery/worker.go \
    internal/backupasset/recovery/worker_test.go
  ! rg -n '[[:blank:]]+$|^(<<<<<<<|=======|>>>>>>>)' \
    internal/backupasset/recovery/target.go \
    internal/backupasset/recovery/target_test.go \
    internal/backupasset/recovery/executor.go \
    internal/backupasset/recovery/executor_test.go \
    internal/backupasset/recovery/worker.go \
    internal/backupasset/recovery/worker_test.go
  ! rg -n 'ResolveRecoveryTargetRootTx|ListRecoveryTargetRoots|PosixRename|filepath\.' \
    internal/backupasset/recovery/target.go
  cd ..
  make lint-backend
  git diff --check
  ```

  Also scan JSON/errors/captured audits for recognizable plan locator,
  node/credential revision and content values. Prove only `TargetPurposeVerify`
  was newly admitted and the seven deferred methods open zero sessions.

- [ ] **Step 4: Run Trellis, manifest, protected-file and Git-state gates**

  Validate Child and parent contexts, JSON/JSONL parsing, exact
  `9 + 55 + 81 = 145` manifest accounting, branch/HEAD/main/origin-main,
  protected hashes, no `000070|000071`, staged-zero and whitespace. Expected
  protected hashes remain:

  ```text
  go.mod b767fc9ef9376c651d0493329b710ac8dcf8d77e52686b847d244aff9f6d48fd
  recovery/testdata/rsync_local_to_remote.json 2570bd4472541322d902c4cdf2fe43f247b69f19d335558830e749567f763892
  ```

- [ ] **Step 5: Review, record evidence and stop**

  Review the complete delta against design 39.1--39.6. Append actual R16--R20
  RED/GREEN and V7 command evidence, then update Task 7 bookkeeping to mark only
  A2a `focused_complete_checked`. Task 7 and Child 13 remain `in_progress`, the
  parent remains `planning`, and program delivery remains 12/15.

  Stop before A2b create/write, A2c preflight, A2d result read, A2e overwrite/
  Lstat/absence, A3 destructive cleanup, runtime/main composition,
  orphan/quarantine and every stage/commit/push/PR/CI/merge action. Obtain
  separate approval for the next slice.

# Task 7 A2b Exact-Plan No-Overwrite Regular-File Create Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> `superpowers:executing-plans` to execute this plan task-by-task. Steps use
> checkbox (`- [ ]`) syntax for tracking. This Trellis task is `codex-inline`:
> do not dispatch implement/check subagents. The existing dedicated branch and
> worktree remain controlling; do not create or switch another one.

**Goal:** Seal exact executed-plan item write authority and implement only
isolated-parent preparation plus no-overwrite regular-file create through the
concrete SFTP target.

**Architecture:** The locked ordinary handoff seals a private item proof on the
existing live mutation permit. Concrete `WriteAtomic` validates literal create
authority before resolving the node, creates only derived isolated parents,
streams into an exclusive same-directory temp, and publishes only through
standard no-overwrite SFTP rename. Final validation reuses A2a's regular-file
observation formula, while overwrite and every other deferred method remain
closed before session creation.

**Tech Stack:** Go 1.26, GORM locked durable aggregates,
`golang.org/x/crypto/ssh`, `github.com/pkg/sftp v1.13.10`, standard-library
`crypto/rand`, SHA-256, base64url, POSIX `path`, bounded `io`, Go tests and race
detector.

---

## A2b file map and immutable scope

- Modify `backend/internal/backupasset/recovery/target.go`: exact item proof,
  injected CSPRNG, mode-specific parent preparation, bounded temp writer,
  concrete create-only `WriteAtomic`, stable write result and closed errors.
- Modify `backend/internal/backupasset/recovery/target_test.go`: proof,
  authority, parent, streaming, rename, result, cancellation and privacy tests.
- Modify `backend/internal/backupasset/recovery/executor.go`: mint an item
  permit from the complete locked handoff rather than an object-only rewrite.
- Modify `backend/internal/backupasset/recovery/executor_test.go`: locked
  handoff issuance, fake admission and existing unresolved projection coverage.
- Do not modify `backend/internal/backupasset/recovery/worker.go` or
  `backend/internal/backupasset/recovery/worker_test.go`: the reviewed A2a
  `interruptedOperationHandoff` already exposes the executed plan, job, item,
  operation, exact object, expectation and locked target-session binding needed
  by Section 40.2. No new durable field, query or transaction is allowed.
- Update only the existing Task 7 `prd.md`, `design.md`, `implement.md`,
  `research/implementation-evidence.md` and `task.json` for planning/evidence.

No new path, dependency, table, model, migration, setting, crypto key domain,
sshutil, Provider, Repository, runtime, main, API or frontend change is allowed.
`go.mod` and `recovery/testdata/rsync_local_to_remote.json` remain protected.
Staging and commit steps are deliberately absent: explicit project policy keeps
staged paths at zero until the whole-task Phase 3.4 gate.

## A2b frozen constants and signatures

Use these exact private names unless a compile-time collision with an existing
identifier is found before R21; if that happens, amend Section 40 and this plan
before product edits:

```go
const (
	targetItemWritePermitProofDomain = "xirang/recovery/target-item-write-permit-proof/v1"
	recoveryPayloadTempPrefix        = ".xirang-recovery-file-v1.tmp-"
	recoveryPayloadTempEntropyBytes  = 32
)

type targetItemWritePermitProof struct {
	sessionBinding recoveryTargetSessionBinding
	jobID          string
	targetMode     TargetMode
	object         TargetObjectRef
	operation      RecoveryOperationKind
	expectedPrior  ExpectedTargetIdentity
	expectedDigest string
	expectedBytes  int64
	bindingDigest  string
}

type targetItemWriteAuthority struct {
	sessionBinding recoveryTargetSessionBinding
	jobID          string
	targetMode     TargetMode
	operation      RecoveryOperationKind
	expectedPrior  ExpectedTargetIdentity
}

type recoveryCreateParentSnapshot struct {
	path string
	mode os.FileMode
}

type recoveryPreparedCreateParents struct {
	finalPath string
	parents  []recoveryCreateParentSnapshot
}
```

`TargetWritePermit` gains `itemProof *targetItemWritePermitProof`. The concrete
helpers have these signatures:

```go
func issueTargetItemWritePermit(
	permit TargetWritePermit,
	proof targetItemWritePermitProof,
) TargetWritePermit

func targetItemWritePermitProofDigest(
	permit TargetWritePermit,
	proof *targetItemWritePermitProof,
) string

func (permit TargetWritePermit) validateItemWriteAt(
	now time.Time,
	request TargetWriteAtomicRequest,
) (targetItemWriteAuthority, error)

func (coordinator *WorkerCoordinator) ordinaryItemWritePermit(
	claim RecoveryWorkerClaim,
	base TargetWritePermit,
	handoff interruptedOperationHandoff,
	expectedRevision string,
) (TargetWritePermit, error)

func prepareRecoveryCreateParents(
	client recoveryTargetSFTPClient,
	binding recoveryTargetSessionBinding,
	authority targetItemWriteAuthority,
	object TargetObjectRef,
	validateLive func() error,
) (recoveryPreparedCreateParents, error)

func revalidateRecoveryCreateParents(
	client recoveryTargetSFTPClient,
	binding recoveryTargetSessionBinding,
	authority targetItemWriteAuthority,
	object TargetObjectRef,
	prepared recoveryPreparedCreateParents,
) error
```

The target's entropy field is `entropy io.Reader`; production and test
constructors initialize it explicitly, with production using `rand.Reader`.
No package-global mutable entropy hook is permitted.

### Task 7-R21/G21: Seal exact locked-handoff item write authority

**Files:**
- Modify: `backend/internal/backupasset/recovery/target_test.go`
- Modify: `backend/internal/backupasset/recovery/executor_test.go`
- Modify: `backend/internal/backupasset/recovery/target.go`
- Modify: `backend/internal/backupasset/recovery/executor.go`

- [x] **Step 1: Add the direct proof RED**

  Add `TestTargetItemWritePermitRequiresExactLockedHandoffProof` beside
  `TestTargetVerifyPermitRequiresExactPrivateSessionProof`. Build a valid base
  mutation permit with `recoveryTargetSessionBindingForTest`, a canonical
  isolated object `jobs/<jobID>/nested/item.txt`, literal create, absent prior,
  lowercase SHA-256 and seven bytes. The test must call the exact private issuer
  and require `validateItemWriteAt` to return the exact authority.

  Freeze table mutations for: nil proof, public node/root/path/root-revision/
  expiry/job/expected-target-revision, session binding and its digest, proof job
  ID/mode/object/operation/prior/digest/bytes/binding digest. Each case requires
  exact `ErrInvalidTargetPermit`. JSON marshaling must contain none of the
  private locator, binding digest or recognizable session values.

  The accepted core assertion is:

  ```go
  authority, err := permit.validateItemWriteAt(now, request)
  if err != nil {
      t.Fatalf("validate exact item write permit: %v", err)
  }
  if authority.sessionBinding != binding || authority.jobID != jobID ||
      authority.targetMode != TargetModeIsolated ||
      authority.operation != RecoveryOperationCreate ||
      authority.expectedPrior != (ExpectedTargetIdentity{Kind: ExpectedTargetAbsent}) {
      t.Fatalf("item authority=%+v, want exact locked create authority", authority)
  }
  ```

- [x] **Step 2: Add the locked ordinary-issuance RED**

  Add `TestRecoveryOrdinaryItemWriteIssuanceUsesExactLockedTargetSessionBinding`
  beside the existing ordinary Verify issuance test. Use
  `newRecoveryExecutionFixture`, run through durable first-write preparation,
  load the real ordinary handoff, and mint the item permit. Require proof fields
  to equal the handoff's session binding, job mode, object, operation and exact
  expected facts. Substituting an unlocked/current-registry binding, job, mode,
  object or expected revision must fail before the target fake records a write.

- [x] **Step 3: Run R21 and record genuine RED**

  ```bash
  cd backend
  go test ./internal/backupasset/recovery \
    -run '^(TestTargetItemWritePermitRequiresExactLockedHandoffProof|TestRecoveryOrdinaryItemWriteIssuanceUsesExactLockedTargetSessionBinding)$' \
    -count=1
  ```

  Expected: compile-time RED for the missing item proof/issuer and the old
  object-only `ordinaryItemWritePermit` signature. Environment, fixture or
  unrelated compile failures do not count.

- [x] **Step 4: Implement the proof and exact issuer**

  In `target.go`, add the frozen constants/types and `itemProof` field. The proof
  digest must use `framedDigest` with this exact ordered input:

  ```go
  return framedDigest(
      targetItemWritePermitProofDomain,
      permit.permit.proof.bindingDigest,
      proof.sessionBinding.bindingDigest,
      proof.jobID,
      string(proof.targetMode),
      proof.object.RootID,
      proof.object.RootLocatorDigest,
      proof.object.TargetPathDigest,
      proof.object.PrivateRelativeLocator,
      string(proof.operation),
      string(proof.expectedPrior.Kind),
      proof.expectedPrior.Digest,
      proof.expectedDigest,
      strconv.FormatInt(proof.expectedBytes, 10),
  )
  ```

  `issueTargetItemWritePermit` must clear any copied proof first, require the
  base permit and supplied private fields to be structurally valid, copy the
  proof, calculate the digest, and return an unsealed permit on failure.
  `validateItemWriteAt` must first run the live base validator and request/object
  parity, then recompute every proof relation. It accepts structurally exact
  create or overwrite facts; unsupported-operation classification remains the
  concrete adapter's responsibility.

  In `executor.go`, change `ordinaryItemWritePermit` to accept the handoff. Copy
  the base permit, set the handoff object's target digest and current target
  revision, reseal the mutation proof with
  `handoff.targetSessionBinding`, construct `TargetWritePermit`, then attach the
  item proof from the handoff. Reject any disagreement as
  `ErrRecoveryWorkerFenceLost`. Change the sole ordinary-loop call site to pass
  `execution.handoff`. Issuance must independently require
  `RecoveryOperationSourceAssetRef` plus `RecoveryDisplayClassRegular` for
  create/overwrite and exact plan/job/item/operation/object/session parity; it
  must not rely only on the caller's earlier handoff validation.

- [x] **Step 5: Run R21 to GREEN and protect A1/A2a authority**

  ```bash
  cd backend
  go test ./internal/backupasset/recovery \
    -run '^(TestTargetItemWritePermitRequiresExactLockedHandoffProof|TestRecoveryOrdinaryItemWriteIssuanceUsesExactLockedTargetSessionBinding|TestRecoveryPrepareFirstWriteCarriesExactTargetSessionBinding|TestTargetVerifyPermitRequiresExactPrivateSessionProof|TestRecoveryOrdinaryVerifyIssuanceUsesExactLockedTargetSessionBinding)$' \
    -count=1
  ```

  Expected: PASS. Workspace permits remain valid without an item proof for A1;
  only concrete item methods require the new proof.

  **Stop/rollback point:** if item proof issuance requires a registry lookup,
  target I/O inside the locked transaction, a public locator/session field, or a
  model/migration change, revert only R21/G21 and amend Section 40.

### Task 7-R22/G22: Freeze create-only admission, entropy and parent preparation

**Files:**
- Modify: `backend/internal/backupasset/recovery/target_test.go`
- Modify: `backend/internal/backupasset/recovery/target.go`

- [x] **Step 1: Add create versus deferred-overwrite admission RED**

  Add `TestRecoverySFTPTargetWriteAtomicAdmitsOnlyExactCreate`. Inject an exact
  item create permit and deterministic 32-byte entropy. Require resolver/dialer
  purpose `TargetPurposeWrite` / `sshutil.PurposeRecoveryWrite`, exact node and
  credential revisions, and safe job correlation. A valid exact overwrite
  permit must return exact `ErrRecoveryTargetUnavailable` with zero resolver,
  dial and SFTP calls. Nil content, request digest/bytes/object substitution,
  missing/forged proof and expired authority return exact
  `ErrInvalidTargetPermit` with zero entropy and zero session calls.

- [x] **Step 2: Add the complete parent matrix RED**

  Add `TestRecoverySFTPTargetWriteAtomicPreparesModeExactParents` using
  `recoveryLocalSFTPTargetFixture` and its local SFTP facade.

  Freeze these isolated cases: top-level file creates no extra directory;
  `nested/deeper/item` creates `nested` then `deeper`; every created/existing
  parent is canonical `0700`; an existing canonical chain is mutation-free; a
  lost `Mkdir` race is accepted only when the winner is canonical `0700`;
  wrong-mode, symlink, file, special and wrong-realpath entries are changed;
  failed chmod is unavailable and no later parent/file mutation occurs.

  Freeze these in-place cases: an existing canonical chain is accepted without
  chmod/mkdir regardless of its initial permission bits; later same-call mode
  drift is rejected. Missing, symlink, file, special or wrong-realpath parent
  returns changed with zero `Mkdir`, `Chmod`, `OpenFile`, `Rename` and `Remove`.
  Drive every otherwise-valid parent case into a scripted `OpenFile` failure so
  the test remains stable after later batches open temp creation: require exact
  unavailable, no final object and only the parent operations asserted here.

- [x] **Step 3: Run R22 and record genuine RED**

  ```bash
  cd backend
  go test ./internal/backupasset/recovery \
    -run '^(TestRecoverySFTPTargetWriteAtomicAdmitsOnlyExactCreate|TestRecoverySFTPTargetWriteAtomicPreparesModeExactParents)$' \
    -count=1
  ```

  Expected: runtime RED because concrete `WriteAtomic` remains unavailable.

- [x] **Step 4: Add injected entropy and authority extraction**

  Add `entropy io.Reader` to `recoverySFTPTarget`. Production construction uses
  `rand.Reader`; `newRecoverySFTPTargetForTest` also defaults to `rand.Reader`,
  and tests replace the instance field. Before resolver use,
  `recoveryTargetItemWriteAuthority` must validate the exact proof/request and
  classify valid overwrite as unavailable. Read entropy using
  `io.ReadFull(target.entropy, nonce[:])`, require exactly 32 bytes, encode with
  `base64.RawURLEncoding`, and reject `tempPath == finalPath` before session.

- [x] **Step 5: Implement one mode-specific parent helper**

  Implement the frozen `prepareRecoveryCreateParents` signature and return the
  exact final path plus an ordered snapshot of the root-relative parent paths
  and their complete `os.FileMode` values. It must call
  `validateRecoveryRootPrefixes`, walk exact POSIX components and
  implement Section 40.4 without `MkdirAll` or `filepath`. In isolated mode,
  `jobs` and job must exist `0700`; only later missing parents may be created.
  Call `validateLive` immediately before every `Mkdir` and `Chmod`. A failed
  `Mkdir` is accepted only through exact canonical `0700` validation. In-place
  mode calls only read methods and accepts no missing parent or alias while
  accepting any initial directory mode.

  `validateLive` is the closed composition of `ctx.Err()` followed by
  `permit.validateItemWriteAt(target.now().UTC(), request)`, so context identity
  wins before each mutation. Implement the frozen read-only
  `revalidateRecoveryCreateParents` helper for pre-temp, pre-rename and
  post-final checks. It must revalidate root prefixes, shape and canonical path;
  isolated parents must remain `0700`, while in-place parents must retain the
  exact initially observed mode. It never creates, chmods or repairs a path.

- [x] **Step 6: Run R22 to its bounded GREEN**

  At this step `WriteAtomic` may stop with unavailable immediately after parent
  preparation; tests must assert only admission/entropy/parent facts and must
  not claim file-create success yet.

  ```bash
  cd backend
  go test ./internal/backupasset/recovery \
    -run '^(TestRecoverySFTPTargetWriteAtomicAdmitsOnlyExactCreate|TestRecoverySFTPTargetWriteAtomicPreparesModeExactParents)$' \
    -count=1
  ```

  Expected: PASS for the frozen partial boundary, with file publication still
  absent and `CreateDirectory` still zero-session unavailable.

  **Stop/rollback point:** if in-place needs a synthesized permission, isolated
  parents cannot be derived solely from the exact object, or lost-race handling
  requires chmodding an existing entry, stop and amend the design.

### Task 7-R23/G23: Freeze bounded exclusive temp creation

**Files:**
- Modify: `backend/internal/backupasset/recovery/target_test.go`
- Modify: `backend/internal/backupasset/recovery/target.go`

- [x] **Step 1: Add temp identity and ownership RED**

  Add `TestRecoverySFTPTargetWriteAtomicUsesExclusivePrivateTemp`. With injected
  nonce bytes `0x5a` x32, require basename
  `.xirang-recovery-file-v1.tmp-WlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlo`,
  same parent as final, flags `os.O_WRONLY|os.O_CREATE|os.O_EXCL`, chmod/verified
  mode `0600`, and no final open with write/truncate flags. Entropy failure must
  precede `Mkdir`; a pre-existing temp collision must not be removed; after a
  successful exclusive open, later failure may remove exactly that temp once.

- [x] **Step 2: Add exact stream and reread RED**

  Add `TestRecoverySFTPTargetWriteAtomicStreamsExactContent`. Table zero-byte,
  ordinary payload, short EOF, extra byte, `(0,nil)` after expected bytes,
  injected read error, short/zero write, wrong digest, Sync failure, write-handle
  close failure, reopen failure, reopened stat drift, reread short/extra/digest
  drift and temp canonical/mode replacement. Only zero/ordinary exact streams
  may reach the later rename boundary. Assert maximum source extra read request
  is one byte and no allocation/read scales beyond the existing bounded copy
  buffer plus one byte.

- [x] **Step 3: Run R23 and record genuine RED**

  ```bash
  cd backend
  go test ./internal/backupasset/recovery \
    -run '^(TestRecoverySFTPTargetWriteAtomicUsesExclusivePrivateTemp|TestRecoverySFTPTargetWriteAtomicStreamsExactContent)$' \
    -count=1
  ```

  Expected: runtime RED at the current post-parent unavailable stop.

- [x] **Step 4: Implement exact streaming**

  Add this closed streaming helper and keep all dependency errors sanitized:

  ```go
  func writeRecoveryRegularContent(
      file recoveryTargetSFTPFile,
      content io.Reader,
      expectedBytes int64,
      expectedDigest string,
  ) error
  ```

  It uses `sha256.New`, `io.MultiWriter(file, hasher)` and
  `io.CopyN(..., expectedBytes)`, requires the exact copied count, then performs
  one `Read` into `[1]byte{}` and accepts only `(0, io.EOF)`. Compare
  `hex.EncodeToString(hasher.Sum(nil))` with the expected lowercase digest.
  Any short, extra, zero-nil, read/write or digest failure returns sanitized
  unavailable and cannot call Sync or Rename.

- [x] **Step 5: Implement temp open, Sync, close and reread**

  In the create operation, prove final absence, exclusive-open temp, set the
  local `tempOwned` flag only after success, chmod/verify `0600`, stream exact
  content, require `Sync`, close exactly once, then reopen and verify using the
  existing bounded regular-file reader plus exact mode/canonical checks. A
  deferred cleanup removes only `tempPath` while `tempOwned` is true and never
  replaces the primary error. Drive the otherwise-successful zero/ordinary
  table rows into a scripted sanitized pre-publication stop; once R24 adds the
  rename call, the same rows inject a rename error. The assertions remain exact
  unavailable, no final object, preserved created parents and removal of at
  most the exact owned temp, so R23 coverage cannot turn into a false success.

- [x] **Step 6: Run R23 to bounded GREEN**

  ```bash
  cd backend
  go test ./internal/backupasset/recovery \
    -run '^(TestRecoverySFTPTargetWriteAtomicUsesExclusivePrivateTemp|TestRecoverySFTPTargetWriteAtomicStreamsExactContent)$' \
    -count=1
  ```

  Expected: PASS, with tests stopping immediately before actual rename and no
  final success claim.

  **Stop/rollback point:** missing server Sync support, inability to distinguish
  exclusive ownership, size-proportional buffering or any need for truncating
  open blocks A2b. Do not add a fallback.

### Task 7-R24/G24: Publish by standard no-overwrite rename and return A2a revision

**Files:**
- Modify: `backend/internal/backupasset/recovery/target_test.go`
- Modify: `backend/internal/backupasset/recovery/target.go`

- [x] **Step 1: Add the final-absence and rename RED**

  Add `TestRecoverySFTPTargetWriteAtomicNeverOverwritesFinal`. Freeze
  pre-temp and pre-rename final matrices for existing same-content regular,
  different regular, directory, symlink and special entries. All return exact
  changed and preserve the entry byte-for-byte. Inject a concurrent final after
  temp verification; standard `Rename` must fail without replacement. An
  ambiguous rename error returns unavailable and never triggers an existence-
  based success inference. Operation logs must contain exactly `Rename`, never
  `PosixRename`, generic upload, final-path `Remove` or truncating `OpenFile`.
  Failure cleanup may contain one `Remove(tempPath)` only after this call's
  exclusive open established ownership.

- [x] **Step 2: Add result/Verify equality RED**

  Add `TestRecoverySFTPTargetWriteAtomicReturnsExactVerifyRevision` on the local
  OS-backed facade. Create zero-byte and ordinary nested files, then issue an
  independent exact A2a Verify permit. Require:

  ```go
  if write.BytesWritten != expectation.Present.Bytes ||
      write.IdentityDigest != expectation.Present.IdentityDigest ||
      write.TargetRevision != observed.ObservedRevision {
      t.Fatalf("write=%+v verify=%+v, want identical content and revision", write, observed)
  }
  ```

  Require final `0600`, no temp entry after success, and stable token computed
  by `recoverySFTPRegularFileObservationRevisionForTest`.

- [x] **Step 3: Add final drift and close RED**

  Add `TestRecoverySFTPTargetWriteAtomicRejectsFinalAndCloseDrift`. Inject final
  mode/content/stat/canonical replacement, live-permit revocation immediately
  before rename and after final verify, SFTP close failure and SSH close failure.
  None may return a result. Final live revocation returns invalid permit;
  transport/close ambiguity returns unavailable; context identity wins.

- [x] **Step 4: Run R24 and record genuine RED**

  ```bash
  cd backend
  go test ./internal/backupasset/recovery \
    -run '^(TestRecoverySFTPTargetWriteAtomicNeverOverwritesFinal|TestRecoverySFTPTargetWriteAtomicReturnsExactVerifyRevision|TestRecoverySFTPTargetWriteAtomicRejectsFinalAndCloseDrift)$' \
    -count=1
  ```

  Expected: runtime RED because no rename/final result is implemented.

- [x] **Step 5: Implement final publish and result**

  After temp verification, call the read-only parent rewalk, live item-permit
  validation and exact final-absence check in that order. Call only
  `client.Rename(tempPath, finalPath)`. On success set `tempOwned=false`, verify
  final `0600`, call `verifyRecoveryPresentRegularFile` with the exact proof
  mode/job/object and request expectation, rewalk mode-specific parents and run
  final live validation. Convert the closed observation to the exact
  `TargetWriteResult`; reject any unexpected observation shape.

  `WriteAtomic` follows A1/A2a session closure order: context wins, operation
  error wins over close noise, and a close error blocks an otherwise successful
  result. Do not infer success from post-error final visibility.

- [x] **Step 6: Run R24 to GREEN and combine target tests**

  ```bash
  cd backend
  go test ./internal/backupasset/recovery \
    -run '^(TestRecoverySFTPTargetWriteAtomic|TestRecoverySFTPTargetVerify|TestRecoverySFTPTargetCreateOwnedJobDir|TestRecoverySFTPTargetValidateOwnedJobDir)' \
    -count=1
  ```

  Expected: PASS. Existing marker create/validation and present Verify remain
  unchanged.

  **Stop/rollback point:** any server overwrites a destination through standard
  Rename, final/Verify revision differs, or success requires adopting an
  existing final. Return the closed category and stop; never use PosixRename.

### Task 7-R25/G25: Close executor, unresolved, cancellation and privacy integration

**Files:**
- Modify: `backend/internal/backupasset/recovery/executor_test.go`
- Modify: `backend/internal/backupasset/recovery/target_test.go`
- Modify: `backend/internal/backupasset/recovery/executor.go`
- Modify: `backend/internal/backupasset/recovery/target.go`

- [x] **Step 1: Strengthen executor fake admission RED**

  Add `TestRecoveryExecuteClaimCreateCarriesExactItemWriteProof`. In
  `recoveryExecutionTargetFake.WriteAtomic`, record the permit as well as the
  request and, for this selector, require `validateItemWriteAt` to succeed with
  the exact loaded create facts before reading content. Prove the source stream
  is opened before the target call, no DB transaction remains open, and the
  subsequent Verify sees the same object/expected content. Remove or mutate the
  item proof in a test hook and require zero fake remote mutation. Because this
  substitution is detected only after `WriteAtomic` begins, require the existing
  terminal `write_result_invalid` unresolved projection; issuer-input
  substitutions tested in R21 remain pre-call `ErrRecoveryWorkerFenceLost`.

- [x] **Step 2: Freeze unresolved behavior and deferred overwrite**

  Extend `TestRecoveryExecuteClaimPostArmCallErrorsBecomeUnresolved` with the
  concrete A2b call-error shape: after parent or temp mutation, a sanitized
  `WriteAtomic` error produces one terminal `write_result_invalid` evidence,
  empty write/observation product facts, unchanged target chain, failed current
  item and no later target call. Separately call concrete `WriteAtomic` with a
  valid exact overwrite proof and require unavailable before resolver/session;
  do not project overwrite success in A2b.

- [x] **Step 3: Add cancellation/resource/privacy RED**

  Add `TestRecoverySFTPTargetWriteAtomicCancellationAndPrivacy`. Cancel during
  entropy, parent mkdir/chmod, source read, temp write, Sync, reopen, pre-rename,
  rename, final read and session close. Require exact context identity, at-most-
  once file/SFTP/SSH close, removal of at most the exact exclusively owned temp,
  and no new mutation after cancellation is observed. Scan errors, JSON and
  captured audit/log products for recognizable host, username, credential,
  root/object locator, content, temp nonce/name, raw SFTP status and injected
  dependency strings; all must be absent.

- [x] **Step 4: Run R25 and record genuine RED if integration is incomplete**

  ```bash
  cd backend
  go test ./internal/backupasset/recovery \
    -run '^(TestRecoveryExecuteClaimCreateCarriesExactItemWriteProof|TestRecoveryExecuteClaimPostArmCallErrorsBecomeUnresolved|TestRecoverySFTPTargetWriteAtomicCancellationAndPrivacy)$' \
    -count=1
  ```

  Expected: either genuine behavior RED attributed to missing integration or
  inherited GREEN only where R21--R24 already closed the exact assertion. Record
  inherited GREEN as coverage, not reconstructed RED.

- [x] **Step 5: Apply only minimal integration fixes**

  Keep the existing executor call-error/unresolved projection. Change only
  permit issuance, fake validation or target cleanup/cancellation ordering that
  the frozen selectors prove missing. Do not add a persisted category, retry,
  directory operation, overwrite branch or new source contract.

- [x] **Step 6: Run R25 to GREEN and the A2b focused race selector**

  ```bash
  cd backend
  go test ./internal/backupasset/recovery \
    -run '^(TestRecoveryExecuteClaimCreateCarriesExactItemWriteProof|TestRecoveryExecuteClaimPostArmCallErrorsBecomeUnresolved|TestRecoverySFTPTargetWriteAtomicCancellationAndPrivacy)$' \
    -count=1
  go test -race ./internal/backupasset/recovery \
    -run '^(TestTargetItemWritePermit|TestRecoveryOrdinaryItemWriteIssuance|TestRecoverySFTPTargetWriteAtomic|TestRecoveryExecuteClaimCreateCarriesExactItemWriteProof)' \
    -count=5
  ```

  Expected: PASS with no goroutine/resource leak and no data race.

### Task 7-V8: A2b focused verification, review and exact stop point

**Files:**
- Update: `.trellis/tasks/07-28-backup-assets-controlled-recovery/prd.md`
- Update: `.trellis/tasks/07-28-backup-assets-controlled-recovery/implement.md`
- Update: `.trellis/tasks/07-28-backup-assets-controlled-recovery/research/implementation-evidence.md`
- Update: `.trellis/tasks/07-28-backup-assets-controlled-recovery/task.json`

- [x] **Step 1: Run A1+A2a+A2b focused normal and repeated race gates**

  ```bash
  cd backend
  go test ./internal/backupasset/recovery \
    -run '^(TestRecoveryWorkspaceMarkerCodec|TestTargetCleanupPermit|TestRecoveryPrepareFirstWrite|TestRecoveryReservedMarkerTakeover|TestRecoveryCleanupTargetValidation|TestRecoveryTargetSessionBinding|TestTargetVerifyPermit|TestTargetItemWritePermit|TestRecoveryOrdinaryVerifyIssuance|TestRecoveryOrdinaryItemWriteIssuance|TestRecoveryDeleteObservationIssuance|TestRecoveryAdoptionVerifyIssuance|TestRecoveryTargetSessionFactory|TestRecoverySFTPTarget)' \
    -count=1
  go test -race ./internal/backupasset/recovery \
    -run '^(TestRecoveryPrepareFirstWrite|TestRecoveryReservedMarkerTakeoverPersistsValidationBeforeFirstItem|TestRecoveryCleanupTargetValidation|TestRecoverySFTPTarget|TestTargetItemWritePermit|TestRecoveryOrdinaryItemWriteIssuance)' \
    -count=5
  ```

  Expected: PASS.

- [x] **Step 2: Run whole Recovery and required PostgreSQL gates**

  Reuse the existing dedicated `xirang-c13-pg` fixture without printing or
  persisting credentials, restarting, replacing or removing it.

  ```bash
  cd backend
  go test ./internal/backupasset/recovery -count=1
  go test -race ./internal/backupasset/recovery -count=1
  REQUIRE_POSTGRES_RECOVERY_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" \
    go test ./internal/backupasset/recovery -count=1
  REQUIRE_POSTGRES_RECOVERY_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" \
    go test -race ./internal/backupasset/recovery -count=1
  ```

  Expected: all pass with no PostgreSQL skip. Missing DSN is an external
  blocker, not permission to weaken the gate.

- [x] **Step 3: Run static, format, privacy and scope gates**

  ```bash
  cd backend
  go vet ./internal/backupasset/recovery
  gofmt -d internal/backupasset/recovery/target.go \
    internal/backupasset/recovery/target_test.go \
    internal/backupasset/recovery/executor.go \
    internal/backupasset/recovery/executor_test.go \
    internal/backupasset/recovery/worker.go \
    internal/backupasset/recovery/worker_test.go
  ! rg -n '[[:blank:]]+$|^(<<<<<<<|=======|>>>>>>>)' \
    internal/backupasset/recovery/target.go \
    internal/backupasset/recovery/target_test.go \
    internal/backupasset/recovery/executor.go \
    internal/backupasset/recovery/executor_test.go \
    internal/backupasset/recovery/worker.go \
    internal/backupasset/recovery/worker_test.go
  ! rg -n 'ResolveRecoveryTargetRootTx|ListRecoveryTargetRoots|PosixRename|filepath\.|MkdirAll' \
    internal/backupasset/recovery/target.go
  cd ..
  make lint-backend
  git diff --check
  ```

  Also prove valid overwrite plus `ProbeRoot`, `Lstat`, `CreateDirectory`,
  `OpenOwnedResult`, `Delete` and `RemoveOwnedJobDir` open zero sessions, and
  scan every recognizable A2b private value across errors/JSON/audits/logs.

- [x] **Step 4: Run Trellis, manifest, protected-file and Git-state gates**

  Run the exact structural checks:

  ```bash
  python3 ./.trellis/scripts/task.py validate \
    .trellis/tasks/07-28-backup-assets-controlled-recovery
  python3 ./.trellis/scripts/task.py validate \
    .trellis/tasks/07-12-backup-data-explorer-design
  jq empty \
    .trellis/tasks/07-28-backup-assets-controlled-recovery/task.json \
    .trellis/tasks/07-12-backup-data-explorer-design/task.json
  jq -s empty \
    .trellis/tasks/07-28-backup-assets-controlled-recovery/implement.jsonl \
    .trellis/tasks/07-28-backup-assets-controlled-recovery/check.jsonl
  test "$(git diff --cached --name-only | wc -l)" -eq 0
  test -z "$(find backend/internal/database/migrations -type f \
    \( -name '000070*' -o -name '000071*' \) -print)"
  test "$(git branch --show-current)" = codex/backup-assets-controlled-recovery
  test "$(git rev-parse HEAD)" = "$(git rev-parse main)"
  test "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)"
  ```

  Parse Sections 2.1--2.3 and enforce exact manifest/dirty-scope accounting with:

  ```bash
  bash -c '
  set -euo pipefail
  plan=.trellis/tasks/07-28-backup-assets-controlled-recovery/implement.md
  extract_manifest() {
    awk -v section="$1" '\''
      $0 ~ "^### " section " " { active = 1; next }
      active && /^### / { exit }
      active && /^```text$/ { block = 1; next }
      block && /^```$/ { exit }
      block && NF { print }
    '\'' "$plan"
  }
  phase1="$(extract_manifest "2[.]1")"
  create="$(extract_manifest "2[.]2")"
  modify="$(extract_manifest "2[.]3")"
  all="$(printf "%s\n%s\n%s\n" "$phase1" "$create" "$modify")"
  phase1_count=$(printf "%s\n" "$phase1" | wc -l)
  create_count=$(printf "%s\n" "$create" | wc -l)
  modify_count=$(printf "%s\n" "$modify" | wc -l)
  total_count=$(printf "%s\n" "$all" | wc -l)
  unique_count=$(printf "%s\n" "$all" | sort -u | wc -l)
  duplicate_count=$((total_count - unique_count))
  dirty="$(git status --porcelain=v1 -uall | cut -c4-)"
  manifest_dirty=$(comm -12 \
    <(printf "%s\n" "$dirty" | sort -u) \
    <(printf "%s\n" "$all" | sort -u) | wc -l)
  protected="$(printf "%s\n" \
    go.mod recovery/testdata/rsync_local_to_remote.json)"
  protected_dirty=$(comm -12 \
    <(printf "%s\n" "$dirty" | sort -u) \
    <(printf "%s\n" "$protected" | sort -u) | wc -l)
  approved_exception="$(printf "%s\n" \
    backend/internal/backupasset/audit_action.go \
    backend/internal/backupasset/audit_action_test.go)"
  approved_exception_dirty=$(comm -12 \
    <(printf "%s\n" "$dirty" | sort -u) \
    <(printf "%s\n" "$approved_exception" | sort -u) | wc -l)
  allowed="$(printf "%s\n%s\n%s\n" "$all" "$protected" "$approved_exception")"
  outside=$(comm -23 \
    <(printf "%s\n" "$dirty" | sort -u) \
    <(printf "%s\n" "$allowed" | sort -u) | wc -l)
  staged=$(git diff --cached --name-only | wc -l)
  create_present=0
  while IFS= read -r path_value; do
    if git cat-file -e "HEAD:$path_value" 2>/dev/null; then
      create_present=$((create_present + 1))
    fi
  done <<< "$create"
  modify_missing=0
  while IFS= read -r path_value; do
    if ! git cat-file -e "HEAD:$path_value" 2>/dev/null; then
      modify_missing=$((modify_missing + 1))
    fi
  done <<< "$modify"
  future=$(find backend/internal/database/migrations -type f \
    \( -name "000070*" -o -name "000071*" \) -print | wc -l)
  printf "phase1=%d create=%d modify=%d total=%d unique=%d duplicates=%d\n" \
    "$phase1_count" "$create_count" "$modify_count" "$total_count" \
    "$unique_count" "$duplicate_count"
  printf "dirty=%d manifest_dirty=%d protected_dirty=%d approved_exception_dirty=%d outside=%d staged=%d\n" \
    "$(printf "%s\n" "$dirty" | wc -l)" "$manifest_dirty" \
    "$protected_dirty" "$approved_exception_dirty" "$outside" "$staged"
  printf "create_present_at_head=%d modify_missing_at_head=%d future_000070_71=%d\n" \
    "$create_present" "$modify_missing" "$future"
  test "$phase1_count" -eq 9
  test "$create_count" -eq 55
  test "$modify_count" -eq 81
  test "$total_count" -eq 145
  test "$unique_count" -eq 145
  test "$duplicate_count" -eq 0
  test "$manifest_dirty" -eq 91
  test "$protected_dirty" -eq 2
  test "$approved_exception_dirty" -eq 2
  test "$outside" -eq 0
  test "$staged" -eq 0
  test "$create_present" -eq 0
  test "$modify_missing" -eq 0
  test "$future" -eq 0
  '
  ```

  Require this complete output, including no dirty path outside the manifest
  except the two protected paths:

  ```text
  phase1=9 create=55 modify=81 total=145 unique=145 duplicates=0
  dirty=95 manifest_dirty=91 protected_dirty=2 approved_exception_dirty=2 outside=0 staged=0
  create_present_at_head=0 modify_missing_at_head=0 future_000070_71=0
  ```

  Verify the protected hashes with:

  ```bash
  sha256sum go.mod recovery/testdata/rsync_local_to_remote.json
  ```

  The output must remain:

  ```text
  go.mod b767fc9ef9376c651d0493329b710ac8dcf8d77e52686b847d244aff9f6d48fd
  recovery/testdata/rsync_local_to_remote.json 2570bd4472541322d902c4cdf2fe43f247b69f19d335558830e749567f763892
  ```

- [x] **Step 5: Perform inline specification and quality review**

  Review the complete A2b delta against design 40.1--40.7 and every PRD A2b
  acceptance row. Resolve every Critical/Important issue with a new genuine
  RED before changing product behavior, then rerun all affected gates. Confirm
  no A2c--A3 product is implemented and no A1/A2a credit is weakened.

- [x] **Step 6: Record evidence and stop**

  Append actual R21--R25 RED/GREEN commands, failure identities, minimal GREEN
  deltas and V8 results to `research/implementation-evidence.md`. Check the A2b
  PRD acceptance rows and add Task 7 bookkeeping
  `a2b_exact_plan_no_overwrite_regular_file_create.status =
  focused_complete_checked`. Task 7 and Child 13 remain `in_progress`, parent
  remains `planning`, and program delivery remains 12/15.

  Stop before A2c preflight, A2d result read, A2e overwrite/Lstat/absence, A3
  destructive cleanup/delete/tombstone, runtime/main composition,
  orphan/quarantine and every stage/commit/push/PR/CI/merge action. Obtain a
  separate approval for the next slice.

# Task 7 A2c Split Concrete Preflight Implementation Plan

> **For inline execution:** REQUIRED SUB-SKILL: use
> `superpowers:test-driven-development` for every behavior and
> `trellis-check` before any completion claim. Do not dispatch implement/check
> sub-agents and do not create per-step commits; this Child keeps one controlled
> work branch and defers Git delivery to its final delivery phase.

**Goal:** replace the composite fake target preflight with an exact draft-plan
target observation capability, then separately compose real source/policy
evidence without allowing either boundary to forge the other.

**Architecture:** A2c1 seals one hook-decrypted draft plan into a private
preflight proof, opens only a purpose-exact read-only target probe, and splits
target facts from a required external-evidence port. A test-only deterministic
issuer keeps the evaluator independently buildable; A2c2 later implements the
production Provider/Repository evidence adapter and durable composition outside
target I/O transactions.

**Tech stack:** Go 1.26, GORM, `golang.org/x/crypto/ssh`, existing
`github.com/pkg/sftp`, existing `sshutil.CommandRunner`, SQLite/PostgreSQL
behavior gates, Go race detector.

---

## A2c file map and authorization boundary

A2c1 may modify only:

```text
backend/internal/backupasset/recovery/preflight.go
backend/internal/backupasset/recovery/preflight_test.go
backend/internal/backupasset/recovery/target.go
backend/internal/backupasset/recovery/target_test.go
```

All four are existing members of the 145-path manifest. No new file is created.
`sshutil.CommandRunner` and pkg/sftp are reused without modifying sshutil or
`go.mod`. A2c2 remains planned but unauthorized for implementation until A2c1
focused closure and separate approval.

### Task 7-R26/G26: Split evaluator evidence and seal exact draft-plan authority

- [x] **Step 1: write the R26 failing authority selector in `target_test.go`.**

  Add `TestRecoverySFTPTargetPreflightRequiresExactObservedDraftPlan`. Build a
  valid draft plan from the existing recovery fixture, then assert the current
  structurally constructed `TargetPreflightPermit` without the private proof
  cannot be accepted by concrete `ProbeRoot`; the GREEN removes the structural
  `NewTargetPreflightPermit` constructor. Freeze table rows that substitute plan
  state, plan ID/binding, transition revision, target mode, node/credential/
  root/filesystem/target revisions, private root/relative locator, request
  source/capability/policy, required bytes/inodes and expiry. Every row requires
  zero resolver/dial/SFTP/command calls.

- [x] **Step 2: write the R26 failing evidence-ownership selector in `preflight_test.go`.**

  Add `TestTargetPreflightEvaluatorRequiresIndependentExternalEvidence`.
  Require target `ProbeRoot` to return only `TargetRootProbeFacts`; require the
  evaluator constructor to reject a nil external port and returned external
  evidence without an exact private request/result proof. Prove source,
  capability, policy, finding, overlap and reserve fields cannot be supplied by
  target facts. The valid path uses a deterministic same-package `_test.go`
  issuer/fake; no production evidence issuer exists in A2c1.

- [x] **Step 3: run both R26 selectors and preserve the genuine RED.**

  ```bash
  cd backend
  go test ./internal/backupasset/recovery \
    -run '^(TestRecoverySFTPTargetPreflightRequiresExactObservedDraftPlan|TestTargetPreflightEvaluatorRequiresIndependentExternalEvidence)$' \
    -count=1
  ```

  Expected RED: missing `recoveryTargetPreflightSessionBinding`, sealed target
  permit, `TargetRootProbeFacts`, external-evidence contract/private proof or
  two-port evaluator. A fixture/compile failure unrelated to those contracts is
  not credit.

- [x] **Step 4: implement the minimal draft binding and proof in `target.go`.**

  Add the exact types and domains from design 41.2, private constructor
  `newRecoveryTargetPreflightSessionBinding`,
  `issueTargetPreflightPermit`, `targetPreflightRequestDigest` and
  `TargetPreflightPermit.ValidateRequestAt`. Remove the structural
  `NewTargetPreflightPermit`; only the private issuer may construct a usable
  wrapper. Add unexported `TargetPreflightInput.targetPermit` without changing
  the caller's structural `Permit` field. Do not loosen executed-plan binding
  validation.

- [x] **Step 5: split target and external evidence in `preflight.go`.**

  Add the exact request/evidence/private-proof/interface contracts and digest
  domains from design 41.4, including
  `preflightExternalEvidenceRequestDigest`,
  `preflightExternalEvidenceProofDigest` and
  `PreflightExternalEvidence.ValidateFor`. Change
  `TargetObservationPort.ProbeRoot` to return
  `TargetRootProbeFacts`; change `TargetPreflightEvaluator` and
  `NewTargetPreflightEvaluator` to require both ports. Validate both evidence
  products, derive the external request's plan fields only from
  `input.targetPermit.proof.sessionBinding`, compute later observed/earlier
  expiry, and pass the two typed facts to the unchanged reason/snapshot
  semantics. Define deterministic
  `issuePreflightExternalEvidenceForTest` and its fake in `preflight_test.go`
  only. Do not define a production issuer, Provider/Repository adapter,
  compatibility echo or success defaults.

- [x] **Step 6: seal the observed plan in `PreflightService`.**

  After `validatePreflightPlanInput(observedPlan, ...)` and before evaluator
  I/O, derive the draft binding, issue the permit over the canonicalized exact
  request and set only `input.targetPermit` in the canonical local copy. The
  evaluator rejects a missing sealed permit or any mismatch with the public
  `input.Permit`/`input.ProbeRequest`; the caller cannot submit a proof. Keep the
  later lock/revalidate transaction unchanged.

- [x] **Step 7: rerun R26 to GREEN and the preflight service set.**

  ```bash
  cd backend
  go test ./internal/backupasset/recovery \
    -run '^(TestRecoverySFTPTargetPreflightRequiresExactObservedDraftPlan|TestTargetPreflightEvaluatorRequiresIndependentExternalEvidence|TestPreflightService.*|TestTargetPreflight.*)$' \
    -count=1
  ```

  Expected: PASS with target I/O still outside the transaction and every
  existing service state/CAS test unchanged.

### Task 7-R27/G27: Open only the purpose-exact preflight session

- [x] **Step 1: write `TestRecoveryTargetSessionFactoryOpensPurposeExactPreflight`.**

  Assert resolver purpose `TargetPurposePreflight`, dial purpose literal
  `recovery_preflight`, safe plan-ID correlation, exact node/credential
  revisions, SFTP then SSH close once, cancellation join and zero acceptance by
  the executed `Open` method. Wrong state/binding/purpose/revision must stop
  before dial.

- [x] **Step 2: run R27 to the missing-capability RED.**

  ```bash
  cd backend
  go test ./internal/backupasset/recovery \
    -run '^TestRecoveryTargetSessionFactoryOpensPurposeExactPreflight$' -count=1
  ```

  Expected RED: `OpenPreflight` does not exist and the current general opener
  rejects preflight.

- [x] **Step 3: add the private `OpenPreflight` path.**

  Share only the resolved SSH/SFTP lifecycle after each public-private entry
  validates its own binding. Keep executed `Open` limited to write/verify/
  cleanup. Attach an injected fixed-command probe to the target instance so
  tests never require a synthetic `*ssh.Client`.

- [x] **Step 4: rerun R27 and session cancellation regressions.**

  ```bash
  cd backend
  go test ./internal/backupasset/recovery \
    -run '^(TestRecoveryTargetSessionFactoryOpensPurposeExactPreflight|TestRecoverySFTPTargetSessionCancellationClosesAndJoins|TestRecoveryTargetSessionFactoryOpensPurposeExactVerify)$' \
    -count=1
  ```

  Expected: PASS; each acquired resource closes at most once.

### Task 7-R28/G28: Freeze root identity, principal and capacity observation

- [x] **Step 1: write `TestRecoverySFTPTargetProbeRootCanonicalIdentityAndCapacity`.**

  Extend the local SFTP fake with `StatVFS` and an injected UID/GID command
  result. Cover canonical directory, root/parent aliases and symlinks,
  non-directory root, world-writable mode, owner UID, effective group, root
  principal, no write/execute bits, zero filesystem ID, different parent
  filesystem, unavailable StatVFS, `Bavail*Frsize` overflow, ordinary and zero
  capacity. Assert `Mkdir`, `Chmod`, `OpenFile`, `Rename` and `Remove` stay zero.

- [x] **Step 2: run R28 to the closed `ProbeRoot` RED.**

  ```bash
  cd backend
  go test ./internal/backupasset/recovery \
    -run '^TestRecoverySFTPTargetProbeRootCanonicalIdentityAndCapacity$' -count=1
  ```

  Expected RED: the method returns `ErrRecoveryTargetUnavailable` without an
  observation.

- [x] **Step 3: implement `TargetRootProbeFacts` and root probing.**

  Add `StatVFS(string) (*sftp.StatVFS, error)` to the private SFTP interface and
  wrapper. Parse exactly one bounded decimal UID line and one bounded unique
  GID list from the injected production `sshutil.CommandRunner` calls. Walk and
  rewalk root prefixes with `Lstat`/`RealPath`; compute owner/mode and overflow-
  safe capacity facts. Do not add a generic command method to `TargetPort`.

- [x] **Step 4: run R28 to GREEN.**

  Use the unchanged R28 command. Expected: PASS with target-owned facts only.

### Task 7-R29/G29: Freeze target existence and three stable revisions

- [x] **Step 1: write `TestRecoverySFTPTargetProbeRootTargetMatrixAndRevisions`.**

  Cover target absent at final and intermediate component, existing regular/
  directory/symlink/special, prefix alias/symlink/non-directory, pre/post root
  replacement, target replacement and filesystem drift. Freeze exact
  `sftpr1:`, `sftpf1:` and `sftpt1:` formulas, length, stability and difference
  across node/root/path/kind/size/mode/UID/GID/mtime/filesystem inputs. Prove
  free-space changes do not alter root/filesystem identity.

- [x] **Step 2: run R29 to RED.**

  ```bash
  cd backend
  go test ./internal/backupasset/recovery \
    -run '^TestRecoverySFTPTargetProbeRootTargetMatrixAndRevisions$' -count=1
  ```

  Expected RED: target classification/revision helpers are absent.

- [x] **Step 3: implement bounded target observation and revision helpers.**

  Use the exact design 41.5 domains and field order. Absence is accepted only
  from `os.IsNotExist`; ambiguous errors remain unavailable. Rewalk all visible
  components and require the same revision inputs before return.

- [x] **Step 4: rerun R29 and existing A1/A2a/A2b target selectors.**

  ```bash
  cd backend
  go test ./internal/backupasset/recovery \
    -run '^(TestRecoverySFTPTargetProbeRootTargetMatrixAndRevisions|TestRecoverySFTPTargetCreateAndValidateReturnSameObservation|TestRecoverySFTPTargetVerifyPresentRegularFile|TestRecoverySFTPTargetWriteAtomicAdmitsOnlyExactCreate|TestRecoverySFTPTargetWriteAtomicPreparesModeExactParents|TestRecoverySFTPTargetWriteAtomicNeverOverwritesFinal|TestRecoverySFTPTargetWriteAtomicReturnsExactVerifyRevision)$' \
    -count=1
  ```

  Expected: PASS; earlier target behavior is unchanged.

### Task 7-R30/G30: Close cancellation, privacy and deferred capabilities

- [x] **Step 1: write `TestRecoverySFTPTargetProbeRootCancellationPrivacyAndNoMutation`.**

  Inject resolver/dial/SFTP/StatVFS/id/parse/close failures containing distinct
  private tokens. Cancel during resolver, command, SFTP and close; require
  context identity, joined goroutines, at-most-once command/SFTP/SSH close,
  sanitized errors and zero mutation calls. Marshal permit/facts and scan
  errors plus captured logs for root/path/host/user/credential/UID/GID/raw
  command/stat tokens. Also prove external evidence without a private proof or
  with any request/result substitution fails before reason/snapshot output.

- [x] **Step 2: run R30 to RED, implement only error/lifecycle closure, rerun GREEN.**

  ```bash
  cd backend
  go test ./internal/backupasset/recovery \
    -run '^TestRecoverySFTPTargetProbeRootCancellationPrivacyAndNoMutation$' -count=1
  ```

  The RED must identify a real lifecycle/privacy gap. Minimal GREEN uses the
  existing recovery target error mapper and close ownership; it adds no logging
  or fallback mutation.

### Task 7-V9: A2c1 focused verification and stop

- [x] **Step 1: run the complete A2c1 selector set normal and race.**

  ```bash
  cd backend
  go test ./internal/backupasset/recovery \
    -run '^(TestRecoverySFTPTargetPreflightRequiresExactObservedDraftPlan|TestTargetPreflightEvaluatorRequiresIndependentExternalEvidence|TestRecoveryTargetSessionFactoryOpensPurposeExactPreflight|TestRecoverySFTPTargetProbeRootCanonicalIdentityAndCapacity|TestRecoverySFTPTargetProbeRootTargetMatrixAndRevisions|TestRecoverySFTPTargetProbeRootCancellationPrivacyAndNoMutation)$' \
    -count=1
  go test -race ./internal/backupasset/recovery \
    -run '^(TestRecoverySFTPTargetPreflightRequiresExactObservedDraftPlan|TestTargetPreflightEvaluatorRequiresIndependentExternalEvidence|TestRecoveryTargetSessionFactoryOpensPurposeExactPreflight|TestRecoverySFTPTargetProbeRootCanonicalIdentityAndCapacity|TestRecoverySFTPTargetProbeRootTargetMatrixAndRevisions|TestRecoverySFTPTargetProbeRootCancellationPrivacyAndNoMutation)$' \
    -count=5
  ```

- [x] **Step 2: run whole Recovery, required PostgreSQL and static gates.**

  Reuse the exact V8 whole-package normal/race, required real PostgreSQL
  normal/race, vet, `make lint-backend`, owned gofmt, privacy, Trellis, JSON/
  JSONL, 145-path manifest, protected hashes, branch/HEAD and staged-zero
  commands. PostgreSQL required mode must not skip.

- [x] **Step 3: inline review and record focused closure.**

  Review every A2c1 delta against design 41.1--41.7 and PRD A2c1 acceptance.
  Resolve Critical/Important findings with new genuine RED before product
  changes. Append exact evidence to `research/implementation-evidence.md` and
  set only `meta.task_7.a2c_split_preflight.a2c1_status` to
  `focused_complete_checked`.

- [x] **Step 4: stop.**

  Do not begin A2c2 without separate approval. A2d result read, A2e overwrite/
  Lstat/absence, A3 cleanup/delete, runtime/main, orphan/quarantine and Git
  delivery remain closed.

## A2c2 focused execution (complete checked 2026-08-05)

### Task 7-R31/G31: Implement production external preflight evidence

- [x] Add a genuine failing selector
  `TestRecoveryPreflightExternalEvidenceAdapterIssuesOnlyObservedEvidence`
  before adding the recovery-owned Provider/Repository adapter and production
  issuer behind the A2c1-frozen port. Prove source/capability/policy/finding/
  overlap/reserve substitutions, test-proof reuse and raw locator crossing all
  fail before durable persistence; no target fact may attest external evidence.

### Task 7-R32/G32: Compose source and target evidence outside transactions

- [x] Add `TestPreflightServiceComposesIndependentEvidenceBeforeLock` as RED,
  then use the production external adapter and target port without a
  transaction, intersect expiry and re-lock/revalidate the exact plan/source/
  evidence digests before insert/CAS. No raw locator crosses either port and no
  `_test.go` issuer is available to the production path.

### Task 7-R33/G33: Preserve the complete reason and persistence matrix

- [x] Add `TestTargetPreflightCompositeEvidenceMatrix` as RED for source access,
  capability/policy/finding drift, protected/source overlap, reserve capacity,
  security disposition, target facts and snapshot fields; implement the
  minimal composer and rerun all existing preflight tests.

### Task 7-V10: A2c2 review and stop

- [x] Run focused normal/race, whole Recovery normal/race, required PostgreSQL,
  static/privacy/Trellis/manifest/index gates and an independent inline review.
  Mark only A2c2/A2c focused complete, then stop before A2d.

# Task 7 A2d Resolver-Bound Published Result Read Implementation Plan

> **For inline execution:** use `superpowers:test-driven-development` for every
> product behavior and `trellis-check` after the final product edit. Keep the
> current branch/worktree; do not use a goal, heartbeat or subagent.

**Goal:** Open only the exact published isolated regular-file result through a
resolver-bound private permit and purpose-exact, marker-validating SFTP reader.

**Architecture:** Extend the existing durable resolver product with an
unexported comparable read authority, seal it into the result-read permit, and
let the concrete target perform a no-buffer full verification pass before
returning a second-pass streaming reader that owns file/SFTP/SSH closure.

**Files:**

```text
backend/internal/backupasset/recovery/delivery.go
backend/internal/backupasset/recovery/delivery_test.go
backend/internal/backupasset/recovery/target.go
backend/internal/backupasset/recovery/target_test.go
```

No create path, model, migration, setting, dependency, runtime/main or public
API file is allowed in this batch.

### Task 7-R34/G34: Require resolver-bound result-read authority

- [x] Add `TestTargetResultReadPermitRequiresResolverBoundPublishedProof` before
  production edits. Prove the current structural constructor succeeds as the
  genuine RED, then require the final constructor/Validate methods to reject
  missing proof and every job/result-set/result/publication/fence/marker/
  deadline/session/object/size/digest/request substitution before resolver or
  target I/O. Marshal permits/resolved products and scan private values.
- [x] Add the unexported comparable authority to `ResolvedRecoveryResult`,
  construct it only after all durable resolver checks plus exact executed-plan
  session binding, compare it during revalidation, and add the domain-separated
  private issuer/validator in `target.go`.
- [x] Update `RecoveryResultDeliveryAdapter.OpenRecoveryResultSource` to seal
  the exact request before calling `NewTargetResultReadPermit`; keep stat-only
  zero-session and Range closed. Update the existing target fake to validate
  the complete request proof.
- [x] Run R34 and all delivery/resolver selectors normal and `-race -count=5`.

### Task 7-R35/G35: Open only the result-read SSH/SFTP purpose

- [x] Add `TestRecoveryTargetSessionFactoryOpensPurposeExactResultRead` as RED.
  Require exact resolver purpose and literal SSH purpose, safe job correlation,
  node/credential revision parity, context identity and SFTP then SSH close.
  Wrong/missing/cross-purpose authority must dial zero times.
- [x] Admit the result-read purpose only through the validated concrete method;
  share the existing resolved-session resource lifecycle without weakening
  write/verify/cleanup or preflight entry checks.
- [x] Run R35 with the factory, closed-method and cancellation selectors normal
  and `-race -count=5`.

### Task 7-R36/G36: Validate marker and exact content before delivery

- [x] Add `TestRecoverySFTPTargetOpenOwnedResultValidatesMarkerAndExactContent`
  as RED using the local SFTP fixture. Cover an ordinary file and zero bytes;
  require exact first-pass byte/digest/EOF verification, no whole-file buffer,
  a second read-only handle, unchanged snapshots and zero mutation calls.
- [x] Implement the exact workspace-marker helper and two-pass open. Reuse
  canonical root/parent/file and regular-file read helpers; do not add an
  arbitrary path method or generic command surface.
- [x] Read the returned source completely and close it. Require literal payload,
  exact provider bytes at the adapter, and file -> SFTP -> SSH close once.
- [x] Run R36 plus marker, Verify, create and write regressions normal and
  `-race -count=5`.

### Task 7-R37/G37: Close drift, cancellation, resource and privacy matrices

- [x] Add `TestRecoverySFTPTargetOpenOwnedResultDriftAndResourceMatrix` as RED.
  Cover marker tamper; root/parent/final alias, symlink and type drift; short,
  extra and digest-mismatched content; first/second open, read, stat, EOF and
  file/SFTP/SSH close errors; pre/post-open and mid-read cancellation; partial
  consumer close; and permit expiry. Require exact sanitized sentinel/context
  identity and at-most-once resource closure.
- [x] Implement only lifecycle/error normalization needed by the matrix. A
  fully read or zero-byte reader must postvalidate marker/path/snapshot/digest;
  partial close must release resources without claiming verified completion.
- [x] Scan errors, JSON, formatted products, audit inputs and captured logs for
  every recognizable host/user/credential/root/object/marker/content/digest/
  raw dependency token. Prove all mutation counters remain zero.
- [x] Run R34--R37 together normal and `-race -count=5`, then all A1--A2d target
  and delivery selectors.

### Task 7-V11: A2d focused review and stop

- [x] Run whole Recovery normal/race, required-real PostgreSQL normal/race with
  no skip, `go vet ./internal/backupasset/recovery`, `make lint-backend`, owned
  gofmt, `git diff --check`, static/private-data scans, Trellis task validation,
  JSON/JSONL parsing, exact 145-path manifest, protected hashes, branch/HEAD,
  staged-zero and no-`000070+` gates.
- [x] Review every A2d delta against PRD A2d acceptance and design 42.1--42.5.
  Resolve Critical/Important findings with a new genuine RED before product
  changes. Append exact RED/GREEN and V11 evidence to
  `research/implementation-evidence.md`.
- [x] Check only A2d acceptance and set only
  `meta.task_7.a2d_resolver_bound_published_result_read.status` to
  `focused_complete_checked`. Task 7 and Child 13 remain `in_progress`, parent
  remains `planning`, and program delivery remains 12/15.
- [x] Stop before A2e overwrite/Lstat/absence, A3 destructive cleanup/delete/
  tombstone, runtime/main, orphan/quarantine and every stage/commit/push/PR/CI/
  merge action.

# Task 7 A2e1 Delete-Oriented Lstat And Exact Absence Implementation Plan

> **Approval boundary:** this section records the reviewed implementation
> sequence only. Do not write product code until the user approves the PRD,
> design 43 and this plan. Execute inline with genuine RED first for every
> behavior, then use `trellis-check`; do not use a goal, heartbeat or subagent.

**Goal:** Return payload-bound identity for an exact present delete target, or
matching exact-absence evidence, through the existing sealed verify authority
without performing any remote mutation.

**Architecture:** Reuse the sealed `TargetVerifyPermit` and purpose-exact
`recovery_verify` session. Add one private canonical two-observation helper in
`target.go`; it produces the unchanged A2c `sftpt1:` revision and, only for a
present entry, a new domain-separated identity digest. Absent `Verify` consumes
the same helper so its evidence cannot drift from `Lstat` semantics.

**Files:**

```text
backend/internal/backupasset/recovery/target.go
backend/internal/backupasset/recovery/target_test.go
```

Only private `ReadLink(string) (string, error)` may be added to the existing
SFTP facade. V12's genuine authority-shape and resealing REDs proved the stated
file boundary insufficient, so design 43 now permits the minimal `worker.go`
issuer revalidation plus focused worker/executor test assertions. No public
API, path, model, migration, setting, dependency or manifest row is added.

### Task 7-R38/G38: Open exact sealed Lstat authority

- [x] Add
  `TestRecoverySFTPTargetLstatRequiresExactSealedDeleteAuthority` before any
  production edit. Preserve the current concrete unavailable result as the
  genuine RED, then prove structural/cross-purpose permits and plan/session/
  job/mode/operation/prior/object/request/expiry substitutions make zero
  resolver, SSH and SFTP calls.
- [x] Admit only the existing sealed `TargetVerifyPermit` through the exact
  `recovery_verify` session. Reuse current resolver, node/credential revision,
  correlation, cancellation and SFTP-then-SSH ownership rules without
  weakening present Verify or any other target method.
- [x] Keep valid overwrite, `CreateDirectory`, `Delete`,
  `RemoveOwnedJobDir`, ProbeRoot and result-read cross-purpose attempts closed.
  Run R38 with existing permit/session/closed-method selectors normal and
  `-race -count=5`.

### Task 7-R39/G39: Produce exact present identity for every entry kind

- [x] Add `TestRecoverySFTPTargetLstatPresentIdentityMatrix` as RED with
  ordinary and zero-byte regular files, directory, symlink and special entry.
  Freeze `xirang/recovery/sftp-delete-entry-identity/v1`, length framing, exact
  field order and the unchanged A2c `sftpt1:` formula with independent expected
  digest builders in the test.
- [x] Implement the minimal private canonical observer. For regular entries,
  stream bounded content to SHA-256 with exact EOF and stable handle/path
  metadata; for symlinks, add only private `ReadLink` and bind exact returned
  bytes without following; directory/special bind an empty payload fact.
- [x] Require two equal complete observations before returning. Prove identity
  stability for identical facts, difference for every bound field/payload,
  empty plaintext retention and zero mutation counters. Run R39 plus existing
  A2a Verify, A2b create and A2c target-revision regressions normal and
  `-race -count=5`.

### Task 7-R40/G40: Make missing Lstat and absent Verify identical

- [x] Add `TestRecoverySFTPTargetExactAbsenceObservationParity` as RED. Cover
  two exact not-exist observations, first-missing/second-present,
  first-present/second-missing, permission, unsupported and transport errors.
  Require missing `Lstat.IdentityDigest == ""` and independent expected
  absent `sftpt1:` revision.
- [x] Route sealed `Verify(ExpectedPresent=false)` through the same observer.
  Exact missing returns `AbsentObservation{Evidence: exact}` with byte-for-byte
  revision parity; present or drift remains closed. Do not change existing
  present regular-file Verify output or error identity.
- [x] Run R40 with executor delete-result validators and all Verify/Lstat
  selectors normal and `-race -count=5`; confirm no executor or worker
  production change was needed.

### Task 7-R41/G41: Close drift, lifecycle and privacy matrices

- [x] Add
  `TestRecoverySFTPTargetLstatAndAbsenceDriftResourcePrivacyMatrix` as RED.
  Cover root/parent/final alias, symlink and type drift; metadata, regular
  content and symlink-target drift; first/second open/read/stat/readlink
  failure; cancellation before and during observation; permit expiry; and
  file/SFTP/SSH close errors.
- [x] Implement only the lifecycle and error normalization required by the
  matrix. Require context identity, exact invalid/changed/unavailable sentinel
  mapping, file -> SFTP -> SSH at-most-once closure and no success after close
  ambiguity.
- [x] Scan errors, JSON, formatted products, audit inputs and captured logs for
  recognizable host/user/credential/root/object/content/link-target/UID/GID/
  digest-input/SFTP/raw dependency tokens. Prove every mutation counter remains
  zero and all deferred methods remain unavailable.
- [x] Run R38--R41 together normal and `-race -count=5`, then all A1--A2e1
  target, executor and worker selectors.

### Task 7-V12: A2e1 focused review and stop

- [x] Run whole Recovery normal/race, required-real PostgreSQL normal/race with
  no skip, `go vet ./internal/backupasset/recovery`, `make lint-backend`, owned
  gofmt, `git diff --check`, static/private-data scans, Trellis task validation,
  JSON/JSONL parsing, exact 145-path manifest, protected hashes, branch/HEAD,
  staged-zero and no-`000070+` gates.
- [x] Review every A2e1 delta against PRD A2e1 acceptance and design
  43.1--43.6. Resolve each Critical/Important finding with a new genuine RED
  before its product change. Append exact RED/GREEN and V12 evidence to
  `research/implementation-evidence.md`.
- [x] Check only A2e1 acceptance and add only its focused-completion metadata.
  Task 7 and Child 13 remain `in_progress`, parent remains `planning`, and
  program delivery remains 12/15.
- [x] Stop before A2e2 overwrite, A3 destructive cleanup/delete/tombstone,
  runtime/main, orphan/quarantine and every stage/commit/push/PR/CI/merge
  action. A2e2 requires separate written concurrency and recovery approval.

## Task 7 A2e2 + A3a Authenticated Overwrite Implementation Plan

> **For agentic workers:** Execute inline in this current Trellis task. Required
> pre-edit skill: `trellis-before-dev`; required post-edit skill:
> `trellis-check`. Do not dispatch implement/check subagents, create another
> worktree, switch branches, use a goal/heartbeat, or run Git delivery actions.
> Steps use checkbox (`- [ ]`) syntax for exact progress tracking.

**Goal:** Implement exact-prior in-place overwrite through authenticated
same-parent no-overwrite sidecars, plus checkpoint-before-finalize recovery of
the successful published marker.

**Architecture:** The locked worker derives one stable HMAC artifact binding
from the immutable item and historical cleanup key, then seals it into private
write proofs. `WriteAtomic` prepares the exact post, captures and validates the
exact prior, publishes without replacement, and retains an authenticated
published marker. The worker persists the existing operation checkpoint before
a fresh proof removes that marker and before job completion.

**Tech Stack:** Go 1.26, GORM, SQLite/PostgreSQL, `pkg/sftp`, existing Recovery
worker/target ports, SHA-256/HMAC, backend TDD.

**File map:**

- Modify `backend/internal/backupasset/recovery/target.go`: private artifact and
  marker contracts, item/finalize proof validation, reentrant SFTP overwrite,
  successful marker finalization and target port method.
- Modify `backend/internal/backupasset/recovery/worker.go`: historical-key
  artifact derivation, locked proof issuers, completed-overwrite reconciliation,
  split checkpoint/failure/completion ordering.
- Modify `backend/internal/backupasset/recovery/executor.go`: ordinary execution
  orchestration around checkpoint, marker finalize, source disposition and
  terminal completion.
- Modify `backend/internal/backupasset/recovery/target_test.go`: authority,
  artifact, marker, remote-state, crash, resource and privacy matrices.
- Modify `backend/internal/backupasset/recovery/worker_test.go`: locked issuer,
  durable checkpoint/finalize/takeover and dual-engine behavior.
- Modify `backend/internal/backupasset/recovery/executor_test.go`: exact call
  ordering, last-item completion, source failure and retry behavior.
- Do not modify models, migrations, state/checkpoint enums, settings, routes,
  dependencies or the exact 145-path manifest.

**Locked private signatures:** Keep the live authority values comparable so the
existing call-time `current != authority` revalidation remains available. Use
strings for exact encoded marker documents rather than slices.

```go
type recoveryOverwriteArtifactBinding struct {
    keyVersion        int
    bindingDigest     string
    token             string
    intentComponent   string
    priorComponent    string
    postComponent     string
    publishedComponent string
    intentDocument    string
    publishedDocument string
}

type targetOverwriteFinalizePermitProof struct {
    sessionBinding      recoveryTargetSessionBinding
    jobID               string
    jobItemID           string
    checkpointID        string
    checkpointAttemptID string
    checkpointAttemptFence uint64
    checkpointNodeFence uint64
    operationDigest     string
    targetMode          TargetMode
    object              TargetObjectRef
    expectedPost        PresentExpectation
    artifacts           recoveryOverwriteArtifactBinding
    sourceFenceDigest   string
    bindingDigest       string
}

type FinalizeTargetOverwriteRequest struct {
    Object TargetObjectRef
}

func (target *recoverySFTPTarget) FinalizeOverwrite(
    context.Context,
    TargetWritePermit,
    FinalizeTargetOverwriteRequest,
) error
```

`TargetWritePermit` gains one mutually exclusive private finalize proof.
`WriteAtomic` accepts only the item proof; `FinalizeOverwrite` accepts only the
checkpoint-derived finalize proof. Issuers clear the other proof so neither
method can reuse the other's capability.

### Task 7-R42/G42: Freeze overwrite artifact authority and private binding

- [x] Add `TestRecoverySFTPTargetWriteAtomicOverwriteRequiresExactArtifactAuthority`
  as a genuine RED. The valid locked overwrite case must currently return exact
  unavailable; substitutions of mode, item ID, operation digest, key version,
  prior digest/bytes, post digest/bytes, object, token or marker binding must
  consume zero entropy and open zero resolver/SSH/SFTP dependencies.
- [x] Add `TestRecoveryOverwriteArtifactBindingUsesHistoricalCleanupKey` as RED.
  Freeze HMAC-SHA-256 domain, length-framed field order, base64url token,
  fixed suffixes, maximum component length, deterministic same-item replay,
  difference across every bound field and absence of raw IDs/locator/digests in
  components and documents.
- [x] Extend the private item write proof to a new domain/version with exact
  `jobItemID`, `operationDigest` and `recoveryOverwriteArtifactBinding`. Derive
  the binding only after locked handoff validation using the item
  `TargetLocatorKeyVersion`; clear key bytes after use. Require literal
  overwrite + expected-present + in-place in the concrete authority helper.
- [x] Keep create on its current proof/behavior and prove isolated overwrite,
  structural permits and cross-purpose proofs remain closed.
- [x] Run:

  ```bash
  cd backend && go test ./internal/backupasset/recovery \
    -run '^(TestRecoverySFTPTargetWriteAtomicOverwriteRequiresExactArtifactAuthority|TestRecoveryOverwriteArtifactBindingUsesHistoricalCleanupKey)$' -count=1
  ```

  Expected: the new selectors pass after an observed compile/behavior RED is
  recorded in `research/implementation-evidence.md`.

### Task 7-R43/G43: Create exact authenticated intent and post artifacts

- [x] Add `TestRecoverySFTPTargetOverwritePreparesAuthenticatedPost` as RED.
  Cover ordinary and zero bytes, exclusive marker/post create, mode 0600,
  bounded short/extra/digest mismatch, mandatory Sync, close/reopen, exact
  document authentication, canonical same-parent paths and exact replay.
- [x] Add collision cases for malformed/wrong-phase/wrong-key/tampered marker,
  pre-existing regular/directory/symlink/special post, parent alias/symlink/type
  drift and permission/unsupported errors. No collision may be removed or
  renamed.
- [x] Implement bounded deterministic intent/published documents and the post
  preparation helper in `target.go`, reusing current exclusive-file,
  regular-file verification and canonical-parent mechanics. Do not add a
  generic artifact-path or marker API.
- [x] Run R42--R43 normal and `-race -count=5`; also run the A2b marker/create
  selectors to prove no-overwrite create is unchanged.

### Task 7-R44/G44: Capture exact prior and restore a mismatched winner

- [x] Add `TestRecoverySFTPTargetOverwriteCapturesExactPriorWithoutReplacement`
  as RED. Require exact prevalidation, one standard `Rename(final, prior)`,
  final absence during captured-prior verification, full bounded prior
  digest/bytes/EOF and stable snapshot, then live permit revalidation.
- [x] Add `TestRecoverySFTPTargetOverwriteRestoresCapturedMismatch` as RED.
  Race final to a different regular file, directory, symlink and special entry
  immediately before capture. Require no publish, no delete, no replace rename,
  same-session no-overwrite restore only after an unambiguous successful capture
  and while final is absent, and restored identity equal to that invocation's
  captured observation.
- [x] Cover external final occupation before restore, captured drift, ambiguous
  capture/restore status, re-entry with a mismatched prior, disappearance and
  permission failures. Re-entry mismatch must never auto-restore. Preserve
  final, prior and all evidence when the tuple cannot be uniquely reconciled.
- [x] Implement the capture observer and restore branch using the A2e1
  non-following entry identity machinery where possible and regular-file exact
  expectation for the accepted prior. Do not infer success from one `Lstat`.
- [x] Run R44 normal and `-race -count=5`, plus A2e1 Lstat/absence selectors.

### Task 7-R45/G45: Publish, acknowledge and replay every crash state

- [x] Add `TestRecoverySFTPTargetOverwritePublishesVerifiedPost` as RED. Require
  standard no-overwrite `Rename(post, final)`, exact post verification,
  exclusive authenticated published marker, safe prior/post/intent removal and
  an A2a-compatible `sftp1:` result. The published marker must remain.
- [x] Add `TestRecoverySFTPTargetOverwriteCrashStateMatrix` as a table RED with
  interruption before/after intent create, post open/write/Sync/close/verify,
  capture, captured read, publish, final verify, published create, owned
  artifact removal and session close. Reinvoke with a fresh target/session and
  require exact resume, restore or closed conflict for every row.
- [x] Add concurrent-final and ambiguous-publish cases. Only the complete
  authenticated tuple may choose a state; final visibility or matching content
  alone is insufficient. Exact acknowledged replay must not consume source
  bytes or perform a second publish.
- [x] Implement one closed tuple classifier and transition driver. Keep every
  mutation immediately preceded by canonical-parent and live-proof validation;
  cap each rename/remove at one per transition.
- [x] Run R42--R45 together normal and `-race -count=5`.

### Task 7-R46/G46: Persist checkpoint before marker finalize

- [x] Add `TestRecoveryOverwriteCheckpointPrecedesPublishedMarkerFinalize` as
  RED using the recording target and DB fixture. The required order is target
  publish -> immutable operation checkpoint/item/target revision -> fresh
  finalize permit -> marker remove -> next item or job completion. Attempt,
  source lease and node lease remain active through marker removal.
- [x] Add private finalize proof fields for job/item/checkpoint/operation,
  current target-chain revision, final post expectation, artifact binding and
  active current attempt/node/source fences. The artifact binding remains
  stable across attempts, while the proof binds the immutable checkpoint's
  historical attempt/fences plus the fresh takeover authority. `FinalizeOverwrite` must open only
  `recovery_write`, validate final + parent + exact artifact absence and exact
  published marker, remove only that marker, and accept exact idempotent marker
  absence when the durable checkpoint proof is valid.
- [x] Split ordinary projection so operation checkpoint/item/target revision is
  durable without terminalizing the job. After successful finalize, continue
  pending work or run the existing completion transaction. Reconcile all
  completed overwrite checkpoints in validated durable history before selecting
  another item and on takeover, including checkpoints written by a predecessor
  attempt.
- [x] For source revalidation drift/failure after remote publish, persist the
  operation checkpoint, finalize the marker, then call the existing
  completed-operation failure projection. Preserve its sanitized outcome and
  lease-release semantics. If takeover starts after checkpoint but before that
  projection, rerun durable source revalidation and never infer `matched` from
  checkpoint presence.
- [x] Add crash cases before/after checkpoint, before/after marker removal and
  before completion; add last-item, multi-overwrite, create/overwrite mix,
  predecessor-checkpoint takeover, post-checkpoint source revalidation takeover
  and repeated finalize. No new row/column/phase is allowed.
- [x] Run focused SQLite normal/race and the required real PostgreSQL selector
  normal/race with no skip. Record the exact commands, durations, role cleanup
  and zero temporary-role residue.

### Task 7-R47/G47: Close authority, drift, resource and privacy matrices

- [x] Add `TestRecoverySFTPTargetOverwriteErrorResourceAndPrivacyMatrix` as
  RED. Cover permit revocation at every mutation boundary, context cancellation
  and deadline, resolver/dial/SFTP/open/read/write/stat/Sync/rename/remove and
  file/SFTP/SSH close errors. Require context identity, sanitized sentinels and
  at-most-once ownership closure.
- [x] Capture errors, formatted results, JSON, audit inputs and logs. Scan for
  recognizable host/user/credential/root/final/artifact/token/marker/content/
  digest-input/SFTP-status/raw-error values; require zero leakage and zero
  direct target logging.
- [x] Run R42--R47 normal and `-race -count=5`, then the broad A1--A2e2 target,
  worker and executor selectors.

### Task 7-V13: A2e2 + A3a focused review and stop

- [x] Self-review PRD A2e2 acceptance, design 44.1--44.7 and every R42--R47
  assertion. Search for direct replacement rename, PosixRename, caller artifact
  paths, mutation without live revalidation, checkpoint-after-cleanup ordering,
  terminal-before-finalize ordering, new schema/state/path drift and raw private
  diagnostics. Fix every Critical/Important finding with a genuine new RED.
- [x] Run whole Recovery normal/race, required-real-PostgreSQL normal/race with
  no skip, `go vet ./internal/backupasset/recovery`, `make lint-backend`, gofmt,
  `git diff --check`, Trellis/JSON/JSONL validation, exact unchanged manifest,
  protected hashes, branch/HEAD/remote and staged-zero checks.
- [x] Update PRD acceptance, design/implementation ledgers, task/parent notes
  and implementation evidence truthfully. A shared `.trellis/spec/` update is
  required only if review finds a reusable shipped convention beyond this
  private unshipped Recovery protocol.
- [x] Stop before full A3 `Delete`, `RemoveOwnedJobDir`, tombstone, terminal
  cleanup-lease release, general orphan/quarantine, runtime/main, whole Task 7
  review and every stage/commit/push/PR/CI/merge action. The separate approval
  required before R42 was received before implementation began.

V13 completed inline on 2026-08-05. The acceptance/design/code review found no
remaining Critical or Important product defect. Its structural gate did expose
one genuine task-manifest RED: the create block contained duplicate
`executor_test.go`, `worker.go` and `worker_test.go` rows, producing
`create=58 total=148 duplicates=3`. The minimal bookkeeping correction removed
only those duplicate rows and restored the frozen
`phase1=9 create=55 modify=81 total=145 unique=145 duplicates=0` contract.

Fresh focused normal, focused race count 5, broad `TestRecovery`, whole Recovery
normal/race, required-real PostgreSQL normal/race, vet, backend lint, format,
diff, static/privacy, Trellis/JSON/JSONL, manifest, protected-hash, Git baseline,
remote-head and staged-zero gates passed. A2e2+A3a is focused complete_checked;
Task 7 and Child 13 remain in progress, and execution stops before every full
A3 and Git-delivery boundary listed above.

# Task 7 Full A3 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: use
> `superpowers:executing-plans` to execute this plan inline, task by task. Before
> the first product edit and whenever the package boundary changes, load
> `trellis-before-dev`; after each vertical slice, load `trellis-check`. The
> current Codex inline workflow forbids implement/check subagents. Steps use
> checkbox (`- [ ]`) syntax for exact progress tracking.

**Goal:** Complete exact-mirror deletion, bounded owned-workspace cleanup with
atomic lifecycle terminalization, and read-only logical orphan reconciliation,
then evaluate whole Task 7 through V14.

**Architecture:** A3b introduces a delete-only sealed permit and authenticated
same-parent capture tuple, then makes the worker reconcile that tuple after the
durable delete authority is consumed. A3c keeps database phase and lease
ownership in `ResultLifecycleService`, while a cleanup-only target removes at
most 256 owned descendants per explicit pass and a separate observation proves
the clean tuple before atomic tombstoning. A3d uses a separate read-only port,
purpose-exact SSH session, keyed expected-component tokens and authenticated
prefix cursors; it never receives a mutation capability.

**Tech Stack:** Go 1.26, GORM, SQLite/PostgreSQL, `pkg/sftp`, OpenSSH SFTP v3,
SHA-256/HMAC, existing Recovery keyring/settings/audit contracts, table-driven
Go TDD and race testing.

## Full A3 execution and Git boundary

- Execute in the existing `codex/backup-assets-controlled-recovery` branch and
  current worktree. Do not create or switch a branch/worktree.
- Do not use a goal, periodic heartbeat, polling owner or unbounded same-turn
  retry. One unavailable invocation stops; only an explicit call or expired-
  lease takeover resumes it.
- Preserve every existing dirty path. Edit only files named below and only
  after the user separately approves implementation.
- This plan intentionally has no stage or commit step. Stage, commit, push,
  pull request, CI and merge remain outside A3/V14 and require a later explicit
  Git-delivery decision.
- Stop after A3b, A3c and A3d independently. Record evidence and report the
  exact remaining scope before asking to enter the next slice.

## Full A3 exact file map

All files already belong to the frozen 145-path manifest. No manifest amendment
or new product path is authorized.

**A3b exact-mirror Delete**

- Modify `backend/internal/backupasset/recovery/target.go`: delete permit,
  artifact codec, tuple classifier, capture/verify/delete transition driver and
  concrete `Delete`.
- Modify `backend/internal/backupasset/recovery/target_test.go`: delete
  authority, artifact, object-kind, crash, resource and privacy matrices.
- Modify `backend/internal/backupasset/recovery/worker.go`: durable consumed-
  authority/artifact loading and locked delete issuance.
- Modify `backend/internal/backupasset/recovery/worker_test.go`: issuance,
  takeover, CAS and error-disposition tests.
- Modify `backend/internal/backupasset/recovery/executor.go`: target call order,
  clean-tuple adoption and retryable-versus-contradictory projection.
- Modify `backend/internal/backupasset/recovery/executor_test.go`: full delete
  execution/crash/takeover matrix on SQLite and required real PostgreSQL.

**A3c owned workspace cleanup**

- Modify `backend/internal/backupasset/recovery/target.go`: cleanup live
  validator, captured-directory artifact, bounded directory traversal and
  clean-tuple observation.
- Modify `backend/internal/backupasset/recovery/target_test.go`: capture,
  filesystem, 256-remove, crash, error and privacy tests.
- Modify `backend/internal/backupasset/recovery/result_lifecycle.go`: one-pass
  published/unpublished cleanup advancement, closing projection, durable
  `deleted`, clean-tuple reconciliation and atomic terminal transaction.
- Modify `backend/internal/backupasset/recovery/result_lifecycle_test.go`:
  published/unpublished parity, lost-owner, takeover and dual-engine atomicity.
- Re-read but do not change `backend/internal/backupasset/recovery/state.go` or
  paired `000069` migrations unless a genuine implementation RED proves the
  already frozen state contract is internally inconsistent. Any such finding
  returns to design review before a schema edit.

**A3d logical reconciliation**

- Modify `backend/internal/backupasset/recovery/contracts.go`: bounded public
  reconciliation products only; no locator or mutation data.
- Modify `backend/internal/backupasset/recovery/contracts_test.go`: closed
  categories, validation, JSON and redaction.
- Modify `backend/internal/backupasset/recovery/service.go`: expected-set
  builder, read-only orchestration, audit/alert and downgrade-readiness method.
- Modify `backend/internal/backupasset/recovery/service_test.go`: database
  snapshot, bounds, audit/alert, freshness and dual-engine tests.
- Modify `backend/internal/backupasset/recovery/target.go`: separate read-only
  permit/port, reconciliation session binding, direct-child scan, keyed token
  matching and authenticated prefix cursor.
- Modify `backend/internal/backupasset/recovery/target_test.go`: purpose,
  classification, cursor, dependency, zero-mutation and privacy matrices.
- Modify `backend/internal/settings/service.go` and
  `backend/internal/settings/service_test.go`: bounded all-roots safe-summary
  listing used only by downgrade readiness; no new setting key or value.
- Modify `backend/internal/sshutil/scope.go`,
  `backend/internal/sshutil/scope_test.go`,
  `backend/internal/sshutil/node_dialer.go` and
  `backend/internal/sshutil/node_dialer_test.go`: exact
  `recovery_reconcile` purpose/action mapping and cross-purpose rejection.

**Evidence/bookkeeping after each slice**

- Modify
  `.trellis/tasks/07-28-backup-assets-controlled-recovery/research/implementation-evidence.md`.
- Modify this `implement.md`, `prd.md`, `design.md` and `task.json` only to
  record verified truth. Update the parent task only when the child delivery
  count actually changes.

## Frozen bounds and type ownership

Use these constants exactly. They are private implementation bounds, not
runtime settings:

```go
const (
    recoveryCleanupRemoveLimit          = 256
    recoveryCleanupReadBatch            = 64
    recoveryCleanupMaxDepth             = 64
    recoveryCleanupMarkerMaxBytes       = 4 << 10
    recoveryReconciliationRootLimit     = 1024
    recoveryReconciliationExpectedLimit = 4096
    recoveryReconciliationPageLimit     = 256
    recoveryReconciliationChainLimit    = 4096
    recoveryReconciliationFindingLimit  = 256
    recoveryReconciliationCursorMax     = 2048
)
```

`recoveryCleanupRemoveLimit` counts only successful remote remove mutations;
leaf removal, captured-directory removal and external verified-marker removal
all consume that same 256 budget. Capture rename and authenticated marker
creation are separately bounded one-shot transitions. Every remote mutation
still requires an immediate live-fence check. Directory enumeration uses
`ReadDir(n)` on an owned directory handle; it never calls the unbounded
`sftp.Client.ReadDir(path)` helper.

## A3b: exact-mirror Delete

### A3b frozen private contracts

Add a delete-only capability rather than teaching `TargetWritePermit` to accept
another cross-purpose shape:

```go
type recoveryDeleteArtifactBinding struct {
    keyVersion       int
    bindingDigest    string
    token            string
    intentComponent  string
    capturedComponent string
    verifiedComponent string
    intentDocument   string
    verifiedDocument string
}

type targetDeletePermitProof struct {
    sessionBinding       recoveryTargetSessionBinding
    jobID                string
    jobItemID            string
    operationDigest      string
    consumedCheckpointID string
    consumedGrantID      string
    consumedGrantDigest  string
    targetMode           TargetMode
    object               TargetObjectRef
    expectedPrior        ExpectedTargetIdentity
    expectedPriorBytes   int64
    artifacts            recoveryDeleteArtifactBinding
    bindingDigest        string
}

type TargetDeletePermit struct {
    permit TargetMutationPermit
    proof  *targetDeletePermitProof
}

type TargetDeleteRequest struct {
    Object TargetObjectRef
}

func (TargetDeletePermit) String() string   { return redactedRecoveryTargetProduct("TargetDeletePermit") }
func (TargetDeletePermit) GoString() string { return redactedRecoveryTargetProduct("TargetDeletePermit") }
func (TargetDeleteRequest) String() string  { return redactedRecoveryTargetProduct("TargetDeleteRequest") }
func (TargetDeleteRequest) GoString() string {
    return redactedRecoveryTargetProduct("TargetDeleteRequest")
}
```

Change only the closed target method signature:

```go
Delete(context.Context, TargetDeletePermit, TargetDeleteRequest) (TargetWriteResult, error)
```

The successful result remains the existing zero-byte delete shape:

```go
TargetWriteResult{
    BytesWritten:   0,
    IdentityDigest: "",
    TargetRevision: exactAbsentRevision,
}
```

Artifact components are target-derived exactly as follows, with the 43-byte
unpadded base64url HMAC token as the only variable component:

```text
.xirang-recovery-delete-<token>.intent
.xirang-recovery-delete-<token>.captured
.xirang-recovery-delete-<token>.verified
```

Use domain-separated length-framed HMAC-SHA-256 domains
`xirang/recovery/delete-artifact/v1`,
`xirang/recovery/delete-intent/v1` and
`xirang/recovery/delete-verified/v1`. The binding includes immutable plan/job/
item/operation, consumed checkpoint and grant facts, root/object, exact prior,
the literal durable delete prior-byte sentinel `-1` and historical cleanup-key
version. The exact prior digest is the complete delete-oriented entry identity,
not a regular-file content digest. It never includes the current attempt; the
outer mutation permit binds the current attempt/node/source fences.

### Task 7-R48/G48: Freeze delete-only authority and stable artifact binding

- [x] **Step 1: add the genuine authority RED.** Add
  `TestRecoverySFTPTargetDeleteRequiresConsumedExactAuthority` to
  `target_test.go`. A valid consumed-delete fixture must currently return exact
  unavailable. Mutate job/item/operation/checkpoint/grant/mode/object/prior/
  key-version fields one at a time and require zero resolver, SSH and SFTP
  calls. Pass create/overwrite/finalize/cleanup permits and require the same
  zero-dependency rejection.
- [x] **Step 2: run only the authority RED.** Run:

  ```bash
  cd backend && go test ./internal/backupasset/recovery \
    -run '^TestRecoverySFTPTargetDeleteRequiresConsumedExactAuthority$' -count=1
  ```

  Expected: FAIL because `TargetDeletePermit` and the concrete delete arm do
  not exist. Record the exact compiler or assertion output before product edit.
- [x] **Step 3: add the independent artifact-vector RED.** Add
  `TestRecoveryDeleteArtifactBindingUsesHistoricalCleanupKey`. Independently
  compute the HMAC input framing and exact components/documents; prove replay
  stability, per-field difference, component-length bounds and absence of raw
  ID/locator/digest material.
- [x] **Step 4: implement only the types, issuer and codec.** Add the frozen
  types above, `issueTargetDeletePermit`, `targetDeletePermitProofDigest`,
  `deriveRecoveryDeleteArtifactBinding`, and strict deterministic marker
  encode/verify helpers. Do not open a session or mutate remote state in this
  step.
- [x] **Step 5: run R48 normal and race.** Run the two R48 selectors once, then
  `-race -count=5`. Expected: PASS; all pre-session substitution cases keep
  dependency counters at zero.

### Task 7-R49/G49: Freeze the authenticated tuple classifier

- [x] **Step 1: write `TestRecoveryDeleteTupleClassifier` as RED.** Enumerate
  fresh, intent, captured, verified, deleted-with-markers and exact-clean rows.
  Add every malformed/forged document, artifact-kind mismatch, external final
  winner, collision and impossible combination. The classifier must return one
  closed state and an allowed next transition without mutation.
- [x] **Step 2: run the classifier selector.** Expected: FAIL because no delete
  tuple classifier exists.
- [x] **Step 3: implement one pure classifier.** Use these exact states:

  ```go
  type recoveryDeleteTupleState uint8

  const (
      recoveryDeleteTupleFresh recoveryDeleteTupleState = iota + 1
      recoveryDeleteTupleIntent
      recoveryDeleteTupleCaptured
      recoveryDeleteTupleVerified
      recoveryDeleteTupleDeleted
      recoveryDeleteTupleClean
      recoveryDeleteTupleConflict
  )
  ```

  It consumes complete observations for final, intent, captured and verified;
  it never performs `Lstat`, read, rename or remove itself.
- [x] **Step 4: prove exact marker reuse and collision preservation.** Exact
  authenticated markers are reusable. Wrong type, bytes, key version, phase or
  binding selects conflict; no helper removes or overwrites a collision.
- [x] **Step 5: run R48--R49 normal and race count 5.** Expected: PASS with
  mutation counters still zero for the pure classifier suite.

### Task 7-R50/G50: Capture and verify the mutation-instant object

- [x] **Step 1: add `TestRecoverySFTPTargetDeleteCapturesMutationInstantObject`
  as RED.** Require exact final prevalidation, exact intent create, live permit
  revalidation and one standard no-overwrite
  `Rename(final, captured)`. Race a different regular, directory, symlink and
  special object into final immediately before rename and require the captured
  object, not the pre-observation, to control the result.
- [x] **Step 2: add `TestRecoverySFTPTargetDeleteCapturedIdentityMatrix` as
  RED.** Cover zero/ordinary/bounded-large regular files with stable bytes,
  digest, metadata and EOF; exact symlink `Lstat+ReadLink`; exact empty
  directory; special entry metadata; plus short/extra/content/metadata/link/
  kind drift and non-empty directory.
- [x] **Step 3: run the two selectors and record genuine RED.** Expected: the
  concrete target still returns unavailable or lacks the transition driver.
- [x] **Step 4: implement intent, capture and kind verification only.** Add
  `driveRecoveryDeleteTransitions` and reuse the A2e1 two-observation helpers.
  The driver may create exact intent and capture final; it must stop after exact
  captured verification in this step and must not remove the captured object.
- [x] **Step 5: add same-invocation mismatch restoration.** Only the stack
  frame that just received successful capture may no-overwrite restore its
  observed mismatch while final is exact absent, then verify the restored
  identity. Re-entry with mismatched captured state returns target changed and
  performs zero restore.
- [x] **Step 6: run R48--R50 normal and `-race -count=5`.** Also rerun A2e1
  Lstat/absence and A2e2 overwrite capture selectors. Expected: PASS; overwrite
  artifact domains and behavior remain unchanged.

### Task 7-R51/G51: Delete exact captured leaf and reach the clean tuple

- [x] **Step 1: add `TestRecoverySFTPTargetDeleteRemovesOnlyVerifiedCaptured`
  as RED.** Require authenticated verified-marker creation before removal,
  live validation immediately before remove, `Remove` for regular/symlink/
  special and non-recursive `RemoveDirectory` only for proven-empty directory.
  Non-empty directory and every external or sibling object remain untouched.
- [x] **Step 2: extend the private SFTP facade minimally.** Add exactly:

  ```go
  type recoveryTargetSFTPClient interface {
      // existing methods remain unchanged
      RemoveDirectory(string) error
  }
  ```

  Implement the wrapper with `client.client.RemoveDirectory(value)`. Add fake
  counters before changing the concrete target.
- [x] **Step 3: implement verified delete and ordered artifact cleanup.** After
  removal, prove exact final and captured absence, remove intent, then remove
  verified last. Reconcile the whole tuple after every ambiguous remove. Never
  infer success from final visibility alone.
- [x] **Step 4: add `TestRecoverySFTPTargetDeleteCrashStateMatrix`.** Inject
  before/after intent create, capture, each captured read/stat/readlink,
  verified create, leaf remove, both absence observations, intent remove,
  verified remove and session close. Fresh re-entry must resume, restore only
  with same-invocation evidence, adopt exact clean or preserve conflict.
- [x] **Step 5: implement the concrete `Delete` method and closed port
  signature.** Open only `recovery_write`, return the zero-byte exact-absence
  result only from the clean tuple, normalize context/error/close precedence
  through the existing target helpers and keep direct logging absent.
- [x] **Step 6: run R48--R51 normal and race count 5.** Expected: PASS for all
  four object kinds and crash rows; standard `Rename` and kind-appropriate leaf
  remove are the only mutation primitives.

### Task 7-R52/G52: Issue delete only from durable consumed authority

- [x] **Step 1: add
  `TestRecoveryConsumedDeleteReconcilesTupleBeforeAbsenceAdoption` as RED.** On
  the current product, durable consumed authority plus final absence skips
  `Target.Delete`; require instead one delete tuple reconciliation before
  `Verify`, checkpoint or target-chain projection.
- [x] **Step 2: add locked issuance tests.** In `worker_test.go`, freeze a
  current consumed checkpoint/grant, exact pending item, historical key version
  and current attempt/node/source fences. Substitution, expired/lost owner or a
  required-but-unconsumed checkpoint must issue no permit.
- [x] **Step 3: change consumption to return durable identity.** Replace the
  error-only result with:

  ```go
  type consumedOrdinaryDeleteAuthority struct {
      CheckpointID string
      GrantID      string
      GrantDigest  string
  }

  func (coordinator *WorkerCoordinator) consumeOrdinaryDeleteAuthority(
      context.Context,
      RecoveryWorkerClaim,
      interruptedOperationHandoff,
      string,
      string,
      TargetLstatResult,
  ) (consumedOrdinaryDeleteAuthority, error)
  ```

  Preserve one transaction and existing grant/checkpoint CAS semantics.
- [x] **Step 4: implement locked delete issuance.** Add
  `ordinaryDeletePermit(ctx, claim, handoff, expectedRevision)`; reload and lock
  job/item/checkpoints/grant/use-latch, validate the consumed history, obtain
  the historical cleanup key by the immutable item key version, derive the
  stable artifacts and seal current live fences into `TargetDeletePermit`.
- [x] **Step 5: reorder the executor.** For every consumed delete, call
  `Target.Delete` first, then sealed absent `Verify`, then the existing
  operation checkpoint/item/target-chain transaction. Remove the old
  `adoptAbsence` fast path. A clean tuple may return idempotent target success;
  the worker still performs Verify.
- [x] **Step 6: run focused SQLite normal/race.** Run the new worker/executor
  selectors plus all existing exact-mirror multi-delete and consumed-takeover
  selectors. Expected: PASS; no later item executes before reconciliation.
- [x] **Step 7: run the applicable real-PostgreSQL selectors.** Use the existing
  command-scoped `TEST_POSTGRES_DSN` harness with
  `REQUIRE_POSTGRES_RECOVERY_TEST=1`; run normal and `-race`, require no skip,
  and record zero temporary schema/role residue. Never echo the DSN or secret.

### Task 7-R53/G53: Separate retryable unavailability from contradiction

- [x] **Step 1: add
  `TestRecoveryConsumedDeleteUnavailableDoesNotTerminalize` as genuine RED.**
  Inject resolver/key/SSH/SFTP/read/stat/readlink/rename/remove/close
  ambiguity, cancellation and deadline after consumption. The current broad
  post-pause path must fail because it writes needs-attention; assert zero item,
  checkpoint, target-chain, evidence, attempt-terminal and lease-release
  updates.
- [x] **Step 2: add
  `TestRecoveryConsumedDeleteContradictionTerminalizesCurrentOwner`.** Closed
  target change, forged tuple and external winner must execute one bounded
  detached closing transaction and project the existing
  remote-outcome-unresolved product. Lost-owner CAS updates zero rows and
  releases no successor lease.
- [x] **Step 3: implement the closed disposition enum.** Keep it private:

  ```go
  type ordinaryDeleteDisposition uint8

  const (
      ordinaryDeleteRetryable ordinaryDeleteDisposition = iota + 1
      ordinaryDeleteContradictory
      ordinaryDeleteFenceLost
  )
  ```

  Exact context identity returns directly. `ErrRecoveryTargetUnavailable` is
  retryable, `ErrRecoveryTargetChanged` is contradictory and
  `ErrInvalidTargetPermit` is fence-lost unless a separately proved tuple
  contradiction already exists.
- [x] **Step 4: narrow `projectOrdinaryPostPauseFailure`.** Invoke it only for
  `ordinaryDeleteContradictory`; re-prove the exact current owner/fences in the
  transaction. Do not call `Heartbeat`, start a timer, poll or retry inside the
  failed invocation.
- [x] **Step 5: add explicit re-entry and lease-expiry takeover rows.** A current
  claim may explicitly replay the stable tuple; an expired claim may be adopted
  under fresh current mutation authority while the artifact binding stays
  stable. Old owner calls perform zero mutation.
- [x] **Step 6: run R52--R53 normal/race and required real PostgreSQL
  normal/race.** Expected: PASS with no skip and exact context identities.

### Task 7-R54/G54: Close resource, live-revocation and privacy matrices

- [x] **Step 1: add
  `TestRecoverySFTPTargetDeleteErrorResourceAndPrivacyMatrix` with the RED
  harness gate.** Inject
  dependency and close errors at every remote boundary, including APIs that
  return a non-nil resource with an error. Require file/directory handle,
  SFTP and SSH closure exactly once in ownership order.
- [x] **Step 2: revoke the live permit immediately before intent create,
  capture rename, verified create, mismatch restore, leaf remove, intent remove
  and verified remove.** Require exact invalid permit and zero next mutation;
  already captured evidence remains preserved.
- [x] **Step 3: add formatting and canary scans.** Scan error, zero/result,
  permit, request, `%v/%+v/%#v`, JSON, SSH audit input and captured logs for
  host/user/credential/root/path/name/token/marker/content/link/digest-input/
  SFTP-status/raw-error canaries. Require zero direct target logging.
- [x] **Step 4: implement only normalization, redaction and ownership fixes
  proven by R54.** Do not broaden a capability or add a generic artifact API.

  R54 found no additional production defect: the existing closed target
  normalization and session ownership already satisfy the Delete matrix, so no
  behavior expansion was made.
- [x] **Step 5: run R48--R54 together normal and `-race -count=5`, then broad
  A1--A3b target/worker/executor selectors.** Expected: PASS.

R54 focused completion is recorded for the Delete resource/revocation/privacy
slice only. The next mandatory stop is A3b-V1; this does not close A3b, A3c,
A3d, V14 or Child 13 delivery.

### Task 7-A3b-V1: Focused review and mandatory stop

- [x] Run whole Recovery normal/race, applicable required-real-PostgreSQL
  normal/race no-skip, Recovery vet, backend lint, owned gofmt, whole diff,
  privacy/static, Trellis/JSON/JSONL, exact manifest, protected hashes and Git
  state gates.
- [x] Review PRD full-A3 delete acceptance and design 46.1--46.4/46.9/47.1.
  Resolve each Critical/Important finding with a fresh genuine RED before a
  product correction.
- [x] Append exact RED/GREEN commands, durations, PostgreSQL residue and review
  disposition to `implementation-evidence.md`. Mark only A3b
  `focused_complete_checked` if all gates pass.
- [ ] Stop before any A3c code edit, A3d/runtime work or Git delivery. Report
  changed paths, evidence and remaining scope and obtain explicit approval to
  enter A3c.

A3b-V1 is `focused_complete_checked` after the fresh whole-scope review and
verification gate. The mandatory stop remains before A3c; this does not mark
the final stop bullet complete or claim Child 13/Task 7 closure.

## A3c: bounded owned-workspace cleanup and lifecycle terminalization

### A3c frozen private contracts

Extend the cleanup port without exposing a caller-supplied path or callback:

```go
type targetCleanupLiveValidator func(context.Context, TargetCleanupPermit) error

type targetCleanupPermitProof struct {
    bindingDigest  string
    sessionBinding recoveryTargetSessionBinding
    validateLive   targetCleanupLiveValidator
}

type OwnedJobDirRemoval struct {
    Complete       bool
    RemovedEntries int
    ProgressDigest string
}

type OwnedJobDirRemovalValidation struct {
    Object         TargetObjectRef
    RootRevision   string
    TargetRevision string
}

type RecoveryCleanupProgress struct {
    Phase          CleanupPhase
    Complete       bool
    RemovedEntries int
    ProgressDigest string
}

func (RecoveryCleanupProgress) String() string {
    return redactedRecoveryTargetProduct("RecoveryCleanupProgress")
}
func (RecoveryCleanupProgress) GoString() string {
    return redactedRecoveryTargetProduct("RecoveryCleanupProgress")
}
```

Add the closed operation and port signatures:

```go
const TargetCleanupValidateRemovedJobDir TargetCleanupOperation = "validate_removed_job_dir"

RemoveOwnedJobDir(
    context.Context,
    TargetCleanupPermit,
    RemoveOwnedJobDirRequest,
) (OwnedJobDirRemoval, error)

ValidateOwnedJobDirRemoved(
    context.Context,
    TargetCleanupPermit,
    RemoveOwnedJobDirRequest,
) (OwnedJobDirRemovalValidation, error)

func (service *ResultLifecycleService) AdvanceRecoveryResultCleanup(
    context.Context,
    RecoveryResultCleanupClaim,
) (RecoveryCleanupProgress, error)

func (service *ResultLifecycleService) AdvanceRecoveryWorkspaceCleanup(
    context.Context,
    RecoveryWorkspaceCleanupClaim,
) (RecoveryCleanupProgress, error)
```

Each `Advance` call performs at most one target remove pass. A caller invokes it
again explicitly for further progress. Durable `deleted` calls only
`ValidateOwnedJobDirRemoved`; they can never re-enter recursive mutation.

### Task 7-R55/G55: Enter `delete_started` and seal live validation

- [x] **Step 1: add published/unpublished REDs.** Add
  `TestResultLifecycleAdvanceCleanupTransitionsValidatedToDeleteStarted` for
  both result-set and workspace claims. Require one short transaction to renew
  the exact node lease and phase, then a remove permit whose private proof owns
  a non-nil live validator.
- [x] **Step 2: add `TestTargetCleanupLiveValidatorRunsBeforeEveryMutation` as
  RED.** Use a recording validator/counter; revoke owner, cleanup fence, node
  fence, lease, use latch and phase separately immediately before each planned
  mutation. Require zero following mutation.
- [x] **Step 3: run the selectors and record RED.** Expected: the `Advance`
  methods and concrete remove result do not exist.
- [x] **Step 4: implement the two entry transactions.** Reuse existing locked
  claim loaders and node-lease renewal. Transition only
  `validated -> delete_started`; preserve resource kind, owner, fence, attempt,
  node lease/fence and expiry in the returned internal issuance.
- [x] **Step 5: implement unforgeable live callbacks.** The callback re-queries
  job/result-set/use-latch/node lease under the current context and accepts only
  the exact current owner in `delete_started`. It returns stable conflict/
  invalid errors without formatting raw database errors. The unexported issuer
  installs it; public struct construction cannot.
- [x] **Step 6: run R55 normal/race and required real PostgreSQL normal/race.**
  Expected: PASS; lost-owner and expired-lease cases issue zero usable permit.

### Task 7-R56/G56: Capture and authenticate the owned namespace

- [x] **Step 1: add
  `TestRecoverySFTPTargetRemoveOwnedJobDirCapturesExactWorkspace` as RED.**
  Require canonical `<root>/jobs/<job>`, exact owner marker, one live check,
  standard no-overwrite rename to a deterministic same-parent captured sibling,
  and zero replacement/cross-directory rename.
- [x] **Step 2: add the cleanup artifact matrix.** Freeze domain-separated
  HMAC components/documents under
  `xirang/recovery/owned-cleanup-artifact/v1` and
  `xirang/recovery/owned-cleanup-verified/v1`. Derive them inside the target
  from the historical owner-marker key; callers pass neither component nor key.
- [x] **Step 3: authenticate after capture.** Re-read and validate the original
  owner marker inside captured, then create and verify one external verified
  marker in `jobs` after another live check. Wrong marker, wrong key, external
  final winner, collision, canonical drift or ambiguous capture preserves
  everything and returns target changed/unavailable according to proved state.
- [x] **Step 4: add crash rows before/after capture, owner-marker read and
  external verified creation.** Re-entry accepts only the exact authenticated
  tuple; unknown siblings never become owned.
- [x] **Step 5: implement capture/marker transitions only.** Stop before any
  descendant remove in this step.
- [x] **Step 6: run R55--R56 normal and `-race -count=5`.** Expected: PASS with
  descendant-remove counters zero.

### Task 7-R57/G57: Remove at most 256 no-follow same-filesystem descendants

- [x] **Step 1: extend the private SFTP facade with bounded directory reads.**
  Add exactly:

  ```go
  type recoveryTargetSFTPFile interface {
      Read([]byte) (int, error)
      Write([]byte) (int, error)
      ReadDir(int) ([]os.FileInfo, error)
      Stat() (os.FileInfo, error)
      Sync() error
      Close() error
  }
  ```

  Update the wrapper and fakes; do not add unbounded `Client.ReadDir`.

  Approved R57 amendment (`2026-08-07`): `pkg/sftp` v1.13.10 exposes no
  directory-handle paging API, so the production wrapper opens one dedicated
  SSH `session`/`sftp` subsystem per cleanup SFTP session, shares it across the
  bounded stack's remote directory handles and implements only
  SFTP v3 `INIT`, `OPENDIR`, `READDIR` and `CLOSE`. Accept at most a 256 KiB
  response packet, retain at most 257 non-dot entries from one packet, require
  exact response IDs/status/attribute framing and return only sanitized target
  errors. Close the remote handle and channel deterministically. Do not use
  reflection, `go:linkname`, shell commands, a new dependency or a `go.mod`
  change. Add protocol-level genuine REDs before replacing the temporary
  `ReadDirContext` facade implementation.
- [x] **Step 2: add `TestRecoveryOwnedCleanupDepthFirstNoFollowMatrix` as RED.**
  Cover regular/symlink/special leaves, empty/non-empty directories, nested
  depth, owner marker as leaf, sibling isolation, symlink-to-directory and
  `ReadDir` pagination. Assert exact post-order removes and zero followed link.
- [x] **Step 3: add `TestRecoveryOwnedCleanupFilesystemBoundaryMatrix`.**
  Require canonical containment and equal `StatVFS.Fsid` on captured root and
  every entered/processed directory. Different Fsid, depth >64, canonical
  escape or mount-like drift stops before the next mutation and preserves the
  boundary entry.
- [x] **Step 4: add `TestRecoveryOwnedCleanupRemovesAtMost256Entries`.** A tree
  with more than 256 removable leaves returns `Complete=false`, exactly 256
  successful remove mutations, a bounded redacted progress digest and no
  external-marker removal.
- [x] **Step 5: implement the bounded iterative depth-first walker.** Use an
  explicit private stack capped at 64, `ReadDir(64)`, `Lstat` before leaf
  remove, `Remove` for non-directories and `RemoveDirectory` only for observed
  empty directories. Call `validateLive` immediately before every remove and
  count the captured root itself. After captured-root absence, prove final and
  captured absence; if budget remains, live-validate and remove the external
  verified marker, then prove the clean tuple and return `Complete=true`. If no
  budget remains, return `Complete=false`; the next explicit pass removes the
  marker as its first counted mutation.
- [x] **Step 6: run R57 normal and race count 5.** Expected: PASS; no path/name
  leaves the target result.

### Task 7-R58/G58: Project bounded progress and retryable cleanup failure

- [x] **Step 1: add
  `TestResultLifecycleAdvanceCleanupPersistsIncompleteProgress` as RED.** For
  published and unpublished claims, target `Complete=false` must renew the
  exact owner/node lease, keep `delete_started`, return bounded progress and
  avoid `cleanup_failed`, `cleanup_due`, `deleted` or tombstone.
- [x] **Step 2: add target error/cancellation closing tests.** Under a short
  `context.WithoutCancel` closing transaction, exact current published owner
  becomes `cleanup_failed`; exact current unpublished owner becomes ownerless
  `cleanup_due`; both keep `delete_started` and release only their exact active
  cleanup node lease. Lost owner updates zero rows.
- [x] **Step 3: implement one-pass `Advance` orchestration.** It calls the
  target once. It never loops on `Complete=false`, starts no timer and performs
  no detached renewal. Return progress to the caller after the closing
  transaction.
- [x] **Step 4: add explicit re-entry and expired takeover tests.** Fresh claims
  resume from the authenticated remote tuple and do not trust
  `ProgressDigest` as a cursor. Crashed owners do nothing; expired ownership is
  reclaimed through existing claim logic with a fresh fence.
- [x] **Step 5: run published/unpublished normal/race and required real
  PostgreSQL normal/race no-skip.** Expected: PASS and exact lease ownership.

### Task 7-R59/G59: Persist `deleted`, re-prove clean tuple and tombstone atomically

- [x] **Step 1: add `TestResultLifecycleCompleteCleanupPersistsDeletedFirst` as
  RED.** A target `Complete=true` result must first CAS
  `delete_started -> deleted` while retaining owner, cleanup lease and node
  lease; no state/workspace tombstone occurs in that transaction.
- [x] **Step 2: add the clean-tuple-only target RED.** Add
  `TestRecoverySFTPTargetValidateOwnedJobDirRemovedIsReadOnly`. Require exact
  final workspace, captured sibling and external verified-marker absence under
  `TargetCleanupValidateRemovedJobDir`; all rename/remove counters remain zero.
  Any present/conflicting tuple blocks terminalization.
- [x] **Step 3: implement `ValidateOwnedJobDirRemoved`.** Open only
  `recovery_cleanup`, validate the deleted-phase permit and canonical jobs
  parent, perform bounded exact-absence observations and return a revision. It
  has no mutation branch.
- [x] **Step 4: add SQLite/PostgreSQL atomic terminal tests.** Published path:
  result-set `revoking/deleted -> cleaned/tombstoned`, owner/expiry/node fields
  cleared and exact node lease released while historical job remains published.
  Unpublished path: job `cleanup_due/deleted -> workspace_cleaned/tombstoned`,
  workspace cleanup fields cleared and exact node lease released. Inject before
  every update/release and require full rollback.
- [x] **Step 5: implement the final transactions with exact CAS predicates.**
  Lock job, node lease and result-set when present. Recheck owner/fence/expiry,
  phase and terminal shape. Release via the existing exact lease helper inside
  the same transaction. Rows affected must be exactly one at every owned
  mutation.
- [x] **Step 6: add durable-deleted takeover rows.** A takeover in `deleted`
  invokes only clean-tuple validation, never capture or traversal. Stale owner
  and concurrent successor both yield zero partial terminal updates.
- [x] **Step 7: run R59 SQLite/PostgreSQL normal/race no-skip.** Also run paired
  `000069` workspace/result tombstone tests unchanged. Expected: PASS with zero
  schema/role residue.

### Task 7-R60/G60: Close A3c resource, crash and privacy matrices

- [x] **Step 1: add a complete crash table.** Cover before/after
  `validated -> delete_started`, capture, owner-marker authentication,
  verified-marker creation, every 256-boundary remove, captured-directory
  absence, verified removal, `deleted`, clean-tuple observation and final
  transaction.

The R60 crash table is authoritative for Step 2. A **hard crash** never runs a
detached closing transaction or heartbeat; the durable owner remains until
lease expiry and a fresh-fence takeover replays the permitted remote tuple. A
returned target error or caller cancellation instead attempts the existing
bounded `context.WithoutCancel` closing transaction, but only while the caller
is still the exact current cleanup and node-lease owner. Lost-owner closing is
a zero-row no-op and cannot release a successor lease.

| Crash boundary | Durable DB phase at crash | Allowed remote tuple after crash | Fresh invocation / takeover rule | Returned-error closing rule |
|---|---|---|---|---|
| before `validated -> delete_started` transaction | `validated` | authenticated final workspace; no owned cleanup artifact required | re-enter the transaction, then issue destructive authority only after its commit | no target call occurred; transaction error preserves `validated` and exact ownership |
| after `validated -> delete_started` commit, before target open | `delete_started` | final workspace or a previously authenticated cleanup tuple | resume `RemoveOwnedJobDir`; never repeat the phase transition | current owner may project retryable failure and release only its exact node lease |
| immediately before capture rename | `delete_started` | final present, permit-derived captured and verified artifacts absent | re-observe and reauthenticate the final workspace, then live-validate before rename | final remains present; current-owner closing is retryable |
| immediately after capture rename | `delete_started` | final absent, deterministic captured sibling present, verified marker absent | locate only the permit-derived sibling and reauthenticate its owner marker; never recapture | current-owner closing is retryable; evidence remains in place |
| before/after captured owner-marker read and authentication | `delete_started` | captured sibling present; verified marker absent | reread the fixed marker, compare exact bytes and creator binding, then continue | dependency ambiguity is sanitized; contradictory marker is target-changed; neither mutates descendants |
| immediately before verified-marker create | `delete_started` | authenticated captured sibling present, verified marker absent | live-validate, create only the deterministic external marker with exclusive create | current-owner closing is retryable; captured evidence remains |
| after verified-marker bytes/close, before tuple re-read | `delete_started` | captured sibling and exact verified marker may both be present | authenticate the existing marker and continue; never replace or append it | close ambiguity is sanitized unavailable and cannot claim marker creation succeeded |
| before each leaf or directory remove, for mutation indexes `0..255` | `delete_started` | authenticated captured tree and verified marker present | re-enumerate from the root, recheck canonical/Fsid state and live-validate immediately before that mutation | no mutation occurs after cancellation, stale fence or dependency failure |
| after each successful remove, for mutation indexes `1..256` | `delete_started` | any exact depth-first prefix may be absent; remaining captured tree and verified marker stay authenticated | replay enumeration from the captured root; `ProgressDigest` is never a cursor | hard crash keeps ownership until expiry; a returned error uses current-owner retryable closing |
| exactly at the 256-remove boundary | `delete_started` | 256 authenticated mutations may have completed; captured root or verified marker can remain | return `Complete=false`; next explicit invocation replays remote state with no same-turn loop | normal bounded progress renews ownership and does not enter failure projection |
| before/after captured-root directory remove and absence proof | `delete_started` | before: empty captured root plus verified marker; after: captured root absent plus verified marker | require final and captured absence, then reauthenticate the external marker | ambiguity cannot remove the verified marker or claim completion |
| before/after verified-marker remove and final clean-tuple observation | `delete_started` | before: final/captured absent and verified present; after: all three may be absent | authenticate before remove, live-validate, then prove all three exact absent; cancellation-after-proof reentry may adopt only the bounded all-absent tuple with zero mutation | pre-verification ambiguity is sanitized and retryable; after all three absences are proved, a later session-close ambiguity cannot discard `Complete=true` |
| before `delete_started -> deleted` transaction | `delete_started` | target may already have returned an exact clean tuple | rerun target reconciliation if the DB write did not commit; never infer `deleted` from the prior return | transaction error preserves `delete_started` and exact ownership |
| after `delete_started -> deleted` commit | `deleted` | final workspace, captured sibling and verified marker must be absent | call only `ValidateOwnedJobDirRemoved`; capture and recursive removal are permanently forbidden | target/DB ambiguity preserves durable `deleted` ownership for read-only retry/takeover |
| before/after clean-tuple-only observation | `deleted` | all three cleanup paths absent; target performs no rename/remove | repeat the read-only observation under renewed exact authority | stale owner stops before target access; dependency ambiguity cannot terminalize |
| before final tombstone/projection/node-lease transaction | `deleted` | clean tuple was observed but is not yet authoritative for DB terminal state | re-observe after any retry, then exact-CAS the whole terminal transaction | any owner/fence/phase/binding drift yields zero partial terminal writes |
| after final transaction commit | `tombstoned` plus `cleaned` or `workspace_cleaned` | all cleanup paths absent | terminal state is immutable; no target cleanup authority remains | exact cleanup owner/node fields are clear and the exact node lease is released atomically |

- [x] **Step 2: inject cancellation/dependency/close errors at directory-open,
  `ReadDir`, Lstat, StatVFS, marker read, rename, leaf remove, directory remove,
  target close and every lifecycle transaction.** Require context identity and
  current-owner closing semantics.
- [x] **Step 3: scan all cleanup products and sinks.** Canary-scan errors,
  permits, requests, progress, `%v/%+v/%#v`, JSON and logs for locator, path,
  child name, marker, content, key/token, Fsid, raw error and SFTP status.
  Require zero target logging and at-most-once handles/SFTP/SSH close.
- [x] **Step 4: implement only fixes proved by R60, then run R55--R60 normal
  and `-race -count=5`.** Run broad ResultLifecycle and target cleanup suites.
  Expected: PASS.

R60 Step 4 passed after one migration-fixture correction proved by the paired
`TestBackupAssetMigration069WorkerPreWriteSourceDrift{SQLite,Postgres}` RED:
`firstWrite` fixtures now derive `rootLocatorDigest` with the same canonical
`settings.RecoveryTargetRootLocatorDigest` contract as the production target
session binding. No production recovery behavior was changed by that correction.

### Task 7-A3c-V1: Focused review and mandatory stop

- [x] Run whole Recovery normal/race, all applicable required-real-PostgreSQL
  normal/race no-skip, vet/lint/format/diff/privacy/static, paired migration,
  Trellis/JSON/JSONL, manifest, protected-hash and Git-state gates.
- [x] Review PRD A3c acceptance and design 46.1/46.5/46.6/46.9/47.1. Confirm
  each mutation has an adjacent live check, one call removes at most 256,
  `Complete=false` is normal, durable deleted cannot mutate and tombstone/
  projection/release is atomic.
- [x] Resolve every Critical/Important issue with a new genuine RED, append
  evidence and mark only A3c `focused_complete_checked` when all gates pass.
- [x] Stop before A3d, Task 8/runtime/main and Git delivery. Report exact
  status and obtain explicit approval to enter A3d.

A3c-V1 is `focused_complete_checked`. A3d, Task 8/runtime/main, V14, stage,
commit, push, PR, CI and merge remain outside this stop boundary.

## A3d: read-only logical orphan/quarantine reconciliation

### A3d frozen public and private contracts

Add these bounded public products to `contracts.go`:

```go
type RecoveryReconciliationCategory string

const (
    RecoveryReconciliationKnownHealthy    RecoveryReconciliationCategory = "known_healthy"
    RecoveryReconciliationKnownDrift      RecoveryReconciliationCategory = "known_drift"
    RecoveryReconciliationDBUnmatched     RecoveryReconciliationCategory = "db_unmatched"
    RecoveryReconciliationForgedOrUnknown RecoveryReconciliationCategory = "forged_or_unknown"
    RecoveryReconciliationScanIncomplete  RecoveryReconciliationCategory = "scan_incomplete"
)

type RecoveryReconciliationState string

const (
    RecoveryReconciliationClear   RecoveryReconciliationState = "clear"
    RecoveryReconciliationBlocked RecoveryReconciliationState = "blocked"
)

type RecoveryReconciliationFinding struct {
    Category    RecoveryReconciliationCategory `json:"category"`
    Fingerprint string                         `json:"fingerprint"`
    EntryKind   TargetEntryKind                `json:"entry_kind"`
    JobID       string                         `json:"job_id,omitempty"`
}

type RecoveryReconciliationCounts struct {
    Scanned         int `json:"scanned"`
    KnownHealthy    int `json:"known_healthy"`
    KnownDrift      int `json:"known_drift"`
    DBUnmatched     int `json:"db_unmatched"`
    ForgedOrUnknown int `json:"forged_or_unknown"`
    ScanIncomplete  int `json:"scan_incomplete"`
}

type RecoveryReconciliationResult struct {
    State       RecoveryReconciliationState   `json:"state"`
    Complete    bool                          `json:"complete"`
    NextCursor string                        `json:"next_cursor,omitempty"`
    Counts      RecoveryReconciliationCounts `json:"counts"`
    Findings    []RecoveryReconciliationFinding `json:"findings"`
}

type ReconcileRecoveryRootRequest struct {
    NodeID uint
    RootID string
    Cursor string `json:"-"`
}

type RecoveryDowngradeReconciliationRequest struct {
    AdmissionGeneration string `json:"-"`
}
```

The result has redacted `String`/`GoString`; validation requires exact category
counts, bounded findings, opaque job IDs, fixed-length base64url fingerprints
and an opaque authenticated cursor. It contains no safe label because labels
can still be user-controlled.

Add this locator-free registry product to `settings/service.go`:

```go
type RecoveryTargetRootReference struct {
    NodeID uint   `json:"node_id"`
    RootID string `json:"root_id"`
}

func (service *Service) ListAllRecoveryTargetRoots(
    context.Context,
) ([]RecoveryTargetRootReference, error)
```

Add a separate target capability, not a method on `TargetPort`:

```go
type TargetReconciliationPort interface {
    ScanRecoveryRoot(
        context.Context,
        TargetReconciliationPermit,
        TargetReconciliationRequest,
    ) (TargetReconciliationPage, error)
}

type TargetReconciliationRequest struct {
    RootID string
}

type TargetReconciliationPage struct {
    Complete    bool
    NextCursor string
    Counts      RecoveryReconciliationCounts
    Findings    []RecoveryReconciliationFinding
}

type TargetReconciliationOperation string

const TargetReconciliationScanRoot TargetReconciliationOperation = "scan_root"

type recoveryTargetReconciliationSessionBinding struct {
    nodeID             uint
    nodeRevision       string
    credentialRevision string
    rootID             string
    rootLocator        string
    rootLocatorDigest  string
    rootRevision       string
    bindingDigest      string
}

type TargetReconciliationPermit struct {
    SchemaVersion       int
    Purpose             TargetPurpose
    Operation           TargetReconciliationOperation
    NodeID              uint
    RootID              string
    RootLocatorDigest   string `json:"-"`
    RootRevision        string
    ExpectedSetDigest   string
    PageLimit           int
    ChainLimit          int
    FindingLimit        int
    Cursor              string `json:"-"`
    AdmissionGeneration string `json:"-"`
    ExpiresAt           time.Time
    proof               *targetReconciliationPermitProof
}

type targetReconciliationExpected struct {
    componentToken      string
    jobID               string
    entryKind           TargetEntryKind
    remoteState         string
    markerBindingDigest string
    markerCreatorID     string
    markerCreatorFence  uint64
}

type targetReconciliationPermitProof struct {
    sessionBinding  recoveryTargetReconciliationSessionBinding
    auditKeyVersion int
    auditTokenKey   [32]byte
    expected        []targetReconciliationExpected
    bindingDigest   string
}
```

`TargetReconciliationPermit` carries only public node/root revision, expected-
set digest, hard bounds, opaque cursor, expiry and an unexported proof. The
proof owns the canonical private root/session binding, fixed-size audit-token
key, audit-key version and private expected-component rows. Its formatting and
JSON are fully redacted.

Define service dependencies explicitly in `service.go`:

```go
type RecoveryReconciliationAlert struct {
    NodeID   uint
    RootID   string
    State    RecoveryReconciliationState
    Counts   RecoveryReconciliationCounts
    Findings []RecoveryReconciliationFinding
}

type RecoveryReconciliationFindingSink interface {
    NotifyRecoveryReconciliation(
        context.Context,
        RecoveryReconciliationAlert,
    ) error
}

type RecoveryReconciliationKeySource interface {
    Active(
        context.Context,
        backupasset.KeyDomain,
    ) (backupasset.DomainKeyMaterial, error)
    ByVersion(
        context.Context,
        backupasset.KeyDomain,
        int,
    ) (backupasset.DomainKeyMaterial, error)
}

type RecoveryReconciliationRootRegistry interface {
    ListAllRecoveryTargetRoots(
        context.Context,
    ) ([]settings.RecoveryTargetRootReference, error)
    ResolveRecoveryTargetRootTx(
        context.Context,
        *gorm.DB,
        uint,
        string,
    ) (settings.RecoveryTargetRootResolution, error)
}

type RecoveryReconciliationServiceDependencies struct {
    DB       *gorm.DB
    Now      func() time.Time
    Roots    RecoveryReconciliationRootRegistry
    Keys     RecoveryReconciliationKeySource
    Target   TargetReconciliationPort
    Audit    RecoveryAuthorizationAuditWriter
    Findings RecoveryReconciliationFindingSink
}

type RecoveryReconciliationService struct {
    db       *gorm.DB
    now      func() time.Time
    roots    RecoveryReconciliationRootRegistry
    keys     RecoveryReconciliationKeySource
    target   TargetReconciliationPort
    audit    RecoveryAuthorizationAuditWriter
    findings RecoveryReconciliationFindingSink
}
```

The service signatures are:

```go
func NewRecoveryReconciliationService(
    RecoveryReconciliationServiceDependencies,
) (*RecoveryReconciliationService, error)

func (service *RecoveryReconciliationService) ReconcileRoot(
    context.Context,
    ReconcileRecoveryRootRequest,
) (RecoveryReconciliationResult, error)

func (service *RecoveryReconciliationService) ReconcileDowngradeReadiness(
    context.Context,
    RecoveryDowngradeReconciliationRequest,
) (RecoveryReconciliationResult, error)
```

`ReconcileDowngradeReadiness` lists every registered root internally, binds the
sticky admission generation into each expected-set/cursor chain and returns
clear only if every root obtains a fresh complete zero-finding pass. Task 8
later supplies the real caller/cadence; A3d itself starts no goroutine.

### Task 7-R61/G61: Add the exact read-only SSH purpose and bounded root catalog

- [x] **Step 1: add SSH purpose REDs.** Extend scope/dialer tests with literal
  `recovery_reconcile`; require exact audit-action mapping, known-purpose
  normalization and rejection of write/verify/cleanup/reconcile mismatches
  before network activity.
- [x] **Step 2: run focused sshutil tests and record RED.** Expected: unknown
  purpose or missing action mapping.
- [x] **Step 3: implement only `PurposeRecoveryReconcile` and
  `TargetPurposeReconcile`.** Extend the target session factory with a separate
  root-scoped reconciliation binding and `OpenReconciliation`; do not pass a
  fake Recovery job ID into the existing job-scoped `Open` method.
- [x] **Step 4: add settings all-roots RED.** Add
  `TestSettingsListAllRecoveryTargetRootsIsBoundedAndSafe`. It must return only
  node ID/root ID, reject malformed/duplicate/inactive-node rows, order by
  node/root, and fail closed at 1025 rows rather than truncate 1024.
- [x] **Step 5: implement `ListAllRecoveryTargetRoots`.** Reuse existing decode
  and active-node validation; expose neither safe label nor locator in the new
  reconciliation catalog interface. Keep existing per-node list behavior
  unchanged.
- [x] **Step 6: run sshutil/settings focused normal and race.** Expected: PASS;
  no new setting key/value or runtime wiring.

### Task 7-R62/G62: Build a complete keyed expected set and seal the permit

- [x] **Step 1: add `TestRecoveryReconciliationExpectedSetMatrix` as RED.**
  Populate isolated jobs in reserved/marker-created/writing/sealed/published/
  cleanup-due/workspace-cleaned states, result sets across cleanup phases and
  A3c captured/verified states. Require only non-tombstoned legal remote
  components, exact DB/job/result relationships and one deterministic expected-
  set digest.
- [x] **Step 2: add incomplete/limit rows.** Malformed encrypted locator,
  missing plan/root revision, impossible phase combination, DB query failure or
  4097 expected components returns blocked `scan_incomplete` and opens no
  target session.
- [x] **Step 3: add token privacy vectors.** Use active audit-fingerprint key
  material and domain `xirang/recovery/reconcile-component-token/v1`; HMAC the
  exact remote component plus root/expected-state binding. Target-facing rows
  contain only token, closed expected kind/state, private marker facts and an
  optional already-safe opaque job ID.
- [x] **Step 4: implement expected-set builder in `service.go`.** Query a fresh
  bounded DB snapshot, resolve the registered root inside that snapshot, load
  active audit key material, derive tokens and clear copied key bytes after
  sealing the private permit proof. The issuer deep-clones every expected row;
  no caller-owned slice or map remains reachable from the proof.
- [x] **Step 5: add permit substitution/cross-purpose tests.** Node,
  credential/root revision, expected digest, bound, cursor, expiry and operation
  substitutions consume zero target dependency. Cleanup/write permits cannot
  become reconciliation permits.
- [x] **Step 6: run R62 SQLite normal/race and required real PostgreSQL
  normal/race no-skip.** Expected: byte-identical expected-set digest and no raw
  component in target requests, errors or formatted products.

### Task 7-R63/G63: Classify direct children without mutation

- [x] **Step 1: extend the directory-handle fake for bounded `ReadDir(64)` and
  add `TestRecoverySFTPTargetReconciliationClassificationMatrix` as RED.** Cover
  exact known healthy, known token with kind/marker/phase drift, historical-key
  authenticated DB-unmatched, invalid/unknown grammar, symlink entry and an
  established scan interrupted by read/stat/marker failure.
- [x] **Step 2: require exact scan scope.** The target canonicalizes only
  `<registered-root>/jobs`, lists direct children, never follows a symlink,
  never recurses into an unknown directory and reads only fixed Recovery owner/
  cleanup artifact document names for classification.
- [x] **Step 3: implement raw-name confinement.** Compute keyed component token
  inside `target.go`; compare constant-time against expected rows; discard the
  raw name before building the page. Only an exact expected-token match may
  attach the DB-provided safe job ID.
- [x] **Step 4: implement all five categories.** `known_healthy` increments
  counts only. The other four create bounded findings with audit-key-versioned
  HMAC fingerprint and closed entry kind. Every remote mutation fake counter
  must remain zero.
- [x] **Step 5: distinguish setup failure from incomplete scan.** Resolver,
  key, SSH/SFTP open or required sink setup failure returns sanitized
  reconciliation unavailable. Once an authenticated scan has begun, an
  interrupted observation returns a normal blocked page with
  `scan_incomplete` and never clear.
- [x] **Step 6: run R63 normal and `-race -count=5`.** Expected: PASS; unknown
  names are absent from returned structs and test diagnostics.

R63/G63 is `focused_complete_checked` after the fresh normal/race, whole
Recovery, static/privacy, Trellis/manifest/protected-file and Git-state gates.
R64--R66, A3d-V1, V14, Task 8/runtime/main and every Git-delivery action remain
open; no cursor codec, prefix replay, audit/alert orchestration, downgrade loop,
goroutine, timer, retry or heartbeat was started.

### Task 7-R64/G64: Authenticate prefix continuation and enforce every bound

- [x] **Step 1: add `TestRecoveryReconciliationCursorPrefixReplay` as RED.**
  Freeze domain `xirang/recovery/reconcile-cursor/v1`, schema/audit-key version,
  root and expected-set digest, admission generation, ordinal, prefix digest
  and bounds. Cursor is an authenticated opaque base64url envelope no longer
  than 2048 bytes and contains no remote component.
- [x] **Step 2: bind cursor key rotation explicitly.** The envelope exposes only
  a non-secret schema and audit-key-version header before its authenticated
  body. Fresh scans use `Active(KeyDomainAuditFingerprint)`; resume parses the
  bounded header, obtains only that version through `ByVersion`, and verifies
  the tag before using ordinal or prefix digest. Unknown/lost versions block.
- [x] **Step 3: add replay/drift rows.** Resume must reopen the directory at the
  beginning and reproduce every processed entry into the same prefix digest.
  Order, name, kind, marker, expected set, root revision, key version,
  generation or bound drift returns blocked `scan_incomplete` and scans no
  unverified suffix. Replay also recomputes cumulative counts and the bounded
  finding set for the whole verified prefix; a final page can never forget a
  prior-page finding and return clear.
- [x] **Step 4: add hard-bound rows.** Enforce 256 entries per page, 4096 total
  entries per chain, 256 findings, 4096 expected rows and 1024 roots. Exact
  limit is accepted; limit+1 blocks. A non-EOF page returns `Complete=false`,
  blocked state and an authenticated cursor; only proven EOF may be complete.
- [x] **Step 5: implement cursor codec and prefix replay in `target.go`.** Use
  exact length framing and constant-time tag comparison. Never seek by raw name
  or trust a caller ordinal without replay.
- [x] **Step 6: add JSON/format/canary scans.** Scan page/result/cursor,
  `%v/%+v/%#v`, errors and captured logs for raw name/path, marker, component
  token input, key, credential/root locator, dependency error and SFTP status.
- [x] **Step 7: run R62--R64 normal and race count 5.** Expected: PASS with zero
  rename/remove/Mkdir/Chmod/OpenFile mutations.

R64/G64 is `focused_complete_checked` after the genuine scanner-behavior RED,
the authenticated binary cursor/prefix-replay GREEN, exact hard-bound tests,
R62--R64 normal/race repetition, whole Recovery normal/race, vet, backend lint,
owned format and diff gates. R65--R66, A3d-V1, V14, Task 8/runtime/main and all
Git-delivery actions remain open; no goroutine, timer, retry or heartbeat was
added.

### Task 7-R65/G65: Require aggregate audit, alert and fresh downgrade clear

- [x] **Step 1: add `TestRecoveryReconciliationWritesOneAggregateAudit` as
  RED.** Every pass, including a clear pass, calls the required finding sink
  exactly once and writes exactly one aggregate audit. Use existing
  `AuditActionRecoveryCleanup`,
  `AuditFieldOperation="recovery_reconcile"`, closed status, bounded item count
  and opaque root/job IDs only. Do not add a new audit action. The user-approved
  foundation exception is limited to admitting exactly this operation/action
  pair in `backupasset/audit_action.go`; every other cleanup operation and every
  cross-action reuse remains rejected.
- [x] **Step 2: implement the already frozen required sink contract in
  `service.go`.** Use the exact interface and alert product declared in the A3d
  frozen-contract section; do not add fields. Both products have redacted
  formatting, and sink payloads contain no safe label or dependency text.

- [x] **Step 3: add audit/alert failure precedence tests.** Every pass writes
  exactly one aggregate audit and calls the sink once; a clear pass carries
  zero findings, while a blocked pass carries the bounded sanitized set. Audit
  or required sink failure returns stable unavailable and callers must treat it
  as blocked; no failure can return clear.
- [x] **Step 4: add
  `TestRecoveryReconcileDowngradeReadinessRequiresFreshAllRootsClear`.** Under
  one sticky admission generation, internally list all roots and require a
  fresh expected set, cursor chain, EOF and zero findings for each. Missing,
  duplicate, old-cache, old-generation, incomplete or unavailable root blocks.
  Existing cleanup-backlog and permanent-use-latch blockers remain independent
  inputs and are not cleared here.
- [x] **Step 5: implement explicit orchestration only.** `ReconcileRoot` performs
  one bounded page and sinks its cumulative product.
  `ReconcileDowngradeReadiness` may continue only a valid authenticated
  pagination-only `scan_incomplete` cursor, for at most 16 pages/4096 entries
  per root. It stops on any substantive finding, unavailable dependency,
  invalid/missing cursor, drift or hard-bound blocker, and returns clear only
  after every root reaches EOF with cumulative zero findings. It starts no
  goroutine, timer, retry or heartbeat.
- [x] **Step 6: run service/settings/target focused normal/race and required
  real PostgreSQL normal/race no-skip.** Expected: PASS with fresh clear only.

R65/G65 is `focused_complete_checked` as of 2026-08-09. After explicit user
approval, a genuine foundation RED proved the frozen aggregate pair was rejected
and the minimal GREEN admitted only cleanup plus `recovery_reconcile`. Exact and
combined normal/race, whole Recovery normal/race, full backupasset, vet, backend
lint, owned format and diff gates pass. A one-use isolated PostgreSQL 18 fixture
ran the required R65 normal/race selectors without skip, left zero schema/role
residue and was removed with its anonymous volume. R66 is next; A3d-V1, V14,
Task 8/runtime/main and every Git-delivery action remain open.

### Task 7-R66/G66: Close A3d resource, error and privacy matrices

- [x] **Step 1: add dependency/resource injection at registry, DB, audit key,
  resolver, SSH dial, SFTP open, jobs handle, ReadDir, Lstat, marker read,
  file/handle close, SFTP close, SSH close, audit writer and alert sink.**
  Require exact context identity first, invalid authority second and sanitized
  unavailable otherwise.
- [x] **Step 2: prove at-most-once resource closure.** Cover non-nil resource
  with error and concurrent cancellation plus close noise. No close ambiguity
  may return clear.
- [x] **Step 3: run the complete privacy matrix.** Capture result, cursor,
  alert, audit, metrics-compatible labels and logs. Scan recognizable host,
  user, credential, locator, path/name, token/HMAC input, marker, content,
  digest input, SFTP status and raw error. Require zero direct target logging.
- [x] **Step 4: add a static zero-mutation gate.** The read-only port and its
  helpers must have no reachable Rename, Remove, RemoveDirectory, Mkdir, Chmod
  or OpenFile call. Runtime/main and managed cadence remain untouched.
- [x] **Step 5: implement only fixes proved by R66, then run R61--R66 together
  normal and `-race -count=5`.** Expected: PASS.

R66/G66 is `focused_complete_checked` as of 2026-08-09. The resource matrix
produced a genuine context-precedence RED for a canceled caller context against
a nil receiver and nil clock; `ScanRecoveryRoot` now handles nil context, exact
caller cancellation and invalid target dependencies in that order. The four
frozen R66 matrices, R61--R66 combined normal/race repetition, whole Recovery,
required-real-PostgreSQL behavior, paired migration, sshutil/settings, vet,
backend lint, format, diff, privacy and static zero-mutation gates pass. No
runtime/main wiring, goroutine, timer, retry or heartbeat was added.

### Task 7-A3d-V1: Focused review and mandatory stop

- [x] Run whole Recovery normal/race, applicable required-real-PostgreSQL
  normal/race no-skip, sshutil/settings focused gates, vet/lint/format/diff,
  privacy/static/zero-mutation, paired migration, Trellis/JSON/JSONL, exact
  manifest, protected hashes and Git-state checks.
- [x] Review PRD A3d acceptance and design 46.1/46.7--46.9/47.1. Confirm the
  independent port/purpose, all five categories, prefix replay, hard bounds,
  required sinks, fresh downgrade authority and zero mutation.
- [x] Resolve every Critical/Important issue with a new genuine RED, append
  evidence and mark only A3d `focused_complete_checked` when all gates pass.
- [x] Stop before Task 8 runtime/main, Git delivery and V14 closure judgment.
  Report exact scope and obtain explicit approval to run V14.

A3d-V1 is `focused_complete_checked` as of 2026-08-09. Fresh post-format
R61--R66 combined normal/race and whole Recovery normal/race gates pass. The
required PostgreSQL Recovery and paired migration gates ran in required mode
without skip against a one-use isolated fixture; temporary schema/role,
container and volume residue is zero. Static/privacy/Trellis/manifest/protected-
hash/Git-state checks pass, and the A3d acceptance/design review found no open
Critical or Important issue. Task 7 and Child 13 remain `in_progress`, the
parent remains `planning`, and V14, Task 8/runtime/main and every Git-delivery
action remain open.

## V14: whole Task 7 review and closure decision

### Task 7-V14-1: Rebuild full acceptance traceability

- [x] Map every unchecked Task 7 PRD acceptance row to its design section,
  product symbol and fresh test selector. A prior V1--V13 pass may support
  history but cannot replace a fresh V14 selector.
- [x] Trace the full data flow: plan snapshot -> preflight -> job/worker ->
  target mutation/verification -> ResultSet publication/delivery -> cleanup ->
  logical reconciliation. Confirm Task 8 runtime wiring is not required to call
  the explicit Task 7 products in tests.
- [x] Review all Task 7 modified paths for Critical/Important security,
  authority, crash, race, privacy, resource and cross-engine defects. Every
  product correction begins with a fresh named genuine RED and returns through
  its owning A3 slice gate.

### Task 7-V14-2: Run fresh dynamic gates

- [x] Run focused A3b/A3c/A3d selectors normal and `-race -count=5`.
- [x] Run whole Recovery:

  ```bash
  cd backend && go test ./internal/backupasset/recovery -count=1
  cd backend && go test -race ./internal/backupasset/recovery -count=1
  ```

  Expected: PASS with no skip in ordinary non-PostgreSQL selectors.
- [x] Construct the PostgreSQL DSN only inside the command from the existing
  healthy `xirang-c13-pg` fixture, do not print it, and run:

  ```bash
  REQUIRE_POSTGRES_RECOVERY_TEST=1 TEST_POSTGRES_DSN="$C13_PG_DSN" \
    go test ./internal/backupasset/recovery -run 'Postgres' -count=1
  REQUIRE_POSTGRES_RECOVERY_TEST=1 TEST_POSTGRES_DSN="$C13_PG_DSN" \
    go test -race ./internal/backupasset/recovery -run 'Postgres' -count=1
  ```

  Expected: PASS, every required selector executes with no skip and temporary
  schema/role residue is zero before and after. Do not restart, replace or
  remove the fixture.
- [x] Run the paired 000069 SQLite/PostgreSQL migration matrices required by
  the full manifest with `REQUIRE_POSTGRES_MIGRATION_TEST=1`; require no future
  `000070`/`000071` migration.

### Task 7-V14-3: Run static, privacy and structural gates

- [x] Run:

  ```bash
  cd backend && go vet ./internal/backupasset/recovery
  make lint-backend
  git diff --check
  ```

  Expected: PASS and backend lint reports zero issues.
- [x] Run owned gofmt verification and the exact direct-log, replacement/
  PosixRename, caller-path, read-only-mutation and private-canary scans recorded
  in the evidence ledger. Expected: zero findings.
- [x] Validate Child 13 and parent Trellis contexts, parse task/parent JSON and
  all Task 7 JSONL rows, then run the exact manifest gate. Expected:
  `phase1=9 create=55 modify=81 total=145 unique=145 duplicates=0`, no
  outside-manifest dirty path and staged paths zero.
- [x] Recompute the protected hashes. Expected:

  ```text
  go.mod
  b767fc9ef9376c651d0493329b710ac8dcf8d77e52686b847d244aff9f6d48fd

  recovery/testdata/rsync_local_to_remote.json
  2570bd4472541322d902c4cdf2fe43f247b69f19d335558830e749567f763892
  ```

- [x] Confirm branch `codex/backup-assets-controlled-recovery`, staged paths
  zero and HEAD/main/origin-main still equal the recorded baseline unless a
  separately approved Git operation has explicitly changed it.

### Task 7-V14-4: Record the bounded closure decision and stop

- [x] Append all fresh commands, results, durations, PostgreSQL handling,
  review findings and exact Git/manifest state to
  `research/implementation-evidence.md`.
- [x] Mark Task 7/Child 13 implementation closure only if every A3 acceptance
  has fresh evidence and the whole review has zero Critical/Important defect.
  Otherwise keep both `in_progress` and name the exact owning gate.
- [x] Do not mark Task 8 runtime/main, Tasks 9--12, parent delivery, commit,
  push, pull request, CI, merge or release automation complete.
- [x] Stop and present the closure/Git-delivery decision to the user. No stage
  or commit occurs inside V14.

V14 is `complete_checked_whole_scope` as of 2026-08-09. Fresh acceptance
traceability closed the remaining seven target-root/plan-snapshot rows and six
A2a exact-plan regular-file verification rows. The whole data-flow and modified-
path review found no open Critical or Important defect. Focused A3 normal/race,
whole Recovery normal/race, required real PostgreSQL Recovery normal/race and
the complete paired `000069` matrix passed without skip. Vet, backend lint,
format, diff, privacy/static, Trellis/JSON/JSONL, exact manifest, protected-hash
and Git-state gates also passed. One Trellis-only path-label defect was corrected:
the protected fixture is the existing root-relative
`recovery/testdata/rsync_local_to_remote.json`, not the absent manifest create
path under `backend/internal/backupasset/recovery/testdata`.

This closes Task 7 only. Child 13 remains `in_progress` because Tasks 8--12 and
Git delivery remain open; its parent remains `planning` and program delivery
remains 12/15. V14 performed no stage, commit, push, pull request, CI, merge,
release or runtime/main action.

## Tasks 1--7 checkpoint delivery authorization (2026-08-10)

The user explicitly directed the controller to finish the project-memory Git
delivery and cleanup flow before starting Task 8. This is a bounded ordering
amendment to the earlier deferred-delivery plan: the coherent Child 13 Tasks
1--7 product, tests, migrations, CI/spec/Trellis records and the separately
approved R65 Foundation exception may be committed as one reviewable checkpoint
after fresh pre-commit gates.

The exact commit allow-list is the dirty intersection of the 145-path manifest
plus the two approved R65 exception paths. The two pre-existing protected paths,
`go.mod` and `recovery/testdata/rsync_local_to_remote.json`, remain excluded and
must be preserved byte-for-byte. No other dirty path may be staged. Child 13 is
not archived because Tasks 8--12 remain open; Task 8 must not start until the
checkpoint commit and session record leave only those two protected unrelated
paths dirty. Push, pull request, CI, merge and release monitoring remain separate
delivery decisions after the local checkpoint.

## Tasks 1--7 checkpoint PR-CI remediation exception (2026-08-10)

Checkpoint commits `fe4eb47` and `185b481` were pushed to the dedicated branch
and opened as PR #410. Hosted CI then exposed four delivery-only failures that
were not visible in the final local checkpoint gate:

- the ordinary paired `000069` migration fixture required a real encryption
  environment even though only the first-write case needs a decryptable
  workspace locator;
- the authorization winner race could surface SQLite busy from receipt, proof or
  immutable-intent reads before the effect transaction and return
  `ErrAuthorizationUnavailable`;
- Worker image scanning rejected `golang.org/x/text v0.38.0` for
  `CVE-2026-56852`, whose fixed version is `v0.39.0`;
- `only-new-issues: true` made the backend lint action depend on a GitHub PR
  patch that cannot be returned once the checkpoint diff exceeds 20,000 lines.

The approved remediation stays on PR #410 and is limited to these paths:

- existing 145-path manifest members:
  `.github/workflows/ci.yml`,
  `backend/internal/backupasset/recovery/service.go`,
  `backend/internal/backupasset/recovery/service_test.go`, and
  `backend/internal/database/backup_asset_migrations_integration_test.go`;
- delivery-only dependency exceptions: `backend/go.mod` and `backend/go.sum`.

The dependency exception permits only the `x/text v0.39.0` security upgrade and
the `go mod tidy`-required `x/mod`/`x/tools` synchronization. It does not modify
the root-level protected `go.mod`. The fixture correction may use a fixed valid
test ciphertext for ordinary DDL-only rows. First-write behavior and the two
worker-state behavior fixtures that load the locator through model hooks continue
to use test-scoped real encryption. The authorization correction is a bounded,
context-aware retry around pre-transaction reads with a three-stage regression
selector. The workflow correction runs the same full-repository backend lint
already required locally and removes only the oversized-PR patch dependency.

Fresh focused/repeated/race, whole Recovery, full SQLite `000069`, module verify,
vet, backend lint, full `make check`, format/diff, Trellis/JSON/JSONL, manifest,
protected-hash and exact-stage gates must pass before the fix commit. The same
branch is then pushed and PR #410 is monitored until every required check passes.
Only after squash merge, post-merge automation disposition, local-main sync and
topic-worktree cleanup may Task 8 begin. Child 13 remains `in_progress`, the
parent remains `planning`, and the two protected unrelated paths remain excluded
and byte-for-byte unchanged.

## Tasks 1--7 checkpoint PR #410 second hosted-CI remediation (2026-08-10)

Hosted runs `31348175030` and `31348176438` closed the first remediation's
PostgreSQL, frontend, Docker, Worker scan, documentation and UTC-safety gates,
but retained three Backend Test & Build concurrency failures:

- plan creation could exhaust SQLite retries after a different-intent winner
  committed without resolving that durable winner;
- same-intent authorization could observe proof use after the winner committed
  but before the losing invocation's final receipt replay;
- the cancel-owner registration test released its synchronization barrier and
  then required an asynchronously scheduled executor to remain unentered, which
  exceeded the synchronous registration contract.

The bounded second correction modifies only existing manifest members
`backend/internal/backupasset/recovery/service.go`,
`backend/internal/backupasset/recovery/service_test.go` and
`backend/internal/task/manager_test.go`. Plan and authorization retry exhaustion
now resolve a durable winner before returning a closed conflict/proof-used
result, and the task test retains the exact return-before-registration assertion
without asserting post-barrier scheduling. No task production code, schema,
migration, dependency, runtime or public API changes.

Fresh focused normal `-count=10` and race `-count=5`, whole Recovery and task
normal/race, full SQLite `000069` with the encryption environment unset, module
verification, `go vet ./...`, backend lint, owned gofmt, diff checks and full
`make check` all pass. The generated `backend/coverage.out` was removed. Exact
stage remains limited to these three paths plus this Task 7 delivery evidence and
metadata; root `go.mod` and `recovery/testdata/rsync_local_to_remote.json` remain
excluded at their frozen hashes. Push the fix on PR #410, monitor all required
checks, squash merge only after GREEN, monitor post-merge automation, sync local
`main`, record closure through the repository workflow and clean the topic branch
before Task 8.

## Tasks 1--7 checkpoint PR #410 third hosted-CI remediation (2026-08-10)

The pull-request merge-ref run `31351417378` retained one Backend Test & Build
failure after the second correction. The real concurrent selector
`TestPlanCreateConcurrentDifferentIntentElectsOneWinner` showed that a losing
plan creator could finish its last transaction attempt and its immediate replay
read before the winning transaction committed, then return
`ErrRecoveryPlanUnavailable` even though the durable winner became visible
immediately afterward.

The bounded third correction remains limited to existing manifest members
`backend/internal/backupasset/recovery/service.go` and
`backend/internal/backupasset/recovery/service_test.go`. A deterministic
regression blocks the winner at its commit seam, forces the loser's final replay
read to observe the uncommitted winner, and releases the winner only after that
read. It was genuinely RED against `4e5072b`. Plan creation now performs one
additional bounded, context-aware durable-winner observation phase after normal
transaction retries are exhausted; it preserves the existing public conflict,
unavailable and context-cancellation identities and does not change schema,
migrations, dependencies, runtime or APIs.

Fresh exact normal/race repetition, whole Recovery normal/race, backend lint,
all backend/frontend tests, builds and full `make check` must pass before the
exact fix commit. Stage only the two product/test paths plus this Trellis
evidence and metadata. Keep the root `go.mod` and
`recovery/testdata/rsync_local_to_remote.json` excluded at their frozen hashes,
push the same PR, and monitor every required check before squash merge. Task 8
remains stopped through post-merge automation disposition, local-main sync and
Task 7 delivery cleanup.

## Full A3 implementation authorization state

This plan has been written after approval of PRD full-A3 requirements and
design sections 46--47. It is not implementation approval. The next authorized
action is user review of this plan. Only an explicit approval after that review
permits loading `trellis-before-dev` and starting Task 7-R48 with the genuine
RED; A3c, A3d, V14 and all Git-delivery actions retain their later stop gates.

## Tasks 1--7 checkpoint PR #410 delivery closure (2026-08-10)

- [x] Final fix `a873b49415347a39ffc2e5819f67649ce29d5f4b` passed the
  pull-request run `31353319143` and the rerun-complete push run `31353316583`.
  Required backend, PostgreSQL migration parity, frontend, Docker, Worker
  runtime/build/scan, documentation, UTC-safety and title checks were green.
- [x] PR #410 was squash-merged at `2026-08-10T04:07:42Z` as
  `def0086da561bc2c1b26c34c1efa6dacf020c3bc`; the local and remote topic branch
  were deleted.
- [x] Post-merge main CI run `31354534726` and Release Please run
  `31354534699` passed. Release Please updated the existing release PR #386
  (`chore(main): release 0.46.0`) and did not create a stable tag or GitHub
  Release, so `Publish Docker Images` and `Sync Docker Hub Description` were not
  expected for this merge.
- [x] Local `main`, `origin/main` and HEAD were synchronized at `def0086` before
  the dedicated bookkeeping branch was created. The only remaining dirty paths
  were the protected historical `go.mod` and
  `recovery/testdata/rsync_local_to_remote.json`, with their frozen hashes.
- [x] Task 7 is delivered and merged. Child 13 remains `in_progress`, the parent
  remains `planning`, program delivery remains `12/15`, and Tasks 8--12 remain
  open. Do not run `task.py archive` at this checkpoint and do not start Task 8
  until this bookkeeping PR is green, merged, post-merge automation is recorded,
  local `main` is resynchronized and the bookkeeping topic branch is cleaned.
