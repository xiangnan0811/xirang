-- Drain all new writers before dropping ordering metadata.
DROP TRIGGER trg_nodes_probe_generation ON nodes;
DROP TRIGGER trg_ssh_keys_probe_generation ON ssh_keys;
DROP FUNCTION nodes_probe_generation_set();
DROP FUNCTION ssh_keys_probe_generation_set();
DROP TABLE node_probe_state;
ALTER TABLE nodes DROP COLUMN probe_generation;
ALTER TABLE ssh_keys DROP COLUMN probe_generation;
