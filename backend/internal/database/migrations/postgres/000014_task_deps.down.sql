-- 000014_task_deps.down.sql (postgres)

DROP INDEX IF EXISTS idx_task_runs_upstream;
DROP INDEX IF EXISTS idx_task_runs_chain_run;
DROP INDEX IF EXISTS idx_tasks_depends_on;

ALTER TABLE task_runs DROP COLUMN skip_reason;
ALTER TABLE task_runs DROP COLUMN upstream_task_run_id;
ALTER TABLE task_runs DROP COLUMN chain_run_id;

ALTER TABLE tasks DROP COLUMN depends_on_task_id;
