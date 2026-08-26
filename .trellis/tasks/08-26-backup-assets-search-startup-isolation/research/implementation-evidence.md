# Implementation Evidence — Search Startup Isolation

Date: 2026-08-26

## Findings first

- Before the production change, one ordinary candidate-local `Build` error
  escaped `StartupPassWithConfig`, and the runtime startup boundary returned the
  same error instead of leaving Search ready.
- Before the production change, a SQLite Catalog row with exact
  `security_state=sealed` produced a failed/inactive Search generation with
  stable code `search_invalid_security_state`, expected count 1, written count
  0, and zero documents.
- The final positive fixture now crosses the real producer boundary:
  `provider.CatalogRecord` -> `catalog.Indexer.Build` -> raw Catalog row ->
  `search.Indexer.Build`. It proves the Catalog producer writes exact `sealed`,
  the raw provider locator remains encrypted and byte-identical, Search maps the
  value to sensitivity `unknown`, and Search does not mutate the Catalog row.
- The worker now joins every started candidate and returns only caller context
  failure from candidate fan-out. Configuration, key, abandoned reconciliation,
  overlay reconciliation, and candidate-list failures still propagate.
- Exact `sealed` is accepted case-sensitively. `future_security`, `SEALED`,
  leading-space `sealed`, and trailing-space `sealed` remain rejected before
  any Search document is written, without replacing an old active projection.
- Ten existing failed Search generations are retained; successful projection
  activates generation 11 with expected=written=1.

## Genuine RED evidence

All Go commands used Go 1.26.6 and task-scoped temporary/cache directories.

```text
go test ./internal/backupasset/runtime -run '^TestSearchWorkerStartupPassIsolatesCandidateBuildFailures$' -count=1 -v
--- FAIL: TestSearchWorkerStartupPassIsolatesCandidateBuildFailures
behavioral RED: candidate-local Build failure escaped StartupPassWithConfig: FAKE_SEARCH_CANDIDATE_BUILD_FAILURE_FOR_TEST_ONLY

go test ./internal/backupasset/runtime -run '^TestRuntimeSearchStartupKeepsReadyAfterCandidateBuildFailure$' -count=1 -v
--- FAIL: TestRuntimeSearchStartupKeepsReadyAfterCandidateBuildFailure
behavioral RED: candidate-local Build failure escaped runtime Search startup: FAKE_RUNTIME_SEARCH_CANDIDATE_FAILURE_FOR_TEST_ONLY

go test ./internal/backupasset/search -run '^TestIndexerSealedCatalogProjectsConservativeUnknownAndActivates$' -count=1 -v
--- FAIL: TestIndexerSealedCatalogProjectsConservativeUnknownAndActivates
behavioral RED: exact sealed Catalog rejected: error=invalid backup asset search security state state="failed" code="search_invalid_security_state" expected=1 written=0 active=false documents=0
```

Production files were unchanged when these failures were captured. The initial
SQLite RED inserted the Catalog row directly so the existing failure shape
could be captured before production edits; the final GREEN replaced that setup
with the real cross-package Catalog producer chain described above.

## Production changes

- `runtime/search_worker.go`: removed candidate-error return aggregation; metric
  classification and goroutine join remain, and the fan-out returns `ctx.Err()`.
- `search/indexer.go`: added only exact `sealed` -> `SensitivityUnknown` mapping.
- Catalog producer/writer, Search projection lifecycle, stable error mapping,
  source/count/key/fence checks, and activation code were not changed.

## Verification evidence

Toolchain:

```text
GOTOOLCHAIN=go1.26.6 go version
go version go1.26.6 linux/amd64
```

Final focused selector matrix:

```text
go test ./internal/backupasset/runtime ./internal/backupasset/search -run '^(TestSearchWorkerStartupPassIsolatesCandidateBuildFailures|TestSearchWorkerSchedulesRepositoryFairAndJoinsCanceledBuilds|TestSearchWorkerPropagatesPassLevelFailures|TestRuntimeSearchStartupKeepsReadyAfterCandidateBuildFailure|TestRuntimeSearchStartupPropagatesInfrastructureFailuresAndKeepsUnready|TestRuntimeSearchStartupUnexpectedUnwrapFailureIsFatal|TestRuntimeSearchStartupEnsuresKeyReconcilesAndTreatsRecordedLossAsUnavailable|TestIndexerSealedCatalogProjectsConservativeUnknownAndActivates|TestIndexerInvalidSecurityFailsAndPreservesOldActiveProjection)$' -count=1
ok xirang/backend/internal/backupasset/runtime
ok xirang/backend/internal/backupasset/search
```

Focused repetition and race:

```text
runtime selected matrix -count=10: PASS
search selected matrix -count=10: PASS
runtime selected matrix -race -count=1: PASS
search selected matrix -race -count=1: PASS
final real Catalog-producer Search matrix -count=10: PASS
final real Catalog-producer Search matrix -race -count=1: PASS
```

Affected packages:

```text
go test ./internal/backupasset/search -count=1
ok xirang/backend/internal/backupasset/search

go test -c -o /home/murray/.cache/xirang-search-startup-isolation.qAZfrJ/runtime.test ./internal/backupasset/runtime
PASS (compiled the complete runtime package test binary with Go 1.26.6)

TMPDIR=/dev/shm/xirang-search-startup-isolation/tmp /home/murray/.cache/xirang-search-startup-isolation.qAZfrJ/runtime.test -test.count=1
PASS
```

The runtime package required split compile/run evidence because its existing
authenticated-cache fixture deliberately rejects a cache root on the `/home`
mount (`cache_root_unverified`). Direct compilation/linking under `/tmp` hit the
environment's user quota, and linking the large runtime test binary under
`/dev/shm` exceeded currently available tmpfs space. Compiling with the
task-scoped `/home` Go cache and running the resulting complete test binary with
task-scoped `/dev/shm` `TMPDIR` exercises the full runtime package and passes.

Static gates:

```text
go vet ./internal/backupasset/runtime ./internal/backupasset/search
PASS

golangci-lint --version
golangci-lint 2.11.4

golangci-lint run ./internal/backupasset/runtime ./internal/backupasset/search
0 issues.

gofmt -l <five changed Go files>
no output

git diff --check
PASS

production-diff sensitive-field scan for credential/password/private key/provider locator/authorization proof/grant/ticket/session secret
no matches
```

## Explicitly unavailable or not run

- `TEST_POSTGRES_DSN=unset`; no PostgreSQL gate was claimed. This change has no
  schema or SQL-dialect branch.
- No full-repository gate was run, per the bounded implementation instruction.
- No commit, push, PR, release, production mutation, feature re-enable, Task 3
  restart, or node-log collector operation was performed.
- Independent Trellis check, CI, release, and guarded production acceptance
  remain for the main session/delivery phases.
