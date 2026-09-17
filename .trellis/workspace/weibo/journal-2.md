# Journal - weibo (Part 2)

> Continuation from `journal-1.md` (archived at ~2000 lines)
> Started: 2026-09-07

---



## Session 52: Release CI token assertion stability and coverage diagnostics

**Date**: 2026-09-07
**Task**: Release CI token assertion stability and coverage diagnostics
**Branch**: `fix/preview-release-ci-stability`

### Summary

Release PR503 exposed an intermittent frontend intermediate-state assertion and a backend coverage script that hid its inner go-test failure output. Kept production code and coverage thresholds unchanged; asserted token reload security behavior instead of idle/loading timing and streamed test diagnostics with pipefail.

### Main Changes

- Preserve no-ticket-before-new-token-load and old-data clearing assertions without pinning transient loading state.
- Stream coverage test stdout/stderr to CI and retain the original failing exit code.

### Git Commits

| Hash | Message |
|------|---------|
| `2d3f6dd8` | (see git log) |

### Testing

- [OK] Frontend 99-test file passed; focused token reload regression passed 10 repeated runs; npm dependencies restored with npm ci.
- [OK] Actual backend coverage gate passed at 73.8% with unchanged 55% floor; synthetic failure smoke preserved exit23 and visible failure output.

### Status

[OK] **Completed**

### Next Steps

- Follow the associated CI follow-up PR and generated release PR503 through Docker Hub publication; use synchronized main for later development.


## Session 53: Drill lifecycle recovery completion assertion

**Date**: 2026-09-07
**Task**: Drill lifecycle recovery completion assertion
**Branch**: `test/drill-lifecycle-ci-stability`

### Summary

Updated Release PR503 race tests exposed a pre-existing drill test that observed terminal database rows before process ownership cleanup. Reused the neighboring bounded-wait pattern to await both conditions without changing production recovery logic, the deadline, or the polling interval.

### Main Changes

- Preserve persistent failure ownership retention and atomic TaskRun/Evidence terminal assertions; wait until the worker also releases local ownership.

### Git Commits

| Hash | Message |
|------|---------|
| `50c511e8` | (see git log) |

### Testing

- [OK] Go1.26.6 focused race test passed 100 repetitions.
- [OK] Full internal/task package passed under the race detector.

### Status

[OK] **Completed**

### Next Steps

- Merge the associated test-only follow-up and refresh generated Release PR503; continue publication monitoring to Docker Hub.


## Session 54: Deterministic export ciphertext tamper regression

**Date**: 2026-09-07
**Task**: Deterministic export ciphertext tamper regression
**Branch**: `test/ciphertext-tamper-ci-stability`

### Summary

Visible coverage diagnostics on post-merge main identified the tampered-body test writing a fixed 0xff into random ciphertext, which can leave the byte unchanged. The fixture now reads and flips the original byte, guaranteeing a real mutation while preserving all fail-closed and reservation charging assertions. Production cryptography and delivery code are unchanged.

### Main Changes

- Use XOR on the original ciphertext byte rather than writing a potentially identical fixed value.

### Git Commits

| Hash | Message |
|------|---------|
| `60e872f1` | (see git log) |

### Testing

- [OK] Focused malformed/tampered ciphertext gateway test passed with race detector for 300 repetitions.
- [OK] Entire backupasset/export package passed under race detector.

### Status

[OK] **Completed**

### Next Steps

- Merge the associated test-only PR, refresh generated Release PR503, and complete Docker Hub publication monitoring.


## Session 55: Rsync SQLite preview source consistency

**Date**: 2026-09-08
**Task**: Rsync SQLite preview source consistency
**Branch**: `fix/rsync-preview-source-consistency`

### Summary

Prepared mutable Rsync sources before initial preview and Retry without Connect; repaired drifted generation readiness, orphan catalog leases, fresh-generation CAS races, and Retry UI retention. Integration and Grok discovery/targeted verification passed for V552-1 through V552-4. Backend full tests/build/lint, targeted race count=10, frontend check and docs checks passed. Actual Chromium controlled-API flow verified separately from real SQLite/Rsync integration. No production deployment claimed.

### Git Commits

| Hash | Message |
|------|---------|
| `628b9e7b` | (see git log) |

### Status

[OK] **Completed**


## Session 56: Reconcile delivered Trellis tasks and remaining acceptance

**Date**: 2026-09-16
**Task**: Reconcile delivered Trellis tasks and remaining acceptance
**Branch**: `chore/trellis-task-reconciliation`

### Summary

