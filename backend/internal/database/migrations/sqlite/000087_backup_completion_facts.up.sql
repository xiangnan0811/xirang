-- 000087 records immutable, classified backup availability facts.
-- Historical Node.last_backup_at values are preserved as explicitly unverified
-- rows, then the authoritative denormalization is rebuilt only from immutable
-- managed commit evidence.
CREATE TABLE backup_completions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id INTEGER,
    task_run_id INTEGER,
    node_id INTEGER NOT NULL,
    executor_type TEXT NOT NULL DEFAULT '',
    fact_kind TEXT NOT NULL,
    evidence_status TEXT NOT NULL,
    completed_at DATETIME NOT NULL,
    evidence_ref TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    CHECK (node_id > 0 OR (fact_kind = 'legacy_unverified' AND node_id = 0)),
    CHECK (task_id IS NULL OR task_id > 0),
    CHECK (task_run_id IS NULL OR task_run_id > 0),
    CHECK (executor_type IN ('', 'rsync', 'restic', 'rclone')),
    CHECK (fact_kind IN ('legacy_transfer_completed', 'managed_committed', 'legacy_unverified')),
    CHECK (evidence_status IN ('verified', 'unverified')),
    CHECK (
        (fact_kind = 'legacy_unverified' AND evidence_status = 'unverified' AND task_id IS NULL AND task_run_id IS NULL AND executor_type = '' AND length(evidence_ref) <= 64)
        OR (fact_kind = 'legacy_transfer_completed' AND evidence_status = 'verified' AND task_id IS NOT NULL AND task_run_id IS NOT NULL AND executor_type IN ('rsync', 'restic', 'rclone') AND evidence_ref = '')
        OR (fact_kind = 'managed_committed' AND evidence_status = 'verified' AND task_id IS NOT NULL AND task_run_id IS NOT NULL AND executor_type IN ('rsync', 'restic', 'rclone') AND length(evidence_ref) = 32)
    )
);

CREATE UNIQUE INDEX idx_backup_completions_task_run
    ON backup_completions(task_run_id);
CREATE INDEX idx_backup_completions_node_completed
    ON backup_completions(node_id, completed_at DESC, id DESC);
CREATE INDEX idx_backup_completions_verified_task
    ON backup_completions(task_id, completed_at DESC, id DESC)
    WHERE evidence_status = 'verified';
CREATE UNIQUE INDEX idx_backup_completions_unverified_ref
    ON backup_completions(evidence_ref)
    WHERE evidence_status = 'unverified' AND evidence_ref <> '';

INSERT INTO backup_completions
    (node_id, fact_kind, evidence_status, completed_at, created_at, updated_at)
SELECT id, 'legacy_unverified', 'unverified', last_backup_at, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP
FROM nodes
WHERE last_backup_at IS NOT NULL;

-- Mutable Node timestamps are never authoritative after this cutover.
UPDATE nodes SET last_backup_at = NULL;

