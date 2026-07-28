# Snapshot indexer test isolation

## Goal

Synchronize the snapshot indexer async test lifecycle so process-global task-ID
admission cannot leak across independent test databases.

## Requirements

- Treat CI run `30354793081` attempt 1 as the observed RED: the empty-snapshot
  test received `任务 1 的索引构建已在运行中`; attempt 2 and the same-commit PR
  run passed, proving a timing-dependent isolation failure.
- Keep the production `Indexer` contract unchanged. Process-global exclusion for
  the same real Task ID is intentional; the defect is that a test returns before
  its own asynchronous build has started and completed.
- In `TestEnsureIndexedRejectsPartialRowsWithoutCompletionMarker`, establish an
  explicit signal after the scheduled build has entered its provider listing
  path, then wait with a bounded timeout for that build to leave the global
  `indexingJobs` registry.
- A timeout must fail loudly. The test must not silently continue when the build
  never starts or never finishes.
- Do not randomize or otherwise make the second test's Task ID unique; that would
  hide the leaked goroutine instead of proving cleanup.
- Limit product/test changes to
  `backend/internal/snapshot/indexer_test.go`. Trellis task artifacts and the
  parent registration are the only additional workflow changes.

## Acceptance Criteria

- [x] CI run `30354793081` attempt 1 is the genuine RED demonstrating that the
      old wait can return before the scheduled build owns and releases Task ID
      1. The same run's attempt 2 passed, confirming timing dependence.
- [x] The test waits for its own build to start and then for `IsIndexing` to
      become false, with deterministic timeout failures for both phases.
- [x] The two implicated tests pass at high repetition in normal and race modes.
- [x] The complete `internal/snapshot` package passes normally, with race
      detection, and with a CI-style coverage invocation.
- [x] Backend/full-project gates, exact task validation, formatting, and diff
      hygiene pass before delivery.
- [x] Changes are delivered through a dedicated PR; required CI and post-merge
      automation are monitored before archiving this child task.

## Non-goals

- No production scheduler or indexer redesign.
- No random Task ID fixture workaround.
- No unrelated snapshot, database, or P3 quality-debt cleanup.

## Approval

The user's standing instruction to continue routine technical and workflow
decisions authorizes this bounded child task, its activation, implementation,
review, and delivery without another approval round. This is a lightweight,
test-only task, so the PRD is the complete planning artifact.

## Current Evidence

- Implementation changed only `backend/internal/snapshot/indexer_test.go`: the
  provider listing hook closes a `sync.Once`-guarded start signal; the test then
  uses independent bounded waits for start and admission cleanup.
- Focused normal repetition passed at `-count=1000`; focused race repetition
  passed at `-count=100`; the complete snapshot package passed at `-count=20`
  and once under the race detector; CI-style snapshot coverage was 44.1%.
- The first full `make check` attempt stopped only because `/tmp` had 2.8 GiB
  free and Go linkers reported `disk quota exceeded`. After removing 4.5 GiB of
  disposable Go caches, a fresh short-`GOTMPDIR` rerun passed backend/frontend
  lint, all backend tests, 168 frontend files / 1388 tests, coverage, and both
  builds. The generated `backend/xirang-server` was removed afterward.
- The existing configured frontend accessibility warning remains one warning
  and zero errors; this task does not touch that path.
- Independent specification-compliance and code-quality reviews both returned
  `APPROVED` with no actionable findings. PR #403 was squash-merged at
  `2026-07-28T12:35:08Z` as `6478c9f882a4f872cb9c9b2fba87886c5195ab06`.
- Post-merge CI run `30359601980` and Release Please run `30359599029` both
  completed successfully. This test-only merge did not create a formal release
  or trigger image publication or Docker Hub synchronization, as expected.
