# Backend Audit

Scope: backend slice only (`backend/**`) for the comprehensive project review.

## Fixed Findings

* Severity: high
* Area: alerting / escalation concurrency
* Status: fixed
* Summary: concurrent escalation ticks for the same alert could both fail under
  SQLite shared-cache locking, leaving a due escalation level unfired.
* Evidence: `TMPDIR=/tmp go test ./...` initially failed
  `TestEngine_ConcurrentTicks_OnlyOneFires` with `totalFires=0`.
* Fix: serialized same-alert fire attempts in-process while keeping the
  transaction optimistic lock and `alert_escalation_events` unique constraint as
  the cross-process/database safety net.
* Verification: `TMPDIR=/tmp go test ./internal/escalation` and
  `TMPDIR=/tmp go test ./...` pass.

* Severity: medium
* Area: alerting / escalation lock lifecycle
* Status: fixed during reviewer pass
* Summary: the same-alert in-process fire lock was stored in a package-level
  map and never released after a fire attempt, so alert IDs would accumulate
  for the lifetime of a busy server.
* Evidence: `alertFireLocks` was a package-level `sync.Map` keyed by alert ID
  with no deletion path.
* Fix: replaced it with a reference-counted lock registry that serializes
  concurrent same-alert fires and removes the lock entry when the last waiter
  exits.
* Verification: added a concurrency test assertion that the alert lock is
  released after same-alert fire contention; targeted backend tests pass with
  repository-local `TMPDIR` / `GOTMPDIR`.

* Severity: high
* Area: auth / JWT revocation persistence
* Status: fixed
* Summary: logout could report success even if token revocation failed to
  persist to the database, making the token valid again after restart.
* Evidence: `JWTManager.RevokeToken` ignored `Create(&TokenRevocation).Error`;
  `loadRevokedFromDB` also ignored load failures.
* Fix: checked revocation load/persist errors, logged startup-load failures,
  returned persistence failures to logout, and used `ON CONFLICT DO NOTHING`
  for idempotent revocations.
* Verification: added JWT persistence/reload/error tests; `TMPDIR=/tmp go test
  ./internal/auth` and `TMPDIR=/tmp go test ./...` pass.

* Severity: high
* Area: auth / 2FA recovery codes
* Status: fixed
* Summary: recovery-code login issued a full JWT even if consuming the one-time
  recovery code failed to save.
* Evidence: `TOTPLogin` ignored both `json.Marshal` and `h.db.Save(&user)`
  errors after `ValidateAndConsumeRecoveryCode`.
* Fix: require recovery-code JSON serialization and DB save to succeed before
  generating the final JWT.
* Verification: added success and forced-save-failure handler tests;
  `TMPDIR=/tmp go test ./internal/api/handlers` and
  `TMPDIR=/tmp go test ./...` pass.

* Severity: medium
* Area: node migration / migration preflight
* Status: fixed
* Summary: migration and preflight could silently continue after ownership,
  policy, or task lookup database errors, producing false no-op or incomplete
  migration/preflight results.
* Evidence: unchecked `Count`, `Pluck`, `Find`, and transaction `Count` calls in
  `node_migrate_handler.go` and `node_migrate_preflight_handler.go`.
* Fix: checked all relevant query errors and mapped them through
  `respondInternalError`.
* Verification: added missing-table regression tests for migrate and preflight;
  `TMPDIR=/tmp go test ./internal/api/handlers` and
  `TMPDIR=/tmp go test ./...` pass.

* Severity: medium
* Area: batch commands / transactional cleanup
* Status: fixed
* Summary: batch deletion ignored cleanup-table delete failures inside the
  transaction, so it could delete the batch task rows while leaving related
  records behind.
* Evidence: `BatchHandler.Delete` ignored delete errors for task logs, runs,
  traffic samples, and alerts.
* Fix: checked each cleanup delete and abort the transaction on the first
  failure.
* Verification: added a regression test that omits cleanup tables and verifies
  500 + rollback; `TMPDIR=/tmp go test ./internal/api/handlers` and
  `TMPDIR=/tmp go test ./...` pass.

* Severity: low
* Area: monitoring / log and anomaly list pagination
* Status: fixed
* Summary: node log and anomaly list endpoints ignored count errors, which
  could return misleading totals/has_more values or defer the error to a later
  query.
* Evidence: unchecked `q.Count(&total)` in `NodeLogsHandler.Query` and
  `AnomalyHandler.List`.
* Fix: checked count errors and returned standard internal-error envelopes.
* Verification: added missing-table count-error tests; `TMPDIR=/tmp go test
  ./internal/api/handlers` and `TMPDIR=/tmp go test ./...` pass.

* Severity: medium
* Area: cross-layer API response envelopes
* Status: fixed during reviewer pass
* Summary: a few pre-upgrade HTTP error paths still returned ad hoc
  `{"error": ...}` or direct JSON shapes, which caused frontend `request()` to
  fall back to generic messages instead of backend envelope messages.
* Evidence: `NodeHandler.Exec`, terminal capacity rejection, WebSocket
  unavailable checks, and dashboard conflict mapping bypassed the response
  helpers or standard `Response` envelope.
* Fix: added a `respondServiceUnavailable` helper, converted the remaining
  HTTP error paths to `Response` envelopes, and kept the node exec product error
  code in `data.error_code`.
* Verification: updated/added handler tests for node exec disabled, dashboard
  duplicate name, terminal capacity, and WebSocket unavailable envelopes.

* Severity: high
* Area: encryption key rotation
* Status: fixed during reviewer pass
* Summary: documentation told operators to set `DATA_ENCRYPTION_LEGACY_KEY`
  while rotating encryption keys, but the crypto package only derived the v1
  legacy key from the current `DATA_ENCRYPTION_KEY`.
* Evidence: `docs/env-vars.md` documented `DATA_ENCRYPTION_LEGACY_KEY`, while
  `backend/internal/secure/crypto.go` did not read that environment variable.
* Fix: `secure.loadKey` now keeps the primary v2 key from
  `DATA_ENCRYPTION_KEY` and, when set, derives the v1 decrypt key from
  `DATA_ENCRYPTION_LEGACY_KEY` so `ReEncryptV1Value` can migrate old data to
  the new primary key.
* Verification: added `TestReEncryptV1ValueUsesLegacyKeyEnv`; targeted
  `./internal/secure` tests pass.

## Backend Checks

* `cd backend && go test ./...` failed before running tests because Go could not
  create a work directory under the default macOS temp path
  (`permission denied` under `/var/folders/.../T/go-build...`).
* `cd backend && TMPDIR=/Users/weibo/Code/xirang/.tmp/tmp GOTMPDIR=/Users/weibo/Code/xirang/.tmp/go-build go test ./internal/secure ./internal/escalation ./internal/api/handlers` passes after reviewer fixes.
* Earlier backend slice checks passed with `TMPDIR=/tmp`; reviewer verification
  uses repository-local temp dirs because the default macOS temp path is not
  writable in this session.
* `cd backend && TMPDIR=/tmp go build ./...` passes.
* `cd backend && TMPDIR=/tmp golangci-lint run ./...` passes.

## Deferred Findings

* Status: deferred
* Area: backend shared WIP outside this slice's edits
* Reason: `backend/.env.production.example` and `backend/README_backend.md` were
  already dirty in the shared worktree and appear to be another slice's changes.
  This backend audit did not modify or revert them.

* Status: deferred
* Area: full repository / frontend contract
* Reason: existing shared frontend changes under `web/**` are outside this
  backend-only ownership scope. Cross-layer response-envelope changes already
  present in backend handler files should be validated by the frontend slice.
