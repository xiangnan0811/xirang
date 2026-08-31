-- Reject ambiguous historical ownership before changing either table. The
-- operator must terminalize duplicate TaskRun/Evidence pairs atomically and
-- retry; the migration never guesses which active drill should win.
BEGIN;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM task_runs
        WHERE trigger_type = 'drill'
          AND status IN ('pending', 'running', 'retrying')
        GROUP BY task_id
        HAVING COUNT(*) > 1
    ) THEN
        RAISE EXCEPTION '000074 rejected duplicate active restore drills';
    END IF;
END
$$;

ALTER TABLE restore_drill_evidences
    ADD COLUMN recovery_owner_id VARCHAR(64) NOT NULL DEFAULT '',
    ADD COLUMN recovery_lease_until TIMESTAMPTZ;

CREATE INDEX idx_restore_drill_recovery_lease
    ON restore_drill_evidences(recovery_lease_until);
CREATE UNIQUE INDEX idx_task_runs_active_drill
    ON task_runs(task_id)
    WHERE trigger_type = 'drill'
      AND status IN ('pending', 'running', 'retrying');

CREATE OR REPLACE FUNCTION drill_durable_recovery_downgrade_admission()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.version < 74 AND EXISTS (
        SELECT 1 FROM task_runs
        WHERE trigger_type = 'drill'
          AND status IN ('pending', 'running', 'retrying')
    ) THEN
        RAISE EXCEPTION '000074 downgrade blocked: active restore drill exists';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_drill_durable_recovery_downgrade_admission ON schema_migrations;
CREATE TRIGGER trg_drill_durable_recovery_downgrade_admission
BEFORE INSERT ON schema_migrations
FOR EACH ROW
EXECUTE FUNCTION drill_durable_recovery_downgrade_admission();

COMMIT;
