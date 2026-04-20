CREATE TABLE IF NOT EXISTS slo_definitions (
    id            BIGSERIAL    PRIMARY KEY,
    name          TEXT         NOT NULL,
    metric_type   TEXT         NOT NULL,
    match_tags    JSONB        NULL,
    threshold     DOUBLE PRECISION NOT NULL,
    window_days   INTEGER      NOT NULL DEFAULT 28,
    enabled       BOOLEAN      NOT NULL DEFAULT TRUE,
    created_by    BIGINT       NOT NULL REFERENCES users(id),
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_slo_enabled ON slo_definitions(enabled);