-- Backfill only committed immutable points whose own snapshots and repository
-- identity prove the transfer. This deliberately does not consult the mutable
-- Task or TaskRun executor snapshot; pre-000086 runs legitimately have an
-- empty executor snapshot. A committed timestamp is required: captured time
-- alone is not proof that a provider commit completed.
WITH ranked_managed AS (
    SELECT
        point.producing_task_id AS task_id,
        point.producing_task_run_id AS task_run_id,
        point.producing_node_id_snapshot AS node_id,
        LOWER(repository.provider_kind) AS executor_type,
        point.id AS evidence_ref,
        point.committed_at AS completed_at,
        ROW_NUMBER() OVER (
            PARTITION BY point.producing_task_run_id
            ORDER BY point.committed_at DESC, point.id DESC
        ) AS row_number
    FROM recovery_points AS point
    JOIN backup_repositories AS repository
      ON repository.id = point.repository_id
    WHERE point.state = 'committed'
      AND point.semantics IN ('native_snapshot', 'xirang_manifest')
      AND point.producing_task_id IS NOT NULL
      AND point.producing_task_id > 0
      AND point.producing_task_run_id IS NOT NULL
      AND point.producing_task_run_id > 0
      AND point.producing_node_id_snapshot > 0
      AND point.id IS NOT NULL
      AND LENGTH(point.id) = 32
      AND point.id NOT GLOB '*[^0-9a-fA-F]*'
      AND point.committed_at IS NOT NULL
      AND json_valid(point.lineage_json)
      -- json_each preserves root keys (including duplicates), so malformed,
      -- nested and ambiguous lineage cannot masquerade as valid provenance.
      AND NOT EXISTS (
          SELECT 1
          FROM json_each(CASE WHEN json_valid(point.lineage_json) THEN point.lineage_json ELSE '{}' END) AS lineage_key
          WHERE lineage_key.key NOT IN (
              'version', 'task_repository_link_id', 'task_id', 'task_run_id',
              'trigger', 'chain_run_id_present', 'chain_run_id_digest',
              'publication_mode', 'point_codec_version', 'tag_codec_version',
              'started_at', 'prepared_at', 'point_deadline_at'
          )
      )
      AND NOT EXISTS (
          SELECT 1
          FROM json_each(CASE WHEN json_valid(point.lineage_json) THEN point.lineage_json ELSE '{}' END) AS lineage_key
          GROUP BY lineage_key.key
          HAVING COUNT(*) > 1
      )
      AND json_type(point.lineage_json, '$.version') = 'integer'
      AND json_extract(point.lineage_json, '$.version') = 1
      AND json_type(point.lineage_json, '$.task_repository_link_id') = 'text'
      AND LENGTH(json_extract(point.lineage_json, '$.task_repository_link_id')) = 32
      AND json_extract(point.lineage_json, '$.task_repository_link_id') NOT GLOB '*[^0-9a-f]*'
      AND json_type(point.lineage_json, '$.task_id') = 'integer'
      AND json_extract(point.lineage_json, '$.task_id') = point.producing_task_id
      AND json_type(point.lineage_json, '$.task_run_id') = 'integer'
      AND json_extract(point.lineage_json, '$.task_run_id') = point.producing_task_run_id
      AND json_type(point.lineage_json, '$.trigger') = 'text'
      AND LENGTH(json_extract(point.lineage_json, '$.trigger')) <= 64
      AND json_extract(point.lineage_json, '$.trigger') NOT GLOB '*[^A-Za-z0-9_.:@-]*'
      AND LOWER(COALESCE(json_extract(point.lineage_json, '$.trigger'), '')) NOT IN ('restore', 'drill')
      AND (
          json_type(point.lineage_json, '$.chain_run_id_present') IS NULL
          OR json_type(point.lineage_json, '$.chain_run_id_present') IN ('true', 'false')
      )
      AND (
          (
              COALESCE(json_extract(point.lineage_json, '$.chain_run_id_present'), 0) = 1
              AND json_type(point.lineage_json, '$.chain_run_id_digest') = 'text'
              AND LENGTH(json_extract(point.lineage_json, '$.chain_run_id_digest')) = 64
              AND json_extract(point.lineage_json, '$.chain_run_id_digest') NOT GLOB '*[^0-9a-f]*'
          )
          OR (
              COALESCE(json_extract(point.lineage_json, '$.chain_run_id_present'), 0) = 0
              AND (
                  json_type(point.lineage_json, '$.chain_run_id_digest') IS NULL
                  OR (
                      json_type(point.lineage_json, '$.chain_run_id_digest') = 'text'
                      AND json_extract(point.lineage_json, '$.chain_run_id_digest') = ''
                  )
              )
          )
      )
      AND json_type(point.lineage_json, '$.publication_mode') = 'text'
      AND json_type(point.lineage_json, '$.point_codec_version') = 'integer'
      AND json_extract(point.lineage_json, '$.point_codec_version') = 1
      AND json_type(point.lineage_json, '$.tag_codec_version') = 'integer'
      AND json_type(point.lineage_json, '$.started_at') = 'text'
      AND json_type(point.lineage_json, '$.prepared_at') = 'text'
      AND json_type(point.lineage_json, '$.point_deadline_at') = 'text'
      AND (
          (point.semantics = 'native_snapshot'
              AND json_extract(point.lineage_json, '$.publication_mode') = 'native_snapshot'
              AND json_extract(point.lineage_json, '$.tag_codec_version') = 1)
          OR (point.semantics = 'xirang_manifest'
              AND json_extract(point.lineage_json, '$.publication_mode') IN
                  ('versioned_hardlink', 'versioned_full_copy', 'versioned_prefix', 'native_object_versions')
              AND json_extract(point.lineage_json, '$.tag_codec_version') = 0)
      )
      AND (
          (point.semantics = 'native_snapshot' AND LOWER(repository.provider_kind) = 'restic')
          OR (point.semantics = 'xirang_manifest'
              AND json_extract(point.lineage_json, '$.publication_mode') IN ('versioned_hardlink', 'versioned_full_copy')
              AND LOWER(repository.provider_kind) = 'rsync')
          OR (point.semantics = 'xirang_manifest'
              AND json_extract(point.lineage_json, '$.publication_mode') IN ('versioned_prefix', 'native_object_versions')
              AND LOWER(repository.provider_kind) = 'rclone')
      )
)
INSERT INTO backup_completions
    (task_id, task_run_id, node_id, executor_type, fact_kind, evidence_status,
     completed_at, evidence_ref, created_at, updated_at)
