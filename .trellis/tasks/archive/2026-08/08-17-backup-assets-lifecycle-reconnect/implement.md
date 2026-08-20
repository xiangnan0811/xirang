# Child 14 Implementation Plan

> Planning status: review required. This plan does not authorize
> `task.py start` or product-code changes.

## 1. Baseline and execution discipline

- Implementation base: exact `origin/main` commit
  `19140e80ac34ebab26bc1f8ed141965c5f4d3cdd`.
- Dedicated branch: `codex/backup-assets-lifecycle-reconnect`.
- Current formal release: `v0.48.0@793af9f8c29ba6274d4f99c1e104fcc01a72752c`;
  do not reset the task to this older tag.
- Before Phase 2, rerun `trellis-before-dev`, load the manifests, and use
  `superpowers:test-driven-development`.
- For every task below:
  1. add only the named focused test;
  2. run the exact selector and append command, exit status, failure category,
     and minimal output to `research/red-green.md`;
  3. implement the bounded behavior;
  4. rerun the exact same selector and append GREEN evidence;
  5. run the adjacent package selector before moving on.
- A compile-time RED is valid only when caused by the just-added contract test.
  Infrastructure, typo, missing fixture, skipped PostgreSQL, or unrelated suite
  failure is not valid RED provenance.
- Child 13's accepted historical same-selector exception is not inherited. The
  very first Child 14 selector and every later selector require real RED -> same
  selector GREEN evidence.
- No commit, push, PR, merge, task archive, or parent completion claim occurs
  until its explicit delivery step.

## 2. Rebaselined file manifest

This is the exact anticipated product/test/docs manifest. If implementation
discovers a required path outside it, stop, update `design.md` and this manifest,
and obtain review before editing that path.

### Create

```text
backend/internal/model/backup_asset_lifecycle.go
backend/internal/model/backup_asset_lifecycle_test.go
backend/internal/database/migrations/sqlite/000070_backup_asset_lifecycle.up.sql
backend/internal/database/migrations/sqlite/000070_backup_asset_lifecycle.down.sql
backend/internal/database/migrations/postgres/000070_backup_asset_lifecycle.up.sql
backend/internal/database/migrations/postgres/000070_backup_asset_lifecycle.down.sql

backend/internal/backupasset/retention/contracts.go
backend/internal/backupasset/retention/policy.go
backend/internal/backupasset/retention/policy_test.go
backend/internal/backupasset/retention/hold.go
backend/internal/backupasset/retention/hold_test.go
backend/internal/backupasset/retention/coordinator.go
backend/internal/backupasset/retention/coordinator_test.go
backend/internal/backupasset/retention/task_facade.go
backend/internal/backupasset/retention/task_facade_test.go
backend/internal/backupasset/retention/audit.go
backend/internal/backupasset/retention/audit_test.go
backend/internal/backupasset/retention/worker.go
backend/internal/backupasset/retention/worker_test.go
backend/internal/backupasset/retention/metrics.go
backend/internal/backupasset/retention/metrics_test.go
backend/internal/backupasset/retention/behavior_integration_test.go
backend/internal/backupasset/retention/source_boundary_test.go
backend/internal/backupasset/retention/disaster_recovery_test.go

backend/internal/backupasset/disaster_recovery.go
backend/internal/backupasset/disaster_recovery_test.go
backend/internal/backupasset/repository/disaster_recovery_test.go

backend/internal/backupasset/provider/deletion.go
backend/internal/backupasset/provider/deletion_test.go
backend/internal/backupasset/provider/restic_deletion.go
backend/internal/backupasset/provider/restic_deletion_test.go
backend/internal/backupasset/provider/rsync_deletion.go
backend/internal/backupasset/provider/rsync_deletion_test.go
backend/internal/backupasset/provider/rclone_deletion.go
backend/internal/backupasset/provider/rclone_deletion_test.go

backend/internal/backupasset/repository/import.go
backend/internal/backupasset/repository/import_test.go
backend/internal/backupasset/repository/rebuild.go
backend/internal/backupasset/repository/rebuild_test.go

backend/internal/backupasset/content/source_lifecycle.go
backend/internal/backupasset/content/source_lifecycle_test.go
backend/internal/backupasset/catalog/source_lifecycle.go
backend/internal/backupasset/catalog/source_lifecycle_test.go
backend/internal/backupasset/search/source_lifecycle.go
backend/internal/backupasset/search/source_lifecycle_test.go
backend/internal/backupasset/processing/source_lifecycle.go
backend/internal/backupasset/processing/source_lifecycle_test.go
backend/internal/backupasset/export/source_lifecycle.go
backend/internal/backupasset/export/source_lifecycle_test.go
backend/internal/backupasset/recovery/source_lifecycle.go
backend/internal/backupasset/recovery/source_lifecycle_test.go
backend/internal/backupasset/runtime/retention_lifecycle.go
backend/internal/backupasset/runtime/retention_lifecycle_test.go

backend/internal/task/archive.go
backend/internal/task/archive_test.go
backend/internal/api/handlers/backup_retention_handler.go
backend/internal/api/handlers/backup_retention_handler_test.go
backend/internal/api/handlers/config_backup_assets.go
backend/internal/api/handlers/config_backup_assets_test.go

web/src/lib/api/backup-retention-api.ts
web/src/lib/api/backup-retention-api.test.ts
web/src/features/backup-assets/repository-management-panel.tsx
web/src/features/backup-assets/repository-management-panel.test.tsx
web/src/features/backup-assets/retention-policy-panel.tsx
web/src/features/backup-assets/retention-policy-panel.test.tsx
web/src/features/backup-assets/backup-assets-lifecycle-panels.a11y.test.tsx
```

