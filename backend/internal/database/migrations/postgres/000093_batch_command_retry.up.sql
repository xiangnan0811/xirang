BEGIN;

-- Existing version-0 rows receive neutral defaults. The trigger guards below
-- prevent historical records from being promoted into the v1 retry contract.
ALTER TABLE batch_commands
    ADD COLUMN provenance_version INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN source_batch_id VARCHAR(64);
ALTER TABLE batch_commands
    ADD CONSTRAINT batch_command_provenance_version_check
        CHECK (provenance_version IN (0, 1));
ALTER TABLE batch_commands
    ADD CONSTRAINT batch_command_source_batch_fk
        FOREIGN KEY (source_batch_id) REFERENCES batch_commands(id) ON DELETE RESTRICT;

ALTER TABLE batch_command_dispatches
    ADD COLUMN command_fingerprint VARCHAR(64) NOT NULL DEFAULT '',
    ADD COLUMN retry_of_task_id BIGINT,
    ADD COLUMN retry_of_run_id BIGINT;
ALTER TABLE batch_command_dispatches
    ADD CONSTRAINT batch_command_dispatch_command_fingerprint_check
        CHECK (command_fingerprint = '' OR command_fingerprint ~ '^[0-9a-f]{64}$'),
    ADD CONSTRAINT batch_command_dispatch_retry_pair_check
        CHECK ((retry_of_task_id IS NULL AND retry_of_run_id IS NULL)
               OR (retry_of_task_id IS NOT NULL AND retry_of_run_id IS NOT NULL)),
    ADD CONSTRAINT batch_command_dispatch_retry_task_check
        CHECK (retry_of_task_id IS NULL OR retry_of_task_id > 0),
    ADD CONSTRAINT batch_command_dispatch_retry_run_check
        CHECK (retry_of_run_id IS NULL OR retry_of_run_id >= 0),
    ADD CONSTRAINT batch_command_dispatch_retry_task_fk
        FOREIGN KEY (retry_of_task_id)
        REFERENCES batch_command_dispatches(task_id) ON DELETE RESTRICT;

CREATE UNIQUE INDEX idx_batch_command_dispatches_retry_source
    ON batch_command_dispatches(retry_of_task_id, retry_of_run_id);

CREATE TABLE batch_command_run_evidences (
    run_id BIGINT NOT NULL PRIMARY KEY,
    task_id BIGINT NOT NULL,
    node_id BIGINT NOT NULL,
    command_fingerprint VARCHAR(64) NOT NULL DEFAULT '',
    outcome VARCHAR(16) NOT NULL DEFAULT 'pending',
    exit_code INTEGER,
    finished_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT batch_command_run_evidence_run_id_check
        CHECK (run_id > 0),
    CONSTRAINT batch_command_run_evidence_task_id_check
        CHECK (task_id > 0),
    CONSTRAINT batch_command_run_evidence_node_id_check
        CHECK (node_id > 0),
    CONSTRAINT batch_command_run_evidence_fingerprint_check
        CHECK (command_fingerprint = '' OR command_fingerprint ~ '^[0-9a-f]{64}$'),
    CONSTRAINT batch_command_run_evidence_outcome_check
        CHECK (outcome IN ('pending', 'exit_failure', 'not_started', 'success', 'unknown')),
    CONSTRAINT batch_command_run_evidence_exit_code_check
        CHECK ((outcome = 'exit_failure' AND exit_code IS NOT NULL AND exit_code > 0)
               OR (outcome <> 'exit_failure' AND exit_code IS NULL)),
    CONSTRAINT batch_command_run_evidence_finished_at_check
        CHECK ((outcome = 'pending' AND finished_at IS NULL)
               OR (outcome <> 'pending' AND finished_at IS NOT NULL)),
    CONSTRAINT batch_command_run_evidence_known_fingerprint_check
        CHECK (outcome IN ('pending', 'unknown', 'success') OR command_fingerprint <> ''),
    CONSTRAINT batch_command_run_evidence_dispatch_fk
        FOREIGN KEY (task_id)
        REFERENCES batch_command_dispatches(task_id) ON DELETE RESTRICT
);
CREATE INDEX idx_batch_command_run_evidences_task_run
    ON batch_command_run_evidences(task_id, run_id);

