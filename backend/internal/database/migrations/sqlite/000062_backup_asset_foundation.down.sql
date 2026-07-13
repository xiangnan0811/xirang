DROP TABLE IF EXISTS backup_asset_audit_events;
DROP TABLE IF EXISTS backup_asset_audit_checkpoints;
DROP TABLE IF EXISTS recovery_point_leases;
DROP TABLE IF EXISTS wrapped_domain_keys;
DROP TABLE IF EXISTS catalog_entries;
DROP TABLE IF EXISTS catalog_generations;
DROP TABLE IF EXISTS recovery_point_manifests;
DROP TABLE IF EXISTS recovery_points;
DROP TABLE IF EXISTS task_repository_links;
DROP TABLE IF EXISTS repository_access_bindings;
DROP TABLE IF EXISTS backup_repositories;

ALTER TABLE tasks DROP COLUMN archived_at;
