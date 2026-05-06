BEGIN;

-- 000055_rpo_rto_gfs.down.sql

ALTER TABLE reports DROP COLUMN IF EXISTS rto_compliant;
ALTER TABLE reports DROP COLUMN IF EXISTS rpo_compliant;
ALTER TABLE reports DROP COLUMN IF EXISTS actual_rto_minutes;
ALTER TABLE reports DROP COLUMN IF EXISTS actual_rpo_minutes;

ALTER TABLE policies DROP COLUMN IF EXISTS keep_yearly;
ALTER TABLE policies DROP COLUMN IF EXISTS keep_monthly;
ALTER TABLE policies DROP COLUMN IF EXISTS keep_weekly;
ALTER TABLE policies DROP COLUMN IF EXISTS keep_daily;
ALTER TABLE policies DROP COLUMN IF EXISTS retention_mode;
ALTER TABLE policies DROP COLUMN IF EXISTS rto_minutes;
ALTER TABLE policies DROP COLUMN IF EXISTS rpo_minutes;

COMMIT;