CREATE OR REPLACE FUNCTION batch_command_retry_batch_guard()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    source_version INTEGER;
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF OLD.provenance_version = 1 THEN
            RAISE EXCEPTION 'batch command retry batch is permanent';
        END IF;
        RETURN OLD;
    END IF;

    IF NEW.provenance_version NOT IN (0, 1) THEN
        RAISE EXCEPTION 'batch command retry batch provenance version is invalid';
    END IF;
    IF NEW.provenance_version = 0 AND NEW.source_batch_id IS NOT NULL THEN
        RAISE EXCEPTION 'batch command retry historical batch cannot have a source';
    END IF;
    IF NEW.provenance_version = 1 AND NEW.source_batch_id IS NOT NULL THEN
        IF NEW.source_batch_id = NEW.id THEN
            RAISE EXCEPTION 'batch command retry batch cannot source itself';
        END IF;
        SELECT provenance_version INTO source_version
        FROM batch_commands
        WHERE id = NEW.source_batch_id;
        IF NOT FOUND OR source_version <> 1 THEN
            RAISE EXCEPTION 'batch command retry source batch is not v1';
        END IF;
    END IF;

    IF TG_OP = 'UPDATE' THEN
        IF OLD.provenance_version = 0 AND NEW.provenance_version = 1 THEN
            RAISE EXCEPTION 'historical batch cannot be promoted to v1';
        END IF;
        IF OLD.provenance_version = 1 AND (
            NEW.id IS DISTINCT FROM OLD.id
            OR NEW.requester_id IS DISTINCT FROM OLD.requester_id
            OR NEW.idempotency_key IS DISTINCT FROM OLD.idempotency_key
            OR NEW.request_hash IS DISTINCT FROM OLD.request_hash
            OR NEW.provenance_version IS DISTINCT FROM OLD.provenance_version
            OR NEW.source_batch_id IS DISTINCT FROM OLD.source_batch_id
            OR NEW.retain IS DISTINCT FROM OLD.retain
        ) THEN
            RAISE EXCEPTION 'batch command retry batch identity is immutable';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_batch_command_retry_batch_insert
BEFORE INSERT ON batch_commands
FOR EACH ROW EXECUTE FUNCTION batch_command_retry_batch_guard();
CREATE TRIGGER trg_batch_command_retry_batch_update
BEFORE UPDATE ON batch_commands
FOR EACH ROW EXECUTE FUNCTION batch_command_retry_batch_guard();
CREATE TRIGGER trg_batch_command_retry_batch_delete
BEFORE DELETE ON batch_commands
FOR EACH ROW EXECUTE FUNCTION batch_command_retry_batch_guard();

CREATE OR REPLACE FUNCTION batch_command_retry_dispatch_guard()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    batch_version INTEGER;
    source_batch_ref VARCHAR(64);
