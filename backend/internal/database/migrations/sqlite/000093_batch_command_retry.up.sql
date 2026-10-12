-- 000093 persists immutable batch-command provenance and terminal run evidence.
-- Existing version-0 rows remain historical: they receive only neutral defaults and
-- are never promoted into the v1 retry contract by an UPDATE.
ALTER TABLE batch_commands
    ADD COLUMN provenance_version INTEGER NOT NULL DEFAULT 0
        CHECK (provenance_version IN (0, 1));
ALTER TABLE batch_commands
    ADD COLUMN source_batch_id VARCHAR(64)
        REFERENCES batch_commands(id) ON DELETE RESTRICT;

ALTER TABLE batch_command_dispatches
    ADD COLUMN command_fingerprint VARCHAR(64) NOT NULL DEFAULT ''
        CHECK (command_fingerprint = '' OR
               (length(command_fingerprint) = 64
                AND command_fingerprint NOT GLOB '*[^0-9a-f]*'));
ALTER TABLE batch_command_dispatches
    ADD COLUMN retry_of_task_id INTEGER
        CHECK (retry_of_task_id IS NULL OR retry_of_task_id > 0)
        REFERENCES batch_command_dispatches(task_id) ON DELETE RESTRICT;
ALTER TABLE batch_command_dispatches
    ADD COLUMN retry_of_run_id INTEGER
        CHECK (((retry_of_task_id IS NULL AND retry_of_run_id IS NULL)
                OR (retry_of_task_id IS NOT NULL AND retry_of_run_id IS NOT NULL))
               AND (retry_of_run_id IS NULL OR retry_of_run_id >= 0));

CREATE UNIQUE INDEX idx_batch_command_dispatches_retry_source
    ON batch_command_dispatches(retry_of_task_id, retry_of_run_id);

CREATE TABLE batch_command_run_evidences (
    run_id INTEGER NOT NULL PRIMARY KEY
        CHECK (run_id > 0),
    task_id INTEGER NOT NULL
        CHECK (task_id > 0)
        REFERENCES batch_command_dispatches(task_id) ON DELETE RESTRICT,
    node_id INTEGER NOT NULL
        CHECK (node_id > 0),
    command_fingerprint VARCHAR(64) NOT NULL DEFAULT ''
        CHECK (command_fingerprint = '' OR
               (length(command_fingerprint) = 64
                AND command_fingerprint NOT GLOB '*[^0-9a-f]*')),
    outcome VARCHAR(16) NOT NULL DEFAULT 'pending'
        CHECK (outcome IN ('pending', 'exit_failure', 'not_started', 'success', 'unknown')),
    exit_code INTEGER,
    finished_at DATETIME,
    created_at DATETIME NOT NULL,
    CHECK ((outcome = 'exit_failure' AND exit_code IS NOT NULL AND exit_code > 0)
           OR (outcome <> 'exit_failure' AND exit_code IS NULL)),
    CHECK ((outcome = 'pending' AND finished_at IS NULL)
           OR (outcome <> 'pending' AND finished_at IS NOT NULL)),
    CHECK (outcome IN ('pending', 'unknown', 'success') OR command_fingerprint <> '')
);
CREATE INDEX idx_batch_command_run_evidences_task_run
    ON batch_command_run_evidences(task_id, run_id);

-- v1 batch identity is fixed once written. A child batch can only point to an
-- existing v1 batch and can never point to itself.
CREATE TRIGGER trg_batch_command_retry_batch_insert
BEFORE INSERT ON batch_commands
WHEN NEW.provenance_version NOT IN (0, 1)
  OR (NEW.provenance_version = 0 AND NEW.source_batch_id IS NOT NULL)
  OR (NEW.provenance_version = 1
      AND NEW.source_batch_id IS NOT NULL
      AND (NEW.source_batch_id = NEW.id
           OR NOT EXISTS (
               SELECT 1 FROM batch_commands AS source_batch
               WHERE source_batch.id = NEW.source_batch_id
                 AND source_batch.provenance_version = 1
           )))
BEGIN
    SELECT RAISE(ABORT, 'batch command retry batch provenance is invalid');
END;

CREATE TRIGGER trg_batch_command_retry_batch_update
BEFORE UPDATE ON batch_commands
WHEN NEW.provenance_version NOT IN (0, 1)
  OR (NEW.provenance_version = 0 AND NEW.source_batch_id IS NOT NULL)
  OR (NEW.provenance_version = 1
      AND NEW.source_batch_id IS NOT NULL
      AND (NEW.source_batch_id = NEW.id
           OR NOT EXISTS (
               SELECT 1 FROM batch_commands AS source_batch
               WHERE source_batch.id = NEW.source_batch_id
                 AND source_batch.provenance_version = 1
           )))
  OR (OLD.provenance_version = 0 AND NEW.provenance_version = 1)
  OR (OLD.provenance_version = 1
      AND (NEW.id IS NOT OLD.id
           OR NEW.requester_id IS NOT OLD.requester_id
           OR NEW.idempotency_key IS NOT OLD.idempotency_key
           OR NEW.request_hash IS NOT OLD.request_hash
           OR NEW.provenance_version IS NOT OLD.provenance_version
           OR NEW.source_batch_id IS NOT OLD.source_batch_id
           OR NEW.retain IS NOT OLD.retain))
