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
- `CORS_ALLOWED_ORIGINS`：跨域白名单（逗号分隔），默认 `*`
- `EXECUTOR_SHELL`：本地命令执行 shell，默认 `/bin/sh`
- `RSYNC_BINARY`：rsync 可执行文件名，默认 `rsync`

## 默认账号（自动初始化）

- `admin / REDACTED`（管理员）
- `operator / REDACTED`（操作员）
- `viewer / REDACTED`（只读）

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
- 手动触发任务：`POST /api/v1/tasks/:id/trigger`
- 取消任务：`POST /api/v1/tasks/:id/cancel`
- 任务日志：`GET /api/v1/tasks/:id/logs`
- 审计日志查询（仅 admin）：`GET /api/v1/audit-logs`
- 审计日志导出（CSV，仅 admin）：`GET /api/v1/audit-logs/export`
- 告警投递重发：`POST /api/v1/alerts/:id/retry-delivery`
- 告警失败投递批量重发：`POST /api/v1/alerts/:id/retry-failed-deliveries`
- 告警投递统计：`GET /api/v1/alerts/delivery-stats`
- WebSocket 日志：`GET /api/v1/ws/logs?token=<jwt>&task_id=<id>&since_id=<last_log_id>`

## 任务执行说明

- `executor_type=local`：执行 `command`；若 `command` 为空，则模拟 rsync 输出，便于本地验证。
- `executor_type=rsync`：执行真实 rsync。
  - 无节点信息时：`rsync -avz <source> <target>`
  - 有节点信息时：自动拼接为远端源 `user@host:source`，并注入 `ssh -p <port>`。
  - 节点有私钥时：执行前创建临时密钥文件，任务结束后自动清理。

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
