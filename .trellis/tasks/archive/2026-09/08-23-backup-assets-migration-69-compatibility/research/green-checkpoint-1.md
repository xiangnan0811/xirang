# GREEN checkpoint 1 evidence — migration core and 000072 convergence

Date: 2026-08-23

## Focused SQLite/shared contract

Command:

```text
env TMPDIR=/home/murray/.cache/xirang-migration69-red/tmp go test ./internal/database -run '^(TestCheckMigrationDirty_NoTable|TestCheckMigrationDirty_CleanTable|TestCheckMigrationDirty_DirtyTable|TestRunMigrations_RejectsDirtyEvenWhenEscapeHatchIsTrue|TestRunMigrationsRejectsCleanVersionSchemaDriftBeforeFixups|TestRunMigrationsClean071Applies072SQLite|TestBackupAssetMigration069TaskRunNodeSnapshotSQLite|TestBackupAssetMigration072PairedFiles|TestBackupAssetMigration072SQLite)$' -count=1 -v
```

Result: PASS (`ok xirang/backend/internal/database 1.424s`). This includes terminal-orphan
preservation, active/unknown/nonpositive atomic rejection, both legacy dirty environment values
(`true` and `1`), clean-71 drift rejection without fixup/version/data mutation, clean-71 apply to
72, paired-file checks, 72 convergence, ordinary-write/status closure, used-down atomic rejection,
and pristine down.

## Database package and shared compile

Commands and results:

```text
env TMPDIR=/home/murray/.cache/xirang-migration69-red/tmp go test ./internal/database -count=1
ok xirang/backend/internal/database 32.964s

env TMPDIR=/home/murray/.cache/xirang-migration69-red/tmp go test ./... -run '^$' -count=1
```

Result: PASS for every backend package; no test binaries failed to compile.

## Paired migration checker

Command:

```text
env TMPDIR=/home/murray/.cache/xirang-migration69-red/tmp bash scripts/check-backup-asset-migration.sh
```

Result: PASS. Paired 000071/000072 files and fail-closed SQLite used-down owners were verified.

## PostgreSQL status

Compile/selection command:

```text
env TMPDIR=/home/murray/.cache/xirang-migration69-red/tmp go test ./internal/database -run '^(TestRunMigrationsPostgresDirtyCheckUsesSearchPath|TestRunMigrationsPostgresSchemaDriftCheckUsesSearchPath|TestBackupAssetMigration069TaskRunNodeSnapshotPostgres|TestBackupAssetMigration072Postgres)$' -count=1 -v
```

The package compiled and the selector exited 0, but all four PostgreSQL tests were SKIP because
`TEST_POSTGRES_DSN` was not configured. This is not required-PostgreSQL GREEN evidence. CI now
selects migration 72 and the PostgreSQL schema-drift `search_path` test; the required run must use
`REQUIRE_POSTGRES_MIGRATION_TEST=1` with a real `TEST_POSTGRES_DSN`.

## Formatting

`gofmt -d` on all changed Go files, `bash -n scripts/check-backup-asset-migration.sh`, and
`git diff --check` produced no output.
