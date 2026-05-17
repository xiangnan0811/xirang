-- 000058_restore_drill_evidence.up.sql
-- Structured restore drill evidence for policy-level trust/confidence consumption.

CREATE TABLE IF NOT EXISTS restore_drill_evidences (
    id                    INTEGER PRIMARY KEY AUTOINCREMENT,
    policy_id             INTEGER NOT NULL,
    task_id               INTEGER NOT NULL,
    task_run_id           INTEGER NOT NULL,
    source_task_run_id    INTEGER,
    snapshot_ref          TEXT    NOT NULL DEFAULT '',
    sandbox_node_id       INTEGER NOT NULL,
    sandbox_node_name     TEXT    NOT NULL DEFAULT '',
    sandbox_path          TEXT    NOT NULL,
    status                TEXT    NOT NULL DEFAULT 'pending',
    failed_step           TEXT    NOT NULL DEFAULT '',
    confidence_eligible   INTEGER NOT NULL DEFAULT 0,
    started_at            DATETIME,
    finished_at           DATETIME,
    duration_ms           INTEGER NOT NULL DEFAULT 0,
    restore_status        TEXT    NOT NULL DEFAULT 'pending',
    restore_started_at    DATETIME,
    restore_finished_at   DATETIME,
    restore_error         TEXT,
    verify_status         TEXT    NOT NULL DEFAULT 'pending',
    verify_started_at     DATETIME,
    verify_finished_at    DATETIME,
    verify_error          TEXT,
    post_verify_status    TEXT    NOT NULL DEFAULT 'skipped',
    post_verify_finished_at DATETIME,
    post_verify_error     TEXT,
    cleanup_status        TEXT    NOT NULL DEFAULT 'pending',
    cleanup_started_at    DATETIME,
    cleanup_finished_at   DATETIME,
    cleanup_error         TEXT,
    created_at            DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at            DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_restore_drill_evidences_task_run ON restore_drill_evidences(task_run_id);
CREATE INDEX IF NOT EXISTS idx_restore_drill_evidences_policy ON restore_drill_evidences(policy_id);
CREATE INDEX IF NOT EXISTS idx_restore_drill_evidences_task ON restore_drill_evidences(task_id);
CREATE INDEX IF NOT EXISTS idx_restore_drill_evidences_sandbox_node ON restore_drill_evidences(sandbox_node_id);
CREATE INDEX IF NOT EXISTS idx_restore_drill_evidences_status ON restore_drill_evidences(status);
CREATE INDEX IF NOT EXISTS idx_restore_drill_evidences_policy_created ON restore_drill_evidences(policy_id, created_at);
