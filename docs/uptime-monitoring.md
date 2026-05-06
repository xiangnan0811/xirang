# HTTP/TCP Uptime 监控

## 概述

Xirang 支持从服务器主动对 HTTP/TCP 端点进行健康探测，跟踪服务可用性（uptime），并在服务异常时自动触发告警。

## 支持的功能

- **HTTP 探测**：对指定 URL 发送 GET/POST/HEAD 请求，验证状态码是否匹配预期（默认 200）
- **TCP 探测**：对 host:port 发起 TCP 连接，验证端口可达
- **Uptime 计算**：过去 24 小时的可用率百分比，按小时聚合
- **告警联动**：服务从 up 变 down 时自动生成 critical 级别告警（错误码 `XR-SERVICE-DOWN-{monitorID}`），恢复时自动 resolve
- **公开 Status Page**：无需认证的公开状态页 `/status`

## 探测机制

- **探测源**：Xirang 服务器本身（不依赖远程节点 SSH）
- **默认间隔**：60 秒（可配置 5-3600 秒）
- **默认超时**：10 秒（可配置 1-300 秒）
- **实现**：独立 Prober goroutine，每 60 秒扫描所有已启用的 monitor，HTTP 使用 `http.Client`，TCP 使用 `net.DialTimeout`

## Status Page

公开访问地址：`/status`（无需登录）

Status Page 展示所有已启用监控项的：
- 名称、类型（HTTP/TCP）
- 当前状态（绿色=正常，红色=异常，灰色=未知）
- 24 小时可用率百分比
- 最近一次探测时间

页面每 30 秒自动刷新。

## 告警

- **触发条件**：服务状态从 `up` 变为 `down`
- **严重级别**：critical
- **错误码**：`XR-SERVICE-DOWN-{monitorID}`
- **自动恢复**：服务状态从 `down` 变为 `up` 时，之前的告警自动 resolve

## API 端点

| 方法 | 路径 | 认证 | 描述 |
|------|------|------|------|
| GET | `/api/v1/service-monitors` | 需要 | 列出所有监控 |
| POST | `/api/v1/service-monitors` | 需要 | 创建监控 |
| GET | `/api/v1/service-monitors/:id` | 需要 | 获取监控详情 |
| PUT | `/api/v1/service-monitors/:id` | 需要 | 更新监控 |
| DELETE | `/api/v1/service-monitors/:id` | 需要 | 删除监控 |
| GET | `/api/v1/status-page` | **公开** | Status Page 数据 |

## 局限性（MVP）

- 不支持 SSL 证书过期检测
- 不支持响应体关键字/正则匹配（仅看状态码和连通性）
- 仅支持单探测源（Xirang 服务器自身）
- HTTP Headers 通过 JSON 字符串传入

## 数据表

- `service_monitors`：监控配置与当前状态
- `service_uptime_samples`：按小时聚合的探测记录
