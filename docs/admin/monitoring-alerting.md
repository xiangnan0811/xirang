# 监控与告警

本文档说明 Xirang 的按需节点连接测试、备份快照异常、告警投递和应用 Prometheus 指标。服务器持续资源监控交由专用监控系统。

## 节点连接与业务状态

节点状态、延迟和最近连接时间仅表示最近一次手动 SSH 测试，不代表实时在线情况。测试不执行磁盘查询；迁移预检与 Doctor 在操作时读取容量，不将容量持久化为节点指标。

节点详情仅展示未解决告警与运行中任务两个业务计数，由 `GET /api/v1/nodes/:id/summary` 按节点读取权限返回。资源采样、历史指标、磁盘预测及旧 metrics/status/metric-series/disk-forecast 接口已移除。

基础 `GET /api/v1/overview` 现在只返回当前身份可见的 `activePolicies`；不在该响应中嵌入节点资源、健康事件、最近任务或任务流量。节点两个业务计数由独立的 `GET /api/v1/nodes/:id/summary` 返回；`/overview/backup-health`、`/overview/backup-confidence` 和 `/overview/storage-usage` 仍是独立的备份接口，不因基础 Overview 收缩而删除。历史成功率、采样吞吐量和运行时长统一在[任务执行与恢复合同的历史任务统计](../spec/domains/task-execution-recovery.md#历史任务统计)和任务页查询。

## 任务日志与历史统计

节点系统日志采集、节点日志配置、节点日志查询和告警关联已退役；Xirang 不再通过 SSH 周期读取 journal 或文件日志。日志页面现仅提供任务及 TaskRun 执行日志；`GET /api/v1/tasks/:id/logs`、`GET /api/v1/task-runs/:id/logs` 和 `/api/v1/ws/logs` 实时任务日志 WebSocket 继续保留，并使用 `tasks:read` 授权。安全审计日志仍由独立审计链保留。

历史成功率、采样吞吐量和运行时长分位的接口、窗口和授权口径见[任务执行与恢复合同的历史任务统计](../spec/domains/task-execution-recovery.md#历史任务统计)。

## 告警与通知

Xirang 支持以下通知渠道：

- Email
- Webhook
- Slack
- Telegram
- 飞书
- 钉钉
- 企业微信

告警能力包括：

- 告警确认与解决。
- 告警去重窗口。
- 投递状态追踪。
- 失败投递重试和批量重试。
- 静默规则。

首次外发前会持久化通道投递意图；进程重启后继续未完成投递，而不是只看到告警已存在就停止。静默、分组、阈值抑制、升级接管和无通道结果有明确持久状态，不会因重放误发。升级前没有投递决定的历史告警不会被批量补发。

界面区分等待发送、发送中、重试中、发送成功、发送失败和未知状态；等待或未知不是已发送。自动与手动重试共享同一投递认领边界。外部渠道收到消息后、数据库回执提交前若进程退出，仍可能重复投递。

飞书、钉钉、企业微信的发送成功要求渠道业务成功码，HTTP 200 本身不足以确认；空、畸形、缺少确认字段或超限响应不能成为 sent。通用 webhook 保留 HTTP 2xx 成功语义。已识别的永久配置拒绝终止自动重试，暂时失败按退避策略重试；错误中不保存响应原文或 webhook 令牌。

无法确定身份的历史通知保持未知，不会盲目删除或自动重发。冷却从可证明的发送成功时间开始计算，未知历史时间不会回填为升级时间。投递、升级、分组、重试和兼容的完整约束见[告警与健康合同](../spec/domains/alerting-health.md)。

职责收敛迁移只封存退役监控/日志来源的历史告警投递：告警行、升级和已发送事实保留，未发送记录以 `unknown`/`feature_retired` 围栏终止自动和手动 claim；`XR-NODE-EXPIRY-*` 到期告警不在退役集合。精确代码集合与状态转换见[告警与健康合同](../spec/domains/alerting-health.md#退役来源告警封存与投递围栏)。

常用环境变量：

| 变量 | 默认值 | 说明 |
|---|---|---|
| `ALERT_DEDUP_WINDOW` | `10m` | 同节点/任务/错误码的告警去重窗口，`0` 关闭。 |
| `INTEGRATION_BLOCK_PRIVATE_ENDPOINTS` | `true` | 阻断 webhook/slack/telegram 指向私网或回环地址。 |
| `SMTP_HOST` / `SMTP_PORT` / `SMTP_USER` / `SMTP_PASS` | 见配置 | Email 通道配置。 |

## 异常事件

备份快照异常默认只写入 `anomaly_events`，不升级为告警和外部通知。周期资源异常检测已退役。

相关设置：

| 设置/环境变量 | 默认值 | 说明 |
|---|---|---|
| `anomaly.alerts_enabled` / `ANOMALY_ALERTS_ENABLED` | `false` | 是否将异常事件升级为告警通知。 |
| `anomaly.events_retention_days` / `ANOMALY_EVENTS_RETENTION_DAYS` | `30` | 异常事件保留天数。 |

检测器包括：

- Restic 快照异常：检测快照变更量异常和勒索后缀，详见 [备份、恢复与快照](backup-recovery.md)。

查看入口：

- 节点详情页的“异常事件”标签。
- 告警详情中的异常上下文。

API：

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/v1/anomaly-events` | 查询异常事件 |
| GET | `/api/v1/nodes/:id/anomaly-events` | 查询指定节点异常事件 |

## Prometheus 指标

`/metrics` 是后端进程提供的 Prometheus 标准指标端点。**生产环境必须配置强 `METRICS_TOKEN`**（非空、≥16 字符、非文档占位符），否则进程拒绝启动。

All-in-One 镜像内置 Nginx 默认只代理 `/api/v1/*`、`/healthz`（进程存活）和 `/readyz`（数据库就绪）以及前端静态资源，不会通过容器入口 `10761` 暴露 `/metrics`。如需抓取指标，请在可信网络中抓取可直达的后端地址，或自行在外层反向代理中将 `/metrics` 转发到后端，并使用 Bearer token。

```bash
# 后端直连部署（例如源码运行 SERVER_ADDR=127.0.0.1:8080）
curl -fsS -H "Authorization: Bearer ${METRICS_TOKEN}" http://127.0.0.1:8080/metrics | head
```

Prometheus scrape 配置示例：

```yaml
scrape_configs:
  - job_name: xirang
    metrics_path: /metrics
    bearer_token_file: /etc/prometheus/secrets/xirang-metrics-token
    static_configs:
      - targets: ['backend-host:8080']  # 替换为 Prometheus 可访问的后端地址
```

节点资源采集、聚合与 remote-write 已退役，旧 `METRICS_REMOTE_*` 配置不再生效。
应用 `/metrics` 仅保留进程和业务可观测性，不替代服务器资源监控。
节点手动测试仅验证 SSH；迁移预检与 Doctor 仍按需检查容量，不周期访问节点。
详细变量见 [环境变量参考](../env-vars.md)。

## 备份资产监控

备份资产默认关闭。请求开启后还须通过[启用门禁](../spec/domains/backup-enablement.md)，请求值与有效状态 `FeatureLive` 不一致通常表示等待就绪或确认，不应直接当作服务宕机。

| 信号 | 指标或日志 | 运维解释 |
|---|---|---|
| 搜索 5xx 比例 | `http_requests_total{method="POST",path="/api/v1/asset-search"}` | 10 分钟窗口内 5xx 占比超过 1%，持续 10 分钟触发建议告警 |
| 搜索 503 | 同一序列的 `status="503"` | 15 分钟内出现即排查；503 也可由就绪等原因产生，须结合 `备份资产搜索审计写入失败` 日志判断审计故障 |
| 请求与有效状态偏差 | `xirang_backup_asset_feature_requested`、`xirang_backup_asset_feature_live` | 差异持续 5 分钟时检查启用条件 |
| 搜索构建失败 | `xirang_backup_asset_search_builds_total{outcome="error"}` | 观察增长并排查构建错误 |
| 遗弃搜索调和 | `xirang_backup_asset_search_reconciled_abandoned_total` | 结合实际遗弃任务判断调和是否推进，不能只凭零增量断言故障 |

后端提供三条 PromQL 建议表达式，启动时只记录规则数量，不会自动向 Prometheus 或 Grafana 安装告警。运维人员须在自己的告警系统中配置表达式、持续时间和接收渠道：

```promql
# backup_asset_search_5xx：持续 10 分钟，级别 page
sum(rate(http_requests_total{method="POST",path="/api/v1/asset-search",status=~"5.."}[10m]))
/
sum(rate(http_requests_total{method="POST",path="/api/v1/asset-search"}[10m]))
> 0.01

# backup_asset_search_audit_fail：立即，级别 page；需结合日志确认原因
increase(http_requests_total{method="POST",path="/api/v1/asset-search",status="503"}[15m]) > 0

# backup_asset_feature_live_jitter：持续 5 分钟，级别 warn
xirang_backup_asset_feature_requested - xirang_backup_asset_feature_live != 0
```

这些表达式不等于“15 分钟内搜索 2xx 达到 99%”的服务保证：第一条只计算 5xx 比例，没有排除 4xx，也没有单独筛选 FeatureLive 请求。关闭 `backup_assets.enabled` 可停止功能准入，但不会恢复已退役的 snapshot 读取 API；旧接口仍返回 410。
