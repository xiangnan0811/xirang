BEGIN;

-- The admission trigger normally rejects a used downgrade before the migrate
-- driver changes schema_migrations. Keep the same used-state test in the body
-- for direct/manual execution.
CREATE TEMP TABLE batch_command_retry_000093_down_guard (
    valid INTEGER NOT NULL CHECK (valid = 1)
);
INSERT INTO batch_command_retry_000093_down_guard(valid)
SELECT CASE WHEN EXISTS (
                    SELECT 1 FROM batch_commands WHERE provenance_version = 1
                )
                OR EXISTS (
                    SELECT 1 FROM batch_command_run_evidences
                )
            THEN 0 ELSE 1 END;
DROP TABLE batch_command_retry_000093_down_guard;

DROP TRIGGER IF EXISTS trg_batch_command_retry_evidence_delete ON batch_command_run_evidences;
DROP TRIGGER IF EXISTS trg_batch_command_retry_evidence_update ON batch_command_run_evidences;
DROP TRIGGER IF EXISTS trg_batch_command_retry_evidence_insert ON batch_command_run_evidences;
DROP FUNCTION IF EXISTS batch_command_retry_evidence_guard();
DROP TRIGGER IF EXISTS trg_batch_command_retry_dispatch_delete ON batch_command_dispatches;
DROP TRIGGER IF EXISTS trg_batch_command_retry_dispatch_update ON batch_command_dispatches;
DROP TRIGGER IF EXISTS trg_batch_command_retry_dispatch_insert ON batch_command_dispatches;
DROP FUNCTION IF EXISTS batch_command_retry_dispatch_guard();
DROP TRIGGER IF EXISTS trg_batch_command_retry_batch_delete ON batch_commands;
DROP TRIGGER IF EXISTS trg_batch_command_retry_batch_update ON batch_commands;
DROP TRIGGER IF EXISTS trg_batch_command_retry_batch_insert ON batch_commands;
DROP FUNCTION IF EXISTS batch_command_retry_batch_guard();
DROP TRIGGER IF EXISTS trg_batch_command_retry_downgrade_admission ON schema_migrations;
DROP FUNCTION IF EXISTS batch_command_retry_downgrade_admission();

DROP INDEX IF EXISTS idx_batch_command_run_evidences_task_run;
DROP TABLE batch_command_run_evidences;
DROP INDEX IF EXISTS idx_batch_command_dispatches_retry_source;
ALTER TABLE batch_command_dispatches
    DROP CONSTRAINT IF EXISTS batch_command_dispatch_retry_task_fk,
    DROP CONSTRAINT IF EXISTS batch_command_dispatch_retry_run_check,
    DROP CONSTRAINT IF EXISTS batch_command_dispatch_retry_task_check,
    DROP CONSTRAINT IF EXISTS batch_command_dispatch_retry_pair_check,
    DROP CONSTRAINT IF EXISTS batch_command_dispatch_command_fingerprint_check;
ALTER TABLE batch_command_dispatches
    DROP COLUMN retry_of_run_id,
    DROP COLUMN retry_of_task_id,
    DROP COLUMN command_fingerprint;
ALTER TABLE batch_commands
    DROP CONSTRAINT IF EXISTS batch_command_source_batch_fk,
    DROP CONSTRAINT IF EXISTS batch_command_provenance_version_check;
ALTER TABLE batch_commands
    DROP COLUMN source_batch_id,
    DROP COLUMN provenance_version;

COMMIT;