BEGIN
    SELECT RAISE(ABORT, 'batch command retry batch identity is immutable or invalid');
END;

CREATE TRIGGER trg_batch_command_retry_batch_delete
BEFORE DELETE ON batch_commands
WHEN OLD.provenance_version = 1
BEGIN
    SELECT RAISE(ABORT, 'batch command retry batch is permanent');
END;

-- A v1 dispatch is either a root receipt (no source pair) or a child receipt
-- whose source dispatch belongs to the immutable source batch and node.
CREATE TRIGGER trg_batch_command_retry_dispatch_insert
BEFORE INSERT ON batch_command_dispatches
WHEN NOT EXISTS (
         SELECT 1 FROM batch_commands AS batch WHERE batch.id = NEW.batch_id
     )
  OR EXISTS (
         SELECT 1 FROM batch_commands AS batch
         WHERE batch.id = NEW.batch_id
           AND batch.provenance_version = 0
           AND (NEW.retry_of_task_id IS NOT NULL OR NEW.retry_of_run_id IS NOT NULL)
     )
  OR EXISTS (
         SELECT 1 FROM batch_commands AS batch
         WHERE batch.id = NEW.batch_id
           AND batch.provenance_version = 1
           AND (
               NOT (length(NEW.command_fingerprint) = 64
                    AND NEW.command_fingerprint NOT GLOB '*[^0-9a-f]*')
               OR (batch.source_batch_id IS NULL
                   AND (NEW.retry_of_task_id IS NOT NULL OR NEW.retry_of_run_id IS NOT NULL))
               OR (batch.source_batch_id IS NOT NULL
                   AND (NEW.retry_of_task_id IS NULL OR NEW.retry_of_run_id IS NULL
                        OR NEW.retry_of_task_id <= 0
                        OR NEW.retry_of_run_id < 0
                        OR NEW.retry_of_task_id = NEW.task_id
                        OR NOT EXISTS (
                            SELECT 1
                            FROM batch_command_dispatches AS source_dispatch
                            JOIN batch_commands AS source_batch
                              ON source_batch.id = source_dispatch.batch_id
                            WHERE source_dispatch.task_id = NEW.retry_of_task_id
                              AND source_dispatch.batch_id = batch.source_batch_id
                              AND source_dispatch.node_id = NEW.node_id
                              AND source_batch.provenance_version = 1
                        )))
           )
     )
BEGIN
    SELECT RAISE(ABORT, 'batch command retry dispatch provenance is invalid');
END;

CREATE TRIGGER trg_batch_command_retry_dispatch_update
BEFORE UPDATE ON batch_command_dispatches
WHEN NOT EXISTS (
         SELECT 1 FROM batch_commands AS batch WHERE batch.id = NEW.batch_id
     )
  OR EXISTS (
         SELECT 1 FROM batch_commands AS batch
         WHERE batch.id = NEW.batch_id
           AND batch.provenance_version = 0
           AND (NEW.retry_of_task_id IS NOT NULL OR NEW.retry_of_run_id IS NOT NULL)
     )
  OR EXISTS (
         SELECT 1 FROM batch_commands AS batch
         WHERE batch.id = NEW.batch_id
           AND batch.provenance_version = 1
           AND (
               NOT (length(NEW.command_fingerprint) = 64
                    AND NEW.command_fingerprint NOT GLOB '*[^0-9a-f]*')
               OR (batch.source_batch_id IS NULL
                   AND (NEW.retry_of_task_id IS NOT NULL OR NEW.retry_of_run_id IS NOT NULL))
               OR (batch.source_batch_id IS NOT NULL
                   AND (NEW.retry_of_task_id IS NULL OR NEW.retry_of_run_id IS NULL
                        OR NEW.retry_of_task_id <= 0
                        OR NEW.retry_of_run_id < 0
                        OR NEW.retry_of_task_id = NEW.task_id
                        OR NOT EXISTS (
                            SELECT 1
                            FROM batch_command_dispatches AS source_dispatch
                            JOIN batch_commands AS source_batch
                              ON source_batch.id = source_dispatch.batch_id
                            WHERE source_dispatch.task_id = NEW.retry_of_task_id
                              AND source_dispatch.batch_id = batch.source_batch_id
                              AND source_dispatch.node_id = NEW.node_id
                              AND source_batch.provenance_version = 1
                        )))
           )
     )
  OR (EXISTS (
          SELECT 1 FROM batch_commands AS old_batch
          WHERE old_batch.id = OLD.batch_id
            AND old_batch.provenance_version = 1
      ) AND (
          NEW.task_id IS NOT OLD.task_id
          OR NEW.batch_id IS NOT OLD.batch_id
          OR NEW.node_id IS NOT OLD.node_id
          OR NEW.command_fingerprint IS NOT OLD.command_fingerprint
          OR NEW.retry_of_task_id IS NOT OLD.retry_of_task_id
          OR NEW.retry_of_run_id IS NOT OLD.retry_of_run_id
      ))
  OR (EXISTS (
          SELECT 1 FROM batch_commands AS old_batch
          WHERE old_batch.id = OLD.batch_id
            AND old_batch.provenance_version = 0
      ) AND EXISTS (
          SELECT 1 FROM batch_commands AS new_batch
          WHERE new_batch.id = NEW.batch_id
            AND new_batch.provenance_version = 1
      ))
