> **Accepted and closed (2026-09-16):** [Production acceptance](research/production-acceptance-2026-09-16.md) supersedes earlier pending delivery/rollout status. The user accepted the required single-node scope; other collectors are not currently required.

> **Current disposition (2026-09-16):** see [reconciliation.md](reconciliation.md). Older status, delivery gates and deferred-work ownership below are historical where that record supersedes them.

# Implement — 节点日志采集超时与队列卡死修复

## Approved follow-up — bounded journal recovery

- [x] RED: execute generated shell for stale/initial/recent cursors, batches over 200,
  empty recent history, mixed journal/file sources, shell quoting and command failure.
- [x] Carry persisted cursor timestamp and implement one-hour/200-entry recovery policy.
- [x] Print safe delimiters and reject incomplete/error framing; retain failure cursors.
- [x] Record successful policy resets without sensitive fields or false continuity claims.
- [x] Verify empty reset then new entries, successive oldest-first batches, and failure
  paths through worker persistence. No schema or production mutation.
- [x] Independent check, repeated/race tests, backend gates, specification and runbook.
- [ ] PR CI, merge, release CI/image publication, then separate NAS acceptance.

The user approved implementation on 2026-09-16. Reuse this existing task on
`codex/node-logs-collector-stall` from current main. There is no code dependency
on the historical migration P0 or backup production acceptance.

## Phase 0 — task start and deterministic RED

- [x] Start on its own dedicated `codex/` branch/worktree from current origin/main.
- [x] Re-read backend quality/logging/database specs and `research/root-cause.md`.
- [x] Reuse the shared fake session seam and add real loopback SSH coverage.
- [x] Write deterministic failing tests for blocked stdout, blocked Wait, max+1 output,
  duplicate enqueue and Shutdown join.
- [x] Capture RED evidence; do not use production SSH endpoints.

## Phase 1 — full-lifecycle Runner cancellation

- [x] Validate timeout/maxBytes before sensitive work.
- [x] Derive the authoritative operation context.
- [x] Coordinate bounded stdout and remote wait.
- [x] On cancel/timeout/error/limit, close session and client and join all owners.
- [x] Return typed/wrapped context and output-limit errors; keep raw output out of errors/audit.
- [x] Preserve valid remote ExitError behavior only for complete bounded output.
- [x] Run runner tests repeatedly and under race.

## Phase 2 — per-node single-flight scheduling

- [x] Add queued/in-flight state with narrow synchronized methods.
- [x] Claim before enqueue and roll back the claim on full/cancel.
- [x] Mark in-flight on receive and defer release across every worker exit.
- [x] Aggregate one queue-full warning per enqueue pass.
- [x] Prove repeated ticks never duplicate a node and completion allows the next cycle.

## Phase 3 — owned lifecycle and shutdown

- [x] Add internal run cancel and worker WaitGroup.
- [x] Ensure one owner closes the jobs channel and `done` closes only after workers join.
- [x] Make Shutdown initiate cancel, wait, return caller deadline error truthfully and remain
  idempotent.
- [x] Test Shutdown while a runner is blocked and after Run already returned.
- [x] Do not broaden into a global lifecycle refactor unless a separate approved task is created.

## Phase 4 — data safety and observability

- [x] Keep cursor/log writes absent for timeout/cancel/output_limit.
- [x] Add closed metrics for dedup, queue rejection, in-flight and fetch outcomes.
- [x] Add metric/log tests that reject sensitive/high-cardinality labels and raw SSH output.
- [x] Re-run existing parser/sanitizer/cursor/retention tests unchanged.

## Phase 5 — specs and operator runbook

- [x] Add executable nodelogs cancellation/single-flight/shutdown contract to backend quality or
  logging spec.
- [x] Document historical production-disabled evidence and single-node/batch re-enable checklist in
  this task's acceptance research; do not expose host details.
- [x] Do not edit production config or claim collection has resumed.

## Phase 6 — verification

- [x] `cd backend && go test ./internal/nodelogs -count=50`.
- [x] `cd backend && go test -race ./internal/nodelogs -count=10`.
- [x] Related `sshutil`, credential-audit and lifecycle tests.
- [x] Full backend test/build/lint/vet (environment-specific fixture reruns recorded in research).
- [x] Privacy/source scans, task validation, `git diff --check`.
- [x] Independent review of goroutine ownership, close ordering and every error exit.

## Phase 7 — delivery and production handoff

- [x] Commit/PR only after gates; monitor required CI to green (PR #533, all 11 passed).
- [x] Merge and monitor release/image automation; v0.55.14 evidence/digest is in
  `research/recent-resume-2026-09-16.md`.
- [ ] Separately verify current production settings and preserve disabled sources during an authorized upgrade.
- [x] Hand the single-node re-enable checks to the user; NAS observations identified
  the bounded-recovery follow-up above. Batch enablement remains deferred.
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
