# 环境变量参考

完整的 Xirang 环境变量列表，按功能分组。示例文件：`backend/.env.example`（开发）、`backend/.env.production.example`（生产）、`web/.env.example`（前端）。

后端进程不会自动读取 `.env` 文件；源码运行时需要由 shell、systemd、Docker Compose 或 `docker run --env-file` 注入环境变量。Docker Compose 生产模板会读取仓库根目录 `.env`。

---

## 1. 服务器与环境

| 变量 | 类型 | 默认值 | 必填 | 说明 |
|------|------|--------|------|------|
| `SERVER_ADDR` | string | `:8080` | 否 | 后端监听地址 |
| `APP_ENV` | string | — | 否 | 应用环境（`development` / `production`），影响弱密钥检测等安全策略 |
| `ENVIRONMENT` | string | — | 否 | `APP_ENV` 的回退变量（优先级低于 `APP_ENV`） |
| `GIN_MODE` | string | — | 否 | `APP_ENV` 的回退变量（`debug` = development，`release` = production） |
| `LOG_LEVEL` | string | 空（info） | 否 | 日志级别：`debug` / `info` / `warn` / `error` |

**读取位置**：`SERVER_ADDR` → `backend/internal/config/config.go` 的 `Load`；`APP_ENV` / `ENVIRONMENT` / `GIN_MODE` → `backend/internal/util/env.go`；`LOG_LEVEL` → `backend/cmd/server/main.go`。

## 2. 数据库

| 变量 | 类型 | 默认值 | 必填 | 说明 |
|------|------|--------|------|------|
| `DB_TYPE` | string | `sqlite` | 否 | 数据库类型：`sqlite` / `postgres` |
| `SQLITE_PATH` | string | `./xirang.db` | 否 | SQLite 文件路径 |
| `DB_DSN` | string | — | PG 时必填 | PostgreSQL 连接串，生产建议 `sslmode=require` |

**读取位置**：`backend/internal/config/config.go` 的 `Load`；系统自助备份接口也会读取 `DB_TYPE` / `SQLITE_PATH`。

## 3. 认证与安全

| 变量 | 类型 | 默认值 | 必填 | 说明 |
|------|------|--------|------|------|
| `JWT_SECRET` | string | 开发环境 `xirang-dev-secret` | 生产必填 | JWT 签名密钥，生产环境必须为强随机字符串（≥16 字符） |
| `JWT_TTL` | duration | `24h` | 否 | JWT 有效期 |
| `LOGIN_RATE_LIMIT` | int | `10` | 否 | 登录接口速率限制（次/窗口） |
| `LOGIN_RATE_WINDOW` | duration | `1m` | 否 | 速率限制时间窗口 |
| `LOGIN_FAIL_LOCK_THRESHOLD` | int | `5` | 否 | 连续登录失败多少次后锁定账号 |
| `LOGIN_FAIL_LOCK_DURATION` | duration | `15m` | 否 | 账号锁定持续时间 |
| `LOGIN_CAPTCHA_ENABLED` | bool | `false` | 否 | 启用登录验证码（settings 键 `login.captcha_enabled`，可通过设置 API 实时调整） |
| `LOGIN_SECOND_CAPTCHA_ENABLED` | bool | `false` | 否 | 启用二次验证码（settings 键 `login.second_captcha_enabled`，可通过设置 API 实时调整） |
| `ADMIN_INITIAL_PASSWORD` | string | — | 首次启动 | 初始 admin 账号密码，仅 bootstrap 阶段使用 |
| `DATA_ENCRYPTION_KEY` | string | 内置开发密钥 | 生产必填 | 敏感字段（密码、私钥）加密密钥，支持 32 字节 base64 或任意字符串（自动 SHA-256 派生） |

**读取位置**：`JWT_SECRET` / `JWT_TTL` / 登录限流与锁定 → `backend/internal/config/config.go`，部分登录安全项同时注册到 settings 服务；登录验证码 → settings 服务 `login.captcha_enabled` / `login.second_captcha_enabled`；`ADMIN_INITIAL_PASSWORD` → `backend/internal/bootstrap/bootstrap.go`；`DATA_ENCRYPTION_KEY` → `backend/internal/secure/crypto.go` 和 `backend/internal/config/config.go`。

