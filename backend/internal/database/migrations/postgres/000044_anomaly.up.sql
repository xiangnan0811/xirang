CREATE TABLE IF NOT EXISTS anomaly_events (
  id BIGSERIAL PRIMARY KEY,
  node_id BIGINT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
  detector VARCHAR(32) NOT NULL,
  metric VARCHAR(32) NOT NULL,
  severity VARCHAR(16) NOT NULL,
  observed_value DOUBLE PRECISION NOT NULL,
  baseline_value DOUBLE PRECISION NOT NULL,
  sigma DOUBLE PRECISION,
  forecast_days DOUBLE PRECISION,
  alert_id BIGINT REFERENCES alerts(id) ON DELETE SET NULL,
  raised_alert BOOLEAN NOT NULL DEFAULT FALSE,
  details TEXT NOT NULL DEFAULT '{}',
  fired_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_anomaly_events_node_fired
  ON anomaly_events(node_id, fired_at DESC);

CREATE INDEX IF NOT EXISTS idx_anomaly_events_detector_fired
  ON anomaly_events(detector, fired_at DESC);
