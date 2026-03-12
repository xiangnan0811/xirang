-- 000002_policy_nodes.up.sql
-- F1: 策略-节点关联表 + 任务来源字段

CREATE TABLE IF NOT EXISTS policy_nodes (
    policy_id INTEGER NOT NULL,
    node_id   INTEGER NOT NULL,
    created_at DATETIME,
    PRIMARY KEY (policy_id, node_id)
);
CREATE INDEX IF NOT EXISTS idx_policy_nodes_node_id ON policy_nodes(node_id);

ALTER TABLE tasks ADD COLUMN source VARCHAR(32) NOT NULL DEFAULT 'manual';
