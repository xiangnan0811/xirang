CREATE INDEX IF NOT EXISTS idx_task_runs_node_id ON task_runs(node_id);
CREATE INDEX IF NOT EXISTS idx_task_runs_node_created ON task_runs(node_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_nodes_created_at ON nodes(created_at DESC);
