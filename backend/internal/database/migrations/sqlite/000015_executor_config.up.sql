-- 000015_executor_config.up.sql
-- 新增 executor_config 字段，存储各执行器的特定配置（JSON 文本，加密存储）

ALTER TABLE tasks ADD COLUMN executor_config TEXT NOT NULL DEFAULT '';
