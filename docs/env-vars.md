# 环境变量参考

完整的 Xirang 环境变量列表，按功能分组。示例文件：`backend/.env.example`（开发）、`backend/.env.production.example`（生产）、`web/.env.example`（前端）。

---

## 1. 服务器与环境

| 变量 | 类型 | 默认值 | 必填 | 说明 |
|------|------|--------|------|------|
| `SERVER_ADDR` | string | `:8080` | 否 | 后端监听地址 |
| `APP_ENV` | string | — | 否 | 应用环境（`development` / `production`），影响弱密钥检测等安全策略 |
| `ENVIRONMENT` | string | — | 否 | `APP_ENV` 的回退变量（优先级低于 `APP_ENV`） |
| `GIN_MODE` | string | — | 否 | `APP_ENV` 的回退变量（`debug` = development，`release` = production） |
| `LOG_LEVEL` | string | 空（info） | 否 | 日志级别：`debug` / `info` / `warn` / `error` |

**读取位置**：`SERVER_ADDR` → `config/config.go:54`，`APP_ENV` / `ENVIRONMENT` / `GIN_MODE` → `util/env.go:54-63`，`LOG_LEVEL` → `cmd/server/main.go:26`

## 2. 数据库

| 变量 | 类型 | 默认值 | 必填 | 说明 |
|------|------|--------|------|------|
| `DB_TYPE` | string | `sqlite` | 否 | 数据库类型：`sqlite` / `postgres` |
| `SQLITE_PATH` | string | `./xirang.db` | 否 | SQLite 文件路径 |
| `DB_DSN` | string | — | PG 时必填 | PostgreSQL 连接串，生产建议 `sslmode=require` |

**读取位置**：`config/config.go:55-57`

## 3. 认证与安全

| 变量 | 类型 | 默认值 | 必填 | 说明 |
|------|------|--------|------|------|
| `JWT_SECRET` | string | 开发环境 `xirang-dev-secret` | 生产必填 | JWT 签名密钥，生产环境必须为强随机字符串（≥16 字符） |
| `JWT_TTL` | duration | `24h` | 否 | JWT 有效期 |
| `LOGIN_RATE_LIMIT` | int | `10` | 否 | 登录接口速率限制（次/窗口） |
| `LOGIN_RATE_WINDOW` | duration | `1m` | 否 | 速率限制时间窗口 |
| `LOGIN_FAIL_LOCK_THRESHOLD` | int | `5` | 否 | 连续登录失败多少次后锁定账号 |
| `LOGIN_FAIL_LOCK_DURATION` | duration | `15m` | 否 | 账号锁定持续时间 |
| `LOGIN_CAPTCHA_ENABLED` | bool | `false` | 否 | 启用登录验证码 |
| `LOGIN_SECOND_CAPTCHA_ENABLED` | bool | `false` | 否 | 启用二次验证码 |
| `ADMIN_INITIAL_PASSWORD` | string | — | 首次启动 | 初始 admin 账号密码，仅 bootstrap 阶段使用 |
| `DATA_ENCRYPTION_KEY` | string | 内置开发密钥 | 生产必填 | 敏感字段（密码、私钥）加密密钥，支持 32 字节 base64 或任意字符串（自动 SHA-256 派生） |

**读取位置**：`JWT_SECRET` → `config/config.go:43`，`LOGIN_*` → `config/config.go:85-123`，`ADMIN_INITIAL_PASSWORD` → `bootstrap/bootstrap.go:30`，`DATA_ENCRYPTION_KEY` → `secure/crypto.go:44` + `config/config.go:170`

## 4. 跨域与 WebSocket

| 变量 | 类型 | 默认值 | 必填 | 说明 |
|------|------|--------|------|------|
| `CORS_ALLOWED_ORIGINS` | string | `http://localhost:5173,http://127.0.0.1:5173` | 否 | 跨域白名单（逗号分隔），留空时仅放行同主机 Origin（忽略端口）；生产环境禁止 `*` |
| `WS_ALLOW_EMPTY_ORIGIN` | bool | `false` | 否 | WebSocket 是否允许空 Origin |
| `WS_MAX_CONNECTIONS` | int | `100` | 否 | WebSocket 最大连接数 |

**读取位置**：`CORS_ALLOWED_ORIGINS` → `config/config.go:38`，`WS_ALLOW_EMPTY_ORIGIN` → `config/config.go:146`，`WS_MAX_CONNECTIONS` → `ws/hub.go:54`

## 5. SSH

