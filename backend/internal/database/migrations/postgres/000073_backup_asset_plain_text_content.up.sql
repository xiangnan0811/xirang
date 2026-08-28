BEGIN;

ALTER TABLE backup_asset_delivery_grants
    DROP CONSTRAINT backup_asset_delivery_grants_renderer_check,
    DROP CONSTRAINT backup_asset_delivery_grants_profile_check,
    DROP CONSTRAINT backup_asset_delivery_grants_renderer_product_check,
    DROP CONSTRAINT backup_asset_delivery_grants_representation_product_check;

ALTER TABLE backup_asset_delivery_grants
    ADD CONSTRAINT backup_asset_delivery_grants_renderer_check CHECK (
        renderer IN ('escaped_text', 'plain_text', 'safe_raster', 'same_origin_pdf', 'native_audio', 'native_video', 'metadata_hex', 'attachment')
    ),
    ADD CONSTRAINT backup_asset_delivery_grants_profile_check CHECK (
        profile IN ('text_v1', 'text_v2', 'raster_v1', 'pdf_v1', 'audio_v1', 'video_v1', 'hex_v1', 'original_v1')
    ),
    ADD CONSTRAINT backup_asset_delivery_grants_renderer_product_check CHECK (
        (renderer = 'escaped_text' AND profile = 'text_v1' AND range_policy = 'none')
        OR (renderer = 'plain_text' AND profile = 'text_v2' AND range_policy = 'none')
        OR (renderer = 'safe_raster' AND profile = 'raster_v1')
        OR (renderer = 'same_origin_pdf' AND profile = 'pdf_v1')
        OR (renderer = 'native_audio' AND profile = 'audio_v1')
        OR (renderer = 'native_video' AND profile = 'video_v1')
        OR (renderer = 'metadata_hex' AND profile = 'hex_v1' AND range_policy = 'none')
        OR (renderer = 'attachment' AND profile = 'original_v1')
    ),
    ADD CONSTRAINT backup_asset_delivery_grants_representation_product_check CHECK (
        (renderer IN ('safe_raster', 'same_origin_pdf', 'native_audio', 'native_video', 'attachment')
            AND representation_source_bytes = source_size
            AND representation_size = source_size
            AND representation_truncated = FALSE)
        OR (renderer IN ('escaped_text', 'plain_text', 'metadata_hex')
            AND ((representation_truncated = FALSE AND representation_source_bytes = source_size)
                OR (representation_truncated = TRUE AND representation_source_bytes < source_size)))
    );

CREATE OR REPLACE FUNCTION backup_asset_plain_text_content_downgrade_admission()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.version < 73
       AND EXISTS (
           SELECT 1 FROM backup_asset_delivery_grants
           WHERE renderer = 'plain_text' OR profile = 'text_v2'
       ) THEN
        RAISE EXCEPTION '000073 downgrade blocked: plain_text/text_v2 delivery grant exists';
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS trg_backup_asset_plain_text_content_downgrade_admission ON schema_migrations;
CREATE TRIGGER trg_backup_asset_plain_text_content_downgrade_admission
BEFORE INSERT ON schema_migrations
FOR EACH ROW EXECUTE FUNCTION backup_asset_plain_text_content_downgrade_admission();

COMMIT;
