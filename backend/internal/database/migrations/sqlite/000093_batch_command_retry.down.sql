-- The metadata admission trigger normally rejects a used downgrade before
-- golang-migrate changes schema_migrations. Keep an independent body guard for
-- direct/manual execution too.
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

DROP TRIGGER IF EXISTS trg_batch_command_retry_evidence_delete;
DROP TRIGGER IF EXISTS trg_batch_command_retry_evidence_update;
DROP TRIGGER IF EXISTS trg_batch_command_retry_evidence_insert;
DROP TRIGGER IF EXISTS trg_batch_command_retry_dispatch_delete;
DROP TRIGGER IF EXISTS trg_batch_command_retry_dispatch_update;
DROP TRIGGER IF EXISTS trg_batch_command_retry_dispatch_insert;
DROP TRIGGER IF EXISTS trg_batch_command_retry_batch_delete;
DROP TRIGGER IF EXISTS trg_batch_command_retry_batch_update;
DROP TRIGGER IF EXISTS trg_batch_command_retry_batch_insert;
DROP TRIGGER IF EXISTS trg_batch_command_retry_downgrade_update_admission;
DROP TRIGGER IF EXISTS trg_batch_command_retry_downgrade_admission;

DROP INDEX IF EXISTS idx_batch_command_run_evidences_task_run;
DROP TABLE batch_command_run_evidences;
DROP INDEX IF EXISTS idx_batch_command_dispatches_retry_source;
ALTER TABLE batch_command_dispatches DROP COLUMN retry_of_run_id;
ALTER TABLE batch_command_dispatches DROP COLUMN retry_of_task_id;
ALTER TABLE batch_command_dispatches DROP COLUMN command_fingerprint;
ALTER TABLE batch_commands DROP COLUMN source_batch_id;
ALTER TABLE batch_commands DROP COLUMN provenance_version;
