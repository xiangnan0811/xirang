CREATE TABLE IF NOT EXISTS node_logs (
    id          BIGSERIAL   PRIMARY KEY,
    node_id     BIGINT      NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    source      TEXT        NOT NULL,
    path        TEXT        NOT NULL,
    timestamp   TIMESTAMPTZ NOT NULL,
    priority    TEXT,
    message     TEXT        NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_node_logs_node_time ON node_logs(node_id, timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_node_logs_retention ON node_logs(created_at);
