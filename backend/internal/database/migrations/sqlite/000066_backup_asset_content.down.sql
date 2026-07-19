CREATE TEMP TABLE backup_asset_066_down_guard (
    allowed INTEGER NOT NULL CHECK (allowed = 1)
);
INSERT INTO backup_asset_066_down_guard(allowed)
SELECT CASE WHEN
    EXISTS (SELECT 1 FROM backup_asset_delivery_grants)
    OR EXISTS (SELECT 1 FROM backup_asset_delivery_requests)
    OR EXISTS (SELECT 1 FROM backup_asset_delivery_usage)
    OR EXISTS (SELECT 1 FROM recovery_point_leases WHERE holder_type = 'content_session')
THEN 0 ELSE 1 END;
DROP TABLE backup_asset_066_down_guard;

DROP INDEX idx_backup_asset_audit_events_content_grant_action;
DROP TABLE backup_asset_delivery_requests;
DROP TABLE backup_asset_delivery_grants;
DROP TABLE backup_asset_delivery_usage;
