# Implementation evidence

## Mandatory RED

- Date: 2026-08-25
- Public seam: `Manager.Cancel`
- Selector: `^TestCancelTerminalPausedTaskReconcilesAuthoritativeRunningOrphan$`
- Command (from `backend/`):
  `TMPDIR=/home/murray/.cache/xirang-orphan-go-tmp GOTMPDIR=/home/murray/.cache/xirang-orphan-go-tmp go test ./internal/task -run '^TestCancelTerminalPausedTaskReconcilesAuthoritativeRunningOrphan$' -count=1`
- Result: expected behavioral failure, exit 1. `Cancel` returned the existing fixed unsupported-state message for a terminal paused Task and left the authoritative running TaskRun unreconciled.
- Privacy: fixture-only identifiers and fixed product messages; no production identifiers, paths, locators, credentials, or raw runtime evidence are recorded.
- Infrastructure note: two earlier attempts did not reach the test because the disposable build/temp cache hit its disk quota. The Go build cache was cleared and `TMPDIR`/`GOTMPDIR` were redirected before this valid RED.

## GREEN and focused verification

- Exact RED selector after implementation: exit 0, `ok xirang/backend/internal/task 0.011s`.
- Seven-test public `Manager.Cancel` orphan matrix: exit 0, `ok xirang/backend/internal/task 0.187s`.
- Matrix repetition (`-count=20`): exit 0, `ok xirang/backend/internal/task 3.487s`.
- Existing and new Cancel/canceled-run surface under the race detector (`-race -count=3`): exit 0, `ok xirang/backend/internal/task 8.537s`.
- Whole task package (`go test ./internal/task -count=1`): exit 0, `ok xirang/backend/internal/task 10.818s`.
- Package vet (`go vet ./internal/task`): exit 0 with no findings.
- Package lint with the repository toolchain (`GOTOOLCHAIN=go1.26.6 golangci-lint run ./internal/task/...`): exit 0, `0 issues` after resolving three test-only errcheck findings. The first unpinned-toolchain attempt used Go 1.27 with a linter built on Go 1.26 and panicked before analysis; it is not counted as lint evidence.
- Final formatting and whitespace gate (`gofmt` on the three changed Go files plus `git diff --check`): exit 0.

## Independent Trellis check

- Fixed runner cleanup so only the exact trigger-owned `pendingRunOwnership` may remove its reservation; direct and legacy runner entry points carry no ownership and cannot erase a concurrent Cancel barrier. The unscheduled trigger/restore cleanup paths also use exact compare-and-delete semantics.
- Added deterministic coverage proving direct runner cleanup cannot remove a Cancel-owned barrier before Cancel finishes. The exact new selector passed once; the eight-test reconciliation/barrier matrix passed with `-count=10`; the new barrier, concurrent-cancel barrier, and safe-error tests passed under `-race -count=3`.
- Restored structured `.Err(...)` evidence on reconciliation query, update, and transaction failures while retaining fixed client-facing sentinels. The injected database-error regression verifies the raw canary appears only in the internal structured log with task/run identifiers.
- Re-ran `go test ./internal/task -count=1` (exit 0, `ok xirang/backend/internal/task 10.454s`), `GOTOOLCHAIN=go1.26.6 go vet ./internal/task` (exit 0), `GOTOOLCHAIN=go1.26.6 golangci-lint run ./internal/task/...` (exit 0, `0 issues`), and `git diff --check` (exit 0).
- Corrected task artifacts to state that the latest production evidence remains running/enabled and the formal Pause result is pending. Completed task-package checks remain separate from the still-pending full-backend gates.

## Covered contract

- Terminal paused Task plus an authoritative running orphan succeeds through public `Manager.Cancel`, preserves the Task aggregate byte-for-byte, and writes one fixed cancellation reason plus finish/duration evidence to the run.
- Active Task plus all current-node authoritative `pending`, `running`, and `retrying` rows terminalizes atomically, including multiple rows with one shared finish time.
- Missing current-node authority fails closed; legacy-unknown, mismatched, already-terminal, and other-Task rows remain unchanged.
- The process-local barrier blocks a new trigger and distinguishes a concurrent Cancel from a live runner; every success/error path removes the barrier.
- TaskRun and Task CAS drift roll back the whole transaction. Injected database failure returns only the fixed safe product error and leaves Task/TaskRun state unchanged.

