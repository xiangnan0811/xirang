# 息壤（XiRang / X-Soil）

息壤是一个基于 **Rsync** 的集中备份管理平台：

- 管理多台 VPS 节点
- 配置备份策略与定时任务
- 手动/自动执行备份任务
- 通过 WebSocket 实时查看任务日志
- 支持响应式控制台与暗黑模式

> 名字寓意来自《山海经》“息壤”：自适应增长、永不耗减。

---

## 技术栈

- **后端**：Go + Gin + GORM + JWT + robfig/cron + gorilla/websocket
- **数据库**：SQLite（默认）/ PostgreSQL（`DB_TYPE=postgres` 切换）
- **前端**：React + TypeScript + Tailwind CSS + shadcn 风格组件 + Lucide Icons

---

## 目录结构

```text
.
├── backend
│   ├── cmd/server/main.go
│   ├── internal
│   │   ├── api
│   │   ├── auth
│   │   ├── middleware
│   │   ├── model
│   │   ├── task
│   │   └── ws
│   └── .env.example
├── web
│   ├── src
│   │   ├── components
│   │   ├── context
│   │   ├── hooks
│   │   ├── lib
│   │   └── pages
│   └── package.json
├── docs/plans
├── docker-compose.yml
└── Makefile
```

---

## 快速启动（本地开发）

### 1) 后端

```bash
cd backend
cp .env.example .env  # 可选
# 首次启动必须设置初始管理员密码
export ADMIN_INITIAL_PASSWORD='请替换为强密码'
go mod tidy
go run ./cmd/server
```

默认地址：`http://localhost:8080`

### 2) 前端

```bash
cd web
npm install
npm run dev
```

默认地址：`http://localhost:5173`

如果你直接连本地后端，建议设置：

```bash
export VITE_API_BASE_URL=http://localhost:8080/api/v1
```

### 3) 一键冒烟验收（推荐）

在前后端服务已启动后执行：

```bash
BASE_URL=http://127.0.0.1:5173/api/v1 \
ADMIN_USERNAME=admin \
ADMIN_PASSWORD='请替换为管理员密码' \
bash scripts/smoke-e2e.sh
# 或先导出凭据后直接
export ADMIN_USERNAME=admin
export ADMIN_PASSWORD='请替换为管理员密码'
make e2e-check
```

该脚本会自动验证：登录、通知通道、SSH Key、节点增改测、策略增改、任务增查触发、通知统计、审计查询与 CSV 导出，并在结束时自动清理测试数据。

---

## 一键开发（Docker Compose）

```bash
docker compose up
```

- 前端：`http://localhost:5173`
- 后端：`http://localhost:8080`

---

## 生产部署（Nginx 反向代理 + HTTPS）

1. 准备生产环境变量：

```bash
cp backend/.env.production.example backend/.env.production
# 修改 JWT_SECRET、DB_DSN、CORS_ALLOWED_ORIGINS
```

2. 准备证书：

```text
deploy/certs/fullchain.pem
deploy/certs/privkey.pem
```

3. 启动生产编排：

```bash
make prod-up
```

4. 停止生产编排：

```bash
make prod-down
```

说明：

- `backend/Dockerfile`：后端生产镜像
- `deploy/nginx/Dockerfile`：前端静态资源 + Nginx 网关镜像
- `docker-compose.prod.yml`：生产编排文件（默认 `ENABLE_TLS=true`）
- `deploy/nginx/README.md`：网关与证书说明

---

## 初始化账号策略

后端首次启动仅自动创建 `admin` 账号，且必须通过
`ADMIN_INITIAL_PASSWORD` 提供初始密码。

- 不再自动创建 `operator`/`viewer`
- 需要多角色时，由 `admin` 登录后手工创建
- 禁止使用弱口令示例（如 `REDACTED`）

---

## 核心接口（v1）

- `POST /api/v1/auth/login`
- `GET /api/v1/me`
- `GET /api/v1/overview`
- `GET|POST|PUT|DELETE /api/v1/nodes`
- `POST /api/v1/nodes/:id/test-connection`
- `GET|POST|PUT|DELETE /api/v1/ssh-keys`
- `GET|POST|PUT|DELETE /api/v1/policies`
- `GET|POST|PUT|DELETE /api/v1/tasks`
- `GET|POST|PUT|DELETE /api/v1/integrations`
- `POST /api/v1/integrations/:id/test`
- `GET /api/v1/alerts`
- `GET /api/v1/alerts/:id`
- `GET /api/v1/alerts/:id/deliveries`
- `POST /api/v1/alerts/:id/ack`
- `POST /api/v1/alerts/:id/resolve`
- `POST /api/v1/alerts/:id/retry-delivery`
- `POST /api/v1/alerts/:id/retry-failed-deliveries`
- `GET /api/v1/alerts/delivery-stats`
- `GET /api/v1/audit-logs`（仅管理员）
- `GET /api/v1/audit-logs/export`（仅管理员）
- `POST /api/v1/tasks/:id/trigger`
- `POST /api/v1/tasks/:id/cancel`
- `GET /api/v1/tasks/:id/logs`
- `GET /api/v1/ws/logs?task_id=<id>&since_id=<last_log_id>`

