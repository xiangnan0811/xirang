BEGIN;

-- 000087 records immutable, classified backup availability facts.
-- Historical Node.last_backup_at values are preserved as explicitly unverified
-- rows, then the authoritative denormalization is rebuilt only from immutable
-- managed commit evidence.
CREATE TABLE IF NOT EXISTS backup_completions (
    id BIGSERIAL PRIMARY KEY,
    task_id BIGINT,
    task_run_id BIGINT,
    node_id BIGINT NOT NULL,
    executor_type VARCHAR(32) NOT NULL DEFAULT '',
    fact_kind VARCHAR(32) NOT NULL,
    evidence_status VARCHAR(16) NOT NULL,
    completed_at TIMESTAMPTZ NOT NULL,
    evidence_ref VARCHAR(64) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT backup_completions_node_positive CHECK (node_id > 0 OR (fact_kind = 'legacy_unverified' AND node_id = 0)),
    CONSTRAINT backup_completions_task_positive CHECK (task_id IS NULL OR task_id > 0),
    CONSTRAINT backup_completions_run_positive CHECK (task_run_id IS NULL OR task_run_id > 0),
    CONSTRAINT backup_completions_executor_valid CHECK (executor_type IN ('', 'rsync', 'restic', 'rclone')),
    CONSTRAINT backup_completions_kind_valid CHECK (fact_kind IN ('legacy_transfer_completed', 'managed_committed', 'legacy_unverified')),
    CONSTRAINT backup_completions_evidence_valid CHECK (evidence_status IN ('verified', 'unverified')),
    CONSTRAINT backup_completions_shape_valid CHECK (
        (fact_kind = 'legacy_unverified' AND evidence_status = 'unverified' AND task_id IS NULL AND task_run_id IS NULL AND executor_type = '' AND length(evidence_ref) <= 64)
        OR (fact_kind = 'legacy_transfer_completed' AND evidence_status = 'verified' AND task_id IS NOT NULL AND task_run_id IS NOT NULL AND executor_type IN ('rsync', 'restic', 'rclone') AND evidence_ref = '')
        OR (fact_kind = 'managed_committed' AND evidence_status = 'verified' AND task_id IS NOT NULL AND task_run_id IS NOT NULL AND executor_type IN ('rsync', 'restic', 'rclone') AND length(evidence_ref) = 32)
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_backup_completions_task_run
    ON backup_completions(task_run_id);
CREATE INDEX IF NOT EXISTS idx_backup_completions_node_completed
    ON backup_completions(node_id, completed_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_backup_completions_verified_task
    ON backup_completions(task_id, completed_at DESC, id DESC)
    WHERE evidence_status = 'verified';
CREATE UNIQUE INDEX IF NOT EXISTS idx_backup_completions_unverified_ref
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
-- Parse the persisted lineage as typed JSON before migration backfill.  The
-- pre-000087 migration used text regular expressions, which could mistake a
-- nested or duplicate key for a root field and could abort on malformed JSON.
-- Invalid lineage deliberately returns an empty provider classification so the
-- point is retained only as an unverified marker below.
CREATE OR REPLACE FUNCTION backup_completion_lineage_provider(
    raw_lineage TEXT,
    expected_task_id BIGINT,
    expected_task_run_id BIGINT,
    point_semantics TEXT
)
RETURNS TEXT
LANGUAGE plpgsql
IMMUTABLE
AS $$
DECLARE
    payload JSON;
    key_name TEXT;
    chain_run_id_present BOOLEAN := FALSE;
    publication_mode TEXT;
    trigger_name TEXT;
    started_at TIMESTAMPTZ;
    prepared_at TIMESTAMPTZ;
    point_deadline_at TIMESTAMPTZ;
BEGIN
    IF raw_lineage IS NULL OR expected_task_id IS NULL OR expected_task_id <= 0
       OR expected_task_run_id IS NULL OR expected_task_run_id <= 0
       OR point_semantics NOT IN ('native_snapshot', 'xirang_manifest') THEN
        RETURN '';
    END IF;

    payload := raw_lineage::JSON;
    IF json_typeof(payload) IS DISTINCT FROM 'object' THEN
        RETURN '';
    END IF;

    -- json (rather than jsonb) preserves duplicate object keys, allowing the
    -- migration to reject ambiguous lineage instead of choosing one value.
    FOR key_name IN
        SELECT entry.key
        FROM json_each(payload) AS entry
    LOOP
        IF key_name NOT IN (
            'version', 'task_repository_link_id', 'task_id', 'task_run_id',
            'trigger', 'chain_run_id_present', 'chain_run_id_digest',
            'publication_mode', 'point_codec_version', 'tag_codec_version',
            'started_at', 'prepared_at', 'point_deadline_at'
        ) THEN
            RETURN '';
        END IF;
    END LOOP;
    IF EXISTS (
        SELECT 1
        FROM json_each(payload) AS entry
        GROUP BY entry.key
        HAVING COUNT(*) > 1
    ) THEN
        RETURN '';
    END IF;

    IF json_typeof(payload -> 'version') IS DISTINCT FROM 'number'
       OR payload ->> 'version' IS DISTINCT FROM '1'
       OR json_typeof(payload -> 'task_repository_link_id') IS DISTINCT FROM 'string'
       OR (payload ->> 'task_repository_link_id') !~ '^[0-9a-f]{32}$'
       OR json_typeof(payload -> 'task_id') IS DISTINCT FROM 'number'
       OR payload ->> 'task_id' IS DISTINCT FROM expected_task_id::TEXT
       OR json_typeof(payload -> 'task_run_id') IS DISTINCT FROM 'number'
       OR payload ->> 'task_run_id' IS DISTINCT FROM expected_task_run_id::TEXT
       OR json_typeof(payload -> 'point_codec_version') IS DISTINCT FROM 'number'
       OR payload ->> 'point_codec_version' IS DISTINCT FROM '1' THEN
        RETURN '';
    END IF;

    IF json_typeof(payload -> 'trigger') IS DISTINCT FROM 'string' THEN
        RETURN '';
    END IF;
    trigger_name := payload ->> 'trigger';
    IF trigger_name !~ '^[[:alnum:]_.:@-]{0,64}$'
       OR LOWER(trigger_name) IN ('restore', 'drill') THEN
        RETURN '';
    END IF;

    IF EXISTS (
        SELECT 1 FROM json_each(payload) AS entry
        WHERE entry.key = 'chain_run_id_present'
    ) THEN
        IF json_typeof(payload -> 'chain_run_id_present') IS DISTINCT FROM 'boolean' THEN
            RETURN '';
        END IF;
        chain_run_id_present := (payload ->> 'chain_run_id_present')::BOOLEAN;
    END IF;
    IF chain_run_id_present THEN
        IF json_typeof(payload -> 'chain_run_id_digest') IS DISTINCT FROM 'string'
           OR (payload ->> 'chain_run_id_digest') !~ '^[0-9a-f]{64}$' THEN
            RETURN '';
        END IF;
    ELSIF EXISTS (
        SELECT 1 FROM json_each(payload) AS entry
        WHERE entry.key = 'chain_run_id_digest'
    ) THEN
        IF json_typeof(payload -> 'chain_run_id_digest') IS DISTINCT FROM 'string'
           OR payload ->> 'chain_run_id_digest' <> '' THEN
            RETURN '';
        END IF;
    END IF;

    IF json_typeof(payload -> 'publication_mode') IS DISTINCT FROM 'string'
       OR json_typeof(payload -> 'tag_codec_version') IS DISTINCT FROM 'number' THEN
        RETURN '';
    END IF;
    publication_mode := payload ->> 'publication_mode';
    IF point_semantics = 'native_snapshot' THEN
        IF publication_mode <> 'native_snapshot'
           OR payload ->> 'tag_codec_version' IS DISTINCT FROM '1' THEN
            RETURN '';
        END IF;
    ELSE
        IF publication_mode NOT IN (
            'versioned_hardlink', 'versioned_full_copy',
            'versioned_prefix', 'native_object_versions'
        ) OR payload ->> 'tag_codec_version' IS DISTINCT FROM '0' THEN
            RETURN '';
        END IF;
    END IF;

    IF json_typeof(payload -> 'started_at') IS DISTINCT FROM 'string'
       OR json_typeof(payload -> 'prepared_at') IS DISTINCT FROM 'string'
       OR json_typeof(payload -> 'point_deadline_at') IS DISTINCT FROM 'string'
       OR (payload ->> 'started_at') !~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}([.][0-9]{1,9})?Z$'
       OR (payload ->> 'prepared_at') !~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}([.][0-9]{1,9})?Z$'
       OR (payload ->> 'point_deadline_at') !~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}([.][0-9]{1,9})?Z$' THEN
        RETURN '';
    END IF;
    started_at := (payload ->> 'started_at')::TIMESTAMPTZ;
    prepared_at := (payload ->> 'prepared_at')::TIMESTAMPTZ;
    point_deadline_at := (payload ->> 'point_deadline_at')::TIMESTAMPTZ;
    IF prepared_at < started_at OR point_deadline_at <= prepared_at THEN
        RETURN '';
    END IF;

    IF point_semantics = 'native_snapshot' THEN
        RETURN 'restic';
    END IF;
    IF publication_mode IN ('versioned_hardlink', 'versioned_full_copy') THEN
        RETURN 'rsync';
    END IF;
    IF publication_mode IN ('versioned_prefix', 'native_object_versions') THEN
        RETURN 'rclone';
    END IF;
    RETURN '';
