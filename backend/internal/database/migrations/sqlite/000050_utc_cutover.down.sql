-- 000050_utc_cutover.down.sql
--
-- 回滚 UTC 平移：把所有 timestamp 列加回 +8h，恢复到本地时区 (Asia/Shanghai) 写入的状态。
-- 同样**不可幂等执行**；只在 up 紧接失败时立即调用，配合还原 GORM NowFunc/DSN 修改。
--
-- 事务保护：本迁移由 golang-migrate sqlite3 驱动在 driver 层使用 tx.Begin/Commit
-- 包裹整个 .sql 内容，所以以下 UPDATE 已被原子化执行。不要在本文件中再写显式
-- BEGIN/COMMIT —— sqlite3 不支持嵌套事务。详见 up.sql 顶部说明。

UPDATE users SET created_at = datetime(created_at, '+8 hours') WHERE created_at IS NOT NULL;
UPDATE users SET updated_at = datetime(updated_at, '+8 hours') WHERE updated_at IS NOT NULL;

UPDATE ssh_keys SET created_at = datetime(created_at, '+8 hours') WHERE created_at IS NOT NULL;
UPDATE ssh_keys SET updated_at = datetime(updated_at, '+8 hours') WHERE updated_at IS NOT NULL;
UPDATE ssh_keys SET last_used_at = datetime(last_used_at, '+8 hours') WHERE last_used_at IS NOT NULL;

UPDATE nodes SET created_at = datetime(created_at, '+8 hours') WHERE created_at IS NOT NULL;
UPDATE nodes SET updated_at = datetime(updated_at, '+8 hours') WHERE updated_at IS NOT NULL;
UPDATE nodes SET last_seen_at = datetime(last_seen_at, '+8 hours') WHERE last_seen_at IS NOT NULL;
UPDATE nodes SET last_backup_at = datetime(last_backup_at, '+8 hours') WHERE last_backup_at IS NOT NULL;
UPDATE nodes SET last_probe_at = datetime(last_probe_at, '+8 hours') WHERE last_probe_at IS NOT NULL;
UPDATE nodes SET maintenance_start = datetime(maintenance_start, '+8 hours') WHERE maintenance_start IS NOT NULL;
UPDATE nodes SET maintenance_end = datetime(maintenance_end, '+8 hours') WHERE maintenance_end IS NOT NULL;
UPDATE nodes SET expiry_date = datetime(expiry_date, '+8 hours') WHERE expiry_date IS NOT NULL;

UPDATE policies SET created_at = datetime(created_at, '+8 hours') WHERE created_at IS NOT NULL;
UPDATE policies SET updated_at = datetime(updated_at, '+8 hours') WHERE updated_at IS NOT NULL;

UPDATE policy_nodes SET created_at = datetime(created_at, '+8 hours') WHERE created_at IS NOT NULL;

UPDATE integrations SET created_at = datetime(created_at, '+8 hours') WHERE created_at IS NOT NULL;
UPDATE integrations SET updated_at = datetime(updated_at, '+8 hours') WHERE updated_at IS NOT NULL;

UPDATE alerts SET created_at = datetime(created_at, '+8 hours') WHERE created_at IS NOT NULL;
UPDATE alerts SET updated_at = datetime(updated_at, '+8 hours') WHERE updated_at IS NOT NULL;
UPDATE alerts SET triggered_at = datetime(triggered_at, '+8 hours') WHERE triggered_at IS NOT NULL;
UPDATE alerts SET last_notified_at = datetime(last_notified_at, '+8 hours') WHERE last_notified_at IS NOT NULL;

UPDATE alert_deliveries SET created_at = datetime(created_at, '+8 hours') WHERE created_at IS NOT NULL;
UPDATE alert_deliveries SET next_retry_at = datetime(next_retry_at, '+8 hours') WHERE next_retry_at IS NOT NULL;

UPDATE tasks SET created_at = datetime(created_at, '+8 hours') WHERE created_at IS NOT NULL;
UPDATE tasks SET updated_at = datetime(updated_at, '+8 hours') WHERE updated_at IS NOT NULL;
UPDATE tasks SET last_run_at = datetime(last_run_at, '+8 hours') WHERE last_run_at IS NOT NULL;
UPDATE tasks SET next_run_at = datetime(next_run_at, '+8 hours') WHERE next_run_at IS NOT NULL;

UPDATE task_runs SET created_at = datetime(created_at, '+8 hours') WHERE created_at IS NOT NULL;
UPDATE task_runs SET updated_at = datetime(updated_at, '+8 hours') WHERE updated_at IS NOT NULL;
UPDATE task_runs SET started_at = datetime(started_at, '+8 hours') WHERE started_at IS NOT NULL;
UPDATE task_runs SET finished_at = datetime(finished_at, '+8 hours') WHERE finished_at IS NOT NULL;

UPDATE task_logs SET created_at = datetime(created_at, '+8 hours') WHERE created_at IS NOT NULL;

UPDATE audit_logs SET created_at = datetime(created_at, '+8 hours') WHERE created_at IS NOT NULL;

