BEGIN;

-- 000056_automation_rules.down.sql

DROP TABLE IF EXISTS automation_rule_logs;
DROP TABLE IF EXISTS automation_rules;

ALTER TABLE policies DROP COLUMN IF EXISTS skip_next;

COMMIT;
