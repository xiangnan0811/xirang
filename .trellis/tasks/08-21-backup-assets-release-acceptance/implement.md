# v0.50.4 Real-Data Production Acceptance Implementation Plan

> **For agentic workers:** If product code becomes necessary, REQUIRED SUB-SKILL: use the Trellis phase context with `trellis-implement`, then an independent `trellis-check`. Steps use checkbox syntax for tracking.

**Goal:** Make the released v0.50.4 Backup -> Data workspace display and preview a real, searchable backup asset through supported production APIs, record the evidence, and only then release the node-log P1 gate.

**Architecture:** Select one existing local Rsync Task through a read-only preflight, invoke the Task-derived repository Connect boundary exactly once, wait conditionally for Catalog and Search workers, and open the opaque AssetRef in the existing Data UI. Production state is never synthesized with SQL; failure-only rollback uses repository Disconnect and leaves backup files untouched.

**Tech stack:** Go 1.26 backend, Gin REST API, GORM/SQLite production state, React 18 Data workspace, browser DevTools on the same-origin authenticated session, Docker health/log inspection.

---

### Task 1: Rebind the Trellis authority to v0.50.4

**Files:**
- Modify: `.trellis/tasks/08-21-backup-assets-release-acceptance/task.json`
- Modify: `.trellis/tasks/08-21-backup-assets-release-acceptance/prd.md`
- Modify: `.trellis/tasks/08-21-backup-assets-release-acceptance/design.md`
- Modify: `.trellis/tasks/08-21-backup-assets-release-acceptance/implement.md`
- Create: `.trellis/tasks/08-21-backup-assets-release-acceptance/research/v0504-real-data-acceptance.md`
- Create: `.trellis/tasks/08-21-backup-assets-release-acceptance/research/v0504-console-runbook.md`
- Preserve: `.trellis/tasks/08-21-backup-assets-release-acceptance/research/acceptance-protocol.md`

- [x] Preserve the v0.50.2 No-Go protocol as immutable history.
- [x] Bind current scope to v0.50.4 revision `214f5e18b47974d4e353227fa52782992cef70f7`.
- [x] Record the supported Task-derived Connect pipeline, privacy boundary, failure-only Disconnect, and node-log sequencing gate.
- [x] Validate `task.json` with `python3 -m json.tool .trellis/tasks/08-21-backup-assets-release-acceptance/task.json`.
- [x] Scan the new plan/runbook for placeholder markers and forbidden secret/locator capture instructions.

### Task 2: Prove the existing repository-to-preview chain locally

**Files:**
- Verify only: `backend/internal/backupasset/repository/`
- Verify only: `backend/internal/backupasset/catalog/`
- Verify only: `backend/internal/backupasset/search/`
- Verify only: `backend/internal/backupasset/content/`
- Verify only: `backend/internal/api/handlers/`
- Record: `.trellis/tasks/08-21-backup-assets-release-acceptance/research/v0504-real-data-acceptance.md`

- [x] Reproduce the local Go build failure and identify `/tmp` user-quota exhaustion at the CGO linker's private `go-link-*` directory.
- [x] Run the same focused package set with both `GOTMPDIR` and `TMPDIR` set to one dedicated `/var/tmp` directory.
- [x] Confirm repository, catalog, search, content, and handler packages pass.
- [x] Record the exact green package results in the acceptance evidence.

Verification command:

```bash
XR_GO_TMP="$(mktemp -d /var/tmp/xirang-go-tmp.XXXXXX)"
GOTMPDIR="$XR_GO_TMP" TMPDIR="$XR_GO_TMP" go test ./internal/backupasset/repository ./internal/backupasset/catalog ./internal/backupasset/search ./internal/backupasset/content ./internal/api/handlers
```

Expected: five `ok` package lines and exit status 0. Remove only the exact temporary directory created for this command after the process exits.

### Task 3: Select one exact production Task without writing

**Files:**
- Execute: `research/v0504-console-runbook.md` sections A, B, and C
- Record: `research/v0504-real-data-acceptance.md` Preflight evidence

- [x] Run the session-scoped read helper/inventory and verify the token is not printed.
- [x] Verify all five inventory endpoints are HTTP 200 and the repository count is still 0; the corrected exact-Task preflight confirmed role Admin.
- [x] Correct the `/me` role projection to `data.user.role`; the first production inventory was HTTP 200 but the earlier console projection printed `null`.
- [x] Separate GA `no_repository` capability-gap classification from the Task API's safe legacy publication projection.
- [x] Choose enabled successful Rsync Task 3 with a local absolute target and `legacy_mutable / legacy / legacy`; it has the freshest successful run in the sanitized inventory.
- [x] Run C for exact numeric Task ID 3 and observe `target_preflight_ok=true`.
- [x] Require no active runs, a recent successful run, and positive backup-root entry count.
- [x] Evaluate every fail-closed condition before permitting the Connect step; none failed and no write occurred during Task 3.

Expected: `target_preflight_ok=true`. Evidence contains no Task name, path, file name, command, error text, or credential.

### Task 4: Connect exactly once and prove repository/link/point state

