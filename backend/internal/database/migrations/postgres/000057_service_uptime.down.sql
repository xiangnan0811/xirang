BEGIN;

-- 000057_service_uptime.down.sql

DROP TABLE IF EXISTS service_uptime_samples;
DROP TABLE IF EXISTS service_monitors;

COMMIT;
