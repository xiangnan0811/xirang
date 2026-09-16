# Root cause — sanitized production evidence

## Decision

Child 18 production acceptance for v0.50.2 is **No-Go**. The failure occurred before backup
asset inventory or enablement, during the Core database migration.

## Confirmed chain

1. A supported node-delete operation removed a Node and its associated Task.
2. Historical TaskRun rows were retained by design.
3. Migration 69 added `task_runs.node_id_snapshot` and backfilled only through the current
   `tasks` table.
4. Retained terminal runs whose Task no longer existed remained NULL and tripped the guard.
5. The failed migration left a dirty version, as expected.
6. The documented dirty escape hatch skipped the precheck, called `migrate.Force` at the failed
   version, and retried later migrations.
7. The database then reported the latest clean version although migration 69 recovery objects
   were absent.

## Evidence strength

- The pre-upgrade backup passed an external digest check and SQLite integrity check and had a
  clean pre-backup-assets migration version.
- Historical backups bracketed the supported node deletion.
- The audit log contained one successful normalized node-delete event in that interval.
- Source inspection confirms node deletion removes Task rows but not TaskRun rows.
- The failed migration's logical forensic copy was internally consistent and showed the later
  clean version while the migration 69 schema contract was absent.
- Production was restored from the verified pre-upgrade backup and is healthy on the prior
  image. Authentication and normal pages were manually exercised after rollback.

## Privacy boundary

The repository records only aggregate counts, version classes and code contracts. Production
Node/Task names, host addresses, local paths, database digests, credentials and raw log content
remain outside version control.

## Root-cause categories

- Primary: migration compatibility assumption contradicted a supported retention/deletion state.
- Amplifier: server-side dirty auto-force treated metadata repair as proof of schema completion.
- Detection gap: startup trusted a clean version number without validating the minimum historical
  schema contract.

## Operational state

- The failed database/WAL/SHM and container log were preserved forensics; do not delete them.
- The dirty escape hatch was removed from production configuration.
- Backup assets remain disabled by default and were never enabled during this attempt.
- A separate, pre-existing node-log collector stall was isolated into its own task.
