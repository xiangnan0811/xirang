CREATE TABLE IF NOT EXISTS slo_definitions (
    id            INTEGER  PRIMARY KEY AUTOINCREMENT,
    name          TEXT     NOT NULL,
    metric_type   TEXT     NOT NULL,
    match_tags    TEXT     NULL,
    threshold     REAL     NOT NULL,
    window_days   INTEGER  NOT NULL DEFAULT 28,
    enabled       INTEGER  NOT NULL DEFAULT 1,
    created_by    INTEGER  NOT NULL REFERENCES users(id),
    created_at    DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at    DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_slo_enabled ON slo_definitions(enabled);
