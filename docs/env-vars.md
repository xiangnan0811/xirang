# 环境变量参考

本文记录应用、前端构建和部署模板支持的环境变量；不收录测试夹具、CI 凭据或 Worker 为子进程生成的内部协议变量。示例文件：[后端开发](../backend/.env.example)、[后端生产](../backend/.env.production.example)、[前端](../web/.env.example)、[容器部署](../.env.deploy)。示例中的配置值是场景选择，不一定等于代码默认值。

后端进程不会自动读取 `.env` 文件；源码运行时需要由 shell、systemd、Docker Compose 或 `docker run --env-file` 注入环境变量。Docker Compose 生产模板会读取仓库根目录 `.env`。

## 配置来源与生效时间

- 后端启动配置和进程环境变量变更需要重启。`duration` 使用 Go 时长格式，如 `30s`、`5m`、`24h`，不使用 `1d`。
- 系统设置的合同为 **数据库覆盖值 > 非空环境变量 > 代码默认值**。环境变量不是仅首次启动有效：删除数据库覆盖值后会再次回退到它。Settings API 更新会清除对应缓存；直接改数据库可能等到 30 秒缓存过期。标记“重启”的设置不能热切换已启动的组件。
- `backup_assets.*` 的环境变量映射、默认值和范围列于下表；一般可经 Settings API 调整，重启项单独标记。合法的单项值仍须通过关联校验，不能用环境变量绕过领域安全约束。
- `VITE_*` 在开发服务器或前端构建时读取；修改已经构建好的容器运行时环境不会重写前端静态资源。`CSP_CONNECT_SRC_EXTRA` 则由容器启动时的 Nginx 模板读取。
- 镜像内 Alpine 依赖的精确版本由 Dockerfile 固定，不由 `.env` 或系统设置覆盖。构建时包版本不可用的处理见[镜像构建依赖](maintainers/automation.md#镜像构建依赖)。
- 官方镜像的 Go 工具链由 Dockerfile 构建阶段固定；容器启动时设置 `GOTOOLCHAIN` 不会替换已经编译的程序。源码开发的工具链选择与 linter 入口见[贡献指南](../CONTRIBUTING.md#开发环境)，版本同步要求见[Go 工具链升级](maintainers/automation.md#go-工具链升级)。

**当前实现与配置合同的已知偏差**：节点探测的三个 `node.probe_*` 键、任务流量与执行记录保留键虽然注册在 Settings 服务中，实际组件由 `config.Load()` 的环境值构造，数据库覆盖目前不生效，重启也不能修复这一点。远程指标推送的 URL/token 实现又显式优先读取非空环境变量。配置合同仍要求统一优先级；这些是待修复的实现问题，不应据此放宽合同。当前部署应使用下表所述实际生效方式。

依据：[启动组装](../backend/cmd/server/main.go)、[配置加载](../backend/internal/config/config.go)、[设置注册表及解析](../backend/internal/settings/service.go)、[任务保留](../backend/internal/task/manager.go)。

## 服务器与环境

| 变量 | 类型 | 默认值 | 必填 | 说明 |
|------|------|--------|------|------|
| `SERVER_ADDR` | string | `:8080` | 否 | 源码运行的后端监听地址；All-in-One 镜像默认为 `:3000`，入口就绪探测和 Nginx 上游固定使用 `127.0.0.1:3000`，不要单独覆盖该值 |
| `APP_ENV` | string | — | 否 | 应用环境。**优先**于 `ENVIRONMENT`。仅 `development` 放宽弱密钥/默认 JWT 与 METRICS/Swagger；**未设置**、`production`/`prod`/`staging` 及其它未知值一律按生产硬化（METRICS_TOKEN、CORS 禁止 `*`、Swagger 默认关） |
| `ENVIRONMENT` | string | — | 否 | 仅当 `APP_ENV` 未设置或去除空白后为空时作为回退；若 `APP_ENV=production`（或 prod/staging）则忽略 `ENVIRONMENT=development`（不会放宽密钥策略） |
| `GIN_MODE` | string | — | 否 | Gin 运行模式（`debug` / `release`）。`debug` **不会**放宽密钥策略。生产硬化不依赖 `GIN_MODE`（未声明 APP_ENV 也 hardened）；`APP_ENV=development` 始终优先于 `GIN_MODE=release` |
| `LOG_LEVEL` | string | 空（info） | 否 | 日志级别：`debug` / `info` / `warn` / `error` |

**读取位置**：`SERVER_ADDR` → `backend/internal/config/config.go` 的 `Load`；`APP_ENV` / `ENVIRONMENT` → `backend/internal/util/env.go` 的 `IsDevelopmentEnv` / `IsProductionEnv`；`GIN_MODE` 仅影响 Gin 框架运行模式（`debug`/`release`），**不**决定开发/生产 CSP 或密钥策略——CSP 放宽分支只检查 `IsDevelopmentEnv()`（即 `APP_ENV`/`ENVIRONMENT`），见 `backend/internal/api/router.go`；`LOG_LEVEL` → `backend/cmd/server/main.go`。

## 数据库

| 变量 | 类型 | 默认值 | 必填 | 说明 |
|------|------|--------|------|------|
| `DB_TYPE` | string | `sqlite` | 否 | 数据库类型：`sqlite` / `postgres` |
| `SQLITE_PATH` | string | `./xirang.db` | 否 | SQLite 文件路径；镜像默认 `/data/xirang.db`。单独运行 `scripts/backup-db.sh` 时该脚本默认 `./backend/xirang.db`，建议显式注入同一路径 |
| `DB_DSN` | string | — | PG 时必填 | PostgreSQL 连接串，生产建议 `sslmode=require` |

**读取位置**：`backend/internal/config/config.go` 的 `Load`；系统自助备份接口也会读取 `DB_TYPE` / `SQLITE_PATH`。数据库迁移 dirty 或 clean-version/schema-drift 状态都会无条件拒绝启动，参见[部署指南](deployment.md)。

## 认证与安全

| 变量 | 类型 | 默认值 | 必填 | 说明 |
|------|------|--------|------|------|
| `JWT_SECRET` | string | 开发环境 `xirang-dev-secret` | 生产必填 | JWT 签名密钥，生产环境必须为强随机字符串（≥32 字符） |
| `JWT_TTL` | duration | `24h` | 否 | JWT 有效期 |
| `LOGIN_RATE_LIMIT` | int | `10` | 否 | 登录接口速率限制（次/窗口） |
| `LOGIN_RATE_WINDOW` | duration | `1m` | 否 | 速率限制时间窗口 |
| `LOGIN_FAIL_LOCK_THRESHOLD` | int | `5` | 否 | 同账号、同来源 IP 连续失败锁定阈值；Settings 键 `login.fail_lock_threshold` |
| `LOGIN_FAIL_LOCK_DURATION` | duration | `15m` | 否 | 同账号、同来源 IP 锁定持续时间；Settings 键 `login.fail_lock_duration` |
| `LOGIN_GLOBAL_FAIL_LOCK_THRESHOLD` | int | `50` | 否 | 同账号跨来源 IP 的累计失败锁定阈值，必须大于 0；仅启动时读取 |
| `LOGIN_GLOBAL_FAIL_LOCK_DURATION` | duration | `15m` | 否 | 跨来源 IP 锁定持续时间，必须大于 0；仅启动时读取 |
| `LOGIN_CAPTCHA_ENABLED` | bool | `false` | 否 | 启用登录验证码（settings 键 `login.captcha_enabled`，可通过设置 API 实时调整） |
| `LOGIN_SECOND_CAPTCHA_ENABLED` | bool | `false` | 否 | 启用二次验证码（settings 键 `login.second_captcha_enabled`，可通过设置 API 实时调整） |
| `ADMIN_INITIAL_PASSWORD` | string | — | 尚无用户名为 `admin` 的用户时 | 初始 `admin` 密码；仅用户名为 `admin` 的记录不存在时创建，不是按管理员角色数量判断；已有该用户后不必填，也不会覆盖其密码 |
| `DATA_ENCRYPTION_KEY` | string | 开发环境自动生成随机密钥（重启后失效） | 生产必填 | 敏感字段（密码、私钥）加密密钥，推荐 32 字节随机密钥的 base64；其他字符串经 Argon2id 派生。生产输入至少 16 字符且不能为已知占位符 |
| `XIRANG_BREAK_GLASS_CONFIRMATION` | string | 空 | 管理员应急恢复时 | 仅 `xirang-recover-admin` 显式调用使用，须与交互确认一致；不是自动启动恢复开关，流程见[安全加固](admin/security.md) |
| `DATA_ENCRYPTION_LEGACY_KEY` | string | — | 否 | 密钥轮替期间用于解密历史 v1 字段，并作为上一把 v2 KEK rewrap 小型 domain-key envelope；两类迁移均验证完成后再清理 |

**读取位置**：`JWT_SECRET` / `JWT_TTL` / 登录限流与锁定 → `backend/internal/config/config.go`，部分登录安全项同时注册到 settings 服务；登录验证码 → settings 服务 `login.captcha_enabled` / `login.second_captcha_enabled`；`ADMIN_INITIAL_PASSWORD` → `backend/internal/bootstrap/bootstrap.go`；`DATA_ENCRYPTION_KEY` → `backend/internal/secure/crypto.go`、`backend/internal/secure/keyring.go` 和 `backend/internal/config/config.go`；`DATA_ENCRYPTION_LEGACY_KEY` → `backend/internal/secure/crypto.go` 与 `backend/internal/secure/keyring.go`。

## 跨域与 WebSocket

| 变量 | 类型 | 默认值 | 必填 | 说明 |
|------|------|--------|------|------|
| `CORS_ALLOWED_ORIGINS` | string | `http://localhost:5173,http://127.0.0.1:5173` | 否 | 跨域白名单（逗号分隔），留空时仅放行同主机 Origin（忽略端口）；生产环境禁止 `*` |
| `TRUSTED_PROXIES` | string | `127.0.0.1,::1` | 否 | Gin 可信反向代理列表（逗号分隔 CIDR/IP），仅这些来源的 `X-Forwarded-For` / `X-Real-IP` 会影响 `ClientIP()`（登录/API 限流与审计）。设为空或 `none` 表示不信任任何代理（始终用直连地址）。非法 IP/CIDR **启动失败**（不静默降级）。**切勿**在公网入口上信任全网段 |
| `WS_ALLOW_EMPTY_ORIGIN` | bool | `false` | 否 | WebSocket 是否允许空 Origin |
| `WS_MAX_CONNECTIONS` | int | `100` | 否 | WebSocket 最大连接数 |

**读取位置**：`CORS_ALLOWED_ORIGINS` / `TRUSTED_PROXIES` / `WS_ALLOW_EMPTY_ORIGIN` → `backend/internal/config/config.go`；`WS_MAX_CONNECTIONS` → `backend/internal/ws/hub.go`。

## SSH

| 变量 | 类型 | 默认值 | 必填 | 说明 |
|------|------|--------|------|------|
| `SSH_STRICT_HOST_KEY_CHECKING` | bool | `true` | 否 | 严格校验远端主机指纹，配合 known_hosts 校验主机指纹（生产建议 `true`） |
| `SSH_KNOWN_HOSTS_PATH` | string | `~/.ssh/known_hosts` | 否 | known_hosts 文件路径 |
| `SSH_AUTO_ACCEPT_NEW_HOSTS` | bool | `false` | 否 | 是否自动接受首次出现的主机指纹并写入 known_hosts（settings 键 `ssh.auto_accept_new_hosts`，可在 系统设置 → 安全 实时调整，DB 值优先；生产建议 `false`） |

**读取位置**：`backend/internal/sshutil/ssh_auth.go`（`SSH_STRICT_HOST_KEY_CHECKING`、`SSH_KNOWN_HOSTS_PATH`，以及未安装 settings 取值源时的 `SSH_AUTO_ACCEPT_NEW_HOSTS` 回退）和 `backend/internal/task/executor/executor.go`；运行时自动接受由 settings 服务 `ssh.auto_accept_new_hosts` 决定。All-in-One 镜像默认将 `SSH_KNOWN_HOSTS_PATH` 设为 `/data/.ssh/known_hosts`，使信任或自动接受的主机指纹随数据卷持久化。

## 备份与执行

| 变量 | 类型 | 默认值 | 必填 | 说明 |
|------|------|--------|------|------|
| `RSYNC_BINARY` | string | `rsync` | 否 | rsync 可执行文件路径 |
| `RSYNC_ALLOWED_SOURCE_PREFIXES` | string | 空（不限制） | 否 | 源文件系统允许根（逗号分隔的绝对路径）；配置后启用执行隔离，不是字符串前缀匹配 |
| `RSYNC_ALLOWED_TARGET_PREFIXES` | string | 空（不限制） | 否 | 目标文件系统允许根；只允许根本身或其真实后代，缺少隔离能力时拒绝执行 |
| `RSYNC_CONFINEMENT_HELPER` | string | `xirang-rsync-confined` | 否 | 本地一次性 Rsync 隔离 helper；须与 Core 同版本，要求 Linux Landlock ABI ≥ 3（包括截断保护） |
| `RSYNC_CONFINEMENT_REMOTE_HELPER` | string | `xirang-rsync-confined` | 否 | SSH 远端同版本 helper 的可执行路径；配置白名单时必须部署，不能回退到未隔离 Rsync |
| `RSYNC_MIN_FREE_GB` | int | `0` | 否 | 本地或 SSH 远端目标目录最小剩余空间（GB），`0` 不检查 |
| `RCLONE_BINARY` | string | `rclone` | 否 | rclone 可执行文件路径 |
| `RESTIC_BINARY` | string | `restic` | 否 | restic 可执行文件路径 |
| `BATCH_COMMAND_BLACKLIST` | string | 空（使用内置规则） | 否 | 批量命令黑名单（逗号分隔正则） |
| `FILE_BROWSER_ALLOW_ALL` | string | 空（禁用） | 否 | 仅显式开发环境中，精确值 `true` 允许任意路径；生产设置该值会拒绝文件访问。默认范围为节点 BasePath 和该节点任务的 RsyncSource |
| `BACKUP_PATH_ALLOW_SHELL_META` | bool | `false` | 否 | 仅历史数据救援用；设为 `true` 会跳过备份路径 shell 元字符防御校验 |
| `SNAPSHOT_INDEX_MAX_SECONDS` | int | `1800` | 否 | 旧版 Restic 快照兼容缓存单次最长构建秒数；无效或非正值回退默认值，不控制资产 Search 索引 |

工具读取位置：[Rsync 执行器](../backend/internal/task/executor/executor.go)、[隔离 helper](../backend/internal/rsyncconfinement/command.go)、[文件访问](../backend/internal/api/handlers/file_handler.go)、[快照兼容索引](../backend/internal/snapshot/indexer.go)。

## 备份资产设置

本节默认值、范围和重启标记对应 [Settings 注册表](../backend/internal/settings/service.go)。类型为整数的字节配额单位均为字节。启用开关默认关闭，填写 `true` 只是请求启用，实际仍须满足[备份资产启用合同](spec/domains/backup-enablement.md)。

`BACKUP_ASSETS_CONTENT_ALLOW_INSECURE_PRIVATE_NETWORK` 可动态调整，数据库覆盖值优先；回退到环境值需要先删除覆盖。HTTP 允许范围、可信代理链及票据失效规则见[内容交付合同](spec/domains/backup-content-delivery.md)。Worker 的网络、身份、socket 与存储隔离见[处理与导出合同](spec/domains/backup-processing-export.md)和[部署指南](deployment.md)。内部 pipeline revision 是 Core 维护的发布状态，不是环境变量或公开 Settings 键。

下面每行列出对应 Settings 键，便于区分 `backup_assets.export.root` 等包含多层名称的映射。范围只是单项边界；租约、心跳、总量/单项配额、票据期限和存储根仍须满足关联校验。启用远程 Worker 时必须提供专用监听地址、服务端证书和私钥、客户端 CA 与信任域；启用 updater 在线模式时必须提供精确 HTTPS 来源白名单。

### 基础、目录与发布

| 环境变量 / Settings 键 | 类型 | 代码默认值 | 范围与用途 |
|---|---|---|---|
| `BACKUP_ASSETS_ENABLED`<br>`backup_assets.enabled` | bool | `false` | 启用备份资产领域功能 |
| `BACKUP_ASSETS_CATALOG_BATCH_SIZE`<br>`backup_assets.catalog_batch_size` | int | `2000` | 目录构建批次大小；范围 `1..100000` |
| `BACKUP_ASSETS_CATALOG_BUILD_TIMEOUT`<br>`backup_assets.catalog_build_timeout` | duration | `30m` | 目录构建超时；范围 `1m..24h` |
| `BACKUP_ASSETS_REPOSITORY_RECONCILE_INTERVAL`<br>`backup_assets.repository_reconcile_interval` | duration | `15m` | 备份仓库对账间隔；范围 `1m..24h` |
| `BACKUP_ASSETS_AUDIT_SEGMENT_MAX_EVENTS`<br>`backup_assets.audit_segment_max_events` | int | `10000` | 资产审计分段最大事件数；范围 `100..1000000` |
| `BACKUP_ASSETS_AUDIT_SEGMENT_MAX_AGE`<br>`backup_assets.audit_segment_max_age` | duration | `24h` | 资产审计分段最大持续时间；范围 `1h..168h` |
| `BACKUP_ASSETS_AUDIT_DETAIL_RETENTION_DAYS`<br>`backup_assets.audit_detail_retention_days` | int | `180` | 资产审计明细保留天数；范围 `1..3650` |
| `BACKUP_ASSETS_AUDIT_CHECKPOINT_RETENTION_DAYS`<br>`backup_assets.audit_checkpoint_retention_days` | int | `2555` | 资产审计检查点保留天数；范围 `180..36500` |
| `BACKUP_ASSETS_LEASE_DURATION`<br>`backup_assets.lease_duration` | duration | `5m` | RecoveryPoint 短租约时长；范围 `30s..30m` |
| `BACKUP_ASSETS_LEASE_HEARTBEAT`<br>`backup_assets.lease_heartbeat` | duration | `60s` | RecoveryPoint 租约心跳间隔；范围 `10s..5m` |
| `BACKUP_ASSETS_LEASE_ABSOLUTE_DEADLINE`<br>`backup_assets.lease_absolute_deadline` | duration | `168h` | RecoveryPoint 租约绝对截止时间；范围 `5m..168h` |
| `BACKUP_ASSETS_PROVIDER_OPERATION_TIMEOUT`<br>`backup_assets.provider_operation_timeout` | duration | `2m` | Provider 只读操作超时；范围 `5s..30m` |
| `BACKUP_ASSETS_PROVIDER_MAX_CONCURRENCY`<br>`backup_assets.provider_max_concurrency` | int | `4` | Provider 只读操作最大并发数；范围 `1..32` |
| `BACKUP_ASSETS_PROVIDER_METADATA_LIMIT_BYTES`<br>`backup_assets.provider_metadata_limit_bytes` | int | `16777216` | Provider 元数据输出字节上限；范围 `65536..67108864` |
| `BACKUP_ASSETS_PUBLICATION_RECONCILE_INTERVAL`<br>`backup_assets.publication_reconcile_interval` | duration | `5m` | 恢复点发布对账间隔；范围 `30s..24h` |
| `BACKUP_ASSETS_PUBLICATION_RECONCILE_BATCH_SIZE`<br>`backup_assets.publication_reconcile_batch_size` | int | `100` | 恢复点发布对账批次大小；范围 `1..1000` |
| `BACKUP_ASSETS_PUBLICATION_WORKER_CONCURRENCY`<br>`backup_assets.publication_worker_concurrency` | int | `2` | 恢复点发布工作并发数；范围 `1..32` |
| `BACKUP_ASSETS_PUBLICATION_MISSING_GRACE`<br>`backup_assets.publication_missing_grace` | duration | `30m` | 发布快照缺失宽限期；范围 `1m..24h` |
| `BACKUP_ASSETS_PUBLICATION_STREAM_MAX_BYTES`<br>`backup_assets.publication_stream_max_bytes` | int | `268435456` | 发布备份 JSON 流总字节上限；范围 `1048576..1073741824` |
| `BACKUP_ASSETS_MANIFEST_TIMEOUT`<br>`backup_assets.manifest_timeout` | duration | `2h` | 恢复点清单构建超时；范围 `1m..24h` |
| `BACKUP_ASSETS_MANIFEST_MAX_BYTES`<br>`backup_assets.manifest_max_bytes` | int | `4294967296` | 恢复点清单总字节上限；范围 `1048576..17179869184` |
| `BACKUP_ASSETS_MANIFEST_MAX_ENTRIES`<br>`backup_assets.manifest_max_entries` | int | `10000000` | 恢复点清单条目上限；范围 `1..100000000` |
| `BACKUP_ASSETS_MANIFEST_MAX_RECORD_BYTES`<br>`backup_assets.manifest_max_record_bytes` | int | `1048576` | 恢复点清单单记录字节上限；范围 `4096..4194304` |
| `BACKUP_ASSETS_MANIFEST_MAX_DEPTH`<br>`backup_assets.manifest_max_depth` | int | `4096` | 恢复点清单目录深度上限；范围 `1..65536` |
| `BACKUP_ASSETS_RCLONE_PREFLIGHT_TTL`<br>`backup_assets.rclone_preflight_ttl` | duration | `30m` | Rclone 版本化预检有效期；范围 `16m..24h` |
| `BACKUP_ASSETS_RCLONE_PORTABLE_DEADLINE`<br>`backup_assets.rclone_portable_deadline` | duration | `24h` | Rclone portable 恢复点绝对时限；范围 `5m..168h` |
| `BACKUP_ASSETS_RCLONE_NATIVE_DEADLINE`<br>`backup_assets.rclone_native_deadline` | duration | `45m` | Rclone native 恢复点绝对时限；范围 `5m..55m` |
| `BACKUP_ASSETS_RCLONE_BOUND_CONFIG_MAX_BYTES`<br>`backup_assets.rclone_bound_config_max_bytes` | int | `65536` | Rclone 绑定配置最大字节数；范围 `1024..65536` |
| `BACKUP_ASSETS_RCLONE_CONTROL_PAYLOAD_MAX_BYTES`<br>`backup_assets.rclone_control_payload_max_bytes` | int | `8388608` | Rclone 控制对象暂存最大字节数；范围 `65536..67108864` |
| `BACKUP_ASSETS_RCLONE_FULL_VERIFY_MAX_BYTES`<br>`backup_assets.rclone_full_verify_max_bytes` | int | `1099511627776` | Rclone 全字节校验最大读取量；范围 `1048576..17592186044416` |
| `BACKUP_ASSETS_RCLONE_MANIFEST_CHUNK_MAX_BYTES`<br>`backup_assets.rclone_manifest_chunk_max_bytes` | int | `8388608` | Rclone 清单分块最大字节数；范围 `65536..67108864` |
| `BACKUP_ASSETS_RCLONE_LOW_LEVEL_RETRIES`<br>`backup_assets.rclone_low_level_retries` | int | `3` | Rclone 单次 attempt 低层重试次数；范围 `1..10` |
| `BACKUP_ASSETS_RCLONE_STAGING_ORPHAN_AGE`<br>`backup_assets.rclone_staging_orphan_age` | duration | `24h` | Rclone 暂存孤儿判定年龄；范围 `1h..168h` |
| `BACKUP_ASSETS_RCLONE_STAGING_SCAN_LIMIT`<br>`backup_assets.rclone_staging_scan_limit` | int | `256` | Rclone 暂存孤儿扫描批次；范围 `1..4096` |
| `BACKUP_ASSETS_RCLONE_KMS_READ_KEY_MAX_COUNT`<br>`backup_assets.rclone_kms_read_key_max_count` | int | `8` | Rclone KMS 保留读取密钥数量上限；范围 `1..32` |
| `BACKUP_ASSETS_RCLONE_HEALTH_INTERVAL`<br>`backup_assets.rclone_health_interval` | duration | `15m` | Rclone 版本化健康检查间隔；范围 `1m..24h` |
| `BACKUP_ASSETS_RCLONE_HEALTH_BATCH_SIZE`<br>`backup_assets.rclone_health_batch_size` | int | `100` | Rclone 版本化健康检查批次；范围 `1..1000` |
| `BACKUP_ASSETS_RCLONE_AWS_SDK_MAX_ATTEMPTS`<br>`backup_assets.rclone_aws_sdk_max_attempts` | int | `3` | Rclone AWS SDK 最大尝试次数；范围 `1..10` |

### 保留与生命周期

| 环境变量 / Settings 键 | 类型 | 代码默认值 | 范围与用途 |
|---|---|---|---|
| `BACKUP_ASSETS_RETENTION_RECONCILE_INTERVAL`<br>`backup_assets.retention_reconcile_interval` | duration | `5m` | 备份资产保留策略协调间隔；范围 `30s..24h` |
| `BACKUP_ASSETS_RETENTION_BATCH_SIZE`<br>`backup_assets.retention_batch_size` | int | `100` | 备份资产保留策略单批处理上限；范围 `1..1000` |
| `BACKUP_ASSETS_RETENTION_DRAIN_TIMEOUT`<br>`backup_assets.retention_drain_timeout` | duration | `30s` | 备份资产保留策略读取排空超时；范围 `5s..30m` |

### 内容交付与缓存

| 环境变量 / Settings 键 | 类型 | 代码默认值 | 范围与用途 |
|---|---|---|---|
| `BACKUP_ASSETS_CONTENT_PREVIEW_TTL`<br>`backup_assets.content_preview_ttl` | duration | `2m` | 备份内容预览票据绝对有效期；范围 `15s..10m` |
| `BACKUP_ASSETS_CONTENT_MEDIA_TTL`<br>`backup_assets.content_media_ttl` | duration | `15m` | 备份媒体与下载票据绝对有效期；范围 `1m..30m` |
| `BACKUP_ASSETS_CONTENT_IDLE_TTL`<br>`backup_assets.content_idle_ttl` | duration | `60s` | 备份内容会话空闲有效期；范围 `15s..10m` |
| `BACKUP_ASSETS_CONTENT_WRITE_IDLE_TIMEOUT`<br>`backup_assets.content_write_idle_timeout` | duration | `30s` | 备份内容流单次写入空闲超时；范围 `5s..2m` |
| `BACKUP_ASSETS_CONTENT_TICKET_TIMEOUT`<br>`backup_assets.content_ticket_timeout` | duration | `20s` | 备份内容签票处理超时；范围 `1s..25s` |
| `BACKUP_ASSETS_CONTENT_REQUEST_MAX_BYTES`<br>`backup_assets.content_request_max_bytes` | int | `67108864` | 单次备份内容请求最大字节数；范围 `65536..1073741824` |
| `BACKUP_ASSETS_CONTENT_CUMULATIVE_MAX_BYTES`<br>`backup_assets.content_cumulative_max_bytes` | int | `536870912` | 单张备份内容票据累计最大字节数；范围 `65536..8589934592` |
| `BACKUP_ASSETS_CONTENT_MAX_REQUESTS`<br>`backup_assets.content_max_requests` | int | `256` | 单张备份内容票据最大请求数；范围 `1..4096` |
| `BACKUP_ASSETS_CONTENT_GRANT_MAX_IN_FLIGHT`<br>`backup_assets.content_grant_max_in_flight` | int | `2` | 单张备份内容票据最大并发数；范围 `1..8` |
| `BACKUP_ASSETS_CONTENT_USER_MAX_CONCURRENCY`<br>`backup_assets.content_user_max_concurrency` | int | `4` | 单用户备份内容最大并发数；范围 `1..32` |
| `BACKUP_ASSETS_CONTENT_PROVIDER_MAX_CONCURRENCY`<br>`backup_assets.content_provider_max_concurrency` | int | `4` | 单 Provider 备份内容最大并发数；范围 `1..32` |
| `BACKUP_ASSETS_CONTENT_GLOBAL_MAX_CONCURRENCY`<br>`backup_assets.content_global_max_concurrency` | int | `16` | 全局备份内容最大并发数；范围 `1..128` |
| `BACKUP_ASSETS_CONTENT_RATE_WINDOW`<br>`backup_assets.content_rate_window` | duration | `1m` | 备份内容范围预算窗口；范围 `10s..10m` |
| `BACKUP_ASSETS_CONTENT_USER_WINDOW_BYTES`<br>`backup_assets.content_user_window_bytes` | int | `1073741824` | 单用户窗口字节预算；范围 `65536..17179869184` |
| `BACKUP_ASSETS_CONTENT_PROVIDER_WINDOW_BYTES`<br>`backup_assets.content_provider_window_bytes` | int | `4294967296` | 单 Provider 窗口字节预算；范围 `65536..68719476736` |
| `BACKUP_ASSETS_CONTENT_GLOBAL_WINDOW_BYTES`<br>`backup_assets.content_global_window_bytes` | int | `8589934592` | 全局窗口字节预算；范围 `65536..137438953472` |
| `BACKUP_ASSETS_CONTENT_USER_WINDOW_REQUESTS`<br>`backup_assets.content_user_window_requests` | int | `1024` | 单用户窗口请求预算；范围 `1..65536` |
| `BACKUP_ASSETS_CONTENT_PROVIDER_WINDOW_REQUESTS`<br>`backup_assets.content_provider_window_requests` | int | `4096` | 单 Provider 窗口请求预算；范围 `1..262144` |
| `BACKUP_ASSETS_CONTENT_GLOBAL_WINDOW_REQUESTS`<br>`backup_assets.content_global_window_requests` | int | `8192` | 全局窗口请求预算；范围 `1..1048576` |
| `BACKUP_ASSETS_CONTENT_CLASSIFICATION_SCAN_BYTES`<br>`backup_assets.content_classification_scan_bytes` | int | `262144` | 备份内容分类扫描字节上限；范围 `4096..4194304` |
| `BACKUP_ASSETS_CONTENT_TEXT_PREVIEW_BYTES`<br>`backup_assets.content_text_preview_bytes` | int | `1048576` | 备份文本预览字节上限；范围 `4096..16777216` |
| `BACKUP_ASSETS_CONTENT_HEX_PREVIEW_BYTES`<br>`backup_assets.content_hex_preview_bytes` | int | `65536` | 备份十六进制预览字节上限；范围 `1024..1048576` |
| `BACKUP_ASSETS_CONTENT_RASTER_MAX_PIXELS`<br>`backup_assets.content_raster_max_pixels` | int | `100000000` | 备份栅格预览像素上限；范围 `1000000..250000000` |
| `BACKUP_ASSETS_CONTENT_MEMORY_GLOBAL_BYTES`<br>`backup_assets.content_memory_global_bytes` | int | `67108864` | 备份内容内存缓存全局字节上限；范围 `1048576..1073741824` |
| `BACKUP_ASSETS_CONTENT_MEMORY_OBJECT_BYTES`<br>`backup_assets.content_memory_object_bytes` | int | `4194304` | 备份内容内存缓存单对象字节上限；范围 `65536..1073741824` |
| `BACKUP_ASSETS_CONTENT_MEMORY_USER_BYTES`<br>`backup_assets.content_memory_user_bytes` | int | `16777216` | 备份内容内存缓存单用户字节上限；范围 `65536..1073741824` |
| `BACKUP_ASSETS_CONTENT_MEMORY_PROVIDER_BYTES`<br>`backup_assets.content_memory_provider_bytes` | int | `33554432` | 备份内容内存缓存单 Provider 字节上限；范围 `65536..1073741824` |
| `BACKUP_ASSETS_CONTENT_CACHE_ENABLED`<br>`backup_assets.content_cache_enabled` | bool | `true` | 启用备份内容认证磁盘缓存 |
| `BACKUP_ASSETS_CONTENT_CACHE_ROOT`<br>`backup_assets.content_cache_root` | string | `/var/cache/xirang/asset-content` | 备份内容认证磁盘缓存专用根目录 |
| `BACKUP_ASSETS_CONTENT_CACHE_CHUNK_BYTES`<br>`backup_assets.content_cache_chunk_bytes` | int | `1048576` | 备份内容缓存分块字节数；范围 `65536..8388608` |
| `BACKUP_ASSETS_CONTENT_CACHE_OBJECT_BYTES`<br>`backup_assets.content_cache_object_bytes` | int | `536870912` | 备份内容缓存单对象字节上限；范围 `65536..8589934592` |
| `BACKUP_ASSETS_CONTENT_CACHE_USER_BYTES`<br>`backup_assets.content_cache_user_bytes` | int | `2147483648` | 备份内容缓存单用户字节上限；范围 `65536..34359738368` |
| `BACKUP_ASSETS_CONTENT_CACHE_PROVIDER_BYTES`<br>`backup_assets.content_cache_provider_bytes` | int | `4294967296` | 备份内容缓存单 Provider 字节上限；范围 `65536..68719476736` |
| `BACKUP_ASSETS_CONTENT_CACHE_GLOBAL_BYTES`<br>`backup_assets.content_cache_global_bytes` | int | `8589934592` | 备份内容缓存全局字节上限；范围 `65536..137438953472` |
| `BACKUP_ASSETS_CONTENT_CACHE_OBJECT_FILES`<br>`backup_assets.content_cache_object_files` | int | `1024` | 备份内容缓存单对象文件上限；范围 `2..131072` |
| `BACKUP_ASSETS_CONTENT_CACHE_USER_FILES`<br>`backup_assets.content_cache_user_files` | int | `4096` | 备份内容缓存单用户文件上限；范围 `2..262144` |
| `BACKUP_ASSETS_CONTENT_CACHE_PROVIDER_FILES`<br>`backup_assets.content_cache_provider_files` | int | `8192` | 备份内容缓存单 Provider 文件上限；范围 `2..262144` |
| `BACKUP_ASSETS_CONTENT_CACHE_GLOBAL_FILES`<br>`backup_assets.content_cache_global_files` | int | `16384` | 备份内容缓存全局文件上限；范围 `16..262144` |
| `BACKUP_ASSETS_CONTENT_CACHE_IDLE_TTL`<br>`backup_assets.content_cache_idle_ttl` | duration | `15m` | 备份内容缓存空闲有效期；范围 `1m..24h` |
| `BACKUP_ASSETS_CONTENT_CACHE_ABSOLUTE_TTL`<br>`backup_assets.content_cache_absolute_ttl` | duration | `2h` | 备份内容缓存绝对有效期；范围 `1m..24h` |
| `BACKUP_ASSETS_CONTENT_RECONCILE_INTERVAL`<br>`backup_assets.content_reconcile_interval` | duration | `1m` | 备份内容状态对账间隔；范围 `10s..1h` |
| `BACKUP_ASSETS_CONTENT_RECONCILE_BATCH_SIZE`<br>`backup_assets.content_reconcile_batch_size` | int | `100` | 备份内容状态对账批次；范围 `1..1000` |
| `BACKUP_ASSETS_CONTENT_AUDIT_BACKLOG_MAX`<br>`backup_assets.content_audit_backlog_max` | int | `10000` | 备份内容审计积压上限；范围 `100..100000` |
| `BACKUP_ASSETS_CONTENT_ALLOW_INSECURE_LOOPBACK`<br>`backup_assets.content_allow_insecure_loopback` | bool | `false` | 仅允许受控本机 HTTP 开发票据 Cookie |
| `BACKUP_ASSETS_CONTENT_ALLOW_INSECURE_PRIVATE_NETWORK`<br>`backup_assets.content_allow_insecure_private_network` | bool | `false` | 允许私有网络通过 HTTP 交付备份内容 |

### 搜索与用户视图

| 环境变量 / Settings 键 | 类型 | 代码默认值 | 范围与用途 |
|---|---|---|---|
| `BACKUP_ASSETS_SEARCH_RECONCILE_INTERVAL`<br>`backup_assets.search_reconcile_interval` | duration | `1m` | 资产搜索索引对账间隔；范围 `10s..1h` |
| `BACKUP_ASSETS_SEARCH_BUILD_TIMEOUT`<br>`backup_assets.search_build_timeout` | duration | `30m` | 资产搜索索引构建超时；范围 `1m..24h` |
| `BACKUP_ASSETS_SEARCH_BATCH_SIZE`<br>`backup_assets.search_batch_size` | int | `500` | 资产搜索索引构建批次；范围 `50..5000` |
| `BACKUP_ASSETS_SEARCH_MAX_CONCURRENCY`<br>`backup_assets.search_max_concurrency` | int | `2` | 资产搜索索引最大并发；范围 `1..16` |
| `BACKUP_ASSETS_SEARCH_AST_MAX_DEPTH`<br>`backup_assets.search_ast_max_depth` | int | `8` | 资产搜索 AST 最大深度；范围 `1..16` |
| `BACKUP_ASSETS_SEARCH_AST_MAX_NODES`<br>`backup_assets.search_ast_max_nodes` | int | `64` | 资产搜索 AST 最大节点数；范围 `2..256` |
| `BACKUP_ASSETS_SEARCH_VALUES_PER_NODE`<br>`backup_assets.search_values_per_node` | int | `32` | 资产搜索 AST 单节点最大值数；范围 `1..64` |
| `BACKUP_ASSETS_SEARCH_BODY_MAX_BYTES`<br>`backup_assets.search_body_max_bytes` | int | `65536` | 资产搜索请求体最大字节数；范围 `1024..65536` |
| `BACKUP_ASSETS_SEARCH_VALUE_MAX_BYTES`<br>`backup_assets.search_value_max_bytes` | int | `1024` | 资产搜索单值最大字节数；范围 `1..4096` |
| `BACKUP_ASSETS_SEARCH_CANDIDATE_LIMIT`<br>`backup_assets.search_candidate_limit` | int | `10000` | 资产搜索候选上限；范围 `100..100000` |
| `BACKUP_ASSETS_SEARCH_QUERY_TIMEOUT`<br>`backup_assets.search_query_timeout` | duration | `5s` | 资产搜索查询超时；范围 `100ms..30s` |
| `BACKUP_ASSETS_SEARCH_PAGE_SIZE_MAX`<br>`backup_assets.search_page_size_max` | int | `200` | 资产搜索单页最大条目数；范围 `1..500` |
| `BACKUP_ASSETS_SEARCH_SUGGESTION_LIMIT`<br>`backup_assets.search_suggestion_limit` | int | `20` | 资产搜索建议上限；范围 `0..50` |
| `BACKUP_ASSETS_SAVED_SEARCH_QUOTA`<br>`backup_assets.saved_search_quota` | int | `100` | 每用户保存搜索配额；范围 `1..1000` |
| `BACKUP_ASSETS_FAVORITE_QUOTA`<br>`backup_assets.favorite_quota` | int | `5000` | 每用户资产收藏配额；范围 `1..100000` |
| `BACKUP_ASSETS_TAG_DEFINITION_QUOTA`<br>`backup_assets.tag_definition_quota` | int | `100` | 每用户资产标签定义配额；范围 `1..1000` |
| `BACKUP_ASSETS_TAG_ASSIGNMENT_QUOTA`<br>`backup_assets.tag_assignment_quota` | int | `10000` | 每用户资产标签绑定配额；范围 `1..200000` |
| `BACKUP_ASSETS_OVERLAY_BULK_MAX_ITEMS`<br>`backup_assets.overlay_bulk_max_items` | int | `200` | 资产用户覆盖批量操作上限；范围 `1..1000` |
| `BACKUP_ASSETS_OVERLAY_LABEL_MAX_BYTES`<br>`backup_assets.overlay_label_max_bytes` | int | `256` | 资产用户标签最大字节数；范围 `1..4096` |
| `BACKUP_ASSETS_RECENT_QUOTA`<br>`backup_assets.recent_quota` | int | `10000` | 每用户最近访问资产配额；范围 `1..100000` |
| `BACKUP_ASSETS_RECENT_RETENTION`<br>`backup_assets.recent_retention` | duration | `720h` | 最近访问资产保留时长；范围 `24h..8760h` |
| `BACKUP_ASSETS_RECENT_WRITES_PER_MINUTE`<br>`backup_assets.recent_writes_per_minute` | int | `120` | 每用户最近访问每分钟写入上限；范围 `1..10000` |
| `BACKUP_ASSETS_IDEMPOTENCY_TTL`<br>`backup_assets.idempotency_ttl` | duration | `24h` | 资产用户覆盖幂等回执保留时长；范围 `1h..168h` |
| `BACKUP_ASSETS_IDEMPOTENCY_KEY_MAX_BYTES`<br>`backup_assets.idempotency_key_max_bytes` | int | `128` | 资产用户覆盖幂等键最大字节数；范围 `32..256` |

### 处理、回填与 Worker

| 环境变量 / Settings 键 | 类型 | 代码默认值 | 范围与用途 |
|---|---|---|---|
| `BACKUP_ASSETS_PROCESSING_QUEUE_MAX`<br>`backup_assets.processing_queue_max` | int | `10000` | 资产处理持久队列上限；范围 `1..100000` |
| `BACKUP_ASSETS_PROCESSING_INTERACTIVE_SLOTS`<br>`backup_assets.processing_interactive_slots` | int | `2` | 资产处理交互保留槽位；范围 `1..64` |
| `BACKUP_ASSETS_PROCESSING_BACKGROUND_SLOTS`<br>`backup_assets.processing_background_slots` | int | `2` | 资产处理后台槽位；范围 `1..64` |
| `BACKUP_ASSETS_PROCESSING_PULL_LEASE`<br>`backup_assets.processing_pull_lease` | duration | `90s` | 资产 Worker 拉取租约时长；范围 `15s..5m` |
| `BACKUP_ASSETS_PROCESSING_PULL_HEARTBEAT`<br>`backup_assets.processing_pull_heartbeat` | duration | `20s` | 资产 Worker 拉取租约心跳；范围 `5s..1m` |
| `BACKUP_ASSETS_PROCESSING_ATTEMPT_TIMEOUT`<br>`backup_assets.processing_attempt_timeout` | duration | `2h` | 资产处理 attempt 绝对超时；范围 `1m..24h` |
| `BACKUP_ASSETS_PROCESSING_RETRY_MAX`<br>`backup_assets.processing_retry_max` | int | `5` | 资产处理最大重试次数；范围 `0..20` |
| `BACKUP_ASSETS_PROCESSING_RETRY_BASE`<br>`backup_assets.processing_retry_base` | duration | `5s` | 资产处理重试基础延迟；范围 `1s..5m` |
| `BACKUP_ASSETS_PROCESSING_RETRY_MAX_DELAY`<br>`backup_assets.processing_retry_max_delay` | duration | `15m` | 资产处理重试最大延迟；范围 `1s..2h` |
| `BACKUP_ASSETS_PROCESSING_INPUT_REQUEST_MAX_BYTES`<br>`backup_assets.processing_input_request_max_bytes` | int | `67108864` | Worker Input 单次读取字节上限；范围 `65536..1073741824` |
| `BACKUP_ASSETS_PROCESSING_INPUT_CUMULATIVE_MAX_BYTES`<br>`backup_assets.processing_input_cumulative_max_bytes` | int | `2147483648` | Worker Input attempt 累计读取字节上限；范围 `65536..17179869184` |
| `BACKUP_ASSETS_PROCESSING_INPUT_MAX_REQUESTS`<br>`backup_assets.processing_input_max_requests` | int | `512` | Worker Input attempt 请求上限；范围 `1..4096` |
| `BACKUP_ASSETS_PROCESSING_INPUT_MAX_IN_FLIGHT`<br>`backup_assets.processing_input_max_in_flight` | int | `4` | Worker Input attempt 并发请求上限；范围 `1..32` |
| `BACKUP_ASSETS_PROCESSING_SINK_MAX_ARTIFACTS`<br>`backup_assets.processing_sink_max_artifacts` | int | `32` | Worker Sink 原子产物数量上限；范围 `1..256` |
| `BACKUP_ASSETS_PROCESSING_SINK_ARTIFACT_MAX_BYTES`<br>`backup_assets.processing_sink_artifact_max_bytes` | int | `536870912` | Worker Sink 单产物字节上限；范围 `65536..4294967296` |
| `BACKUP_ASSETS_PROCESSING_SINK_TOTAL_MAX_BYTES`<br>`backup_assets.processing_sink_total_max_bytes` | int | `1073741824` | Worker Sink 原子产物集总字节上限；范围 `65536..17179869184` |
| `BACKUP_ASSETS_PROCESSING_PROTOCOL_JSON_MAX_BYTES`<br>`backup_assets.processing_protocol_json_max_bytes` | int | `65536` | Worker 协议 JSON 请求体上限；范围 `4096..1048576` |
| `BACKUP_ASSETS_PROCESSING_SECRET_CLASSIFY`<br>`backup_assets.processing_secret_classify` | bool | `false` | 启用有限秘密分类增强 |
| `BACKUP_ASSETS_PROCESSING_BACKFILL_PAUSED`<br>`backup_assets.processing_backfill_paused` | bool | `true` | 暂停资产处理后台回填 |
| `BACKUP_ASSETS_PROCESSING_BACKFILL_BATCH_SIZE`<br>`backup_assets.processing_backfill_batch_size` | int | `100` | 资产处理回填批次大小；范围 `1..10000` |
| `BACKUP_ASSETS_PROCESSING_BACKFILL_JOBS_PER_HOUR`<br>`backup_assets.processing_backfill_jobs_per_hour` | int | `1000` | 资产处理回填每小时任务上限；范围 `1..100000` |
| `BACKUP_ASSETS_PROCESSING_BACKFILL_BYTES_PER_HOUR`<br>`backup_assets.processing_backfill_bytes_per_hour` | int | `10737418240` | 资产处理回填每小时字节上限；范围 `65536..1099511627776` |
| `BACKUP_ASSETS_PROCESSING_BACKFILL_PROVIDER_CONCURRENCY`<br>`backup_assets.processing_backfill_provider_concurrency` | int | `1` | 资产处理回填单 Provider 并发上限；范围 `1..32` |
| `BACKUP_ASSETS_PROCESSING_BACKFILL_CAPABILITY_CONCURRENCY`<br>`backup_assets.processing_backfill_capability_concurrency` | int | `1` | 资产处理回填单能力并发上限；范围 `1..32` |
| `BACKUP_ASSETS_PROCESSING_BACKFILL_RECENT_WINDOW`<br>`backup_assets.processing_backfill_recent_window` | duration | `720h` | 资产处理近期回填窗口；范围 `24h..8760h` |
| `BACKUP_ASSETS_PROCESSING_BACKFILL_HISTORY_AGING_STEP`<br>`backup_assets.processing_backfill_history_aging_step` | duration | `24h` | 资产处理历史回填老化步长；范围 `1h..720h` |
| `BACKUP_ASSETS_WORKER_LOCAL_ENABLED`<br>`backup_assets.worker_local_enabled` | bool | `false` | 启用本机资产 Worker 传输；**重启生效** |
| `BACKUP_ASSETS_WORKER_LOCAL_SOCKET`<br>`backup_assets.worker_local_socket` | string | `/run/xirang/asset-worker.sock` | 本机资产 Worker Unix socket；**重启生效**；仓库 Compose 固定覆盖为 `/run/xirang/worker/asset-worker.sock` |
| `BACKUP_ASSETS_WORKER_REMOTE_ENABLED`<br>`backup_assets.worker_remote_enabled` | bool | `false` | 启用远程资产 Worker mTLS 传输；**重启生效** |
| `BACKUP_ASSETS_WORKER_REMOTE_LISTEN_ADDR`<br>`backup_assets.worker_remote_listen_addr` | string | 空 | 远程资产 Worker 专用监听地址；**重启生效** |
| `BACKUP_ASSETS_WORKER_REMOTE_SERVER_CERT_FILE`<br>`backup_assets.worker_remote_server_cert_file` | string | 空 | 远程资产 Worker 服务端证书路径；**重启生效** |
| `BACKUP_ASSETS_WORKER_REMOTE_SERVER_KEY_FILE`<br>`backup_assets.worker_remote_server_key_file` | string | 空 | 远程资产 Worker 服务端私钥路径；**重启生效** |
| `BACKUP_ASSETS_WORKER_REMOTE_CLIENT_CA_FILE`<br>`backup_assets.worker_remote_client_ca_file` | string | 空 | 远程资产 Worker 客户端 CA 路径；**重启生效** |
| `BACKUP_ASSETS_WORKER_REMOTE_TRUST_DOMAIN`<br>`backup_assets.worker_remote_trust_domain` | string | 空 | 远程资产 Worker SPIFFE 信任域；**重启生效** |
| `BACKUP_ASSETS_WORKER_UPDATER_ENABLED`<br>`backup_assets.worker_updater_enabled` | bool | `false` | 启用独立资产 Worker updater；**重启生效** |
| `BACKUP_ASSETS_WORKER_UPDATER_ONLINE_ENABLED`<br>`backup_assets.worker_updater_online_enabled` | bool | `false` | 启用 updater 受限在线模式；**重启生效** |
| `BACKUP_ASSETS_WORKER_UPDATER_ONLINE_ORIGINS`<br>`backup_assets.worker_updater_online_origins` | string | 空 | updater 精确 HTTPS origin allowlist；**重启生效** |
| `BACKUP_ASSETS_DERIVED_STORE_ROOT`<br>`backup_assets.derived_store_root` | string | `/var/lib/xirang-asset-runtime/derived` | 加密派生资产专用根目录；**重启生效** |
| `BACKUP_ASSETS_DERIVED_STORE_CHUNK_BYTES`<br>`backup_assets.derived_store_chunk_bytes` | int | `1048576` | 派生资产认证加密分块字节数；范围 `65536..8388608`；**重启生效** |
| `BACKUP_ASSETS_DERIVED_STORE_BLOB_MAX_BYTES`<br>`backup_assets.derived_store_blob_max_bytes` | int | `4294967296` | 派生资产单 blob 字节上限；范围 `65536..17179869184` |
| `BACKUP_ASSETS_DERIVED_STORE_GLOBAL_MAX_BYTES`<br>`backup_assets.derived_store_global_max_bytes` | int | `107374182400` | 派生资产全局字节配额；范围 `65536..1099511627776` |
| `BACKUP_ASSETS_DERIVED_STORE_RECONCILE_INTERVAL`<br>`backup_assets.derived_store_reconcile_interval` | duration | `15m` | 派生资产对账间隔；范围 `1m..24h` |
| `BACKUP_ASSETS_DERIVED_STORE_RECONCILE_BATCH_SIZE`<br>`backup_assets.derived_store_reconcile_batch_size` | int | `256` | 派生资产对账批次；范围 `1..10000` |

### 受控恢复

| 环境变量 / Settings 键 | 类型 | 代码默认值 | 范围与用途 |
|---|---|---|---|
| `BACKUP_ASSETS_RECOVERY_RECEIPT_REPLAY_TTL`<br>`backup_assets.recovery.receipt_replay_ttl` | duration | `20m` | 恢复授权回执回放与保留有效期；范围 `5m..24h` |
| `BACKUP_ASSETS_RECOVERY_WRITE_GRANT_TTL`<br>`backup_assets.recovery.write_grant_ttl` | duration | `15m` | 恢复写入授权有效期；范围 `1m..24h` |
| `BACKUP_ASSETS_RECOVERY_DELETE_GRANT_TTL`<br>`backup_assets.recovery.delete_grant_ttl` | duration | `10m` | 恢复精确镜像删除授权有效期；范围 `1m..24h` |
| `BACKUP_ASSETS_RECOVERY_RECEIPT_REAPER_CADENCE`<br>`backup_assets.recovery.receipt_reaper_cadence` | duration | `1m` | 恢复授权回执清理周期；范围 `10s..1h` |
| `BACKUP_ASSETS_RECOVERY_RECEIPT_REAPER_BATCH_SIZE`<br>`backup_assets.recovery.receipt_reaper_batch_size` | int | `100` | 恢复授权回执单次清理批次；范围 `1..1000` |
| `BACKUP_ASSETS_RECOVERY_ENABLED`<br>`backup_assets.recovery.enabled` | bool | `false` | 启用受控恢复；同时受总开关和恢复准入条件约束 |
| `BACKUP_ASSETS_RECOVERY_PREFLIGHT_TTL`<br>`backup_assets.recovery.preflight_ttl` | duration | `10m` | 受控恢复预检有效期；范围 `1m..1h` |
| `BACKUP_ASSETS_RECOVERY_MAX_SELECTION_ITEMS`<br>`backup_assets.recovery.max_selection_items` | int | `10000` | 单次受控恢复条目上限；范围 `1..100000` |
| `BACKUP_ASSETS_RECOVERY_MAX_LOGICAL_BYTES`<br>`backup_assets.recovery.max_logical_bytes` | int | `10737418240` | 单次受控恢复逻辑字节上限；范围 `65536..1099511627776` |
| `BACKUP_ASSETS_RECOVERY_WORKER_CONCURRENCY`<br>`backup_assets.recovery.worker_concurrency` | int | `2` | 受控恢复 Worker 并发上限；范围 `1..16` |
| `BACKUP_ASSETS_RECOVERY_LEASE_TTL`<br>`backup_assets.recovery.lease_ttl` | duration | `90s` | 受控恢复尝试租约时长；范围 `30s..10m` |
| `BACKUP_ASSETS_RECOVERY_LEASE_RENEW_MARGIN`<br>`backup_assets.recovery.lease_renew_margin` | duration | `20s` | 受控恢复租约续期余量；范围 `5s..5m` |
| `BACKUP_ASSETS_RECOVERY_TAKEOVER_CADENCE`<br>`backup_assets.recovery.takeover_cadence` | duration | `15s` | 受控恢复过期尝试接管周期；范围 `1s..5m` |
| `BACKUP_ASSETS_RECOVERY_RETRY_BASE`<br>`backup_assets.recovery.retry_base` | duration | `5s` | 受控恢复重试基础延迟；范围 `1s..5m` |
| `BACKUP_ASSETS_RECOVERY_RETRY_MAX_DELAY`<br>`backup_assets.recovery.retry_max_delay` | duration | `5m` | 受控恢复重试最大延迟；范围 `1s..1h` |
| `BACKUP_ASSETS_RECOVERY_SCAN_LIMIT`<br>`backup_assets.recovery.scan_limit` | int | `100` | 受控恢复持久调度扫描上限；范围 `1..1000` |
| `BACKUP_ASSETS_RECOVERY_EXECUTION_TIMEOUT`<br>`backup_assets.recovery.execution_timeout` | duration | `2h` | 受控恢复执行绝对时限；范围 `5m..24h` |
| `BACKUP_ASSETS_RECOVERY_RESULT_DEFAULT_TTL`<br>`backup_assets.recovery.result_default_ttl` | duration | `1h` | 恢复结果默认明文有效期；范围 `5m..24h` |
| `BACKUP_ASSETS_RECOVERY_RESULT_RETAIN_HARD_CAP`<br>`backup_assets.recovery.result_retain_hard_cap` | duration | `24h` | 恢复结果保留硬上限；范围 `5m..720h` |
| `BACKUP_ASSETS_RECOVERY_RESULT_READ_PERMIT_TTL`<br>`backup_assets.recovery.result_read_permit_ttl` | duration | `2m` | 恢复结果读取许可有效期；范围 `10s..10m` |
| `BACKUP_ASSETS_RECOVERY_RESULT_DRAIN_TIMEOUT`<br>`backup_assets.recovery.result_drain_timeout` | duration | `30s` | 恢复结果读取排空时限；范围 `1s..5m` |
| `BACKUP_ASSETS_RECOVERY_CLEANUP_CADENCE`<br>`backup_assets.recovery.cleanup_cadence` | duration | `1m` | 恢复结果清理周期；范围 `10s..1h` |
| `BACKUP_ASSETS_RECOVERY_CLEANUP_BATCH_SIZE`<br>`backup_assets.recovery.cleanup_batch_size` | int | `100` | 恢复结果清理批次；范围 `1..1000` |
| `BACKUP_ASSETS_RECOVERY_CLEANUP_LEASE_TTL`<br>`backup_assets.recovery.cleanup_lease_ttl` | duration | `2m` | 恢复结果清理租约时长；范围 `30s..30m` |
| `BACKUP_ASSETS_RECOVERY_CLEANUP_RETRY_BASE`<br>`backup_assets.recovery.cleanup_retry_base` | duration | `10s` | 恢复结果清理重试基础延迟；范围 `1s..10m` |
| `BACKUP_ASSETS_RECOVERY_CLEANUP_RETRY_MAX_DELAY`<br>`backup_assets.recovery.cleanup_retry_max_delay` | duration | `10m` | 恢复结果清理重试最大延迟；范围 `1s..2h` |
| `BACKUP_ASSETS_RECOVERY_RECONCILIATION_FINDING_LIMIT`<br>`backup_assets.recovery.reconciliation_finding_limit` | int | `100` | 恢复对账单次 finding 上限；范围 `1..256` |

### 导出与归档

| 环境变量 / Settings 键 | 类型 | 代码默认值 | 范围与用途 |
|---|---|---|---|
| `BACKUP_ASSETS_EXPORT_ENABLED`<br>`backup_assets.export.enabled` | bool | `false` | 启用备份资产导出；同时受总开关和导出准入条件约束 |
| `BACKUP_ASSETS_EXPORT_ROOT`<br>`backup_assets.export.root` | string | `/var/lib/xirang-asset-runtime/export` | 备份资产导出密文专用根目录；**重启生效** |
| `BACKUP_ASSETS_EXPORT_DEFAULT_PROFILE`<br>`backup_assets.export.default_profile` | string | `zip_deflate_v1` | 备份资产导出默认归档配置 |
| `BACKUP_ASSETS_EXPORT_CHUNK_BYTES`<br>`backup_assets.export.chunk_bytes` | int | `1048576` | 备份资产导出认证加密分块字节数；范围 `65536..8388608` |
| `BACKUP_ASSETS_EXPORT_MAX_ITEMS`<br>`backup_assets.export.max_items` | int | `10000` | 单次备份资产导出条目上限；范围 `1..100000` |
| `BACKUP_ASSETS_EXPORT_MAX_SOURCE_POINTS`<br>`backup_assets.export.max_source_points` | int | `128` | 单次备份资产导出恢复点上限；范围 `1..1024` |
| `BACKUP_ASSETS_EXPORT_MAX_ITEM_BYTES`<br>`backup_assets.export.max_item_bytes` | int | `2147483648` | 单条备份资产导出逻辑字节上限；范围 `65536..274877906944` |
| `BACKUP_ASSETS_EXPORT_MAX_LOGICAL_BYTES`<br>`backup_assets.export.max_logical_bytes` | int | `10737418240` | 单次备份资产导出逻辑字节上限；范围 `65536..1099511627776` |
| `BACKUP_ASSETS_EXPORT_MAX_PROVIDER_BYTES`<br>`backup_assets.export.max_provider_bytes` | int | `21474836480` | 单次备份资产导出 Provider 读取字节上限；范围 `65536..2199023255552` |
| `BACKUP_ASSETS_EXPORT_MAX_CIPHERTEXT_BYTES`<br>`backup_assets.export.max_ciphertext_bytes` | int | `12884901888` | 单次备份资产导出密文字节上限；范围 `65536..1374389534720` |
| `BACKUP_ASSETS_EXPORT_USER_ACTIVE_JOBS`<br>`backup_assets.export.user_active_jobs` | int | `2` | 单用户备份资产导出活动作业上限；范围 `1..16` |
| `BACKUP_ASSETS_EXPORT_GLOBAL_ACTIVE_JOBS`<br>`backup_assets.export.global_active_jobs` | int | `8` | 全局备份资产导出活动作业上限；范围 `1..64` |
| `BACKUP_ASSETS_EXPORT_WORKER_CONCURRENCY`<br>`backup_assets.export.worker_concurrency` | int | `2` | 备份资产导出 Worker 并发上限；范围 `1..16` |
| `BACKUP_ASSETS_EXPORT_MAX_OPEN_READERS`<br>`backup_assets.export.max_open_readers` | int | `2` | 单次备份资产导出读取器上限；范围 `1..8` |
| `BACKUP_ASSETS_EXPORT_MAX_DURATION`<br>`backup_assets.export.max_duration` | duration | `2h` | 单次备份资产导出绝对执行时长；范围 `5m..24h` |
| `BACKUP_ASSETS_EXPORT_MAX_ATTEMPTS`<br>`backup_assets.export.max_attempts` | int | `3` | 单次备份资产导出最大尝试次数；范围 `1..10` |
| `BACKUP_ASSETS_EXPORT_RETRY_BASE`<br>`backup_assets.export.retry_base` | duration | `5s` | 备份资产导出重试基础延迟；范围 `1s..1m` |
| `BACKUP_ASSETS_EXPORT_RETRY_MAX_DELAY`<br>`backup_assets.export.retry_max_delay` | duration | `5m` | 备份资产导出重试最大延迟；范围 `5s..30m` |
| `BACKUP_ASSETS_EXPORT_LEASE_TTL`<br>`backup_assets.export.lease_ttl` | duration | `90s` | 备份资产导出内部租约时长；范围 `30s..5m` |
| `BACKUP_ASSETS_EXPORT_LEASE_RENEW_MARGIN`<br>`backup_assets.export.lease_renew_margin` | duration | `20s` | 备份资产导出租约续期安全余量；范围 `5s..2m` |
| `BACKUP_ASSETS_EXPORT_READY_TTL`<br>`backup_assets.export.ready_ttl` | duration | `24h` | 备份资产导出就绪产物绝对有效期；范围 `15m..168h` |
| `BACKUP_ASSETS_EXPORT_SUMMARY_TTL`<br>`backup_assets.export.summary_ttl` | duration | `2160h` | 备份资产导出终态摘要保留期；范围 `24h..8760h` |
| `BACKUP_ASSETS_EXPORT_TICKET_TTL`<br>`backup_assets.export.ticket_ttl` | duration | `5m` | 备份资产导出下载票据有效期；范围 `30s..15m` |
| `BACKUP_ASSETS_EXPORT_TICKET_MAX_REQUESTS`<br>`backup_assets.export.ticket_max_requests` | int | `256` | 备份资产导出下载票据请求上限；范围 `1..4096` |
| `BACKUP_ASSETS_EXPORT_TICKET_MAX_IN_FLIGHT`<br>`backup_assets.export.ticket_max_in_flight` | int | `2` | 备份资产导出下载票据并发上限；范围 `1..8` |
| `BACKUP_ASSETS_EXPORT_TICKET_MAX_CUMULATIVE_BYTES`<br>`backup_assets.export.ticket_max_cumulative_bytes` | int | `25769803776` | 备份资产导出下载票据累计字节上限；范围 `65536..2748779069440` |
| `BACKUP_ASSETS_EXPORT_USER_STORE_QUOTA`<br>`backup_assets.export.user_store_quota` | int | `26843545600` | 单用户备份资产导出密文存储配额；范围 `1073741824..2199023255552` |
| `BACKUP_ASSETS_EXPORT_STORE_QUOTA`<br>`backup_assets.export.store_quota` | int | `107374182400` | 全局备份资产导出密文存储配额；范围 `1073741824..10995116277760` |
| `BACKUP_ASSETS_EXPORT_GC_CADENCE`<br>`backup_assets.export.gc_cadence` | duration | `5m` | 备份资产导出清理周期；范围 `30s..1h` |
| `BACKUP_ASSETS_EXPORT_RECONCILE_BATCH_SIZE`<br>`backup_assets.export.reconcile_batch_size` | int | `100` | 备份资产导出对账批次；范围 `1..1000` |
| `BACKUP_ASSETS_ARCHIVE_MEMBER_TTL`<br>`backup_assets.archive.member_ttl` | duration | `1h` | 归档 member 临时产物有效期；范围 `5m..24h` |
| `BACKUP_ASSETS_ARCHIVE_MAX_EXPANDED_BYTES`<br>`backup_assets.archive.max_expanded_bytes` | int | `8589934592` | 归档展开总字节上限；范围 `1048576..8589934592` |
| `BACKUP_ASSETS_ARCHIVE_MEMBER_MAX_BYTES`<br>`backup_assets.archive.member_max_bytes` | int | `268435456` | 归档单 member 字节上限；范围 `65536..268435456` |
| `BACKUP_ASSETS_ARCHIVE_MAX_ENTRIES`<br>`backup_assets.archive.max_entries` | int | `100000` | 归档条目上限；范围 `1..100000` |
| `BACKUP_ASSETS_ARCHIVE_MAX_DEPTH`<br>`backup_assets.archive.max_depth` | int | `16` | 归档目录深度上限；范围 `1..16` |
| `BACKUP_ASSETS_ARCHIVE_MAX_COMPRESSION_RATIO`<br>`backup_assets.archive.max_compression_ratio` | int | `100` | 归档压缩比上限；范围 `1..100` |
| `BACKUP_ASSETS_ARCHIVE_MAX_DURATION`<br>`backup_assets.archive.max_duration` | duration | `10m` | 归档处理绝对时长；范围 `1s..10m` |

## 节点探测

| 变量 | 类型 | 默认值 | 必填 | 说明 |
|------|------|--------|------|------|
| `NODE_PROBE_INTERVAL` | duration | `5m` | 否 | 探测间隔（最小 30s） |
| `NODE_PROBE_FAIL_THRESHOLD` | int | `3` | 否 | 连续失败多少次标记节点离线 |
| `NODE_PROBE_CONCURRENCY` | int | `10` | 否 | 并发探测数（生产建议 `20`） |

**读取位置**：`backend/internal/config/config.go`；这三个键虽然注册为需重启的 Settings 项，当前启动组装仍直接使用 `config.Load()` 的环境值，数据库覆盖未被消费；修改环境变量后重启生效。此偏差见本文开头，不能把设置 API 写入成功视为探测配置已采用。

## 数据保留

| 变量 | 类型 | 默认值 | 必填 | 说明 |
|------|------|--------|------|------|
| `TASK_TRAFFIC_RETENTION_DAYS` | int | `8` | 否 | 任务流量数据保留天数；当前由启动环境值控制，`0` 停止清理，Settings API 允许范围为 `1..365` |
| `TASK_RUN_RETENTION_DAYS` | int | `90` | 否 | 普通任务执行历史保留天数；当前由启动环境值控制，`0` 停止清理，Settings API 允许范围为 `1..3650`；当前非空备份代次（含 dirty）、恢复记录及活动演练引用的来源证据不按此期限删除 |
| `RETENTION_CHECK_INTERVAL` | duration | `6h` | 否 | 备份保留策略检查间隔（最小 1m），定期清理过期备份并检查存储空间 |
| `BACKUP_STORAGE_MIN_FREE_GB` | int | `10` | 否 | 本地备份存储最低剩余空间（GB），低于此值触发告警 |
| `BACKUP_STORAGE_MAX_USAGE_PCT` | int | `90` | 否 | 本地备份存储最大使用率（%），超过此值触发告警 |
| `INTEGRITY_CHECK_MULTIPLIER` | int | `4` | 否 | 完整性检查频率倍数——每隔多少个保留清理周期运行一次 `restic check` / `rclone check`（默认 4，即 `RETENTION_CHECK_INTERVAL=6h` 时每 24h 一次） |
| `LOG_RETENTION_DAYS_DEFAULT` | int | `30` | 否 | 节点日志默认保留天数，节点未单独配置时生效 |
| `SILENCE_RETENTION_DAYS` | int | `30` | 否 | 已过期静默规则的审计保留天数，超出后删除 |

**读取位置**：基础任务保留与存储阈值 → `backend/internal/config/config.go` 和 settings 服务；`INTEGRITY_CHECK_MULTIPLIER` → `backend/internal/task/retention_worker.go`；节点日志保留 → `backend/internal/nodelogs/retention.go`；静默规则保留 → `backend/internal/alerting/silence_retention.go`。

## 邮件通知

| 变量 | 类型 | 默认值 | 必填 | 说明 |
|------|------|--------|------|------|
| `SMTP_HOST` | string | — | 启用 email 时 | SMTP 服务器地址，为空时 email 通道失败 |
| `SMTP_PORT` | string | `587` | 否 | SMTP 端口 |
| `SMTP_USER` | string | 空 | 服务器要求认证时 | SMTP 用户名；非空时启用 SMTP PLAIN 认证 |
| `SMTP_PASS` | string | 空 | 服务器要求认证时 | SMTP 密码；Settings 键 `smtp.password` 为敏感键，加密入库 |
| `SMTP_FROM` | string | 回退到 `SMTP_USER` | 两者至少一项非空 | 发件人地址 |
| `SMTP_REQUIRE_TLS` | bool | `true` | 否 | 强制 TLS 连接（465 隐式 TLS，其他端口 STARTTLS），设为 `false` 使用明文 |

上述 `SMTP_*` 映射到 `smtp.*` 系统设置，可通过 `/api/v1/settings` API 动态调整。数据库覆盖值优先；没有覆盖值时读取当前进程环境，再回退代码默认值。密钥可由外部密钥管理系统注入。

**读取位置**：settings 服务键 `smtp.host` / `smtp.port` / `smtp.user` / `smtp.password` / `smtp.from` / `smtp.require_tls`，邮件发送路径位于 `backend/internal/alerting/dispatcher.go`。

## 告警

| 变量 | 类型 | 默认值 | 必填 | 说明 |
|------|------|--------|------|------|
| `ALERT_DEDUP_WINDOW` | duration | `10m` | 否 | 告警去重窗口（同节点+同任务+同错误码），`0` 关闭去重 |
| `INTEGRATION_BLOCK_PRIVATE_ENDPOINTS` | bool | `true` | 否 | 阻断 webhook/slack/telegram 指向私网地址；无论开发还是生产默认均开启 |
| `BACKUP_STALE_THRESHOLD_HOURS` | int | `48` | 否 | 备份健康面板判定节点备份过期的小时阈值 |

**读取位置**：`ALERT_DEDUP_WINDOW` → settings 服务 / `backend/internal/alerting/dispatcher.go`；`INTEGRATION_BLOCK_PRIVATE_ENDPOINTS` → `backend/internal/integration/service.go`；`BACKUP_STALE_THRESHOLD_HOURS` → `backend/internal/api/handlers/overview_backup_health_handler.go`。

### 异常检测

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

## 前端

| 变量 | 类型 | 默认值 | 必填 | 说明 |
|------|------|--------|------|------|
| `VITE_API_BASE_URL` | string | `/api/v1` | 否 | API 路径前缀 |
| `VITE_PROXY_TARGET` | string | `http://127.0.0.1:8080` | 否 | 开发模式 Vite 代理目标；`vite.config.ts` 直接读 `process.env`，启动前需在 shell 注入，不能只依赖 `.env` 文件加载 |
| `VITE_DEV_API_DIRECT_URL` | string | — | 否 | 仅开发模式、API 基址为相对路径时，未携带 Authorization 或 step-up 的 GET 可在失败后回退到此地址；不会转发认证材料，生产构建不启用该回退 |
| `VITE_WS_URL` | string | 自动推导 | 否 | 自定义 WebSocket 地址（构建时注入）。若指向非同源主机，生产 Nginx 需同时设置 `CSP_CONNECT_SRC_EXTRA`（见部署文档） |
| `CSP_CONNECT_SRC_EXTRA` | string | 空 | 否 | All-in-One Nginx CSP `connect-src` 附加源（空格分隔，如 `wss://ws.example.com`）；默认仅 `'self'` |
| `VITE_ENABLE_DEMO_MODE` | string | — | 否 | 仅开发/测试时设为 `true` 启用演示数据和认证旁路；生产构建禁止使用 |

**读取位置**：`VITE_API_BASE_URL` / `VITE_DEV_API_DIRECT_URL` → `web/src/lib/api/core.ts` 和 WebSocket URL 推导；`VITE_PROXY_TARGET` → `web/vite.config.ts`；`VITE_WS_URL` → `web/src/lib/ws/logs-socket.ts`；`VITE_ENABLE_DEMO_MODE` → `web/src/hooks/use-console-data.ts`、`web/src/components/protected-route.tsx`、登录页与部分 demo 面板。

## 部署变量（Docker Compose / All-in-One 镜像）

以下变量用于 `docker-compose.yml` 或 All-in-One 镜像，不一定是后端应用运行时变量：

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `IMAGE_TAG` | `latest` | 镜像标签；官方镜像固定为 `docker.io/linnea7171/xirang`，`latest` 仅代表最新稳定版，生产环境建议固定为 `vX.Y.Z` |
| `ASSET_WORKER_IMAGE_TAG` | `local` | 仅可选 `asset-worker` profile 的本地构建标签；不是稳定公共镜像标签 |
| `ASSET_WORKER_INBOX_DIR` | `./asset-worker-inbox` | updater-only 固定 inbox bind source；目录必须由 UID/GID `10002:10002` 拥有、mode `0555` 且不是符号链接 |
| `ASSET_WORKER_UPDATER_TRUST_FILE` | `./asset-worker-updater-trust.json` | updater-only Ed25519 trust 文档；文件必须由 UID/GID `10002:10002` 拥有、mode `0440` 且不是符号链接 |

All-in-One 的 Nginx 固定监听 `10761`，后端默认监听 `:3000`，生产 Compose 固定映射 `10761:10761`。HTTPS/TLS 由外部反向代理负责，不通过项目环境变量配置。上述 `ASSET_WORKER_*` 变量只控制仓库内非 GA、本地 build 的可选 profile；不会改变官方 Core image selector，也不表示 Docker Hub/GitHub Release 发布 Worker。

该 profile 的 socket、身份、卷挂载和网络隔离不能通过环境变量合并或放宽；部署要求统一见[部署指南](deployment.md)及[处理与导出合同](spec/domains/backup-processing-export.md)。

---

## 版本检查与系统备份

| 变量 | 类型 | 默认值 | 必填 | 说明 |
|------|------|--------|------|------|
| `VERSION_CHECK_URL` | string | — | 否 | 版本检查地址，推荐使用 `https://api.github.com/repos/xiangnan0811/xirang/releases/latest`；当前仅支持稳定版 semver 响应，未设置时版本检查接口返回"未配置" |
| `DB_BACKUP_DIR` | string | `./backups`（相对于 DB 文件目录） | 否 | 系统自助 SQLite 备份目录；未设置时取 SQLite 文件所在目录的 `backups` 子目录，显式相对路径则相对进程工作目录 |
| `DB_BACKUP_MAX_COUNT` | int | `20` | 否 | 系统自助 SQLite 备份接口保留的最大备份数量 |

**读取位置**：[版本检查](../backend/internal/api/handlers/version_handler.go)、[系统备份 API](../backend/internal/api/handlers/system_handler.go)。`DB_BACKUP_MAX_COUNT` 只约束 `POST /api/v1/system/backup-db`，无效或非正值回退到 20；不会控制容器 cron 或 `scripts/backup-db.sh`。cron 使用固定的 `/backup/db` 和文件年龄保留策略，备份/恢复步骤见[部署指南](deployment.md)。版本检查比较 GitHub Release 的稳定 semver 与编译时版本；未注入构建版本时显示 `dev`。

## 指标远程推送（Prometheus remote-write）

可选功能。设置 `METRICS_REMOTE_URL` 后，每次节点探测样本同时通过 Prometheus remote-write 协议（snappy + protobuf）推送到外部 TSDB（Mimir、Cortex、VictoriaMetrics、Grafana Cloud 等）。`FanSink` 自动吞掉远程错误，DBSink 不受影响。

| 变量 | 类型 | 默认值 | 必填 | 说明 |
|------|------|--------|------|------|
| `METRICS_REMOTE_URL` | string | 空 | 否 | Prometheus remote-write 端点 URL（如 `https://mimir.example.com/api/v1/push`）；环境值为空时回退 Settings，最终有效值为空才禁用推送 |
| `METRICS_REMOTE_BEARER_TOKEN` | string | 空 | 否 | 可选 Bearer token；Settings 键 `metrics.remote_bearer_token` 为敏感键，会加密入库。可由密钥管理系统注入环境。 |
| `METRICS_REMOTE_TIMEOUT` | duration | `5s` | 否 | 单次 HTTP 请求超时（Go duration 格式）。解析失败或非正值时回退到 5 秒 |

可观测性：失败时通过 `xirang_metrics_remote_write_total{status="failure"}` 计数，建议在 Grafana 上配置 `rate(...)` 持续大于 0 的告警面板。

**读取位置**：`backend/cmd/server/main.go` 的 `buildRemoteWriteSinkFromConfig`，仅启动时读取；当前 URL/token 的非空环境值优先，否则读取 Settings 键 `metrics.remote_url` / `metrics.remote_bearer_token`，变更需重启。该实现与通用数据库优先合同的偏差见本文开头。

### /metrics 端点鉴权与限流

`/metrics` 端点暴露 Prometheus 标准指标（含 `http_requests_total{path=...}` 标签集），未鉴权时会泄露所有 secured 路由清单和流量画像，且无限流可被 DoS 放大。使用下列变量保护端点：

| 变量 | 类型 | 默认值 | 必填 | 说明 |
|------|------|--------|------|------|
| `METRICS_TOKEN` | string | 空 | **生产必填** | Bearer token 保护 `/metrics`。除显式 `APP_ENV`/`ENVIRONMENT=development` 外一律要求非空、≥16 字符且非文档占位符（含未设置 APP_ENV），否则启动失败；开发环境可留空但会打采样 warn；设置后必须 `Authorization: Bearer <token>` |
| `SWAGGER_ENABLED` | bool | 显式开发环境 `true`；其余 `false` | 否 | 是否挂载 `/swagger/*`。默认只在显式开发环境开启；显式 `true` 可强制开启无需认证的 Swagger 页面 |
| `METRICS_RATE_LIMIT` | int | `5` | 否 | `/metrics` 独立限流桶（per IP）允许的请求次数，与 `/api` 限流分离 |
| `METRICS_RATE_WINDOW` | duration | `1s` | 否 | 限流时间窗口（Go duration 格式）。默认 `5 req/s` 对应 Prometheus 通常 15-30s 一次抓取，留有充足余量；超过返回 429 |

抓取配置与告警示例见[监控指南](admin/monitoring-alerting.md)。

**读取位置**：`backend/internal/config/config.go`（`MetricsToken` / `MetricsRateLimit` / `MetricsRateWindow`） → `backend/internal/api/router.go` 通过 `middleware.MetricsAuth` + `middleware.MetricsRateLimit` 注册到 `/metrics`。

---

## 容器与运行时

| 变量 | 类型 | 默认值 | 必填 | 说明 |
|------|------|--------|------|------|
| `TZ` | string | 未设置时使用系统默认（通常 UTC）；All-in-One 镜像默认 `Asia/Shanghai` | 否 | 容器与应用使用的 IANA 时区（例如 `Asia/Shanghai`、`UTC`）。生产建议显式设置，确保备份文件名、日志时间戳与运维一致。`deploy/allinone/Dockerfile` 已预装 `tzdata`，仅需通过环境变量切换。源码运行时不设置则使用宿主机系统时区 |
| `LOG_FILE` | string | All-in-One 镜像默认 `/logs/xirang.log` | 否 | 设置后应用日志同时写入该文件（保留 stdout 输出供 docker logs/journald 收集）。源码运行留空时仅 stdout；文件打开失败也回退 stdout 并记录错误。应用不内置轮转，Docker 日志驱动轮转不会清理 `/logs/xirang.log` |
| `TASK_MAX_EXECUTION_SECONDS` | int | `86400` | 否 | 单次任务执行的全局最大秒数兜底，防 executor 卡死导致 goroutine 泄漏。Policy 级 `max_execution_seconds` >0 时优先于本变量。无效、0 或负数回退 86400；超时取消执行并记录失败 |

**读取位置**：`TZ` → 容器初始化时被 musl 解析，应用层 `time.Now()` 自动遵循；`LOG_FILE` → `backend/internal/logger/logger.go`；`TASK_MAX_EXECUTION_SECONDS` → `backend/internal/task/runner.go`。

---

## 密钥管理

密钥保管、轮替、历史字段兼容与恢复流程统一见[安全加固](admin/security.md)。数据库备份必须配合可恢复的加密密钥；不要把密钥写入公开文档、Issue 或日志。
