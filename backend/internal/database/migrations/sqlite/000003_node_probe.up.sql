-- 000003_node_probe.up.sql
-- F2: 节点探测字段

ALTER TABLE nodes ADD COLUMN last_probe_at DATETIME;
ALTER TABLE nodes ADD COLUMN consecutive_failures INTEGER NOT NULL DEFAULT 0;
