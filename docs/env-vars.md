# 环境变量参考

完整的 Xirang 环境变量列表，按功能分组。示例文件：`backend/.env.example`（开发）、`backend/.env.production.example`（生产）、`web/.env.example`（前端）。

后端进程不会自动读取 `.env` 文件；源码运行时需要由 shell、systemd、Docker Compose 或 `docker run --env-file` 注入环境变量。Docker Compose 生产模板会读取仓库根目录 `.env`。

---

## 1. 服务器与环境

| 变量 | 类型 | 默认值 | 必填 | 说明 |
|------|------|--------|------|------|
| `SERVER_ADDR` | string | `:8080` | 否 | 后端监听地址 |
| `APP_ENV` | string | — | 否 | 应用环境。**优先**于 `ENVIRONMENT`。仅 `development` 放宽弱密钥/默认 JWT 与 METRICS/Swagger；**未设置**、`production`/`prod`/`staging` 及其它未知值一律按生产硬化（METRICS_TOKEN、CORS 禁止 `*`、Swagger 默认关） |
| `ENVIRONMENT` | string | — | 否 | 仅当 `APP_ENV` **未设置**时作为回退；若 `APP_ENV=production`（或 prod/staging）则忽略 `ENVIRONMENT=development`（不会放宽密钥策略） |
| `GIN_MODE` | string | — | 否 | Gin 运行模式（`debug` / `release`）。`debug` **不会**放宽密钥策略。生产硬化不依赖 `GIN_MODE`（未声明 APP_ENV 也 hardened）；`APP_ENV=development` 始终优先于 `GIN_MODE=release` |
| `LOG_LEVEL` | string | 空（info） | 否 | 日志级别：`debug` / `info` / `warn` / `error` |

**读取位置**：`SERVER_ADDR` → `backend/internal/config/config.go` 的 `Load`；`APP_ENV` / `ENVIRONMENT` → `backend/internal/util/env.go` 的 `IsDevelopmentEnv` / `IsProductionEnv`；`GIN_MODE` 仅影响 Gin 框架运行模式（`debug`/`release`），**不**决定开发/生产 CSP 或密钥策略——CSP 放宽分支只检查 `IsDevelopmentEnv()`（即 `APP_ENV`/`ENVIRONMENT`），见 `backend/internal/api/router.go`；`LOG_LEVEL` → `backend/cmd/server/main.go`。

## 2. 数据库

| 变量 | 类型 | 默认值 | 必填 | 说明 |
|------|------|--------|------|------|
| `DB_TYPE` | string | `sqlite` | 否 | 数据库类型：`sqlite` / `postgres` |
| `SQLITE_PATH` | string | `./xirang.db` | 否 | SQLite 文件路径 |
| `DB_DSN` | string | — | PG 时必填 | PostgreSQL 连接串，生产建议 `sslmode=require` |

