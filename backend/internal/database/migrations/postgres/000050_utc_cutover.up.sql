-- 000050_utc_cutover.up.sql
--
-- 一次性把所有历史 timestamp 列从本地时区 (Asia/Shanghai, UTC+8) 平移到 UTC，
-- 配合 GORM NowFunc=time.Now().UTC() 与 PostgreSQL DSN timezone=UTC 形成端到端 UTC 存储。
--
-- WARNING: 本迁移**不可幂等执行** —— 重复运行会让时间偏移翻倍。
-- 仅适用于当前 +8h (Asia/Shanghai) 部署；其他时区部署需要 fork 此文件并改 INTERVAL 数。
-- 必须在 maintenance window 执行；不能 in-flight 写入时迁移（否则 race condition）。
-- 详细 runbook 见 docs/migration-utc-cutover.md
--
-- 平移涉及的所有 (table.column) 与 SQLite 版本一致，详见 sqlite/000050_utc_cutover.up.sql 顶部清单。
-- PostgreSQL 列类型为 timezone-naive TIMESTAMP（无时区），用 INTERVAL '8 hours' 做绝对值平移。
-- WHERE col IS NOT NULL 是显式表达意图：NULL - INTERVAL = NULL 本身不影响，但显式过滤更清晰。
--
-- 事务保护：以下所有 UPDATE 语句包裹在显式 BEGIN/COMMIT 中，确保任一语句失败时整体回滚 ——
-- 避免出现部分列已 -8h、部分列未平移的「双时区污染」永久性脏数据状态。
--
-- 与 SQLite 不同（sqlite3 driver 自身在 driver 层 wrap tx；嵌套 BEGIN 会报错），
-- golang-migrate 的 pgx v5 driver 不在 driver 层 wrap tx（参见 pgx.go runStatement 直接
-- ExecContext），所以 PostgreSQL 端必须由 SQL 文件自己加显式 BEGIN/COMMIT 才能获得原子性。
-- PG 支持嵌套 BEGIN（subsequent BEGIN 报 warning 不报错），所以即使将来驱动改为 wrap tx
-- 也能向前兼容。
--
-- schema_migrations.dirty=1 标记仍会被设置（驱动语义）；下次启动时由 migrator.go 通过
-- ALLOW_DIRTY_STARTUP 守卫拒绝启动，强制运维介入修复。
-- 完整流程见 docs/migration-utc-cutover.md「Rollback」与「Dirty 状态恢复」章节。

BEGIN;

UPDATE users SET created_at = created_at - INTERVAL '8 hours' WHERE created_at IS NOT NULL;
UPDATE users SET updated_at = updated_at - INTERVAL '8 hours' WHERE updated_at IS NOT NULL;

UPDATE ssh_keys SET created_at = created_at - INTERVAL '8 hours' WHERE created_at IS NOT NULL;
UPDATE ssh_keys SET updated_at = updated_at - INTERVAL '8 hours' WHERE updated_at IS NOT NULL;
UPDATE ssh_keys SET last_used_at = last_used_at - INTERVAL '8 hours' WHERE last_used_at IS NOT NULL;

UPDATE nodes SET created_at = created_at - INTERVAL '8 hours' WHERE created_at IS NOT NULL;
UPDATE nodes SET updated_at = updated_at - INTERVAL '8 hours' WHERE updated_at IS NOT NULL;
UPDATE nodes SET last_seen_at = last_seen_at - INTERVAL '8 hours' WHERE last_seen_at IS NOT NULL;
UPDATE nodes SET last_backup_at = last_backup_at - INTERVAL '8 hours' WHERE last_backup_at IS NOT NULL;
UPDATE nodes SET last_probe_at = last_probe_at - INTERVAL '8 hours' WHERE last_probe_at IS NOT NULL;
UPDATE nodes SET maintenance_start = maintenance_start - INTERVAL '8 hours' WHERE maintenance_start IS NOT NULL;
UPDATE nodes SET maintenance_end = maintenance_end - INTERVAL '8 hours' WHERE maintenance_end IS NOT NULL;
UPDATE nodes SET expiry_date = expiry_date - INTERVAL '8 hours' WHERE expiry_date IS NOT NULL;

