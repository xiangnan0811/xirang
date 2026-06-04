BEGIN;

-- 000058_restore_drill_evidence.up.sql
-- Structured restore drill evidence for policy-level trust/confidence consumption.

CREATE TABLE IF NOT EXISTS restore_drill_evidences (
    id                    SERIAL PRIMARY KEY,
    policy_id             INTEGER NOT NULL,
    task_id               INTEGER NOT NULL,
    task_run_id           INTEGER NOT NULL,
    source_task_run_id    INTEGER,
    snapshot_ref          VARCHAR(128) NOT NULL DEFAULT '',
    sandbox_node_id       INTEGER NOT NULL,
    sandbox_node_name     VARCHAR(128) NOT NULL DEFAULT '',
    sandbox_path          VARCHAR(512) NOT NULL,
    status                VARCHAR(32)  NOT NULL DEFAULT 'pending',
    failed_step           VARCHAR(64)  NOT NULL DEFAULT '',
    confidence_eligible   BOOLEAN      NOT NULL DEFAULT false,
    started_at            TIMESTAMPTZ,
    finished_at           TIMESTAMPTZ,
    duration_ms           BIGINT       NOT NULL DEFAULT 0,
    restore_status        VARCHAR(32)  NOT NULL DEFAULT 'pending',
    restore_started_at    TIMESTAMPTZ,
    restore_finished_at   TIMESTAMPTZ,
    restore_error         TEXT,
    verify_status         VARCHAR(32)  NOT NULL DEFAULT 'pending',
    verify_started_at     TIMESTAMPTZ,
    verify_finished_at    TIMESTAMPTZ,
    verify_error          TEXT,
    post_verify_status    VARCHAR(32)  NOT NULL DEFAULT 'skipped',
    post_verify_finished_at TIMESTAMPTZ,
    post_verify_error     TEXT,
    cleanup_status        VARCHAR(32)  NOT NULL DEFAULT 'pending',
    cleanup_started_at    TIMESTAMPTZ,
    cleanup_finished_at   TIMESTAMPTZ,
    cleanup_error         TEXT,
    created_at            TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_restore_drill_evidences_task_run ON restore_drill_evidences(task_run_id);
CREATE INDEX IF NOT EXISTS idx_restore_drill_evidences_policy ON restore_drill_evidences(policy_id);
CREATE INDEX IF NOT EXISTS idx_restore_drill_evidences_task ON restore_drill_evidences(task_id);
CREATE INDEX IF NOT EXISTS idx_restore_drill_evidences_sandbox_node ON restore_drill_evidences(sandbox_node_id);
CREATE INDEX IF NOT EXISTS idx_restore_drill_evidences_status ON restore_drill_evidences(status);
CREATE INDEX IF NOT EXISTS idx_restore_drill_evidences_policy_created ON restore_drill_evidences(policy_id, created_at);
CREATE INDEX IF NOT EXISTS idx_restore_drill_evidences_source_task_run ON restore_drill_evidences(source_task_run_id);

COMMIT;
