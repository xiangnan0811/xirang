BEGIN;

-- 000052_drill_config.up.sql
-- policies 表加恢复演练（drill）配置字段

ALTER TABLE policies ADD COLUMN IF NOT EXISTS drill_enabled        BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE policies ADD COLUMN IF NOT EXISTS drill_cron           TEXT    NOT NULL DEFAULT '';
ALTER TABLE policies ADD COLUMN IF NOT EXISTS drill_target_node_id INTEGER;
ALTER TABLE policies ADD COLUMN IF NOT EXISTS drill_restore_path   TEXT    NOT NULL DEFAULT '/tmp/xirang-drill';
ALTER TABLE policies ADD COLUMN IF NOT EXISTS drill_pre_verify     TEXT    NOT NULL DEFAULT '';
ALTER TABLE policies ADD COLUMN IF NOT EXISTS drill_verify         TEXT    NOT NULL DEFAULT '';
ALTER TABLE policies ADD COLUMN IF NOT EXISTS drill_post_verify    TEXT    NOT NULL DEFAULT '';
ALTER TABLE policies ADD COLUMN IF NOT EXISTS drill_auto_cleanup   BOOLEAN NOT NULL DEFAULT true;
CREATE INDEX IF NOT EXISTS idx_policies_drill_target_node_id ON policies (drill_target_node_id);

COMMIT;
