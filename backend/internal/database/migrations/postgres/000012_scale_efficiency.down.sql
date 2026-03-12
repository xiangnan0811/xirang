-- 000012_scale_efficiency.down.sql
ALTER TABLE policies DROP COLUMN IF EXISTS is_template;
ALTER TABLE nodes DROP COLUMN IF EXISTS maintenance_start;
ALTER TABLE nodes DROP COLUMN IF EXISTS maintenance_end;
