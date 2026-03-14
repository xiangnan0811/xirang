CREATE TABLE IF NOT EXISTS report_configs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    scope_type TEXT NOT NULL DEFAULT 'all',
    scope_value TEXT NOT NULL DEFAULT '',
    period TEXT NOT NULL DEFAULT 'weekly',
    cron TEXT NOT NULL DEFAULT '0 8 * * 1',
    integration_ids TEXT NOT NULL DEFAULT '[]',
    enabled INTEGER NOT NULL DEFAULT 1,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS reports (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    config_id INTEGER NOT NULL REFERENCES report_configs(id) ON DELETE CASCADE,
    period_start DATETIME NOT NULL,
    period_end DATETIME NOT NULL,
    total_runs INTEGER NOT NULL DEFAULT 0,
    success_runs INTEGER NOT NULL DEFAULT 0,
    failed_runs INTEGER NOT NULL DEFAULT 0,
    success_rate REAL NOT NULL DEFAULT 0,
    avg_duration_ms INTEGER NOT NULL DEFAULT 0,
    top_failures TEXT NOT NULL DEFAULT '[]',
    disk_trend TEXT NOT NULL DEFAULT '[]',
    generated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_reports_config_id ON reports(config_id);
CREATE INDEX IF NOT EXISTS idx_reports_period_start ON reports(period_start);
