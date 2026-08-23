# GREEN checkpoint 2 — TaskRun product contract and operator guidance

Date: 2026-08-23

Scope: checkpoint 2 only. No commit, push, PR, P1 work, backup-asset enablement,
or Worker publication was performed.

## Genuine RED evidence

The package-owned TaskRun contract was introduced test-first. The initial
four-package gate failed to compile because the executable status/snapshot
contract did not exist yet:

```bash
cd backend
env TMPDIR=/home/murray/.cache/xirang-migration69-red/tmp \
  go test ./internal/database ./internal/model ./internal/task ./internal/repository/gorm
```

```text
internal/model/task_run_contract_test.go:9:12: undefined: TaskRunActiveStatuses
internal/model/task_run_contract_test.go:10:14: undefined: TaskRunTerminalStatuses
internal/task/manager_test.go:1756:73: undefined: model.TaskRunNodeIDLegacyUnknown
internal/task/manager_test.go:1769:24: undefined: model.TaskRunStatusPending
FAIL
```

After the test fixture was corrected to tolerate the deliberately removed
trigger, the version-specific clean-schema validator produced the intended RED:

```bash
cd backend
env TMPDIR=/home/murray/.cache/xirang-migration69-red/tmp \
  go test ./internal/database \
  -run '^TestBackupAssetMigration072SQLite$/CleanVersionMissingFinalContractIsRejected$' \
  -count=1
```

```text
clean sqlite version 72 missing final contract returned <nil>, want ErrMigrationSchemaDrift
FAIL xirang/backend/internal/database
```

The restore-authority regression also failed for the intended reason before
the success prerequisite was bound to the positive frozen node snapshot:

```bash
cd backend
env TMPDIR=/home/murray/.cache/xirang-migration69-red/tmp \
  go test ./internal/task \
  -run '^TestTriggerRestoreRejectsLegacyUnknownSuccessBeforeAdmission$' -count=1
```

```text
TriggerRestore legacy_unknown prerequisite run ID=0 error=同节点有恢复任务正在运行，请稍候再试: node write conflict
FAIL xirang/backend/internal/task
```

## Implemented checkpoint contract

- Added a model-owned closed TaskRun status set and the
  `legacy_unknown=0`/positive-authority classifier while keeping
  `node_id_snapshot` hidden from JSON.
- Ordinary reservation, executor entry, restore prerequisite checks, Recovery
  admission, publication locks, and versioning active-run queries reject a
  zero or mismatched TaskRun node snapshot before using it as authority.
- Version-aware startup validation now checks the 000069 minimum objects for
  every clean version >=69 and the 000072 final compatibility triggers plus
  the PostgreSQL compatibility constraint for versions >=72.
- Single-node deletion is characterized through the production repository:
  the Task is deleted, the terminal TaskRun is retained, and an upgrade from
  68 converges it to snapshot 0. Batch deletion at 72 retains positive frozen
  snapshots for both runs.
- Dirty startup covers legacy env unset, empty, false, true, and 1; every case
  returns `ErrMigrationDirty` with identical migration metadata and SQLite
  schema. An AST regression forbids `Force` and `allowDirtyStartup` in the
  production `RunMigrations` implementation.
- Removed the obsolete env from current deployment/env documentation and
  PostgreSQL export test setup. Current guidance is backup-first, read-only
  diagnosis, verified restore or audited offline repair; service startup never
  force-cleans dirty metadata.

## GREEN evidence

Focused migration/model/task gate:

```bash
cd backend
env TMPDIR=/home/murray/.cache/xirang-migration69-red/tmp \
  go test ./internal/database \
  -run '^(TestRunMigrationsRejectsDirtyForEveryLegacyEnvValue|TestRunMigrationsHasNoDirtyBypassOrForceCall|TestRunMigrationsRejectsCleanVersionSchemaDriftBeforeFixups|TestRunMigrationsClean071Applies072SQLite|TestBackupAssetMigration072SQLite)$' \
  -count=1
env TMPDIR=/home/murray/.cache/xirang-migration69-red/tmp \
  go test ./internal/model ./internal/task \
  -run '^(TestTaskRunStatusAndSnapshotContractIsClosed|TestReserveTaskRunRejectsUnknownOrMismatchedNodeBeforeAdmission|TestTriggerRestoreRejectsLegacyUnknownSuccessBeforeAdmission)$' \
  -count=1
```

```text
ok xirang/backend/internal/database 1.156s
ok xirang/backend/internal/model 0.004s
ok xirang/backend/internal/task 0.021s
```

Owned package gate:

