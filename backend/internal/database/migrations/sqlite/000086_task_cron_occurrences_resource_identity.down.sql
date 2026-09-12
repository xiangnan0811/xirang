-- Refuse to remove any durable intent, attempt classification, or resource
-- identity. This check is intentionally executed before dropping the objects.
CREATE TEMP TABLE task_cron_occurrences_resource_identity_000086_downgrade_guard (
    valid INTEGER NOT NULL CHECK (valid = 1)
);
INSERT INTO task_cron_occurrences_resource_identity_000086_downgrade_guard(valid)
SELECT CASE WHEN EXISTS (SELECT 1 FROM task_cron_occurrences)
    OR EXISTS (SELECT 1 FROM task_runs WHERE COALESCE(executor_type_snapshot, '') <> '')
    OR EXISTS (SELECT 1 FROM task_runs WHERE COALESCE(resource_key, '') <> '')
    OR EXISTS (SELECT 1 FROM task_runs WHERE COALESCE(resource_evidence, '') <> '')
    THEN 0 ELSE 1 END;
DROP TABLE task_cron_occurrences_resource_identity_000086_downgrade_guard;

DROP TRIGGER IF EXISTS trg_task_cron_occurrences_resource_identity_downgrade_admission;
DROP TRIGGER IF EXISTS trg_task_cron_occurrences_identity_immutable;
DROP TRIGGER IF EXISTS trg_task_runs_execution_resource_immutable;
DROP INDEX IF EXISTS idx_task_cron_occurrences_task_run;
DROP INDEX IF EXISTS idx_task_cron_occurrences_queue;
DROP INDEX IF EXISTS idx_task_runs_resource_active_unique;
DROP INDEX IF EXISTS idx_task_runs_resource_hold;
DROP INDEX IF EXISTS idx_task_runs_executor_snapshot;
DROP TABLE IF EXISTS task_cron_occurrences;
ALTER TABLE task_runs DROP COLUMN resource_evidence;
ALTER TABLE task_runs DROP COLUMN resource_locator;
ALTER TABLE task_runs DROP COLUMN resource_namespace;
ALTER TABLE task_runs DROP COLUMN resource_node_id;
ALTER TABLE task_runs DROP COLUMN resource_provider;
ALTER TABLE task_runs DROP COLUMN resource_key;
ALTER TABLE task_runs DROP COLUMN executor_type_snapshot;