BEGIN
    IF TG_OP = 'DELETE' THEN
        SELECT provenance_version INTO batch_version
        FROM batch_commands WHERE id = OLD.batch_id;
        IF batch_version = 1 THEN
            RAISE EXCEPTION 'batch command retry dispatch is permanent';
        END IF;
        RETURN OLD;
    END IF;

    SELECT batch.provenance_version, batch.source_batch_id
      INTO batch_version, source_batch_ref
    FROM batch_commands AS batch
    WHERE batch.id = NEW.batch_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'batch command retry dispatch batch is missing';
    END IF;
    IF batch_version = 0 AND (NEW.retry_of_task_id IS NOT NULL OR NEW.retry_of_run_id IS NOT NULL) THEN
        RAISE EXCEPTION 'historical dispatch cannot have a retry source';
    END IF;
    IF batch_version = 1 THEN
        IF NEW.command_fingerprint !~ '^[0-9a-f]{64}$' THEN
            RAISE EXCEPTION 'batch command retry dispatch fingerprint is invalid';
        END IF;
        IF source_batch_ref IS NULL THEN
            IF NEW.retry_of_task_id IS NOT NULL OR NEW.retry_of_run_id IS NOT NULL THEN
                RAISE EXCEPTION 'root dispatch cannot have a retry source';
            END IF;
        ELSE
            IF NEW.retry_of_task_id IS NULL OR NEW.retry_of_run_id IS NULL
               OR NEW.retry_of_task_id <= 0 OR NEW.retry_of_run_id < 0
               OR NEW.retry_of_task_id = NEW.task_id
               OR NOT EXISTS (
                   SELECT 1
                   FROM batch_command_dispatches AS source_dispatch
                   JOIN batch_commands AS source_batch
                     ON source_batch.id = source_dispatch.batch_id
                   WHERE source_dispatch.task_id = NEW.retry_of_task_id
                     AND source_dispatch.batch_id = source_batch_ref
                     AND source_dispatch.node_id = NEW.node_id
                     AND source_batch.provenance_version = 1
               ) THEN
                RAISE EXCEPTION 'child dispatch retry source is invalid';
            END IF;
        END IF;
    END IF;

    IF TG_OP = 'UPDATE' THEN
        SELECT provenance_version INTO batch_version
        FROM batch_commands WHERE id = OLD.batch_id;
        IF batch_version = 1 AND (
            NEW.task_id IS DISTINCT FROM OLD.task_id
            OR NEW.batch_id IS DISTINCT FROM OLD.batch_id
            OR NEW.node_id IS DISTINCT FROM OLD.node_id
            OR NEW.command_fingerprint IS DISTINCT FROM OLD.command_fingerprint
            OR NEW.retry_of_task_id IS DISTINCT FROM OLD.retry_of_task_id
            OR NEW.retry_of_run_id IS DISTINCT FROM OLD.retry_of_run_id
        ) THEN
            RAISE EXCEPTION 'batch command retry dispatch identity is immutable';
        END IF;
        IF batch_version = 0 AND EXISTS (
            SELECT 1 FROM batch_commands
            WHERE id = NEW.batch_id AND provenance_version = 1
        ) THEN
            RAISE EXCEPTION 'historical dispatch cannot be promoted to v1';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_batch_command_retry_dispatch_insert
BEFORE INSERT ON batch_command_dispatches
FOR EACH ROW EXECUTE FUNCTION batch_command_retry_dispatch_guard();
CREATE TRIGGER trg_batch_command_retry_dispatch_update
BEFORE UPDATE ON batch_command_dispatches
FOR EACH ROW EXECUTE FUNCTION batch_command_retry_dispatch_guard();
CREATE TRIGGER trg_batch_command_retry_dispatch_delete
BEFORE DELETE ON batch_command_dispatches
FOR EACH ROW EXECUTE FUNCTION batch_command_retry_dispatch_guard();