**读取位置**：`backend/internal/config/config.go` 的 `Load`；系统自助备份接口也会读取 `DB_TYPE` / `SQLITE_PATH`。数据库迁移 dirty 或 clean-version/schema-drift 状态都会无条件拒绝启动，参见[部署指南](deployment.md#迁移-dirty-状态排障)。

## 3. 认证与安全

| 变量 | 类型 | 默认值 | 必填 | 说明 |
|------|------|--------|------|------|
| `JWT_SECRET` | string | 开发环境 `xirang-dev-secret` | 生产必填 | JWT 签名密钥，生产环境必须为强随机字符串（≥32 字符） |
| `JWT_TTL` | duration | `24h` | 否 | JWT 有效期 |
| `LOGIN_RATE_LIMIT` | int | `10` | 否 | 登录接口速率限制（次/窗口） |
| `LOGIN_RATE_WINDOW` | duration | `1m` | 否 | 速率限制时间窗口 |
| `LOGIN_FAIL_LOCK_THRESHOLD` | int | `5` | 否 | 连续登录失败多少次后锁定账号 |
| `LOGIN_FAIL_LOCK_DURATION` | duration | `15m` | 否 | 账号锁定持续时间 |
| `LOGIN_CAPTCHA_ENABLED` | bool | `false` | 否 | 启用登录验证码（settings 键 `login.captcha_enabled`，可通过设置 API 实时调整） |
| `LOGIN_SECOND_CAPTCHA_ENABLED` | bool | `false` | 否 | 启用二次验证码（settings 键 `login.second_captcha_enabled`，可通过设置 API 实时调整） |
| `ADMIN_INITIAL_PASSWORD` | string | — | 仅首次无 admin | 初始 admin 密码；**仅库中尚无 admin 用户时** bootstrap 需要，已有 admin 后不必填、也不会因留空拒绝启动 |
| `DATA_ENCRYPTION_KEY` | string | 开发环境自动生成随机密钥（重启后失效） | 生产必填 | 敏感字段（密码、私钥）加密密钥，支持 32 字节 base64 或任意字符串（自动 argon2id 派生） |
| `DATA_ENCRYPTION_LEGACY_KEY` | string | — | 否 | 密钥轮替期间用于解密历史 v1 字段，并作为上一把 v2 KEK rewrap 小型 domain-key envelope；两类迁移均验证完成后再清理 |

**读取位置**：`JWT_SECRET` / `JWT_TTL` / 登录限流与锁定 → `backend/internal/config/config.go`，部分登录安全项同时注册到 settings 服务；登录验证码 → settings 服务 `login.captcha_enabled` / `login.second_captcha_enabled`；`ADMIN_INITIAL_PASSWORD` → `backend/internal/bootstrap/bootstrap.go`；`DATA_ENCRYPTION_KEY` → `backend/internal/secure/crypto.go`、`backend/internal/secure/keyring.go` 和 `backend/internal/config/config.go`；`DATA_ENCRYPTION_LEGACY_KEY` → `backend/internal/secure/crypto.go` 与 `backend/internal/secure/keyring.go`。

## 4. 跨域与 WebSocket

| 变量 | 类型 | 默认值 | 必填 | 说明 |
|------|------|--------|------|------|
| `CORS_ALLOWED_ORIGINS` | string | `http://localhost:5173,http://127.0.0.1:5173` | 否 | 跨域白名单（逗号分隔），留空时仅放行同主机 Origin（忽略端口）；生产环境禁止 `*` |
| `TRUSTED_PROXIES` | string | `127.0.0.1,::1` | 否 | Gin 可信反向代理列表（逗号分隔 CIDR/IP），仅这些来源的 `X-Forwarded-For` / `X-Real-IP` 会影响 `ClientIP()`（登录/API 限流与审计）。设为空或 `none` 表示不信任任何代理（始终用直连地址）。非法 IP/CIDR **启动失败**（不静默降级）。**切勿**在公网入口上信任全网段 |
| `WS_ALLOW_EMPTY_ORIGIN` | bool | `false` | 否 | WebSocket 是否允许空 Origin |
| `WS_MAX_CONNECTIONS` | int | `100` | 否 | WebSocket 最大连接数 |

**读取位置**：`CORS_ALLOWED_ORIGINS` / `TRUSTED_PROXIES` / `WS_ALLOW_EMPTY_ORIGIN` → `backend/internal/config/config.go`；`WS_MAX_CONNECTIONS` → `backend/internal/ws/hub.go`。

## 5. SSH

| 变量 | 类型 | 默认值 | 必填 | 说明 |
|------|------|--------|------|------|
| `SSH_STRICT_HOST_KEY_CHECKING` | bool | `true` | 否 | 严格校验远端主机指纹，配合 known_hosts 校验主机指纹（生产建议 `true`） |
| `SSH_KNOWN_HOSTS_PATH` | string | `~/.ssh/known_hosts` | 否 | known_hosts 文件路径 |
| `SSH_AUTO_ACCEPT_NEW_HOSTS` | bool | `false` | 否 | 是否自动接受首次出现的主机指纹并写入 known_hosts（设为 `true` 可启用，生产建议 `false`） |

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
| `BACKUP_PATH_ALLOW_SHELL_META` | bool | `false` | 否 | 仅历史数据救援用；设为 `true` 会跳过备份路径 shell 元字符防御校验 |
| `SNAPSHOT_INDEX_MAX_SECONDS` | int | `1800` | 否 | Restic 快照文件搜索异步索引单次最长构建秒数 |
| `BACKUP_ASSETS_ENABLED` | bool | `false` | 否 | 备份资产领域 **请求**开关；CodeDefault 仍为 `false`。请求启用必须先通过就绪门禁（双引擎迁移、密钥域、导出根、库存盘点）。全新安装在 `ready` 后才算有效开启；已有安装还须管理员确认当前库存摘要。环境变量为 `true` 但门禁未过时，Core 仍会启动，admission / Catalog / Search / Content 保持关闭。设置界面启用现有安装会 409 直到盘点 + ack |
| `BACKUP_ASSETS_CATALOG_BATCH_SIZE` | int | `2000` | 否 | 目录构建批次大小，范围 `1..100000` |
| `BACKUP_ASSETS_CATALOG_BUILD_TIMEOUT` | duration | `30m` | 否 | 单次目录构建超时，范围 `1m..24h` |
| `BACKUP_ASSETS_REPOSITORY_RECONCILE_INTERVAL` | duration | `15m` | 否 | 仓库元数据对账间隔，范围 `1m..24h` |
| `BACKUP_ASSETS_AUDIT_SEGMENT_MAX_EVENTS` | int | `10000` | 否 | typed 资产审计每段最大事件数，范围 `100..1000000` |
| `BACKUP_ASSETS_AUDIT_SEGMENT_MAX_AGE` | duration | `24h` | 否 | typed 资产审计每段最大持续时间，范围 `1h..168h` |
| `BACKUP_ASSETS_AUDIT_DETAIL_RETENTION_DAYS` | int | `180` | 否 | 资产审计事件明细保留天数，范围 `1..3650` |
| `BACKUP_ASSETS_AUDIT_CHECKPOINT_RETENTION_DAYS` | int | `2555` | 否 | 审计 segment checkpoint 保留天数，范围 `180..36500` |
| `BACKUP_ASSETS_LEASE_DURATION` | duration | `5m` | 否 | RecoveryPoint 短租约时长，范围 `30s..30m` |
| `BACKUP_ASSETS_LEASE_HEARTBEAT` | duration | `60s` | 否 | 租约心跳间隔，范围 `10s..5m`，且必须小于短租约时长 |
| `BACKUP_ASSETS_LEASE_ABSOLUTE_DEADLINE` | duration | `168h` | 否 | 租约绝对截止时间，范围 `5m..168h`；takeover/renew 不得延长该截止时间 |
| `BACKUP_ASSETS_PROVIDER_OPERATION_TIMEOUT` | duration | `2m` | 否 | Provider probe/list/stat 等元数据操作超时，范围 `5s..30m` |
| `BACKUP_ASSETS_PROVIDER_MAX_CONCURRENCY` | int | `4` | 否 | Provider 只读操作共享最大并发数，范围 `1..32` |
| `BACKUP_ASSETS_PROVIDER_METADATA_LIMIT_BYTES` | int | `16777216` | 否 | Provider 元数据输出/解析硬上限（字节），范围 `65536..67108864` |
| `BACKUP_ASSETS_PUBLICATION_RECONCILE_INTERVAL` | duration | `5m` | 否 | Restic 恢复点持久队列的周期对账间隔，范围 `30s..24h` |
| `BACKUP_ASSETS_PUBLICATION_RECONCILE_BATCH_SIZE` | int | `100` | 否 | 单次恢复点对账最大候选数，范围 `1..1000` |
| `BACKUP_ASSETS_PUBLICATION_WORKER_CONCURRENCY` | int | `2` | 否 | Manifest/对账 worker 共享并发上限，范围 `1..32` |
| `BACKUP_ASSETS_PUBLICATION_MISSING_GRACE` | duration | `30m` | 否 | 带精确标记但暂未发现 snapshot 时的宽限期，范围 `1m..24h`；必须不小于短租约且小于绝对截止时间 |
| `BACKUP_ASSETS_PUBLICATION_STREAM_MAX_BYTES` | int | `268435456` | 否 | Restic backup JSON/NDJSON 证据流总字节上限，范围 `1048576..1073741824` |
| `BACKUP_ASSETS_MANIFEST_TIMEOUT` | duration | `2h` | 否 | 单个精确 snapshot Manifest 枚举超时，范围 `1m..24h`；必须小于绝对截止时间 |
| `BACKUP_ASSETS_MANIFEST_MAX_BYTES` | int | `4294967296` | 否 | 单个 Manifest 的累计逻辑字节上限，范围 `1048576..17179869184` |
| `BACKUP_ASSETS_MANIFEST_MAX_ENTRIES` | int | `10000000` | 否 | 单个 Manifest 的目录/文件条目上限，范围 `1..100000000` |
| `BACKUP_ASSETS_MANIFEST_MAX_RECORD_BYTES` | int | `1048576` | 否 | 单条 Restic Manifest 记录上限，范围 `4096..4194304`；不得大于 Manifest 总字节上限 |
| `BACKUP_ASSETS_MANIFEST_MAX_DEPTH` | int | `4096` | 否 | Manifest 遍历的最大目录深度，范围 `1..65536` |
| `BACKUP_ASSETS_RCLONE_PREFLIGHT_TTL` | duration | `30m` | 否 | Rclone 版本化预检有效期，范围 `16m..24h`；Task、绑定或能力 revision 改变时会提前失效 |
| `BACKUP_ASSETS_RCLONE_PORTABLE_DEADLINE` | duration | `24h` | 否 | 单个 Rclone Portable 恢复点的绝对时限，范围 `5m..168h` |
| `BACKUP_ASSETS_RCLONE_NATIVE_DEADLINE` | duration | `45m` | 否 | 单个 Rclone AWS Native 恢复点的绝对时限，范围 `5m..55m` |
| `BACKUP_ASSETS_RCLONE_BOUND_CONFIG_MAX_BYTES` | int | `65536` | 否 | write-only Rclone bound config 最大字节数，范围 `1024..65536` |
| `BACKUP_ASSETS_RCLONE_CONTROL_PAYLOAD_MAX_BYTES` | int | `8388608` | 否 | Rclone manifest/control 对象单次暂存最大字节数，范围 `65536..67108864` |
| `BACKUP_ASSETS_RCLONE_FULL_VERIFY_MAX_BYTES` | int | `1099511627776` | 否 | weak/no-hash Remote 单个恢复点允许的全字节校验读取量，范围 `1048576..17592186044416`；超过时失败关闭 |
| `BACKUP_ASSETS_RCLONE_MANIFEST_CHUNK_MAX_BYTES` | int | `8388608` | 否 | Rclone canonical manifest 单个分块最大字节数，范围 `65536..67108864` |
| `BACKUP_ASSETS_RCLONE_LOW_LEVEL_RETRIES` | int | `3` | 否 | Rclone 单次 publication attempt 的 low-level retries，范围 `1..10`；重启 attempt 仍使用新身份 |
| `BACKUP_ASSETS_RCLONE_STAGING_ORPHAN_AGE` | duration | `24h` | 否 | Rclone 未提交 staging 前缀进入 orphan 调和候选前的最小年龄，范围 `1h..168h` |
| `BACKUP_ASSETS_RCLONE_STAGING_SCAN_LIMIT` | int | `256` | 否 | 单次 Rclone staging orphan 扫描上限，范围 `1..4096` |
| `BACKUP_ASSETS_RCLONE_KMS_READ_KEY_MAX_COUNT` | int | `8` | 否 | AWS Native SSE-KMS 保留 decrypt-only read key 的数量上限，范围 `1..32` |
| `BACKUP_ASSETS_RCLONE_HEALTH_INTERVAL` | duration | `15m` | 否 | Rclone 受管 Repository 健康复核间隔，范围 `1m..24h` |
| `BACKUP_ASSETS_RCLONE_HEALTH_BATCH_SIZE` | int | `100` | 否 | 单次 Rclone 版本化健康检查批次大小，范围 `1..1000` |
| `BACKUP_ASSETS_RCLONE_AWS_SDK_MAX_ATTEMPTS` | int | `3` | 否 | Rclone AWS Native SDK 操作最大尝试次数，范围 `1..10` |
| `BACKUP_ASSETS_PROCESSING_QUEUE_MAX` | int | `10000` | 否 | 资产处理持久队列上限，范围 `1..100000` |
| `BACKUP_ASSETS_PROCESSING_INTERACTIVE_SLOTS` | int | `2` | 否 | 交互处理保留槽位，范围 `1..64`；后台回填不能借用 |
| `BACKUP_ASSETS_PROCESSING_BACKGROUND_SLOTS` | int | `2` | 否 | 后台处理槽位，范围 `1..64` |
| `BACKUP_ASSETS_PROCESSING_PULL_LEASE` | duration | `90s` | 否 | Worker pull lease，范围 `15s..5m` |
| `BACKUP_ASSETS_PROCESSING_PULL_HEARTBEAT` | duration | `20s` | 否 | Worker heartbeat，范围 `5s..1m`，且必须小于 pull lease 的一半 |
| `BACKUP_ASSETS_PROCESSING_ATTEMPT_TIMEOUT` | duration | `2h` | 否 | 单次 attempt 绝对超时，范围 `1m..24h`，不得超过 RecoveryPoint lease 绝对截止时间 |
| `BACKUP_ASSETS_PROCESSING_RETRY_MAX` | int | `5` | 否 | transient processing 最大重试次数，范围 `0..20` |
| `BACKUP_ASSETS_PROCESSING_RETRY_BASE` | duration | `5s` | 否 | 重试基础延迟，范围 `1s..5m` |
| `BACKUP_ASSETS_PROCESSING_RETRY_MAX_DELAY` | duration | `15m` | 否 | 重试最大延迟，范围 `1s..2h`，不得小于 retry base |
| `BACKUP_ASSETS_PROCESSING_INPUT_REQUEST_MAX_BYTES` | int | `67108864` | 否 | 单次 Input grant 读取上限，范围 `65536..1073741824` 字节 |
| `BACKUP_ASSETS_PROCESSING_INPUT_CUMULATIVE_MAX_BYTES` | int | `2147483648` | 否 | attempt 累计 Input 上限，范围 `65536..17179869184` 字节，且不得小于单次上限 |
| `BACKUP_ASSETS_PROCESSING_INPUT_MAX_REQUESTS` | int | `512` | 否 | attempt Input 请求数上限，范围 `1..4096` |
| `BACKUP_ASSETS_PROCESSING_INPUT_MAX_IN_FLIGHT` | int | `4` | 否 | attempt Input 并发请求上限，范围 `1..32` |
| `BACKUP_ASSETS_PROCESSING_SINK_MAX_ARTIFACTS` | int | `32` | 否 | 原子 Sink artifact 数量上限，范围 `1..256`；闭合 profile 仍可设置更低硬上限 |
| `BACKUP_ASSETS_PROCESSING_SINK_ARTIFACT_MAX_BYTES` | int | `536870912` | 否 | 单 artifact 上限，范围 `65536..4294967296` 字节 |
| `BACKUP_ASSETS_PROCESSING_SINK_TOTAL_MAX_BYTES` | int | `1073741824` | 否 | 原子 artifact set 总上限，范围 `65536..17179869184` 字节，且不得小于单 artifact 上限 |
| `BACKUP_ASSETS_PROCESSING_PROTOCOL_JSON_MAX_BYTES` | int | `65536` | 否 | Worker/updater 协议 JSON 请求体上限，范围 `4096..1048576` 字节 |
| `BACKUP_ASSETS_PROCESSING_SECRET_CLASSIFY` | bool | `false` | 否 | 启用有限 text/OCR 秘密分类增强；默认关闭，可动态调整 |
| `BACKUP_ASSETS_PROCESSING_BACKFILL_PAUSED` | bool | `true` | 否 | 暂停后台回填；默认暂停，可动态调整，不阻塞显式交互预览 |
| `BACKUP_ASSETS_PROCESSING_BACKFILL_BATCH_SIZE` | int | `100` | 否 | 回填批次大小，范围 `1..10000` |
| `BACKUP_ASSETS_PROCESSING_BACKFILL_JOBS_PER_HOUR` | int | `1000` | 否 | 回填每小时 job 上限，范围 `1..100000` |
| `BACKUP_ASSETS_PROCESSING_BACKFILL_BYTES_PER_HOUR` | int | `10737418240` | 否 | 回填每小时字节上限，范围 `65536..1099511627776` |
| `BACKUP_ASSETS_PROCESSING_BACKFILL_PROVIDER_CONCURRENCY` | int | `1` | 否 | 单 Provider 回填并发上限，范围 `1..32` |
| `BACKUP_ASSETS_PROCESSING_BACKFILL_CAPABILITY_CONCURRENCY` | int | `1` | 否 | 单 capability 回填并发上限，范围 `1..32` |
| `BACKUP_ASSETS_PROCESSING_BACKFILL_RECENT_WINDOW` | duration | `720h` | 否 | recent/history 分界窗口，范围 `24h..8760h` |
| `BACKUP_ASSETS_PROCESSING_BACKFILL_HISTORY_AGING_STEP` | duration | `24h` | 否 | 旧 history 排队老化步长，范围 `1h..720h` |
| `BACKUP_ASSETS_WORKER_LOCAL_ENABLED` | bool | `false` | 否 | 启用本机 parser Worker UDS；默认关闭，变更需重启 |
| `BACKUP_ASSETS_WORKER_LOCAL_SOCKET` | string | `/run/xirang/asset-worker.sock` | 否 | 本机 parser Worker UDS；变更需重启。仓库 `asset-worker` profile 固定覆盖为 `/run/xirang/worker/asset-worker.sock` |
| `BACKUP_ASSETS_WORKER_REMOTE_ENABLED` | bool | `false` | 否 | 启用远程 parser Worker mTLS；默认关闭，变更需重启 |
| `BACKUP_ASSETS_WORKER_REMOTE_LISTEN_ADDR` | string | 空 | 否 | 远程 Worker 专用监听地址；变更需重启，不得复用公开 HTTP 监听 |
| `BACKUP_ASSETS_WORKER_REMOTE_SERVER_CERT_FILE` | string | 空 | 远程模式必填 | 远程 Worker 服务端证书文件；敏感 restart-time 配置 |
| `BACKUP_ASSETS_WORKER_REMOTE_SERVER_KEY_FILE` | string | 空 | 远程模式必填 | 远程 Worker 服务端私钥文件；敏感 restart-time 配置 |
| `BACKUP_ASSETS_WORKER_REMOTE_CLIENT_CA_FILE` | string | 空 | 远程模式必填 | 远程 Worker 客户端 CA 文件；敏感 restart-time 配置 |
| `BACKUP_ASSETS_WORKER_REMOTE_TRUST_DOMAIN` | string | 空 | 远程模式必填 | 远程 Worker SPIFFE trust domain；敏感 restart-time 配置 |
| `BACKUP_ASSETS_WORKER_UPDATER_ENABLED` | bool | `false` | 否 | 启用独立 updater UDS；默认关闭，变更需重启 |
| `BACKUP_ASSETS_WORKER_UPDATER_ONLINE_ENABLED` | bool | `false` | 否 | 启用 updater 受限在线模式；默认关闭，变更需重启；仓库 Compose profile 不提供在线网络 |
| `BACKUP_ASSETS_WORKER_UPDATER_ONLINE_ORIGINS` | string | 空 | online 模式必填 | 逗号分隔的 exact HTTPS origin allowlist；变更需重启，不能替代外部 allowlist proxy/firewall |
| `BACKUP_ASSETS_DERIVED_STORE_ROOT` | string | `/var/lib/xirang-asset-runtime/derived` | 否 | Core 加密 Derived Store 根；变更需重启。Compose profile 在默认路径挂载专用 `asset-worker-derived-store` volume；`/data`、`/backup`、`/logs` 及其子路径会被安全校验拒绝 |
| `BACKUP_ASSETS_EXPORT_ROOT` | string | `/var/lib/xirang-asset-runtime/export` | 否 | Core 加密 Export Store 根；变更需重启。官方 Compose 在默认路径挂载专用 `asset-worker-export-store` volume（`0700:10000:10000`），仅 Core 与 `asset-worker-init` 挂载；parser/updater 不可见。`/data`、`/backup`、`/logs`、Content cache 与 Derived Store 及其子路径会被安全校验拒绝 |
| `BACKUP_ASSETS_DERIVED_STORE_CHUNK_BYTES` | int | `1048576` | 否 | Derived 认证加密分块大小，范围 `65536..8388608` 字节；变更需重启 |
| `BACKUP_ASSETS_DERIVED_STORE_BLOB_MAX_BYTES` | int | `4294967296` | 否 | 单 Derived blob 上限，范围 `65536..17179869184` 字节 |
| `BACKUP_ASSETS_DERIVED_STORE_GLOBAL_MAX_BYTES` | int | `107374182400` | 否 | Derived Store 全局配额，范围 `65536..1099511627776` 字节 |
| `BACKUP_ASSETS_DERIVED_STORE_RECONCILE_INTERVAL` | duration | `15m` | 否 | Derived 状态/引用/blob 对账间隔，范围 `1m..24h` |
| `BACKUP_ASSETS_DERIVED_STORE_RECONCILE_BATCH_SIZE` | int | `256` | 否 | 单次 Derived 对账批次，范围 `1..10000` |

**读取位置**：`RSYNC_BINARY` → `backend/internal/config/config.go`；`RSYNC_ALLOWED_*` / `RSYNC_MIN_FREE_GB` → rsync 任务处理与执行器；`RCLONE_BINARY` / `RESTIC_BINARY` → 对应执行器、完整性检查与备份 Repository 只读适配器；`BATCH_COMMAND_BLACKLIST` → `backend/internal/api/handlers/batch_handler.go`；`FILE_BROWSER_ALLOW_ALL` → `backend/internal/api/handlers/file_handler.go`（仅开发环境允许放开）；`BACKUP_PATH_ALLOW_SHELL_META` → `backend/internal/api/handlers/helpers.go`；`SNAPSHOT_INDEX_MAX_SECONDS` → `backend/internal/snapshot/indexer.go`；`BACKUP_ASSETS_*` → settings 服务的 `backup_assets.*` registry、`backend/internal/backupasset/runtime` 共享运行时与 foundation service（DB override > env > default）。涉及 `BACKUP_ASSETS_ENABLED` 的设置/导入/删除会先检查 GA 就绪门禁；只有就绪（已有安装还须确认当前库存摘要）后才会关闭新的备份资产 admission，排空当前 generation 中已获准的 legacy 访问、Restic/Rsync/Rclone 发布与调和，再提交新值。表中标注 restart-time 的 Worker/updater/Derived/Export 设置不会热切换 listener、身份或存储根；其它 processing/backfill quota 可动态调整并接受组合校验。

`backup_assets.internal.processing_content_pipeline_revision` 与 `backup_assets.internal.processing_ocr_pipeline_revision` 是 Core 原子激活事务维护的内部发布状态，不是环境变量或公共 settings registry 键。它们不会出现在 Settings API/配置导出中，配置导入也会拒绝调用方写入。

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
| `INTEGRATION_BLOCK_PRIVATE_ENDPOINTS` | bool | `true` | 否 | 阻断 webhook/slack/telegram 指向私网地址（`.env.example` 开发值 `true`，生产建议 `true`） |
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
| `VITE_WS_URL` | string | 自动推导 | 否 | 自定义 WebSocket 地址（构建时注入）。若指向非同源主机，生产 Nginx 需同时设置 `CSP_CONNECT_SRC_EXTRA`（见部署文档） |
| `CSP_CONNECT_SRC_EXTRA` | string | 空 | 否 | All-in-One Nginx CSP `connect-src` 附加源（空格分隔，如 `wss://ws.example.com`）；默认仅 `'self'` |
| `VITE_ENABLE_DEMO_MODE` | string | — | 否 | 设为 `true` 启用 mock 数据（仅演示/测试用，不连接真实服务器、SSH Key 或备份存储） |

**读取位置**：`VITE_API_BASE_URL` / `VITE_DEV_API_DIRECT_URL` → `web/src/lib/api/core.ts` 和 WebSocket URL 推导；`VITE_PROXY_TARGET` → `web/vite.config.ts`；`VITE_WS_URL` → `web/src/lib/ws/logs-socket.ts`；`VITE_ENABLE_DEMO_MODE` → `web/src/hooks/use-console-data.ts`、`web/src/components/protected-route.tsx`、登录页与部分 demo 面板。

## 12. 部署变量（Docker Compose / All-in-One 镜像）

以下变量用于 `docker-compose.yml` 或 All-in-One 镜像，不一定是后端应用运行时变量：

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `IMAGE_TAG` | `latest` | 镜像标签；官方镜像固定为 `docker.io/linnea7171/xirang`，`latest` 仅代表最新稳定版，生产环境建议固定为 `vX.Y.Z` |
| `ASSET_WORKER_IMAGE_TAG` | `local` | 仅可选 `asset-worker` profile 的本地构建标签；不是稳定公共镜像标签 |
| `ASSET_WORKER_INBOX_DIR` | `./asset-worker-inbox` | updater-only 固定 inbox bind source；目录必须由 UID/GID `10002:10002` 拥有、mode `0555` 且不是符号链接 |
| `ASSET_WORKER_UPDATER_TRUST_FILE` | `./asset-worker-updater-trust.json` | updater-only Ed25519 trust 文档；文件必须由 UID/GID `10002:10002` 拥有、mode `0440` 且不是符号链接 |

All-in-One 容器固定监听 `10761`，生产 Compose 固定映射 `10761:10761`。HTTPS/TLS 由外部反向代理负责，不通过项目环境变量配置。上述 `ASSET_WORKER_*` 变量只控制仓库内非 GA、本地 build 的可选 profile；不会改变官方 Core image selector，也不表示 Docker Hub/GitHub Release 发布 Worker。

该 profile 的 socket 拓扑不是环境变量：`asset-worker-worker-runtime` 只读提供 parser UDS `/run/xirang/worker/asset-worker.sock`，`asset-worker-updater-runtime` 只读提供 updater UDS `/run/xirang/asset-worker-updater.sock`。Core 同时挂载两者，parser/updater 各自只挂载自己的 volume，且 parser 不加入 updater GID；任何环境变量都不能合并这两个 volume、把 peer socket 暴露给另一进程或放宽 `network_mode: none`。`asset-worker-derived-store` 仅由 Core 与 initializer 挂载，固定在默认 Derived root 并初始化为 `0700:10000:10000`；parser/updater 不可见。全局、本机、远程、updater online 与有限秘密分类默认仍为 `false`，后台回填默认暂停。

---

## 默认值不一致说明

以下变量在 `.env.example`（开发）中设为 `false`，但代码默认值为 `true`，这是有意设计——开发环境放宽限制：

| 变量 | 代码默认值 | `.env.example` | `.env.production.example` |
|------|-----------|----------------|--------------------------|
| `INTEGRATION_BLOCK_PRIVATE_ENDPOINTS` | `true` | `true` | `true` |

> 注意：`SSH_STRICT_HOST_KEY_CHECKING` 和 `SSH_AUTO_ACCEPT_NEW_HOSTS` 已在 v0.44+ 统一为安全默认值（code、`.env.example`、`.env.production.example` 均为 `true` / `false`），不再纳入不一致清单。生产环境启动时若 `SSH_STRICT_HOST_KEY_CHECKING=false` 会记录 warn 日志。

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
| `METRICS_TOKEN` | string | 空 | **生产必填** | Bearer token 保护 `/metrics`。除显式 `APP_ENV`/`ENVIRONMENT=development` 外一律要求非空、≥16 字符且非文档占位符（含未设置 APP_ENV），否则启动失败；开发环境可留空但会打采样 warn；设置后必须 `Authorization: Bearer <token>` |
| `SWAGGER_ENABLED` | bool | 非生产默认 true；生产默认 false | 否 | 是否挂载 `/swagger/*`。生产默认关闭（完整 API 表面无需认证）；显式 `true` 可强制开启 |
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
| `TZ` | string | 未设置时使用系统默认（通常 UTC）；All-in-One 镜像默认 `Asia/Shanghai` | 否 | 容器与应用使用的 IANA 时区（例如 `Asia/Shanghai`、`UTC`）。生产建议显式设置，确保备份文件名、日志时间戳与运维一致。`deploy/allinone/Dockerfile` 已预装 `tzdata`，仅需通过环境变量切换。源码运行时不设置则使用宿主机系统时区 |
| `LOG_FILE` | string | All-in-One 镜像默认 `/logs/xirang.log` | 否 | 设置后应用日志同时写入该文件（保留 stdout 输出供 docker logs/journald 收集）。源码运行留空时仅 stdout |
| `TASK_MAX_EXECUTION_SECONDS` | int | `86400` | 否 | 单次任务执行的全局最大秒数兜底，防 executor 卡死导致 goroutine 泄漏。Policy 级 `max_execution_seconds` >0 时优先于本变量。超时后任务被强制中止并 status=failed，last_error 含"超时"字样 |

**读取位置**：`TZ` → 容器初始化时被 musl 解析，应用层 `time.Now()` 自动遵循；`LOG_FILE` → `backend/internal/logger/logger.go`；`TASK_MAX_EXECUTION_SECONDS` → `backend/internal/task/runner.go`。

---

## 敏感字段加密策略

Xirang 会加密存储密码、私钥、TOTP 密钥、通知通道 endpoint/secret、HTTP 代理地址等敏感字段。生产环境必须设置 `DATA_ENCRYPTION_KEY`；密钥轮替期间可临时设置 `DATA_ENCRYPTION_LEGACY_KEY` 解密旧字段，并 rewrap 备份资产 domain-key v2 envelope。

运营建议：

- 把 `DATA_ENCRYPTION_KEY` 放入环境变量或密钥管理系统，不要写入公开文档、Issue 或日志。
- 轮替密钥时，先设置新的 `DATA_ENCRYPTION_KEY` 并保留旧密钥到 `DATA_ENCRYPTION_LEGACY_KEY`；确认历史字段可读、完成字段重写，并验证所有 domain key 已 rewrap 后再移除旧密钥。
- 备份数据库前确认密钥备份策略；只有数据库备份而没有加密密钥时，敏感字段无法恢复明文。
- 详细安全建议见 [安全加固](admin/security.md)。
