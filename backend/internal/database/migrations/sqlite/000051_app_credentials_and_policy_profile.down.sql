-- 000051_app_credentials_and_policy_profile.down.sql

DROP INDEX IF EXISTS idx_policies_app_credential_id;
DROP INDEX IF EXISTS idx_app_credentials_name;

-- SQLite ≥3.35: ALTER TABLE ... DROP COLUMN
ALTER TABLE policies DROP COLUMN app_credential_id;
ALTER TABLE policies DROP COLUMN app_profile;

DROP TABLE IF EXISTS app_credentials;
