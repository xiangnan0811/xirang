-- 000004_verify_fields.down.sql (PostgreSQL)
ALTER TABLE tasks DROP COLUMN IF EXISTS verify_status;
ALTER TABLE policies DROP COLUMN IF EXISTS verify_enabled;
ALTER TABLE policies DROP COLUMN IF EXISTS verify_sample_rate;
