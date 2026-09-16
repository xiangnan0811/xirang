# Task reconciliation — 2026-09-16

This disposition supersedes stale current-state, delivery and execution instructions in this task; old evidence remains historical. User authorized evidence-based archival and consolidation on 2026-09-16.

Retain P1: current nodelogs still lacks post-dial cancellation, hard output-limit detection, per-node deduplication and worker join. Reuse shared SSH execution where compatible. Product repair can be planned independently; production re-enable remains separately gated.

This task remains open; assessment does not authorize a production change or imply new implementation has run.

Full inventory and evidence: `.trellis/workspace/weibo/task-reconciliation-2026-09-16.md`. No product code, production configuration or Provider data was changed by this reconciliation. No new task was created.

## Necessity and refreshed next implementation scope

**Keep P1; this is an unresolved reliability defect, not a stale completed task.**
Current-source assessment found:

- `nodelogs/ssh_runner.go:29-74`: timeout reaches DialSSH, but NewSession/Start/
  ReadAll/Wait have no cancellation owner; `LimitReader(maxBytes)` cannot detect
  truncation, and deferred Close cannot unblock an already blocked Wait.
- `nodelogs/scheduler.go:35-87`: no per-node queued/in-flight claim, no internal
  cancellation/worker join; done only denotes scheduling-loop completion.
- `nodelogs/prom.go` and `fetcher.go`: required dedup/rejection/in-flight/shutdown
  metrics and distinct output-limit/canceled classification remain absent.
- `nodelogs/worker.go:43` and its tests already preserve the cursor on ordinary
  fetch failure. Extend this to timeout/cancel/limit cases rather than replacing
  the existing data flow.

Plan the next implementation in this order:

1. Evaluate `sshutil.NewSSHCommandRunnerWithTransportClose` and
   `OpenRawExecution` (`command_runner.go:135,424,470`) for owned cancellation,
   output bounds and close/join. Preserve nodelogs' explicit nonzero-exit/output
   compatibility; merely switching to a generic Run is not sufficient.
2. Add exact-limit versus limit+1 handling and typed cancellation/error mapping,
   with blocked stdout/Start/Wait regression tests and no partial payload commit.
3. Add per-node queue claims, owned scheduler cancel, worker join and release on
   every failure path. Verify queue saturation recovery and idempotent shutdown.
4. Add low-cardinality aggregate telemetry and unchanged-cursor tests; run the
   existing PRD's repeated/race and backend gates before delivering a fixed image.
5. Production restoration stays separate: verify current state, then one
   authorized low-risk node for at least two cycles before staged restoration.
   Do not infer today's collector state from the August incident record.

The old design remains useful but its instruction to build an independent SSH
lifecycle must first be reconciled with the shared runner now present. There is
no technical dependency on backup preview acceptance for code planning/testing;
the historical operational sequencing must not hide the unresolved defect.
This task remains planning: the current user request authorized assessment and
task cleanup, not implementation of every retained backlog item or production
collector changes.

## Previous metadata note (historical)

Planning complete pending user approval. Pre-existing defect: SSH context stops at dial/handshake, ReadAll/Wait can block forever, scheduler duplicates the same nodes, and Shutdown does not join workers. Plan adds whole-operation close-and-join, strict output limit, per-node single-flight, owned cancellation, aggregate telemetry, and controlled production re-enable. Production collectors remain disabled until a fixed release is verified.