```bash
cd backend
env TMPDIR=/home/murray/.cache/xirang-migration69-red/tmp \
  go test ./internal/database ./internal/model ./internal/task \
  ./internal/backupasset/repository ./internal/backupasset/recovery \
  ./internal/repository/gorm -count=1
```

```text
ok xirang/backend/internal/database 36.681s
ok xirang/backend/internal/model 0.148s
ok xirang/backend/internal/task 12.566s
ok xirang/backend/internal/backupasset/repository 8.208s
ok xirang/backend/internal/backupasset/recovery 46.215s
?  xirang/backend/internal/repository/gorm [no test files]
```

The obsolete PostgreSQL test env removal is compile/behavior safe:

```bash
env TMPDIR=/home/murray/.cache/xirang-migration69-red/tmp \
  go test ./internal/backupasset/export -count=1
```

```text
ok xirang/backend/internal/backupasset/export 20.873s
```

Repetition and race gates:

```bash
env TMPDIR=/home/murray/.cache/xirang-migration69-red/tmp \
  go test ./internal/database \
  -run '^(TestRunMigrationsRejectsDirtyForEveryLegacyEnvValue|TestRunMigrationsHasNoDirtyBypassOrForceCall|TestRunMigrationsRejectsCleanVersionSchemaDriftBeforeFixups|TestBackupAssetMigration0(69|72)SQLite)$' \
  -count=3
env TMPDIR=/home/murray/.cache/xirang-migration69-red/tmp \
  go test -race ./internal/database \
  -run '^(TestRunMigrationsRejectsDirtyForEveryLegacyEnvValue|TestRunMigrationsHasNoDirtyBypassOrForceCall|TestBackupAssetMigration0(69|72)SQLite)$' \
  -count=1
```

```text
ok xirang/backend/internal/database 29.698s
ok xirang/backend/internal/database 15.347s
```

```bash
env TMPDIR=/home/murray/.cache/xirang-migration69-red/tmp \
  go test -race ./internal/model ./internal/task ./internal/backupasset/runtime \
  -run '^(TestTaskRunStatusAndSnapshotContractIsClosed|TestRunTaskRejectsNonAuthoritativeNodeSnapshotBeforeExecutor|TestReserveTaskRunRejectsUnknownOrMismatchedNodeBeforeAdmission|TestTriggerRestoreRejectsLegacyUnknownSuccessBeforeAdmission|TestNodeWriteCoordinator.*)$' \
  -count=1
env TMPDIR=/home/murray/.cache/xirang-migration69-red/tmp \
  go test ./internal/model ./internal/task ./internal/backupasset/runtime \
  -run '^(TestTaskRunStatusAndSnapshotContractIsClosed|TestRunTaskRejectsNonAuthoritativeNodeSnapshotBeforeExecutor|TestReserveTaskRunRejectsUnknownOrMismatchedNodeBeforeAdmission|TestTriggerRestoreRejectsLegacyUnknownSuccessBeforeAdmission|TestNodeWriteCoordinator.*)$' \
  -count=3
```

```text
ok xirang/backend/internal/model 1.018s
ok xirang/backend/internal/task 1.136s
ok xirang/backend/internal/backupasset/runtime 1.803s
ok xirang/backend/internal/model 0.004s
ok xirang/backend/internal/task 0.090s
ok xirang/backend/internal/backupasset/runtime 0.323s
```

Checker and documentation gates:

```bash
env TMPDIR=/home/murray/.cache/xirang-migration69-red/tmp \
  bash scripts/check-backup-asset-migration.sh
bash scripts/check-doc-freshness.test.sh
bash scripts/check-doc-freshness.sh
git diff --check
```

```text
ok xirang/backend/internal/database 1.484s
backup-asset migration check: PASS
OK: doc freshness self-test passed
文档新鲜度检查通过
git diff --check: exit 0
```

The first checker invocation without the task-owned `TMPDIR` failed with
SQLite `disk quota exceeded`; the identical checker passed after applying the
required task-owned TMPDIR shown above. No shared files were deleted.

## PostgreSQL parity and environment-partitioned package evidence

The worker checkpoint initially failed closed because no PostgreSQL DSN was
available. Final root verification used the existing isolated PostgreSQL test
container and passed the complete required selector without recording the DSN:

```bash
env TMPDIR=/home/murray/.cache/xirang-migration69-red/tmp \
  REQUIRE_POSTGRES_MIGRATION_TEST=1 \
  go test ./internal/database \
  -run '^(TestBackupAssetMigration062PostgresApplyDown|TestBackupAssetMigration0(63|64|65|66|67|68|69|70|71|72)Postgres|TestPostgresTimestamptzScanUsesConfiguredUTC|TestRunMigrationsPostgresDirtyCheckUsesSearchPath|TestRunMigrationsPostgresSchemaDriftCheckUsesSearchPath)$' \
  -count=1
```

