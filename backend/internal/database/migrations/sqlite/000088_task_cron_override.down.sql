CREATE TEMP TABLE task_cron_override_000088_downgrade_guard (
    valid INTEGER NOT NULL CHECK (valid = 1)
);
INSERT INTO task_cron_override_000088_downgrade_guard(valid)
SELECT CASE WHEN EXISTS (SELECT 1 FROM tasks WHERE cron_override <> 0) THEN 0 ELSE 1 END;
DROP TABLE task_cron_override_000088_downgrade_guard;

DROP TRIGGER IF EXISTS trg_task_cron_override_downgrade_admission;
ALTER TABLE tasks DROP COLUMN cron_override;
