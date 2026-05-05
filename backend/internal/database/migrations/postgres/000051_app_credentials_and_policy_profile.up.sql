BEGIN;

-- 000051_app_credentials_and_policy_profile.up.sql

CREATE TABLE IF NOT EXISTS app_credentials (
    id          SERIAL PRIMARY KEY,
    name        TEXT    NOT NULL,
    type        TEXT    NOT NULL,
    description TEXT    NOT NULL DEFAULT '',
    config      TEXT    NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_app_credentials_name ON app_credentials (name);

ALTER TABLE policies ADD COLUMN IF NOT EXISTS app_profile         TEXT NOT NULL DEFAULT '';
ALTER TABLE policies ADD COLUMN IF NOT EXISTS app_credential_id   INTEGER;
CREATE INDEX IF NOT EXISTS idx_policies_app_credential_id ON policies (app_credential_id);

COMMIT;
