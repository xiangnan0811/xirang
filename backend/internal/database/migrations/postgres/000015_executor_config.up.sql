-- 000015_executor_config.up.sql

ALTER TABLE tasks ADD COLUMN executor_config TEXT NOT NULL DEFAULT '';