### Modify

```text
backend/internal/model/backup_asset.go
backend/internal/backupasset/domain.go
backend/internal/backupasset/domain_test.go
backend/internal/backupasset/lease.go
backend/internal/backupasset/lease_test.go
backend/internal/backupasset/service.go
backend/internal/backupasset/service_test.go

backend/internal/database/backup_asset_migrations_integration_test.go

backend/internal/backupasset/provider/contracts.go
backend/internal/backupasset/provider/registry.go
backend/internal/backupasset/provider/registry_test.go
backend/internal/backupasset/provider/runner.go
backend/internal/backupasset/provider/runner_test.go
backend/internal/backupasset/provider/rsync_tree.go
backend/internal/backupasset/provider/rsync_tree_linux.go
backend/internal/backupasset/provider/rsync_tree_other.go
backend/internal/backupasset/provider/rsync_tree_test.go
backend/internal/backupasset/provider/rclone_native.go
backend/internal/backupasset/provider/rclone_native_aws_sdk.go
backend/internal/backupasset/provider/rclone_native_aws_sdk_test.go
backend/internal/backupasset/provider/publication_boundary_test.go

backend/internal/backupasset/repository/service.go
backend/internal/backupasset/repository/service_test.go
backend/internal/backupasset/repository/connect.go
backend/internal/backupasset/repository/connect_test.go
backend/internal/backupasset/repository/reconcile.go
backend/internal/backupasset/repository/reconcile_test.go
backend/internal/backupasset/repository/query.go
backend/internal/backupasset/repository/query_test.go
backend/internal/backupasset/repository/managed_history.go
backend/internal/backupasset/repository/managed_history_test.go
backend/internal/backupasset/repository/testutil_test.go

backend/internal/backupasset/catalog/indexer.go
backend/internal/backupasset/catalog/indexer_test.go
backend/internal/backupasset/search/indexer.go
backend/internal/backupasset/search/indexer_test.go
backend/internal/backupasset/search/ingest.go
backend/internal/backupasset/search/ingest_test.go
backend/internal/backupasset/search/behavior_integration_test.go
backend/internal/backupasset/content/broker.go
backend/internal/backupasset/content/broker_test.go
backend/internal/backupasset/content/cache.go
backend/internal/backupasset/content/cache_test.go
backend/internal/backupasset/content/lease.go
backend/internal/backupasset/content/lease_test.go
backend/internal/backupasset/processing/coordinator.go
backend/internal/backupasset/processing/coordinator_test.go
backend/internal/backupasset/processing/behavior_integration_test.go
backend/internal/backupasset/processing/derived_lifecycle.go
backend/internal/backupasset/processing/derived_lifecycle_test.go
backend/internal/backupasset/processing/reconciler_test.go
backend/internal/backupasset/export/lifecycle.go
backend/internal/backupasset/export/lifecycle_test.go
backend/internal/backupasset/recovery/worker.go
backend/internal/backupasset/recovery/worker_test.go
backend/internal/backupasset/overlay/service.go
backend/internal/backupasset/overlay/service_test.go
backend/internal/backupasset/overlay/lifecycle.go
backend/internal/backupasset/overlay/lifecycle_test.go
backend/internal/backupasset/runtime/processing_runtime.go
backend/internal/backupasset/runtime/processing_runtime_test.go
backend/internal/backupasset/runtime/runtime.go
backend/internal/backupasset/runtime/runtime_test.go

backend/internal/task/retention.go
backend/internal/task/retention_test.go
backend/internal/task/retention_worker.go
backend/internal/task/service.go
backend/internal/task/manager.go
backend/internal/task/manager_test.go

backend/internal/api/router.go
backend/internal/api/router_test.go
backend/internal/api/backup_asset_rbac_test.go
backend/internal/api/handlers/backup_repository_handler.go
backend/internal/api/handlers/backup_repository_handler_test.go
backend/internal/api/handlers/backup_asset_handler.go
backend/internal/api/handlers/backup_asset_handler_test.go
backend/internal/api/handlers/task_handler.go
backend/internal/api/handlers/task_handler_test.go
backend/internal/api/handlers/config_handler.go
backend/internal/api/handlers/config_handler_test.go
backend/internal/api/handlers/step_up_test.go

backend/internal/auth/step_up_action.go
backend/internal/auth/step_up_action_test.go
backend/internal/settings/service.go
backend/internal/settings/service_test.go
backend/internal/snapshot/indexer_test.go

backend/internal/api/docs/docs.go

web/src/lib/api/backup-repositories-api.ts
web/src/lib/api/backup-repositories-api.test.ts
web/src/lib/api/client.ts
web/src/lib/api/client.test.ts
web/src/lib/step-up-storage.ts
web/src/lib/step-up-storage.test.ts
web/src/features/backup-assets/backup-assets-workspace.tsx
web/src/features/backup-assets/backup-assets-workspace.test.tsx
web/src/pages/backups-page.data.tsx
web/src/types/domain.ts
web/src/i18n/locales/zh.ts
web/src/i18n/locales/en.ts

docs/admin/backup-recovery.md
docs/admin/security.md
.gitignore
```

