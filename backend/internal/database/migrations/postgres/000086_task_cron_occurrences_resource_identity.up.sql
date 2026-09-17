BEGIN;

-- 000086 separates durable cron intent from executor admission and freezes
-- provider/resource identity before legacy mutable writers can arm.
ALTER TABLE task_runs
    ADD COLUMN IF NOT EXISTS executor_type_snapshot VARCHAR(32) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS resource_key VARCHAR(64) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS resource_provider VARCHAR(32) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS resource_node_id BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS resource_namespace VARCHAR(255) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS resource_locator VARCHAR(512) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS resource_evidence TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS task_cron_occurrences (
    id BIGSERIAL PRIMARY KEY,
    task_id BIGINT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    scheduled_at TIMESTAMPTZ NOT NULL,
    state VARCHAR(16) NOT NULL DEFAULT 'queued'
        CHECK (state IN ('queued', 'dispatched', 'skipped', 'canceled')),
    task_run_id BIGINT REFERENCES task_runs(id) ON DELETE SET NULL,
    dispatch_owner_id VARCHAR(64) NOT NULL DEFAULT '',
    dispatch_lease_until TIMESTAMPTZ,
    reason TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
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

CREATE OR REPLACE FUNCTION task_runs_execution_resource_immutable_guard()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF (COALESCE(OLD.executor_type_snapshot, '') <> ''
        AND NEW.trigger_type IS DISTINCT FROM OLD.trigger_type)
       OR NEW.executor_type_snapshot IS DISTINCT FROM OLD.executor_type_snapshot
       OR NEW.resource_key IS DISTINCT FROM OLD.resource_key
       OR NEW.resource_provider IS DISTINCT FROM OLD.resource_provider
       OR NEW.resource_node_id IS DISTINCT FROM OLD.resource_node_id
       OR NEW.resource_namespace IS DISTINCT FROM OLD.resource_namespace
       OR NEW.resource_locator IS DISTINCT FROM OLD.resource_locator
       OR NEW.resource_evidence IS DISTINCT FROM OLD.resource_evidence THEN
        RAISE EXCEPTION 'TaskRun executor classification and resource identity are immutable';
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS trg_task_runs_execution_resource_immutable ON task_runs;
CREATE TRIGGER trg_task_runs_execution_resource_immutable
BEFORE UPDATE OF trigger_type, executor_type_snapshot, resource_key,
    resource_provider, resource_node_id, resource_namespace,
    resource_locator, resource_evidence ON task_runs
FOR EACH ROW EXECUTE FUNCTION task_runs_execution_resource_immutable_guard();

CREATE OR REPLACE FUNCTION task_cron_occurrences_identity_immutable_guard()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.task_id IS DISTINCT FROM OLD.task_id
       OR NEW.scheduled_at IS DISTINCT FROM OLD.scheduled_at THEN
        RAISE EXCEPTION 'cron occurrence identity is immutable';
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS trg_task_cron_occurrences_identity_immutable ON task_cron_occurrences;
CREATE TRIGGER trg_task_cron_occurrences_identity_immutable
BEFORE UPDATE OF task_id, scheduled_at ON task_cron_occurrences
FOR EACH ROW EXECUTE FUNCTION task_cron_occurrences_identity_immutable_guard();

-- Never erase durable cron intent, immutable attempt classification, or
-- resource identity evidence on downgrade.
CREATE OR REPLACE FUNCTION task_cron_occurrences_resource_identity_downgrade_admission()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.version < 86 AND (
        EXISTS (SELECT 1 FROM task_cron_occurrences)
        OR EXISTS (SELECT 1 FROM task_runs WHERE COALESCE(executor_type_snapshot, '') <> '')
        OR EXISTS (SELECT 1 FROM task_runs WHERE COALESCE(resource_key, '') <> '')
        OR EXISTS (SELECT 1 FROM task_runs WHERE COALESCE(resource_evidence, '') <> '')
    ) THEN
        RAISE EXCEPTION '000086 downgrade blocked: cron intent or immutable execution/resource evidence exists';
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS trg_task_cron_occurrences_resource_identity_downgrade_admission ON schema_migrations;
CREATE TRIGGER trg_task_cron_occurrences_resource_identity_downgrade_admission
BEFORE INSERT ON schema_migrations
FOR EACH ROW EXECUTE FUNCTION task_cron_occurrences_resource_identity_downgrade_admission();

COMMIT;
