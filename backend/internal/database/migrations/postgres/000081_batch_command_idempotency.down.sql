BEGIN;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM batch_commands) THEN
        RAISE EXCEPTION '000081 downgrade blocked: batch idempotency records exist';
    END IF;
END;
$$;
DROP TRIGGER IF EXISTS trg_batch_command_downgrade_admission ON schema_migrations;
DROP FUNCTION batch_command_downgrade_admission();
DROP TABLE batch_command_dispatches;
DROP TABLE batch_commands;
COMMIT;
