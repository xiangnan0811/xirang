# Phase 0 RED evidence

Date: 2026-08-23

## Baseline

- Branch: `codex/backup-assets-migration-69-compatibility`
- Baseline/head: `0853532a5777b92f3f997b9e288c1a10f1c91f4e`
- Baseline `origin/main`: `0853532a5777b92f3f997b9e288c1a10f1c91f4e`
- Latest migration before implementation: `000071_backup_asset_ga`
- This checkpoint changed tests and task research only. No migration or other production code was edited.

## Focused RED command

From `backend/`:

```text
env TMPDIR=/home/murray/.cache/xirang-migration69-red/tmp go test ./internal/database -run '^(TestBackupAssetMigration069TaskRunNodeSnapshotSQLite|TestRunMigrations_RejectsDirtyEvenWhenEscapeHatchIsTrue|TestRunMigrationsRejectsCleanVersionSchemaDriftBeforeFixups)$' -count=1 -v
```

Result: exit 1, with the intended missing-contract failures:

- `TerminalOrphanPreservedAsLegacyUnknown` failed while applying SQLite 000069 because the
  existing `valid = 1` guard rejected the orphan's NULL backfill. The expected preserved row
  with `node_id_snapshot=0` was therefore not reached.
- `FailClosed/active_orphan` and `FailClosed/unknown-state_orphan` passed. Each rejected 000069,
  retained the seeded TaskRun, kept the version dirty, and left no snapshot column, TaskRun
  trigger, or recovery table behind.
- `TestRunMigrations_RejectsDirtyEvenWhenEscapeHatchIsTrue` failed after logging the current
  automatic-force path for dirty version 50. The returned error was the later migration retry
  failure, not `ErrMigrationDirty`.
- `TestRunMigrationsRejectsCleanVersionSchemaDriftBeforeFixups` failed because startup renamed
  `policies.bw_limit` to `policies.bwlimit` and then accepted clean version 71. This proves the
  missing migration-69 schema was not rejected before pre-migration writes.

Concise failing lines:

```text
apply sqlite 000069 for terminal orphan TaskRun: CHECK constraint failed: valid = 1
dirty startup with ALLOW_DIRTY_STARTUP=true returned 强制执行迁移失败: ... want ErrMigrationDirty
clean-version schema drift was mutated before rejection: ... bw_limit ... after=... bwlimit ...
FAIL xirang/backend/internal/database
```

## Focused denial confirmation

```text
env TMPDIR=/home/murray/.cache/xirang-migration69-red/tmp go test ./internal/database -run '^TestBackupAssetMigration069TaskRunNodeSnapshotSQLite/FailClosed/(active_orphan|unknown-state_orphan)$' -count=1 -v
```

Result: exit 0; both denial subtests passed. The shared fixture is used by the existing SQLite
and required PostgreSQL migration-69 entry points; real PostgreSQL execution remains a later
required implementation gate and was not claimed by this RED checkpoint.
