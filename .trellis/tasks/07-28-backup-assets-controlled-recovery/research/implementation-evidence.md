# Task 1 000069 implementation evidence

## RED

```bash
cd /home/murray/code/xirang/backend
go test ./internal/database -run 'BackupAssetMigration069' -count=1
```

Result: failed before implementation because migration version `000069` and all
four paired `000069_backup_asset_recovery` SQL files were absent. This genuine
RED was controller-reproduced; no expectation was weakened.

## GREEN: SQLite and model

```bash
cd /home/murray/code/xirang/backend
gofmt -w internal/database/backup_asset_migrations_integration_test.go \
  internal/model/backup_asset_recovery.go internal/model/backup_asset_recovery_test.go
go test ./internal/database -run 'BackupAssetMigration069' -count=1
```

```text
ok  	xirang/backend/internal/database	0.924s
```

```bash
cd /home/murray/code/xirang/backend
go test ./internal/model -run 'BackupAssetRecovery' -count=1
```

```text
ok  	xirang/backend/internal/model	0.026s
```

## GREEN: PostgreSQL 18

Disposable container: `codex-child13-task1-pg18`, PostgreSQL 18, host port
`127.0.0.1:55472`; it was removed after the commands below completed.

```bash
cd /home/murray/code/xirang/backend
REQUIRE_POSTGRES_MIGRATION_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" \
  go test ./internal/database -run '^TestBackupAssetMigration069Postgres$' -count=1
```

```text
ok  	xirang/backend/internal/database	19.547s
```

```bash
cd /home/murray/code/xirang/backend
REQUIRE_POSTGRES_MIGRATION_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" \
  go test ./internal/database \
  -run '^(TestBackupAssetMigration062PostgresApplyDown|TestBackupAssetMigration0(63|64|65|66|67|68|69)Postgres|TestPostgresTimestamptzScanUsesConfiguredUTC|TestRunMigrationsPostgresDirtyCheckUsesSearchPath)$' \
  -count=1
```

```text
ok  	xirang/backend/internal/database	125.304s
```

```bash
docker rm -f codex-child13-task1-pg18
docker ps -a --filter name=^/codex-child13-task1-pg18$ --format '{{.Names}}'
```

```text
codex-child13-task1-pg18
```

The final `docker ps -a` command produced no output.

## GREEN: focused aggregate and static scope

```bash
cd /home/murray/code/xirang/backend
go test ./internal/database ./internal/model ./internal/backupasset/recovery -count=1
go vet ./internal/database ./internal/model ./internal/backupasset/recovery
```

```text
ok  	xirang/backend/internal/database	6.075s
ok  	xirang/backend/internal/model	0.069s
ok  	xirang/backend/internal/backupasset/recovery	0.002s
```

`go vet` produced no output and exited zero.

```bash
cd /home/murray/code/xirang
git diff --check
git diff --cached --quiet
find backend/internal/database/migrations/sqlite \
  backend/internal/database/migrations/postgres -maxdepth 1 -type f \
  -name '000069_*' -print | sort
find backend/internal/database/migrations/sqlite \
  backend/internal/database/migrations/postgres -maxdepth 1 -type f \
  -regextype posix-extended -regex '.*/0000(70|71|72|73|74|75|76|77|78|79|[89][0-9]).*' -print | sort
```

`git diff --check` and `git diff --cached --quiet` exited zero. The first
`find` printed exactly the four paired `000069` files; the second printed no
`000070+` migration files.

## Task 1 Downgrade-Admission Correction

### RED: Real Runner Before Admission

The used-down matrix was changed from direct `db.Exec` of the 000069 down SQL
to the production runner path, `migrator.Steps(-1)`, before changing migration
SQL.

```bash
cd /home/murray/code/xirang/backend
go test ./internal/database -run '^TestBackupAssetMigration069SQLite$' -count=1
```

Result: failed for every real-runner refusal case:
`EveryRecoveryFamily`, `LatchOnlyCrashAfterCommit`,
`PurgeToEmptyEvidenceStillLeavesPermanentLatch`,
`RecoveryContentGrantAndRequest`, `SharedContentUsage`,
`ContentSessionLease`, and `ActiveRecoveryJobLease`.

```text
rejected 000069 down changed migration snapshot: version=69 dirty=false ->
version=68 dirty=true; tables_changed=false definitions_changed=false
indexes_changed=false triggers_changed=false rows_changed=false
```

This confirmed that the existing first-statement down guard ran only after
`golang-migrate` had independently committed `SetVersion(68, true)`.

### GREEN: Paired Admission

The four paired 000069 SQL files now install a
`trg_backup_asset_recovery_downgrade_admission` trigger on
`schema_migrations`. It rejects only inserts below version 69 while the same
complete used-state guard is nonempty. SQLite and pgx roll back their own
delete/truncate-plus-insert `SetVersion` transaction, preserving 69 clean.
The trigger permits version 69 and later, and pristine down removes it before
the protected tables disappear. The integration snapshot now includes all
affected tables, definitions, indexes, triggers, and row counts.

```bash
cd /home/murray/code/xirang/backend
gofmt -w internal/database/backup_asset_migrations_integration_test.go
go test ./internal/database -run '^TestBackupAssetMigration069SQLite$' -count=1
go test ./internal/database -run '^TestBackupAssetMigration069PairedFiles$' -count=1
```

```text
ok  	xirang/backend/internal/database	0.872s
ok  	xirang/backend/internal/database	0.005s
```

```bash
cd /home/murray/code/xirang/backend
REQUIRE_POSTGRES_MIGRATION_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" \
  go test ./internal/database -run '^TestBackupAssetMigration069Postgres$' -count=1
REQUIRE_POSTGRES_MIGRATION_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" \
  go test ./internal/database \
  -run '^(TestBackupAssetMigration062PostgresApplyDown|TestBackupAssetMigration0(63|64|65|66|67|68|69)Postgres|TestPostgresTimestamptzScanUsesConfiguredUTC|TestRunMigrationsPostgresDirtyCheckUsesSearchPath)$' \
  -count=1
```

```text
ok  	xirang/backend/internal/database	7.097s
ok  	xirang/backend/internal/database	51.476s
```

```bash
cd /home/murray/code/xirang/backend
go test ./internal/database -count=1
go test ./internal/model ./internal/backupasset/recovery -count=1
go vet ./internal/database ./internal/model ./internal/backupasset/recovery

cd /home/murray/code/xirang
gofmt -d backend/internal/database/backup_asset_migrations_integration_test.go
git diff --check
python3 ./.trellis/scripts/task.py validate .trellis/tasks/07-28-backup-assets-controlled-recovery
python3 ./.trellis/scripts/task.py validate .trellis/tasks/07-12-backup-data-explorer-design
```

```text
ok  	xirang/backend/internal/database	5.808s
ok  	xirang/backend/internal/model	0.070s
ok  	xirang/backend/internal/backupasset/recovery	0.002s
```

`go vet`, `gofmt -d`, and `git diff --check` produced no output. Both task
validators passed.

### PostgreSQL 18 Container Lifecycle

One disposable PostgreSQL 18 container named
`codex-child13-task1-pg18-downgrade` was used on `127.0.0.1:55473`. The normal
bridged start failed because this environment cannot create a veth pair, so the
same single container used host networking with PostgreSQL explicitly bound to
that verified-unused localhost port. It was removed with:

```bash
docker rm -f codex-child13-task1-pg18-downgrade
docker ps -a --filter name=^/codex-child13-task1-pg18-downgrade$ --format '{{.Names}}'
```

The removal command printed the container name; the final `docker ps -a` command
printed no rows.

### Final Scope Checks

`git diff --cached --quiet` exited zero. `git diff --check` and `gofmt -d`
produced no output. The migration scan listed exactly these four 000069 files:

```text
backend/internal/database/migrations/postgres/000069_backup_asset_recovery.down.sql
backend/internal/database/migrations/postgres/000069_backup_asset_recovery.up.sql
backend/internal/database/migrations/sqlite/000069_backup_asset_recovery.down.sql
backend/internal/database/migrations/sqlite/000069_backup_asset_recovery.up.sql
```

The 000070+ scan and final named-container scan both produced no rows. The
tracked and untracked dirty union remains within the approved Child 13 manifest;
this correction touched only its assigned test, four 000069 migrations,
implementation evidence, and the permitted database guideline.

## Task 1 PostgreSQL Evidence-Trigger And Isolated Content Correction

### RED: Ordinary PostgreSQL Evidence UPDATE Was Silently Discarded

The existing ordinary-evidence regression was retained. Before changing the
PostgreSQL migration SQL, the exact real-PostgreSQL 000069 selector failed
because the latch trigger function returned `OLD` for every ordinary UPDATE:

```bash
cd /home/murray/code/xirang/backend
REQUIRE_POSTGRES_MIGRATION_TEST=1 \
TEST_POSTGRES_DSN='postgres://postgres:FAKE_POSTGRES_PASSWORD_FOR_TEST_ONLY@127.0.0.1:55474/postgres?sslmode=disable' \
  go test ./internal/database -run '^TestBackupAssetMigration069Postgres$' -count=1
```

```text
--- FAIL: TestBackupAssetMigration069Postgres (7.95s)
    --- FAIL: TestBackupAssetMigration069Postgres/UseLatchImmutabilityAndOrdinaryEvidenceUpdates (0.74s)
        backup_asset_migrations_integration_test.go:1047: postgres ordinary recovery evidence update did not persist: outcome="succeeded" updated_at=2026-07-28T18:25:12Z, want degraded/2026-07-28T18:25:13Z
FAIL
FAIL    xirang/backend/internal/database    7.960s
```

This is the expected genuine RED: the SQL UPDATE succeeded but neither changed
column persisted.

### Test-First Content Admission Isolation

Before editing SQL, the used-down matrix replaced the aggregate-backed combined
Content case with separate `RecoveryContentGrantOnly` and
`RecoveryContentRequest` real-runner cases. Each inserts an otherwise
CHECK-valid RecoveryResult Content row while suppressing only foreign-key
trigger enforcement for the intentionally orphaned grant. The assertions prove
all twelve Recovery tables, the use latch, shared usage, and Content/Recovery
leases are empty before `migrator.Steps(-1)`, then compare the complete version,
dirty flag, table definitions, indexes, triggers, and row counts after refusal.

```bash
cd /home/murray/code/xirang/backend
go test ./internal/database \
  -run '^TestBackupAssetMigration069SQLite$/UsedDownIsRejectedAtomically/RecoveryContent(GrantOnly|Request)$' \
  -count=1
```

```text
ok      xirang/backend/internal/database    0.149s
```

```bash
cd /home/murray/code/xirang/backend
REQUIRE_POSTGRES_MIGRATION_TEST=1 \
TEST_POSTGRES_DSN='postgres://postgres:FAKE_POSTGRES_PASSWORD_FOR_TEST_ONLY@127.0.0.1:55474/postgres?sslmode=disable' \
  go test ./internal/database \
  -run '^TestBackupAssetMigration069Postgres$/UsedDownIsRejectedAtomically/RecoveryContent(GrantOnly|Request)$' \
  -count=1
```

```text
ok      xirang/backend/internal/database    1.145s
```

### Minimal GREEN

The PostgreSQL UPDATE and DELETE triggers now use the same fixed-ID/kind
`WHEN` predicate as SQLite. Ordinary evidence rows no longer invoke the latch
function; the distinguished `schema_use_latch` row still raises on UPDATE and
DELETE. No table, migration number, down behavior, or latch lifecycle changed.

```bash
cd /home/murray/code/xirang/backend
REQUIRE_POSTGRES_MIGRATION_TEST=1 \
TEST_POSTGRES_DSN='postgres://postgres:FAKE_POSTGRES_PASSWORD_FOR_TEST_ONLY@127.0.0.1:55474/postgres?sslmode=disable' \
  go test ./internal/database -run '^TestBackupAssetMigration069Postgres$' -count=1
```

```text
ok      xirang/backend/internal/database    7.952s
```

```bash
cd /home/murray/code/xirang/backend
gofmt -w internal/database/backup_asset_migrations_integration_test.go
go test ./internal/database -run '^TestBackupAssetMigration069SQLite$' -count=1
go test ./internal/database -run '^TestBackupAssetMigration069PairedFiles$' -count=1
go test ./internal/model -run 'BackupAssetRecovery' -count=1
go test ./internal/backupasset/recovery -run 'State|Contract' -count=1
```

```text
ok      xirang/backend/internal/database                 0.965s
ok      xirang/backend/internal/database                 0.006s
ok      xirang/backend/internal/model                    0.028s
ok      xirang/backend/internal/backupasset/recovery     0.002s
```

```bash
cd /home/murray/code/xirang/backend
REQUIRE_POSTGRES_MIGRATION_TEST=1 \
TEST_POSTGRES_DSN='postgres://postgres:FAKE_POSTGRES_PASSWORD_FOR_TEST_ONLY@127.0.0.1:55474/postgres?sslmode=disable' \
  go test ./internal/database \
  -run '^(TestBackupAssetMigration062PostgresApplyDown|TestBackupAssetMigration0(63|64|65|66|67|68|69)Postgres|TestPostgresTimestamptzScanUsesConfiguredUTC|TestRunMigrationsPostgresDirtyCheckUsesSearchPath)$' \
  -count=1
```

```text
ok      xirang/backend/internal/database    53.961s
```

```bash
cd /home/murray/code/xirang/backend
go test ./internal/database -count=1
go test ./internal/model ./internal/backupasset/recovery -count=1
go vet ./internal/database ./internal/model ./internal/backupasset/recovery
```

```text
ok      xirang/backend/internal/database                 5.845s
ok      xirang/backend/internal/model                    0.063s
ok      xirang/backend/internal/backupasset/recovery     0.002s
```

`go vet` produced no output and exited zero.

### PostgreSQL 18 Lifecycle And Final Integrity

One disposable PostgreSQL 18 container named
`codex-child13-task1-pg18-trigger` used host networking with PostgreSQL bound
only to the verified-unused `127.0.0.1:55474`. It was removed after the exact
000069 and historical 000062-000069 selectors completed:

```bash
docker rm -f codex-child13-task1-pg18-trigger
test -z "$(docker ps -a --filter name=^/codex-child13-task1-pg18-trigger$ --format '{{.Names}}')"
test -z "$(ss -H -ltn 'sport = :55474')"
```

The removal printed the container name; both absence checks produced no output
and exited zero.

```bash
cd /home/murray/code/xirang
gofmt -d backend/internal/database/backup_asset_migrations_integration_test.go
git diff --check
python3 ./.trellis/scripts/task.py validate \
  .trellis/tasks/07-28-backup-assets-controlled-recovery
python3 ./.trellis/scripts/task.py validate \
  .trellis/tasks/07-12-backup-data-explorer-design
test -z "$(git diff --cached --name-only)"
test "$(find backend/internal/database/migrations/sqlite \
  backend/internal/database/migrations/postgres -maxdepth 1 -type f \
  -name '000069_*' | wc -l | tr -d ' ')" = 4
test -z "$(find backend/internal/database/migrations/sqlite \
  backend/internal/database/migrations/postgres -maxdepth 1 -type f \
  -regextype posix-extended -regex '.*/0000(7[0-9]|[89][0-9]).*' -print)"
```

`gofmt -d` and `git diff --check` produced no output. Both Trellis task
validators passed. Staging remained empty, exactly four paired 000069 files
were present, and the 000070+ scan produced no rows.

## Task 1 Review Remediation: Grant Terminality, Unique Structure, And Limited PostgreSQL Role

### RED: Recovery Grant Terminal Rewrites And Duplicate Job `plan_id` Uniqueness

Before changing the paired migrations, the new focused SQLite selector observed
all required illegal grant transitions and the duplicate physical uniqueness
structure:

```bash
cd /home/murray/code/xirang/backend
gofmt -w internal/database/backup_asset_migrations_integration_test.go
go test ./internal/database \
  -run '^TestBackupAssetMigration069SQLite$/^(GrantTerminalTransitions|PlanIDUsesExactlyOneUniqueStructure)$' \
  -count=1
```

```text
--- FAIL: TestBackupAssetMigration069SQLite
    --- FAIL: TestBackupAssetMigration069SQLite/GrantTerminalTransitions
        sqlite recovery grant terminal transitions unexpectedly allowed:
        consumed_at to NULL, consumed_at timestamp rewrite, consumed to revoked,
        revoked_at to NULL, revoked_at timestamp rewrite, revoked to consumed,
        active grant with both terminal timestamps
    --- FAIL: TestBackupAssetMigration069SQLite/PlanIDUsesExactlyOneUniqueStructure
        sqlite recovery jobs has 2 unique plan_id structures, want exactly one named index
FAIL
```

The same test proves the two legal transitions, active-to-consumed and
active-to-revoked, before it probes each prohibited rewrite in its own rolled
back transaction.

### RED: PostgreSQL Limited Role Could Not Use The Superuser Fixture Bypass

Disposable PostgreSQL 18 instance:
`codex-child13-task1-pg18-limited`, host networking, `127.0.0.1:55475`.
It created `xirang_recovery_limited` with `LOGIN`, database `CONNECT` and
database `CREATE`, but no superuser, role-admin, or database-owner privilege.
The role could create its own schema, and PostgreSQL rejected the old fixture's
superuser-only mechanism:

```bash
docker exec -e PGPASSWORD=FAKE_LIMITED_POSTGRES_PASSWORD_FOR_TEST_ONLY \
  codex-child13-task1-pg18-limited psql -v ON_ERROR_STOP=1 \
  -h 127.0.0.1 -p 55475 -U xirang_recovery_limited -d postgres \
  -c 'SET session_replication_role = replica'
```

```text
ERROR:  permission denied to set parameter "session_replication_role"
```

The exact isolated grant-only selector then failed for that same reason before
the fixture implementation changed:

```bash
cd /home/murray/code/xirang/backend
REQUIRE_POSTGRES_MIGRATION_TEST=1 \
TEST_POSTGRES_DSN='postgres://xirang_recovery_limited:FAKE_LIMITED_POSTGRES_PASSWORD_FOR_TEST_ONLY@127.0.0.1:55475/postgres?sslmode=disable' \
  go test ./internal/database \
  -run '^TestBackupAssetMigration069Postgres$/^UsedDownIsRejectedAtomically$/^RecoveryContentGrantOnly$' \
  -count=1
```

```text
--- FAIL: TestBackupAssetMigration069Postgres/UsedDownIsRejectedAtomically/RecoveryContentGrantOnly
    suspend postgres foreign-key triggers for isolated recovery Content grant:
    ERROR: permission denied to set parameter "session_replication_role" (SQLSTATE 42501)
FAIL
```

### GREEN: Minimal Paired Enforcement And Portable Fixture

Both up migrations now retain only
`idx_backup_asset_recovery_jobs_plan` as the unique `plan_id` structure. They
add a mutual-exclusion CHECK and the paired terminal-transition trigger; the
PostgreSQL function/trigger and SQLite trigger are explicitly removed by their
respective down migrations.

The PostgreSQL isolated Content fixture now creates its valid ordinary owner
and lease parents, temporarily drops only the two RecoveryResult foreign keys
as the table owner, inserts the intentional orphan, and deletes it before
recreating the exact `pg_get_constraintdef` definitions. Its snapshot now also
compares those foreign keys, so cleanup proves the complete pre-isolation
schema/data snapshot is restored. Product foreign keys and the 000069 schema
remain unchanged.

```bash
cd /home/murray/code/xirang/backend
go test ./internal/database -run '^TestBackupAssetMigration069SQLite$' -count=1
REQUIRE_POSTGRES_MIGRATION_TEST=1 \
TEST_POSTGRES_DSN='postgres://xirang_recovery_limited:FAKE_LIMITED_POSTGRES_PASSWORD_FOR_TEST_ONLY@127.0.0.1:55475/postgres?sslmode=disable' \
  go test ./internal/database -run '^TestBackupAssetMigration069Postgres$' -count=1
REQUIRE_POSTGRES_MIGRATION_TEST=1 \
TEST_POSTGRES_DSN='postgres://postgres:FAKE_POSTGRES_PASSWORD_FOR_TEST_ONLY@127.0.0.1:55475/postgres?sslmode=disable' \
  go test ./internal/database -run '^TestBackupAssetMigration069Postgres$' -count=1
```

```text
ok      xirang/backend/internal/database    1.080s
ok      xirang/backend/internal/database    9.360s
ok      xirang/backend/internal/database    9.143s
```

```bash
cd /home/murray/code/xirang/backend
REQUIRE_POSTGRES_MIGRATION_TEST=1 \
TEST_POSTGRES_DSN='postgres://postgres:FAKE_POSTGRES_PASSWORD_FOR_TEST_ONLY@127.0.0.1:55475/postgres?sslmode=disable' \
  go test ./internal/database \
  -run '^(TestBackupAssetMigration062PostgresApplyDown|TestBackupAssetMigration0(63|64|65|66|67|68|69)Postgres|TestPostgresTimestamptzScanUsesConfiguredUTC|TestRunMigrationsPostgresDirtyCheckUsesSearchPath)$' \
  -count=1
go test ./internal/database -count=1
go test ./internal/model ./internal/backupasset/recovery -count=1
go vet ./internal/database ./internal/model ./internal/backupasset/recovery
```

```text
ok      xirang/backend/internal/database    54.465s
ok      xirang/backend/internal/database    6.606s
ok      xirang/backend/internal/model       0.093s
ok      xirang/backend/internal/backupasset/recovery  0.003s
```

`go vet` produced no output and exited zero.

### Historical Model/State/Contract TDD Evidence

Status: `not_observed_before_green`.

The original Task 1 model and recovery State/Contract implementation had no
recorded pre-GREEN RED. That process deviation is irreversible and is not
backfilled or described as TDD. The following frozen Task 1-owned review names
were added only as post-GREEN regression coverage and passed immediately:

```bash
cd /home/murray/code/xirang/backend
go test ./internal/database ./internal/model ./internal/backupasset/recovery \
  -run '^(TestRecoveryReviewF1TargetModeAndOperationDigests|TestRecoveryReviewF6UseLatchSQLite|TestRecoveryReviewF6UsedDownAtomicRefusal|TestRecoveryReviewF8LocatorCiphertextAtRest)$' \
  -count=1
```

```text
ok      xirang/backend/internal/database                 0.659s
ok      xirang/backend/internal/model                    0.031s
ok      xirang/backend/internal/backupasset/recovery     0.003s
```

These are post-GREEN coverage, not observed RED evidence.

### PostgreSQL 18 Container Cleanup

After the limited-role and normal-role selectors completed, the disposable
container and its host port were removed:

```bash
docker rm -f codex-child13-task1-pg18-limited
docker ps -a --filter name=^/codex-child13-task1-pg18-limited$ --format '{{.Names}}'
ss -H -ltn 'sport = :55475'
```

The removal command printed `codex-child13-task1-pg18-limited`; both absence
checks produced no output.

### Broad Backend Gate Limitation

The focused database/model/recovery checks and real-PostgreSQL selectors above
passed. A subsequent `cd backend && go test ./...` could not complete because
the environment exhausted its `/tmp` per-process disk quota while linking
unrelated packages and writing large updater fixtures. Representative output:

```text
write /tmp/go-link-...: disk quota exceeded
write /tmp/TestServiceStreams.../bundle.tar: disk quota exceeded
```

The same run reported `ok` for `internal/database`, `internal/model`, and
`internal/backupasset/recovery`. This is an environment-capacity limitation,
not evidence that the broad backend gate passed.

## Task 1 Review-Finding Follow-up: Cleanup Fence And Mutation Arm

### Root Cause

The paired `000069` ResultSet CHECK admitted `state='cleaned'` with
`cleanup_attempt > 0` but did not require a positive `cleanup_fence`. A ready
row could therefore become a cleaned/tombstoned row without durable cleanup
fence allocation.

The paired attempt CHECK required an armed attempt to remain `running`. An
armed attempt could not close terminally unless its arm was cleared, and neither
engine had a trigger preventing that true-to-false rewrite. A terminal/takeover
could therefore erase the mutation ambiguity used by the pre-write supersede
guard.

### RED

The following test-only change was written before the four migration files were
edited. It asserts zero-fence cleaned ResultSets, the direct ready-to-cleaned
shape, all four requested armed terminal states, direct and takeover arm
erasure, and arm preservation through the existing supersede guard.

```bash
cd /home/murray/code/xirang/backend
gofmt -w internal/database/backup_asset_migrations_integration_test.go
go test ./internal/database -run '^TestBackupAssetMigration069SQLite/(ResultSetCleanupFenceInvariant|AttemptMutationArmTerminalAndTakeoverInvariant)$' -count=1
```

The SQLite RED failed as required:

```text
CleanedResultSetCannotLoseAllocatedCleanupFence: sqlite invalid migration row unexpectedly succeeded
ReadyResultSetCannotTransitionDirectlyToZeroFenceTombstone: sqlite invalid migration row unexpectedly succeeded
ArmedAttemptsCanReachTerminalStates/{lost,failed,completed,canceled}:
  CHECK constraint failed: mutation_armed = 0 OR state = 'running'
MutationArmCannotBeCleared: sqlite invalid migration row unexpectedly succeeded
TerminalTakeoverCannotEraseArm: sqlite terminal takeover erased a durable mutation arm
ArmedTerminalAttemptStillDeniesPreWriteSupersedeAfterTakeover:
  CHECK constraint failed: mutation_armed = 0 OR state = 'running'
```

The requested claimed-state denial already existed and passed immediately; it
is regression coverage, not represented as a defect RED:

```bash
cd /home/murray/code/xirang/backend
go test ./internal/database -run '^TestBackupAssetMigration069SQLite/AttemptMutationArmTerminalAndTakeoverInvariant/ClaimedAttemptCannotArm$' -count=1
```

```text
ok  xirang/backend/internal/database  0.078s
```

The paired-file/down-cleanup assertions were also added before SQL edits and
failed because neither engine declared or removed the monotonic-arm trigger:

```bash
cd /home/murray/code/xirang/backend
go test ./internal/database -run '^TestBackupAssetMigration069PairedFiles$' -count=1
```

```text
SQLiteUp/PostgresUp: missing "trg_backup_asset_recovery_attempts_mutation_arm_monotonic"
SQLiteDown/PostgresDown: missing "trg_backup_asset_recovery_attempts_mutation_arm_monotonic"
```

### Minimal GREEN

- Both ResultSet CHECKs now require `cleanup_fence > 0` for `cleaned` while
  retaining the valid tombstone, owner/lease, node-fence, and attempt clauses.
- Both attempt CHECKs allow an armed `running`, `completed`, `failed`,
  `canceled`, or `lost` attempt. `claimed` and `superseded` remain unarmed.
- SQLite adds `trg_backup_asset_recovery_attempts_mutation_arm_monotonic`.
  PostgreSQL adds the matching trigger plus
  `backup_asset_recovery_attempt_mutation_arm_monotonic()`. Both down scripts
  explicitly remove their trigger; PostgreSQL also drops its function.
- No model edit was required: the existing GORM metadata already matches the
  affected columns/defaults, while the cross-field CHECK and trigger are
  migration-only contracts.

### GREEN And Verification

```bash
cd /home/murray/code/xirang/backend
gofmt -w internal/database/backup_asset_migrations_integration_test.go
go test ./internal/database -run '^TestBackupAssetMigration069SQLite/(ResultSetCleanupFenceInvariant|AttemptMutationArmTerminalAndTakeoverInvariant)$' -count=1
go test ./internal/database -run '^TestBackupAssetMigration069PairedFiles$' -count=1
go test ./internal/database -run '^TestBackupAssetMigration069SQLite$' -count=1
go test ./internal/database -run '^TestRecoveryReviewF6(UseLatchSQLite|UsedDownAtomicRefusal)$' -count=1
go test ./internal/database -count=1
go test ./internal/model -run 'BackupAssetRecovery' -count=1
go test ./internal/backupasset/recovery -run 'State|Contract' -count=1
go vet ./internal/database ./internal/model ./internal/backupasset/recovery
```

```text
ok  xirang/backend/internal/database             0.748s
ok  xirang/backend/internal/database             0.006s
ok  xirang/backend/internal/database             1.814s
ok  xirang/backend/internal/database             0.656s
ok  xirang/backend/internal/database             7.356s
ok  xirang/backend/internal/model                0.032s
ok  xirang/backend/internal/backupasset/recovery 0.003s
```

`go vet` produced no output and exited zero. The pristine-down contract also
asserts that the new trigger is absent after both engines down-migrate, and that
the PostgreSQL function is absent after its pristine down.

### PostgreSQL 18

Docker was available. A fresh normal-role PostgreSQL 18 container named
`codex-child13-task1-review-pg18` ran with host networking on verified-unused
`127.0.0.1:55474`; this migration fixture creates isolated schemas itself and
has no separate limited-role selector for this contract.

```bash
cd /home/murray/code/xirang/backend
REQUIRE_POSTGRES_MIGRATION_TEST=1 TEST_POSTGRES_DSN='postgres://xirang_review:FAKE_PASSWORD_FOR_TEST_ONLY@127.0.0.1:55474/xirang_review?sslmode=disable' go test ./internal/database -run '^TestBackupAssetMigration069Postgres$' -count=1
```

```text
ok  xirang/backend/internal/database  15.567s
```

The wider paired PostgreSQL migration selector also completed against this
fresh service without emitted failures. The container cleanup was:

```bash
docker rm -f codex-child13-task1-review-pg18
docker ps -a --filter name=^/codex-child13-task1-review-pg18$ --format '{{.Names}}'
ss -ltn '( sport = :55474 )'
```

The removal command printed the container name. Both absence checks produced no
output.

### Cleanup And Scope

```bash
cd /home/murray/code/xirang
git diff --check
git diff --cached --quiet
python3 ./.trellis/scripts/task.py validate .trellis/tasks/07-28-backup-assets-controlled-recovery
python3 ./.trellis/scripts/task.py validate .trellis/tasks/07-12-backup-data-explorer-design
rg --files backend/internal/database/migrations -g '000069_*'
rg --files backend/internal/database/migrations -g '00007[0-9]_*'
```

`git diff --check` produced no output and the index remained empty. Both task
validators passed. The first migration scan printed exactly the four paired
`000069_backup_asset_recovery` files; the `000070+` scan printed no files. No
container or listener remained. This follow-up changes only the allowed database
integration test, paired `000069` migration files, and this evidence record; it
does not stage, commit, push, change task status, or start Task 2.

## Task 1 Review-Finding Correction: Content Authorization, Grant Authority, PostgreSQL F6, And Down Snapshots

### Root Cause And Ownership

All accepted findings are Task 1 migration/integration-test ownership; none
belongs exclusively to Tasks 2--6.

- SQLite rebuilt `backup_asset_delivery_grants` in 000069 with nullable
  step-up action comparisons and length-only proof checks, weakening the
  inherited asset arm and admitting malformed RecoveryResult proofs. Its down
  rebuild did not restore the exact 000066/000068 asset predicate.
- PostgreSQL had the same SQL-CHECK-UNKNOWN hole for the RecoveryResult arm;
  the real-engine RED also found the inherited asset arms had lost explicit
  `IS NOT NULL` checks during the 000069 constraint replacement.
- The grant terminal trigger named only `UPDATE OF consumed_at, revoked_at`,
  so a consumed grant could still have its ID, plan/job binding, category,
  hash, actor/session, reason, expiry, timestamps, or binding digest rewritten.
- The rejected-down snapshot did not collect
  `trg_backup_asset_recovery_attempts_mutation_arm_monotonic` or the paired
  PostgreSQL function, so it could not prove that those objects survived a
  rejected down unchanged.
- The exact PostgreSQL F6 wrapper was absent, allowing the required selector to
  report `[no tests to run]` rather than exercise the fail-closed DSN gate.

### Genuine RED Evidence

The following tests were added before the paired SQL and snapshot-helper
GREEN changes. The original selector gap was first observed as:

```bash
cd /home/murray/code/xirang/backend
REQUIRE_POSTGRES_MIGRATION_TEST=1 TEST_POSTGRES_DSN='' \
  go test ./internal/database -run '^TestRecoveryReviewF6UseLatchPostgres$' -count=1
```

```text
ok  xirang/backend/internal/database  [no tests to run]
```

After adding the exact wrapper, the same required-mode command failed closed,
which is a gate-coverage correction rather than a product RED:

```text
--- FAIL: TestRecoveryReviewF6UseLatchPostgres
    TEST_POSTGRES_DSN is required when REQUIRE_POSTGRES_MIGRATION_TEST=1
```

Focused SQLite REDs were observed before production SQL/helper changes:

```bash
cd /home/murray/code/xirang/backend
GOTMPDIR=/dev/shm TMPDIR=/dev/shm go test ./internal/database \
  -run '^TestBackupAssetMigration069SQLite$/^ContentAuthorizationRequiresExactStepUp$' -count=1
```

```text
sqlite RecoveryResult Content authorization unexpectedly allowed:
RecoveryResult step-up action NULL, RecoveryResult non-hex proof id,
RecoveryResult uppercase proof id, existing backup-asset step-up action NULL,
existing backup-asset non-hex proof id, existing backup-asset uppercase proof id
```

```bash
GOTMPDIR=/dev/shm TMPDIR=/dev/shm go test ./internal/database \
  -run '^TestBackupAssetMigration069SQLite$/^GrantTerminalTransitions$' -count=1
```

```text
sqlite recovery grant terminal transitions unexpectedly allowed: consumed grant
id rewrite, consumed grant plan/job binding rewrite, consumed grant authority
category rewrite, consumed grant hash rewrite, consumed grant actor rewrite,
consumed grant session rewrite, consumed grant binding digest rewrite, consumed
grant reason rewrite, consumed grant expiry rewrite, consumed grant created
timestamp rewrite, consumed grant updated timestamp rewrite
```

```bash
GOTMPDIR=/dev/shm TMPDIR=/dev/shm go test ./internal/database \
  -run '^TestBackupAssetMigration069SQLite$/^PreservesExisting068ContentAndExportArms$' -count=1
GOTMPDIR=/dev/shm TMPDIR=/dev/shm go test ./internal/database \
  -run '^TestBackupAssetMigration069SQLite$/^RejectedDownSnapshotCoversMutationArm$' -count=1
```

```text
sqlite pristine 000069 down did not restore the exact pre-000069 Content definition
sqlite recovery down snapshot is missing mutation-arm trigger
"trg_backup_asset_recovery_attempts_mutation_arm_monotonic"
```

The first real PostgreSQL aggregate run also genuinely failed before the final
PostgreSQL asset-arm correction:

```text
postgres RecoveryResult Content authorization unexpectedly allowed: existing
backup-asset step-up action NULL
```

### Minimal GREEN

- SQLite now requires non-NULL exact actions and lowercase-hex proof IDs for
  both inherited asset proof products and the RecoveryResult product; its down
  rebuild restores the pre-000069 Content predicate.
- PostgreSQL now explicitly requires non-NULL action values in every
  proof-bearing asset and RecoveryResult arm; its existing lowercase-hex regex
  remains the proof validator.
- Both grant terminal triggers now fire on every UPDATE and reject any update
  once `OLD.consumed_at` or `OLD.revoked_at` is set. Active-to-consumed and
  active-to-revoked remain the legal one-use/revoke transitions.
- The rejected-down snapshot now records the mutation-arm trigger on both
  engines and `backup_asset_recovery_attempt_mutation_arm_monotonic()` on
  PostgreSQL; the focused test replaces both objects and proves the snapshot
  observes each replacement. Pristine down still proves their removal.
- `TestRecoveryReviewF6UseLatchPostgres` is now the exact required selector.

Final focused GREEN evidence:

```bash
cd /home/murray/code/xirang/backend
GOTMPDIR=/dev/shm TMPDIR=/dev/shm go test ./internal/database \
  -run '^TestBackupAssetMigration069SQLite$' -count=1
GOTMPDIR=/dev/shm TMPDIR=/dev/shm go test ./internal/database \
  -run '^TestBackupAssetMigration069PairedFiles$' -count=1
GOTMPDIR=/dev/shm TMPDIR=/dev/shm go test ./internal/database \
  -run '^(TestRecoveryReviewF6UseLatchSQLite|TestRecoveryReviewF6UsedDownAtomicRefusal)$' -count=1
GOTMPDIR=/dev/shm TMPDIR=/dev/shm go test ./internal/database -count=1
GOTMPDIR=/dev/shm TMPDIR=/dev/shm go test ./internal/model ./internal/backupasset/recovery \
  -run 'BackupAssetRecovery|State|Contract' -count=1
GOTMPDIR=/dev/shm TMPDIR=/dev/shm go vet \
  ./internal/database ./internal/model ./internal/backupasset/recovery
```

```text
ok  xirang/backend/internal/database                 1.953s
ok  xirang/backend/internal/database                 0.007s
ok  xirang/backend/internal/database                 0.678s
ok  xirang/backend/internal/database                 7.522s
ok  xirang/backend/internal/model                    0.029s
ok  xirang/backend/internal/backupasset/recovery     0.003s
```

`go vet` and `gofmt -d` produced no output.

### Required PostgreSQL 18 Disposition And Cleanup

Docker bridge networking could not create a veth pair in this environment, so
the failed disposable record was removed and a fresh PostgreSQL 18 container
`codex-child13-task1-correction-pg18` ran with host networking, PostgreSQL
bound only to verified-unused `127.0.0.1:55476`. Against its real DSN, the final
required selectors passed:

```bash
cd /home/murray/code/xirang/backend
REQUIRE_POSTGRES_MIGRATION_TEST=1 TEST_POSTGRES_DSN="$TASK1_PG_DSN" \
  go test ./internal/database -run '^TestBackupAssetMigration069Postgres$' -count=1
REQUIRE_POSTGRES_MIGRATION_TEST=1 TEST_POSTGRES_DSN="$TASK1_PG_DSN" \
  go test ./internal/database -run '^TestRecoveryReviewF6UseLatchPostgres$' -count=1
```

```text
ok  xirang/backend/internal/database  17.343s
ok  xirang/backend/internal/database  0.641s
```

Cleanup was observed:

```bash
docker rm -f codex-child13-task1-correction-pg18
docker ps -a --filter name=^/codex-child13-task1-correction-pg18$ --format '{{.Names}}'
ss -H -ltn 'sport = :55476'
```

The removal printed the container name; both absence checks produced no output.

### Scope And Chronology

This shared checkout already contained approved Child 13 changes. This
correction wrote only the allowed database integration test, SQLite 000069 up
and down scripts, PostgreSQL 000069 up script, and this evidence ledger; the
paired PostgreSQL down script was inspected and verified without a correction
edit. `git diff --check` was clean; the index stayed empty; exactly four paired
000069 files exist; no 000070+ migration exists; both Child and parent
`task.py validate` commands passed; and no correction container/listener
remained. No staging, commit, push, task-status change, or Task 2 work occurred.

The original Task 1 model/state/contract RED chronology remains
`not_observed_before_green`. It is not retroactively claimed as TDD. Only the
focused correction REDs recorded in this section were genuinely observed before
their corresponding GREEN changes.

## Task 1 Final Independent Review Closure

On 2026-07-29 the final independent specification re-review returned
`APPROVED` with no open Critical/Important finding. A separate fresh
live-worktree quality reviewer also returned `APPROVED` and found no code-quality
issue. The quality re-review confirmed that an earlier armed-attempt finding was
stale against the current worktree: both engines allow armed attempts to close
as `completed`, `failed`, `canceled`, or `lost`; paired guards keep the mutation
arm monotonic, freeze terminal attempt identity/owner/fence/state, reject
terminal resurrection/delete/replacement, and require takeover to insert a
fresh claimed attempt.

The focused closure verification passed the paired terminal/takeover selector,
the complete SQLite 000069 selector, the complete real PostgreSQL 18.4 000069
selector, paired-file checks, the database/model/recovery packages, and `go
vet`. The disposable PostgreSQL service and temporary caches were removed.
`gofmt`, `git diff --check`, the exact four-file 000069 reservation, absence of
000070+, and staged-zero checks remained clean. The final technical verifier and
quality reviewer made no repository edits.

Task 1 is therefore `complete_approved`. Its original model/state/contracts RED
chronology remains the accepted `not_observed_before_green` historical deviation;
no retroactive RED is claimed. Tasks 2--10 and every new correction still require
genuine RED before GREEN. At this Task 1 closure checkpoint, Task 2 was authorized
but remained `not_executed`; the later Task 2 record follows below. This Task 1
closure does not claim Child/full-gate or delivery completion.

## Task 2 Exact Selection, Source Revision And Plan Idempotency Closure

### Scope And Review Disposition

Task 2 changed only the approved recovery contracts/service/tests and
publication contracts/tests. Batch A closed mutable source substitution,
parent-entry directory traversal, overlapping provenance, the exact raw-domain
locator digest formula, and the idempotency/replay matrix. Finding 8 was a
coverage gap: the new matrix was immediate GREEN against existing behavior, so
no fake RED or production change was invented.

Batch B closed the three remaining findings one at a time:

- caller-owned `CreatePlanTx` now uses a scoped savepoint, rolls back only its
  own partial plan/item write set, and never commits or rolls back the caller's
  transaction;
- ordinary source validation commits before Provider consumer I/O, while the
  caller-Tx API returns only a materialized typed handoff and locks the exact
  Repository/RecoveryPoint/generation/manifest/selected-entry tuple until the
  caller transition commits; and
- Provider consumer failures map to a closed source-unavailable product without
  locator/digest leakage, while cancellation and deadline errors retain their
  exact `errors.Is` semantics.

The controller-inline specification pass checked Task 2 against PRD §1,
design §§4.1/4.3/5/6 and implement Task 2. The separate controller-inline
quality pass checked transaction ownership, lock order, error closure, tests,
lint and static hygiene. Both returned `APPROVED` with no open Task 2 finding.
These inline passes are not relabeled as independent sub-agent reviews.

### Genuine RED Evidence

The final Batch B REDs were observed before their production changes:

```text
TestPlanCreateTxRollsBackItsPartialWriteSetBeforeCallerCommit:
  *model.BackupAssetRecoveryPlan rows = 1, want 0 after rollback

TestSourceValidatorCallsConsumerAfterValidationTransactionCommits:
  database table is locked: backup_repositories

caller-Tx materialized-handoff API:
  assignment mismatch: RevalidateTx returns 1 value
  not enough arguments: existing API required a Provider consumer

TestSourceValidatorSanitizesConsumerErrorsAndPreservesContextSemantics:
  generic Provider failure returned raw locator+digest text
  wrapped cancellation leaked the locator
  wrapped deadline leaked the locator digest
```

Batch A Findings 1, 2, 3 and 6 retain their previously recorded genuine RED
chronology. Finding 8 remains explicitly immediate GREEN.

### Final GREEN And Static Evidence

Fresh final commands after the last semantic change all exited zero:

```bash
cd /home/murray/code/xirang/backend
go test ./internal/backupasset/recovery ./internal/backupasset/publication \
  -run 'Selection|SourceRevision|Plan|Idempotency|Locator|Directory|Duplicate' -count=1
go test ./internal/backupasset/recovery \
  -run '^(TestPlanCreateTxRollsBackItsPartialWriteSetBeforeCallerCommit|TestSourceValidatorCallsConsumerAfterValidationTransactionCommits|TestSourceValidatorRevalidateTxReturnsLockedMaterializedHandoff|TestSourceValidatorSanitizesConsumerErrorsAndPreservesContextSemantics)$' -count=10
go test -race ./internal/backupasset/recovery \
  -run 'Plan.*Concurrent|Idempotency' -count=10
go test -race ./internal/backupasset/recovery \
  -run '^(TestPlanCreateTxRollsBackItsPartialWriteSetBeforeCallerCommit|TestSourceValidatorCallsConsumerAfterValidationTransactionCommits|TestSourceValidatorRevalidateTxReturnsLockedMaterializedHandoff|TestSourceValidatorSanitizesConsumerErrorsAndPreservesContextSemantics)$' -count=10
go test ./internal/backupasset/recovery ./internal/backupasset/publication -count=1
go vet ./internal/backupasset/recovery ./internal/backupasset/publication
golangci-lint run ./internal/backupasset/recovery ./internal/backupasset/publication
```

Observed results were recovery/publication focused `1.791s/0.006s`, new Batch B
matrix `1.256s`, prescribed race `22.250s`, new Batch B race `3.181s`, full
packages `1.891s/0.007s`, `go vet` exit 0, and golangci-lint `0 issues`.
The quality pass removed one dead `frontier = nil` assignment reported by
`ineffassign`; no behavior changed.

Both Child and parent `task.py validate` commands, six-file gofmt, full-worktree
`git diff --check`, cached diff check, final-newline/trailing-whitespace checks,
and staged-empty assertion passed. No stage, commit, push, PR, CI, merge, task
status change, or migration-number change occurred. Task 2 is
`complete_approved`; Tasks 3--10, Child completion, full gates and delivery
remain pending.

## Task 3 Target Preflight, SSH Purposes And Node-Write Coordination

Task 3 implementation, its reviewed node-write remediations, and its
deterministic deadline-seam correction are green, but the task remains
`implementation_done_independent_specification_and_quality_rereviews_pending`.
It does not start Task 4 or claim Child/full-gate completion.

### Task 3A/3B Chronology Correction And Current Coverage

The original Task 3A child session was stopped after applying its first test
patch and before running that test. Its final handoff explicitly said no RED had
run. The later Task 3A/3B implementation has no preserved executed pre-GREEN
failure output. Its original chronology is therefore
`not_observed_before_green`, not a passed TDD gate and not retroactively claimed
as RED-to-GREEN.

The resulting regression coverage and production contracts deliver closed
operation/delete products, security-finding binding, eligibility policy A,
read-only target preflight, the closed `TargetPort`, and five independent
Recovery SSH purposes. Tests derive expiry values from the test clock rather
than fixed calendar fixtures. Task 3C retains separate genuine RED-to-GREEN
evidence below.

### First Independent Task 3 Specification Review And Remediation

The first independent specification review returned:

- Critical: a blocked finding product could be rewritten and rehashed as
  `allow_clean` with count zero because validation lacked a trusted disposition;
- Important: generic observation/mutation permits were reusable across
  purpose-distinct TargetPort methods;
- Important: the Task 3A/3B RED chronology was not evidenced; and
- Important: the real PostgreSQL concurrency proof existed only in a removed
  overlay rather than a permanent required-mode test.

The security rewrite regression produced a genuine RED before its production
fix:

```text
TestPreflightSecurityDecisionRejectsBlockedFindingsRewrittenAsRehashedClean:
  ValidateBinding(rehashed blocked-to-clean rewrite) error = <nil>,
  want ErrInvalidSecurityDecision
```

The purpose-exact interface regression also produced a genuine RED before its
production fix:

```text
TestTargetPortOperationPermitsArePurposeExact:
  TargetPort.OpenOwnedResult permit = TargetObservationPermit,
  want purpose-exact TargetResultReadPermit
```

Minimal GREEN uses the trusted Target probe's closed `clean|blocked` finding
disposition to reject a caller-rehashed contradictory decision, and gives every
TargetPort method a purpose-specific, constructor-validated permit type. The
generic raw permit products no longer satisfy the interface methods.

`backend/internal/backupasset/recovery/behavior_integration_test.go` permanently
defines `TestRecoveryBehaviorPostgres`. Its first real PostgreSQL 18.4 execution
was immediate GREEN, so it is recorded as a coverage-gap closure with no fake
RED and no production change. Required mode without a DSN fails closed with:

```text
TEST_POSTGRES_DSN is required when REQUIRE_POSTGRES_RECOVERY_TEST=1
```

The permanent test creates and drops an isolated schema and covers active
Recovery lease denial with zero TaskRun residue, the `pending|running` versus
terminal TaskRun matrix, and ten same-node Task/Recovery races with exactly one
durable winner.

Fresh controller verification after both corrections passed:

```bash
cd /home/murray/code/xirang/backend
go test ./internal/backupasset/recovery \
  -run '^(TestPreflightSecurityDecisionRejectsBlockedFindingsRewrittenAsRehashedClean|TestTargetPreflightRejectsBlockedFindingsRewrittenAsRehashedClean|TestTargetPortOperationPermitsArePurposeExact)$' \
  -count=1
go test -race ./internal/backupasset/recovery \
  -run '^(TestPreflightSecurityDecisionRejectsBlockedFindingsRewrittenAsRehashedClean|TestTargetPreflightRejectsBlockedFindingsRewrittenAsRehashedClean|TestTargetPortOperationPermitsArePurposeExact)$' \
  -count=10
REQUIRE_POSTGRES_RECOVERY_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" \
  go test ./internal/backupasset/recovery \
  -run '^TestRecoveryBehaviorPostgres$' -count=10
REQUIRE_POSTGRES_RECOVERY_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" \
  go test -race ./internal/backupasset/recovery \
  -run '^TestRecoveryBehaviorPostgres$' -count=1
go test ./internal/sshutil ./internal/backupasset/recovery ./internal/task \
  ./internal/backupasset/runtime ./cmd/server -count=1
go vet ./internal/sshutil ./internal/backupasset/recovery ./internal/task \
  ./internal/backupasset/runtime ./cmd/server
golangci-lint run ./internal/backupasset/recovery
```

Observed results were focused normal `0.053s`, focused race x10 `1.571s`, real
PostgreSQL count10 `1.411s`, real PostgreSQL race `1.758s`, five-package normal
`1.714s/1.295s/2.754s/6.165s/0.063s`, `go vet` exit 0, and golangci-lint
`0 issues`. All isolated PostgreSQL schemas were removed.

### Task 3C Genuine RED

Before any Task 3C production edit, the following command failed on the exact
missing durable boundary:

```bash
cd /home/murray/code/xirang/backend
go test ./internal/task ./internal/backupasset/runtime ./cmd/server \
  -run '^(TestTriggerManualNodeWriteConflictLeavesNoReservationOrMarker|TestTriggerRestoreNodeWriteConflictLeavesNoRunMarkerPrecheckOrExecutor|TestManagedLegacyRestoreBlockCleansReservationMarkers|TestNodeWriteCoordinator.*|TestMainWiresSharedBackupAssetRuntimeBeforeSchedules)$' \
  -count=1
```

The observed failures were undefined `task.ErrNodeWriteConflict`,
`task.NodeWriteAdmission`, `Manager.SetNodeWriteAdmission`,
`runtime.NewNodeWriteCoordinator`/`NodeWriteCoordinator`, and the missing main
source-order coordinator construction. Production code had not been edited.

### Task 3C GREEN And Cross-Engine Evidence

The minimal implementation reserves ordinary and legacy-restore `TaskRun`
rows in the same caller-owned transaction as node admission, locks the shared
PostgreSQL `nodes` row with `FOR UPDATE`, uses a SQLite no-op node write before
conflict queries, maps bounded busy/lock exhaustion to closed products, and
installs the coordinator before schedules load. Admission failures leave no
TaskRun, `pendingRuns`, `restoreNodes`, precheck or executor residue. Recovery
admission blocks `pending|running` TaskRuns while terminal rows remain inert;
same-node Task/Recovery contenders elect one durable winner.

Fresh final local commands after replacing one forbidden fixed-calendar lease
fixture with test-start-relative UTC time all exited zero:

```bash
cd /home/murray/code/xirang/backend
go test ./internal/task ./internal/backupasset/runtime ./cmd/server \
  -run '^(TestTriggerManualNodeWriteConflictLeavesNoReservationOrMarker|TestTriggerManualRetriesRawSQLiteBusyAroundWholeReservationTransaction|TestTriggerRestoreNodeWriteConflictLeavesNoRunMarkerPrecheckOrExecutor|TestManagedLegacyRestoreBlockCleansReservationMarkers|TestNodeWriteCoordinator.*|TestMainWiresSharedBackupAssetRuntimeBeforeSchedules)$' \
  -count=1
go test -race ./internal/task ./internal/backupasset/runtime ./cmd/server \
  -run '^(TestTriggerManualNodeWriteConflictLeavesNoReservationOrMarker|TestTriggerManualRetriesRawSQLiteBusyAroundWholeReservationTransaction|TestTriggerRestoreNodeWriteConflictLeavesNoRunMarkerPrecheckOrExecutor|TestManagedLegacyRestoreBlockCleansReservationMarkers|TestNodeWriteCoordinator.*|TestMainWiresSharedBackupAssetRuntimeBeforeSchedules)$' \
  -count=10
go test ./internal/sshutil ./internal/backupasset/recovery ./internal/task \
  ./internal/backupasset/runtime ./cmd/server -count=1
go test -race ./internal/sshutil ./internal/backupasset/recovery ./internal/task \
  ./internal/backupasset/runtime ./cmd/server -count=1
go vet ./internal/task ./internal/backupasset/runtime ./cmd/server
golangci-lint run ./...
```

Observed package results were focused normal `0.080s/0.116s/0.066s`, focused
race x10 `2.174s/3.088s/1.605s`, Task 3 aggregate normal
`2.197s/2.415s/3.284s/6.563s/0.079s`, aggregate race
`3.722s/5.751s/6.218s/11.618s/1.899s`, `go vet` exit 0 and golangci-lint
`0 issues`.

A real PostgreSQL 18.4 host-network service was first exercised through an
isolated temporary database and a non-worktree Go overlay. The active-lease
matrix, `pending|running` versus terminal TaskRun matrix, and simultaneous
same-node Task/Recovery transaction race passed ten repetitions in `2.233s`,
proving the row-lock winner/loser behavior rather than only dry-run SQL
generation. The temporary database and overlay files were removed. The later
specification-review correction above now persists this matrix in the required
`TestRecoveryBehaviorPostgres` gate and re-proves it against isolated schemas.

The seven Task 3C paths are all in the approved 71-modify manifest, and the
seven review-remediation paths are approved create paths. Fresh manifest
classification found `9 + 55 + 71 = 135` unique paths, 43 current dirty paths,
zero out-of-manifest path, zero overlap/duplicate, and zero staged path. All
55 create paths remain absent at HEAD and all 71 modify paths tracked at HEAD.
`gofmt -d`, `git diff --check`, final-newline/trailing-whitespace checks, task
validation and staged-empty checks passed. No migration, model, Task 4, stage,
commit, push, PR, CI or merge action occurred. The first Task 3 specification
review is `changes_required_remediated_rereview_pending`; independent quality
review remains `pending_not_executed`.

### Second Independent Task 3 Specification Review

The independent re-review did not approve Task 3. It returned two further
Important findings in the durable same-node writer boundary:

- `TaskRun` cancellation can commit `canceled` after the runner's one-time
  check, while the runner later unconditionally writes `running`. Recovery may
  acquire the node lease during that terminal-state window before the runner
  resurrects the task and enters its executor.
- Recovery derives the node of an active Task writer through mutable
  `tasks.node_id`. Node migration can update that value after signaling but
  before the old remote writer terminates, making the live writer disappear
  from the old node's conflict query.

The reviewer required a shared-boundary `pending` CAS before executor entry and
an immutable per-writing-run node binding, with deterministic SQLite and real
PostgreSQL cancel/start/migration/Recovery races. The reviewer's focused normal,
race, real PostgreSQL, `go vet`, golangci-lint and `git diff --check` commands
all passed; those green commands did not cover the two newly identified
interleavings. The dedicated strict TDD remediation and its later deterministic
deadline-seam correction are recorded below with observed output. Task 4 remains
unstarted; independent specification rereview and independent quality rereview
remain pending/not executed.

### Focused Task 3 Node-Snapshot Manifest Amendment

Controller root-cause tracing rejected `credential_audit_events` as an
authority source: it is append-only audit evidence, not a unique fenced writer
reservation, and may be retained or deleted independently of TaskRun lifecycle.
The Recovery node-lease table is likewise unavailable because its closed owner
product permits only Recovery job/cleanup holders.

The focused remediation therefore keeps migration ownership in the unshipped
paired `000069` and adds one tracked path,
`backend/internal/model/task.go`, to the modify manifest. At that Task 3 review,
the exact scope was
9 Phase-1 + 55 create + 72 modify = 136 unique paths; `000070+` remains reserved
and no thirteenth Recovery table is introduced. Paired 000069 will backfill and
guard an immutable `task_runs.node_id_snapshot`; Task reservation freezes it,
the shared node-boundary transaction owns the exact pending-to-running CAS, and
Recovery admission queries the snapshot rather than mutable `tasks.node_id`.
No product GREEN or TDD RED is claimed by this planning amendment.

### Task 3 Canceled-Run Resurrection RED

Before any Task 3 remediation production edit, deterministic barriers fixed the
two runner windows without timing sleeps. The ordinary runner completed its
one-time `TaskRun` status read and then waited on the already-held strategy
lock; the legacy restore runner waited immediately before its unconditional
running update. Cancellation committed before either barrier was released.

```bash
cd /home/murray/code/xirang/backend
go test ./internal/task \
  -run '^(TestRunTaskCanceledAfterInitialCheckCannotResurrectOrEnterExecutor|TestRunRestoreTaskCanceledBeforeStartCannotEnterPrecheckOrExecutor)$' \
  -count=1
```

The command failed for the intended behavior, not setup or environment noise:

```text
TestRunTaskCanceledAfterInitialCheckCannotResurrectOrEnterExecutor:
  canceled pending TaskRun entered executor 1 time(s)
TestRunRestoreTaskCanceledBeforeStartCannotEnterPrecheckOrExecutor:
  canceled restore entered remote precheck 1 time(s)
```

No production file had been edited for this remediation when the RED was
observed.

### Task 3 Immutable Writer-Node RED

Before adding the TaskRun snapshot field or changing admission queries, the
same behavior test ran on SQLite and an isolated-schema PostgreSQL 18.4 fixture.
Each created a running TaskRun on one node, migrated `tasks.node_id` to another
node, and then attempted Recovery admission on the original node.

```bash
cd /home/murray/code/xirang/backend
go test ./internal/backupasset/runtime \
  -run '^TestNodeWriteCoordinatorRecoveryAdmissionUsesImmutableRunNodeAfterTaskMigration$' \
  -count=1
REQUIRE_POSTGRES_RECOVERY_TEST=1 \
TEST_POSTGRES_DSN='postgres://xirang:xirang_test@127.0.0.1:55470/xirang_test?sslmode=disable' \
go test ./internal/backupasset/recovery \
  -run '^TestRecoveryBehaviorPostgres/TaskRunNodeSnapshotSurvivesTaskMigration$' \
  -count=1
```

Both commands failed at the intended boundary:

```text
old-node recovery admission error=<nil>, want live-writer conflict
```

The PostgreSQL test used its existing isolated schema and cleanup. No migration,
model or admission production file had been edited when either RED was
observed.

### Task 3 Paired 000069 TaskRun-Snapshot RED

The paired migration regression starts at 000068 with a running TaskRun,
applies 000069, and requires cross-engine backfill, insert matching,
post-insert immutability, Task migration independence, and pristine down that
preserves the original TaskRun rows. Before either up/down migration or the
TaskRun model was changed, both focused selectors failed on the missing column:

```bash
cd /home/murray/code/xirang/backend
go test ./internal/database \
  -run '^TestBackupAssetMigration069TaskRunNodeSnapshotSQLite$' -count=1
REQUIRE_POSTGRES_MIGRATION_TEST=1 \
TEST_POSTGRES_DSN='postgres://xirang:xirang_test@127.0.0.1:55470/xirang_test?sslmode=disable' \
go test ./internal/database \
  -run '^TestBackupAssetMigration069TaskRunNodeSnapshotPostgres$' -count=1
```

Observed expected failures:

```text
sqlite 000069 omitted task_runs.node_id_snapshot
postgres 000069 omitted task_runs.node_id_snapshot
```

The PostgreSQL fixture removed its isolated schema. These failures complete the
RED evidence for both reviewed findings; production remediation had not begun.

### Task 3 Atomic No-Executor Compensation RED

Before the replacement compensation implementation, the focused atomic test
suite ran against the durable-entry path with isolated Go caches:

```bash
cd backend && \
env GOCACHE=/home/murray/.cache/codex-c13-task3-cancel/gocache \
  TMPDIR=/home/murray/.cache/codex-c13-task3-cancel/tmp \
  GOTMPDIR=/home/murray/.cache/codex-c13-task3-cancel/tmp \
  go test ./internal/task \
    -run '^(TestCancelAfterDurableTaskEntryPreservesPriorTerminalOutcomeWithoutExecutor|TestNoExecutorCompensationAfterDurableEntryRestoresPendingAndRetryingTasks|TestLegacyRestoreNoExecutorCompensationAfterDurableEntry)$' \
    -count=1
```

All 13 cases failed only because `started_at` remained non-nil. Executor and
precheck calls were zero. The intended matrix covered terminal
`success|warning|failed|canceled|skipped`, pending and retrying shutdown/deadline
paths, and legacy restore shutdown/deadline paths. No production edit had been
made when this RED was observed.

### Task 3 Atomic No-Executor Compensation GREEN

The replacement implementation made the compensation atomic: prior terminal
outcomes remain terminal, and only pending/retrying work can be restored after a
durable entry without entering an executor or precheck. The focused, full, race,
and PostgreSQL gates for this remediation passed before the later harness review.

### PostgreSQL Deadline-Harness Review And Amendment

The subsequent independent review found that the PostgreSQL `/deadline` harness
used a one-second wall-clock before `commitEntered`. A first test-only child
confirmed there was no public injection seam and made no edits. The controller
then approved the smallest in-manifest amendment, limited to the existing
`backend/internal/task/manager.go`, `backend/internal/task/runner.go`, and
`backend/internal/backupasset/recovery/behavior_integration_test.go` paths. The
exact manifest at that observation remained 9 current + 55 create + 72 modify =
136 unique paths.

### Deterministic Deadline-Seam RED

The external PostgreSQL behavior harness was written first:

```bash
go test ./internal/backupasset/recovery \
  -run '^TestRecoveryBehaviorPostgres$' -count=1
```

It failed at compile time only because `task.WithRunContextFactory` and the
corresponding constructor option did not exist. This was the intended RED; no
runtime timing failure, production workaround, or global/setter seam was used.

### Deterministic Deadline-Seam GREEN

- `WithRunContextFactory` is a constructor-time `ManagerOption`, defaults to
  `context.WithTimeout`, and is shared by ordinary `triggerCore` and legacy
  `TriggerRestore`; there is no setter or global override.
- The PostgreSQL harness closes `Done` and returns `context.DeadlineExceeded`
  only after `commitEntered`. Watchdogs are diagnostic rather than correctness
  clocks, and `Cancel` or `Shutdown` is not used as a deadline proxy.
- The focused PostgreSQL behavior normal and race suites passed against real
  PostgreSQL 18 at `127.0.0.1:55470`, including 10 normal and 10 race runs. Any
  `TEST_POSTGRES_DSN` use is represented only by its environment placeholder;
  no credential is recorded in this ledger.

### Task 3 Remediation Verification And Environment Note

Atomic compensation normal and race suites each passed with `-count=10`.
`go test -race ./internal/task ./internal/backupasset/recovery -count=1` passed.
Runtime node-write selectors passed in normal and race modes with `-count=10`.
`go vet ./internal/task ./internal/backupasset/recovery ./internal/backupasset/runtime`
passed, and `golangci-lint run ./internal/task/... ./internal/backupasset/recovery/...`
returned zero issues. `gofmt`, `git diff --check`, and the staged-zero assertion
also passed.

The broad runtime cache test remains locally environment-blocked: `/home`
reports `IFree=0`, causing `cache_root_unverified`, while the `/tmp` linker path
hits quota. Runtime was not edited, and this is not recorded as a product test
failure.

### Task 3 Fresh Quality Disposition (2026-07-30)

At the time of this fresh quality disposition, Task 3 was
`implementation_done_quality_remediation_required`. The earlier
independent specification and live-worktree quality `APPROVED` outcomes are
superseded/incomplete evidence, not final Task 3 approval, because a fresh
bounded quality verifier found the following live-source defects:

- **Critical - unbound recovery target authority/path safety:**
  `recovery/contracts.go` accepts absolute and `..` locators;
  `TargetBinding.Validate` neither validates `EncryptedRelativePath` nor
  recomputes `PathDigest`; the preflight snapshot omits root/path identity; and
  `TargetMutationPermit` carries no object binding. A preflight for target A can
  therefore authorize target B or a path outside the safe root. Required
  remediation is strict normalized-relative validation, a server-side digest,
  and root/path-digest binding through permits, snapshot, preflight, and frozen
  job state.
- **Important - cancellation before owner registration:** the ordinary runner
  observes pending before owner registration, so `Cancel` can persist `canceled`
  and `enterTaskExecution` can subsequently permit `canceled -> pending ->
  running`; `TriggerRestore` has the analogous window. Required remediation is
  early ownership or per-task serialization plus deterministic barrier tests.
- **Important - expired active Recovery lease is never reclaimed:**
  `admission.go` treats every active Recovery lease as live without checking
  `lease_expires_at`; migrations permit an expired active unique row, and no
  production reclaim exists, so a crash can block a node permanently. Required
  remediation is atomic expired-state transition under the node boundary plus
  fenced renew/reclaim tests.

The verifier confirmed the former post-reservation trigger-to-goroutine cancel
ownership gap, paired SQLite/PostgreSQL `000069` fail-closed immutable
`TaskRun.node_id_snapshot` parity and Recovery direct snapshot use, and the
permanent required real-PostgreSQL `000069` migration plus Recovery
cancel/start/race CI gate are closed. Focused checks passed with staged paths at
zero and no verifier mutation. The Task 3A/3B `not_observed_before_green`
chronology, the immediate-GREEN PostgreSQL coverage-gap qualification, and the
locally environment-blocked broad runtime-cache test above remain unchanged.

### Task 3 Final Quality-Remediation Closure (2026-07-30)

The final legacy restore early-cancellation review identified one more real
terminal-state overwrite. Before the fix, all three `runRestoreTaskWithContext`
early cancellation exits updated `TaskRun` by id alone. A public-trigger
regression held the restore runner at its semaphore, committed `Cancel`, then
released the runner. It observed the committed `canceled` / `任务已取消` row being
overwritten with `恢复任务已取消` and a later `finished_at`. The production change
was limited to `backend/internal/task/runner.go`: each of the three exits now
uses `WHERE id = ? AND status = 'pending'`. The regression in
`backend/internal/task/manager_test.go` proves the already committed terminal
row, including its timestamp and error, remains unchanged and that no precheck
or executor runs.

The observed RED was followed by exit-0 GREEN runs of the exact regression,
the five-test cancellation/restore matrix at `-count=10`, and the same matrix
under `-race -count=10`. A fresh full `internal/task` normal and race run, full
`internal/backupasset/recovery`, focused normal and race
`TestNodeWriteCoordinator`, full model/SSH/publication packages, SQLite 000069
selectors, real PostgreSQL `TestRecoveryBehaviorPostgres`, and real PostgreSQL
000069 TaskRun-snapshot migration selectors all exited zero. `go vet` across
the affected packages and `golangci-lint` across task/recovery/runtime/model/
database/SSH/server packages reported no issue. `gofmt`, `git diff --check`,
task and parent validation, migration reservation scans, staged-zero, generated
binary absence, and current-dirty-union subset checks also passed.

An independent read-only Task 3 specification rereview returned `APPROVED`.
The controller-inline quality recheck found no remaining Task 3 finding. The
dirty union at that Task 3 review was 45 paths, all within the then-approved
136-path manifest;
`000069` remains exactly four paired files and `000070+` remains absent. The
full runtime package still fails only the known host `IFree=0` cache-root
environment condition (`cache_root_unverified`); it is not a product pass or a
Task 3 quality failure. Task 3 is therefore `complete_approved` at task scope.
At that Task 3 closure, Task 4 was authorized but remained `not_started`;
Child/full gates, staging, commit, push, PR, CI, merge, and archive remained
unexecuted.

### Task 4 Batch B1: Rsync Typed Restore Adapter (2026-07-30)

The inherited focused Rsync test initially imported
`github.com/stretchr/testify/require`, which is not a direct dependency of
`backend/go.mod`; the untouched command therefore stopped with
`go: updates to go.mod needed; to update it: go mod tidy`. The same test also
referred to pre-Batch-A RestoreSource, RestoreTarget, RestoreRequest, and
checkpoint fields. The test was corrected only to the existing closed Batch-A
types and standard-library assertions; neither `go.mod` nor `go.sum` changed.

With `rsync_restore.go` still absent, the corrected focused command observed
the required source-absent RED:

```bash
cd backend
TMPDIR=/tmp \
GOCACHE=/home/murray/.cache/c13-task4-go \
GOMODCACHE=/home/murray/.cache/c13-task4-mod \
go test ./internal/backupasset/provider \
  -run '^(TestRsyncRestoreExecuteUsesManagedLocalSourceToBoundRemoteTarget|TestRsyncRestoreExecuteRejectsStaleTargetMutationPermitBeforeRunner)$' \
  -count=1
```

It failed at compile time with undefined `NewRsyncRestorePort`,
`RsyncRestoreExecuteCall`, `RsyncRestorePreflightCall`,
`RsyncRestoreRunnerEvidence`, `RsyncRestoreRunnerResult`,
`RsyncRestoreVerifyCall`, and `RsyncRestoreReconcileCall` symbols. No adapter
source existed during that observation.

The minimal GREEN implementation creates the provider-local typed Rsync port.
It derives an absolute, normalized managed-local source only after the existing
source binding validates; passes a copied frozen intent and opaque bound remote
target/fence/permit to its typed runner; performs the final mutation-permit and
fence validation immediately before `Execute`; and maps only sanitized
`RestoreEvidence` plus typed `RestoreCheckpoint` facts back to the four port
phases. It imports none of Recovery, Runtime, Repository, Task, Gin, or
frontend packages.

After `gofmt`, the same focused selector exited zero:

```text
ok  xirang/backend/internal/backupasset/provider  0.005s
```

The prescribed subsequent verification also exited zero:

```bash
TMPDIR=/tmp GOCACHE=/home/murray/.cache/c13-task4-go \
GOMODCACHE=/home/murray/.cache/c13-task4-mod \
go test ./internal/backupasset/provider \
  -run 'Restore|Rsync.*Recovery|Restic.*Recovery|Rclone.*Recovery' -count=1
# ok  xirang/backend/internal/backupasset/provider  0.008s

TMPDIR=/tmp GOCACHE=/home/murray/.cache/c13-task4-go \
GOMODCACHE=/home/murray/.cache/c13-task4-mod \
go test -race ./internal/backupasset/provider -run 'Restore' -count=10
# ok  xirang/backend/internal/backupasset/provider  1.059s

TMPDIR=/tmp GOCACHE=/home/murray/.cache/c13-task4-go \
GOMODCACHE=/home/murray/.cache/c13-task4-mod \
go test ./internal/backupasset/provider -count=1
# ok  xirang/backend/internal/backupasset/provider  0.951s
```

`gofmt`, `git diff --check`, and the staged-zero assertion also passed after
this B1 slice. No staging, commit, push, task-status, archive, or journal
action occurred.

### Task 4 Batch B1 Security Remediation: Closed Rsync Source Capability And Runner Facts (2026-07-30)

This focused remediation started by inspecting the inherited
`rsync_restore_test.go` change and running it against the then-current
production code. No prior writer action is used as TDD evidence here. After
adding only regression tests for direct `RestoreSource` construction, a
symlinked local locator, and the Recovery raw-Rsync handoff, the observed
behavioral RED was:

```bash
cd backend
go test ./internal/backupasset/provider ./internal/backupasset/recovery \
  -run '^(TestRestoreRequestRejectsDirectSourceConstructionBeforePortCall|TestRsyncRestorePreflightRejectsZeroObservationFactsBeforeRunner|TestRsyncRestorePreflightRejectsMismatchedRunnerObservationFacts|TestRsyncRestoreVerifyRejectsTypedCheckpointMismatch|TestRsyncRestoreReconcileRejectsTypedCheckpointMismatch|TestRsyncManagedLocalSourceRejectsArbitraryAbsoluteLocator|TestRsyncManagedLocalSourceRejectsSymlinkedLocator|TestSourceValidatorRefusesRawRsyncHandoffBeforeProviderConsumer)$' \
  -count=1
```

It exited nonzero. The provider failures were the expected missing denials:
direct source construction returned nil; zero and mismatched Preflight facts
returned nil; Verify/Reconcile accepted a drifted checkpoint; `/etc` and a
symlinked source reached `newRsyncManagedLocalSource`. Recovery also reported
that `Revalidate()` handed an Rsync locator directly to its provider consumer.

The minimal GREEN adds a sealed `RestoreSource` capability, makes the legacy
raw `NewRestoreSource` constructor fail closed, and reserves Rsync issuance for
the Recovery `RevalidateRsyncRestoreSource` path. That path revalidates the
exact database source binding, performs realpath/Lstat containment checks from
a typed managed root, rejects symlinks/outside roots/world-writable components,
and binds owner/device/inode/mode identity facts before the Provider issuer sees
the locator. The generic raw `Revalidate` and `RevalidateTx` handoffs now reject
Rsync. Rsync Preflight maps validated runner binding/revision/checkpoint facts
instead of fabricating them, and all read-only phases require exact current
purpose, fence, target binding/revision, and typed checkpoint facts.

The same focused selector plus the new capability test then exited zero:

```text
ok  xirang/backend/internal/backupasset/provider  0.005s
ok  xirang/backend/internal/backupasset/recovery  0.107s
```

The subsequent read-only positive dispatch/drift matrix was an immediate-GREEN
coverage addition after the observed core RED→GREEN; it is not claimed as a
retroactive RED. It proves Preflight/Verify/Reconcile forward their
purpose-specific permits and exact fence/checkpoint, and reject drifted binding,
revision, and fence facts.

Fresh focused and package verification all exited zero:

```bash
cd backend
go test ./internal/backupasset/provider \
  -run 'Restore|Rsync.*Recovery|Restic.*Recovery|Rclone.*Recovery' -count=1
# ok  xirang/backend/internal/backupasset/provider  0.009s

go test -race ./internal/backupasset/provider -run 'Restore' -count=10
# ok  xirang/backend/internal/backupasset/provider  1.073s

go test ./internal/backupasset/recovery \
  -run '^(TestSourceValidator|TestSourceLocatorDigest|TestExactSelection)' -count=1
# ok  xirang/backend/internal/backupasset/recovery  0.416s

go test -race ./internal/backupasset/recovery -run '^TestSourceValidator' -count=10
# ok  xirang/backend/internal/backupasset/recovery  12.335s

go test ./internal/backupasset/provider ./internal/backupasset/recovery -count=1
# ok  xirang/backend/internal/backupasset/provider  0.953s
# ok  xirang/backend/internal/backupasset/recovery  1.223s

go test -race ./internal/backupasset/provider ./internal/backupasset/recovery -count=1
# ok  xirang/backend/internal/backupasset/provider  2.191s
# ok  xirang/backend/internal/backupasset/recovery  5.231s

go vet ./internal/backupasset/provider ./internal/backupasset/recovery
```

The final caller-transaction/capability pair also passed under race repetition:

```bash
cd backend
go test -race ./internal/backupasset/recovery \
  -run '^(TestSourceValidatorRefusesRawRsyncHandoffBeforeProviderConsumer|TestSourceValidatorIssuesRsyncCapabilityOnlyAfterLocalContainmentValidation)$' -count=10
# ok  xirang/backend/internal/backupasset/recovery  3.415s
```

`gofmt`, the focused forbidden-import/no-logging scan, `git diff --check`, and
the staged-zero assertion also exited zero. No staging, commit, push,
task-status, archive, or journal action occurred.

One final immediate-GREEN regression closes the caller-transaction bypass:
`RevalidateTx` returns an empty source plus an error for Rsync, so it cannot
reintroduce the raw `ExactRecoverySource` handoff. Fresh post-addition evidence
was:

```bash
cd backend
go test ./internal/backupasset/recovery \
  -run '^(TestSourceValidatorRefusesRawRsyncHandoffBeforeProviderConsumer|TestSourceValidatorIssuesRsyncCapabilityOnlyAfterLocalContainmentValidation)$' -count=1
# ok  xirang/backend/internal/backupasset/recovery  0.129s

go test ./internal/backupasset/provider ./internal/backupasset/recovery -count=1
# ok  xirang/backend/internal/backupasset/provider  0.939s
# ok  xirang/backend/internal/backupasset/recovery  1.211s

go test -race ./internal/backupasset/provider ./internal/backupasset/recovery -count=1
# ok  xirang/backend/internal/backupasset/provider  2.166s
# ok  xirang/backend/internal/backupasset/recovery  5.181s

go vet ./internal/backupasset/provider ./internal/backupasset/recovery
```

### Task 4 B1 architecture and scope correction (2026-07-30)

This is an authorized controller planning correction, not a Task 4 execution,
test result, approval or completion claim. The preceding Provider-local B1
entries are retained as chronological worktree observations only; they are
superseded as acceptance evidence because their raw-locator issuer and
validate-then-reconstruct-path design do not meet the corrected boundary.

The accepted design moves the concrete managed-Rsync descriptor resolver and
`RestorePort` to Repository. Provider retains portable closed
`RsyncRestoreSourceRef`/source-resolver/`RestorePort`/target-writer contracts,
registry kind checking and typed sanitized errors; it imports neither Repository
nor Recovery. Recovery creates only the scalar ref and removes its managed-root
issuer path.
Repository revalidates the durable plan/point/catalog/selection/revision/manifest
and managed root immediately before every preflight, execute, verify and
reconcile call. fileaccess holds a strict no-follow descriptor through bounded
declared-regular-entry use, so the runner receives neither a reconstructed
source/root path nor a raw remote path. Source drift after a target write remains
a Task 6 partial-write condition.

No create or current-planning path changes and no path is removed. The eight
new tracked modify paths are `provider/rsync.go`, `provider/rsync_test.go`,
`repository/query.go`, `repository/query_test.go`, `fileaccess/contracts.go`,
`fileaccess/local_linux.go`, `fileaccess/local_other.go`, and
`fileaccess/local_test.go` under `backend/internal/`; the exact manifest at that
amendment was 9 current + 55 create + 80 modify = 144 unique paths. No migration, table,
`000070+`, `repository/binding.go`, or
`repository/rsync_publication_execution.go` is added.

The replacement B1 gate is specified in `implement.md` Task 4: named
fileaccess/provider/repository/recovery tests must first demonstrate the forged
ref, source/root swap, descriptor mismatch, resolver substitution and error
redaction failures, then pass unchanged after the minimal boundary work. The
Task 8 runtime-injection test remains pending. This amendment ran no product
test or implementation command; at that amendment, Task 4 remained
`authorized/not_started` and Tasks 5--10 remained `not_executed`.

### Task 4 B1 Catalog completion blocker disposition (2026-07-30)

The controller-supplied current disposition is that the corrected B1
Repository-owned resolver/port and pinned fileaccess implementation plus its
focused gates are otherwise complete. This ledger does not reconstruct or
invent a missing command transcript for that work, and it does not treat those
facts as Task 4 approval. A fresh read-only inspection found a deterministic
blocker that keeps Task 4 open.

`provider/catalog.go`'s generic `catalogReadSession.acceptEntry` constructs a
`CatalogRecord` from `Entry` without assigning `FingerprintStrength`. Committed
managed Rsync uses this generic session. Repository's
`sealedCatalogReadSession` only encrypts/clears the Provider locator and does not
repair metadata. `catalog.Indexer.catalogEntryFromRecord` then calls the closed
`ParseFingerprintStrength`; the empty value is invalid, so a real immutable
managed-Rsync Catalog cannot complete and the restore resolver cannot rely on a
genuine active generation.

Read-only test inspection fixed the genuine TDD ownership without adding an
unneeded test path:

- `provider/rsync_test.go` already owns
  `TestRsyncCommittedCatalogRevalidatesExactManifestAtFinalize` and its real
  committed managed-tree fixture. Its pre-GREEN assertion must require empty
  fingerprint plus closed strength `none` on every generic record.
- `repository/query_test.go` already owns the real managed publication/tree
  fixture used by the resolver. It will add
  `TestManagedRsyncCatalogBuildCompletesWithFingerprintNone`, route that fixture
  through Repository and `catalog.Indexer`, and require an active complete
  generation with persisted strength `none`.
- `provider/catalog_contract_test.go`, `repository/catalog_read_test.go`, and
  `catalog/indexer_test.go` were inspected. Their synthetic contract,
  locator-only wrapper and closed-parser coverage remain verification inputs but
  do not require modification for the minimal repair.

The only added tracked modify path is:

```text
backend/internal/backupasset/provider/catalog.go
```

The manifest therefore moves from 9 current + 55 create + 80 modify = 144 to
9 current + 55 create + 81 modify = 145 unique, disjoint paths. No create path,
migration, model/table, `000070+`, or Task 5--10 path is added.

The exact first RED command planned by `implement.md` is:

```bash
cd backend
go test ./internal/backupasset/provider ./internal/backupasset/repository \
  -run '^(TestRsyncCommittedCatalogRevalidatesExactManifestAtFinalize|TestManagedRsyncCatalogBuildCompletesWithFingerprintNone)$' \
  -count=1
```

It has not been executed for this correction. No product file was edited, no
RED or GREEN is claimed, and no focused Provider/Catalog/Repository/Task 4
normal, race, vet, static, specification-review or quality-review gate is
claimed. The minimal future GREEN belongs only in Provider: emit
`FingerprintStrength="none"` with an empty fingerprint for generic entries that
have no proved fingerprint, while leaving Repository sealing and Catalog
validation strict. At that disposition, Task 4 remained open; Tasks 5--10
remained `not_executed`.

Ledger-only validation observed 61 dirty paths. Exactly 59 are members of the
145-path manifest. The only excluded unrelated paths are `go.mod` and the
actual root-level `recovery/testdata/rsync_local_to_remote.json`; both were
preserved without edit. The supplied similarly named path under
`backend/internal/backupasset/recovery/testdata` was absent and remains an
unexecuted manifest create path. This accounting is not product-test evidence.

### Task 4 Catalog fingerprint-strength execution (2026-07-30)

The owned-file hash baseline was captured before edits and the staged count was
zero. The initial literal planned selector could not compile because the
default Go build cache was mounted read-only. Redirecting only `GOCACHE` to
`/tmp` also failed before test execution because that filesystem applied a
per-process disk quota. The selector was therefore rerun with isolated,
temporary workspace `GOCACHE` and `GOTMPDIR` paths; those generated directories
were removed after the checks.

RED command (the selector itself is unchanged):

```bash
cd backend
env GOCACHE=/home/murray/code/xirang/.tmp-go-build \
  GOTMPDIR=/home/murray/code/xirang/.tmp-go-work \
  go test ./internal/backupasset/provider ./internal/backupasset/repository \
  -run '^(TestRsyncCommittedCatalogRevalidatesExactManifestAtFinalize|TestManagedRsyncCatalogBuildCompletesWithFingerprintNone)$' \
  -count=1
```

Observed RED:

```text
--- FAIL: TestRsyncCommittedCatalogRevalidatesExactManifestAtFinalize
    rsync_test.go:246: generic committed-tree record={... Fingerprint: FingerprintStrength: ...}, want empty fingerprint with none strength
FAIL    xirang/backend/internal/backupasset/provider
```

Only `provider/catalog.go` was then changed: generic
`catalogReadSession.acceptEntry` now emits `Fingerprint: ""` and
`FingerprintStrength: "none"`. The focused Provider GREEN was observed after
formatting:

```bash
cd backend
env GOCACHE=/home/murray/code/xirang/.tmp-go-build \
  GOTMPDIR=/home/murray/code/xirang/.tmp-go-work \
  go test ./internal/backupasset/provider \
  -run '^TestRsyncCommittedCatalogRevalidatesExactManifestAtFinalize$' -count=1
```

```text
ok      xirang/backend/internal/backupasset/provider    0.065s
```

The required Repository-to-Indexer regression does not currently reach the
fingerprint parser. Its real managed publication/tree setup deterministically
fails earlier in `repository.openRsyncCatalogRead`:

```text
TestManagedRsyncCatalogBuildCompletesWithFingerprintNone:
build real managed-Rsync Catalog: backup asset conflict: immutable Rsync Catalog point changed
```

The exact guard compares `RsyncCommittedPointAdapter.ListPoints`'
derived committed-tree `SourceRevision` with
`RecoveryPoint.SourceFingerprint`; the two values are generated by different
algorithms. Correcting that comparison requires a Repository change, which is
explicitly outside this focused correction's ownership. A broader normal run
also found an unrelated environment-sensitive Provider preflight failure where
the container reports `FreeInodes:0`. No Catalog/Repository GREEN, race, vet,
or Task 4 completion is claimed. At that incomplete execution stage, Task 4
remained open and Tasks 5--10 remained unexecuted.

### Task 4 Catalog and source-revision corrected execution evidence (2026-07-30)

This entry supersedes the incomplete disposition immediately above. A
provisional GREEN attempted after that entry used a synthetic
`managedRsyncCatalogReadFactory`, constructed an adapter/Catalog request outside
the real Repository read boundary, and rewrote the persisted
`RecoveryPoint.SourceFingerprint` to the runtime access revision. That bypass
is discarded and is not valid RED-to-GREEN or completion evidence. The final
fixture preserves the publication-authenticated fingerprint, passes the real
`*repository.Service` as `catalog.PointReadFactory`, and exercises the real
`Service.OpenCatalogRead`, sealing session and `catalog.Indexer`.

The corrected shared committed-point fixture first asserted that runtime
`SourceRevision` must equal the authenticated request `SourceFingerprint`.
Before the production correction, the exact selector was run with only the
authorized workspace caches:

```bash
cd backend
env GOCACHE=/home/murray/code/xirang/.tmp-c13-task4-gocache \
  GOTMPDIR=/home/murray/code/xirang/.tmp-c13-task4-gotmp \
  go test ./internal/backupasset/provider ./internal/backupasset/repository \
  -run '^(TestRsyncCommittedCatalogRevalidatesExactManifestAtFinalize|TestManagedRsyncCatalogBuildCompletesWithFingerprintNone)$' \
  -count=1
```

Observed behavioral RED:

```text
--- FAIL: TestRsyncCommittedCatalogRevalidatesExactManifestAtFinalize
    rsync_test.go:225: committed runtime source revision="92cc98d70448b6c5ceec447ae9edca2ded5c4dc50ae2a893951a3043dca47012", want authenticated source fingerprint "3333333333333333333333333333333333333333333333333333333333333333"
FAIL    xirang/backend/internal/backupasset/provider

--- FAIL: TestManagedRsyncCatalogBuildCompletesWithFingerprintNone
    query_test.go:170: build real managed-Rsync Catalog: backup asset conflict: immutable Rsync Catalog point changed
FAIL    xirang/backend/internal/backupasset/repository
```

The minimal production correction retains the earlier Provider Catalog change
(`Fingerprint=""`, `FingerprintStrength="none"`) and makes
`RsyncCommittedPointRuntimeAccess.SourceRevision` use the already validated
`request.SourceFingerprint`. The obsolete alternate SHA derivation helper was
removed. No Repository production code, Catalog indexer/parser, or sealing
wrapper was changed for this correction.

The same exact selector then passed unchanged:

```text
ok      xirang/backend/internal/backupasset/provider    0.065s
ok      xirang/backend/internal/backupasset/repository  0.157s
```

Focused committed-runtime coverage also passed:

```bash
go test ./internal/backupasset/provider \
  -run '^TestRsyncCommitted(PointReader|Catalog)' -count=1
# ok  xirang/backend/internal/backupasset/provider  0.409s
```

The focused Catalog/Repository selector passed:

```text
ok      xirang/backend/internal/backupasset/catalog     0.260s
ok      xirang/backend/internal/backupasset/repository  0.194s
```

The exact two-test selector passed under race detection with ten repetitions:

```text
ok      xirang/backend/internal/backupasset/provider    1.682s
ok      xirang/backend/internal/backupasset/repository  2.959s
```

`go vet` passed for `provider`, `catalog`, and `repository`. `gofmt -d`,
`git diff --check`, the focused bypass scan, binary-absence check, branch/HEAD,
empty index and absent `.git/index.lock` checks also passed.

A broader ordinary package run passed Catalog and Repository but remains
environment-blocked in Provider:

```text
ok      xirang/backend/internal/backupasset/catalog
ok      xirang/backend/internal/backupasset/repository
--- FAIL: TestRsyncTreePreflightBuildsBoundEvidenceFromTrustedRoot
    host filesystem evidence reported FreeInodes:0
FAIL    xirang/backend/internal/backupasset/provider
```

This host-capacity failure is recorded separately and does not replace or
invalidate the exact/focused/race/vet gates. It also is not claimed as a broad
Provider package pass. At that controller handoff, Task 4 remained open for
controller review; neither independent Task 4 review was marked complete, and
Tasks 5--10 remained `not_executed`. No staging, commit, push, task-status or
archive action occurred.

Final controller-handoff validation passed both Child and parent `task.py
validate`. Structural extraction from `implement.md` proved 9 current, 55
create and 81 modify paths: 145 unique paths with no overlap or duplicate; all
55 create paths are absent from HEAD and all 81 modify paths are tracked in
HEAD. Migration inspection found exactly the four paired `000069` files and no
`000070+` file. The final dirty union is 64 paths: 62 are in the exact manifest
and the only two exclusions are the preserved unrelated `go.mod` and
`recovery/testdata/rsync_local_to_remote.json`. Both authorized Task 4 cache
directories are absent, the staged count is zero, `.git/index.lock` is absent,
and `backend/xirang-server` is absent with no binary diff.

### Task 4 Catalog source-revision follow-up: inherited GREEN requires context (2026-07-30)

This follow-up was assigned to correct the managed-Rsync Catalog path after the
preceding real Repository-to-Indexer fixture reached the committed-point
source-revision guard. The accepted production fix is that
`NewRsyncCommittedPointRuntimeAccess` must use the already authenticated
`RsyncCommittedPointReadRequest.SourceFingerprint` as its runtime
`SourceRevision`; it must not derive a separate SHA revision.

Before this writer made any product or test edit, the dirty worktree already
contained that exact change in `provider/rsync.go`, including removal of the
separate derived-revision helper. It also already contained the Provider helper
assertion that the committed runtime source revision equals the authenticated
request fingerprint. The Repository fixture already used the real `Service` as
the Catalog `PointReadFactory`; it did not contain a
`managedRsyncCatalogReadFactory` or rewrite `RecoveryPoint.SourceFingerprint`.

The detached factory/fingerprint-rewrite attempt is explicitly rejected and
receives no GREEN credit. It bypasses `Service.OpenCatalogRead` and therefore
does not prove the publication -> Repository sealing -> Catalog Indexer path.
The current fixture reaches that real path and persisted a complete active
generation with empty fingerprint plus `fingerprint_strength=none`.

The required unchanged selector was run with the assigned isolated paths:

```bash
cd backend
env GOCACHE=/home/murray/code/xirang/.tmp-c13-task4-gocache \
  GOTMPDIR=/home/murray/code/xirang/.tmp-c13-task4-gotmp \
  go test ./internal/backupasset/provider ./internal/backupasset/repository \
  -run '^(TestRsyncCommittedCatalogRevalidatesExactManifestAtFinalize|TestManagedRsyncCatalogBuildCompletesWithFingerprintNone)$' \
  -count=1
```

Observed result was inherited full GREEN, not the required behavioral RED:

```text
ok  xirang/backend/internal/backupasset/provider
ok  xirang/backend/internal/backupasset/repository
```

The same two tests also passed under `-race -count=10`. Focused
`RsyncCommitted` Provider coverage, the named Provider/Catalog/Repository
Catalog selector, and `go vet ./internal/backupasset/provider
./internal/backupasset/catalog ./internal/backupasset/repository` passed.
`gofmt -d` for the owned Go files, `git diff --check`, Child and parent task
validation, no-`000070+`, generated-binary absence, staged-index-empty, and
no-index-lock checks also passed.

No genuine RED can be claimed for this follow-up: recreating it would require
reverting the pre-existing correct production change, which this assignment
forbids. No Task 4 completion, review completion, staging, commit, task-status,
or archive action is claimed. This follow-up is `NEEDS_CONTEXT` until the
controller decides how to preserve the required TDD chronology in the presence
of the inherited production GREEN.

### Task 4 committed source-revision binding repair (2026-07-31)

The specification review found that the exported
`RsyncCommittedPointRuntimeAccess.SourceRevision` could be changed after
construction together with `ReadSnapshot.SourceRevision`. The committed adapter
and manifest prover then accepted and published that caller-controlled value.
A separate negative case confirmed that substituting
`RsyncCommittedPointReadRequest.SourceFingerprint` is already rejected by the
authenticated committed-tree evidence.

Observed RED before the production repair:

```bash
cd backend
go test ./internal/backupasset/provider \
  -run '^(TestRsyncCommittedPointReaderRejectsSourceFingerprintSubstitution|TestRsyncCommittedPointReaderRejectsMutatedRuntimeSourceRevision|TestRsyncCommittedCatalogProofRejectsMutatedRuntimeSourceRevision)$' \
  -count=1
```

```text
--- FAIL: TestRsyncCommittedPointReaderRejectsMutatedRuntimeSourceRevision (0.05s)
    rsync_test.go:244: mutated committed runtime source revision published points={Items:[{OpaqueDigest:e99140fba6721e7e4818ea60127cbcf1077fbaa682aae1c361d329f51c337cfc CapturedAt:2026-07-15 11:00:00 +0000 UTC Semantics:xirang_manifest SourceRevision:9999999999999999999999999999999999999999999999999999999999999999 Locator:{Native:committed:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa:ce1d1bdce591d35b14a14916df36092a325517d8403b5ed7f4621e85c42d8b65}}] NextCursor:} err=<nil>, want invalid state
--- FAIL: TestRsyncCommittedCatalogProofRejectsMutatedRuntimeSourceRevision (0.05s)
    rsync_test.go:270: mutated committed runtime source revision published proof={ManifestID:dddddddddddddddddddddddddddddddd Revision:1 DigestAlgorithm:sha256 Digest:b508d0f5529aad92e701fcaf148f5bcdc2593e1eb2d879c00204a4877a90da9f EntryCount:1 Completeness:complete SourceRevision:9999999999999999999999999999999999999999999999999999999999999999} err=<nil>, want Catalog protocol error
FAIL
FAIL    xirang/backend/internal/backupasset/provider    0.153s
FAIL
```

Minimal GREEN requires the runtime revision to equal the private authenticated
request fingerprint in committed-adapter binding validation and manifest proof;
the proof publishes the private request fingerprint. The unchanged focused
selector then passed:

```text
ok  xirang/backend/internal/backupasset/provider  0.153s
```

Fresh focused gates passed: all committed-Rsync Provider tests; the real
managed-Rsync Repository-to-Catalog selector; the strict Catalog contract
selector; the Task 4 Restore/Rsync/Resolver selector; and the three new tests
plus real managed Catalog selector under `-race -count=10` (`provider 3.018s`,
`repository 2.793s`). `go vet` passed for Provider, Catalog and Repository.
`gofmt -d`, Provider dependency scan, Repository locator-wrapper/Catalog-indexer
no-diff check, `git diff --check`, and Child/parent `task.py validate` passed.

Structural validation reported `9 current + 55 create + 81 modify = 145`
unique paths with zero duplicates and valid HEAD contracts. The dirty union was
64 paths: 62 manifest paths plus only preserved unrelated `go.mod` and
`recovery/testdata/rsync_local_to_remote.json`; unexpected paths were zero.
Exactly four paired `000069` files exist, `000070+` is absent,
`backend/xirang-server` is absent, and staged paths are zero. At that repair
observation, Task 4 remained open for controller-owned reviews; no task status,
staging, commit, push, archive or delivery action occurred.

### Task 4 final focused review closure (2026-07-31)

Task 4 is `complete_approved` at focused task scope. The accepted chronology is
unchanged: the Provider missing-strength selector first produced its observed
RED; the real Repository-managed immutable Rsync Catalog then produced the
separate `immutable Rsync Catalog point changed` RED; and the detached synthetic
factory/fingerprint-rewrite attempt was rejected because it bypassed
`Service.OpenCatalogRead`. The production correction uses authenticated
`request.SourceFingerprint` as the committed runtime source revision. The real
`Service.OpenCatalogRead` -> sealing session -> `catalog.Indexer` path then
passed the unchanged focused selector normally and under `-race -count=10`.

The inherited-GREEN follow-up is not a new RED. Recreating a failure after the
authenticated-fingerprint correction would require reverting a pre-existing
correct change, so it is not credited as a separate RED and does not invalidate
the recorded RED-to-GREEN sequence above.

Specification review receipt: `SPEC APPROVED`.

Quality review receipt: `QUALITY APPROVED`.

This closure credits only the focused Task 4 normal/race, vet, format/static,
manifest, staged-index, generated-binary, and task-validation gates already
recorded above. The broad Provider package remains locally blocked by host
`IFree=0` and is not claimed as a pass. No Child/full, PostgreSQL, frontend,
CI, PR, merge, staging, commit, archive, or parent-completion claim is made.
Child remains `in_progress`, parent remains `planning`, Tasks 1--3 remain
`complete_approved`, and Tasks 5--10 remain `not_executed`. The exact manifest
remains 9 current + 55 create + 81 modify = 145.

### Task 4 review follow-up: strict Catalog strength and post-runner revalidation (2026-07-31)

Chronology clarification for the preceding committed source-revision repair:
within its three-test selector, signed request-fingerprint substitution was
immediate GREEN against the authenticated committed-tree evidence and receives
no RED credit. Only the two post-construction runtime-mutation cases were
genuine REDs: mutated runtime revision publication and mutated runtime revision
Catalog proof.

This follow-up added tests and fake support before changing Repository
production code. The unchanged focused selector was:

```bash
cd backend
go test ./internal/backupasset/repository \
  -run '^(TestRsyncResolverRejectsEmptyCatalogFingerprintStrength|TestRsyncRestorePortRevalidatesAfterRunnerError)$' \
  -count=1
```

The genuine RED showed that a real otherwise-valid active Catalog entry with
empty `fingerprint_strength` resolved successfully (`error=<nil>`). It also
showed that Preflight, Execute, Verify and Reconcile returned an ordinary runner
failure as sanitized unavailable after only one source revalidation; the
cancellation/deadline cases likewise recorded only one revalidation instead of
the required post-runner second check.

The minimal GREEN removes empty strength from the accepted Repository contract.
All four runner paths now call `source.Revalidate` after every runner return.
Post-runner source drift overrides an ordinary runner failure, while passing
both causes through the existing sanitizer preserves cancellation/deadline
identity without leaking the runner error. The unchanged selector then passed:

```text
ok  xirang/backend/internal/backupasset/repository
```

Fresh focused verification passed the real committed Catalog/strength selector
for Provider and Repository, plus the Task 4 Restore/Rsync/Resolver matrix for
Provider, Repository and Recovery. The relevant Provider/Repository selector
passed under `-race -count=10` (`provider 1.582s`, `repository 6.301s`). The
full Repository package (`4.373s`), `go vet ./internal/backupasset/repository`,
`gofmt -d` for both owned Go files and `git diff --check` passed.

Both Child and parent `task.py validate` commands passed; Child remains
`in_progress` and parent remains `planning`. Structural extraction remains
exactly 9 current + 55 create + 81 modify = 145 unique paths with zero
duplicates, all 55 create paths absent from HEAD and all 81 modify paths tracked
in HEAD. The dirty union remains 64 paths: 62 manifested plus only the preserved
unrelated `go.mod` and `recovery/testdata/rsync_local_to_remote.json`. Exactly
four paired `000069` files exist, no `000070+` migration exists, and staged
paths remain zero. No task status, staging, commit, push or archive action was
performed.

### Task 5 authorization-receipt model and paired 000069 schema tranche (2026-07-31)

Task 5 remains `in_progress`; this entry closes only the model and paired
migration tranche. Before production edits, the unchanged named RED selector
failed because `BackupAssetRecoveryEvidence` had no receipt fields, 000069 had
no `authorization_receipt` arm or `plan_id`, Recovery had no authorization
service contract, and the API package had no Recovery handler. The focused
SQLite direct-SQL selector independently failed at the absent `plan_id` column.
The required PostgreSQL command was also fail-closed rather than skipped when
its DSN was absent at that observation.

The model now maps the private receipt fields, including the explicit
`step_up_jti_digest` column name. The existing evidence table gains the closed
receipt arm in both 000069 engines without a thirteenth table or a later
migration. Paired CHECKs freeze operation/category/endpoint/effect shapes and
require `proof_expires_at <= replay_expires_at <=
presenting_session_expires_at`; partial unique indexes close requester/endpoint/
key replay and global proof use; the bounded retention and owner-first singleton
source-lease indexes are present. Insert guards bind plan/requester, grant,
job/checkpoint/attempt, node lease/fence, and exactly one `recovery_job` source
lease to the plan RecoveryPoint. Receipt updates and pre-expiry deletes fail
closed, and paired down migration cleanup includes the receipt-owned guards,
functions and shared-table index.

During GREEN iteration, the valid direct-SQL fixture first exposed that its
20-minute replay window was shorter than the already frozen 30-minute write
grant. The fixture was corrected to a 40-minute replay window inside a
50-minute presenting session; production did not weaken the grant deadline
contract. The first full PostgreSQL 000069 down run then exposed the new
evidence-to-grant FK drop order. GREEN drops the evidence table before its
referenced grant table after the unchanged first-statement state guard.

Fresh focused results:

```text
go test ./internal/model ./internal/database \
  -run '^(TestRecoveryAuthorizationReceiptModelContract|TestBackupAssetMigration069AuthorizationReceiptSQLite|TestRecoveryAuthorizationReceiptDirectSQLSQLite|TestBackupAssetMigration069SQLite)$' \
  -count=1
ok  xirang/backend/internal/model
ok  xirang/backend/internal/database

REQUIRE_POSTGRES_RECOVERY_TEST=1 TEST_POSTGRES_DSN=<local isolated PostgreSQL URL> \
  go test ./internal/database \
  -run '^(TestBackupAssetMigration069Postgres|TestRecoveryAuthorizationReceiptDirectSQLPostgres)$' \
  -count=1
ok  xirang/backend/internal/database  49.696s
```

The PostgreSQL URL was assembled in-process from the existing local test
container and was not printed. `git diff --check` passed; the index is empty and
there is no index lock. This tranche makes no service/API/runtime, broad gate,
Task 5 completion, Child completion, staging, commit, push or delivery claim.

### Task 5 authorization-receipt implementation closure and focused verification (2026-07-31)

At this dated implementation checkpoint, Task 5 was
`implementation_done_pending_independent_reviews` at its focused
authorization-receipt scope. This historical implementation disposition was not
`complete_approved`: no independent Task 5 specification or quality receipt had
yet been issued, Tasks 6--10 remained `not_executed`, and no
Child/full/frontend/Docker/CI/delivery action was credited. The later independent
review closure below supersedes only that pending status.

The preceding schema-tranche entry preserves the original named-selector RED:
the model lacked receipt fields, paired 000069 lacked the receipt arm, Recovery
lacked the authorization service contract, and the API package lacked the
Recovery handler. It also preserves the independent SQLite direct-SQL RED. This
entry does not manufacture more chronology. The later expanded direct-SQL cases
and cross-plan/cross-category concurrency cases were immediate-GREEN coverage
corrections against the implemented product and receive no product-defect RED
credit. A transient post-reap SQLite failure used an application clock one day
ahead of the database clock; correcting that fixture to use a truly expired DB
time was test repair, not product RED/GREEN evidence. Separate pre-GREEN output
for every later settings, operation-snapshot and owner coverage addition is not
present in the surviving evidence, so no missing output is reconstructed here.

The implemented focused product now includes:

- one closed `authorization_receipt` arm in the existing evidence table on both
  engines, with requester/endpoint/key and global proof uniqueness, immutable
  update, replay-retention delete guards, deadline ordering and exact private
  effect linkage;
- security override, write grant, exact-mirror delete grant and execute as four
  receipt-first single-commit transactions with same-session replay, full-intent
  conflict classification, live authority revalidation and post-commit bounded
  sanitized audit projection;
- canonical client-supplied 32-byte base64url secrets whose persisted form is a
  domain-separated hash only, with lost-response replay returning metadata and
  execute requiring the matching one-use write grant;
- an encrypted versioned preflight operation snapshot and exact create,
  overwrite, skip and delete job-item projection, with nullable plan-item linkage
  only for delete rows and no guessed fallback;
- atomic Recovery receipt/grant/reaper settings, bounded stateless receipt
  eligibility, and a standalone maintenance owner that continues while admission
  is disabled, retries failed passes, reloads valid config, prevents duplicate
  loops and joins before shutdown/schema drain. Full managed Recovery graph and
  Router/main composition remain owned by Task 8.

The unchanged local Step 7, Step 11 and Step 12 selectors passed:

```text
go test ./internal/model ./internal/database ./internal/backupasset/recovery \
  ./internal/api/handlers \
  -run '^(TestBackupAssetMigration069AuthorizationReceiptSQLite|TestRecoveryAuthorizationReceipt(ModelContract|DirectSQLSQLite|SecurityOverrideReplayAndConflict|WriteAuthorizeReplayAndConflict|DeleteAuthorizeReplayAndConflict|ExecuteReplayAndConflict|ProofReuseAcrossPlanAndCategory|ReplayAfterProofExpiryInSameLoginSession|RejectsDifferentPresentingSession|DoesNotAssertProofJTIEqualsSessionJTI|RejectsUncoverableProofLifetime|ReaperNeverReopensLiveProof|RollbackBeforeCommit|AuditFailureAfterCommit|ConcurrentSQLiteWinner|ReaperProgressAndRestart)|TestRecoveryGrantSecretCanonicalShape|TestRecoveryWriteGrantSecretLostResponseReplay|TestRecoveryDeleteGrantSecretLostResponseReplay|TestRecoveryExecuteRejectsMismatchedGrantSecret)$' \
  -count=1
ok  xirang/backend/internal/model
ok  xirang/backend/internal/database
ok  xirang/backend/internal/backupasset/recovery
ok  xirang/backend/internal/api/handlers

go test ./internal/settings ./internal/backupasset ./internal/backupasset/repository \
  ./internal/api/handlers -run '^TestRecoveryAuthorizationReceiptSettings(RegistryComplete|AtomicSnapshot|DeadlineOrdering|Transitions)$' -count=1
ok  xirang/backend/internal/settings
ok  xirang/backend/internal/backupasset
ok  xirang/backend/internal/backupasset/repository
ok  xirang/backend/internal/api/handlers

go test ./internal/model ./internal/database ./internal/backupasset/recovery \
  -run '^(TestRecoveryAuthorizationReceipt(OperationSnapshotModelContract|ExecutePersistsExactOperationRows|ExecuteRejectsMissingOrTamperedOperationSnapshot|ExecuteDeleteRowHasNoPlanItem)|TestBackupAssetMigration069.*OperationSnapshot)' \
  -count=1
ok  xirang/backend/internal/model
ok  xirang/backend/internal/database
ok  xirang/backend/internal/backupasset/recovery
```

Required real PostgreSQL used the local isolated test service with its URL
redacted. The exact receipt selector and PostgreSQL operation-snapshot selector
passed without skips:

```text
REQUIRE_POSTGRES_RECOVERY_TEST=1 TEST_POSTGRES_DSN=<redacted local PostgreSQL URL> \
  go test ./internal/backupasset/recovery ./internal/database \
  -run '^(TestRecoveryAuthorizationReceiptDirectSQLPostgres|TestRecoveryAuthorizationReceiptConcurrentPostgresWinner|TestRecoveryAuthorizationReceiptRollbackBeforeCommitPostgres)$' \
  -count=1
ok  xirang/backend/internal/backupasset/recovery  2.142s
ok  xirang/backend/internal/database              3.580s

REQUIRE_POSTGRES_RECOVERY_TEST=1 TEST_POSTGRES_DSN=<redacted local PostgreSQL URL> \
  go test ./internal/database ./internal/backupasset/recovery \
  -run '^TestBackupAssetMigration069PostgresOperationSnapshot$' -count=1
ok  xirang/backend/internal/database              1.733s
ok  xirang/backend/internal/backupasset/recovery  0.052s [no tests to run]
```

Repeated race coverage passed for SQLite and PostgreSQL winner election and for
the standalone owner lifecycle:

```text
go test -race ./internal/backupasset/recovery \
  -run 'TestRecoveryAuthorizationReceipt(ConcurrentSQLiteWinner|.*ReplayAndConflict|.*RollbackBeforeCommit|RejectsUncoverableProofLifetime|ReaperNeverReopensLiveProof|ReaperProgressAndRestart)' \
  -count=10
ok  xirang/backend/internal/backupasset/recovery  10.019s

REQUIRE_POSTGRES_RECOVERY_TEST=1 TEST_POSTGRES_DSN=<redacted local PostgreSQL URL> \
  go test -race ./internal/backupasset/recovery \
  -run '^TestRecoveryAuthorizationReceiptConcurrentPostgresWinner$' -count=10
ok  xirang/backend/internal/backupasset/recovery  12.373s

go test -race ./internal/backupasset/runtime \
  -run '^TestRecoveryAuthorizationReceiptOwner' -count=50
ok  xirang/backend/internal/backupasset/runtime  1.868s
```

The Step 10 aggregate passed for model, database, Recovery and handler packages.
Additional required PostgreSQL reaper parity passed for database-clock authority,
protected live grants ahead of `LIMIT`, and all four effect kinds. The full real
PostgreSQL 000069 matrix then passed:

```text
REQUIRE_POSTGRES_MIGRATION_TEST=1 TEST_POSTGRES_DSN=<redacted local PostgreSQL URL> \
  go test ./internal/database -run '^TestBackupAssetMigration069Postgres$' -count=1
ok  xirang/backend/internal/database  132.059s
```

The first broad affected-package run found one genuine completeness regression
in an existing settings invariant:

```text
--- FAIL: TestBackupAssetSearchConfigAndOverlayConfigDefinitionsAndSafeDefaults
    service_test.go:350: backup asset setting count=191, want 186
FAIL  xirang/backend/internal/settings
```

The product registry correctly contained the five planned Recovery keys; only
the aggregate test count omitted `backupAssetRecoverySettingKeys`. The minimal
test correction derives the expected count from that closed key list. Its
unchanged focused selector, the Task 5 settings selector and the full settings
package then passed:

```text
ok  xirang/backend/internal/settings
```

In that same broad run, model, database, Recovery, runtime, Foundation,
Repository and handler packages passed. `go vet` passed for Recovery, handlers,
model and database; touched Go files had empty `gofmt -d` output. The named fake
privacy scan returned only test fixtures in `recovery/testutil_test.go`,
`recovery/service_test.go` and `backup_recovery_handler_test.go`; no production
model, service or handler match appeared.

After the settings-count correction, a final `-count=1` broad rerun passed all
eight affected packages together: model, database, Recovery, runtime, settings,
Foundation, Repository and handlers. Expanded `go vet` over the same eight
packages passed. Both Child and parent `task.py validate` commands passed.

The historical post-implementation structural reconciliation at this checkpoint
reported exactly 9 current
+ 55 create + 81 modify = 145 unique manifest paths, with all create paths absent
from HEAD and all modify paths tracked at HEAD. That historical dirty union was
76 paths: 74 manifest paths plus only the preserved unrelated `go.mod` and
`recovery/testdata/rsync_local_to_remote.json`. Exactly four paired 000069 files
exist, no 000070+ migration or `backend/xirang-server` exists, and the staged
index remains empty.

### Task 5 independent specification and quality review closure (2026-07-31)

This is a ledger-only closure after the implementation and focused verification
recorded above; it preserves that RED/GREEN chronology without adding or
reconstructing a failure. Task 5 is `complete_approved` at focused
authorization-receipt scope only.

The independent specification review receipt
`019fb71a-75df-7770-a17d-9b3d8647d99d` returned `SPEC APPROVED`. It independently
passed exact Steps 7/11/12, SQLite and PostgreSQL races, the full PostgreSQL
`000069` matrix, manifest/static/Trellis/index checks, and confirmed that full
Task 8 graph wiring is intentionally deferred.

The independent quality review receipt
`019fb73d-03b6-7111-baf3-83e1ae2e3f8b` returned `QUALITY APPROVED: READY` with
no Critical, Important, or Minor finding. It independently passed focused and
eight-package tests, SQLite race x10, runtime owner x50, PostgreSQL winner x10,
direct-SQL/rollback, and vet/format/diff/Trellis/manifest/index checks.

The two receipts leave the exact 9 + 55 + 81 = 145 manifest and its two
unrelated dirty-union exclusions, `go.mod` and
`recovery/testdata/rsync_local_to_remote.json`, unchanged. Tasks 6--10, the full
Task 8 graph, frontend/Child/full/CI/delivery gates remain `not_executed` or
open. Child remains `in_progress`; parent remains `planning`. No product gate is
rerun for this ledger-only entry, and it makes no staging, commit, push, PR,
merge, archive, or delivery claim.

### Task 6 restart-adoption persistence amendment specification closure (2026-07-31)

This is a ledger-only planning/specification record. The controller approved
the focused Task 6 amendment and the final independent result is `SPEC
APPROVED`. It is not implementation evidence: no amendment test, RED, GREEN,
product/model/migration change, Task 6 completion, Child/full gate or delivery
action occurred.

The proposal review chronology is preserved without rewriting any review into a
different result:

1. Initial independent review: 3 Critical + 2 Important.
2. First revision: rejected because it invented a delete absence digest.
3. Corrected controlling revision: removed that digest, then received 2
   Important findings for skip/source identity conflation and insert-before-
   grant ordering.
4. Both corrections were adopted.
5. Final independent result: `SPEC APPROVED`.
6. Nonblocking clarification: skip's separately frozen prior-target bytes are
   immutable, and the exact key/version lock is transaction-scoped through
   grant CAS plus complete aggregate insert.

The approved design freezes thirteen product corrections without adding scope:

1. every operation snapshot/job item persists operation-correct expected-post
   digest and byte facts, including a length-framed empty delete post field;
2. create/overwrite fresh source revalidation must equal persisted frozen
   RestoreEntry post digest/bytes;
3. skip source revalidation remains separate from exact unchanged-target
   verification against frozen prior-target digest/bytes and projects skipped;
4. delete requires explicit exact absence plus durable delete authority and has
   no absence digest;
5. Verify is a closed present/absent expectation and observation union with a
   strong observed revision;
6. target-chain absence is separately domain-bound while expected-post remains
   empty;
7. every schema-v2 operation, including delete, persists its canonical
   `target_relative_locator` and `SemanticTargetDigest`, with no locator
   fallback and with alias/normalization/duplicate/collision/cross-item/rename
   behavior rejected;
8. execute preparation preallocates job/item/workspace identity and a distinct
   final `TargetObjectDigest`; isolated complete insert starts at none-state,
   `PrepareFirstWrite` reuses that identity, and only the final digest enters
   `TargetObjectRef.TargetPathDigest` after locked decrypt/strict join/recompute;
9. every item locator is recovery-local row-bound AEAD ciphertext with explicit
   positive key/cipher versions and complete length-framed row/job/root/
   workspace/digest/operation binding; generic model hooks are excluded;
10. generic `enc:v2` is the encrypted preflight `EncryptedOperationRows`
    snapshot, not item AEAD; generic encryption remains for that snapshot and
    the job workspace locator, while the whole operation product is revalidated
    on every load;
11. restart adoption is exactly
    `AdoptInterruptedOperation(ctx, claim, jobItemID)` and derives all facts in a
    DB load/decrypt/validation phase, performs target I/O without a DB
    transaction, then executes one final re-lock/fenced CAS;
12. permanent cleanup-key loss performs bounded idempotent DB-only current
    post-arm reconciliation before returning the original fatal startup error;
    execute prepares everything outside the effect transaction, then uses
    ordered locks, byte equality, exact `LockActiveTx`, grant-CAS-first complete
    aggregate insertion and atomic rollback on failure; and
13. paired `000069` alone freezes workspace states and identities, both item
    digests, locator ciphertext/versions, the operation matrix, uniqueness and
    one-way terminal projection, then removes every added trigger/function only
    after the existing down data guard.

The mandatory future TDD gate includes expected-post/skip-byte/delete-empty
goldens; snapshot/envelope/ciphertext/cross-row tamper; non-echo fakes; wrong
root/item/cross-adoption/collision; removal of caller-forged API inputs; key
loss/version/auth; source drift; skip unchanged-target behavior; explicit delete
absence versus ambiguous missing; SQLite plus required real PostgreSQL direct
SQL/pristine/down/reapply parity; and deterministic race/takeover one-winner
coverage. Every negative must assert zero sequence-1 checkpoint, item/job
success or skipped projection, target-chain advance, forbidden target I/O and
plaintext leak.

At this specification closure those RED and GREEN selectors were
`not_started`. The later current ledger identifies the remaining Task-6-owned
work as original-review F3/F4/F6 portions, first-thirteen evidence closure,
unchecked execution items and whole gates/reviews; the original review is F1--F8
and has no Finding 9. Tasks 7--10 remain open/not executed. Tasks 1--5 keep their
prior approvals. The exact manifest remains 9 current + 55 create + 81 modify =
145 unique paths; only the existing paired `000069` may later be amended, and no
`000070`, backfill, new table or new path is authorized.

### Dated ledger reconciliation after Task 6 planning approval (2026-07-31)

This current snapshot supersedes, but does not rewrite, the explicitly
historical 76-path/74-manifest-path Task 5 implementation checkpoint above. The
expanded dirty union is 80 paths: 78 belong to the approved manifest, comprising
all 9 Phase/current paths, 32 of 55 create paths and 37 of 81 modify paths. The
only two unrelated paths remain `go.mod` and
`recovery/testdata/rsync_local_to_remote.json`; staged paths remain zero.

The approved manifest itself remained exactly 9 current + 55 create + 81 modify
= 145 unique paths. At this dated snapshot Task 5 was `complete_approved` at focused
authorization-receipt scope with specification receipt
`019fb71a-75df-7770-a17d-9b3d8647d99d` (`SPEC APPROVED`) and quality receipt
`019fb73d-03b6-7111-baf3-83e1ae2e3f8b` (`QUALITY APPROVED: READY`). Task 6
product/test/migration work and its RED/GREEN selectors had not started at that
point. The later artifact record below supersedes current-status wording without
rewriting that history: Task 6 is now `in_progress`; original-review F3/F4/F6
portions, first-thirteen evidence closure, unchecked execution items and whole
gates/reviews remain open, while Tasks 7--10 remain open/not executed. The
original review contains F1--F8 only and no Finding 9.

### Task 6 locator-contract artifact repair — planning only (2026-08-01)

This section is explicitly **not implementation evidence**. No product, test,
model, migration or spec file was changed for this repair; no Task 6 RED,
GREEN, focused gate, PostgreSQL gate or completion claim was produced. The
independent design result was `DESIGN READY`, and controller direction approved
it inside the existing thirteen corrections. Their count remains thirteen, the
exact 9 current + 55 create + 81 modify = 145 manifest is unchanged, and no new
path, table, route, migration, backfill or `000070` is authorized. Task 6
remains `in_progress`; Tasks 7--10 remain open/not executed.

The controlling planning contract is:

1. execute preparation allocates the opaque job ID, every item ID and isolated
   `jobs/<opaque>` workspace identity in memory outside the transaction, without
   a row or remote reservation;
2. every schema-v2 operation, including delete, persists its canonical
   `target_relative_locator` and `SemanticTargetDigest`, while the strict-joined
   final `TargetObjectDigest` remains separate and is the only value carried by
   `TargetObjectRef.TargetPathDigest`;
3. isolated complete insert starts at workspace phase `none` with the encrypted
   preallocated workspace locator and immutable `WorkspaceBindingDigest`, but
   empty marker/owner/fence/deadline; in-place none has no workspace fields, and
   `PrepareFirstWrite` reuses the persisted identity;
4. each item persists both digests, the complete operation product,
   recovery-local AEAD locator ciphertext and positive explicit key/cipher
   versions; generic model hooks exclude the item locator, while generic
   encryption remains for `EncryptedOperationRows` and job workspace locator;
   existing generic `enc:v2` is the encrypted preflight snapshot, not item AEAD;
5. `TargetLocatorEnvelopeBinding` length-frames all row/job/root/workspace/
   digest/version/operation facts and authenticates the exact item and workspace
   locator plaintexts;
6. outside the execute transaction: replay, decode, whole-product validation,
   association resolution, preallocation, exact-key selection, digesting and
   encryption; inside it: ordered locks/rechecks, byte-for-byte recomputation,
   exact `LockActiveTx`, grant CAS as first effect mutation, complete aggregate
   and receipt insert, one commit, and no encryption/provider/SSH/target/audit/
   reservation work;
7. preparation failure leaves no state, transaction failure rolls back grant
   and aggregate, post-commit failure leaves a complete unreserved aggregate
   whose identity is reused, and an unexpected remote directory fails closed;
8. paired `000069` alone freezes the workspace matrix, identities, both digests,
   ciphertext/versions, operation matrix, uniqueness and terminal projection;
   service reload reconstructs and validates the full item set before I/O; and
9. adoption remains exactly
   `AdoptInterruptedOperation(ctx, claim, jobItemID)`: short DB load/decrypt/
   validation, transaction-free target I/O, then final re-lock and fenced CAS.

The following twelve selector names are frozen as the next Task 6 TDD contract:

```text
TestRecoveryOperationSnapshotV2CanonicalLocatorMatrix
TestRecoveryOperationSnapshotV2WholeProductTamperMatrix
TestRecoveryExecutePreparedAggregateGrantFirstMatrix
TestRecoveryTargetLocatorCiphertextBindingMatrix
TestRecoveryPrepareFirstWriteUsesPreallocatedWorkspaceMatrix
TestRecoveryAdoptInterruptedOperationDurableDerivationMatrix
TestRecoveryVerifyOperationProductMatrix
TestRecoveryPermanentCleanupKeyLossMatrix
TestBackupAssetMigration069RecoveryLocatorProductSQLite
TestBackupAssetMigration069RecoveryLocatorProductPostgres
TestRecoveryLocatorRaceTakeoverOneWinner
TestRecoveryLocatorProductNoPlaintextLeak
```

Every future negative case must assert zero sequence-1 checkpoint, zero item
`success`/`skipped`, zero job success, zero target-chain advance, zero forbidden
target I/O and zero raw locator leak. The SQLite-first RED command and the
required real PostgreSQL RED command are both `not_executed`; this planning
section must never be cited as their output.

At this artifact snapshot the dirty union is 82 paths: 80 manifest paths plus
the protected unrelated `go.mod` and
`recovery/testdata/rsync_local_to_remote.json`; staged paths remain zero. Their
SHA-256 values remain respectively
`b767fc9ef9376c651d0493329b710ac8dcf8d77e52686b847d244aff9f6d48fd` and
`2570bd4472541322d902c4cdf2fe43f247b69f19d335558830e749567f763892`.

### Task 6 B3 unresolved-remote-outcome focused closure (2026-08-02)

This dated entry records controller-supplied execution/review evidence; the
ledger writer did not rerun product tests. Task 6 remains `in_progress`. The
stable batch status is:

| Batch | Scope | Status and credit |
|---|---|---|
| B1 | ordinary/foundation implementation for Corrections 1--3, 5 and 7--13 | `IMPLEMENTED_UNPROVEN`; no retroactive RED or review credit |
| B2 | exact-mirror/multi-delete implementation for Corrections 4 and 6 plus its delete row | `IMPLEMENTED_UNPROVEN`; no retroactive RED or review credit |
| B3 | Correction 14 unresolved remote outcome | `PROVED_COMPLETE_FOCUSED_ONLY` |

The sole bounded B3 final writer changed only
`backend/internal/backupasset/recovery/executor_test.go` and
`backend/internal/database/backup_asset_migrations_integration_test.go`. Its
recorded final evidence is limited to the focused Correction 14 scope:

- required real PostgreSQL `000069` plus the six-case behavior matrix passed
  with no skip;
- the bounded cancellation set and focused race passed;
- affected exact-mirror regressions, `go vet`, owned `gofmt -d`,
  `git diff --check`, manifest and staged-zero guards passed;
- disposable resources were cleaned; and
- the exact manifest remained 9 Phase-1 + 55 create + 81 modify = 145 unique,
  disjoint paths.

Independent specification receipt
`019fc0c2-cfda-74e3-b218-246f3a425545` returned `APPROVED` and closed both prior
Important test-evidence findings. The controller-inline code-quality review had
no findings; its fresh vet, owned `gofmt -d` and `git diff --check` exited zero,
with staged paths zero. A local reviewer rerun could not link because of host
disk quota and is deliberately not claimed as either pass or fail.

Only Correction 14's focused evidence is closed. The remaining Task-6-owned
ledger is original-review F3/F4/F6 portions, first-thirteen evidence closure,
unchecked execution items, whole specification and quality reviews, and whole
gates. The original review is F1--F8; Design Corrections 5--9 belong to the
approved first thirteen and are not review-finding numbers. No Finding 9 exists.

Ownership remains split: Task 6 owns preallocated workspace/reservation,
deadline and cleanup-only classification plus bounded restart adoption/
reconciliation semantics. Task 7 owns publication, Content revalidation,
revoking takeover, cleanup node-lease behavior and RecoveryResultRef denial.
Task 8 owns startup/listener ordering and managed lifecycle. Tasks 7--10 remain
`not_executed`.

The next authorized planning gate is focused F6 live-mutation-permit TDD, still
`not_executed` pending independent planning/spec approval before its writer
starts. Its permanent path is
`backend/internal/backupasset/recovery/worker_test.go`; its temporary controlled
baseline is `backend/internal/backupasset/recovery/target.go`, which must be
restored to final intended behavior and never staged in RED state. After F6 the
sequence is F3, bounded B1/B2 evidence closure, Task-6-owned F4 workspace/
deadline/cleanup-only proof, whole Task 6 specification review, whole Task 6
quality review and all frozen/race/required-PostgreSQL/static/manifest gates,
then Task 7. No path, migration, context row, table, route or API is added or
removed by this reconciliation.

### Task 6 F6 live-mutation-permit focused closure (2026-08-02)

This later entry records the writer and independent-review receipts without
rerunning product tests in the ledger-writer turn. It supersedes only the current
F6 status and next-order wording in the dated B3 snapshot above; historical
commands and statuses remain unchanged.

F6 is `complete_approved` at focused live-mutation-permit scope only. It gives no
credit to F3, B1/B2, F4, whole Task 6, Child, delivery or full gates. The sole
permanent delta is exactly:

- the admission-recording fake near
  `backend/internal/backupasset/recovery/worker_test.go:34`; and
- `TestRecoveryReviewF6LatchBeforeTargetMutation` near line 669 of the same
  file.

The frozen hashes are:

```text
target.go final        8a0efaafc5bb08d3981790cc0fa27760936b80a58862f1910fd3e96dd5c64822
worker_test.go start   a2452e6d5f01c4afb9fb5255ecc188b8790b695f0121430ac078a58cce373534
worker_test.go final   352c31b6e5ced3f9f4a033a096ee90c5cd196be3bc4da65ab426bca18254ab3d
```

`target.go` was changed only for the controlled RED and restored byte-for-byte.
The genuine RED bypassed only the `TargetMutationPermit` live-proof callback.
Every revoked permanent latch, current job, attempt fence, node-lease fence and
source fence reached `CreateOwnedJobDir` and produced this failure:

```text
revoked authority CreateOwnedJobDir error=<nil>, want ErrInvalidTargetPermit
```

Compilation and quota failures are explicitly not RED evidence. With final live
validation restored, current authority admits `CreateOwnedJobDir`,
`CreateDirectory`, `WriteAtomic` and `Delete`; permanent latch loss plus current
job, attempt-fence, node-lease-fence and source-fence loss reject before the
recording fake mutates. `RemoveOwnedJobDir` remains deferred to Task 7.

The writer recorded focused PASS for:

```bash
cd /home/murray/code/xirang/backend
go test ./internal/database ./internal/model ./internal/backupasset/recovery \
  -run '^(TestRecoveryReviewF6UseLatchSQLite|TestRecoveryReviewF6LatchBeforeTargetMutation|TestRecoveryReviewF6UsedDownAtomicRefusal)$' \
  -count=1
go test -race ./internal/backupasset/recovery \
  -run '^TestRecoveryReviewF6LatchBeforeTargetMutation$' -count=10
go test ./internal/backupasset/recovery \
  -run '^(TestRecoveryPrepareFirstWriteUsesPreallocatedWorkspaceMatrix|TestWorkerPrepareFirstWriteCommitsLatchReservationAndFenceBoundPermit|TestRecoveryOrdinaryExecutionMutationMatrix|TestRecoveryExactMirrorSuccessfulDeleteProjectsAbsenceCheckpointAndChain)$' \
  -count=1
```

Owned `gofmt`, `go vet ./internal/backupasset/recovery ./internal/database`,
`git diff --check`, exact-manifest and staged-zero checks also passed.

Independent specification thread `019fc136-feca-7fb0-82bc-3c33739aef12`
returned `SPEC APPROVED`, confirming the sole permanent `worker_test.go` delta,
byte-for-byte `target.go` restoration, all three frozen hashes, the 145-path
manifest and staged paths zero. Independent quality thread
`019fc13c-0710-7343-b261-dd866382a8c0` returned `QUALITY APPROVED`, confirming
deterministic isolated fixtures, reliable admission recording, the frozen
hashes, manifest and staged paths zero.

Required PostgreSQL gate thread `019fc13d-ea0e-7f93-b1c6-32aebcb7368e`
returned `POSTGRES GATE PASSED` for exactly:

```bash
REQUIRE_POSTGRES_MIGRATION_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" go test ./internal/database -run '^TestRecoveryReviewF6UseLatchPostgres$' -count=1
```

The result was exit 0, `ok xirang/backend/internal/database 1.709s`, wall
31.032s, against PostgreSQL 18.4 from isolated `postgres:18-alpine` at loopback
database `xirang_f6_gate`. The first two compile attempts exhausted `/tmp` quota
and never reached tests, so they are neither RED nor test evidence. The passing
run moved Go and cgo temporary work to `/dev/shm`. Created container and scratch
resources were removed; pre-existing resources were untouched.

The exact approved manifest remains 9 Phase-1 + 55 create + 81 modify = 145
unique/disjoint paths, with staged paths zero. Task 6 and the Child remain
`in_progress`; the parent remains `planning`. The fixed next order is F3,
B1-E1/E2/E3, B2-E1/E2, Task-6-owned F4, whole Task 6 specification review,
whole quality review, all final gates, then Task 7.

### Task 6 F3 Plan A+ bounded adjudication (2026-08-02)

This entry records a design decision only. The bounded inline adjudication made
no product/test/migration edit, ran no F3 RED/GREEN or PostgreSQL product gate,
and grants no F3 implementation, Task-6, Child, delivery or final-gate credit.

The adjudication compared the completed stateless scope task
`019fc163-15de-7ca3-be2b-1c1cb42284ce`, the interrupted persistent-plan tiebreak
`019fc16d-fa98-7c13-9aed-7f94b447a233`, the controlling F3 fairness and
reconciliation contracts, current `ClaimNext`/`TakeoverExpired` behavior, the
restart tests, paired `000069` schema/down guards and the durable Export
cursor/high-water precedent. It found a required third candidate class:
post-selection conflict/fence failure can leave a candidate SQL-eligible at the
same ordered key across repeated invocations and process restarts. The current
tests cover only an old prefix excluded by SQL before `LIMIT`, and no existing
durable fact advances beyond the persistent eligible prefix.

The user approved persistent Plan A+. The exact product decision is:

- extend the existing `backup_asset_recovery_evidence` product with a closed
  `scheduler_state` arm and two seeded fixed rows, claim
  `0000000000000000000000000000006a` and takeover
  `0000000000000000000000000000006b`;
- persist scope, cursor timestamp/ID, high-water timestamp/ID and monotonic
  scheduler revision, with claim ordered by `recovery_job.updated_at,id` and
  takeover ordered by `attempt_row.lease_expires_at,id`;
- durably pre-advance one candidate in a short scheduler transaction before its
  separate claim/takeover transaction, trying at most the scan bound per public
  call;
- treat expected conflict/fence loss as scheduler-only progress with zero stale
  domain/remote mutation, crash as one-candidate delay until wrap, and a
  database-wide failure as a failed pass rather than candidate success;
- amend both SQLite and PostgreSQL `000069` up/down files, with down ignoring
  only the two exact shape-checked scheduler rows while remaining fail-closed
  for the latch and every real Recovery/Content/lease fact; and
- retain atomic authority-only pre-write-drift terminalization plus replay from
  the immutable receipt/frozen execute intent as part of the same F3 batch.

Stateless Plan B is rejected because same-call skipping loses its traversal
position on restart. Per-Job/Attempt `next_attempt_at` rotation is rejected
because a stale fence loser cannot mutate domain state to repair scheduler
fairness. No new table, path, `000070`, route or API is authorized.

The product owner set is exactly `worker.go`, `worker_test.go`, `service.go`,
`service_test.go`, `model/backup_asset_recovery.go`, paired SQLite/PostgreSQL
`000069` up/down files and
`backup_asset_migrations_integration_test.go`, all already in the 145-path
manifest. The four frozen selectors are:

```text
TestRecoveryReviewF3PreWriteDriftTransactionSQLite
TestRecoveryReviewF3ExecuteReplayAfterDrift
TestRecoveryReviewF3TwoWorkerAndCrashBarriers
TestRecoveryReviewF3PreWriteDriftTransactionPostgres
```

The next milestone is a separate bounded TDD writer: controlled-baseline RED,
same-selector GREEN, required non-skipped real PostgreSQL, focused race/
regression/static/paired-migration/manifest/staged-zero evidence, then a hard
stop before B1/B2. Task 6 and the Child remain `in_progress`; the parent remains
`planning`.

Fresh adjudication-only validation after recording the decision passed:

- Child and parent `task.py validate` both exited zero; Child context contains
  17 valid implement entries and 18 valid check entries;
- `task.json`, `implement.jsonl` and `check.jsonl` parse as JSON/JSONL;
- the exact manifest parser returned
  `phase1=9 create=55 modify=81 total=145 unique=145 duplicates=0`, and all ten
  F3-owned product paths are members;
- `target.go` and `worker_test.go` still match their recorded F6 final SHA-256
  values; the complete tracked and untracked non-Trellis fingerprints still
  match the R0 frozen values; and
- no changed adjudication file has trailing whitespace and staged paths remain
  zero.

No product test, build, lint or PostgreSQL gate was run because this milestone
made no product change and claims no product correctness. No `.trellis/spec/`
file was changed: Plan A+ is still a task-specific, unimplemented contract and
the user-approved adjudication boundary is limited to Child 13 task artifacts.
The F3 implementation milestone must reassess `trellis-update-spec` after the
schema and dual-engine behavior are implemented and verified.

### Task 6 F3 persistent scheduler and pre-write-drift focused closure (2026-08-02)

F3 is `complete_checked` at its exact focused scope. This record closes only
the persistent claim/takeover scheduler, legal authority-only pre-write-drift
transaction and frozen execute-replay batch. It grants no B1/B2, F4, whole
Task 6, Child, delivery or final-gate credit. No B1/B2 work started.

The four selector names remained fixed throughout the controlled RED/GREEN
cycle. Genuine RED was observed before production correction:

- authority-only pre-write drift returned a wrapped
  `ErrRecoveryWorkerUnavailable` rather than committing the legal zero-write
  terminal transaction;
- same-key execute replay after source drift returned an idempotency conflict;
- claim stopped at the first persistent fence loser instead of reaching later
  candidates within the bounded call;
- takeover could skip a loser within one call but lost traversal progress after
  process restart; and
- the required real PostgreSQL authority-drift selector reproduced the same
  missing terminalization behavior.

GREEN added exactly the approved Plan A+ product in the ten F3-owned files:

- two fixed `scheduler_state` evidence rows for claim and takeover with closed
  cursor/high-water/revision fields and paired SQLite/PostgreSQL row shape,
  update/delete guards, seeds and scheduler-aware down/admission guards;
- durable scheduler pre-advance before the candidate-local claim/takeover
  transaction, bounded skip of expected conflict/fence loss, restart-stable
  progress and one-candidate crash delay until sweep wrap;
- the sole guarded `executed -> superseded` authority-drift transaction before
  mutation arm/checkpoint/ambiguous observation, including failed job,
  superseded attempt, unused-authority revocation and source/node lease release;
  and
- execute receipt replay from the frozen request plus durable effect linkage,
  without re-reading mutable source intent.

The first broad migration run after the core selectors were GREEN found that
legacy migration tests and the UP downgrade-admission triggers still treated the
two permanent scheduler rows as ordinary Recovery evidence. A focused migration
RED then failed only pristine/scheduler-only down. The paired UP guards now
ignore only the exact fixed ID/scope scheduler rows, while ordinary cleanup and
Content-only assertions exclude only those infrastructure rows. Dual-engine
migration coverage now proves all six scheduler fields, both triggers, the
PostgreSQL guard function, fixed identity/scope/created-at immutability,
permanent deletion refusal, exact one-step revision increments, legal cursor
advancement and scheduler-only down.

The first post-GREEN frozen-selector run also exposed one test-only ordering
assumption. Four jobs shared one creation timestamp, initial claims therefore
ordered by random opaque ID, and takeover expiry was assigned in `claims`
order; the assertion incorrectly expected `jobs[2]` while already comparing the
attempt to `claims[2]`. The focused subtest failed 7 of 10 repetitions. Changing
only the expected job to `claims[2].JobID` made the same subtest pass 20 of 20;
no scheduler behavior changed.

Fresh focused verification passed:

```text
all SQLite TestBackupAssetMigration069*                         PASS (11.778s)
real PostgreSQL TestBackupAssetMigration069Postgres + F3 drift PASS (138.198s)
three SQLite/unit frozen F3 selectors                          PASS (0.258s)
same three selectors under -race -count=10                     PASS (7.103s)
model/recovery/database affected package regressions           PASS
go vet recovery/model/database                                 PASS
owned gofmt -d and git diff --check                            PASS
```

The PostgreSQL run used required mode against the isolated container
`xirang-f3-red-pg-019fc14d`; no selector skipped. The exact manifest
remains `phase1=9 create=55 modify=81 total=145 unique=145 duplicates=0`.
The actual dirty union remains 82 paths: 80 manifest members plus only the two
preserved unrelated paths `go.mod` and
`recovery/testdata/rsync_local_to_remote.json`; outside-allow-list paths are
zero. All ten F3 product paths are manifest members, `000070+` remains absent,
the staged index is empty, `.git/index.lock` is absent, and branch/HEAD remain
`codex/backup-assets-controlled-recovery` at
`51771654a85967656fe1ca69686590b734ff9214`.

Final F3-owned file SHA-256 values are:

```text
worker.go                                      75e4c4a2beb421a6f76d6d9b752f7afb47cc034a6609d00dd0760b66e2798972
worker_test.go                                 a55edadc9bf91067582a2c5bbc08c0eb73d2e6c5d44944b8b8f0b312e107cc03
service.go                                     9cfd9f07178061297ff3be7cc7cc12f6f823a3c82ed5c8d9a5c7e6f8c1f00a57
service_test.go                                d220aac729d6ee40b1ca9a97cd09e002bd5296bef85b2ccf6e12ca639b6ac53b
model/backup_asset_recovery.go                 30e52c58f52e4df7f138653dc06321fa0cd122bd8d49369258b0628fbea679bf
backup_asset_migrations_integration_test.go    ac40a3afbc3902e94a7100267676983426fa1860266ea95e66edb716da600f34
sqlite 000069 up                               f842be1eece0adcd1d84b9ec5e4216e96c67a4eca9b9308886be2b6ee97edad5
sqlite 000069 down                             4e178bd1733537a0375fde2003af4155c8d03614ab0bc67713efee2a4c68eb1c
postgres 000069 up                             42c92b3c4e6c56ac57d67cdf249746c682abe94efbe915182e54bb0f3ca8c67d
postgres 000069 down                           48bb3c57bf8757db6e86f8254b989584384a2153778f027a6a210557e13dcafb
```

The pre-existing F6 `target.go` hash remains exactly
`8a0efaafc5bb08d3981790cc0fa27760936b80a58862f1910fd3e96dd5c64822`.
The isolated PostgreSQL container and exact `/dev/shm` Go scratch directory
were removed after all gates. `trellis-check` found no remaining focused issue.
No `.trellis/spec/` update was made: the durable scheduler is a Child-13-specific
unshipped product contract already captured in `design.md`, not a new general
repository convention. No stage, commit, push, PR, CI, merge or goal action was
performed. Task 6 and Child 13 remain `in_progress`; the parent remains
`planning`. This is the required stop/report checkpoint before B1-E1.

### Task 6 B1-E1 ordinary operation and Verify focused closure (2026-08-02)

B1-E1 is `complete_checked` for Corrections 1--3 and 5 only. It closes exact
ordinary create/overwrite/skip operation identity and byte products,
create/overwrite source equality, skip source/target separation and the closed
Verify product. It grants no B1-E2/E3, B2, F4, whole Task 6, Child, delivery or
final-gate credit, and this milestone stops before B1-E2.

The permanent B1-E1 owner set is exactly:

```text
backend/internal/backupasset/recovery/executor.go
backend/internal/backupasset/recovery/contracts_test.go
backend/internal/backupasset/recovery/executor_test.go
```

`contracts.go` was changed only inside the controlled RED baseline and restored
byte-for-byte before GREEN. Its final SHA-256 remains
`5c2338ce250ffda7a4749ab1629af6eddcc80c23f7ad7ca4397fdb3b47ec3cb8`.

The frozen selector is `TestRecoveryVerifyOperationProductMatrix`. Before
removing any production behavior, its body covered:

- all nine invalid ordinary create/overwrite/skip digest and byte-sentinel
  combinations, plus valid round-trip and snapshot/job-item persistence;
- fresh create/overwrite materialized source digest and size equality before
  mutation;
- source revalidation before and after each operation;
- a skip fixture whose valid source digest/size are intentionally distinct from
  the frozen prior-target digest/bytes; and
- closed Verify presence arms, exact present digest/byte comparison, and bounded
  opaque revision rules.

Two controlled production baselines produced genuine RED. The first removed
ordinary operation product validation, source digest/size enforcement and exact
present Verify comparison. The unchanged selector then showed all nine invalid
ordinary digest/byte products being accepted, rejected the valid distinct skip
source as source drift, and accepted mismatching present digest/bytes. The
second baseline allowed skip to proceed while keeping the create/overwrite and
per-operation protections removed: create reached mutation arm, workspace
reservation, schema-use latch and remote directory preparation before source
rejection; overwrite wrote and checkpointed an earlier item before later source
rejection; and source drift allowed additional write/Verify work without the
required pre-operation revalidation. These are the credited RED observations.

An initial missing `GOTMPDIR` directory failure and an initial PostgreSQL
authentication failure were environment failures before the relevant assertion
and are explicitly not RED evidence.

The final implementation separates source and target identity:

- create/overwrite Catalog size and materialized digest must equal the persisted
  post bytes/digest;
- skip Catalog size must equal frozen `EstimatedBytes`, while its materialized
  source capability may carry any independently valid digest;
- skip target Verify still uses only frozen prior-target digest/bytes and can
  project only `skipped`;
- exact present Verify requires observed digest and bytes to equal expectation;
  and
- source revalidation remains on both sides of every ordinary operation.

Fresh post-wrapper-removal verification passed:

```text
TestRecoveryVerifyOperationProductMatrix                     PASS (0.226s)
full recovery package                                        PASS (11.135s)
frozen selector under -race -count=10                        PASS (6.183s)
provider/repository/recovery/model/database packages         PASS
go vet on the same five affected packages                    PASS
SQLite job-item operation and locator bindings matrix        PASS (0.109s)
required real PostgreSQL companion, no skip                  PASS (1.878s)
owned gofmt -d and git diff --check                          PASS
```

The real PostgreSQL run reused the pre-existing healthy `xirang-c13-pg`
PostgreSQL 18 container on loopback port 55470. Its credential was passed only
through the test-process environment and was not recorded in this evidence. The
container was not removed, restarted or otherwise reconfigured.

Final B1-E1 code/test SHA-256 values before the ledger-only edits are:

```text
contracts.go       5c2338ce250ffda7a4749ab1629af6eddcc80c23f7ad7ca4397fdb3b47ec3cb8
executor.go        23762c8e435a553d1e0da1dc346b8f03bef0e100003cd8530036e37bb6d913a9
contracts_test.go  5361219ffea41ac092c4899cffff5ef58fbaf95ee60e9dec5a2847afd8a3a606
executor_test.go   28c7d0142bddefd6c38f1e02fa986d018e42d14120b4758f5aa121f09161bfb6
```

After applying the ledger-only updates, both Child and parent `task.py validate`
commands exited zero. The Child retained 17 valid implement entries and 18
valid check entries; both task JSON files and both Child JSONL files parsed.
The exact manifest parser returned
`phase1=9 create=55 modify=81 total=145 unique=145 duplicates=0`.

The final dirty union remained 82 paths: 80 manifest members plus only the two
protected unrelated paths `go.mod` and
`recovery/testdata/rsync_local_to_remote.json`; outside-allow-list paths were
zero, and all three permanent B1-E1 paths were manifest members. The protected
SHA-256 values remained respectively
`b767fc9ef9376c651d0493329b710ac8dcf8d77e52686b847d244aff9f6d48fd`
and
`2570bd4472541322d902c4cdf2fe43f247b69f19d335558830e749567f763892`.
Staged paths were zero, `.git/index.lock` was absent, `000070+` migration paths
were zero, and branch/HEAD remained `codex/backup-assets-controlled-recovery`
at `51771654a85967656fe1ca69686590b734ff9214`.

The PostgreSQL container remained running and healthy. The exact B1-E1 scratch
directory `/dev/shm/xirang-b1e1-red.ByBaq9` was removed after verification;
no other container or scratch path was changed. `trellis-check` found no
remaining focused issue. No `.trellis/spec/` update was made because this is a
task-specific, unshipped Recovery contract already explicit in `design.md`, not
a general repository convention. No stage, commit, push, PR, CI, merge, branch,
worktree or goal action was performed. Task 6 and Child 13 remain
`in_progress`; the parent remains `planning`. This is the required stop before
B1-E2.

### Task 6 B1-E2 canonical locator and encrypted product focused closure (2026-08-02)

B1-E2 is `complete_checked` for Corrections 7--10 only. It closes canonical
schema-v2 locator mapping, preallocated workspace/distinct final-object binding,
row-bound recovery-local item AEAD and the versioned aggregate envelope. It
grants no B1-E3, B2, F4, whole Task 6, Child, delivery or final-gate credit and
stops before B1-E3.

The permanent B1-E2 product/test owner set is exactly:

```text
backend/internal/backupasset/recovery/contracts_test.go
backend/internal/backupasset/recovery/worker_test.go
```

`contracts.go` and `service.go` were changed only inside the controlled RED
baseline and restored byte-for-byte before GREEN. Their final SHA-256 values
remain respectively:

```text
contracts.go  5c2338ce250ffda7a4749ab1629af6eddcc80c23f7ad7ca4397fdb3b47ec3cb8
service.go    9cfd9f07178061297ff3be7cc7cc12f6f823a3c82ed5c8d9a5c7e6f8c1f00a57
```

The frozen selector set is exactly:

```text
TestRecoveryOperationSnapshotV2CanonicalLocatorMatrix
TestRecoveryOperationSnapshotV2WholeProductTamperMatrix
TestRecoveryTargetLocatorCiphertextBindingMatrix
TestRecoveryPrepareFirstWriteUsesPreallocatedWorkspaceMatrix
TestRecoveryLocatorProductNoPlaintextLeak
```

Before production behavior was removed, the expanded selectors covered:

- exact canonical locator plus mode/root-bound `SemanticTargetDigest` on every
  schema-v2 row, including delete, alias rejection, duplicate/collision denial
  and semantic/final-object domain separation;
- canonical snapshot round-trip, operation/source/policy/locator whole-product
  rebuild, self-consistent invalid-product rejection and generic encrypted
  aggregate persistence;
- canonical in-place/isolated locator envelopes, operation-fact tamper matrices,
  actual HKDF-SHA256/AES-256-GCM seal/open and every row/job/source/root/
  workspace/digest/operation/key/cipher field in the authenticated binding;
- exact preallocated `jobs/<jobID>` none-state persistence, generic-encrypted
  workspace at rest, distinct `jobs/<jobID>/<suffix>` final-object digest,
  recovery-local item ciphertext and exact locator open before the first remote
  reservation; and
- no item/workspace locator plaintext in the item JSON, ciphertext or sanitized
  decode failures.

The initial run against the inherited implementation was GREEN in `0.141s` and
is recorded only as coverage confirmation, not RED. One controlled production
baseline then removed exactly three tested behaviors: schema-v2 snapshot row
locator/semantic fields, `JobItemID` from the item AEAD binding digest, and the
preallocated workspace prefix from isolated final-object derivation. The
unchanged selector set produced genuine behavioral RED:

- canonical and amended snapshots failed to decode because the persisted
  locator product was missing;
- `recovery-local_AEAD/job_item_id` reported that the authenticated digest did
  not bind the row identity; and
- isolated execute preparation returned authorization denied because the final
  object no longer matched `jobs/<jobID>/<suffix>`.

There was no compilation, cache, authentication or fixture failure in this RED.
Restoring the three intended production behaviors returned both production
files to their exact baseline hashes; the unchanged selector then passed in
`0.136s`.

Fresh focused verification passed:

```text
five frozen selectors, unchanged after RED                         PASS (0.136s)
same five selectors under -race -count=10                         PASS (3.274s)
full recovery package                                              PASS (11.174s)
provider/repository/recovery/model/database packages              PASS
go vet on the same five affected packages                         PASS
SQLite model + operation-snapshot/job-item/locator companions     PASS
required real PostgreSQL three companion selectors, no skip       PASS (7.338s)
owned gofmt -d and git diff --check                                PASS
```

The real PostgreSQL run reused the pre-existing healthy `xirang-c13-pg`
PostgreSQL 18 container on loopback port 55470. Its credential was derived
inside the test-process shell and was not printed or recorded. The container
was not removed, restarted or reconfigured. The paired migration selectors are
supporting B1-E2 regression only; their immutable-row assertions do not grant
B1-E3/Correction 13 credit.

Final B1-E2 test SHA-256 values before the ledger-only edits are:

```text
contracts_test.go  5cc68e7e660cb9e0a5c57f43f7199b7f5b6dbc54874d1a6f208fd27254d8a763
worker_test.go     9db5ddc126abfbc1ff773ae48258ab1d5b5373b3b36df260e6333ac9d4683e08
```

The exact manifest parser returned
`phase1=9 create=55 modify=81 total=145 unique=145 duplicates=0`. The final dirty
union remained 82 paths: 80 manifest members plus only the protected unrelated
`go.mod` and `recovery/testdata/rsync_local_to_remote.json`; outside-allow-list
paths were zero. Their SHA-256 values remained respectively
`b767fc9ef9376c651d0493329b710ac8dcf8d77e52686b847d244aff9f6d48fd` and
`2570bd4472541322d902c4cdf2fe43f247b69f19d335558830e749567f763892`.
Staged paths were zero, `.git/index.lock` was absent, `000070+` migration paths
were zero, and branch/HEAD remained `codex/backup-assets-controlled-recovery`
at `51771654a85967656fe1ca69686590b734ff9214`.

The exact B1-E2 scratch directory `/dev/shm/xirang-b1e2-red.eZ7GDe` was removed
after verification; no other container or scratch path changed. `trellis-check`
found no remaining focused issue. No new `.trellis/spec/` update was made
because the behavior is a task-specific, unshipped Recovery contract already
explicit in `design.md`, not a new general repository convention. No stage,
commit, push, PR, CI, merge, branch, worktree, subagent, goal or heartbeat action
was performed. Task 6 and Child 13 remain `in_progress`; the parent remains
`planning`. This is the required stop before B1-E3.

### Task 6 B1-E3 durable adoption, cleanup-key and paired immutable closure (2026-08-02)

B1-E3 is `complete_checked` for Corrections 11--13 only. It closes the
three-boundary durable adoption product, bounded DB-only permanent cleanup-key
reconciliation, grant-first/exact-key prepared aggregate, deterministic
takeover winner and paired SQLite/PostgreSQL immutable enforcement. It does not
close the B1 aggregate and gives no credit to B2, F4, whole Task 6, Child
delivery or final gates. The next stop is before B2-E1.

The permanent B1-E3 delta is limited to:

```text
backend/internal/backupasset/recovery/service_test.go
backend/internal/backupasset/recovery/worker_test.go
backend/internal/database/backup_asset_migrations_integration_test.go
```

The frozen selector set remained unchanged throughout the controlled
RED/GREEN cycle:

```text
TestRecoveryExecutePreparedAggregateGrantFirstMatrix
TestRecoveryAdoptInterruptedOperationDurableDerivationMatrix
TestRecoveryPermanentCleanupKeyLossMatrix
TestRecoveryLocatorRaceTakeoverOneWinner
TestBackupAssetMigration069RecoveryLocatorProductSQLite
TestBackupAssetMigration069RecoveryLocatorProductPostgres
```

The inherited product first passed the expanded selectors and received only
coverage credit. Five narrow controlled baselines then produced genuine
behavioral RED:

- removing the durable adoption digest comparison accepted changed durable
  state instead of returning fence loss;
- selecting pre-arm cleanup candidates omitted the current post-arm attempt;
- omitting persisted grant `consumed_at` prevented the prepared aggregate from
  completing after the grant-first boundary;
- bypassing the exact transaction-scoped active-key comparison accepted a
  mismatched key; and
- removing `semantic_target_digest` from the paired immutable arm let SQLite
  and PostgreSQL accept an illegal field rewrite combined with an otherwise
  legal `pending -> failed` projection.

The controlled changes were limited to `recovery/service.go`,
`recovery/worker.go` and the paired `000069` up migrations. They were restored
byte-for-byte before final GREEN. Their final SHA-256 values are:

```text
worker.go          75e4c4a2beb421a6f76d6d9b752f7afb47cc034a6609d00dd0760b66e2798972
service.go         9cfd9f07178061297ff3be7cc7cc12f6f823a3c82ed5c8d9a5c7e6f8c1f00a57
sqlite 000069 up   f842be1eece0adcd1d84b9ec5e4216e96c67a4eca9b9308886be2b6ee97edad5
postgres 000069 up 42c92b3c4e6c56ac57d67cdf249746c682abe94efbe915182e54bb0f3ca8c67d
```

The permanent B1-E3 test SHA-256 values before ledger-only edits are:

```text
service_test.go                                  3078b9069ac3e5f54794f900ac7a34db7076a107c6cbc456b55d513255ccea01
worker_test.go                                   9335559d73b57403bb93c2da1c9bda26fa487dc12e5282069b08bde74eb075fd
backup_asset_migrations_integration_test.go      aad45fc1193e9102cdc5e885d97d3f982121676d6098cbb6ad2930008105217b
```

Focused verification before the ledger-only closure passed:

```text
local frozen selectors: recovery                              PASS (0.831s)
local frozen selector: database                               PASS (0.431s)
required real PostgreSQL focused companions, no skip:
  recovery                                                     PASS (0.801s)
  database                                                     PASS (7.069s)
recovery frozen selector set under -race -count=10            PASS (26.643s)
full recovery package                                         PASS (11.862s)
runtime permanent-cleanup-key startup selector                PASS (0.186s)
provider/repository/recovery/runtime/model/database packages  PASS
go vet on the same six packages                               PASS
SQLite 000069 full/paired/locator companions                  PASS (7.675s)
PostgreSQL locator/adoption/rollback/full-000069 companions:
  recovery                                                     PASS (1.682s)
  database                                                     PASS (141.058s)
owned gofmt -d                                                 PASS (no output)
```

Three B1-E3-owned lint findings were corrected: two nil-dereference ordering
findings in `service_test.go` and one unchecked `tx.AddError` return in
`worker_test.go`. Complete recovery-package lint still reports seven earlier
Task 6 findings in protected production files or inherited test helpers. This
closure therefore does not claim a whole-package `golangci-lint` pass.

After the ledger edits, the exact six frozen selectors were rerun from the
unchanged product tree:

```text
four recovery frozen selectors                                PASS (0.814s)
TestBackupAssetMigration069RecoveryLocatorProductSQLite       PASS (0.416s)
TestBackupAssetMigration069RecoveryLocatorProductPostgres     PASS (6.951s, required real PostgreSQL, no skip)
```

Both Child and parent `task.py validate` commands exited zero. The Child kept
17 valid implement entries and 18 valid check entries; both task JSON files
and both Child JSONL files parsed. `git diff --check` exited zero. The exact
manifest parser returned
`phase1=9 create=55 modify=81 total=145 unique=145 duplicates=0`.

The dirty union remained exactly 82 paths: 80 manifest members plus only the
two protected unrelated paths `go.mod` and
`recovery/testdata/rsync_local_to_remote.json`; outside-allow-list paths were
zero. All three permanent B1-E3 paths are manifest members. The protected
unrelated SHA-256 values remain:

```text
go.mod                                        b767fc9ef9376c651d0493329b710ac8dcf8d77e52686b847d244aff9f6d48fd
recovery/testdata/rsync_local_to_remote.json  2570bd4472541322d902c4cdf2fe43f247b69f19d335558830e749567f763892
```

Staged paths were zero, `.git/index.lock` was absent, and `000070+` migration
paths were zero. Branch/HEAD remained
`codex/backup-assets-controlled-recovery` at
`51771654a85967656fe1ca69686590b734ff9214`.

The required-real-PostgreSQL run reused the pre-existing healthy
`xirang-c13-pg` PostgreSQL 18 container at loopback port 55470. Its credential
was derived only inside the test-process shell and was not printed. The
container was not removed, restarted or reconfigured. The exact temporary
directory `/dev/shm/xirang-b1e3-baseline.R5W7xg` was removed after the final
selector run and scope checks; no other scratch path was changed.

`trellis-update-spec` required no shared `.trellis/spec/` edit because the
behavior is an unshipped, task-local Recovery contract already captured in
`design.md` section 19 and the frozen matrix. No stage, commit, push, PR, CI,
merge, branch, worktree, subagent, goal or heartbeat action was performed.
Task 6 and Child 13 remain `in_progress`; the parent remains `planning`. This
is the required stop before B2-E1.

## Task 6 B2-E1 Focused Closure Evidence (2026-08-02)

B2-E1 is `complete_checked` for Correction 4 plus its delete row only. The
permanent delta is limited to
`backend/internal/backupasset/recovery/contracts_test.go`. The unchanged set of
seventeen top-level frozen Task 6 selector names was preserved; the B2-E1
subset is exactly:

```text
TestRecoveryOperationSnapshotV2WholeProductTamperMatrix
TestRecoveryVerifyOperationProductMatrix
TestBackupAssetMigration069RecoveryLocatorProductSQLite
TestBackupAssetMigration069RecoveryLocatorProductPostgres
```

The support-only regressions were
`TestRecoveryAuthorizationReceiptExecuteDeleteRowHasNoPlanItem`,
`TestRecoveryExactMirrorDeletePauseRequiresOrdinaryExecutionHistory` and
`TestRecoveryExactMirrorSuccessfulDeleteProjectsAbsenceCheckpointAndChain`.
The last selector's inherited absence-chain assertions do not grant B2-E2 or
Correction 6 credit.

The inherited baseline passed the two Recovery selectors and three support
regressions (`0.291s`), SQLite locator-product (`0.382s`) and required real
PostgreSQL locator-product (`6.949s`). Two initial PostgreSQL invocations used
an obsolete `xirang` test-role credential and failed at SASL authentication
before migration setup; they are environment diagnostics, not product RED or
test evidence. The required passing run used the container's current
PostgreSQL owner credential without printing it.

The frozen selectors were strengthened in place to cover:

- delete only under durable `in_place + exact_mirror` policy;
- prior `present` with valid lowercase SHA-256, empty expected-post digest,
  expected prior/post bytes both `-1`, null plan item/source and no synthetic
  absence digest;
- explicit `AbsentObservation{Evidence: exact}` as the only successful absent
  arm; and
- permission denial, timeout, unsupported stat, transport failure, ambiguous
  missing and missing durable delete authority as fail-closed products.

The inherited implementation passed the strengthened selectors immediately;
that run is coverage confirmation only. Four controlled behavior removals then
produced genuine unchanged-selector RED:

1. `AbsentObservation.valid()` temporarily accepted any nonempty evidence.
   `TestRecoveryVerifyOperationProductMatrix` failed for
   `permission_denied` and for `ambiguous_missing` against an absent
   expectation.
2. The empty-delete-grant pause condition was temporarily bypassed.
   The same Verify selector failed because the exact-mirror execution lacked
   its durable `delete_authority_required` history.
3. Delete expected-post validation temporarily accepted a valid SHA-256 post
   digest. `TestRecoveryOperationSnapshotV2WholeProductTamperMatrix` failed at
   `delete_has_no_invented_absence_digest`.
4. The paired job-item insert trigger temporarily admitted every delete row,
   independent of parent job/plan mode. The unchanged SQLite and required-real-
   PostgreSQL locator-product selectors both failed because an invalid isolated
   delete row unexpectedly succeeded.

All controlled files were restored before final GREEN. Their exact SHA-256
values are:

```text
contracts.go       5c2338ce250ffda7a4749ab1629af6eddcc80c23f7ad7ca4397fdb3b47ec3cb8
executor.go        23762c8e435a553d1e0da1dc346b8f03bef0e100003cd8530036e37bb6d913a9
sqlite 000069 up   f842be1eece0adcd1d84b9ec5e4216e96c67a4eca9b9308886be2b6ee97edad5
postgres 000069 up 42c92b3c4e6c56ac57d67cdf249746c682abe94efbe915182e54bb0f3ca8c67d
```

The permanent `contracts_test.go` SHA-256 before ledger-only edits is
`0ed3e14ff3ea3c957a18ebc65b55d59e43ec81fdb9cb5b03d86f32bdbe0df47b`.
Focused verification before the ledger-only closure passed:

```text
frozen selectors plus B2-E1 support regressions              PASS (0.708s)
core frozen/support selectors under -race -count=10          PASS (17.784s)
full recovery package                                        PASS (12.022s)
SQLite locator-product companion                             PASS (0.436s)
required real PostgreSQL locator-product, no skip            PASS (7.216s)
recovery/database/model/runtime/backupasset/server packages  PASS
go vet on the same six packages                              PASS
owned gofmt -d                                                PASS (no output)
git diff --check                                             PASS (no output)
```

The complete recovery-package `golangci-lint` run still reports the same seven
earlier Task 6 findings: one `errcheck`, four `staticcheck` and two unused test
helpers in `executor.go`, `executor_test.go`, `worker.go` and `worker_test.go`.
No finding points to the B2-E1-owned `contracts_test.go` delta, so this closure
does not claim a whole-package lint pass.

The required PostgreSQL selector reused the healthy pre-existing
`xirang-c13-pg` PostgreSQL 18 container on loopback port 55470. The container
was not removed, restarted or reconfigured. No scratch directory, branch,
worktree, subagent, goal or heartbeat was created. B2 aggregate remains partial;
B2-E2, F4, whole Task 6 reviews/gates, Child delivery and every Git delivery
action remain open. The required stop is before B2-E2.

After the ledger-only edits, final unchanged-product verification passed:

```text
two frozen Recovery selectors plus three support regressions  PASS (0.582s)
TestBackupAssetMigration069RecoveryLocatorProductSQLite       PASS (0.402s)
TestBackupAssetMigration069RecoveryLocatorProductPostgres     PASS (7.001s, required real PostgreSQL, no skip)
```

Both Child and parent `task.py validate` commands exited zero. The Child kept
17 valid implement entries and 18 valid check entries; both task JSON files and
both Child JSONL files parsed. The exact manifest parser returned
`phase1=9 create=55 modify=81 total=145 unique=145 duplicates=0`; all 55 create
paths remain absent from HEAD and all 81 modify paths remain tracked in HEAD.
The final dirty union is 82 paths: 80 manifest members plus only `go.mod` and
`recovery/testdata/rsync_local_to_remote.json`; outside-allow-list paths are
zero. Staged paths are zero, `.git/index.lock` is absent, exactly four paired
`000069` migration files exist, and `000070+` remains absent. Branch, HEAD,
local `main` and live remote `main` remain at the recorded `5177165` baseline.

## Task 6 B2-E2 Focused Closure Evidence (2026-08-02)

B2-E2 is `complete_checked` for Correction 6 only. B2 aggregate remains
partial, and no F4, combined/whole Task 6, Child or delivery credit is granted.
The permanent delta is limited to:

```text
backend/internal/backupasset/recovery/contracts_test.go
backend/internal/backupasset/recovery/executor_test.go
backend/internal/backupasset/recovery/testutil_test.go
```

No top-level selector, product interface, model, table, migration, crypto
domain or manifest path was added. The unchanged set of seventeen top-level
frozen Task 6 selectors was preserved. The B2-E2 frozen selector is
`TestRecoveryVerifyOperationProductMatrix`; its support regressions are:

```text
TestRecoveryExactMirrorSuccessfulDeleteProjectsAbsenceCheckpointAndChain
TestRecoveryExactMirrorMultipleDeletesReuseConsumedSetAuthorityInSameExecution
TestRecoveryExactMirrorMultipleDeletesConsumeSetAuthorityOnceAcrossRestart
TestRecoveryExactMirrorConsumedDeleteAuthorityReloadReconcilesAbsence
```

The inherited baseline passed the frozen selector and four support regressions
in `0.752s`. The strengthened selector also passed immediately in `1.079s`,
which is coverage confirmation only. The permanent assertions bind:

- the literal `xirang/recovery/target-absence-chain/v1` domain and complete
  length-framed exact-absence inputs;
- exact delete call and job-item ordinal order;
- exactly one durable delete-set authority consumption and one operation
  checkpoint per delete;
- each delete's next revision as the following delete's prior revision and the
  terminal job chain as the last next revision;
- same-execution and post-restart reuse of the consumed checkpoint pair without
  another bearer, consumption or duplicate completed-item delete; and
- neutral B3 unresolved fields on successful delete checkpoints.

Two separate controlled behavior removals produced genuine unchanged-selector
RED:

1. `TargetAbsenceChainAdvance.NextRevision` temporarily used the ordinary
   present-target chain domain. The frozen selector failed its exact single-
   delete and same-execution multi-delete revision assertions (`0.743s`).
2. Interrupted-operation loading temporarily ignored a valid durable
   `delete_authority_required + delete_authority_consumed` pair. The same
   frozen selector failed the same-execution second delete, restart continuation
   and consumed-absence reconciliation with fence loss (`0.758s`).

Both controlled product files were restored before final GREEN. Their exact
SHA-256 values are:

```text
executor.go  23762c8e435a553d1e0da1dc346b8f03bef0e100003cd8530036e37bb6d913a9
worker.go    75e4c4a2beb421a6f76d6d9b752f7afb47cc034a6609d00dd0760b66e2798972
```

The production-`000069` PostgreSQL fixture then exposed a test-only clock
fault. Migration scheduler rows use PostgreSQL's subsecond
`CURRENT_TIMESTAMP`; the shared deterministic authorization fixture freezes
Go time at a whole second. When both fell in the same second, the correct
monotonic scheduler trigger rejected the fixture's earlier `updated_at` before
claim. The PostgreSQL migration fixture now reads that durable scheduler floor
and advances only its injected test clock just past it. No trigger or worker
behavior was relaxed. The original trigger rejection is fixture evidence, not
a product RED. The exact PostgreSQL subtest passed `-count=5` in `9.248s`, then
the full frozen selector passed in required mode without skip.

Permanent test-file SHA-256 values before these ledger-only edits are:

```text
contracts_test.go  02c633aff6a77c952e8d6beaa4dd3acd95db42171daf2591f4c59da3d95e9f54
executor_test.go   7699d40d610ab3cd3a275aada8bcdee2c388becda0391d423cd3931792fd5ae9
testutil_test.go   482a0fba26d78fc770ed4be97642cfa2d07ff59ea903b66d883b30d29320fb83
```

Focused verification before the ledger-only closure passed:

```text
frozen selector plus four support regressions                    PASS (1.204s)
same frozen/support set under -race -count=10                   PASS (31.893s)
required real PostgreSQL full frozen selector, no skip          PASS (2.799s)
full recovery package                                            PASS (12.486s)
database/model/runtime/backupasset/server packages               PASS
go vet on recovery plus those five packages                      PASS
owned gofmt -d                                                   PASS (no output)
```

Complete recovery-package `golangci-lint` still reports exactly the seven
earlier Task 6 findings: one `errcheck`, four `staticcheck` and two unused test
helpers in `executor.go`, `executor_test.go`, `worker.go` and `worker_test.go`.
No finding points to a B2-E2-owned line, so no whole-package lint pass is
claimed.

After the ledger-only edits, unchanged-product re-verification passed:

```text
frozen selector plus four support regressions                    PASS (1.196s)
required real PostgreSQL full frozen selector, no skip          PASS (2.695s)
```

Both Child and parent `task.py validate` commands exited zero. The Child keeps
17 valid implement entries and 18 valid check entries; the task JSON and both
JSONL files parse. The exact manifest remains
`phase1=9 create=55 modify=81 total=145 unique=145 duplicates=0`, with all
create/modify HEAD contracts valid. The dirty union remains 82 paths: 80
manifest members plus only protected `go.mod` and
`recovery/testdata/rsync_local_to_remote.json`; outside-allow-list paths are
zero. Staged paths are zero, `.git/index.lock` is absent, exactly four paired
`000069` files exist, and `000070+` is absent.

The protected unrelated SHA-256 values remain:

```text
go.mod                                        b767fc9ef9376c651d0493329b710ac8dcf8d77e52686b847d244aff9f6d48fd
recovery/testdata/rsync_local_to_remote.json  2570bd4472541322d902c4cdf2fe43f247b69f19d335558830e749567f763892
```

The healthy pre-existing `xirang-c13-pg` PostgreSQL 18 container on loopback
port 55470 was reused. Its credential was derived only inside the test shell
and not printed. The container was not restarted, reconfigured or removed. No
scratch directory, branch, worktree, subagent, goal or heartbeat was created.
No stage, commit, push, PR, CI, merge or delivery action was performed. Branch,
HEAD, local `main` and live remote `main` remain at `5177165`, and no remote
Child 13 branch exists. Task 6 and Child 13 remain `in_progress`; the parent
remains `planning`. The required stop is before Task-6-owned F4.

## Task 6 F4 Focused Closure Evidence (2026-08-02)

F4 is `complete_checked` at the Task-6-owned preallocated workspace,
reservation, immutable plaintext deadline and cleanup-only boundary. This gives
no B1/B2 aggregate, unchecked-row, whole Task 6, Child, delivery or Task 7
credit. The permanent delta is limited to:

```text
backend/internal/backupasset/recovery/executor_test.go
```

Its final SHA-256 is
`9964b2d1b02c2f6071c4b4d58614ff9af5975f5c046aaa29a246f4e3d5a900a2`.
The two new stable selectors are:

```text
TestRecoveryReviewF4WorkspaceDeadlineAndPublication
TestRecoveryReviewF4PartialWorkspaceCleanupOnly
```

They prove the following exact product:

- execute preallocates one `jobs/<opaque>` identity, persists its generic-
  encrypted locator plus immutable `WorkspaceBindingDigest` at `none`, and
  leaves marker/owner/fence/deadline empty;
- `PrepareFirstWrite` reuses the persisted identity and commits `reserved`,
  marker binding, owner/fence, the exact 24-hour deadline, reservation
  checkpoint and permanent latch before `CreateOwnedJobDir` or content bytes;
- successful execution stops at `sealed` for later Task 7 publication and
  creates no ResultSet/result row;
- an unexpected remote directory fails closed, and retry keeps the exact
  locator, workspace/marker bindings, deadline and single checkpoint;
- pre-arm failure and queued cancellation stay `workspace_phase=none`; and
- armed cancellation and post-arm unresolved outcome preserve the bounded
  workspace and enter `needs_attention|cleanup_due`, without publication.

The unchanged selectors observed two genuine controlled REDs. First, the
deadline calculation was temporarily changed to `24h - 1s`, and
`TestRecoveryReviewF4WorkspaceDeadlineAndPublication` failed its exact deadline
assertion. Second, the armed-cancellation `cleanup_due` projection was
temporarily removed, and
`TestRecoveryReviewF4PartialWorkspaceCleanupOnly/armed_cancellation_becomes_cleanup_only`
failed because the workspace remained illegally `reserved`. A missing `secure`
test import and one backend-relative package invocation made from the repository
root were compile/command diagnostics and are not RED evidence.

All temporary product changes were restored before final GREEN. Protected
product hashes are:

```text
worker.go    75e4c4a2beb421a6f76d6d9b752f7afb47cc034a6609d00dd0760b66e2798972
executor.go  23762c8e435a553d1e0da1dc346b8f03bef0e100003cd8530036e37bb6d913a9
```

Fresh post-GREEN verification returned:

```text
F4 Recovery normal selectors                              PASS (0.180s)
SQLite terminal-workspace/locator/deadline companions     PASS (0.945s)
F4 Recovery selectors under -race -count=10               PASS (6.020s)
full Recovery package                                     PASS (12.227s)
model package                                              PASS (0.050s)
database package                                           PASS (18.249s)
runtime package                                            PASS (6.185s)
backupasset package                                        PASS (0.571s)
server package                                             PASS (0.062s)
required real PostgreSQL three-companion set, no skip      PASS (17.749s)
same six-package go vet                                    PASS
executor_test.go gofmt -d                                  PASS (no output)
git diff --check                                           PASS (no output)
```

The required PostgreSQL set was exactly
`TestBackupAssetMigration069TerminalWorkspacePhasesPostgres`,
`TestBackupAssetMigration069RecoveryLocatorProductPostgres` and
`TestBackupAssetMigration069PublicationAndDeadlineIntegrityPostgres`. The first
invocation used a libpq keyword DSN although the fixture requires a PostgreSQL
URL, so it failed during fixture argument validation before connection or
migration setup. That is a harness diagnostic, not product failure, skip or RED.
The same unchanged selectors then passed with a URL DSN derived inside the test
shell from the healthy pre-existing `xirang-c13-pg` PostgreSQL 18 container on
loopback port 55470. Its credential was not printed, and the container was not
restarted, reconfigured or removed.

The complete Recovery `golangci-lint` invocation still reports exactly seven
earlier findings: one `errcheck`, four `staticcheck` and two unused test helpers
in `executor.go`, `executor_test.go`, `worker.go` and `worker_test.go`. None
points into the new F4 selector range. Whole-package lint therefore remains
open and is not claimed as passed.

Both Child and parent `task.py validate` commands exited zero. The Child retains
17 valid implement entries and 18 valid check entries; both task JSON files and
both Child JSONL streams parse. The final manifest/scope check returned:

```text
phase1=9 create=55 modify=81 total=145 unique=145 duplicates=0
dirty=82 manifest_dirty=80 protected_dirty=2 outside=0
```

All 55 create paths remain absent from HEAD, all 81 modify paths remain tracked
in HEAD, staged paths are zero, `.git/index.lock` is absent, exactly four paired
`000069` files exist and `000070+` is absent. Two initial read-only scope-wrapper
attempts produced no valid gate result: one was rejected before process start
because it included recursive temporary cleanup, and one used zsh's special
`path` variable and consequently hid `PATH`. Neither edited a file; the final
no-temporary-file parser above passed.

The protected unrelated hashes remain:

```text
go.mod                                        b767fc9ef9376c651d0493329b710ac8dcf8d77e52686b847d244aff9f6d48fd
recovery/testdata/rsync_local_to_remote.json  2570bd4472541322d902c4cdf2fe43f247b69f19d335558830e749567f763892
```

Branch, HEAD, local `main`, live `origin/main` and merge base remain
`codex/backup-assets-controlled-recovery` at
`51771654a85967656fe1ca69686590b734ff9214`. No stage, commit, push, PR, CI,
merge, branch, worktree, goal, subagent or heartbeat action was performed. Task
6 and Child 13 remain `in_progress`; the parent remains `planning`. The required
stop is before whole Task 6 specification review.

After the ledger-only edits, the unchanged F4 selector set passed once more:

```text
F4 Recovery selectors                         PASS (0.189s)
SQLite workspace/locator/deadline companions  PASS (0.967s)
required real PostgreSQL companions, no skip  PASS (17.435s)
```

## Task 6 Correction 19 Durable Marker-Validation Takeover Evidence (2026-08-03)

Correction 19 is `complete_checked` at its controller-approved marker-validation
takeover boundary. This does not complete whole Task 6, grant any Task 7 credit,
or authorize the separate consumed exact-mirror authority takeover correction.

The permanent Correction 19 product and regression delta is limited to:

```text
backend/internal/model/backup_asset_recovery.go
backend/internal/model/backup_asset_recovery_test.go
backend/internal/backupasset/recovery/worker.go
backend/internal/backupasset/recovery/worker_test.go
backend/internal/database/backup_asset_migrations_integration_test.go
backend/internal/database/migrations/sqlite/000069_backup_asset_recovery.up.sql
backend/internal/database/migrations/postgres/000069_backup_asset_recovery.up.sql
```

The frozen RED sequence was genuine and occurred before the corresponding
product changes:

1. Model reflection reported all three private marker-validation fields
   missing. The SQLite whole-Task-6 migration selector reported
   `workspace_marker_validation_attempt_id` missing, and the new Recovery
   selectors could not load the absent column.
2. After schema-only GREEN, the reserved takeover selector reached
   `prepare ordinary first write: recovery worker fence lost`, while the
   marker-created selector observed that the initial marker-validation product
   remained empty. Those failures isolated the missing runtime admission and
   atomic projection behavior.

One test-harness callback initially required `marker_created` for every write
and therefore rejected the normal second `writing` operation. Restricting that
assertion to the first write corrected the harness; it is not product RED
credit.

The minimal GREEN product adds the private
`workspace_marker_validation_attempt_id`,
`workspace_marker_validation_attempt_fence` and
`workspace_marker_validation_node_fence` tuple. It stays empty through
`none|reserved`, is assigned atomically with `reserved -> marker_created`, is
complete through `marker_created|writing|sealed|published`, and is retained as
either the wholly empty or wholly complete source product through cleanup.
SQLite and PostgreSQL enforce the same shape, assignment edge and immutability.

A fresh `reserved` takeover may use only a workspace permit to validate the
same immutable marker, then commits its current attempt/node tuple before item
I/O. Marker creator owner/fence provenance never changes. A different claim
that finds `marker_created` fails ordinary execution before workspace or item
mutation and must use interrupted-operation adoption. At `writing`, the tuple
is historical validation provenance only; Correction 18's current operation
checkpoint remains the continuation authority. The tuple is included in the
durable interrupted-operation digest.

Fresh post-GREEN verification returned:

```text
two Correction 19 Recovery selectors                         PASS (0.157s)
same selectors under -race -count=10                         PASS (3.675s)
full Recovery package                                        PASS (13.538s)
full Recovery package under -race                            PASS (35.611s)
model package                                                 PASS (0.098s)
database package                                              PASS (22.825s)
SQLite whole-Task-6 plus paired-file selectors               PASS (3.317s)
required real PostgreSQL whole-Task-6 selector, no skip      PASS (57.726s)
same-scope go vet                                             PASS
owned gofmt -d                                                PASS (no output)
git diff --check                                              PASS (no output)
```

The required PostgreSQL run used the healthy pre-existing PostgreSQL 18
`xirang-c13-pg` fixture on loopback port 55470 with
`REQUIRE_POSTGRES_RECOVERY_TEST=1`. Every workspace-phase subtest ran and the
selector did not skip. The container was not restarted, reconfigured or
removed.

The configured backend lint first found three earlier Provider implementation
findings: two `S1016` identical-structure conversions and one unused private
Rsync evidence-copy helper. The RED was reproduced by `make lint-backend`.
The behavior-neutral cleanup directly converts the two closed result types and
removes the unused helper. Provider tests passed in `1.112s`, Provider lint
reported `0 issues`, Provider vet passed, and the complete backend lint rerun
reported `0 issues`. The cleanup paths are already in the exact approved
manifest:

```text
backend/internal/backupasset/provider/restore.go
backend/internal/backupasset/provider/rsync_restore.go
```

Final SHA-256 values before this ledger-only append are:

```text
model/backup_asset_recovery.go                         99580077ccbf61de8c2d3f0cb0065fb1e760208f2bec2565f54c70b5e5422359
model/backup_asset_recovery_test.go                    9a7adcf632e2a062a49c4006e2f00cc82fd9b76369589bda19a172b04a6ff60c
recovery/worker.go                                     873fcc1e869883cdb26e801ee88ce7dcd4b1881cea9604d78894dc0a22f41f80
recovery/worker_test.go                                db8654f83471d97e46cb6a304724aa63851e5b1a4af0ceb9da5d281106cd2bed
database/backup_asset_migrations_integration_test.go   87f78b8e2adb502d158a20e53f818fe2e6a1ddc0768072bb36a0934ee61b3b81
sqlite/000069_backup_asset_recovery.up.sql             7cc29ff175572ebb151dbdb1ef5fe355c1e2724689e6146c10ee3b63c8d192cd
postgres/000069_backup_asset_recovery.up.sql           990f68f18ca1958704f503733c72dac6aa2fac1f22f9da5759ed7aef3ab1b0da
provider/restore.go                                    bc7e590fa3297efebad51419a946674da936e59f2fd2f9a2203b523448735356
provider/rsync_restore.go                              8fa59cc268f0b0b6b7253e20a45bbc089a8344dbf0a904353d6068b899d7fcf3
```

Child and parent `task.py validate` passed; the Child has 17 valid implement
and 18 valid check entries; both task JSON files and both Child JSONL streams
parse. The final exact-manifest parser, using `git status -uall` so untracked
directories are not collapsed, returned:

```text
phase1=9 create=55 modify=81 total=145 unique=145 duplicates=0
dirty=82 manifest_dirty=80 protected_dirty=2 outside=0
```

Two earlier read-only parser invocations produced no gate result: the first let
an outer shell expand the nested script, and the second used Git's default
collapsed untracked-directory view. Neither changed a file. The corrected
parser also proved all create/modify HEAD contracts, exactly four paired
`000069` files, no `000070|000071`, no index lock and staged-zero.

The two protected unrelated hashes remain:

```text
go.mod                                        b767fc9ef9376c651d0493329b710ac8dcf8d77e52686b847d244aff9f6d48fd
recovery/testdata/rsync_local_to_remote.json  2570bd4472541322d902c4cdf2fe43f247b69f19d335558830e749567f763892
```

Branch, HEAD, local `main` and `origin/main` remain
`codex/backup-assets-controlled-recovery` at
`51771654a85967656fe1ca69686590b734ff9214`. No stage, commit, push, PR, CI,
merge, branch, worktree, goal, subagent or heartbeat action was performed.
Task 6 and Child 13 remain `in_progress`; the parent remains `planning` and the
program remains 12/15.

The remaining whole-Task-6 blocker is separate: current
`validateConsumedOrdinaryDeleteGrantTx` still requires the immutable consumed
exact-mirror checkpoint pair and grant provenance to belong to the new takeover
claim. Correction 6 treats that pair as durable delete-set authority. Resolving
the mismatch requires a separately approved controlling amendment and its own
RED/GREEN; no such product change is included here.

## Task 6 Correction 20 Consumed Exact-Mirror Takeover Evidence (2026-08-03)

The user approved a bounded inline correction after Correction 19. No goal,
heartbeat, subagent, branch, worktree, stage, commit, push, PR, CI, merge or
Task 7 action was used. The controlling distinction is now explicit:

```text
historical authority = immutable required/consumed checkpoint tuple + consumed grant tuple
current authority    = live attempt/source/node/latch + latest current operation checkpoint
```

The new permanent selector is:

```text
TestRecoveryExactMirrorConsumedAuthorityFreshTakeoverRequiresAdoption
```

It consumes one grant for a two-delete set, injects a crash after the first
remote Delete and before its operation checkpoint, expires the original claim
and performs a fresh takeover. Ordinary execution under the fresh claim first
fails with zero new workspace/write/delete/verify calls and preserves the old
checkpoint/grant tuple. Exact-absence adoption must then append a current-claim
operation checkpoint, retain the current attempt while the second delete is
pending, and allow bearer-free continuation to execute that second delete once.
The historical grant remains consumed once and retains the original claim's
delete-attempt id and fences.

Before the product edit, the selector failed after all pre-adoption assertions
had passed:

```text
executor_test.go:4574: adopt consumed exact-mirror delete under fresh claim:
recovery worker fence lost
FAIL (3.1s command wall time)
```

This is the genuine RED. It was not a compile, migration, fixture or environment
failure. The root cause was the private
`validateConsumedOrdinaryDeleteGrantTx` comparison of the immutable historical
checkpoint/grant provenance to the later takeover claim.

The minimal GREEN removes the current claim from that private validator. The
required and consumed checkpoints must instead share the same valid historical
attempt id, positive attempt fence and positive node fence; the grant's
`delete_attempt_id`, `delete_attempt_fence` and `delete_node_fence` must match
that pair. All existing job/plan/checkpoint/delete-set/target-revision/binding,
expiry and consumed-time checks remain. The separate latest-current-checkpoint
condition in `currentFirstWritePermitPathTx` is unchanged, so ordinary execution
still cannot cross the target boundary before adoption.

Fresh verification returned:

```text
new selector GREEN                                           PASS (0.226s)
focused delete/adoption set                                  PASS (0.741s)
new selector -race -count=10                                 PASS (4.694s)
focused delete/adoption set -race                            PASS (3.292s)
full Recovery package                                        PASS (13.839s)
full Recovery package -race                                  PASS (36.038s)
model package                                                PASS (0.112s)
database package                                             PASS (22.054s)
required real PostgreSQL operation-product selector, no skip PASS (2.702s)
recovery go vet                                              PASS
full make lint-backend                                       PASS (0 issues)
owned gofmt -d                                               PASS (no output)
git diff --check                                             PASS (no output)
```

The real PostgreSQL run reused the healthy `xirang-c13-pg` PostgreSQL 18
fixture on loopback port 55470. Two attempts using the stale container
initialization password failed at authentication before PostgreSQL test setup;
a first temporary-role wrapper also failed before role creation because the
unset `POSTGRES_USER` default was not applied. Those are harness diagnostics,
not RED or product results. The passing run used a command-scoped random role
with only database `CONNECT` and `CREATE`; the command trap removed its owned
objects and role. Final read-only queries found zero `xirang_c20_%` roles and
zero `xirang_recovery_authorization_%` schemas. The container was not restarted,
reconfigured or removed, and no existing role password was changed.

Correction 20 product/test SHA-256 values before this ledger-only edit are:

```text
worker.go         b98764f97ddf89ccd4ca12d52d28fbf29dc112db995cee1dfdbc189b7457a0ec
executor.go       da353e2ef2e1322386bbf6f8f7665a02058eb36bd422729263cd0453aedb33f5
executor_test.go  f2bbbd0b1643b0b69be49c77944e145c8f35fb6ed601576606020c4764ec5f88
```

Child and parent `task.py validate` passed; the Child retains 17 valid
implement entries and 18 valid check entries. Both task JSON files and both
Child JSONL streams parse. The exact parser used
`git status --porcelain=v1 -z -uall` and returned:

```text
phase1=9 create=55 modify=81 total=145 unique=145 duplicates=0
dirty=82 manifest_dirty=80 protected_dirty=2 outside=0
```

All 55 create paths remain absent from HEAD, all 81 modify paths remain tracked
in HEAD, staged paths are zero, `.git/index.lock` is absent, exactly four paired
`000069` files exist and `000070|000071` remain absent. Correction 20 adds no
path, model, DDL, migration, Provider, Repository, runtime, API, frontend or
Task 7 change. Task 6 and Child 13 remain `in_progress`; the parent remains
`planning`. The next bounded step is a fresh whole Task 6 specification review,
then whole quality review and final gates before Task 7.

## Task 6 Whole Specification And Quality Closure (2026-08-03)

Task 6 is `complete_checked` at whole scope. This controller-inline review did
not use a goal, heartbeat, subagent, branch, worktree or Git delivery action.
It reviewed the complete current product rather than inheriting focused credit.
The PRD omission for Correction 20 was repaired, and the current whole-review
instruction now says twenty corrections rather than fourteen. Historical B3
wording that accurately describes the then-fourteenth correction was retained.

### Whole-product traceability

| Correction | Controlling product | Production boundary | Fresh proof |
|---|---|---|---|
| 1 | frozen post identity and byte facts | `contracts.go`, `service.go`, job-item model/DDL | operation snapshot tamper and Verify matrices |
| 2 | create/overwrite source equals frozen post product | `executor.go` source materialization/write path | Verify and per-operation source selectors |
| 3 | skip revalidates source and verifies unchanged target | `executor.go`, closed source/target products | Verify and per-operation source selectors |
| 4 | delete uses explicit exact absence, never a digest | `contracts.go`, `executor.go` delete observation | Verify exact-absence arms plus locator SQL selector |
| 5 | closed present/absent Verify and opaque revision | `target.go`, `contracts.go` | `TestRecoveryVerifyOperationProductMatrix` |
| 6 | separately domain-bound absence chain and ordered deletes | `executor.go`, checkpoint history | Verify/multi-delete and fresh-takeover selectors |
| 7 | canonical item locator plus semantic digest on every row | `contracts.go`, `service.go` | canonical-locator and whole-product-tamper selectors |
| 8 | preallocated job/item/workspace identity and persisted none state | `service.go`, `worker.go` | prepared-aggregate and preallocated-workspace selectors |
| 9 | row-bound recovery-local item-locator AEAD | `service.go`, `worker.go` | ciphertext-binding and no-plaintext selectors |
| 10 | generic `enc:v2` remains distinct from item AEAD | model/service encryption boundaries | ciphertext-binding and no-plaintext selectors |
| 11 | durable-only three-boundary adoption | `worker.go` | durable-derivation/adopter race selectors |
| 12 | fatal key-loss reconciliation and grant-first aggregate | `worker.go`, `service.go` | permanent-key-loss and prepared-aggregate selectors |
| 13 | paired immutable twelve-table `000069` product | model plus paired up/down migrations | SQLite/PostgreSQL recovery-locator and base selectors |
| 14 | terminal unresolved remote outcome | state/worker/model/paired DDL | five B3 selectors plus fresh whole-closure migration runs |
| 15 | post-arm call error with no trustworthy product | `executor.go`, unresolved projection/paired DDL | `TestRecoveryExecuteClaimPostArmCallErrorsBecomeUnresolved` |
| 16 | Provider source resolution and completed-operation evidence | `executor.go`, `worker.go`, existing resolver port | pinned-source and adoption-source selectors |
| 17 | durable `marker_created` before item I/O | `worker.go`, paired phase guards | marker-created and whole-closure migration selectors |
| 18 | immutable marker provenance and current-checkpoint continuation | `worker.go`, `executor.go` | later-isolated-operation adoption selector |
| 19 | private current marker-validation tuple | worker/model/paired DDL | two marker-takeover selectors plus whole-closure SQL |
| 20 | historical consumed-delete authority separated from current claim | `worker.go`, `executor.go` | consumed exact-mirror fresh-takeover selector |

F3 maps to the persistent scheduler, guarded pre-write drift transaction and
execute replay through its four frozen selectors. F4 maps only to preallocated
workspace, immutable deadline and cleanup-only classification through its two
Task-6-owned selectors; Task 7 publication/Content/revoking cleanup remains
unstarted. F6 maps to permanent use-latch plus live permit revalidation before
every Task-6 mutator. B1 now has fresh combined evidence for Corrections 1--3,
5 and 7--13; B2 has fresh combined evidence for Corrections 4 and 6; B3 keeps
its focused Correction 14 chronology and is incorporated by this whole review.

No Critical or Important specification or code-quality finding remains in this
Task 6 scope. Marker creator provenance, marker validation provenance, current
live fences, latest current operation checkpoint and historical consumed-delete
authority remain five separate products. The ownership split remains coherent:
Task 7 owns publication/content/revoking cleanup, and Task 8 owns managed
runtime/listener lifecycle.

Corrections 15--18 have code, stable selectors and prior RED/GREEN summaries in
`task.json`, but their exact historical RED output was not independently added
to this ledger. This review does not reconstruct that output or retroactively
check their historical empty boxes. Their present behavior is supported by the
fresh GREEN evidence below. Corrections 19 and 20 retain their exact earlier
ledger chronology.

### Fresh whole-scope evidence

```text
30 Recovery/fence/takeover selectors                         PASS (4.109s)
10 SQLite/base/whole/F3/F4/F6 migration selectors            PASS (13.608s)
5 model closed-product selectors                             PASS (0.004s)
Provider / Repository / runtime / backupasset packages       PASS (1.096s / 5.124s / 6.548s / 0.786s)
full Recovery package                                        PASS (14.313s)
full model / database packages                               PASS (0.099s / 24.115s)
15 focused Task 6 race selectors, count=10                   PASS (41.096s)
full Recovery package under race                             PASS (36.850s)
Provider / Repository / runtime packages under race          PASS (2.350s / 13.778s / 11.752s)
required real PostgreSQL Task 6 matrix                       PASS (329 pass events, 0 fail, 0 skip)
affected-package go vet                                      PASS
full make lint-backend                                       PASS (0 issues)
all dirty Go files gofmt -d                                  PASS (no output)
tracked git diff --check and untracked whitespace scan       PASS
```

The PostgreSQL matrix ran ten exact top-level selectors covering base `000069`,
Recovery locator product, unresolved outcome, whole Task 6 closure, F3, F6,
terminal workspace phases, publication/deadline integrity, pre-write source
drift and worker behavior. It used the healthy PostgreSQL 18.4
`xirang-c13-pg` fixture on loopback port 55470 with a command-scoped random
role granted only database `CONNECT` and `CREATE`. JSON events reported 329
test/subtest passes and no fail or skip. `DROP OWNED` and `DROP ROLE` ran after
the test; final counts were zero `xirang_c6w_%` roles and zero
`xirang_recovery_authorization_%` schemas. Two earlier wrapper attempts were
rejected before process creation by command construction/safety checks; they
created no role and are not test evidence.

The first dirty-Go format invocation was made from `backend/` with repo-root
paths and emitted only `lstat` diagnostics. It is not a gate result. The same
complete path set was rerun from the repository root with stdout/stderr required
empty and passed. Placeholder/privacy review classified every `any` match as a
Go built-in, GORM update map or comment; sensitive-word matches were generic
errors, domain-digest inputs or explicit no-leak test fixtures. No raw locator,
secret, proof, reason or command output path was found.

Both Child and parent Trellis validation passed. Child JSON/JSONL parsed with 17
implement and 18 check entries. Final scope reconciliation before the ledger
edit was:

```text
phase1=9 create=55 modify=81 total=145 unique=145
dirty=82 manifest_dirty=80 protected_dirty=2 outside=0 staged=0
paired_000069=4 future_000070_71=0 index_lock=0
```

All 55 create paths remain absent from HEAD, all 81 modify paths remain tracked
at HEAD, and the two protected unrelated hashes remain:

```text
go.mod                                        b767fc9ef9376c651d0493329b710ac8dcf8d77e52686b847d244aff9f6d48fd
recovery/testdata/rsync_local_to_remote.json  2570bd4472541322d902c4cdf2fe43f247b69f19d335558830e749567f763892
```

Branch, HEAD, local `main`, `origin/main`, merge base and live remote `main`
remain `codex/backup-assets-controlled-recovery` / `51771654a85967656fe1ca69686590b734ff9214`;
the matching remote feature branch is absent. No shared code-spec addition is
needed for Correction 20: it is an unshipped task-private Recovery fencing
product, while the reusable paired-`000069` and used-down admission rules are
already captured in `backend/database-guidelines.md`.

Task 6 is complete at whole scope, but the Trellis task and Child 13 remain
`in_progress`; the parent remains `planning` and program delivery remains 12/15.
The next step is Task 7. This closure does not start it and performs no stage,
commit, push, PR, CI, merge or delivery action.

## Task 7 Publication, Resolver And Content Adapter Focused Closure (2026-08-03)

Task 7 is now `in_progress`. Atomic isolated-result publication, the durable
owner-bound resolver, the Recovery-specific Content grant/source/audit arm and
the purpose-exact Target delivery adapter are `complete_checked` only at this
focused boundary. Retain, revoke, cleanup takeover/node-lease ordering,
crash/orphan reconciliation, both-engine lifecycle behavior and whole Task 7
review remain open. This run used no goal, heartbeat, subagent, branch,
worktree, stage, commit, push, PR, CI or merge action.

The adapter batch observed these genuine RED products before production fixes:

```text
missing delivery adapter API                         compile RED
missing owner on private Recovery source request     compile RED
post-open publication drift returned a live session behavior RED
Target open error leaked its returned reader         resource RED
shared-memory broker test DSN collided at -count=5   repetition RED
```

The minimal product now keeps the Content request owner-bound, resolves the
current terminal published ResultSet on each authorization/open/revalidation,
derives a domain-separated publication fingerprint without the private
locator, issues `recovery_result_read` Target permits, exposes stat and
sequential reads only, counts Provider bytes, and closes the reader on both
post-open durable drift and open-error paths. The adapter does not claim Target
Range capability. Content continues to own ticket deadlines, budgets, cookies,
heartbeat reauthorization, logout revocation, cache exclusion and redacted
Recovery audit projection.

The repeated-test failure was traced to `newBrokerTestHarness` using only
`t.Name()` for a named shared-memory SQLite database while prior GORM pools
remained alive in the same `go test -count=N` process. The existing repository
`atomic.Uint64` DSN pattern was applied to that fixture; the unchanged failing
race/repetition command then passed. The reusable rule was already present in
`backend/database-guidelines.md`, so no new shared spec text was needed.

Fresh verification after the final production edit returned:

```text
full Recovery and Content packages                   PASS (13.918s / 0.610s)
focused Recovery/Content race, count=5               PASS (9.265s / 1.861s)
affected-package go vet                              PASS
full make lint-backend                               PASS (0 issues)
owned gofmt -d                                       PASS (no output)
tracked git diff --check                             PASS (no output)
Child and parent task.py validate                    PASS (17/18 and 0/0 entries)
```

The fresh scope parser returned:

```text
phase1=9 create=55 modify=81 total=145 unique=145
dirty=92 manifest_dirty=90 protected_dirty=2 outside=0 staged=0
create_present_at_head=0 modify_missing_at_head=0 future_000070_71=0
```

The protected unrelated hashes remain unchanged, and Content's frozen
`lease.go` and `source_contracts.go` remain byte-for-byte at HEAD:

```text
go.mod                                        b767fc9ef9376c651d0493329b710ac8dcf8d77e52686b847d244aff9f6d48fd
recovery/testdata/rsync_local_to_remote.json  2570bd4472541322d902c4cdf2fe43f247b69f19d335558830e749567f763892
```

The next bounded Task 7 batch is retain/revoke admission followed by cleanup
claim and node-lease ordering. It must not inherit whole Task 7 credit from this
focused closure.

## Task 7 Retain And Published ResultSet Cleanup Claim Focused Closure (2026-08-03)

This bounded inline batch completes only two additional Task 7 products:
Admin/owner/fresh-purpose retain, and published ResultSet cleanup claim
admission. It does not implement unpublished-workspace admission, Content
revocation/drain, remote validation/delete/tombstone, node-lease renewal,
crash/orphan reconciliation or whole Task 7 cross-engine review. No goal,
heartbeat, subagent, branch, worktree, stage, commit, push, PR, CI or merge
action was used.

The retain service now locks the terminal published job and ready ResultSet,
requires `backup_assets:recover`, Admin, exact plan requester ownership, a
currently valid exact `recovery.result_retain` proof and the expected published
job revision. Its CAS can only move `plaintext_deadline` forward, never beyond
the immutable ResultSet hard deadline; expired plaintext and
`revoking|cleanup_failed|cleaned` rows cannot be revived. The job outcome and
revision, hard deadline, marker binding and cleanup products remain unchanged.

Cleanup claim first reads exactly one candidate ResultSet without a locking
clause. Its transaction then locks the owning terminal job, invokes the shared
`RecoveryNodeAdmission` node boundary, allocates a fresh monotonic node fence
and `recovery_cleanup` lease, and only then locks/CASes the ResultSet. Initial
`ready` and `cleanup_failed` claims enter `revoking|claimed`; an expired
`revoking` owner is replaced under fresh cleanup/node fences while preserving
the durable cleanup phase. Active owners, cleaned tombstones and busy nodes are
closed conflicts. A forced zero-row ResultSet CAS releases the newly inserted
node lease in the same committed transaction, leaving one `released` evidence
row and no active lease.

The exact TDD chronology retained these genuine RED observations:

```text
missing Retain request/method product                         compile RED
fixed-rejection Retain shell rejected the valid extension    behavior RED
authorization/conflict matrices returned the wrong sentinel  behavior RED
missing cleanup claim/dependency product                      compile RED
fixed-conflict cleanup shell never reached ResultSet CAS      behavior RED
cleanup_failed and expired revoking could not be claimed      behavior RED
busy node leaked task.ErrNodeWriteConflict                    behavior RED
```

The expired-plaintext test initially expired its proof at the same time and was
corrected before product credit so it isolates the intended deadline conflict.
The first lint run found only an impossible `int64 > math.MaxInt64` branch; the
exact comparison was corrected and the unchanged gates were rerun.

Fresh verification after the final production edit returned:

```text
focused RecoveryResult normal, count=5              PASS (4.560s)
focused RecoveryResult race, count=5                PASS (17.687s)
full Recovery and Content packages                  PASS (15.066s / 0.657s)
affected-package go vet                             PASS
full make lint-backend                              PASS (0 issues)
```

Scope reconciliation before this ledger edit returned:

```text
phase1=9 create=55 modify=81 total=145 unique=145
dirty=92 manifest_dirty=90 protected_dirty=2 outside=0 staged=0
create_present_at_head=0 modify_missing_at_head=0 future_000070_71=0
```

The protected unrelated files remain byte-for-byte unchanged at their recorded
SHA-256 values, and frozen Content `lease.go` and `source_contracts.go` remain
equal to HEAD. This focused closure adds no path, table, migration, route,
backfill or shared spec rule. Child 13 remains `in_progress`, its parent remains
`planning`, and program delivery remains 12/15.

## Task 7 Retained Deadline Delivery Review Remediation (2026-08-03)

Manual review of the preceding retain/claim batch found a cross-boundary
deadline defect before cleanup execution was started. Retain updated only the
ResultSet effective deadline, as required by the paired schema, while the
resolver still required that deadline to equal the job's immutable initial
workspace deadline. A successful retain therefore made the result immediately
unresolvable. Retain also rejected a second extension after the initial anchor
expired even when the already-retained ResultSet remained live.

The test-first selector produced the expected genuine RED:

```text
TestRecoveryResultRetainKeepsDeliveryAvailableBeyondInitialDeadline
resolve retained recovery result: recovery result unavailable
```

The minimal correction introduces one shared deadline-window predicate. The
job deadline remains the immutable pre-first-byte anchor; the ResultSet deadline
is the current effective delivery deadline and must remain in the future, no
earlier than the anchor and no later than the immutable hard deadline. Retain
and resolver now apply the same predicate, so an extension remains deliverable,
changes the existing publication fingerprint, and can be extended again within
the hard cap after the initial anchor passes.

Fresh verification after the production correction returned:

```text
focused retained-deadline regression                    PASS
focused RecoveryResult normal, count=5                  PASS (4.464s)
focused RecoveryResult race, count=5                    PASS (17.090s)
full Recovery and Content packages                      PASS (14.444s / 0.614s)
affected-package go vet                                 PASS
full make lint-backend                                  PASS (0 issues)
owned gofmt -d                                          PASS (no output)
```

This review remediation does not implement or claim unpublished workspace
admission, revoke/drain, target validation/delete/tombstone, lease renewal,
orphan reconciliation, cross-engine lifecycle review or whole Task 7 closure.

Post-ledger reconciliation returned:

```text
phase1=9 create=55 modify=81 total=145 unique=145
dirty=92 manifest_dirty=90 protected_dirty=2 outside=0 staged=0
create_present_at_head=0 modify_missing_at_head=0 future_000070_71=0
```

Both Trellis validators and all JSON/JSONL parses passed. The two protected
hashes and the frozen Content files remain unchanged from the preceding focused
closure.

## Task 7 Unpublished Workspace Cleanup Claim Focused Closure (2026-08-03)

This bounded Plan A batch completes only the unpublished `cleanup_due`
workspace claim product. It adds a separate seven-field cleanup tuple to the
existing recovery job aggregate, paired SQLite/PostgreSQL state guards, and one
claim path under the shared recovery cleanup node boundary. It does not change
Task 6 execution or marker provenance and does not implement revoke, drain,
renewal execution, target validation, remote delete, failure/tombstone
execution, orphan/runtime scheduling or whole Task 7 closure. No goal,
heartbeat, subagent, branch, worktree, stage, commit, push, PR, CI or merge
action was used.

The permanent TDD selectors first observed these genuine RED products:

```text
model contract could not find WorkspaceCleanupPhase
SQLite 000069 contract could not find workspace_cleanup_phase
workspace cleanup request, claim and service method were absent and did not compile
```

The minimal GREEN keeps marker-creation and marker-validation provenance
unchanged and adds only the private cleanup phase/owner/lease/fence/node/
attempt tuple. Neutral, active, retryable and tombstoned shapes and their
one-way transitions are enforced by paired `000069`. Claim performs an
unlocked candidate read, locks the job/workspace row, rejects any published or
invalid aggregate, acquires a fresh `recovery_cleanup` node lease/fence, then
CASes the complete cleanup snapshot. Initial and retryable claims enter
`claimed`; expired active takeover preserves its durable phase. A lost CAS
commits the fresh node lease as `released` and returns the closed cleanup
conflict.

The first complete real-PostgreSQL migration run exposed a distinct pristine-
down ordering RED after the feature behavior was already GREEN:

```text
cannot drop table backup_asset_recovery_node_leases because other objects depend on it
constraint backup_asset_recovery_jobs_workspace_cleanup_node_lease_fk
on table backup_asset_recovery_jobs depends on table backup_asset_recovery_node_leases
```

The focused `ApplyExactAggregateAndPristineDown` subtest reproduced SQLSTATE
`2BP01`. The minimal fix explicitly drops that named job-to-node-lease
constraint before dropping the node-lease table; it does not use `CASCADE` or
change used-down admission. The unchanged subtest then passed in `3.630s`, the
workspace PostgreSQL selector passed in `7.113s`, and the complete
`TestBackupAssetMigration069Postgres` matrix passed in `134.901s`.

Fresh verification after the final production edit returned:

```text
SQLite base/whole/workspace/paired selectors             PASS (10.676s)
private model tuple selector                              PASS (0.003s)
published + unpublished cleanup, count=5                 PASS (1.758s)
published + unpublished cleanup, race count=5            PASS (7.443s)
full Recovery / Content packages                         PASS (14.767s / 0.627s)
full model / database packages                           PASS (0.097s / 23.399s)
affected-package go vet                                  PASS
full make lint-backend                                   PASS (0 issues)
owned gofmt -d                                           PASS (no output)
git diff --check                                         PASS (no output)
```

The PostgreSQL checks reused the healthy PostgreSQL 18 `xirang-c13-pg`
fixture on loopback port 55470 with credentials derived inside the command and
not printed. Required mode was enabled and neither selector skipped. The
container was not restarted, reconfigured or removed.

Pre-ledger scope reconciliation returned:

```text
phase1=9 create=55 modify=81 total=145 unique=145 duplicates=0
dirty=92 manifest_dirty=90 protected_dirty=2 outside=0 staged=0
create_present_at_head=0 modify_missing_at_head=0 future_000070_71=0
```

The protected hashes remain unchanged, both Child and parent Trellis
validators passed, both task JSON files and the 17/18-entry Child JSONL streams
parsed, `.git/index.lock` was absent, and the live remote `main` remained
`51771654a85967656fe1ca69686590b734ff9214` with no remote feature branch.
Task 7 and Child 13 remain `in_progress`; the parent remains `planning` and the
program remains 12/15.

## Task 7 Resource-Scoped Revoke And Drain Focused Closure (2026-08-03)

This bounded inline batch advances published ResultSet and unpublished
workspace cleanup from their current fenced claims through durable `drained`.
It adds Recovery-job-scoped Content grant revocation, post-commit read
cancellation, a late-registration barrier, bounded read drain and exact
Content lease release. The lifecycle service renews the cleanup owner and
`recovery_cleanup` node lease under the fixed job -> node lease ->
ResultSet/workspace lock order before every phase boundary. Published Content
drain runs between two short transactions; unpublished cleanup has no Content
call. No target permit, credential/root/marker validation, remote delete,
failure/tombstone projection, terminal cleanup-lease release, orphan/runtime
scheduling or whole Task 7 credit is included.

The permanent TDD selectors first observed these genuine RED products:

```text
Content selector: Broker had no RevokeRecoveryResultGrantsTx,
                  CancelRecoveryResultReads or DrainRecoveryResult
Lifecycle selector: ResultLifecycleService had no published/workspace
                    revoke or drain methods and no Content lifecycle dependency
```

The unchanged Content selector then proved that a transaction can revoke only
one Recovery job while a second Recovery job and a backup-asset grant remain
active; rollback restores the selected grant. Only after commit does
cancellation block late read registration. Drain joins the selected blocking
read, requires terminal grants with zero persisted in-flight work, releases
only the selected retained Content lease and preserves unrelated bindings. A
canceled drain leaves the selected binding and lease retained for conservative
retry.

The lifecycle selector proved atomic Content revoke plus
`claimed -> revoked`, post-commit cancellation, pre-drain renewal, transaction-
free Content drain, a second re-lock/renewal and `revoked -> drained`. Injected
revoke failure rolls back both the Content-side transaction marker and phase/
lease renewal; injected drain failure commits only the renewed `revoked`
phase. Expired or mismatched cleanup/node fences make zero Content calls. A
fresh owner can take over while the old owner is inside external drain, after
which the old owner cannot advance the phase. The workspace path performs the
same sequential renewal and phases without a Content call and preserves all
Task 6 execution and marker-validation provenance.

Fresh verification after the final production edit returned:

```text
scoped Content normal selector                         PASS (0.083s)
scoped Recovery cleanup normal selector                PASS (0.625s)
combined Content regressions, count=5                  PASS (0.228s)
combined Recovery result/workspace regressions,count=5 PASS (6.671s)
scoped Content race, count=5                           PASS (1.681s)
scoped Recovery cleanup race, count=5                  PASS (10.687s)
full Content / Recovery packages                       PASS (0.636s / 14.710s)
affected-package go vet                                PASS
full make lint-backend                                 PASS (0 issues)
owned gofmt -d                                         PASS (no output)
git diff --check                                       PASS (no output)
```

Both Child and parent Trellis validators passed; the Child JSONL streams
parsed with 17 implementation and 18 check entries. Scope reconciliation
returned:

```text
phase1=9 create=55 modify=81 total=145 unique=145 duplicates=0
dirty=92 manifest_dirty=90 protected_dirty=2 outside=0 staged=0
create_present_at_head=0 modify_missing_at_head=0 future_000070_71=0
```

The protected local files remain at their recorded SHA-256 values:

```text
go.mod                                        b767fc9ef9376c651d0493329b710ac8dcf8d77e52686b847d244aff9f6d48fd
recovery/testdata/rsync_local_to_remote.json  2570bd4472541322d902c4cdf2fe43f247b69f19d335558830e749567f763892
```

Frozen Content `lease.go` and `source_contracts.go` remain byte-for-byte equal
to HEAD. No PostgreSQL rerun was required because this batch changes no model,
SQL or migration. Local HEAD, `main`, `origin/main` and live remote `main`
remain `51771654a85967656fe1ca69686590b734ff9214`; no remote feature branch,
stage, commit, push, PR, CI or merge action exists. No goal, heartbeat,
subagent, branch or worktree action was used. Task 7 and Child 13 remain
`in_progress`; the parent remains `planning` and the program remains 12/15.

## Task 7 Cleanup Target Validation Focused Closure (2026-08-03)

This bounded inline batch advances published ResultSet and unpublished
workspace cleanup from durable `drained` to durable `validated` through one
read-only target observation. Cleanup now has its own immutable, resource-
scoped permit and closed `ValidateOwnedJobDir` request/result boundary; the
execution mutation permit is write-only again. The lifecycle performs two short
transactions around exactly one target call, renews the resource and exact
`recovery_cleanup` node lease together, and retains the successful owner/fences
for the separately approved delete batch. The recording target observed zero
`RemoveOwnedJobDir` calls in every success, drift, takeover and failure arm.

Still-current validation failures use a five-second bounded detached context to
atomically release the exact node lease and project an ownerless retry product:
published rows become `cleanup_failed + drained`, while unpublished workspaces
remain `cleanup_due + drained`. Cleanup fence/attempt history is retained. A
fresh owner increments cleanup and node fences, resumes the exact durable phase,
and can validate without repeating Content revoke/cancel/drain. Lost failure-
projection CAS rolls back lease release and returns the closed cleanup conflict.
The paired `000069` guard now admits only neutral zero-history `claimed` or an
exact non-tombstoned positive-history phase; it rejects every rewind or advance.

The permanent TDD chronology retained these genuine RED observations:

```text
R5: cleanup resource/operation/request/observation types and the closed
    TargetPort observation method were absent                              RED
R6: ResultLifecycleService had no Target dependency or validation methods RED
R7 lifecycle: failed validation retained active authority and fresh retry
              could not resume drained                                    RED
R7 SQLite: positive-history ownerless retry incorrectly accepted claimed  RED
```

Continuation resumed while G7 intentionally did not compile because the two
failure-projection helpers were still absent. The unchanged R7 selectors then
passed after the minimal product implementation. A quality-check strengthening
initially canceled the caller before the first transaction and correctly
returned `context.Canceled`; that invalid harness was not counted as a product
RED. Cancellation was moved to the target observation boundary, after the first
transaction committed, and the unchanged production code then proved the
detached failure projection with only the sanitized validation sentinel.

Fresh verification after the final test edit returned:

```text
R7 exact lifecycle selector                                PASS (0.773s)
complete focused Recovery matrix                           PASS (1.168s)
SQLite workspace guard + paired-file selector              PASS (0.486s)
stateful cleanup validation race, count=5                  PASS (16.751s)
full Recovery / Content packages                           PASS (16.406s / 0.743s)
full Recovery package race                                 PASS (43.454s)
full database package                                      PASS (22.480s)
required real PostgreSQL workspace + full 000069 matrix    PASS (141.597s, no skip)
affected-package go vet                                    PASS
full make lint-backend                                     PASS (0 issues)
owned gofmt / whitespace / merge-marker scans              PASS (no output)
git diff --check                                           PASS (no output)
```

The PostgreSQL checks reused the running PostgreSQL 18 `xirang-c13-pg` fixture
on loopback port 55470. Credentials were derived only inside the command and
were not printed; required mode was set, and the container was not restarted,
reconfigured or removed.

Pre-ledger scope reconciliation returned:

```text
phase1=9 create=55 modify=81 total=145 unique=145 duplicates=0
dirty=92 manifest_dirty=90 protected_dirty=2 outside=0 staged=0
create_present_at_head=0 modify_missing_at_head=0 future_000070_71=0
paired_000069=4 index_lock=0
```

Both Trellis validators and all JSON/JSONL parses passed. The protected hashes,
frozen Content files, local HEAD/main/origin-main baseline and staged-zero
contract remain unchanged. No goal, heartbeat, subagent, branch, worktree,
stage, commit, push, PR, CI or merge action was used. This is focused
`validated` credit only: production root registration and locator population,
marker codec interoperability, concrete target wiring, `delete_started`, remote
delete, `deleted|tombstoned|cleaned`, successful terminal lease release,
orphan/quarantine scheduling and whole Task 7 review remain open. Task 7 and
Child 13 remain `in_progress`; the parent remains `planning` and the program
remains 12/15.

## Task 7 Production Target-Root Registry And Encrypted Plan Snapshot Focused Closure (2026-08-04)

This bounded Section 16 batch is `complete_checked` only for the private
production target-root registry and the transaction-bound immutable locator
snapshot in newly created Recovery plans. It does not implement or claim the
marker codec, SSH/SFTP or runtime target adapter, remote-root mutation,
`RemoveOwnedJobDir`, `delete_started`, `deleted|tombstoned|cleaned`, orphan
reconciliation, Task 8/9, whole Task 7 review or Git delivery.

The permanent R8 tests first produced the intended missing-product RED: the
typed private registry definitions, exact transaction methods and dynamic
internal-key classification did not exist. The R9 tests then produced the
intended missing-product/behavior RED: `PlanService` had no required target-root
resolver and a new plan did not resolve and persist the encrypted locator
snapshot. Fixture-only compilation or unrelated environment failures were not
counted as product RED.

The minimal product implementation is limited to five paths:

```text
backend/internal/settings/service.go
backend/internal/settings/service_test.go
backend/internal/api/handlers/config_handler_test.go
backend/internal/backupasset/recovery/service.go
backend/internal/backupasset/recovery/service_test.go
```

The settings service now owns private per-node/per-root v1 keys whose values are
current `enc:v2:` ciphertext. It strictly validates the root ID, safe label,
canonical absolute POSIX locator and bounded exact JSON document; registration,
rotation, deletion and resolution use exact caller-owned transactions. Generic
settings and both config export modes neither enumerate nor decrypt the rows,
and config import rejects them. Public summaries exclude locator and digest;
the resolution-only locator and digest fields use `json:"-"`.

For a new plan, `PlanService` resolves the exact node/root inside its existing
transaction, recomputes the locator digest and persists the locator through the
model's existing encryption hook. Idempotent replay validates the persisted
snapshot and never consults or repairs from the current registry. Rotation and
deletion therefore do not rewrite an old plan, while a new intent can capture
only one complete old or new tuple.

V4 full-race verification found one genuine test-fake race: concurrent locator
adopters shared unsynchronized resolver refs and source lifecycle counters. The
test-only GREEN synchronizes those mutable fakes in the already-manifested
`executor_test.go` and `worker_test.go`; no production behavior changed. The
later required PostgreSQL gate exposed a separate deterministic test assertion
mismatch. Its PostgreSQL fixture has one job item, so adopting the sole item
correctly follows normal terminalization; only the default three-item SQLite
fixture retains a running continuation attempt. The corrected PostgreSQL test
freezes `itemCount=1`, `pending=0`, completed attempt, `succeeded|sealed` job and
post-terminal rewrite rejection. Its original assertion failed five of five
runs, the corrected selector passed five of five, and both multi-item SQLite
continuation regressions passed five of five.

Fresh final verification returned:

```text
combined R8/R9/config focused matrix                         PASS
registry + replay + rotation race, count=5                  PASS
locator takeover synchronized-fake race, count=10           PASS
full settings / API handlers                                PASS
required PostgreSQL full Recovery                           PASS (978 pass, 0 fail, 0 skip)
required PostgreSQL full Recovery race                      PASS (978 pass, 0 fail, 0 skip)
affected-package go vet                                     PASS
full make lint-backend                                      PASS (0 issues)
owned gofmt / whitespace / merge-marker scans               PASS (no output)
git diff --check                                            PASS (no output)
```

The recognizable test-only locator/label checks and captured JSON/error checks
found no private locator, ciphertext or raw underlying error in a public result,
error, log, audit or config export. Source inspection confirmed no public
`SettingDef`, cache entry or bootstrap v1 allowlist entry for a dynamic root
key, and no production config-handler change was needed.

Both Child and parent Trellis validators passed; the Child JSONL streams retain
17 implementation and 18 check entries, and both task JSON files plus both
streams parse. Final scope reconciliation returned:

```text
phase1=9 create=55 modify=81 total=145 unique=145 duplicates=0
dirty=93 manifest_dirty=91 protected_dirty=2 outside=0 staged=0
create_present_at_head=0 modify_missing_at_head=0 future_000070_71=0
paired_000069=4 index_lock=0
```

The two protected unrelated paths remain byte-for-byte at their recorded
SHA-256 values:

```text
go.mod                                        b767fc9ef9376c651d0493329b710ac8dcf8d77e52686b847d244aff9f6d48fd
recovery/testdata/rsync_local_to_remote.json  2570bd4472541322d902c4cdf2fe43f247b69f19d335558830e749567f763892
```

The required PostgreSQL runs reused the healthy PostgreSQL 18
`xirang-c13-pg` fixture on loopback port 55470. Credentials were derived only
inside each command and were not printed; required mode was set, and the
container was not restarted, reconfigured or removed. Test schemas were
cleaned by their fixtures.

Branch, local HEAD, local `main`, cached `origin/main` and live remote `main`
remain `51771654a85967656fe1ca69686590b734ff9214`; the live remote has no
`codex/backup-assets-controlled-recovery` branch. The active main worktree and
nine pre-existing detached Codex worktrees were inspected but not changed or
created. The ignored `backend/server` binary predates this batch (2026-07-19)
and was not regenerated. No goal, heartbeat, subagent, branch switch, worktree,
stage, commit, push, PR, CI or merge action was used. Task 7 and Child 13 remain
`in_progress`; the parent remains `planning` and program delivery remains 12/15.

## Task 7 Recovery Workspace Marker Codec Focused Closure (2026-08-04)

This bounded Section 17 batch is `focused_complete_checked` only for the strict
authenticated private workspace-marker codec and immutable creator/fence
interoperability across the existing workspace-create and cleanup-validation
contracts. It does not select a remote marker filename, open SSH/SFTP, create a
remote directory, write or read a remote marker, invoke `RemoveOwnedJobDir`,
enter `delete_started|deleted|tombstoned|cleaned`, compose runtime wiring, or
implement orphan/quarantine handling.

The permanent R10 selectors first produced the intended missing-product RED:
`newRecoveryWorkspaceMarkerCodec`, the bounded marker/error products and the
creator-bound request/permit fields did not exist. The permanent R11 selectors
then produced the intended contract RED: Worker create requests carried empty
creator/fence facts, while published and unpublished cleanup validation failed
before the recording target because creator provenance was absent. Fixture-only
or unrelated failures were not counted as product RED.

The minimal product implementation is limited to three existing paths:

```text
backend/internal/backupasset/recovery/target.go
backend/internal/backupasset/recovery/worker.go
backend/internal/backupasset/recovery/result_lifecycle.go
```

Focused tests remain in the three colocated existing test paths. `target.go`
now owns an exact nine-field, 2048-byte-bounded JSON document, a 32-byte CSPRNG
nonce encoded as canonical unpadded base64url, exact-version key lookup and two
separate HMAC-SHA256 domains:

```text
xirang/recovery/workspace-marker-installation/v1
xirang/recovery/workspace-marker-document/v1
```

The decoder rejects empty/oversized input, unknown, duplicate or missing fields,
trailing JSON, noncanonical typed values and every authenticated substitution.
Digest and tag comparisons are constant-time. Permit mismatches collapse to
`ErrInvalidTargetPermit`; malformed or unauthenticated documents collapse to
`ErrInvalidRecoveryWorkspaceMarker`; key and entropy failures collapse to
`ErrRecoveryWorkspaceMarkerUnavailable`; cancellation and deadline identities
are preserved. No codec path logs or returns key, nonce, marker, creator or
private locator material.

Worker initial reservation and reserved takeover now populate creation requests
from immutable `workspace_owner/workspace_fence`, not from the current attempt.
Marker-created confirmation rejects creator substitution. Published ResultSet
and unpublished workspace cleanup carry that same tuple through permit proof,
request issuance and the closing binding comparison without adding a column,
query, transaction or target call. A later test-only strengthening independently
froze both exact HMAC-domain outputs and proved that envelope metadata rewrap
with unchanged key bytes preserves installation identity.

Fresh final verification returned:

```text
combined focused marker/permit/worker/cleanup selector          PASS
focused stateful race, count=5                                  PASS
full Recovery package                                           PASS
full Recovery package race                                      PASS
required real PostgreSQL full Recovery                          PASS (1008 pass, 0 fail, 0 skip)
required real PostgreSQL full Recovery race                     PASS (1008 pass, 0 fail, 0 skip)
go vet ./internal/backupasset/recovery                           PASS
make lint-backend                                                PASS (0 issues)
owned gofmt -d                                                   PASS (no output)
whitespace and merge-marker scans                               PASS (no output)
git diff --check                                                 PASS (no output)
```

Both Child and parent Trellis validators passed. Both task JSON files and the
Child JSONL streams parse; the streams retain 17 implementation and 18 check
entries. Scope reconciliation returned:

```text
phase1=9 create=55 modify=81 total=145 unique=145 duplicates=0
dirty=93 manifest_dirty=91 protected_dirty=2 outside=0 staged=0
create_present_at_head=0 modify_missing_at_head=0 future_000070_71=0
```

The two protected unrelated paths remain byte-for-byte at their recorded
SHA-256 values:

```text
go.mod                                        b767fc9ef9376c651d0493329b710ac8dcf8d77e52686b847d244aff9f6d48fd
recovery/testdata/rsync_local_to_remote.json  2570bd4472541322d902c4cdf2fe43f247b69f19d335558830e749567f763892
```

The required PostgreSQL checks reused the healthy PostgreSQL 18
`xirang-c13-pg` fixture. Credentials were derived only inside the commands and
were not printed; required mode was set, the fixture was not restarted,
reconfigured or removed, and test schemas were cleaned by the tests.

No shared `.trellis/spec/` update is required: this is an unpublished,
task-local document format already frozen by design section 37, not a new
general project convention. Local HEAD, local `main`, cached `origin/main` and
live remote `main` remain `51771654a85967656fe1ca69686590b734ff9214`, and no
remote feature branch exists. No goal, heartbeat, subagent, branch switch,
worktree, stage, commit, push, PR, CI or merge action was used. Task 7 and Child
13 remain `in_progress`; the parent remains `planning` and program delivery
remains 12/15. The next open product boundary is the concrete SSH/SFTP target
adapter and fixed remote marker I/O, which was not started in this batch.

## Task 7 A1 Exact-Plan SFTP Workspace Marker Focused Closure (2026-08-04)

This bounded Section 18 batch is `focused_complete_checked` only for the
immutable executed-plan session capability and the real SFTP implementation of
`CreateOwnedJobDir` plus observational `ValidateOwnedJobDir`. It does not
implement or claim `ProbeRoot`, `Lstat`, `CreateDirectory`, `WriteAtomic`,
`Delete`, `Verify`, `OpenOwnedResult` or `RemoveOwnedJobDir`. It performs no A2
payload operation, A3 deletion/tombstone/terminal cleanup transition,
runtime/main composition, orphan/quarantine work or Git delivery.

### Genuine RED And Minimal GREEN

R12 first proved that the package had no exact locked-plan session binding in
write or cleanup proof authority. Worker and lifecycle calls could not carry or
reconstruct the hook-decrypted plan snapshot. GREEN added the private
`recoveryTargetSessionBinding`, sealed its length-framed digest into write and
cleanup proofs, constructed it only from locked executed plans and exact-
compared it again in the cleanup closing transaction. Registry rotation or
deletion cannot redirect an existing plan, and no A1 call reads the current
target-root registry.

R13 first proved that no concrete SFTP target, exact node-session resolver,
purpose-selecting factory or stable target-unavailable product existed. GREEN
added a narrow resolver and real `sshutil.NodeDialer` factory whose only A1
purpose paths are `recovery_write` and `recovery_cleanup`. SFTP construction
failure closes SSH; successful sessions close SFTP before SSH exactly once;
cancellation closes and joins the session watcher. Every non-A1 method returns
`ErrRecoveryTargetUnavailable` before resolver or session use.

R14 first proved that concrete marker creation remained closed-unavailable and
the codec lacked authenticated `ValidateForCreate` replay. GREEN added the
fixed `.xirang-recovery-owner-v1` namespace, exact `jobs/<job>` binding,
canonical prefix/component checks, `0700` directories and the exclusive
SHA-256 temp protocol: `0600` verification, complete short-write handling,
mandatory `Sync`, close/reopen bounded comparison, standard non-overwrite
`Rename`, final authentication and stable observation revision. Only the exact
temp exclusively created by the current call may be best-effort removed.

R15 first proved that concrete cleanup validation remained unavailable and
could not return the same observation revision. GREEN added a mutation-free
cleanup-purpose path with a 2049-byte read bound, pre/open/post stat parity,
repeated canonical checks and strict `ValidateForCleanup` authentication.

Two additional product defects were found by genuine failing regression tests
during review rather than inferred after GREEN:

1. `marker disappears before open` and `marker disappears after read` returned
   `ErrRecoveryTargetUnavailable`; both tests required
   `ErrRecoveryTargetChanged`. Missing-file results in those replacement
   windows now map to target drift, while other I/O failures remain sanitized
   unavailable.
2. `temp wrong mode is rejected before marker write` observed marker bytes
   reaching a wrong-mode temporary file. The implementation now validates the
   exclusively created temp as a canonical regular `0600` file immediately
   after `Chmod` and before the first marker byte. The unchanged regression
   rejects the path with `ErrRecoveryTargetChanged` and proves zero marker
   writes.

Whole-package verification also exposed invalid shared fixtures rather than a
production defect. The base fixture now derives its locator digest with
`settings.RecoveryTargetRootLocatorDigest`; multi-job worker clones rebind the
plan, preflight and job root tuple after changing `target_node_id`, using
`UpdateColumn(s)` so scheduler timestamps do not drift. Production binding
validation was not relaxed.

Permanent product ownership stayed within:

```text
backend/internal/backupasset/recovery/target.go
backend/internal/backupasset/recovery/worker.go
backend/internal/backupasset/recovery/result_lifecycle.go
```

The focused and fixture tests stayed in the already manifested
`target_test.go`, `worker_test.go`, `result_lifecycle_test.go` and
`testutil_test.go` paths. No model, DDL, migration, settings implementation,
sshutil implementation, runtime/main, API, Provider, Repository, frontend,
`executor.go`, `go.mod` or root-level `recovery/` product edit belongs to A1.

### Verification And Review

The executed A1 gates returned:

```text
combined A1 normal selectors                              PASS (0.352s)
stateful SFTP race, count=5                               PASS (1.828s)
V6 focused normal                                         PASS (1.259s)
V6 focused race, count=5                                  PASS (20.956s)
full Recovery normal                                      PASS (15.920s)
full Recovery race                                        PASS (65.615s)
required PostgreSQL full Recovery normal                  PASS (22.539s, no skip)
required PostgreSQL full Recovery race                    PASS (53.117s, no skip)
go vet ./internal/backupasset/recovery                    PASS
make lint-backend                                         PASS (0 issues)
owned gofmt / whitespace / merge-marker / privacy scans  PASS (no output)
git diff --check                                          PASS (no output)
```

Controller-inline review reconciled every design 38.1--38.8 row with the
current implementation and found no open Critical or Important issue. The
Section 18 placeholder scan returned no match; the plan names and signatures
for the private binding, node resolver, session factory, concrete target and
`ValidateForCreate` all match the product. Static scans found no
`ResolveRecoveryTargetRootTx`, `ListRecoveryTargetRoots`, `PosixRename` or
`filepath` use in `target.go`. Product code contains no
`RemoveOwnedJobDir` call, and the closed-method test proves all eight non-A1
methods open zero sessions.

Final structural reconciliation returned:

```text
phase1=9 create=55 modify=81 total=145 unique=145 duplicates=0
dirty=93 manifest_dirty=91 protected_dirty=2 manifest_clean=54 staged=0
dirty_manifest_breakdown=9_phase1+36_create+46_modify
create_present_at_head=0 modify_missing_at_head=0 future_000070_71=0
HEAD=main=origin/main=51771654a85967656fe1ca69686590b734ff9214
```

The only dirty paths outside the exact manifest remain the two preserved
unrelated paths, unchanged at their recorded SHA-256 values:

```text
go.mod                                        b767fc9ef9376c651d0493329b710ac8dcf8d77e52686b847d244aff9f6d48fd
recovery/testdata/rsync_local_to_remote.json  2570bd4472541322d902c4cdf2fe43f247b69f19d335558830e749567f763892
```

No shared `.trellis/spec/` update is required. Both defects are specific to the
unpublished A1 protocol, are already explicit in design sections 38.4--38.7 and
are permanently enforced by the new regression tests. Task 7 and Child 13 stay
`in_progress`; the parent stays `planning`, and program delivery stays 12/15.
No goal, heartbeat, subagent, branch switch, worktree, stage, commit, push, PR,
CI or merge action was used. A2 requires separate approval.

After the A1 ledger update, the fresh stop-point recheck returned:

```text
V6 focused normal                                      PASS (1.458s)
V6 focused race, count=5                               PASS (21.827s)
full Recovery normal                                   PASS (16.794s)
go vet ./internal/backupasset/recovery                 PASS
make lint-backend                                      PASS (0 issues)
gofmt / whitespace / merge-marker / privacy / scope   PASS (no output)
Child / parent Trellis validation                      PASS (17/18 and 0/0)
JSON / JSONL parsing                                   PASS
exact manifest / protected hashes / live remote main  PASS
git diff --check / staged paths                        PASS / 0
```

The live remote read showed only `refs/heads/main` at the frozen baseline and
no `refs/heads/codex/backup-assets-controlled-recovery` row. This recheck did
not fetch, change refs, stage files or perform a delivery action.

## Task 7 A2a Exact-Plan Regular-File Verify Evidence

### R16 Sealed Verify Permit RED

At `2026-08-04T10:25:24Z`, after adding only
`TestTargetVerifyPermitRequiresExactPrivateSessionProof`, the required focused
command returned the expected compile-time RED:

```text
cd backend
go test ./internal/backupasset/recovery \
  -run '^TestTargetVerifyPermitRequiresExactPrivateSessionProof$' -count=1

target_test.go:3299:12: undefined: issueTargetVerifyPermit
target_test.go:3328:10: value.proof undefined (type *TargetObservationPermit has no field or method proof)
FAIL xirang/backend/internal/backupasset/recovery [build failed]
```

The first implementation attempt called a nonexistent package validator instead
of the existing `JobState.Valid` method and failed to compile. After that
one-line correction, the unchanged metrics selector was GREEN:

```text
cd backend
go test ./internal/backupasset/recovery -run '^TestRecoveryMetrics' -count=1
ok xirang/backend/internal/backupasset/recovery 0.052s
```

The managed Recovery settings RED proved the typed snapshot and cross-field
validation were both missing:

```text
cd backend
go test ./internal/backupasset ./internal/settings \
  -run '^(TestFoundationRecoveryConfig|TestBackupAssetRecoveryManagedRuntime)' -count=1

FoundationService.RecoveryConfig undefined
accepted unsafe Recovery settings map[
  backup_assets.recovery.lease_renew_margin:30s
  backup_assets.recovery.lease_ttl:30s]
FAIL xirang/backend/internal/backupasset
FAIL xirang/backend/internal/settings
```

The failure identity is the missing package-private issuer/proof required by
design 39.2. It is not an environment, fixture, syntax or unrelated-package
failure. No product file had been edited before this RED.

### R16 Sealed Verify Permit GREEN

At `2026-08-04T10:27:29Z`, GREEN added only the package-private verify proof,
its length-framed domain digest, the shape-only issuer and Verify-specific
constructor/validation path. Preflight and result-read constructors continue
to use the unchanged structural validation path. The focused command passed:

```text
cd backend
go test ./internal/backupasset/recovery \
  -run '^(TestTargetVerifyPermitRequiresExactPrivateSessionProof|TestTargetPortOperationPermitsArePurposeExact|TestTargetPurposeSpecificPermitConstructionRejectsCrossPurpose|TestTargetPermitsRequireExactObjectAndFrozenJobBinding|TestTargetPortVerifyUsesClosedExpectationObservationBoundary)$' \
  -count=1

ok xirang/backend/internal/backupasset/recovery 0.051s
```

The proof test rejects every public-field mutation plus job, mode, plan,
node/credential revision, root locator and proof-digest substitution, and its
JSON scan exposes none of the private binding values. Owned `gofmt -d` and
`git diff --check` returned no output, staged paths remain zero, and both
protected root-level hashes remain unchanged. This is R16/G16 focused credit
only; all existing worker/executor issuance sites remain intentionally RED
until R17.

### R17 Locked Handoff And Issuance RED

At `2026-08-04T10:33:24Z`, after adding only the four locked-handoff/issuance
tests and strengthening existing target fakes to validate/capture permits, the
required selector returned the expected compile-time RED:

```text
cd backend
go test ./internal/backupasset/recovery \
  -run '^(TestRecoveryInterruptedOperationHandoffCarriesLockedTargetSessionBinding|TestRecoveryOrdinaryVerifyIssuanceUsesExactLockedTargetSessionBinding|TestRecoveryDeleteObservationIssuanceUsesExactLockedTargetSessionBinding|TestRecoveryAdoptionVerifyIssuanceUsesExactLockedTargetSessionBinding)$' \
  -count=1

worker_test.go:440:13: handoff.targetSessionBinding undefined
worker_test.go:477:17: undefined: newRecoveryTargetVerifyPermit
FAIL xirang/backend/internal/backupasset/recovery [build failed]
```

The failure identity is the missing locked private binding and single issuer
helper required by design 39.2. No worker/executor product code had been edited
for R17 before this RED.

### R17 Locked Handoff And Issuance GREEN

At `2026-08-04T10:37:38Z`, GREEN constructed the private session binding inside
the locked durable aggregate load, included its digest in the in-memory handoff
digest and routed all four existing issuance paths through one exact helper.
The four-path selector passed in `0.228s`; the expanded selector covering every
`TestRecoveryExecuteClaim*` and `TestRecoveryAdoptInterruptedOperation*` test
then passed in `6.878s`.

The handoff test additionally recomputes otherwise self-consistent substitute
plan, node-revision, credential-revision and root-locator bindings and proves
the helper returns `ErrRecoveryWorkerFenceLost` before target I/O. Ordinary,
delete-pause, delete-observation and adoption fakes validate the permit against
the exact object and compare the private session/job/mode tuple with the locked
plan. A test-only loader correction changed the current-attempt assertion from
the restart-only loader to the existing `ordinaryExecution=true` loader; the
restart loader deliberately requires a prior attempt and product behavior was
not relaxed.

Owned `gofmt -d`, `git diff --check`, protected hashes and staged-zero checks
passed. This is R17/G17 focused credit only; the concrete session factory still
rejects `TargetPurposeVerify` until R18.

### R18 Purpose-Exact Verify Session RED

At `2026-08-04T10:39:02Z`, after adding only
`TestRecoveryTargetSessionFactoryOpensPurposeExactVerify`, the focused command
failed as expected:

```text
cd backend
go test ./internal/backupasset/recovery \
  -run '^TestRecoveryTargetSessionFactoryOpensPurposeExactVerify$' -count=1

target_test.go:755: open exact verify session: invalid recovery target permit
FAIL xirang/backend/internal/backupasset/recovery
```

The existing factory rejected Verify at its literal purpose allowlist before
resolver or dialer use. No R18 product edit preceded this RED.

### R18 Purpose-Exact Verify Session GREEN

At `2026-08-04T10:39:47Z`, GREEN expanded only the existing session-factory
allowlist by the literal `TargetPurposeVerify`. The required combined selector
passed:

```text
cd backend
go test ./internal/backupasset/recovery \
  -run '^(TestRecoveryTargetSessionFactoryOpensPurposeExactVerify|TestRecoverySFTPTargetFactory|TestRecoverySFTPTargetSessionCancellationClosesAndJoins|TestRecoverySFTPTargetCreateOwnedJobDir|TestRecoverySFTPTargetValidateOwnedJobDir)' \
  -count=1

ok xirang/backend/internal/backupasset/recovery 0.074s
```

The new test proves the resolver receives Verify, the dialer receives exactly
`recovery_verify`, the audit correlation is the safe job ID, a substituted node
revision is denied before dial, and an accepted session closes SFTP then SSH
exactly once. Existing write/cleanup factory and cancellation behavior remained
GREEN; preflight and result-read purposes remain outside the allowlist. This is
R18/G18 focused credit only; concrete `Verify` still returns
`ErrRecoveryTargetUnavailable` until R19.

### R19 Present Regular-File Verify RED

At `2026-08-04T10:46:40Z`, after adding only the regular-file success,
namespace and observation-revision tests plus test-only authority/oracle
helpers, the required selector returned the expected behavioral RED:

```text
cd backend
go test ./internal/backupasset/recovery \
  -run '^(TestRecoverySFTPTargetVerifyPresentRegularFile|TestRecoverySFTPTargetVerifyNamespaceAndObservationRevision)$' \
  -count=1

--- FAIL: TestRecoverySFTPTargetVerifyPresentRegularFile
    target_test.go:889: ... error=recovery target unavailable
    target_test.go:921: ... error=recovery target unavailable
    target_test.go:943: ... error=recovery target unavailable
--- FAIL: TestRecoverySFTPTargetVerifyNamespaceAndObservationRevision
    target_test.go:964: first stable verify: recovery target unavailable
    target_test.go:1037: ... error=recovery target unavailable, want ErrInvalidTargetPermit
    target_test.go:1080: ... error=recovery target unavailable, want ErrRecoveryTargetChanged
    target_test.go:1099: ... error=recovery target unavailable
FAIL xirang/backend/internal/backupasset/recovery 0.056s
```

Both selectors compiled and reached the concrete adapter. The failure identity
is exactly its still-closed `Verify` arm, not a fixture, syntax, authority or
environment failure. No R19 product edit preceded this RED.

### R19 Present Regular-File Verify GREEN

At `2026-08-04T10:50:34Z`, GREEN added only the pre-session sealed authority
and namespace checks, canonical regular-file observation, exact bounded stream
read, closed present observation and frozen opaque token. The required selector
passed:

```text
cd backend
go test ./internal/backupasset/recovery \
  -run '^(TestRecoverySFTPTargetVerifyPresentRegularFile|TestRecoverySFTPTargetVerifyNamespaceAndObservationRevision|TestRecoverySFTPTargetCreateOwnedJobDir|TestRecoverySFTPTargetValidateOwnedJobDir|TestRecoverySFTPTargetCreateAndValidateReturnSameObservation)$' \
  -count=1

ok xirang/backend/internal/backupasset/recovery 0.057s
```

The focused matrix proves isolated regular-file, zero-byte and in-place
success; exact bounded reads; stable `sftp1:` length-49 revision bytes under
the frozen domain and field order; root/path/content/byte separation; first-item
marker/temp rejection before resolver use; exact private `0700` jobs/job
parents; and acceptance of a deeper ordinary marker-named file. A1 marker
creation/validation and its observation revision remained GREEN after the
canonical-file snapshot helper was factored for reuse.

R19 intentionally retains the plan's minimal unavailable mapping for
non-successful payload read/stat outcomes. R20 owns the independent adversarial
RED that refines drift versus transport failures, cancellation/privacy and the
seven deferred concrete methods. Owned formatting, `git diff --check`, both
protected hashes and staged-zero checks passed after GREEN.

### R20 Drift, Cancellation, Privacy And Deferred-Boundary RED

At `2026-08-04T10:55:45Z`, after adding only the adversarial path/content/stat,
dependency/cancellation/resource and absent/deferred boundary tests, the
required selector returned the expected classification RED:

```text
cd backend
go test ./internal/backupasset/recovery \
  -run '^(TestRecoverySFTPTargetVerifyRejectsPathContentAndStatDrift|TestRecoverySFTPTargetVerifyCancellationAndErrors|TestRecoverySFTPTargetA2aDeferredMethodsOpenNoSession)$' \
  -count=1

--- FAIL: TestRecoverySFTPTargetVerifyRejectsPathContentAndStatDrift
    missing_final: recovery target unavailable, want exact ErrRecoveryTargetChanged
    final_symlink: recovery target unavailable, want exact ErrRecoveryTargetChanged
    final_directory: recovery target unavailable, want exact ErrRecoveryTargetChanged
    final_special_file: recovery target unavailable, want exact ErrRecoveryTargetChanged
    wrong_final_realpath: recovery target unavailable, want exact ErrRecoveryTargetChanged
    declared_size_mismatch: recovery target unavailable, want exact ErrRecoveryTargetChanged
    content_digest_mismatch: recovery target unavailable, want exact ErrRecoveryTargetChanged
    opened_size/mode/modtime_drift: recovery target unavailable, want exact ErrRecoveryTargetChanged
    post_size/mode/modtime_drift: recovery target unavailable, want exact ErrRecoveryTargetChanged
    short_read: recovery target unavailable, want exact ErrRecoveryTargetChanged
    extra_byte: recovery target unavailable, want exact ErrRecoveryTargetChanged
    zero_nil_after_expected_bytes: recovery target unavailable, want exact ErrRecoveryTargetChanged
FAIL xirang/backend/internal/backupasset/recovery 0.065s
```

All sixteen failures are the exact R19 minimal-unavailable behavior that R20
was reserved to refine. The dependency error, file/client close, caller
cancellation and valid-absent/seven-deferred zero-session tests already passed,
including exact sentinel privacy and exactly-once file/SFTP closure assertions.
No R20 product edit preceded this RED.

### R20 Drift, Cancellation, Privacy And Deferred-Boundary GREEN

At `2026-08-04T10:57:03Z`, GREEN refined only the concrete present-file
classification branches. Missing/shape/alias, declared size, opened/post
snapshot, short EOF, extra byte, `(0,nil)` and digest drift now return exact
`ErrRecoveryTargetChanged`; non-missing dependency and close failures remain
sanitized unavailable, and caller context still wins. The focused adversarial
selector passed in `0.063s`.

The required combined A2a normal selector then passed:

```text
cd backend
go test ./internal/backupasset/recovery \
  -run '^(TestTargetVerifyPermitRequiresExactPrivateSessionProof|TestRecoveryInterruptedOperationHandoffCarriesLockedTargetSessionBinding|TestRecoveryOrdinaryVerifyIssuanceUsesExactLockedTargetSessionBinding|TestRecoveryDeleteObservationIssuanceUsesExactLockedTargetSessionBinding|TestRecoveryAdoptionVerifyIssuanceUsesExactLockedTargetSessionBinding|TestRecoveryTargetSessionFactoryOpensPurposeExactVerify|TestRecoverySFTPTargetVerify|TestRecoverySFTPTargetA2aDeferredMethodsOpenNoSession)$' \
  -count=1

ok xirang/backend/internal/backupasset/recovery 0.220s
```

The required repeated race selector also passed:

```text
cd backend
go test -race ./internal/backupasset/recovery \
  -run '^(TestRecoveryTargetSessionFactoryOpensPurposeExactVerify|TestRecoverySFTPTargetVerify)' \
  -count=5

ok xirang/backend/internal/backupasset/recovery 1.819s
```

Every opened-file failure branch closes the file exactly once, session closure
closes the SFTP client exactly once, operation classification wins over close
noise, successful observation is blocked by close failure, and injected raw
dependency/root/object/credential strings remain absent from returned errors.
Valid absent Verify and all seven deferred A2a/A3 methods open zero resolver,
SSH or SFTP sessions.

### Post-R20 Controller Review Defect RED/GREEN

Controller-inline design 39.3/39.5 review found that a structurally sealed
isolated marker-namespace object paired with a valid absent expectation reached
the closed-unavailable return before concrete namespace validation. This did
not open a session or expand authority, but it violated the exact invalid-object
classification.

At `2026-08-04T11:09:47Z`, the added assertion in
`TestRecoverySFTPTargetA2aDeferredMethodsOpenNoSession` produced genuine RED:

```text
reserved absent Verify error=recovery target unavailable,
want exact ErrInvalidTargetPermit
FAIL xirang/backend/internal/backupasset/recovery 0.051s
```

GREEN moved only the valid-absent closed return after exact proof/session/object
namespace validation. At `2026-08-04T11:10:24Z`, the same selector passed in
`0.053s`. Reserved marker/temp objects now return exact
`ErrInvalidTargetPermit`; a namespace-valid absent expectation remains exact
`ErrRecoveryTargetUnavailable`; both paths retain zero resolver/SSH/SFTP calls.

### Task 7 A2a V7 Focused Completion And Stop Point

At `2026-08-04T11:16:18Z`, after the controller-review correction, every V7
dynamic gate was rerun from the final product state:

```text
A1 + A2a focused normal                              PASS (1.348s)
A1 + A2a focused race, count=5                       PASS (20.915s)
full Recovery normal                                 PASS (16.311s)
full Recovery race                                   PASS (46.130s)
required PostgreSQL full Recovery normal             PASS (22.855s, no skip)
required PostgreSQL full Recovery race               PASS (53.769s, no skip)
go vet ./internal/backupasset/recovery               PASS
make lint-backend                                    PASS (0 issues)
owned gofmt / whitespace / merge-marker / scope scan PASS (no output)
git diff --check                                     PASS (no output)
```

The current shell did not inherit `TEST_POSTGRES_DSN`. The pre-existing
task-specific `xirang-c13-pg` container still listened on the prior isolated
port, but its persisted volume and declared role/password environment had
diverged: the declared role did not exist and two initial forced invocations
failed authentication. No product assertion failed in those attempts. Through
the container-local administrator socket, the existing `postgres` test role
was reset to the container's already-stored test password without printing or
persisting it in the repository. Both final forced whole-package commands then
passed with no skip. The container was not restarted, replaced or removed.

Controller-inline review reconciled the complete A2a delta with design
39.1--39.6. The review found only the namespace-before-absent classification
defect recorded above; its genuine RED/GREEN and all final reruns are complete.
There is no remaining Critical or Important A2a finding. The implementation:

- seals Verify with the exact locked executed-plan binding and routes all four
  durable issuance paths through one issuer;
- admits only `recovery_verify`, with exact node/credential revisions and safe
  job correlation;
- returns a closed present observation only after canonical root/parent/final
  revalidation, exact bounded streaming and exact EOF;
- uses the frozen 49-byte `sftp1:` token domain/field order without metadata
  fidelity claims; and
- keeps valid absence and all seven deferred methods closed before session use.

No shared `.trellis/spec/` update is required: the review defect was already an
explicit design 39.3/39.5 rule and is now enforced by regression coverage.
Task 7 bookkeeping marks only A2a `focused_complete_checked`. Task 7 and Child
13 remain `in_progress`, the parent remains `planning`, and program delivery
remains 12/15. The stop point excludes A2b create/write, A2c preflight, A2d
result read, A2e overwrite/Lstat/absence, A3 destructive cleanup,
runtime/main, orphan/quarantine and every stage/commit/push/PR/CI/merge action.

## Task 7 A2b Exact-Plan No-Overwrite Regular-File Create Closure

### R21 Exact Locked-Handoff Item Authority RED/GREEN

At `2026-08-04T20:20:08+08:00`, the frozen R21 selector failed to compile
after only the proof and issuance tests were added. The compiler reported the
old object-only `ordinaryItemWritePermit` argument, missing `itemProof`, missing
`targetItemWritePermitProofDigest`, missing `validateItemWriteAt`, and missing
`targetItemWritePermitProof` / `issueTargetItemWritePermit`. This is the
genuine capability RED: the pre-A2b product could not express or validate the
locked handoff's exact item operation authority.

GREEN added the private length-framed item proof, complete public/private
substitution checks, and the handoff-bound ordinary issuer. Early GREEN
attempts exposed a regression where skip operations were incorrectly required
to carry a payload proof; the final delta limits item-proof issuance to regular
create/overwrite while preserving the existing A1/A2a paths. At
`2026-08-04T20:24:34+08:00`, the required R21 plus A1/A2a authority selector
passed:

```text
ok xirang/backend/internal/backupasset/recovery 0.179s
```

### R22 Create Admission And Parent Preparation RED/GREEN

At `2026-08-04T20:36:20+08:00`, the R22 selector produced its genuine
compile-time RED:

```text
target.entropy undefined (type *recoverySFTPTarget has no field or method entropy)
FAIL xirang/backend/internal/backupasset/recovery [build failed]
```

GREEN added instance-local 32-byte entropy, create-only pre-session admission,
exact `recovery_write` authority, isolated ordered parent creation and in-place
read-only parent validation. A later failure was confined to the test helper
treating `ENOTDIR` as proof that a descendant existed; the product already
returned the required changed identity without mutation. After correcting only
that assertion, the complete R22 selector passed at
`2026-08-04T20:39:44+08:00` in `0.058s`.

### R23 Exclusive Temp And Bounded Stream RED/GREEN

At `2026-08-04T20:45:25+08:00`, the R23 selector reached the post-parent stop
and failed with no temp open (`temp opens=[] flags=[]`) across the exclusive
temp and streaming matrices. This is the genuine runtime RED reserved for
R23. GREEN added the exact same-directory CSPRNG basename,
`O_WRONLY|O_CREATE|O_EXCL`, `0600`, ownership-only cleanup, bounded
copy/hash/one-byte EOF proof, `Sync`, single close, canonical reopen and exact
reread. The final R23 selector passed at `2026-08-04T20:48:18+08:00`:

```text
ok xirang/backend/internal/backupasset/recovery 0.065s
```

### R24 No-Overwrite Publish And Exact Result RED/GREEN

At `2026-08-04T20:52:24+08:00`, the R24 selector produced the intended
publication RED: concurrent/ambiguous cases recorded `rename calls=0`, both
successful result cases stopped at `recovery target unavailable`, and final
drift/live-revocation cases could not reach their frozen classifications.
GREEN added the pre-rename parent/live/final-absence revalidation, standard
SFTP `Rename` only, final `0600`/content/canonical verification, the exact A2a
`sftp1:` revision and clean-session success boundary. An intermediate GREEN
run caught a missing final live check before rename; the final correction
closed that window. At `2026-08-04T20:53:59+08:00`, the combined target
selector passed:

```text
ok xirang/backend/internal/backupasset/recovery 0.064s
```

### R25 Executor, Unresolved, Cancellation And Privacy Integration

The frozen R25 behavior was inherited GREEN from R21--R24 and the existing
unresolved projection. Its first run did not establish a new product RED: the
fake tried to revalidate the recorded permit after the successful write had
already advanced the durable target revision, so the now-stale permit correctly
failed. The fake was changed only to validate and record item authority at call
time before reading source content. The final R25 selector passed in `0.373s`;
the required repeated race selector then passed:

```text
ok xirang/backend/internal/backupasset/recovery 3.730s
```

The executor matrix proves the source stream opens before the target call,
the call receives the exact locked create proof after the durable load
transaction closes, proof mutation causes the existing terminal
`write_result_invalid` disposition, target-chain revision does not advance on
unresolved output, and later items are not called. Target tests prove context
identity, at-most-once file/SFTP/SSH closure, cleanup of at most the exact owned
temp, zero raw/private leakage, and zero-session unavailability for valid
overwrite plus all deferred methods.

### V8 Fresh Verification And Inline Review

The final product state passed every V8 dynamic and static gate:

```text
A1 + A2a + A2b focused normal                       PASS (1.362s)
A1 + A2a + A2b focused race, count=5                PASS (22.421s)
whole Recovery normal                               PASS (16.464s)
whole Recovery race                                 PASS (45.729s)
required PostgreSQL whole Recovery normal           PASS (23.284s, no skip)
required PostgreSQL whole Recovery race             PASS (54.791s, no skip)
go vet ./internal/backupasset/recovery               PASS
make lint-backend                                    PASS (0 issues)
owned gofmt / whitespace / merge-marker / scope scan PASS (no output)
git diff --check                                     PASS (no output)
```

The shell had no inherited `TEST_POSTGRES_DSN`. The healthy existing
`xirang-c13-pg` fixture was inspected without restart, replacement or removal.
Two initial key/value DSN attempts failed only because the permanent tests
require a PostgreSQL URL; a URL was then assembled in-process with URI-encoded
fixture values and was neither printed nor persisted. Both required final runs
passed with no skip.

Both Trellis tasks validated; Task 7 JSON/JSONL and the parent JSON parsed.
Exact scope accounting remained:

```text
phase1=9 create=55 modify=81 total=145 unique=145 duplicates=0
dirty=93 manifest_dirty=91 protected_dirty=2 outside=0 staged=0
create_present_at_head=0 modify_missing_at_head=0 future_000070_71=0
```

The branch stayed `codex/backup-assets-controlled-recovery`; `HEAD`, local
`main` and `origin/main` stayed at `51771654a85967656fe1ca69686590b734ff9214`;
the staged count stayed zero. Protected hashes remained:

```text
go.mod b767fc9ef9376c651d0493329b710ac8dcf8d77e52686b847d244aff9f6d48fd
recovery/testdata/rsync_local_to_remote.json 2570bd4472541322d902c4cdf2fe43f247b69f19d335558830e749567f763892
```

Controller-inline review reconciled the complete delta with design
40.1--40.7 and every A2b PRD acceptance row. No Critical or Important finding
remains. `validateRecoveryCreateParentAuthority` is private and receives the
same immutable object already bound and validated by `validateItemWriteAt`, so
duplicating root/digest comparisons there is not an independently reachable
authority check. The post-`Mkdir` live check before `Chmod` is the explicitly
approved R22 order; any failed call leaves only a partial directory beneath the
existing canonical `0700` job workspace and cannot proceed to temp creation.
Neither point warrants an unplanned product change or reconstructed RED.

No shared `.trellis/spec/` update is required because all reviewed behavior is
already task-local in design Section 40. Task 7 bookkeeping marks only A2b
`focused_complete_checked`. Task 7 and Child 13 remain `in_progress`, the
parent remains `planning`, and program delivery remains 12/15. The exact stop
is before A2c preflight, A2d result read, A2e overwrite/Lstat/absence, A3
destructive cleanup/delete/tombstone, runtime/main composition,
orphan/quarantine and every stage/commit/push/PR/CI/merge action.

## Task 7 A2c1 R26 Exact Draft Authority And Evidence Ownership

At `2026-08-04T22:54:21+08:00`, only R26/G26 is closed. A2c1 remains in
progress: R27--R30 and V9 have not started, and A2c2 remains separately gated
and unauthorized.

The two frozen R26 selectors first produced a genuine compile-time capability
RED after only their tests were added:

```text
go test ./internal/backupasset/recovery \
  -run '^(TestRecoverySFTPTargetPreflightRequiresExactObservedDraftPlan|TestTargetPreflightEvaluatorRequiresIndependentExternalEvidence)$' \
  -count=1

FAIL: missing TargetRootProbeFacts, PreflightExternalEvidence request/result
proof contracts and digest helpers, and the required two-port evaluator shape
```

This was product RED rather than harness RED: the prior composite target facts
could not represent independent evidence ownership, and structurally built
preflight authority could not prove the exact hook-decrypted draft plan.

The minimal GREEN added the private draft-plan session binding and sealed
target permit, kept the caller's public permit structural, split
`TargetRootProbeFacts` from `PreflightExternalEvidence`, required both evaluator
ports, and confined the deterministic external issuer to `preflight_test.go`.
The concrete target still returns closed unavailable after accepting valid R26
authority; no preflight session is opened.

The first focused GREEN passed in `0.090s`. The broader planned R26 selector
then exposed one existing error-classification regression: a sound private
proof paired with a substituted public target request returned invalid instead
of conflict. The implementation was narrowed so missing/damaged private proof
remains invalid while public request substitution remains a preflight conflict.
The planned R26 service/CAS selector then passed in `0.191s`.

A whole-package run subsequently found one old deferred-method fixture still
passing an empty structural preflight permit. The fixture now supplies a
test-only sealed permit and preserves the original assertion: valid R26
authority returns unavailable without resolver/dial/session use. Fresh final
checks passed:

```text
R26 + PreflightService/TargetPreflight selectors PASS (fresh 0.186s)
whole Recovery normal                           PASS (16.351s)
go vet ./internal/backupasset/recovery          PASS
golangci-lint recovery package                  PASS (0 issues)
owned gofmt                                     PASS (no output)
Task 7 and parent Trellis context validation    PASS
staged files                                    0
```

Static boundary scans found no production external-evidence issuer, no
Provider/Repository evidence adapter, and no `OpenPreflight`. No shared
`.trellis/spec/` update is required because the authority/evidence split is
already frozen in task-local PRD A2c1 and design Section 41. R26 alone is
checked; A2c1, A2c, Task 7 and Child 13 remain incomplete.

## Task 7 A2c1 R27 Purpose-Exact Preflight Session

At `2026-08-04T23:12:33+08:00`, only R27/G27 is closed. A2c1 remains in
progress: R28--R30 and V9 have not started, and A2c2 remains separately gated
and unauthorized.

The exact R27 selector first produced the genuine missing-capability RED after
only `TestRecoveryTargetSessionFactoryOpensPurposeExactPreflight` was added:

```text
# xirang/backend/internal/backupasset/recovery [xirang/backend/internal/backupasset/recovery.test]
internal/backupasset/recovery/target_test.go:957:27: factory.OpenPreflight undefined
FAIL xirang/backend/internal/backupasset/recovery [build failed]
```

The minimal GREEN added a private `OpenPreflight` entry that accepts only the
exact draft-plan binding, resolves `TargetPurposePreflight`, dials literal
`recovery_preflight` with opaque plan-ID correlation, revalidates exact node
and credential revisions, and shares only the post-validation SSH/SFTP
lifecycle. Executed `Open` remains limited to write, verify and cleanup. The
test also proves non-draft/damaged bindings and resolved node, archive,
node-revision or credential-revision drift stop before dial, while cancellation
closes SFTP then SSH exactly once and joins.

Inline review against design Section 41.3 found that the first GREEN had not
yet attached the injected command runner required for the next target-owned
probe while keeping R27 itself command-free. A focused test-only assertion then
produced this genuine dependency-binding RED:

```text
internal/backupasset/recovery/target_test.go:958:11: factory.openCommandRunner undefined
internal/backupasset/recovery/target_test.go:976:41: session.commandRunner undefined
internal/backupasset/recovery/target_test.go:978:33: session.commandRunner undefined
FAIL xirang/backend/internal/backupasset/recovery [build failed]
```

The correction reuses `sshutil.NewSSHCommandRunner` with concurrency one and
attaches the injected runner from the same dialed SSH client to the preflight
session. It adds no command invocation, UID/GID parsing, StatVFS call, path or
capacity observation, so R28 remains unstarted. The corrected exact selector
passed in `0.083s`; the planned cancellation/verify combination passed in
`0.085s`.

Fresh final checks passed:

```text
whole Recovery normal                           PASS (15.962s)
go vet ./internal/backupasset/recovery          PASS
golangci-lint recovery package                  PASS (0 issues)
final exact R27 selector                        PASS (0.089s)
owned gofmt / whitespace / merge-marker scans  PASS (no output)
```

No shared `.trellis/spec/` update is required because the purpose-exact
preflight session and fixed server-owned command boundary are already frozen in
task-local design Section 41.3. R27 alone is checked; `ProbeRoot` remains
closed, R28--R30 and V9 remain unchecked, and A2c1, A2c, Task 7 and Child 13
remain incomplete.

## Task 7 A2c1 R28 Root Identity, Principal And Capacity Observation

At `2026-08-05T08:17:53+08:00`, R28/G28 is closed. A2c1 remains in progress:
R29, R30 and V9 have not started, and A2c2 remains separately gated and
unauthorized.

The exact R28 selector first produced a genuine closed-capability RED after the
21-case canonical/root-principal/capacity matrix was added:

```text
commands=[], want exact fixed commands [id -u id -G]
FAIL xirang/backend/internal/backupasset/recovery
```

This was product RED rather than fixture RED: valid sealed preflight authority
still returned closed unavailable before any fixed command, SFTP path or
capacity observation. The minimal GREEN added private SFTP `StatVFS`, strict
bounded UID/GID parsing, root-prefix `Lstat`/`RealPath` walk and rewalk,
owner/group/root-principal permission evaluation, non-world-writable directory
validation, root/parent filesystem checks and overflow-safe available
byte/inode conversion. The probe returns only `TargetRootProbeFacts`; it makes
zero target mutation calls.

The first whole-package run then exposed one obsolete A2a deferred-method test
that still invoked the now-open `ProbeRoot` and expected zero sessions:

```text
TestRecoverySFTPTargetA2aDeferredMethodsOpenNoSession:
absent/deferred methods resolved=1 dialed=1, want zero sessions
```

Only that obsolete preflight fixture/call was removed. The same test retains
the absent `Verify`, `Lstat`, `CreateDirectory`, `WriteAtomic`, `Delete`,
`OpenOwnedResult`, `RemoveOwnedJobDir` and final zero-session assertions.

Inline review against design Section 41.3 found one R28 deadline defect: the
two fixed commands each received the full remaining permit duration, so the
second command could outlive permit expiry. A test assertion requiring one
shared absolute deadline produced a second genuine RED; representative output
was:

```text
command deadlines=[...m=+60.049227817 ...m=+60.049246000],
want one shared permit deadline for 2 commands
FAIL xirang/backend/internal/backupasset/recovery
```

The correction creates one timeout context around the complete principal
probe. Both `CommandRunner.Run` calls now share its absolute deadline while an
earlier caller cancellation/deadline still wins. Fresh final checks passed:

```text
exact R28 selector                            PASS (0.057s)
R26--R28 combined selector                    PASS (0.150s)
whole Recovery normal                         PASS (16.633s)
go vet ./internal/backupasset/recovery        PASS
golangci-lint recovery package                PASS (0 issues)
owned gofmt / whitespace / merge-marker scans PASS (no output)
Task 7 and parent Trellis validation           PASS
manifest phase1/create/modify/total            9/55/81/145; 145 unique
dirty/manifest/protected/outside/staged        93/91/2/0/0
future 000070/000071                           0
```

The branch remains `codex/backup-assets-controlled-recovery`; `HEAD`, local
`main` and `origin/main` remain
`51771654a85967656fe1ca69686590b734ff9214`. Protected hashes remain:

```text
go.mod b767fc9ef9376c651d0493329b710ac8dcf8d77e52686b847d244aff9f6d48fd
recovery/testdata/rsync_local_to_remote.json 2570bd4472541322d902c4cdf2fe43f247b69f19d335558830e749567f763892
```

Static boundary scans found no target-existence assignment, R29 selector or
`sftpr1:`, `sftpf1:` or `sftpt1:` implementation. No shared `.trellis/spec/`
update is required because this contract is already frozen in task-local
design Sections 41.3--41.5. R28 alone is checked; R29--R30, V9, A2c1, A2c,
Task 7 and Child 13 remain incomplete.

## Task 7 A2c1 R29 Target Matrix And Stable Observation Revisions

At `2026-08-05T08:55:03+08:00`, R29/G29 is closed. A2c1 remains in progress:
R30 and V9 have not started, and A2c2 remains separately gated and
unauthorized.

`TestRecoverySFTPTargetProbeRootTargetMatrixAndRevisions` first produced a
genuine product RED after only the target matrix and exact formula assertions
were added. Representative failures were:

```text
regular: TargetExists:false RootRevision:root-revision-before-probe,
want exists=true and all revisions
prefix alias: error=<nil>, want recovery target changed
revisions root="root-revision-before-probe" fs="filesystem-revision-1"
target="target-revision-1", want exact sftpr1:/sftpf1:/sftpt1: tokens
FAIL xirang/backend/internal/backupasset/recovery
```

This was the planned missing-capability RED: R28 still observed only root,
returned plan-carried revisions and did not classify the target or compare
stable pre/post inputs.

The minimal GREEN extends each read-only pass to the exact target path. Exact
`os.IsNotExist` at a final or intermediate component yields absent; ambiguous
errors remain unavailable. Existing final regular, directory, symlink and
special entries are classified without following the final entry. Every
existing parent must remain a canonical real directory, and its `StatVFS`
filesystem ID contributes to `MountValid`. A second complete observation must
match root, filesystem and target revision inputs plus stable validity flags;
only free byte/inode counters may change.

The three length-framed raw-url-base64 revision products now use the exact
task-local domains and field order:

```text
sftpr1:  xirang/recovery/sftp-root-observation/v1
sftpf1:  xirang/recovery/sftp-filesystem-observation/v1
sftpt1:  xirang/recovery/sftp-target-observation/v1
```

Each token is exactly 50 bytes and non-SHA-shaped. Tests prove stability and
difference across node, root ID/digest/locator, target path/kind/size/mode/
UID/GID/mtime and every stable filesystem input. `Bfree`, `Bavail`, `Ffree`
and `Favail` are excluded from stable revisions while the returned capacity is
from the second observation.

Inline formula review found that the first GREEN had reused the existing
`TargetEntryMissing` value `missing`, while design Section 41.5 freezes the
target-revision literal `absent`. An exact assertion produced a second genuine
RED:

```text
absent TargetRevision=sftpt1:5MhwQIs_XVfUjGTllHreOk2-Kjm6BU33on74yUx8_S8,
want sftpt1:kJGXRLhw1Mcd4otyAXK3jJz9DqjvlLynVdvpZbzUUg4
FAIL xirang/backend/internal/backupasset/recovery
```

Only the private revision input was changed to `absent`; the existing public
`TargetEntryMissing="missing"` contract remains unchanged. The common opaque
SFTP revision encoder is also reused by the unchanged `sftp1:` regular-file
observation path. Fresh final checks passed:

```text
exact R29 selector                            PASS (0.056s)
R26--R29 combined selector                    PASS (0.148s)
R29 + A1/A2a/A2b target regression            PASS (0.095s)
whole Recovery normal                         PASS (16.935s)
go vet ./internal/backupasset/recovery        PASS
golangci-lint recovery package                PASS (0 issues)
owned gofmt / whitespace / merge-marker scans PASS (no output)
Task 7 and parent Trellis validation           PASS
manifest phase1/create/modify/total            9/55/81/145; 145 unique
dirty/manifest/protected/outside/staged        93/91/2/0/0
future 000070/000071                           0
```

The branch remains `codex/backup-assets-controlled-recovery`; `HEAD`, local
`main` and `origin/main` remain
`51771654a85967656fe1ca69686590b734ff9214`. Protected hashes remain:

```text
go.mod b767fc9ef9376c651d0493329b710ac8dcf8d77e52686b847d244aff9f6d48fd
recovery/testdata/rsync_local_to_remote.json 2570bd4472541322d902c4cdf2fe43f247b69f19d335558830e749567f763892
```

Static boundary scans found no R30 cancellation/privacy selector and no new
dependency, path, model, migration, setting, external-evidence adapter or
runtime/main composition. No shared `.trellis/spec/` update is required because
the revision contract is already frozen in task-local design Sections
41.4--41.5. R29 alone is checked; R30, V9, A2c1, A2c, Task 7 and Child 13
remain incomplete.

## Task 7 A2c1 R30 Cancellation, Privacy And Deferred Capabilities

At `2026-08-05T09:28:25+08:00`, R30/G30 is closed. A2c1 remains in progress:
V9 has not started, and A2c2 remains separately gated and unauthorized.

After only `TestRecoverySFTPTargetProbeRootCancellationPrivacyAndNoMutation`
and its test fixtures were added, the exact selector produced a genuine
product RED. Representative failures were:

```text
resolver cancellation identity: error=recovery target unavailable,
want exact context canceled
statvfs cancellation identity: error=recovery target unavailable,
want exact context canceled
close deadline identity: error=recovery target unavailable,
want exact context deadline exceeded
close deadline: returned complete successful TargetRootProbeFacts and nil,
want zero facts and exact context deadline exceeded
FAIL xirang/backend/internal/backupasset/recovery
```

This was one coherent error/lifecycle gap. Resolver and SFTP observation
boundaries collapsed wrapped context sentinels before the target error mapper
could preserve them. The mapper itself did not recognize wrapped cancellation
or deadline errors. In addition, `ProbeRoot` did not recheck the caller context
after an otherwise successful close, so cancellation occurring during close
could incorrectly return a successful observation.

The minimal GREEN makes the existing target error mapper return the exact
`context.Canceled` or `context.DeadlineExceeded` sentinel before mapping all
other dependency detail to closed Recovery errors. The preflight resolver,
shared dial/SFTP open boundary, fixed principal command boundary and read-only
SFTP observation calls use that mapper before detail is discarded. `ProbeRoot`
also gates success on a live context after its joined close. The session's
existing `sync.Once` close ownership and SFTP-then-SSH order are unchanged;
no log, fallback mutation or capability was added.

The selector covers raw resolver, dial, SFTP-open, `StatVFS`, fixed-command,
principal-parse, SFTP-close and SSH-close failures with distinct private
tokens. It deterministically cancels during resolver, command, SFTP and close,
requires zero facts and exact caller context identity, joins the probe and
command goroutines, and requires every acquired command/SFTP/SSH resource to
close exactly once. Every arm records zero `Mkdir`, `Chmod`, `Open`,
`OpenFile`, `Rename` and `Remove` calls. Public/sealed permit, request and
facts JSON plus returned errors and captured logs are scanned for private
root/path/host/user/credential/UID/GID/command/stat material. Unproved external
evidence and request/result substitutions each return
`ErrInvalidTargetPreflight` with a zero result before reasons or snapshot
output.

One initial anonymous callback signature typo failed compilation and was fixed
before RED attribution. A later test-only case made an internal command-session
factory return `DeadlineExceeded` while the caller context remained live; it
was removed because the shared `sshutil.CommandRunner` intentionally exposes
that as `ErrCommandFailed`, not caller cancellation. The retained command
deadline arm triggers the actual caller context while the command is in flight
and proves exact `context.DeadlineExceeded` identity.

Fresh R30-scope checks passed:

```text
exact R30 selector                            PASS (0.053s)
owned gofmt                                   PASS (no output)
protected go.mod SHA-256                      b767fc9ef9376c651d0493329b710ac8dcf8d77e52686b847d244aff9f6d48fd
protected rsync fixture SHA-256               2570bd4472541322d902c4cdf2fe43f247b69f19d335558830e749567f763892
```

No shared `.trellis/spec/` update is required because the behavior is already
frozen in task-local design Sections 41.3--41.7. R30 alone is checked; V9,
A2c1, A2c, Task 7 and Child 13 remain incomplete. No new dependency, path,
model, migration, setting, external-evidence production adapter, runtime/main
composition, mutation capability or Git delivery action was introduced.

## Task 7 A2c1 V9 Focused Verification And Closure

At `2026-08-05T10:01:24+08:00`, V9 closes only A2c1. A2c2 remains separately
gated and unauthorized; A2c, Task 7 and Child 13 remain in progress, the parent
remains planning, and no Git delivery action is authorized.

The frozen A2c1 selector and package gates first passed with these exact fresh
results:

```text
A2c1 six-selector normal                    PASS (0.130s)
A2c1 six-selector race count 5              PASS (2.358s)
whole Recovery normal                       PASS (16.664s)
whole Recovery race                         PASS (46.084s)
required real PostgreSQL normal, no skip    PASS (23.122s)
required real PostgreSQL race, no skip      PASS (55.185s)
```

The required PostgreSQL commands reused the healthy `xirang-c13-pg` fixture.
The DSN was derived only inside each process and was neither printed nor
persisted; the fixture was not restarted, replaced or removed.

The first full `make lint-backend` run then produced a genuine static RED:

```text
internal/backupasset/recovery/target.go:1988:72: SA1012: do not pass a nil Context
internal/backupasset/recovery/target.go:2001:72: SA1012: do not pass a nil Context
internal/backupasset/recovery/target.go:2024:71: SA1012: do not pass a nil Context
3 issues: staticcheck
```

Root-cause review found that `ProbeRoot` owned a validated caller context but
discarded it across the two private SFTP observation helpers; seven dependency
error mappings therefore received `nil`, while the linter stopped after the
first three reports. The minimal correction threads the existing context
through those helpers and all seven mappings. It changes no command, SFTP
operation, target fact, mutation capability or close order. The exact R30
cancellation/privacy selector then passed in `0.063s`, a static scan found zero
remaining nil-context mappings, and fresh backend lint passed with `0 issues`.

Inline review covered every A2c1 delta against design Sections 41.1--41.7 and
all six PRD acceptance rows. No remaining Critical or Important finding was
found. The review confirmed exact draft-plan/private-proof authority,
transaction-free target I/O, purpose-exact session/correlation, target-only
facts, independently proved external evidence, stable revision formulas,
read-only double observation, sanitized lifecycle errors and the exact stop
point. Static boundary scans reported:

```text
production_external_issuer=0 provider_repository_adapter=0 probe_mutation_calls=0
```

The pre-bookkeeping static and structural gates passed:

```text
go vet / owned gofmt / whitespace / merge markers / forbidden APIs PASS
make lint-backend                                                      PASS (0 issues)
git diff --check                                                       PASS
Task 7 and parent Trellis validation                                   PASS
JSON and JSONL parsing                                                 PASS
phase1/create/modify/total/unique/duplicates                           9/55/81/145/145/0
dirty/manifest/protected/outside/staged                                93/91/2/0/0
create-present/modify-missing/future-000070-71                          0/0/0
branch                                                                 codex/backup-assets-controlled-recovery
HEAD/main/origin-main                                                  51771654a85967656fe1ca69686590b734ff9214
```

Protected hashes remained exact:

```text
go.mod b767fc9ef9376c651d0493329b710ac8dcf8d77e52686b847d244aff9f6d48fd
recovery/testdata/rsync_local_to_remote.json 2570bd4472541322d902c4cdf2fe43f247b69f19d335558830e749567f763892
```

Only `meta.task_7.a2c_split_preflight.a2c1_status` is promoted to
`focused_complete_checked`. A2c2 production external evidence, A2d result
read, A2e overwrite/Lstat/absence, A3 cleanup/delete, runtime/main,
orphan/quarantine and every stage/commit/push/PR action remain closed.

Fresh post-bookkeeping verification, including the context-threading
correction, passed with:

```text
A2c1 six-selector normal                    PASS (0.135s)
A2c1 six-selector race count 5              PASS (2.401s)
whole Recovery normal                       PASS (16.279s)
whole Recovery race                         PASS (46.112s)
required real PostgreSQL normal, no skip    PASS (23.133s)
required real PostgreSQL race, no skip      PASS (54.962s)
```

## Task 7 A2c2 R31--R33 And V10 Focused Closure

At `2026-08-05T11:07:17+08:00`, A2c2 and the complete A2c split are
`focused_complete_checked`. Task 7 and Child 13 remain `in_progress`; A2d
result read, A2e overwrite/Lstat/absence, A3 cleanup/delete, runtime/main,
orphan/quarantine, whole Task 7 review and every Git delivery action remain
open or closed by the stop boundary as applicable.

R31 first added only
`TestRecoveryPreflightExternalEvidenceAdapterIssuesOnlyObservedEvidence`.
The exact selector produced a genuine compile RED because the production
observation product and adapter did not exist:

```text
undefined: PreflightExternalEvidenceObservation
undefined: NewRecoveryPreflightExternalEvidenceAdapter
FAIL xirang/backend/internal/backupasset/recovery [build failed]
```

The minimal GREEN adds the Recovery-owned production adapter over an injected
Provider/Repository authority. Both request and observation are closed scalar
products with no locator, credential, command or target-session fields. The
adapter requires the returned plan/binding/transition, source/capability/
policy/finding, target observation and reserve-request tuple to reproduce the
exact request, sanitizes dependency errors, and is the only production source
of a private proof whose digest binds both the production bit and every
evidence field. The A2c1 `_test.go` issuer remains non-production.

R32 then added only
`TestPreflightServiceComposesIndependentEvidenceBeforeLock`. Its first run
proved target -> external -> lock ordering but produced the intended durable
authority RED:

```text
test_issuer_cannot_persist:
EvaluateAndPersist(test proof) error = <nil>, want ErrInvalidTargetPreflight
FAIL xirang/backend/internal/backupasset/recovery
```

The GREEN keeps a private copy of the exact external request/evidence in the
evaluator result. Commit validation now rejects test proofs, reconstructs the
request from the observed and then locked plan plus target snapshot, validates
the production evidence digest and security disposition before insert, and
repeats that check after `FOR UPDATE` and source revalidation before the
encrypted operation snapshot plus `draft -> preflight_ready` CAS. Target and
external authority calls remain transaction-free.

R33 added only `TestTargetPreflightCompositeEvidenceMatrix`. Source access,
overlap, reserves, target facts, security disposition, observed/expiry
intersection, snapshot fields and the persistence split were already green.
The production-adapter revision arms exposed one genuine error-classification
RED:

```text
capability_drift: error = recovery target preflight is unavailable,
want recovery preflight binding conflict
policy_drift: error = recovery target preflight is unavailable,
want recovery preflight binding conflict
finding_drift: error = recovery target preflight is unavailable,
want recovery preflight binding conflict
FAIL xirang/backend/internal/backupasset/recovery
```

The minimal correction preserves only Recovery's closed `conflict` and
`invalid` sentinels from the production adapter. Unknown Provider/Repository
errors remain sanitized unavailable, and caller cancellation/deadline identity
remains unchanged. The final matrix proves ordinary target/external rejection
does not write, while the existing security-only blocked snapshot remains the
sole ineligible product eligible for durable override processing.

Fresh V10 pre-bookkeeping gates passed:

```text
all Preflight normal                         PASS (0.275s)
all Preflight race                           PASS (2.215s)
whole Recovery normal                        PASS (16.371s)
whole Recovery race                          PASS (45.982s)
required real PostgreSQL normal, no skip     PASS (0.389s)
required real PostgreSQL race, no skip       PASS (2.016s)
go vet ./internal/backupasset/recovery       PASS
make lint-backend                            PASS (0 issues)
owned gofmt / git diff --check               PASS
Task 7 and parent Trellis validation         PASS
JSON and JSONL parsing                       PASS
manifest/dirty/outside/staged/future         145/93/0/0/0
```

The PostgreSQL fixture's persisted cluster no longer matched the container's
stale initialization environment, so two preliminary commands failed at
authentication before any test ran. The successful required gates used a
random temporary login with only `CREATE` on the test database; the same shell
removed its owned test objects and role, and a final query confirmed zero
`codex_a2c2_%` roles. The fixture was not restarted or reconfigured and no DSN
was printed or persisted.

Protected hashes, branch and baseline remained exact:

```text
go.mod b767fc9ef9376c651d0493329b710ac8dcf8d77e52686b847d244aff9f6d48fd
recovery/testdata/rsync_local_to_remote.json 2570bd4472541322d902c4cdf2fe43f247b69f19d335558830e749567f763892
branch codex/backup-assets-controlled-recovery
HEAD/main/origin-main 51771654a85967656fe1ca69686590b734ff9214
```

Inline review against PRD A2c2, design Sections 41.6--41.7 and implementation
R31--R33 found no remaining Critical or Important issue. Static review found
one production issuer, zero Repository/Runtime import from Recovery, no raw
external-evidence field, no new dependency/model/migration/setting/path and no
stage/commit/push/PR action. A shared Trellis spec update is unnecessary
because the contract is already frozen in the task-local PRD and design.

Post-bookkeeping focused verification passed freshly:

```text
R31/R32/R33 normal count 5                   PASS (0.435s)
R31/R32/R33 race count 5                     PASS (2.701s)
```

### V10 post-bookkeeping SQLite cancellation harness correction

The first post-bookkeeping whole Recovery normal run passed in `16.447s`, but
the whole race run exposed one existing test-fixture lifetime failure outside
the A2c2 product path:

```text
TestRecoveryPlanTargetRootResolutionFailsClosedBeforeWrites/context_cancellation:
service_test.go:2833: no such table: backup_asset_recovery_plans
FAIL xirang/backend/internal/backupasset/recovery
```

No A2c2 production frame reaches that test. Investigation reproduced the exact
context-cancellation arm under race `count=20`, while normal `count=20` passed.
The named shared-memory SQLite fixture had only its transaction connection
keeping the database alive; caller cancellation can make database/sql discard
that connection, so the postcondition query opened a new empty database with
the same name. This is schema lifetime loss, not a plan-service write or
migration result.

The minimal test-only correction reserves one dedicated `*sql.Conn` in that
single cancellation arm and closes it through `t.Cleanup`. It keeps the named
in-memory schema alive through the postcondition query without changing pool
limits, production context behavior or any Recovery product contract. Fresh
focused verification passed:

```text
exact cancellation arm race count 20         PASS (2.634s)
complete parent selector normal count 20     PASS (3.461s)
complete parent selector race count 5        PASS (4.337s)
```

Fresh final post-correction gates passed:

```text
whole Recovery normal                        PASS (16.202s)
whole Recovery race                          PASS (45.773s)
required real PostgreSQL normal, no skip     PASS (0.389s)
required real PostgreSQL race, no skip       PASS (2.042s)
go vet ./internal/backupasset/recovery       PASS
make lint-backend                            PASS (0 issues)
owned gofmt / git diff --check               PASS
Task 7 and parent Trellis validation         PASS
JSON and JSONL parsing                       PASS
manifest/dirty/manifest-dirty/protected      145/93/91/2
outside/staged/future-000070-71               0/0/0
temporary PostgreSQL roles after cleanup     0
```

Task 7 remains `in_progress`, A2c and A2c2 are
`focused_complete_checked`, and the parent remains `planning`. Branch,
HEAD/main/origin-main and both protected hashes remain the exact values
recorded above. V10 stops here before A2d with no stage, commit, push or PR.

## Task 7 A2d R34--R37 And V11 Focused Closure

At `2026-08-05T13:14:19+08:00`, only A2d resolver-bound published result read
is `focused_complete_checked`. Task 7 and Child 13 remain `in_progress`, the
parent remains `planning`, and program delivery remains 12/15. A2e
overwrite/Lstat/absence, A3 destructive cleanup/delete/tombstone,
runtime/main, orphan/quarantine and every Git delivery action remain closed.

R34 first proved that a structurally valid result-read observation could be
passed to `NewTargetResultReadPermit` without durable publication authority.
The GREEN adds an unexported comparable authority constructed only by the
durable resolver after exact owner, terminal job, executed plan, published
workspace, ready ResultSet, zero cleanup fence, marker and deadline checks. A
domain-separated private proof binds the exact permit, session, job/result
tuple, publication/fence, marker creator/fence, object, locator digest, bytes,
content digest, request and deadline. Resolver revalidation and the public
publication fingerprint include the private authority without exposing it.

R35 first proved the session factory rejected the new result-read purpose and
that the concrete method remained unconditionally unavailable. The GREEN
admits only literal `recovery_result_read`, reuses exact node and credential
revision checks, and uses only the validated job ID as correlation. Cross-
purpose, missing and substituted authority fail before dial/SFTP.

R36 first proved the concrete target had no result-read implementation. The
GREEN authenticates the workspace marker, validates canonical private parents
and an exact `0600` regular file, streams a bounded first-pass SHA-256 without
retaining plaintext, then reopens an unchanged second handle for sequential
delivery with a maximum 32 KiB read request. Ordinary and zero-byte payloads
use exactly two result opens and zero mutation calls.

R37 and review produced the following genuine REDs before their corresponding
product corrections:

```text
marker tamper                 exposed marker-codec sentinel instead of target changed
bytes plus raw read error     returned all bytes without the raw non-EOF failure
private marker/session drift old Content authorization could reauthorize
verify issuer                 retained a result-read private proof
first/second Open file+error  leaked the returned file handle
blocked Read plus Close       Close could not actively unblock Read
stat-only Reader              returned a non-nil interface containing a nil pointer
zero-byte post-open drift     Close error=<nil>, want exact ErrRecoveryTargetChanged
context cancellation order   [sftp ssh file], want [file sftp ssh]
```

The final GREEN normalizes visible marker/path/content/snapshot drift to
`ErrRecoveryTargetChanged`, preserves raw dependency ambiguity only as
`ErrRecoveryTargetUnavailable`, erases cross-purpose proof, closes files
returned with an Open error, returns a true nil stat reader and actively
interrupts blocked delivery reads. A2d now tracks every result-read file under
the target session with per-file at-most-once closure, so cancellation closes
all open files before SFTP and SSH. Zero-byte `Close` forcibly repeats exact
EOF, digest, snapshot, parent, marker and live-permit validation. The named R37
matrix also directly covers root, parent and final canonical alias rejection.

One expanded A1--A2d regression exposed a historical A2a-only expectation:

```text
TestRecoverySFTPTargetA2aDeferredMethodsOpenNoSession:
OpenOwnedResult error = invalid recovery target permit,
want ErrRecoveryTargetUnavailable
```

The concrete result-read arm is no longer deferred in A2d. The test-only
correction now requires an unsealed permit to return exact
`ErrInvalidTargetPermit` with zero resolver/dial calls, while Lstat, Delete,
RemoveOwnedJobDir and the other still-deferred arms remain unavailable. No
product code changed for this historical expectation correction.

Fresh final test gates after the last product edit passed:

```text
R34--R37 normal count 5                       PASS (0.186s)
R34--R37 race count 5                         PASS (1.931s)
cancel close-order race count 20              PASS (1.691s)
A1--A2d target/delivery normal count 5        PASS (4.183s)
A1--A2d target/delivery race count 5          PASS (15.597s)
whole Recovery normal                         PASS (16.303s)
whole Recovery race                           PASS (45.440s)
Content recovery-result normal count 5        PASS (0.198s)
Content recovery-result race count 5          PASS (1.916s)
required real PostgreSQL normal, no skip      PASS (23.116s)
required real PostgreSQL race, no skip        PASS (53.985s)
```

The PostgreSQL gates reused the healthy `xirang-c13-pg` PostgreSQL 18 fixture
on loopback port 55470. A random command-scoped login received only `CREATE`
on `xirang_test`; its DSN was neither printed nor persisted. `DROP OWNED` and
`DROP ROLE` ran from the same shell and a final query confirmed zero
`codex_a2d_final_%` roles.

Fresh static and pre-bookkeeping structural gates passed:

```text
go vet ./internal/backupasset/recovery       PASS
make lint-backend                            PASS (0 issues)
owned gofmt / git diff --check               PASS
target result-read mutation-call scan        PASS (zero)
target result-read direct logging scan       PASS (zero)
Task 7 and parent Trellis validation         PASS
JSON and JSONL parsing                       PASS
phase1/create/modify/total/unique/duplicates 9/55/81/145/145/0
dirty/manifest-dirty/protected-dirty         93/91/2
outside/staged/future-000070-71              0/0/0
create-present/modify-missing                0/0
```

Protected hashes and Git baseline remained exact:

```text
go.mod b767fc9ef9376c651d0493329b710ac8dcf8d77e52686b847d244aff9f6d48fd
recovery/testdata/rsync_local_to_remote.json 2570bd4472541322d902c4cdf2fe43f247b69f19d335558830e749567f763892
branch codex/backup-assets-controlled-recovery
HEAD/main/origin-main 51771654a85967656fe1ca69686590b734ff9214
staged 0
```

Inline review against all A2d PRD acceptance rows and design Sections
42.1--42.5 found no remaining Critical or Important issue after the two review
RED/GREEN corrections. The change adds no mutation arm, API, public locator,
runtime/main composition, model, table, migration, setting, dependency or
manifest path. A shared Trellis spec update is unnecessary because the exact
contract is task-local and the existing backend error/quality/SSH scope rules
remain unchanged.

Fresh post-bookkeeping validation reproduced the exact structural results
above. `meta.task_7.a2d_resolver_bound_published_result_read.status` is the only
new Task 7 completion field; Task 7 remains `in_progress`, the parent remains
`planning`, the dirty union remains 93, staged paths remain zero, and no
stage/commit/push/PR/CI/merge action was performed.

## Task 7 A2e1 R38--R41 And V12 Focused Closure

At `2026-08-05`, only A2e1 delete-oriented `Lstat` and exact absent `Verify`
are `focused_complete_checked`. Task 7 and Child 13 remain `in_progress`, the
parent remains `planning`, and program delivery remains 12/15. A2e2 overwrite,
A3 destructive cleanup/delete/tombstone, runtime/main, orphan/quarantine and
every Git delivery action remain closed.

R38 first proved that an otherwise exact sealed permit still left concrete
`Lstat` unavailable and opened zero sessions. R39 then proved all five present
cases (ordinary and zero-byte regular, directory, symlink and special) returned
unavailable. R40 proved exact missing still returned unavailable. R41 proved a
permit expiring during observation incorrectly returned success after only one
clock read. These were genuine observed REDs, not reconstructed output.

The initial GREEN added one private two-complete-observation path under the
purpose-exact `recovery_verify` session. Present results use lowercase
hexadecimal SHA-256 under
`xirang/recovery/sftp-delete-entry-identity/v1`; regular files bind a bounded
full-content SHA-256, symlinks bind exact non-followed `ReadLink` bytes, and
directory/special entries bind an empty payload fact. Present and missing
results reuse the exact existing `sftpt1:` metadata revision, while missing
keeps an empty identity. Absent `Verify` consumes the same observer. Every
success requires two equal complete observations plus live permit revalidation,
and the observer contains no target mutation or logging call.

V12 inline review against PRD A2e1 and design 43.1--43.6 found one Important
authority defect before closure: the A2a private proof bound plan/session/job/
mode/object/expiry but did not bind operation or expected-prior facts. Thus a
separately valid overwrite permit could open delete-oriented `Lstat`. The first
review RED was the expected compile failure after the test required those
missing issuer inputs:

```text
target_test.go: too many arguments in call to issueTargetVerifyPermit
have (... RecoveryOperationKind, ExpectedTargetIdentity)
want (... TargetMode)
```

The minimal correction evolves only the ephemeral private proof to
`xirang/recovery/target-verify-permit-proof/v2`, appending operation kind and
expected-prior kind/digest to the existing proof fields. `Lstat` and absent
`Verify` now require literal delete plus expected-present before any resolver,
SSH or SFTP call. This remains one `TargetVerifyPermit`; no second delete
authority was added.

A second genuine behavioral RED then proved a structurally valid substituted
operation/prior could still be resealed after locked load:

```text
TestRecoveryInterruptedOperationHandoffCarriesLockedTargetSessionBinding:
substituted handoff operation/prior verify permit error=<nil>,
want ErrRecoveryWorkerFenceLost
```

The final GREEN makes the sole worker issuer reproduce immutable job-item
operation/prior fields and the existing operation digest before sealing. Both
delete pause and consumed-delete production paths now prove exact delete/
present authority; create/overwrite/skip present Verify remains compatible.
No `executor.go` product change was needed.

Fresh dynamic gates after the final product correction passed:

```text
R38 plus exact authority normal                      PASS (0.052s)
R38--R41 plus proof/issuer normal count 5           PASS (0.560s)
R38--R41 plus proof/issuer race count 5             PASS (3.205s)
delete production issuance normal count 5           PASS (0.296s)
delete production issuance race count 5             PASS (2.450s)
R38--R41 focused race count 5                       PASS (1.838s)
whole Recovery normal                               PASS (16.891s)
whole Recovery race                                 PASS (46.441s)
required real PostgreSQL normal, no skip            PASS (23.373s)
required real PostgreSQL race, no skip              PASS (54.841s)
```

The required PostgreSQL gates reused the healthy `xirang-c13-pg` PostgreSQL 18
fixture on loopback port 55470. A command-scoped random role received only
database `CONNECT` and `CREATE`; its DSN was neither printed nor persisted.
`DROP OWNED` and `DROP ROLE` ran from the same wrapper, and the final role count
for `codex_a2e1_%` was zero. One preliminary wrapper exited before role creation
because its noninteractive shell did not define `EPOCHSECONDS`; no test ran in
that attempt.

Fresh static and structural gates passed:

```text
go vet ./internal/backupasset/recovery                 PASS
make lint-backend                                      PASS (0 issues)
owned gofmt / tracked git diff --check                 PASS
A2e1 observer mutation-call and direct-log scans       PASS (zero)
Task 7 and parent Trellis validation                   PASS
JSON and JSONL parsing                                 PASS
phase1/create/modify/total/unique/cross-duplicates     9/55/81/145/145/0
dirty/manifest-dirty/protected-dirty/outside           93/91/2/0
staged/create-present/modify-missing/future-000070-71  0/0/0/0
```

Protected hashes and the Git baseline remained exact:

```text
go.mod b767fc9ef9376c651d0493329b710ac8dcf8d77e52686b847d244aff9f6d48fd
recovery/testdata/rsync_local_to_remote.json 2570bd4472541322d902c4cdf2fe43f247b69f19d335558830e749567f763892
branch codex/backup-assets-controlled-recovery
HEAD/main/origin-main 51771654a85967656fe1ca69686590b734ff9214
staged 0
```

Inline review found no remaining Critical or Important A2e1 issue after the
two authority corrections. A shared Trellis spec update is unnecessary: this
is a task-local, unshipped private Recovery capability contract, and it does
not establish a reusable project convention beyond the existing backend
security, error, privacy and SSH-purpose specs. The change adds no remote
mutation, public API, path, model, table, migration, setting, dependency or
manifest row.

Fresh post-bookkeeping regression passed after the Child metadata, acceptance
rows, evidence and parent status summary were updated:

```text
A1--A2e1 broad target/issuer normal              PASS (1.455s)
A2e1 target/issuer race count 5                  PASS (5.293s)
```

Task 7 and Child 13 remain `in_progress`; the parent remains `planning` at
12/15 delivered. The parent note now truthfully distinguishes completed A2d
and A2e1 from still-closed A2e2. No stage, commit, push or PR action ran.

## Task 7 A2e2 + A3a Approved Planning Amendment

At `2026-08-05`, the user approved option A after A2e1 focused closure. This is
a planning receipt only: A2e2/A3a product code, RED/GREEN credit and completion
remain not started. Task 7 and Child 13 stay `in_progress`, the parent stays
`planning`, and program delivery stays 12/15.

Current-code research confirmed that `pkg/sftp` standard `Rename` is
no-overwrite while `PosixRename` is replacement, and the target exposes no CAS,
exchange or external-writer lock. Direct replacement remains rejected because
an external update between validation and rename could be silently overwritten.
The approved protocol instead prepares a verified post sibling, captures final
to an authenticated same-parent prior sibling with standard Rename, verifies
the captured exact prior while final is absent, and publishes post with another
standard no-overwrite Rename. The accepted availability cost is bounded by
expected prior bytes, context/deadline and transport progress, not a fixed
wall-clock promise.

Planning review found that prior/post sidecars alone were insufficient for the
remote-success/DB-crash window. The corrected design adds deterministic
authenticated `intent` and `published` marker siblings. `WriteAtomic` returns
success only with exact final post, exact published marker and exact absence of
prior/post/intent. It retains published until the existing immutable operation
checkpoint and target-chain revision are durable. A fresh checkpoint-bound
private finalize proof then validates final/artifact state, removes only that
marker, and proves absence before another item or terminal completion. Crash
before checkpoint re-enters `WriteAtomic`; crash after checkpoint replays
finalize; crash after marker removal accepts exact idempotent absence.

The plan reuses the immutable job item, historical target-locator cleanup key
version, current operation checkpoint and active attempt/node/source fences. It
adds no table, column, checkpoint phase, migration number, public route,
setting, dependency or manifest path. Exact product boundaries are the existing
`target.go`, `worker.go`, `executor.go` and their three test files. R42--R47 and
V13 remain unchecked and require separate product-code implementation approval.

Inline planning review closed two unsafe ambiguities before implementation.
First, a mismatched prior may be restored automatically only by the same
unambiguous session that just completed capture and immediately observed that
winner; re-entry mismatch or ambiguous capture preserves all evidence and never
auto-restores. Second, the artifact token is stable across attempts, while a
fresh finalize proof binds both a predecessor's immutable checkpoint and the
current takeover fences. A takeover after checkpoint but before source-failure
projection must also rerun durable source revalidation rather than infer a
matched source from checkpoint presence.

A3a is deliberately narrow: it reconciles only a successful overwrite's
checkpoint-bound published marker while execution fences remain active.
Mismatched captured priors, external final winners, tampered/unknown artifacts,
terminal orphan/quarantine, `Delete`, `RemoveOwnedJobDir`, tombstones, terminal
cleanup-lease release, runtime/main, whole Task 7 review and all Git delivery
actions remain closed. No stage, commit, push, PR, goal, heartbeat, branch switch
or worktree action ran during this planning amendment.

## Task 7 R42/G42 Overwrite Artifact Authority and Binding (2026-08-05)

The approved A2e2 product implementation began with the two exact R42
selectors and no remote mutation. The genuine RED failed at compile time only
because `recoveryOverwriteArtifactBindingInput`, the historical-key derivation
function, and the new item/operation/prior-bytes/artifact proof fields did not
exist:

```text
target_test.go:2720:35: undefined: recoveryOverwriteArtifactBindingInput
target_test.go:7033:20: undefined: newRecoveryOverwriteArtifactBinding
target_test.go:7052:3: unknown field jobItemID in targetItemWritePermitProof
target_test.go:7053:3: unknown field operationDigest in targetItemWritePermitProof
target_test.go:7058:3: unknown field expectedPriorBytes in targetItemWritePermitProof
target_test.go:7061:3: unknown field artifacts in targetItemWritePermitProof
FAIL xirang/backend/internal/backupasset/recovery [build failed]
```

GREEN moved the private item-write proof to v2 and bound the immutable job item
ID, operation digest, exact prior bytes and complete comparable overwrite
artifact binding. The locked in-place overwrite handoff uses exactly the job
item's historical cleanup-key version, clears the returned key bytes after
derivation, and seals the result into the purpose-exact write permit. Create
continues with an empty artifact arm; isolated overwrite remains closed.

The HMAC-SHA-256 artifact input is length-framed and binds key version,
plan/job/item/operation, target mode, node/root/object/root revision, private
final locator and exact prior/post facts. Its canonical base64url token is the
only variable filename component under the fixed
`.xirang-recovery-overwrite-v1-` prefix and the closed intent/prior/post/
published suffixes. Intent and published are deterministic five-field JSON
documents whose tags use a separate framed HMAC domain. Tests independently
recompute both domains, verify field sensitivity, size/path safety and raw-input
absence, and prove private permits/authorities do not serialize the token,
components or documents.

The exact overwrite authority now validates literal in-place + overwrite +
present prior + exact prior bytes + authenticated artifact shape. R43 remains
the first step allowed to create remote intent/post artifacts, so even the
valid R42 overwrite returns exact `ErrRecoveryTargetUnavailable` before entropy,
resolver, SSH or SFTP; every substituted mode/item/operation/key/prior/post/
object/token/document/proof returns exact `ErrInvalidTargetPermit` at the same
zero-dependency boundary.

Fresh verification after the implementation and inline review:

```text
R42 focused normal                                      PASS (0.087s)
R42 focused race count 5                                PASS (2.043s)
item-proof/create/WriteAtomic regressions normal         PASS (0.136s)
item-proof/create/WriteAtomic regressions race count 5   PASS (2.681s)
whole Recovery normal                                   PASS (16.417s)
whole Recovery race                                     PASS (46.096s)
go vet ./internal/backupasset/recovery                   PASS
make lint-backend                                        PASS (0 issues)
owned gofmt / git diff --check                           PASS
```

The protected hashes and Git baseline remained exact:

```text
go.mod b767fc9ef9376c651d0493329b710ac8dcf8d77e52686b847d244aff9f6d48fd
recovery/testdata/rsync_local_to_remote.json 2570bd4472541322d902c4cdf2fe43f247b69f19d335558830e749567f763892
branch codex/backup-assets-controlled-recovery
HEAD/main/origin-main 51771654a85967656fe1ca69686590b734ff9214
staged 0
```

R42/G42 and only the first A2e2+A3a acceptance row are focused complete.
R43--R47 and V13 remain unchecked. No remote sidecar creation, rename, remove,
checkpoint/finalize split, model, migration, route, setting, dependency or
manifest path was added. Task 7 and Child 13 remain `in_progress`; the parent
remains `planning` at 12/15 delivered. No stage, commit, push, PR, goal,
heartbeat, branch switch or worktree action ran.

## Task 7 R43/G43 Authenticated Intent and Post Preparation (2026-08-05)

The genuine R43 RED compiled and failed because the exact in-place overwrite
authority still stopped at the R42 closed boundary. Ordinary and zero-byte
cases returned the expected unavailable sentinel but left `intent` absent;
malformed/wrong-phase/wrong-key/tampered intent, post collisions and canonical
parent drift also returned unavailable instead of their required exact changed
classification. Representative output was:

```text
ordinary_payload: intent="" ... no such file or directory, want exact authenticated document
zero-byte_payload: intent="" ... no such file or directory, want exact authenticated document
intent_collision_matrix/*: error=recovery target unavailable, want exact ErrRecoveryTargetChanged
post_collision_matrix/*: error=recovery target unavailable, want exact ErrRecoveryTargetChanged
parent_alias_symlink_and_type_drift/*: error=recovery target unavailable, want exact ErrRecoveryTargetChanged
FAIL xirang/backend/internal/backupasset/recovery
```

GREEN opens only the exact R42 literal in-place overwrite authority. It derives
all artifact paths from the sealed sibling components inside the canonical
final parent, proves the final remains the exact expected regular-file prior,
and classifies the complete pre-capture tuple before mutation. Fresh preparation
uses direct `O_WRONLY|O_CREATE|O_EXCL`, mode 0600, bounded exact bytes/digest/EOF,
mandatory `Sync`, close/reopen and stable full-content verification for the
authenticated intent document and post payload. Exact intent-only retry creates
only post; exact intent+post replay reads no caller source and performs no file
mutation. A post without intent is never adopted even when its bytes are exact.

The focused matrix covers ordinary and zero bytes, short/extra/digest-mismatched
source and reopened post, Sync/close/reopen failures, permission/unsupported
open failures, malformed/wrong-phase/wrong-key/tampered intent, regular/
directory/symlink/special post collisions, parent alias/symlink/type drift,
exclusive flags, 0600 mode and deterministic same-parent paths. Every collision
is preserved. The R43 helper contains no `Rename`, `Remove`, `PosixRename` or
direct logging call, so final-to-prior capture, publish, restore, acknowledgement
and cleanup remain owned by R44--R47.

Fresh pre-bookkeeping verification:

```text
R42--R43 focused normal                               PASS (0.104s)
R42--R43 focused race count 5                         PASS (2.338s)
A2b WriteAtomic/create normal                         PASS (0.135s)
A2b marker/create normal                              PASS (0.063s)
A2b WriteAtomic/marker/create race count 5            PASS (2.263s)
whole Recovery normal                                 PASS (18.033s)
whole Recovery race                                   PASS (47.259s)
go vet ./internal/backupasset/recovery                 PASS
make lint-backend                                      PASS (0 issues)
owned gofmt / static R43 mutation-log scan             PASS
Task 7 and parent Trellis validation                   PASS (17/18 and 0/0)
JSON / JSONL parsing                                   PASS
```

The dirty union remains 93 paths, with no staged path and no new manifest path.
The protected hashes and Git baseline remain exact:

```text
go.mod b767fc9ef9376c651d0493329b710ac8dcf8d77e52686b847d244aff9f6d48fd
recovery/testdata/rsync_local_to_remote.json 2570bd4472541322d902c4cdf2fe43f247b69f19d335558830e749567f763892
branch codex/backup-assets-controlled-recovery
HEAD/main/origin-main 51771654a85967656fe1ca69686590b734ff9214
staged 0
```

R43/G43 is focused complete_checked only. R44--R47 and V13 remain unchecked;
Task 7 and Child 13 remain `in_progress`, the parent remains `planning`, and the
program remains 12/15 delivered. No model, migration, route, setting, dependency,
manifest, stage, commit, push, PR, goal, heartbeat, branch switch or worktree
action was added. A shared spec update is unnecessary because this is the
already approved task-local private overwrite protocol, not a new repository
convention.

## Task 7 R44/G44 Exact Prior Capture and Mismatched Winner Restore (2026-08-05)

The two required R44 selectors were added before product behavior. The genuine
RED compiled and failed because an exact prepared tuple still stopped at the
R43 boundary and performed no capture; every raced mismatch returned the
closed unavailable sentinel instead of same-invocation restoration:

```text
TestRecoverySFTPTargetOverwriteCapturesExactPriorWithoutReplacement:
capture renames=[], want one standard final-to-prior rename
TestRecoverySFTPTargetOverwriteRestoresCapturedMismatch/{regular,directory,symlink,special}:
error=recovery target unavailable, want exact ErrRecoveryTargetChanged
FAIL xirang/backend/internal/backupasset/recovery
```

GREEN opens only the next prepared-to-captured transition. It revalidates the
exact final prior, authenticated intent, exact post, absent future artifacts,
canonical parent and live permit, then performs one standard no-overwrite
`Rename(final, prior)`. While final remains exactly absent, the captured entry
is observed twice with the existing A2e1 non-following entry machinery. A
regular accepted prior must reproduce the exact expected digest, bytes, EOF and
stable snapshot. Exact capture stops before R45 publication.

A mismatched regular, directory, symlink or special winner is restored only
when this invocation received an unambiguous successful capture rename and
immediately obtained a stable captured observation. Before the no-overwrite
`Rename(prior, final)`, the target revalidates intent/post/published evidence,
reobserves the captured entry, revalidates canonical parents and live authority,
and proves final absent. It then proves the restored final equals the same
session observation. Capture or restore ambiguity, permission/unsupported
failure, external final occupation, authenticated artifact drift, captured
entry drift/disappearance, restored-entry verification failure and re-entry
with a mismatched prior perform no automatic inference, cleanup or publish.

The matrix preserves all observable final/prior/intent/post evidence for every
unresolved state, performs no `Remove` or replacement rename, and leaves the
published marker absent. Prepared replay remains source-free. R43 fixtures now
inject their capture boundary explicitly so their focused preparation contract
does not accidentally exercise the newly opened R44 transition.

Fresh pre-bookkeeping and post-bookkeeping verification:

```text
R42--R44 plus A2e1 focused normal                     PASS (0.119s)
R42--R44 plus A2e1 focused race count 5               PASS (2.630s)
whole Recovery normal                                 PASS (16.406s)
whole Recovery race                                   PASS (45.950s)
go vet ./internal/backupasset/recovery                 PASS
make lint-backend                                      PASS (0 issues)
owned gofmt / static capture-restore boundary scan     PASS
Task 7 and parent Trellis validation                   PASS (17/18 and 0/0)
JSON / JSONL parsing                                   PASS
```

The dirty union remains 93 paths, with no staged path and no new manifest path.
The protected hashes and Git baseline remain exact:

```text
go.mod b767fc9ef9376c651d0493329b710ac8dcf8d77e52686b847d244aff9f6d48fd
recovery/testdata/rsync_local_to_remote.json 2570bd4472541322d902c4cdf2fe43f247b69f19d335558830e749567f763892
branch codex/backup-assets-controlled-recovery
HEAD/main/origin-main 51771654a85967656fe1ca69686590b734ff9214
staged 0
```

R44/G44 is focused complete_checked only. R45--R47 and V13 remain unchecked;
Task 7 and Child 13 remain `in_progress`, the parent remains `planning`, and the
program remains 12/15 delivered. No R45 publish/acknowledgement/cleanup, model,
migration, route, setting, dependency, manifest, stage, commit, push, PR, goal,
heartbeat, branch switch or worktree action was added. A shared spec update is
unnecessary because this is the already approved task-local private overwrite
protocol, not a new repository convention.

## Task 7 R45/G45 Publish, Acknowledge and Crash-State Replay (2026-08-05)

The two required R45 selectors were added before product behavior. The genuine
RED proved that the existing linear R43/R44 implementation could not classify
or resume captured, published or acknowledged tuples and could not return the
stable post result:

```text
TestRecoverySFTPTargetOverwritePublishesVerifiedPost:
result={BytesWritten:0 IdentityDigest: TargetRevision:}
error=recovery target changed, want exact stable result
TestRecoverySFTPTargetOverwriteCrashStateMatrix/{fresh,intent-only,prepared}:
error=recovery target unavailable, want exact <nil>
TestRecoverySFTPTargetOverwriteCrashStateMatrix/{captured,published-unacknowledged,
acknowledged_with_residue,acknowledged_cleaned_replay}:
error=recovery target changed, want exact <nil>
FAIL xirang/backend/internal/backupasset/recovery
```

GREEN replaces the overwrite-only linear stop with one bounded transition
driver over a complete final/intent/prior/post/published classifier. Every
regular fact is a canonical stable full-content observation that distinguishes
exact prior, exact 0600 post, exact absence and conflict. Intent and published
facts require the exact authenticated document, 0600 mode and a stable final
canonical snapshot. Matching final content alone, malformed/tampered markers,
unknown artifacts and unexplained tuples remain closed changed states.

The recognized forward transitions are fresh or exact intent-only preparation,
prepared capture, captured standard no-overwrite `Rename(post, final)`, exact
final-post verification, exclusive authenticated published-marker creation,
and acknowledged cleanup. Every publish, marker-create and residue-remove
mutation is preceded by a complete state check plus canonical-parent and live
permit validation. Rename/remove dependency errors return unavailable without
visibility inference; a fresh session resumes only when the complete tuple
selects one authenticated state. Cleanup removes at most one exact owned
prior/post/intent residue per transition, proves absence, and permanently
retains published. Success is returned only from exact final post + exact
published + exact absence of all other artifacts and reproduces the existing
A2a `sftp1:` bytes/digest/revision without reading caller source.

The crash matrix covers fresh, intent-only, prepared, captured,
published-unacknowledged, acknowledged with residue and cleaned acknowledged
replay, plus interruption before/after intent creation, post open/write/Sync/
close/verify, capture and captured read, publish and final verification,
published creation, prior/intent removal and SFTP session close. Exact
interrupted states resume with a fresh target/session; a partial unknown post
is preserved and closes changed. Ambiguous capture and publish errors are not
interpreted in the failing invocation; exact resulting tuples resume on the
fresh invocation. A concurrent final is preserved with all evidence and fails
closed. Exact acknowledged replay performs no caller-source read, second
publish, marker creation, chmod or cleanup mutation.

Inline `trellis-check` review found two stable-snapshot defects after the first
GREEN. Each received a genuine focused RED before its fix:

```text
regular_fact_final_canonical_drift_is_rejected:
late regular drift error=<nil>, want exact changed
marker_fact_final_canonical_drift_is_rejected:
late marker drift error=<nil>, want exact changed
```

Both observers had validated the last canonical type/path but had not compared
that final `FileInfo` to the just-read stable snapshot. GREEN now requires exact
snapshot equality at that final observation for regular facts and authenticated
overwrite markers. No shared spec update is needed because this implements the
already approved task-private overwrite protocol rather than a new repository
convention.

Fresh post-review verification:

```text
R42--R45 focused normal                                PASS (0.235s)
R42--R45 focused race count 5                          PASS (4.538s)
whole Recovery normal                                  PASS (18.513s)
whole Recovery race                                    PASS (50.151s)
go vet ./internal/backupasset/recovery                  PASS
make lint-backend                                       PASS (0 issues)
owned gofmt / git diff --check / static boundary scan   PASS
Task 7 and parent Trellis validation                    PASS (17/18 and 0/0)
task/parent JSON and Task 7 JSONL parsing                PASS
```

The exact Git and protected-file evidence remains:

```text
go.mod b767fc9ef9376c651d0493329b710ac8dcf8d77e52686b847d244aff9f6d48fd
recovery/testdata/rsync_local_to_remote.json 2570bd4472541322d902c4cdf2fe43f247b69f19d335558830e749567f763892
branch codex/backup-assets-controlled-recovery
HEAD/main/origin-main/remote-main 51771654a85967656fe1ca69686590b734ff9214
dirty union 93 paths
staged 0
```

R45/G45 is `focused_complete_checked`. R46--R47 and V13 remain unchecked;
Task 7 and Child 13 remain `in_progress`, the parent remains `planning`, and
program delivery remains 12/15. No R46 checkpoint/finalize, R47 broad
error/resource/privacy implementation, model, migration, route, setting,
dependency, manifest path, stage, commit, push, PR, goal, heartbeat, branch
switch or worktree action was added.

## Task 7 R46/G46 Durable Checkpoint Before Marker Finalize (2026-08-05)

R46 began with the required ordering and replay assertions against the existing
ordinary execution fixture and recording target. The genuine RED sequence
proved that successful in-place overwrite still returned through the old
single-stage projection path and had no checkpoint-bound finalize operation:

```text
checkpoint ordering: finalize calls=0, want 1
checkpoint re-entry: finalize calls=1, want 2
predecessor takeover: prepare ordinary first write: recovery worker fence lost
checkpointed source failure: finalize calls=1, want 2
takeover source failure: finalize ran, but the job remained running
FAIL xirang/backend/internal/backupasset/recovery
```

GREEN adds a closed `TargetFinalizeOverwritePermit` and request. Its private
proof binds job, item, operation checkpoint and operation digest; the immutable
checkpoint's historical attempt/fences; the current attempt, node lease and
source fences; current target-chain plus checkpoint prior/next revisions; exact
prior/post facts; target object; and the stable authenticated overwrite artifact
binding. The stable artifact binding remains attempt-independent, while every
finalize authority is freshly sealed from locked durable state under the
current takeover fences.

The concrete SFTP target opens only `recovery_write`, derives every sibling path
inside the exact canonical final parent, proves final is the exact post and
intent/prior/post are absent, and accepts only the exact authenticated
`published` marker or its checkpoint-authorized idempotent absence. Its only
mutation is removal of that derived published path, followed by a second exact
absence observation. A remove that succeeds remotely but returns an error is
therefore safely replayed with the same durable checkpoint proof.

Ordinary in-place overwrite projection is now deliberately two-stage:

```text
WriteAtomic publication
-> immutable item/checkpoint/target-chain commit with job and leases active
-> fresh locked finalize permit
-> published-marker finalize
-> source-failure projection, next item, or existing completion transaction
```

Every completed overwrite is reconciled from validated durable history before
pending item selection. Predecessor checkpoints can be finalized only after a
fresh takeover source revalidation and only when the same invocation carries
the checkpoint ID it actually reconciled. Source drift/failure follows the same
ordering, and no-pending last-item re-entry repeats idempotent finalize before
the existing transaction closes the attempt and releases source/node leases.
No row, column, checkpoint phase, migration, setting, public route, dependency
or manifest path was added.

The focused matrix covers checkpoint ordering with active attempt/source/node
authority, checkpoint-to-finalize interruption, predecessor takeover, source
drift/failure in the same and successor attempts, repeated finalize, several
overwrites before a following item, create/skip/last-overwrite completion,
remote success before checkpoint followed by adoption, marker-remove ambiguity,
and crash after marker finalize before completion. The permanent PostgreSQL
selector uses the real paired `000069` schema and is forced not to skip.

Fresh final verification used these commands without printing the task-local
PostgreSQL password or DSN:

```text
go test ./internal/backupasset/recovery -run '<12 R46 selectors>' -count=5
PASS (1.645s)

go test -race ./internal/backupasset/recovery -run '<12 R46 selectors>' -count=5
PASS (8.174s)

stored_password=$(docker exec xirang-c13-pg sh -lc 'printf %s "$POSTGRES_PASSWORD"')
encoded_password=$(PASSWORD_VALUE="$stored_password" python3 -c 'import os, urllib.parse; print(urllib.parse.quote(os.environ["PASSWORD_VALUE"], safe=""))')
REQUIRE_POSTGRES_RECOVERY_TEST=1 TEST_POSTGRES_DSN="postgres://postgres:${encoded_password}@127.0.0.1:55470/xirang_test?sslmode=disable" go test ./internal/backupasset/recovery -run '^TestRecoveryOverwriteCheckpointPrecedesPublishedMarkerFinalizePostgres069$' -count=1
PASS (1.879s)

REQUIRE_POSTGRES_RECOVERY_TEST=1 TEST_POSTGRES_DSN="postgres://postgres:${encoded_password}@127.0.0.1:55470/xirang_test?sslmode=disable" go test -race ./internal/backupasset/recovery -run '^TestRecoveryOverwriteCheckpointPrecedesPublishedMarkerFinalizePostgres069$' -count=1
PASS (3.589s)

go test ./internal/backupasset/recovery -count=1      PASS (17.094s)
go test -race ./internal/backupasset/recovery -count=1 PASS (48.740s)
go vet ./internal/backupasset/recovery                 PASS
make lint-backend                                      PASS (0 issues)
owned gofmt / git diff --check                         PASS
Child and parent task.py validate                      PASS (17/18 and 0/0)
task/parent JSON and Task 7 JSONL parsing              PASS
```

The task-owned PostgreSQL 18.4 container remained healthy and was neither
restarted nor replaced. Both forced selectors cleaned their temporary schemas;
post-run catalog checks reported `schemas=0` and `roles=0` for the
`xirang_recovery_%` prefix. Inline contract review found no remaining
Critical/Important R46 issue: terminal completion and lease release are behind
successful finalize, historical/current fences and artifact/post facts are
sealed, source-failure paths carry only a genuinely reconciled predecessor ID,
and the finalize helper contains exactly one `Remove(paths.published)` mutation.
A shared spec update is unnecessary because this is the approved task-private,
still-unshipped Recovery protocol rather than a reusable repository convention.

The Git and protected-file evidence remains exact:

```text
go.mod b767fc9ef9376c651d0493329b710ac8dcf8d77e52686b847d244aff9f6d48fd
recovery/testdata/rsync_local_to_remote.json 2570bd4472541322d902c4cdf2fe43f247b69f19d335558830e749567f763892
branch codex/backup-assets-controlled-recovery
HEAD/main/origin-main/remote-main 51771654a85967656fe1ca69686590b734ff9214
remote child branch absent
dirty union 93 paths
staged 0
```

R46/G46 is now `focused_complete_checked`. R47/G47 and V13 remain unchecked and
must receive a later explicit continuation. Task 7 and Child 13 remain
`in_progress`, the parent remains `planning`, and program delivery remains
12/15. No stage, commit, push, PR, CI, merge, goal, heartbeat, branch switch or
worktree action ran, and this checkpoint does not claim A2e2/A3a, Task 7 or
Child 13 completion.

## Task 7 R47/G47 Authority, Error, Resource and Privacy Closure (2026-08-05)

R47 began with the required single comprehensive target selector. The first
genuine RED showed that `%+v` and `%#v` expanded private overwrite authority
internals even though JSON already omitted them:

```text
formatted overwrite products leaked "existing/nested/target.bin"
FAIL xirang/backend/internal/backupasset/recovery
```

The minimal GREEN gives `TargetObjectRef`, `TargetWritePermit`,
`TargetWriteAtomicRequest`, `TargetFinalizeOverwritePermit` and
`TargetFinalizeOverwriteRequest` one uniform redacted `String`/`GoString`
representation. It does not change their validation, JSON shape or authority.

The next genuine resource RED exercised dependency APIs that returned a
non-nil owned resource together with an error:

```text
dial SFTP/SSH close=0/0, want 0/1
SFTP opener SFTP/SSH close=0/1, want 1/1
FAIL xirang/backend/internal/backupasset/recovery
```

The minimal product correction closes a non-nil SSH client returned with a
dial error and a non-nil SFTP client returned with an opener error exactly
once. Resolver behavior, successful session ownership, sanitized sentinels and
context precedence are unchanged.

The completed matrix covers resolver, dial, SFTP opener, `Open`, `OpenFile`,
read, write, file `Stat`, `Sync`, capture/publish/restore rename, residue and
finalize remove, file close, SFTP close and SSH close. Every acquired file,
SFTP client and SSH client is closed exactly once. Error paths return a zero
result and the exact closed sentinel; a table repeats all dependency and close
boundaries for both active cancellation and a real expiring
`context.WithDeadline`, with exact `context.Canceled` or
`context.DeadlineExceeded` winning raw dependency and close noise.

Live proof revocation is independently injected immediately before intent
create, post create, capture rename, same-session mismatch restore, publish
rename, published-marker create, prior/post/intent removal and checkpoint-bound
published-marker finalize. Each case returns exact `ErrInvalidTargetPermit`
and proves the next mutation did not occur; the one case that had already
captured a mismatched winner preserves that evidence and performs no restore
after revocation.

Errors, zero/results, permits, requests, `%v`/`%+v`/`%#v`, JSON, SSH dial audit
input and captured logs are scanned for recognizable host, user, credential,
root, final/private locator, artifact token/component/document, prior/post
content, raw dependency and SFTP-status values. All scans pass. Captured target
logs are empty and a static source gate finds no direct `logger.` or `log.` call
in `target.go`.

Fresh dynamic verification:

```text
R47 focused normal                                      PASS (1.409s)
R47 focused race count 5 after final lint edit          PASS (8.801s)
R42--R47 focused normal                                 PASS (1.967s)
R42--R47 focused race count 5                           PASS (17.555s)
A1--A2e2 all target/worker/executor TestRecovery normal PASS (11.270s)
whole Recovery normal                                   PASS (18.243s)
whole Recovery race                                     PASS (50.014s)
go vet ./internal/backupasset/recovery                  PASS
make lint-backend                                       PASS (0 issues)
owned gofmt / whole git diff --check / static log scan  PASS
```

Structural verification after the final product and test edits:

```text
Task 7 and parent task.py validate                      PASS (17/18 and 0/0)
task/parent JSON and JSONL parse                        PASS (6 files, 37 JSONL rows)
phase1/create/modify/total/unique                        9/55/81/145/145
dirty/manifest-dirty/protected/outside/staged/future    93/91/2/0/0/0
go.mod SHA-256 b767fc9ef9376c651d0493329b710ac8dcf8d77e52686b847d244aff9f6d48fd
recovery/testdata/rsync_local_to_remote.json SHA-256 2570bd4472541322d902c4cdf2fe43f247b69f19d335558830e749567f763892
branch codex/backup-assets-controlled-recovery
HEAD/main/origin-main/remote-main 51771654a85967656fe1ca69686590b734ff9214
remote child branch absent
```

R47/G47 is `focused_complete_checked`. V13 remains deliberately unchecked and
owns the final A2e2+A3a acceptance/design review. Task 7 and Child 13 remain
`in_progress`, the parent remains `planning`, and program delivery remains
12/15. General A3 delete/RemoveOwnedJobDir/tombstone, terminal cleanup-lease
release, orphan/quarantine, runtime/main, whole Task 7 review and every stage,
commit, push, PR, CI or merge action remain open. No goal, heartbeat, branch
switch, worktree or Git delivery action ran.

## Task 7 V13 A2e2+A3a Focused Review Closure (2026-08-05)

V13 re-read the controlling A2e2+A3a PRD acceptance, design 44.1--44.7 and
R42--R47 plan, then traced the complete target/worker/executor data flow. The
review confirmed that callers cannot provide artifact paths, all four siblings
are derived from the sealed historical-key binding inside the canonical final
parent, only standard no-overwrite `Rename` is exposed, and a matching final
alone never proves an ambiguous rename/remove succeeded. The same-invocation
mismatch restore remains ephemeral and re-entry preserves unresolved evidence.

The durable path remains ordered as publication -> immutable operation
checkpoint/item/target-chain projection -> fresh checkpoint-bound finalize
proof -> published-marker removal -> source disposition/next item/terminal
completion. Takeover and pre-source-failure paths reconcile completed overwrite
checkpoints first. `Delete`, `RemoveOwnedJobDir`, general orphan/quarantine and
terminal cleanup-lease release remain closed. Static review also confirmed the
ordinary 64 KiB+17 and zero-byte cases exercise bounded multi-read/EOF behavior,
and raw Provider/SSH/SFTP/private artifact errors are collapsed before worker or
executor diagnostics.

No remaining Critical or Important product defect was found. The structural
manifest gate did find one genuine task-artifact RED:

```text
phase1=9 create=58 modify=81 total=148 unique=145 duplicates=3
dirty=93 manifest_dirty=91 protected_dirty=2 outside=0 staged=0
create_present_at_head=0 modify_missing_at_head=0 future_000070_71=0
```

The create block repeated `executor_test.go`, `worker.go` and `worker_test.go`.
The minimal correction deleted only those three duplicate rows. The exact same
gate then returned:

```text
phase1=9 create=55 modify=81 total=145 unique=145 duplicates=0
dirty=93 manifest_dirty=91 protected_dirty=2 outside=0 staged=0
create_present_at_head=0 modify_missing_at_head=0 future_000070_71=0
```

Fresh dynamic evidence after the final R47 product state was:

```text
R42--R47 plus checkpoint/finalize focused normal       PASS (1.849s)
R42--R47 plus checkpoint/finalize race count 5         PASS (17.672s)
A1--A2e2 broad TestRecovery normal                     PASS (15.703s)
whole Recovery normal                                  PASS (18.181s)
whole Recovery race fresh recorded rerun               PASS (49.188s)
required-real PostgreSQL all Postgres normal, no skip  PASS (6.885s)
required-real PostgreSQL all Postgres race, no skip    PASS (10.038s)
PostgreSQL schema/role residue before and after        0/0
```

The PostgreSQL 18 fixture `xirang-c13-pg` remained running and healthy on
loopback port 55470; its password was read and URL-encoded only inside each
command and neither the password nor DSN was printed. The container was not
restarted, replaced or removed.

Fresh static and structural evidence passed:

```text
go vet ./internal/backupasset/recovery                 PASS
make lint-backend                                      PASS (0 issues)
owned gofmt / git diff --check                         PASS
replacement/PosixRename, caller-path and direct-log scans PASS (zero)
Task 7 and parent Trellis validation                   PASS (17/18 and 0/0)
task/parent JSON and Task 7 JSONL parsing              PASS
protected hashes                                       PASS
branch/HEAD/main/origin-main/remote-main               exact 51771654a85967656fe1ca69686590b734ff9214
remote child branch / staged paths / future 000070-71 absent/0/0
```

The protected hashes remain:

```text
go.mod b767fc9ef9376c651d0493329b710ac8dcf8d77e52686b847d244aff9f6d48fd
recovery/testdata/rsync_local_to_remote.json 2570bd4472541322d902c4cdf2fe43f247b69f19d335558830e749567f763892
```

A2e2+A3a is now `focused_complete_checked`. A shared `.trellis/spec/` update is
not warranted because this is a private, unshipped task-local protocol and the
review established no new reusable repository convention. Task 7 and Child 13
remain `in_progress`; the parent remains `planning` at 12/15 delivered. Full A3,
whole Task 7 review, Tasks 8--12, Child-wide delivery and every stage/commit/
push/PR/CI/merge action remain open. No goal, heartbeat, branch switch or new
worktree was used.

## Task 7 R48/G48 Execution Evidence (2026-08-06, in progress)

R48 Step 1 added only
`TestRecoverySFTPTargetDeleteRequiresConsumedExactAuthority` to
`target_test.go`. The existing overwrite-authority selector passed before that
edit, establishing a compiling package baseline. The required isolated command
then produced this genuine compiler RED before any product edit:

```text
# xirang/backend/internal/backupasset/recovery [xirang/backend/internal/backupasset/recovery.test]
internal/backupasset/recovery/target_test.go:7218:11: undefined: targetDeletePermitProof
internal/backupasset/recovery/target_test.go:7234:25: undefined: deriveRecoveryDeleteArtifactBinding
internal/backupasset/recovery/target_test.go:7236:3: undefined: recoveryDeleteArtifactBindingInput
internal/backupasset/recovery/target_test.go:7260:12: undefined: issueTargetDeletePermit
internal/backupasset/recovery/target_test.go:7264:13: undefined: TargetDeleteRequest
internal/backupasset/recovery/target_test.go:7268:13: undefined: TargetDeletePermit
internal/backupasset/recovery/target_test.go:7291:16: undefined: TargetDeletePermit
internal/backupasset/recovery/target_test.go:7293:37: undefined: TargetDeletePermit
internal/backupasset/recovery/target_test.go:7294:38: undefined: TargetDeletePermit
internal/backupasset/recovery/target_test.go:7295:43: undefined: TargetDeletePermit
internal/backupasset/recovery/target_test.go:7295:43: too many errors
FAIL xirang/backend/internal/backupasset/recovery [build failed]
FAIL
```

The command exited 1 for the intended missing delete-only capability, artifact
derivation and issuer. No resolver, SSH, SFTP, PostgreSQL or product code path
ran. R48 remains `in_progress`; this entry does not claim GREEN or completion.

R48 Step 3 then added the independent artifact-vector selector before any
product edit. It froze the framed-HMAC field order, exact three components,
phase-specific intent/verified authenticated documents, replay stability,
field separation, component/document bounds and raw-private-input exclusion.
Its isolated command produced this second genuine compiler RED:

```text
# xirang/backend/internal/backupasset/recovery [xirang/backend/internal/backupasset/recovery.test]
internal/backupasset/recovery/target_test.go:7222:11: undefined: recoveryDeleteArtifactBindingInput
internal/backupasset/recovery/target_test.go:7238:16: undefined: deriveRecoveryDeleteArtifactBinding
internal/backupasset/recovery/target_test.go:7242:19: undefined: deriveRecoveryDeleteArtifactBinding
internal/backupasset/recovery/target_test.go:7353:48: undefined: recoveryDeleteArtifactBindingInput
internal/backupasset/recovery/target_test.go:7355:85: undefined: recoveryDeleteArtifactBindingInput
internal/backupasset/recovery/target_test.go:7358:86: undefined: recoveryDeleteArtifactBindingInput
internal/backupasset/recovery/target_test.go:7362:72: undefined: recoveryDeleteArtifactBindingInput
internal/backupasset/recovery/target_test.go:7365:80: undefined: recoveryDeleteArtifactBindingInput
internal/backupasset/recovery/target_test.go:7368:71: undefined: recoveryDeleteArtifactBindingInput
internal/backupasset/recovery/target_test.go:7371:72: undefined: recoveryDeleteArtifactBindingInput
internal/backupasset/recovery/target_test.go:7371:72: too many errors
FAIL xirang/backend/internal/backupasset/recovery [build failed]
FAIL
```

The command exited 1 at the intended absent artifact codec. It did not enter a
resolver, SSH/SFTP or PostgreSQL path. Product implementation had still not
started at this checkpoint.

The minimal GREEN added the frozen private `TargetDeletePermit`,
`TargetDeleteRequest`, consumed-authority proof/digest, historical-key artifact
input/binding, exact phase-specific marker encoder/verifier and closed
`TargetPort.Delete` signature. The concrete Delete arm validates context and
the complete sealed request before returning exact unavailable; it contains no
session open, resolver, SSH/SFTP call, rename, remove or other remote mutation.

The interface signature required mechanical compiler adaptations in the
existing executor and test fakes. The current executor deliberately carries
only its old outer write permit into an unsealed `TargetDeletePermit`; the real
target rejects it before dependencies. R52 still exclusively owns loading the
durable consumed checkpoint/grant, historical key and issuing the first valid
delete permit. The executor fake temporarily admits only that outer permit so
the already-delivered logical exact-mirror tests continue to exercise their
prior database projection; it is not a product capability.

Fresh final verification after the last product edit:

```text
R48 two selectors normal                              PASS (0.059s)
R48 two selectors race count 5                        PASS (1.695s)
R48 plus port/deferred/F6/exact-mirror regression     PASS (0.278s)
whole Recovery normal                                 PASS (18.478s)
go vet ./internal/backupasset/recovery                PASS
make lint-backend                                     PASS (0 issues)
owned gofmt / git diff --check                        PASS
Task 7 and parent Trellis validation                  PASS (17/18 and 0/0)
```

No PostgreSQL gate ran because R48 is a pure private target authority/codec
slice with no DB, CAS, lease or transaction path. The existing PostgreSQL
fixture was not queried, restarted, replaced or removed. Protected hashes
remain exact:

```text
go.mod b767fc9ef9376c651d0493329b710ac8dcf8d77e52686b847d244aff9f6d48fd
recovery/testdata/rsync_local_to_remote.json 2570bd4472541322d902c4cdf2fe43f247b69f19d335558830e749567f763892
```

Branch remains `codex/backup-assets-controlled-recovery`; HEAD, `main` and
`origin/main` remain `51771654a85967656fe1ca69686590b734ff9214`, and the
staged set remains empty. R48/G48 is `focused_complete_checked`. R49 and all
later A3b/A3c/A3d/V14 work remain open; Task 7 and Child 13 remain
`in_progress`, the parent remains `planning` at 12/15 delivered. No goal,
heartbeat, branch switch, worktree, stage, commit, push or PR action ran.

## Task 7 R49/G49 Execution Evidence (2026-08-06, in progress)

R49 Step 1 added only `TestRecoveryDeleteTupleClassifier` to `target_test.go`.
The pure table freezes fresh, intent, captured, verified, deleted-with-markers,
verified-only cleanup and exact-clean rows together with their single allowed
next transition. It also covers exact marker replay, malformed/forged/wrong
phase/key-version/binding/bytes/type/mode marker observations, an external
final winner, a captured collision and impossible tuples. Its signature takes
only value observations, historical key material and sealed authority; the
test uses no filesystem fixture, client, resolver, SSH/SFTP, rename or remove.

Before any product edit, the required isolated command produced this genuine
compiler RED:

```text
# xirang/backend/internal/backupasset/recovery [xirang/backend/internal/backupasset/recovery.test]
internal/backupasset/recovery/target_test.go:7673:19: undefined: recoveryDeleteMarkerObservation
internal/backupasset/recovery/target_test.go:7674:34: undefined: recoveryDeleteMarkerObservation
internal/backupasset/recovery/target_test.go:7677:10: undefined: recoveryDeleteMarkerObservation
internal/backupasset/recovery/target_test.go:7691:11: undefined: recoveryDeleteTupleObservation
internal/backupasset/recovery/target_test.go:7711:15: undefined: recoveryDeleteTupleObservation
internal/backupasset/recovery/target_test.go:7712:13: undefined: recoveryDeleteTupleState
internal/backupasset/recovery/target_test.go:7713:18: undefined: recoveryDeleteTupleTransition
internal/backupasset/recovery/target_test.go:7719:10: undefined: classifyRecoveryDeleteTuple
internal/backupasset/recovery/target_test.go:7731:15: undefined: recoveryDeleteTupleObservation
internal/backupasset/recovery/target_test.go:7732:15: undefined: recoveryDeleteTupleState
internal/backupasset/recovery/target_test.go:7732:15: too many errors
FAIL\txirang/backend/internal/backupasset/recovery [build failed]
FAIL
```

The command exited 1 for the intended absent pure classifier contract. No
product code or remote/dependency path ran. R49 remains `in_progress`; this
entry records RED only and does not claim GREEN or completion.

The minimal GREEN added the seven frozen `recoveryDeleteTupleState` values, a
closed next-transition enum, complete final/intent/captured/verified value
observations and `classifyRecoveryDeleteTuple`. The classifier authenticates
the exact historical-key marker documents, requires exact regular-file marker
type/mode/bytes/digest, distinguishes exact absence from any collision, and
maps only these rows forward:

```text
fresh                    -> create intent
intent                   -> capture
captured                 -> verify captured
verified                 -> delete captured
deleted + intent         -> remove intent
deleted + verified only  -> remove verified
clean                    -> complete
all other tuples         -> conflict/stop
```

It accepts no client, path or callback and performs no observation or mutation.
Exact copied authenticated marker bytes replay to the same state. Every forged,
malformed, substituted or impossible row returns conflict while the test proves
all value inputs remain unchanged.

Fresh focused verification passed:

```text
go test ./internal/backupasset/recovery \
  -run '^(TestRecoveryDeleteArtifactBindingUsesHistoricalCleanupKey|TestRecoverySFTPTargetDeleteRequiresConsumedExactAuthority|TestRecoveryDeleteTupleClassifier)$' \
  -count=1
ok  xirang/backend/internal/backupasset/recovery  0.056s

go test -race ./internal/backupasset/recovery \
  -run '^(TestRecoveryDeleteArtifactBindingUsesHistoricalCleanupKey|TestRecoverySFTPTargetDeleteRequiresConsumedExactAuthority|TestRecoveryDeleteTupleClassifier)$' \
  -count=5
ok  xirang/backend/internal/backupasset/recovery  1.678s

go test ./internal/backupasset/recovery -count=1
ok  xirang/backend/internal/backupasset/recovery  18.296s

go vet ./internal/backupasset/recovery  PASS
make lint-backend                       PASS (0 issues)
```

Owned `gofmt`, `git diff --check`, classifier/test scans for filesystem,
client, `Lstat`, read/open, rename and remove dependencies, Task 7 and parent
Trellis validation, task/JSONL parsing and the empty-staged-set check all
passed. R49 used no PostgreSQL selector and did not restart, replace, inspect
credentials for or otherwise touch the retained `xirang-c13-pg` fixture.

The protected hashes remain:

```text
go.mod b767fc9ef9376c651d0493329b710ac8dcf8d77e52686b847d244aff9f6d48fd
recovery/testdata/rsync_local_to_remote.json 2570bd4472541322d902c4cdf2fe43f247b69f19d335558830e749567f763892
```

Branch remains `codex/backup-assets-controlled-recovery`; HEAD, `main` and
`origin/main` remain `51771654a85967656fe1ca69686590b734ff9214`. The worktree
still reports 65 porcelain entries and zero staged paths. R49/G49 is
`focused_complete_checked`; R50 and all later A3b/A3c/A3d/V14 work remain open.
Task 7 and Child 13 remain `in_progress`, and the parent remains `planning` at
12/15 delivered. No goal, heartbeat, branch switch, worktree, stage, commit,
push or PR action ran.

## Task 7 R50/G50 Execution Evidence (2026-08-06, in progress)

R50 Step 1 added only the delete-capture fixture and
`TestRecoverySFTPTargetDeleteCapturesMutationInstantObject` to `target_test.go`.
The fresh row requires exact prior prevalidation, exact authenticated intent,
live validation immediately before one standard no-overwrite
`Rename(final, captured)`, exact captured evidence retention and zero remove.
Four mutation-instant races replace the prevalidated final with a different
regular, directory, symlink or FIFO immediately before rename and require the
captured winner to control the result plus same-invocation no-overwrite restore.
An existing captured sibling must be preserved without rename or remove.

Before any R50 product edit, the isolated selector produced this genuine
behavioral RED:

```text
--- FAIL: TestRecoverySFTPTargetDeleteCapturesMutationInstantObject (0.00s)
    --- FAIL: TestRecoverySFTPTargetDeleteCapturesMutationInstantObject/exact_prior_is_captured_after_live_revalidation (0.00s)
        target_test.go:7987: exact delete capture renames=[], want one standard no-overwrite capture
    --- FAIL: TestRecoverySFTPTargetDeleteCapturesMutationInstantObject/mutation-instant_regular_controls_result (0.00s)
        target_test.go:8091: raced regular delete result={BytesWritten:0 IdentityDigest: TargetRevision:} error=recovery target unavailable, want zero/exact target changed
    --- FAIL: TestRecoverySFTPTargetDeleteCapturesMutationInstantObject/mutation-instant_directory_controls_result (0.00s)
        target_test.go:8091: raced directory delete result={BytesWritten:0 IdentityDigest: TargetRevision:} error=recovery target unavailable, want zero/exact target changed
    --- FAIL: TestRecoverySFTPTargetDeleteCapturesMutationInstantObject/mutation-instant_symlink_controls_result (0.00s)
        target_test.go:8091: raced symlink delete result={BytesWritten:0 IdentityDigest: TargetRevision:} error=recovery target unavailable, want zero/exact target changed
    --- FAIL: TestRecoverySFTPTargetDeleteCapturesMutationInstantObject/mutation-instant_special_controls_result (0.00s)
        target_test.go:8091: raced special delete result={BytesWritten:0 IdentityDigest: TargetRevision:} error=recovery target unavailable, want zero/exact target changed
    --- FAIL: TestRecoverySFTPTargetDeleteCapturesMutationInstantObject/captured_collision_is_preserved (0.00s)
        target_test.go:8121: captured collision result={BytesWritten:0 IdentityDigest: TargetRevision:} error=recovery target unavailable, want zero/exact target changed
FAIL
FAIL\txirang/backend/internal/backupasset/recovery\t0.054s
FAIL
```

The command exited 1 only because the closed concrete Delete arm has no R50
transition driver: it performed no rename/remove and returned its existing
focused-stop unavailable result. Resolver/SSH/SFTP product work did not run.
R50 remains `in_progress`; this entry records the first RED only.

R50 Step 2 then added `TestRecoverySFTPTargetDeleteCapturedIdentityMatrix`
before any product edit. The matrix covers zero, ordinary and bounded-large
regular payloads with two complete reads, exact EOF, digest/metadata stability
and a 32 KiB maximum read request; exact symlink `Lstat+ReadLink`; exact empty
directory proof; special/FIFO metadata without open/follow; non-empty directory
preservation; and short, extra, content, metadata, kind and link-target drift.
The isolated selector produced this genuine compiler RED:

```text
# xirang/backend/internal/backupasset/recovery [xirang/backend/internal/backupasset/recovery.test]
internal/backupasset/recovery/target_test.go:8156:9: undefined: observeRecoveryDeleteCapturedEntry
FAIL\txirang/backend/internal/backupasset/recovery [build failed]
FAIL
```

The required combined R50 selector produced the same precise missing-observer
RED:

```text
# xirang/backend/internal/backupasset/recovery [xirang/backend/internal/backupasset/recovery.test]
internal/backupasset/recovery/target_test.go:8156:9: undefined: observeRecoveryDeleteCapturedEntry
FAIL\txirang/backend/internal/backupasset/recovery [build failed]
FAIL
```

Both commands exited 1 before any filesystem fixture or product path ran. R50
Steps 1--3 now have genuine pre-product RED evidence; GREEN has not started at
this checkpoint.

## Task 7 R50/G50 Focused Completion Evidence (2026-08-06)

R50 Steps 4--5 added the delete-only transition driver and captured-entry
observer in `target.go`. The driver loads the historical cleanup-key version,
observes the complete final/intent/captured/verified tuple, creates or reuses
only the exact authenticated intent, revalidates the live mutation authority
and performs one standard no-overwrite `Rename(final, captured)`. It verifies
regular bytes/digest/metadata/EOF twice, symlinks through `Lstat+ReadLink`
without following, directories through two exact empty-directory proofs and
special entries through metadata only.

An exact captured mismatch is restored only by the invocation that just
completed the capture, only while final is exactly absent, through
`Rename(captured, final)`, followed by exact restored-identity and captured-
absence verification. A re-entry mismatch has no ephemeral restore authority
and returns exact `ErrRecoveryTargetChanged`. Captured collisions are preserved.
The R50 product path creates no verified marker, calls no `Remove`, never
deletes the captured leaf and retains the exact intent/captured tuple while
returning the focused-stop `ErrRecoveryTargetUnavailable`.

The two R50 selectors first passed independently, including the added re-entry
mismatch row. R48--R50 plus A2e1 Lstat/absence and A2e2 overwrite capture/
restore then passed together before final review. The final post-review command
also included the historical A2a unsealed-permit boundary:

```text
combined normal                                  PASS (0.118s)
combined -race -count=5                          PASS (2.687s)
```

The first whole-package run found one real fail-fast ordering regression:

```text
TestRecoverySFTPTargetA2aDeferredMethodsOpenNoSession:
unsealed Delete error=recovery target unavailable, want exact ErrInvalidTargetPermit
```

Root-cause tracing showed that R50's new `marker.keys` availability guard ran
before the sealed delete authority check. The minimal correction retains the
receiver/clock guard, validates the permit and request first, and only then
checks session/marker/key dependencies. The isolated regression passed, all
invalid/substituted delete authorities still open zero resolver/SSH/SFTP
dependencies, and final gates passed:

```text
TestRecoverySFTPTargetA2aDeferredMethodsOpenNoSession  PASS (0.056s)
whole Recovery normal                                PASS (18.317s)
go vet ./internal/backupasset/recovery               PASS
make lint-backend                                    PASS (0 issues)
```

Owned `gofmt`, `git diff --check`, merge-marker, logging/privacy and secret-
shaped-literal scans passed. Static review confirmed that the R50 driver has
exactly the capture and same-invocation restore rename paths, uses the private
directory facade only for `Readdir(1)` empty proof, and creates neither the
verified marker nor a delete/remove transition. Task 7 and parent Trellis
validation both passed.

The protected hashes remain:

```text
go.mod b767fc9ef9376c651d0493329b710ac8dcf8d77e52686b847d244aff9f6d48fd
recovery/testdata/rsync_local_to_remote.json 2570bd4472541322d902c4cdf2fe43f247b69f19d335558830e749567f763892
```

Branch remains `codex/backup-assets-controlled-recovery`; HEAD, `main` and
`origin/main` remain `51771654a85967656fe1ca69686590b734ff9214`. The worktree
still reports 65 porcelain entries and zero staged paths. No PostgreSQL fixture,
goal, heartbeat, branch, worktree, stage, commit, push or PR action ran.

R50/G50 is `focused_complete_checked`. R51 and all later A3b/A3c/A3d/V14 work
remain open; Task 7 and Child 13 remain `in_progress`, the parent remains
`planning`, and program delivery remains 12/15. No shared `.trellis/spec/`
update is made at this checkpoint because R50 is an incomplete task-private
remote-delete protocol already specified in the Child design; shared contract
extraction remains a V14 whole-feature review concern.

## Task 7 R51/G51 Focused Completion Evidence (2026-08-06)

Before R51 behavior work, tracing the durable delete contract found one R48/R50
planning and fixture drift. `RecoveryOperationDelete` has always required
`ExpectedPriorBytes == -1`, and the executor compares the pause `Lstat` product's
complete entry identity with `ExpectedPrior.Digest`. The private delete artifact
and permit code instead required a nonnegative byte count and treated the prior
digest as a regular-file payload digest. That shape could not be issued from the
real durable row and could not recognize directory, symlink or special priors.

Tests were first changed to require the durable sentinel and full closed-entry
identity. The R48--R50 selector produced genuine pre-product RED:

```text
TestRecoveryDeleteArtifactBindingUsesHistoricalCleanupKey       invalid recovery target permit
TestRecoverySFTPTargetDeleteRequiresConsumedExactAuthority      invalid recovery target permit
TestRecoveryDeleteTupleClassifier                               invalid recovery target permit
TestRecoverySFTPTargetDeleteCapturesMutationInstantObject       invalid recovery target permit
FAIL
```

The minimal correction binds and accepts only the literal `-1`, validates exact
prior through `TargetLstatResult.IdentityDigest` for all four closed kinds and
recomputes captured identity facts against the original final locator. A focused
regression also proved that a missing captured sidecar retains an empty identity;
an intermediate implementation had incorrectly rebound missing to a nonempty
digest and was corrected before R51 mutation work. The five R48--R50 contract/
capture selectors then passed together in 0.087s.

R51 Step 1 added `TestRecoverySFTPTargetDeleteRemovesOnlyVerifiedCaptured`.
The first run produced the expected compiler RED because the private facade and
test doubles had no directory-specific non-recursive remove:

```text
target_test.go:8667:20: testCase.client.removeDirectory undefined
target_test.go:8673:26: testCase.base.RemoveDirectory undefined
target_test.go:8698:22: testCase.base.removeDirectoryCalls undefined
FAIL xirang/backend/internal/backupasset/recovery [build failed]
```

After adding only `RemoveDirectory(string) error` to the private SFTP facade,
wrapper and fakes, the same selector became behavioral RED for regular,
symlink, empty directory and special entries: every row still returned the R50
focused-stop `ErrRecoveryTargetUnavailable` with no successful absence result.

R51 Steps 3 and 5 added exact verified-marker creation, kind-specific captured
leaf removal, whole-tuple reconciliation after every ambiguous remove, exact
final/captured absence proof, intent removal and verified-last cleanup. Standard
`Remove` is used only for regular/symlink/special captured leaves and the two
regular markers; `RemoveDirectory` is used only for a twice-proven empty captured
directory. Every mutation runs canonical-parent plus live permit validation.
Only the fully clean tuple returns the zero-byte final absence revision, and an
already clean tuple is idempotently adopted.

The fresh-to-clean matrix then passed all four kinds, preserved external
siblings, and proved that a non-empty captured directory and child receive zero
remove calls. The final focused run passed in 0.102s. The historical R50 exact
capture row was narrowed to its capture-only helpers after concrete Delete
legitimately opened clean success; mismatch restore, re-entry and collision rows
continue through the concrete method.

R51 Step 4 added `TestRecoverySFTPTargetDeleteCrashStateMatrix` for before/after
intent create, capture, captured regular read/stat, captured symlink ReadLink,
verified create, captured leaf remove, separate final and captured absence
observations, intent remove, verified remove and remote-success session close.
Each ambiguous first invocation returns only zero/unavailable or an exactly
proved clean success; a fresh target resumes from authenticated remote state and
returns the same clean absence result. This matrix inherited GREEN from the
Step 3 transition driver on its first run; no historical RED is reconstructed.
The final R51 behavior/crash pair passed in 0.312s.

Fresh combined verification passed:

```text
R48--R51 + A2a/A2e1/A2e2 normal              PASS (0.381s)
R48--R51 + A2a/A2e1/A2e2 -race -count=5      PASS (7.351s)
whole Recovery normal                         PASS (20.218s)
go vet ./internal/backupasset/recovery        PASS
make lint-backend                             PASS (0 issues)
```

Owned gofmt and whole `git diff --check` passed. Static review found only the
delete-specific `final -> captured`, same-invocation `captured -> final`,
kind-specific captured leaf remove and ordered intent/verified marker removes
inside the R48--R51 region. It found no direct logging, recursive delete,
caller-supplied artifact path, R52 worker/executor issuance or later A3 work.

R51/G51 is `focused_complete_checked`. R52 and all later A3b/A3c/A3d/V14 work
remain open; Task 7 and Child 13 remain `in_progress`, the parent remains
`planning`, and program delivery remains 12/15.

## Task 7 R51/G51 Final Gate Refresh (2026-08-06)

After removing temporary diagnostic instrumentation used only to isolate an
unrelated full-backend fixture failure, the R51 boundary was rerun from the
same worktree without staging or changing branches:

```text
R48--R51 + A2a/A2e1/A2e2 normal              PASS (0.393s)
R48--R51 + A2a/A2e1/A2e2 -race -count=5      PASS (7.617s)
whole Recovery normal                         PASS (25.509s)
go vet ./internal/backupasset/recovery        PASS
make lint-backend                             PASS (0 issues)
gofmt -l Recovery + migration fixture         PASS (no output)
git diff --check                              PASS
Task 7 JSONL validation                       PASS (17/18)
Child 13/parent JSONL validation              PASS (0/0)
task.json jq parse                            PASS
```

An additional `go test ./...` diagnostic remains outside this R51 gate: the
SQLite `TestBackupAssetRecoveryWorkerFirstWriteSQLite` and
`TestBackupAssetMigration069WorkerPreWriteSourceDriftSQLite` selectors fail
before R51 code at `newRecoveryTargetSessionBinding`. Their migration fixture
uses `recoveryMigrationDigest(base+4)` as `RootLocatorDigest`, while the runtime
requires the canonical `settings.RecoveryTargetRootLocatorDigest`; the
PostgreSQL counterparts were skipped because `TEST_POSTGRES_DSN` is unset. No
fixture or worker change is included in R51.

The protected state remains unchanged: branch
`codex/backup-assets-controlled-recovery`, `HEAD == main == origin/main ==
51771654a85967656fe1ca69686590b734ff9214`, staged paths `0`, all worktree
status entries `93`, and protected hashes unchanged. R51 remains
`focused_complete_checked`; the next authorized boundary is R52 durable
consumed-delete-authority issuance/reconciliation, which was not started.

## Task 7 R52 Focused Completion Evidence (2026-08-06)

R52 closes the gap between durable exact-mirror delete-authority consumption
and the actual remote tuple mutation. A consumed grant is no longer sufficient
to adopt an absence checkpoint without re-entering the locked target delete
path. The worker now consumes durable authority, issues a permit bound to the
current attempt/node/source fences and target-chain revision, calls
Target.Delete, verifies absence, and only then projects the operation
checkpoint. Restarted operations use the same durable consumed identity while
reconciling the tuple; they do not require the client to resend the bearer
secret.

### Step 1 RED: consumed authority skipped tuple reconciliation

TestRecoveryConsumedDeleteReconcilesTupleBeforeAbsenceAdoption was added before
the worker change. It reproduced the old adoptAbsence fast path:

~~~
verify=true deletes_at_verify=1 deletes=1
want one tuple reconciliation before verify
~~~

This is genuine behavioral RED: the reload reached Verify without a second
Target.Delete call after the first remote mutation had already occurred.

### Step 2 RED/GREEN: locked ordinary-delete permit issuance

TestRecoveryOrdinaryDeletePermitLockedIssuance covers the complete durable
identity and live-fence matrix: consumed checkpoint/grant, exact pending item,
historical cleanup-key version, current attempt/node/source fences, item or
grant substitution, expired owner, and required-but-unconsumed checkpoint.
The implementation now returns the consumed checkpoint/grant identity from
consumeOrdinaryDeleteAuthority and validates it again inside a locked
transaction before issuing TargetDeletePermit.

The executor no longer uses the consumed-final-absence shortcut. For multiple
delete operations in one invocation, the consumed checkpoint's original target
revision remains historical evidence; each subsequent delete is chained from
the current target-chain revision while retaining the same durable consumed
authority.

### GREEN: focused normal and race gates

~~~
cd backend
go test ./internal/backupasset/recovery -count=1
~~~

~~~
ok  xirang/backend/internal/backupasset/recovery  18.808s
~~~

The first post-change package run exposed three stale expectations in restart
tests. They were updated to the R52 contract: the second item in the
projection-crash multi-delete case has two delete calls (one before and one
after restart), the single-item consumed reload has two total delete calls,
and an invalid verification after the reconciliating delete retains the
non-empty write-result digest and target-revision-delete evidence.

~~~
cd backend
go test -race ./internal/backupasset/recovery \
  -run 'TestRecovery(SFTPTargetDelete|DeleteTupleClassifier|DeleteArtifactBindingUsesHistoricalCleanupKey|DeleteObservationIssuanceUsesExactLockedTargetSessionBinding|ExactMirror(SuccessfulDelete|MultipleDeletes|Consumed|StaleDeleteAuthority)|ConsumedDelete|OrdinaryDeletePermitLockedIssuance)' \
  -count=5
~~~

~~~
ok  xirang/backend/internal/backupasset/recovery  24.963s
~~~

git diff --check and the owned gofmt pass after the test expectation updates.
No files were staged, committed, pushed, or moved to another branch; the
existing worktree modifications remain protected. R52 is
focused_complete_checked; whole Task 6/Child 13 gates, PostgreSQL-required
gates, frontend/Docker/CI delivery, and later A3 work remain open.

### R52 required real-PostgreSQL gate

The existing healthy xirang-c13-pg PostgreSQL 18 fixture was reused on loopback
port 55470. Its password was read transiently and URL-encoded in the command
environment; neither the password nor the DSN was printed. The fixture was not
restarted, reconfigured, replaced, or removed.

~~~
REQUIRE_POSTGRES_RECOVERY_TEST=1 TEST_POSTGRES_DSN=<redacted fixture URL> \
  go test ./internal/backupasset/recovery \
  -run '^TestRecoveryVerifyOperationProductMatrix/exact-mirror_multi-delete_honors_production_PostgreSQL_000069$' \
  -count=1
~~~

~~~
ok  xirang/backend/internal/backupasset/recovery  2.221s
~~~

~~~
REQUIRE_POSTGRES_RECOVERY_TEST=1 TEST_POSTGRES_DSN=<redacted fixture URL> \
  go test -race ./internal/backupasset/recovery \
  -run '^TestRecoveryVerifyOperationProductMatrix/exact-mirror_multi-delete_honors_production_PostgreSQL_000069$' \
  -count=1
~~~

~~~
ok  xirang/backend/internal/backupasset/recovery  3.880s
~~~

Post-run catalog checks against the same fixture returned zero temporary
Recovery schemas and zero temporary Recovery roles: schemas=0, roles=0.

## Task 7 R53 Focused Completion Evidence (2026-08-06)

R53 separates a consumed delete tuple that is temporarily unavailable from a
tuple that is provably contradictory. The private
ordinaryDeleteDisposition enum has three closed outcomes:
retryable, contradictory, and fence-lost. After durable delete authority is
consumed, temporary target/key/context failures return through the sanitized
unavailable boundary without writing an operation checkpoint, changing the
target chain, creating failure evidence, terminalizing the attempt, or
releasing either lease. A current owner may explicitly re-enter after the
dependency recovers. A typed target change or other unclassified product
contradiction continues through the existing remote-outcome-unresolved
projection; invalid permits remain fence loss.

### Step 1 RED: consumed delete unavailability incorrectly terminalized

TestRecoveryConsumedDeleteUnavailableDoesNotTerminalize was added against the
R52 implementation. It first crashes after the remote delete and durable
authority consumption but before operation projection, then injects a second
Target.Delete returning ErrRecoveryTargetUnavailable. The genuine RED was:

~~~
consumed-delete unavailable retry error=invalid recovery target verification,
want worker unavailable without terminal verification
~~~

The old broad post-pause/unresolved path had already changed the job, item,
checkpoint/evidence and lease state before returning.

### Step 2 RED/GREEN: contradictory current owner

TestRecoveryConsumedDeleteContradictionTerminalizesCurrentOwner injects a
typed ErrRecoveryTargetChanged at the same post-consumption boundary. It
asserts one unresolved checkpoint, failed item/job, failure evidence, closed
attempt and released source/node leases, while rejecting fence-loss handling.
The existing verification-error projection test remains green and continues to
prove the unresolved product for a non-typed verification failure.

### GREEN: disposition and re-entry gates

~~~
cd backend
go test ./internal/backupasset/recovery \
  -run '^TestRecovery(ConsumedDelete(UnavailableDoesNotTerminalize|ContradictionTerminalizesCurrentOwner)|OrdinaryDeleteDispositionMatrix)$' \
  -count=1
~~~

~~~
ok  xirang/backend/internal/backupasset/recovery  0.195s
~~~

The disposition matrix covers cancellation, deadline, target/key unavailability,
target change, invalid permit, fence loss and an unclassified error. The
unavailability test then clears the injected fault and re-enters with the same
current claim; the tuple completes successfully without a second authority
consumption. Existing
TestRecoveryExactMirrorConsumedAuthorityFreshTakeoverRequiresAdoption covers
lease-expiry takeover, stable historical artifact binding, old-owner zero
mutation and fresh-claim continuation.

The complete Recovery package, vet and backend lint were also refreshed:

~~~
go test ./internal/backupasset/recovery -count=1
ok  xirang/backend/internal/backupasset/recovery  18.946s

go vet ./internal/backupasset/recovery
make lint-backend
0 issues.

go test -race ./internal/backupasset/recovery -count=1
ok  xirang/backend/internal/backupasset/recovery  53.873s
~~~

The final combined R52--R53 focused gates passed:

~~~
go test ./internal/backupasset/recovery -run '<R52--R53 delete/consumed/takeover selectors>' -count=1
ok  xirang/backend/internal/backupasset/recovery  1.788s

go test -race ./internal/backupasset/recovery -run '<same selectors>' -count=5
ok  xirang/backend/internal/backupasset/recovery  29.189s
~~~

The required real-PostgreSQL R52--R53 normal/race selectors were run against
the unchanged healthy xirang-c13-pg PostgreSQL 18 fixture on loopback port
55470. The command constructed the URL transiently from the container secret
and did not print the URL or secret:

~~~
REQUIRE_POSTGRES_RECOVERY_TEST=1 TEST_POSTGRES_DSN=<redacted fixture URL> \
  go test ./internal/backupasset/recovery \
  -run '^(TestRecoveryBehaviorPostgres|TestRecoveryVerifyOperationProductMatrix/exact-mirror_multi-delete_honors_production_PostgreSQL_000069)$' \
  -count=1
ok  xirang/backend/internal/backupasset/recovery  0.407s

REQUIRE_POSTGRES_RECOVERY_TEST=1 TEST_POSTGRES_DSN=<redacted fixture URL> \
  go test -race ./internal/backupasset/recovery \
  -run '^(TestRecoveryBehaviorPostgres|TestRecoveryVerifyOperationProductMatrix/exact-mirror_multi-delete_honors_production_PostgreSQL_000069)$' \
  -count=1
ok  xirang/backend/internal/backupasset/recovery  2.076s
~~~

The fixture was not restarted, reconfigured, replaced or removed. Post-run
catalog checks found zero temporary Recovery schemas and zero temporary Recovery
roles: schemas=0, roles=0. R53 is focused_complete_checked; R54 and later A3
milestones, whole Child 13 closure, delivery and Git operations remain open.

## Task 7 R54 Focused Evidence (2026-08-06)

R54 added `TestRecoverySFTPTargetDeleteErrorResourceAndPrivacyMatrix` for the
concrete SFTP Delete path. The matrix injects resolver, dial, SFTP opener,
file-open/read/stat/write/Sync, standard Rename, Remove, file-close, SFTP-close
and SSH-close failures. It includes APIs that return a non-nil file/client with
an error and checks that each acquired file, SFTP client and SSH client closes
exactly once; the error/result boundary is the closed unavailable sentinel.
The live permit matrix revokes immediately before intent creation, capture
Rename, verified-marker creation, captured-leaf removal, intent removal and
verified-marker removal. A mismatch-capture re-entry row preserves the captured
winner and performs no restore after revocation. Product/request/result/error
formatting and JSON are scanned with SSH audit input and captured logs for
private host/user/credential/root/path/name/token/marker/content/link/digest/
SFTP-status/raw-error canaries; the target emits no direct log. No production
behavior expansion was warranted by the matrix; existing normalization and
session ownership remained sufficient.

The first local run exposed only incorrect test-fixture baselines (preinstalled
setup mutation counters and a close callback that did not count its own close),
which were corrected before the behavior result was accepted; no product
failure remained after the fixture correction.

Fresh dynamic gates:

```text
R48--R54 focused normal                               PASS (1.897s)
R48--R54 focused race -count=5                        PASS (17.473s)
whole Recovery normal                                 PASS (18.978s)
whole Recovery race                                   PASS (52.806s)
go vet ./internal/backupasset/recovery                PASS
make lint-backend                                     PASS (0 issues)
gofmt -d target.go target_test.go                     PASS
git diff --check                                      PASS
```

R54 is complete only for this focused Delete slice. A3b-V1 remains the next
mandatory bounded review/stop; A3c, A3d, V14, Git delivery and the remaining
Child 13 acceptance are still open.

## Task 7 A3b-V1 Focused Review and Mandatory Stop (2026-08-06)

A3b-V1 re-reviewed the PRD full-A3 Delete acceptance against design sections
46.1--46.4, 46.9 and 47.1. The review traced the closed capability boundary,
historical consumed-authority/artifact binding, tuple ordering and state
machine, current-fence permit issuance, worker re-entry/takeover and error
disposition, resource ownership, context precedence and privacy/redaction
contracts. It found one Important behavioral defect and no remaining
Critical/Important defect after the correction described below.

### Fresh genuine RED and minimal GREEN

`TestRecoveryFreshlyConsumedDeleteUnavailableDoesNotTerminalize` was added to
cover the previously untested same-invocation window: the worker consumes the
durable delete authority and then receives temporary unavailability at either
`Target.Delete` or the required absence `Verify`. Before the product correction
the focused command failed with both cases mapped to terminal verification:

```text
--- FAIL: TestRecoveryFreshlyConsumedDeleteUnavailableDoesNotTerminalize
    delete_unavailable: freshly consumed delete unavailable error=invalid recovery target verification,
    want retryable worker unavailable
    verification_unavailable: freshly consumed delete unavailable error=invalid recovery target verification,
    want retryable worker unavailable
FAIL
```

This was a genuine RED: `executeOrdinaryOperation` continued to inspect the
pre-consumption handoff bit after `consumeOrdinaryDeleteAuthority` had already
committed. Temporary dependency ambiguity therefore entered the old unresolved
projection instead of remaining re-enterable.

The minimal GREEN keeps a local `deleteAuthorityConsumed` state for the whole
execution path, flips it immediately after the durable consume returns, and
uses it for every subsequent Delete/Verify error and result-validation branch.
No authority, permit, tuple, projection or public error contract was broadened.

### Fresh dynamic and structural gates

```text
focused genuine RED selector                       FAIL as intended (0.12s)
focused new GREEN normal                           PASS (0.176s)
focused new GREEN race -count=5                    PASS (3.360s)
R48--R54 focused normal                            PASS (1.355s)
R48--R54 focused race -count=5                     PASS (22.497s)
whole Recovery normal                              PASS (19.102s)
whole Recovery race                                PASS (53.696s)
go vet ./internal/backupasset/recovery             PASS
make lint-backend                                  PASS (0 issues)
owned gofmt / git diff --check                     PASS
direct target-log scan                             PASS (zero)
```

The required real-PostgreSQL gate reused the healthy, unchanged `xirang-c13-pg`
PostgreSQL 18 fixture on loopback port 55470. The password was read and
URL-encoded only inside the command; neither the password nor DSN was printed,
and the fixture was not restarted, replaced or removed:

```text
REQUIRE_POSTGRES_RECOVERY_TEST=1 ... \
  go test ./internal/backupasset/recovery -run 'Postgres' -count=1
PASS (6.855s; all Postgres selectors, no skip)

REQUIRE_POSTGRES_RECOVERY_TEST=1 ... \
  go test -race ./internal/backupasset/recovery -run 'Postgres' -count=1
PASS (10.040s; all Postgres selectors, no skip)
```

Post-run fixture catalog checks found zero temporary Recovery schemas and zero
temporary Recovery roles: `schemas=0 roles=0`.

The exact Trellis/JSON/JSONL, manifest, protected-hash and Git-state gates
remain green after the correction:

```text
phase1=9 create=55 modify=81 total=145 unique=145 duplicates=0
dirty=93 manifest_dirty=91 protected_dirty=2 outside=0 staged=0
create_present_at_head=0 modify_missing_at_head=0 future_000070_71=0
go.mod SHA-256 b767fc9ef9376c651d0493329b710ac8dcf8d77e52686b847d244aff9f6d48fd
recovery/testdata/rsync_local_to_remote.json SHA-256 2570bd4472541322d902c4cdf2fe43f247b69f19d335558830e749567f763892
branch codex/backup-assets-controlled-recovery
HEAD/main/origin/main 51771654a85967656fe1ca69686590b734ff9214
staged paths 0; outside-scope paths 0
```

A3b-V1 is now `focused_complete_checked`. The stop is immediately before
A3c: no RemoveOwnedJobDir/lifecycle terminalization, A3d reconciliation,
runtime/main composition, V14 whole Task 7 closure or Git delivery was started.
Task 7 and Child 13 remain `in_progress`; the parent remains `planning` at
12/15 delivered.

## Task 7 A3c R55/G55 genuine RED (2026-08-06)

The R55/G55 tests were added before changing the cleanup production contract:

```text
backend/internal/backupasset/recovery/result_lifecycle_test.go
  TestResultLifecycleAdvanceCleanupTransitionsValidatedToDeleteStarted
  TestTargetCleanupLiveValidatorRunsBeforeEveryMutation
```

The focused selector was run from `backend/`:

```text
go test ./internal/backupasset/recovery -run 'Test(ResultLifecycleAdvanceCleanupTransitionsValidatedToDeleteStarted|TargetCleanupLiveValidatorRunsBeforeEveryMutation)$' -count=1
```

It failed during test compilation as intended because the pre-R55 product has
no `OwnedJobDirRemoval`, no cleanup proof `validateLive`, and no
`AdvanceRecoveryResultCleanup`/`AdvanceRecoveryWorkspaceCleanup` methods:

```text
undefined: OwnedJobDirRemoval
permit.proof.validateLive undefined
fixture.service.AdvanceRecoveryResultCleanup undefined
```

This is genuine RED for the frozen R55 contracts. No A3c production code was
changed before observing this failure.

### R55/G55 minimal GREEN and gates

The minimal GREEN added only the frozen cleanup result/permit surface and the
two lifecycle entry transactions. `Advance` performs one target remove pass;
it does not persist incomplete progress or start a retry loop (those belong to
R58). The private callback re-queries the exact published/unpublished durable
tuple, the node lease and the permanent use latch and rejects any mismatch
before the fake's planned mutation counter advances.

```text
R55 focused normal                                      PASS (0.286s)
R55 focused race -count=5                               PASS (4.720s)
R55 PostgreSQL normal                                   PASS (0.377s)
R55 PostgreSQL race -count=5                            PASS (3.659s)
```

The PostgreSQL run used the existing healthy `xirang-c13-pg` fixture on
loopback port 55470 through a temporary schema; credentials and the DSN were
not printed. The temporary schema was cleaned by the test and no PostgreSQL
roles or schemas were left behind. The focused R55 normal/race and required
PostgreSQL normal/race gates are green; R56 is the next bounded slice.

## Task 7 A3c R56/G56 genuine RED (2026-08-06)

The concrete-target capture test was added before implementing the R56 target
operation:

```text
backend/internal/backupasset/recovery/target_test.go
  TestRecoverySFTPTargetRemoveOwnedJobDirCapturesExactWorkspace
```

The focused selector was run from `backend/`:

```text
go test ./internal/backupasset/recovery -run '^TestRecoverySFTPTargetRemoveOwnedJobDirCapturesExactWorkspace$' -count=1
```

It failed at the existing concrete target boundary with the sanitized
dependency result:

```text
capture owned workspace: recovery target unavailable
```

The test therefore proves the intended missing behavior rather than an
inherited pass: no capture rename, marker reauthentication, verified marker or
bounded-progress result exists before R56.

## Task 7 A3c R56/G56 GREEN and focused gates (2026-08-06)

The R56 RED was traced to a re-entry locator gap. The initial captured sibling
component included the owner-marker key version and marker digest, but those
facts are reachable only inside the captured tree after the final workspace has
disappeared. A retry therefore failed while requiring `jobs/<job>` and could
not reach authenticated capture evidence.

The minimal correction keeps the complete cleanup artifact documents bound to
the historical owner-marker key, marker digest, key version, job/root/owner
facts and domain-separated HMACs. The captured sibling component is now derived
from the sealed cleanup permit's stable fields alone. Re-entry can locate only
that one candidate; it must still re-read and authenticate the captured owner
marker, recompute the historical-key artifacts, and verify the external
`verified` document. Unknown jobs-directory siblings are never scanned or
adopted. The target remains capture/marker-only in R56 and performs no
descendant removal.

The concrete and fail-closed matrices are:

```text
TestRecoverySFTPTargetRemoveOwnedJobDirCapturesExactWorkspace             PASS
TestRecoveryOwnedCleanupArtifactBindingUsesHistoricalKey                 PASS
TestRecoverySFTPTargetRemoveOwnedJobDirReentersAuthenticatedCapture      PASS
TestRecoverySFTPTargetRemoveOwnedJobDirRejectsUnownedReentryAndWinners   PASS
TestRecoverySFTPTargetRemoveOwnedJobDirCrashReentryMatrix                PASS
```

The last matrix covers captured and verified collisions, a forged captured
marker, canonical captured-sibling drift, and an external final winner. Each
case preserves evidence, performs no descendant remove, and returns the closed
target-changed result where state is contradicted. The interrupted verified
marker creation case re-enters without a second rename and creates exactly one
authenticated verified marker while preserving an unknown sibling.

Focused dynamic gates after the correction:

```text
R55 + R56 focused normal                                         PASS (0.287s)
R55 + R56 focused race -count=5                                  PASS (4.716s)
whole Recovery normal                                            PASS (19.202s)
whole Recovery race                                              PASS (53.794s)
R55 PostgreSQL normal (TestResultLifecycleAdvanceCleanupPostgres) PASS (0.378s)
R55 PostgreSQL race (TestResultLifecycleAdvanceCleanupPostgres)   PASS (2.016s)
```

The whole-package normal gate initially exposed an uncovered deferred-
dependency expectation: an SFTP target without marker-key dependencies must
return `ErrRecoveryTargetUnavailable` before opening a session. The target now
preserves that boundary; the permit validation contract is otherwise unchanged.
The rerun passed, and the race gate remained clean. No files were staged,
committed, pushed, or moved to another branch. R56/G56 is focused-complete;
R57 bounded descendant removal is the next slice and has not started.

Additional quality checks for this slice:

```text
go vet ./internal/backupasset/recovery                   PASS
make lint-backend                                        PASS (0 issues)
gofmt -d target.go target_test.go                         PASS (empty)
git diff --check                                          PASS
task.py validate Child 13                                 PASS
direct target logger scan                                 PASS (no logger/log calls)
```

After adding the post-verified-marker external-final recheck, the final
regression gates were refreshed:

```text
R55 + R56 focused race -count=5 (final)                  PASS (4.810s)
whole Recovery normal (final)                            PASS (19.272s)
whole Recovery race (final)                              PASS (53.524s)
go vet ./internal/backupasset/recovery (final)           PASS
make lint-backend (final)                                PASS (0 issues)
```

## Task 7 A3c R57 partial implementation and bounded-walker gates (2026-08-07)

R57 added the private `ReadDir(int)` facade surface, updated local/scripted
fakes and implemented an iterative depth-first cleanup walker. The walker uses
an explicit maximum depth of 64, requests directory entries in batches of 64,
re-`Lstat`s every leaf, treats symlink and special entries as leaves, rechecks
canonical directory identity and `StatVFS.Fsid` on entry and before directory
removal, calls the private live validator immediately before every remove, and
counts leaf, directory, captured-root and external verified-marker removal
against the same 256-successful-mutation budget.

The existing R56 crash/re-entry matrix was updated only for the intentional
R57 transition: a retry after verified-marker creation now completes the
three-entry empty-workspace cleanup instead of remaining marker-only. Earlier
capture/marker interruption rows remain incomplete with zero descendant
mutation.

Fresh matrices now cover:

```text
TestRecoveryOwnedCleanupDepthFirstNoFollowMatrix            PASS
TestRecoveryOwnedCleanupFilesystemBoundaryMatrix            PASS
TestRecoveryOwnedCleanupRemovesAtMost256Entries             PASS
TestRecoverySFTPTargetRemoveOwnedJobDirCrashReentryMatrix    PASS
```

The filesystem matrix covers different `Fsid` on directory entry, `Fsid`
drift after enumeration, canonical escape and a 65-level child beneath the
captured root. Each row stops before the boundary mutation and preserves that
entry. The limit matrix creates 288 leaves and proves exactly 256 successful
leaf removals, `Complete=false`, a deterministic redacted digest, no captured-
root or verified-marker removal, and `ReadDir(64)` pagination. The live
validator count includes capture rename, verified-marker creation and every
successful remove.

Fresh gates:

```text
R56/R57 focused normal                                      PASS (0.097s)
R56/R57 focused race -count=5                               PASS (2.036s)
whole Recovery normal                                       PASS (19.203s)
whole Recovery race                                         PASS (53.886s)
go vet ./internal/backupasset/recovery                      PASS
make lint-backend                                           PASS (0 issues)
gofmt target.go target_test.go                              PASS (empty)
git diff --check                                            PASS
```

R57 is deliberately **not** marked focused-complete. `github.com/pkg/sftp`
v1.13.10 exposes `Client.ReadDir`/`ReadDirContext`, both of which collect the
entire directory before returning, and `*sftp.File` exposes no `ReadDir` or
`Readdir` method. The current production wrapper implements facade pagination
by calling `Client.ReadDirContext` once and slicing the returned entries, so
the caller and retained batches are bounded but the dependency-level read is
not. This contradicts R57 Step 1's no-unbounded-directory-read contract. Step
1 remains unchecked; R58 has not started. No stage, commit, push, branch switch
or PR action ran.

On `2026-08-07`, the user approved the strict R57 correction: retain the true
bounded-read contract and add a minimal private SFTP v3 directory pager over a
dedicated SSH subsystem channel. The approval explicitly rejects relaxing the
contract to a dependency-level full directory snapshot. The amended design and
R57 Step 1 freeze a 256 KiB packet bound, at most 257 retained non-dot entries,
strict framing/ID/status validation, deterministic remote-handle/channel close,
no shell command, no reflection/`go:linkname`, no new dependency and no
`go.mod` change. Product implementation and RED/GREEN evidence follow this
receipt; R58 remains stopped.

Implementation review then found that one subsystem channel per entered
directory would make the allowed depth 64 incompatible with OpenSSH's common
`MaxSessions=10` default. The resource topology was therefore narrowed before
R57 closure: one dedicated subsystem belongs to the cleanup SFTP session and
all bounded-stack directory handles share its sequential request stream. This
keeps the same packet/entry/protocol bounds while avoiding depth-proportional
SSH session channels. A new genuine RED covers multiple handles over one
handshake/channel before this correction is implemented.

## Task 7 A3c R57/G57 bounded SFTP pager RED/GREEN and closure (2026-08-07)

Five test-first boundaries were observed as genuine RED before their
corresponding product change:

```text
TestRecoveryBoundedSFTPDirectoryNamePacketMatrix
  RED: undefined decodeRecoverySFTPDirectoryNamePacket

TestRecoveryBoundedSFTPDirectoryProtocolPagesAndCloses
TestRecoveryBoundedSFTPDirectoryRejectsOversizeAndRawStatus
  RED: undefined newRecoveryBoundedSFTPDirectory and packet bound

TestRecoverySFTPFileReadDirUsesBoundedPager
TestRecoverySFTPTargetHasNoUnboundedDirectoryRead
  RED: wrapper required *sftp.File, had no pager opener and retained
       ReadDirContext

TestRecoveryBoundedSFTPDirectoryOpensDedicatedSubsystem
  RED: production opener required concrete *ssh.Client and could not be
       contract-tested through the exact OpenChannel boundary

TestRecoveryBoundedSFTPDirectorySessionSharesOneTransport
  RED: undefined newRecoveryBoundedSFTPDirectorySession
```

The minimal GREEN implements SFTP v3 `INIT`, `OPENDIR`, `READDIR`, `CLOSE`,
`VERSION`, `STATUS`, `HANDLE` and `NAME` framing only. Response allocation is
capped at 256 KiB. The `NAME` decoder strictly consumes v3 size, uid/gid,
permission, time and extended attributes, filters only `.`/`..`, retains at
most 257 remote entries and rejects wrong IDs, malformed framing, unsupported
version/type/status and oversized packets with the stable sanitized target
error. Raw SFTP status text is parsed only for framing and never returned.

One lazy dedicated SSH `session`/`sftp` subsystem belongs to the concrete
cleanup SFTP client. Its sequential request stream and request-ID space are
shared by every directory handle in the bounded DFS stack. Channel close is
independent of the request mutex so target-session cancellation can interrupt
a blocked wire read. Each directory handle closes once; the shared channel
closes once with the owning target session. Ordinary reads/writes/stats/syncs
and all non-directory SFTP operations remain on `pkg/sftp`.

The compatibility test connects the new pager directly to the repository's
actual `pkg/sftp` server implementation over `net.Pipe`, reads 130 real files
to EOF in pages of at most 64, then closes the directory handle, shared session
and server without residue. The static source gate proves there is no
`Client.ReadDir` or `Client.ReadDirContext` call in `target.go`.

Final R57 gates after the shared-session correction:

```text
R56/R57 + bounded-protocol focused normal                  PASS (0.143s)
R56/R57 + bounded-protocol focused race -count=5           PASS (2.346s)
whole Recovery normal                                      PASS (19.336s)
whole Recovery race                                        PASS (54.213s)
go vet ./internal/backupasset/recovery                     PASS
make lint-backend                                          PASS (0 issues)
gofmt target.go target_test.go                             PASS (empty)
git diff --check                                           PASS
unbounded directory-read source scan                       PASS (zero)
direct target logger/printf scan                           PASS (zero)
task.py validate Child 13                                  PASS
```

R57/G57 is `focused_complete_checked`. All six R57 plan steps are complete;
the former dependency-level full-directory-read gap is closed without a new
dependency or `go.mod` change. R58 has not started. No file was staged,
committed or pushed; no branch switch or PR action ran.

## Task 7 A3c R58/G58 bounded-progress and retryable-failure closure (2026-08-07)

R58 began from the checked R57 boundary with no stage, commit, push, branch
switch or PR action. Its three test-first boundaries were observed as genuine
RED before the corresponding product changes:

```text
TestResultLifecycleAdvanceCleanupPersistsIncompleteProgress
  RED: after a three-entry incomplete target pass, published and workspace
       rows retained the permit expiry instead of completing the exact closing
       lease renewal.

TestResultLifecycleAdvanceCleanupFailureReleasesCurrentOwner
  RED: target unavailable and caller cancellation left the current owner and
       active cleanup node lease on delete_started; a lost owner returned the
       target outcome instead of the zero-update closing conflict.

TestResultLifecycleAdvanceCleanupReentryAndExpiredTakeover
  RED: a delete_started claim was rejected as invalid, so neither an explicit
       second pass nor a fresh-fence expired-owner takeover could resume.
```

The minimal GREEN makes each `Advance` invocation a single target pass. An
incomplete result performs an exact current-owner closing renewal and returns
its bounded private progress without a retry loop, timer, goroutine or
background heartbeat. Target error/cancellation first uses the short
`context.WithoutCancel` closing transaction: published rows become
`cleanup_failed`, unpublished workspaces become ownerless `cleanup_due`, both
remain `delete_started`, and only the exact active cleanup node lease is
released. Caller cancellation identity remains the returned error; a lost
owner is a zero-update conflict and cannot release a successor lease.

Explicit delete_started re-entry reads the durable lease expiry only after
locking and revalidating the same owner/fence/attempt/node-lease identity, then
renews and issues a new authenticated target permit. It never accepts or
interprets a prior `ProgressDigest`; the request has no progress-cursor field.
Expired ownership is instead reclaimed by the existing Claim path with a new
cleanup and node fence before a subsequent target pass.

Fresh R58 gates:

```text
R58 published/workspace focused normal                       PASS (1.124s)
R58 published/workspace focused race -count=5                PASS (19.631s)
R58 required real PostgreSQL normal, no skip                 PASS (0.785s)
R58 required real PostgreSQL race, no skip                   PASS (2.433s)
whole Recovery normal                                        PASS (19.515s)
whole Recovery race                                          PASS (54.594s)
go vet ./internal/backupasset/recovery                       PASS
make lint-backend                                            PASS (0 issues)
gofmt result_lifecycle.go result_lifecycle_test.go           PASS (empty)
git diff --check                                             PASS
Advance timer/loop/goroutine source scan                     PASS (zero)
```

The PostgreSQL commands built the fixture URL transiently from the existing
`xirang-c13-pg` container's password and never printed or persisted the URL or
secret. The container was not restarted, reconfigured, replaced or removed.

R58/G58 is `focused_complete_checked`. R59 has not started. No file was
staged, committed or pushed; no branch switch or PR action ran.

## Task 7 A3c R59/G59 deleted-tuple and atomic-tombstone closure (2026-08-07)

R59 started from the checked R58 boundary. The target and lifecycle product
were developed as separate bounded steps. The observed REDs were:

```text
TestResultLifecycleCompleteCleanupPersistsDeletedFirst
  RED: Complete=true remained delete_started for both published ResultSet and
       unpublished workspace claims.

TestRecoverySFTPTargetValidateOwnedJobDirRemovedIsReadOnly
  RED: the removed-tuple operation, validation product and TargetPort method
       did not exist.

TestResultLifecycleAdvanceDeletedCleanupUsesReadOnlyValidation
  RED 1: durable deleted claims were rejected as invalid lifecycle input.
  RED 2: after read-only validation was added, the claim remained deleted and
         did not reach the required cleaned/tombstoned terminal projection.
```

The minimal GREEN first persists `delete_started -> deleted` while retaining
the exact cleanup owner, cleanup lease and active node lease. A later Advance
of the durable deleted claim renews only the exact current authority and issues
`TargetCleanupValidateRemovedJobDir` without a live mutation validator. The
target observes exact absence of the final workspace, authenticated captured
sibling and external verified marker; its SFTP mutation counters remain zero.

After the clean tuple is re-proved, one final transaction locks the job, exact
cleanup node lease and ResultSet when present. Published cleanup performs
`revoking/deleted -> cleaned/tombstoned`; unpublished cleanup performs
`cleanup_due/deleted -> workspace_cleaned/tombstoned`. Both clear owner,
expiry and node fields and release the exact node lease through the existing
RowsAffected-one helper. Injected result/job CAS and node-release failures all
rolled back to the intact durable-deleted authority. Those rollback rows passed
on first execution after the minimal terminal transaction; no false RED is
claimed for them.

Expired durable-deleted claims preserve phase during fresh-fence takeover.
The stale owner is rejected before target access, while the successor invokes
only clean-tuple validation and never calls recursive removal again. These
takeover rows also passed immediately against the existing phase-preserving
claim mechanism and required no additional production change.

Fresh R59 gates:

```text
R59 focused SQLite normal                                 PASS (0.618s)
R59 focused SQLite race -count=5                          PASS (4.470s)
R59 required real PostgreSQL normal, no skip              PASS (0.773s)
R59 required real PostgreSQL race, no skip                PASS (2.602s)
paired 000069 workspace/result tombstone normal           PASS (19.848s)
paired 000069 workspace/result tombstone race             PASS (22.205s)
whole Recovery normal                                     PASS (19.720s)
whole Recovery race                                       PASS (52.028s)
go vet ./internal/backupasset/recovery                    PASS
make lint-backend                                         PASS (0 issues)
gofmt R59 Go files                                        PASS (empty)
git diff --check                                          PASS
```

The required PostgreSQL selectors used the existing healthy PostgreSQL 18
container `xirang-c13-pg` on its already-published localhost port. The
container was not restarted, reconfigured, replaced or removed. No credential
or DSN was written to the repository or test output.

R59/G59 is `focused_complete_checked`. Per the approved stop boundary, R60 has
not started. No file was staged, committed or pushed; no branch switch,
worktree or PR action ran.

## Task 7 A3c R60/G60 crash, resource and privacy closure (2026-08-07)

R60 Step 1 froze the complete crash table in `implement.md`, including the
`validated -> delete_started` barrier, capture and owner-marker
reauthentication, verified-marker creation/close, every bounded remove
boundary, captured-root and verified-marker absence, durable `deleted`,
clean-tuple-only observation and final atomic transaction. The table separates
hard-crash lease-expiry takeover from returned-error/cancellation
`context.WithoutCancel` closing semantics.

Step 2 added genuine failure coverage for the missing cleanup boundaries. The
new RED/GREEN findings were:

- A verified clean tuple was discarded when the SFTP session close failed after
  all remote mutations and absence checks. Complete removal now remains a
  valid result after that late close ambiguity; incomplete removal remains
  sanitized unavailable.
- Caller cancellation could win after complete remote cleanup and leave the
  next invocation unable to adopt the all-absent tuple. `delete_started`
  re-entry now performs bounded, read-only verified-marker absence observation
  and returns zero-mutation `Complete=true` only with current live authority.
- A non-`ENOENT` `RemoveDirectory` dependency error was classified as
  `ErrRecoveryTargetChanged`; it is now sanitized unavailable because no
  contradictory remote state was proved.

Step 2/3 selectors passed:

```text
go test ./internal/backupasset/recovery -run '^(TestRecoverySFTPTargetRemoveOwnedJobDir(CrashReentryMatrix|VerifiedCompletionSurvivesTargetCloseError|CanceledAfterCompletionReentersCleanTuple|R60DependencyFailureMatrix|R60MarkerResourceMatrix)|TestRecoveryCleanupProductsR60RedactPrivateFields|TestResultLifecycle(R60CleanupAdvanceTransactionFailureRollsBack|R60DeleteStartedBarrierFailurePreventsTarget|AdvanceCleanupFailureReleasesCurrentOwner|DeletedCleanupTerminalizationRollsBackOnEachFinalWrite))$' -count=1  PASS
```

The resource/privacy rows assert exactly-once directory/file/SFTP/SSH close,
context identity precedence, sanitized dependency errors, no direct target
logging, and zero canary leakage through errors, JSON, `%v`, `%+v`, `%#v` or
the dial audit product. At the time of this Step 2/3 entry, R60 Step 4 broad
normal/race and cross-engine gates were still open; the fresh A3c-V1 gate below
closes them. No file was staged, committed or pushed.

R60 Step 4 and A3c-V1 fresh verification (2026-08-07):

```text
R60 focused normal selector                                      PASS (0.429s)
R60 focused race selector -count=5                               PASS (7.295s)
whole Recovery normal -count=1                                   PASS (19.731s)
whole Recovery race -count=5                                    PASS (273.629s)
required real PostgreSQL Recovery normal, no skip                PASS (8.587s)
required real PostgreSQL Recovery race, no skip                  PASS (12.182s)
paired 000069 migration matrix initial run                       RED (305.252s)
paired 000069 migration matrix after fixture correction           PASS (304.495s)
source-drift SQLite selector after correction                    PASS (0.13s)
source-drift PostgreSQL selector after correction                 PASS (1.73s)
go vet ./internal/backupasset/recovery                            PASS
make lint-backend                                                 PASS (0 issues)
owned Recovery/database gofmt -d                                 PASS (empty)
git diff --check                                                  PASS
Trellis Child 13 and parent validation                            PASS
task/parent JSON and Task 7 JSONL parsing                         PASS
exact manifest / dirty-scope gate                                PASS
protected hashes / branch / HEAD / main / origin-main / staged    PASS
direct target logger, bounded ReadDir and read-only removed scan    PASS (zero findings)
```

The initial paired migration RED was the same SQLite/PostgreSQL failure:
`TestBackupAssetMigration069WorkerPreWriteSourceDrift{SQLite,Postgres}`
returned `recovery worker fence lost` instead of `ErrRecoverySourceChanged`.
Root-cause tracing showed that the migration `firstWrite` fixture used a random
`root_locator_digest`; the newly enforced target session binding rejected that
fixture before source revalidation. The minimal test-fixture correction derives
the digest with `settings.RecoveryTargetRootLocatorDigest` for the same canonical
target-root locator. The two selectors and the complete paired matrix then
passed; no production recovery behavior was changed by this correction.

The A3c review against PRD A3c acceptance and design 46.1, 46.5, 46.6, 46.9
and 47.1 found no remaining Critical or Important issue. Every cleanup
mutation has an adjacent live check; each target pass is capped at 256 removes;
`Complete=false` is normal bounded progress; durable `deleted` is read-only;
and tombstone, projection, owner-field clearing and exact node-lease release
are atomic. No A3d, Task 8/runtime/main or Git-delivery action was started.
A3c-V1 is `focused_complete_checked`; Task 7 and Child 13 remain `in_progress`.

## Task 7 A3d R61/G61 exact read-only SSH purpose and bounded root catalog (2026-08-08)

R61 began with two genuine RED commands against the pre-R61 product:

```text
go test ./internal/sshutil ./internal/settings -run '^(TestRecoveryPurposesNormalizeAsKnownAndRemainIndependent|TestRecoveryReconcilePurposeIsKnownAndIndependent|TestNodeDialerAuditsPurposeExactRecoverySessions|TestNodeDialerRejectsRecoveryReconcileAuditActionMismatchBeforeNetwork|TestSettingsListAllRecoveryTargetRootsIsBoundedAndSafe)$' -count=1
RED: PurposeRecoveryReconcile, RecoveryTargetRootReference and ListAllRecoveryTargetRoots were absent.

go test ./internal/backupasset/recovery -run '^(TestRecoveryTargetSessionFactoryOpensRootScopedReconciliation|TestRecoveryTargetSessionFactoryRejectsReconciliationRevisionSubstitutionBeforeDial)$' -count=1
RED: the private reconciliation session binding, reconcile purpose and root-scoped opener were absent.
```

The original RED wall times were not retained in the carried terminal output;
they are not reconstructed. The minimal GREEN adds the literal
`recovery_reconcile` known SSH purpose and exact credential-audit action, a
private root-scoped session binding and `OpenReconciliation`, and a hard-bounded
all-root catalog. The catalog queries at most 1025 rows, fails closed rather
than truncating 1024, decrypts and validates every row, rejects duplicate
identities and missing/archived nodes, sorts by numeric node ID and root ID,
and returns neither safe label nor locator.

Fresh final R61 evidence:

```text
exact R61 normal selector across sshutil/settings/recovery   PASS (1.058s)
exact R61 race selector across sshutil/settings/recovery     PASS (2.705s)
go vet ./internal/sshutil ./internal/settings \
  ./internal/backupasset/recovery                            PASS (0.804s)
golangci-lint run ./internal/sshutil ./internal/settings \
  ./internal/backupasset/recovery                            PASS (2.148s; 0 issues)
git diff --check                                             PASS
git diff --cached --name-only                                PASS (zero paths)
git rev-parse HEAD                                           51771654a85967656fe1ca69686590b734ff9214
git branch --show-current                                    codex/backup-assets-controlled-recovery
```

Controller-inline capability/privacy review found no R61 Critical or Important
issue. Reconciliation remains excluded from generic `TargetPurpose.valid()`, so
the existing job-scoped observation permit and `Open` path cannot acquire this
authority. The new opener accepts only the package-private root binding, checks
the private binding and locator digests before resolution, revalidates exact
node/credential revisions before dialing, uses a locator-free audit correlation
digest, and opens the purpose-exact SSH session. R61 adds no permit issuer,
target scan, mutation, runtime/main wiring, goroutine, timer, retry or heartbeat.

R61/G61 is `focused_complete_checked`. A3d and Task 7 remain `in_progress`;
R62--R66, A3d-V1, V14, Task 8/runtime/main and all Git-delivery actions remain
open. No file was staged, committed or pushed.

## Task 7 A3d R62/G62 complete keyed expected set and sealed permit (2026-08-08)

R62 started with the frozen expected-set, incomplete-state, token-privacy,
permit-substitution and cross-engine selectors. The first implementation
established the read-only repeatable-read snapshot and initially passed the
narrow matrix, but controller-inline contract review found that it did not yet
implement the complete design 46.7 product. The review therefore added focused
regressions and preserved the following genuine RED observations before the
corresponding production correction:

```text
exact A3c components:
expected component rows=1, want final, captured and verified

empty registered root:
empty registered root ... want a real complete scan

historical plan revisions:
historical revisions ... want one current complete scan

large published result set:
... want component-bounded scan

unreserved isolated job:
... want a real complete scan

tombstone history:
... want real zero-expected scan
```

The earlier R62 chronology also retained a required-real-PostgreSQL mixed-
snapshot RED and three illegal durable-state-shape REDs: an illegal reserved
job state, invalid workspace-cleanup ownership and invalid ResultSet cleanup
ownership. Their original failure wall times and any output no longer present
in the carried terminal were not reconstructed.

The minimal final R62 product now builds one complete expected-component set
inside a read-only repeatable-read transaction. Every contributing isolated
job contributes exactly three rows: the final workspace, the deterministic A3c
captured sibling and the deterministic verified marker. The latter two are
derived from persisted job/root/workspace/owner facts; their authenticated
documents still bind the historical cleanup-key version and marker digest.
Raw component strings are cleared before the target call and cross the target
boundary only as audit-key-versioned HMAC tokens with closed expected kind,
state and private marker facts.

The scan session no longer infers current node, credential or root revisions
from historical plans. A narrow `RecoveryReconciliationRevisionSource`
resolves one current snapshot in the same DB transaction, while each historical
plan revision remains available only to derive its own A3c component names.
An empty registered root and a legal unreserved isolated job now issue a real
zero-expected permit. The 4096 bound applies to remote expected components,
not 4097 published result rows or closed tombstone history; malformed
tombstones and malformed relationships remain fail closed. Cleanup-due jobs
accept the marker-validation tuple only in one of its two legal shapes,
including the legally empty tuple.

The permit proof deep-clones and deterministically sorts every private expected
row, seals the exact expected-set digest, and HMAC-binds schema, purpose,
operation, node/root facts, current revision, every bound, cursor, admission
generation, expiry and private session binding with the active audit token key.
Node, credential/root revision, expected digest, bound, cursor, expiry,
operation, purpose, private row, binding and audit-key substitutions all fail
before any target dependency. The active key slice and temporary raw component
copies are cleared after sealing. Formatting/JSON tests cover the permit, proof
and revision snapshot and retain no raw locator, component, token input, marker,
key or private revision.

The focused GREEN evidence before final ledger validation was:

```text
R62 SQLite normal                                      PASS (0.593s)
R62 SQLite race -count=5                               PASS (10.979s)
R62 PostgreSQL normal, required/no-skip                PASS (0.281s)
R62 PostgreSQL race -count=5, required/no-skip         PASS (3.148s)
whole Recovery normal                                  PASS (20.438s)
whole Recovery race                                    PASS (57.835s)
go vet ./internal/backupasset/recovery                 PASS
make lint-backend                                      PASS (0 issues)
gofmt -l Recovery-owned Go files                       PASS (empty)
git diff --check                                       PASS
PostgreSQL temporary schema/role residue               0/0
```

The retained `xirang-c13-pg` PostgreSQL fixture stayed healthy and unchanged.
Two initial invocations failed before test setup because a stale local username/
credential combination was used; they are environment invocation failures, not
product failures or passing evidence. The passing commands read the fixture
password internally, URL-encoded it without printing or persisting the secret
or DSN, connected as the fixture's `postgres` user through its published
loopback port and used `xirang_test`.

Controller-inline review against PRD A3d acceptance and design 46.7/46.9/47.1
found no remaining R62 Critical or Important issue after these corrections.
R62/G62 is `focused_complete_checked`; A3d and Task 7 remain `in_progress`.
R63--R66, A3d-V1, V14, Task 8/runtime/main and every Git-delivery action remain
open. No R63 classifier, SFTP directory scan, cursor, audit/alert orchestration,
downgrade loop, runtime, goroutine, timer, retry or heartbeat was started.

Fresh post-ledger verification then reran the final R62 and whole-package gates:

```text
R62 SQLite normal                                      PASS (0.604s)
R62 SQLite race -count=5                               PASS (10.828s)
R62 PostgreSQL normal, required/no-skip                PASS (0.279s)
R62 PostgreSQL race -count=5, required/no-skip         PASS (3.160s)
whole Recovery normal                                  PASS (20.436s)
whole Recovery race                                    PASS (57.985s)
go vet ./internal/backupasset/recovery                 PASS
make lint-backend                                      PASS (0 issues)
gofmt -l backend/internal/backupasset/recovery/*.go    PASS (empty)
git diff --check                                       PASS
PostgreSQL temporary schema/role residue               0/0
Child and parent task.py validate                      PASS (17/18 and 0/0)
task/parent JSON and Child JSONL parsing                PASS
```

The exact final structural reconciliation returned:

```text
phase1=9 create=55 modify=81 total=145 unique=145 duplicates=0
dirty=93 manifest_dirty=91 protected_dirty=2 outside=0 staged=0
create_present_at_head=0 modify_missing_at_head=0 future_000070_71=0
paired_000069=4 index_lock=0
```

Both protected files retained their expected SHA-256 values:

```text
go.mod
b767fc9ef9376c651d0493329b710ac8dcf8d77e52686b847d244aff9f6d48fd

recovery/testdata/rsync_local_to_remote.json
2570bd4472541322d902c4cdf2fe43f247b69f19d335558830e749567f763892
```

The branch is still `codex/backup-assets-controlled-recovery`; `HEAD`, local
`main`, local `origin/main` and live remote `main` all equal
`51771654a85967656fe1ca69686590b734ff9214`. The remote feature branch remains
absent and the index remains empty. No branch/worktree switch, stage, commit,
push or pull-request action occurred.

## Task 7 A3d R63/G63 direct-child read-only classification (2026-08-08)

R63 began with the exact frozen selector before the concrete target scan existed:

```text
go test ./internal/backupasset/recovery \
  -run '^TestRecoverySFTPTargetReconciliationClassificationMatrix$' -count=1
RED: target.ScanRecoveryRoot was undefined on *recoverySFTPTarget.
```

Controller-inline review then produced two additional genuine behavior REDs
before their matching corrections. The setup matrix proved that canonical-open
identity drift was not yet collapsed to the exact sanitized setup-unavailable
result. The empty-root/cursor matrix proved that a missing `jobs` directory was
not yet treated as a complete empty scan and that a valid non-empty future cursor
could be silently handled as a fresh R63 scan instead of failing closed before
target dependencies. Historical failure wall times not retained in carried
terminal output are not reconstructed.

The minimal final product implements only
`recoverySFTPTarget.ScanRecoveryRoot` and its private R63 helpers. It validates
the sealed root-scoped permit, opens the purpose-exact reconciliation session,
canonicalizes the registered root and `<root>/jobs`, validates the directory
identity before and after opening, and enumerates only direct children through
`ReadDir(64)`. It never follows a direct-child symlink, recursively enters an
unknown directory, or reads arbitrary user content. Only the fixed Recovery
owner marker and cleanup verified-artifact document are read for classification.

Remote component names remain inside the target boundary. The target recomputes
audit-key-versioned component tokens and compares them with `hmac.Equal`; only
an exact expected-token match can attach the DB-provided opaque job ID. Returned
findings contain only a closed category/kind, optional safe job ID and a
fixed-size HMAC fingerprint. The five frozen outcomes are implemented:
`known_healthy`, `known_drift`, `db_unmatched`, `forged_or_unknown` and
`scan_incomplete`. Historical cleanup ownership keys authenticate DB-unmatched
workspace markers and verified cleanup artifacts without exposing their raw
component names.

Setup key, resolver, dial, SFTP, jobs-open and opened-identity failures return
the exact sanitized unavailable result. Once the authenticated directory scan
has begun, `ReadDir`, `Lstat`, marker observation and close/cancellation
ambiguity return a normal incomplete blocked page. A missing `jobs` directory is
a complete scan, including exact missing-expected findings. Until R64 supplies
the authenticated cursor codec and prefix replay, a non-empty sealed cursor
returns `scan_incomplete` before resolver/session access.

Fresh final product evidence before Trellis/Git ledger reconciliation:

```text
R61--R63 adjacent normal selector                    PASS
R61--R63 adjacent race -count=5                      PASS (10.383s)
whole Recovery normal                               PASS (19.924s)
whole Recovery race                                 PASS (54.970s)
go vet ./internal/backupasset/recovery              PASS (0.659s)
make lint-backend                                   PASS (6.969s; 0 issues)
gofmt -d target.go target_test.go service.go        PASS (empty)
git diff --check                                    PASS
R63 implementation mutation/direct-log scan        PASS (zero matches)
```

The matrix explicitly asserts zero `Mkdir`, `Chmod`, `OpenFile`, `Rename`,
`Remove`, `RemoveDirectory` and `Sync` calls, bounded `ReadDir(64)`, no symlink
follow, raw-name absence from JSON and `%v/%+v/%#v`, sanitized dependency
errors and fixed-size fingerprints. No PostgreSQL gate is added for R63 because
this slice changes no database query, transaction, model, migration or
cross-engine behavior. R64--R66, A3d-V1, V14, Task 8/runtime/main and every Git
delivery action remain open; no goroutine, timer, retry or heartbeat was added.

Post-ledger structural reconciliation passed with the exact current state:

```text
Child task.py validate                               PASS (17/18 JSONL entries)
parent task.py validate                              PASS (0/0 JSONL entries)
task/parent JSON and Child JSONL parsing             PASS
phase1=9 create=55 modify=81 total=145 unique=145 duplicates=0
dirty=93 manifest_dirty=91 protected_dirty=2 outside=0 staged=0
create_present_at_head=0 modify_missing_at_head=0 future_000070_71=0 paired_000069=4
index_lock=0
```

Both protected files retain their required SHA-256 values:

```text
go.mod
b767fc9ef9376c651d0493329b710ac8dcf8d77e52686b847d244aff9f6d48fd

recovery/testdata/rsync_local_to_remote.json
2570bd4472541322d902c4cdf2fe43f247b69f19d335558830e749567f763892
```

The active branch remains `codex/backup-assets-controlled-recovery`. `HEAD`,
local `main`, local `origin/main` and live remote `main` all equal
`51771654a85967656fe1ca69686590b734ff9214`; the remote feature branch is absent.
The repository has nine linked worktrees in total: this active branch worktree
and eight detached Codex worktrees. R63 created, switched or modified none of
them. The index is empty and there is no index lock.

Controller-inline review against the R63 plan, PRD A3d read-only classification
contract and design 46.7--46.9/47.1 found no remaining Critical or Important
issue within this slice. R63/G63 is `focused_complete_checked`; A3d and Task 7
remain `in_progress`. R64--R66, A3d-V1, V14, Task 8/runtime/main and every
stage/commit/push/PR/CI/merge action remain open.

The final post-bookkeeping regression used the unchanged exact R63 selector and
passed:

```text
go test ./internal/backupasset/recovery \
  -run '^TestRecoverySFTPTargetReconciliationClassificationMatrix$' -count=1
PASS (package-reported 0.056s)
```

Child/parent Trellis validation, task/parent JSON and Child JSONL parsing, and
`git diff --check` were rerun after the bookkeeping edit and remained PASS.

## Task 7 A3d R64/G64 authenticated cursor and prefix-replay closure (2026-08-09)

R64 started from the checked R63 boundary. The exact new selector was run before
the cursor product existed:

    go test ./internal/backupasset/recovery \
      -run '^TestRecoveryReconciliationCursorPrefixReplay$' -count=1

It produced a genuine scanner-behavior RED in 16.423s: the pre-R64 target could
not satisfy authenticated continuation, prefix replay, cumulative page product
and hard-bound assertions. The first complete but unoptimized GREEN took
256.701s. A default ten-minute race invocation was then projected to exceed its
deadline and was deliberately stopped; that invocation is neither PASS nor FAIL
evidence and is not used for completion credit.

The performance correction prefilters impossible expected-token candidates
before expensive marker authentication while retaining exact constant-time
token comparison for viable candidates. Its first version incorrectly discarded
a known token whose marker phase had drifted; the existing
known_token_with_phase_drift classification row produced a genuine behavior RED.
The minimal correction preserved that candidate for known-drift classification
without restoring full expected-set work for every remote entry.

The final R64 product uses a bounded binary base64url cursor with a public
schema/key-version header and an HMAC-authenticated private body. Resume loads
only the recorded historical audit-key version, verifies the tag with
constant-time comparison, reopens the directory from the beginning and rebuilds
the exact prefix digest, cumulative counts and bounded findings before reading
any suffix. Session/root, expected-set digest, admission generation, ordinal,
prefix digest and page/chain/finding bounds are all bound. Marker and cleanup
artifact classification bind exact bytes. Page 256, chain 4096, findings 256,
expected rows 4096 and roots 1024 are accepted exactly; plus one fails closed.
Directory reads remain ReadDir(64), and every mutation counter remains zero.

The retained final product gate was:

    R62--R64 combined normal                              PASS (12.667s)
    R62--R64 combined race -count=5 -timeout 60m          PASS (243.307s)
    whole Recovery normal                                 PASS (32.176s)
    whole Recovery race                                   PASS (104.218s)
    go vet ./internal/backupasset/recovery                PASS
    make lint-backend                                     PASS (0 issues)
    owned gofmt -d                                        PASS (empty)
    git diff --check                                      PASS
    owned whitespace/conflict scan                        PASS (zero findings)

No new PostgreSQL gate was run for R64. This slice adds no database query, CAS,
lease, model, schema or migration behavior; its database-facing expected-set and
historical-key foundations retain the R62 required-real-PostgreSQL normal/race
evidence. This is the approved pure-target-slice boundary, not a claim that the
future R65 service orchestration has cross-engine coverage.

After the bookkeeping update, the fresh exact commands remained GREEN:

    go test ./internal/backupasset/recovery \
      -run '^TestRecoveryReconciliationCursorPrefixReplay$' -count=1
    PASS (package 12.294s; wall 13s)

    go test ./internal/backupasset/recovery \
      -run '^(TestRecoveryReconciliation|TestRecoverySFTPTargetReconciliation)' \
      -count=1
    PASS (package 12.730s; wall 14s)

The post-bookkeeping structural and Git boundary gate also passed:

    Child task.py validate                          PASS (17/18 JSONL entries)
    parent task.py validate                         PASS (0/0 JSONL entries)
    task/parent JSON and Child JSONL parsing        PASS
    phase1=9 create=55 modify=81 total=145 unique=145 duplicates=0
    dirty=93 manifest_dirty=91 protected_dirty=2 outside=0 staged=0
    create_present_at_head=0 modify_missing_at_head=0 future_000070_71=0
    protected hashes                                PASS
    branch/HEAD/main/origin-main                    PASS
    remote feature branch                           absent
    index lock / staged paths                       0 / 0
    git diff --check                                PASS

The protected hashes remain
b767fc9ef9376c651d0493329b710ac8dcf8d77e52686b847d244aff9f6d48fd for
go.mod and 2570bd4472541322d902c4cdf2fe43f247b69f19d335558830e749567f763892
for recovery/testdata/rsync_local_to_remote.json. HEAD, local main and local
origin/main remain 51771654a85967656fe1ca69686590b734ff9214.

The current read-only worktree inventory reports ten linked worktrees: exactly
one branch worktree for codex/backup-assets-controlled-recovery and nine detached
Codex worktrees. This differs from R63's earlier nine-total snapshot because the
environment now exposes one additional detached worktree; R64 closure created,
removed, switched or modified none of them.

Controller-inline review against R64, PRD A3d and design 46.7--46.9/47.1 found
no remaining Critical or Important defect within this slice. R64/G64 is
focused_complete_checked. Task 7 and Child 13 remain in_progress, the parent
remains planning and program delivery remains 12/15. R65--R66, A3d-V1, V14,
Task 8/runtime/main and every stage/commit/push/PR/CI/merge action remain open;
no goal, heartbeat, subagent, goroutine, timer or retry was introduced.

## Task 7 A3d R65 partial aggregate audit/alert and fresh downgrade evidence (2026-08-09)

R65 began from the checked R64 boundary. The first frozen selector was added
before production orchestration changed:

```text
go test ./internal/backupasset/recovery \
  -run '^TestRecoveryReconciliationWritesOneAggregateAudit$' -count=1
RED (package 0.066s): aggregate audit writes=0, want exactly one
```

The minimal product then added redacted `RecoveryReconciliationAlert`
formatting and one synchronous result-publish boundary. Every completed clear
or blocked pass writes one `AuditActionRecoveryCleanup` aggregate with
`AuditFieldOperation="recovery_reconcile"`, closed status and bounded scanned
count, and invokes the required finding sink once with a cloned bounded product.
Both calls are attempted exactly once; exact caller cancellation wins, and an
audit or alert failure returns only `ErrRecoveryReconciliationUnavailable` and
never returns clear.

The downgrade selector also produced a genuine pre-orchestration RED:

```text
go test ./internal/backupasset/recovery \
  -run '^TestRecoveryReconcileDowngradeReadinessRequiresFreshAllRootsClear$' \
  -count=1
RED (package 0.143s): the stub returned reconciliation unavailable and made
zero root scans.
```

The GREEN implementation keeps public `ReconcileRoot` to one bounded page and
adds only explicit synchronous downgrade orchestration. It lists and validates
the complete non-empty unique root set, binds one sticky admission generation
to every pass, rebuilds every expected set, follows only a structurally valid
zero-finding healthy cumulative pagination product, and stops immediately on
substantive findings, absent/invalid cursor, incomplete product or unavailable
dependency. Each root is capped at 16 passes/4096 cumulative entries. Repeated
calls under the same generation and a later generation both rescan; no result
cache, goroutine, timer, retry or heartbeat exists.

Fresh retained local gates:

```text
R65 exact normal                                             PASS (0.241s)
R65 exact race -count=5                                     PASS (4.458s)
R61--R65 Recovery/Settings combined normal                  PASS (12.848s / 0.012s)
R61--R65 Recovery/Settings combined race -count=5           PASS (247.261s / 1.152s)
whole Recovery normal                                       PASS (32.611s)
whole Recovery race                                         PASS (106.325s)
go vet ./internal/backupasset/recovery                      PASS
make lint-backend                                           PASS (0 issues)
owned gofmt -d                                              PASS (empty)
git diff --check                                            PASS
```

R65 is not `focused_complete_checked`. Two material gates remain:

1. The required `xirang-c13-pg` container is absent from `docker ps -a`, and
   `TEST_POSTGRES_DSN` is unconfigured. No existing fixture was restarted or
   replaced and no new database infrastructure was created, so required-real-
   PostgreSQL normal/race no-skip evidence is still missing.
2. Controller cross-boundary review found a frozen-plan conflict. The service
   must send `AuditFieldOperation="recovery_reconcile"` with existing
   `AuditActionRecoveryCleanup`, but foundation `NewAuditEvent` calls
   `validatePublicationAuditOperationField`, which rejects every operation field
   unless the action is `AuditActionResticLegacyOperationBlocked`. The same R65
   plan explicitly says not to modify `backupasset/audit_action.go`. The spy-level
   service contract is GREEN, but the real foundation writer would reject this
   aggregate input, so R65 cannot claim production audit success until the
   written constraint or audit representation is explicitly corrected.

The branch remains `codex/backup-assets-controlled-recovery`; HEAD, local main
and local origin/main remain
`51771654a85967656fe1ca69686590b734ff9214`. The remote feature branch remains
absent, ten worktrees remain (one branch plus nine detached), and staged paths
remain zero. R66, A3d-V1, V14, Task 8/runtime/main and all Git-delivery actions
remain unstarted.

## Task 7 A3d R65/G65 foundation contract and PostgreSQL closure (2026-08-09)

The user explicitly approved the smallest correction to the previously frozen
foundation constraint: admit only `AuditActionRecoveryCleanup` with
`AuditFieldOperation="recovery_reconcile"`. No new action or field was added.
The required-real-PostgreSQL gate was also authorized to use one isolated
temporary fixture because the former `xirang-c13-pg` fixture no longer existed.

Before the foundation implementation changed,
`TestRecoveryReconciliationAuditOperationIsPurposeExact` produced a genuine RED:
`NewAuditEvent` returned `ErrInvalidState` for the required cleanup/reconcile
pair. The same test freezes the negative matrix for another cleanup operation,
a non-string value, and reuse of `recovery_reconcile` by another action.

The minimal GREEN adds one exact branch in
`validatePublicationAuditOperationField`. It accepts only the approved pair;
all three negative rows still return `ErrInvalidState`. The existing service
contract therefore reaches the real foundation writer without weakening the
legacy Restic operation allowlist or any other audit action.

Fresh post-correction local gates:

```text
foundation plus exact R65 normal                              PASS
foundation plus exact R65 race -count=5                      PASS
R61--R65 combined normal                                     PASS
R61--R65 combined race -count=5                              PASS (Recovery 246.265s)
whole Recovery normal                                        PASS (32.779s)
whole Recovery race                                          PASS (105.478s)
full internal/backupasset                                    PASS (0.488s)
go vet ./internal/backupasset ./internal/backupasset/recovery PASS
make lint-backend                                            PASS (0 issues)
owned gofmt -d                                               PASS (empty)
git diff --check                                             PASS
```

One disposable PostgreSQL 18 fixture named
`xirang-r65-pg-temp-20260809` ran only for the approved gate. The R65 exact
required selector passed normal in `0.148s` and race `-count=5` in `2.231s`,
with required mode enabled and no skip. Schema and command-scoped role residue
were both `0/0` before and after. The DSN and credentials were neither printed
nor written to disk. The container and its anonymous volume were removed, and
the final label-filtered container/volume residue count was zero.

R65/G65 is now `focused_complete_checked`. Task 7 and Child 13 remain
`in_progress`, the parent remains `planning`, and program delivery remains
12/15. R66 is next. A3d-V1, V14, Task 8/runtime/main, stage, commit, push and PR
remain open.

## Task 7 A3d R66/G66 and A3d-V1 closure (2026-08-09)

R66 added the frozen dependency/resource, privacy and static zero-mutation
matrices. The service matrix covers exact caller-context identity before invalid
authority, invalid authority before dependencies, registry list/root resolution,
database transaction, audit-key, audit writer and alert sink failures. The SFTP
matrix covers key/resolver/dial/open, non-nil SSH/SFTP/jobs/marker resources
returned with errors, `ReadDir`, `Lstat`, marker read, concurrent cancellation
and close noise. Owned handles close at most once in reverse ownership order,
and no ambiguous close path can return clear.

The nil-receiver/nil-clock context row produced a genuine behavior RED:
`ScanRecoveryRoot` returned `ErrInvalidTargetPermit` for a canceled non-nil
context instead of the exact `context.Canceled`. The minimal production fix in
`target.go` establishes the frozen precedence:

1. nil context returns invalid permit;
2. an existing caller context error returns exactly;
3. nil receiver or nil clock returns invalid permit;
4. only then may authority validation or dependency access begin.

The same row and the complete resource matrix turned GREEN. No other production
behavior changed for R66. The four exact matrices passed together normally in
`0.765s` and under race `-count=5` in `3.678s`:

```text
TestRecoveryReconciliationDependencyErrorAndContextMatrix
TestRecoveryReconciliationPrivacyCanaryMatrix
TestRecoverySFTPTargetReconciliationResourceErrorAndPrivacyMatrix
TestRecoveryTargetReconciliationPortStaticZeroMutationGate
```

The static gate finds no reachable `Rename`, `Remove`, `RemoveDirectory`,
`Mkdir`, `Chmod` or `OpenFile` call in the reconciliation port/helpers. Result,
cursor, alert, audit, metrics-compatible labels, formatted values and captured
failure products contain none of the host/user/credential/root locator,
path/name, token/HMAC input, marker/content, digest input, SFTP status or raw
dependency-error canaries. The reconciliation implementation performs no direct
logging.

Fresh A3d-V1 dynamic evidence on the final post-format test tree is:

```text
R61--R66 combined normal -count=5                         PASS (56.612s)
R61--R66 combined race -count=5                           PASS (259.003s)
whole Recovery normal                                    PASS (31.729s)
whole Recovery race                                      PASS (106.913s)
```

The earlier retained production-fix gates also passed R61--R66 combined normal
in `54.924s`, race `-count=5` in `246.333s`, whole Recovery normal in `31.924s`
and race in `106.300s`. R61 sshutil/settings focused normal and race `-count=5`
also pass.

A one-use isolated `postgres:18-alpine` fixture ran the required Recovery
PostgreSQL selectors with required mode enabled and no skip: normal `8.023s`,
race `11.200s`. Paired migration evidence passed SQLite paired-file validation
in `0.059s`, `TestBackupAssetMigration069SQLite` in `7.466s`, and required
PostgreSQL `TestBackupAssetMigration069Postgres` plus paired validation in
`132.853s`. Temporary schema and command-scoped role residue was zero; the
container and anonymous volume were removed, and final label-filtered container
and volume residue was zero. No existing fixture was restarted or replaced.

The user-approved R65 foundation exception remains exactly two independently
allow-listed paths:

```text
backend/internal/backupasset/audit_action.go
backend/internal/backupasset/audit_action_test.go
```

Only `AuditActionRecoveryCleanup` plus
`AuditFieldOperation="recovery_reconcile"` is accepted. Another cleanup
operation, a non-string value and cross-action reuse remain rejected. The base
product manifest remains `9 + 55 + 81 = 145`; the structural gate reports the
two paths separately as `approved_exception`.

Final quality and structural evidence before bookkeeping was:

```text
go vet ./internal/backupasset/recovery                    PASS
make lint-backend                                         PASS (0 issues)
owned gofmt -d                                            PASS (empty)
git diff --check                                          PASS
privacy/direct-log/zero-mutation/whitespace/conflict      PASS
Child task.py validate                                    PASS (17/18 JSONL entries)
parent task.py validate                                   PASS (0/0 JSONL entries)
task/parent JSON and Child JSONL parsing                  PASS
phase1=9 create=55 modify=81 total=145 unique=145 duplicates=0
dirty=95 manifest_dirty=91 protected_dirty=2 approved_exception_dirty=2 outside=0 staged=0
create_present_at_head=0 modify_missing_at_head=0 future_000070_71=0
```

Protected hashes remain
`b767fc9ef9376c651d0493329b710ac8dcf8d77e52686b847d244aff9f6d48fd`
for `go.mod` and
`2570bd4472541322d902c4cdf2fe43f247b69f19d335558830e749567f763892`
for `recovery/testdata/rsync_local_to_remote.json`.

Controller-inline review against PRD A3d and design
46.1/46.7--46.9/47.1 found no remaining Critical or Important issue. A3d and
A3d-V1 are `focused_complete_checked`; Task 7 and Child 13 remain
`in_progress`, the parent remains `planning`, and program delivery remains
12/15. V14, Task 8/runtime/main and every stage/commit/push/PR/CI/merge action
remain open. No goal, heartbeat or subagent was introduced.

The `trellis-update-spec` review found no project-wide code-spec update to make
at this checkpoint. The recovery reconciliation operation pair is an unshipped,
task-local product exception already frozen in `implement.md`; copying it into
`.trellis/spec/` before V14 would widen the approved workflow scope and create a
second source of truth. Reassess code-spec promotion only after whole Task 7
review and delivery disposition.

## Task 7 V14 whole-scope review and closure (2026-08-09)

V14 re-read every Task 7 acceptance against design sections 33--47, the final
product symbols and fresh selectors. The only remaining unchecked Task 7 PRD
rows were the seven target-root/immutable-plan-snapshot rows and the six A2a
exact-plan regular-file Verify rows. Their fresh traceability is:

| Acceptance product | Fresh symbols/selectors |
|---|---|
| private multi-root v2 registry, strict stored-document validation | `TestRecoveryTargetRootRegistryPersistsPrivateV2Records`, `TestRecoveryTargetRootRegistryRejectsInvalidDefinitions`, `TestRecoveryTargetRootRegistryRejectsInvalidStoredRecords` |
| generic settings/config isolation | `TestRecoveryTargetRootRegistryKeysNeverUseGenericSettings`, `TestConfigExportAndImportExcludeRecoveryTargetRootRegistry` |
| transaction-bound encrypted plan snapshot, zero-write failure, replay and rotation | `TestRecoveryPlanSnapshotsResolvedTargetRootLocator`, `TestRecoveryPlanTargetRootResolutionFailsClosedBeforeWrites`, `TestRecoveryPlanIdempotentReplayUsesFrozenTargetRootSnapshot`, `TestRecoveryPlanTargetRootRotationCannotCrossBind` |
| sealed exact-plan observation authority at every issuance flow | `TestTargetVerifyPermitRequiresExactPrivateSessionProof`, `TestRecoveryOrdinaryVerifyIssuanceUsesExactLockedTargetSessionBinding`, `TestRecoveryDeleteObservationIssuanceUsesExactLockedTargetSessionBinding`, `TestRecoveryAdoptionVerifyIssuanceUsesExactLockedTargetSessionBinding` |
| purpose-exact regular-file observation, namespace, drift, cancellation and deferred capabilities | `TestRecoverySFTPTargetVerifyPresentRegularFile`, `TestRecoverySFTPTargetVerifyNamespaceAndObservationRevision`, `TestRecoverySFTPTargetVerifyRejectsPathContentAndStatDrift`, `TestRecoverySFTPTargetVerifyCancellationAndErrors`, `TestRecoverySFTPTargetA2aDeferredMethodsOpenNoSession` |
| closed present product and opaque stable revision | `TestTargetVerifyClosedProductAndOpaqueRevision`, `TestRecoverySFTPTargetObservationRevisionIsExact` |

The full flow review traced plan snapshot -> preflight -> execute aggregate ->
worker/target write and observation -> ResultSet publication and Content delivery
-> retain/revoke/drain -> owned-workspace cleanup -> logical reconciliation.
Every explicit Task 7 product remains callable in tests without Task 8
runtime/main composition. Review of the Task 7 product delta found zero open
Critical or Important security, authority, crash, race, privacy, resource or
cross-engine defect.

Fresh V14 dynamic evidence on the final product tree was:

```text
A3b/A3c/A3d focused normal                              PASS (13.134s)
A3b/A3c/A3d focused race -count=5                       PASS (287.599s)
whole Recovery normal                                   PASS (30.007s)
whole Recovery race                                     PASS (103.294s)
A2a + target-root/plan-snapshot normal                  PASS (0.370s)
A2a + target-root/plan-snapshot race -count=5           PASS (6.795s)
sshutil/settings/config focused normal                  PASS (0.012s / 0.051s / 0.068s)
sshutil/settings/config focused race -count=5           PASS (1.187s / 1.767s / 1.800s)
```

The former `xirang-c13-pg` fixture was absent. Under the user's explicit
approval, V14 created one isolated `postgres:18-alpine` fixture named
`xirang-v14-pg-20260809-01`. The first test invocation used a libpq keyword DSN;
the test harness rejected it before any connection because the Recovery helpers
at `recovery/testutil_test.go:496` and `behavior_integration_test.go:848` require
a PostgreSQL URL. No product or test code changed. The final commands constructed
a `postgresql://` URL only inside the process and did not print or persist the
DSN or credentials:

```text
required Recovery PostgreSQL normal, no skip            PASS (8.528s)
required Recovery PostgreSQL race, no skip              PASS (12.067s)
complete TestBackupAssetMigration069 SQLite/PostgreSQL   PASS (293.149s)
temporary schemas before/after                          0 / 0
additional roles before/after                           0 / 0
future 000070/000071 files                               0
```

The fixture container and anonymous volume were removed. Final checks found
`xirang-v14-pg-20260809-01=0`, its volume-prefix residue `=0`, and the old
`xirang-c13-pg=0`; no old fixture was restarted, replaced or removed.

Fresh static and structural evidence before bookkeeping was:

```text
go vet ./internal/backupasset/recovery                    PASS
make lint-backend                                         PASS (0 issues)
owned dirty-Go gofmt -d                                   PASS (empty)
git diff --check                                          PASS
whitespace / conflict-marker / direct-log scans           PASS
PosixRename / filepath / caller registry-path scans       PASS
privacy / read-only zero-mutation selector gates          PASS
Child task.py validate                                    PASS (17/18 JSONL entries)
parent task.py validate                                   PASS (0/0 JSONL entries)
task/parent JSON and Child JSONL parsing                  PASS
phase1=9 create=55 modify=81 total=145 unique=145 duplicates=0
dirty=95 manifest_dirty=91 protected_dirty=2 approved_exception_dirty=2 outside=0 staged=0
create_present_at_head=0 modify_missing_at_head=0 future_000070_71=0 index_lock=0
```

The structural review found one process-only path-label defect in V14's written
hash gate. The dirty protected fixture has always been
`recovery/testdata/rsync_local_to_remote.json`; the similarly named
`backend/internal/backupasset/recovery/testdata/rsync_local_to_remote.json` is a
different manifest create path and remains absent. The plan label was corrected
without touching either product scope or fixture contents. The protected hashes
are:

```text
go.mod
b767fc9ef9376c651d0493329b710ac8dcf8d77e52686b847d244aff9f6d48fd

recovery/testdata/rsync_local_to_remote.json
2570bd4472541322d902c4cdf2fe43f247b69f19d335558830e749567f763892
```

Final read-only Git evidence before bookkeeping was:

```text
branch                       codex/backup-assets-controlled-recovery
HEAD/main/origin-main/remote main
                             51771654a85967656fe1ca69686590b734ff9214
remote feature branch        absent
worktrees                    10 (one branch plus nine detached)
index lock / staged paths    0 / 0
```

Task 7 is therefore `complete_checked_whole_scope`. Child 13 remains
`in_progress` because Tasks 8--12 and Git delivery are still open. The parent
remains `planning`, program delivery remains 12/15, and V14 performed no
runtime/main wiring, stage, commit, push, pull request, CI, merge or release
action. No goal, heartbeat or subagent was used.

Post-bookkeeping verification reran whole Recovery normal (`29.809s`), the
target-root settings/config selectors (`0.083s` / `0.073s`), Recovery vet,
full backend lint (`0 issues`), `git diff --check`, Child/parent validators and
all JSON/JSONL parses; every command exited zero. The final structural rerun
retained `145/145` unique manifest paths, dirty accounting
`95/91/2/2/0/0`, both protected hashes, Task 7/V14
`complete_checked_whole_scope`, Child `in_progress`, parent `planning`, the
recorded branch/baseline and staged paths zero.

## Tasks 1--7 checkpoint delivery authorization (2026-08-10)

The user explicitly required the project-memory commit and cleanup flow to
finish before Task 8. The pre-delivery audit classified all 95 dirty paths:
91 are dirty members of the frozen 145-path Child 13 manifest, two are the
user-approved R65 Foundation exception, and exactly two are pre-existing
protected unrelated paths. The resulting local work-commit allow-list is 93
paths; `go.mod` and `recovery/testdata/rsync_local_to_remote.json` remain
excluded with their recorded hashes, outside-scope paths are zero and staged
paths are zero before the fresh gate.

This checkpoint contains the cumulative Tasks 1--7 result, not Task 7 alone.
It does not archive Child 13 or claim Tasks 8--12, push, PR, CI, merge or release
completion. Task 8 remains stopped until the local checkpoint and session record
are complete and the only remaining dirty paths are the two protected unrelated
files.

The fresh pre-commit `make check` gate completed with exit zero on 2026-08-10:
backend lint reported `0 issues`; all backend packages passed; frontend lint had
zero errors and one pre-existing accessibility warning; all 168 frontend test
files and 1388 tests passed; backend build and the TypeScript/Vite production
build succeeded. Required-real-PostgreSQL evidence remains the V14 no-skip
one-use-fixture run above; the removed fixture was not recreated merely for the
local commit gate.

## Tasks 1--7 checkpoint PR #410 hosted-CI remediation (2026-08-10)

The local checkpoint was committed as `fe4eb47` and its session record as
`185b481`, then pushed on `codex/backup-assets-controlled-recovery` and opened as
PR #410. The PR remains mergeable but blocked by required checks while this
remediation is uncommitted. Hosted run `31344568520` provides the retained RED
evidence:

```text
Backend lint action
  failed to fetch pull request patch: diff exceeded 20,000 lines

Backend Test & Build
  TestRecoveryAuthorizationReceiptConcurrentSQLiteWinner/SameIntentReplay
  one caller returned recovery authorization is unavailable
  TestBackupAssetMigration069SQLite ordinary fixture rows
  failed because DATA_ENCRYPTION_KEY was absent

PostgreSQL Migration Parity
  TestBackupAssetMigration069Postgres ordinary fixture rows
  failed for the same absent DATA_ENCRYPTION_KEY requirement

Asset Worker Build & Scan (amd64 and arm64)
  golang.org/x/text v0.38.0 / CVE-2026-56852 / HIGH
  fixed version 0.39.0
```

The narrow worktree correction is six tracked files. The existing manifest owns
the workflow, Recovery service/test and paired migration fixture paths. Only
`backend/go.mod` and `backend/go.sum` are added as delivery-only dependency
exceptions. The root-level protected `go.mod` and
`recovery/testdata/rsync_local_to_remote.json` remain unrelated and excluded.

Implementation disposition before the final fresh gate:

- ordinary DDL-only migration rows use fixed valid test ciphertext; first-write
  and worker behavior rows that traverse model decryption use test-scoped real
  ciphertext from `secure.EncryptString`;
- authorization receipt, proof and immutable intent reads share the existing
  bounded SQLite conflict retry policy, preserve caller cancellation and expose
  only the existing closed public errors;
- a three-row regression injects a transient lock at receipt, proof and intent
  lookup respectively;
- `golang.org/x/text` is upgraded to `v0.39.0`, with tidy-required `x/mod
  v0.37.0` and `x/tools v0.47.0` synchronization;
- backend lint no longer requests a GitHub PR patch and instead runs the full
  repository lint gate.

The retained pre-bookkeeping worktree checks passed the full SQLite `000069`
fixture without an encryption environment, the real first-write selector,
authorization focused normal `-count=100` and race `-count=20`, and `go mod
verify`. These results are supporting evidence only; the final pre-commit gate is
rerun after this Trellis record and recorded separately below. No Task 8,
PostgreSQL fixture creation, stage, fix commit, push, merge or cleanup action has
been taken by this remediation checkpoint yet.

The first fresh full-project gate found one over-broad fixture assumption and
exited nonzero. `TestBackupAssetRecoveryWorkerBehaviorSQLite` and
`TestBackupAssetMigration069WorkerCancelQueuedSQLite` load the job through GORM,
so the model hook tried to decrypt the fixed DDL ciphertext and returned the
expected `解密数据格式错误`. This was a genuine regression RED in the pending CI
fixture correction, not a Task 7 product defect.

Backward tracing showed that every pure SQL migration selector only needs a
schema-valid opaque ciphertext, while exactly those two non-first-write behavior
fixtures cross the model-decryption boundary. The minimal correction adds a
private seed option only for that boundary and gives the two tests a scoped fake
test key; it does not make ordinary DDL fixtures depend on environment secrets or
change first-write product data. Fresh GREEN evidence is:

```text
authorization busy/winner normal -count=100                 PASS (13.526s)
authorization busy/winner race -count=20                    PASS (10.802s)
full SQLite 000069 + paired files, encryption env unset     PASS (9.331s)
whole Recovery normal                                       PASS (35.504s)
whole Recovery race                                         PASS (110.885s)
go mod verify                                                PASS
go vet ./...                                                 PASS
make lint-backend                                            PASS (0 issues)
owned gofmt -d                                               PASS (empty)
git diff --check                                             PASS

worker claim + queued-cancel regression selectors           PASS (0.331s)
full SQLite 000069 + paired files after fixture refinement  PASS (7.562s)
fresh make check                                             PASS
  backend lint                                               0 issues
  backend packages                                           all passed
  frontend lint                                              0 errors, 1 pre-existing warning
  frontend tests                                             168 files / 1388 tests passed
  backend build                                              passed
  TypeScript/Vite production build                           passed
```

Required real PostgreSQL and Worker image scan closure remains assigned to the
hosted PR run after the fix commit is pushed. The local run did not create or
restart PostgreSQL infrastructure and did not claim local Trivy evidence.

## PR #410 second hosted-CI remediation and fresh local gate (2026-08-10)

Hosted runs `31348175030` and `31348176438` passed PostgreSQL migration parity,
frontend, Docker, Worker runtime/build/scan, documentation and UTC-safety checks.
Both Backend Test & Build jobs failed on the remaining concurrency windows:

```text
TestPlanCreateConcurrentDifferentIntentElectsOneWinner
  retry exhaustion returned unavailable instead of resolving the durable winner
TestRecoveryAuthorizationReceiptConcurrentSQLiteWinner/SameIntentReplay
  proof use became visible before the final same-intent receipt replay
TestTriggerRegistersCancelOwnerBeforeReturning/ordinary
  post-barrier executor scheduling violated a test-only zero-call assertion
```

Two new deterministic selectors first reproduced the missing durable-winner and
proof-visibility behavior. The minimal product correction reads the plan winner
after each retryable transaction failure and replays the same authorization
receipt after proof use becomes visible. The task-manager production contract was
already correct: the test continues to prove cancel-owner registration before
return and no longer asserts executor non-entry after releasing its barrier.

Fresh post-edit evidence:

```text
five affected selectors normal -count=10             PASS (Recovery 1.542s, task 0.230s)
five affected selectors race -count=5                PASS (Recovery 3.481s, task 1.280s)
whole Recovery and task normal                       PASS (31.810s / 2.838s)
whole Recovery and task race                         PASS (103.487s / 5.514s)
full SQLite 000069 plus paired files, env unset      PASS (7.240s)
go mod verify                                        PASS
go vet ./...                                         PASS
make lint-backend                                    PASS (0 issues)
owned gofmt -d / git diff --check                    PASS (empty)
fresh make check                                     PASS
  backend packages                                   all passed
  frontend lint                                      0 errors, 1 pre-existing warning
  frontend tests                                     168 files / 1388 tests passed
  backend and TypeScript/Vite production builds      passed
Child and parent task.py validate                    PASS (17/18 and 0/0 entries)
task/parent JSON and Child JSONL parsing             PASS (2 JSON / 35 JSONL rows)
exact manifest                                       9/55/81/145/145, duplicates 0
```

Before bookkeeping, branch is
`codex/backup-assets-controlled-recovery`, HEAD and remote feature are
`280aad979fc47232ef6a8a4394443a628c1c7e3b`, local `main`, `origin/main` and the
merge base are `51771654a85967656fe1ca69686590b734ff9214`, PR #410 is open and
blocked only by the old Backend Test & Build results, and staged paths are zero.
The generated `backend/coverage.out` was removed. Protected unrelated paths
remain excluded and byte-identical:

```text
go.mod
b767fc9ef9376c651d0493329b710ac8dcf8d77e52686b847d244aff9f6d48fd
recovery/testdata/rsync_local_to_remote.json
2570bd4472541322d902c4cdf2fe43f247b69f19d335558830e749567f763892
```

No PostgreSQL fixture or other infrastructure was created or restarted for this
second local remediation. Hosted PR checks own fresh PostgreSQL and Worker scan
closure after the exact fix commit is pushed. Task 8 remains stopped through PR
GREEN, squash merge, post-merge automation disposition, local-main sync, closure
bookkeeping and topic cleanup.

## PR #410 third hosted-CI remediation and final-commit winner race (2026-08-10)

The second correction was committed and pushed as `4e5072b`. Duplicate hosted
runs `31351415372` and `31351417378` passed every PostgreSQL, frontend, Docker,
Worker runtime/build/scan, documentation and UTC-safety check; the push run's
Backend Test & Build also passed. The merge-ref Backend Test & Build retained one
timing failure:

```text
TestPlanCreateConcurrentDifferentIntentElectsOneWinner
  different-intent CreatePlan() error = recovery plan is unavailable
```

Backward tracing confirmed a final visibility window. After the last retryable
transaction failure, the losing invocation performed exactly one immediate
non-transactional replay read. That read could finish while the winner was still
inside its commit seam, and the loop then returned unavailable without any
further durable observation.

`TestPlanCreateRetryExhaustionWaitsForCommittingWinner` deterministically blocks
the winner at `planCreateBeforeCommit`, injects retryable failures into all loser
transaction replay reads, lets the loser's final non-transactional replay observe
the uncommitted winner, and only then releases commit. Against `4e5072b` the
unchanged test produced the genuine expected RED:

```text
retry-exhausted loser error=recovery plan is unavailable,
want durable idempotency conflict
```

The minimal GREEN adds a bounded context-aware durable-winner observation phase
only after the ordinary plan-create attempts and immediate replay reads are
exhausted. A visible same-intent winner is replayed, a different-intent winner is
the existing stable idempotency conflict, caller cancellation/deadline preserves
identity, and exhaustion without a winner remains the existing closed
unavailable result. No schema, migration, dependency, runtime, public API or
protected-path change is involved.

Fresh local evidence after the GREEN:

```text
four plan selectors normal -count=20                         PASS (3.166s)
four plan selectors race -count=10                          PASS (4.551s)
whole Recovery normal                                       PASS (34.006s)
whole Recovery race                                         PASS (110.354s)
env -u NODE_ENV make check                                  PASS
  backend lint                                               0 issues
  backend packages                                           all passed
  frontend lint                                              0 errors, 1 pre-existing warning
  frontend tests                                             168 files / 1388 tests passed
  backend and TypeScript/Vite production builds              passed
owned gofmt and git diff --check                             PASS
```

The generated `backend/xirang-server` was removed. No PostgreSQL fixture or other
infrastructure was created or restarted; the next hosted PR run owns fresh
PostgreSQL and Worker scan closure. Before bookkeeping, the only dirty product
paths are `service.go` and `service_test.go`; the two protected unrelated paths
retain their frozen hashes. Task 8 remains stopped through exact commit/push, PR
GREEN, squash merge, post-merge automation, local-main sync and delivery cleanup.

## PR #410 merge and post-merge delivery disposition (2026-08-10)

Final product commit `a873b49415347a39ffc2e5819f67649ce29d5f4b` closed the
retry-exhaustion winner visibility window. Pull-request run `31353319143` and
the rerun-complete push run `31353316583` both concluded successfully. The
required check set included Backend Test & Build, PostgreSQL Migration Parity,
Frontend Test & Build, Docker Build, Worker runtime/build/scan on amd64 and
arm64, documentation freshness, migration UTC safety and PR title validation.

PR #410 was squash-merged at `2026-08-10T04:07:42Z` as
`def0086da561bc2c1b26c34c1efa6dacf020c3bc`. The local and remote
`codex/backup-assets-controlled-recovery` branches were deleted. Main CI run
`31354534726` passed on the squash commit. Release Please run `31354534699`
also passed; its only job was `Prepare Release PR`, and the existing open release
PR #386 now carries `chore(main): release 0.46.0`. `gh release list` still shows
`v0.45.0` as the latest release, so this merge created no stable tag or GitHub
Release. Consequently no `Publish Docker Images` or
`Sync Docker Hub Description` run was expected.

After fetch/prune, local `main`, `origin/main` and HEAD were equal to `def0086`.
The feature branch was absent locally and remotely. Detached Codex worktrees were
left untouched. The worktree contained only the two protected historical
untracked paths:

```text
go.mod
b767fc9ef9376c651d0493329b710ac8dcf8d77e52686b847d244aff9f6d48fd

recovery/testdata/rsync_local_to_remote.json
2570bd4472541322d902c4cdf2fe43f247b69f19d335558830e749567f763892
```

The shared code-spec review found no new reusable implementation contract to
promote: the durable recovery contracts were already captured by Task 7 and the
paired migration/database spec, while the facts above are delivery history.
No `.trellis/spec/` file changes in this closure.

Task 7 is delivered and merged. Child 13 remains `in_progress` because Tasks
8--12 remain open; its parent remains `planning` and program delivery remains
12/15. The Child is intentionally not archived. Task 8 remains stopped until the
bookkeeping branch itself passes CI, merges, receives a post-merge disposition,
local `main` is resynchronized and the bookkeeping branch is removed.

## Task 8 managed runtime first TDD slice (2026-08-12)

The first lifecycle RED adds focused contracts for metadata reconciliation
before publication, no queued-work execution during `Startup`, and rejection of
nil or duplicate graph publication. The initial formatting command used a
repo-root-relative path while already inside `backend`; it failed before the Go
compiler and is not counted as RED evidence. The corrected unchanged selector
produced the genuine missing-feature failure:

```text
cd backend
go test ./internal/backupasset/runtime \
  -run '^TestManagedRecovery(RuntimeStartup|Publication)' -count=1

undefined: newManagedRecoveryPublication
undefined: managedRecoveryGraph
undefined: newManagedRecoveryRuntime
undefined: managedRecoveryRuntimeDependencies
FAIL xirang/backend/internal/backupasset/runtime [build failed]
```

The minimal graph owner and worker wrapper then produced GREEN while delegating
all durable claim and takeover selection to the existing coordinator methods:

```text
cd backend
go test ./internal/backupasset/runtime \
  -run '^TestManagedRecovery(RuntimeStopAccepting|WorkerWake)' -count=1
ok xirang/backend/internal/backupasset/runtime 0.053s
```

The genuinely absent Task 8 Recovery metrics test file then produced RED:

```text
cd backend
go test ./internal/backupasset/recovery -run '^TestRecoveryMetrics' -count=1

undefined: NewPrometheusMetrics
undefined: MetricOutcomeSuccess
undefined: MetricOutcome
FAIL xirang/backend/internal/backupasset/recovery [build failed]
```

No production file had been edited when this RED was observed.

The minimal managed publication/runtime implementation then produced GREEN:

```text
cd backend
go test ./internal/backupasset/runtime \
  -run '^TestManagedRecovery(RuntimeStartup|Publication)' -count=1
ok xirang/backend/internal/backupasset/runtime 0.055s
```

The next unchanged lifecycle/scheduler selector produced RED for the missing
ordered graph teardown callbacks, sticky stop/shutdown owner methods, and
managed worker wrapper:

```text
cd backend
go test ./internal/backupasset/runtime \
  -run '^TestManagedRecovery(RuntimeStopAccepting|WorkerWake)' -count=1

unknown field stopClaims in managedRecoveryGraph
unknown field cancelJoinAttempts in managedRecoveryGraph
unknown field fenceOwnership in managedRecoveryGraph
unknown field revokeDrainDelivery in managedRecoveryGraph
unknown field shutdownLifecycle in managedRecoveryGraph
manager.StopAccepting undefined
manager.Shutdown undefined
undefined: newManagedRecoveryWorker
FAIL xirang/backend/internal/backupasset/runtime [build failed]
```

The managed worker originally selected directly on `time.After` inside its
loop. A focused timer-ownership test required one reusable timer for the worker
lifetime, no timer allocation or reset for job wakes, one reset after a
takeover deadline, and an explicit stop on worker exit. The unchanged test
produced the genuine missing-seam RED:

```text
cd backend
go test ./internal/backupasset/runtime \
  -run '^TestManagedRecoveryWorkerReusesAndStopsTakeoverTimer$' -count=1

unknown field NewTimer in struct literal of type managedRecoveryWorkerDependencies
undefined: managedRecoveryTimer
FAIL xirang/backend/internal/backupasset/runtime [build failed]
```

The minimal implementation owns one injected/resettable timer and delegates
all durable claim/takeover decisions to the existing coordinator. The same
selector then produced GREEN:

```text
cd backend
go test ./internal/backupasset/runtime \
  -run '^TestManagedRecoveryWorkerReusesAndStopsTakeoverTimer$' -count=1
ok xirang/backend/internal/backupasset/runtime 0.055s
```

The next lifecycle selector froze single ownership for `Run` and required
`Shutdown` to cancel and join that owner. Before the lifecycle state existed,
the unchanged selector observed the genuine duplicate-owner RED:

```text
cd backend
go test ./internal/backupasset/runtime \
  -run '^TestManagedRecoveryRuntimeConcurrentRunHasOneOwnerAndShutdownJoinsIt$' -count=1

concurrent Run started graph 2 times, want one owner
FAIL
```

The minimal GREEN records one process-lifetime `Run` owner, its cancel function
and completion channel under a dedicated mutex. Concurrent `Run` calls return;
`Shutdown` cancels and context-boundedly joins the owner without waiting under
the lifecycle lock:

```text
cd backend
go test ./internal/backupasset/runtime \
  -run '^TestManagedRecoveryRuntimeConcurrentRunHasOneOwnerAndShutdownJoinsIt$' -count=1
ok xirang/backend/internal/backupasset/runtime 0.076s
```

A follow-up 100-iteration stress matrix concurrently races `Run`,
`TransitionSettings`, `StopAccepting` and `Shutdown`; it verifies bounded joins,
closed invalid-state outcomes and zero graph publication after shutdown. It
passed under the race detector:

```text
cd backend
go test -race ./internal/backupasset/runtime \
  -run '^TestManagedRecoveryRuntimeRunTransitionStopAndShutdownRace$' -count=1
ok xirang/backend/internal/backupasset/runtime 1.547s
```

## Task 8 managed runtime continuation (2026-08-13)

### Transition join, readiness, facades and composition

The graph-replacement contract was strengthened so persistence cannot begin
until the retired graph owner has been canceled and joined. The focused test
first observed the expected RED because persistence became visible while the
old graph was still blocked. The minimal GREEN moved the join before the
persistence callback. The combined lifecycle selector then passed under the
race detector:

```text
TestManagedRecoveryRuntimeTransitionCancelsAndJoinsOldGraphBeforePersistence
RED: old graph was not joined before persistence
GREEN

go test -race ./internal/backupasset/runtime \
  -run '^(TestManagedRecoveryRuntimeConcurrentRunHasOneOwnerAndShutdownJoinsIt|TestManagedRecoveryRuntimeRunTransitionStopAndShutdownRace|TestManagedRecoveryRuntimeTransitionCancelsAndJoinsOldGraphBeforePersistence)$' \
  -count=1
PASS
```

The downgrade-readiness matrix was added test-first. The initial selectors
failed on the absent closed readiness states, sticky fence and database
inspector. The GREEN provides exactly
`pristine_downgrade_allowed`, `blocked` and `forward_fix_only`; requires a
disabled graph; installs the sticky fence before snapshot/reconciliation;
makes the use latch dominate every other result; treats unavailable or fresh
non-clear reconciliation as a blocker; and never invokes schema down. A
production database inspector was then proven against the paired `000069` down
guard's Recovery aggregate, authority, source/node lease, attempt, Result,
Recovery Content grant/request/stream, shared usage/content-lease and ordinary
evidence blockers:

```text
TestManagedRecoveryRuntimeDowngradeReadinessMatrixIsDisabledStickyAndNeverRunsDown
TestManagedRecoveryRuntimeDowngradeReadinessRejectsEnabledFeatureWithoutInstallingFence
TestManagedRecoveryRuntimeDowngradeReadinessFailureStillFencesReenable
TestManagedRecoveryDowngradeDBInspectorMatchesPairedDownGuard
RED -> GREEN
```

Runtime composition originally lacked publication-backed Recovery facades and
constructed a second node-write coordinator in `cmd/server`. Focused RED/GREEN
selectors now prove one Recovery publication is shared by the graph, Router
authorization facade and Content Broker result facade; successful durable
execute wakes the managed worker; result authorization/source access remains
available while admission is disabled; and main reuses
`assetRuntime.NodeWriteCoordinator()`:

```text
TestManagedRecoveryAuthorizationFacadeBorrowsPublishedAdmissionAndWakesExecutedJob
TestManagedRecoveryResultFacadeRemainsPublishedWhileAdmissionDisabled
TestRuntimeNewOwnsDefaultDisabledRecoveryReceiptMaintenance
shared node-coordinator main/runtime source selectors
RED -> GREEN

go test ./internal/backupasset/runtime ./internal/api ./cmd/server -count=1
PASS
```

### Disabled maintenance ownership

The next test started an enabled graph, transitioned it to disabled and
required result delivery plus downgrade reconciliation to remain published.
The unchanged production transition produced the expected RED because the
disabled candidate contained neither maintenance service:

```text
=== RUN   TestManagedRecoveryRuntimeDisableRetainsResultAndReconciliationFacades
disabled graph lost maintenance services: resultDelivery:<nil>
downgradeReconciler:<nil>
FAIL
```

The first GREEN carried those maintenance services into the disabled graph.
The test was then strengthened to prove the retired mutation graph stops
claims, joins attempts and fences ownership before persistence, but does not
drain delivery or stop the lifecycle being retained by the same binary. That
produced a second genuine RED:

```text
disable transition events=[stop_claims join_attempts fence_ownership
drain_delivery shutdown_lifecycle persist]
want [stop_claims join_attempts fence_ownership persist]
FAIL
```

The final GREEN transfers maintenance service and teardown ownership only
after publication references drain, then retires mutation ownership. A
wait-idle failure therefore still republishes a fully owned prior graph, while
persist/install rollback discards the transferred candidate and rebuilds the
prior graph:

```text
go test ./internal/backupasset/runtime \
  -run '^(TestManagedRecoveryRuntimeDisableRetainsResultAndReconciliationFacades|TestManagedRecoveryRuntimeTransition)' \
  -count=1
PASS

go test -race ./internal/backupasset/runtime \
  -run '^(TestManagedRecoveryRuntimeRunTransitionStopAndShutdownRace|TestManagedRecoveryRuntimeTransition|TestManagedRecoveryWorker)' \
  -count=1
PASS
```

### Narrow downgrade facade

Task 9 needs a one-hop runtime boundary rather than access to the managed graph
or database inspector. The test-first selector produced compile-time RED for
the missing manager contract and public method:

```text
runtime.RecoveryDowngradeReadiness undefined
runtimeRecoveryManagerFake has no readiness contract
FAIL [build failed]
```

The minimal GREEN extends only the internal manager interface and delegates
through `Runtime.RecoveryDowngradeReadiness(ctx)`:

```text
go test ./internal/backupasset/runtime \
  -run '^TestRuntimeExposesRecoveryDowngradeReadinessFacade$' -count=1
PASS
```

No Task 9 route, handler, proof, reason, idempotency or Swagger change was
added by this slice.

### Worker concurrency, retry and fencing

Configured Recovery worker concurrency and the configured retry bounds were
previously parsed but not composed. The new tests first produced clean
compile-time RED for exactly the missing worker fields:

```text
unknown field WorkerConcurrency in managedRecoveryWorkerDependencies
unknown field RetryBase in managedRecoveryWorkerDependencies
unknown field RetryMaxDelay in managedRecoveryWorkerDependencies
FAIL [build failed]
```

The GREEN event loop keeps durable claim and takeover selection inside
`WorkerCoordinator`, bounds active executions by the frozen
`WorkerConcurrency`, and gives claim and takeover scheduler failures separate
lazy exponential timers starting at `RetryBase` and capped at
`RetryMaxDelay`. Job wake, takeover cadence and the two retry schedules do not
reset each other. Execution errors do not invent a runtime retry state; durable
lease expiry and the takeover scheduler remain authoritative.

The teardown test then produced RED for the missing active-claim fence:

```text
worker.FenceActiveClaims undefined
FAIL [build failed]
```

The GREEN tracks each durable active claim, cancels and joins execution first,
then calls the existing `WorkerCoordinator.CancelJob` transaction for every
context-interrupted claim. That transaction provides the durable attempt,
source-lease and node-lease fence/release transition; runtime does not rewrite
those rows directly.

A review pass then froze two shutdown races. First, an executor may return a
`context.Canceled`-shaped error while the managed worker context is still live;
the initial implementation retained that completed claim and the new selector
failed with `active Recovery claims=1, want 0`. GREEN now retains a claim only
when the worker run context itself was canceled. Second, a claim may complete
durably as shutdown arrives; its post-join `CancelJob` correctly returns
`ErrRecoveryWorkerFenceLost`. The focused selector first failed on that raw
sentinel, then GREEN normalized only that exact already-lost result while
preserving database, unavailable and caller-context failures.

```text
go test ./internal/backupasset/runtime \
  -run '^(TestManagedRecoveryWorker|TestManagedRecoveryAuthorizationFacade)' \
  -count=1
PASS

go test ./internal/backupasset/runtime \
  -run '^TestManagedRecoveryWorkerShutdownJoinsThenFencesActiveClaims$' \
  -count=1
PASS

go test -race ./internal/backupasset/runtime \
  -run '^(TestManagedRecoveryWorker|TestManagedRecoveryRuntimeRunTransitionStopAndShutdownRace|TestManagedRecoveryRuntimeConcurrentRunHasOneOwnerAndShutdownJoinsIt)$' \
  -count=1
PASS

go test ./internal/backupasset/runtime \
  -run '^(TestManagedRecoveryWorkerFencingAcceptsAlreadyLostOwnership|TestManagedRecoveryWorkerDoesNotFenceCompletedContextError|TestManagedRecoveryWorkerShutdownJoinsThenFencesActiveClaims)$' \
  -count=1
PASS

go test ./internal/backupasset/runtime -count=1
ok xirang/backend/internal/backupasset/runtime
```

### Open production blockers at this checkpoint

Enabled Recovery still has no production implementation anywhere in the
repository for the explicit node-revision, preflight-evidence,
authority-revalidation, typed Rsync restore-runner, reconciliation-revision or
reconciliation-finding authorities. Only test fakes implement these contracts.
`Runtime.New` therefore safely supports default-disabled startup, while an
operator enable transition fails closed before graph publication. No authority,
credential scope, security evidence, target permission or alert behavior was
synthesized in runtime to hide this gap.

Periodic result/workspace cleanup is also not safely schedulable yet.
`ResultLifecycleService` owns fenced candidate-addressed claim and phase
advancement, but exposes no bounded keyset/high-water candidate-selection port.
A runtime table scan would duplicate Task 7's private candidate predicates and
violate the domain boundary. Consequently the retained result/reconciliation
facades and receipt owner are wired, but the Task 8 cleanup cadence/batch/retry
owner and ordinary periodic orphan reconciliation remain open pending a
domain-owned bounded enumeration contract and the missing production
authorities. The Task 8 checklist remains open; no completion, stage, commit,
push or PR claim is made by this checkpoint.

### Stale retry timer reset correction (2026-08-13)

The retry scheduler had a latent timer-ownership defect: a successful claim
lookup reset only the logical retry channel and delay, leaving an already-fired
physical timer tick queued. The focused regression first failed against the
unchanged worker because the reset path made zero `Stop` calls:

```text
go test ./internal/backupasset/runtime \\
  -run '^TestManagedRecoveryWorkerDoesNotRearmStaleRetryTickAfterSuccess$' -count=1

--- FAIL: TestManagedRecoveryWorkerDoesNotRearmStaleRetryTickAfterSuccess
    recovery_runtime_test.go:1894: successful retry reset Stop calls=0, want 1
FAIL
```

The minimal GREEN adds `stopAndDrainManagedRecoveryTimer`, which stops the
armed retry timer and drains one expired tick before clearing the logical
schedule. The same selector then passed, along with the direct expired-timer
contract and the existing independent bounded-backoff selector:

```text
go test ./internal/backupasset/runtime \\
  -run '^TestManagedRecoveryWorkerDoesNotRearmStaleRetryTickAfterSuccess$' -count=1
ok  xirang/backend/internal/backupasset/runtime  0.104s

go test ./internal/backupasset/runtime \\
  -run '^(TestManagedRecoveryWorkerRetryResetDrainsExpiredTimer|TestManagedRecoveryWorkerRetriesWithIndependentBoundedBackoff)$' \\
  -count=1
ok  xirang/backend/internal/backupasset/runtime  0.052s
```

The final affected package, race, vet, format, diff and Trellis validation
commands also passed. Cold-disabled startup remains deliberately fail-closed:
production Target/revision/finding authorities and a bounded result/workspace
candidate enumeration port are still absent, so runtime does not invent those
authorities or duplicate Task 7 predicates.

### Task 8 disabled-runtime cleanup owner slice (2026-08-13)

The first cleanup-owner RED proved that a default-disabled graph still returned
only a sleeping no-op and had no configured cleanup owner:

```text
unknown field CleanupWorkerID in managedRecoveryGraphBuildDependencies
disabledGraph.cleanup undefined
FAIL
```

The minimal GREEN composes the real production target and
`ResultLifecycleService` in disabled mode, installs a stable process worker
identity, and starts a bounded cleanup owner from the graph. The owner performs
one immediate pass, uses one reusable cadence timer, applies the existing
bounded exponential retry formula for operational busy failures, and joins on
context cancellation. Enabled graphs run restore and cleanup owners together;
disabled graphs run cleanup only and never publish mutation admission.

The next RED caught an unbounded cleanup loop when a remote directory removal
returned `Complete=false`: one scheduler pass repeatedly called
`AdvanceRecovery*Cleanup` until the test hung. GREEN limits each selected
candidate to one lifecycle advance per pass, leaving durable phase continuation
for the next cadence.

Candidate enumeration was then moved behind the Recovery domain boundary. The
new closed `ListScheduledCleanupCandidates` product owns the indexed reads and
due predicates for ready/expired ResultSets, retryable cleanup-failed or expired
revoking ResultSets, and terminal unpublished `cleanup_due` workspaces. The
scheduled claim rechecks the current plaintext deadline after locking, so a
ResultSet retained after discovery is rejected rather than cleaned. Runtime
orchestration now calls only this product plus the existing fenced claim/revoke/
drain/validate/advance lifecycle methods.

Focused RED/GREEN selectors:

```text
TestManagedRecoveryCleanupOwnerProcessesOnlyBoundedDueLifecycleCandidates
TestManagedRecoveryCleanupOwnerRunsImmediateCadenceAndBoundedRetry
TestManagedRecoveryCleanupOwnerAdvancesOneRemoteChunkPerCandidatePass
TestManagedRecoveryCleanupOwnerRetriesBusyButDefersClaimConflictsToCadence
TestRecoveryScheduledResultCleanupClaimRechecksCurrentPlaintextDeadline
TestRecoveryListScheduledCleanupCandidatesIsClosedBoundedAndDueOnly
TestRecoveryListScheduledCleanupCandidatesIncludesUnpublishedWorkspace
TestRuntimeNewOwnsDefaultDisabledRecoveryReceiptMaintenance
RED -> GREEN
```

Fresh affected verification after the final refactor:

```text
go test ./internal/backupasset/runtime ./internal/backupasset/recovery \\
  ./internal/backupasset/repository ./cmd/server -count=1
ok runtime 7.079s
ok recovery 34.578s
ok repository 5.103s
ok cmd/server 0.058s

go test -race ./internal/backupasset/runtime \\
  -run '^(TestManagedRecoveryCleanupOwner|TestBuildManagedRecoveryGraphRequiresAuthoritiesAndBindsProductionServices|TestManagedRecoveryRuntimeRunTransitionStopAndShutdownRace|TestManagedRecoveryRuntimeConcurrentRunHasOneOwnerAndShutdownJoinsIt)$' \\
  -count=1
ok runtime 1.616s

go vet ./internal/backupasset/runtime ./internal/backupasset/recovery ./cmd/server
gofmt -l <affected files>  # no output
git diff --check           # clean
```

This slice does not claim durable cleanup fairness across restarts: `000069`
has no cleanup scheduler cursor/high-water or per-row retry eligibility fields,
and runtime is not allowed to invent them. Ordinary remote-root orphan
reconciliation also remains fail-closed until a current root-revision registry
exists. Enabled production Recovery remains fail-closed for the unresolved
preflight, write-authority, and reconciliation authority sources recorded above.

## Task 8 full-scope quality review and fail-closed checkpoint (2026-08-14)

The final full-scope review found and fixed two Critical and two Important
runtime/composition defects before this checkpoint:

1. **Critical shutdown lifecycle inversion.** Lifecycle owners could stop before
   active attempts joined, ownership was durably fenced, and Content delivery
   drained. Teardown now enforces `stop claims -> join attempts -> fence ->
   revoke/drain -> stop lifecycle`, including context-bounded worker and graph
   joins.
2. **Critical Recovery ticket publication race.** Result authorization released
   the graph publication borrow before Content had durably registered the grant.
   Issue-scoped authorization now retains the borrow through durable grant
   registration and the in-memory binding.
3. **Important missing facade consumers.** The Recovery authorization facade was
   injected but its four existing security-override, write-authorization,
   exact-mirror-delete-authorization and execute routes were absent. They are now
   registered behind Auth, `backup_assets:recover`, and the applicable Admin
   boundary; focused tests prove `401`/`403`/`503` behavior. This narrow wiring
   is Task 8 composition evidence, not Task 9 route-matrix completion.
4. **Important ignored drain setting.** Parsed `result_drain_timeout` now bounds
   enabled and disabled graph revoke/drain teardown instead of being inert.

The same review closed missing logical/permanent-cleanup-key reconciliation,
an unrelated Content downgrade blocker, incomplete delivery shutdown wiring,
non-context worker fencing, and the staticcheck `S1016` finding. The disabled
graph now owns independent joined cleanup and logical-reconciliation loops:
both run immediately, use separate cadence ownership, retry blocked/unavailable
results, record `clear` only for complete finding-free cursor-free scans, and
join on cancellation. This supersedes the earlier research audit's dated
disabled-reconciliation finding.

Final verification recorded after those fixes:

```text
focused API/Content/runtime normal -count=10                     PASS
focused API/Content/runtime race -count=10                       PASS
go test ./... -count=1                                           PASS
go test -race ./internal/backupasset/content \
  ./internal/backupasset/runtime \
  ./internal/backupasset/recovery -count=1                        PASS
go vet ./...                                                      PASS
make lint-backend                                                 PASS (0 issues)
make backend-build                                                PASS
make check                                                        PASS
git diff --check                                                  PASS
```

`make check` included the backend gates plus 168 frontend test files and 1,388
passing tests, TypeScript checking, and the Vite build. It retained one existing
non-blocking accessibility warning at
`web/src/features/backup-assets/export-job-panel.tsx:195`
(`jsx-a11y/no-noninteractive-tabindex`). The generated
`backend/xirang-server` artifact was removed after the build.

The required real-PostgreSQL parity gate used one disposable
`postgres:18-alpine` container. Required/no-skip Recovery normal passed in
approximately 10.334 seconds; the corresponding race gate passed in
approximately 14.192 seconds. The container used `--rm`, was stopped, and left
zero container/volume residue.

### Remaining completion blockers

Enabled production Recovery deliberately remains unavailable because no real
production implementation yet owns the complete
`RecoveryPreflightExternalEvidenceAuthority`,
`RecoveryAuthorityRevalidator`, or
`RecoveryReconciliationRevisionSource` products. The missing evidence includes
current policy/finding disposition, overlap and reserve policy, and an
independent durable target-root revision. Frozen plan fields, timestamps,
locator digests, or clean/false/zero defaults are prohibited stand-ins.

The following settings are parsed and participate in transition validation but
have no corresponding production Recovery domain seam: `DefaultRootID`,
`PreflightTTL`, `MaxSelectionItems`, `MaxLogicalBytes`, `LeaseRenewMargin`,
`ExecutionTimeout`, `VerificationTimeout`, and `OrphanQuarantineLimit`. A
focused plan/preflight/worker/executor/reconciliation contract amendment is
required before they can be consumed honestly.

Therefore Task 8 is `in_progress_fail_closed_checkpoint`, not complete. Tasks
9--10 remain `not_executed`; no Task 8 stage, commit, push, PR, CI, merge or
delivery credit is authorized by this evidence.

### Task 8-A encrypted target-root authority v2 completion evidence (2026-08-14)

T8-A was implemented only in its approved six-file product slice. The RED
selectors first failed against the schema-1 registry and the absent authority
service; the GREEN implementation now uses a strict schema-v2 encrypted
document while retaining the `enc:v2:` envelope. Missing, duplicate, unknown,
tampered, legacy-envelope, substituted-key and substituted-payload documents
all map to the single unavailable sentinel. Authority and root-observation
revisions are independent; exact replay and safe-label-only updates preserve
authority, while locator, observation, reserve or overlap-policy changes rotate
it. Reserve policy fields are private on every JSON boundary, including direct
policy serialization.

The target-root authority owner performs a read-only probe outside the write
transaction, captures distinct pre-probe, post-probe, lock-start and post-lock
clocks, locks node/credential/exact root state, rechecks credential expiry and
freshness after the locks, and never probes during delete. Malformed root
references are rejected before database access with
`ErrRecoveryTargetRootInvalid`; dependency and persistence failures remain
sanitized. The shared concurrent mutation matrix covers rotate-vs-rotate,
register-vs-rotate, register-vs-delete, and register-on-absent-vs-delete-on-
missing with deterministic ready/start barriers and runs from both engine
selectors.

RED/GREEN and review evidence:

```text
go test ./internal/settings ./internal/backupasset/recovery \
  -run 'RecoveryTargetRootV2|TargetRootAuthorityService' -count=1   PASS
go test -race ./internal/settings ./internal/backupasset/recovery \
  -run 'RecoveryTargetRootV2|TargetRootAuthorityService' -count=1   PASS
go test -race ./internal/backupasset/recovery \
  -run '^TestTargetRootAuthorityServiceRegisterRotateDelete$' -count=10 PASS
go test ./internal/settings ./internal/backupasset/recovery \
  ./internal/api/handlers -count=1                                 PASS
go vet ./internal/settings ./internal/backupasset/recovery \
  ./internal/api/handlers                                            PASS
git diff --check                                                     PASS
```

The first fresh specification review closed the prior three findings after
the clock, JSON-null and concurrency fixes, then identified direct policy JSON
leakage and an incomplete matrix. Those were fixed with private JSON tags, a
post-lock expiry recheck, and the four-case matrix. The same review's final
Important finding (malformed nonempty root ID mapping) was fixed by routing
delete validation through the settings registry validator before any database
access; the regression test passes even after the fixture database is closed.

Required PostgreSQL parity was run with a disposable `postgres:18-alpine`
fixture in required/no-skip mode. Both the normal and race
`TestRecoveryTargetRootAuthorityPostgres` selectors passed after the expanded
matrix, and the temporary test schema count was zero before the container was
removed. No migration, `000070`, staging, commit, push, PR, or T8-B work was
performed by this slice.

#### T8-A deletion lifecycle quality closure (2026-08-14)

The independent quality re-review found one Important lifecycle defect after
the original T8-A specification gate: deleting a DB-only root still required a
currently eligible SSH credential. An archived node or a disabled, expired or
detached credential could therefore leave an undeletable encrypted registry
row and block reconciliation or downgrade readiness.

The new regression first failed for all four lifecycle states with
`recovery target unavailable`. `TargetRootAuthorityService.Delete` now locks
only the exact node row; the registry owner separately locks, decodes and
ciphertext-CAS-deletes the exact root row. Delete still validates the node/root
reference before database access, never opens a target session or registration
probe, and preserves sanitized context/database/not-found behavior.

Fresh closure evidence:

```text
go test ./internal/backupasset/recovery \
  -run '^TestTargetRootAuthorityServiceDeleteSurvivesNodeCredentialLifecycle$' \
  -count=1                                                       PASS
go test ./internal/settings ./internal/backupasset/recovery \
  -run 'RecoveryTargetRootV2|TargetRootAuthorityService' -count=1 PASS
go test -race ./internal/settings ./internal/backupasset/recovery \
  -run 'RecoveryTargetRootV2|TargetRootAuthorityService' -count=1 PASS
go test ./internal/settings ./internal/backupasset/recovery -count=1 PASS
go test -race ./internal/settings ./internal/backupasset/recovery -count=1 PASS
go vet ./internal/settings ./internal/backupasset/recovery        PASS
git diff --check                                                  PASS
```

A fresh read-only reviewer also ran the lifecycle race selector with
`-count=10`, checked formatting and the SQLite/PostgreSQL lock/CAS shape, and
returned `QUALITY_OK` with no Critical or Important finding. No review edit,
staging, commit, push or PR occurred.

### Task 8-B eligibility authority B7 checkpoint (2026-08-15)

T8-B now has one Recovery-owned eligibility authority projected through the
preflight, live-effect and reconciliation interfaces. Managed Rsync source
evidence is assembled from the Repository-owned pinned capability plus a
purpose-exact strict-known-host SSH/SFTP namespace observation; Processing
contributes one closed plan-level canonical malware product. The target-root
port locks and revalidates the exact encrypted v2 authority row and independent
root-observation revision, while the target observer performs a strict,
read-only canonical namespace and capacity observation. Restic and Rclone stop
as unavailable before source or target access.

Authorization, worker and executor paths now observe before opening their
mutation transaction, then revalidate the identical private sealed product in
the caller-owned transaction. The complete drift matrix covers source and
capability revisions, security policy/finding evidence, root authority/root
observation, node/credential, overlap and reserve substitution, partial
products, request echo, zero defaults and close-once ownership. Repository and
Processing transaction seams perform durable reads only; Repository,
Processing artifact, SSH and SFTP work do not occur inside the mutation
transaction.

The production graph no longer injects the three known-unavailable shells. A
single `RecoveryEligibilityAuthority` instance supplies all three narrow
projections. A composition RED first failed because that common owner projector
did not exist. The first full runtime GREEN attempt then exposed Processing's
derived-artifact reader as a startup-installed dependency; composition now
retains the stable Processing runtime owner and validates the late-bound reader
at observation time. Disabled Recovery maintenance can therefore start before
Processing artifacts exist, while every live observation still fails closed if
Processing is unavailable.

The required PostgreSQL selector did not previously exist. Its RED failed on
the missing PostgreSQL-capable harness. The final test runs the same authority
over a real PostgreSQL caller-owned transaction, proves exact current
revalidation succeeds, then mutates the durable root authority after
observation and proves locked revalidation returns the closed changed result.

Fresh B7 evidence from `backend/`:

```text
go test ./internal/backupasset/processing ./internal/backupasset/repository \
  ./internal/backupasset/recovery ./internal/backupasset/runtime \
  -run 'Recovery(SecurityObservation|RsyncSourceAuthority|EligibilityAuthority|AuthorityRevalidation|ReconciliationRevision)' \
  -count=1                                                     PASS (2.1s)

go test -race ./internal/backupasset/repository \
  ./internal/backupasset/recovery ./internal/backupasset/runtime \
  -run 'Recovery(RsyncSourceAuthority|EligibilityAuthority|AuthorityRevalidation|ReconciliationRevision)' \
  -count=1                                                     PASS (7.3s)

go test ./internal/backupasset/runtime -count=1                PASS (6.815s)

REQUIRE_POSTGRES_RECOVERY_TEST=1 TEST_POSTGRES_DSN=<redacted> \
  go test ./internal/backupasset/recovery \
  -run '^TestRecoveryEligibilityAuthorityPostgres$' -count=1  PASS (0.066s; no skip)

REQUIRE_POSTGRES_RECOVERY_TEST=1 TEST_POSTGRES_DSN=<redacted> \
  go test -race ./internal/backupasset/recovery \
  -run '^TestRecoveryEligibilityAuthorityPostgres$' -count=1  PASS (1.564s; no skip)

git diff --check                                               PASS
```

The PostgreSQL gates used one disposable `postgres:18-alpine` container with a
command-scoped random password and private DSN. Both scoped schemas were
dropped by test cleanup; the `--rm` container was removed and the explicit
residue check returned `TASK8_PG_CLEAN`. Error/privacy tests retain the raw
source, malware, locator, credential and dependency-error canaries inside test
fixtures only and reject their appearance at returned error/format/JSON
boundaries. No staging, commit, push or PR occurred. T8-B is complete at the B7
checkpoint; T8-C and later slices remain unexecuted by this checkpoint.

### Task 8-C policy, heartbeat and absolute-deadline completion evidence (2026-08-15)

T8-C removed `DefaultRootID`, `VerificationTimeout` and
`OrphanQuarantineLimit`, installed `ReconciliationFindingLimit`, and rejected
the removed keys through ordinary, transactional/config-import and batch
settings update paths without persistence. Immutable plan, preflight, worker
and reconciliation policies now own every retained setting. Plan limits are
checked before writes and again against materialized rows. The server freezes
one preflight expiry at plan creation; caller TTL/expiry substitution and
advancing-clock idempotency replay cannot change it.

The managed claim carries one immutable absolute deadline equal to the earliest
execution timeout, grant expiry and preflight expiry. Heartbeat renewal updates
the source lease, node lease and attempt atomically; renewal failure cancels the
claim, fences it durably and joins the executor before another target effect.
Every actual item effect refreshes a private lease-bounded permit from the
current locked source/node/attempt/latch state without replaying external
eligibility, while the Provider session remains bounded by the immutable
absolute deadline. Cancellation closes SFTP and SSH transports before remote
tracked file cleanup so a blocked `pkg/sftp.File.Close` cannot prevent the
executor from joining.

Two independent reviews drove additional RED/GREEN closure. Preflight now
resamples time after external observation and again after locked source
revalidation; crossing durable expiry during either observation or row-lock
delay atomically returns conflict with zero preflight rows and an unchanged
draft plan. The final specification receipt was `SPEC_OK` and the final quality
receipt was `QUALITY_OK`.

Fresh T8-C evidence from `backend/`:

```text
go test ./internal/settings ./internal/backupasset \
  -run 'Recovery(Config|Policy|RemovedSetting|ReconciliationFindingLimit)' \
  -count=1                                                     PASS

go test ./internal/settings ./internal/backupasset \
  ./internal/backupasset/recovery ./internal/backupasset/runtime \
  -run 'Recovery(PlanPolicy|PreflightPolicy|WorkerPolicy|Heartbeat|ExecutionDeadline|ReconciliationFindingLimit|RemovedSetting)' \
  -count=1                                                     PASS

go test -race ./internal/backupasset/recovery ./internal/backupasset/runtime \
  -run 'Recovery(Heartbeat|ExecutionDeadline|ReconciliationFindingLimit)' \
  -count=1                                                     PASS

go test ./internal/settings ./internal/backupasset \
  ./internal/backupasset/recovery ./internal/backupasset/runtime -count=1 PASS

go vet ./internal/settings ./internal/backupasset \
  ./internal/backupasset/recovery ./internal/backupasset/runtime PASS

removed-setting and retained-policy scans                      PASS
git diff --check; staged-zero                                  PASS
```

No staging, commit, push, PR or T8-D work was credited by this checkpoint.

### Task 8-D production composition and transition completion evidence (2026-08-16)

Production composition now requires genuinely ready Repository, Processing
and target authorities before enabled Recovery publication. Exactly one
`RecoveryEligibilityAuthority` supplies the preflight, live and reconciliation
projections, and publication occurs once only after metadata reconciliation.
Nil or known-unavailable authorities and disabled/control-plane-only Processing
security evidence publish no enabled admission graph. After a genuinely ready
publication, a late Processing reader failure closes effects while preserving
result and logical-reconciliation maintenance.

The production target-root service uses an independent
`recovery_target_root_registration` credential purpose and strict
`known_hosts`; it performs two stable canonical, no-symlink, read-only SFTP
observations and never accepts an insecure/accept-new `NodeDialer`. The runtime
facade returns only safe summaries. Register, rotate and delete validate before
drain and execute through the installed current-config transition owner.
Recovery alone owns the opaque, redacted rollback token and exact CAS restore
of the prior encrypted setting row/timestamp or prior absence; Runtime never
interprets or synthesizes private locator/revision/policy state.

Transitions cover validate, drain, persist, construct, reconcile and install
failures. A proven restoration restores persistence before rebuilding and
publishing the prior graph. An unproven old-owner join, root restoration
failure or graph restoration failure leaves publication nil and a sticky fence
that blocks admission, re-enable and downgrade readiness. Pre-start current-
config/root transitions fail before callbacks or publication, so a zero-value
config cannot suppress the later configured startup. Disabled graphs run and
join cleanup, logical reconciliation and receipt reaping before schema drain;
metrics retain only fixed provider/state/outcome/category labels.

The focused amendment's Task 9 boundary was restored during specification
review: Task 8 retains internal Recovery authorization and target-root facades,
but does not register the security-override, write-authority, exact-mirror
delete-authority, execute, target-root or downgrade-readiness routes and does
not claim their RBAC/audit/Swagger work. Final independent receipts were
`SPEC_OK` and `QUALITY_OK`, with no open Critical or Important finding.

Fresh T8-D evidence from `backend/`:

```text
go test ./cmd/server ./internal/api ./internal/backupasset/runtime \
  ./internal/backupasset/recovery \
  -run 'Recovery(ProductionAuthority|Runtime|Transition|Disabled|Downgrade|Metrics|RBAC)' \
  -count=10                                                    PASS

go test -race ./cmd/server ./internal/api ./internal/backupasset/runtime \
  ./internal/backupasset/recovery \
  -run 'Recovery(ProductionAuthority|Runtime|Transition|Disabled|Downgrade)' \
  -count=10                                                    PASS

go test ./internal/backupasset/processing ./internal/backupasset/runtime \
  ./internal/backupasset/recovery ./internal/api ./cmd/server -count=1 PASS

go vet ./...                                                   PASS
strict-purpose/insecure-path/000070 scans                      PASS
git diff --check; staged-zero                                  PASS
```

The exact D5 normal/race selectors and additional target-root rollback,
pre-start and Processing-readiness race selectors each passed for ten
iterations. No staging, commit, push, PR or T8-V1 work occurred. Per the
approved implementation plan, execution stops here before whole-scope V1 and
required final PostgreSQL gates.

### Task 8-V1 whole-scope quality and completion evidence (2026-08-16)

The fresh reviewer loaded the task JSONL context first, then the PRD, design
sections 48.1--48.7, the V1 implementation plan and every affected backend and
cross-cutting Quality Check section. Review covered encrypted root v2,
independent authority/observation revisions, eligibility issuance and effect
revalidation, setting ownership and transitions, enabled/disabled lifecycle,
SQLite/PostgreSQL parity, privacy and the Task 9 route/RBAC/audit/Swagger
boundary.

#### Findings fixed

- Seven scoped lint findings were closed: two unchecked target-session closes,
  two unchecked GORM `AddError` calls and three private-field/nil-safety issues
  in source-namespace tests. `make lint-backend` then reported zero issues.
- Repository-root `gofmt` exposed brittle main wiring tests that compared
  horizontal alignment. `main.go` was formatted and the tests now compare the
  same wiring fragments/order while ignoring horizontal whitespace.
- A fresh Important review finding showed a late namespace drift window:
  `Task.RsyncSource`, the source node or the source credential could change
  after namespace observation but before eligibility issuance, or after issue
  but before a caller's effect transaction. Strict TDD first produced a genuine
  compile RED in
  `TestRecoveryEligibilityAuthorityRejectsLateSourceNamespaceDrift` because the
  opaque observation had no durable/request/captured seam. The minimal GREEN
  retains only the private exact durable snapshot, revalidates it inside both
  the final issuance transaction and the caller's effect transaction, and adds
  all six drift cases. The transactions perform only GORM revalidation; they
  make zero SSH/SFTP/Repository external-observation calls. Opaque JSON and
  formatting tests prove no path or canary disclosure.
- Final manifest reconciliation corrected stale ledger-only counts without
  product expansion. The authoritative union is exactly `9 current + 64
  create + 84 modify = 157`, unique and disjoint, with zero dirty product path
  outside the ledger.

The late-drift fix implements the existing task-local sections 48.3, 48.5 and
48.7 contract that the final transaction revalidate every durable
source/node/credential revision. The reusable cross-package rule is also
captured in `.trellis/spec/backend/quality-guidelines.md` as the managed
Recovery authority/runtime-publication scenario: short capture -> external
observation -> locked revalidation, private source drift revalidation in both
issuance and effect transactions, Processing readiness before publication, and
sticky failure on unproven join or rollback.

#### Fresh dynamic verification

From `backend/`:

```text
go test ./... -count=1                                      PASS 47.27s
go test -race ./internal/backupasset/processing \
  ./internal/backupasset/repository \
  ./internal/backupasset/recovery \
  ./internal/backupasset/runtime ./internal/api \
  ./cmd/server -count=1                                      PASS 122.27s
go vet ./...                                                 PASS 1.22s
```

The race packages reported: Processing 8.726s, Repository 16.214s, Recovery
117.694s, Runtime 13.777s, API 2.735s and server 1.627s.

Focused late-drift TDD and regression evidence:

```text
go test ./internal/backupasset/recovery \
  -run '^TestRecoveryEligibilityAuthorityRejectsLateSourceNamespaceDrift$' \
  -count=1                                                    PASS 0.054s
go test -race ./internal/backupasset/recovery \
  -run '^TestRecoveryEligibilityAuthorityRejectsLateSourceNamespaceDrift$' \
  -count=1                                                    PASS 1.538s
go test ./internal/backupasset/recovery \
  -run 'SourceNamespace|Eligibility' -count=1                 PASS 0.114s
go test -race ./internal/backupasset/recovery \
  -run 'SourceNamespace|Eligibility' -count=1                 PASS 1.656s
```

The disposable `postgres:18-alpine` harness used a command-scoped random
secret, loopback-only random port and PostgreSQL 18's `/var/lib/postgresql`
data mount. Neither the password nor DSN was printed. With
`REQUIRE_POSTGRES_RECOVERY_TEST=1`, the complete Recovery `Postgres` selector
reported:

```text
normal: PASS 8.780s; 55 pass events; 0 skip; 0 fail
race:   PASS 12.793s; 55 pass events; 0 skip; 0 fail
```

Before removal, schemas matching `xirang_recovery_%` were zero and the
container had zero volume mounts. After graceful stop and `docker rm -v`, the
exact container residue and named-volume-prefix residue were both zero. Two
earlier readiness attempts failed before tests because the harness used the
pre-v18 data mount; their cleanup traps removed both containers. Correcting
only the harness path produced the no-skip evidence above; no product or test
code changed for this harness diagnosis.

From repository root:

```text
make lint-backend                                           PASS 3.93s, 0 issues
make backend-build                                          PASS 3.94s
make check                                                  PASS 105.25s
git diff --check                                            PASS
both Trellis task validations; task JSON/JSONL jq parses    PASS
```

`make check` passed backend lint/test/build plus frontend lint, typecheck,
1,388 tests in 168 files and the Vite build. The sole frontend lint diagnostic
was the pre-existing non-blocking warning at
`web/src/components/backup-assets/export-job-panel.tsx:195`. The generated
`backend/xirang-server` was removed after verification and confirmed absent.

#### Static, privacy and scope receipt

Forbidden direct standard-log/print calls, private production canaries,
insecure or accept-new SSH host-key behavior, removed Recovery settings,
merge markers and binary artifacts were absent. All dirty Go files were
`gofmt` clean. Paired migrations remain exactly four `000069` files with no
`000070` or `000071`. The exact manifest gate reported `157` unique paths,
zero duplicates, zero dirty product paths outside the manifest and zero staged
paths. The user's main-worktree `.codex/agents/trellis-research.toml` still
exists only as its protected user-owned main-worktree delta and is absent from
this worktree delta.

Task 8's internal facades remain unregistered: the effect, target-root and
downgrade-readiness route/response/RBAC/audit/privacy/Swagger matrix remains
Task 9 work. Tasks 9--10 are `not_executed`.

Fresh review receipt: `QUALITY_OK`. There is no open Critical or Important
Task 8 finding across design sections 48.1--48.7, settings transitions,
SQLite/PostgreSQL parity, privacy, runtime lifecycle or the Task 9 boundary.

#### Phase 3.4 local delivery receipt

After the whole-scope gates and review were complete, the controller entered
Phase 3.4 and created local work commit
`82dc261fe6e185f4e6e83dbe13f0d0dd12102011` (`feat(backup-assets): complete
managed recovery runtime`) on `codex/task8-managed-runtime`. No push, PR, CI,
merge, worktree cleanup or task archive occurred. Child 13 remains
`in_progress`, the parent remains planning, and Tasks 9--10 remain
`not_executed`.

### Task 8 hosted-CI security remediation evidence (2026-08-16)

The controller pushed the reviewed Task 8 work and opened ready PR #424. Its
first hosted CI run `31918892574` passed both native Worker runtime-closure
jobs, the Worker image build/smoke path, Docker Build, Frontend Test & Build,
PR Title, Doc Freshness, Migration UTC Safety, Backend Test & Build and
PostgreSQL Migration Parity. The amd64 and arm64 Worker scan jobs
`95095555595` and `95095555560` each built and smoked their images successfully
before Trivy 0.70.0's current database reported eight fixed HIGH Go
standard-library CVEs in binaries built by Go 1.26.5. Every finding named Go
1.26.6 as the fixed release. This was a genuine hosted security RED, not a
Recovery behavior or test failure, and the scanner was not waived.

The bounded same-branch remediation upgrades `backend/go.mod`, both
All-in-One Go builders, the digest-pinned multi-architecture Worker builder,
the Processing toolchain inventory, its smoke contracts and README to Go
1.26.6. The Worker builder uses verified multi-architecture digest
`sha256:af8d6740070b8906d12eae1c3e3ea0957fb63f492051ea05e354c38ef9fe88df`.
No Recovery behavior, model, migration, route, setting or frontend path
changed. The exact delivery manifest is now `9 current + 64 create + 91 modify
= 164` unique, disjoint paths.

Fresh local RED-to-GREEN evidence after the toolchain update:

```text
bash scripts/test-asset-worker.test.sh                       PASS
bash scripts/check-doc-freshness.sh                         PASS
bash scripts/check-doc-freshness.test.sh                    PASS
docker build --platform linux/amd64 -f deploy/worker/Dockerfile . PASS
Trivy 0.70.0 --severity HIGH,CRITICAL --ignore-unfixed      PASS, 0 findings
go test ./... -count=1                                      PASS
go test -race ./internal/backupasset/processing \
  ./internal/backupasset/repository \
  ./internal/backupasset/recovery \
  ./internal/backupasset/runtime ./internal/api \
  ./cmd/server -count=1                                      PASS
go vet ./...                                                 PASS
govulncheck ./...                                            PASS, 0 called vulnerabilities
make lint-backend                                            PASS, 0 issues
make backend-build                                           PASS
make check                                                   PASS
```

The race packages reported Processing 8.783s, Repository 15.987s, Recovery
116.576s, Runtime 14.047s, API 2.627s and server 1.618s. `make check` again
passed 1,388 frontend tests in 168 files and the Vite build with only the same
pre-existing non-blocking `export-job-panel.tsx` warning.

The disposable PostgreSQL 18 rerun used a random unprinted password,
loopback-only random port and tmpfs at `/var/lib/postgresql`. The complete
Recovery `Postgres` selector reported 55 pass, 0 skip in both normal and race
modes. Test schemas, volume mounts, exact container residue and matching named
volume residue were all zero after cleanup. Hosted CI must now rerun against
the remediation commit; merge, post-merge automation, delivery bookkeeping and
workspace cleanup remain pending. Child 13 stays `in_progress`, the parent
stays planning, and Tasks 9--10 remain `not_executed`.
