-- 000052_drill_config.up.sql
-- policies 表加恢复演练（drill）配置字段

ALTER TABLE policies ADD COLUMN drill_enabled        INTEGER NOT NULL DEFAULT 0;
ALTER TABLE policies ADD COLUMN drill_cron           TEXT    NOT NULL DEFAULT '';
ALTER TABLE policies ADD COLUMN drill_target_node_id INTEGER;
ALTER TABLE policies ADD COLUMN drill_restore_path   TEXT    NOT NULL DEFAULT '/tmp/xirang-drill';
ALTER TABLE policies ADD COLUMN drill_pre_verify     TEXT    NOT NULL DEFAULT '';
ALTER TABLE policies ADD COLUMN drill_verify         TEXT    NOT NULL DEFAULT '';
ALTER TABLE policies ADD COLUMN drill_post_verify    TEXT    NOT NULL DEFAULT '';
ALTER TABLE policies ADD COLUMN drill_auto_cleanup   INTEGER NOT NULL DEFAULT 1;
CREATE INDEX IF NOT EXISTS idx_policies_drill_target_node_id ON policies (drill_target_node_id);
