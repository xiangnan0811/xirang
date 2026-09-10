-- 000082 adds durable scheduler occurrence identity and immutable backup
-- configuration fingerprints. Legacy rows remain readable with NULL/empty values;
-- they are never treated as evidence for a restore.
ALTER TABLE task_runs ADD COLUMN cron_scheduled_at DATETIME;
ALTER TABLE task_runs ADD COLUMN backup_config_fingerprint TEXT NOT NULL DEFAULT '';

CREATE TEMP TABLE task_runs_000082_cron_duplicate_guard (
    valid INTEGER NOT NULL CHECK (valid = 1)
);
INSERT INTO task_runs_000082_cron_duplicate_guard(valid)
SELECT CASE WHEN EXISTS (
    SELECT task_id, cron_scheduled_at
    FROM task_runs
    WHERE trigger_type = 'cron' AND cron_scheduled_at IS NOT NULL
    GROUP BY task_id, cron_scheduled_at
    HAVING COUNT(*) > 1
) THEN 0 ELSE 1 END;
DROP TABLE task_runs_000082_cron_duplicate_guard;

CREATE UNIQUE INDEX IF NOT EXISTS idx_task_runs_cron_occurrence
    ON task_runs(task_id, cron_scheduled_at)
    WHERE trigger_type = 'cron' AND cron_scheduled_at IS NOT NULL;

-- A scheduled occurrence is immutable. A pending run may populate an empty
-- backup binding exactly once immediately before execution; once populated,
-- the successful backup provenance cannot be rebound.
DROP TRIGGER IF EXISTS trg_task_runs_cron_provenance_immutable;
CREATE TRIGGER trg_task_runs_cron_provenance_immutable
BEFORE UPDATE OF trigger_type, cron_scheduled_at, backup_config_fingerprint ON task_runs
WHEN NEW.trigger_type IS NOT OLD.trigger_type
    OR NEW.cron_scheduled_at IS NOT OLD.cron_scheduled_at
    OR (
        NEW.backup_config_fingerprint IS NOT OLD.backup_config_fingerprint
        AND NOT (
            COALESCE(OLD.backup_config_fingerprint, '') = ''
            AND COALESCE(NEW.backup_config_fingerprint, '') <> ''
            AND OLD.status = 'pending'
        )
    )
BEGIN
    SELECT RAISE(ABORT, 'TaskRun cron occurrence and backup provenance are immutable');
END;

-- Never erase durable occurrence identity or backup provenance on downgrade.
DROP TRIGGER IF EXISTS trg_task_runs_cron_provenance_downgrade_admission;
CREATE TRIGGER trg_task_runs_cron_provenance_downgrade_admission
BEFORE INSERT ON schema_migrations
WHEN NEW.version < 82
 AND (
    EXISTS (SELECT 1 FROM task_runs WHERE cron_scheduled_at IS NOT NULL)
    OR EXISTS (SELECT 1 FROM task_runs WHERE COALESCE(backup_config_fingerprint, '') <> '')
 )
BEGIN
    SELECT RAISE(ABORT, '000082 downgrade blocked: TaskRun cron occurrence or backup provenance exists');
END;