## 4. 跨域与 WebSocket

| 变量 | 类型 | 默认值 | 必填 | 说明 |
|------|------|--------|------|------|
| `CORS_ALLOWED_ORIGINS` | string | `http://localhost:5173,http://127.0.0.1:5173` | 否 | 跨域白名单（逗号分隔），留空时仅放行同主机 Origin（忽略端口）；生产环境禁止 `*` |
| `WS_ALLOW_EMPTY_ORIGIN` | bool | `false` | 否 | WebSocket 是否允许空 Origin |
| `WS_MAX_CONNECTIONS` | int | `100` | 否 | WebSocket 最大连接数 |

**读取位置**：`CORS_ALLOWED_ORIGINS` / `WS_ALLOW_EMPTY_ORIGIN` → `backend/internal/config/config.go`；`WS_MAX_CONNECTIONS` → `backend/internal/ws/hub.go`。

## 5. SSH

| 变量 | 类型 | 默认值 | 必填 | 说明 |
|------|------|--------|------|------|
| `SSH_STRICT_HOST_KEY_CHECKING` | bool | `true` | 否 | 严格校验远端主机指纹（`.env.example` 开发值 `false`，生产建议 `true`） |
| `SSH_KNOWN_HOSTS_PATH` | string | `~/.ssh/known_hosts` | 否 | known_hosts 文件路径 |
| `SSH_AUTO_ACCEPT_NEW_HOSTS` | bool | `true` | 否 | 严格校验开启时，是否自动接受首次出现的主机指纹（设为 `false` 可禁用） |

**读取位置**：`backend/internal/sshutil/ssh_auth.go` 和 `backend/internal/task/executor/executor.go`。All-in-One 镜像默认将 `SSH_KNOWN_HOSTS_PATH` 设为 `/data/.ssh/known_hosts`，使自动接受的新主机指纹随数据卷持久化。

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

**读取位置**：`RSYNC_BINARY` → `backend/internal/config/config.go`；`RSYNC_ALLOWED_*` / `RSYNC_MIN_FREE_GB` → rsync 任务处理与执行器；`RCLONE_BINARY` / `RESTIC_BINARY` → 对应执行器与完整性检查；`BATCH_COMMAND_BLACKLIST` → `backend/internal/api/handlers/batch_handler.go`；`FILE_BROWSER_ALLOW_ALL` → `backend/internal/api/handlers/file_handler.go`（仅开发环境允许放开）。

## 7. 节点探测

| 变量 | 类型 | 默认值 | 必填 | 说明 |
|------|------|--------|------|------|
| `NODE_PROBE_INTERVAL` | duration | `5m` | 否 | 探测间隔（最小 30s） |
| `NODE_PROBE_FAIL_THRESHOLD` | int | `3` | 否 | 连续失败多少次标记节点离线 |
| `NODE_PROBE_CONCURRENCY` | int | `10` | 否 | 并发探测数（生产建议 `20`） |

**读取位置**：`backend/internal/config/config.go`；这些键也注册到 settings 服务，当前探测 worker 启动时读取配置，变更需重启生效。

## 8. 数据保留

| 变量 | 类型 | 默认值 | 必填 | 说明 |
|------|------|--------|------|------|
| `TASK_TRAFFIC_RETENTION_DAYS` | int | `8` | 否 | 任务流量数据保留天数 |
| `TASK_RUN_RETENTION_DAYS` | int | `90` | 否 | 任务执行记录保留天数 |
| `RETENTION_CHECK_INTERVAL` | duration | `6h` | 否 | 备份保留策略检查间隔（最小 1m），定期清理过期备份并检查存储空间 |
| `BACKUP_STORAGE_MIN_FREE_GB` | int | `10` | 否 | 本地备份存储最低剩余空间（GB），低于此值触发告警 |
| `BACKUP_STORAGE_MAX_USAGE_PCT` | int | `90` | 否 | 本地备份存储最大使用率（%），超过此值触发告警 |
| `INTEGRITY_CHECK_MULTIPLIER` | int | `4` | 否 | 完整性检查频率倍数——每隔多少个保留清理周期运行一次 `restic check` / `rclone check`（默认 4，即 `RETENTION_CHECK_INTERVAL=6h` 时每 24h 一次） |
| `LOG_RETENTION_DAYS_DEFAULT` | int | `30` | 否 | 节点日志默认保留天数，节点未单独配置时生效 |
| `SILENCE_RETENTION_DAYS` | int | `30` | 否 | 已过期静默规则的审计保留天数，超出后删除 |

