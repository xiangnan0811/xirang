CREATE TABLE IF NOT EXISTS node_log_cursors (
    id          BIGSERIAL   PRIMARY KEY,
    node_id     BIGINT      NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    source      TEXT        NOT NULL,
    path        TEXT        NOT NULL,
    cursor_text TEXT,
    file_offset BIGINT,
    file_inode  BIGINT,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(node_id, source, path)
);
