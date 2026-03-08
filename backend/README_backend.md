# Xirang Backend MVP

## 概述

这是一个基于 Go + Gin + GORM 的后端 MVP，包含：

- 数据库切换：`DB_TYPE=sqlite|postgres`
- 用户登录：JWT
- 权限控制：RBAC 中间件
- 资源管理：节点 / 策略 / 任务 CRUD
- 任务执行：手动触发 + 状态机
- 重试策略：最多 2 次，指数退避 `30s / 90s`
- 日志推送：WebSocket 框架
- 定时调度：cron 框架

## 快速运行

```bash
cd backend
go mod tidy
go run ./cmd/server
```

默认监听：`127.0.0.1:8080`

## 环境变量

- `SERVER_ADDR`：服务地址，默认 `:8080`
- `DB_TYPE`：数据库类型，`sqlite` 或 `postgres`，默认 `sqlite`
- `SQLITE_PATH`：SQLite 文件路径，默认 `./xirang.db`
- `DB_DSN`：PostgreSQL DSN（仅 `DB_TYPE=postgres` 时必填）
- `JWT_SECRET`：JWT 密钥，默认 `xirang-dev-secret`
- `JWT_TTL`：JWT 有效期，默认 `24h`
- `LOGIN_RATE_LIMIT`：登录限流次数，默认 `10`
- `LOGIN_RATE_WINDOW`：登录限流窗口，默认 `1m`
- `LOGIN_FAIL_LOCK_THRESHOLD`：登录失败锁定阈值（用户名+IP），默认 `5`
- `LOGIN_FAIL_LOCK_DURATION`：登录锁定时长，默认 `15m`
- `LOGIN_CAPTCHA_ENABLED`：登录验证码字段校验开关，默认 `false`
- `LOGIN_SECOND_CAPTCHA_ENABLED`：登录二次验证码字段校验开关，默认 `false`
- `ALERT_DEDUP_WINDOW`：告警去重窗口（同节点+同任务+同错误码），默认 `10m`，`0` 为关闭
- `CORS_ALLOWED_ORIGINS`：跨域白名单（逗号分隔）
  - 若未命中白名单，但 `Origin` 与请求主机同名（忽略端口）也会放行
- `WS_ALLOW_EMPTY_ORIGIN`：是否允许 WebSocket 空 Origin，默认 `false`
- `EXECUTOR_SHELL`：历史参数，当前本地执行器已禁用（保留兼容）
- `RSYNC_BINARY`：rsync 可执行文件名，默认 `rsync`
- `ADMIN_INITIAL_PASSWORD`：首次启动创建 `admin` 的初始密码（必填）

## 初始化账号策略（自动初始化）

- 首次启动仅自动创建 `admin`
- 必须通过 `ADMIN_INITIAL_PASSWORD` 显式提供管理员初始密码
- 不再自动创建 `operator` 与 `viewer`，需由 `admin` 手工创建

## 关键接口

- 登录：`POST /api/v1/auth/login`
- 当前用户：`GET /api/v1/me`
- 节点 CRUD：`/api/v1/nodes`
- 节点连通性测试：`POST /api/v1/nodes/:id/test-connection`
- SSH Key CRUD：`/api/v1/ssh-keys`
- 通知通道 CRUD：`/api/v1/integrations`
- 通知通道测试发送：`POST /api/v1/integrations/:id/test`
- 策略 CRUD：`/api/v1/policies`
- 任务 CRUD：`/api/v1/tasks`
- 概览统计：`GET /api/v1/overview`
- 流量趋势：`GET /api/v1/overview/traffic?window=1h|24h|7d`

## 流量趋势采样与保留

- 后端会在任务执行期间记录吞吐采样，用于概览页 `1h / 24h / 7d` 趋势展示。
- 采样保留天数由 `TASK_TRAFFIC_RETENTION_DAYS` 控制，默认 `8` 天。
- 设置为 `0` 可禁用自动清理。
- 手动触发任务：`POST /api/v1/tasks/:id/trigger`
- 取消任务：`POST /api/v1/tasks/:id/cancel`
- 任务日志：`GET /api/v1/tasks/:id/logs`
- 审计日志查询（仅 admin）：`GET /api/v1/audit-logs`
- 审计日志导出（CSV，仅 admin）：`GET /api/v1/audit-logs/export`
- 告警投递重发：`POST /api/v1/alerts/:id/retry-delivery`
- 告警失败投递批量重发：`POST /api/v1/alerts/:id/retry-failed-deliveries`
- 告警投递统计：`GET /api/v1/alerts/delivery-stats`
- WebSocket 日志：`GET /api/v1/ws/logs?task_id=<id>&since_id=<last_log_id>`（建立连接后发送 `{"type":"auth","token":"<jwt>"}` 进行鉴权）

## 任务执行说明

- `executor_type=rsync`：执行真实 rsync（唯一允许的执行器）。
  - 无节点信息时：`rsync -avz <source> <target>`
  - 有节点信息时：自动拼接为远端源 `user@host:source`，并注入 `ssh -p <port>`。
  - 节点有私钥时：执行前创建临时密钥文件，任务结束后自动清理。
- `executor_type=local` 与 `command` 输入链路已禁用，后端会直接拒绝。

## 测试与格式化

```bash
cd backend
gofmt -w ./cmd ./internal
go test ./...
```

## 数据库备份与恢复脚本

仓库根目录提供：

- `scripts/backup-db.sh`
- `scripts/restore-db.sh`

示例：

```bash
# 仓库根目录执行
DB_TYPE=sqlite SQLITE_PATH=./backend/xirang.db bash scripts/backup-db.sh ./backups
DB_TYPE=sqlite SQLITE_PATH=./backend/xirang.db bash scripts/restore-db.sh ./backups/xirang-sqlite-20260215-120000.db
```