Task 16 plan amendment (2026-08-18): Task 14 created
`backend/internal/backupasset/disaster_recovery.go` plus the three
`disaster_recovery_test.go` files. Lifecycle RBAC coverage lives in the
existing `backend/internal/api/backup_asset_rbac_test.go` rather than a new
`middleware/rbac_test.go`. `backend/internal/task/retention_worker_test.go`
was never created; Task worker coverage stays in
`backend/internal/backupasset/retention/worker_test.go` and
`backend/internal/task/retention_test.go`. `backend/cmd/server/main.go`
remains intentionally absent: managed Task retention is installed through
the existing `SetInterruptedRunReadiness` hook.

Task 16 hygiene amendment (2026-08-19): `make check` rebuilds
`backend/xirang-server`. `.gitignore` now matches the existing
`backend/xirang` / `backend/server` binary ignores so the 99MB artifact
cannot enter the Task 17 commit.

### Task/evidence bookkeeping during implementation

```text
.trellis/tasks/08-17-backup-assets-lifecycle-reconnect/research/red-green.md
.trellis/tasks/08-17-backup-assets-lifecycle-reconnect/research/review.md
.trellis/tasks/08-17-backup-assets-lifecycle-reconnect/research/delivery-evidence.md
.trellis/tasks/08-17-backup-assets-lifecycle-reconnect/task.json
.trellis/tasks/07-12-backup-data-explorer-design/task.json
```

### Explicitly forbidden/out of scope

```text
.codex/**
.github/**
deploy/**
docker-compose.yml
web/package.json
web/package-lock.json
backend/internal/database/migrations/**/000071*
legacy snapshot browser/removal routes and components
Child 13 archived task/research paths
any Child 15 task directory
```

`backend/cmd/server/main.go` is intentionally absent from the modify manifest:
the live asset runtime is already one joined lifecycle worker. Runtime-internal
composition is sufficient. If live implementation proves a composition change
in `main.go` unavoidable, amend the plan before touching it.

## 3. Task-by-task TDD plan

### Task 1 — Establish the first genuine RED: managed Task retention delegates exact IDs

**Files:**

- Modify `backend/internal/task/retention.go`
- Modify `backend/internal/task/retention_test.go`
- Modify `backend/internal/task/manager.go`

**RED first:** add
`TestManagedTaskRetentionDelegatesExactRecoveryPointIDs`. The fixture seeds a
managed repository with multiple immutable points and a fake exact authority.
It expects sorted point IDs to be delegated and asserts zero legacy path,
credential, SSH, Restic age/GFS, and Rclone min-age effects. Current main lacks
the authority seam, so this selector must fail before implementation.

```bash
cd backend && go test ./internal/task -run '^TestManagedTaskRetentionDelegatesExactRecoveryPointIDs$' -count=1
```

Implement a narrow `ManagedRecoveryPointRetention` port on `task.Manager`.
Lineage-guard-proven managed retention delegates to it; pristine legacy behavior
remains behind the existing guard. Do not yet implement the real authority.

Rerun the exact selector for GREEN, then:

```bash
cd backend && go test ./internal/task -run '^(TestManagedTaskRetentionDelegatesExactRecoveryPointIDs|TestRetention|TestPristine)' -count=1
```

### Task 2 — Add paired `000070` and lifecycle models

**Files:** model lifecycle files, model/domain/lease files, paired migrations,
the existing RecoveryPoint model, and the migration integration test listed in
the manifest.

**RED selectors:** add paired tests for schema/model parity, constraints,
immutable per-operation tombstone events (including one retained mutable-retire
event followed by one explicit-purge event for the same point), active
uniqueness, retention-worker lease holder, pristine down, used down through
`migrator.Steps(-1)`, prior clean version preservation, paired file inventory,
and a distinct monotonic RecoveryPoint point revision that is backfilled,
advances on every row mutation, and restores cleanly on pristine down.