## Full backend gate

- Full test suite: `GOTOOLCHAIN=go1.26.6 TMPDIR=/run/user/1000 GOTMPDIR=/run/user/1000 go test -p 1 ./...` from `backend/`, exit 0. Every backend package passed.
- Full lint: `GOTOOLCHAIN=go1.26.6 TMPDIR=/run/user/1000 GOTMPDIR=/run/user/1000 golangci-lint run ./...`, exit 0 with `0 issues`.
- Full vet: `GOTOOLCHAIN=go1.26.6 TMPDIR=/run/user/1000 GOTMPDIR=/run/user/1000 go vet ./...`, exit 0.
- Server build: `GOTOOLCHAIN=go1.26.6 TMPDIR=/run/user/1000 GOTMPDIR=/run/user/1000 go build -o /run/user/1000/xirang-orphan-server ./cmd/server`, exit 0.
- The successful suite used the direct, non-symlinked per-user runtime tmpfs and serialized package builds. Earlier attempts under a long Btrfs path and the capacity-constrained shared `/tmp` did not satisfy Unix-socket path, free-inode, or linker-space prerequisites and are not counted as code-test evidence.

## PostgreSQL CI regression and ordering repair

- PR #459 first ran against commit `e7d2173075aaf57857b74f32890a85440295f775`. Its PostgreSQL Recovery behavior parity job reached a real behavioral RED: `ManagerCancelAfterEntryCommitPreservesPriorOutcome` failed because the executor-entry transaction did not durably commit.
- Log analysis proved this was not PostgreSQL lock contention. `Cancel` signaled the live `pendingRunOwnership` before reading the durable Task outcome; the canceled executor context then failed admission and rolled back the entry transaction. The approximately five-second Task query timing came from the test's deliberate post-query callback barrier.
- A deterministic public `Manager.Cancel` regression, `TestCancelReadsTaskOutcomeBeforeSignalingLiveOwner`, reproduced the ordering defect locally and failed because the owner was signaled before the prior Task outcome was read.
- The first repair moved every owner signal after the Task read. That made the new regression green but correctly exposed a separate direct/legacy-runner regression: `TestDirectRunnerCleanupDoesNotDeleteCancelBarrier` timed out because that path must be signaled while the Cancel barrier owns `pendingRuns`.
- The final repair distinguishes the paths. Trigger-owned `pendingRunOwnership` reads the Task outcome before signaling; a direct/legacy `chainRunner` under the Cancel barrier is signaled before the read. If a trigger owner disappears between the read and signal, Cancel returns the fixed in-progress error and never falls through to orphan reconciliation.
- Final focused ordering/direct-runner/executor-entry selectors passed with `-count=10` (`ok xirang/backend/internal/task 0.878s`). The complete Cancel/direct race surface passed with `-race -count=3` (`ok xirang/backend/internal/task 2.959s`).
- Post-repair package verification passed: `go test ./internal/task -count=1` (`ok xirang/backend/internal/task 3.280s`), Go 1.26.6 package vet, focused lint (`0 issues`), `gofmt -d`, and `git diff --check`.
- Post-repair full backend verification passed: serialized `go test -p 1 ./...`, full Go 1.26.6 vet, full lint (`0 issues`), and `go build ./cmd/server`.
- Independent read-only review found no blocking issue and confirmed the two-path ordering repair. The actual PostgreSQL selector remains intentionally unclaimed locally because `TEST_POSTGRES_DSN` is unavailable; the next CI run is the required proof.

## Deliberately not claimed

- The implementation and task metadata commits were pushed and PR #459 is open. The first CI run exposed the PostgreSQL ordering RED described above; the repaired head still requires a new green CI run before merge.
- No merge, fixed release, production upgrade, formal production Cancel/Resume, or backup-assets production acceptance has been completed yet.
