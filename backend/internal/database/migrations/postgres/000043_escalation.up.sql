CREATE TABLE IF NOT EXISTS escalation_policies (
  id BIGSERIAL PRIMARY KEY,
  name VARCHAR(100) NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  min_severity VARCHAR(16) NOT NULL,
  enabled BOOLEAN NOT NULL DEFAULT TRUE,
  levels TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS uk_escalation_policies_name ON escalation_policies(name);

CREATE TABLE IF NOT EXISTS alert_escalation_events (
  id BIGSERIAL PRIMARY KEY,
  alert_id BIGINT NOT NULL REFERENCES alerts(id) ON DELETE CASCADE,
  escalation_policy_id BIGINT REFERENCES escalation_policies(id) ON DELETE SET NULL,
  level_index INTEGER NOT NULL,
  integration_ids TEXT NOT NULL DEFAULT '[]',
  severity_before VARCHAR(16) NOT NULL,
  severity_after VARCHAR(16) NOT NULL,
  tags_added TEXT NOT NULL DEFAULT '[]',
  fired_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS uk_escalation_events_alert_level
  ON alert_escalation_events(alert_id, level_index);

ALTER TABLE alerts ADD COLUMN IF NOT EXISTS tags TEXT NOT NULL DEFAULT '[]';
ALTER TABLE alerts ADD COLUMN IF NOT EXISTS last_level_fired INTEGER NOT NULL DEFAULT -1;

ALTER TABLE tasks ADD COLUMN IF NOT EXISTS escalation_policy_id BIGINT REFERENCES escalation_policies(id) ON DELETE SET NULL;
ALTER TABLE policies ADD COLUMN IF NOT EXISTS escalation_policy_id BIGINT REFERENCES escalation_policies(id) ON DELETE SET NULL;
ALTER TABLE slo_definitions ADD COLUMN IF NOT EXISTS escalation_policy_id BIGINT REFERENCES escalation_policies(id) ON DELETE SET NULL;
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS escalation_policy_id BIGINT REFERENCES escalation_policies(id) ON DELETE SET NULL;
