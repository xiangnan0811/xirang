-- 000054_snapshot_file_index.up.sql
-- 快照文件索引表，支持跨快照按文件名/路径模糊搜索。

CREATE TABLE IF NOT EXISTS snapshot_file_indices (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id     INTEGER NOT NULL,
    snapshot_id TEXT    NOT NULL,
    path        TEXT    NOT NULL,
    size        INTEGER NOT NULL DEFAULT 0,
    mtime       TEXT    NOT NULL DEFAULT '',
    created_at  DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_sfi_task_snap_path ON snapshot_file_indices (task_id, snapshot_id, path);
CREATE INDEX IF NOT EXISTS idx_sfi_path ON snapshot_file_indices (path);
