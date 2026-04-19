CREATE TABLE IF NOT EXISTS silences (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    name            TEXT NOT NULL,
    match_node_id   INTEGER NULL REFERENCES nodes(id) ON DELETE CASCADE,
    match_category  TEXT NULL,
    match_tags      TEXT NULL,
    starts_at       DATETIME NOT NULL,
    ends_at         DATETIME NOT NULL,
    created_by      INTEGER NOT NULL REFERENCES users(id),
    note            TEXT,
    created_at      DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at      DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_silences_active  ON silences(starts_at, ends_at);
CREATE INDEX IF NOT EXISTS idx_silences_cleanup ON silences(ends_at);