**读取位置**：基础任务保留与存储阈值 → `backend/internal/config/config.go` 和 settings 服务；`INTEGRITY_CHECK_MULTIPLIER` → `backend/internal/task/retention_worker.go`；节点日志保留 → `backend/internal/nodelogs/retention.go`；静默规则保留 → `backend/internal/alerting/silence_retention.go`。

## 9. 邮件通知

| 变量 | 类型 | 默认值 | 必填 | 说明 |
|------|------|--------|------|------|
| `SMTP_HOST` | string | — | 启用 email 时 | SMTP 服务器地址，为空时 email 通道失败 |
| `SMTP_PORT` | string | `587` | 否 | SMTP 端口 |
| `SMTP_USER` | string | — | 启用 email 时 | SMTP 用户名 |
| `SMTP_PASS` | string | — | 启用 email 时 | SMTP 密码 |
| `SMTP_FROM` | string | 回退到 `SMTP_USER` | 否 | 发件人地址 |
| `SMTP_REQUIRE_TLS` | bool | `true` | 否 | 强制 TLS 连接（465 隐式/587 STARTTLS），设为 `false` 回退到明文 |

> 上述 `SMTP_*` 变量从 v0.18+ 起已纳入系统设置注册表（key 前缀 `smtp.`），可通过 `/settings` API 实时调整；环境变量仅作为首次启动时的回退默认值。生产环境建议把 `SMTP_PASS` 仍以环境变量注入而非入库。

**读取位置**：settings 服务键 `smtp.host` / `smtp.port` / `smtp.user` / `smtp.password` / `smtp.from` / `smtp.require_tls`，邮件发送路径位于 `backend/internal/alerting/dispatcher.go`。

## 10. 告警

| 变量 | 类型 | 默认值 | 必填 | 说明 |
|------|------|--------|------|------|
| `ALERT_DEDUP_WINDOW` | duration | `10m` | 否 | 告警去重窗口（同节点+同任务+同错误码），`0` 关闭去重 |
| `INTEGRATION_BLOCK_PRIVATE_ENDPOINTS` | bool | `true` | 否 | 阻断 webhook/slack/telegram 指向私网地址（`.env.example` 开发值 `false`，生产建议 `true`） |
| `BACKUP_STALE_THRESHOLD_HOURS` | int | `48` | 否 | 备份健康面板判定节点备份过期的小时阈值 |

**读取位置**：`ALERT_DEDUP_WINDOW` → settings 服务 / `backend/internal/alerting/dispatcher.go`；`INTEGRATION_BLOCK_PRIVATE_ENDPOINTS` → `backend/internal/api/handlers/integration_handler.go`；`BACKUP_STALE_THRESHOLD_HOURS` → `backend/internal/api/handlers/overview_backup_health_handler.go`。

### 10.1 异常检测

异常检测默认保留事件记录，但不会升级为告警中心告警或外部通知。需要恢复异常通知时，将 `ANOMALY_ALERTS_ENABLED` 设为 `true`，或在系统设置中打开 `anomaly.alerts_enabled`。