UPDATE policies SET created_at = created_at - INTERVAL '8 hours' WHERE created_at IS NOT NULL;
UPDATE policies SET updated_at = updated_at - INTERVAL '8 hours' WHERE updated_at IS NOT NULL;

UPDATE policy_nodes SET created_at = created_at - INTERVAL '8 hours' WHERE created_at IS NOT NULL;

UPDATE integrations SET created_at = created_at - INTERVAL '8 hours' WHERE created_at IS NOT NULL;
UPDATE integrations SET updated_at = updated_at - INTERVAL '8 hours' WHERE updated_at IS NOT NULL;

UPDATE alerts SET created_at = created_at - INTERVAL '8 hours' WHERE created_at IS NOT NULL;
UPDATE alerts SET updated_at = updated_at - INTERVAL '8 hours' WHERE updated_at IS NOT NULL;
UPDATE alerts SET triggered_at = triggered_at - INTERVAL '8 hours' WHERE triggered_at IS NOT NULL;
UPDATE alerts SET last_notified_at = last_notified_at - INTERVAL '8 hours' WHERE last_notified_at IS NOT NULL;

UPDATE alert_deliveries SET created_at = created_at - INTERVAL '8 hours' WHERE created_at IS NOT NULL;
UPDATE alert_deliveries SET next_retry_at = next_retry_at - INTERVAL '8 hours' WHERE next_retry_at IS NOT NULL;

UPDATE tasks SET created_at = created_at - INTERVAL '8 hours' WHERE created_at IS NOT NULL;
UPDATE tasks SET updated_at = updated_at - INTERVAL '8 hours' WHERE updated_at IS NOT NULL;
UPDATE tasks SET last_run_at = last_run_at - INTERVAL '8 hours' WHERE last_run_at IS NOT NULL;
UPDATE tasks SET next_run_at = next_run_at - INTERVAL '8 hours' WHERE next_run_at IS NOT NULL;

UPDATE task_runs SET created_at = created_at - INTERVAL '8 hours' WHERE created_at IS NOT NULL;
UPDATE task_runs SET updated_at = updated_at - INTERVAL '8 hours' WHERE updated_at IS NOT NULL;
UPDATE task_runs SET started_at = started_at - INTERVAL '8 hours' WHERE started_at IS NOT NULL;
UPDATE task_runs SET finished_at = finished_at - INTERVAL '8 hours' WHERE finished_at IS NOT NULL;

UPDATE task_logs SET created_at = created_at - INTERVAL '8 hours' WHERE created_at IS NOT NULL;

UPDATE audit_logs SET created_at = created_at - INTERVAL '8 hours' WHERE created_at IS NOT NULL;

UPDATE task_traffic_samples SET created_at = created_at - INTERVAL '8 hours' WHERE created_at IS NOT NULL;
UPDATE task_traffic_samples SET run_started_at = run_started_at - INTERVAL '8 hours' WHERE run_started_at IS NOT NULL;
UPDATE task_traffic_samples SET sampled_at = sampled_at - INTERVAL '8 hours' WHERE sampled_at IS NOT NULL;

UPDATE node_metric_samples SET created_at = created_at - INTERVAL '8 hours' WHERE created_at IS NOT NULL;
UPDATE node_metric_samples SET sampled_at = sampled_at - INTERVAL '8 hours' WHERE sampled_at IS NOT NULL;

UPDATE node_metric_samples_hourly SET created_at = created_at - INTERVAL '8 hours' WHERE created_at IS NOT NULL;
UPDATE node_metric_samples_hourly SET bucket_start = bucket_start - INTERVAL '8 hours' WHERE bucket_start IS NOT NULL;

UPDATE node_metric_samples_daily SET created_at = created_at - INTERVAL '8 hours' WHERE created_at IS NOT NULL;
UPDATE node_metric_samples_daily SET bucket_start = bucket_start - INTERVAL '8 hours' WHERE bucket_start IS NOT NULL;

