BEGIN;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM backup_completions) THEN
        RAISE EXCEPTION '000087 downgrade blocked: backup completion evidence exists';
    END IF;
END
$$;

DROP TRIGGER IF EXISTS trg_backup_completions_downgrade_admission ON schema_migrations;
DROP FUNCTION IF EXISTS backup_completions_downgrade_admission();
DROP TRIGGER IF EXISTS trg_backup_repositories_provider_kind_immutable ON backup_repositories;
DROP FUNCTION IF EXISTS backup_repositories_provider_kind_immutable_guard();
DROP TRIGGER IF EXISTS trg_backup_completions_immutable ON backup_completions;
DROP FUNCTION IF EXISTS backup_completions_immutable_guard();
DROP FUNCTION IF EXISTS backup_completion_lineage_provider(TEXT, BIGINT, BIGINT, TEXT);
DROP INDEX IF EXISTS idx_backup_completions_node_completed;
DROP INDEX IF EXISTS idx_backup_completions_task_run;
DROP TABLE backup_completions;

COMMIT;
