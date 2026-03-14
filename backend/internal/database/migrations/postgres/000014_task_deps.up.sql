-- 000014_task_deps.up.sql (postgres)
-- F10: 任务依赖编排 - Task 新增前置依赖字段，TaskRun 新增链路追踪字段

ALTER TABLE tasks ADD COLUMN depends_on_task_id BIGINT REFERENCES tasks(id) ON DELETE SET NULL;

ALTER TABLE task_runs ADD COLUMN chain_run_id VARCHAR(64) NOT NULL DEFAULT '';
ALTER TABLE task_runs ADD COLUMN upstream_task_run_id BIGINT REFERENCES task_runs(id) ON DELETE SET NULL;
ALTER TABLE task_runs ADD COLUMN skip_reason TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_tasks_depends_on ON tasks(depends_on_task_id);
CREATE INDEX IF NOT EXISTS idx_task_runs_chain_run ON task_runs(chain_run_id);
CREATE INDEX IF NOT EXISTS idx_task_runs_upstream ON task_runs(upstream_task_run_id);
