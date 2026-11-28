-- Older writers must be drained before upgrading to this schema.
ALTER TABLE nodes ADD COLUMN config_revision TEXT NOT NULL DEFAULT '';
ALTER TABLE node_probe_state ADD COLUMN applied_status TEXT NOT NULL DEFAULT '';
UPDATE nodes SET config_revision = lower(hex(randomblob(16)));

CREATE TRIGGER trg_nodes_config_revision_insert AFTER INSERT ON nodes
BEGIN
    UPDATE nodes SET config_revision = lower(hex(randomblob(16))) WHERE id = NEW.id;
END;
CREATE TRIGGER trg_nodes_config_revision_update AFTER UPDATE OF name, host, port, username, auth_type, ssh_key_id, password, private_key, tags, base_path, backup_dir, use_sudo, maintenance_start, maintenance_end, expiry_date, archived, escalation_policy_id ON nodes
WHEN OLD.name IS NOT NEW.name OR OLD.host IS NOT NEW.host OR OLD.port IS NOT NEW.port
  OR OLD.username IS NOT NEW.username OR OLD.auth_type IS NOT NEW.auth_type
  OR OLD.ssh_key_id IS NOT NEW.ssh_key_id OR OLD.password IS NOT NEW.password
  OR OLD.private_key IS NOT NEW.private_key OR OLD.tags IS NOT NEW.tags
  OR OLD.base_path IS NOT NEW.base_path OR OLD.backup_dir IS NOT NEW.backup_dir
  OR OLD.use_sudo IS NOT NEW.use_sudo OR OLD.maintenance_start IS NOT NEW.maintenance_start
  OR OLD.maintenance_end IS NOT NEW.maintenance_end OR OLD.expiry_date IS NOT NEW.expiry_date
  OR OLD.archived IS NOT NEW.archived OR OLD.escalation_policy_id IS NOT NEW.escalation_policy_id
BEGIN
    UPDATE nodes SET config_revision = lower(hex(randomblob(16))) WHERE id = NEW.id;
END;
