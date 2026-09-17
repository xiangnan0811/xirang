BEGIN;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM task_cron_occurrences)
       OR EXISTS (SELECT 1 FROM task_runs WHERE COALESCE(executor_type_snapshot, '') <> '')
       OR EXISTS (SELECT 1 FROM task_runs WHERE COALESCE(resource_key, '') <> '')
       OR EXISTS (SELECT 1 FROM task_runs WHERE COALESCE(resource_evidence, '') <> '') THEN
        RAISE EXCEPTION '000086 downgrade blocked: cron intent or immutable execution/resource evidence exists';
    END IF;
END
$$;

DROP TRIGGER IF EXISTS trg_task_cron_occurrences_resource_identity_downgrade_admission ON schema_migrations;
DROP FUNCTION IF EXISTS task_cron_occurrences_resource_identity_downgrade_admission();
DROP TRIGGER IF EXISTS trg_task_cron_occurrences_identity_immutable ON task_cron_occurrences;
DROP FUNCTION IF EXISTS task_cron_occurrences_identity_immutable_guard();
DROP TRIGGER IF EXISTS trg_task_runs_execution_resource_immutable ON task_runs;
DROP FUNCTION IF EXISTS task_runs_execution_resource_immutable_guard();
DROP INDEX IF EXISTS idx_task_cron_occurrences_task_run;
DROP INDEX IF EXISTS idx_task_cron_occurrences_queue;
DROP INDEX IF EXISTS idx_task_runs_resource_active_unique;
DROP INDEX IF EXISTS idx_task_runs_resource_hold;
DROP INDEX IF EXISTS idx_task_runs_executor_snapshot;
DROP TABLE IF EXISTS task_cron_occurrences;
ALTER TABLE task_runs
    DROP COLUMN IF EXISTS resource_evidence,
    DROP COLUMN IF EXISTS resource_locator,
    DROP COLUMN IF EXISTS resource_namespace,
    DROP COLUMN IF EXISTS resource_node_id,
    DROP COLUMN IF EXISTS resource_provider,
    DROP COLUMN IF EXISTS resource_key,
    DROP COLUMN IF EXISTS executor_type_snapshot;

COMMIT;
