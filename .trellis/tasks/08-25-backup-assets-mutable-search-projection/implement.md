# Implementation Plan — Mutable Catalog Search Projection

## Phase 1 — Contract and RED

- [x] Record the production pre-first-write evidence in `research/root-cause.md`.
- [x] Add a focused Search Indexer fixture for an eligible manifest-less
  mutable-head Catalog with positive written entries.
- [x] Run only the exact selector and capture the behavioral RED:
  `ErrSearchCatalogChanged` and zero Search generations.
- [x] Add immutable mismatch and mutable drift negative controls before changing
  production code.

## Phase 2 — Minimal implementation

- [x] Add one package-local Catalog readiness predicate; do not change models,
  migrations, settings, Provider adapters, runtime composition, API, or web.
- [x] Apply it at initial freeze and activation-time revalidation.
- [x] Prove the mutable fixture activates with expected/written document counts
  derived from the Catalog written count.
- [x] Prove immutable count mismatch and every existing drift/fence/key/count
  failure remain closed.

## Phase 3 — Trellis verification

- [x] Run exact normal and repeated selectors, focused race, Search package,
  `go vet`, pinned `golangci-lint`, `gofmt -d`, privacy/source scans, and
  `git diff --check` with task-scoped temporary/cache directories.
- [x] Invoke an independent `trellis-check` reviewer; self-fix all findings and
  rerun affected gates.
- [x] Update task evidence/checklists without claiming unavailable PostgreSQL or
  full-repository gates as local passes.

## Phase 4 — Delivery and production acceptance

- [ ] Commit and push the dedicated branch; open one conventional-title PR.
- [ ] Monitor required CI, fix failures on this branch, and merge only when all
  required checks are green.
- [ ] Monitor Release Please, GitHub Release, Docker multi-arch publish, and
  Docker Hub stable image publication.
- [ ] Guarded production upgrade: root identity, exact compose/image/revision,
  health/restarts/schema/integrity, Task 3 paused/zero active, verified database
  backup, rollback compose file, and post-upgrade health/error checks.
- [ ] Wait for active complete Search generation; require an exact-point file
  result while printing only opaque AssetRef and safe metadata.
- [ ] Complete Backup -> Data metadata/preview and final health/privacy/
  collector acceptance, then return to the parent task and start node-log P1.