```bash
cd backend && go test ./internal/database -run '^TestBackupAssetMigration070(SQLite|PairedFiles|UsedDownAdmissionSQLite)$' -count=1
```

After the SQLite selector is GREEN, run required PostgreSQL with the same
contract. Missing DSN is a failure, not a skip:

```bash
cd backend && REQUIRE_POSTGRES_MIGRATION_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" go test ./internal/database -run '^TestBackupAssetMigration070(Postgres|UsedDownAdmissionPostgres)$' -count=1
```

Add encrypted reason hooks and JSON exclusion tests. Extend, do not duplicate,
RecoveryPoint/hold/lease enums.

### Task 3 — Implement versioned policy and hold services

**Files:** retention policy/hold/contracts files plus backupasset service/settings
files, step-up registries, and the shared repository Foundation test fixture
that must enumerate every registered backup-asset setting.

**RED selectors:** cover canonical rules and digest, deterministic selection,
CAS revision, exact repository/Task-link scope, mutable exclusion, active-hold
exclusion, create/release/expiry, Admin actor, encrypted reason, aggregate
projection, and cross-purpose step-up isolation.

```bash
cd backend && go test ./internal/backupasset/retention ./internal/auth -run '^(TestPolicy|TestHold|TestStepUpActionRetentionHoldRelease)' -count=1
```

Implement `PolicyService` and `HoldService`. Add
`retention.hold_release` to backend/frontend registries only after the RED is
captured. Keep `backup_assets.enabled` default false and add bounded retention
interval/batch/drain settings through `settings.Service`.

Selection must return separate point and capability revisions and hold the
selected RecoveryPoint rows in the caller transaction. Hold projection changes
advance the point revision; `CapabilityRevision` is not an acceptable alias for
the lifecycle point CAS token.

### Task 4 — Reuse reconnect and add import/review/rebuild

**Files:** repository import/rebuild files and the listed repository service,
connect/reconcile/query tests/files.

**RED selectors:** prove existing identity-safe reconnect remains stable,
retired mutable heads never reactivate, failed reconcile preserves last-good
facts, attributable Restic import, valid marker+commit requirements for
Rsync/Rclone, Admin-only unknown candidate visibility, at-most-one reviewed
baseline, idempotent discovery/review, and truthful rebuild reports.

```bash
cd backend && go test ./internal/backupasset/repository -run '^(TestDisconnectPreservesMutableEvidenceAndReconnectsWithRetainedSalt|TestLifecycleReconnectRetiredHeadDoesNotReactivate|TestImport|TestRebuild)' -count=1
```

Do not add a second reconnect state machine or arbitrary locator/credential API.
Build import and rebuild on the existing Provider registry, publication facts,
Catalog generation, and low-priority derived backfill boundaries.

### Task 5 — Implement expiry, lease, hold, mutable retirement, and purge state machine

**Files:** retention coordinator/behavior files, backupasset lease files, and
RecoveryPoint model projection.

**RED selectors:** exercise committed/degraded -> expiring -> expired, hold and
live lease blocks, bounded takeover, fence loss, restart at every phase,
idempotent retry, WORM/unavailable/identity conflict -> purge_blocked,
purge_blocked retry, observed/retired exclusion from ordinary retention,
non-destructive mutable retirement, late admission rejection, and explicit
mutable purge.

```bash
cd backend && go test ./internal/backupasset/retention -run '^(TestLifecycle|TestExpiry|TestPurge|TestMutableHeadRetirement|TestLease)' -count=1
```

Wire the real `retention.PolicyService` selection facade into the Task port
created in Task 1; rerun Task 1's exact selector unchanged.

### Task 6 — Add owner-scoped dependent cleanup ports

**Files:** the six new `source_lifecycle` owner pairs, the pure runtime aggregate
adapter pair, the listed Search/Processing late-output and projection-boundary
files, the Search dual-engine behavior fixture required by the new lifecycle
authority table, Content Broker point-scoped issue tracking and exact cache
eviction files, the shared closed owner-stage contract in the listed backupasset
domain/tests, Overlay lifecycle/admission files, and retention coordinator/tests.
Do not install the aggregate into Runtime startup/run/shutdown; Task 8 owns that
production composition.

**RED selectors:** for one exact point, prove revoke/drain of Content; Catalog
generation supersede; Search projection removal; Processing artifact/key
revocation; Export source-expiry/delivery/key/ciphertext lifecycle; Recovery
source-job cancellation/lease release; Overlay broken/tombstone/recent behavior;
and startup removal/rejection of late outputs.

The first Task 6 edit is only the Overlay resurrection test. It seeds an
`observed` mutable point plus an active `mutable_retire` attempt, proves existing
overlays are reconciled, and then attempts saved-search, favorite, tag, and
recent writes. Current live code admits at least one late write, so this selector
must be a genuine behavioral RED before any Task 6 production edit:

```bash
cd backend && go test ./internal/backupasset/overlay -run '^TestLifecycleLateOutputRejectsOverlayResurrection$' -count=1
```

Implement transaction-bound point/lifecycle admission for every point-bearing
Overlay write. Add a lifecycle-only maintenance path that verifies the exact
active attempt and may run while the feature is disabled. The aggregate maps
mutable retirement to `SourceRetired`, expiry/purge to `SourceExpiring`, and
loops bounded Overlay batches through a zero-result completion proof.

Then add the six owner contracts and the pure aggregate with TDD. Revoking must
invoke Content, Catalog, Search, Processing, Export, then Recovery before the
generic lease drain; Catalog and Search active builders hold ordinary point
leases and therefore cannot be deferred to Cleaning. Revoking only fences
admission, cancels/joins active work, durably terminalizes only the corresponding
work-authority rows, and releases exact owner leases; completed projections,
wrapped keys, references, and ciphertext remain unchanged until Cleaning.
Cleaning idempotently replays that owner order to prove completion, performs
durable cleanup, then settles Overlay. The shared owner request carries an exact
attempt ID, operation, and closed `prepare|cleanup` stage; every owner verifies
that stage against the live lifecycle attempt before mutation. Aggregate tests
prove all six owners have zero active work/live leases after prepare, payloads
remain intact until cleanup, and Processing destruction occurs only after the
Search cleanup proof. Every owner is exact-point, idempotent, bounded,
context-aware, evidence-preserving, and returns an error while any owner work or
lease remains unproven. Search generation creation/activation and
content/OCR ingest use point-before-lease locking and reject lifecycle drift.
Content keeps its global Issue wait for Shutdown, but registers validated
backup-asset issues by exact RecoveryPoint before lease acquisition and removes
them on every exit. Source lifecycle waits only the target point's pre-publication
issues; unrelated points and RecoveryResult issues cannot delay exact cleanup.
It releases live BackupAsset sessions through their exact fence and uses the
existing cleanup takeover only for the matching persisted grant/lease; managed
RecoveryResult grants, reads, and leases remain outside this owner and outside
the subsequent generic source drain. Exact cache eviction has one serialized
owner across lifecycle, reconcile, and concurrent retries, never leaks private
chunk paths, and reclaims completed in-memory drain markers without reopening
admission. Catalog prepare contextually joins its exact canceled builder before
durable completion proof or lease release. Processing first revokes grant and
manifest publication authority, then waits for in-flight work; Derived cleanup
returns only closed errors and never a root or opaque locator. Export uses a
real owner transition that makes queued/running/sealing/ready jobs
source-expired before the existing fence/revoke/drain ports while preserving
selection, keys, and ciphertext until Cleaning. Recovery uses a typed,
transaction-bound cancellation handoff that validates the lifecycle request,
plan, job, attempt, and exact point lease without changing Child 13 result or
workspace cleanup state. Overlay cleanup never treats `SKIP LOCKED` as a zero
proof and uses the same closed readable semantics/state matrix as shared
admission. The PostgreSQL RequestWork/Pull regression observes and rejects any
deadlock retry rather than accepting eventual success alone.
Processing `RequestWork` uses RecoveryPoint-to-lifecycle-attempt-to-job order:
it performs lifecycle admission before locking/reusing an existing job or
inserting a new job/interest. `Pull` discovers candidates without locking, then
uses the same point-to-attempt order before locking and revalidating the exact
queued job. Add a PostgreSQL concurrency regression that rejects `40P01` and
requires zero transaction retry for both paths. Exercise the unchanged real
derived-manifest entry point after source
revoke to prove its existing job/attempt/grant/lease fence rejects late output.

```bash
cd backend && go test ./internal/backupasset/{content,catalog,search,processing,export,recovery,overlay,retention,runtime} -run '^(TestRecoveryPointSourceLifecycle|TestLifecycleDependentCleanup|TestLifecycleLateOutput)' -count=1
```

Add a source-boundary test proving retention does not delete owner tables or
import Child 13 result-lifecycle internals. Recovery source cleanup may cancel a
source job through its existing owner but must not query or mutate RecoveryResult
rows, result-cleanup phase, workspace-cleanup owner/phase/fence, or result IDs.
Run the existing Child 13 lifecycle selectors unchanged to prove
non-regression:

```bash
cd backend && go test ./internal/backupasset/recovery ./internal/backupasset/runtime -run 'RecoveryResult|RecoveryWorkspaceCleanup' -count=1
```

### Task 7 — Add exact Provider point deletion

**Files:** provider deletion files and listed provider registry/runner/tree/
native-version files/tests.

