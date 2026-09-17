-- 000086 separates durable cron intent from executor admission and freezes
-- provider/resource identity before legacy mutable writers can arm.
ALTER TABLE task_runs ADD COLUMN executor_type_snapshot VARCHAR(32) NOT NULL DEFAULT '';
ALTER TABLE task_runs ADD COLUMN resource_key VARCHAR(64) NOT NULL DEFAULT '';
ALTER TABLE task_runs ADD COLUMN resource_provider VARCHAR(32) NOT NULL DEFAULT '';
ALTER TABLE task_runs ADD COLUMN resource_node_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE task_runs ADD COLUMN resource_namespace VARCHAR(255) NOT NULL DEFAULT '';
ALTER TABLE task_runs ADD COLUMN resource_locator VARCHAR(512) NOT NULL DEFAULT '';
ALTER TABLE task_runs ADD COLUMN resource_evidence TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS task_cron_occurrences (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id INTEGER NOT NULL,
    scheduled_at DATETIME NOT NULL,
    state VARCHAR(16) NOT NULL DEFAULT 'queued'
        CHECK (state IN ('queued', 'dispatched', 'skipped', 'canceled')),
    task_run_id INTEGER,
    dispatch_owner_id VARCHAR(64) NOT NULL DEFAULT '',
    dispatch_lease_until DATETIME,
    reason TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    FOREIGN KEY (task_id) REFERENCES tasks(id) ON DELETE CASCADE,
    FOREIGN KEY (task_run_id) REFERENCES task_runs(id) ON DELETE SET NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_task_cron_occurrences_task_scheduled
    ON task_cron_occurrences(task_id, scheduled_at);
CREATE INDEX IF NOT EXISTS idx_task_cron_occurrences_queue
    ON task_cron_occurrences(state, scheduled_at, id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_task_cron_occurrences_task_run
    ON task_cron_occurrences(task_run_id)
    WHERE task_run_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_task_runs_resource_hold
    ON task_runs(resource_key, backup_generation_state, status);
CREATE UNIQUE INDEX IF NOT EXISTS idx_task_runs_resource_active_unique
    ON task_runs(resource_key)
    WHERE resource_key <> '' AND backup_generation_state IN ('writing', 'unknown');
CREATE INDEX IF NOT EXISTS idx_task_runs_executor_snapshot
    ON task_runs(executor_type_snapshot, node_id_snapshot, status);

-- New TaskRuns are classified and their resource evidence is immutable. A
-- historical row with no executor snapshot remains compatibility history: its
-- trigger type may still be repaired, but once classified it cannot be rebound.
DROP TRIGGER IF EXISTS trg_task_runs_execution_resource_immutable;
CREATE TRIGGER trg_task_runs_execution_resource_immutable
BEFORE UPDATE OF trigger_type, executor_type_snapshot, resource_key,
    resource_provider, resource_node_id, resource_namespace,
    resource_locator, resource_evidence ON task_runs
WHEN (
    COALESCE(OLD.executor_type_snapshot, '') <> ''
    AND NEW.trigger_type IS NOT OLD.trigger_type
)
OR NEW.executor_type_snapshot IS NOT OLD.executor_type_snapshot
OR NEW.resource_key IS NOT OLD.resource_key
OR NEW.resource_provider IS NOT OLD.resource_provider
OR NEW.resource_node_id IS NOT OLD.resource_node_id
OR NEW.resource_namespace IS NOT OLD.resource_namespace
OR NEW.resource_locator IS NOT OLD.resource_locator
OR NEW.resource_evidence IS NOT OLD.resource_evidence
BEGIN
    SELECT RAISE(ABORT, 'TaskRun executor classification and resource identity are immutable');
END;

DROP TRIGGER IF EXISTS trg_task_cron_occurrences_identity_immutable;
CREATE TRIGGER trg_task_cron_occurrences_identity_immutable
BEFORE UPDATE OF task_id, scheduled_at ON task_cron_occurrences
WHEN NEW.task_id IS NOT OLD.task_id OR NEW.scheduled_at IS NOT OLD.scheduled_at
BEGIN
    SELECT RAISE(ABORT, 'cron occurrence identity is immutable');
END;

-- Never erase durable cron intent, immutable attempt classification, or
-- resource identity evidence on downgrade.
DROP TRIGGER IF EXISTS trg_task_cron_occurrences_resource_identity_downgrade_admission;
CREATE TRIGGER trg_task_cron_occurrences_resource_identity_downgrade_admission
BEFORE INSERT ON schema_migrations
WHEN NEW.version < 86
 AND (
    EXISTS (SELECT 1 FROM task_cron_occurrences)
    OR EXISTS (SELECT 1 FROM task_runs WHERE COALESCE(executor_type_snapshot, '') <> '')
    OR EXISTS (SELECT 1 FROM task_runs WHERE COALESCE(resource_key, '') <> '')
    OR EXISTS (SELECT 1 FROM task_runs WHERE COALESCE(resource_evidence, '') <> '')
 )
BEGIN
    SELECT RAISE(ABORT, '000086 downgrade blocked: cron intent or immutable execution/resource evidence exists');
END;
