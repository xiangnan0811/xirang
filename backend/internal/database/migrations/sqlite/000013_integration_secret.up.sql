-- 000013_integration_secret.up.sql
-- F13: 额外通知渠道 - Integration 新增 Secret 加密字段

ALTER TABLE integrations ADD COLUMN secret TEXT NOT NULL DEFAULT '';
