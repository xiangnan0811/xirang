BEGIN;

-- 000056_automation_rules.up.sql
-- 事件触发-动作编排：automation_rules + automation_rule_logs

CREATE TABLE IF NOT EXISTS automation_rules (
    id           SERIAL PRIMARY KEY,
    name         VARCHAR(128) NOT NULL,
    description  VARCHAR(255) NOT NULL DEFAULT '',
    event_type   VARCHAR(64)  NOT NULL,
    event_filter TEXT         NOT NULL DEFAULT '{}',
    action_type  VARCHAR(64)  NOT NULL,
    action_config TEXT        NOT NULL DEFAULT '{}',
    enabled      BOOLEAN      NOT NULL DEFAULT true,
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS uk_automation_rules_name ON automation_rules(name);
CREATE INDEX IF NOT EXISTS idx_automation_rules_event_type ON automation_rules(event_type);

CREATE TABLE IF NOT EXISTS automation_rule_logs (
    id          SERIAL PRIMARY KEY,
    rule_id     INTEGER      NOT NULL,
    event_type  VARCHAR(64)  NOT NULL,
    action_type VARCHAR(64)  NOT NULL,
    result      VARCHAR(16)  NOT NULL,
    error       TEXT         NOT NULL DEFAULT '',
    details     TEXT         NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_automation_rule_logs_rule ON automation_rule_logs(rule_id);

-- Policy.skip_next: 自动化规则 pause_policy 动作需要
ALTER TABLE policies ADD COLUMN IF NOT EXISTS skip_next BOOLEAN NOT NULL DEFAULT false;

COMMIT;
