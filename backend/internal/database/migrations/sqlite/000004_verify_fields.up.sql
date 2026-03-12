-- 000004_verify_fields.up.sql
-- F3: 备份校验字段

ALTER TABLE tasks ADD COLUMN verify_status VARCHAR(16) NOT NULL DEFAULT 'none';
ALTER TABLE policies ADD COLUMN verify_enabled BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE policies ADD COLUMN verify_sample_rate INTEGER NOT NULL DEFAULT 0;
