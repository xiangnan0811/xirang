BEGIN;

CREATE TABLE backup_asset_installations (
    id VARCHAR(32) NOT NULL PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
    slot INTEGER NOT NULL DEFAULT 1 CHECK (slot = 1),
    class VARCHAR(16) NOT NULL CHECK (class IN ('fresh', 'existing')),
    readiness VARCHAR(16) NOT NULL CHECK (readiness IN ('unknown', 'blocked', 'ready', 'acknowledged')),
    inventory_digest VARCHAR(64) NOT NULL CHECK (
        inventory_digest = ''
        OR inventory_digest ~ '^[0-9a-f]{64}$'
    ),
    ack_actor_id BIGINT,
    ack_at TIMESTAMPTZ,
    enablement_succeeded_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
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
    id VARCHAR(32) NOT NULL PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
    digest VARCHAR(64) NOT NULL CHECK (digest ~ '^[0-9a-f]{64}$'),
    status VARCHAR(16) NOT NULL CHECK (status IN ('running', 'complete', 'failed')),
    counts_json TEXT NOT NULL CHECK (length(counts_json) > 0 AND counts_json::jsonb IS NOT NULL),
    error_category VARCHAR(32) NOT NULL DEFAULT '' CHECK (error_category IN (
        '', 'inventory_failed', 'inventory_incomplete', 'identity_conflict', 'capability_gap', 'command_unsupported'
    )),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_backup_asset_inventory_runs_created
    ON backup_asset_inventory_runs(created_at, status);

CREATE TABLE backup_asset_repository_conflicts (
    id VARCHAR(32) NOT NULL PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
    run_id VARCHAR(32) NOT NULL CHECK (run_id ~ '^[0-9a-f]{32}$')
        REFERENCES backup_asset_inventory_runs(id) ON DELETE RESTRICT,
    kind VARCHAR(32) NOT NULL CHECK (kind IN (
        'shared_restic_identity', 'task_repository_mismatch', 'capability_gap', 'command_unsupported'
    )),
    task_ids_json TEXT NOT NULL CHECK (length(task_ids_json) > 0 AND task_ids_json::jsonb IS NOT NULL),
    repository_id VARCHAR(32) NOT NULL DEFAULT '' CHECK (
        repository_id = ''
        OR repository_id ~ '^[0-9a-f]{32}$'
    ),
    stable_reason_code VARCHAR(128) NOT NULL CHECK (
        length(stable_reason_code) BETWEEN 1 AND 128
        AND stable_reason_code ~ '^[a-z0-9._]+$'
    ),
    created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_backup_asset_repository_conflicts_run
    ON backup_asset_repository_conflicts(run_id, kind);

CREATE OR REPLACE FUNCTION backup_asset_ga_downgrade_admission()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.version < 71 AND (
        EXISTS (
            SELECT 1 FROM backup_asset_installations
            WHERE readiness IN ('ready', 'acknowledged')
               OR enablement_succeeded_at IS NOT NULL
        )
        OR EXISTS (SELECT 1 FROM backup_asset_repository_conflicts)
    ) THEN
        RAISE EXCEPTION '000071 downgrade blocked: backup asset GA readiness or conflict state exists';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_backup_asset_ga_downgrade_admission
BEFORE INSERT ON schema_migrations
FOR EACH ROW EXECUTE FUNCTION backup_asset_ga_downgrade_admission();

COMMIT;
