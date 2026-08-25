# Implementation evidence — Catalog lifecycle race stability

## Findings first

- No independent production defect was found. The stabilization is test-only and leaves the Catalog heartbeat, lease, generation, Provider session, active-build registry, and source-lifecycle implementation unchanged.
- The two affected tests now inject `ErrLeaseFenceLost` only after their Provider session has entered canonical enumeration. Both waits have a context or bounded test exit, and failure cleanup cancels and joins the test-owned build before returning.
- The exact broad CI race selector remains locally blocked by the runner's temporary-filesystem constraints. The target Catalog package passed inside the first broad run, but the overall command could not complete cleanly; this gate is deliberately not marked complete.

## Immutable RED baseline

The genuine pre-change REDs are the two independent GitHub Actions failures already captured in `root-cause.md`:

- run `32811380329`, job `97691289454`: `TestCatalogIndexerRenewalLossCancelsBuild` timed out waiting for Catalog enumeration cancellation;
- run `32814904444`, job `97701179482`: `TestRecoveryPointSourceLifecycleCatalogWaitsForUnifiedBuilderTeardown` timed out waiting for Provider enumeration entry.

Both jobs ran:

```text
go test -race ./internal/backupasset/... ./internal/api/handlers/ -count=1
```

Before the patch, the unchanged combined selector passed once locally in both modes:

```text
go test ./internal/backupasset/catalog -run '^(TestCatalogIndexerRenewalLossCancelsBuild|TestRecoveryPointSourceLifecycleCatalogWaitsForUnifiedBuilderTeardown)$' -count=1
ok  xirang/backend/internal/backupasset/catalog  0.384s

go test -race ./internal/backupasset/catalog -run '^(TestCatalogIndexerRenewalLossCancelsBuild|TestRecoveryPointSourceLifecycleCatalogWaitsForUnifiedBuilderTeardown)$' -count=1
ok  xirang/backend/internal/backupasset/catalog  1.505s
```

This local pass is consistent with the diagnosed scheduler race and is not evidence against the two immutable CI REDs. No additional deterministic local RED can honestly be produced without changing production behavior or adding a synthetic timing seam, so none was invented.

## Minimal GREEN change

- `TestCatalogIndexerRenewalLossCancelsBuild` wraps `catalogFailingLeaseRenewer` with the existing context-aware `catalogBlockingLeaseRenewer`, observes heartbeat renewal and Provider enumeration entry, and only then releases the renewal failure.
- `catalogUnifiedTeardownLease.Renew` waits on `providerEntered` or `ctx.Done()`. It closes `renewed` and returns `ErrLeaseFenceLost` only after Provider entry. `providerJoined` remains used only by the final-release ordering assertion.
- Both tests use test-owned build cancellation and bounded cleanup joins. The indexer test's renewal and Provider release channels are once-protected, including failure cleanup.
- No timeout was increased and no sleep was added.

## Post-change verification

All commands below ran from `backend/` with task-scoped temporary directories.

```text
go test ./internal/backupasset/catalog -run '^(TestCatalogIndexerRenewalLossCancelsBuild|TestRecoveryPointSourceLifecycleCatalogWaitsForUnifiedBuilderTeardown)$' -count=1
ok  xirang/backend/internal/backupasset/catalog  0.386s

go test ./internal/backupasset/catalog -run '^(TestCatalogIndexerRenewalLossCancelsBuild|TestRecoveryPointSourceLifecycleCatalogWaitsForUnifiedBuilderTeardown)$' -count=100
ok  xirang/backend/internal/backupasset/catalog  38.138s

go test -race ./internal/backupasset/catalog -run '^(TestCatalogIndexerRenewalLossCancelsBuild|TestRecoveryPointSourceLifecycleCatalogWaitsForUnifiedBuilderTeardown)$' -count=50
ok  xirang/backend/internal/backupasset/catalog  24.090s

go test ./internal/backupasset/catalog -count=1
ok  xirang/backend/internal/backupasset/catalog  5.955s

go vet ./internal/backupasset/catalog
exit 0
```

`gofmt` completed on both edited Go tests, and the focused `git diff --check` returned exit 0.

## Broad CI race selector blocker

The exact broad selector was attempted without changing its package or test selection:

```text
go test -race ./internal/backupasset/... ./internal/api/handlers/ -count=1
```

- With task temp space on the home filesystem, `internal/backupasset/catalog` passed in `13.447s`, but unrelated filesystem-safety and Unix-socket tests rejected that non-dedicated/long temp path.
- With a CI-compatible task directory on `/tmp`, both the normal invocation and a package-serialized retry (`GOFLAGS=-p=1`) failed with `disk quota exceeded` while linking race binaries or writing the existing 320 MiB updater fixture. The tmpfs had approximately 2.8 GiB available.
- The main session also separated a short `/tmp` runtime root from a large `/var/tmp` build root. The Go test runner still places per-test roots under its build work directory: the large filesystem satisfies capacity but reports no usable inode evidence and produces overlong Unix-socket paths, while the short tmpfs hits the same runner quota during multi-package race linking. Both task-created roots were empty after Go cleanup and were removed with exact `rmdir` targets.

This is an environment-only verification gap. No unrelated test, timeout, production implementation, or fixture was changed to bypass it.

The main session also attempted the remaining full backend gates under Go 1.26.6. `go test ./...` and serialized `go build -p=1 ./...` both stopped during compile/link with the same runner-level `disk quota exceeded` before relevant tests or binaries could execute. Exact task-created empty temp directories were removed with `rmdir`. These attempts are not counted as GREEN; the PR's clean GitHub Actions environment remains the authoritative full-backend test/build/race proof.

## Self-review

- Diff scope is limited to the two approved Catalog test files plus this task's evidence/checklist.
- Production code, schema, API, Swagger, settings, frontend, logging, metrics, security, and privacy contracts are unchanged.
- Renewal failure cannot be released before both required entry observations in the indexer test.
- Unified teardown renewal never waits for `providerJoined`; only final lease release uses that signal.
- Failure paths cancel the build, release test barriers exactly once, and wait for build exit with the existing timeout budget.
