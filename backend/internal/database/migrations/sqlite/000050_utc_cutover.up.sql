-- 000050_utc_cutover.up.sql
--
-- 一次性把所有历史 timestamp 列从本地时区 (Asia/Shanghai, UTC+8) 平移到 UTC，
-- 配合 GORM NowFunc=time.Now().UTC() 与 SQLite DSN _loc=UTC 形成端到端 UTC 存储。
--
-- WARNING: 本迁移**不可幂等执行** —— 重复运行会让时间偏移翻倍。
-- 仅适用于当前 +8h (Asia/Shanghai) 部署；其他时区部署需要 fork 此文件并改 hour 数。
-- 必须在 maintenance window 执行；不能 in-flight 写入时迁移（否则 race condition）。
-- 详细 runbook 见 docs/migration-utc-cutover.md
--
-- 平移涉及的所有 (table.column)：
--   users.created_at, users.updated_at
--   ssh_keys.created_at, ssh_keys.updated_at, ssh_keys.last_used_at
--   nodes.created_at, nodes.updated_at, nodes.last_seen_at, nodes.last_backup_at,
--     nodes.last_probe_at, nodes.maintenance_start, nodes.maintenance_end, nodes.expiry_date
--   policies.created_at, policies.updated_at
--   policy_nodes.created_at
--   integrations.created_at, integrations.updated_at
--   alerts.created_at, alerts.updated_at, alerts.triggered_at, alerts.last_notified_at
--   alert_deliveries.created_at, alert_deliveries.next_retry_at
--   tasks.created_at, tasks.updated_at, tasks.last_run_at, tasks.next_run_at
--   task_runs.created_at, task_runs.updated_at, task_runs.started_at, task_runs.finished_at
--   task_logs.created_at
--   audit_logs.created_at
--   task_traffic_samples.created_at, task_traffic_samples.run_started_at, task_traffic_samples.sampled_at
--   node_metric_samples.created_at, node_metric_samples.sampled_at
--   node_metric_samples_hourly.created_at, node_metric_samples_hourly.bucket_start
--   node_metric_samples_daily.created_at, node_metric_samples_daily.bucket_start
--   node_owners.created_at
--   report_configs.created_at, report_configs.updated_at
--   reports.created_at, reports.updated_at, reports.period_start, reports.period_end, reports.generated_at
--   login_failures.updated_at, login_failures.locked_until
--   system_settings.updated_at
--   token_revocations.created_at, token_revocations.expires_at
--   silences.created_at, silences.updated_at, silences.starts_at, silences.ends_at
--   slo_definitions.created_at, slo_definitions.updated_at
--   node_logs.created_at, node_logs.timestamp
--   node_log_cursors.updated_at
--   dashboards.created_at, dashboards.updated_at, dashboards.custom_start, dashboards.custom_end
--   dashboard_panels.created_at, dashboard_panels.updated_at
--   escalation_policies.created_at, escalation_policies.updated_at
--   alert_escalation_events.fired_at
--   anomaly_events.fired_at

-- core
UPDATE users SET created_at = datetime(created_at, '-8 hours') WHERE created_at IS NOT NULL;
UPDATE users SET updated_at = datetime(updated_at, '-8 hours') WHERE updated_at IS NOT NULL;

UPDATE ssh_keys SET created_at = datetime(created_at, '-8 hours') WHERE created_at IS NOT NULL;
UPDATE ssh_keys SET updated_at = datetime(updated_at, '-8 hours') WHERE updated_at IS NOT NULL;
UPDATE ssh_keys SET last_used_at = datetime(last_used_at, '-8 hours') WHERE last_used_at IS NOT NULL;

UPDATE nodes SET created_at = datetime(created_at, '-8 hours') WHERE created_at IS NOT NULL;
UPDATE nodes SET updated_at = datetime(updated_at, '-8 hours') WHERE updated_at IS NOT NULL;
UPDATE nodes SET last_seen_at = datetime(last_seen_at, '-8 hours') WHERE last_seen_at IS NOT NULL;
UPDATE nodes SET last_backup_at = datetime(last_backup_at, '-8 hours') WHERE last_backup_at IS NOT NULL;
UPDATE nodes SET last_probe_at = datetime(last_probe_at, '-8 hours') WHERE last_probe_at IS NOT NULL;
UPDATE nodes SET maintenance_start = datetime(maintenance_start, '-8 hours') WHERE maintenance_start IS NOT NULL;
UPDATE nodes SET maintenance_end = datetime(maintenance_end, '-8 hours') WHERE maintenance_end IS NOT NULL;
UPDATE nodes SET expiry_date = datetime(expiry_date, '-8 hours') WHERE expiry_date IS NOT NULL;

