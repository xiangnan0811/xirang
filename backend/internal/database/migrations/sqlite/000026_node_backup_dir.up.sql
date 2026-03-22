ALTER TABLE nodes ADD COLUMN backup_dir VARCHAR(128) NOT NULL DEFAULT '';
UPDATE nodes SET backup_dir = name WHERE backup_dir = '';
CREATE UNIQUE INDEX idx_nodes_backup_dir ON nodes(backup_dir);
