DROP INDEX IF EXISTS idx_nodes_backup_dir;
ALTER TABLE nodes DROP COLUMN backup_dir;
