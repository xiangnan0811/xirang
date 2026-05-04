-- 000050_utc_cutover.down.sql
--
-- 回滚 UTC 平移：把所有 timestamp 列加回 +8h，恢复到本地时区 (Asia/Shanghai) 写入的状态。

UPDATE users SET created_at = created_at + INTERVAL '8 hours' WHERE created_at IS NOT NULL;
UPDATE users SET updated_at = updated_at + INTERVAL '8 hours' WHERE updated_at IS NOT NULL;

UPDATE ssh_keys SET created_at = created_at + INTERVAL '8 hours' WHERE created_at IS NOT NULL;
UPDATE ssh_keys SET updated_at = updated_at + INTERVAL '8 hours' WHERE updated_at IS NOT NULL;
UPDATE ssh_keys SET last_used_at = last_used_at + INTERVAL '8 hours' WHERE last_used_at IS NOT NULL;

UPDATE nodes SET created_at = created_at + INTERVAL '8 hours' WHERE created_at IS NOT NULL;
UPDATE nodes SET updated_at = updated_at + INTERVAL '8 hours' WHERE updated_at IS NOT NULL;
UPDATE nodes SET last_seen_at = last_seen_at + INTERVAL '8 hours' WHERE last_seen_at IS NOT NULL;
UPDATE nodes SET last_backup_at = last_backup_at + INTERVAL '8 hours' WHERE last_backup_at IS NOT NULL;
UPDATE nodes SET last_probe_at = last_probe_at + INTERVAL '8 hours' WHERE last_probe_at IS NOT NULL;
UPDATE nodes SET maintenance_start = maintenance_start + INTERVAL '8 hours' WHERE maintenance_start IS NOT NULL;
UPDATE nodes SET maintenance_end = maintenance_end + INTERVAL '8 hours' WHERE maintenance_end IS NOT NULL;
UPDATE nodes SET expiry_date = expiry_date + INTERVAL '8 hours' WHERE expiry_date IS NOT NULL;

UPDATE policies SET created_at = created_at + INTERVAL '8 hours' WHERE created_at IS NOT NULL;
UPDATE policies SET updated_at = updated_at + INTERVAL '8 hours' WHERE updated_at IS NOT NULL;

UPDATE policy_nodes SET created_at = created_at + INTERVAL '8 hours' WHERE created_at IS NOT NULL;

UPDATE integrations SET created_at = created_at + INTERVAL '8 hours' WHERE created_at IS NOT NULL;
UPDATE integrations SET updated_at = updated_at + INTERVAL '8 hours' WHERE updated_at IS NOT NULL;

UPDATE alerts SET created_at = created_at + INTERVAL '8 hours' WHERE created_at IS NOT NULL;
UPDATE alerts SET updated_at = updated_at + INTERVAL '8 hours' WHERE updated_at IS NOT NULL;
UPDATE alerts SET triggered_at = triggered_at + INTERVAL '8 hours' WHERE triggered_at IS NOT NULL;
UPDATE alerts SET last_notified_at = last_notified_at + INTERVAL '8 hours' WHERE last_notified_at IS NOT NULL;

UPDATE alert_deliveries SET created_at = created_at + INTERVAL '8 hours' WHERE created_at IS NOT NULL;
UPDATE alert_deliveries SET next_retry_at = next_retry_at + INTERVAL '8 hours' WHERE next_retry_at IS NOT NULL;

UPDATE tasks SET created_at = created_at + INTERVAL '8 hours' WHERE created_at IS NOT NULL;
UPDATE tasks SET updated_at = updated_at + INTERVAL '8 hours' WHERE updated_at IS NOT NULL;
UPDATE tasks SET last_run_at = last_run_at + INTERVAL '8 hours' WHERE last_run_at IS NOT NULL;
UPDATE tasks SET next_run_at = next_run_at + INTERVAL '8 hours' WHERE next_run_at IS NOT NULL;

UPDATE task_runs SET created_at = created_at + INTERVAL '8 hours' WHERE created_at IS NOT NULL;
UPDATE task_runs SET updated_at = updated_at + INTERVAL '8 hours' WHERE updated_at IS NOT NULL;
UPDATE task_runs SET started_at = started_at + INTERVAL '8 hours' WHERE started_at IS NOT NULL;
UPDATE task_runs SET finished_at = finished_at + INTERVAL '8 hours' WHERE finished_at IS NOT NULL;

UPDATE task_logs SET created_at = created_at + INTERVAL '8 hours' WHERE created_at IS NOT NULL;

UPDATE audit_logs SET created_at = created_at + INTERVAL '8 hours' WHERE created_at IS NOT NULL;

UPDATE task_traffic_samples SET created_at = created_at + INTERVAL '8 hours' WHERE created_at IS NOT NULL;
UPDATE task_traffic_samples SET run_started_at = run_started_at + INTERVAL '8 hours' WHERE run_started_at IS NOT NULL;
UPDATE task_traffic_samples SET sampled_at = sampled_at + INTERVAL '8 hours' WHERE sampled_at IS NOT NULL;

