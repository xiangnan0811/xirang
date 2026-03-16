CREATE TABLE IF NOT EXISTS login_failures (
    id BIGSERIAL PRIMARY KEY,
    username TEXT NOT NULL DEFAULT '',
    client_ip TEXT NOT NULL DEFAULT '',
    fail_count INTEGER NOT NULL DEFAULT 0,
    locked_until TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_login_failures_user_ip ON login_failures(username, client_ip);