UPDATE policies SET created_at = datetime(created_at, '-8 hours') WHERE created_at IS NOT NULL;
UPDATE policies SET updated_at = datetime(updated_at, '-8 hours') WHERE updated_at IS NOT NULL;

UPDATE policy_nodes SET created_at = datetime(created_at, '-8 hours') WHERE created_at IS NOT NULL;

UPDATE integrations SET created_at = datetime(created_at, '-8 hours') WHERE created_at IS NOT NULL;
UPDATE integrations SET updated_at = datetime(updated_at, '-8 hours') WHERE updated_at IS NOT NULL;

-- alerts + deliveries
UPDATE alerts SET created_at = datetime(created_at, '-8 hours') WHERE created_at IS NOT NULL;
UPDATE alerts SET updated_at = datetime(updated_at, '-8 hours') WHERE updated_at IS NOT NULL;
UPDATE alerts SET triggered_at = datetime(triggered_at, '-8 hours') WHERE triggered_at IS NOT NULL;
UPDATE alerts SET last_notified_at = datetime(last_notified_at, '-8 hours') WHERE last_notified_at IS NOT NULL;

UPDATE alert_deliveries SET created_at = datetime(created_at, '-8 hours') WHERE created_at IS NOT NULL;
UPDATE alert_deliveries SET next_retry_at = datetime(next_retry_at, '-8 hours') WHERE next_retry_at IS NOT NULL;

-- tasks
UPDATE tasks SET created_at = datetime(created_at, '-8 hours') WHERE created_at IS NOT NULL;
UPDATE tasks SET updated_at = datetime(updated_at, '-8 hours') WHERE updated_at IS NOT NULL;
UPDATE tasks SET last_run_at = datetime(last_run_at, '-8 hours') WHERE last_run_at IS NOT NULL;
UPDATE tasks SET next_run_at = datetime(next_run_at, '-8 hours') WHERE next_run_at IS NOT NULL;

UPDATE task_runs SET created_at = datetime(created_at, '-8 hours') WHERE created_at IS NOT NULL;
UPDATE task_runs SET updated_at = datetime(updated_at, '-8 hours') WHERE updated_at IS NOT NULL;
UPDATE task_runs SET started_at = datetime(started_at, '-8 hours') WHERE started_at IS NOT NULL;
UPDATE task_runs SET finished_at = datetime(finished_at, '-8 hours') WHERE finished_at IS NOT NULL;

UPDATE task_logs SET created_at = datetime(created_at, '-8 hours') WHERE created_at IS NOT NULL;

UPDATE audit_logs SET created_at = datetime(created_at, '-8 hours') WHERE created_at IS NOT NULL;

-- traffic / metrics samples
UPDATE task_traffic_samples SET created_at = datetime(created_at, '-8 hours') WHERE created_at IS NOT NULL;
UPDATE task_traffic_samples SET run_started_at = datetime(run_started_at, '-8 hours') WHERE run_started_at IS NOT NULL;
UPDATE task_traffic_samples SET sampled_at = datetime(sampled_at, '-8 hours') WHERE sampled_at IS NOT NULL;

UPDATE node_metric_samples SET created_at = datetime(created_at, '-8 hours') WHERE created_at IS NOT NULL;
UPDATE node_metric_samples SET sampled_at = datetime(sampled_at, '-8 hours') WHERE sampled_at IS NOT NULL;

UPDATE node_metric_samples_hourly SET created_at = datetime(created_at, '-8 hours') WHERE created_at IS NOT NULL;
UPDATE node_metric_samples_hourly SET bucket_start = datetime(bucket_start, '-8 hours') WHERE bucket_start IS NOT NULL;

