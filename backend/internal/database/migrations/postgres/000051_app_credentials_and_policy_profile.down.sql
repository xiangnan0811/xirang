BEGIN;

DROP INDEX IF EXISTS idx_policies_app_credential_id;
DROP INDEX IF EXISTS idx_app_credentials_name;

ALTER TABLE policies DROP COLUMN IF EXISTS app_credential_id;
ALTER TABLE policies DROP COLUMN IF EXISTS app_profile;

DROP TABLE IF EXISTS app_credentials;

COMMIT;
