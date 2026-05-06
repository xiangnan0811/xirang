# HTTP/TCP Uptime 探测 + Status Page

## Goal

从 Xirang 服务器主动对 HTTP/TCP 端点做健康探测，跟踪 uptime，服务 down 时告警，提供对外 Status Page。

## Requirements

### 数据模型（迁移 000057）
- 新增 `service_monitors`：
  - `name` (unique), `description`, `target` (url or host:port)
  - `type` (http/tcp), `interval_seconds` (default 60), `timeout_seconds` (default 10)
  - `http_method` (GET/POST/HEAD), `http_expected_status` (default 200)
  - `http_headers` (JSON, optional)
  - `enabled` (bool)
  - `last_status` (up/down), `last_checked_at`, `uptime_pct` (float, trailing 24h)
- 新增 `service_uptime_samples`（按小时聚合）：
  - `monitor_id`, `hour` (timestamp), `probe_count`, `probe_ok`

### Prober
- 独立 goroutine，60s tick 扫描所有 enabled monitor
- HTTP：`http.Client.Get/Post(url)` with timeout
- TCP：`net.DialTimeout("tcp", host:port, timeout)`
- 结果写入 `service_uptime_samples`（upsert hourly aggregate）
- 同步更新 `service_monitors.last_status` / `uptime_pct`

### Alert
- 状态从 up→down：Raise alert (severity=critical, error_code=XR-SERVICE-DOWN-{monitorID})
- 状态从 down→up：Auto-resolve 之前的 alert

### Status Page
- `GET /api/v1/status-page` — 无需认证的公开端点
- 返回所有 enabled monitor 的 name/type/status/uptime_pct/last_checked
- 前端 Status Page：公开页面 + Dashboard 集成

### API
- CRUD: `/api/v1/service-monitors`

### 兼容性
- 新表，不影响现有 Node probe

## Acceptance Criteria

- [ ] 创建 HTTP monitor → 定时探测 → uptime 正确计算
- [ ] 模拟服务 down → 状态变 down → Alert 触发
- [ ] 服务恢复 → 状态变 up → Alert resolve
- [ ] Status Page 可无认证访问
- [ ] `go test ./...` + `npm run check` 全绿

## Out of Scope

- SSL 证书过期检测（后续）
- 关键字/响应体匹配（MVP 只看状态码+连通性）
- 多探测源

## Decision

### D1: 探测源 — Approach A (Xirang 服务器直接探测)
### D2: 架构 — 独立 Prober goroutine（不依赖 SSH）

## Implementation Plan

| PR | 内容 | 预估 |
|---|---|---|
| **PR1** | 模型+迁移+Prober+Uptime 计算+Alert | 2.5 天 |
| **PR2** | Status Page + 前端管理页 + 文档 | 2 天 |

总计：**~4.5 天**