### 本轮接口补充（筛选与投递查询）

1) 任务筛选：`GET /api/v1/tasks`

- 查询参数：
  - `status`：按任务状态筛选（如 `pending/running/failed/success/retrying/canceled`）
  - `node_id`：按节点 ID 筛选
  - `policy_id`：按策略 ID 筛选
  - `keyword`：模糊匹配 `name/command/rsync_source/rsync_target`
  - `limit`：返回条数，`1~500`
  - `offset`：分页偏移，`>=0`
  - `sort`：排序字段，默认 `id asc`，支持 `-id`、`created_at:desc`、`next_run_at desc` 等

2) 日志筛选：`GET /api/v1/tasks/:id/logs`

- 查询参数：
  - `level`：日志级别筛选（大小写不敏感，如 `error`）
  - `before_id`：游标分页，仅返回 `id < before_id` 的更早日志
  - `limit`：返回条数，默认 `200`，最大 `500`

3) 告警投递查询：`GET /api/v1/alerts/:id/deliveries`

- 作用：查询某条告警对应的投递记录（`alert_deliveries`）
- 返回顺序：按 `id desc`
- 权限：`alerts:deliveries`（角色模型中 `admin/operator/viewer` 可分配）

示例：

```bash
# 任务筛选
curl -H "Authorization: Bearer <jwt>" \
  "http://localhost:8080/api/v1/tasks?status=failed&keyword=rsync&sort=-id&limit=20"

# 任务日志筛选
curl -H "Authorization: Bearer <jwt>" \
  "http://localhost:8080/api/v1/tasks/12/logs?level=error&before_id=500&limit=50"

# 告警投递记录
curl -H "Authorization: Bearer <jwt>" \
  "http://localhost:8080/api/v1/alerts/88/deliveries"
```

---

## 告警与安全配置

后端新增了“真实通知发送 + 告警规则触发 + 敏感字段加密存储”：

- `DATA_ENCRYPTION_KEY`：用于加密 `Node.password`、`Node.private_key`、`SSHKey.private_key`
- `SMTP_HOST/SMTP_PORT/SMTP_USER/SMTP_PASS/SMTP_FROM`：`email` 通道发送配置

- `INTEGRATION_BLOCK_PRIVATE_ENDPOINTS`：阻断 webhook/slack/telegram 私网与回环目标
- `ALERT_DEDUP_WINDOW`：告警去重窗口（同节点+同任务+同错误码）
- `SSH_STRICT_HOST_KEY_CHECKING` / `SSH_KNOWN_HOSTS_PATH`：SSH 严格主机校验
- `RSYNC_ALLOWED_SOURCE_PREFIXES` / `RSYNC_ALLOWED_TARGET_PREFIXES`：rsync 路径白名单
- `RSYNC_MIN_FREE_GB`：本地目标目录最小剩余空间阈值
- `LOGIN_FAIL_LOCK_THRESHOLD` / `LOGIN_FAIL_LOCK_DURATION`：登录失败锁定阈值与时长（用户名+IP）
- `LOGIN_CAPTCHA_ENABLED` / `LOGIN_SECOND_CAPTCHA_ENABLED`：登录验证码字段校验开关
- 生产禁用演示模式开关：建议关闭（示例：`VITE_ENABLE_DEMO_MODE=false`）
- endpoint 协议校验内置开启（webhook/slack/telegram 仅允许 http/https）
- 私网/回环阻断开关：建议开启（示例：`INTEGRATION_BLOCK_PRIVATE_ENDPOINTS=true`）
- SSH 严格主机校验开关：建议开启（示例：`SSH_STRICT_HOST_KEY_CHECKING=true`，可配 `SSH_KNOWN_HOSTS_PATH`）

说明：

- 未配置 SMTP 时，`email` 通道会发送失败，并记录到 `alert_deliveries`（`status=failed`）
- `slack/telegram/webhook` 通道通过 HTTP POST 发送，失败同样写入 `alert_deliveries`
- 节点状态与磁盘数据来自服务端 SSH 主动探测，默认不要求目标机安装 Agent
- 运行中的任务取消会发送进程中断信号并进入 `canceled`；待执行/重试中的任务可直接取消
- rsync 执行前会做前置校验（`rsync_source/rsync_target`、可选路径白名单与最小可用空间）

---

## 生产可观测与故障排查（简明）

常用入口：

- 健康检查：`curl -fsS http://127.0.0.1:8080/healthz`
- 后端日志：`docker compose -f docker-compose.prod.yml logs -f xirang-backend`
- 网关日志：`docker compose -f docker-compose.prod.yml logs -f xirang-gateway`
- 服务状态：`docker compose -f docker-compose.prod.yml ps`

