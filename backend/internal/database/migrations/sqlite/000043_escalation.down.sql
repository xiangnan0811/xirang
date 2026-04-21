-- SQLite convention in this repo: leave orphan columns on ALTER TABLE rollbacks.
DROP INDEX IF EXISTS uk_escalation_events_alert_level;
DROP TABLE IF EXISTS alert_escalation_events;
DROP INDEX IF EXISTS uk_escalation_policies_name;
DROP TABLE IF EXISTS escalation_policies;
