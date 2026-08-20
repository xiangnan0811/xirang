CREATE TABLE backup_asset_installations (
    id TEXT NOT NULL PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    slot INTEGER NOT NULL DEFAULT 1 CHECK (slot = 1),
    class TEXT NOT NULL CHECK (class IN ('fresh', 'existing')),
    readiness TEXT NOT NULL CHECK (readiness IN ('unknown', 'blocked', 'ready', 'acknowledged')),
    inventory_digest TEXT NOT NULL CHECK (
        inventory_digest = ''
        OR (length(inventory_digest) = 64 AND inventory_digest NOT GLOB '*[^0-9a-f]*')
    ),
    ack_actor_id INTEGER,
    ack_at DATETIME,
    enablement_succeeded_at DATETIME,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    CHECK (
        (class = 'fresh'
            AND readiness IN ('unknown', 'blocked', 'ready')
            AND ack_actor_id IS NULL
            AND ack_at IS NULL)
        OR
        (class = 'existing'
            AND (
                (readiness IN ('unknown', 'blocked', 'ready') AND ack_actor_id IS NULL AND ack_at IS NULL)
                OR (readiness = 'acknowledged'
                    AND ack_actor_id IS NOT NULL
                    AND ack_at IS NOT NULL
                    AND length(inventory_digest) = 64)
            ))
    )
);
CREATE UNIQUE INDEX idx_backup_asset_installations_slot
    ON backup_asset_installations(slot);

CREATE TABLE backup_asset_inventory_runs (
    id TEXT NOT NULL PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    digest TEXT NOT NULL CHECK (length(digest) = 64 AND digest NOT GLOB '*[^0-9a-f]*'),
    status TEXT NOT NULL CHECK (status IN ('running', 'complete', 'failed')),
    counts_json TEXT NOT NULL CHECK (length(counts_json) > 0 AND json_valid(counts_json)),
    error_category TEXT NOT NULL DEFAULT '' CHECK (error_category IN (
        '', 'inventory_failed', 'inventory_incomplete', 'identity_conflict', 'capability_gap', 'command_unsupported'
    )),
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL
);
CREATE INDEX idx_backup_asset_inventory_runs_created
    ON backup_asset_inventory_runs(created_at, status);

CREATE TABLE backup_asset_repository_conflicts (
    id TEXT NOT NULL PRIMARY KEY CHECK (length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
    run_id TEXT NOT NULL CHECK (length(run_id) = 32 AND run_id NOT GLOB '*[^0-9a-f]*')
        REFERENCES backup_asset_inventory_runs(id) ON DELETE RESTRICT,
    kind TEXT NOT NULL CHECK (kind IN (
        'shared_restic_identity', 'task_repository_mismatch', 'capability_gap', 'command_unsupported'
    )),
    task_ids_json TEXT NOT NULL CHECK (length(task_ids_json) > 0 AND json_valid(task_ids_json)),
    repository_id TEXT NOT NULL DEFAULT '' CHECK (
        repository_id = ''
        OR (length(repository_id) = 32 AND repository_id NOT GLOB '*[^0-9a-f]*')
    ),
    stable_reason_code TEXT NOT NULL CHECK (
        length(stable_reason_code) BETWEEN 1 AND 128
        AND stable_reason_code NOT GLOB '*[^a-z0-9._]*'
    ),
    created_at DATETIME NOT NULL
);
CREATE INDEX idx_backup_asset_repository_conflicts_run
    ON backup_asset_repository_conflicts(run_id, kind);

CREATE TRIGGER trg_backup_asset_ga_downgrade_admission
BEFORE INSERT ON schema_migrations
WHEN NEW.version < 71 AND (
    EXISTS (
        SELECT 1 FROM backup_asset_installations
        WHERE readiness IN ('ready', 'acknowledged')
           OR enablement_succeeded_at IS NOT NULL
    )
    OR EXISTS (SELECT 1 FROM backup_asset_repository_conflicts)
)
BEGIN
    SELECT RAISE(ABORT, '000071 downgrade blocked: backup asset GA readiness or conflict state exists');
END;
