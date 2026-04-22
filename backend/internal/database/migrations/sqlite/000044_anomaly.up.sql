CREATE TABLE IF NOT EXISTS anomaly_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  node_id INTEGER NOT NULL,
  detector TEXT NOT NULL,
  metric TEXT NOT NULL,
  severity TEXT NOT NULL,
  observed_value REAL NOT NULL,
  baseline_value REAL NOT NULL,
  sigma REAL,
  forecast_days REAL,
  alert_id INTEGER,
  raised_alert INTEGER NOT NULL DEFAULT 0,
  details TEXT NOT NULL DEFAULT '{}',
  fired_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY (node_id) REFERENCES nodes(id) ON DELETE CASCADE,
  FOREIGN KEY (alert_id) REFERENCES alerts(id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS idx_anomaly_events_node_fired
  ON anomaly_events(node_id, fired_at DESC);

CREATE INDEX IF NOT EXISTS idx_anomaly_events_detector_fired
  ON anomaly_events(detector, fired_at DESC);
