CREATE TABLE catalog_candidate_sweeps (
    id INTEGER NOT NULL PRIMARY KEY,
    revision INTEGER NOT NULL,
    after_created_at DATETIME,
    after_point_id TEXT,
    high_water_created_at DATETIME,
    high_water_point_id TEXT,
    CONSTRAINT catalog_candidate_sweep_id_check CHECK (id = 1),
    CONSTRAINT catalog_candidate_sweep_revision_check CHECK (
        typeof(revision) = 'integer' AND revision >= 1 AND revision <= 9223372036854775807
    ),
    CONSTRAINT catalog_candidate_sweep_after_id_check CHECK (
        after_point_id IS NULL OR (typeof(after_point_id) = 'text' AND length(after_point_id) = 32 AND length(CAST(after_point_id AS BLOB)) = 32 AND after_point_id NOT GLOB '*[^0-9a-f]*')
    ),
    CONSTRAINT catalog_candidate_sweep_high_water_id_check CHECK (
        high_water_point_id IS NULL OR (typeof(high_water_point_id) = 'text' AND length(high_water_point_id) = 32 AND length(CAST(high_water_point_id AS BLOB)) = 32 AND high_water_point_id NOT GLOB '*[^0-9a-f]*')
    ),
    CONSTRAINT catalog_candidate_sweep_boundary_check CHECK (
        (after_created_at IS NULL AND after_point_id IS NULL AND high_water_created_at IS NULL AND high_water_point_id IS NULL)
        OR
        (after_created_at IS NOT NULL AND after_point_id IS NOT NULL AND high_water_created_at IS NOT NULL AND high_water_point_id IS NOT NULL
            AND (after_created_at < high_water_created_at OR (after_created_at = high_water_created_at AND after_point_id <= high_water_point_id)))
    )
);

INSERT INTO catalog_candidate_sweeps (id, revision) VALUES (1, 1);

CREATE INDEX idx_recovery_points_created_at_id ON recovery_points(created_at, id);
