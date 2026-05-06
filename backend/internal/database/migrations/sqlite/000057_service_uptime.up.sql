-- 000057_service_uptime.up.sql
-- HTTP/TCP Uptime 探测：service_monitors + service_uptime_samples

CREATE TABLE IF NOT EXISTS service_monitors (
    id                    INTEGER PRIMARY KEY AUTOINCREMENT,
    name                  TEXT    NOT NULL,
    description           TEXT    NOT NULL DEFAULT '',
    type                  TEXT    NOT NULL,  -- "http" | "tcp"
    target                TEXT    NOT NULL,  -- URL or host:port
    interval_seconds      INTEGER NOT NULL DEFAULT 60,
    timeout_seconds       INTEGER NOT NULL DEFAULT 10,
    http_method           TEXT    NOT NULL DEFAULT 'GET',
    http_expected_status  INTEGER NOT NULL DEFAULT 200,
    http_headers          TEXT    NOT NULL DEFAULT '{}',
    enabled               INTEGER NOT NULL DEFAULT 1,
    last_status           TEXT    NOT NULL DEFAULT 'unknown',
    uptime_pct            REAL    NOT NULL DEFAULT 0,
    last_checked_at       DATETIME,
    created_at            DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at            DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE UNIQUE INDEX IF NOT EXISTS uk_service_monitors_name ON service_monitors(name);
CREATE INDEX IF NOT EXISTS idx_service_monitors_enabled ON service_monitors(enabled);

CREATE TABLE IF NOT EXISTS service_uptime_samples (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    monitor_id  INTEGER NOT NULL,
    hour        DATETIME NOT NULL,  -- truncated to hour
    probe_count INTEGER NOT NULL DEFAULT 0,
    probe_ok    INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_sus_monitor_hour ON service_uptime_samples(monitor_id, hour);
