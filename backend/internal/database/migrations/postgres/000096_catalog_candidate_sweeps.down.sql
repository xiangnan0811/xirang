BEGIN;

DROP INDEX idx_recovery_points_created_at_id;
DROP TABLE catalog_candidate_sweeps;

COMMIT;