**Files:**
- Execute: `research/v0504-console-runbook.md` section D
- Record: `research/v0504-real-data-acceptance.md` Mutation contract and End-to-end evidence

- [x] Re-run immediate health/role/Task/run/file/repository guards inside D.
- [x] Confirm the prompt names exact numeric Task ID 3.
- [x] Send one body from the guarded runtime value: `{ "task_id": 3 }`.
- [x] Observe HTTP 200; no retry or SQL repair was attempted.
- [x] Require and record valid 32-hex repository and recovery-point IDs.
- [x] Require repository `online`, `access_active=true`, and one active `task_link` lineage for Task 3 with `legacy_mutable` publication.
- [x] Require the listed point to be `mutable_head`, `observed`, and produced by Task 3.

Expected: `connect_postcondition_ok=true`. The only production mutation is the one formal Connect call.

### Task 5: Wait for Catalog/Search and prove a real file AssetRef

**Files:**
- Execute: `research/v0504-console-runbook.md` section E
- Record: `research/v0504-real-data-acceptance.md` End-to-end evidence

- [x] Start Catalog condition polling; it stopped on the first durable failed build without restarting the process.
- [x] Stop immediately on the failed latest build and record stable code `catalog_build_failed` through one GET-only allow-listed projection.
- [ ] Require complete generation/coverage, positive indexed entries, content available, and Catalog list permission; prove preview authorization later through the delivery-ticket/UI path.
- [ ] Poll the exact-point schema-v1 `type=file` search for at most 95 seconds.
- [ ] Require HTTP 200, complete/partial index coverage, and at least one item.
- [ ] Record only opaque AssetRef and safe type/size/MIME/timestamp metadata.

Expected: `real_asset_search_ok=true` and a valid `/app/backups/data` route.

### Task 6: Complete UI, health, privacy, and collector acceptance

**Files:**
- Execute: `research/v0504-console-runbook.md` section F
- Record: `research/v0504-real-data-acceptance.md`

- [ ] Open the generated Data route and require selected-asset metadata.
- [ ] Require the Preview tab to render through one supported renderer.
- [ ] If classification requires step-up, perform it only in UI; record only whether it was required.
- [ ] Re-read every node log config and require `remaining_collectors=0` without printing paths.
- [ ] Inspect the exact v0.50.4 container and require running/healthy, restart count 0, and zero counted critical matches in the acceptance window.
- [ ] Confirm the evidence contains no token, password, TOTP, proof, locator, path, file name, breadcrumb, or content.

Expected: AC1-AC8 all checked with timestamps and request IDs; no hidden failure or waiver is marked passed.

### Task 7: Conditional product gap path

**Files:**
- Create only if needed: `.trellis/tasks/08-24-backup-assets-rsync-production-onboarding-gap/`
- Do not modify production state outside the published API

- [ ] Trigger this task only when no existing Task can pass the read-only preflight or the formal Connect/Catalog/Search/Content path proves a missing product capability.
- [x] Triggered after four automatic Catalog generations failed deterministically with `catalog_build_failed` while repository/content/Task state remained healthy and quiescent.
- [x] Create child `08-24-backup-assets-catalog-sqlite-batch-limit` with a narrow PRD/design/plan and privacy contract.
- [ ] Read the relevant backend/frontend Trellis specs.
- [ ] Create the first failing test before implementation.
- [ ] Invoke the Phase 2.1 Trellis context with a `trellis-implement` worker.
- [ ] Invoke the Phase 2.2 Trellis context with an independent `trellis-check` reviewer.
- [ ] Commit on a dedicated branch, create a PR, monitor required CI, fix failures on the same branch, merge when green, and monitor post-merge automation before returning to Task 3.

Expected: no product code is changed unless runtime evidence proves this conditional path is necessary.

### Task 8: Close this gate, then start node-log P1

**Files:**
- Modify: `.trellis/tasks/08-21-backup-assets-release-acceptance/research/v0504-real-data-acceptance.md`
- Modify: `.trellis/tasks/08-21-backup-assets-release-acceptance/task.json`
- Create or resume after acceptance only: the approved node-log P1 Trellis task

- [ ] Mark the v0.50.4 gate accepted only after Tasks 3-6 pass.
- [ ] Keep node-log collectors disabled during the transition.
- [ ] Create or resume the node-log P1 task on a fresh dedicated branch/worktree.
- [ ] Use TDD, `trellis-implement`, independent `trellis-check`, PR/CI, green merge, and post-merge automation monitoring.
- [ ] Do not stop at “waiting for merge”; continue until green/merged/monitored or record a real external blocker.

Expected: node-log P1 starts only after the real-data preview evidence is complete.

## Plan self-review

- Spec coverage: R1-R7 map to Tasks 3-6; the product-gap escape hatch maps to Task 7; P1 ordering maps to Task 8.
- Placeholder scan: runtime-selected IDs are obtained by guarded prompts and then stored as exact values; no design or implementation item is left unspecified.
- Type consistency: repository/recovery-point IDs are 32 lowercase hex, entry IDs are 64 lowercase hex, and search uses the backend schema-v1 field names.
- Execution choice: the user already approved Trellis implement/check subagents for any code path; no further choice gate is needed.
