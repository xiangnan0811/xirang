# Implement — 节点日志采集超时与队列卡死修复

Do not run `task.py start` until the user approves this planning set in a later message.
This task is independent from the migration P0 and should start only after the P0 implementation
order is confirmed.

## Phase 0 — task start and deterministic RED

- [ ] Start on its own dedicated `codex/` branch/worktree from current origin/main.
- [ ] Re-read backend quality/logging/database specs and `research/root-cause.md`.
- [ ] Add fake SSH session/dial seams without changing production behavior yet.
- [ ] Write deterministic failing tests for blocked stdout, blocked Wait, max+1 output,
  duplicate enqueue and Shutdown join.
- [ ] Capture RED evidence; do not use production SSH endpoints.

## Phase 1 — full-lifecycle Runner cancellation

- [ ] Validate timeout/maxBytes before sensitive work.
- [ ] Derive the authoritative operation context.
- [ ] Coordinate bounded stdout and remote wait.
- [ ] On cancel/timeout/error/limit, close session and client and join all owners.
- [ ] Return typed/wrapped context and output-limit errors; keep raw output out of errors/audit.
- [ ] Preserve valid remote ExitError behavior only for complete bounded output.
- [ ] Run runner tests repeatedly and under race.

## Phase 2 — per-node single-flight scheduling

- [ ] Add queued/in-flight state with narrow synchronized methods.
- [ ] Claim before enqueue and roll back the claim on full/cancel.
- [ ] Mark in-flight on receive and defer release across every worker exit.
- [ ] Aggregate one queue-full warning per enqueue pass.
- [ ] Prove repeated ticks never duplicate a node and completion allows the next cycle.

## Phase 3 — owned lifecycle and shutdown

- [ ] Add internal run cancel and worker WaitGroup.
- [ ] Ensure one owner closes the jobs channel and `done` closes only after workers join.
- [ ] Make Shutdown initiate cancel, wait, return caller deadline error truthfully and remain
  idempotent.
- [ ] Test Shutdown while a runner is blocked and after Run already returned.
- [ ] Do not broaden into a global lifecycle refactor unless a separate approved task is created.

## Phase 4 — data safety and observability

- [ ] Keep cursor/log writes absent for timeout/cancel/output_limit.
- [ ] Add closed metrics for dedup, queue rejection, in-flight and fetch outcomes.
- [ ] Add metric/log tests that reject sensitive/high-cardinality labels and raw SSH output.
- [ ] Re-run existing parser/sanitizer/cursor/retention tests unchanged.

## Phase 5 — specs and operator runbook

- [ ] Add executable nodelogs cancellation/single-flight/shutdown contract to backend quality or
  logging spec.
- [ ] Document temporary production-disabled state and single-node/batch re-enable checklist in
  this task's acceptance research; do not expose host details.
- [ ] Do not edit production config or claim collection has resumed.

## Phase 6 — verification

- [ ] `cd backend && go test ./internal/nodelogs -count=50`.
- [ ] `cd backend && go test -race ./internal/nodelogs -count=10`.
- [ ] Related `sshutil`, credential-audit and lifecycle tests.
- [ ] Full backend test/build/lint/vet.
- [ ] Privacy/source scans, task validation, `git diff --check`.
- [ ] Independent review of goroutine ownership, close ordering and every error exit.

## Phase 7 — delivery and production handoff

- [ ] Commit/PR only after gates; monitor required CI to green.
- [ ] Merge and monitor release/image automation; record immutable image digest.
- [ ] Keep production collection disabled during Core upgrade.
- [ ] Hand the exact single-node then batch re-enable commands/checks to the user.
- [ ] Mark product implementation complete only after code/release gates; mark production recovery
  complete only after the user supplies observation evidence.

## Risky boundaries

- A timeout return before goroutine join is still a resource leak.
- Stopping read at maxBytes without closing the session can reproduce the original deadlock.
- A missing defer release can permanently suppress one node.
- Closing the jobs channel from multiple owners can panic.
- Tests must synchronize with channels, not rely on sleep timing.

## Rollback

Disable all node log sources again, preserve cursors/log rows, and run the previous image if the
new collector regresses. No schema rollback or data deletion is required by this task.