| 变量 | 类型 | 默认值 | 必填 | 说明 |
|------|------|--------|------|------|
| `SSH_STRICT_HOST_KEY_CHECKING` | bool | `true` | 否 | 严格校验远端主机指纹（`.env.example` 开发值 `false`，生产建议 `true`） |
| `SSH_KNOWN_HOSTS_PATH` | string | `~/.ssh/known_hosts` | 否 | known_hosts 文件路径 |
| `SSH_AUTO_ACCEPT_NEW_HOSTS` | bool | `true` | 否 | 严格校验开启时，是否自动接受首次出现的主机指纹（设为 `false` 可禁用） |

**读取位置**：`SSH_STRICT_HOST_KEY_CHECKING` → `sshutil/ssh_auth.go:126` + `task/executor/executor.go:147`，`SSH_KNOWN_HOSTS_PATH` → `sshutil/ssh_auth.go:135` + `task/executor/executor.go:152`，`SSH_AUTO_ACCEPT_NEW_HOSTS` → `sshutil/ssh_auth.go:155` + `task/executor/executor.go:158`

## 6. 备份与执行

| 变量 | 类型 | 默认值 | 必填 | 说明 |
|------|------|--------|------|------|
| `RSYNC_BINARY` | string | `rsync` | 否 | rsync 可执行文件路径 |
| `RSYNC_ALLOWED_SOURCE_PREFIXES` | string | 空（不限制） | 否 | rsync 源路径白名单（逗号分隔） |
| `RSYNC_ALLOWED_TARGET_PREFIXES` | string | 空（不限制） | 否 | rsync 目标路径白名单（逗号分隔） |
| `RSYNC_MIN_FREE_GB` | int | `0` | 否 | 本地目标目录最小剩余空间（GB），`0` 不检查 |
| `RCLONE_BINARY` | string | `rclone` | 否 | rclone 可执行文件路径 |
| `RESTIC_BINARY` | string | `restic` | 否 | restic 可执行文件路径 |
| `BATCH_COMMAND_BLACKLIST` | string | 空（使用内置规则） | 否 | 批量命令黑名单（逗号分隔正则） |
| `FILE_BROWSER_ALLOW_ALL` | string | 空（禁用） | 否 | 设为 `true` 允许浏览任意路径（默认仅允许备份目录） |

**读取位置**：`RSYNC_BINARY` → `config/config.go:59`，`RSYNC_ALLOWED_*` → `api/handlers/task_handler.go:593-594`，`RSYNC_MIN_FREE_GB` → `task/executor/executor.go:491,628`，`RCLONE_BINARY` → `task/executor/rclone_executor.go:43`，`RESTIC_BINARY` → `task/executor/restic_executor.go:38`，`BATCH_COMMAND_BLACKLIST` → `api/handlers/batch_handler.go:229`，`FILE_BROWSER_ALLOW_ALL` → `api/handlers/file_handler.go:261`

## 7. 节点探测

| 变量 | 类型 | 默认值 | 必填 | 说明 |
|------|------|--------|------|------|
| `NODE_PROBE_INTERVAL` | duration | `5m` | 否 | 探测间隔（最小 30s） |
| `NODE_PROBE_FAIL_THRESHOLD` | int | `3` | 否 | 连续失败多少次标记节点离线 |
| `NODE_PROBE_CONCURRENCY` | int | `10` | 否 | 并发探测数（生产建议 `20`） |

**读取位置**：`config/config.go:125-143`

## 8. 数据保留

| 变量 | 类型 | 默认值 | 必填 | 说明 |
|------|------|--------|------|------|
| `TASK_TRAFFIC_RETENTION_DAYS` | int | `8` | 否 | 任务流量数据保留天数 |
| `TASK_RUN_RETENTION_DAYS` | int | `90` | 否 | 任务执行记录保留天数 |
| `RETENTION_CHECK_INTERVAL` | duration | `6h` | 否 | 备份保留策略检查间隔（最小 1m），定期清理过期备份并检查存储空间 |
| `BACKUP_STORAGE_MIN_FREE_GB` | int | `10` | 否 | 本地备份存储最低剩余空间（GB），低于此值触发告警 |
| `BACKUP_STORAGE_MAX_USAGE_PCT` | int | `90` | 否 | 本地备份存储最大使用率（%），超过此值触发告警 |
| `INTEGRITY_CHECK_MULTIPLIER` | int | `4` | 否 | 完整性检查频率倍数——每隔多少个保留清理周期运行一次 `restic check` / `rclone check`（默认 4，即 `RETENTION_CHECK_INTERVAL=6h` 时每 24h 一次） |

