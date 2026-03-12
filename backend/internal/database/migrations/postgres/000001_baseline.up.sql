-- 000001_baseline.up.sql (PostgreSQL)
-- 基线迁移：当前完整 schema

CREATE TABLE IF NOT EXISTS users (
    id SERIAL PRIMARY KEY,
    username VARCHAR(64) NOT NULL,
    password_hash VARCHAR(255) NOT NULL,
    role VARCHAR(32) NOT NULL,
    created_at TIMESTAMP,
    updated_at TIMESTAMP
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_username ON users(username);
CREATE INDEX IF NOT EXISTS idx_users_role ON users(role);

CREATE TABLE IF NOT EXISTS ssh_keys (
    id SERIAL PRIMARY KEY,
    name VARCHAR(128) NOT NULL,
    username VARCHAR(128) NOT NULL,
    key_type VARCHAR(32) NOT NULL DEFAULT 'auto',
    private_key TEXT NOT NULL,
    fingerprint VARCHAR(255) NOT NULL,
    last_used_at TIMESTAMP,
    created_at TIMESTAMP,
    updated_at TIMESTAMP
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_ssh_keys_name ON ssh_keys(name);

CREATE TABLE IF NOT EXISTS nodes (
    id SERIAL PRIMARY KEY,
    name VARCHAR(128) NOT NULL,
    host VARCHAR(255) NOT NULL,
    port INTEGER NOT NULL DEFAULT 22,
    username VARCHAR(128) NOT NULL,
    auth_type VARCHAR(32) NOT NULL DEFAULT 'key',
    password VARCHAR(255),
    private_key TEXT,
    ssh_key_id INTEGER,
    tags VARCHAR(512),
    status VARCHAR(32) NOT NULL DEFAULT 'offline',
    base_path VARCHAR(255),
    connection_latency INTEGER NOT NULL DEFAULT 0,
    disk_used_gb INTEGER NOT NULL DEFAULT 0,
    disk_total_gb INTEGER NOT NULL DEFAULT 0,
    last_seen_at TIMESTAMP,
    last_backup_at TIMESTAMP,
    created_at TIMESTAMP,
    updated_at TIMESTAMP
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_nodes_name ON nodes(name);
CREATE INDEX IF NOT EXISTS idx_nodes_ssh_key_id ON nodes(ssh_key_id);

CREATE TABLE IF NOT EXISTS policies (
    id SERIAL PRIMARY KEY,
    name VARCHAR(128) NOT NULL,
    description VARCHAR(255),
    source_path VARCHAR(512) NOT NULL,
    target_path VARCHAR(512) NOT NULL,
    cron_spec VARCHAR(128) NOT NULL,
    exclude_rules TEXT,
    bwlimit INTEGER NOT NULL DEFAULT 0,
    retention_days INTEGER NOT NULL DEFAULT 7,
    max_concurrent INTEGER NOT NULL DEFAULT 1,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMP,
    updated_at TIMESTAMP
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_policies_name ON policies(name);

CREATE TABLE IF NOT EXISTS integrations (
    id SERIAL PRIMARY KEY,
    type VARCHAR(32) NOT NULL,
    name VARCHAR(128) NOT NULL,
    endpoint VARCHAR(1024) NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    fail_threshold INTEGER NOT NULL DEFAULT 1,
    cooldown_minutes INTEGER NOT NULL DEFAULT 5,
    created_at TIMESTAMP,
    updated_at TIMESTAMP
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_integrations_name ON integrations(name);

CREATE TABLE IF NOT EXISTS alerts (
    id SERIAL PRIMARY KEY,
    node_id INTEGER NOT NULL,
    node_name VARCHAR(128) NOT NULL,
    task_id INTEGER,
    policy_name VARCHAR(128),
    severity VARCHAR(16) NOT NULL,
    status VARCHAR(16) NOT NULL,
    error_code VARCHAR(64) NOT NULL,
    message TEXT NOT NULL,
    retryable BOOLEAN NOT NULL DEFAULT FALSE,
    triggered_at TIMESTAMP,
    last_notified_at TIMESTAMP,
    created_at TIMESTAMP,
    updated_at TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_alerts_severity ON alerts(severity);
CREATE INDEX IF NOT EXISTS idx_alerts_status ON alerts(status);
CREATE INDEX IF NOT EXISTS idx_alerts_triggered_at ON alerts(triggered_at);
CREATE INDEX IF NOT EXISTS idx_alerts_task_id ON alerts(task_id);
CREATE INDEX IF NOT EXISTS idx_alerts_dedup ON alerts(node_id, error_code, created_at);

CREATE TABLE IF NOT EXISTS alert_deliveries (
    id SERIAL PRIMARY KEY,
    alert_id INTEGER NOT NULL,
    integration_id INTEGER NOT NULL,
    status VARCHAR(16) NOT NULL,
    error TEXT,
    created_at TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_alert_deliveries_alert_id ON alert_deliveries(alert_id);
CREATE INDEX IF NOT EXISTS idx_alert_deliveries_integration_id ON alert_deliveries(integration_id);

CREATE TABLE IF NOT EXISTS tasks (
    id SERIAL PRIMARY KEY,
    name VARCHAR(128) NOT NULL,
    node_id INTEGER NOT NULL,
    policy_id INTEGER,
    command TEXT,
    rsync_source VARCHAR(512),
    rsync_target VARCHAR(512),
    executor_type VARCHAR(32) NOT NULL DEFAULT 'local',
    cron_spec VARCHAR(128),
    status VARCHAR(32) NOT NULL,
    retry_count INTEGER NOT NULL DEFAULT 0,
    last_error TEXT,
    last_run_at TIMESTAMP,
    next_run_at TIMESTAMP,
    created_at TIMESTAMP,
    updated_at TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_tasks_node_id ON tasks(node_id);
CREATE INDEX IF NOT EXISTS idx_tasks_policy_id ON tasks(policy_id);
CREATE INDEX IF NOT EXISTS idx_tasks_status ON tasks(status);

CREATE TABLE IF NOT EXISTS task_logs (
    id SERIAL PRIMARY KEY,
    task_id INTEGER NOT NULL,
    level VARCHAR(16) NOT NULL,
    message TEXT NOT NULL,
    created_at TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_task_logs_task_id ON task_logs(task_id);
CREATE INDEX IF NOT EXISTS idx_tasklog_task_cursor ON task_logs(task_id, id DESC);

CREATE TABLE IF NOT EXISTS audit_logs (
    id SERIAL PRIMARY KEY,
    user_id INTEGER,
    username VARCHAR(64),
    role VARCHAR(32),
    method VARCHAR(16),
    path VARCHAR(255),
    status_code INTEGER,
    client_ip VARCHAR(64),
    user_agent VARCHAR(255),
    prev_hash VARCHAR(64),
    entry_hash VARCHAR(64),
    created_at TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_audit_logs_user_id ON audit_logs(user_id);
CREATE INDEX IF NOT EXISTS idx_audit_logs_username ON audit_logs(username);
CREATE INDEX IF NOT EXISTS idx_audit_logs_role ON audit_logs(role);
CREATE INDEX IF NOT EXISTS idx_audit_logs_method ON audit_logs(method);
CREATE INDEX IF NOT EXISTS idx_audit_logs_path ON audit_logs(path);
CREATE INDEX IF NOT EXISTS idx_audit_logs_status_code ON audit_logs(status_code);
CREATE INDEX IF NOT EXISTS idx_audit_logs_prev_hash ON audit_logs(prev_hash);
CREATE INDEX IF NOT EXISTS idx_audit_logs_entry_hash ON audit_logs(entry_hash);
CREATE INDEX IF NOT EXISTS idx_audit_logs_created_at ON audit_logs(created_at);

CREATE TABLE IF NOT EXISTS task_traffic_samples (
    id SERIAL PRIMARY KEY,
    task_id INTEGER NOT NULL,
    node_id INTEGER NOT NULL,
    run_started_at TIMESTAMP NOT NULL,
    sampled_at TIMESTAMP NOT NULL,
    throughput_mbps DOUBLE PRECISION NOT NULL DEFAULT 0,
    created_at TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_task_traffic_task_run_sample ON task_traffic_samples(task_id, run_started_at, sampled_at);
CREATE INDEX IF NOT EXISTS idx_task_traffic_sampled_at ON task_traffic_samples(sampled_at);
CREATE INDEX IF NOT EXISTS idx_task_traffic_node_sample ON task_traffic_samples(node_id, sampled_at);
