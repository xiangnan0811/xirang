CREATE INDEX IF NOT EXISTS idx_backup_asset_search_documents_recovery_point
    ON backup_asset_search_documents(recovery_point_id, search_generation_id);
