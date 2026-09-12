CREATE TEMP TABLE backup_completions_000087_downgrade_guard (
    valid INTEGER NOT NULL CHECK (valid = 1)
);
INSERT INTO backup_completions_000087_downgrade_guard(valid)
SELECT CASE WHEN EXISTS (SELECT 1 FROM backup_completions) THEN 0 ELSE 1 END;
DROP TABLE backup_completions_000087_downgrade_guard;

DROP TRIGGER IF EXISTS trg_backup_completions_downgrade_admission;
DROP TRIGGER IF EXISTS trg_backup_repositories_provider_kind_immutable;
DROP TRIGGER IF EXISTS trg_backup_completions_immutable_update;
DROP TRIGGER IF EXISTS trg_backup_completions_immutable_delete;
DROP INDEX IF EXISTS idx_backup_completions_verified_task;
DROP INDEX IF EXISTS idx_backup_completions_node_completed;
DROP INDEX IF EXISTS idx_backup_completions_task_run;
DROP TABLE backup_completions;
