# Implementation Plan — Search Startup Isolation

## Phase 1 — Contract and genuine RED

- [x] Record the production crash/recovery and stable Search error evidence.
- [x] Add a focused SearchWorker startup fixture whose candidate Build returns a
  typed ordinary Search failure; capture the current escaping-error RED.
- [x] Add a runtime boundary RED proving that candidate-local failure clears
  Search readiness today.
- [x] Add a real SQLite Indexer fixture with Catalog `security_state=sealed`;
  capture `search_invalid_security_state`, zero written documents, and inactive
  failed generation before production edits.
- [x] Add infrastructure, cancellation, future-state, and inactive-output
  negative controls.

## Phase 2 — Minimal implementation

- [x] Change only Search candidate fan-out so joined candidate-local failures do
  not escape; preserve context cancellation and all pass-level failures.
- [x] Map only Catalog `sealed` to conservative sensitivity `unknown`; keep
  arbitrary non-empty states rejected.
- [x] Keep Indexer failure classification, staging evidence, normalization,
  count, key, lease, and activation code unchanged.
- [x] Prove runtime startup returns success/Search ready for point-local failure
  and remains failed/unready for infrastructure errors.

## Phase 3 — Trellis verification

- [x] Run exact GREEN selectors, repeated and race matrices, affected runtime
  and Search packages, Go 1.26.6 vet, pinned golangci-lint, gofmt, privacy/source
  scans, and `git diff --check` with task-scoped caches.
- [x] Invoke an independent Trellis check reviewer, self-fix all findings, and
  rerun affected gates.
- [x] Record PostgreSQL and full-repository gates accurately; unavailable
  infrastructure is not a pass.

## Phase 4 — Delivery and production acceptance

- [ ] Commit and push the dedicated branch; open a conventional-title PR.
- [ ] Monitor every required CI job, fix failures on the same branch, and merge
  only when all required jobs are green.
- [ ] Monitor Release Please, merge the release PR when green, and monitor GitHub
  Release plus amd64/arm64 Docker publication for v0.50.8.
- [ ] Guarded production upgrade with a fresh verified database backup and exact
  rollback compose; do not re-enable the feature during image health acceptance.
- [ ] Re-enable backup assets only through authenticated Settings API and verify
  repository/Catalog/Search convergence without restarting Task 3.
- [ ] Require active complete Search with positive documents, HTTP 200 exact-point
  file result, UI metadata/content preview, healthy container, zero critical
  errors, and zero node-log collectors.
- [ ] Close the parent preview gate; only then create/resume node-log P1.