常见排查命令：

```bash
# 1) 快速看最近 200 行后端日志
docker compose -f docker-compose.prod.yml logs --tail=200 xirang-backend

# 2) 检查失败任务
curl -H "Authorization: Bearer <jwt>" \
  "http://localhost:8080/api/v1/tasks?status=failed&sort=-updated_at&limit=20"

# 3) 拉取指定任务 error 日志
curl -H "Authorization: Bearer <jwt>" \
  "http://localhost:8080/api/v1/tasks/<task_id>/logs?level=error&limit=100"

# 4) 查询告警投递失败原因
curl -H "Authorization: Bearer <jwt>" \
  "http://localhost:8080/api/v1/alerts/<alert_id>/deliveries"

# 5)（SQLite）容器内直接查看投递失败明细
docker exec -it xirang-backend sh -lc \
  "sqlite3 /data/xirang.db \"SELECT id,alert_id,integration_id,status,error,created_at FROM alert_deliveries ORDER BY id DESC LIMIT 20;\""
```

详细运维说明见：`docs/production-observability-troubleshooting.md`

---

## CI 持续集成

- 工作流文件：`.github/workflows/ci.yml`
- 触发事件：`push`、`pull_request`
- 后端阶段：`go test ./...` + `go build ./...`
- 前端阶段：`npm test` + `npm run build`

---

## 任务状态机与重试

状态流转：

```text
pending -> running -> success
                \-> failed
                \-> retrying -> running
                \-> canceled
```

失败重试策略：

- 最多重试 **2 次**
- 指数退避：**30s / 90s**

---

## 验证命令（与 CI 对齐）

推荐在仓库根目录执行：

```bash
make backend-test backend-build
make web-test web-build
```

不使用 Makefile 时：

```bash
# 后端
(cd backend && go test ./... && go build ./...)

# 前端
(cd web && npm ci && npm test && npm run build)
```

### E2E 告警链路演示（自动化）

```bash
# 一键执行（默认自动清理演示资源）
make e2e-alert-demo

# 或手动执行并保留演示资源
CLEANUP=0 XR_LOGIN_USERNAME=admin XR_LOGIN_PASSWORD='请替换为管理员密码' bash scripts/e2e-alert-demo.sh
```

- 脚本位置：`scripts/e2e-alert-demo.sh`
- 登录变量：`XR_LOGIN_USERNAME` / `XR_LOGIN_PASSWORD`（避免与系统 `USERNAME` 冲突）
- 详细说明：`docs/e2e-alert-demo.md`

---

## 当前实现说明

- 后端已支持 SQLite / PostgreSQL 切换。
- Rsync 执行器支持真实 `rsync -avz source target`；本地执行器支持模拟输出，方便联调。
- 前端在开发/演示环境可回退到演示数据，便于 UI 演示；生产建议按上文开关禁用该行为。
- 节点连通性与磁盘快照通过服务端主动 SSH 探测获取，默认无需在目标服务器安装客户端（Agent）。
- 已提供生产部署所需 Dockerfile + Nginx 反向代理 + HTTPS 模板。

---

## 安全加固清单

上线前请逐条核对：`docs/security-hardening-checklist.md`

---

## 数据库备份与恢复演练（P0）

已提供脚本：

- `scripts/backup-db.sh`：按 `DB_TYPE` 备份数据库
- `scripts/restore-db.sh`：按 `DB_TYPE` 恢复数据库

### SQLite（默认）

```bash
# 1) 备份（默认从 ./backend/xirang.db 读取）
DB_TYPE=sqlite SQLITE_PATH=./backend/xirang.db \
  bash scripts/backup-db.sh ./backups

# 2) 恢复（恢复前会自动生成 .before-restore 时间戳回滚文件）
DB_TYPE=sqlite SQLITE_PATH=./backend/xirang.db \
  bash scripts/restore-db.sh ./backups/xirang-sqlite-20260215-120000.db
```

### PostgreSQL

```bash
# 1) 备份（custom dump）
DB_TYPE=postgres \
DB_DSN='postgresql://user:pass@127.0.0.1:5432/xirang?sslmode=disable' \
  bash scripts/backup-db.sh ./backups

# 2) 恢复 custom dump（.dump）
DB_TYPE=postgres \
DB_DSN='postgresql://user:pass@127.0.0.1:5432/xirang?sslmode=disable' \
  bash scripts/restore-db.sh ./backups/xirang-postgres-20260215-120000.dump

# 3) 恢复 SQL 文件（.sql）
DB_TYPE=postgres \
DB_DSN='postgresql://user:pass@127.0.0.1:5432/xirang?sslmode=disable' \
  bash scripts/restore-db.sh ./backups/xirang.sql
```

说明：

- PostgreSQL 依赖本机 `pg_dump` / `pg_restore` / `psql` 客户端命令
- 建议将 `./backups` 挂载到独立磁盘并按周期做异地归档
