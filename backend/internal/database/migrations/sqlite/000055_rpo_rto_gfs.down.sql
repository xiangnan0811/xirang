-- 000055_rpo_rto_gfs.down.sql

ALTER TABLE reports DROP COLUMN rto_compliant;
ALTER TABLE reports DROP COLUMN rpo_compliant;
ALTER TABLE reports DROP COLUMN actual_rto_minutes;
ALTER TABLE reports DROP COLUMN actual_rpo_minutes;

ALTER TABLE policies DROP COLUMN keep_yearly;
ALTER TABLE policies DROP COLUMN keep_monthly;
ALTER TABLE policies DROP COLUMN keep_weekly;
ALTER TABLE policies DROP COLUMN keep_daily;
ALTER TABLE policies DROP COLUMN retention_mode;
ALTER TABLE policies DROP COLUMN rto_minutes;
ALTER TABLE policies DROP COLUMN rpo_minutes;