```text
ok xirang/backend/internal/database 278.542s
```

This verified both migration paths, paired down migrations, PostgreSQL
`search_path` dirty/schema-drift checks, and the guard-failure atomicity of
000069/000072. PostgreSQL 000069 deliberately uses the simple-query implicit
transaction rather than embedding `BEGIN`/`COMMIT`: a guard failure therefore
rolls back atomically without returning an aborted pooled connection.

A low-parallel whole-backend scan found only tests whose filesystem contracts
cannot all be satisfied by one local `TMPDIR`: content/provider/runtime require
a dedicated filesystem, while processing/updater require a short Unix-socket
path plus durable space. Partitioning those environment-sensitive packages
made every affected package green:

```bash
TMPDIR=/dev/shm/p0 go test ./internal/backupasset/content \
  ./internal/dashboards/providers ./internal/backupasset/runtime -count=1
TMPDIR=/home/murray/xp go test ./internal/backupasset/processing \
  ./internal/backupasset/updater -count=1
```

```text
ok xirang/backend/internal/backupasset/content 1.375s
ok xirang/backend/internal/dashboards/providers 1.083s
ok xirang/backend/internal/backupasset/runtime 7.580s
ok xirang/backend/internal/backupasset/processing 3.389s
ok xirang/backend/internal/backupasset/updater 2.487s
```

## Main-review authority closure

A final consumer audit found four remaining queries that could still treat a
TaskRun as authority without binding the frozen node snapshot, plus one runner
query that did not bind the TaskRun's Task ID. Focused tests produced genuine
RED before the production correction:

```bash
cd backend
env TMPDIR=/home/murray/.cache/xirang-migration69-red/tmp \
  go test ./internal/api/handlers ./internal/task \
  -run '^(TestTaskRestoreGrantEligibilityRequiresAuthoritativeSuccessSnapshot|TestDrillSourceIgnoresNonAuthoritativeSuccessRuns|TestReconcileInterruptedRunsIgnoresLegacyUnknownActiveRun|TestReportInterruptedPublicationLegacyUnknownNewerRunDoesNotBlockAggregate|TestRunTaskRejectsTaskRunOwnedByAnotherTaskBeforeExecutor)$' \
  -count=1
```

```text
legacy_unknown: eligibility=true, want false
mismatched: eligibility=true, want false
latestSuccessfulRunID: too many arguments (new task+node contract not implemented)
FAIL
```

The minimal correction bound restore grants and drill sources to
`task_id + node_id_snapshot + success`, excluded snapshot `0` from interrupted
publication authority, and bound pre-executor admission to the requested Task
ID. The identical focused selector then passed:

```text
ok xirang/backend/internal/api/handlers 0.072s
ok xirang/backend/internal/task 0.030s
```

## Final model authority, lint, and build closure

The final audit tightened ordinary GORM TaskRun creation itself: a new row now
requires an existing Task with a positive node, auto-fills an omitted snapshot,
and rejects zero, missing, or mismatched Task authority. Repository helpers for
publication and active-run admission received direct fail-closed tests. The
updated owned packages then passed together:

```text
ok xirang/backend/internal/database 38.145s
ok xirang/backend/internal/model 0.290s
ok xirang/backend/internal/task 15.041s
ok xirang/backend/internal/api/handlers 14.470s
ok xirang/backend/internal/backupasset/repository 9.043s
?  xirang/backend/internal/repository/gorm [no test files]
ok xirang/backend/internal/backupasset/export 22.700s
ok xirang/backend/internal/backupasset/recovery 45.391s
ok xirang/backend/internal/backupasset/runtime 7.613s (dedicated /dev/shm TMPDIR)
```

The host's global linter was built with Go 1.26 but attempted to parse the Go
1.27 standard library. A task-local `golangci-lint v2.11.4` was rebuilt and run
with the repository-declared Go 1.26.6 toolchain, matching CI. It found one
unchecked test `rows.Close` error; after that genuine lint fix, the complete
lint and production build gates passed:

```bash
PATH=/home/murray/.cache/xirang-migration69-red/bin-go126:$PATH \
  GOTOOLCHAIN=go1.26.6 \
  TMPDIR=/home/murray/.cache/xirang-migration69-red/tmp \
  make lint
GOTOOLCHAIN=go1.26.6 \
  TMPDIR=/home/murray/.cache/xirang-migration69-red/tmp \
  make build
```

```text
golangci-lint: 0 issues
eslint: exit 0
backend go build: exit 0
frontend tsc -b && vite build: exit 0
```
