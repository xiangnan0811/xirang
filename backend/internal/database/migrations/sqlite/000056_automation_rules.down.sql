-- 000056_automation_rules.down.sql

DROP TABLE IF EXISTS automation_rule_logs;
DROP TABLE IF EXISTS automation_rules;

-- Note: SQLite does not support DROP COLUMN via ALTER TABLE.
-- skip_next on policies is harmless to leave; the model default (false) is safe.
