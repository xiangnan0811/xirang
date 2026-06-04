BEGIN;

-- 000058_restore_drill_evidence.down.sql

DROP INDEX IF EXISTS idx_restore_drill_evidences_source_task_run;
DROP INDEX IF EXISTS idx_restore_drill_evidences_policy_created;
DROP INDEX IF EXISTS idx_restore_drill_evidences_status;
DROP INDEX IF EXISTS idx_restore_drill_evidences_sandbox_node;
DROP INDEX IF EXISTS idx_restore_drill_evidences_task;
DROP INDEX IF EXISTS idx_restore_drill_evidences_policy;
DROP INDEX IF EXISTS idx_restore_drill_evidences_task_run;
DROP TABLE IF EXISTS restore_drill_evidences;

COMMIT;
