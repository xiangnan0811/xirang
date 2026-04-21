ALTER TABLE nodes DROP COLUMN IF EXISTS log_retention_days;
ALTER TABLE nodes DROP COLUMN IF EXISTS log_journalctl_enabled;
ALTER TABLE nodes DROP COLUMN IF EXISTS log_paths;
