-- Drain all new writers before dropping ordering metadata.
DROP TRIGGER trg_nodes_probe_generation_insert;
DROP TRIGGER trg_nodes_probe_generation_update;
DROP TRIGGER trg_ssh_keys_probe_generation_insert;
DROP TRIGGER trg_ssh_keys_probe_generation_update;
DROP TABLE node_probe_state;
ALTER TABLE nodes DROP COLUMN probe_generation;
ALTER TABLE ssh_keys DROP COLUMN probe_generation;
