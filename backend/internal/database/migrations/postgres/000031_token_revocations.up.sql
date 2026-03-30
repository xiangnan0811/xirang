CREATE TABLE IF NOT EXISTS token_revocations (
    id         SERIAL PRIMARY KEY,
    token_hash VARCHAR(128) NOT NULL UNIQUE,
    user_id    INTEGER NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_token_revocations_expires_at ON token_revocations(expires_at);
CREATE INDEX IF NOT EXISTS idx_token_revocations_user_id ON token_revocations(user_id);
