CREATE TABLE batch_commands (
    id TEXT PRIMARY KEY,
    requester_id INTEGER NOT NULL,
    idempotency_key TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    retain INTEGER NOT NULL DEFAULT 0,
    deleted_at DATETIME,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    UNIQUE (requester_id, idempotency_key)
);
CREATE TABLE batch_command_dispatches (
    task_id INTEGER PRIMARY KEY,
    batch_id TEXT NOT NULL REFERENCES batch_commands(id),
    node_id INTEGER NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending','dispatching','accepted','failed')),
    run_id INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL
);
CREATE INDEX idx_batch_command_dispatches_batch_id ON batch_command_dispatches(batch_id);
CREATE TRIGGER trg_batch_command_downgrade_admission
BEFORE INSERT ON schema_migrations
WHEN NEW.version < 81 AND EXISTS (SELECT 1 FROM batch_commands)
BEGIN
    SELECT RAISE(ABORT, '000081 downgrade blocked: batch idempotency records exist');
END;
