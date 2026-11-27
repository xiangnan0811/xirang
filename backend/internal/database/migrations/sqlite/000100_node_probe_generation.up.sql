-- Older writers must be drained before upgrading to this schema.
ALTER TABLE nodes ADD COLUMN probe_generation TEXT NOT NULL DEFAULT '';
ALTER TABLE ssh_keys ADD COLUMN probe_generation TEXT NOT NULL DEFAULT '';
UPDATE nodes SET probe_generation = lower(hex(randomblob(16)));
UPDATE ssh_keys SET probe_generation = lower(hex(randomblob(16)));

CREATE TABLE node_probe_state (
    node_id INTEGER PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE,
    admitted_seq INTEGER NOT NULL DEFAULT 0,
    applied_seq INTEGER NOT NULL DEFAULT 0,
    applied_token TEXT NOT NULL DEFAULT ''
);

CREATE TRIGGER trg_nodes_probe_generation_insert AFTER INSERT ON nodes
BEGIN
    UPDATE nodes SET probe_generation = lower(hex(randomblob(16))) WHERE id = NEW.id;
END;
CREATE TRIGGER trg_nodes_probe_generation_update AFTER UPDATE OF host, port, username, auth_type, ssh_key_id, password, private_key, tags, archived ON nodes
WHEN OLD.host IS NOT NEW.host OR OLD.port IS NOT NEW.port OR OLD.username IS NOT NEW.username
  OR OLD.auth_type IS NOT NEW.auth_type OR OLD.ssh_key_id IS NOT NEW.ssh_key_id
  OR OLD.password IS NOT NEW.password OR OLD.private_key IS NOT NEW.private_key
  OR OLD.tags IS NOT NEW.tags OR OLD.archived IS NOT NEW.archived
BEGIN
    UPDATE nodes SET probe_generation = lower(hex(randomblob(16))) WHERE id = NEW.id;
END;
CREATE TRIGGER trg_ssh_keys_probe_generation_insert AFTER INSERT ON ssh_keys
BEGIN
    UPDATE ssh_keys SET probe_generation = lower(hex(randomblob(16))) WHERE id = NEW.id;
END;
CREATE TRIGGER trg_ssh_keys_probe_generation_update AFTER UPDATE OF private_key, disabled, expires_at, allowed_purposes, allowed_node_ids, allowed_node_tags ON ssh_keys
WHEN OLD.private_key IS NOT NEW.private_key OR OLD.disabled IS NOT NEW.disabled
  OR OLD.expires_at IS NOT NEW.expires_at OR OLD.allowed_purposes IS NOT NEW.allowed_purposes
  OR OLD.allowed_node_ids IS NOT NEW.allowed_node_ids OR OLD.allowed_node_tags IS NOT NEW.allowed_node_tags
BEGIN
    UPDATE ssh_keys SET probe_generation = lower(hex(randomblob(16))) WHERE id = NEW.id;
END;