**RED selectors:** prove optional registry capability, exact full Restic ID,
handle-relative committed Rsync component, exact Rclone prefix, exact native
object-version set, already-absent idempotency, WORM typed block, pre/post
identity verification, cancellation/join, bounded output, and no raw leakage.

```bash
cd backend && go test ./internal/backupasset/provider -run '^(TestPointDeleter|TestResticExactPointDeletion|TestRsyncExactPointDeletion|TestRcloneExactPointDeletion)' -count=1
```

Also rerun the read-only boundary tests. They must continue to reject mutation
through read adapters while allowing only the separately registered deletion
port:

```bash
cd backend && go test ./internal/backupasset/provider -run '^(TestProviderPublicationBoundary|Test.*Adapter.*Mutation)' -count=1
```

Connect the deletion port to the coordinator. Only `deleted` or
`already_absent` plus post-verification permits `expired` and private-locator
clearing.

### Task 8 — Add audit retention, metrics, worker, and runtime composition

**Files:** retention audit/metrics/worker files and runtime files/tests.

**RED selectors:** cover startup pass, periodic bounded batches, dynamic config,
claim heartbeat/fence loss, cancellation, shutdown join, disabled maintenance,
policy selection, blocked retry, operational-hold expiry, import/rebuild
reconciliation, and eligible audit detail pruning with checkpoint continuity.

```bash
cd backend && go test ./internal/backupasset/retention ./internal/backupasset/runtime -run '^(TestRetentionWorker|TestAuditRetention|TestRuntime.*Retention)' -count=1
```

Construct the tombstone source before managed-history admission, inject it into
the existing resolver, compose every owner port, expose handler facades, and add
the worker to Runtime startup/run/shutdown. Do not add an unjoined goroutine or
edit `backend/cmd/server/main.go` under the approved manifest.

### Task 9 — Change Task HTTP delete to archive/unlink

**Files:** new Task archive pair plus listed task service/manager/handler tests
and files.

**RED selector:** prove archive/disable/link-unlink snapshots, repository/point/
run/audit preservation, dependent conflict, commit-before-schedule-removal,
commit failure leaves schedule, schedule failure remains safely archived, and
zero Provider effects.

```bash
cd backend && go test ./internal/task ./internal/api/handlers -run '^(TestTaskArchive|TestTaskDeleteArchivesAndUnlinks|TestTaskDeleteDoesNotRemoveScheduleWhenArchiveFails)' -count=1
```

Replace direct handler `db.Delete` with `task.ArchiveService`. Preserve the HTTP
ownership/RBAC boundary and return an archive result through the standard
envelope.

### Task 10 — Add lifecycle handlers, routes, RBAC, proofs, and Swagger

**Files:** retention handler pair, repository/asset handlers, router/tests,
auth/RBAC tests, and generated Swagger files.

**RED selectors:** assert every exact route, feature-gate-first behavior, strict
body/cursor limits, Admin/RBAC/ownership parity, safe 400/404/409/501/503/500
envelopes, impact-plan drift, purge and hold-release pairwise proof isolation,
and audit action/count privacy.

```bash
cd backend && go test ./internal/api/... ./internal/auth ./internal/middleware -run '^(TestBackupRetentionHandler|TestBackupRepositoryLifecycleHandler|TestBackupLifecycleRoutes|Test.*StepUp.*(Purge|HoldRelease)|Test.*RBAC.*Backup)' -count=1
```

Regenerate and verify Swagger only after focused handler GREEN:

```bash
make swag-init
git diff --check backend/internal/api/docs
```

### Task 11 — Implement config export/import v2

**Files:** config helper pair and config handler/tests.

**RED selectors:** default v2, safe stable refs, shared repository remap, repeat
idempotency, whole-graph conflict rollback, disconnected imported bindings,
zero Provider calls, v1 compatibility, default secret omission, sensitive
binding only through existing proof/grant route, and absence of numeric source
IDs/proofs/grants/tickets/fences/private locators.

```bash
cd backend && go test ./internal/api/handlers -run '^TestConfig(ExportV2|ImportV2|ImportV1Compatibility|AssetGraph)' -count=1
```

Keep existing settings transition and foreign managed Task disconnect behavior.
Do not weaken config-export/import step-up or credential-grant middleware.
Export always emits empty `recovery_point_holds`. Import rejects any non-empty
hold list (`errConfigAssetGraphInvalid` / HTTP 400). Holds stay DB-plus-key
disaster-recovery facts and are not config-restorable.

### Task 12 — Add typed frontend lifecycle APIs

**Files:** repository API pair, new retention API pair, API client/tests, domain
types, and step-up storage/tests.

**RED selectors:** snake_case -> camelCase mapping, invalid/unknown fail-closed
states, request methods/bodies/headers, no direct fetch, impact counts/revisions,
import/rebuild results, proof propagation, and cross-purpose proof storage.

