-- 000012_scale_efficiency.up.sql
-- F15: Scale Efficiency - 策略模板 + 节点维护窗口

ALTER TABLE policies ADD COLUMN is_template BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE nodes ADD COLUMN maintenance_start TIMESTAMPTZ;
ALTER TABLE nodes ADD COLUMN maintenance_end TIMESTAMPTZ;
