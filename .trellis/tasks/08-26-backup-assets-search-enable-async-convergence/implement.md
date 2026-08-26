# Implementation Plan — Search Enable Async Convergence

## Phase 1 — Contract and genuine RED

- [x] Record production generation-11 timeout/count/duration and compensated
  enablement evidence in `research/root-cause.md`.
- [x] Add a runtime hot-enable selector with a blocking candidate Build and a
  short caller deadline; capture the current deadline failure and unchanged
  enabled setting before production edits.
- [x] Add a cold-startup selector proving current startup synchronously enters
  candidate Build.
- [x] Add SearchWorker preparation and wake contract tests before changing
  production code, including infrastructure failures, no synchronous Build,
  wake coalescing, immediate long-timer wake, cancellation, and shutdown.

## Phase 2 — Minimal implementation

- [x] Extract/reuse the pass-level validation/reconcile/list sequence in an
  infrastructure-only `PrepareWithConfig` operation; keep full dynamic and
  explicit passes behaviorally unchanged.
- [x] Add one worker-owned buffered wake channel and non-blocking wake method;
  integrate it into the existing single Run loop without spawning work.
- [x] Change Runtime Search startup/hot enable to prepare and mark ready rather
  than synchronously call candidate fan-out; cold startup emits no wake.
- [x] Emit a hot-enable wake only after persistence, success stamp, Content
  readiness, and all fallible stages complete; assert zero wake on every failed
  or compensated path and zero backend work for a committed disabled config.
- [x] Preserve key rewrap/ensure, readiness, compensation, candidate isolation,
  context propagation, metrics, and Indexer activation code unchanged.

## Phase 3 — Trellis verification

- [x] Run exact GREEN selectors, focused negative matrix, repeated and race
  gates, complete runtime and Search packages, Go 1.26.6 vet, pinned lint,
  gofmt, privacy/source scans, and `git diff --check` with task-scoped caches.
- [x] Dispatch independent Trellis check, self-fix all findings within the child
  scope, and rerun affected gates.
- [x] Record PostgreSQL/full-repository and local filesystem limits accurately;
  unavailable infrastructure is not a pass.
- [x] Update permanent specs only where the new bounded-transition/background
  convergence contract is not already explicit.

Implementation and independent-check evidence is recorded in
`research/implementation-evidence.md`. The complete runtime package now passes
after compiling with task-scoped home caches and running the resulting test
binary under a task-owned `/dev/shm` temporary root. PostgreSQL remains
`not_executed` because `TEST_POSTGRES_DSN` is absent.

## Phase 4 — Delivery and production acceptance

- [x] Commit and push the dedicated branch; open conventional-title PR #466.
- [ ] Monitor all required CI jobs, fix failures on the same branch, and merge
  only when all required checks are green.
- [ ] Monitor Release Please; merge the release PR when green and monitor GitHub
  Release plus amd64/arm64 Docker publication.
- [ ] Guarded production upgrade with fresh verified SQLite and compose backups;
  retain feature-disabled health acceptance and exact rollback.
- [ ] Re-enable only through authenticated Settings API; verify a prompt HTTP 200
  without Task 3 or collector activation.
- [ ] Poll generation 12 through building to active/complete 60,515 documents;
  retain inactive generations 1-11 and require zero active Search leases.
- [ ] Complete exact-point HTTP 200 real AssetRef, UI metadata/content preview,
  health/error/privacy/collector evidence and close the parent preview gate.
- [ ] Only after parent acceptance, create/resume node-log P1.
