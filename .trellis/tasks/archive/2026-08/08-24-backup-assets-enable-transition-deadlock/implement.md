# Implement — 备份资产启用事务死锁修复

Do not run `task.py start` until the user explicitly approves this planning set in a later message.
Do not retry production enablement during implementation. The current production mitigation is a healthy
v0.50.3 process with the feature disabled.

## Phase 0 — isolated start and genuine RED

- [x] After approval, start this task in its own ignored `.worktrees/` worktree from current `origin/main` on
  `codex/backup-assets-enable-transition-deadlock` (or move this planning branch into that worktree safely).
- [x] Re-read task research and routed backend specs; record exact implementation baseline.
- [x] Add a helper-subprocess deadlock regression for real Settings → Runtime → Content Foundation re-entry.
- [x] Add the separate real Search Config-closure re-entry RED.
- [x] Add a concurrent waiter cancellation RED and retain the existing atomic snapshot blocking test.
- [x] Capture failing selectors without touching production endpoints or settings.

## Phase 1 — prospective parser and coordinator primitives

- [x] Extract shared validated `ContentConfigFromValues` and `SearchOverlayConfigFromValues` parsers.
- [x] Make current Foundation getters reuse those parsers after one atomic snapshot.
- [x] Build complete prospective and prior transition bundles before runtime work.
- [x] Replace the private mutation mutex boundary with a context-aware one-token gate while preserving blocking
  snapshot readers and single-owner exclusivity.
- [x] Prove canceled waiter/no-callback, gate reuse, failure release and full old/new snapshot visibility.
- [x] Run parser/settings focused tests repeatedly and under race.

## Phase 2 — config-aware Content and Search transitions

- [x] Change internal Content enable/restore methods to receive typed config; remove Foundation reads from the
  mutation-inner path.
- [x] Add explicit-config Search StartupPass and map prospective Search config to worker config.
- [x] Keep background Search Run dynamic outside mutation; do not call its Config closure during enable.
- [x] Derive enable/live authority from prospective enabled plus readiness/admission state, not current-setting
  getters inside the gate.
- [x] Add a source/AST guard preventing Foundation snapshot/current getters in config-aware transition functions.
- [x] Turn both genuine RED selectors green before broadening scope.

## Phase 3 — bounded transition and exact rollback

- [x] Introduce one centralized transition ceiling below the 30-second server write timeout and compose it with
  the caller context.
- [x] Propagate opCtx through Content, Admission, persistence, Search, Export and Recovery steps.
- [x] Ensure every cancellation path closes/drains and joins owned work before return.
- [x] Make success stamp semantics exact: only full enable success retains it; failure restores the prior value.
- [x] Implement reverse-order enable compensation and prior-config disable restoration.
- [x] Test Content/Admission/persist/stamp/Search failure and timeout at every blocking seam.
- [x] Keep failure state not-ready/fail-closed when compensation itself fails; return joined typed errors.

## Phase 4 — PUT, DELETE and config import integration

- [x] Route settings PUT through the prospective transition bundle.
- [x] Route DELETE fallback-to-true/false through the same contract before deleting the override.
- [x] Route config import Foundation changes through the same contract and one import persistence boundary.
- [x] Preserve non-Foundation direct persistence and existing readiness 409 behavior.
- [x] Replace silent-only handler success tests with production-equivalent Content/Search consumers while keeping
  small spies for ordering assertions.
- [x] Assert standard envelopes, generic unexpected 500s and zero sensitive values in responses/logs.

## Phase 5 — executable spec and verification

- [x] Add the lock-order/prospective-transition contract to backend quality and error-handling specs with seven
  required sections.
- [x] Record RED/GREEN, failure matrix, race/repetition and privacy evidence in task research.
- [x] `cd backend && go test ./internal/settings -count=50`.
- [x] Focused runtime/settings/handler deadlock and rollback selectors with `-count=50`.
- [x] Focused race selectors with at least `-count=10`.
- [x] Owned package tests for settings, backupasset, runtime and API handlers/config import.
- [x] Full backend test, build, lint and vet; source/privacy scans; task validation; `git diff --check`.
- [x] Independent review focused on lock order, context/goroutine ownership, exact stamp/settings rollback and fake
  versus production graph parity.

## Phase 6 — PR, release and production acceptance

- [x] Commit/push/open PR only after local gates; monitor every required CI job and fix on the same branch.
- [x] Merge only when required CI is green; monitor exact post-merge CI and Release Please.
- [x] Merge the release PR when authorized and monitor GitHub Release, amd64/arm64 image builds, manifest publish
  and Docker Hub description when expected.
- [x] Record immutable version/commit/manifest/image evidence and provide the user a single-copy production runbook.
- [x] User creates/verifies a pre-upgrade logical SQLite backup, upgrades Core, and verifies safe disabled baseline.
- [x] User clicks enable exactly once under timed observation and returns HTTP/DB/readiness/log/health evidence.
- [x] Keep Child 18 release acceptance failed until production evidence passes; do not start node-logs P1 earlier.

## Risky boundaries

- Fixing only the first Content read leaves the Search self-deadlock intact.
- Returning on context timeout before drain/join turns a visible timeout into hidden background mutation.
- Moving persistence/runtime outside the exclusive gate breaks all-or-nothing visibility.
- Writing `enablement_succeeded_at` before full success without exact compensation creates false audit truth.
- Restoring Content by rereading Foundation recreates the same deadlock on the disable failure path.
- A fake that never calls Foundation can produce a false GREEN identical to the tests that missed this incident.

## Rollback

Product rollback remains configuration-first: keep `backup_assets.enabled` false/absent and the runtime not-ready.
If the fixed release regresses during the one permitted acceptance attempt, preserve the failed request logs and a
logical DB copy, perform a controlled restart to clear any in-memory owner, and roll back the image only if health or
data integrity requires it. Do not delete settings/history rows or repeatedly click enable.
