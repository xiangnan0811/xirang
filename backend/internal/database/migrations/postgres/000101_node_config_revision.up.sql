-- Older writers must be drained before upgrading to this schema.
ALTER TABLE nodes ADD COLUMN config_revision TEXT NOT NULL DEFAULT '';
ALTER TABLE node_probe_state ADD COLUMN applied_status TEXT NOT NULL DEFAULT '';
UPDATE nodes SET config_revision = replace(gen_random_uuid()::text, '-', '');

CREATE FUNCTION nodes_config_revision_set() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'INSERT' OR ROW(OLD.name, OLD.host, OLD.port, OLD.username, OLD.auth_type, OLD.ssh_key_id, OLD.password, OLD.private_key, OLD.tags, OLD.base_path, OLD.backup_dir, OLD.use_sudo, OLD.maintenance_start, OLD.maintenance_end, OLD.expiry_date, OLD.archived, OLD.escalation_policy_id)
        IS DISTINCT FROM ROW(NEW.name, NEW.host, NEW.port, NEW.username, NEW.auth_type, NEW.ssh_key_id, NEW.password, NEW.private_key, NEW.tags, NEW.base_path, NEW.backup_dir, NEW.use_sudo, NEW.maintenance_start, NEW.maintenance_end, NEW.expiry_date, NEW.archived, NEW.escalation_policy_id) THEN
        NEW.config_revision := replace(gen_random_uuid()::text, '-', '');
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_nodes_config_revision BEFORE INSERT OR UPDATE ON nodes
FOR EACH ROW EXECUTE FUNCTION nodes_config_revision_set();