CREATE OR REPLACE FUNCTION batch_command_retry_evidence_guard()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'batch command retry evidence is permanent';
    END IF;

    IF TG_OP = 'INSERT' THEN
        IF NEW.outcome <> 'pending'
           OR NEW.exit_code IS NOT NULL
           OR NEW.finished_at IS NOT NULL
           OR NOT (NEW.command_fingerprint = ''
                   OR NEW.command_fingerprint ~ '^[0-9a-f]{64}$')
           OR NOT EXISTS (
               SELECT 1
               FROM batch_command_dispatches AS dispatch
               JOIN batch_commands AS batch ON batch.id = dispatch.batch_id
               JOIN tasks AS task
                 ON task.id = NEW.task_id
                AND task.batch_id = dispatch.batch_id
               WHERE dispatch.task_id = NEW.task_id
                 AND batch.provenance_version = 1
                 AND (batch.source_batch_id IS NULL
                      OR (dispatch.node_id = NEW.node_id
                          AND dispatch.command_fingerprint = NEW.command_fingerprint))
           )
           OR NOT EXISTS (
               SELECT 1
               FROM task_runs AS run
               WHERE run.id = NEW.run_id
                 AND run.task_id = NEW.task_id
                 AND run.node_id_snapshot = NEW.node_id
                 AND run.status IN ('pending', 'running', 'retrying')
                 AND run.finished_at IS NULL
           ) THEN
            RAISE EXCEPTION 'batch command retry evidence identity is invalid';
        END IF;
        RETURN NEW;
    END IF;

    IF OLD.outcome <> 'pending'
       OR NEW.outcome = 'pending'
       OR NEW.run_id IS DISTINCT FROM OLD.run_id
       OR NEW.task_id IS DISTINCT FROM OLD.task_id
       OR NEW.node_id IS DISTINCT FROM OLD.node_id
       OR NEW.command_fingerprint IS DISTINCT FROM OLD.command_fingerprint
       OR NEW.created_at IS DISTINCT FROM OLD.created_at
       OR NOT (NEW.command_fingerprint = ''
               OR NEW.command_fingerprint ~ '^[0-9a-f]{64}$')
       OR NOT EXISTS (
           SELECT 1
           FROM batch_command_dispatches AS dispatch
           JOIN batch_commands AS batch ON batch.id = dispatch.batch_id
           JOIN tasks AS task
             ON task.id = NEW.task_id
            AND task.batch_id = dispatch.batch_id
           WHERE dispatch.task_id = NEW.task_id
             AND batch.provenance_version = 1
             AND (batch.source_batch_id IS NULL
                  OR (dispatch.node_id = NEW.node_id
                      AND dispatch.command_fingerprint = NEW.command_fingerprint))
       )
       OR NOT EXISTS (
           SELECT 1
           FROM task_runs AS run
           WHERE run.id = NEW.run_id
             AND run.task_id = NEW.task_id
             AND run.node_id_snapshot = NEW.node_id
             AND run.finished_at IS NOT NULL
             AND run.status IN ('success', 'failed', 'canceled', 'warning', 'skipped')
       )
       OR (NEW.outcome IN ('exit_failure', 'not_started') AND NOT EXISTS (
           SELECT 1 FROM task_runs AS run
           WHERE run.id = NEW.run_id AND run.status = 'failed' AND run.finished_at IS NOT NULL
       ))
       OR (NEW.outcome = 'success' AND NOT EXISTS (
           SELECT 1 FROM task_runs AS run
           WHERE run.id = NEW.run_id AND run.status = 'success' AND run.finished_at IS NOT NULL
       )) THEN
        RAISE EXCEPTION 'batch command retry evidence transition is invalid';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_batch_command_retry_evidence_insert
BEFORE INSERT ON batch_command_run_evidences
FOR EACH ROW EXECUTE FUNCTION batch_command_retry_evidence_guard();
CREATE TRIGGER trg_batch_command_retry_evidence_update
BEFORE UPDATE ON batch_command_run_evidences
FOR EACH ROW EXECUTE FUNCTION batch_command_retry_evidence_guard();
CREATE TRIGGER trg_batch_command_retry_evidence_delete
BEFORE DELETE ON batch_command_run_evidences
FOR EACH ROW EXECUTE FUNCTION batch_command_retry_evidence_guard();

CREATE OR REPLACE FUNCTION batch_command_retry_downgrade_admission()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.version < 93 AND (
        EXISTS (SELECT 1 FROM batch_commands WHERE provenance_version = 1)
        OR EXISTS (SELECT 1 FROM batch_command_run_evidences)
    ) THEN
        RAISE EXCEPTION '000093 downgrade blocked: batch command retry provenance or evidence is permanent';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_batch_command_retry_downgrade_admission
BEFORE INSERT OR UPDATE ON schema_migrations
FOR EACH ROW EXECUTE FUNCTION batch_command_retry_downgrade_admission();

COMMIT;
