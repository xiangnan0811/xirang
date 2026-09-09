CREATE TEMP TABLE batch_command_down_guard (valid INTEGER NOT NULL CHECK(valid=1));
INSERT INTO batch_command_down_guard SELECT CASE WHEN EXISTS(SELECT 1 FROM batch_commands) THEN 0 ELSE 1 END;
DROP TABLE batch_command_down_guard;
DROP TRIGGER IF EXISTS trg_batch_command_downgrade_admission;
DROP TABLE batch_command_dispatches;
DROP TABLE batch_commands;