```bash
cd web && npm run test -- --run src/lib/api/backup-repositories-api.test.ts src/lib/api/backup-retention-api.test.ts src/lib/api/client.test.ts src/lib/step-up-storage.test.ts
```

Add only new policy/hold-record/import/impact/result types; reuse existing
repository/RecoveryPoint/hold projection unions.

### Task 13 — Build repository-management and retention-policy panels

**Files:** two new panel pairs, a11y test, workspace/tests, data page, domain
types, and zh/en locales.

**RED selectors:** replace the current read-only assertion with Admin-visible
reconnect/reconcile/disconnect/import/rebuild/policy/hold/purge flows; keep
non-Admin read-only behavior. Cover loading, empty, error, conflict, blocked,
success/refresh, exact impact confirmation, reason/proof, dialog focus return,
labels, keyboard, color-independent states, responsive layout, and no private
data rendering.

```bash
cd web && npm run test -- --run src/features/backup-assets/repository-management-panel.test.tsx src/features/backup-assets/retention-policy-panel.test.tsx src/features/backup-assets/backup-assets-workspace.test.tsx src/features/backup-assets/backup-assets-lifecycle-panels.a11y.test.tsx
```

Extract the existing embedded `RepositoryManagementView`; do not duplicate it.
Use existing primitives and `ensureStepUpProof`. Refresh repositories/recovery
points through existing controller actions after a successful mutation.

### Task 14 — Add disaster-recovery behavior and documentation

**Files:** retention behavior test, repository rebuild test, and
`docs/admin/backup-recovery.md`, `docs/admin/security.md`.

**RED selector:** simulate Provider facts with a fresh control-plane DB, valid
reconnect/import, wrong/missing encryption key, and absent overlays/audit.
Assert Provider-derived facts can rebuild only after authority, while overlays,
audit, policies, holds, bindings, and key readability require DB + correct key.

```bash
cd backend && go test ./internal/backupasset/retention ./internal/backupasset/repository -run '^TestDisasterRecovery' -count=1
```

Document exactly the behavioral matrix. Do not modify deployment/Worker/GA
metadata. Run the existing documentation freshness gate:

```bash
bash scripts/check-doc-freshness.sh
```

### Task 15 — Cross-engine, fault, race, privacy, and full gates

Run focused normal and race suites first:

```bash
cd backend && go test ./internal/backupasset/retention ./internal/backupasset/repository ./internal/backupasset/provider ./internal/task ./internal/api/... -run 'Retention|RetentionPolicy|RecoveryPointHold|Reconnect|Import|Rebuild|Purge|TaskArchive|AuditRetention|DisasterRecovery' -count=1
cd backend && go test -race ./internal/backupasset/retention ./internal/backupasset/repository ./internal/backupasset/provider ./internal/task -run 'Retention|Reconnect|Import|Purge|TaskArchive' -count=1
```

Run required real PostgreSQL lifecycle/behavior coverage without skip:

```bash
cd backend && REQUIRE_POSTGRES_MIGRATION_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" go test ./internal/database ./internal/backupasset/retention -run 'BackupAssetMigration070|RetentionBehaviorPostgres' -count=1
```

Run source/privacy/manifest checks:

```bash
rg -n 'RemoveAll|--min-age|forget|--prune' backend/internal/task/retention.go
rg -n 'provider_locator|rollback_locator|encrypted_config|fence_token|step_up|grant|ticket|private_key|password' backend/internal/api/handlers/backup_{repository,retention}_handler.go web/src/features/backup-assets/{repository-management-panel,retention-policy-panel}.tsx
git diff --name-only origin/main...HEAD
git diff --check
```

The first `rg` is reviewed semantically: managed authority must be absent; only
the explicitly isolated pristine-legacy compatibility seam may remain. The
privacy scan must have no raw private-field flow; field names in safe denial
tests are reviewed, not blindly accepted.

Then full gates:

```bash
cd backend && go test ./... && go build ./...
cd ../web && npm run check
cd .. && make swag-init && git diff --check
make check
bash scripts/check-doc-freshness.sh
```

Do not change dependencies to address an unrelated npm-audit residual. Record
any current audit result separately from product test status.

### Task 16 — Independent high-risk review and amendment loop

Request an independent review focused on:

- used-down migration admission and cross-engine parity;
- exact point selection/deletion and no broad Provider mutation;
- revoke/drain/lease/fence ordering and truthful `purge_blocked`;
- late-output and restart reconciliation;
- RecoveryResult ownership non-interference;
- RBAC/ownership/step-up pairwise isolation;
- API/audit/log/UI/config privacy;
- config v2 idempotency and zero Provider effect;
- runtime shutdown join and feature default false;
- exact changed-path manifest and Child 15 exclusion.

Record findings first in `research/review.md` as Critical, Important, Minor with
absolute file:line evidence. Resolve all Critical/Important findings with new
RED -> same selector GREEN evidence. Amend PRD/design/plan if review changes a
contract; do not hide the deviation in code comments.

