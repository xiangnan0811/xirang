-- 000055_rpo_rto_gfs.up.sql
-- Policy: RPO/RTO 目标 + GFS 多级保留
-- Report: RPO/RTO 实际值 + 达标标记

ALTER TABLE policies ADD COLUMN rpo_minutes     INTEGER NOT NULL DEFAULT 0;
ALTER TABLE policies ADD COLUMN rto_minutes     INTEGER NOT NULL DEFAULT 0;
ALTER TABLE policies ADD COLUMN retention_mode  TEXT    NOT NULL DEFAULT 'simple';
ALTER TABLE policies ADD COLUMN keep_daily      INTEGER NOT NULL DEFAULT 0;
ALTER TABLE policies ADD COLUMN keep_weekly     INTEGER NOT NULL DEFAULT 0;
ALTER TABLE policies ADD COLUMN keep_monthly    INTEGER NOT NULL DEFAULT 0;
ALTER TABLE policies ADD COLUMN keep_yearly     INTEGER NOT NULL DEFAULT 0;

ALTER TABLE reports ADD COLUMN actual_rpo_minutes INTEGER;
ALTER TABLE reports ADD COLUMN actual_rto_minutes INTEGER;
ALTER TABLE reports ADD COLUMN rpo_compliant       INTEGER;
ALTER TABLE reports ADD COLUMN rto_compliant       INTEGER;
