# 监控、告警与状态页

本文档说明 Xirang 的节点资源监控、HTTP/TCP uptime 监控、公开状态页、异常事件、告警投递和 Prometheus 指标。

## 节点资源监控

Xirang 通过 SSH 探测节点状态，并采样 CPU、内存、磁盘、负载、延迟等指标。节点页面可查看实时状态、历史序列、磁盘容量趋势和异常事件。

相关环境变量：

| 变量 | 默认值 | 说明 |
|---|---|---|
| `NODE_PROBE_INTERVAL` | `5m` | 节点探测间隔，最小 30 秒。 |
| `NODE_PROBE_FAIL_THRESHOLD` | `3` | 连续失败多少次后标记节点离线。 |
| `NODE_PROBE_CONCURRENCY` | `10` | 并发探测数量，生产可按规模提高。 |

## HTTP/TCP Uptime 监控

服务监控从 Xirang 服务端主动探测 HTTP/TCP 端点，不依赖远程节点 SSH。

支持能力：

- HTTP 探测：支持 GET/POST/HEAD，校验状态码。
- TCP 探测：连接指定 host:port。
- Uptime 计算：按小时聚合并展示过去 24 小时可用率。
- 告警联动：服务从 up 变 down 时产生 critical 告警，恢复时自动 resolve。
- 公开状态页：`/status` 无需登录即可访问。

默认探测参数：

| 参数 | 默认值 | 范围 |
|---|---:|---|
| interval | 60 秒 | 5-3600 秒 |
| timeout | 10 秒 | 1-300 秒 |

Web 入口：

- 登录后管理监控项：`/app/service-monitors`
- 公开状态页：`/status`

API：

| 方法 | 路径 | 认证 | 说明 |
|---|---|---|---|
| GET | `/api/v1/service-monitors` | 需要 | 列出监控项 |
| POST | `/api/v1/service-monitors` | 需要 | 创建监控项 |
| GET | `/api/v1/service-monitors/:id` | 需要 | 获取详情 |
| PUT | `/api/v1/service-monitors/:id` | 需要 | 更新监控项 |
| DELETE | `/api/v1/service-monitors/:id` | 需要 | 删除监控项 |
| GET | `/api/v1/status-page` | 公开 | 状态页数据 |

当前限制：

- 不检测 TLS 证书过期。
- 不支持响应体关键字或正则匹配。
- 仅支持单探测源，即 Xirang 服务端自身。
- HTTP headers 通过 JSON 字符串传入。

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
- 告警触发前后节点日志关联。

常用环境变量：

| 变量 | 默认值 | 说明 |
|---|---|---|
| `ALERT_DEDUP_WINDOW` | `10m` | 同节点/任务/错误码的告警去重窗口，`0` 关闭。 |
| `INTEGRATION_BLOCK_PRIVATE_ENDPOINTS` | `true` | 阻断 webhook/slack/telegram 指向私网或回环地址。 |
| `SMTP_HOST` / `SMTP_PORT` / `SMTP_USER` / `SMTP_PASS` | 见配置 | Email 通道配置。 |

## 异常事件

异常检测默认启用，但默认只写入 `anomaly_events`，不升级为告警和外部通知。

相关设置：

| 设置/环境变量 | 默认值 | 说明 |
|---|---|---|
| `anomaly.enabled` / `ANOMALY_ENABLED` | `true` | 异常检测总开关。 |
| `anomaly.alerts_enabled` / `ANOMALY_ALERTS_ENABLED` | `false` | 是否将异常事件升级为告警通知。 |
| `anomaly.events_retention_days` / `ANOMALY_EVENTS_RETENTION_DAYS` | `30` | 异常事件保留天数。 |

检测器包括：

- EWMA 基线异常：对 CPU、内存、负载等节点指标做基线检测。
- 磁盘容量预测：基于历史磁盘用量预测是否将在阈值天数内写满。
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
# 后端直连部署（例如源码运行 SERVER_ADDR=:8080）
curl -fsS http://127.0.0.1:8080/metrics | head

# 设置 METRICS_TOKEN 后
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

可选 remote-write：

| 变量 | 说明 |
|---|---|
| `METRICS_REMOTE_URL` | Prometheus remote-write 端点，留空禁用。 |
| `METRICS_REMOTE_BEARER_TOKEN` | 可选 Bearer Token。 |
| `METRICS_REMOTE_TIMEOUT` | 单次请求超时，默认 `5s`。 |

详细变量见 [环境变量参考](../env-vars.md)。
