-- 000003_node_probe.down.sql (PostgreSQL)
ALTER TABLE nodes DROP COLUMN IF EXISTS last_probe_at;
ALTER TABLE nodes DROP COLUMN IF EXISTS consecutive_failures;
