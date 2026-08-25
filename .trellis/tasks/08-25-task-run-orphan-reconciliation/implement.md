# Implement — formal cancellation of orphaned task runs

## Phase 0 — isolate and lock the contract

- [x] Create `codex/task-run-orphan-reconciliation` in a dedicated worktree from current `main`.
- [x] Create this Trellis child under the parent backup-assets release acceptance task.
- [x] Load backend/database/error/quality/logging and cross-layer workflow specs.
- [x] Record the production orphan proof: the latest state is `running`/enabled with formal Pause
  pending; keep collectors off and do not claim Pause completion without verification.
- [x] Complete Trellis research and self-review PRD/design/implementation contexts.

## Phase 1 — mandatory RED

- [x] Add a public-path `Manager.Cancel` regression for a terminal Task with an authoritative running
  TaskRun and no live owner.
- [x] Assert desired durable cancellation, finish evidence, aggregate preservation, and sanitized
  text; run only this test before production code changes and record the expected failure.
- [x] Add coverage for a no-owner running aggregate plus active TaskRun.
- [x] If the tests do not reproduce the current gap, stop implementation and return to diagnosis.

## Phase 2 — minimal GREEN

- [x] Add a temporary process-local cancellation/trigger barrier for the no-owner fallback.
- [x] Add one transactional, authority-filtered, CAS-guarded orphan reconciliation helper.
- [x] Integrate it only into `Manager.Cancel`; preserve live-owner signaling and ordinary terminal
  rejection when no orphan is reconciled.
- [x] Re-run the exact RED tests to GREEN before broader tests.

## Phase 3 — adversarial tests and Trellis reviews

- [x] Prove all current-node authoritative active statuses and multiple rows reconcile atomically;
  non-authoritative, unrelated, or already-terminal rows remain untouched.
- [x] Prove CAS drift rolls back and a concurrent trigger owner cannot be mistaken for an orphan.
- [x] Run existing cancellation/reservation/runner race tests and focused race detection repeatedly.
- [x] Use a Trellis implement subagent for RED/GREEN and self-review; main session coordinates.
- [x] Run independent Trellis spec-compliance review, fix every finding, and re-review until approved.
- [x] Run independent Trellis check/code-quality review, fix every finding, and re-review until approved.
- [x] Run task-package tests, package lint/vet, focused race/repetition checks, formatting, and
  `git diff --check`.
- [x] Run the remaining full-backend lint/test/build gates.
- [x] Reproduce the PostgreSQL CI live-owner ordering failure with a deterministic public-path RED,
  preserve the distinct direct-runner barrier ordering, and re-run focused race plus full-backend gates.

## Phase 4 — PR, release, and production acceptance

- [ ] Commit/push and open PR #459; monitor every required CI job, fix on the same branch, and merge only green.
- [ ] Monitor post-merge Release Please, merge the release PR when green, and monitor Docker publish.
- [ ] Give the operator a no-`test`, no-`cd` root-safe preflight/backup/upgrade/rollback runbook.
- [ ] On the fixed stable image, use formal Cancel to prove zero active runs before formal Resume.
- [ ] Observe a new successful backup and complete Catalog/Search/metadata-content UI preview,
  container health, and critical-error evidence in the parent task.
- [ ] Archive this child and finish the parent acceptance; only then start the approved node-log P1.
