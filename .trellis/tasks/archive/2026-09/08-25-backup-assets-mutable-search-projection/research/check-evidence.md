# Independent Check Evidence — Mutable Catalog Search Projection

## Findings

### Critical

- None.

### Important

- None. The mutable exception is limited to an eligible `mutable_head` whose
  Catalog has `ManifestID=nil`, `ExpectedEntryCount=0`, and non-negative counts.
  A manifest-less mutable Catalog with any positive expected count is rejected
  before lease acquisition or Search-generation creation.

### Minor — fixed

- The activation test matrix did not directly exercise the new exact point
  semantics freeze or Catalog manifest-identity freeze. Added activation-time
  mutations for both while leaving the resulting point/count products otherwise
  eligible. Each case returns the existing typed Search drift error, leaves zero
  active output, and persists one failed staging generation.

## Contract review

- Initial freeze accepts the unknown-count sentinel only for manifest-less
  mutable heads. Manifest-backed mutable and every non-mutable Catalog still
  require `WrittenEntryCount == ExpectedEntryCount`.
- `ExpectedDocumentCount` is frozen from the Catalog's non-negative
  `WrittenEntryCount`. Activation re-locks the exact point and Catalog, rechecks
  point source/semantics/state/eligibility, Catalog active/complete identity,
  source, written/expected counts, manifest identity, Search-key version, and
  the lease fence.
- Activation still requires physical Search document count == persisted written
  document count == frozen expected document count. Drift produces no active
  partial generation.
- The production diff adds no logging, API/schema/settings/Provider/frontend
  surface and no path, name, content, locator, credential, proof, ticket, grant,
  or secret output.
- The existing task PRD/design/root-cause evidence already records this narrow
  sentinel contract, so no shared `.trellis/spec/` change is required.

## TDD evidence review

- `research/implementation-evidence.md` records an exact pre-implementation RED:
  the real SQLite fixture had `ExpectedEntryCount=0`,
  `WrittenEntryCount=2`, returned `ErrSearchCatalogChanged`, and created zero
  Search generations.
- The pre-change diff condition rejected every written/expected mismatch, so the
  recorded fixture is a genuine behavioral regression rather than a synthetic
  assertion failure. The same focused test is GREEN after the narrow predicate.

## Fresh verification

All Go commands used Go 1.26.6 and isolated task-scoped temporary/build-cache
directories.

| Check | Result |
|---|---|
| Focused normal: mutable GREEN + fail-closed matrix + activation revalidation, `-count=1 -v` | pass |
| Focused repetition, `-count=20` | pass |
| Focused race, `-race -count=3` | pass |
| Whole `./internal/backupasset/search`, `-count=1` | pass |
| `go vet ./internal/backupasset/search` | pass |
| `golangci-lint` 2.11.4 on `./internal/backupasset/search` | pass, 0 issues |
| `gofmt -d` on the two changed Go files | pass, no diff |
| `git diff --check` | pass |
| Boundary-aware added-production-line privacy scan | pass, no match |
| `TEST_POSTGRES_DSN` | missing; required real PostgreSQL Search gate `not_executed` |
| Full backend / full repository gate | intentionally not executed by scoped review |

One initial privacy-scan command used an over-broad `log.` substring pattern and
matched the ordinary identifier fragment `catalog.`. That scanner command exited
1; the boundary-aware corrected scan was rerun in full and exited 0.
