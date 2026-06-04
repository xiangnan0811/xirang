ALTER TABLE ssh_keys ADD COLUMN disabled INTEGER NOT NULL DEFAULT 0;
ALTER TABLE ssh_keys ADD COLUMN expires_at DATETIME;
ALTER TABLE ssh_keys ADD COLUMN allowed_purposes TEXT NOT NULL DEFAULT '';
ALTER TABLE ssh_keys ADD COLUMN allowed_node_ids TEXT NOT NULL DEFAULT '';
ALTER TABLE ssh_keys ADD COLUMN allowed_node_tags TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_ssh_keys_expires_at ON ssh_keys(expires_at);

CREATE TABLE IF NOT EXISTS credential_audit_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL DEFAULT 0,
    username TEXT NOT NULL DEFAULT '',
    role TEXT NOT NULL DEFAULT '',
    action TEXT NOT NULL,
    purpose TEXT NOT NULL,
    credential_kind TEXT NOT NULL,
    credential_source TEXT NOT NULL,
    ssh_key_id INTEGER,
    node_id INTEGER,
    task_id INTEGER,
    task_run_id INTEGER,
    policy_id INTEGER,
    outcome TEXT NOT NULL,
    error_message TEXT NOT NULL DEFAULT '',
    metadata TEXT NOT NULL DEFAULT '{}',
    client_ip TEXT NOT NULL DEFAULT '',
    user_agent TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_credential_audit_events_user_id ON credential_audit_events(user_id);
CREATE INDEX IF NOT EXISTS idx_credential_audit_events_username ON credential_audit_events(username);
CREATE INDEX IF NOT EXISTS idx_credential_audit_events_role ON credential_audit_events(role);
CREATE INDEX IF NOT EXISTS idx_credential_audit_events_action ON credential_audit_events(action);
CREATE INDEX IF NOT EXISTS idx_credential_audit_events_purpose ON credential_audit_events(purpose);
CREATE INDEX IF NOT EXISTS idx_credential_audit_events_credential_kind ON credential_audit_events(credential_kind);
CREATE INDEX IF NOT EXISTS idx_credential_audit_events_ssh_key_id ON credential_audit_events(ssh_key_id);
CREATE INDEX IF NOT EXISTS idx_credential_audit_events_node_id ON credential_audit_events(node_id);
CREATE INDEX IF NOT EXISTS idx_credential_audit_events_task_id ON credential_audit_events(task_id);
CREATE INDEX IF NOT EXISTS idx_credential_audit_events_task_run_id ON credential_audit_events(task_run_id);
CREATE INDEX IF NOT EXISTS idx_credential_audit_events_policy_id ON credential_audit_events(policy_id);
CREATE INDEX IF NOT EXISTS idx_credential_audit_events_outcome ON credential_audit_events(outcome);
CREATE INDEX IF NOT EXISTS idx_credential_audit_events_created_at ON credential_audit_events(created_at);