UPDATE node_owners SET created_at = created_at - INTERVAL '8 hours' WHERE created_at IS NOT NULL;

UPDATE report_configs SET created_at = created_at - INTERVAL '8 hours' WHERE created_at IS NOT NULL;
UPDATE report_configs SET updated_at = updated_at - INTERVAL '8 hours' WHERE updated_at IS NOT NULL;

UPDATE reports SET created_at = created_at - INTERVAL '8 hours' WHERE created_at IS NOT NULL;
UPDATE reports SET updated_at = updated_at - INTERVAL '8 hours' WHERE updated_at IS NOT NULL;
UPDATE reports SET period_start = period_start - INTERVAL '8 hours' WHERE period_start IS NOT NULL;
UPDATE reports SET period_end = period_end - INTERVAL '8 hours' WHERE period_end IS NOT NULL;
UPDATE reports SET generated_at = generated_at - INTERVAL '8 hours' WHERE generated_at IS NOT NULL;

UPDATE login_failures SET updated_at = updated_at - INTERVAL '8 hours' WHERE updated_at IS NOT NULL;
UPDATE login_failures SET locked_until = locked_until - INTERVAL '8 hours' WHERE locked_until IS NOT NULL;

UPDATE system_settings SET updated_at = updated_at - INTERVAL '8 hours' WHERE updated_at IS NOT NULL;

UPDATE token_revocations SET created_at = created_at - INTERVAL '8 hours' WHERE created_at IS NOT NULL;
UPDATE token_revocations SET expires_at = expires_at - INTERVAL '8 hours' WHERE expires_at IS NOT NULL;

UPDATE silences SET created_at = created_at - INTERVAL '8 hours' WHERE created_at IS NOT NULL;
UPDATE silences SET updated_at = updated_at - INTERVAL '8 hours' WHERE updated_at IS NOT NULL;
UPDATE silences SET starts_at = starts_at - INTERVAL '8 hours' WHERE starts_at IS NOT NULL;
UPDATE silences SET ends_at = ends_at - INTERVAL '8 hours' WHERE ends_at IS NOT NULL;

UPDATE slo_definitions SET created_at = created_at - INTERVAL '8 hours' WHERE created_at IS NOT NULL;
UPDATE slo_definitions SET updated_at = updated_at - INTERVAL '8 hours' WHERE updated_at IS NOT NULL;

UPDATE node_logs SET created_at = created_at - INTERVAL '8 hours' WHERE created_at IS NOT NULL;
UPDATE node_logs SET timestamp = timestamp - INTERVAL '8 hours' WHERE timestamp IS NOT NULL;

UPDATE node_log_cursors SET updated_at = updated_at - INTERVAL '8 hours' WHERE updated_at IS NOT NULL;

UPDATE dashboards SET created_at = created_at - INTERVAL '8 hours' WHERE created_at IS NOT NULL;
UPDATE dashboards SET updated_at = updated_at - INTERVAL '8 hours' WHERE updated_at IS NOT NULL;
UPDATE dashboards SET custom_start = custom_start - INTERVAL '8 hours' WHERE custom_start IS NOT NULL;
UPDATE dashboards SET custom_end = custom_end - INTERVAL '8 hours' WHERE custom_end IS NOT NULL;

UPDATE dashboard_panels SET created_at = created_at - INTERVAL '8 hours' WHERE created_at IS NOT NULL;
UPDATE dashboard_panels SET updated_at = updated_at - INTERVAL '8 hours' WHERE updated_at IS NOT NULL;

UPDATE escalation_policies SET created_at = created_at - INTERVAL '8 hours' WHERE created_at IS NOT NULL;
UPDATE escalation_policies SET updated_at = updated_at - INTERVAL '8 hours' WHERE updated_at IS NOT NULL;

UPDATE alert_escalation_events SET fired_at = fired_at - INTERVAL '8 hours' WHERE fired_at IS NOT NULL;

UPDATE anomaly_events SET fired_at = fired_at - INTERVAL '8 hours' WHERE fired_at IS NOT NULL;

COMMIT;
