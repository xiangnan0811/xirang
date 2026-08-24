# Implement — Catalog SQLite safe persistence batching

## Phase 0 — isolate and load contracts

- [ ] Merge the parent acceptance/evidence planning change through PR and CI.
- [ ] Create dedicated `codex/backup-assets-catalog-sqlite-batch-limit` branch/worktree from updated main.
- [ ] Read backend/guides specs selected by `trellis-before-dev` and this task's PRD/design/plan.
- [ ] Confirm production remains connected, healthy and untouched; node-log collectors remain off.

## Phase 1 — mandatory RED

- [ ] Add one real SQLite `Indexer.Build` regression using logical batch size 2000 and at least 2000 canonical records.
- [ ] Require desired complete generation, exact written count and encrypted stored locators.
- [ ] Run only that test before production code changes and capture the expected SQL-variable failure.
- [ ] If the test does not fail at the predicted boundary, stop this implementation and return to diagnosis.

## Phase 2 — minimal GREEN

- [ ] Add a private, database-safe Catalog entry persistence chunk with documented parameter headroom.
- [ ] Change only `Indexer.insertBatch` to use GORM's batched create path; keep logical validation and flush limits unchanged.
- [ ] Re-run the exact RED to GREEN and confirm no plaintext locator at rest.
- [ ] Run existing Catalog tests to detect proof/order/lease/source-lifecycle regressions.

## Phase 3 — Trellis reviews and gates

- [ ] Use a Trellis implement subagent for RED/GREEN and self-review; main session coordinates only.
- [ ] Run Trellis spec-compliance review, fix every finding, and re-review until approved.
- [ ] Run independent Trellis check/code-quality review, fix every finding, and re-review until approved.
- [ ] Run repeated focused test, full Catalog package, related repository/search tests, backend lint/test/build/vet and `git diff --check`.

## Phase 4 — PR, release and parent acceptance

- [ ] Commit/push, open PR, monitor all required CI, fix on the same branch, and merge only when green.
- [ ] Monitor post-merge Release Please, merge the release PR when green, and monitor Docker publish.
- [ ] Give the operator an exact read-only preflight, backup/rollback and stable-image upgrade runbook.
- [ ] After production upgrade, observe normal worker completion without manual generation writes.
- [ ] Return to the parent task for Search/UI/health/privacy evidence; do not start node-log P1 early.