| 变量 | 类型 | 默认值 | 必填 | 说明 |
|------|------|--------|------|------|
| `ANOMALY_ENABLED` | bool | `true` | 否 | 启用异常检测总开关；关闭后 EWMA 与磁盘预测检测器都停止 |
| `ANOMALY_ALERTS_ENABLED` | bool | `false` | 否 | 是否将异常事件升级为告警/通知；默认仅写入 `anomaly_events` 供诊断 |
| `ANOMALY_EWMA_ALPHA` | string | `0.3` | 否 | EWMA 平滑因子 α |
| `ANOMALY_EWMA_SIGMA` | string | `5.0` | 否 | EWMA 异常判定标准差倍数，默认更保守以降低低负载误报 |
| `ANOMALY_EWMA_WINDOW_HOURS` | int | `6` | 否 | EWMA 回看样本窗口（小时） |
| `ANOMALY_EWMA_MIN_SAMPLES` | int | `24` | 否 | EWMA 最少样本数 |
| `ANOMALY_DISK_FORECAST_DAYS` | int | `7` | 否 | 磁盘预测阈值，预计小于等于该天数爆满时记录事件 |
| `ANOMALY_DISK_FORECAST_MIN_HISTORY_HOURS` | int | `72` | 否 | 磁盘预测所需最少历史小时数 |
| `ANOMALY_EVENTS_RETENTION_DAYS` | int | `30` | 否 | 异常事件保留天数 |

**读取位置**：settings 服务键 `anomaly.enabled` / `anomaly.alerts_enabled` / `anomaly.ewma_*` / `anomaly.disk_forecast_*` / `anomaly.events_retention_days`，消费端位于 `backend/internal/anomaly/`。

## 11. 前端

| 变量 | 类型 | 默认值 | 必填 | 说明 |
|------|------|--------|------|------|
| `VITE_API_BASE_URL` | string | `/api/v1` | 否 | API 路径前缀 |
| `VITE_PROXY_TARGET` | string | `http://127.0.0.1:8080` | 否 | 开发模式 Vite 代理目标（仅 `vite.config.ts` 使用） |
| `VITE_DEV_API_DIRECT_URL` | string | — | 否 | 开发模式直连后端地址（`VITE_API_BASE_URL` 为相对路径时使用） |
| `VITE_WS_URL` | string | 自动推导 | 否 | 自定义 WebSocket 地址 |
| `VITE_ENABLE_DEMO_MODE` | string | — | 否 | 设为 `true` 启用 mock 数据（仅演示/测试用） |

**读取位置**：`VITE_API_BASE_URL` / `VITE_DEV_API_DIRECT_URL` → `web/src/lib/api/core.ts` 和 WebSocket URL 推导；`VITE_PROXY_TARGET` → `web/vite.config.ts`；`VITE_WS_URL` → `web/src/lib/ws/logs-socket.ts`；`VITE_ENABLE_DEMO_MODE` → `web/src/hooks/use-console-data.ts`。

## 12. 部署变量（Docker Compose / All-in-One 模板）

以下变量用于 `docker-compose.prod.yml` 或 All-in-One 镜像的 Nginx 模板，部分不是后端应用运行时变量：

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `IMAGE_REGISTRY` | `docker.io` | 镜像仓库地址 |
| `IMAGE_NAMESPACE` | `xirang` | 镜像命名空间；官方公开镜像默认使用 `docker.io/xirang/xirang` |
| `IMAGE_TAG` | `latest` | 镜像标签；`latest` 仅代表最新稳定版，生产环境建议固定为 `vX.Y.Z` |
| `HTTP_PORT` | `80` | 宿主机 HTTP 端口映射到容器 `8080` |
| `HTTPS_PORT` | `443` | 宿主机 HTTPS 端口映射到容器 `8443` |
| `BACKEND_UPSTREAM` | `http://127.0.0.1:3000` | All-in-One 镜像内 Nginx 反代后端地址，需包含协议 |

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
| `DB_BACKUP_MAX_COUNT` | int | `20` | 否 | 系统自助 SQLite 备份接口保留的最大备份数量 |

**读取位置**：`VERSION_CHECK_URL` → `backend/internal/api/handlers/version_handler.go`；`DB_BACKUP_DIR` / `DB_BACKUP_MAX_COUNT` → `backend/internal/api/handlers/system_handler.go`。容器内 cron 备份使用 `scripts/backup-db.sh` 与 `/etc/supercronic/xirang-backup`，按文件 mtime 清理 30 天前的备份，不读取 `DB_BACKUP_MAX_COUNT`。

