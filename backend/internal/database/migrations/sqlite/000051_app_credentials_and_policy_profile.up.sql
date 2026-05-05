-- 000051_app_credentials_and_policy_profile.up.sql
-- 新增 app_credentials 表 + policies 表加 app_profile / app_credential_id 列

CREATE TABLE IF NOT EXISTS app_credentials (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT    NOT NULL,
    type        TEXT    NOT NULL,
    description TEXT    NOT NULL DEFAULT '',
    config      TEXT    NOT NULL DEFAULT '{}',
    created_at  DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at  DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_app_credentials_name ON app_credentials (name);

-- policies: 应用感知备份字段（nullable，留空 = 当前行为）
ALTER TABLE policies ADD COLUMN app_profile         TEXT NOT NULL DEFAULT '';
ALTER TABLE policies ADD COLUMN app_credential_id   INTEGER;
CREATE INDEX IF NOT EXISTS idx_policies_app_credential_id ON policies (app_credential_id);
