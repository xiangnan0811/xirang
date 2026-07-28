BEGIN;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM backup_asset_export_jobs)
       OR EXISTS (SELECT 1 FROM backup_asset_export_keys)
       OR EXISTS (SELECT 1 FROM backup_asset_export_items)
       OR EXISTS (SELECT 1 FROM backup_asset_export_attempts)
       OR EXISTS (SELECT 1 FROM backup_asset_export_item_attempts)
       OR EXISTS (SELECT 1 FROM backup_asset_export_source_leases)
       OR EXISTS (SELECT 1 FROM backup_asset_export_artifacts)
       OR EXISTS (SELECT 1 FROM backup_asset_export_idempotency)
       OR EXISTS (SELECT 1 FROM backup_asset_export_quota_buckets)
       OR EXISTS (SELECT 1 FROM backup_asset_export_reservations)
       OR EXISTS (SELECT 1 FROM backup_asset_export_delivery_grants)
       OR EXISTS (SELECT 1 FROM backup_asset_export_delivery_requests)
       OR EXISTS (SELECT 1 FROM backup_asset_archive_member_requests)
       OR EXISTS (SELECT 1 FROM recovery_point_leases WHERE holder_type = 'export_job')
       OR EXISTS (SELECT 1 FROM wrapped_domain_keys WHERE domain = 'export_store') THEN
        RAISE EXCEPTION '000068 down blocked: export, archive member, lease, or key state exists';
    END IF;
END $$;

DROP TABLE backup_asset_export_delivery_requests;
DROP TABLE backup_asset_export_delivery_grants;
DROP TABLE backup_asset_archive_member_requests;
DROP TABLE backup_asset_export_reservations;
DROP TABLE backup_asset_export_quota_buckets;
DROP TABLE backup_asset_export_idempotency;
DROP TABLE backup_asset_export_artifacts;
DROP TABLE backup_asset_export_source_leases;
DROP TABLE backup_asset_export_item_attempts;
DROP TABLE backup_asset_export_attempts;
DROP TABLE backup_asset_export_items;
DROP TABLE backup_asset_export_keys;
DROP TABLE backup_asset_export_jobs;

ALTER TABLE wrapped_domain_keys DROP CONSTRAINT wrapped_domain_keys_domain_check;
ALTER TABLE wrapped_domain_keys ADD CONSTRAINT wrapped_domain_keys_domain_check
    CHECK (domain IN ('entry_identity', 'cursor_signing', 'audit_fingerprint', 'recovery_cleanup_ownership', 'search_token', 'derived_store'));

COMMIT;
