CREATE TABLE IF NOT EXISTS silences (
    id              BIGSERIAL PRIMARY KEY,
    name            TEXT NOT NULL,
    match_node_id   BIGINT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    match_category  TEXT NULL,
    match_tags      JSONB NULL,
    starts_at       TIMESTAMPTZ NOT NULL,
    ends_at         TIMESTAMPTZ NOT NULL,
    created_by      BIGINT NOT NULL REFERENCES users(id),
    note            TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_silences_active  ON silences(starts_at, ends_at);
CREATE INDEX IF NOT EXISTS idx_silences_cleanup ON silences(ends_at);