UPDATE node_metric_samples SET created_at = created_at + INTERVAL '8 hours' WHERE created_at IS NOT NULL;
UPDATE node_metric_samples SET sampled_at = sampled_at + INTERVAL '8 hours' WHERE sampled_at IS NOT NULL;

UPDATE node_metric_samples_hourly SET created_at = created_at + INTERVAL '8 hours' WHERE created_at IS NOT NULL;
UPDATE node_metric_samples_hourly SET bucket_start = bucket_start + INTERVAL '8 hours' WHERE bucket_start IS NOT NULL;

UPDATE node_metric_samples_daily SET created_at = created_at + INTERVAL '8 hours' WHERE created_at IS NOT NULL;
UPDATE node_metric_samples_daily SET bucket_start = bucket_start + INTERVAL '8 hours' WHERE bucket_start IS NOT NULL;

UPDATE node_owners SET created_at = created_at + INTERVAL '8 hours' WHERE created_at IS NOT NULL;

UPDATE report_configs SET created_at = created_at + INTERVAL '8 hours' WHERE created_at IS NOT NULL;
UPDATE report_configs SET updated_at = updated_at + INTERVAL '8 hours' WHERE updated_at IS NOT NULL;

UPDATE reports SET created_at = created_at + INTERVAL '8 hours' WHERE created_at IS NOT NULL;
UPDATE reports SET updated_at = updated_at + INTERVAL '8 hours' WHERE updated_at IS NOT NULL;
UPDATE reports SET period_start = period_start + INTERVAL '8 hours' WHERE period_start IS NOT NULL;
UPDATE reports SET period_end = period_end + INTERVAL '8 hours' WHERE period_end IS NOT NULL;
UPDATE reports SET generated_at = generated_at + INTERVAL '8 hours' WHERE generated_at IS NOT NULL;

UPDATE login_failures SET updated_at = updated_at + INTERVAL '8 hours' WHERE updated_at IS NOT NULL;
UPDATE login_failures SET locked_until = locked_until + INTERVAL '8 hours' WHERE locked_until IS NOT NULL;

UPDATE system_settings SET updated_at = updated_at + INTERVAL '8 hours' WHERE updated_at IS NOT NULL;

UPDATE token_revocations SET created_at = created_at + INTERVAL '8 hours' WHERE created_at IS NOT NULL;
UPDATE token_revocations SET expires_at = expires_at + INTERVAL '8 hours' WHERE expires_at IS NOT NULL;

UPDATE silences SET created_at = created_at + INTERVAL '8 hours' WHERE created_at IS NOT NULL;
UPDATE silences SET updated_at = updated_at + INTERVAL '8 hours' WHERE updated_at IS NOT NULL;
UPDATE silences SET starts_at = starts_at + INTERVAL '8 hours' WHERE starts_at IS NOT NULL;
UPDATE silences SET ends_at = ends_at + INTERVAL '8 hours' WHERE ends_at IS NOT NULL;

UPDATE slo_definitions SET created_at = created_at + INTERVAL '8 hours' WHERE created_at IS NOT NULL;
UPDATE slo_definitions SET updated_at = updated_at + INTERVAL '8 hours' WHERE updated_at IS NOT NULL;

UPDATE node_logs SET created_at = created_at + INTERVAL '8 hours' WHERE created_at IS NOT NULL;
UPDATE node_logs SET timestamp = timestamp + INTERVAL '8 hours' WHERE timestamp IS NOT NULL;

UPDATE node_log_cursors SET updated_at = updated_at + INTERVAL '8 hours' WHERE updated_at IS NOT NULL;

UPDATE dashboards SET created_at = created_at + INTERVAL '8 hours' WHERE created_at IS NOT NULL;
UPDATE dashboards SET updated_at = updated_at + INTERVAL '8 hours' WHERE updated_at IS NOT NULL;
UPDATE dashboards SET custom_start = custom_start + INTERVAL '8 hours' WHERE custom_start IS NOT NULL;
UPDATE dashboards SET custom_end = custom_end + INTERVAL '8 hours' WHERE custom_end IS NOT NULL;

UPDATE dashboard_panels SET created_at = created_at + INTERVAL '8 hours' WHERE created_at IS NOT NULL;
UPDATE dashboard_panels SET updated_at = updated_at + INTERVAL '8 hours' WHERE updated_at IS NOT NULL;

UPDATE escalation_policies SET created_at = created_at + INTERVAL '8 hours' WHERE created_at IS NOT NULL;
UPDATE escalation_policies SET updated_at = updated_at + INTERVAL '8 hours' WHERE updated_at IS NOT NULL;

UPDATE alert_escalation_events SET fired_at = fired_at + INTERVAL '8 hours' WHERE fired_at IS NOT NULL;

UPDATE anomaly_events SET fired_at = fired_at + INTERVAL '8 hours' WHERE fired_at IS NOT NULL;