SELECT task_id, task_run_id, node_id, executor_type, 'managed_committed', 'verified',
       completed_at, evidence_ref, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP
FROM ranked_managed
WHERE row_number = 1;

-- Every committed immutable point that could not be proved above remains an
-- explicitly unverified marker. Its nullable TaskRun identity means a later
-- verified replay is never blocked by the marker.
INSERT INTO backup_completions
    (node_id, fact_kind, evidence_status, completed_at, evidence_ref, created_at, updated_at)
SELECT CASE WHEN point.producing_node_id_snapshot > 0 THEN point.producing_node_id_snapshot ELSE 0 END,
       'legacy_unverified', 'unverified',
       COALESCE(point.committed_at, point.captured_at, point.updated_at, point.created_at, CURRENT_TIMESTAMP),
       point.id, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP
FROM recovery_points AS point
WHERE point.state = 'committed'
  AND point.semantics IN ('native_snapshot', 'xirang_manifest', 'imported_baseline')
  AND point.id IS NOT NULL
  AND LENGTH(point.id) > 0
  AND NOT EXISTS (
      SELECT 1
      FROM backup_completions AS completion
      WHERE completion.evidence_status = 'verified'
        AND completion.evidence_ref = point.id
  )
  AND NOT EXISTS (
      SELECT 1

      FROM backup_completions AS completion
      WHERE completion.evidence_status = 'unverified'
        AND completion.evidence_ref = point.id
  );

UPDATE nodes
SET last_backup_at = (
    SELECT MAX(completed_at)
    FROM backup_completions AS completion
    WHERE completion.node_id = nodes.id
      AND completion.evidence_status = 'verified'
)
WHERE EXISTS (
    SELECT 1
    FROM backup_completions AS completion
    WHERE completion.node_id = nodes.id
      AND completion.evidence_status = 'verified'
);
-- Repository provider kind is the immutable type evidence used by future
-- completion classification. Existing ambiguous points remain unverified;
-- this guard prevents new provider reclassification after the cutover.
DROP TRIGGER IF EXISTS trg_backup_repositories_provider_kind_immutable;
CREATE TRIGGER trg_backup_repositories_provider_kind_immutable
BEFORE UPDATE OF provider_kind ON backup_repositories
WHEN NEW.provider_kind IS NOT OLD.provider_kind
BEGIN
    SELECT RAISE(ABORT, '000087 backup repository provider kind is immutable');
END;


-- Completion facts are append-only. Reclassification, timestamp edits and
-- deletion would invalidate audit and freshness history.
DROP TRIGGER IF EXISTS trg_backup_completions_immutable_update;
CREATE TRIGGER trg_backup_completions_immutable_update
BEFORE UPDATE ON backup_completions
BEGIN
    SELECT RAISE(ABORT, '000087 backup completion facts are immutable');
END;
DROP TRIGGER IF EXISTS trg_backup_completions_immutable_delete;
CREATE TRIGGER trg_backup_completions_immutable_delete
BEFORE DELETE ON backup_completions
BEGIN
    SELECT RAISE(ABORT, '000087 backup completion facts are immutable');
END;

-- Never downgrade away durable evidence. An empty table remains safely
-- reversible for test/upgrade tooling; once any fact exists, downgrade is
-- refused and an audited data-preservation procedure is required.
DROP TRIGGER IF EXISTS trg_backup_completions_downgrade_admission;
CREATE TRIGGER trg_backup_completions_downgrade_admission
BEFORE INSERT ON schema_migrations
WHEN NEW.version < 87
 AND EXISTS (SELECT 1 FROM backup_completions)
BEGIN
    SELECT RAISE(ABORT, '000087 downgrade blocked: backup completion evidence exists');
END;
