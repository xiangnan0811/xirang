-- Fail closed before changing schema when historical ambiguity prevents a safe
-- choice of active owner. Operators must terminalize duplicate TaskRun/Evidence
-- pairs before retrying the migration; this migration never mutates one side.
DROP TABLE IF EXISTS drill_durable_recovery_000074_guard;
CREATE TEMP TABLE drill_durable_recovery_000074_guard (
    valid INTEGER NOT NULL CHECK (valid = 1)
);
INSERT INTO drill_durable_recovery_000074_guard(valid)
SELECT CASE WHEN EXISTS (
    SELECT 1
    FROM task_runs
    WHERE trigger_type = 'drill'
      AND status IN ('pending', 'running', 'retrying')
    GROUP BY task_id
    HAVING COUNT(*) > 1
) THEN 0 ELSE 1 END;
DROP TABLE drill_durable_recovery_000074_guard;

ALTER TABLE restore_drill_evidences
    ADD COLUMN recovery_owner_id TEXT NOT NULL DEFAULT '';
ALTER TABLE restore_drill_evidences
    ADD COLUMN recovery_lease_until DATETIME;

CREATE INDEX idx_restore_drill_recovery_lease
    ON restore_drill_evidences(recovery_lease_until);
CREATE UNIQUE INDEX idx_task_runs_active_drill
    ON task_runs(task_id)
    WHERE trigger_type = 'drill'
      AND status IN ('pending', 'running', 'retrying');

DROP TRIGGER IF EXISTS trg_drill_durable_recovery_downgrade_admission;
CREATE TRIGGER trg_drill_durable_recovery_downgrade_admission
BEFORE INSERT ON schema_migrations
WHEN NEW.version < 74
 AND EXISTS (
    SELECT 1 FROM task_runs
    WHERE trigger_type = 'drill'
      AND status IN ('pending', 'running', 'retrying')
 )
BEGIN
    SELECT RAISE(ABORT, '000074 downgrade blocked: active restore drill exists');
END;
