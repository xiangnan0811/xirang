# Implementation Evidence — Search Enable Async Convergence

## Scope and boundary

- `SearchWorker.PrepareWithConfig` reuses validation, abandoned reconciliation,
  Overlay reconciliation, and candidate enumeration with the caller's explicit
  prospective config. It never calls candidate `Build` and never reads the
  dynamic config source.
- The existing dynamic and explicit full-pass paths still own the complete,
  repository-fair, joined candidate fan-out and candidate-local failure
  isolation.
- `SearchWorker.TryWake` is a non-blocking capacity-one signal. The single
  lifecycle-owned `Run` loop drains a pre-Run token into its normal initial
  pass, then selects on cancellation, its long timer, or a wake. A woken pass
  re-reads committed dynamic configuration.
- Cold startup only prepares Search infrastructure and marks readiness. A hot
  enable queues one wake only after persistence, the durable enablement-success
  stamp, Search preparation, and Content readiness have succeeded. No handler or
  transition goroutine was added.

## Genuine RED evidence

Before production edits, the focused hot/cold behavioral selector was:

```text
env GOTOOLCHAIN=go1.26.6 \
  TMPDIR=/home/murray/.codex/tmp/search-enable-async/tmp \
  GOTMPDIR=/home/murray/.codex/tmp/search-enable-async/gotmp \
  GOCACHE=/home/murray/.cache/codex/search-enable-async/go-build \
  go test ./internal/backupasset/runtime \
  -run 'Test(FeatureTransitionHotEnableDoesNotOwnSearchCandidateBuild|ColdStartupSearchPreparationDoesNotOwnCandidateBuild)$' \
  -count=1
```

It failed exactly because both bounded paths entered candidate `Build`:

```text
--- FAIL: TestFeatureTransitionHotEnableDoesNotOwnSearchCandidateBuild
    hot enable synchronously entered candidate Build before returning: context canceled
--- FAIL: TestColdStartupSearchPreparationDoesNotOwnCandidateBuild
    cold startup synchronously entered candidate Build before returning: context canceled
FAIL
```

The pre-implementation worker contract selector then failed to compile at the
first missing `worker.wake`, followed by missing `PrepareWithConfig` and
`TryWake`. This fixed the new API and ownership contract before implementation.

## GREEN and negative matrix

All commands below used Go 1.26.6 and the task-scoped cache paths shown above.

- Original hot/cold selector: PASS, `ok .../runtime 0.081s`.
- Prepare/wake/coalescing/disabled/shutdown/failure selector: PASS,
  `ok .../runtime 0.157s`.
- Search preparation failure, success-stamp failure, hot enable, and cold
  startup selector: PASS, `ok .../runtime 0.112s`.
- Caller cancellation before the final wake: PASS, `ok .../runtime 0.053s`;
  wake count and candidate Build count both remained zero.
- Focused Search/runtime matrix covering all `SearchWorker`, runtime Search,
  Search transition, hot/cold, failure, and cancellation tests: PASS,
  `ok .../runtime 0.403s`.
- Repeated focused matrix `-count=20`: PASS,
  `ok .../runtime 4.597s`.
- Race-focused matrix `-race -count=5`: PASS,
  `ok .../runtime 4.147s`.
- Post-review wake subset, including a sleeping committed-disabled worker,
  pre-Run coalescing, hot-enable ordering, and shutdown, passed `-count=20`
  (`2.446s`) and `-race -count=3` (`2.029s`).
- Complete `./internal/backupasset/search`: PASS,
  `ok .../search 1.370s`.
- Settings HTTP boundary selectors
  `TestSettingsFailedTransitionLeavesEnabledOverrideUnchanged` and
  `TestSettingsEnabledTransitionDrainsBeforePersistingDatabaseValue`: PASS,
  `ok .../handlers 0.058s`. Together with the real Runtime blocking-candidate
  selector, these preserve HTTP 500 plus exact prior state for runtime failure
  and prompt HTTP 200 after successful bounded transition without exposing
  Runtime's unexported Search dependencies to the handler fixture.
- `go vet ./internal/backupasset/runtime ./internal/backupasset/search`: PASS.
- pinned `golangci-lint` v2.11.4 on the same packages: PASS, `0 issues.`.
- `gofmt -d` over every touched Go file: empty output.
- `git diff --check`: PASS.
- Production identifiers, addresses, ports, repository/recovery-point IDs, and
  production row-count literals scan over touched Go files: no matches.

## Accurate local limits

The implementation pass's direct complete
`./internal/backupasset/runtime -count=1` invocation found exactly one failing
test: `TestRuntimeAuthenticatedCacheUsesSharedContentMetrics`, whose local
temporary cache root was rejected as `cache_root_unverified`. The independent
review then compiled `runtime.test` with task-scoped home TMP/GOTMP/GOCACHE and
ran that binary under `/dev/shm/codex-search-enable-async-check-runtime`; the
complete package finished `PASS` with exit code 0. The earlier environment
failure is retained as history, not converted retroactively into a pass.

`TEST_POSTGRES_DSN` is absent in this worktree environment. PostgreSQL is
recorded as `not_executed`, never pass/skip; this task changes no schema or SQL.

## Independent Trellis check

The independent reviewer inspected the complete production/test/spec diff and
self-fixed three issues inside the assigned scope:

- The hot-enable regression test previously did not run the lifecycle worker,
  so it proved only that a wake token was queued. It now starts `Run`, observes
  the woken candidate `Build` block, and proves the transition returns
  successfully before that worker-owned Build is released or canceled. It also
  proves exactly one synchronous infrastructure prepare and one background full
  pass.
- The permanent quality-spec Correct example previously duplicated
  `PrepareWithConfig`/`TryWake` outside Runtime. It now shows Runtime as the sole
  preparation/wake owner.
- The child design used the obsolete `Wake` name; it now matches `TryWake`.

Post-fix independent results, all with Go 1.26.6 and fresh reviewer-scoped
TMP/GOTMP/GOCACHE unless stated otherwise:

- exact strengthened hot-enable selector: PASS, `ok .../runtime 0.071s`;
- focused runtime matrix `-count=20`: PASS, `ok .../runtime 4.887s`;
- focused race matrix `-race -count=3`: PASS,
  `ok .../runtime 2.592s`, no race report;
- complete Search package: PASS, `ok .../search 1.726s`;
- complete compiled runtime test binary under the task-owned `/dev/shm` root:
  PASS, exit code 0;
- settings failure and success HTTP-boundary selectors in separate fresh
  processes: PASS (`0.057s` and `0.058s` respectively);
- `go vet` on affected packages: PASS;
- pinned `golangci-lint` 2.11.4: PASS, `0 issues.`;
- `gofmt -d`, `git diff --check`, JSON/JSONL validation, production-identifier
  privacy scan, and new-production-code anti-pattern scan: PASS/no matches.

An exploratory same-process handler run with `-count=20` is not recorded as a
gate pass: its success-path fixture retained the prior iteration's in-memory
SQLite row because the existing shared-memory DSN is derived only from
`t.Name()`. Both required handler selectors pass independently with `-count=1`;
the repeat-fixture isolation issue is outside this child's allowed handler-file
scope and does not alter the production HTTP mapping.

Direct compilation in `/dev/shm` first exhausted that filesystem's quota and is
not recorded as a gate result. Compiling with home caches and using `/dev/shm`
only as the runtime temporary root is the successful complete-package evidence
above.

Delivery, CI, release, and production acceptance are owned by the main session
and remain open.
