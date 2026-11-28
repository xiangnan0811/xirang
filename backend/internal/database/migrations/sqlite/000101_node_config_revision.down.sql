-- New writers and in-flight probes must be drained before downgrading.
DROP TRIGGER trg_nodes_config_revision_insert;
DROP TRIGGER trg_nodes_config_revision_update;
ALTER TABLE node_probe_state DROP COLUMN applied_status;
ALTER TABLE nodes DROP COLUMN config_revision;
