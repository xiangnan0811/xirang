# Implement — Catalog lifecycle race stability

## Phase 0 — diagnosis and contract

- [x] Create `codex/catalog-lifecycle-race-stability` in a dedicated worktree from released `main`.
- [x] Create this Trellis child under the active backup-assets release acceptance parent.
- [x] Load backend quality and cross-layer workflow specs for asynchronous lifecycle tests.
- [x] Preserve the two exact failed CI selectors and verify the production heartbeat/build ordering.
- [x] Complete independent read-only research for both CI failure shapes and reconcile findings into `research/root-cause.md`.

## Phase 1 — mandatory RED and minimal GREEN

- [x] Record the two existing CI failures as immutable pre-change RED evidence.
- [x] Add a deterministic pre-change regression for the fixture ordering if it can honestly fail without modifying production behavior; otherwise record why the CI RED is the correct test-only baseline.
- [x] Gate each failing renewer on its Provider session's explicit enumeration-entry signal, with context cancellation as the bounded exit.
- [x] Keep all production files unchanged; if that is impossible, stop and revise the design before proceeding.
- [x] Run the two exact selectors to GREEN.

## Phase 2 — stability and review

- [x] Run the combined selectors repeatedly in normal mode and under `-race`.
- [ ] Run the Catalog package and the exact backup-asset CI race selector. (Catalog passed; the broad selector is locally blocked by temp-filesystem quota/safety constraints recorded in `research/implementation-evidence.md`.)
- [x] Use a Trellis implement subagent for RED/GREEN, evidence, and self-review; main session coordinates.
- [x] Run an independent Trellis spec-compliance and code-quality check, fix every finding, and re-review until approved. (`research/check-evidence.md`: all Important/Minor findings fixed; `SPEC_COMPLIANCE_OK` and `QUALITY_OK`.)
- [ ] Run focused lint/vet, formatting, `git diff --check`, and remaining backend gates. (Pinned Go 1.26.6 `golangci-lint`/`go vet`, `gofmt`, and focused diff check passed; remaining backend gates stay with the main session.)

## Phase 3 — PR, release, and production gate

- [ ] Commit/push and PR #461 are complete; monitor all required CI and fix failures on the same branch until green.
- [ ] Merge only when green; monitor post-merge main CI and do not accept a rerun-only resolution to another Catalog timing failure.
- [ ] Merge the subsequent stable Release Please PR only when green; monitor GitHub Release and multi-arch Docker publication and verify labels/digests.
- [ ] Only after the fixed release is verified, provide the root-safe no-`test`, no-`cd` production upgrade/rollback/acceptance command.
- [ ] Resume the parent real-data Catalog/Search/UI acceptance; keep node-log collectors disabled until that acceptance passes.
