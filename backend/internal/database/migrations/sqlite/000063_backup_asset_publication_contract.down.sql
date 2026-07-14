CREATE TEMP TABLE backup_asset_063_down_guard (
    allowed INTEGER NOT NULL CHECK (allowed = 1)
);
INSERT INTO backup_asset_063_down_guard(allowed)
SELECT CASE WHEN
    EXISTS (SELECT 1 FROM recovery_point_leases WHERE holder_type = 'point_publication' AND status = 'active')
    OR EXISTS (SELECT 1 FROM recovery_points WHERE semantics = 'native_snapshot')
THEN 0 ELSE 1 END;
DROP TABLE backup_asset_063_down_guard;

DROP INDEX idx_recovery_points_producing_task_run_unique;
DROP INDEX idx_recovery_points_native_source_unique;

CREATE TABLE task_repository_links_new (
    id TEXT PRIMARY KEY,
    task_id INTEGER,
    repository_id TEXT NOT NULL,
    task_name_snapshot TEXT NOT NULL DEFAULT '',
    node_id_snapshot INTEGER NOT NULL DEFAULT 0,
    node_name_snapshot TEXT NOT NULL DEFAULT '',
    publication_mode TEXT NOT NULL CHECK (publication_mode IN ('legacy_mutable', 'versioned_hardlink', 'versioned_full_copy', 'versioned_prefix', 'native_object_versions')),
    encrypted_legacy_locator TEXT NOT NULL DEFAULT '',
    linked_at DATETIME NOT NULL,
    unlinked_at DATETIME,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    FOREIGN KEY (task_id) REFERENCES tasks(id) ON DELETE SET NULL,
    FOREIGN KEY (repository_id) REFERENCES backup_repositories(id) ON DELETE RESTRICT
);

INSERT INTO task_repository_links_new
    (id, task_id, repository_id, task_name_snapshot, node_id_snapshot,
     node_name_snapshot, publication_mode, encrypted_legacy_locator,
     linked_at, unlinked_at, created_at, updated_at)
SELECT link.id,
       link.task_id,
       link.repository_id,
       link.task_name_snapshot,
       link.node_id_snapshot,
       link.node_name_snapshot,
       CASE
           WHEN repository.provider_kind = 'restic'
                AND link.publication_mode = 'native_snapshot'
           THEN 'native_object_versions'
           ELSE link.publication_mode
       END,
       link.encrypted_legacy_locator,
       link.linked_at,
       link.unlinked_at,
       link.created_at,
       link.updated_at
FROM task_repository_links AS link
JOIN backup_repositories AS repository ON repository.id = link.repository_id;

DROP TABLE task_repository_links;
ALTER TABLE task_repository_links_new RENAME TO task_repository_links;
CREATE UNIQUE INDEX idx_task_repository_links_active_task
    ON task_repository_links(task_id)
    WHERE task_id IS NOT NULL AND unlinked_at IS NULL;
CREATE INDEX idx_task_repository_links_repository_id ON task_repository_links(repository_id);

CREATE TABLE recovery_point_leases_new (
    id TEXT PRIMARY KEY,
    recovery_point_id TEXT NOT NULL,
    holder_type TEXT NOT NULL CHECK (holder_type IN ('rsync_parent', 'catalog_build', 'content_session', 'processing_job', 'export_job', 'recovery_job')),
    owner_id TEXT NOT NULL,
    attempt_id TEXT NOT NULL,
    fence_token TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('active', 'released', 'expired')),
    lease_expires_at DATETIME NOT NULL,
    absolute_deadline DATETIME NOT NULL,
    last_heartbeat_at DATETIME NOT NULL,
    released_at DATETIME,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    FOREIGN KEY (recovery_point_id) REFERENCES recovery_points(id) ON DELETE CASCADE
);

INSERT INTO recovery_point_leases_new
    (id, recovery_point_id, holder_type, owner_id, attempt_id, fence_token,
     status, lease_expires_at, absolute_deadline, last_heartbeat_at,
     released_at, created_at, updated_at)
SELECT id, recovery_point_id, holder_type, owner_id, attempt_id, fence_token,
       status, lease_expires_at, absolute_deadline, last_heartbeat_at,
       released_at, created_at, updated_at
FROM recovery_point_leases;

DROP TABLE recovery_point_leases;
ALTER TABLE recovery_point_leases_new RENAME TO recovery_point_leases;
CREATE UNIQUE INDEX idx_recovery_point_leases_active_owner_slot
    ON recovery_point_leases(recovery_point_id, holder_type, owner_id)
    WHERE status = 'active';
CREATE INDEX idx_recovery_point_leases_recovery_status_expiry
    ON recovery_point_leases(recovery_point_id, status, lease_expires_at);
CREATE INDEX idx_recovery_point_leases_absolute_deadline ON recovery_point_leases(absolute_deadline);
