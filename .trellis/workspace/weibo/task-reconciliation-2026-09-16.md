# Trellis task reconciliation — 2026-09-16

## Authority and scope

User requested processing every unarchived task according to actual delivery, supersession and remaining value. This session changes task records only, using a dedicated branch from synchronized main `1ec1b2b8621fb2ae86b5888247ab49c69c629764`. No new task, product implementation, production access, deployment or data deletion is part of this pass. Existing task histories are preserved by archival, not deletion.

## Verified delivery evidence

- `git fetch origin --prune` then `main...origin/main` = `0 0`, clean starting tree.
- Live GitHub PR state/merge commits/check rollups verified for #367, #451, #457, #459, #461, #462, #464, #466, #468, #470, #478, #509 and #530. All substantive returned checks succeeded; #367 also contains a skipped duplicate PR-title check alongside the successful title check.
- Every recorded merge is an ancestor of the baseline. PR #367 contains all six archived audit children plus the original integrated implementation (151-file change).
- GitHub latest release is `v0.55.13`, published 2026-09-14T11:07:13Z at the baseline. Exact-release CI [34836543632](https://github.com/xiangnan0811/xirang/actions/runs/34836543632), Release Please [34836543567](https://github.com/xiangnan0811/xirang/actions/runs/34836543567), and Publish Docker Images [34836558879](https://github.com/xiangnan0811/xirang/actions/runs/34836558879) all returned completed/success. This verifies automation, not current NAS installation or registry digest readback.
- Existing local verification/evidence files remain available inside each archive. No new product-test success is inferred from task metadata.

## Complete disposition inventory

17 initial directories: archive 13, retain 4. Completed implementation and transferred criteria are distinguished explicitly.

| Task | Disposition | Delivery evidence | Reason / remaining owner |
|---|---|---|---|
| `07-11-07-11-full-stack-audit-remediation` | Archive | #367 / `b0deb897` | Six bounded audit children delivered and already archived in main. Remaining P3 is retained independently; no claim that its remaining items were implemented. Remaining owner: `07-11-07-11-p3-quality-debt`. |
| `07-11-07-11-p3-quality-debt` | Retain `planning` | Current source / task evidence | Retain independent P3 backlog: API middleware envelope, panel-editor RAF cleanup, and bounded runtime logging. Keep current WebSocket protocol; defer KDF salt migration pending a concrete threat/compatibility design. |
| `07-12-backup-data-explorer-design` | Retain `in_progress` | Current source / task evidence | Retain the program until consolidated production acceptance closes. 19 of 20 direct children archived after this reconciliation; code delivery is not live acceptance. |
| `08-21-backup-assets-release-acceptance` | Retain `in_progress` | Current source / task evidence | Retain as the single owner of outstanding real-data production acceptance from ten delivered repair tasks. Current NAS version, enabled state and collectors were not inspected. Rebaseline before any live steps. |
| `08-23-backup-assets-migration-69-compatibility` | Archive | #451 / `23e31601` | Migration repair delivered; repeated deployment/schema and real-data acceptance now owned by release acceptance. Remaining owner: `08-21-backup-assets-release-acceptance`. |
| `08-23-node-logs-collector-stall` | Retain `planning` | Current source / task evidence | Retain P1: current nodelogs still lacks post-dial cancellation, hard output-limit detection, per-node deduplication and worker join. Reuse shared SSH execution where compatible. Product repair can be planned independently; production re-enable remains separately gated. |
| `08-24-backup-assets-catalog-sqlite-batch-limit` | Archive | #457 / `56185103` | SQLite persistence batching delivered; parent records subsequent complete 60,515-entry Catalog as historical evidence. Remaining owner: `08-21-backup-assets-release-acceptance`. |
| `08-25-task-run-orphan-reconciliation` | Archive | #459 / `d7734487` | Formal orphan cancellation delivered; parent records v0.50.6 deployment and formal acceptance. Broader backup acceptance is transferred. Remaining owner: `08-21-backup-assets-release-acceptance`. |
| `08-25-catalog-lifecycle-race-stability` | Archive | #461 / `9ebb82ba` | Race fixture repair delivered with successful PR checks; no independent production mutation remains. Remaining owner: `08-21-backup-assets-release-acceptance`. |
| `08-25-backup-assets-mutable-search-projection` | Archive | #462 / `dc755c10` | Mutable Catalog projection repair delivered; current-point Search acceptance transferred. Remaining owner: `08-21-backup-assets-release-acceptance`. |
| `08-26-backup-assets-search-startup-isolation` | Archive | #464 / `6c124d95` | Candidate-local startup failure isolation delivered; healthy startup and current Search readiness acceptance transferred. Remaining owner: `08-21-backup-assets-release-acceptance`. |
| `08-26-backup-assets-search-enable-async-convergence` | Archive | #466 / `ff7d0d02` | Asynchronous enable convergence delivered; live enable/build/ready proof transferred without pinning old generation IDs. Remaining owner: `08-21-backup-assets-release-acceptance`. |
| `08-26-backup-assets-preview-authorization-ui` | Archive | #468 / `eff1be7e` | Preview eligibility fix delivered and followed by file-center repairs; current UI authorization/content acceptance transferred. Remaining owner: `08-21-backup-assets-release-acceptance`. |
| `08-27-backup-assets-private-network-http-content` | Archive | #470 / `013518ff` | Private-network HTTP content delivery delivered; transport, role, setting and rollback live checks transferred. Remaining owner: `08-21-backup-assets-release-acceptance`. |
| `08-27-backup-file-center-direct-preview` | Archive | #478 / `380725ca` | File center and repairs delivered through PRs 472, 474, 476, 478; later 480, 492, 502, 507 extend the released path. Final representative product acceptance transferred. Remaining owner: `08-21-backup-assets-release-acceptance`. |
| `09-08-audit-e8d2668-remediation` | Archive | #509 / `64770db7` | Requested audit repairs verified and delivered; local-only delivery notes are historical. Remaining owner: `none`. |
| `09-14-v05512-review` | Archive | #530 / `bb55ccbc` | RG-01 through RG-16 closed; requested repair acceptance and merged delivery complete. Remaining owner: `none`. |

## Remaining order and necessity

1. **Node-log P1 remains worthwhile.** Current `backend/internal/nodelogs/ssh_runner.go:29-74` only applies context at dial and still uses `LimitReader(maxBytes)` followed by synchronous Wait. `scheduler.go:35-87` lacks owned cancellation, per-node claims and worker join. Shared `backend/internal/sshutil/command_runner.go` has owned-transport execution support that should be evaluated before adding another implementation. Code planning is technically independent of backup preview acceptance; production re-enable still requires the fixed release and controlled single-node evidence. This session assessed and refreshed the existing plan, not implemented it.
2. **Consolidated backup production acceptance remains necessary.** Keep `08-21-backup-assets-release-acceptance` as the sole live acceptance owner and `07-12-backup-data-explorer-design` as its program parent. Do not replay v0.50.x incident commands or old IDs. Rebaseline actual current NAS state first. The consolidated checklist explicitly retains transport, preview, authorization, schema, indexing and collector gates from archived slices.
3. **P3 remains a low-priority bounded backlog.** `/api/v1` middleware still emits legacy error objects while the frontend extracts the standard envelope; panel-editor still stores a RAF ID on a state setter via a cast. Runtime logging should migrate by module with privacy and startup-order checks. Current bounded first-frame WebSocket authentication and durable revocation coverage support retaining that protocol. Per-deployment Argon2 salt is a future compatibility/threat-model decision, not a completed migration and not a reason to keep the old audit wrapper open.

## Hierarchy and preservation

- Normalize SQLite child's parent/children references to full directory names.
- Add previously omitted HTTP content child and the file-center delivery slice to consolidated acceptance ownership.
- Detach P3 from the delivered audit wrapper, preserving `origin_task`; archive the wrapper only after this explicit transfer.
- Keep old notes and metadata as labeled history; new status and next actions take precedence.
- Use `task.py archive --no-commit` for moves, then validate the actual final tree and one reviewable PR. No task deletion.
- This bookkeeping-only PR does not warrant a new public release or Docker publish. If a release is independently triggered, inspect its automation separately.

## Local validation

- All 17 reconciled task context validators pass after repairing 26 moved
  JSONL references. Current disposition is injected for all four active tasks.
- 13 archived / 4 active directories; retained parent/child references are
  reciprocal and all referenced child directories resolve.
- All 109 original files from the 13 archived tasks remain at their archive
  destinations. Original external audit report for the September 8 task is
  absent from this checkout; the context now explicitly uses its committed
  adjudication ledger, without pretending to recreate the original report.
- `bash scripts/check-doc-freshness.sh` and `git diff --check` pass.
- A full historical metadata scan encountered an unrelated pre-existing invalid
  JSON file at `archive/2026-05/05-13-trivy-platform-digest-scan/task.json`.
  Its bytes match HEAD and it is outside this unarchived-task reconciliation;
  it was excluded from the focused 17-task validation and left unchanged.
- No product source changed, so product suites were not rerun locally for this
  task-only change. The PR still goes through the repository's required CI.
- Independent bounded task review found one stale live acceptance description;
  it was corrected and rechecked. Final review reports no remaining material
  findings. The reviewer independently checked task structure, original-file
  preservation and scope transfers; remote delivery was verified by the main
  session, not rechecked by that reviewer.
