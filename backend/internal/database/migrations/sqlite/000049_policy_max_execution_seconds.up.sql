-- 给策略增加任务最大执行秒数字段，用于 runner 的全局执行超时兜底。
-- 默认 0 = 使用环境变量 TASK_MAX_EXECUTION_SECONDS（默认 24h=86400）。
ALTER TABLE policies ADD COLUMN max_execution_seconds INTEGER NOT NULL DEFAULT 0;
