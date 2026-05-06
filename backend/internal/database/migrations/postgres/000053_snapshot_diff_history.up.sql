BEGIN;

-- 000053_snapshot_diff_history.up.sql
-- 快照差异历史表，为快照异常变更检测提供基线数据。

CREATE TABLE IF NOT EXISTS snapshot_diff_histories (
    id                SERIAL PRIMARY KEY,
    policy_id         INTEGER NOT NULL,
    task_id           INTEGER NOT NULL,
    task_run_id       INTEGER NOT NULL,
    added_count       INTEGER NOT NULL DEFAULT 0,
    removed_count     INTEGER NOT NULL DEFAULT 0,
    changed_count     INTEGER NOT NULL DEFAULT 0,
    total_size_bytes  BIGINT NOT NULL DEFAULT 0,
    ransom_suffix_hits INTEGER NOT NULL DEFAULT 0,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_sdh_policy ON snapshot_diff_histories (policy_id);
CREATE INDEX IF NOT EXISTS idx_sdh_task ON snapshot_diff_histories (task_id);
CREATE INDEX IF NOT EXISTS idx_sdh_task_run ON snapshot_diff_histories (task_run_id);

COMMIT;
