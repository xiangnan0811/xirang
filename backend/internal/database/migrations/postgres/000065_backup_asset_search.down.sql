BEGIN;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM wrapped_domain_keys WHERE domain = 'search_token')
       OR EXISTS (SELECT 1 FROM recovery_point_leases WHERE holder_type = 'search_index')
       OR EXISTS (SELECT 1 FROM backup_asset_search_generations)
       OR EXISTS (SELECT 1 FROM backup_asset_search_documents)
       OR EXISTS (SELECT 1 FROM backup_asset_search_postings)
       OR EXISTS (SELECT 1 FROM backup_asset_search_document_fields)
       OR EXISTS (SELECT 1 FROM backup_asset_saved_searches)
       OR EXISTS (SELECT 1 FROM backup_asset_saved_search_scope_points)
       OR EXISTS (SELECT 1 FROM backup_asset_favorites)
       OR EXISTS (SELECT 1 FROM backup_asset_tag_definitions)
       OR EXISTS (SELECT 1 FROM backup_asset_tag_assignments)
       OR EXISTS (SELECT 1 FROM backup_asset_recent_access)
       OR EXISTS (SELECT 1 FROM backup_asset_overlay_usage)
       OR EXISTS (SELECT 1 FROM backup_asset_overlay_idempotency) THEN
        RAISE EXCEPTION '000065 down blocked: backup asset search or overlay data exists';
    END IF;
END $$;

DROP TABLE backup_asset_overlay_idempotency;
DROP TABLE backup_asset_overlay_usage;
DROP TABLE backup_asset_recent_access;
DROP TABLE backup_asset_tag_assignments;
DROP TABLE backup_asset_tag_definitions;
DROP TABLE backup_asset_favorites;
DROP TABLE backup_asset_saved_search_scope_points;
DROP TABLE backup_asset_saved_searches;
DROP TABLE backup_asset_search_document_fields;
DROP TABLE backup_asset_search_postings;
DROP TABLE backup_asset_search_documents;
DROP TABLE backup_asset_search_generations;

DROP INDEX idx_catalog_entries_generation_entry_recovery_point;
DROP INDEX idx_catalog_generations_id_recovery_point;

ALTER TABLE wrapped_domain_keys
    DROP CONSTRAINT wrapped_domain_keys_domain_check;
ALTER TABLE wrapped_domain_keys
    ADD CONSTRAINT wrapped_domain_keys_domain_check
    CHECK (domain IN ('entry_identity', 'cursor_signing', 'audit_fingerprint', 'recovery_cleanup_ownership'));

ALTER TABLE recovery_point_leases
    DROP CONSTRAINT recovery_point_leases_holder_type_check;
ALTER TABLE recovery_point_leases
    ADD CONSTRAINT recovery_point_leases_holder_type_check
    CHECK (holder_type IN ('rsync_parent', 'catalog_build', 'content_session', 'processing_job', 'export_job', 'recovery_job', 'point_publication'));

COMMIT;