BEGIN
    SELECT RAISE(ABORT, 'batch command retry dispatch identity is immutable or invalid');
END;

CREATE TRIGGER trg_batch_command_retry_dispatch_delete
BEFORE DELETE ON batch_command_dispatches
WHEN EXISTS (
    SELECT 1 FROM batch_commands AS batch
    WHERE batch.id = OLD.batch_id AND batch.provenance_version = 1
)
BEGIN
    SELECT RAISE(ABORT, 'batch command retry dispatch is permanent');
END;

-- Evidence is created pending only for an existing v1 dispatch and the actual
-- TaskRun. A pending row may transition exactly once to a terminal outcome;
-- terminal rows and all identity fields are permanent.
CREATE TRIGGER trg_batch_command_retry_evidence_insert
BEFORE INSERT ON batch_command_run_evidences
WHEN NEW.outcome <> 'pending'
  OR NEW.exit_code IS NOT NULL
  OR NEW.finished_at IS NOT NULL
  OR NOT (NEW.command_fingerprint = ''
          OR (length(NEW.command_fingerprint) = 64
              AND NEW.command_fingerprint NOT GLOB '*[^0-9a-f]*'))
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
  )
BEGIN
    SELECT RAISE(ABORT, 'batch command retry evidence identity is invalid');
END;

CREATE TRIGGER trg_batch_command_retry_evidence_update
BEFORE UPDATE ON batch_command_run_evidences
WHEN OLD.outcome <> 'pending'
  OR NEW.outcome = 'pending'
  OR NEW.run_id IS NOT OLD.run_id
  OR NEW.task_id IS NOT OLD.task_id
  OR NEW.node_id IS NOT OLD.node_id
  OR NEW.command_fingerprint IS NOT OLD.command_fingerprint
  OR NEW.created_at IS NOT OLD.created_at
  OR NOT (NEW.command_fingerprint = ''
          OR (length(NEW.command_fingerprint) = 64
              AND NEW.command_fingerprint NOT GLOB '*[^0-9a-f]*'))
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
  OR (NEW.outcome IN ('exit_failure', 'not_started')
      AND NOT EXISTS (
          SELECT 1 FROM task_runs AS run
          WHERE run.id = NEW.run_id
            AND run.status = 'failed'
            AND run.finished_at IS NOT NULL
      ))
  OR (NEW.outcome = 'success'
      AND NOT EXISTS (
          SELECT 1 FROM task_runs AS run
          WHERE run.id = NEW.run_id
            AND run.status = 'success'
            AND run.finished_at IS NOT NULL
      ))
BEGIN
    SELECT RAISE(ABORT, 'batch command retry evidence transition is invalid');
END;

CREATE TRIGGER trg_batch_command_retry_evidence_delete
BEFORE DELETE ON batch_command_run_evidences
BEGIN
    SELECT RAISE(ABORT, 'batch command retry evidence is permanent');
END;

-- golang-migrate writes the target version before running a down migration.
-- Preserve the used-state proof at that metadata boundary as well as in the
-- down body below. The UPDATE trigger covers SQLite driver metadata rewrites.
CREATE TRIGGER trg_batch_command_retry_downgrade_admission
BEFORE INSERT ON schema_migrations
WHEN NEW.version < 93
 AND (
    EXISTS (SELECT 1 FROM batch_commands WHERE provenance_version = 1)
    OR EXISTS (SELECT 1 FROM batch_command_run_evidences)
 )
BEGIN
    SELECT RAISE(ABORT, '000093 downgrade blocked: batch command retry provenance or evidence is permanent');
END;

CREATE TRIGGER trg_batch_command_retry_downgrade_update_admission
BEFORE UPDATE ON schema_migrations
WHEN NEW.version < 93
 AND (
    EXISTS (SELECT 1 FROM batch_commands WHERE provenance_version = 1)
    OR EXISTS (SELECT 1 FROM batch_command_run_evidences)
 )
BEGIN
    SELECT RAISE(ABORT, '000093 downgrade blocked: batch command retry provenance or evidence is permanent');
END;
