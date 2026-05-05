BEGIN;

-- 000052_drill_config.down.sql

DROP INDEX IF EXISTS idx_policies_drill_target_node_id;
ALTER TABLE policies DROP COLUMN IF EXISTS drill_auto_cleanup;
ALTER TABLE policies DROP COLUMN IF EXISTS drill_post_verify;
ALTER TABLE policies DROP COLUMN IF EXISTS drill_verify;
ALTER TABLE policies DROP COLUMN IF EXISTS drill_pre_verify;
ALTER TABLE policies DROP COLUMN IF EXISTS drill_restore_path;
ALTER TABLE policies DROP COLUMN IF EXISTS drill_target_node_id;
ALTER TABLE policies DROP COLUMN IF EXISTS drill_cron;
ALTER TABLE policies DROP COLUMN IF EXISTS drill_enabled;

COMMIT;
