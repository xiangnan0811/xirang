# Comprehensive Audit Report

This report is the durable landing place for findings from the sliced audit
execution. Each slice should add findings and fix notes here or in an adjacent
slice-specific artifact, then the main session will consolidate the final
summary before wrap-up.

## Execution Slices

* Backend: Go server, migrations, auth/security, tasks, alerts, backup/recovery,
  monitoring/probes, API handler behavior, backend tests and backend docs that
  are directly tied to backend behavior.
* Frontend: React/Vite console, routes, API client contracts, forms/dialogs,
  page states, accessibility, type-safety, frontend tests and UI-gallery truth.
* Docs/Deploy/Repo hygiene: README, deployment/env/release docs, workflows,
  Docker/Compose/env files, scripts, repository metadata, public process
  surfaces.

## Slice Artifacts

* Backend findings and checks: `backend-audit.md`
* Frontend findings and checks: `frontend-audit.md`
* Docs/deploy/repo-hygiene findings and checks: `docs-deploy-audit.md`

## Fixed Finding Summary

### Backend

* High: escalation concurrency could lose due firings under SQLite locking.
* Medium: escalation same-alert in-process locks were not released after fire
  attempts, creating unbounded lock retention in long-running servers.
* High: JWT revocation persistence/load errors were ignored.
* High: 2FA recovery-code login could issue JWTs before consumed codes were
  saved.
* High: encryption-key rotation docs claimed `DATA_ENCRYPTION_LEGACY_KEY`
  support, but the crypto layer did not read it.
* Medium: node migration/preflight ignored ownership, policy, task, and
  transaction query errors.
* Medium: batch deletion ignored cleanup-table delete failures inside the
  transaction.
* Low: node-log and anomaly pagination ignored count errors.
* Cross-layer/API consistency: admin metrics, node metrics, task trigger, and
  SQLite backup endpoints now use the standard response envelope helpers;
  node exec disabled, dashboard conflict, terminal capacity, and WebSocket
  unavailable errors were also brought back under the unified envelope.

### Frontend

* Medium: self-backup API contract omitted the backend `filename`.
* Medium: command palette route search drifted from the canonical navigation
  registry.
* Medium: dashboard cards used click-only containers instead of link semantics.
* Medium: node grid cards exposed non-interactive keyboard focus.
* Medium: service monitor forms accepted invalid frontend-side input states.
* Low: backups page primary action was a no-op.
* Low: status polling processed aborted requests and exposed decorative icons.
* Low: scoped lint/a11y debt around labels, hook dependencies, and stale
  disables was cleaned up.

### Docs / Deploy / Repo Hygiene

* Medium: development Compose used Go `1.26.1` while the repo targets `1.26.2`.
* Medium: Nginx deploy README described stale backend/frontend/TLS behavior.
* Medium: local Docker targets could accidentally tag and push `latest`.
* Medium: production env examples used secret-shaped placeholders instead of
  blank fail-closed values.
* Medium: doc freshness checks targeted ignored/local-only docs.
* Medium: backend database Trellis spec had a stale latest migration reference.
* Low: backend README migration version was stale.
* Low: release maintainer docs had stale manifest baseline text.
* Low: remaining workflow action refs used moving tags.
* Low: generated/local deploy artifacts and verification temp directories were
  not consistently ignored.

## Deferred Findings

* Frontend Fast Refresh lint warnings remain warning-only debt in shared module
  exports.
* Existing autofocus warnings need a product-level focus policy before broad
  changes.
* Logs viewer still emits the TanStack Virtual compiler warning.
* Vite bundle-size warnings remain and likely need a chunking strategy.
* Vitest still emits a `--localstorage-file` warning, though tests pass.
* `actionlint`, `shellcheck`, `yq`, and PyYAML are unavailable locally; this
  pass used available substitutes.

## Consolidated Summary

The audit found and fixed correctness, security, accessibility, deployment
truth, release-governance, and repo-hygiene issues across all requested areas.
The remaining items are warning-level or tool-availability follow-ups that are
not blocking correctness or CI parity for this branch.

Final local quality-gate results are recorded below.

## Final Verification

Passed:

* `cd backend && TMPDIR=/Users/weibo/Code/xirang/.tmp/tmp GOTMPDIR=/Users/weibo/Code/xirang/.tmp/go-build go test ./...`
* `cd backend && TMPDIR=/Users/weibo/Code/xirang/.tmp/tmp GOTMPDIR=/Users/weibo/Code/xirang/.tmp/go-build go build ./...`
* `cd backend && TMPDIR=/Users/weibo/Code/xirang/.tmp/tmp GOTMPDIR=/Users/weibo/Code/xirang/.tmp/go-build golangci-lint run ./...`
* `cd web && TMPDIR=../.tmp/tmp npm run check`
* `git diff --check`
* `bash scripts/check-doc-freshness.sh`
* `bash scripts/check-doc-freshness.test.sh`
* `bash scripts/check-migration-utc-safety.sh`
* `bash scripts/check-migration-utc-safety.test.sh`

Notes:

* A first backend `go test ./...` attempt used relative `TMPDIR`/`GOTMPDIR`
  values and failed because Go tests enter package directories; absolute
  repo-local temp paths fixed that environment issue.
* Frontend `npm run check` passes, but still emits warning-only Fast Refresh,
  autofocus, TanStack Virtual, Vitest localstorage-file, and Vite large-chunk
  warnings. These are recorded as deferred follow-ups.
