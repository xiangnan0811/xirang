# Xirang 后端

## 概述

基于 Go + Gin + GORM 的后端服务，提供完整的服务器运维管理 API。

主要能力：
- 多引擎备份（Rsync / Restic / Rclone）+ 命令执行
- 节点管理与健康探测（SSH 连接、资源采样）
- 任务调度与依赖编排（cron、链式执行、暂停/跳过）
- 多渠道通知（邮件 / Webhook / Slack / Telegram / 飞书 / 钉钉 / 企业微信）
- RBAC 权限控制 + TOTP 两步验证 + 审计日志
- SLA 报告、配置导入导出、系统自助备份

## 快速运行

```bash
cd backend
go mod tidy
go run ./cmd/server
```

默认监听：`127.0.0.1:8080`

## API 接口

所有接口前缀 `/api/v1`，需 JWT 认证的接口标注 🔒。

### 认证与用户

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | /auth/captcha | 获取登录验证码 |
| POST | /auth/login | 用户登录 |
| POST | /auth/2fa/login | TOTP 二次验证登录 |
| GET | /me | 🔒 当前用户信息 |
| POST | /me/onboarded | 🔒 完成新手引导 |
| POST | /auth/logout | 🔒 注销 |
| POST | /auth/change-password | 🔒 修改密码 |
| POST | /auth/2fa/setup | 🔒 配置 TOTP |
| POST | /auth/2fa/verify | 🔒 验证 TOTP |
| POST | /auth/2fa/disable | 🔒 关闭 TOTP |
| GET | /users | 🔒 用户列表 |
| POST | /users | 🔒 创建用户 |
| PUT | /users/:id | 🔒 更新用户 |
| DELETE | /users/:id | 🔒 删除用户 |

### 概览与监控

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | /overview | 🔒 仪表盘概览 |
| GET | /overview/traffic | 🔒 任务流量趋势 |
| GET | /overview/backup-health | 🔒 备份健康状态 |
| GET | /overview/storage-usage | 🔒 存储使用统计 |

### 节点管理

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | /nodes | 🔒 节点列表 |
| GET | /nodes/:id | 🔒 节点详情 |
| POST | /nodes | 🔒 创建节点 |
| POST | /nodes/batch-delete | 🔒 批量删除 |
| PUT | /nodes/:id | 🔒 更新节点 |
| DELETE | /nodes/:id | 🔒 删除节点 |
| POST | /nodes/:id/test-connection | 🔒 测试连接 |
| GET | /nodes/:id/metrics | 🔒 资源指标 |
| GET | /nodes/:id/status | 🔒 节点状态快照（最新采样 + 1h/24h 聚合 + 告警/任务计数） |
| GET | /nodes/:id/files | 🔒 远程文件列表 |
| GET | /nodes/:id/files/content | 🔒 文件内容 |
| GET | /nodes/:id/docker-volumes | 🔒 Docker 卷列表 |
| GET | /nodes/:id/owners | 🔒 节点 owner 列表 |
| POST | /nodes/:id/owners | 🔒 添加 owner |
| DELETE | /nodes/:id/owners/:user_id | 🔒 移除 owner |
| POST | /nodes/:id/emergency-backup | 🔒 紧急备份 |
| POST | /nodes/:id/migrate | 🔒 节点迁移 |
| POST | /nodes/:id/migrate/preflight | 🔒 迁移预检 |

### SSH 密钥

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | /ssh-keys | 🔒 密钥列表（含派生公钥） |
| POST | /ssh-keys | 🔒 创建密钥 |
| POST | /ssh-keys/batch | 🔒 批量创建（最多 50 条） |
| POST | /ssh-keys/batch-delete | 🔒 批量删除（跳过使用中） |
| GET | /ssh-keys/export | 🔒 导出（authorized_keys/json/csv） |
| GET | /ssh-keys/:id | 🔒 密钥详情 |
| PUT | /ssh-keys/:id | 🔒 更新密钥 |
| DELETE | /ssh-keys/:id | 🔒 删除密钥 |
| POST | /ssh-keys/:id/test-connection | 🔒 测试密钥连接节点 |

### 备份策略

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | /policies | 🔒 策略列表 |
| GET | /policies/:id | 🔒 策略详情 |
| POST | /policies | 🔒 创建策略 |
| POST | /policies/batch-toggle | 🔒 批量启停 |
| POST | /policies/from-template/:id | 🔒 从模板创建 |
| PUT | /policies/:id | 🔒 更新策略 |
| DELETE | /policies/:id | 🔒 删除策略 |

### 任务与执行

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | /tasks | 🔒 任务列表 |
| GET | /tasks/:id | 🔒 任务详情 |
| GET | /tasks/:id/logs | 🔒 任务日志 |
| POST | /tasks | 🔒 创建任务 |
| PUT | /tasks/:id | 🔒 更新任务 |
| DELETE | /tasks/:id | 🔒 删除任务 |
| GET | /tasks/:id/runs | 🔒 执行历史 |
| POST | /tasks/batch-trigger | 🔒 批量触发 |
| POST | /tasks/:id/trigger | 🔒 手动触发 |
| POST | /tasks/:id/cancel | 🔒 取消执行 |
| POST | /tasks/:id/pause | 🔒 暂停调度 |
| POST | /tasks/:id/resume | 🔒 恢复调度 |
| POST | /tasks/:id/skip-next | 🔒 跳过下次 |
| POST | /tasks/:id/restore | 🔒 从备份恢复 |
| GET | /tasks/:id/backup-files | 🔒 备份文件列表 |
| GET | /task-runs/:id | 🔒 执行详情 |
| GET | /task-runs/:id/logs | 🔒 执行日志 |

