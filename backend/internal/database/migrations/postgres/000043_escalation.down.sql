ALTER TABLE nodes DROP COLUMN IF EXISTS escalation_policy_id;
ALTER TABLE slo_definitions DROP COLUMN IF EXISTS escalation_policy_id;
ALTER TABLE policies DROP COLUMN IF EXISTS escalation_policy_id;
ALTER TABLE tasks DROP COLUMN IF EXISTS escalation_policy_id;

ALTER TABLE alerts DROP COLUMN IF EXISTS last_level_fired;
ALTER TABLE alerts DROP COLUMN IF EXISTS tags;

DROP INDEX IF EXISTS uk_escalation_events_alert_level;
DROP TABLE IF EXISTS alert_escalation_events;
DROP INDEX IF EXISTS uk_escalation_policies_name;
DROP TABLE IF EXISTS escalation_policies;
