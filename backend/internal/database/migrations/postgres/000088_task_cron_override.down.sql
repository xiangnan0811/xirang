BEGIN;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM tasks WHERE cron_override) THEN
        RAISE EXCEPTION '000088 downgrade blocked: task cron override provenance exists';
    END IF;
END
$$;

DROP TRIGGER IF EXISTS trg_task_cron_override_downgrade_admission ON schema_migrations;
DROP FUNCTION IF EXISTS task_cron_override_downgrade_admission();
ALTER TABLE tasks DROP COLUMN IF EXISTS cron_override;

COMMIT;
