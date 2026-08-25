# Independent Trellis check evidence — Catalog lifecycle race stability

## Findings first

### Important — fixed

- File: `/home/murray/code/xirang/.worktrees/catalog-lifecycle-race-stability/backend/internal/backupasset/catalog/indexer_test.go:739`
- Issue: failure cleanup canceled the build and closed the blocking-renewer release channel together. If cleanup ran before Provider enumeration entry, `catalogBlockingLeaseRenewer.Renew` could select the release arm instead of `ctx.Done()` and inject the fake `ErrLeaseFenceLost` before the phase the test claims to exercise.
- Fix: cleanup now cancels the build, releases the cancellation-ignoring Provider, requires the build/heartbeat to join, and closes the renewal barrier only after `buildExited`. The normal path still releases renewal only after both `blockingRenewer.entered` and `session.entered` have been observed.

### Minor — fixed

- File: `/home/murray/code/xirang/.worktrees/catalog-lifecycle-race-stability/backend/internal/backupasset/catalog/indexer_test.go:746`
- File: `/home/murray/code/xirang/.worktrees/catalog-lifecycle-race-stability/backend/internal/backupasset/catalog/source_lifecycle_test.go:186`
- Issue: the target tests' cleanup joins silently discarded their timeout branch, so a failure-path goroutine leak could be hidden behind the test's original failure.
- Fix: both target cleanup joins now report a bounded cleanup failure while preserving the existing one-second/three-second budgets. No timeout was increased and no sleep was added.

## Findings not fixed

- None.

## Spec compliance re-review

- The indexer test cannot release its fake renewal loss until heartbeat `Renew` and Provider enumeration entry are both observed. Its early-failure cleanup cannot manufacture lease loss before Provider entry.
- `catalogUnifiedTeardownLease.Renew` waits only for `providerEntered` or `ctx.Done()`; `providerJoined` remains confined to the exact final-release ordering assertion.
- Test-owned cancellation and `sync.Once` release functions make Provider/lease barriers idempotent. Both builders are joined on success and on failure cleanup, and cleanup join failures are visible.
- Existing assertions still prove `ErrLeaseFenceLost`, Provider cancellation and `Close`, durable failed generation evidence, one exact bounded lease release, and active-builder registry removal.
- Diff review found no production/runtime, schema, API, Swagger, settings, frontend, logging, metrics, security, release, or privacy change. No `.trellis/spec/` update is required for this test-only use of the existing channel/context synchronization convention.

## Verification

All successful Go gates used `GOTOOLCHAIN=go1.26.6` (the `backend/go.mod`/CI toolchain) with fresh task-owned `GOCACHE` and `TMPDIR` on the home filesystem.

```text
go test ./internal/backupasset/catalog -run '^(TestCatalogIndexerRenewalLossCancelsBuild|TestRecoveryPointSourceLifecycleCatalogWaitsForUnifiedBuilderTeardown)$' -count=1
ok  xirang/backend/internal/backupasset/catalog  0.385s

go test ./internal/backupasset/catalog -run '^(TestCatalogIndexerRenewalLossCancelsBuild|TestRecoveryPointSourceLifecycleCatalogWaitsForUnifiedBuilderTeardown)$' -count=100
ok  xirang/backend/internal/backupasset/catalog  37.709s

go test -race ./internal/backupasset/catalog -run '^(TestCatalogIndexerRenewalLossCancelsBuild|TestRecoveryPointSourceLifecycleCatalogWaitsForUnifiedBuilderTeardown)$' -count=50
ok  xirang/backend/internal/backupasset/catalog  24.126s

go test ./internal/backupasset/catalog -count=1
ok  xirang/backend/internal/backupasset/catalog  6.099s

go vet ./internal/backupasset/catalog
exit 0

golangci-lint v2.11.4 run ./internal/backupasset/catalog/...
0 issues.

gofmt -d internal/backupasset/catalog/indexer_test.go internal/backupasset/catalog/source_lifecycle_test.go
no output

git diff --check
exit 0
```

- Lint: pass.
- TypeCheck: pass (pinned `golangci-lint` type analysis and package compilation).
- Tests: pass for focused normal/repeated/race selectors and the complete Catalog package.
- The known environment-blocked multi-package command `go test -race ./internal/backupasset/... ./internal/api/handlers/ -count=1` was not retried locally, per the task boundary; CI remains the required proof for that exact gate.

`SPEC_COMPLIANCE_OK`

`QUALITY_OK`
