BEGIN;

-- 000057_service_uptime.up.sql
-- HTTP/TCP Uptime 探测：service_monitors + service_uptime_samples

CREATE TABLE IF NOT EXISTS service_monitors (
    id                    SERIAL PRIMARY KEY,
    name                  VARCHAR(128) NOT NULL,
    description           VARCHAR(255) NOT NULL DEFAULT '',
    type                  VARCHAR(16)  NOT NULL,  -- "http" | "tcp"
    target                VARCHAR(512) NOT NULL,  -- URL or host:port
    interval_seconds      INTEGER      NOT NULL DEFAULT 60,
    timeout_seconds       INTEGER      NOT NULL DEFAULT 10,
    http_method           VARCHAR(8)   NOT NULL DEFAULT 'GET',
    http_expected_status  INTEGER      NOT NULL DEFAULT 200,
    http_headers          TEXT         NOT NULL DEFAULT '{}',
    enabled               BOOLEAN      NOT NULL DEFAULT true,
    last_status           VARCHAR(8)   NOT NULL DEFAULT 'unknown',
    uptime_pct            DOUBLE PRECISION NOT NULL DEFAULT 0,
    last_checked_at       TIMESTAMPTZ,
    created_at            TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS uk_service_monitors_name ON service_monitors(name);
CREATE INDEX IF NOT EXISTS idx_service_monitors_enabled ON service_monitors(enabled);

CREATE TABLE IF NOT EXISTS service_uptime_samples (
    id          SERIAL PRIMARY KEY,
    monitor_id  INTEGER        NOT NULL,
    hour        TIMESTAMPTZ    NOT NULL,  -- truncated to hour
    probe_count INTEGER        NOT NULL DEFAULT 0,
    probe_ok    INTEGER        NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_sus_monitor_hour ON service_uptime_samples(monitor_id, hour);

COMMIT;
