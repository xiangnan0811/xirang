-- node_logs retention DELETE filters on (node_id, created_at). Without a
-- composite covering both columns, the planner falls back to the per-node
-- index and scans every row for the node, even when the cutoff matches
-- zero rows. Adding (node_id, created_at) satisfies both predicates from
-- the index in O(log n).
CREATE INDEX IF NOT EXISTS idx_node_logs_node_created ON node_logs(node_id, created_at);