EXCEPTION WHEN OTHERS THEN
    -- A malformed value, invalid scalar cast, or unexpected legacy shape is
    -- historical ambiguity, not a reason to abort the entire migration.
    RETURN '';
END;
$$;

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
      AND point.id ~ '^[0-9a-fA-F]{32}$'
      AND point.committed_at IS NOT NULL
      AND backup_completion_lineage_provider(
          point.lineage_json,
          point.producing_task_id,
          point.producing_task_run_id,
          point.semantics
      ) = LOWER(repository.provider_kind)
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
  AND length(point.id) > 0
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
CREATE OR REPLACE FUNCTION backup_repositories_provider_kind_immutable_guard()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.provider_kind IS DISTINCT FROM OLD.provider_kind THEN
        RAISE EXCEPTION '000087 backup repository provider kind is immutable';
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS trg_backup_repositories_provider_kind_immutable ON backup_repositories;
CREATE TRIGGER trg_backup_repositories_provider_kind_immutable
BEFORE UPDATE OF provider_kind ON backup_repositories
FOR EACH ROW EXECUTE FUNCTION backup_repositories_provider_kind_immutable_guard();

CREATE OR REPLACE FUNCTION backup_completions_immutable_guard()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION '000087 backup completion facts are immutable';
END;
$$;
DROP TRIGGER IF EXISTS trg_backup_completions_immutable ON backup_completions;
CREATE TRIGGER trg_backup_completions_immutable
BEFORE UPDATE OR DELETE ON backup_completions
FOR EACH ROW EXECUTE FUNCTION backup_completions_immutable_guard();

-- Never downgrade away durable evidence. An empty table remains safely
-- reversible for test/upgrade tooling; once any fact exists, downgrade is
-- refused and an audited data-preservation procedure is required.
CREATE OR REPLACE FUNCTION backup_completions_downgrade_admission()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.version < 87 AND EXISTS (SELECT 1 FROM backup_completions) THEN
        RAISE EXCEPTION '000087 downgrade blocked: backup completion evidence exists';
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS trg_backup_completions_downgrade_admission ON schema_migrations;
CREATE TRIGGER trg_backup_completions_downgrade_admission
BEFORE INSERT ON schema_migrations
FOR EACH ROW EXECUTE FUNCTION backup_completions_downgrade_admission();

COMMIT;