UPDATE node_metric_samples_daily SET created_at = datetime(created_at, '-8 hours') WHERE created_at IS NOT NULL;
UPDATE node_metric_samples_daily SET bucket_start = datetime(bucket_start, '-8 hours') WHERE bucket_start IS NOT NULL;

-- ownership / reports
UPDATE node_owners SET created_at = datetime(created_at, '-8 hours') WHERE created_at IS NOT NULL;

UPDATE report_configs SET created_at = datetime(created_at, '-8 hours') WHERE created_at IS NOT NULL;
UPDATE report_configs SET updated_at = datetime(updated_at, '-8 hours') WHERE updated_at IS NOT NULL;

UPDATE reports SET created_at = datetime(created_at, '-8 hours') WHERE created_at IS NOT NULL;
UPDATE reports SET updated_at = datetime(updated_at, '-8 hours') WHERE updated_at IS NOT NULL;
UPDATE reports SET period_start = datetime(period_start, '-8 hours') WHERE period_start IS NOT NULL;
UPDATE reports SET period_end = datetime(period_end, '-8 hours') WHERE period_end IS NOT NULL;
UPDATE reports SET generated_at = datetime(generated_at, '-8 hours') WHERE generated_at IS NOT NULL;

-- auth / sessions / settings
UPDATE login_failures SET updated_at = datetime(updated_at, '-8 hours') WHERE updated_at IS NOT NULL;
UPDATE login_failures SET locked_until = datetime(locked_until, '-8 hours') WHERE locked_until IS NOT NULL;

UPDATE system_settings SET updated_at = datetime(updated_at, '-8 hours') WHERE updated_at IS NOT NULL;

UPDATE token_revocations SET created_at = datetime(created_at, '-8 hours') WHERE created_at IS NOT NULL;
UPDATE token_revocations SET expires_at = datetime(expires_at, '-8 hours') WHERE expires_at IS NOT NULL;

-- silences / slo / node logs
UPDATE silences SET created_at = datetime(created_at, '-8 hours') WHERE created_at IS NOT NULL;
UPDATE silences SET updated_at = datetime(updated_at, '-8 hours') WHERE updated_at IS NOT NULL;
UPDATE silences SET starts_at = datetime(starts_at, '-8 hours') WHERE starts_at IS NOT NULL;
UPDATE silences SET ends_at = datetime(ends_at, '-8 hours') WHERE ends_at IS NOT NULL;

UPDATE slo_definitions SET created_at = datetime(created_at, '-8 hours') WHERE created_at IS NOT NULL;
UPDATE slo_definitions SET updated_at = datetime(updated_at, '-8 hours') WHERE updated_at IS NOT NULL;

UPDATE node_logs SET created_at = datetime(created_at, '-8 hours') WHERE created_at IS NOT NULL;
UPDATE node_logs SET timestamp = datetime(timestamp, '-8 hours') WHERE timestamp IS NOT NULL;

UPDATE node_log_cursors SET updated_at = datetime(updated_at, '-8 hours') WHERE updated_at IS NOT NULL;

-- dashboards
UPDATE dashboards SET created_at = datetime(created_at, '-8 hours') WHERE created_at IS NOT NULL;
UPDATE dashboards SET updated_at = datetime(updated_at, '-8 hours') WHERE updated_at IS NOT NULL;
UPDATE dashboards SET custom_start = datetime(custom_start, '-8 hours') WHERE custom_start IS NOT NULL;
UPDATE dashboards SET custom_end = datetime(custom_end, '-8 hours') WHERE custom_end IS NOT NULL;

UPDATE dashboard_panels SET created_at = datetime(created_at, '-8 hours') WHERE created_at IS NOT NULL;
UPDATE dashboard_panels SET updated_at = datetime(updated_at, '-8 hours') WHERE updated_at IS NOT NULL;

-- escalation / anomaly
UPDATE escalation_policies SET created_at = datetime(created_at, '-8 hours') WHERE created_at IS NOT NULL;
UPDATE escalation_policies SET updated_at = datetime(updated_at, '-8 hours') WHERE updated_at IS NOT NULL;

UPDATE alert_escalation_events SET fired_at = datetime(fired_at, '-8 hours') WHERE fired_at IS NOT NULL;

UPDATE anomaly_events SET fired_at = datetime(fired_at, '-8 hours') WHERE fired_at IS NOT NULL;
