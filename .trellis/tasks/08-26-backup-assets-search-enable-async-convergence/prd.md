# Backup Assets Search Enable Async Convergence

## Goal

Allow guarded backup-assets enablement and cold startup to complete within the
bounded runtime transition while a large, valid Search projection converges in
the existing background worker. Preserve fail-closed infrastructure readiness,
durable failed-generation evidence, Search atomic activation, and production
data privacy.

## Production evidence

- Production runs healthy v0.50.8 at revision
  `d9db1d98fa5e33c54a83bad72d69217c98a7784d`, restart count zero, schema
  `72|0`, and SQLite `quick_check=ok`.
- GA readiness is acknowledged with the current 64-character inventory digest;
  Task 3 is canceled/disabled, global active TaskRuns are zero, and node-log
  collectors remain zero.
- The active mutable Catalog generation 50 is complete with 60,515 durable
  Catalog rows and no manifest-backed expected count.
- Formal authenticated `PUT /api/v1/settings` to set
  `backup_assets.enabled=true` returned HTTP 500 and compensated the setting
  back to `false`; repository/Catalog APIs correctly remained feature-disabled.
- Search generation 11 proves the current code reached the corrected projection:
  it failed inactive with `search_build_timeout` after 24,979 ms, with
  expected=60,515 and written=physical=8,000. There is no active Search lease,
  the active Search key exists, and prior generations 1-10 remain failed with
  `search_invalid_security_state`.
- This exactly matches the 25-second feature-transition ceiling and proves that
  synchronous candidate fan-out, rather than configuration, key, Catalog,
  security-state mapping, database integrity, or Provider access, blocked
  enablement.

## Requirements

- Hot enable and cold startup MUST synchronously validate Search worker config,
  Search key readiness, abandoned-generation reconciliation, overlay
  reconciliation, and candidate enumeration before marking Search ready.
- Hot enable and cold startup MUST NOT synchronously execute candidate Search
  `Build` operations inside the bounded transition/startup readiness path.
- After a hot enable is fully committed—including persistence, success stamp,
  Content readiness, and every fallible transition stage—the existing
  background Search worker MUST be signaled without blocking so a sleeping
  disabled worker begins a convergence pass promptly.
- Failed or compensated transitions MUST NOT signal candidate work. A queued
  signal that observes committed disabled config MUST perform zero backend work.
- Cold startup MUST rely on the lifecycle worker's normal initial pass and MUST
  NOT enqueue a duplicate wake.
- Wake requests MUST be bounded/coalesced and MUST NOT create unowned goroutines,
  concurrent worker loops, or bypass worker shutdown/cancellation/join behavior.
- Dynamic background passes MUST continue to execute the complete existing
  flow: infrastructure reconciliation, candidate enumeration, joined candidate
  fan-out, metrics, durable failure evidence, and context propagation.
- Configuration, key, abandoned-generation, overlay, candidate-list, caller
  cancellation, and readiness errors MUST remain enablement/startup failures.
- Candidate-local Build failures MUST remain isolated as delivered in v0.50.8;
  failed or partial Search output MUST never activate.
- Failed generations 1-11 and the 8,000 generation-11 staging documents MUST be
  preserved. The next normal generation MUST rebuild from the active Catalog
  and activate only after all 60,515 documents pass existing count, identity,
  key, fence, and source checks.
- No transition timeout increase, schema/API/frontend change, manual SQL repair,
  failed-row deletion, synthetic generation, Provider enumeration, provider-data
  write, Task 3 restart, or node-log collector change is allowed.

## Acceptance criteria

- [x] Genuine focused RED proves hot enable invokes a blocking candidate Build,
  exceeds the caller/operation deadline, returns failure, and does not persist
  enabled state before the fix.
- [x] Genuine focused RED proves cold startup synchronously invokes candidate
  Build before the fix.
- [x] GREEN proves hot enable and cold startup run infrastructure preparation but
  perform zero synchronous candidate Builds and leave Search ready.
- [x] A sleeping background worker is deterministically woken after successful
  enablement and executes candidate fan-out without waiting for its long timer.
- [x] Persistence, success-stamp, Content-readiness, and compensation failures
  emit zero wakes; a queued wake with committed disabled config executes zero
  candidate Builds; cold startup emits no duplicate wake.
- [x] Repeated wake requests coalesce; cancellation/shutdown joins cleanly and
  active-build metrics return to zero.
- [x] Infrastructure/config/key/list/context failure matrices remain fail closed;
  candidate-local failure isolation remains unchanged.
- [x] Focused repetition/race, complete affected runtime/Search packages, Go
  1.26.6 vet, pinned golangci-lint, gofmt, privacy/static scans, and diff checks
  pass with accurately recorded environment limits.
- [x] Independent Trellis check has no unresolved Critical/Important findings.
- [ ] PR CI is fully green, merged, and the next stable release plus amd64/arm64
  Docker publication completes before production upgrade.
- [ ] Guarded production upgrade keeps backup assets disabled until image health,
  database integrity, Task 3, active-run, collector, Catalog, and Search evidence
  pass.
- [ ] Formal Settings API enablement returns HTTP 200 promptly; generation 12 is
  created by the background worker, becomes active/complete with 60,515 physical
  documents, and failed generations 1-11 remain inactive evidence.
- [ ] Exact-point file search returns HTTP 200 with a real opaque AssetRef; UI
  selection proves metadata and supported content preview without exposing
  credentials, locators, paths, names, or content in diagnostic output.
- [ ] Container remains healthy with zero critical/collector errors; only then
  may the parent gate advance to node-log P1.

## Out of scope

- Making Search generation builds resumable across failed generations.
- Weakening Catalog/Search count, security, identity, key, lease, or activation
  invariants.
- Re-enabling Task 3 or node-log collectors before real-data preview acceptance.
