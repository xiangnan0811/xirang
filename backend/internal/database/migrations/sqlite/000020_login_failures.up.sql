CREATE TABLE IF NOT EXISTS login_failures (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    username TEXT NOT NULL DEFAULT '',
    client_ip TEXT NOT NULL DEFAULT '',
    fail_count INTEGER NOT NULL DEFAULT 0,
    locked_until DATETIME,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_login_failures_user_ip ON login_failures(username, client_ip);
