CREATE TABLE IF NOT EXISTS credential_access_grants (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    requester_user_id INTEGER NOT NULL,
    requester_username TEXT NOT NULL DEFAULT '',
    requester_role TEXT NOT NULL DEFAULT '',
    action TEXT NOT NULL,
    purpose TEXT NOT NULL,
    node_id INTEGER,
    task_id INTEGER,
    policy_id INTEGER,
    reason TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL,
    requested_ttl_seconds INTEGER NOT NULL DEFAULT 0,
    requested_at DATETIME NOT NULL DEFAULT (datetime('now')),
    approved_at DATETIME,
    approver_user_id INTEGER,
    approver_username TEXT NOT NULL DEFAULT '',
    expires_at DATETIME NOT NULL,
    revoked_at DATETIME,
    revoked_by_user_id INTEGER,
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_credential_access_grants_requester_user_id ON credential_access_grants(requester_user_id);
CREATE INDEX IF NOT EXISTS idx_credential_access_grants_action ON credential_access_grants(action);
CREATE INDEX IF NOT EXISTS idx_credential_access_grants_purpose ON credential_access_grants(purpose);
CREATE INDEX IF NOT EXISTS idx_credential_access_grants_node_id ON credential_access_grants(node_id);
CREATE INDEX IF NOT EXISTS idx_credential_access_grants_task_id ON credential_access_grants(task_id);
CREATE INDEX IF NOT EXISTS idx_credential_access_grants_policy_id ON credential_access_grants(policy_id);
CREATE INDEX IF NOT EXISTS idx_credential_access_grants_status ON credential_access_grants(status);
CREATE INDEX IF NOT EXISTS idx_credential_access_grants_requested_at ON credential_access_grants(requested_at);
CREATE INDEX IF NOT EXISTS idx_credential_access_grants_expires_at ON credential_access_grants(expires_at);
CREATE INDEX IF NOT EXISTS idx_credential_access_grants_approver_user_id ON credential_access_grants(approver_user_id);
CREATE INDEX IF NOT EXISTS idx_credential_access_grants_revoked_by_user_id ON credential_access_grants(revoked_by_user_id);
CREATE INDEX IF NOT EXISTS idx_credential_access_grants_operation ON credential_access_grants(action, purpose, node_id);