### 批量命令

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | /batch-commands | 🔒 创建批量命令 |
| GET | /batch-commands/:batch_id | 🔒 查询状态 |
| DELETE | /batch-commands/:batch_id | 🔒 取消/删除 |

### 通知集成

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | /integrations | 🔒 渠道列表 |
| GET | /integrations/:id | 🔒 渠道详情 |
| POST | /integrations | 🔒 创建渠道 |
| PUT | /integrations/:id | 🔒 更新渠道 |
| PATCH | /integrations/:id | 🔒 部分更新 |
| POST | /integrations/:id/test | 🔒 测试发送 |
| DELETE | /integrations/:id | 🔒 删除渠道 |

### 告警

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | /alerts | 🔒 告警列表 |
| GET | /alerts/unread-count | 🔒 未读数量 |
| GET | /alerts/:id | 🔒 告警详情 |
| GET | /alerts/delivery-stats | 🔒 投递统计 |
| GET | /alerts/:id/deliveries | 🔒 投递记录 |
| POST | /alerts/:id/ack | 🔒 确认告警 |
| POST | /alerts/:id/resolve | 🔒 解决告警 |
| POST | /alerts/:id/retry-delivery | 🔒 重试投递 |
| POST | /alerts/:id/retry-failed-deliveries | 🔒 批量重试 |

### 审计日志

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | /audit-logs | 🔒 日志列表 |
| GET | /audit-logs/export | 🔒 导出 CSV |

### SLA 报告

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | /report-configs | 🔒 报告配置列表 |
| POST | /report-configs | 🔒 创建配置 |
| PUT | /report-configs/:id | 🔒 更新配置 |
| DELETE | /report-configs/:id | 🔒 删除配置 |
| POST | /report-configs/:id/generate | 🔒 立即生成 |
| GET | /report-configs/:id/reports | 🔒 报告列表 |
| GET | /reports/:id | 🔒 报告详情 |

### 快照与恢复

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | /tasks/:id/snapshots | 🔒 快照列表 |
| GET | /tasks/:id/snapshots/:sid/files | 🔒 快照文件 |
| POST | /tasks/:id/snapshots/:sid/restore | 🔒 从快照恢复 |
| GET | /tasks/:id/snapshots/diff | 🔒 快照对比 |

### 系统设置与配置

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | /settings | 🔒 全部设置 |
| PUT | /settings | 🔒 批量更新 |
| DELETE | /settings/:key | 🔒 删除设置 |
| GET | /config/export | 🔒 导出配置 |
| POST | /config/import | 🔒 导入配置 |
| GET | /hook-templates | 🔒 钩子模板列表 |

### 系统管理

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | /version | 🔒 版本信息 |
| GET | /version/check | 🔒 检查更新 |
| POST | /system/backup-db | 🔒 备份数据库 |
| GET | /system/backups | 🔒 备份列表 |
| POST | /system/verify-mount | 🔒 验证挂载点 |

### WebSocket

| 路径 | 说明 |
|------|------|
| /ws/logs | 实时日志推送（协议内认证） |
| /ws/terminal | Web SSH 终端（协议内认证） |

### 健康检查与监控

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | /healthz | 健康检查（无需认证） |
| GET | /metrics | Prometheus 指标（无需认证） |
| GET | /swagger/*any | Swagger UI（无需认证） |

## 执行器

| 类型 | 说明 |
|------|------|
| `rsync` | 基于 rsync 的文件同步，支持远端源/目标、SSH 密钥注入、带宽限制 |
| `restic` | 加密去重备份，支持仓库初始化、快照管理、进度解析 |
| `rclone` | 云存储同步（S3/MinIO 等），支持进度解析 |
| `command` | 远程 SSH 命令执行（批量命令场景） |

## 环境变量

完整参考见 [docs/env-vars.md](../docs/env-vars.md)。

关键必填项（生产环境）：
- `ADMIN_INITIAL_PASSWORD`：初始 admin 密码
- `JWT_SECRET`：JWT 签名密钥（≥16 字符）
- `DATA_ENCRYPTION_KEY`：敏感字段加密密钥

## 数据库

支持 SQLite（默认）和 PostgreSQL。当前迁移版本：`000030_task_run_progress`。

核心模型：User, SSHKey, Node, Policy, PolicyNode, Integration, Alert, AlertDelivery, Task, TaskRun, TaskLog, TaskTrafficSample, NodeMetricSample, NodeOwner, AuditLog, ReportConfig, Report, LoginFailure, SystemSetting

## 测试

```bash
cd backend
go test ./...
```
