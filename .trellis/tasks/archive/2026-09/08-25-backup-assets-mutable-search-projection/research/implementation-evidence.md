# Implementation Evidence — Mutable Catalog Search Projection

## Behavioral RED

- Date: 2026-08-25.
- Toolchain: Go 1.26.6.
- Fixture: real in-memory SQLite through the Search Indexer harness; one eligible
  `mutable_head` / `observed` point, one active complete manifest-less Catalog,
  `ExpectedEntryCount=0`, `WrittenEntryCount=2`, and two opaque Catalog entries.
- Exact selector:

  ```text
  env GOTOOLCHAIN=go1.26.6 TMPDIR=/home/murray/.codex/tmp/xirang-mutable-search/tmp GOTMPDIR=/home/murray/.codex/tmp/xirang-mutable-search/gotmp GOCACHE=/home/murray/.cache/codex/xirang-mutable-search/go-build go test ./internal/backupasset/search -run '^TestIndexerManifestlessMutableCatalogProjectionActivates$' -count=1 -v
  ```

- Result: exit 1 before any production-code edit.
- Behavioral failure: `ErrSearchCatalogChanged` with zero Search generations;
  the fixture expected one active complete two-document Search generation.

## GREEN and verification

### Implementation

- Added one package-local Catalog count-readiness predicate and applied it at
  initial freeze and activation revalidation.
- The exception is limited to an eligible manifest-less `mutable_head` with
  `ExpectedEntryCount=0` and non-negative `WrittenEntryCount`.
- `ExpectedDocumentCount` remains frozen from `WrittenEntryCount`; activation
  still requires physical, written, and expected Search document counts to be
  equal.
- Activation additionally rejects point semantics/state, Catalog expected-count,
  and manifest identity drift from the frozen projection.

### Fresh local results

All commands below used Go 1.26.6 and the task-scoped `TMPDIR`, `GOTMPDIR`, and
`GOCACHE` paths shown in the RED command unless the command does not use Go.

| Check | Command summary | Result |
|---|---|---|
| Exact GREEN | `go test ./internal/backupasset/search -run '^TestIndexerManifestlessMutableCatalogProjectionActivates$' -count=1 -v` | exit 0 |
| Exact regression and negative matrix | `go test ./internal/backupasset/search -run '^(TestIndexerManifestlessMutableCatalogProjectionActivates|TestIndexerCatalogReadinessRemainsFailClosed|TestIndexerActivationRevalidatesMutableCatalogAndDocumentCounts)$' -count=1 -v` | exit 0 |
| Repetition | same three selectors with `-count=20` | exit 0 |
| Race | same three selectors with `-race -count=3` | exit 0 |
| Whole Search package | `go test ./internal/backupasset/search -count=1` | exit 0 |
| Vet | `go vet ./internal/backupasset/search` | exit 0 |
| Pinned lint | `golangci-lint 2.11.4`; `golangci-lint run ./internal/backupasset/search` | exit 0, `0 issues` |
| Formatting | `gofmt -d` on the two changed Go files | exit 0, no diff |
| Whitespace | `git diff --check` | exit 0 |
| Privacy/source | new production diff scan for logging, raw errors, Provider locators, credentials, path/name, or content surfaces | exit 0, no match |

The negative matrix covers manifest-backed mutable mismatch, manifest-less
non-mutable mismatch, mutable source drift, ineligible mutable state, negative
written count, unexpected manifest-less expected count, activation-time expected
count drift, activation-time eligible point state drift, and physical Search
document count drift. Rejected activation produces zero active output and one
durable failed staging generation.

### Explicitly not executed

- `TEST_POSTGRES_DSN` was not configured. Required real PostgreSQL Search tests
  are `not_executed`; no skipped PostgreSQL test is claimed as evidence.
- Full backend and full repository gates were not run because this child is
  restricted to the Search package.
- Independent `trellis-check` completed with its one Minor test-coverage finding
  fixed and all focused gates rerun. Commit, push, PR, CI, merge, release, and
  production acceptance remain pending outside this implementation handoff.

### Self-review

- No schema, API, settings, Provider, runtime-composition, frontend, generated,
  parent-task, or user-config file was edited by this implementation.
- No new logging, error text, path/name/content, locator, credential, proof,
  grant, ticket, or secret surface was introduced.
- The worktree's pre-existing parent task modification and other child task
  artifacts were preserved.
