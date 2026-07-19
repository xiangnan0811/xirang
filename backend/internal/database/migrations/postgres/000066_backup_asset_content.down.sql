BEGIN;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM backup_asset_delivery_grants)
       OR EXISTS (SELECT 1 FROM backup_asset_delivery_requests)
       OR EXISTS (SELECT 1 FROM backup_asset_delivery_usage)
       OR EXISTS (SELECT 1 FROM recovery_point_leases WHERE holder_type = 'content_session') THEN
        RAISE EXCEPTION '000066 down blocked: backup asset content state exists';
    END IF;
END $$;

DROP INDEX idx_backup_asset_audit_events_content_grant_action;
DROP TABLE backup_asset_delivery_requests;
DROP TABLE backup_asset_delivery_grants;
DROP TABLE backup_asset_delivery_usage;

COMMIT;
