CREATE TABLE backup_asset_managed_history_latches (
    id TEXT PRIMARY KEY,
    scope TEXT NOT NULL CHECK (scope IN ('installation', 'repository')),
    repository_id TEXT,
    repository_identity_digest TEXT NOT NULL DEFAULT '',
    first_semantics TEXT NOT NULL CHECK (first_semantics IN ('native_snapshot', 'xirang_manifest', 'imported_baseline')),
    first_origin TEXT NOT NULL CHECK (length(first_origin) > 0),
    first_seen_at DATETIME NOT NULL,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    CHECK (
        (scope = 'installation' AND repository_id IS NULL AND repository_identity_digest = '')
        OR
        (scope = 'repository' AND repository_id IS NOT NULL AND length(repository_id) = 32 AND length(repository_identity_digest) = 64)
    )
);

CREATE UNIQUE INDEX idx_backup_asset_managed_history_latches_installation_unique
    ON backup_asset_managed_history_latches(scope)
    WHERE scope = 'installation';
CREATE UNIQUE INDEX idx_backup_asset_managed_history_latches_repository_unique
    ON backup_asset_managed_history_latches(repository_id)
    WHERE scope = 'repository';

CREATE UNIQUE INDEX idx_recovery_points_managed_tree_source_unique
    ON recovery_points(repository_id, source_fingerprint)
    WHERE semantics IN ('xirang_manifest', 'imported_baseline')
      AND source_fingerprint <> '';

INSERT OR IGNORE INTO backup_asset_managed_history_latches
    (id, scope, repository_id, repository_identity_digest, first_semantics, first_origin, first_seen_at, created_at, updated_at)
SELECT 'managed-history-installation',
       'installation',
       NULL,
       '',
       'native_snapshot',
       'migration_backfill',
       COALESCE(captured_at, committed_at, observed_at, created_at),
       created_at,
       updated_at
FROM recovery_points
WHERE semantics = 'native_snapshot'
ORDER BY COALESCE(captured_at, committed_at, observed_at, created_at), id
LIMIT 1;

INSERT OR IGNORE INTO backup_asset_managed_history_latches
    (id, scope, repository_id, repository_identity_digest, first_semantics, first_origin, first_seen_at, created_at, updated_at)
SELECT 'managed-history-repository-' || repository_id,
       'repository',
       repository_id,
       substr('00000000000000000000000000000000' || repository_id, -64),
       'native_snapshot',
       'migration_backfill',
       MIN(COALESCE(captured_at, committed_at, observed_at, created_at)),
       MIN(created_at),
       MIN(updated_at)
FROM recovery_points
WHERE semantics = 'native_snapshot'
GROUP BY repository_id;
