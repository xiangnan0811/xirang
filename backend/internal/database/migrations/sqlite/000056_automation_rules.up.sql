-- 000056_automation_rules.up.sql
-- 事件触发-动作编排：automation_rules + automation_rule_logs

CREATE TABLE IF NOT EXISTS automation_rules (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    name         TEXT    NOT NULL,
    description  TEXT    NOT NULL DEFAULT '',
    event_type   TEXT    NOT NULL,
    event_filter TEXT    NOT NULL DEFAULT '{}',
    action_type  TEXT    NOT NULL,
    action_config TEXT   NOT NULL DEFAULT '{}',
    enabled      INTEGER NOT NULL DEFAULT 1,
    created_at   DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at   DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE UNIQUE INDEX IF NOT EXISTS uk_automation_rules_name ON automation_rules(name);
CREATE INDEX IF NOT EXISTS idx_automation_rules_event_type ON automation_rules(event_type);

CREATE TABLE IF NOT EXISTS automation_rule_logs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    rule_id     INTEGER NOT NULL,
    event_type  TEXT    NOT NULL,
    action_type TEXT    NOT NULL,
    result      TEXT    NOT NULL,
    error       TEXT    NOT NULL DEFAULT '',
    details     TEXT    NOT NULL DEFAULT '{}',
    created_at  DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_automation_rule_logs_rule ON automation_rule_logs(rule_id);

-- Policy.skip_next: 自动化规则 pause_policy 动作需要
ALTER TABLE policies ADD COLUMN skip_next INTEGER NOT NULL DEFAULT 0;
