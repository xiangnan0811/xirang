BEGIN;

-- 000079 adds ordinary TaskRun execution ownership and durable terminal effects.
-- Terminal effects are intentionally task-run scoped; they are not a generic
-- event bus and are retained until their consumer has acknowledged them.
ALTER TABLE task_runs
    ADD COLUMN execution_owner_id VARCHAR(64) NOT NULL DEFAULT '',
    ADD COLUMN execution_lease_until TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS idx_task_runs_execution_lease
    ON task_runs(execution_lease_until);

CREATE TABLE IF NOT EXISTS task_run_effects (
    id SERIAL PRIMARY KEY,
    task_run_id INTEGER NOT NULL
        REFERENCES task_runs(id) ON DELETE CASCADE,
    effect_key VARCHAR(160) NOT NULL,
    effect_type VARCHAR(32) NOT NULL,
    payload TEXT NOT NULL DEFAULT '',
    status VARCHAR(16) NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'running', 'succeeded', 'failed')),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at TIMESTAMPTZ,
    claimed_by VARCHAR(64) NOT NULL DEFAULT '',
    claim_lease_until TIMESTAMPTZ,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE(task_run_id, effect_key)
);
CREATE INDEX IF NOT EXISTS idx_task_run_effects_ready
    ON task_run_effects(status, next_attempt_at, claim_lease_until);
CREATE INDEX IF NOT EXISTS idx_task_run_effects_task_run
    ON task_run_effects(task_run_id);

-- At most one chain trigger may be created for a given upstream TaskRun and
-- downstream task. This closes the crash window between trigger commit and
-- durable effect acknowledgement.
-- A unique downstream edge is part of the durable contract. Reject historical
-- duplicates instead of silently choosing a winner during index creation.
DO $$
BEGIN
    IF EXISTS (
        SELECT task_id, upstream_task_run_id
        FROM task_runs
        WHERE upstream_task_run_id IS NOT NULL
        GROUP BY task_id, upstream_task_run_id
        HAVING COUNT(*) > 1
    ) THEN
        RAISE EXCEPTION '000079 upgrade blocked: duplicate downstream upstream TaskRun pairs exist';
    END IF;
END
$$;
CREATE UNIQUE INDEX IF NOT EXISTS idx_task_runs_downstream_once
    ON task_runs(task_id, upstream_task_run_id)
    WHERE upstream_task_run_id IS NOT NULL;
-- Metadata admission runs before the migration driver marks a downgrade dirty.
-- Durable effects and live ordinary execution leases must never be erased by
-- a version rollback.
CREATE OR REPLACE FUNCTION task_run_terminal_effects_downgrade_admission()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.version < 79 AND (
        EXISTS (SELECT 1 FROM task_run_effects)
        OR EXISTS (
            SELECT 1
            FROM task_runs
            WHERE COALESCE(trigger_type, '') <> 'drill'
              AND status IN ('pending', 'running', 'retrying')
              AND execution_lease_until IS NOT NULL
              AND execution_lease_until > NOW()
        )
    ) THEN
        RAISE EXCEPTION '000079 downgrade blocked: terminal effects or live ordinary execution lease exists';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_task_run_terminal_effects_downgrade_admission
BEFORE INSERT ON schema_migrations
FOR EACH ROW EXECUTE FUNCTION task_run_terminal_effects_downgrade_admission();
COMMIT;