### Task 17 — PR, CI, merge, post-merge, and archive

Only after Task 15 gates and Task 16 review are clean:

1. inspect the exact diff and verify `.codex/**`, Child 13 evidence, Child 15,
   deployment, lockfile, and `000071` are untouched;
2. verify `backup_assets.enabled` remains default false;
3. update Child 14 task evidence and parent progress to implemented-but-not-yet-
   delivered status;
4. create one coherent conventional commit on the dedicated branch;
5. push and open a PR with exact selectors, PostgreSQL evidence, privacy review,
   rollback, and explicit Child 15 exclusions;
6. monitor every required CI job, fix failures on the same branch with preserved
   TDD evidence, and do not merge pending/missing/failing checks;
7. squash merge only after required CI and review are green;
8. verify `origin/main` contains the exact squash, then monitor exact-main CI and
   Release Please;
9. record whether a formal GitHub Release/Docker publish was expected. A normal
   non-release Child 14 merge must explicitly record that no public image/release
   was expected; never invent one;
10. sync the root local `main` to `origin/main` without overwriting user work;
11. mark Child 14 complete_checked, archive it through Trellis, update the parent
    to program delivery 14/15, and record the session journal;
12. stop. Do not create or start Child 15.

Task 17 delivery receipt (2026-08-20): complete_checked. PR #435 squash
`a303abd08f47c52977036d12e1dfdab540283ee3` is on main; exact-squash CI
32342716003 and Release Please 32342716000 succeeded. v0.49.0 published from
`1a45cf107d47d25a05f471a49ab0284cd02abf87`, but Docker 32343632231 failed
Trivy HIGH on `golang.org/x/mod` v0.37.0 in `usr/local/bin/xirang`. Security
PR #437 squash `9b012ba2b73e2ddef81ced839f967a95cd8eb84e` and release PR #438
squash `43a8d067f92dae37ba583f8348fd7441a073d0f9` published v0.49.1; Docker
32347642009 succeeded for linux/amd64, linux/arm64, and the multi-arch
manifest. Exact-release main CI 32347625831 and Release Please 32347625898
succeeded. Sync Docker Hub Description did not run. `backup_assets.enabled`
remains false. Parent stays planning at 14/15. Child 15 is not created.

## 4. Parent 12-step coverage map

| Parent Child 14 logical step | Rebaselined tasks |
|---|---|
| 1. Task/policy/hold tests | 1, 3, 9 |
| 2. reconnect/import/rebuild | 4, 10, 12, 13 |
| 3. expiry/lease/hold/purge | 3, 5, 7 |
| 4. dependent cleanup/audit retention | 6, 8 |
| 5. prove old path/age bypass | 1 (the first RED) and 15 |
| 6. paired 000070/unified worker | 2, 5, 8 |
| 7. exact RecoveryPoint-ID retention | 1, 5, 7 |
| 8. Task archive/unlink | 9 |
| 9. policy/hold/reconnect/disconnect/purge API/UI | 10, 12, 13 |
| 10. config export/import v2 | 11 |
| 11. DB disaster recovery docs/tests | 14 |
| 12. focused/fault/full/delivery | 15, 16, 17 |

No parent safety semantic is delegated to Child 15 or weakened by this
rebaseline.

## 5. Planning self-review checklist

- [x] Live repository paths replace the stale `provider/provider.go` manifest.
- [x] Existing reconnect, RecoveryPoint states, leases, audit actions, hold
      projection, overlay tombstones, and RecoveryResult cleanup are reused.
- [x] `000070` remains Child 14; `000071` remains Child 15.
- [x] The first selector is an explicit genuine RED -> same selector GREEN gate.
- [x] All twelve parent logical steps map to exact tasks/selectors.
- [x] SQLite and required PostgreSQL apply/down/used-down evidence is explicit.
- [x] Provider deletion is separately registered and exact-point scoped.
- [x] Every dependent cleanup owner has a narrow point lifecycle port.
- [x] Task delete becomes archive/unlink without Provider side effects.
- [x] Config v2 stable refs, v1 compatibility, idempotency, disconnected import,
      and zero Provider mutation are explicit.
- [x] DR truth separates Provider-rebuildable facts from DB/key-owned facts.
- [x] Admin UI/API, a11y, i18n, privacy, and purpose-exact proof gates are explicit.
- [x] `backup_assets.enabled` remains false and Child 15/deploy/dependency scope is
      explicitly forbidden.
- [x] Independent review, PR/CI, exact-main/post-merge, archive, and stop boundary
      are delivery requirements.

## 6. Approval gate

The next authorized action is user review of this planning package. Do not run
`python3 ./.trellis/scripts/task.py start ...`, create implementation tests, or
edit any product path until the user explicitly approves the Child 14 plan and
permits the Phase 2 start transition.
