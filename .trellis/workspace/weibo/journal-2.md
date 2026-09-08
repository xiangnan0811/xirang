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