版本检查会把 GitHub latest release 的 `tag_name` 与服务端当前构建版本比较；当前构建版本来自编译时注入。未注入版本信息的本地二进制或镜像会显示 `dev`，检查结果只适合作为开发提示。

## 14. 指标远程推送（Prometheus remote-write）

可选功能。设置 `METRICS_REMOTE_URL` 后，每次节点探测样本同时通过 Prometheus remote-write 协议（snappy + protobuf）推送到外部 TSDB（Mimir、Cortex、VictoriaMetrics、Grafana Cloud 等）。`FanSink` 自动吞掉远程错误，DBSink 不受影响。

| 变量 | 类型 | 默认值 | 必填 | 说明 |
|------|------|--------|------|------|
| `METRICS_REMOTE_URL` | string | 空 | 否 | Prometheus remote-write 端点 URL（如 `https://mimir.example.com/api/v1/push`）。留空禁用远程推送。 |
| `METRICS_REMOTE_BEARER_TOKEN` | string | 空 | 否 | 可选 Bearer Token，作为 `Authorization: Bearer <token>` 请求头。生产环境建议使用此环境变量而非设置 UI，避免明文存库。 |
| `METRICS_REMOTE_TIMEOUT` | duration | `5s` | 否 | 单次 HTTP 请求超时（Go duration 格式）。解析失败时回退到 5 秒。 |

可观测性：失败时通过 `xirang_metrics_remote_write_total{status="failure"}` 计数，建议在 Grafana 上配置 `rate(...)` 持续大于 0 的告警面板。

**读取位置**：`backend/cmd/server/main.go` 的 `buildRemoteWriteSinkFromConfig`，并回退读取 settings 服务键 `metrics.remote_url` / `metrics.remote_bearer_token`。

### 14.1 /metrics 端点鉴权与限流

`/metrics` 端点暴露 Prometheus 标准指标（含 `http_requests_total{path=...}` 标签集），未鉴权时会泄露所有 secured 路由清单和流量画像，且无限流可被 DoS 放大。Wave 2 PR-B 引入下列变量来加固：

| 变量 | 类型 | 默认值 | 必填 | 说明 |
|------|------|--------|------|------|
| `METRICS_TOKEN` | string | 空 | 否 | Bearer token，留空时 `/metrics` 仍可公开访问（兼容旧行为）但会在日志中提示一次（每 10 分钟最多再提示一次）；设置后请求必须携带 `Authorization: Bearer <token>`，否则返回 401 |
| `METRICS_RATE_LIMIT` | int | `5` | 否 | `/metrics` 独立限流桶（per IP）允许的请求次数，与 `/api` 限流分离 |
| `METRICS_RATE_WINDOW` | duration | `1s` | 否 | 限流时间窗口（Go duration 格式）。默认 `5 req/s` 对应 Prometheus 通常 15-30s 一次抓取，留有充足余量；超过返回 429 |

Prometheus scrape config 示例：

```yaml
scrape_configs:
  - job_name: xirang
    metrics_path: /metrics
    scheme: http
    bearer_token_file: /etc/prometheus/secrets/xirang-metrics-token
    static_configs:
      - targets: ['xirang-backend:8080']
```

**读取位置**：`backend/internal/config/config.go`（`MetricsToken` / `MetricsRateLimit` / `MetricsRateWindow`） → `backend/internal/api/router.go` 通过 `middleware.MetricsAuth` + `middleware.MetricsRateLimit` 注册到 `/metrics`。

---

## 15. 容器与运行时

| 变量 | 类型 | 默认值 | 必填 | 说明 |
|------|------|--------|------|------|
| `TZ` | string | 镜像默认（UTC） | 否 | 容器与应用使用的 IANA 时区（例如 `Asia/Shanghai`）。生产建议显式设置，确保备份文件名、日志时间戳与运维一致。`deploy/allinone/Dockerfile` 已预装 `tzdata`，仅需通过环境变量切换 |
| `LOG_FILE` | string | — | 否 | 设置后应用日志同时写入该文件（保留 stdout 输出供 docker logs/journald 收集）。留空时仅 stdout |
| `TASK_MAX_EXECUTION_SECONDS` | int | `86400` | 否 | 单次任务执行的全局最大秒数兜底，防 executor 卡死导致 goroutine 泄漏。Policy 级 `max_execution_seconds` >0 时优先于本变量。超时后任务被强制中止并 status=failed，last_error 含"超时"字样 |

