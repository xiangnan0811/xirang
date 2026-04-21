CREATE TABLE IF NOT EXISTS node_log_cursors (
    id          INTEGER  PRIMARY KEY AUTOINCREMENT,
    node_id     INTEGER  NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    source      TEXT     NOT NULL,
    path        TEXT     NOT NULL,
    cursor_text TEXT,
    file_offset BIGINT,
    file_inode  BIGINT,
    updated_at  DATETIME NOT NULL DEFAULT (datetime('now')),
    UNIQUE(node_id, source, path)
);
