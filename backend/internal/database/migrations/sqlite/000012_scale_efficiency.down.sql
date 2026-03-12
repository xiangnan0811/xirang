-- 000012_scale_efficiency.down.sql
ALTER TABLE policies DROP COLUMN is_template;
ALTER TABLE nodes DROP COLUMN maintenance_start;
ALTER TABLE nodes DROP COLUMN maintenance_end;
