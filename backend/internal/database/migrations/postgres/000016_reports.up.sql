CREATE TABLE IF NOT EXISTS report_configs (
    id SERIAL PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    scope_type TEXT NOT NULL DEFAULT 'all',
    scope_value TEXT NOT NULL DEFAULT '',
    period TEXT NOT NULL DEFAULT 'weekly',
    cron TEXT NOT NULL DEFAULT '0 8 * * 1',
    integration_ids TEXT NOT NULL DEFAULT '[]',
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS reports (
    id SERIAL PRIMARY KEY,
    config_id INTEGER NOT NULL REFERENCES report_configs(id) ON DELETE CASCADE,
    period_start TIMESTAMPTZ NOT NULL,
    period_end TIMESTAMPTZ NOT NULL,
    total_runs INTEGER NOT NULL DEFAULT 0,
    success_runs INTEGER NOT NULL DEFAULT 0,
    failed_runs INTEGER NOT NULL DEFAULT 0,
    success_rate FLOAT NOT NULL DEFAULT 0,
    avg_duration_ms BIGINT NOT NULL DEFAULT 0,
    top_failures TEXT NOT NULL DEFAULT '[]',
    disk_trend TEXT NOT NULL DEFAULT '[]',
    generated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_reports_config_id ON reports(config_id);
CREATE INDEX IF NOT EXISTS idx_reports_period_start ON reports(period_start);