Reviewed all 17 unarchived tasks against current source and merged delivery; archived 13 with history preserved and retained 4 with explicit next scope. Consolidated live backup acceptance without claiming production success; retained node-log P1 and independent P3 backlog.

### Main Changes

- Normalized task hierarchy and moved context references; preserved all 109 archive source files.

### Git Commits

| Hash | Message |
|------|---------|
| `886fd0c2` | (see git log) |

### Testing

- [OK] All 17 task validators, focused hierarchy/preservation checks, documentation freshness and diff checks passed; independent bounded review has no remaining material findings.

### Status

[OK] **Completed**

### Next Steps

- Monitor bookkeeping PR CI; production acceptance and retained backlog implementation remain separate work.


## Session 57: Node-log P1 implementation and PR 533

**Date**: 2026-09-16
**Task**: Node-log P1 implementation and PR 533
**Branch**: `codex/node-logs-collector-stall`

### Summary

Implemented owned SSH cancellation and strict output bounds, per-node queue claims, joined shutdown and bounded metrics; PR 533 awaits full CI.

### Git Commits

| Hash | Message |
|------|---------|
| `664555e6` | (see git log) |

### Testing

- [OK] nodelogs count50 and race10; related race; loopback SSH count20; backend build/vet/lint; all backend packages including environment-specific reruns; govulncheck; independent review passed

### Status

[OK] **Completed**

### Next Steps

- Monitor PR 533 CI and post-merge automation; production image deployment and collector re-enable remain separately unverified.


## Session 58: 节点日志近期恢复与脚本修复

**Date**: 2026-09-16
**Task**: 节点日志近期恢复与脚本修复
**Branch**: `codex/node-logs-recent-resume`

### Summary

User-approved one-hour/200-entry recovery replaces historical replay; shell framing and persistence evidence repaired. Local gates and independent review passed; PR/release and NAS acceptance remain pending.

### Main Changes

- Carry cursor timestamps; verify actual journal position; share deadline and cumulative bytes across probe/collection; preserve stored history.

### Git Commits

| Hash | Message |
|------|---------|
| `d95de83f` | (see git log) |

### Testing

- [OK] nodelogs count50 and race10; independent exact-budget/file-offset cases; full backend test/build/vet/lint with known filesystem fixture reruns; related SSH/audit/lifecycle race.

### Status

[OK] **Completed**

### Next Steps

- Monitor PR CI, merge and patch release/image publication. Keep task open for NAS single-node acceptance; no production SQL reset.


## Session 59: 节点日志 NAS 验收通过并归档

**Date**: 2026-09-16
**Task**: 节点日志 NAS 验收通过并归档
**Branch**: `codex/archive-node-logs-acceptance`

### Summary

User accepted v0.55.15 production scope: required journal node ingested 162 additional entries across nine completed fetches, one stale reset, no reported fetch errors and no queue backlog. Other nodes do not currently require collection. Archived existing P1; bookkeeping only, no release expected.

### Git Commits

| Hash | Message |
|------|---------|
| `d3101cf1` | (see git log) |
| `4a0c0aed` | (see git log) |

### Status

[OK] **Completed**


## Session 60: v0.55.16 backup assets production acceptance and program archive

**Date**: 2026-09-17
**Task**: v0.55.16 backup assets production acceptance and program archive
**Branch**: `docs/backup-assets-v05516-acceptance`

### Summary

Recorded user-confirmed Task 13 search/preview, completed-index recovery, UI/secret/retained-data checks and v0.55.16 NAS healthy/rest0. Archived existing acceptance and parent tasks; preserved historical evidence and explicit observation limits. Docs-only PR/CI required; P3 quality debt follows merge.

### Git Commits

| Hash | Message |
|------|---------|
| `4504bbe5` | (see git log) |

### Status

[OK] **Completed**


## Session 61: Complete bounded P3 middleware RAF and logging fixes

**Date**: 2026-09-17
**Task**: Complete bounded P3 middleware RAF and logging fixes
**Branch**: `fix/p3-quality-debt`

### Summary

Reused and archived the existing P3 task after implementing ordinary middleware envelopes, effect-owned RAF cleanup and per-Hub overflow log bounds. Backend focused race/lint/build and API tests pass; frontend full gate 1987 tests passes; independent check added protocol-safe log regressions. Existing repeated shared-SQLite fixture limitations recorded. Required PR/post-merge CI pending; no NAS deployment.

### Git Commits

| Hash | Message |
|------|---------|
| `29ce30b0` | (see git log) |

### Status

[OK] **Completed**
