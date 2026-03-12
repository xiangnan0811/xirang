-- 000002_policy_nodes.down.sql
DROP TABLE IF EXISTS policy_nodes;
-- SQLite < 3.35 不支持 DROP COLUMN，仅 PostgreSQL 可回滚
-- ALTER TABLE tasks DROP COLUMN source;
