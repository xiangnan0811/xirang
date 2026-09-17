BEGIN;

-- 000082 adds durable scheduler occurrence identity and immutable backup
-- configuration fingerprints. Legacy rows remain readable with NULL/empty values;
-- they are never treated as evidence for a restore.
ALTER TABLE task_runs
    ADD COLUMN cron_scheduled_at TIMESTAMPTZ,
    ADD COLUMN backup_config_fingerprint VARCHAR(64) NOT NULL DEFAULT '';

DO $$
BEGIN
    IF EXISTS (
        SELECT task_id, cron_scheduled_at
        FROM task_runs
        WHERE trigger_type = 'cron' AND cron_scheduled_at IS NOT NULL
        GROUP BY task_id, cron_scheduled_at
        HAVING COUNT(*) > 1
    ) THEN
        RAISE EXCEPTION '000082 upgrade blocked: duplicate cron occurrences exist';
    END IF;
END
$$;

CREATE UNIQUE INDEX IF NOT EXISTS idx_task_runs_cron_occurrence
    ON task_runs(task_id, cron_scheduled_at)
    WHERE trigger_type = 'cron' AND cron_scheduled_at IS NOT NULL;

-- A scheduled occurrence is immutable. A pending run may populate an empty
-- backup binding exactly once immediately before execution; once populated,
-- the successful backup provenance cannot be rebound.
CREATE OR REPLACE FUNCTION task_runs_cron_provenance_immutable_guard()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.trigger_type IS DISTINCT FROM OLD.trigger_type
       OR NEW.cron_scheduled_at IS DISTINCT FROM OLD.cron_scheduled_at
       OR (
           NEW.backup_config_fingerprint IS DISTINCT FROM OLD.backup_config_fingerprint
           AND NOT (
               COALESCE(OLD.backup_config_fingerprint, '') = ''
               AND COALESCE(NEW.backup_config_fingerprint, '') <> ''
               AND OLD.status = 'pending'
           )
       ) THEN
        RAISE EXCEPTION 'TaskRun cron occurrence and backup provenance are immutable';
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS trg_task_runs_cron_provenance_immutable ON task_runs;
CREATE TRIGGER trg_task_runs_cron_provenance_immutable
BEFORE UPDATE OF trigger_type, cron_scheduled_at, backup_config_fingerprint ON task_runs
FOR EACH ROW EXECUTE FUNCTION task_runs_cron_provenance_immutable_guard();

-- Never erase durable occurrence identity or backup provenance on downgrade.
CREATE OR REPLACE FUNCTION task_runs_cron_provenance_downgrade_admission()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.version < 82 AND (
        EXISTS (SELECT 1 FROM task_runs WHERE cron_scheduled_at IS NOT NULL)
        OR EXISTS (SELECT 1 FROM task_runs WHERE COALESCE(backup_config_fingerprint, '') <> '')
    ) THEN
        RAISE EXCEPTION '000082 downgrade blocked: TaskRun cron occurrence or backup provenance exists';
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS trg_task_runs_cron_provenance_downgrade_admission ON schema_migrations;
CREATE TRIGGER trg_task_runs_cron_provenance_downgrade_admission
BEFORE INSERT ON schema_migrations
FOR EACH ROW EXECUTE FUNCTION task_runs_cron_provenance_downgrade_admission();

COMMIT;