**读取位置**：`TZ` → 容器初始化时被 glibc/musl 解析，应用层 `time.Now()` 自动遵循；`LOG_FILE` → `backend/internal/util/logger.go`（PR-C 引入）。

---

## 敏感字段加密策略

息壤把"密码、私钥、TOTP 密钥、通知通道 endpoint 与 secret、HTTP 代理地址"统一视为敏感字段，落库前必须加密、读取后必须解密。这一节集中说明实现细节，避免新代码绕过统一加解密路径。

### 涉及的字段

| 模型（`backend/internal/model/models.go`） | 字段 | 钩子位置 |
|------|------|----------|
| `User` | `PasswordHash`（bcrypt，不再二次加密）、`TOTPSecret`、`RecoveryCodes` | `BeforeSave` / `AfterFind`（约第 523 / 541 行） |
| `Node` | `Password`、`PrivateKey` | `BeforeSave` / `AfterFind`（约第 485 / 503 行） |
| `SSHKey` | `PrivateKey` | `BeforeSave` / `AfterFind`（约第 461 / 473 行） |
| `Integration` | `Endpoint`、`Secret`、`ProxyURL` | `BeforeSave` / `AfterFind`（约第 136 / 161 行） |
| `Task` | 命令/路径相关的敏感子字段（按 hook 实现为准） | `BeforeSave` / `AfterFind`（约第 249 / 261 行） |

> 新增任何敏感字段都必须同时实现 `BeforeSave` / `AfterFind` 钩子，并补充模型层测试。

### 加密原语

实现位于 `backend/internal/secure/crypto.go`：

- `EncryptIfNeeded(raw)` / `DecryptIfNeeded(raw)`：幂等版本，对已加密内容跳过；推荐在 model hook 内使用
- `EncryptString(raw)` / `DecryptString(raw)`：强制版本，用于明确知道当前状态的迁移/工具脚本
- `IsEncrypted(raw)` / `IsV1Encrypted(raw)`：状态探测，用于轮替密钥时甄别旧版本
- `ReEncryptV1Value(raw)`：把 v1 密钥加密的内容重新用主密钥加密，用于密钥轮替

### API 响应脱敏

- `Node` 提供 `Sanitized()`（`models.go:40` 附近），在序列化前置空 `Password`、`PrivateKey`、`SSHKey.PrivateKey`；handler 在响应前必须调用
- `SSHKey` 通过 `backend/internal/api/handlers/ssh_key_handler.go` 中专属的 `sshKeyResponseItem` + `toSSHKeyResponse()` 完成脱敏；模型字段 `PrivateKey` 在 PR-B 之后已改为 `json:"-"` 提供深度防御。**禁止直接** `c.JSON(model.SSHKey{...})`
- `Integration` 通过 `maskIntegrationEndpoint()` + 模型上 `Secret json:"-"` 双重防护

### 密钥来源与轮替

- 主密钥：环境变量 `DATA_ENCRYPTION_KEY`（参见 §3）；生产必须使用强随机值
- 旧密钥：环境变量 `DATA_ENCRYPTION_LEGACY_KEY`（如果存在），用于解密历史数据后再以主密钥重写
- 轮替流程：设置新 `DATA_ENCRYPTION_KEY` 同时保留 `DATA_ENCRYPTION_LEGACY_KEY` → 启动 → 调用迁移工具或下次写入时自然 `ReEncryptV1Value` → 确认无 v1 数据后清理旧密钥

### 不要这样做

- 直接给敏感字段做 JSON marshal 而不走脱敏函数
- 在 handler 里调用 `EncryptString` / `DecryptString` 重复加解密
- 在 zerolog 日志里直接打印含 secret 的字段；推送到外部聚合系统前必须脱敏
- 把任何敏感字段写入 audit log 的 detail JSON 而不脱敏
