# Independent Trellis Check Evidence — Search Startup Isolation

Date: 2026-08-26

## Findings first

- No Critical, Important, or Minor finding remained after an independent review
  of the complete task diff, active task artifacts, every `implement.jsonl` and
  `check.jsonl` reference, the updated backend quality/cross-layer specs, and the
  archived Catalog/Search designs. No reviewer code self-fix was required.
- Candidate-local `Build` outcomes remain locally owned: success, ordinary
  failure, caller-context cancellation, and lease-fence loss are classified in
  per-candidate metrics; every started goroutine is joined, the active-build
  gauge returns to zero, and only the caller context can escape fan-out. Config
  validation/source, abandoned reconciliation, overlay reconciliation, and
  candidate-list failures still propagate as pass-level infrastructure errors.
- Runtime readiness follows that ownership boundary: an isolated candidate
  failure leaves Search ready, while pass-level infrastructure and unexpected
  Search-key failures return an error and leave Search unready.
- Catalog interoperability remains deliberately narrow and case-sensitive:
  exact `sealed` maps to conservative `unknown`; `SEALED`, leading/trailing
  whitespace, and arbitrary future states retain `search_invalid_security_state`,
  write zero documents, and do not replace the old active projection.
- The positive fixture crosses the real producer path
  `provider.CatalogRecord -> catalog.Indexer.Build -> raw Catalog row ->
  search.Indexer.Build`. It proves the Catalog producer stores exact `sealed`,
  preserves an encrypted provider locator, retains ten immutable failed Search
  generations, activates complete generation 11, and does not mutate Catalog.
- The production diff contains no model/migration, Provider mutation, API, or
  frontend change. The sensitive-field/privacy scan found no prohibited data
  surface in the production diff.

## Independent verification

All commands below used the active worktree. Go static commands explicitly put
the Go 1.26.6 toolchain binary first in `PATH`, set `GOTOOLCHAIN=go1.26.6`, and
used task-scoped temporary directories.

```text
GOTOOLCHAIN=go1.26.6 go version
go version go1.26.6 linux/amd64

Search exact positive/negative selectors -count=1: PASS
runtime selected ownership/readiness matrix -count=10: PASS
Search selected producer/security matrix -count=10: PASS
Search selected producer/security matrix -race -count=1: PASS
go test ./internal/backupasset/search -count=1: PASS
complete current runtime package test binary -test.count=1: PASS

go vet ./internal/backupasset/runtime ./internal/backupasset/search
PASS (independent rerun, Go 1.26.6)

golangci-lint 2.11.4 run ./internal/backupasset/runtime ./internal/backupasset/search
0 issues. (independent rerun, Go 1.26.6)

gofmt -l <five changed Go files>
no output

git diff --check
PASS

child task JSON/JSONL plus parent task JSON parse
json_ok=true

production-diff scan for credentials, private keys, provider locators,
authorization/proofs/grants/tickets/session secrets/tokens/content/paths/filenames
no matches
```

The implementation-stage evidence also records the selected runtime race matrix
passing against the same sources. A second reviewer-side runtime race recompile
could not produce another large test binary because the shared environment's
user disk quota rejected linker/compiler output; this is recorded as an
environment limitation, not a second pass or a code finding. The independently
rerun exact/repeated runtime matrix and complete runtime-package binary both
passed.

## Explicit boundary

- `TEST_POSTGRES_DSN=unset`; no PostgreSQL result is claimed. The task has no
  schema change or SQL-dialect branch.
- No unnecessarily broad full-repository gate was run locally; CI owns it.
- No commit, push, PR, production mutation, feature re-enable, Task 3 restart,
  or node-log collector operation was performed by this reviewer.