**读取位置**：`config/config.go:64-76`，`RETENTION_CHECK_INTERVAL` → `config/config.go:146`，`BACKUP_STORAGE_*` → `config/config.go:152-164`，`INTEGRITY_CHECK_MULTIPLIER` → `task/retention.go`

## 9. 邮件通知

| 变量 | 类型 | 默认值 | 必填 | 说明 |
|------|------|--------|------|------|
| `SMTP_HOST` | string | — | 启用 email 时 | SMTP 服务器地址，为空时 email 通道失败 |
| `SMTP_PORT` | string | `587` | 否 | SMTP 端口 |
| `SMTP_USER` | string | — | 启用 email 时 | SMTP 用户名 |
| `SMTP_PASS` | string | — | 启用 email 时 | SMTP 密码 |
| `SMTP_FROM` | string | 回退到 `SMTP_USER` | 否 | 发件人地址 |
| `SMTP_REQUIRE_TLS` | bool | `true` | 否 | 强制 TLS 连接（465 隐式/587 STARTTLS），设为 `false` 回退到明文 |

**读取位置**：`alerting/dispatcher.go:394-407`

## 10. 告警

| 变量 | 类型 | 默认值 | 必填 | 说明 |
|------|------|--------|------|------|
| `ALERT_DEDUP_WINDOW` | duration | `10m` | 否 | 告警去重窗口（同节点+同任务+同错误码），`0` 关闭去重 |
| `INTEGRATION_BLOCK_PRIVATE_ENDPOINTS` | bool | `true` | 否 | 阻断 webhook/slack/telegram 指向私网地址（`.env.example` 开发值 `false`，生产建议 `true`） |

**读取位置**：`ALERT_DEDUP_WINDOW` → `alerting/dispatcher.go:237`，`INTEGRATION_BLOCK_PRIVATE_ENDPOINTS` → `api/handlers/integration_handler.go:101`

## 11. 前端

| 变量 | 类型 | 默认值 | 必填 | 说明 |
|------|------|--------|------|------|
| `VITE_API_BASE_URL` | string | `/api/v1` | 否 | API 路径前缀 |
| `VITE_PROXY_TARGET` | string | `http://127.0.0.1:8080` | 否 | 开发模式 Vite 代理目标（仅 `vite.config.ts` 使用） |
| `VITE_DEV_API_DIRECT_URL` | string | — | 否 | 开发模式直连后端地址（`VITE_API_BASE_URL` 为相对路径时使用） |
| `VITE_WS_URL` | string | 自动推导 | 否 | 自定义 WebSocket 地址 |
| `VITE_ENABLE_DEMO_MODE` | string | — | 否 | 设为 `true` 启用 mock 数据（仅演示/测试用） |

**读取位置**：`VITE_ENABLE_DEMO_MODE` → `hooks/use-console-data.ts:108`

## 12. 部署变量（Docker Compose 模板）

以下变量用于 `docker-compose.prod.yml`，非应用运行时变量：

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `IMAGE_REGISTRY` | `docker.io` | 镜像仓库地址 |
| `IMAGE_NAMESPACE` | `xirang` | 镜像命名空间；官方公开镜像默认使用 `docker.io/xirang/xirang` |
| `IMAGE_TAG` | `latest` | 镜像标签；`latest` 仅代表最新稳定版，生产环境建议固定为 `vX.Y.Z` |
| `BACKEND_UPSTREAM` | `127.0.0.1:8080` | Nginx 反代后端地址 |

---

## 默认值不一致说明

以下变量在 `.env.example`（开发）中设为 `false`，但代码默认值为 `true`，这是有意设计——开发环境放宽限制：

| 变量 | 代码默认值 | `.env.example` | `.env.production.example` |
|------|-----------|----------------|--------------------------|
| `INTEGRATION_BLOCK_PRIVATE_ENDPOINTS` | `true` | `false` | `true` |
| `SSH_STRICT_HOST_KEY_CHECKING` | `true` | `false` | `true` |

## 13. 版本检查与系统备份

| 变量 | 类型 | 默认值 | 必填 | 说明 |
|------|------|--------|------|------|
| `VERSION_CHECK_URL` | string | — | 否 | 版本检查地址，推荐使用 `https://api.github.com/repos/xiangnan0811/xirang/releases/latest`；当前仅支持稳定版 semver 响应，未设置时版本检查接口返回"未配置" |
| `DB_BACKUP_DIR` | string | `./backups`（相对于 DB 文件目录） | 否 | 数据库备份文件存放目录 |

**读取位置**：`VERSION_CHECK_URL` → `api/handlers/version_handler.go:37`，`DB_BACKUP_DIR` → `api/handlers/system_handler.go:39`
