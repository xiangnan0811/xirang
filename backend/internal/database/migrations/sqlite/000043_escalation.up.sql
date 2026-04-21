CREATE TABLE IF NOT EXISTS escalation_policies (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  min_severity TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  levels TEXT NOT NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS uk_escalation_policies_name ON escalation_policies(name);

CREATE TABLE IF NOT EXISTS alert_escalation_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  alert_id INTEGER NOT NULL,
  escalation_policy_id INTEGER,
  level_index INTEGER NOT NULL,
  integration_ids TEXT NOT NULL DEFAULT '[]',
  severity_before TEXT NOT NULL,
  severity_after TEXT NOT NULL,
  tags_added TEXT NOT NULL DEFAULT '[]',
  fired_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY (alert_id) REFERENCES alerts(id) ON DELETE CASCADE,
  FOREIGN KEY (escalation_policy_id) REFERENCES escalation_policies(id) ON DELETE SET NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS uk_escalation_events_alert_level
  ON alert_escalation_events(alert_id, level_index);

ALTER TABLE alerts ADD COLUMN tags TEXT NOT NULL DEFAULT '[]';
ALTER TABLE alerts ADD COLUMN last_level_fired INTEGER NOT NULL DEFAULT -1;

ALTER TABLE tasks ADD COLUMN escalation_policy_id INTEGER;
ALTER TABLE policies ADD COLUMN escalation_policy_id INTEGER;
ALTER TABLE slo_definitions ADD COLUMN escalation_policy_id INTEGER;
ALTER TABLE nodes ADD COLUMN escalation_policy_id INTEGER;
