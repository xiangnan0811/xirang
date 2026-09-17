BEGIN;
CREATE TABLE batch_commands (
    id VARCHAR(64) PRIMARY KEY,
    requester_id BIGINT NOT NULL,
    idempotency_key VARCHAR(256) NOT NULL,
    request_hash VARCHAR(64) NOT NULL,
    retain BOOLEAN NOT NULL DEFAULT false,
    deleted_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE (requester_id, idempotency_key)
);
CREATE TABLE batch_command_dispatches (
    task_id BIGINT PRIMARY KEY,
    batch_id VARCHAR(64) NOT NULL REFERENCES batch_commands(id),
    node_id BIGINT NOT NULL,
    status VARCHAR(32) NOT NULL CHECK (status IN ('pending','dispatching','accepted','failed')),
    run_id BIGINT NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_batch_command_dispatches_batch_id ON batch_command_dispatches(batch_id);
CREATE FUNCTION batch_command_downgrade_admission() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.version < 81 AND EXISTS (SELECT 1 FROM batch_commands) THEN
        RAISE EXCEPTION '000081 downgrade blocked: batch idempotency records exist';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_batch_command_downgrade_admission BEFORE INSERT ON schema_migrations
FOR EACH ROW EXECUTE FUNCTION batch_command_downgrade_admission();
COMMIT;
