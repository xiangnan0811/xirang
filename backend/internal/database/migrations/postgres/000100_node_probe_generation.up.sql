-- Older writers must be drained before upgrading to this schema.
ALTER TABLE nodes ADD COLUMN probe_generation TEXT NOT NULL DEFAULT '';
ALTER TABLE ssh_keys ADD COLUMN probe_generation TEXT NOT NULL DEFAULT '';
UPDATE nodes SET probe_generation = replace(gen_random_uuid()::text, '-', '');
UPDATE ssh_keys SET probe_generation = replace(gen_random_uuid()::text, '-', '');

CREATE TABLE node_probe_state (
    node_id BIGINT PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE,
    admitted_seq BIGINT NOT NULL DEFAULT 0,
    applied_seq BIGINT NOT NULL DEFAULT 0,
    applied_token TEXT NOT NULL DEFAULT ''
);

CREATE FUNCTION nodes_probe_generation_set() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'INSERT' OR ROW(OLD.host, OLD.port, OLD.username, OLD.auth_type, OLD.ssh_key_id, OLD.password, OLD.private_key, OLD.tags, OLD.archived)
        IS DISTINCT FROM ROW(NEW.host, NEW.port, NEW.username, NEW.auth_type, NEW.ssh_key_id, NEW.password, NEW.private_key, NEW.tags, NEW.archived) THEN
        NEW.probe_generation := replace(gen_random_uuid()::text, '-', '');
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_nodes_probe_generation BEFORE INSERT OR UPDATE ON nodes
FOR EACH ROW EXECUTE FUNCTION nodes_probe_generation_set();

CREATE FUNCTION ssh_keys_probe_generation_set() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'INSERT' OR ROW(OLD.private_key, OLD.disabled, OLD.expires_at, OLD.allowed_purposes, OLD.allowed_node_ids, OLD.allowed_node_tags)
        IS DISTINCT FROM ROW(NEW.private_key, NEW.disabled, NEW.expires_at, NEW.allowed_purposes, NEW.allowed_node_ids, NEW.allowed_node_tags) THEN
        NEW.probe_generation := replace(gen_random_uuid()::text, '-', '');
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_ssh_keys_probe_generation BEFORE INSERT OR UPDATE ON ssh_keys
FOR EACH ROW EXECUTE FUNCTION ssh_keys_probe_generation_set();