UPDATE task_traffic_samples SET created_at = datetime(created_at, '+8 hours') WHERE created_at IS NOT NULL;
UPDATE task_traffic_samples SET run_started_at = datetime(run_started_at, '+8 hours') WHERE run_started_at IS NOT NULL;
UPDATE task_traffic_samples SET sampled_at = datetime(sampled_at, '+8 hours') WHERE sampled_at IS NOT NULL;

UPDATE node_metric_samples SET created_at = datetime(created_at, '+8 hours') WHERE created_at IS NOT NULL;
UPDATE node_metric_samples SET sampled_at = datetime(sampled_at, '+8 hours') WHERE sampled_at IS NOT NULL;

UPDATE node_metric_samples_hourly SET created_at = datetime(created_at, '+8 hours') WHERE created_at IS NOT NULL;
UPDATE node_metric_samples_hourly SET bucket_start = datetime(bucket_start, '+8 hours') WHERE bucket_start IS NOT NULL;

UPDATE node_metric_samples_daily SET created_at = datetime(created_at, '+8 hours') WHERE created_at IS NOT NULL;
UPDATE node_metric_samples_daily SET bucket_start = datetime(bucket_start, '+8 hours') WHERE bucket_start IS NOT NULL;

UPDATE node_owners SET created_at = datetime(created_at, '+8 hours') WHERE created_at IS NOT NULL;

UPDATE report_configs SET created_at = datetime(created_at, '+8 hours') WHERE created_at IS NOT NULL;
UPDATE report_configs SET updated_at = datetime(updated_at, '+8 hours') WHERE updated_at IS NOT NULL;

UPDATE reports SET created_at = datetime(created_at, '+8 hours') WHERE created_at IS NOT NULL;
UPDATE reports SET updated_at = datetime(updated_at, '+8 hours') WHERE updated_at IS NOT NULL;
UPDATE reports SET period_start = datetime(period_start, '+8 hours') WHERE period_start IS NOT NULL;
UPDATE reports SET period_end = datetime(period_end, '+8 hours') WHERE period_end IS NOT NULL;
UPDATE reports SET generated_at = datetime(generated_at, '+8 hours') WHERE generated_at IS NOT NULL;

UPDATE login_failures SET updated_at = datetime(updated_at, '+8 hours') WHERE updated_at IS NOT NULL;
UPDATE login_failures SET locked_until = datetime(locked_until, '+8 hours') WHERE locked_until IS NOT NULL;

UPDATE system_settings SET updated_at = datetime(updated_at, '+8 hours') WHERE updated_at IS NOT NULL;

UPDATE token_revocations SET created_at = datetime(created_at, '+8 hours') WHERE created_at IS NOT NULL;
UPDATE token_revocations SET expires_at = datetime(expires_at, '+8 hours') WHERE expires_at IS NOT NULL;

UPDATE silences SET created_at = datetime(created_at, '+8 hours') WHERE created_at IS NOT NULL;
UPDATE silences SET updated_at = datetime(updated_at, '+8 hours') WHERE updated_at IS NOT NULL;
UPDATE silences SET starts_at = datetime(starts_at, '+8 hours') WHERE starts_at IS NOT NULL;
UPDATE silences SET ends_at = datetime(ends_at, '+8 hours') WHERE ends_at IS NOT NULL;

UPDATE slo_definitions SET created_at = datetime(created_at, '+8 hours') WHERE created_at IS NOT NULL;
UPDATE slo_definitions SET updated_at = datetime(updated_at, '+8 hours') WHERE updated_at IS NOT NULL;

UPDATE node_logs SET created_at = datetime(created_at, '+8 hours') WHERE created_at IS NOT NULL;
UPDATE node_logs SET timestamp = datetime(timestamp, '+8 hours') WHERE timestamp IS NOT NULL;

UPDATE node_log_cursors SET updated_at = datetime(updated_at, '+8 hours') WHERE updated_at IS NOT NULL;

UPDATE dashboards SET created_at = datetime(created_at, '+8 hours') WHERE created_at IS NOT NULL;
UPDATE dashboards SET updated_at = datetime(updated_at, '+8 hours') WHERE updated_at IS NOT NULL;
UPDATE dashboards SET custom_start = datetime(custom_start, '+8 hours') WHERE custom_start IS NOT NULL;
UPDATE dashboards SET custom_end = datetime(custom_end, '+8 hours') WHERE custom_end IS NOT NULL;

UPDATE dashboard_panels SET created_at = datetime(created_at, '+8 hours') WHERE created_at IS NOT NULL;
UPDATE dashboard_panels SET updated_at = datetime(updated_at, '+8 hours') WHERE updated_at IS NOT NULL;

UPDATE escalation_policies SET created_at = datetime(created_at, '+8 hours') WHERE created_at IS NOT NULL;
UPDATE escalation_policies SET updated_at = datetime(updated_at, '+8 hours') WHERE updated_at IS NOT NULL;

UPDATE alert_escalation_events SET fired_at = datetime(fired_at, '+8 hours') WHERE fired_at IS NOT NULL;

UPDATE anomaly_events SET fired_at = datetime(fired_at, '+8 hours') WHERE fired_at IS NOT NULL;
