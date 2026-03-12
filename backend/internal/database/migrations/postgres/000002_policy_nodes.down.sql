-- 000002_policy_nodes.down.sql (PostgreSQL)
DROP TABLE IF EXISTS policy_nodes;
ALTER TABLE tasks DROP COLUMN IF EXISTS source;
